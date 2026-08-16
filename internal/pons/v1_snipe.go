package pons

import (
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

const v1WithdrawGasLimit = 80_000

// SniperV1 buys new pons v1 launches (straight-into-Uniswap-V3 stack) and,
// when AutoSell is set, flips each on a profit target, stop-loss, dev dump, or
// timeout. Trading runs through the official SwapRouter02; valuation through
// QuoterV2.
type SniperV1 struct {
	Client *Client
	Signer *Signer
	Log    *slog.Logger

	// BuyETH is the ETH spent per snipe.
	BuyETH float64
	// SlippageBps bounds buy/sell price movement (e.g. 1500 = 15%).
	SlippageBps int64
	// PriorityTipGwei is added to the suggested tip.
	PriorityTipGwei float64

	// MinInitialBuyETH skips launches whose creator bought less than this at
	// launch. On v1 the creator's initial buy is the strongest commitment
	// signal: sampled pools with 1+ ETH initial buys saw hundreds of trades,
	// while <0.05 ETH launches saw almost none.
	MinInitialBuyETH float64
	// Filter selects launches by metadata; nil accepts everything.
	Filter func(V1Launch, V1LaunchInfo) bool

	AutoSell      bool
	ProfitTarget  float64
	StopLossFrac  float64
	FollowDevExit float64
	HoldTimeout   time.Duration
	PollInterval  time.Duration

	DryRun      bool
	Limit       int
	Concurrency int
	Verbose     bool
}

// V1Result reports one v1 snipe.
type V1Result struct {
	Launch    V1Launch
	Info      V1LaunchInfo
	BuyTxHash string
	TokensOut *big.Int
	CostWei   *big.Int
	Confirmed bool
}

func (s *SniperV1) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (s *SniperV1) concurrency() int {
	if s.Concurrency > 1 {
		return s.Concurrency
	}
	return 1
}

func (s *SniperV1) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return 15 * time.Second
}

func (s *SniperV1) holdTimeout() time.Duration {
	if s.HoldTimeout > 0 {
		return s.HoldTimeout
	}
	return 10 * time.Minute
}

func (s *SniperV1) buyWei() *big.Int { return ethToWei(s.BuyETH) }

