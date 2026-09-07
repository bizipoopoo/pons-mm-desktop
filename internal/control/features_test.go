package control

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/ponsmm"
)

// The gate wallet is hardcoded as raw bytes; this pins it to the intended
// address so a typo in the byte literal cannot go unnoticed.
func TestInitGateWalletBytesMatchAddress(t *testing.T) {
	want := common.HexToAddress("0xd439325794932c3ccd45affa85effe5363af1ca8")
	if initGateWallet != want {
		t.Fatalf("initGateWallet = %s, want %s", initGateWallet.Hex(), want.Hex())
	}
}

func TestJobStatsFromMarkToMarket(t *testing.T) {
	st := ponsmm.Stats{
		StartBalanceWei:    new(big.Int).SetUint64(1_000_000_000_000_000_000),
		MarkBalanceWei:     new(big.Int).SetUint64(850_000_000_000_000_000),
		EstimatedProfitWei: big.NewInt(-150_000_000_000_000_000),
	}
	got := jobStatsFrom(st)
	if !strings.HasPrefix(got.StartBalance, "1.") {
		t.Fatalf("start = %q", got.StartBalance)
	}
	if got.Profit != "" {
		t.Fatalf("realized profit should stay empty while running, got %q", got.Profit)
	}
	if !strings.HasPrefix(got.EstimatedProfit, "-0.15") {
		t.Fatalf("estimated = %q", got.EstimatedProfit)
	}
}
