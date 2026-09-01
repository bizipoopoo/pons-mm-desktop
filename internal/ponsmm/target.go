package ponsmm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"sync"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// The price-target retail response ("target" mode). When a retail buy arrives
// and the full exit is not yet profitable, RetailTargetRatio decides the
// action relative to the retail buyers' volume-weighted average buy price:
//
//	ratio == 0  do nothing;
//	ratio  < 0  sell whole wallets, adding one at a time, until the projected
//	            curve price is pushed to avg*(1+ratio), then execute the batch;
//	ratio  > 0  buy exactly enough to lift the curve price to avg*(1+ratio).
//
// All projections use the v2 constant-product curve locally off the cached
// reserves; on-chain slippage bounds still police the actual fills.

var oneWholeToken = big.NewFloat(1e18)

// weiPerWholeToken is the marginal curve price quoteReserve/tokenReserve
// scaled to wei per whole (1e18 base units) token.
func weiPerWholeToken(quoteReserve, tokenReserve *big.Int) *big.Float {
	if quoteReserve == nil || tokenReserve == nil || tokenReserve.Sign() <= 0 {
		return nil
	}
	price := new(big.Float).Quo(new(big.Float).SetInt(quoteReserve), new(big.Float).SetInt(tokenReserve))
	return price.Mul(price, oneWholeToken)
}

// projectedPriceAfterSell returns the marginal price after tokensIn are sold
// into the curve. The swap fee is skimmed from the quote leaving the curve, so
// the reserve loses the gross constant-product output.
func projectedPriceAfterSell(quoteReserve, tokenReserve, tokensIn *big.Int) *big.Float {
	if tokensIn == nil || tokensIn.Sign() <= 0 {
		return weiPerWholeToken(quoteReserve, tokenReserve)
	}
	newTokens := new(big.Int).Add(tokenReserve, tokensIn)
	gross := new(big.Int).Mul(quoteReserve, tokensIn)
	gross.Div(gross, newTokens)
	newQuote := new(big.Int).Sub(quoteReserve, gross)
	return weiPerWholeToken(newQuote, newTokens)
}

// quoteNeededForPrice returns the quote amount that must enter the curve
// (after fees) to lift the marginal price to target wei-per-whole-token:
// price' = (Q+net)^2 * 1e18 / (Q*T)  =>  net = sqrt(target*Q*T/1e18) - Q.
// Returns zero when the price is already at or above target.
func quoteNeededForPrice(quoteReserve, tokenReserve *big.Int, target *big.Float) *big.Int {
	if quoteReserve == nil || tokenReserve == nil || target == nil ||
		quoteReserve.Sign() <= 0 || tokenReserve.Sign() <= 0 || target.Sign() <= 0 {
		return big.NewInt(0)
	}
	square := new(big.Float).Mul(new(big.Float).SetInt(quoteReserve), new(big.Float).SetInt(tokenReserve))
	square.Mul(square, target)
	square.Quo(square, oneWholeToken)
	net := square.Sqrt(square)
	net.Sub(net, new(big.Float).SetInt(quoteReserve))
	if net.Sign() <= 0 {
		return big.NewInt(0)
	}
	out, _ := net.Int(nil)
	return out
}

// grossFromNet converts a required in-curve amount to the amount the buyer
// must pay before the curve fee and creator tax are taken off the quote leg.
func grossFromNet(net *big.Int, totalFeeBps int64) *big.Int {
	if net == nil || net.Sign() <= 0 {
		return big.NewInt(0)
	}
	if totalFeeBps <= 0 {
		return new(big.Int).Set(net)
	}
	if totalFeeBps >= 10_000 {
		return big.NewInt(0)
	}
	// Ceiling division: rounding down would leave the post-fee amount 1 wei
	// short of the required net input.
	den := big.NewInt(10_000 - totalFeeBps)
	out := new(big.Int).Mul(net, big.NewInt(10_000))
	out.Add(out, new(big.Int).Sub(den, big.NewInt(1)))
	return out.Div(out, den)
}

// targetPlan is one prepared response batch.
type targetPlan struct {
	sell    bool
	wallets []*Wallet
	// Sell plans: aggregate tokens and their quote for sellWalletsFast pricing.
	totalTokens *big.Int
	quote       *big.Int
	// Buy plans: per-wallet spends aligned with wallets.
	spends []*big.Int
}

// planTargetSellCount walks balances in order, accumulating whole wallets
// until selling the running total is projected to push the price to target.
// reached is false when even the full set cannot get there.
func planTargetSellCount(quoteReserve, tokenReserve *big.Int, balances []*big.Int, target *big.Float) (count int, total *big.Int, reached bool) {
	total = big.NewInt(0)
	for i, bal := range balances {
		if bal == nil || bal.Sign() <= 0 {
			continue
		}
		total.Add(total, bal)
		count = i + 1
		price := projectedPriceAfterSell(quoteReserve, tokenReserve, total)
		if price != nil && price.Cmp(target) <= 0 {
			return count, total, true
		}
	}
	return count, total, false
}

