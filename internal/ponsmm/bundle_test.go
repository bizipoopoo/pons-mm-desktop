package ponsmm

import (
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

func bundleConfig() Config {
	c := DefaultConfig()
	c.RPCEndpoint = "wss://example"
	c.Token.Name, c.Token.Symbol = "T", "T"
	c.BundleBuys = true
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

	// Bundling off: the window and router are ignored even when malformed.
	c = bundleConfig()
	c.BundleBuys = false
	c.MMRouter = "junk"
	c.BundleMaxBlocks = 999
	if err := c.Validate(true); err != nil {
		t.Fatalf("disabled bundle must not validate its parameters: %v", err)
	}
}
