package ponsmm

import (
	"math/big"
	"testing"
)

func TestRetailBuyResponse(t *testing.T) {
	cases := []struct {
		name           string
		proceeds, cost int64
		want           State
	}{
		{"break-even clears", 100, 100, ClearAll},
		{"profit clears", 150, 100, ClearAll},
		{"loss distributes", 90, 100, Distributing},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := retailBuyResponse(0.1, 0.6, big.NewInt(c.proceeds), big.NewInt(c.cost))
			if got != c.want {
				t.Fatalf("retailBuyResponse(proceeds=%d, cost=%d) = %s, want %s",
					c.proceeds, c.cost, got, c.want)
			}
		})
	}
}

func TestOscillationAction(t *testing.T) {
	anchor := big.NewFloat(100)
	band := 0.20 // band spans [80, 100]
	cases := []struct {
		name  string
		price float64
		want  int
	}{
		{"above anchor sells", 101, actionSell},
		{"at anchor holds", 100, actionHold},
		{"inside band holds", 90, actionHold},
		{"at lower edge holds", 80, actionHold},
		{"below band buys", 79, actionBuy},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := oscillationAction(big.NewFloat(c.price), anchor, band)
			if got != c.want {
				t.Fatalf("oscillationAction(price=%v) = %d, want %d", c.price, got, c.want)
			}
		})
	}
	if oscillationAction(nil, anchor, band) != actionHold {
		t.Fatal("nil price must hold")
	}
	if oscillationAction(big.NewFloat(1), nil, band) != actionHold {
		t.Fatal("nil anchor must hold")
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