// startTargetResponse plans and asynchronously executes one target-mode
// response. Reuses the distribution round guard so overlapping retail buys
// cannot double-execute; the running batch reports back through
// distributionResults like a distribution batch.
func (e *Engine) startTargetResponse(ctx context.Context, snap Snapshot) {
	if e.distributionRoundRunning {
		e.log.Info("target response already running; retail buy noted")
		return
	}
	plan := e.planTargetResponse(ctx, snap)
	if plan == nil {
		return
	}
	if e.distributionResults == nil {
		return // unit-constructed engines
	}
	e.distributionRoundRunning = true
	go func() {
		err := e.execTargetResponse(ctx, plan)
		e.distributionResults <- distributionResult{err: err}
	}()
}

func (e *Engine) planTargetResponse(ctx context.Context, snap Snapshot) *targetPlan {
	ratio := e.cfg.RetailTargetRatio
	if ratio == 0 {
		e.log.Info("target response: ratio 0 -> no action on unprofitable retail buy")
		return nil
	}
	avg := snap.RetailAvgBuyPx
	if avg == nil || avg.Sign() <= 0 {
		e.log.Warn("target response skipped: no retail average buy price yet")
		return nil
	}
	quoteReserve, tokenReserve, err := e.curveReserves(ctx)
	if err != nil {
		e.log.Warn("target response skipped: curve reserves unavailable", "err", err)
		return nil
	}
	target := new(big.Float).Mul(avg, big.NewFloat(1+ratio))
	current := weiPerWholeToken(quoteReserve, tokenReserve)
	if current == nil {
		return nil
	}
	feeBps := e.v2Info.FeeBps + e.v2Info.CreatorTaxBps

	if ratio < 0 {
		if current.Cmp(target) <= 0 {
			e.log.Info("target response: price already at or below the sell target",
				"price", current.String(), "target", target.String())
			return nil
		}
		var wallets []*Wallet
		var balances []*big.Int
		for _, w := range e.pool.All() {
			if bal := w.tokenBalance(); bal.Sign() > 0 {
				wallets = append(wallets, w)
				balances = append(balances, bal)
			}
		}
		if len(wallets) == 0 {
			e.log.Warn("target response: no wallet holds tokens to sell")
			return nil
		}
		count, total, reached := planTargetSellCount(quoteReserve, tokenReserve, balances, target)
		if !reached {
			e.log.Warn("target response: selling every held wallet still cannot reach the sell target; executing the full batch",
				"wallets", count, "target", target.String())
		}
		quote := pons.QuoteOutForTokens(quoteReserve, tokenReserve, total, e.v2Info.FeeBps, e.v2Info.CreatorTaxBps)
		e.log.Info("target response: sell batch planned",
			"wallets", count, "tokens", total.String(),
			"retail_avg_px", avg.String(), "target_px", target.String(), "current_px", current.String(),
			"quote_eth", weiToEthStr(quote))
		return &targetPlan{sell: true, wallets: wallets[:count], totalTokens: total, quote: quote}
	}

	if current.Cmp(target) >= 0 {
		e.log.Info("target response: price already at or above the buy target",
			"price", current.String(), "target", target.String())
		return nil
	}
	needNet := quoteNeededForPrice(quoteReserve, tokenReserve, target)
	needGross := grossFromNet(needNet, feeBps)
	if needGross.Sign() <= 0 {
		return nil
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	remaining := new(big.Int).Set(needGross)
	var wallets []*Wallet
	var spends []*big.Int
	for _, w := range e.buyEligibleMakers() {
		spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction)
		if spend.Sign() <= 0 {
			continue
		}
		if spend.Cmp(remaining) > 0 {
			spend = new(big.Int).Set(remaining) // cap the last wallet: no overshoot
		}
		wallets = append(wallets, w)
		spends = append(spends, spend)
		remaining.Sub(remaining, spend)
		if remaining.Sign() <= 0 {
			break
		}
	}
	if len(wallets) == 0 {
		e.log.Warn("target response: no eligible maker has spendable ETH for the buy support")
		return nil
	}
	if remaining.Sign() > 0 {
		e.log.Warn("target response: maker funds cover the buy target only partially",
			"needed_eth", weiToEthStr(needGross), "short_eth", weiToEthStr(remaining))
	}
	e.log.Info("target response: buy support planned",
		"wallets", len(wallets), "spend_eth", weiToEthStr(new(big.Int).Sub(needGross, remaining)),
		"retail_avg_px", avg.String(), "target_px", target.String(), "current_px", current.String())
	return &targetPlan{sell: false, wallets: wallets, spends: spends}
}

func (e *Engine) execTargetResponse(ctx context.Context, plan *targetPlan) error {
	if plan.sell {
		return e.sellWalletsFast(ctx, plan.wallets, plan.quote, plan.totalTokens, e.cfg.ConcurrentSells)
	}
	var mu sync.Mutex
	var buyErrors []error
	var wg sync.WaitGroup
	for i := range plan.wallets {
		wg.Add(1)
		go func(w *Wallet, spend *big.Int) {
			defer wg.Done()
			if err := e.buyOnce(ctx, ctx, w, spend); err != nil && !errors.Is(err, context.Canceled) {
				mu.Lock()
				buyErrors = append(buyErrors, fmt.Errorf("wallet %s: %w", w.Addr.Hex(), err))
				mu.Unlock()
			}
		}(plan.wallets[i], plan.spends[i])
	}
	wg.Wait()
	return errors.Join(buyErrors...)
}
