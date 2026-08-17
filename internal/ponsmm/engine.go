package ponsmm

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
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
	Oscillating
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
	case Oscillating:
		return "oscillating"
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
}

// NewEngine wires an engine. token/poolAddr may be zero if a launch will run
// first; call Bind after launch to attach them.
func NewEngine(cfg *Config, client *pons.Client, pool *Pool, log *slog.Logger) *Engine {
	return &Engine{
		cfg:             cfg,
		client:          client,
		pool:            pool,
		log:             log,
		state:           Launching,
		extraTipWei:     gweiToWei(cfg.PriorityTipGwei),
		exitAllRequests: make(chan chan error),
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
	launched, ok := pons.LaunchedFromReceipt(rcpt)
	if !ok {
		return fmt.Errorf("launch receipt %s carried no TokenLaunched event", tx.Hash().Hex())
	}
	e.log.Info("token launched",
		"token", launched.Token.Hex(),
		"pool", launched.Pool.Hex(),
		"block", launched.Block)
	return e.Bind(ctx, launched.Token, launched.Pool)
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
	e.log.Info("v2 launch preflight ok",
		"deployer", who.Hex(), "launch_fee_eth", weiToEthStr(fee),
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
	pr, err := e.pool.txParams(ctx, e.pool.Treasury, launchV2GasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	tx, err := e.pool.Treasury.Signer.BuildV2Launch(params, e.cfg.LaunchConfigID, pairToken, exemptions, fee, pr)
	if err != nil {
		return fmt.Errorf("build v2 launch: %w", err)
	}
	if err := e.pool.send(ctx, e.pool.Treasury, tx); err != nil {
		return fmt.Errorf("send v2 launch: %w", err)
	}
	e.log.Info("v2 launch submitted", "tx", tx.Hash().Hex())
	rcpt, err := e.client.WaitReceipt(ctx, tx.Hash(), 120*time.Second)
	if err != nil {
		return fmt.Errorf("v2 launch confirm: %w", err)
	}
	launched, ok := pons.V2LaunchedFromReceipt(rcpt)
	if !ok {
		return fmt.Errorf("v2 launch receipt %s carried no TokenLaunched event", tx.Hash().Hex())
	}
	e.log.Info("v2 token launched", "token", launched.Token.Hex(), "curve", launched.Curve.Hex(), "block", launched.Block)
	return e.Bind(ctx, launched.Token, launched.Curve)
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
	if err := e.pool.RefreshETH(ctx); err != nil {
		return err
	}
	e.lastFundsRefresh = time.Now()
	if err := e.pool.RefreshToken(ctx, e.token); err != nil {
		return err
	}
	if err := e.monitor.RefreshReserves(ctx); err != nil {
		e.log.Warn("initial reserve refresh failed", "err", err)
	}
	e.seedMonitorFromBalances()

	go e.monitor.Run(ctx)

	accTick := time.NewTicker(e.cfg.AccumulateInterval)
	defer accTick.Stop()
	sellTick := time.NewTicker(e.cfg.SellInterval)
	defer sellTick.Stop()
	reserveTick := time.NewTicker(10 * time.Second)
	defer reserveTick.Stop()

	e.log.Info("market maker running",
		"token", e.token.Hex(), "pool", e.poolAddr.Hex(), "state", e.state.String())

	for {
		select {
		case <-ctx.Done():
			e.log.Info("shutting down", "state", e.state.String())
			return nil
		case ev := <-e.monitor.Retail:
			e.onRetail(ctx, ev)
		case done := <-e.exitAllRequests:
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
				e.accumulateStep(ctx)
			}
		case <-sellTick.C:
			switch e.state {
			case Distributing:
				e.distributeStep(ctx)
			case Oscillating:
				e.oscillateStep(ctx)
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
// or an earlier accumulation run) so cost/holding math is anchored. Cost is
// unknown for pre-existing holdings, so it is recorded at the current pool price.
func (e *Engine) seedMonitorFromBalances() {
	held := e.pool.TotalTokens()
	if held.Sign() == 0 {
		return
	}
	e.monitor.mu.Lock()
	e.monitor.ourTokens = new(big.Int).Set(held)
	e.monitor.mu.Unlock()
}

// onRetail reacts to an outside trade.
func (e *Engine) onRetail(ctx context.Context, ev RetailEvent) {
	snap := e.monitor.Snapshot()
	if ev.IsBuy {
		e.log.Info("retail buy detected",
			"tokens", ev.TokenAmount.String(), "weth", weiToEthStr(ev.WethAmount),
			"our_hold_frac", snap.OurHoldFrac, "state", e.state.String())
		if snap.OurHoldFrac >= e.cfg.HighHold {
			if e.state != Oscillating {
				e.log.Info("high holding + retail buy -> oscillating around retail cost")
				e.state = Oscillating
			}
			responseFrac := retailResponseFraction(ev.TokenAmount, snap.OurTokens, e.cfg.SellTranche)
			if responseFrac > 0 {
				e.log.Info("responding to retail buy with immediate sell",
					"fraction", responseFrac, "concurrent", e.cfg.ConcurrentSells)
				if err := e.sellTranche(ctx, responseFrac); err != nil {
					e.log.Warn("immediate retail-response sell failed", "err", err)
				}
			}
			return
		}
		// Low holding: decide clear-all vs slow distribute by whether an
		// immediate full exit at least breaks even.
		proceeds := e.quoteSellAll(ctx, snap.OurTokens)
		next := retailBuyResponse(snap.OurHoldFrac, e.cfg.HighHold, proceeds, snap.OurWethSpent)
		e.log.Info("low holding + retail buy",
			"sell_all_proceeds_eth", weiToEthStr(proceeds), "our_cost_eth", weiToEthStr(snap.OurWethSpent),
			"decision", next.String())
		e.state = next
		if e.state == ClearAll {
			if err := e.clearAllStep(ctx); err != nil {
				e.log.Warn("clear-all response failed", "err", err)
			}
		}
		return
	}
	e.log.Info("retail sell detected",
		"tokens", ev.TokenAmount.String(), "weth", weiToEthStr(ev.WethAmount),
		"retail_net_tokens", snap.RetailNetTokens.String(), "state", e.state.String())
	// A seller is leaving the flow we were responding to. Distribution and
	// oscillation both return to accumulation; if makers are empty the next tick
	// enters an explicit waiting-for-funds state instead of silently holding an
	// obsolete retail price anchor.
	if e.state == Distributing || e.state == Oscillating {
		if snap.RetailNetTokens.Sign() == 0 {
			e.log.Info("retail position exited; cleared retail price anchor -> accumulating")
		} else {
			e.log.Info("retail sell -> accumulating")
		}
		e.state = Accumulating
	}
}

// accumulateStep buys from the next maker wallet with spendable ETH, unless we
// have already met the chip target and (if graduating) the pool is past the
// threshold.
func (e *Engine) accumulateStep(ctx context.Context) {
	snap := e.monitor.Snapshot()
	graduated := e.pastGraduation(ctx)
	if e.cfg.ProtocolName() == ProtocolV2 && graduated {
		e.log.Warn("v2 curve graduated; accumulation stopped", "token", e.token.Hex())
		e.state = Done
		return
	}
	if snap.OurHoldFrac >= e.cfg.ChipTarget && (!e.cfg.Graduate || graduated) {
		return
	}
	if !e.ensureMakerFunds(ctx) {
		return
	}
	if e.cfg.ConcurrentBuys {
		runWalletActions(true, e.fundedMakers(), func(w *Wallet) {
			e.buyFromMaker(ctx, w, 1)
		})
		return
	}
	e.buyFromMaker(ctx, e.nextFundedMaker(), 1)
}

// distributeStep sells one slow tranche of our remaining holding. When we reach
// zero the token is done.
func (e *Engine) distributeStep(ctx context.Context) {
	snap := e.monitor.Snapshot()
	if snap.OurTokens.Sign() <= 0 {
		e.log.Info("distribution complete; holding is zero -> done")
		e.state = Done
		return
	}
	if err := e.sellTranche(ctx, e.cfg.SellTranche); err != nil {
		e.log.Warn("distribution sell round failed", "err", err)
	}
}

// clearAllStep liquidates the entire position across all wallets in one pass.
func (e *Engine) clearAllStep(ctx context.Context) error {
	e.log.Info("clearing entire position")
	if err := e.sellTranche(ctx, 1.0); err != nil {
		return err
	}
	e.state = Done
	return nil
}

func (e *Engine) exitAllStep(ctx context.Context) error {
	e.log.Info("one-click exit: concurrently clearing every wallet position")
	if err := e.sellTrancheWithMode(ctx, 1.0, true); err != nil {
		return err
	}
	e.state = Done
	return nil
}

// oscillateStep keeps the price under the retail cost anchor, wiggling within
// the configured band: sell when price rises to the anchor, buy when it dips
// below the lower band.
func (e *Engine) oscillateStep(ctx context.Context) {
	snap := e.monitor.Snapshot()
	if snap.RetailLastBuyPx == nil || snap.PriceWeiPerToken == nil {
		return
	}
	action := oscillationAction(snap.PriceWeiPerToken, snap.RetailLastBuyPx, e.cfg.OscillationBand)
	switch action {
	case actionSell:
		if err := e.sellTranche(ctx, e.cfg.SellTranche); err != nil {
			e.log.Warn("oscillation sell round failed", "err", err)
		}
	case actionBuy:
		if !e.ensureMakerFunds(ctx) {
			return
		}
		if e.cfg.ConcurrentBuys {
			runWalletActions(true, e.fundedMakers(), func(w *Wallet) {
				e.buyFromMaker(ctx, w, 0.25)
			})
		} else {
			e.buyFromMaker(ctx, e.nextFundedMaker(), 0.25)
		}
	}
}

func (e *Engine) buyFromMaker(ctx context.Context, w *Wallet, fractionScale float64) {
	if w == nil {
		return
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction*fractionScale)
	if spend.Sign() <= 0 {
		return
	}
	spend = e.clampBuyToWalletCap(ctx, w, spend)
	if spend.Sign() <= 0 {
		return
	}
	if err := e.buyOnce(ctx, w, spend); err != nil {
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

// nextFundedMaker round-robins to the next maker wallet with spendable ETH.
func (e *Engine) nextFundedMaker() *Wallet {
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	n := len(e.pool.Makers)
	for i := 0; i < n; i++ {
		w := e.pool.Makers[(e.rr+i)%n]
		if w.spendableWei(gasReserve).Sign() > 0 {
			e.rr = (e.rr + i + 1) % n
			return w
		}
	}
	return nil
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
	room := new(big.Int).Sub(capTokens, w.TokenRaw)
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

// buyOnce spends wethIn from wallet w on the launch token and records the fill.
func (e *Engine) buyOnce(ctx context.Context, w *Wallet, wethIn *big.Int) error {
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
	pr, err := e.pool.txParams(ctx, w, buyGasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	var tx *types.Transaction
	if e.cfg.ProtocolName() == ProtocolV2 {
		tx, err = w.Signer.BuildBuy(e.poolAddr, wethIn, minOut, true, pr)
	} else {
		tx, err = w.Signer.BuildV1Buy(e.token, wethIn, minOut, pr)
	}
	if err != nil {
		return fmt.Errorf("build buy: %w", err)
	}
	e.monitor.MarkOurTx(tx.Hash())
	if err := e.pool.send(ctx, w, tx); err != nil {
		return fmt.Errorf("send buy: %w", err)
	}
	if _, err := e.client.WaitReceipt(ctx, tx.Hash(), 90*time.Second); err != nil {
		return fmt.Errorf("buy confirm: %w", err)
	}
	after, err := e.client.TokenBalance(ctx, e.token, w.Addr)
	if err != nil {
		return fmt.Errorf("balance after: %w", err)
	}
	got := new(big.Int).Sub(after, before)
	if got.Sign() < 0 {
		got.SetInt64(0)
	}
	w.TokenRaw = after
	if e.cfg.ProtocolName() == ProtocolV2 {
		if balance, balanceErr := e.client.EthBalance(ctx, w.Addr); balanceErr == nil {
			w.ETHWei = balance
		}
	} else if w.ETHWei != nil {
		w.ETHWei = new(big.Int).Sub(w.ETHWei, wethIn)
	}
	e.monitor.RecordOurBuy(wethIn, got)
	e.log.Info("bought", "wallet", w.Addr.Hex(),
		"weth_in_eth", weiToEthStr(wethIn), "tokens", got.String(), "tx", tx.Hash().Hex())
	return nil
}

// sellTranche sells `frac` of each wallet's holding back to WETH and records the
// fills. frac=1 clears everything.
func (e *Engine) sellTranche(ctx context.Context, frac float64) error {
	return e.sellTrancheWithMode(ctx, frac, e.cfg.ConcurrentSells)
}

func (e *Engine) sellTrancheWithMode(ctx context.Context, frac float64, concurrent bool) error {
	wallets := make([]*Wallet, 0, len(e.pool.All()))
	for _, w := range e.pool.All() {
		if w.TokenRaw == nil || w.TokenRaw.Sign() == 0 {
			continue
		}
		wallets = append(wallets, w)
	}
	var errMu sync.Mutex
	var sellErrors []error
	runWalletActions(concurrent, wallets, func(w *Wallet) {
		amount := w.TokenRaw
		if frac < 1.0 {
			amount = scaleWei(w.TokenRaw, frac)
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
	if e.cfg.ProtocolName() == ProtocolV2 {
		if graduated, err := e.client.Graduated(ctx, e.poolAddr); err != nil {
			return fmt.Errorf("check v2 curve state: %w", err)
		} else if graduated {
			return fmt.Errorf("v2 curve has graduated; curve sell is closed and the position must be handled on Uniswap v4")
		}
	}
	if err := e.ensureApprove(ctx, w, tokens); err != nil {
		return err
	}
	quote, err := e.quoteSell(ctx, tokens)
	if err != nil {
		return fmt.Errorf("quote sell: %w", err)
	}
	minOut := applySlippage(quote, e.cfg.SlippageBps)
	pr, err := e.pool.txParams(ctx, w, sellGasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	var tx *types.Transaction
	if e.cfg.ProtocolName() == ProtocolV2 {
		tx, err = w.Signer.BuildSell(e.poolAddr, tokens, minOut, pr)
	} else {
		tx, err = w.Signer.BuildV1Sell(e.token, tokens, minOut, pr)
	}
	if err != nil {
		return fmt.Errorf("build sell: %w", err)
	}
	e.monitor.MarkOurTx(tx.Hash())
	if err := e.pool.send(ctx, w, tx); err != nil {
		return fmt.Errorf("send sell: %w", err)
	}
	if _, err := e.client.WaitReceipt(ctx, tx.Hash(), 90*time.Second); err != nil {
		return fmt.Errorf("sell confirm: %w", err)
	}
	after, err := e.client.TokenBalance(ctx, e.token, w.Addr)
	if err == nil {
		w.TokenRaw = after
	} else {
		w.TokenRaw = new(big.Int).Sub(w.TokenRaw, tokens)
	}
	e.monitor.RecordOurSell(tokens, quote)
	e.log.Info("sold", "wallet", w.Addr.Hex(),
		"tokens", tokens.String(), "weth_out_eth", weiToEthStr(quote), "tx", tx.Hash().Hex())
	if e.cfg.ProtocolName() == ProtocolV1 {
		e.unwrapWeth(ctx, w)
	} else if balance, balanceErr := e.client.EthBalance(ctx, w.Addr); balanceErr == nil {
		w.ETHWei = balance
	}
	return nil
}

// ensureApprove grants the router an infinite allowance if the current one is
// below `need`.
func (e *Engine) ensureApprove(ctx context.Context, w *Wallet, need *big.Int) error {
	spender := common.HexToAddress(pons.V1SwapRouter)
	if e.cfg.ProtocolName() == ProtocolV2 {
		spender = e.poolAddr
	}
	cur, err := e.client.Allowance(ctx, e.token, w.Addr, spender)
	if err != nil {
		return fmt.Errorf("read allowance: %w", err)
	}
	if cur.Cmp(need) >= 0 {
		return nil
	}
	pr, err := e.pool.txParams(ctx, w, approveGasLimit, e.extraTipWei)
	if err != nil {
		return err
	}
	tx, err := w.Signer.BuildApprove(e.token, spender, maxUint256(), pr)
	if err != nil {
		return fmt.Errorf("build approve: %w", err)
	}
	if err := e.pool.send(ctx, w, tx); err != nil {
		return fmt.Errorf("send approve: %w", err)
	}
	if _, err := e.client.WaitReceipt(ctx, tx.Hash(), 60*time.Second); err != nil {
		return fmt.Errorf("approve confirm: %w", err)
	}
	return nil
}

// unwrapWeth converts a wallet's WETH proceeds back to native ETH (best effort).
func (e *Engine) unwrapWeth(ctx context.Context, w *Wallet) {
	weth := common.HexToAddress(pons.V1WETH)
	bal, err := e.client.TokenBalance(ctx, weth, w.Addr)
	if err != nil || bal.Sign() == 0 {
		return
	}
	pr, err := e.pool.txParams(ctx, w, withdrawGasLimit, e.extraTipWei)
	if err != nil {
		return
	}
	tx, err := w.Signer.BuildWethWithdraw(bal, pr)
	if err != nil {
		return
	}
	if err := e.pool.send(ctx, w, tx); err != nil {
		e.log.Warn("weth unwrap send failed", "err", err)
		return
	}
	if _, err := e.client.WaitReceipt(ctx, tx.Hash(), 60*time.Second); err == nil {
		if w.ETHWei != nil {
			w.ETHWei = new(big.Int).Add(w.ETHWei, bal)
		}
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
