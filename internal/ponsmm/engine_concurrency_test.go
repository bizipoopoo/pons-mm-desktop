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

func TestRetailSellLeavesOscillation(t *testing.T) {
	monitor := newTestMonitor(testPool(), big.NewInt(1_000_000))
	engine := &Engine{
		cfg: &Config{}, monitor: monitor, log: slog.New(slog.DiscardHandler), state: Oscillating,
	}
	engine.onRetail(context.Background(), RetailEvent{
		IsBuy: false, TokenAmount: big.NewInt(100), WethAmount: big.NewInt(1),
	})
	if engine.state != Accumulating {
		t.Fatalf("state after retail sell = %s, want accumulating", engine.state)
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
