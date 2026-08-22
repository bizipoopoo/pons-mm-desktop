package ponsmm

import (
	"context"
	"log/slog"
	"math/big"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunWalletActionsConcurrency(t *testing.T) {
	wallets := []*Wallet{{}, {}, {}}
	var active, maxActive, completed int32
	runWalletActions(true, wallets, func(*Wallet) {
		current := atomic.AddInt32(&active, 1)
		for {
			previous := atomic.LoadInt32(&maxActive)
			if current <= previous || atomic.CompareAndSwapInt32(&maxActive, previous, current) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		atomic.AddInt32(&active, -1)
		atomic.AddInt32(&completed, 1)
	})
	if completed != int32(len(wallets)) || maxActive < 2 {
		t.Fatalf("concurrent actions completed=%d maxActive=%d", completed, maxActive)
	}

	active, maxActive, completed = 0, 0, 0
	runWalletActions(false, wallets, func(*Wallet) {
		current := atomic.AddInt32(&active, 1)
		if current > atomic.LoadInt32(&maxActive) {
			atomic.StoreInt32(&maxActive, current)
		}
		atomic.AddInt32(&active, -1)
		atomic.AddInt32(&completed, 1)
	})
	if completed != int32(len(wallets)) || maxActive != 1 {
		t.Fatalf("sequential actions completed=%d maxActive=%d", completed, maxActive)
	}
}

func TestAnyRetailSellStopsDistributionAndResumesAccumulation(t *testing.T) {
	monitor := newTestMonitor(testPool(), big.NewInt(1_000_000))
	monitor.mu.Lock()
	monitor.retailNetTokens = big.NewInt(100)
	monitor.mu.Unlock()
	engine := &Engine{
		cfg: &Config{}, monitor: monitor, log: slog.New(slog.DiscardHandler),
		state: Distributing, retailResponseActive: true,
	}
	engine.onRetail(context.Background(), RetailEvent{
		IsBuy: false, TokenAmount: big.NewInt(100), WethAmount: big.NewInt(1),
	})
	if engine.state != Accumulating || engine.retailResponseActive {
		t.Fatalf("partial retail exit state = %s active:%v, want accumulating/false", engine.state, engine.retailResponseActive)
	}
}

func TestFullRetailExitResumesPriorStrategy(t *testing.T) {
	monitor := newTestMonitor(testPool(), big.NewInt(1_000_000))
	engine := &Engine{
		cfg: &Config{}, monitor: monitor, log: slog.New(slog.DiscardHandler),
		state: Distributing, retailResponseActive: true,
	}
	engine.onRetail(context.Background(), RetailEvent{
		IsBuy: false, TokenAmount: big.NewInt(100), WethAmount: big.NewInt(1),
	})
	if engine.state != Accumulating || engine.retailResponseActive {
		t.Fatalf("full retail exit state = %s active:%v, want accumulating/false", engine.state, engine.retailResponseActive)
	}
}

func TestExitAllRequestWaitsForEngineResult(t *testing.T) {
	engine := &Engine{exitAllRequests: make(chan chan error)}
	go func() {
		done := <-engine.exitAllRequests
		done <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := engine.ExitAll(ctx); err != nil {
		t.Fatalf("ExitAll: %v", err)
	}
}

func TestRetailBuyInterruptsActiveBuyRound(t *testing.T) {
	pool := &Pool{Treasury: &Wallet{TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)}}
	monitor := newTestMonitor(pool, big.NewInt(1_000_000))
	roundCtx, cancel := context.WithCancel(context.Background())
	engine := &Engine{
		cfg:  &Config{ConcurrentSells: true},
		pool: pool, monitor: monitor, log: slog.New(slog.DiscardHandler),
		state: Accumulating, buyRoundRunning: true, buyRoundCancel: cancel,
	}

	engine.onRetail(context.Background(), RetailEvent{
		IsBuy: true, TokenAmount: big.NewInt(100), WethAmount: big.NewInt(1),
	})
	select {
	case <-roundCtx.Done():
	default:
		t.Fatal("retail buy did not cancel the active accumulation round")
	}
	if engine.state != Done {
		t.Fatalf("state = %s, want done after the empty position is already cleared", engine.state)
	}
}

func TestDistributionBatchSelectsFourToSixWallets(t *testing.T) {
	treasury := &Wallet{TokenRaw: big.NewInt(100), ETHWei: big.NewInt(0)}
	pool := &Pool{Treasury: treasury}
	for i := 0; i < 11; i++ {
		pool.Makers = append(pool.Makers, &Wallet{TokenRaw: big.NewInt(100), ETHWei: big.NewInt(0)})
	}
	engine := &Engine{cfg: &Config{}, pool: pool}
	batch := engine.distributionWalletBatch()
	if len(batch) < distributionBatchMin || len(batch) > distributionBatchMax {
		t.Fatalf("batch wallets = %d, want between %d and %d", len(batch), distributionBatchMin, distributionBatchMax)
	}
	for _, wallet := range batch {
		if wallet.tokenBalance().Cmp(big.NewInt(100)) != 0 {
			t.Fatal("distribution batch must select complete wallet balances")
		}
	}
	// The drawn size is stable across the batches of one distribution response.
	if again := engine.distributionWalletBatch(); len(again) != len(batch) {
		t.Fatalf("batch size changed between rounds: %d then %d", len(batch), len(again))
	}
}

func TestDistributionBatchCapsAtRemainingWallets(t *testing.T) {
	pool := &Pool{Treasury: &Wallet{TokenRaw: big.NewInt(100), ETHWei: big.NewInt(0)}}
	pool.Makers = append(pool.Makers, &Wallet{TokenRaw: big.NewInt(100), ETHWei: big.NewInt(0)})
	engine := &Engine{cfg: &Config{}, pool: pool}
	if batch := engine.distributionWalletBatch(); len(batch) != 2 {
		t.Fatalf("batch wallets = %d, want all 2 remaining", len(batch))
	}
}

func TestBuyEligibleMakersBuyOncePerCycle(t *testing.T) {
	funded := &Wallet{TokenRaw: big.NewInt(0), ETHWei: big.NewInt(1e18)}
	holding := &Wallet{TokenRaw: big.NewInt(5), ETHWei: big.NewInt(1e18)}
	broke := &Wallet{TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)}
	pool := &Pool{Treasury: &Wallet{TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)}, Makers: []*Wallet{funded, holding, broke}}
	engine := &Engine{cfg: &Config{}, pool: pool}
	eligible := engine.buyEligibleMakers()
	if len(eligible) != 1 || eligible[0] != funded {
		t.Fatalf("eligible makers = %d, want only the funded wallet without a position", len(eligible))
	}
	// Selling out restores eligibility for the resumed pump cycle.
	holding.setTokenBalance(big.NewInt(0))
	if len(engine.buyEligibleMakers()) != 2 {
		t.Fatal("wallet that fully sold must become buy-eligible again")
	}
}

func TestClearAllWaitsForPendingBuySettlement(t *testing.T) {
	pool := &Pool{Treasury: &Wallet{TokenRaw: big.NewInt(0), ETHWei: big.NewInt(0)}}
	engine := &Engine{pool: pool, state: ClearAll}
	engine.addPendingBuy()
	engine.finishDrainIfEmpty()
	if engine.state != ClearAll {
		t.Fatalf("state = %s, want clear-all while a buy is pending", engine.state)
	}
	engine.finishPendingBuy()
	engine.finishDrainIfEmpty()
	if engine.state != Done {
		t.Fatalf("state = %s, want done after pending buys and balances reach zero", engine.state)
	}
}
