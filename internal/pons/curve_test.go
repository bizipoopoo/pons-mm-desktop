package pons

import (
	"math/big"
	"testing"
)

func bi(s string) *big.Int {
	x, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad int: " + s)
	}
	return x
}

func TestTokensOutForQuote(t *testing.T) {
	// Constant-product with equal (large) reserves and no fee: spending 1 unit
	// into (1000,1000) yields 1000*1/1001 = 0 by integer floor, but spending
	// 100 yields 1000*100/1100 = 90.
	q := big.NewInt(1000)
	tok := big.NewInt(1000)
	out := TokensOutForQuote(q, tok, big.NewInt(100), 0, 0)
	if out.Cmp(big.NewInt(90)) != 0 {
		t.Fatalf("tokensOut=%s, want 90", out)
	}

	// A 100% fee leaves nothing to swap.
	if got := TokensOutForQuote(q, tok, big.NewInt(100), 10_000, 0); got.Sign() != 0 {
		t.Fatalf("full-fee tokensOut=%s, want 0", got)
	}

	// Fee reduces output versus the same buy with no fee (large reserves so the
	// difference survives integer flooring).
	bq := bi("30000000000000000000")
	btok := bi("1073000000000000000000000000")
	spend := bi("1000000000000000000") // 1 ETH
	feeOut := TokensOutForQuote(bq, btok, spend, 100, 0)
	noFeeOut := TokensOutForQuote(bq, btok, spend, 0, 0)
	if feeOut.Cmp(noFeeOut) >= 0 {
		t.Fatalf("fee did not reduce output: fee=%s noFee=%s", feeOut, noFeeOut)
	}

	// A larger buy pays a worse average price (more slippage): 2x the spend
	// returns less than 2x the tokens.
	small := TokensOutForQuote(q, tok, big.NewInt(100), 0, 0)
	big2 := TokensOutForQuote(q, tok, big.NewInt(200), 0, 0)
	if new(big.Int).Mul(small, big.NewInt(2)).Cmp(big2) <= 0 {
		t.Fatalf("no price impact: 2x buy=%s, 2*(1x buy)=%s", big2, new(big.Int).Mul(small, big.NewInt(2)))
	}
}

func TestQuoteOutForTokens(t *testing.T) {
	// Round-trip sanity: buying then immediately selling the tokens back into
	// the same reserves recovers less than was spent (fees + price impact).
	q := bi("30000000000000000000")           // 30 ETH phantom-ish reserve
	tok := bi("1073000000000000000000000000") // ~1.073B tokens
	spend := bi("100000000000000000")         // 0.1 ETH

	bought := TokensOutForQuote(q, tok, spend, 100, 0)
	if bought.Sign() <= 0 {
		t.Fatal("bought zero tokens")
	}
	// Reserves after the buy.
	q2 := new(big.Int).Add(q, afterFee(spend, 100))
	tok2 := new(big.Int).Sub(tok, bought)
	back := QuoteOutForTokens(q2, tok2, bought, 100, 0)
	if back.Cmp(spend) >= 0 {
		t.Fatalf("round-trip returned %s >= spent %s (should lose to fees)", back, spend)
	}
}

func TestApplySlippageDown(t *testing.T) {
	if got := applySlippageDown(big.NewInt(10_000), 1500); got.Cmp(big.NewInt(8_500)) != 0 {
		t.Fatalf("15%% slippage of 10000 = %s, want 8500", got)
	}
	if got := applySlippageDown(big.NewInt(10_000), 0); got.Cmp(big.NewInt(10_000)) != 0 {
		t.Fatalf("0 slippage changed value: %s", got)
	}
}

func TestScaleFrac(t *testing.T) {
	x := bi("1000000000000000000") // 1 ETH
	if got := scaleFrac(x, 2); got.Cmp(bi("2000000000000000000")) != 0 {
		t.Fatalf("2x = %s", got)
	}
	if got := scaleFrac(x, 0.5); got.Cmp(bi("500000000000000000")) != 0 {
		t.Fatalf("0.5x = %s", got)
	}
}

func TestEthToWei(t *testing.T) {
	if got := ethToWei(0.1); got.Cmp(bi("100000000000000000")) != 0 {
		t.Fatalf("0.1 ETH = %s wei", got)
	}
	if got := gweiToWei(1); got.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Fatalf("1 gwei = %s wei", got)
	}
	if gweiToWei(0) != nil {
		t.Fatal("0 gwei should be nil (no extra tip)")
	}
}