// Run watches for v1 launches and snipes each match until ctx ends or Limit is
// reached, running up to Concurrency positions in parallel.
func (s *SniperV1) Run(ctx context.Context) ([]V1Result, error) {
	if s.Client == nil || s.Signer == nil {
		return nil, fmt.Errorf("pons v1 snipe: client and signer are required")
	}
	if s.BuyETH <= 0 {
		return nil, fmt.Errorf("pons v1 snipe: buy amount must be > 0")
	}
	log := s.logger()

	conc := s.concurrency()
	slots := make(chan struct{}, conc)
	for i := 0; i < conc; i++ {
		slots <- struct{}{}
	}

	var (
		mu          sync.Mutex
		results     []V1Result
		inflight    int
		pendingBuys int
		wg          sync.WaitGroup
		posDone     = make(chan struct{}, conc)
	)
	var observed, skipped uint64

	drain := func() []V1Result {
		wg.Wait()
		mu.Lock()
		defer mu.Unlock()
		return results
	}
	limitReached := func() bool {
		if s.Limit <= 0 {
			return false
		}
		mu.Lock()
		defer mu.Unlock()
		return len(results) >= s.Limit && inflight == 0
	}

	minInitialWei := ethToWei(s.MinInitialBuyETH)
	launches := s.Client.WatchV1Launches(ctx, log)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		if limitReached() {
			log.Info("pons v1 snipe limit reached", "limit", s.Limit)
			return drain(), nil
		}
		select {
		case <-ctx.Done():
			return drain(), ctx.Err()
		case <-posDone:
		case <-ticker.C:
			mu.Lock()
			sniped, active := len(results), inflight
			mu.Unlock()
			log.Info("watching for pons v1 launches",
				"observed", observed, "skipped", skipped, "sniped", sniped,
				"active_positions", active, "free_slots", conc-active)
		case l, ok := <-launches:
			if !ok {
				return drain(), ctx.Err()
			}
			observed++

			if l.PairToken != (common.Address{}) && l.PairToken != common.HexToAddress(V1WETH) {
				skipped++
				if s.Verbose {
					log.Debug("skip: non-WETH pair", "token", l.Token.Hex(), "pair", l.PairToken.Hex())
				}
				continue
			}
			if s.MinInitialBuyETH > 0 && (l.InitialBuyWei == nil || l.InitialBuyWei.Cmp(minInitialWei) < 0) {
				skipped++
				if s.Verbose {
					log.Debug("skip: creator initial buy too small",
						"token", l.Token.Hex(), "initial_buy_eth", weiToEth(l.InitialBuyWei), "min_eth", s.MinInitialBuyETH)
				}
				continue
			}

			infoCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			info := s.Client.LoadV1TokenMeta(infoCtx, l.Token)
			cancel()
			if s.Verbose {
				log.Debug("observed v1 launch", "symbol", info.Symbol, "name", info.Name,
					"token", l.Token.Hex(), "initial_buy_eth", weiToEth(l.InitialBuyWei))
			}
			if s.Filter != nil && !s.Filter(l, info) {
				skipped++
				continue
			}

			mu.Lock()
			overBudget := s.Limit > 0 && len(results)+pendingBuys >= s.Limit
			mu.Unlock()
			if overBudget {
				continue
			}
			select {
			case <-slots:
			default:
				log.Info("all slots busy; skipping launch", "token", l.Token.Hex(), "symbol", info.Symbol)
				continue
			}

			mu.Lock()
			inflight++
			pendingBuys++
			mu.Unlock()
			wg.Add(1)
			go func(l V1Launch, info V1LaunchInfo) {
				defer wg.Done()
				res, err := s.snipeOne(ctx, l, info)
				mu.Lock()
				pendingBuys--
				if err == nil {
					results = append(results, res)
				}
				mu.Unlock()
				if err != nil {
					if ctx.Err() == nil {
						log.Error("pons v1 snipe failed", "token", l.Token.Hex(), "symbol", info.Symbol, "err", err)
					}
					if isInsufficientFunds(err) && ctx.Err() == nil {
						log.Warn("wallet balance too low to buy; sleeping this slot",
							"cooldown", brokeCooldown, "payer", s.Signer.Address().Hex())
						select {
						case <-time.After(brokeCooldown):
							log.Info("slot cooldown over; resuming")
						case <-ctx.Done():
						}
					}
				} else if s.AutoSell && res.Confirmed {
					if aerr := s.autoExitV1(ctx, res); aerr != nil && ctx.Err() == nil {
						log.Error("pons v1 auto-sell failed", "token", l.Token.Hex(), "err", aerr)
					}
				}
				mu.Lock()
				inflight--
				mu.Unlock()
				slots <- struct{}{}
				select {
				case posDone <- struct{}{}:
				default:
				}
			}(l, info)
		}
	}
}

// snipeOne waits out the launch-protection window, prices, buys through the
// router, and waits for the buy to confirm.
func (s *SniperV1) snipeOne(ctx context.Context, l V1Launch, info V1LaunchInfo) (V1Result, error) {
	log := s.logger()
	t0 := time.Now()

	// Prefetch the nonce and gas params concurrently while we sit out the
	// launch-protection block: we send nothing during the wait, so the nonce
	// stays valid, and gas comes from the background cache. This leaves only
	// quote + send on the post-wait critical path instead of five serial RPC
	// round trips.
	type prep struct {
		nonce       uint64
		tip, feeCap *big.Int
		err         error
	}
	prepCh := make(chan prep, 1)
	go func() {
		var p prep
		p.nonce, p.err = s.Client.PendingNonce(ctx, s.Signer.Address())
		if p.err == nil {
			p.tip, p.feeCap, p.err = s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
		}
		prepCh <- p
	}()

	if err := s.waitRestrictions(ctx, l, info); err != nil {
		return V1Result{}, err
	}

	ethIn := s.buyWei()
	expTokens, err := s.Client.QuoteV1Buy(ctx, l.Token, ethIn)
	if err != nil {
		return V1Result{}, fmt.Errorf("quote buy: %w", err)
	}
	if expTokens.Sign() <= 0 {
		return V1Result{}, fmt.Errorf("quoted zero tokens out")
	}
	minTokensOut := applySlippageDown(expTokens, s.SlippageBps)

	log.Info("v1 target matched; buying",
		"token", l.Token.Hex(), "symbol", info.Symbol,
		"buy_eth", s.BuyETH, "exp_tokens", weiToStr(expTokens), "min_tokens", weiToStr(minTokensOut),
		"creator_initial_buy_eth", weiToEth(l.InitialBuyWei))

	if s.DryRun {
		log.Info("dry-run: priced v1 buy, not sending", "token", l.Token.Hex(), "build_ms", time.Since(t0).Milliseconds())
		return V1Result{Launch: l, Info: info, TokensOut: expTokens}, nil
	}

	p := <-prepCh
	if p.err != nil {
		return V1Result{}, fmt.Errorf("prep buy tx: %w", p.err)
	}
	tx, err := s.Signer.BuildV1Buy(l.Token, ethIn, minTokensOut, TxParams{
		Nonce: p.nonce, GasLimit: buyGasLimit, TipCap: p.tip, FeeCap: p.feeCap,
	})
	if err != nil {
		return V1Result{}, err
	}
	sendMs := time.Now()
	if err := s.Client.Send(ctx, tx); err != nil {
		return V1Result{}, fmt.Errorf("send buy: %w", err)
	}
	log.Info("v1 buy submitted", "token", l.Token.Hex(), "tx", tx.Hash().Hex(),
		"send_ms", time.Since(sendMs).Milliseconds())

	rcpt, err := s.waitReceipt(ctx, tx.Hash(), 60*time.Second)
	if err != nil {
		return V1Result{Launch: l, Info: info, BuyTxHash: tx.Hash().Hex()}, nil
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		return V1Result{}, fmt.Errorf("buy tx reverted (%s) — likely still inside the launch-protection block", tx.Hash().Hex())
	}

	held, err := s.Client.TokenBalance(ctx, l.Token, s.Signer.Address())
	if err != nil || held.Sign() <= 0 {
		return V1Result{}, fmt.Errorf("buy confirmed but holding not found: %v", err)
	}
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(rcpt.GasUsed), rcpt.EffectiveGasPrice)
	cost := new(big.Int).Add(ethIn, gasCost)

	log.Info("v1 buy confirmed",
		"token", l.Token.Hex(), "tokens", weiToStr(held), "cost_eth", weiToEth(cost),
		"total_ms", time.Since(t0).Milliseconds())
	return V1Result{
		Launch: l, Info: info, BuyTxHash: tx.Hash().Hex(),
		TokensOut: held, CostWei: cost, Confirmed: true,
	}, nil
}

