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

// Gas limits for pons trades. Generous fixed caps avoid an eth_estimateGas
// round-trip on the hot path; unused gas is refunded on an EVM chain.
const (
	buyGasLimit     = 500_000
	sellGasLimit    = 500_000
	approveGasLimit = 80_000
)

// brokeCooldown parks a position slot after a buy failed for lack of funds, so
// the sniper does not burn RPC calls retrying an unfundable wallet.
const brokeCooldown = time.Hour

// Sniper buys new pons launches that pass Filter and, when AutoSell is set,
// flips each one on a profit target, stop-loss, dev dump, or timeout.
type Sniper struct {
	Client *Client
	Signer *Signer
	Log    *slog.Logger

	// BuyETH is the quote spent per snipe, in ETH (the launch's quote asset;
	// only native-ETH launches are sniped).
	BuyETH float64
	// SlippageBps bounds buy/sell price impact (e.g. 1500 = 15%).
	SlippageBps int64
	// PriorityTipGwei is added to the suggested tip so a snipe outbids ordinary
	// transactions in the block.
	PriorityTipGwei float64

	// Filter selects which launches to buy. Nil buys every native-quote launch.
	Filter func(Launch, LaunchInfo) bool

	// MaxSnipeTaxBps is the highest anti-snipe buy tax the sniper will accept.
	// Every pons launch opens with a ~99% buy tax decaying to zero over ~15s
	// (see docs.ponsfamily.com/v2, snipe protection), so buying instantly
	// donates almost the whole spend to the launch. The sniper polls
	// currentSnipeTaxBps and only buys once it is at or below this. 0 means
	// wait for the tax to fully expire; default 100 (1%).
	MaxSnipeTaxBps int64

	// AutoSell hands each confirmed buy to the exit monitor.
	AutoSell bool
	// ProfitTarget is the gain over cost required to take profit (0=break-even,
	// 1=2x). StopLossFrac exits when value falls this fraction below cost
	// (0.5=lost half; 0 disables). FollowDevExit exits when the creator sells
	// this fraction of the holding they had at monitor start (0 disables).
	ProfitTarget  float64
	StopLossFrac  float64
	FollowDevExit float64
	HoldTimeout   time.Duration
	PollInterval  time.Duration

	// DryRun builds and prices but never sends a transaction.
	DryRun bool
	// Limit stops after this many buys (0 = unlimited).
	Limit int
	// Concurrency is the number of parallel snipe→flip position slots.
	Concurrency int
	// Verbose logs every observed launch.
	Verbose bool
}

// Result reports one snipe.
type Result struct {
	Launch    Launch
	Info      LaunchInfo
	BuyTxHash string
	TokensOut *big.Int
	CostWei   *big.Int
	Confirmed bool
	BuildMs   int64
}

func (s *Sniper) logger() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (s *Sniper) concurrency() int {
	if s.Concurrency > 1 {
		return s.Concurrency
	}
	return 1
}

func (s *Sniper) pollInterval() time.Duration {
	if s.PollInterval > 0 {
		return s.PollInterval
	}
	return 15 * time.Second
}

func (s *Sniper) holdTimeout() time.Duration {
	if s.HoldTimeout > 0 {
		return s.HoldTimeout
	}
	return 10 * time.Minute
}

// buyWei is the per-snipe quote spend in wei.
func (s *Sniper) buyWei() *big.Int { return ethToWei(s.BuyETH) }

func (s *Sniper) maxSnipeTaxBps() int64 {
	if s.MaxSnipeTaxBps > 0 {
		return s.MaxSnipeTaxBps
	}
	return 100
}

