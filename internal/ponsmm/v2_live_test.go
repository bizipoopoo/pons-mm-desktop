package ponsmm

import (
	"context"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// TestLiveBindV2 is an opt-in, read-only end-to-end bind check for the desktop
// v2 engine. It constructs an in-memory throwaway wallet but sends nothing.
func TestLiveBindV2(t *testing.T) {
	if os.Getenv("PUMPWATCH_LIVE") == "" {
		t.Skip("set PUMPWATCH_LIVE=1 to run the live v2 bind test")
	}
	token, curve := os.Getenv("PONS_TOKEN"), os.Getenv("PONS_CURVE")
	if token == "" || curve == "" {
		t.Skip("set PONS_TOKEN and PONS_CURVE")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := pons.Dial(ctx, pons.DefaultRPC)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	log := slog.New(slog.DiscardHandler)
	pool, err := NewPool(client, []string{"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, log)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Protocol = ProtocolV2
	eng := NewEngine(&cfg, client, pool, log)
	if err := eng.Bind(ctx, common.HexToAddress(token), common.HexToAddress(curve)); err != nil {
		t.Fatalf("Bind v2: %v", err)
	}
	if err := eng.monitor.RefreshReserves(ctx); err != nil {
		t.Fatalf("RefreshReserves: %v", err)
	}
	snap := eng.monitor.Snapshot()
	if snap.PoolTokenReserve.Sign() <= 0 {
		t.Fatalf("bad curve token reserve: %s", snap.PoolTokenReserve)
	}
}
