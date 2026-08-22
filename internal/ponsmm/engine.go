package ponsmm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	mrand "math/rand/v2"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// Gas limits for the router/token operations. The v1 snipe path uses the same
// 500k for swaps; approvals/withdraws are cheap.
const (
	buyGasLimit      = 500_000
	sellGasLimit     = 500_000
	approveGasLimit  = 80_000
	withdrawGasLimit = 80_000
	// Recent v2 factory launches consume ~3.7M gas before exemption writes.
	// The unused portion is refunded, so keep headroom for up to 32 makers.
	launchV2GasLimit = 6_000_000
)

// restrictionGrace is how long after launch we conservatively cap a single
// wallet's holding to maxWalletBps of supply. The factory only enforces the cap
// for the first ~2 parent-chain blocks; this wall-clock guard is a simple,
// safe over-approximation of that window.
const restrictionGrace = 12 * time.Second

// makerBalanceRefreshInterval lets a live strategy notice externally topped-up
// maker wallets without hammering the RPC at the fastest 100ms cadence.
const makerBalanceRefreshInterval = 5 * time.Second

// State is the market-making state machine's current mode.
type State int

const (
	Launching State = iota
	Accumulating
	Distributing
	ClearAll
	Done
)

func (s State) String() string {
	switch s {
	case Launching:
		return "launching"
	case Accumulating:
		return "accumulating"
	case Distributing:
		return "distributing"
	case ClearAll:
		return "clear-all"
	case Done:
		return "done"
	default:
		return "unknown"
	}
}

// Engine launches a token and market-makes it per the config.
type Engine struct {
	cfg     *Config
	client  *pons.Client
	pool    *Pool
	monitor *Monitor
	log     *slog.Logger

	token    common.Address
	poolAddr common.Address

	// From the active launch config / launch record.
	supply              *big.Int
	graduationThreshold *big.Int
	maxWalletBps        uint16
	tokenIsToken0       bool
	v2Info              pons.LaunchInfo

	launchAt time.Time
	state    State
	rr       int // round-robin cursor over maker wallets

	extraTipWei *big.Int

	fundWaitLogged   bool
	lastFundsRefresh time.Time
	exitAllRequests  chan chan error

	retailResponseActive bool

	distributionRoundRunning bool
	distributionBatchSize    int
	distributionResults      chan distributionResult

	statsMu sync.Mutex
	stats   Stats
	onStats func(Stats)

	buyRoundMu      sync.Mutex
	buyRoundCancel  context.CancelFunc
	buyRoundRunning bool
	pendingBuyMu    sync.Mutex
	pendingBuys     int
	buySettlements  chan buySettlement

	approvalMu        sync.Mutex
	approvalReady     map[common.Address]bool
	approvalSubmitted map[common.Address]bool
}

type buySettlement struct {
	wallet    *Wallet
	wethIn    *big.Int
	txHash    common.Hash
	tokensGot *big.Int
	ethAfter  *big.Int
	err       error
}

type distributionResult struct {
	err error
}

// NewEngine wires an engine. token/poolAddr may be zero if a launch will run
// first; call Bind after launch to attach them.
func NewEngine(cfg *Config, client *pons.Client, pool *Pool, log *slog.Logger) *Engine {
	return &Engine{
		cfg:                 cfg,
		client:              client,
		pool:                pool,
		log:                 log,
		state:               Launching,
		extraTipWei:         gweiToWei(cfg.PriorityTipGwei),
		exitAllRequests:     make(chan chan error),
		buySettlements:      make(chan buySettlement, 1024),
		distributionResults: make(chan distributionResult, 16),
		approvalReady:       make(map[common.Address]bool),
		approvalSubmitted:   make(map[common.Address]bool),
	}
}

