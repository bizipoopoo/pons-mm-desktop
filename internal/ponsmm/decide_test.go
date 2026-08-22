package ponsmm

import (
	"math/big"
	"testing"
	"time"
)

func TestRetailBuyResponse(t *testing.T) {
	cases := []struct {
		name           string
		proceeds, cost int64
		costBasisKnown bool
		want           State
	}{
		{"profit over total cost clears", 150, 100, true, ClearAll},
		{"break-even distributes", 100, 100, true, Distributing},
		{"loss distributes", 90, 100, true, Distributing},
		{"unknown basis distributes", 150, 0, false, Distributing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := retailBuyResponse(big.NewInt(c.proceeds), big.NewInt(c.cost), c.costBasisKnown)
			if got != c.want {
				t.Fatalf("retailBuyResponse(proceeds=%d, cost=%d) = %s, want %s",
					c.proceeds, c.cost, got, c.want)
			}
		})
	}
}

func TestTotalCostIncludesGasAndLaunchFee(t *testing.T) {
	engine := &Engine{}
	engine.mutateStats(func(s *Stats) {
		s.GasFeeWei.SetInt64(30)
		s.LaunchFeeWei.SetInt64(20)
	})
	if got := engine.totalCostWei(big.NewInt(50)); got.Int64() != 100 {
		t.Fatalf("total cost = %d, want 100 (net 50 + gas 30 + launch 20)", got.Int64())
	}
	// A full-exit quote of exactly the notional cost must NOT clear once fees
	// are added on top.
	if got := retailBuyResponse(big.NewInt(60), engine.totalCostWei(big.NewInt(50)), true); got != Distributing {
		t.Fatalf("fees not covered must distribute, got %s", got)
	}
	if got := retailBuyResponse(big.NewInt(101), engine.totalCostWei(big.NewInt(50)), true); got != ClearAll {
		t.Fatalf("fees covered with profit must clear, got %s", got)
	}
}

func TestApplySlippage(t *testing.T) {
	got := applySlippage(big.NewInt(1000), 1500) // 15%
	if got.Int64() != 850 {
		t.Fatalf("applySlippage(1000, 1500) = %d, want 850", got.Int64())
	}
	if applySlippage(big.NewInt(1000), 0).Int64() != 1000 {
		t.Fatal("zero slippage must be identity")
	}
	if applySlippage(nil, 100).Sign() != 0 {
		t.Fatal("nil amount must be zero")
	}
}

func TestApplySellSlippageUsesMaximumTolerance(t *testing.T) {
	quote := new(big.Int).SetUint64(1_000_000)
	if got := applySellSlippage(quote); got.Cmp(big.NewInt(100)) != 0 {
		t.Fatalf("sell min out = %s, want 100 (0.01%% of quote)", got)
	}
	if sellSlippageBps != 9_999 {
		t.Fatalf("sell slippage = %d bps, want 9999", sellSlippageBps)
	}
}

func TestScaleWei(t *testing.T) {
	if got := scaleWei(big.NewInt(1000), 0.25); got.Int64() != 250 {
		t.Fatalf("scaleWei(1000, 0.25) = %d, want 250", got.Int64())
	}
	if got := scaleWei(big.NewInt(1000), 1.0); got.Int64() != 1000 {
		t.Fatalf("scaleWei frac>=1 must copy, got %d", got.Int64())
	}
	if got := scaleWei(big.NewInt(1000), 0); got.Sign() != 0 {
		t.Fatal("scaleWei frac<=0 must be zero")
	}
}

func TestEthToWei(t *testing.T) {
	if got := ethToWei(1); got.Cmp(new(big.Int).SetUint64(1e18)) != 0 {
		t.Fatalf("ethToWei(1) = %s, want 1e18", got)
	}
	if got := ethToWei(0.0005); got.Cmp(big.NewInt(5e14)) != 0 {
		t.Fatalf("ethToWei(0.0005) = %s, want 5e14", got)
	}
	if ethToWei(0).Sign() != 0 {
		t.Fatal("ethToWei(0) must be zero")
	}
}

func TestConfigProtocolCompatibility(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.ProtocolName() != ProtocolV2 {
		t.Fatalf("new config protocol = %q, want v2", cfg.ProtocolName())
	}
	if cfg.AccumulateInterval != 100*time.Millisecond || cfg.SellInterval != time.Second || !cfg.ConcurrentBuys || !cfg.ConcurrentSells {
		t.Fatalf("unexpected execution defaults: %+v", cfg)
	}
	legacy := Config{}
	if legacy.ProtocolName() != ProtocolV1 {
		t.Fatalf("legacy config protocol = %q, want v1", legacy.ProtocolName())
	}
	cfg.DevBuyETH = 0.1
	cfg.RPCEndpoint = "https://rpc.example"
	cfg.Token.Name, cfg.Token.Symbol = "Test", "TEST"
	if err := cfg.Validate(true); err == nil {
		t.Fatal("v2 launch with atomic dev buy must be rejected")
	}
}
