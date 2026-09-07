package ponsmm

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func addr(n byte) common.Address {
	var a common.Address
	a[19] = n
	return a
}

func TestSelectSnipeExemptionsCapsAndKeepsTreasury(t *testing.T) {
	treasury, deployer := addr(1), addr(1)
	makers := make([]common.Address, 50)
	for i := range makers {
		makers[i] = addr(byte(i + 2))
	}

	list, exempt := selectSnipeExemptions(treasury, deployer, makers, false)
	if len(list) != 32 {
		t.Fatalf("direct launch list = %d, want 32", len(list))
	}
	if _, ok := exempt[treasury]; !ok {
		t.Fatal("treasury must be exempt as deployer")
	}
	if _, ok := exempt[makers[31]]; !ok {
		t.Fatal("32nd maker should be exempt")
	}
	if _, ok := exempt[makers[32]]; ok {
		t.Fatal("33rd maker must wait for tax decay")
	}

	list, exempt = selectSnipeExemptions(treasury, deployer, makers, true)
	if len(list) != 31 {
		t.Fatalf("initial-buy list = %d, want 31", len(list))
	}
	if _, ok := exempt[makers[30]]; !ok {
		t.Fatal("31st maker should be exempt with initial buy")
	}
	if _, ok := exempt[makers[31]]; ok {
		t.Fatal("32nd maker must wait when initial buy occupies a slot")
	}

	router := addr(9)
	list, exempt = selectSnipeExemptions(treasury, router, makers, true)
	if len(list) != 31 || list[0] != treasury {
		t.Fatalf("bundled list = %d first=%s, want 31 starting with treasury", len(list), list[0].Hex())
	}
	if _, ok := exempt[treasury]; !ok {
		t.Fatal("bundled treasury must be listed and exempt")
	}
}
