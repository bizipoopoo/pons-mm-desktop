package ponsmm

import (
	"fmt"
	"math/big"
)

// Oscillation actions.
const (
	actionHold = iota
	actionBuy
	actionSell
)

// retailBuyResponse decides how to react to a retail buy while we hold a LOW
// position (below high_hold). If liquidating our whole position right now at
// least breaks even, clear it all; otherwise sell slowly.
//
// holdFrac/highHold are passed for symmetry and future tuning; the low-holding
// branch has already been selected by the caller.
func retailBuyResponse(holdFrac, highHold float64, sellAllProceeds, ourCost *big.Int) State {
	if sellAllProceeds == nil {
		sellAllProceeds = big.NewInt(0)
	}
	if ourCost == nil {
		ourCost = big.NewInt(0)
	}
	if sellAllProceeds.Cmp(ourCost) >= 0 {
		return ClearAll
	}
	return Distributing
}

// oscillationAction keeps the market price under the retail cost anchor. The
// band spans [anchor*(1-band), anchor]: above the anchor we sell to push price
// down, below the lower edge we buy to lift it, in between we hold.
func oscillationAction(price, retailCost *big.Float, band float64) int {
	if price == nil || retailCost == nil || retailCost.Sign() <= 0 {
		return actionHold
	}
	upper := new(big.Float).Set(retailCost)
	lower := new(big.Float).Mul(retailCost, big.NewFloat(1-band))
	if price.Cmp(upper) > 0 {
		return actionSell
	}
	if price.Cmp(lower) < 0 {
		return actionBuy
	}
	return actionHold
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