// waitRestrictions blocks until the current L1 block is past the launch block,
// where only the creator's initial buy can execute. The remaining window (one
// more L1 block, per-wallet 5%-of-supply caps) does not constrain a small buy,
// so we enter as soon as the creator-only block has passed.
func (s *SniperV1) waitRestrictions(ctx context.Context, l V1Launch, info V1LaunchInfo) error {
	if l.RestrictionsEndBlock == 0 {
		return nil
	}
	log := s.logger()
	// TokenLaunched carries the LAST restricted L1 block; the launch itself
	// happened two L1 blocks earlier.
	launchL1 := l.RestrictionsEndBlock - 2
	deadline := time.Now().Add(90 * time.Second)
	first := true
	for {
		l1bn, err := s.Client.L1BlockNumber(ctx)
		if err != nil {
			return fmt.Errorf("read l1 block: %w", err)
		}
		if l1bn > launchL1 {
			if !first {
				log.Info("creator-only block passed; proceeding", "token", l.Token.Hex(), "l1_block", l1bn)
			}
			return nil
		}
		if first {
			log.Info("waiting out v1 launch-protection block",
				"token", l.Token.Hex(), "symbol", info.Symbol,
				"l1_block", l1bn, "buy_after_l1_block", launchL1)
			first = false
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("l1 block still %d after 90s (need > %d)", l1bn, launchL1)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// autoExitV1 mirrors the v2 monitor: exit on profit target, stop-loss, dev
// dump, or timeout. There is no graduation exit — a v1 pool keeps trading in
// place forever.
func (s *SniperV1) autoExitV1(ctx context.Context, res V1Result) error {
	log := s.logger()
	token := res.Launch.Token
	router := common.HexToAddress(V1SwapRouter)

	target := scaleFrac(res.CostWei, 1+s.ProfitTarget)

	// Stop-loss baseline: on a thin V3 pool our own buy moves the price so
	// much that the immediate sell-back value sits far below cost (often
	// -50%+ round-trip). A cost-based stop would trigger instantly and lock
	// that impact in as a realized loss, so the stop is measured from the
	// first post-buy valuation instead: exit only if the position loses
	// another StopLossFrac from where we actually entered.
	var stop *big.Int
	if s.StopLossFrac > 0 {
		base := res.CostWei
		if v, err := s.Client.QuoteV1Sell(ctx, token, res.TokensOut); err == nil && v.Sign() > 0 && v.Cmp(base) < 0 {
			base = v
		}
		stop = scaleFrac(base, 1-s.StopLossFrac)
	}
	log.Info("pons v1 monitor started",
		"token", token.Hex(), "cost_eth", weiToEth(res.CostWei),
		"take_profit_eth", weiToEth(target), "stop_loss_eth", weiToEthOrDash(stop),
		"hold_timeout", s.holdTimeout())

	if err := s.ensureApprovalV1(ctx, token, router, res.TokensOut); err != nil {
		log.Warn("pre-approve failed; will approve at exit", "err", err)
	}

	// On v1 the creator holds their initial buy from block one, so the
	// dev-dump watch is armed on almost every position.
	var devBaseline, devTrip *big.Int
	if s.FollowDevExit > 0 {
		if bal, err := s.Client.TokenBalance(ctx, token, res.Launch.Deployer); err == nil && bal.Sign() > 0 {
			devBaseline = bal
			devTrip = scaleFrac(bal, 1-s.FollowDevExit)
			log.Info("dev-dump watch armed", "creator", res.Launch.Deployer.Hex(),
				"baseline", weiToStr(bal), "exit_below", weiToStr(devTrip))
		}
	}

	swaps := s.Client.WatchPoolSwaps(ctx, res.Launch.Pool, log)
	deadline := time.NewTimer(s.holdTimeout())
	defer deadline.Stop()
	poll := time.NewTicker(s.pollInterval())
	defer poll.Stop()

	evaluate := func() (done bool, reason string) {
		value, err := s.Client.QuoteV1Sell(ctx, token, res.TokensOut)
		if err != nil {
			log.Warn("valuation failed", "err", err)
			return false, ""
		}
		log.Info("valuation",
			"value_eth", weiToEth(value), "target_eth", weiToEth(target),
			"profitable", value.Cmp(target) >= 0)
		switch {
		case value.Cmp(target) >= 0:
			return true, "target"
		case stop != nil && value.Cmp(stop) <= 0:
			return true, "stop-loss"
		}
		return false, ""
	}

	for {
		select {
		case <-ctx.Done():
			log.Warn("shutdown requested; liquidating position", "token", token.Hex())
			cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			return s.sellAllV1(cleanup, res, "shutdown")

		case <-deadline.C:
			log.Info("hold timeout reached; selling", "token", token.Hex())
			return s.sellAllV1(ctx, res, "timeout")

		case <-tradeSignal(swaps):
			if done, reason := evaluate(); done {
				return s.sellAllV1(ctx, res, reason)
			}

		case <-poll.C:
			if devTrip != nil {
				if bal, err := s.Client.TokenBalance(ctx, token, res.Launch.Deployer); err == nil && bal.Cmp(devTrip) <= 0 {
					log.Warn("creator dumped their holding; following exit",
						"creator", res.Launch.Deployer.Hex(), "now", weiToStr(bal), "baseline", weiToStr(devBaseline))
					return s.sellAllV1(ctx, res, "dev-dump")
				}
			}
			if done, reason := evaluate(); done {
				return s.sellAllV1(ctx, res, reason)
			}
		}
	}
}

// sellAllV1 sells the full holding through the router, then unwraps the WETH
// proceeds back to ETH (best-effort) so the next snipe can spend them.
func (s *SniperV1) sellAllV1(ctx context.Context, res V1Result, reason string) error {
	log := s.logger()
	token := res.Launch.Token
	router := common.HexToAddress(V1SwapRouter)
	me := s.Signer.Address()

	held, err := s.Client.TokenBalance(ctx, token, me)
	if err != nil {
		return fmt.Errorf("read holding: %w", err)
	}
	if held.Sign() <= 0 {
		log.Info("nothing to sell (already flat)", "token", token.Hex(), "reason", reason)
		return nil
	}
	if err := s.ensureApprovalV1(ctx, token, router, held); err != nil {
		return fmt.Errorf("approve before sell: %w", err)
	}

	expOut, err := s.Client.QuoteV1Sell(ctx, token, held)
	if err != nil {
		return fmt.Errorf("quote sell: %w", err)
	}
	minOut := applySlippageDown(expOut, s.SlippageBps)

	nonce, err := s.Client.PendingNonce(ctx, me)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		return fmt.Errorf("gas: %w", err)
	}
	tx, err := s.Signer.BuildV1Sell(token, held, minOut, TxParams{
		Nonce: nonce, GasLimit: sellGasLimit, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		return err
	}
	if err := s.Client.Send(ctx, tx); err != nil {
		return fmt.Errorf("send sell: %w", err)
	}
	log.Info("v1 sell submitted", "token", token.Hex(), "reason", reason, "tx", tx.Hash().Hex(),
		"exp_out_eth", weiToEth(expOut), "min_out_eth", weiToEth(minOut))

	rcpt, err := s.waitReceipt(ctx, tx.Hash(), 90*time.Second)
	if err != nil {
		return fmt.Errorf("sell submitted (%s) but not confirmed: %w", tx.Hash().Hex(), err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("sell tx reverted (%s)", tx.Hash().Hex())
	}
	log.Info("v1 position exited", "token", token.Hex(), "reason", reason, "sell_tx", tx.Hash().Hex())

	s.unwrapWeth(ctx)
	return nil
}

// unwrapWeth converts the wallet's whole WETH balance back to ETH. Best-effort:
// the position is already exited, so a failure only leaves proceeds as WETH.
func (s *SniperV1) unwrapWeth(ctx context.Context) {
	log := s.logger()
	me := s.Signer.Address()
	weth := common.HexToAddress(V1WETH)
	bal, err := s.Client.TokenBalance(ctx, weth, me)
	if err != nil || bal.Sign() <= 0 {
		return
	}
	nonce, err := s.Client.PendingNonce(ctx, me)
	if err != nil {
		log.Warn("weth unwrap skipped", "err", err)
		return
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		log.Warn("weth unwrap skipped", "err", err)
		return
	}
	tx, err := s.Signer.BuildWethWithdraw(bal, TxParams{
		Nonce: nonce, GasLimit: v1WithdrawGasLimit, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		log.Warn("weth unwrap build failed", "err", err)
		return
	}
	if err := s.Client.Send(ctx, tx); err != nil {
		log.Warn("weth unwrap send failed", "err", err)
		return
	}
	if _, err := s.waitReceipt(ctx, tx.Hash(), 60*time.Second); err != nil {
		log.Warn("weth unwrap not confirmed", "tx", tx.Hash().Hex(), "err", err)
		return
	}
	log.Info("weth proceeds unwrapped to ETH", "amount_eth", weiToEth(bal), "tx", tx.Hash().Hex())
}

// ensureApprovalV1 grants the router an allowance covering amount if needed.
func (s *SniperV1) ensureApprovalV1(ctx context.Context, token, spender common.Address, amount *big.Int) error {
	me := s.Signer.Address()
	cur, err := s.Client.Allowance(ctx, token, me, spender)
	if err == nil && cur.Cmp(amount) >= 0 {
		return nil
	}
	nonce, err := s.Client.PendingNonce(ctx, me)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		return fmt.Errorf("gas: %w", err)
	}
	tx, err := s.Signer.BuildApprove(token, spender, maxUint256(), TxParams{
		Nonce: nonce, GasLimit: approveGasLimit, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		return err
	}
	if err := s.Client.Send(ctx, tx); err != nil {
		return fmt.Errorf("send approve: %w", err)
	}
	if _, err := s.waitReceipt(ctx, tx.Hash(), 60*time.Second); err != nil {
		return fmt.Errorf("approve not confirmed: %w", err)
	}
	return nil
}

// waitReceipt polls for a receipt until it appears or the timeout passes.
func (s *SniperV1) waitReceipt(ctx context.Context, hash common.Hash, timeout time.Duration) (*types.Receipt, error) {
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(1 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
			rcpt, err := s.Client.Eth().TransactionReceipt(ctx, hash)
			if err == nil && rcpt != nil {
				return rcpt, nil
			}
			if time.Now().After(deadline) {
				return nil, fmt.Errorf("receipt not found within %s", timeout)
			}
		}
	}
}

// buildV1Filter compiles metadata filters, mirroring the v2 CLI filter.
func BuildV1Filter(matchName, matchSymbol, creator string) func(V1Launch, V1LaunchInfo) bool {
	matchName = strings.ToLower(strings.TrimSpace(matchName))
	matchSymbol = strings.ToLower(strings.TrimSpace(matchSymbol))
	creator = strings.TrimSpace(creator)
	if matchName == "" && matchSymbol == "" && creator == "" {
		return nil
	}
	var creatorAddr common.Address
	if creator != "" {
		creatorAddr = common.HexToAddress(creator)
	}
	return func(l V1Launch, info V1LaunchInfo) bool {
		if matchName != "" && !strings.Contains(strings.ToLower(info.Name), matchName) {
			return false
		}
		if matchSymbol != "" && !strings.Contains(strings.ToLower(info.Symbol), matchSymbol) {
			return false
		}
		if creator != "" && l.Deployer != creatorAddr {
			return false
		}
		return true
	}
}