// ExitAll asks the running state machine to stop normal decisions and sell the
// full token balance of every strategy wallet. The Run goroutine serializes the
// transition so liquidation cannot overlap another strategy step.
func (e *Engine) ExitAll(ctx context.Context) error {
	done := make(chan error, 1)
	select {
	case e.exitAllRequests <- done:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Binding returns the token and pool currently attached to the engine. It is
// used to persist launch results so a desktop strategy can resume after restart.
func (e *Engine) Binding() (common.Address, common.Address) {
	return e.token, e.poolAddr
}

// Launch performs the token launch from the treasury wallet, waits for the
// receipt, and binds the resulting token+pool. dryRun stops before sending.
func (e *Engine) Launch(ctx context.Context, dryRun bool) error {
	if e.cfg.ProtocolName() == ProtocolV2 {
		return e.launchV2(ctx, dryRun)
	}
	return e.launchV1(ctx, dryRun)
}

func (e *Engine) launchV1(ctx context.Context, dryRun bool) error {
	who := e.pool.Treasury.Addr
	can, err := e.client.CanLaunch(ctx, who)
	if err != nil {
		return fmt.Errorf("canLaunch preflight: %w", err)
	}
	if !can {
		return fmt.Errorf("wallet %s may not launch: launches are disabled and it is not whitelisted", who.Hex())
	}
	fee, err := e.client.LaunchFee(ctx)
	if err != nil {
		return fmt.Errorf("read launchFee: %w", err)
	}
	cfg, err := e.client.GetLaunchConfig(ctx, e.cfg.LaunchConfigID)
	if err != nil {
		return fmt.Errorf("read launch config %d: %w", e.cfg.LaunchConfigID, err)
	}
	if !cfg.Enabled {
		return fmt.Errorf("launch config %d is disabled on-chain", e.cfg.LaunchConfigID)
	}

	devBuy := ethToWei(e.cfg.DevBuyETH)
	value := new(big.Int).Add(fee, devBuy)

	feeWallet := common.Address{}
	if fw := e.cfg.FeeWalletAddr(); fw != nil {
		feeWallet = *fw
	}
	params := pons.V1TokenParams{
		Name:        e.cfg.Token.Name,
		Symbol:      e.cfg.Token.Symbol,
		Logo:        e.cfg.Token.Logo,
		Description: e.cfg.Token.Description,
		Socials: pons.V1Socials{
			Twitter:   e.cfg.Token.Socials.Twitter,
			Telegram:  e.cfg.Token.Socials.Telegram,
			Discord:   e.cfg.Token.Socials.Discord,
			Website:   e.cfg.Token.Socials.Website,
			Farcaster: e.cfg.Token.Socials.Farcaster,
		},
		FeeWallet: feeWallet,
	}

	if err := e.ensureLaunchFunds(ctx, value, buyGasLimit); err != nil {
		return err
	}
	e.log.Info("launch preflight ok",
		"deployer", who.Hex(),
		"launch_fee_eth", weiToEthStr(fee),
		"dev_buy_eth", e.cfg.DevBuyETH,
		"value_eth", weiToEthStr(value),
		"supply", cfg.Supply.String(),
		"graduation_threshold_eth", weiToEthStr(cfg.GraduationThreshold),
		"max_wallet_bps", cfg.MaxWalletBps)

	if dryRun {
		e.log.Info("dry-run: not sending launch transaction")
		return nil
	}

	if err := e.pool.RefreshETH(ctx); err != nil {
		return err
	}
	e.captureStartBalance()
	pr, err := e.pool.txParams(ctx, e.pool.Treasury, buyGasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	var salt [32]byte // zero salt: deterministic per (deployer, params, config)
	tx, err := e.pool.Treasury.Signer.BuildV1Launch(params, e.cfg.LaunchConfigID, e.cfg.DexID, salt, value, pr)
	if err != nil {
		return fmt.Errorf("build launch: %w", err)
	}
	if err := e.pool.send(ctx, e.pool.Treasury, tx); err != nil {
		return fmt.Errorf("send launch: %w", err)
	}
	e.log.Info("launch submitted", "tx", tx.Hash().Hex())
	rcpt, err := e.client.WaitReceipt(ctx, tx.Hash(), 120*time.Second)
	if err != nil {
		return fmt.Errorf("launch confirm: %w", err)
	}
	e.recordLaunchCost(fee, rcpt)
	launched, ok := pons.LaunchedFromReceipt(rcpt)
	if !ok {
		return fmt.Errorf("launch receipt %s carried no TokenLaunched event", tx.Hash().Hex())
	}
	e.log.Info("token launched",
		"token", launched.Token.Hex(),
		"pool", launched.Pool.Hex(),
		"block", launched.Block)
	if err := e.Bind(ctx, launched.Token, launched.Pool); err != nil {
		return err
	}
	// Subscribe first when Run begins, then recover every pool trade from the
	// launch block. Tag the launch transaction so an atomic v1 creator buy is not
	// mistaken for retail during that recovery.
	e.monitor.SetStartBlock(launched.Block)
	e.monitor.MarkOurTx(tx.Hash())
	return nil
}

// ensureLaunchFunds verifies before a launch that the treasury can cover the
// transaction value plus the node's worst-case gas reservation (gas limit x
// suggested fee cap), so underfunding fails preflight with a clear message
// instead of an "insufficient funds" send error.
func (e *Engine) ensureLaunchFunds(ctx context.Context, value *big.Int, gasLimit uint64) error {
	who := e.pool.Treasury.Addr
	balance, err := e.client.EthBalance(ctx, who)
	if err != nil {
		return fmt.Errorf("read deployer balance: %w", err)
	}
	_, feeCap, err := e.client.SuggestGas(ctx, e.extraTipWei)
	if err != nil {
		return fmt.Errorf("suggest gas: %w", err)
	}
	required := new(big.Int).Mul(feeCap, new(big.Int).SetUint64(gasLimit))
	required.Add(required, value)
	if balance.Cmp(required) < 0 {
		return fmt.Errorf("deployer %s holds %s ETH but the launch needs at least %s ETH (%s ETH value + gas reservation for %d gas); top up the treasury wallet",
			who.Hex(), weiToEthStr(balance), weiToEthStr(required), weiToEthStr(value), gasLimit)
	}
	return nil
}

func (e *Engine) launchV2(ctx context.Context, dryRun bool) error {
	who := e.pool.Treasury.Addr
	can, err := e.client.CanLaunchV2(ctx, who)
	if err != nil {
		return fmt.Errorf("v2 canLaunch preflight: %w", err)
	}
	if !can {
		return fmt.Errorf("wallet %s may not launch through pons v2 right now", who.Hex())
	}
	fee, err := e.client.V2LaunchFee(ctx)
	if err != nil {
		return fmt.Errorf("read v2 launchFee: %w", err)
	}
	launchCfg, err := e.client.GetV2LaunchConfig(ctx, e.cfg.LaunchConfigID)
	if err != nil {
		return fmt.Errorf("read v2 launch config %d: %w", e.cfg.LaunchConfigID, err)
	}
	if !launchCfg.Enabled {
		return fmt.Errorf("v2 launch config %d is disabled on-chain", e.cfg.LaunchConfigID)
	}
	pairToken := common.Address{} // Desktop v2 market making currently supports native ETH curves.
	economics, err := e.client.PreviewV2LaunchEconomics(ctx, e.cfg.LaunchConfigID, pairToken)
	if err != nil {
		return fmt.Errorf("preview v2 launch economics: %w", err)
	}
	var salt [32]byte
	if _, err := rand.Read(salt[:]); err != nil {
		return fmt.Errorf("generate v2 launch salt: %w", err)
	}
	creator := who
	if fw := e.cfg.FeeWalletAddr(); fw != nil {
		creator = *fw
	}
	params := pons.V2TokenParams{
		Name: e.cfg.Token.Name, Symbol: e.cfg.Token.Symbol,
		Logo: e.cfg.Token.Logo, Description: e.cfg.Token.Description,
		Socials: pons.V2Socials{
			Twitter: e.cfg.Token.Socials.Twitter, Telegram: e.cfg.Token.Socials.Telegram,
			Discord: e.cfg.Token.Socials.Discord, Website: e.cfg.Token.Socials.Website,
			Farcaster: e.cfg.Token.Socials.Farcaster,
		},
		CreatorFeeRecipient: creator,
		BuybackEnabled:      true,
		ExpectedEconomics:   economics,
		Salt:                salt,
	}
	exemptions := make([]common.Address, 0, len(e.pool.Makers))
	for _, wallet := range e.pool.Makers {
		exemptions = append(exemptions, wallet.Addr)
	}
	if len(exemptions) > 32 {
		return fmt.Errorf("v2 supports at most 32 additional snipe-tax-exempt maker wallets; got %d", len(exemptions))
	}
	// A non-zero initial buy routes the launch through the official
	// launch-and-buy router: the treasury's first buy executes inside the
	// launch transaction itself, so no sniper can trade before it.
	devBuy := ethToWei(e.cfg.DevBuyETH)
	value := new(big.Int).Add(fee, devBuy)
	gasLimit := uint64(launchV2GasLimit)
	if devBuy.Sign() > 0 {
		gasLimit += buyGasLimit
	}
	if err := e.ensureLaunchFunds(ctx, value, gasLimit); err != nil {
		return err
	}
	e.log.Info("v2 launch preflight ok",
		"deployer", who.Hex(), "launch_fee_eth", weiToEthStr(fee),
		"atomic_initial_buy_eth", weiToEthStr(devBuy),
		"supply", launchCfg.Supply.String(),
		"graduation_threshold_eth", weiToEthStr(launchCfg.GraduationThreshold),
		"maker_exemptions", len(exemptions))
	if dryRun {
		e.log.Info("dry-run: not sending v2 launch transaction")
		return nil
	}
	if err := e.pool.RefreshETH(ctx); err != nil {
		return err
	}
	e.captureStartBalance()
	pr, err := e.pool.txParams(ctx, e.pool.Treasury, gasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	var tx *types.Transaction
	if devBuy.Sign() > 0 {
		tx, err = e.pool.Treasury.Signer.BuildV2LaunchAndBuy(params, e.cfg.LaunchConfigID, pairToken,
			devBuy, big.NewInt(0), who, exemptions, value, pr)
	} else {
		tx, err = e.pool.Treasury.Signer.BuildV2Launch(params, e.cfg.LaunchConfigID, pairToken, exemptions, fee, pr)
	}
	if err != nil {
		return fmt.Errorf("build v2 launch: %w", err)
	}
	if err := e.pool.send(ctx, e.pool.Treasury, tx); err != nil {
		return fmt.Errorf("send v2 launch: %w", err)
	}
	e.log.Info("v2 launch submitted", "tx", tx.Hash().Hex())
	rcpt, err := e.client.WaitReceiptEvery(ctx, tx.Hash(), 120*time.Second, 100*time.Millisecond)
	if err != nil {
		return fmt.Errorf("v2 launch confirm: %w", err)
	}
	launched, ok := pons.V2LaunchedFromReceipt(rcpt)
	if !ok {
		return fmt.Errorf("v2 launch receipt %s carried no TokenLaunched event", tx.Hash().Hex())
	}
	// Broadcast the first maker buys before anything else touches the RPC:
	// every millisecond here is a block on this chain, and launch snipers are
	// already racing us.
	burst := e.launchBuyBurst(ctx, launched.Curve, pr)
	e.recordLaunchCost(fee, rcpt)
	e.log.Info("v2 token launched", "token", launched.Token.Hex(), "curve", launched.Curve.Hex(), "block", launched.Block)
	// Bind from data we already hold instead of re-reading the chain: the
	// monitor must be watching within a second of the launch receipt, because
	// snipers buy in the very first blocks.
	e.bindV2FromLaunch(launched, launchCfg, int64(params.CreatorTaxBps))
	e.monitor.SetStartBlock(launched.Block)
	e.monitor.MarkOurTx(tx.Hash())
	if devBuy.Sign() > 0 {
		if tokensOut, ok := pons.V2AtomicBuyFromReceipt(rcpt, launched.Curve, who); ok {
			e.pool.Treasury.setTokenBalance(tokensOut)
			e.monitor.RecordOurBuy(devBuy, tokensOut)
			e.recordBuy(devBuy)
			e.log.Info("atomic initial buy landed inside the launch transaction",
				"eth_in", weiToEthStr(devBuy), "tokens", tokensOut.String())
		} else {
			e.log.Warn("launch receipt carried no CurveBuy for the treasury; atomic initial buy unaccounted")
		}
	}
	for _, b := range burst {
		e.monitor.MarkOurTx(b.hash)
		e.addPendingBuy()
		// Queue the sell approval right behind the buy by nonce, mirroring the
		// normal buy path, so an instant retail response can sell immediately.
		// Both run off the launch path: approving nine wallets serially used to
		// hold up Run() — and with it the trade monitor — for over ten seconds.
		go func(b launchBurstBuy) {
			if err := e.ensureApprove(ctx, b.wallet, big.NewInt(1), false); err != nil {
				e.log.Warn("sell approval prewarm after burst buy failed", "wallet", b.wallet.Addr.Hex(), "err", err)
			}
			e.settleBuy(ctx, b.wallet, b.spend, big.NewInt(0), b.hash)
		}(b)
	}
	return nil
}

type launchBurstBuy struct {
	wallet *Wallet
	spend  *big.Int
	hash   common.Hash
}

// launchBuyBurst broadcasts the first maker buys the instant the launch
// receipt is decoded. It deliberately performs no per-wallet RPC besides the
// send itself: the token is seconds old so balances are zero and the curve
// cannot have graduated, minTokensOut is zero because the makers are
// registered snipe-tax-exempt first buyers, and the launch transaction's gas
// pricing is reused. This lands our wallets within the first blocks after
// launch instead of several seconds later.
func (e *Engine) launchBuyBurst(ctx context.Context, curve common.Address, launchPr pons.TxParams) []launchBurstBuy {
	eligible := e.fundedMakers()
	if len(eligible) == 0 {
		e.log.Warn("launch buy burst skipped: no maker wallet has spendable ETH")
		return nil
	}
	// Sequential-buy configurations still snipe with the first maker; the
	// remaining wallets follow on the normal accumulation cadence.
	if !e.cfg.ConcurrentBuys {
		eligible = eligible[:1]
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	var mu sync.Mutex
	out := make([]launchBurstBuy, 0, len(eligible))
	runWalletActions(true, eligible, func(w *Wallet) {
		spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction)
		if spend.Sign() <= 0 {
			return
		}
		w.txMu.Lock()
		defer w.txMu.Unlock()
		pr := pons.TxParams{Nonce: w.Nonce, GasLimit: buyGasLimit, TipCap: launchPr.TipCap, FeeCap: launchPr.FeeCap}
		buyTx, err := w.Signer.BuildBuy(curve, spend, big.NewInt(0), true, pr)
		if err != nil {
			e.log.Warn("launch burst buy build failed", "wallet", w.Addr.Hex(), "err", err)
			return
		}
		if err := e.pool.send(ctx, w, buyTx); err != nil {
			e.log.Warn("launch burst buy send failed", "wallet", w.Addr.Hex(), "err", err)
			return
		}
		mu.Lock()
		out = append(out, launchBurstBuy{wallet: w, spend: spend, hash: buyTx.Hash()})
		mu.Unlock()
	})
	e.log.Info("launch buy burst broadcast", "buys", len(out), "makers", len(eligible))
	return out
}

// Bind attaches an already-launched token+pool (used by `mm` on an existing
// token, or right after Launch).
func (e *Engine) Bind(ctx context.Context, token, poolAddr common.Address) error {
	if e.cfg.ProtocolName() == ProtocolV2 {
		return e.bindV2(ctx, token, poolAddr)
	}
	return e.bindV1(ctx, token, poolAddr)
}

func (e *Engine) bindV1(ctx context.Context, token, poolAddr common.Address) error {
	st, err := e.client.GetV1Launch(ctx, token)
	if err != nil {
		return fmt.Errorf("read launch record: %w", err)
	}
	if poolAddr == (common.Address{}) {
		return fmt.Errorf("pool address is required to bind token %s", token.Hex())
	}
	cfg, err := e.client.GetLaunchConfig(ctx, st.LaunchConfigId.Uint64())
	if err != nil {
		return fmt.Errorf("read launch config: %w", err)
	}
	e.token = token
	e.poolAddr = poolAddr
	e.supply = st.Supply
	e.graduationThreshold = cfg.GraduationThreshold
	e.maxWalletBps = cfg.MaxWalletBps
	e.tokenIsToken0 = st.IsToken0
	e.monitor = NewMonitor(e.client, e.pool, token, poolAddr, st.IsToken0, st.Supply, e.log)
	e.launchAt = time.Now()
	e.state = Accumulating
	return nil
}

// bindV2FromLaunch binds a token this engine just launched using data already
// in hand: the preflighted launch config and the TokenLaunched event. A cold
// bindV2 performs eight serial chain reads (~4s on the public RPC) to learn
// values we chose ourselves seconds ago — time a launch sniper trades in
// unanswered, since the trade monitor cannot start until the bind completes.
func (e *Engine) bindV2FromLaunch(launched pons.Launch, launchCfg pons.V2LaunchConfig, creatorTaxBps int64) {
	e.token, e.poolAddr = launched.Token, launched.Curve
	e.supply = launchCfg.Supply
	e.graduationThreshold = launched.GraduationThreshold
	e.maxWalletBps = 0
	e.v2Info = pons.LaunchInfo{
		Token: launched.Token, Curve: launched.Curve, Deployer: e.pool.Treasury.Addr,
		NativeQuote: true, FeeBps: launchCfg.CurveFeeBps.Int64(), CreatorTaxBps: creatorTaxBps,
		Name: e.cfg.Token.Name, Symbol: e.cfg.Token.Symbol, Decimals: 18,
	}
	e.monitor = NewCurveMonitor(e.client, e.pool, launched.Token, launched.Curve, launchCfg.Supply, e.log)
	e.launchAt = time.Now()
	e.state = Accumulating
}

func (e *Engine) bindV2(ctx context.Context, token, curve common.Address) error {
	if token == (common.Address{}) || curve == (common.Address{}) {
		return fmt.Errorf("token and curve addresses are required for pons v2")
	}
	pairToken, err := e.client.CurvePairToken(ctx, curve)
	if err != nil {
		return fmt.Errorf("read v2 pair token: %w", err)
	}
	info := pons.LaunchInfo{Token: token, Curve: curve, PairToken: pairToken}
	if err := e.client.LoadLaunchInfo(ctx, &info); err != nil {
		return fmt.Errorf("read v2 launch record: %w", err)
	}
	if !info.NativeQuote {
		return fmt.Errorf("v2 custom-pair curves are not supported by the desktop market maker")
	}
	supply, err := e.client.TokenSupply(ctx, token)
	if err != nil {
		return fmt.Errorf("read v2 token supply: %w", err)
	}
	threshold, err := e.client.CurveGraduationThreshold(ctx, curve)
	if err != nil {
		return fmt.Errorf("read v2 graduation threshold: %w", err)
	}
	e.token, e.poolAddr = token, curve
	e.supply, e.graduationThreshold = supply, threshold
	e.maxWalletBps = 0
	e.v2Info = info
	e.monitor = NewCurveMonitor(e.client, e.pool, token, curve, supply, e.log)
	e.launchAt = time.Now()
	e.state = Accumulating
	return nil
}

// Run drives the market-making state machine until ctx ends. On shutdown it
// leaves positions as-is (use `collect` to liquidate and sweep).
func (e *Engine) Run(ctx context.Context) error {
	if e.monitor == nil {
		return fmt.Errorf("engine not bound to a token; call Launch or Bind first")
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	ctx = runCtx

	// The monitor starts before anything else: every second it is down is a
	// second a sniper can trade unseen. Detected trades queue on the Retail
	// channel until the event loop below starts draining them.
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		e.monitor.Run(ctx)
	}()
	defer func() {
		cancelRun()
		<-monitorDone
	}()

	// The three startup reads are independent; run them concurrently so the
	// event loop (which acts on retail trades) starts as soon as possible.
	var ethErr, tokErr error
	var initWg sync.WaitGroup
	initWg.Add(2)
	go func() { defer initWg.Done(); ethErr = e.pool.RefreshETH(ctx) }()
	go func() { defer initWg.Done(); tokErr = e.pool.RefreshToken(ctx, e.token) }()
	if err := e.monitor.RefreshReserves(ctx); err != nil {
		e.log.Warn("initial reserve refresh failed", "err", err)
	}
	initWg.Wait()
	if ethErr != nil {
		return ethErr
	}
	if tokErr != nil {
		return tokErr
	}
	e.captureStartBalance()
	defer e.finalizeStats()
	e.lastFundsRefresh = time.Now()
	e.seedMonitorFromBalances(ctx)

	// Approval warming must not block the event loop. A sell can be queued behind
	// an already-submitted approval by nonce, so confirmation is not required on
	// the latency-sensitive path.
	approvalDone := make(chan struct{})
	go func() {
		defer close(approvalDone)
		e.prepareExistingSellApprovals(ctx)
	}()
	defer func() {
		cancelRun()
		<-approvalDone
	}()

	accTick := time.NewTicker(e.cfg.AccumulateInterval)
	defer accTick.Stop()
	sellTick := time.NewTicker(e.cfg.SellInterval)
	defer sellTick.Stop()
	reserveTick := time.NewTicker(10 * time.Second)
	defer reserveTick.Stop()

	e.log.Info("market maker running",
		"token", e.token.Hex(), "pool", e.poolAddr.Hex(), "state", e.state.String())

	for {
		// Retail trades decide state transitions and time the exits; a burst of
		// queued buy settlements must never starve them, so drain the retail
		// channel first on every iteration.
		select {
		case ev := <-e.monitor.Retail:
			e.onRetail(ctx, ev)
			if e.state == Done {
				e.log.Info("state machine reached Done; exiting")
				return nil
			}
			continue
		default:
		}
		select {
		case <-ctx.Done():
			e.interruptBuys()
			e.log.Info("shutting down", "state", e.state.String())
			return nil
		case ev := <-e.monitor.Retail:
			e.onRetail(ctx, ev)
		case settled := <-e.buySettlements:
			e.applyBuySettlement(ctx, settled)
		case result := <-e.distributionResults:
			e.applyDistributionResult(result)
		case done := <-e.exitAllRequests:
			e.interruptBuys()
			e.log.Warn("one-click exit requested; selling all strategy token balances",
				"concurrent", true)
			e.state = ClearAll
			err := e.exitAllStep(ctx)
			done <- err
			if err != nil {
				return fmt.Errorf("one-click exit: %w", err)
			}
		case <-accTick.C:
			if e.state == Accumulating {
				e.startAccumulationRound(ctx)
			}
		case <-sellTick.C:
			switch e.state {
			case Distributing:
				e.distributeStep(ctx)
			case ClearAll:
				if err := e.clearAllStep(ctx); err != nil {
					e.log.Warn("clear-all round failed", "err", err)
				}
			}
		case <-reserveTick.C:
			curveClosed := false
			if e.cfg.ProtocolName() == ProtocolV2 {
				graduated, err := e.client.Graduated(ctx, e.poolAddr)
				if err == nil && graduated {
					e.log.Warn("v2 curve graduated to Uniswap v4; stopping curve market maker",
						"token", e.token.Hex(), "curve", e.poolAddr.Hex())
					e.state = Done
					curveClosed = true
				}
			}
			if !curveClosed {
				if err := e.monitor.RefreshReserves(ctx); err != nil {
					e.log.Warn("reserve refresh failed", "err", err)
				}
			}
		}
		if e.state == Done {
			e.log.Info("state machine reached Done; exiting")
			return nil
		}
	}
}

// seedMonitorFromBalances records our starting token position (e.g. the dev buy
// or an earlier accumulation run) so cost/holding math is anchored. Existing
// holdings use their current executable full-exit quote as the startup cost
// baseline; this avoids treating an unknown pre-restart cost as zero profit.
func (e *Engine) seedMonitorFromBalances(ctx context.Context) {
	held := e.pool.TotalTokens()
	if held.Sign() == 0 {
		return
	}
	baseline, err := e.quoteSell(ctx, held)
	e.monitor.mu.Lock()
	e.monitor.ourTokens = new(big.Int).Set(held)
	if err != nil {
		e.monitor.costBasisKnown = false
	} else {
		e.monitor.ourWethSpent = new(big.Int).Set(baseline)
		e.monitor.costBasisKnown = true
	}
	e.monitor.mu.Unlock()
	if err != nil {
		e.log.Warn("existing position cost baseline unavailable; automatic profit exit disabled until restart",
			"tokens", held.String(), "err", err)
		return
	}
	e.log.Info("existing position valued for automatic profit exit",
		"tokens", held.String(), "baseline_exit_eth", weiToEthStr(baseline))
}

// onRetail reacts to an outside trade. A retail buy triggers exactly one of
// two responses: when the full-exit quote covers everything we paid (net buy
// ETH + gas + tips + launch fee) with profit left over, every wallet sells
// concurrently at once; otherwise wallets are cleared in slow batches of 4-6
// until the retail buyer sells (resume accumulation) or nothing remains
// (strategy stops).
func (e *Engine) onRetail(ctx context.Context, ev RetailEvent) {
	snap := e.monitor.Snapshot()
	if ev.IsBuy {
		// Retail flow has priority over accumulation. Stop every buy that has not
		// reached broadcast; already-broadcast buys remain tracked and are drained
		// immediately after confirmation.
		e.interruptBuys()
		e.log.Info("retail buy detected",
			"tokens", ev.TokenAmount.String(), "weth", weiToEthStr(ev.WethAmount),
			"our_hold_frac", snap.OurHoldFrac, "state", e.state.String(),
			"event_queue_delay_ms", time.Since(ev.At).Milliseconds())

		proceeds := e.quoteSellAll(ctx, snap.OurTokens)
		totalCost := e.totalCostWei(snap.OurWethSpent)
		next := retailBuyResponse(proceeds, totalCost, snap.CostBasisKnown)
		e.log.Info("retail buy decision",
			"sell_all_proceeds_eth", weiToEthStr(proceeds),
			"total_cost_eth", weiToEthStr(totalCost),
			"cost_basis_known", snap.CostBasisKnown, "decision", next.String())
		e.state = next
		switch next {
		case ClearAll:
			e.retailResponseActive = false
			e.log.Info("profit covers all fees -> immediate concurrent full exit")
			if err := e.clearAllStepWithQuote(ctx, proceeds); err != nil {
				e.log.Warn("profitable concurrent full exit failed", "err", err)
			}
		case Distributing:
			if e.beginRetailDistribution() {
				e.log.Info("profit does not yet cover fees -> distributing in small wallet batches",
					"batch_wallets", fmt.Sprintf("%d-%d", distributionBatchMin, distributionBatchMax),
					"interval", e.cfg.SellInterval, "concurrent", e.cfg.ConcurrentSells)
				e.startDistributionRound(ctx)
			} else {
				e.log.Info("additional retail buy detected; continuing batch distribution",
					"retail_net_tokens", snap.RetailNetTokens.String())
			}
		}
		return
	}
	e.log.Info("retail sell detected",
		"tokens", ev.TokenAmount.String(), "weth", weiToEthStr(ev.WethAmount),
		"retail_net_tokens", snap.RetailNetTokens.String(), "state", e.state.String())
	// Any external sell means the retail pressure has started to leave. Stop
	// scheduling further liquidation batches and immediately return to the
	// original accumulation strategy; a batch already broadcast is allowed to
	// settle in the background.
	if e.retailResponseActive && e.state == Distributing {
		e.resumeAfterRetailSell()
	}
}

func (e *Engine) beginRetailDistribution() bool {
	if e.retailResponseActive {
		e.state = Distributing
		return false
	}
	e.retailResponseActive = true
	e.distributionBatchSize = 0
	e.state = Distributing
	return true
}

func (e *Engine) resumeAfterRetailSell() {
	// The accumulation tick itself still waits for pending buys, so changing the
	// state immediately is safe while already-broadcast transactions settle.
	e.retailResponseActive = false
	e.state = Accumulating
	e.log.Info("retail sell detected; stopping liquidation batches and resuming accumulation",
		"pending_buys", e.pendingBuyCount())
}

// accumulateStep buys from the next maker wallet with spendable ETH, unless we
// have already met the chip target and (if graduating) the pool is past the
// threshold.
func (e *Engine) startAccumulationRound(ctx context.Context) {
	if e.pendingBuyCount() > 0 {
		return
	}
	e.buyRoundMu.Lock()
	if e.buyRoundRunning {
		e.buyRoundMu.Unlock()
		return
	}
	roundCtx, cancel := context.WithCancel(ctx)
	e.buyRoundCancel = cancel
	e.buyRoundRunning = true
	e.buyRoundMu.Unlock()

	go func() {
		defer func() {
			e.buyRoundMu.Lock()
			e.buyRoundRunning = false
			e.buyRoundCancel = nil
			e.buyRoundMu.Unlock()
		}()
		e.accumulateStep(roundCtx, ctx)
	}()
}

func (e *Engine) interruptBuys() {
	e.buyRoundMu.Lock()
	if e.buyRoundCancel != nil {
		e.buyRoundCancel()
	}
	e.buyRoundMu.Unlock()
}

func (e *Engine) accumulateStep(ctx, settleCtx context.Context) {
	snap := e.monitor.Snapshot()
	graduated := e.pastGraduation(ctx)
	if ctx.Err() != nil {
		return
	}
	if e.cfg.ProtocolName() == ProtocolV2 && graduated {
		e.log.Warn("v2 curve graduated; accumulation stopped", "token", e.token.Hex())
		return
	}
	if snap.OurHoldFrac >= e.cfg.ChipTarget && (!e.cfg.Graduate || graduated) {
		return
	}
	if !e.ensureMakerFunds(ctx) {
		return
	}
	// Each wallet buys exactly once per cycle: a wallet that already holds the
	// token never buys again until its position has been fully sold (for
	// example by a distribution batch), which makes it eligible again when the
	// pump strategy resumes.
	eligible := e.buyEligibleMakers()
	if len(eligible) == 0 {
		return
	}
	if e.cfg.ConcurrentBuys {
		runWalletActions(true, eligible, func(w *Wallet) {
			e.buyFromMaker(ctx, settleCtx, w)
		})
		return
	}
	e.buyFromMaker(ctx, settleCtx, eligible[e.rr%len(eligible)])
	e.rr++
}

// distributeStep starts one slow batch. A batch fully clears a group of wallets;
// it never sells a small fraction from every wallet.
func (e *Engine) distributeStep(ctx context.Context) {
	e.startDistributionRound(ctx)
}

func (e *Engine) startDistributionRound(ctx context.Context) {
	if e.distributionRoundRunning {
		return
	}
	wallets := e.distributionWalletBatch()
	if len(wallets) == 0 {
		if e.pendingBuyCount() == 0 && e.pool.TotalTokens().Sign() == 0 {
			e.log.Warn("retail buy arrived before any maker position confirmed; no order submitted and strategy stopped")
			e.retailResponseActive = false
			e.state = Done
		}
		return
	}
	if e.distributionResults == nil {
		// Unit callers may construct an Engine literal; keep those paths safe.
		e.distributionRoundRunning = false
		return
	}
	e.distributionRoundRunning = true
	go func(batch []*Wallet) {
		err := e.sellWalletBatch(ctx, batch)
		e.distributionResults <- distributionResult{err: err}
	}(wallets)
	e.log.Info("distribution batch submitted", "wallets", len(wallets),
		"concurrent", e.cfg.ConcurrentSells, "interval", e.cfg.SellInterval)
}

func (e *Engine) distributionWalletBatch() []*Wallet {
	var held []*Wallet
	for _, w := range e.pool.All() {
		if w.tokenBalance().Sign() > 0 {
			held = append(held, w)
		}
	}
	if len(held) == 0 {
		return nil
	}
	if e.distributionBatchSize <= 0 {
		// 4-6 wallets per batch; the size is drawn once per distribution
		// response and reused for every following batch.
		e.distributionBatchSize = distributionBatchMin + mrand.IntN(distributionBatchMax-distributionBatchMin+1)
	}
	size := e.distributionBatchSize
	if size > len(held) {
		size = len(held)
	}
	return held[:size]
}

func (e *Engine) sellWalletBatch(ctx context.Context, wallets []*Wallet) error {
	var errs []error
	var mu sync.Mutex
	runWalletActions(e.cfg.ConcurrentSells, wallets, func(w *Wallet) {
		if err := e.sellOnce(ctx, w, w.tokenBalance()); err != nil {
			mu.Lock()
			errs = append(errs, fmt.Errorf("wallet %s: %w", w.Addr.Hex(), err))
			mu.Unlock()
		}
	})
	return errors.Join(errs...)
}

func (e *Engine) applyDistributionResult(result distributionResult) {
	e.distributionRoundRunning = false
	if result.err != nil {
		e.log.Warn("distribution batch failed", "err", result.err)
	}
	if e.state == Distributing && e.pendingBuyCount() == 0 && e.pool.TotalTokens().Sign() == 0 {
		e.log.Info("distribution complete; all wallet batches cleared -> done")
		e.retailResponseActive = false
		e.state = Done
	}
}

// clearAllStep liquidates the entire position across all wallets at once. Both
// profit-taking and one-click exit take this fastest concurrent path.
//
// It re-reads every wallet's token balance from the chain first. The cached
// balances cannot be trusted here: a buy broadcast just before the exit may
// land on-chain after its settlement goroutine was cancelled, leaving tokens
// the cache still reports as zero. Selling off the cache would strand those
// tokens and, because round P&L is measured purely in ETH, show them as loss.
func (e *Engine) clearAllStep(ctx context.Context) error {
	e.refreshTokensForExit(ctx)
	return e.clearAllStepWithQuote(ctx, e.quoteSellAll(ctx, e.pool.TotalTokens()))
}

func (e *Engine) clearAllStepWithQuote(ctx context.Context, totalQuote *big.Int) error {
	e.log.Info("concurrently clearing entire position")
	if err := e.sellAllFast(ctx, totalQuote); err != nil {
		return err
	}
	e.finishDrainIfEmpty()
	if e.state != Done {
		e.log.Info("exit sweep submitted; re-checking on-chain balances next tick",
			"pending_buys", e.pendingBuyCount(), "remaining_tokens", e.pool.TotalTokens().String())
	}
	return nil
}

func (e *Engine) exitAllStep(ctx context.Context) error {
	e.log.Info("one-click exit: concurrently clearing every wallet position")
	e.refreshTokensForExit(ctx)
	return e.clearAllStepWithQuote(ctx, e.quoteSellAll(ctx, e.pool.TotalTokens()))
}

// refreshTokensForExit reloads on-chain token balances so an exit sweep sells
// what the wallets actually hold, not a stale cache. Best effort: a refresh
// failure logs and leaves the cache in place rather than aborting the exit.
func (e *Engine) refreshTokensForExit(ctx context.Context) {
	if err := e.pool.RefreshToken(ctx, e.token); err != nil {
		e.log.Warn("exit balance refresh failed; selling off cached balances", "err", err)
	}
}

func (e *Engine) buyFromMaker(ctx, settleCtx context.Context, w *Wallet) {
	if w == nil {
		return
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction)
	if spend.Sign() <= 0 {
		return
	}
	spend = e.clampBuyToWalletCap(ctx, w, spend)
	if spend.Sign() <= 0 {
		return
	}
	if err := e.buyOnce(ctx, settleCtx, w, spend); err != nil && !errors.Is(err, context.Canceled) {
		e.log.Warn("maker buy failed", "wallet", w.Addr.Hex(), "err", err)
	}
}

// ensureMakerFunds refreshes externally changeable ETH balances at a bounded
// cadence and emits only state transitions, avoiding log floods at 100ms.
func (e *Engine) ensureMakerFunds(ctx context.Context) bool {
	if len(e.fundedMakers()) == 0 && time.Since(e.lastFundsRefresh) >= makerBalanceRefreshInterval {
		e.lastFundsRefresh = time.Now()
		if err := e.pool.RefreshETH(ctx); err != nil {
			e.log.Warn("maker balance refresh failed", "err", err)
		}
	}
	if len(e.fundedMakers()) > 0 {
		if e.fundWaitLogged {
			e.log.Info("maker funds available; resuming accumulation")
		}
		e.fundWaitLogged = false
		return true
	}
	if !e.fundWaitLogged {
		e.log.Warn("waiting for maker funds: no wallet has spendable ETH",
			"gas_reserve_eth", e.cfg.GasReserveETH)
		e.fundWaitLogged = true
	}
	return false
}

func (e *Engine) fundedMakers() []*Wallet {
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	wallets := make([]*Wallet, 0, len(e.pool.Makers))
	for _, w := range e.pool.Makers {
		if w.spendableWei(gasReserve).Sign() > 0 {
			wallets = append(wallets, w)
		}
	}
	return wallets
}

// buyEligibleMakers returns funded makers that do not currently hold the
// token. Holding any balance means the wallet already used its single buy for
// this cycle; fully selling the position restores its eligibility.
func (e *Engine) buyEligibleMakers() []*Wallet {
	funded := e.fundedMakers()
	wallets := make([]*Wallet, 0, len(funded))
	for _, w := range funded {
		if w.tokenBalance().Sign() == 0 {
			wallets = append(wallets, w)
		}
	}
	return wallets
}

// pastGraduation reports whether the pool's paired principal has reached the
// graduation threshold.
func (e *Engine) pastGraduation(ctx context.Context) bool {
	if e.cfg.ProtocolName() == ProtocolV2 {
		graduated, err := e.client.Graduated(ctx, e.poolAddr)
		return err == nil && graduated
	}
	if e.graduationThreshold == nil || e.graduationThreshold.Sign() == 0 {
		return false
	}
	principal, _, graduated, err := e.client.GraduationStatus(ctx, e.token)
	if err != nil {
		return false
	}
	if graduated {
		return true
	}
	return principal.Cmp(e.graduationThreshold) >= 0
}

// clampBuyToWalletCap caps a buy so a wallet's resulting holding stays under
// maxWalletBps of supply while the launch restriction window may still apply.
func (e *Engine) clampBuyToWalletCap(ctx context.Context, w *Wallet, spend *big.Int) *big.Int {
	if e.cfg.ProtocolName() == ProtocolV2 {
		return spend
	}
	if time.Since(e.launchAt) > restrictionGrace || e.maxWalletBps == 0 {
		return spend
	}
	capTokens := new(big.Int).Div(new(big.Int).Mul(e.supply, big.NewInt(int64(e.maxWalletBps))), big.NewInt(10_000))
	room := new(big.Int).Sub(capTokens, w.tokenBalance())
	if room.Sign() <= 0 {
		return big.NewInt(0)
	}
	// Estimate the ETH needed to buy `room` tokens; if our intended spend buys
	// fewer than room, no clamp needed.
	wantTokens, err := e.client.QuoteV1Buy(ctx, e.token, spend)
	if err != nil || wantTokens.Cmp(room) <= 0 {
		return spend
	}
	// Scale spend down by room/wantTokens.
	scaled := new(big.Int).Div(new(big.Int).Mul(spend, room), wantTokens)
	return scaled
}

// buyOnce performs preflight and broadcasts a buy, then settles its receipt in
// the background. The strategy event loop therefore stays free to interrupt
// the round as soon as retail flow arrives.
func (e *Engine) buyOnce(ctx, settleCtx context.Context, w *Wallet, wethIn *big.Int) error {
	if e.cfg.ProtocolName() == ProtocolV2 {
		if graduated, err := e.client.Graduated(ctx, e.poolAddr); err != nil {
			return fmt.Errorf("check v2 curve state: %w", err)
		} else if graduated {
			return fmt.Errorf("v2 curve has graduated; curve buy is closed")
		}
	}
	quote, err := e.quoteBuy(ctx, w, wethIn)
	if err != nil {
		return fmt.Errorf("quote buy: %w", err)
	}
	minOut := applySlippage(quote, e.cfg.SlippageBps)
	before, err := e.client.TokenBalance(ctx, e.token, w.Addr)
	if err != nil {
		return fmt.Errorf("balance before: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	w.txMu.Lock()
	if err := ctx.Err(); err != nil {
		w.txMu.Unlock()
		return err
	}
	pr, err := e.pool.txParams(ctx, w, buyGasLimit, e.extraTipWei)
	if err != nil {
		w.txMu.Unlock()
		return err
	}
	var tx *types.Transaction
	if e.cfg.ProtocolName() == ProtocolV2 {
		tx, err = w.Signer.BuildBuy(e.poolAddr, wethIn, minOut, true, pr)
	} else {
		tx, err = w.Signer.BuildV1Buy(e.token, wethIn, minOut, pr)
	}
	if err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("build buy: %w", err)
	}
	e.monitor.MarkOurTx(tx.Hash())
	if err := e.pool.send(ctx, w, tx); err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("send buy: %w", err)
	}
	w.txMu.Unlock()

	e.addPendingBuy()
	// Queue approval behind the buy without waiting for its receipt. If a retail
	// event arrives now, the emergency sell can immediately take the next nonce.
	if err := e.ensureApprove(settleCtx, w, big.NewInt(1), false); err != nil {
		e.log.Warn("sell approval prewarm after buy failed", "wallet", w.Addr.Hex(), "err", err)
	}
	go e.settleBuy(settleCtx, w, wethIn, before, tx.Hash())
	return nil
}

func (e *Engine) settleBuy(ctx context.Context, w *Wallet, wethIn, before *big.Int, txHash common.Hash) {
	settled := buySettlement{wallet: w, wethIn: new(big.Int).Set(wethIn), txHash: txHash}
	rcpt, err := e.client.WaitReceipt(ctx, txHash, 90*time.Second)
	if err != nil {
		settled.err = fmt.Errorf("buy confirm: %w", err)
	} else {
		e.recordGas(rcpt)
		got := big.NewInt(0)
		if e.cfg.ProtocolName() == ProtocolV2 {
			for _, trade := range pons.CurveTradesFromReceipt(rcpt) {
				if trade.IsBuy && (trade.Recipient == w.Addr || trade.Trader == w.Addr) {
					got.Add(got, trade.TokenAmount)
				}
			}
		} else {
			for _, trade := range pons.PoolTradesFromReceipt(rcpt, e.poolAddr, e.tokenIsToken0) {
				if trade.IsBuy {
					got.Add(got, trade.TokenAmount)
				}
			}
		}
		// Older pools may not emit a decodable trade event. Retain the previous
		// balance-difference fallback for that compatibility path.
		if got.Sign() == 0 {
			if after, balanceErr := e.client.TokenBalance(ctx, e.token, w.Addr); balanceErr == nil {
				got.Sub(after, before)
				if got.Sign() < 0 {
					got.SetInt64(0)
				}
			}
		}
		settled.tokensGot = got
		settled.ethAfter, _ = e.client.EthBalance(ctx, w.Addr)
	}
	select {
	case e.buySettlements <- settled:
	case <-ctx.Done():
	}
}

func (e *Engine) addPendingBuy() {
	e.pendingBuyMu.Lock()
	e.pendingBuys++
	e.pendingBuyMu.Unlock()
}

func (e *Engine) finishPendingBuy() {
	e.pendingBuyMu.Lock()
	if e.pendingBuys > 0 {
		e.pendingBuys--
	}
	e.pendingBuyMu.Unlock()
}

func (e *Engine) pendingBuyCount() int {
	e.pendingBuyMu.Lock()
	defer e.pendingBuyMu.Unlock()
	return e.pendingBuys
}

func (e *Engine) applyBuySettlement(ctx context.Context, settled buySettlement) {
	e.finishPendingBuy()
	if settled.err != nil {
		e.log.Warn("pending buy failed", "wallet", settled.wallet.Addr.Hex(),
			"tx", settled.txHash.Hex(), "err", settled.err)
		e.finishDrainIfEmpty()
		return
	}
	got := settled.tokensGot
	if got == nil {
		got = big.NewInt(0)
	}
	if current, err := e.client.TokenBalance(ctx, e.token, settled.wallet.Addr); err == nil {
		settled.wallet.setTokenBalance(current)
	}
	if settled.ethAfter != nil {
		settled.wallet.setETHBalance(settled.ethAfter)
	}
	e.monitor.RecordOurBuy(settled.wethIn, got)
	e.recordBuy(settled.wethIn)
	e.log.Info("buy confirmed", "wallet", settled.wallet.Addr.Hex(),
		"weth_in_eth", weiToEthStr(settled.wethIn), "tokens", got.String(),
		"tx", settled.txHash.Hex(), "pending_buys", e.pendingBuyCount())

	switch e.state {
	case ClearAll:
		e.log.Info("pending buy confirmed during exit; immediately selling residual",
			"wallet", settled.wallet.Addr.Hex())
		if err := e.sellWalletFast(ctx, settled.wallet, 1.0); err != nil {
			e.log.Warn("residual sell after pending buy failed", "wallet", settled.wallet.Addr.Hex(), "err", err)
		}
		e.finishDrainIfEmpty()
	case Distributing:
		if e.retailResponseActive {
			e.log.Info("pending buy confirmed during distribution; adding wallet to full-clear batches",
				"wallet", settled.wallet.Addr.Hex())
			e.startDistributionRound(ctx)
		}
	}
}

func (e *Engine) finishDrainIfEmpty() {
	if e.state == ClearAll && e.pendingBuyCount() == 0 && e.pool.TotalTokens().Sign() == 0 {
		e.state = Done
	}
}

// prepareExistingSellApprovals warms the sell allowance for every wallet that
// already holds the strategy token. It runs approvals concurrently so a
// restarted strategy can react to a profitable retail buy without a first-sell
// approval delay.
func (e *Engine) prepareExistingSellApprovals(ctx context.Context) {
	wallets := make([]*Wallet, 0, len(e.pool.All()))
	for _, w := range e.pool.All() {
		if w.tokenBalance().Sign() > 0 {
			wallets = append(wallets, w)
		}
	}
	if len(wallets) == 0 {
		return
	}
	e.log.Info("preparing concurrent sell approvals", "wallets", len(wallets))
	runWalletActions(true, wallets, func(w *Wallet) {
		if err := e.ensureApprove(ctx, w, big.NewInt(1), false); err != nil {
			e.log.Warn("sell approval prewarm failed", "wallet", w.Addr.Hex(), "err", err)
		}
	})
}

// sellTranche sells `frac` of each wallet's holding back to WETH and records the
// fills. frac=1 clears everything.
func (e *Engine) sellTranche(ctx context.Context, frac float64) error {
	return e.sellTrancheWithMode(ctx, frac, e.cfg.ConcurrentSells)
}

func (e *Engine) sellTrancheWithMode(ctx context.Context, frac float64, concurrent bool) error {
	wallets := make([]*Wallet, 0, len(e.pool.All()))
	for _, w := range e.pool.All() {
		if w.tokenBalance().Sign() == 0 {
			continue
		}
		wallets = append(wallets, w)
	}
	var errMu sync.Mutex
	var sellErrors []error
	runWalletActions(concurrent, wallets, func(w *Wallet) {
		balance := w.tokenBalance()
		amount := balance
		if frac < 1.0 {
			amount = scaleWei(balance, frac)
		}
		if amount.Sign() <= 0 {
			return
		}
		if err := e.sellOnce(ctx, w, amount); err != nil {
			e.log.Warn("sell failed", "wallet", w.Addr.Hex(), "err", err)
			errMu.Lock()
			sellErrors = append(sellErrors, fmt.Errorf("wallet %s: %w", w.Addr.Hex(), err))
			errMu.Unlock()
		}
	})
	return errors.Join(sellErrors...)
}

// sellAllFast uses the single aggregate profitability quote already fetched
// for the retail decision. It avoids repeating reserve and graduation calls for
// every wallet and broadcasts every sell before waiting on confirmations.
func (e *Engine) sellAllFast(ctx context.Context, totalQuote *big.Int) error {
	type plannedSell struct {
		wallet *Wallet
		amount *big.Int
		quote  *big.Int
	}
	var plans []plannedSell
	totalTokens := big.NewInt(0)
	for _, w := range e.pool.All() {
		amount := w.tokenBalance()
		if amount.Sign() == 0 {
			continue
		}
		plans = append(plans, plannedSell{wallet: w, amount: amount})
		totalTokens.Add(totalTokens, amount)
	}
	if totalTokens.Sign() == 0 {
		return nil
	}
	if totalQuote == nil || totalQuote.Sign() <= 0 {
		totalQuote = e.quoteSellAll(ctx, totalTokens)
	}
	for i := range plans {
		plans[i].quote = new(big.Int).Div(
			new(big.Int).Mul(totalQuote, plans[i].amount), totalTokens)
	}
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var sellErrors []error
	wg.Add(len(plans))
	for _, plan := range plans {
		plan := plan
		go func() {
			defer wg.Done()
			if err := e.sellOnceWithQuote(ctx, plan.wallet, plan.amount, plan.quote, true); err != nil {
				errMu.Lock()
				sellErrors = append(sellErrors, fmt.Errorf("wallet %s: %w", plan.wallet.Addr.Hex(), err))
				errMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return errors.Join(sellErrors...)
}

func (e *Engine) sellWalletFast(ctx context.Context, w *Wallet, frac float64) error {
	balance := w.tokenBalance()
	amount := balance
	if frac < 1 {
		amount = scaleWei(balance, frac)
	}
	if amount.Sign() == 0 {
		return nil
	}
	quote := e.quoteSellAll(ctx, amount)
	return e.sellOnceWithQuote(ctx, w, amount, quote, true)
}

func runWalletActions(concurrent bool, wallets []*Wallet, action func(*Wallet)) {
	if !concurrent {
		for _, w := range wallets {
			action(w)
		}
		return
	}
	var wg sync.WaitGroup
	wg.Add(len(wallets))
	for _, w := range wallets {
		go func(wallet *Wallet) {
			defer wg.Done()
			action(wallet)
		}(w)
	}
	wg.Wait()
}

// sellOnce sells `tokens` from wallet w, ensuring a router approval first, and
// unwraps the WETH proceeds back to ETH.
func (e *Engine) sellOnce(ctx context.Context, w *Wallet, tokens *big.Int) error {
	return e.sellOnceWithQuote(ctx, w, tokens, nil, false)
}

func (e *Engine) sellOnceWithQuote(ctx context.Context, w *Wallet, tokens, quoteHint *big.Int, fast bool) error {
	w.sellMu.Lock()
	defer w.sellMu.Unlock()
	// Another response (for example one-click exit) may have planned this wallet
	// while a distribution batch was still confirming. Re-read the balance under
	// the per-wallet sell lock so a stale plan cannot submit more than remains.
	current := w.tokenBalance()
	if current.Sign() == 0 {
		return nil
	}
	if tokens == nil || tokens.Cmp(current) > 0 {
		tokens = current
	}
	if !fast && e.cfg.ProtocolName() == ProtocolV2 {
		if graduated, err := e.client.Graduated(ctx, e.poolAddr); err != nil {
			return fmt.Errorf("check v2 curve state: %w", err)
		} else if graduated {
			return fmt.Errorf("v2 curve has graduated; curve sell is closed and the position must be handled on Uniswap v4")
		}
	}
	if err := e.ensureApprove(ctx, w, tokens, false); err != nil {
		return err
	}
	quote := quoteHint
	if quote == nil || quote.Sign() <= 0 {
		var err error
		quote, err = e.quoteSell(ctx, tokens)
		if err != nil {
			return fmt.Errorf("quote sell: %w", err)
		}
	}
	minOut := applySellSlippage(quote)
	if minOut.Sign() == 0 {
		minOut.SetInt64(1)
	}
	w.txMu.Lock()
	pr, err := e.pool.txParams(ctx, w, sellGasLimit, e.extraTipWei)
	if err != nil {
		w.txMu.Unlock()
		return err
	}
	var tx *types.Transaction
	if e.cfg.ProtocolName() == ProtocolV2 {
		tx, err = w.Signer.BuildSell(e.poolAddr, tokens, minOut, pr)
	} else {
		tx, err = w.Signer.BuildV1Sell(e.token, tokens, minOut, pr)
	}
	if err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("build sell: %w", err)
	}
	e.monitor.MarkOurTx(tx.Hash())
	if err := e.pool.send(ctx, w, tx); err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("send sell: %w", err)
	}
	w.txMu.Unlock()
	e.log.Info("sell submitted", "wallet", w.Addr.Hex(), "tokens", tokens.String(),
		"tx", tx.Hash().Hex(), "fast_exit", fast)
	rcpt, err := e.client.WaitReceipt(ctx, tx.Hash(), 90*time.Second)
	if err != nil {
		return fmt.Errorf("sell confirm: %w", err)
	}
	e.recordGas(rcpt)
	after, err := e.client.TokenBalance(ctx, e.token, w.Addr)
	if err == nil {
		w.setTokenBalance(after)
	} else {
		w.subtractToken(tokens)
	}
	e.monitor.RecordOurSell(tokens, quote)
	e.recordSell(tokens, quote)
	e.log.Info("sold", "wallet", w.Addr.Hex(),
		"tokens", tokens.String(), "weth_out_eth", weiToEthStr(quote), "tx", tx.Hash().Hex())
	if e.cfg.ProtocolName() == ProtocolV1 {
		e.unwrapWeth(ctx, w)
	} else if balance, balanceErr := e.client.EthBalance(ctx, w.Addr); balanceErr == nil {
		w.setETHBalance(balance)
	}
	return nil
}

// ensureApprove grants the router an infinite allowance if needed. When wait
// is false the approval is only submitted; a following sell uses the next
// nonce and can be broadcast immediately without waiting for confirmation.
func (e *Engine) ensureApprove(ctx context.Context, w *Wallet, need *big.Int, wait bool) error {
	e.approvalMu.Lock()
	if e.approvalReady[w.Addr] || e.approvalSubmitted[w.Addr] {
		e.approvalMu.Unlock()
		return nil
	}
	e.approvalMu.Unlock()

	spender := common.HexToAddress(pons.V1SwapRouter)
	if e.cfg.ProtocolName() == ProtocolV2 {
		spender = e.poolAddr
	}
	cur, err := e.client.Allowance(ctx, e.token, w.Addr, spender)
	if err != nil {
		return fmt.Errorf("read allowance: %w", err)
	}
	if cur.Cmp(need) >= 0 {
		e.approvalMu.Lock()
		e.approvalReady[w.Addr] = true
		e.approvalMu.Unlock()
		return nil
	}
	w.txMu.Lock()
	e.approvalMu.Lock()
	if e.approvalReady[w.Addr] || e.approvalSubmitted[w.Addr] {
		e.approvalMu.Unlock()
		w.txMu.Unlock()
		return nil
	}
	e.approvalMu.Unlock()
	pr, err := e.pool.txParams(ctx, w, approveGasLimit, e.extraTipWei)
	if err != nil {
		w.txMu.Unlock()
		return err
	}
	tx, err := w.Signer.BuildApprove(e.token, spender, maxUint256(), pr)
	if err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("build approve: %w", err)
	}
	if err := e.pool.send(ctx, w, tx); err != nil {
		w.txMu.Unlock()
		return fmt.Errorf("send approve: %w", err)
	}
	e.approvalMu.Lock()
	e.approvalSubmitted[w.Addr] = true
	e.approvalMu.Unlock()
	w.txMu.Unlock()
	e.log.Info("sell approval submitted", "wallet", w.Addr.Hex(), "tx", tx.Hash().Hex())
	if !wait {
		go e.watchApproval(ctx, w, tx.Hash())
		return nil
	}
	rcpt, err := e.client.WaitReceipt(ctx, tx.Hash(), 60*time.Second)
	if err != nil {
		e.approvalMu.Lock()
		e.approvalSubmitted[w.Addr] = false
		e.approvalMu.Unlock()
		return fmt.Errorf("approve confirm: %w", err)
	}
	e.recordGas(rcpt)
	e.approvalMu.Lock()
	e.approvalReady[w.Addr] = true
	e.approvalMu.Unlock()
	return nil
}

func (e *Engine) watchApproval(ctx context.Context, w *Wallet, txHash common.Hash) {
	rcpt, err := e.client.WaitReceipt(ctx, txHash, 60*time.Second)
	if err != nil {
		e.approvalMu.Lock()
		e.approvalSubmitted[w.Addr] = false
		e.approvalMu.Unlock()
		e.log.Warn("sell approval confirmation failed", "wallet", w.Addr.Hex(), "tx", txHash.Hex(), "err", err)
		return
	}
	e.recordGas(rcpt)
	e.approvalMu.Lock()
	e.approvalReady[w.Addr] = true
	e.approvalMu.Unlock()
}

// unwrapWeth converts a wallet's WETH proceeds back to native ETH (best effort).
func (e *Engine) unwrapWeth(ctx context.Context, w *Wallet) {
	weth := common.HexToAddress(pons.V1WETH)
	bal, err := e.client.TokenBalance(ctx, weth, w.Addr)
	if err != nil || bal.Sign() == 0 {
		return
	}
	w.txMu.Lock()
	pr, err := e.pool.txParams(ctx, w, withdrawGasLimit, e.extraTipWei)
	if err != nil {
		w.txMu.Unlock()
		return
	}
	tx, err := w.Signer.BuildWethWithdraw(bal, pr)
	if err != nil {
		w.txMu.Unlock()
		return
	}
	if err := e.pool.send(ctx, w, tx); err != nil {
		w.txMu.Unlock()
		e.log.Warn("weth unwrap send failed", "err", err)
		return
	}
	w.txMu.Unlock()
	if rcpt, err := e.client.WaitReceipt(ctx, tx.Hash(), 60*time.Second); err == nil {
		e.recordGas(rcpt)
		w.addETH(bal)
	}
}

// LiquidateAll refreshes balances and sells every wallet's holding of the bound
// token in a single pass. Used by `collect` before sweeping ETH.
func (e *Engine) LiquidateAll(ctx context.Context) error {
	if e.token == (common.Address{}) {
		return fmt.Errorf("engine not bound to a token")
	}
	if err := e.pool.RefreshToken(ctx, e.token); err != nil {
		return err
	}
	return e.sellTranche(ctx, 1.0)
}

// quoteSellAll prices selling our entire holding back to WETH in one hop
// (approximate: real execution splits across wallets and moves price).
func (e *Engine) quoteSellAll(ctx context.Context, tokens *big.Int) *big.Int {
	if tokens == nil || tokens.Sign() == 0 {
		return big.NewInt(0)
	}
	out, err := e.quoteSell(ctx, tokens)
	if err != nil {
		return big.NewInt(0)
	}
	return out
}

func (e *Engine) quoteBuy(ctx context.Context, wallet *Wallet, quoteIn *big.Int) (*big.Int, error) {
	if e.cfg.ProtocolName() == ProtocolV1 {
		return e.client.QuoteV1Buy(ctx, e.token, quoteIn)
	}
	quoteReserve, tokenReserve, err := e.client.Reserves(ctx, e.poolAddr)
	if err != nil {
		return nil, err
	}
	snipeTax, err := e.client.SnipeTaxBps(ctx, e.poolAddr, wallet.Addr)
	if err != nil {
		return nil, err
	}
	return pons.TokensOutForQuote(quoteReserve, tokenReserve, quoteIn,
		e.v2Info.FeeBps, e.v2Info.CreatorTaxBps+snipeTax), nil
}

func (e *Engine) quoteSell(ctx context.Context, tokens *big.Int) (*big.Int, error) {
	if e.cfg.ProtocolName() == ProtocolV1 {
		return e.client.QuoteV1Sell(ctx, e.token, tokens)
	}
	quoteReserve, tokenReserve, err := e.client.Reserves(ctx, e.poolAddr)
	if err != nil {
		return nil, err
	}
	return pons.QuoteOutForTokens(quoteReserve, tokenReserve, tokens,
		e.v2Info.FeeBps, e.v2Info.CreatorTaxBps), nil
}
