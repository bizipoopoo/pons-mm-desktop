package pons

import (
	"context"
	"math/big"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestLiveReadLaunch is an opt-in read-only check against a real pons launch on
// Robinhood Chain. It loads the immutable launch facts, reads live reserves,
// and prices a buy — exercising the exact decode/pricing path the sniper uses,
// without sending a transaction.
//
//	PUMPWATCH_LIVE=1 PONS_CURVE=0x... PONS_TOKEN=0x... go test ./internal/pons -run TestLiveReadLaunch -v
func TestLiveReadLaunch(t *testing.T) {
	if os.Getenv("PUMPWATCH_LIVE") == "" {
		t.Skip("set PUMPWATCH_LIVE=1 to run the live read test")
	}
	curve := os.Getenv("PONS_CURVE")
	token := os.Getenv("PONS_TOKEN")
	if curve == "" || token == "" {
		t.Skip("set PONS_CURVE and PONS_TOKEN to a live launch")
	}
	rpc := os.Getenv("PONS_RPC")
	if rpc == "" {
		rpc = DefaultRPC
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	c, err := Dial(ctx, rpc)
	if err != nil {
		t.Fatal(err)
	}
	if c.ChainID().Cmp(big.NewInt(RobinhoodChainID)) != 0 {
		t.Fatalf("chain id %v, want %d", c.ChainID(), RobinhoodChainID)
	}

	info := LaunchInfo{Token: common.HexToAddress(token), Curve: common.HexToAddress(curve)}
	if err := c.LoadLaunchInfo(ctx, &info); err != nil {
		t.Fatalf("LoadLaunchInfo: %v", err)
	}
	t.Logf("symbol=%q name=%q decimals=%d feeBps=%d creatorTaxBps=%d native=%v",
		info.Symbol, info.Name, info.Decimals, info.FeeBps, info.CreatorTaxBps, info.NativeQuote)

	// The anti-snipe buy tax must be readable; it is 0 once the launch is more
	// than ~15s old, and up to ~9900 in the opening seconds.
	tax, err := c.SnipeTaxBps(ctx, info.Curve, common.HexToAddress("0xaE2E39813C345Ae8EeaeA15dDD989A890da6d3e2"))
	if err != nil {
		t.Fatalf("SnipeTaxBps: %v", err)
	}
	if tax < 0 || tax > 9990 {
		t.Fatalf("snipe tax out of range: %d bps", tax)
	}
	t.Logf("currentSnipeTaxBps=%d", tax)

	graduated, err := c.Graduated(ctx, info.Curve)
	if err != nil {
		t.Fatalf("Graduated: %v", err)
	}
	t.Logf("graduated=%v", graduated)
	if graduated {
		t.Skip("launch already graduated; reserves are not meaningful")
	}

	qr, tr, err := c.Reserves(ctx, info.Curve)
	if err != nil {
		t.Fatalf("Reserves: %v", err)
	}
	if qr.Sign() <= 0 || tr.Sign() <= 0 {
		t.Fatalf("bad reserves: quote=%s token=%s", qr, tr)
	}
	spend := ethToWei(0.01)
	out := TokensOutForQuote(qr, tr, spend, info.FeeBps, info.CreatorTaxBps)
	if out.Sign() <= 0 {
		t.Fatalf("priced zero tokens out for 0.01 quote (reserves quote=%s token=%s)", qr, tr)
	}
	t.Logf("quoteReserve=%s tokenReserve=%s  ->  0.01 quote buys %s token base units", qr, tr, out)

	// Round-trip: selling those tokens straight back returns less (fees + impact).
	back := QuoteOutForTokens(qr, tr, out, info.FeeBps, info.CreatorTaxBps)
	t.Logf("immediate round-trip value: %s quote base units (spent %s)", back, spend)
	if back.Cmp(spend) >= 0 {
		t.Fatalf("round-trip returned >= spent, fee math is wrong")
	}
}
