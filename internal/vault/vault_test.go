package vault

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

func TestEncryptedRoundTrip(t *testing.T) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	testKey := hex.EncodeToString(crypto.FromECDSA(privateKey))
	path := filepath.Join(t.TempDir(), "wallets.vault")
	s := New(path)
	if err := s.Create("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	added, err := s.ImportPrivateKeys(testKey, "Maker")
	if err != nil || len(added) != 1 {
		t.Fatalf("import: %v, added=%d", err, len(added))
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), testKey) || strings.Contains(string(b), added[0].Address) {
		t.Fatal("vault leaked plaintext key or address")
	}
	s.Lock()
	if err := s.Unlock("wrong password"); err == nil {
		t.Fatal("wrong password unexpectedly unlocked vault")
	}
	if err := s.Unlock("correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	keys, err := s.Keys([]string{added[0].ID})
	if err != nil || len(keys) != 1 || keys[0] != testKey {
		t.Fatalf("round trip failed: %v", err)
	}
}

func TestMnemonicDerivation(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "wallets.vault"))
	if err := s.Create("password123"); err != nil {
		t.Fatal(err)
	}
	mnemonic, err := GenerateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	added, err := s.ImportMnemonic(mnemonic, 3, "Derived")
	if err != nil {
		t.Fatal(err)
	}
	if len(added) != 3 || added[0].Address == added[1].Address {
		t.Fatalf("unexpected derivation: %+v", added)
	}
}

func TestClearAllAllowsReimport(t *testing.T) {
	privateKey, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	testKey := hex.EncodeToString(crypto.FromECDSA(privateKey))
	path := filepath.Join(t.TempDir(), "wallets.vault")
	s := New(path)
	if err := s.Create("password123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportPrivateKeys(testKey, "Maker"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ImportPrivateKeys(testKey, "Maker"); err == nil {
		t.Fatal("duplicate import should fail while the wallet is stored")
	}
	if err := s.ClearAll(); err != nil {
		t.Fatal(err)
	}
	if got := s.Summaries(); len(got) != 0 {
		t.Fatalf("cleared vault still has %d wallets", len(got))
	}
	added, err := s.ImportPrivateKeys(testKey, "Maker")
	if err != nil || len(added) != 1 {
		t.Fatalf("reimport after clear: %v, added=%d", err, len(added))
	}
	s.Lock()
	if err := s.Unlock("password123"); err != nil {
		t.Fatal(err)
	}
	if got := s.Summaries(); len(got) != 1 || got[0].Address != added[0].Address {
		t.Fatalf("persisted after clear+reimport: %+v", got)
	}
	if err := s.ClearAll(); err != nil {
		t.Fatal(err)
	}
	s.Lock()
	if err := s.ClearAll(); err == nil {
		t.Fatal("clear while locked should fail")
	}
}
