package ponsmm

import (
	"context"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// RetailEvent is a pool trade the monitor attributed to an outside (non-pool)
// address. The engine reacts to these to switch state.
type RetailEvent struct {
	IsBuy       bool
	TokenAmount *big.Int
	WethAmount  *big.Int
	// PriceWeiPerToken is WethAmount/TokenAmount scaled to wei-per-whole-token
	// (18-dec token), i.e. the marginal execution price of this trade.
	PriceWeiPerToken *big.Float
	Trader           common.Address
	At               time.Time
}

// Monitor subscribes to a launch pool's trades, separates our wallets' flow from
// retail flow, and keeps a live view of price, our position and cost, and the
// last retail activity. It is safe for concurrent reads via Snapshot.
type Monitor struct {
	client        *pons.Client
	pool          *Pool
	token         common.Address
	poolAddr      common.Address
	tokenIsToken0 bool
	curveMode     bool
	supply        *big.Int
	log           *slog.Logger

	Retail chan RetailEvent

	mu sync.Mutex
	// Our cost basis, maintained by the engine via RecordOurBuy/RecordOurSell.
	ourWethSpent   *big.Int // cumulative wei paid on our buys minus wei received on our sells (net invested; may become negative)
	ourTokens      *big.Int // our token holding as tracked from our own fills
	costBasisKnown bool     // false only when an existing position could not be valued at startup
	// Retail state.
	retailNetTokens  *big.Int   // retail net token holding (their buys - sells)
	retailLastBuyPx  *big.Float // wei-per-token of the most recent retail buy (their cost anchor)
	lastRetailBuyAt  time.Time
	lastRetailSellAt time.Time
	// Market state.
	lastPriceWeiPerTok *big.Float
	lastTradeAt        time.Time
	poolTokenReserve   *big.Int // tokens still sitting in the pool (not yet bought)
	ownTx              map[common.Hash]bool
	startBlock         uint64
	seenTrades         map[tradeLogID]struct{}
}

type tradeLogID struct {
	tx    common.Hash
	index uint
}

// NewCurveMonitor builds a monitor for a pons v2 bonding curve. It shares the
// strategy accounting with the v1 pool monitor while sourcing reserves and
// trade events from the curve contract.
func NewCurveMonitor(client *pons.Client, pool *Pool, token, curve common.Address, supply *big.Int, log *slog.Logger) *Monitor {
	m := NewMonitor(client, pool, token, curve, false, supply, log)
	m.curveMode = true
	return m
}

// NewMonitor builds a monitor for a launched token/pool.
func NewMonitor(client *pons.Client, pool *Pool, token, poolAddr common.Address, tokenIsToken0 bool, supply *big.Int, log *slog.Logger) *Monitor {
	return &Monitor{
		client:           client,
		pool:             pool,
		token:            token,
		poolAddr:         poolAddr,
		tokenIsToken0:    tokenIsToken0,
		supply:           supply,
		log:              log,
		Retail:           make(chan RetailEvent, 4096),
		ourWethSpent:     big.NewInt(0),
		ourTokens:        big.NewInt(0),
		costBasisKnown:   true,
		retailNetTokens:  big.NewInt(0),
		poolTokenReserve: big.NewInt(0),
		ownTx:            map[common.Hash]bool{},
		seenTrades:       map[tradeLogID]struct{}{},
	}
}

// SetStartBlock tells the monitor where to begin historical recovery. It is
// set from a launch receipt so trades later in that same block are not missed.
func (m *Monitor) SetStartBlock(block uint64) {
	m.mu.Lock()
	m.startBlock = block
	m.mu.Unlock()
}

// MarkOurTx tags a transaction hash we submitted so the trade it produces is
// attributed to us even if the pool's Swap recipient is a router sentinel
// (common when the router unwraps WETH on a sell).
func (m *Monitor) MarkOurTx(h common.Hash) {
	m.mu.Lock()
	m.ownTx[h] = true
	m.mu.Unlock()
}

// RecordOurBuy updates our cost basis after one of our buys confirms.
func (m *Monitor) RecordOurBuy(wethSpent, tokensGot *big.Int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ourWethSpent.Add(m.ourWethSpent, wethSpent)
	m.ourTokens.Add(m.ourTokens, tokensGot)
}

// RecordOurSell updates our cost basis after one of our sells confirms.
func (m *Monitor) RecordOurSell(tokensSold, wethGot *big.Int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ourWethSpent.Sub(m.ourWethSpent, wethGot)
	m.ourTokens.Sub(m.ourTokens, tokensSold)
	if m.ourTokens.Sign() < 0 {
		m.ourTokens.SetInt64(0)
	}
}

// Snapshot is a consistent read of the monitor's live state.
type Snapshot struct {
	PriceWeiPerToken *big.Float
	OurTokens        *big.Int
	OurWethSpent     *big.Int
	CostBasisKnown   bool
	RetailNetTokens  *big.Int
	RetailLastBuyPx  *big.Float
	LastRetailBuyAt  time.Time
	LastRetailSellAt time.Time
	LastTradeAt      time.Time
	PoolTokenReserve *big.Int
	// OurHoldFrac is our holding as a fraction of the token's fixed total supply.
	OurHoldFrac float64
	// AvgCostWeiPerToken is our average buy cost in wei per whole token, 0 when
	// we hold nothing.
	AvgCostWeiPerToken *big.Float
}

// Snapshot returns the current state. All big values are copies.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	holdFrac := 0.0
	if m.supply.Sign() > 0 {
		f := new(big.Float).Quo(new(big.Float).SetInt(m.ourTokens), new(big.Float).SetInt(m.supply))
		holdFrac, _ = f.Float64()
	}
	avgCost := big.NewFloat(0)
	if m.ourTokens.Sign() > 0 && m.ourWethSpent.Sign() > 0 {
		// wei per whole token = wethSpent / (tokens / 1e18)
		tokWhole := new(big.Float).Quo(new(big.Float).SetInt(m.ourTokens), big.NewFloat(1e18))
		avgCost = new(big.Float).Quo(new(big.Float).SetInt(m.ourWethSpent), tokWhole)
	}
	return Snapshot{
		PriceWeiPerToken:   cloneFloat(m.lastPriceWeiPerTok),
		OurTokens:          new(big.Int).Set(m.ourTokens),
		OurWethSpent:       new(big.Int).Set(m.ourWethSpent),
		CostBasisKnown:     m.costBasisKnown,
		RetailNetTokens:    new(big.Int).Set(m.retailNetTokens),
		RetailLastBuyPx:    cloneFloat(m.retailLastBuyPx),
		LastRetailBuyAt:    m.lastRetailBuyAt,
		LastRetailSellAt:   m.lastRetailSellAt,
		LastTradeAt:        m.lastTradeAt,
		PoolTokenReserve:   new(big.Int).Set(m.poolTokenReserve),
		OurHoldFrac:        holdFrac,
		AvgCostWeiPerToken: avgCost,
	}
}

