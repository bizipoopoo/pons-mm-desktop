package control

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The gate wallet is hardcoded as raw bytes; this pins it to the intended
// address so a typo in the byte literal cannot go unnoticed.
func TestInitGateWalletBytesMatchAddress(t *testing.T) {
	want := common.HexToAddress("0xd439325794932c3ccd45affa85effe5363af1ca8")
	if initGateWallet != want {
		t.Fatalf("initGateWallet = %s, want %s", initGateWallet.Hex(), want.Hex())
	}
}
