package pons

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum/common"
)

// TestLiveReadV2LaunchBindings validates the v2 launch gate, fee, config and
// economics pin against the current factory using eth_call only.
//
//	PUMPWATCH_LIVE=1 go test ./internal/pons -run TestLiveReadV2LaunchBindings -v
func TestLiveReadV2LaunchBindings(t *testing.T) {
	if os.Getenv("PUMPWATCH_LIVE") == "" {
		t.Skip("set PUMPWATCH_LIVE=1 to run the live v2 launch-binding test")
	}
	rpcURL := os.Getenv("PONS_RPC")
	if rpcURL == "" {
		rpcURL = DefaultRPC
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := Dial(ctx, rpcURL)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	probeAddress := os.Getenv("PONS_LAUNCHER")
	if probeAddress == "" {
		probeAddress = "0x000000000000000000000000000000000000dEaD"
	}
	probe := common.HexToAddress(probeAddress)
	can, err := c.CanLaunchV2(ctx, probe)
	if err != nil {
		t.Fatalf("CanLaunchV2: %v", err)
	}
	t.Logf("CanLaunchV2(%s) = %v", probe.Hex(), can)

	fee, err := c.V2LaunchFee(ctx)
	if err != nil {
		t.Fatalf("V2LaunchFee: %v", err)
	}
	if fee.Sign() < 0 {
		t.Fatalf("negative v2 launch fee: %s", fee)
	}

	cfg, err := c.GetV2LaunchConfig(ctx, 0)
	if err != nil {
		t.Fatalf("GetV2LaunchConfig: %v", err)
	}
	if cfg.Supply == nil || cfg.Supply.Sign() <= 0 || cfg.GraduationThreshold == nil || cfg.GraduationThreshold.Sign() <= 0 {
		t.Fatalf("invalid v2 config: %+v", cfg)
	}
	economics, err := c.PreviewV2LaunchEconomics(ctx, 0, common.Address{})
	if err != nil {
		t.Fatalf("PreviewV2LaunchEconomics: %v", err)
	}
	if economics == ([32]byte{}) {
		t.Fatal("preview economics returned zero")
	}
	t.Logf("fee=%s supply=%s graduation=%s enabled=%v economics=%x", fee, cfg.Supply, cfg.GraduationThreshold, cfg.Enabled, economics)
}
