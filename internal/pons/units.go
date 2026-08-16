package pons

import (
	"math/big"
)

var (
	weiPerEth  = new(big.Float).SetFloat64(1e18)
	weiPerGwei = big.NewInt(1_000_000_000)
)

// ethToWei converts a float ETH amount to wei (rounded down).
func ethToWei(eth float64) *big.Int {
	f := new(big.Float).Mul(big.NewFloat(eth), weiPerEth)
	wei, _ := f.Int(nil)
	if wei == nil {
		return big.NewInt(0)
	}
	return wei
}

// gweiToWei converts a float gwei amount to wei (rounded down). Returns nil for
// a non-positive amount so SuggestGas adds no extra tip.
func gweiToWei(gwei float64) *big.Int {
	if gwei <= 0 {
		return nil
	}
	f := new(big.Float).Mul(big.NewFloat(gwei), new(big.Float).SetInt(weiPerGwei))
	wei, _ := f.Int(nil)
	return wei
}

// weiToEth formats wei as an ETH string with 6 decimals.
func weiToEth(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), weiPerEth)
	return f.Text('f', 6)
}

func weiToEthOrDash(wei *big.Int) string {
	if wei == nil {
		return "-"
	}
	return weiToEth(wei)
}

// weiToStr is the plain integer string of a raw amount (token base units).
func weiToStr(x *big.Int) string {
	if x == nil {
		return "0"
	}
	return x.String()
}

// scaleFrac returns round(x * f) for a non-negative float factor, via a
// fixed-point multiply that avoids float precision loss on large wei values.
func scaleFrac(x *big.Int, f float64) *big.Int {
	if x == nil || f <= 0 {
		return big.NewInt(0)
	}
	const scale = 1_000_000
	num := big.NewInt(int64(f*scale + 0.5))
	out := new(big.Int).Mul(x, num)
	return out.Div(out, big.NewInt(scale))
}

// maxUint256 is the ERC-20 "infinite" approval amount.
func maxUint256() *big.Int {
	one := big.NewInt(1)
	return new(big.Int).Sub(new(big.Int).Lsh(one, 256), one)
}
