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
