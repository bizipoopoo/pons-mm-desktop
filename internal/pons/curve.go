package pons

import "math/big"

// The pons bonding curve is a constant-product market (x*y=k) between a quote
// reserve (which includes a non-withdrawable phantom amount that sets the
// opening price) and the token reserve. These helpers price a buy/sell locally
// off getReserves() so the hot path needs no quoter round-trip; the on-chain
// call still enforces the exact result via minTokensOut/minQuoteOut.

const bpsDenom = 10_000

// TokensOutForQuote returns the launch tokens received for spending quoteIn of
// the quote asset, net of the total fee (base fee + creator tax, both in bps
// and charged on the quote leg). All amounts are raw (wei / token base units).
func TokensOutForQuote(quoteReserve, tokenReserve, quoteIn *big.Int, feeBps, creatorTaxBps int64) *big.Int {
	if sign(quoteReserve) <= 0 || sign(tokenReserve) <= 0 || sign(quoteIn) <= 0 {
		return big.NewInt(0)
	}
	net := afterFee(quoteIn, feeBps+creatorTaxBps)
	// tokensOut = tokenReserve * net / (quoteReserve + net)
	num := new(big.Int).Mul(tokenReserve, net)
	den := new(big.Int).Add(quoteReserve, net)
	return num.Div(num, den)
}

// QuoteOutForTokens returns the quote asset received for selling tokensIn launch
// tokens, net of the total fee charged on the received quote. Amounts are raw.
func QuoteOutForTokens(quoteReserve, tokenReserve, tokensIn *big.Int, feeBps, creatorTaxBps int64) *big.Int {
	if sign(quoteReserve) <= 0 || sign(tokenReserve) <= 0 || sign(tokensIn) <= 0 {
		return big.NewInt(0)
	}
	// gross = quoteReserve * tokensIn / (tokenReserve + tokensIn)
	num := new(big.Int).Mul(quoteReserve, tokensIn)
	den := new(big.Int).Add(tokenReserve, tokensIn)
	gross := num.Div(num, den)
	return afterFee(gross, feeBps+creatorTaxBps)
}

// afterFee returns amount * (bpsDenom - feeBps) / bpsDenom, clamped at zero for
// a fee at or above 100%.
func afterFee(amount *big.Int, feeBps int64) *big.Int {
	if feeBps <= 0 {
		return new(big.Int).Set(amount)
	}
	if feeBps >= bpsDenom {
		return big.NewInt(0)
	}
	out := new(big.Int).Mul(amount, big.NewInt(bpsDenom-feeBps))
	return out.Div(out, big.NewInt(bpsDenom))
}

// applySlippageDown returns amount * (10000 - slippageBps) / 10000, the minimum
// acceptable output to pass as minTokensOut / minQuoteOut.
func applySlippageDown(amount *big.Int, slippageBps int64) *big.Int {
	if slippageBps <= 0 {
		return new(big.Int).Set(amount)
	}
	if slippageBps >= bpsDenom {
		return big.NewInt(0)
	}
	out := new(big.Int).Mul(amount, big.NewInt(bpsDenom-slippageBps))
	return out.Div(out, big.NewInt(bpsDenom))
}

func sign(x *big.Int) int {
	if x == nil {
		return 0
	}
	return x.Sign()
}
