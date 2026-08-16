package ponsmm

import (
	"encoding/json"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestBuildGmgnImportFormat(t *testing.T) {
	treasury := common.HexToAddress("0x1111111111111111111111111111111111111111")
	m1 := common.HexToAddress("0x2222222222222222222222222222222222222222")
	m2 := common.HexToAddress("0x3333333333333333333333333333333333333333")
	pool := &Pool{
		Treasury: &Wallet{Addr: treasury, TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)},
		Makers: []*Wallet{
			{Addr: m1, TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)},
			{Addr: m2, TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)},
		},
	}
	marks := BuildGmgnImport(pool, "", "")
	if len(marks) != 3 {
		t.Fatalf("want 3 marks, got %d", len(marks))
	}
	if marks[0].Name != "PONSMM-deployer" || marks[0].Address != treasury.Hex() {
		t.Fatalf("deployer entry wrong: %+v", marks[0])
	}
	if marks[1].Name != "PONSMM-01" || marks[2].Name != "PONSMM-02" {
		t.Fatalf("maker names wrong: %s %s", marks[1].Name, marks[2].Name)
	}
	if marks[1].Emoji != "🤖" {
		t.Fatalf("default emoji wrong: %q", marks[1].Emoji)
	}

	// Must serialize to gmgn's {address,name,emoji} shape.
	b, _ := json.Marshal(marks[0])
	var round map[string]string
	if err := json.Unmarshal(b, &round); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"address", "name", "emoji"} {
		if _, ok := round[k]; !ok {
			t.Fatalf("missing json key %q in %s", k, string(b))
		}
	}
}
