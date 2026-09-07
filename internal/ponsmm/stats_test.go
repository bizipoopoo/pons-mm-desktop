package ponsmm

import (
	"math/big"
	"testing"
)

func TestMarkToMarket(t *testing.T) {
	e18 := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	eth := func(tenths int64) *big.Int {
		return new(big.Int).Mul(big.NewInt(tenths), new(big.Int).Div(e18, big.NewInt(10)))
	}
	start, ethNow, quoted := eth(10), eth(4), eth(7)
	mark, profit := markToMarket(ethNow, quoted, start)
	if mark.Cmp(eth(11)) != 0 {
		t.Fatalf("mark = %s want 1.1", weiToEthStr(mark))
	}
	if profit.Cmp(eth(1)) != 0 {
		t.Fatalf("profit = %s want 0.1", weiToEthStr(profit))
	}

	_, loss := markToMarket(ethNow, eth(2), start)
	if loss.Cmp(new(big.Int).Neg(eth(4))) != 0 {
		t.Fatalf("loss = %s want -0.4", weiToEthStr(loss))
	}
	if _, p := markToMarket(ethNow, quoted, nil); p != nil {
		t.Fatal("profit without a start balance should be nil")
	}
}
