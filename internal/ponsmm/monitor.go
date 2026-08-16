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
	supply        *big.Int
	log           *slog.Logger

	Retail chan RetailEvent

	mu sync.Mutex
	// Our cost basis, maintained by the engine via RecordOurBuy/RecordOurSell.
	ourWethSpent *big.Int // cumulative wei paid on our buys minus wei received on our sells (>=0 = net invested)
	ourTokens    *big.Int // our token holding as tracked from our own fills
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
		Retail:           make(chan RetailEvent, 128),
		ourWethSpent:     big.NewInt(0),
		ourTokens:        big.NewInt(0),
		retailNetTokens:  big.NewInt(0),
		poolTokenReserve: big.NewInt(0),
		ownTx:            map[common.Hash]bool{},
	}
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
	RetailNetTokens  *big.Int
	RetailLastBuyPx  *big.Float
	LastRetailBuyAt  time.Time
	LastRetailSellAt time.Time
	LastTradeAt      time.Time
	PoolTokenReserve *big.Int
	// OurHoldFrac is our holding as a fraction of circulating supply
	// (supply minus tokens still in the pool). 0 when nothing has circulated.
	OurHoldFrac float64
	// AvgCostWeiPerToken is our average buy cost in wei per whole token, 0 when
	// we hold nothing.
	AvgCostWeiPerToken *big.Float
}

// Snapshot returns the current state. All big values are copies.
func (m *Monitor) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	circulating := new(big.Int).Sub(m.supply, m.poolTokenReserve)
	holdFrac := 0.0
	if circulating.Sign() > 0 {
		f := new(big.Float).Quo(new(big.Float).SetInt(m.ourTokens), new(big.Float).SetInt(circulating))
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
	trades := m.client.WatchPoolTrades(ctx, m.poolAddr, m.tokenIsToken0, m.log)
	if trades == nil {
		m.log.Warn("pons mm: pool trade subscription unavailable; monitor is price-only via periodic reserve refresh")
		<-ctx.Done()
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case t, ok := <-trades:
			if !ok {
				return
			}
			m.onTrade(t)
		}
	}
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
			if m.retailNetTokens.Sign() < 0 {
				m.retailNetTokens.SetInt64(0)
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
