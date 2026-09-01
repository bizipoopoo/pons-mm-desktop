package ponsmm

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

func eth(f float64) *big.Int { return ethToWei(f) }

func TestWeiPerWholeToken(t *testing.T) {
	// 10 ETH quote vs 1000 whole tokens -> 0.01 ETH per whole token.
	price := weiPerWholeToken(eth(10), eth(1000))
	want := big.NewFloat(0.01e18)
	if price == nil || new(big.Float).Sub(price, want).Abs(new(big.Float).Sub(price, want)).Cmp(big.NewFloat(1e6)) > 0 {
		t.Fatalf("price = %v, want ~%v", price, want)
	}
}

func TestProjectedPriceAfterSellDropsPrice(t *testing.T) {
	quote, tokens := eth(10), eth(1000)
	before := weiPerWholeToken(quote, tokens)
	after := projectedPriceAfterSell(quote, tokens, eth(100))
	if after.Cmp(before) >= 0 {
		t.Fatalf("selling must lower the price: before %v after %v", before, after)
	}
	// Constant product: selling 100 into (10, 1000) -> gross = 10*100/1100,
	// Q' ≈ 9.0909, T' = 1100, price ≈ 0.008264e18.
	want := big.NewFloat(0.0082644e18)
	diff := new(big.Float).Sub(after, want)
	if diff.Abs(diff).Cmp(big.NewFloat(1e12)) > 0 {
		t.Fatalf("projected price = %v, want ~%v", after, want)
	}
}

func TestQuoteNeededForPriceReachesTarget(t *testing.T) {
	quote, tokens := eth(10), eth(1000)
	target := big.NewFloat(0.0121e18) // +21% over the 0.01 current price
	net := quoteNeededForPrice(quote, tokens, target)
	if net.Sign() <= 0 {
		t.Fatal("a higher target must need a positive buy amount")
	}
	// Verify by simulating the buy with the returned net amount.
	newQuote := new(big.Int).Add(quote, net)
	newTokens := new(big.Int).Div(new(big.Int).Mul(tokens, quote), newQuote)
	got := weiPerWholeToken(newQuote, newTokens)
	ratio := new(big.Float).Quo(got, target)
	lo, hi := big.NewFloat(0.999), big.NewFloat(1.001)
	if ratio.Cmp(lo) < 0 || ratio.Cmp(hi) > 0 {
		t.Fatalf("price after buying the computed amount = %v, want ≈ %v", got, target)
	}
}

func TestQuoteNeededForPriceZeroWhenAlreadyAbove(t *testing.T) {
	if n := quoteNeededForPrice(eth(10), eth(1000), big.NewFloat(0.005e18)); n.Sign() != 0 {
		t.Fatalf("target below current price must need 0, got %v", n)
	}
}

func TestGrossFromNetCoversFee(t *testing.T) {
	net := eth(1)
	gross := grossFromNet(net, 100) // 1% total fee
	back := afterFeeLocal(gross, 100)
	if back.Cmp(net) < 0 {
		t.Fatalf("gross %v after 1%% fee = %v, must cover net %v", gross, back, net)
	}
}

// afterFeeLocal mirrors pons.afterFee for test verification.
func afterFeeLocal(amount *big.Int, feeBps int64) *big.Int {
	out := new(big.Int).Mul(amount, big.NewInt(10_000-feeBps))
	return out.Div(out, big.NewInt(10_000))
}

func TestPlanTargetSellCountAddsWalletsUntilTarget(t *testing.T) {
	quote, tokens := eth(10), eth(1000)
	// Target: push the price down ~10% (0.01 -> 0.009). One 20-token wallet is
	// not enough; it should keep adding wallets.
	target := big.NewFloat(0.009e18)
	balances := []*big.Int{eth(20), eth(20), eth(20), eth(20)}
	count, total, reached := planTargetSellCount(quote, tokens, balances, target)
	if !reached {
		t.Fatalf("four 20-token wallets must reach the -10%% target, got count=%d", count)
	}
	if count < 2 {
		t.Fatalf("a single 20-token wallet cannot push -10%%; count = %d", count)
	}
	if wantTotal := new(big.Int).Mul(eth(20), big.NewInt(int64(count))); total.Cmp(wantTotal) != 0 {
		t.Fatalf("total = %v, want %v for %d whole wallets", total, wantTotal, count)
	}
	// The projected price with the chosen batch must be at or below target,
	// and with one fewer wallet it must still be above target.
	if p := projectedPriceAfterSell(quote, tokens, total); p.Cmp(target) > 0 {
		t.Fatalf("chosen batch does not reach target: %v > %v", p, target)
	}
	prev := new(big.Int).Mul(eth(20), big.NewInt(int64(count-1)))
	if p := projectedPriceAfterSell(quote, tokens, prev); p.Cmp(target) <= 0 {
		t.Fatal("one fewer wallet already reaches target; batch is larger than needed")
	}
}

func TestPlanTargetSellCountUnreachable(t *testing.T) {
	count, total, reached := planTargetSellCount(eth(10), eth(1000), []*big.Int{eth(1)}, big.NewFloat(0.001e18))
	if reached {
		t.Fatal("one tiny wallet cannot push the price down 90%")
	}
	if count != 1 || total.Cmp(eth(1)) != 0 {
		t.Fatalf("unreachable plan must still include every wallet: count=%d total=%v", count, total)
	}
}

func TestMonitorTracksRetailAverageBuyPrice(t *testing.T) {
	m := newTestMonitor(testPool(), eth(1_000_000))
	retailTrade := func(isBuy bool, tokens, weth *big.Int) pons.PoolTrade {
		return pons.PoolTrade{
			IsBuy: isBuy, TokenAmount: tokens, WethAmount: weth,
			Recipient: common.HexToAddress("0x00000000000000000000000000000000000RETAIL"),
		}
	}
	// Two retail buys at different prices: 1 ETH for 100 tokens, 3 ETH for 100
	// tokens -> average 0.02 ETH per token.
	m.onTrade(retailTrade(true, eth(100), eth(1)))
	m.onTrade(retailTrade(true, eth(100), eth(3)))
	snap := m.Snapshot()
	if snap.RetailAvgBuyPx == nil {
		t.Fatal("average buy price missing after retail buys")
	}
	want := big.NewFloat(0.02e18)
	ratio := new(big.Float).Quo(snap.RetailAvgBuyPx, want)
	if ratio.Cmp(big.NewFloat(0.999)) < 0 || ratio.Cmp(big.NewFloat(1.001)) > 0 {
		t.Fatalf("avg = %v, want ~%v", snap.RetailAvgBuyPx, want)
	}
	// Retail selling flat resets the anchor.
	m.onTrade(pons.PoolTrade{
		IsBuy: false, TokenAmount: eth(200), WethAmount: eth(4),
		Recipient: common.HexToAddress("0x00000000000000000000000000000000000RETAIL"),
	})
	if snap := m.Snapshot(); snap.RetailAvgBuyPx != nil {
		t.Fatalf("average must reset once retail is flat, got %v", snap.RetailAvgBuyPx)
	}
}
