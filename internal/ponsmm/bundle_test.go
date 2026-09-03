package ponsmm

import (
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

func bundleConfig() Config {
	c := DefaultConfig()
	c.RPCEndpoint = "wss://example"
	c.Token.Name, c.Token.Symbol = "T", "T"
	c.BundleMode = BundleWindow
	return c
}

func TestBundleConfigValidation(t *testing.T) {
	c := bundleConfig()
	if err := c.Validate(true); err != nil {
		t.Fatalf("default bundle config should validate: %v", err)
	}
	if c.BundleMaxBlocksOrDefault() != DefaultBundleMaxBlocks {
		t.Fatalf("default window = %d", c.BundleMaxBlocksOrDefault())
	}
	if c.MMRouterAddr() != common.HexToAddress(pons.MMRouter) {
		t.Fatalf("empty mm_router should resolve to the built-in deployment")
	}

	c = bundleConfig()
	c.Protocol = ProtocolV1
	if err := c.Validate(true); err == nil || !strings.Contains(err.Error(), "v2") {
		t.Fatalf("v1 bundle should be rejected, got %v", err)
	}

	c = bundleConfig()
	c.MMRouter = "not-an-address"
	if err := c.Validate(true); err == nil || !strings.Contains(err.Error(), "mm_router") {
		t.Fatalf("bad router should be rejected, got %v", err)
	}

	c = bundleConfig()
	c.MMRouter = "0x00000000000000000000000000000000000000AA"
	if c.MMRouterAddr() != common.HexToAddress("0x00000000000000000000000000000000000000AA") {
		t.Fatal("override router not applied")
	}

	c = bundleConfig()
	c.BundleMaxBlocks = MaxBundleMaxBlocks + 1
	if err := c.Validate(true); err == nil || !strings.Contains(err.Error(), "bundle_max_blocks") {
		t.Fatalf("oversized window should be rejected, got %v", err)
	}

	// Atomic mode has no window: an out-of-range value is irrelevant.
	c = bundleConfig()
	c.BundleMode = BundleAtomic
	c.BundleMaxBlocks = 999
	if err := c.Validate(true); err != nil {
		t.Fatalf("atomic mode must ignore the window: %v", err)
	}

	c = bundleConfig()
	c.BundleMode = "sometimes"
	if err := c.Validate(true); err == nil || !strings.Contains(err.Error(), "bundle_mode") {
		t.Fatalf("unknown mode should be rejected, got %v", err)
	}

	// Bundling off (empty or "none"): the window and router are ignored even
	// when malformed.
	for _, off := range []string{"", "none", "off"} {
		c = bundleConfig()
		c.BundleMode = off
		c.MMRouter = "junk"
		c.BundleMaxBlocks = 999
		if err := c.Validate(true); err != nil {
			t.Fatalf("disabled bundle %q must not validate its parameters: %v", off, err)
		}
		if c.Bundling() {
			t.Fatalf("%q should mean off", off)
		}
	}
}

func TestBundleBuySpendReservesGas(t *testing.T) {
	gasReserve := ethToWei(0.002)
	feeCap := big.NewInt(3_000_000_000) // 3 gwei
	w := &Wallet{ETHWei: ethToWei(0.005)}

	spend := bundleBuySpend(w, gasReserve, feeCap, bundleGasLimit, 1)
	// 0.005 - 0.002 gas reserve - 400k*3gwei gas = 0.0018 ETH
	want := ethToWei(0.0018)
	if spend.Cmp(want) != 0 {
		t.Fatalf("spend = %s want %s", weiToEthStr(spend), weiToEthStr(want))
	}
	gasCost := new(big.Int).Mul(feeCap, new(big.Int).SetUint64(bundleGasLimit))
	total := new(big.Int).Add(spend, gasCost)
	if total.Cmp(w.ETHWei) > 0 {
		t.Fatalf("value+gas %s exceeds balance %s", weiToEthStr(total), weiToEthStr(w.ETHWei))
	}

	// Tight wallet: only gas reserve left after the tx's own gas.
	w.ETHWei = ethToWei(0.0032)
	if got := bundleBuySpend(w, gasReserve, feeCap, bundleGasLimit, 1); got.Sign() != 0 {
		t.Fatalf("expected zero spend on tight wallet, got %s", weiToEthStr(got))
	}
}