// RefreshReserves reads how many launch tokens still sit in the pool, so the
// engine can compute circulating supply and our holding fraction.
func (m *Monitor) RefreshReserves(ctx context.Context) error {
	if m.curveMode {
		_, tokenReserve, err := m.client.Reserves(ctx, m.poolAddr)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.poolTokenReserve = tokenReserve
		m.mu.Unlock()
		return nil
	}
	bal, err := m.client.TokenBalance(ctx, m.token, m.poolAddr)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.poolTokenReserve = bal
	m.mu.Unlock()
	return nil
}

// Run consumes pool trades until ctx ends. Retail trades are pushed onto
// m.Retail (best effort; dropped if the engine is slow).
func (m *Monitor) Run(ctx context.Context) {
	if m.curveMode {
		m.runCurve(ctx)
		return
	}
	live := m.client.WatchPoolTrades(ctx, m.poolAddr, m.tokenIsToken0, m.log)
	if live == nil {
		m.log.Warn("pons v1: pool trade subscription unavailable; monitor will poll")
	}
	from, ready := m.recoveryStart(ctx)
	if ready {
		from = m.replayPool(ctx, from)
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-live:
			if !ok {
				live = nil
				m.log.Warn("pons v1: pool subscription closed; continuing with log polling")
				continue
			}
			m.consumeTrade(t)
		case <-tick.C:
			if !ready {
				from, ready = m.recoveryStart(ctx)
				if !ready {
					continue
				}
			}
			from = m.replayPool(ctx, from)
		}
	}
}

func (m *Monitor) runCurve(ctx context.Context) {
	live := m.client.WatchCurveTradeEvents(ctx, m.poolAddr, m.log)
	if live == nil {
		m.log.Warn("pons v2: curve trade subscription unavailable; monitor will poll")
	}
	from, ready := m.recoveryStart(ctx)
	if ready {
		from = m.replayCurve(ctx, from)
	}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case trade, ok := <-live:
			if !ok {
				live = nil
				m.log.Warn("pons v2: curve subscription closed; continuing with log polling")
				continue
			}
			m.consumeTrade(pons.PoolTrade{
				IsBuy:       trade.IsBuy,
				TokenAmount: trade.TokenAmount,
				WethAmount:  trade.QuoteAmount,
				Sender:      trade.Trader,
				Recipient:   trade.Recipient,
				Block:       trade.Block,
				LogIndex:    trade.LogIndex,
				TxHash:      trade.TxHash,
			})
		case <-tick.C:
			if !ready {
				from, ready = m.recoveryStart(ctx)
				if !ready {
					continue
				}
			}
			from = m.replayCurve(ctx, from)
		}
	}
}

func (m *Monitor) recoveryStart(ctx context.Context) (uint64, bool) {
	m.mu.Lock()
	start := m.startBlock
	m.mu.Unlock()
	if start > 0 {
		return start, true
	}
	head, err := m.client.BlockNumber(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return 0, false
		}
		m.log.Warn("cannot initialize trade recovery head", "err", err)
		return 0, false
	}
	// Existing-pair strategies have no known launch block. The subscription is
	// already live, so polling can safely begin after the observed head.
	return head + 1, true
}

