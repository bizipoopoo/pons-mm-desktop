package ponsmm

import (
	"fmt"
	"math/big"
)

// sellSlippageBps deliberately leaves only 0.01% of the latest quote as
// amountOutMinimum. Concurrent liquidation wallets all quote before earlier
// sells move the curve, so near-unbounded tolerance keeps the batch moving.
const sellSlippageBps int64 = 9_999

// Distribution batches fully clear 4-6 wallets per round when the position
// cannot yet be exited at a profit that covers every fee paid.
const (
	distributionBatchMin = 4
	distributionBatchMax = 6
)

// retailBuyResponse decides how to react to every retail buy. totalCost must
// include everything the strategy paid so far (net buy ETH, gas, priority
// tips, launch fee). When the full exit quote beats that total, the engine
// clears everything concurrently; otherwise it distributes in small wallet
// batches until either the retail position exits or nothing is left.
func retailBuyResponse(sellAllProceeds, totalCost *big.Int, costBasisKnown bool) State {
	if sellAllProceeds == nil {
		sellAllProceeds = big.NewInt(0)
	}
	if totalCost == nil {
		totalCost = big.NewInt(0)
	}
	if costBasisKnown && sellAllProceeds.Cmp(totalCost) > 0 {
		return ClearAll
	}
	return Distributing
}

// applySlippage returns amount * (10000 - slippageBps) / 10000, the minimum
// acceptable output for a swap.
func applySlippage(amount *big.Int, slippageBps int64) *big.Int {
	if amount == nil {
		return big.NewInt(0)
	}
	num := big.NewInt(10_000 - slippageBps)
	if num.Sign() < 0 {
		num.SetInt64(0)
	}
	return new(big.Int).Div(new(big.Int).Mul(amount, num), big.NewInt(10_000))
}

func applySellSlippage(amount *big.Int) *big.Int {
	return applySlippage(amount, sellSlippageBps)
}

// scaleWei multiplies a wei amount by a fraction in [0,1], rounding down.
func scaleWei(amount *big.Int, frac float64) *big.Int {
	if amount == nil || frac <= 0 {
		return big.NewInt(0)
	}
	if frac >= 1 {
		return new(big.Int).Set(amount)
	}
	f := new(big.Float).Mul(new(big.Float).SetInt(amount), big.NewFloat(frac))
	out, _ := f.Int(nil)
	return out
}

// gweiToWei converts a float gwei to wei; returns nil for <= 0 (no extra tip).
func gweiToWei(gwei float64) *big.Int {
	if gwei <= 0 {
		return nil
	}
	f := new(big.Float).Mul(big.NewFloat(gwei), big.NewFloat(1e9))
	out, _ := f.Int(nil)
	return out
}

// maxUint256 is the ERC-20 "infinite" approval amount.
func maxUint256() *big.Int {
	one := big.NewInt(1)
	return new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
}

// ETHToWei is the exported form of ethToWei for CLI callers.
func ETHToWei(eth float64) *big.Int { return ethToWei(eth) }

// GweiToWei is the exported form of gweiToWei for CLI callers.
func GweiToWei(gwei float64) *big.Int { return gweiToWei(gwei) }

// weiToEthStr formats wei as an ETH string with 6 decimals.
func weiToEthStr(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))
	return fmt.Sprintf("%.6f", f)
}
