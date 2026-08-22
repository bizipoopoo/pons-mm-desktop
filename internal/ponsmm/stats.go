package ponsmm

import (
	"context"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// Stats is a snapshot of the cumulative execution statistics for one engine
// run. All big values are copies and safe to retain.
type Stats struct {
	BuyCount  int64
	SellCount int64
	// EthSpentWei is the ETH paid into confirmed buys (excluding gas).
	EthSpentWei *big.Int
	// EthReceivedWei is the ETH received from confirmed sells (quote value).
	EthReceivedWei *big.Int
	// TokensSoldRaw is the raw (18-dec) token amount sold.
	TokensSoldRaw *big.Int
	// GasFeeWei is gasUsed * effectiveGasPrice (priority tip included) summed
	// over every transaction the strategy confirmed: launch, buys, sells,
	// approvals and WETH unwraps.
	GasFeeWei *big.Int
	// LaunchFeeWei is the factory launch fee paid when this run launched the
	// token; zero when binding an existing pair.
	LaunchFeeWei *big.Int
	// StartBalanceWei is the summed ETH of every strategy wallet (treasury +
	// makers) captured when the run started; nil until captured.
	StartBalanceWei *big.Int
	// EndBalanceWei is the same sum captured when the run finished; nil while
	// the strategy is still running.
	EndBalanceWei *big.Int
}

// TotalCostWei is gas + launch fee: everything paid on top of swap notional.
func (s Stats) TotalCostWei() *big.Int {
	total := big.NewInt(0)
	if s.GasFeeWei != nil {
		total.Add(total, s.GasFeeWei)
	}
	if s.LaunchFeeWei != nil {
		total.Add(total, s.LaunchFeeWei)
	}
	return total
}

// SetStatsListener registers a callback invoked with a snapshot after every
// statistics update. Must be set before Run/Launch.
func (e *Engine) SetStatsListener(fn func(Stats)) {
	e.statsMu.Lock()
	e.onStats = fn
	e.statsMu.Unlock()
}

// StatsSnapshot returns a copy of the current statistics.
func (e *Engine) StatsSnapshot() Stats {
	e.statsMu.Lock()
	defer e.statsMu.Unlock()
	return cloneStats(e.stats)
}

// mutateStats applies fn under the stats lock and notifies the listener with
// a detached copy. Safe on Engine literals used in unit tests.
func (e *Engine) mutateStats(fn func(*Stats)) {
	e.statsMu.Lock()
	ensureStats(&e.stats)
	fn(&e.stats)
	snapshot := cloneStats(e.stats)
	listener := e.onStats
	e.statsMu.Unlock()
	if listener != nil {
		listener(snapshot)
	}
}

func ensureStats(s *Stats) {
	if s.EthSpentWei == nil {
		s.EthSpentWei = big.NewInt(0)
	}
	if s.EthReceivedWei == nil {
		s.EthReceivedWei = big.NewInt(0)
	}
	if s.TokensSoldRaw == nil {
		s.TokensSoldRaw = big.NewInt(0)
	}
	if s.GasFeeWei == nil {
		s.GasFeeWei = big.NewInt(0)
	}
	if s.LaunchFeeWei == nil {
		s.LaunchFeeWei = big.NewInt(0)
	}
}

func cloneStats(s Stats) Stats {
	out := s
	out.EthSpentWei = cloneInt(s.EthSpentWei)
	out.EthReceivedWei = cloneInt(s.EthReceivedWei)
	out.TokensSoldRaw = cloneInt(s.TokensSoldRaw)
	out.GasFeeWei = cloneInt(s.GasFeeWei)
	out.LaunchFeeWei = cloneInt(s.LaunchFeeWei)
	out.StartBalanceWei = cloneInt(s.StartBalanceWei)
	out.EndBalanceWei = cloneInt(s.EndBalanceWei)
	return out
}

func cloneInt(v *big.Int) *big.Int {
	if v == nil {
		return nil
	}
	return new(big.Int).Set(v)
}

// recordGas accumulates the real fee paid by a confirmed transaction.
func (e *Engine) recordGas(rcpt *types.Receipt) {
	if rcpt == nil || rcpt.EffectiveGasPrice == nil {
		return
	}
	fee := new(big.Int).Mul(rcpt.EffectiveGasPrice, new(big.Int).SetUint64(rcpt.GasUsed))
	e.mutateStats(func(s *Stats) { s.GasFeeWei.Add(s.GasFeeWei, fee) })
}

func (e *Engine) recordLaunchCost(feeWei *big.Int, rcpt *types.Receipt) {
	e.mutateStats(func(s *Stats) {
		if feeWei != nil {
			s.LaunchFeeWei.Add(s.LaunchFeeWei, feeWei)
		}
	})
	e.recordGas(rcpt)
}

func (e *Engine) recordBuy(wethIn *big.Int) {
	e.mutateStats(func(s *Stats) {
		s.BuyCount++
		if wethIn != nil {
			s.EthSpentWei.Add(s.EthSpentWei, wethIn)
		}
	})
}

func (e *Engine) recordSell(tokens, quote *big.Int) {
	e.mutateStats(func(s *Stats) {
		s.SellCount++
		if tokens != nil {
			s.TokensSoldRaw.Add(s.TokensSoldRaw, tokens)
		}
		if quote != nil {
			s.EthReceivedWei.Add(s.EthReceivedWei, quote)
		}
	})
}

// totalCostWei is the profit hurdle for a full exit: net ETH invested plus
// every payment made so far (gas, priority tips, launch fee).
func (e *Engine) totalCostWei(netSpentWei *big.Int) *big.Int {
	total := e.StatsSnapshot().TotalCostWei()
	if netSpentWei != nil {
		total.Add(total, netSpentWei)
	}
	return total
}

// captureStartBalance records the summed wallet ETH once, at run start, so a
// final capture can report the round's realized profit or loss.
func (e *Engine) captureStartBalance() {
	total := e.pool.totalETH()
	e.mutateStats(func(s *Stats) {
		if s.StartBalanceWei == nil {
			s.StartBalanceWei = total
		}
	})
}

// finalizeStats re-reads every wallet balance when the run ends and records
// the closing total. It uses a fresh context because the run context is
// already cancelled during shutdown.
func (e *Engine) finalizeStats() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := e.pool.RefreshETH(ctx); err != nil {
		e.log.Warn("final balance refresh failed; round P&L unavailable", "err", err)
		return
	}
	total := e.pool.totalETH()
	e.mutateStats(func(s *Stats) { s.EndBalanceWei = total })
	snap := e.StatsSnapshot()
	if snap.StartBalanceWei != nil {
		profit := new(big.Int).Sub(total, snap.StartBalanceWei)
		e.log.Info("round complete",
			"start_balance_eth", weiToEthStr(snap.StartBalanceWei),
			"end_balance_eth", weiToEthStr(total),
			"profit_eth", weiToEthStr(profit),
			"buys", snap.BuyCount, "sells", snap.SellCount,
			"total_cost_eth", weiToEthStr(snap.TotalCostWei()))
	}
}