func (m *Monitor) replayPool(ctx context.Context, from uint64) uint64 {
	head, err := m.client.BlockNumber(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return from
		}
		m.log.Warn("pons v1: trade recovery head failed", "err", err)
		return from
	}
	if head < from {
		return from
	}
	trades, err := m.client.FilterPoolTrades(ctx, m.poolAddr, m.tokenIsToken0, from, head)
	if err != nil {
		if ctx.Err() != nil {
			return from
		}
		m.log.Warn("pons v1: trade recovery failed", "from_block", from, "to_block", head, "err", err)
		return from
	}
	recovered := 0
	for _, trade := range trades {
		if m.consumeTrade(trade) {
			recovered++
		}
	}
	if recovered > 0 {
		m.log.Info("recovered pool trades", "count", recovered, "from_block", from, "to_block", head)
	}
	return head + 1
}

func (m *Monitor) replayCurve(ctx context.Context, from uint64) uint64 {
	head, err := m.client.BlockNumber(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return from
		}
		m.log.Warn("pons v2: trade recovery head failed", "err", err)
		return from
	}
	if head < from {
		return from
	}
	trades, err := m.client.FilterCurveTradeEvents(ctx, m.poolAddr, from, head)
	if err != nil {
		if ctx.Err() != nil {
			return from
		}
		m.log.Warn("pons v2: trade recovery failed", "from_block", from, "to_block", head, "err", err)
		return from
	}
	recovered := 0
	for _, trade := range trades {
		if m.consumeTrade(pons.PoolTrade{
			IsBuy:       trade.IsBuy,
			TokenAmount: trade.TokenAmount,
			WethAmount:  trade.QuoteAmount,
			Sender:      trade.Trader,
			Recipient:   trade.Recipient,
			Block:       trade.Block,
			LogIndex:    trade.LogIndex,
			TxHash:      trade.TxHash,
		}) {
			recovered++
		}
	}
	if recovered > 0 {
		m.log.Info("recovered curve trades", "count", recovered, "from_block", from, "to_block", head)
	}
	return head + 1
}

func (m *Monitor) onTrade(t pons.PoolTrade) {
	now := time.Now()
	price := priceWeiPerToken(t.WethAmount, t.TokenAmount)
	ours := m.classify(t)

	m.mu.Lock()
	m.lastTradeAt = now
	if price != nil {
		m.lastPriceWeiPerTok = price
	}
	if !ours {
		if t.IsBuy {
			m.retailNetTokens.Add(m.retailNetTokens, t.TokenAmount)
			m.retailLastBuyPx = price
			m.lastRetailBuyAt = now
		} else {
			m.retailNetTokens.Sub(m.retailNetTokens, t.TokenAmount)
			if m.retailNetTokens.Sign() <= 0 {
				m.retailNetTokens.SetInt64(0)
				m.retailLastBuyPx = nil
			}
			m.lastRetailSellAt = now
		}
	}
	m.mu.Unlock()

	if ours {
		return
	}
	ev := RetailEvent{
		IsBuy:            t.IsBuy,
		TokenAmount:      new(big.Int).Set(t.TokenAmount),
		WethAmount:       new(big.Int).Set(t.WethAmount),
		PriceWeiPerToken: price,
		Trader:           t.Recipient,
		At:               now,
	}
	select {
	case m.Retail <- ev:
	default:
		m.log.Warn("pons mm: retail event dropped (engine busy)", "is_buy", t.IsBuy)
	}
}

// consumeTrade deduplicates overlap between live subscription delivery and
// block-log recovery. Zero hashes belong to unit fixtures and remain repeatable.
func (m *Monitor) consumeTrade(t pons.PoolTrade) bool {
	if t.TxHash != (common.Hash{}) {
		id := tradeLogID{tx: t.TxHash, index: t.LogIndex}
		m.mu.Lock()
		if _, seen := m.seenTrades[id]; seen {
			m.mu.Unlock()
			return false
		}
		m.seenTrades[id] = struct{}{}
		m.mu.Unlock()
	}
	m.onTrade(t)
	return true
}

// classify decides whether a pool trade came from one of our wallets. It checks
// the tx hash we tagged first, then the swap's sender/recipient against the pool.
func (m *Monitor) classify(t pons.PoolTrade) bool {
	m.mu.Lock()
	tagged := m.ownTx[t.TxHash]
	m.mu.Unlock()
	if tagged {
		return true
	}
	return m.pool.IsOurs(t.Recipient) || m.pool.IsOurs(t.Sender)
}

// priceWeiPerToken computes wei-of-WETH per whole (1e18) token for a trade.
func priceWeiPerToken(wethAmount, tokenAmount *big.Int) *big.Float {
	if wethAmount == nil || tokenAmount == nil || tokenAmount.Sign() == 0 {
		return nil
	}
	tokWhole := new(big.Float).Quo(new(big.Float).SetInt(tokenAmount), big.NewFloat(1e18))
	if tokWhole.Sign() == 0 {
		return nil
	}
	return new(big.Float).Quo(new(big.Float).SetInt(wethAmount), tokWhole)
}

func cloneFloat(f *big.Float) *big.Float {
	if f == nil {
		return nil
	}
	return new(big.Float).Set(f)
}
