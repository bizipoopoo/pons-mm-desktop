package ponsmm

import (
	"log/slog"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

func testPool(ours ...common.Address) *Pool {
	p := &Pool{byAddr: map[common.Address]*Wallet{}}
	for _, a := range ours {
		w := &Wallet{Addr: a, TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)}
		p.byAddr[a] = w
		p.Makers = append(p.Makers, w)
	}
	if len(p.Makers) > 0 {
		p.Treasury = p.Makers[0]
	}
	return p
}

func newTestMonitor(pool *Pool, supply *big.Int) *Monitor {
	log := slog.New(slog.DiscardHandler)
	return NewMonitor(nil, pool, common.HexToAddress("0xToken"), common.HexToAddress("0xPool"), false, supply, log)
}

func TestPriceWeiPerToken(t *testing.T) {
	// 2e18 wei of WETH for 1 whole token => price 2e18 wei/token.
	price := priceWeiPerToken(new(big.Int).SetUint64(2e18), new(big.Int).SetUint64(1e18))
	if price == nil {
		t.Fatal("price nil")
	}
	want := big.NewFloat(2e18)
	if price.Cmp(want) != 0 {
		t.Fatalf("price = %v, want %v", price, want)
	}
	if priceWeiPerToken(big.NewInt(1), big.NewInt(0)) != nil {
		t.Fatal("zero tokens must yield nil price")
	}
}

func TestMonitorClassifyByAddress(t *testing.T) {
	mine := common.HexToAddress("0x1111111111111111111111111111111111111111")
	m := newTestMonitor(testPool(mine), new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))

	// A trade whose recipient is our wallet is ours: no retail event.
	m.onTrade(pons.PoolTrade{
		IsBuy:       true,
		TokenAmount: big.NewInt(500),
		WethAmount:  big.NewInt(100),
		Recipient:   mine,
		Sender:      common.HexToAddress("0xRouter"),
	})
	select {
	case <-m.Retail:
		t.Fatal("our own trade must not emit a retail event")
	default:
	}
}

func TestMonitorClassifyByTaggedTx(t *testing.T) {
	m := newTestMonitor(testPool(), new(big.Int).Exp(big.NewInt(10), big.NewInt(24), nil))
	h := common.HexToHash("0xdeadbeef")
	m.MarkOurTx(h)
	m.onTrade(pons.PoolTrade{
		IsBuy:       true,
		TokenAmount: big.NewInt(1),
		WethAmount:  big.NewInt(1),
		Recipient:   common.HexToAddress("0xStranger"),
		TxHash:      h,
	})
	select {
	case <-m.Retail:
		t.Fatal("tagged tx must be classified as ours")
	default:
	}
}

func TestMonitorRetailBuyUpdatesState(t *testing.T) {
	stranger := common.HexToAddress("0x2222222222222222222222222222222222222222")
	m := newTestMonitor(testPool(), new(big.Int).SetUint64(1_000_000))

	m.onTrade(pons.PoolTrade{
		IsBuy:       true,
		TokenAmount: big.NewInt(1000),
		WethAmount:  big.NewInt(10),
		Recipient:   stranger,
		Sender:      common.HexToAddress("0xRouter"),
	})
	select {
	case ev := <-m.Retail:
		if !ev.IsBuy {
			t.Fatal("expected a retail buy event")
		}
	default:
		t.Fatal("expected a retail event to be emitted")
	}
	snap := m.Snapshot()
	if snap.RetailNetTokens.Int64() != 1000 {
		t.Fatalf("retail net tokens = %d, want 1000", snap.RetailNetTokens.Int64())
	}
	if snap.RetailLastBuyPx == nil {
		t.Fatal("retail last buy price must be recorded")
	}
}

func TestMonitorRetailExitClearsPriceAnchor(t *testing.T) {
	stranger := common.HexToAddress("0x2222222222222222222222222222222222222222")
	m := newTestMonitor(testPool(), new(big.Int).SetUint64(1_000_000))
	m.onTrade(pons.PoolTrade{
		IsBuy: true, TokenAmount: big.NewInt(1000), WethAmount: big.NewInt(10),
		Recipient: stranger, Sender: common.HexToAddress("0xRouter"),
	})
	m.onTrade(pons.PoolTrade{
		IsBuy: false, TokenAmount: big.NewInt(1000), WethAmount: big.NewInt(9),
		Recipient: stranger, Sender: common.HexToAddress("0xRouter"),
	})
	snap := m.Snapshot()
	if snap.RetailNetTokens.Sign() != 0 {
		t.Fatalf("retail net tokens = %s, want zero", snap.RetailNetTokens)
	}
	if snap.RetailLastBuyPx != nil {
		t.Fatal("retail price anchor must be cleared after the tracked position exits")
	}
}

func TestSnapshotHoldFraction(t *testing.T) {
	supply := new(big.Int).SetUint64(1_000_000)
	m := newTestMonitor(testPool(), supply)
	// 300k tokens circulating (pool holds 700k), we hold 150k -> 50%.
	m.mu.Lock()
	m.poolTokenReserve = big.NewInt(700_000)
	m.ourTokens = big.NewInt(150_000)
	m.mu.Unlock()

	snap := m.Snapshot()
	if got := snap.OurHoldFrac; got < 0.499 || got > 0.501 {
		t.Fatalf("hold fraction = %f, want ~0.5", got)
	}
}
