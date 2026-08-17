package control

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bizipoopoo/pons-mm-desktop/internal/ponsmm"
)

func TestNewStrategyDefaultsToV2(t *testing.T) {
	s := NewStrategy()
	if s.Protocol != ponsmm.ProtocolV2 {
		t.Fatalf("new strategy protocol = %q, want v2", s.Protocol)
	}
	if s.DevBuyETH != 0 {
		t.Fatalf("new v2 strategy dev buy = %v, want 0", s.DevBuyETH)
	}
	if s.AccumulateIntervalMS != 1000 || s.SellIntervalMS != 1000 {
		t.Fatalf("new strategy intervals = %d/%d, want 1000/1000", s.AccumulateIntervalMS, s.SellIntervalMS)
	}
	if s.ConcurrentBuys || s.SequentialSells {
		t.Fatalf("new strategy concurrency = buys:%v sequential-sells:%v, want false/false", s.ConcurrentBuys, s.SequentialSells)
	}
}

func TestMissingProtocolRemainsV1(t *testing.T) {
	s := Strategy{}
	if s.protocolName() != ponsmm.ProtocolV1 {
		t.Fatalf("missing protocol normalized to %q, want v1", s.protocolName())
	}
}

func TestV2LaunchRejectsTooManyExemptions(t *testing.T) {
	s := NewStrategy()
	s.Name, s.Mode = "too many", ModeLaunch
	s.Token.Name, s.Token.Symbol = "Test", "TEST"
	for i := 0; i < 34; i++ {
		s.WalletIDs = append(s.WalletIDs, string(rune('a'+i)))
	}
	err := s.validate(Settings{RPCEndpoint: "https://rpc.example"})
	if err == nil {
		t.Fatal("expected v2 wallet limit validation error")
	}
}

func TestConfigStoreMigratesMissingProtocolToV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(`{"version":1,"settings":{},"strategies":[{"id":"legacy","name":"legacy","mode":"launch"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := newConfigStore(path)
	if err != nil {
		t.Fatal(err)
	}
	_, strategies := store.snapshot()
	if len(strategies) != 1 || strategies[0].Protocol != ponsmm.ProtocolV1 {
		t.Fatalf("legacy protocol migration failed: %+v", strategies)
	}
}

func TestExitAllRequiresExplicitConfirmation(t *testing.T) {
	service := &Service{}
	if err := service.ExitAll("strategy", "LIVE"); err == nil {
		t.Fatal("one-click exit must reject the live-start confirmation phrase")
	}
}