// Run watches for launches and snipes each match until ctx ends or Limit is
// reached, running up to Concurrency positions in parallel.
func (s *Sniper) Run(ctx context.Context) ([]Result, error) {
	if s.Client == nil || s.Signer == nil {
		return nil, fmt.Errorf("pons snipe: client and signer are required")
	}
	if s.BuyETH <= 0 {
		return nil, fmt.Errorf("pons snipe: buy amount must be > 0")
	}
	log := s.logger()

	conc := s.concurrency()
	slots := make(chan struct{}, conc)
	for i := 0; i < conc; i++ {
		slots <- struct{}{}
	}

	var (
		mu          sync.Mutex
		results     []Result
		inflight    int
		pendingBuys int
		wg          sync.WaitGroup
		posDone     = make(chan struct{}, conc)
	)
	var observed, skipped uint64

	drain := func() []Result {
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

	launches := s.Client.WatchLaunches(ctx, log)
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		if limitReached() {
			log.Info("pons snipe limit reached", "limit", s.Limit)
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
			log.Info("watching for pons launches",
				"observed", observed, "skipped", skipped, "sniped", sniped,
				"active_positions", active, "free_slots", conc-active)
		case l, ok := <-launches:
			if !ok {
				return drain(), ctx.Err()
			}
			observed++

			// Only native-ETH launches are sniped: a custom-pair launch needs
			// the pairing asset held and approved, which a hot snipe cannot set
			// up in time.
			if l.PairToken != (common.Address{}) {
				skipped++
				if s.Verbose {
					log.Debug("skip: custom-pair launch", "token", l.Token.Hex(), "pair", l.PairToken.Hex())
				}
				continue
			}

			info := LaunchInfo{Token: l.Token, Curve: l.Curve, Deployer: l.Deployer, PairToken: l.PairToken}
			infoCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
			err := s.Client.LoadLaunchInfo(infoCtx, &info)
			cancel()
			if err != nil {
				skipped++
				log.Warn("skip: cannot load launch info", "token", l.Token.Hex(), "err", err)
				continue
			}
			if s.Verbose {
				log.Debug("observed launch", "symbol", info.Symbol, "name", info.Name, "token", l.Token.Hex())
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
			go func(l Launch, info LaunchInfo) {
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
						log.Error("pons snipe failed", "token", l.Token.Hex(), "symbol", info.Symbol, "err", err)
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
					if aerr := s.autoExit(ctx, res); aerr != nil && ctx.Err() == nil {
						log.Error("pons auto-sell failed", "token", l.Token.Hex(), "err", aerr)
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

// snipeOne prices and buys a single launch, then waits for the buy to confirm.
func (s *Sniper) snipeOne(ctx context.Context, l Launch, info LaunchInfo) (Result, error) {
	log := s.logger()
	t0 := time.Now()

	// Anti-snipe tax gate: pons opens every launch with a ~99% buy tax that
	// decays to zero over ~15s, so the profitable entry is the earliest moment
	// the residual tax is acceptable — not the first possible block.
	snipeTax, err := s.waitSnipeTaxDecay(ctx, l, info)
	if err != nil {
		return Result{}, err
	}

	quoteIn := s.buyWei()
	quoteReserve, tokenReserve, err := s.Client.Reserves(ctx, l.Curve)
	if err != nil {
		return Result{}, fmt.Errorf("read reserves: %w", err)
	}
	// The residual snipe tax comes off the input exactly like the other fees.
	expTokens := TokensOutForQuote(quoteReserve, tokenReserve, quoteIn, info.FeeBps, info.CreatorTaxBps+snipeTax)
	if expTokens.Sign() <= 0 {
		return Result{}, fmt.Errorf("priced zero tokens out (curve may be graduating)")
	}
	minTokensOut := applySlippageDown(expTokens, s.SlippageBps)

	log.Info("target matched; buying",
		"token", l.Token.Hex(), "symbol", info.Symbol,
		"buy_eth", s.BuyETH, "exp_tokens", weiToStr(expTokens), "min_tokens", weiToStr(minTokensOut),
		"fee_bps", info.FeeBps, "creator_tax_bps", info.CreatorTaxBps, "snipe_tax_bps", snipeTax)

	if s.DryRun {
		log.Info("dry-run: priced buy, not sending", "token", l.Token.Hex(), "build_ms", time.Since(t0).Milliseconds())
		return Result{Launch: l, Info: info, TokensOut: expTokens, BuildMs: time.Since(t0).Milliseconds()}, nil
	}

	tx, err := s.buildBuyTx(ctx, l.Curve, quoteIn, minTokensOut, info.NativeQuote)
	if err != nil {
		return Result{}, err
	}
	buildMs := time.Since(t0).Milliseconds()
	if err := s.Client.Send(ctx, tx); err != nil {
		return Result{}, fmt.Errorf("send buy: %w", err)
	}
	log.Info("buy submitted", "token", l.Token.Hex(), "tx", tx.Hash().Hex(), "build_ms", buildMs)

	rcpt, err := s.waitReceipt(ctx, tx.Hash(), 60*time.Second)
	if err != nil {
		return Result{Launch: l, Info: info, BuyTxHash: tx.Hash().Hex(), BuildMs: buildMs}, nil
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		return Result{}, fmt.Errorf("buy tx reverted (%s)", tx.Hash().Hex())
	}

	// Read the exact holding and total cost (quote spent + gas).
	held, err := s.Client.TokenBalance(ctx, l.Token, s.Signer.Address())
	if err != nil || held.Sign() <= 0 {
		return Result{}, fmt.Errorf("buy confirmed but holding not found: %v", err)
	}
	gasCost := new(big.Int).Mul(new(big.Int).SetUint64(rcpt.GasUsed), rcpt.EffectiveGasPrice)
	cost := new(big.Int).Add(quoteIn, gasCost)

	log.Info("buy confirmed",
		"token", l.Token.Hex(), "tokens", weiToStr(held), "cost_eth", weiToEth(cost),
		"total_ms", time.Since(t0).Milliseconds())
	return Result{
		Launch: l, Info: info, BuyTxHash: tx.Hash().Hex(),
		TokensOut: held, CostWei: cost, Confirmed: true, BuildMs: buildMs,
	}, nil
}

// waitSnipeTaxDecay blocks until the launch's anti-snipe buy tax has decayed to
// at most maxSnipeTaxBps, and returns the residual tax to include in pricing.
// The tax starts near 9900 bps and reaches zero within ~15s of launch, so this
// typically waits a dozen seconds when we detect a launch instantly, and not at
// all when the launch is older than the window.
func (s *Sniper) waitSnipeTaxDecay(ctx context.Context, l Launch, info LaunchInfo) (int64, error) {
	log := s.logger()
	recipient := s.Signer.Address()
	limit := s.maxSnipeTaxBps()
	deadline := time.Now().Add(60 * time.Second)

	first := true
	for {
		tax, err := s.Client.SnipeTaxBps(ctx, l.Curve, recipient)
		if err != nil {
			return 0, fmt.Errorf("read snipe tax: %w", err)
		}
		if tax <= limit {
			if !first {
				log.Info("snipe tax decayed; proceeding",
					"token", l.Token.Hex(), "symbol", info.Symbol, "residual_tax_bps", tax)
			}
			return tax, nil
		}
		if first {
			log.Info("waiting out anti-snipe buy tax",
				"token", l.Token.Hex(), "symbol", info.Symbol,
				"current_tax_bps", tax, "buy_at_bps", limit)
			first = false
		}
		if time.Now().After(deadline) {
			return 0, fmt.Errorf("snipe tax still %d bps after 60s (limit %d)", tax, limit)
		}
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func (s *Sniper) buildBuyTx(ctx context.Context, curve common.Address, quoteIn, minTokensOut *big.Int, native bool) (*types.Transaction, error) {
	nonce, err := s.Client.PendingNonce(ctx, s.Signer.Address())
	if err != nil {
		return nil, fmt.Errorf("nonce: %w", err)
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		return nil, fmt.Errorf("gas: %w", err)
	}
	return s.Signer.BuildBuy(curve, quoteIn, minTokensOut, native, TxParams{
		Nonce: nonce, GasLimit: buyGasLimit, TipCap: tip, FeeCap: feeCap,
	})
}

// waitReceipt polls for a transaction receipt until it appears or ctx/timeout.
func (s *Sniper) waitReceipt(ctx context.Context, hash common.Hash, timeout time.Duration) (*types.Receipt, error) {
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

// isInsufficientFunds reports whether a buy failure means the wallet cannot fund
// the purchase, so its slot should be parked rather than retried.
func isInsufficientFunds(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	return strings.Contains(s, "insufficient funds") ||
		strings.Contains(s, "insufficient balance") ||
		strings.Contains(s, "gas required exceeds")
}
