package control

import (
	"math/big"
	"testing"

	"github.com/bizipoopoo/pons-mm-desktop/internal/vault"
)

func TestRandomNearEvenSplitSumsExactly(t *testing.T) {
	total := big.NewInt(1_000_000_000_000_007)
	for _, n := range []int{1, 3, 10, 100} {
		parts := randomNearEvenSplit(total, n)
		if len(parts) != n {
			t.Fatalf("n=%d: got %d parts", n, len(parts))
		}
		sum := big.NewInt(0)
		mean := new(big.Int).Div(total, big.NewInt(int64(n)))
		for i, p := range parts {
			if p.Sign() <= 0 {
				t.Fatalf("n=%d part %d is not positive: %s", n, i, p)
			}
			// Every slot stays near the mean: within ±25% is a loose bound for
			// the ±15% weights plus rounding.
			lo := new(big.Int).Mul(mean, big.NewInt(75))
			lo.Div(lo, big.NewInt(100))
			hi := new(big.Int).Mul(mean, big.NewInt(125))
			hi.Div(hi, big.NewInt(100))
			if n > 1 && (p.Cmp(lo) < 0 || p.Cmp(hi) > 0) {
				t.Fatalf("n=%d part %d=%s outside [%s,%s]", n, i, p, lo, hi)
			}
			sum.Add(sum, p)
		}
		if sum.Cmp(total) != 0 {
			t.Fatalf("n=%d parts sum %s != total %s", n, sum, total)
		}
	}
}

func TestRelaySliceCoversAllSlots(t *testing.T) {
	for _, n := range []int{1, 5, 10, 15, 100, 250} {
		covered := make([]int, n)
		for r := 0; r < fundingRelayCount; r++ {
			start, end := relaySlice(r, n)
			if start > end || start < 0 || end > n {
				t.Fatalf("n=%d relay %d: bad slice [%d,%d)", n, r, start, end)
			}
			for i := start; i < end; i++ {
				covered[i]++
			}
		}
		for i, c := range covered {
			if c != 1 {
				t.Fatalf("n=%d slot %d covered %d times", n, i, c)
			}
		}
	}
}

func TestBuildFundingHopsLayout(t *testing.T) {
	// Deterministic dummy keys: route wallets and two targets.
	key := func(b byte) string {
		out := make([]byte, 32)
		out[31] = b
		return "0x" + bigHex(out)
	}
	routeKeys := make([]string, 1+2*fundingRelayCount)
	for i := range routeKeys {
		routeKeys[i] = key(byte(i + 1))
	}
	mnemonics := make([]string, fundingBatchCount)
	for i := range mnemonics {
		m, err := vault.GenerateMnemonic()
		if err != nil {
			t.Fatal(err)
		}
		mnemonics[i] = m
	}
	rec := fundingTaskRecord{
		FundingTask: FundingTask{
			Kind:    FundingKindDistribute,
			Targets: []string{"0x1111111111111111111111111111111111111111", "0x2222222222222222222222222222222222222222"},
		},
		BatchMnemonics: mnemonics,
	}
	cfg := FundingConfig{WithdrawCold: "0x3333333333333333333333333333333333333333"}
	hops, err := buildFundingHops(rec, cfg, routeKeys, nil, big.NewInt(4663))
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != fundingBatchCount+2 {
		t.Fatalf("distribute hops = %d, want %d", len(hops), fundingBatchCount+2)
	}
	n := len(rec.Targets)
	// cold split + relay splits + 4 batch forwards + final payout
	wantTransfers := []int{minInt(fundingRelayCount, n), n, n, n, n, n, n}
	for i, hop := range hops {
		if hop.transferCount() != wantTransfers[i] {
			t.Fatalf("hop %d (%s) transfers = %d, want %d", i, hop.name, hop.transferCount(), wantTransfers[i])
		}
	}

	// Withdraw layout mirrors the route.
	rec.Kind = FundingKindWithdraw
	sourceKeys := []string{key(0x51), key(0x52)}
	hops, err = buildFundingHops(rec, cfg, routeKeys, sourceKeys, big.NewInt(4663))
	if err != nil {
		t.Fatal(err)
	}
	if len(hops) != fundingBatchCount+2 {
		t.Fatalf("withdraw hops = %d, want %d", len(hops), fundingBatchCount+2)
	}
	last := hops[len(hops)-1]
	if len(last.sweeps) != fundingRelayCount {
		t.Fatalf("final relay hop sweeps = %d, want %d", len(last.sweeps), fundingRelayCount)
	}
	for _, sw := range last.sweeps {
		if sw.to.Hex() != cfg.WithdrawCold {
			t.Fatalf("final sweep pays %s, want the withdraw cold wallet", sw.to.Hex())
		}
	}
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func bigHex(b []byte) string {
	const digits = "0123456789abcdef"
	out := make([]byte, 0, len(b)*2)
	for _, v := range b {
		out = append(out, digits[v>>4], digits[v&0xf])
	}
	return string(out)
}
