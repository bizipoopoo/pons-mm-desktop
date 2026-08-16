package pons

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// packSwap builds a synthetic Uniswap V3 Swap log for the pool ABI. amount0/
// amount1 are signed (positive into pool, negative out of pool).
func packSwap(t *testing.T, sender, recipient common.Address, amount0, amount1 *big.Int) types.Log {
	t.Helper()
	ev := v1PoolABI.Events["Swap"]
	data, err := ev.Inputs.NonIndexed().Pack(amount0, amount1, big.NewInt(1), big.NewInt(1), big.NewInt(0))
	if err != nil {
		t.Fatalf("pack swap data: %v", err)
	}
	return types.Log{
		Topics: []common.Hash{
			v1SwapTopic,
			common.BytesToHash(sender.Bytes()),
			common.BytesToHash(recipient.Bytes()),
		},
		Data: data,
	}
}

func TestDecodePoolTradeBuyTokenIsToken1(t *testing.T) {
	sender := common.HexToAddress("0xRouter")
	buyer := common.HexToAddress("0xBuyer")
	// token = token1, weth = token0. A buy: weth in (+100), token out (-500).
	lg := packSwap(t, sender, buyer, big.NewInt(100), big.NewInt(-500))
	tr, ok := decodePoolTrade(lg, false)
	if !ok {
		t.Fatal("decode failed")
	}
	if !tr.IsBuy {
		t.Fatal("expected buy (token left the pool)")
	}
	if tr.TokenAmount.Int64() != 500 || tr.WethAmount.Int64() != 100 {
		t.Fatalf("amounts token=%d weth=%d, want 500/100", tr.TokenAmount.Int64(), tr.WethAmount.Int64())
	}
	if tr.Recipient != buyer {
		t.Fatalf("recipient = %s, want %s", tr.Recipient.Hex(), buyer.Hex())
	}
}

func TestDecodePoolTradeSellTokenIsToken0(t *testing.T) {
	sender := common.HexToAddress("0xRouter")
	seller := common.HexToAddress("0xSeller")
	// token = token0, weth = token1. A sell: token in (+500), weth out (-90).
	lg := packSwap(t, sender, seller, big.NewInt(500), big.NewInt(-90))
	tr, ok := decodePoolTrade(lg, true)
	if !ok {
		t.Fatal("decode failed")
	}
	if tr.IsBuy {
		t.Fatal("expected sell (token entered the pool)")
	}
	if tr.TokenAmount.Int64() != 500 || tr.WethAmount.Int64() != 90 {
		t.Fatalf("amounts token=%d weth=%d, want 500/90", tr.TokenAmount.Int64(), tr.WethAmount.Int64())
	}
}

func TestDecodePoolTradeIgnoresNonSwap(t *testing.T) {
	lg := types.Log{Topics: []common.Hash{common.HexToHash("0xnotaswap")}}
	if _, ok := decodePoolTrade(lg, false); ok {
		t.Fatal("non-Swap log must be ignored")
	}
}
