package pons

import (
	"context"
	"log/slog"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// Launch is a newly detected pons launch, normalized from a TokenLaunched log.
type Launch struct {
	Token               common.Address
	Curve               common.Address
	Deployer            common.Address
	PairToken           common.Address
	LaunchConfigID      *big.Int
	GraduationThreshold *big.Int
	Block               uint64
	ObservedAt          time.Time
}

// WatchLaunches streams new TokenLaunched events from the factory. It prefers a
// websocket subscription (lowest latency) and falls back to polling getLogs
// when the endpoint is HTTP-only. The returned channel is closed when ctx ends.
func (c *Client) WatchLaunches(ctx context.Context, log *slog.Logger) <-chan Launch {
	out := make(chan Launch, 64)
	q := ethereum.FilterQuery{
		Addresses: []common.Address{c.factory},
		Topics:    [][]common.Hash{{tokenLaunchedTopic}},
	}
	go func() {
		defer close(out)
		if c.isWebsocket() {
			if c.subscribeLaunches(ctx, q, out, log) {
				return // ctx ended during a healthy subscription
			}
			log.Warn("pons: log subscription unavailable, falling back to polling")
		}
		c.pollLaunches(ctx, q, out, log)
	}()
	return out
}

func (c *Client) isWebsocket() bool {
	return len(c.rpcURL) >= 3 && (c.rpcURL[:3] == "wss" || c.rpcURL[:2] == "ws")
}

// subscribeLaunches runs a live log subscription until ctx ends (returns true)
// or the subscription fails (returns false so the caller can fall back).
func (c *Client) subscribeLaunches(ctx context.Context, q ethereum.FilterQuery, out chan<- Launch, log *slog.Logger) bool {
	logs := make(chan types.Log, 64)
	sub, err := c.eth.SubscribeFilterLogs(ctx, q, logs)
	if err != nil {
		log.Warn("pons: SubscribeFilterLogs failed", "err", err)
		return false
	}
	defer sub.Unsubscribe()
	log.Info("pons: subscribed to factory launches", "factory", c.factory.Hex())
	for {
		select {
		case <-ctx.Done():
			return true
		case err := <-sub.Err():
			log.Warn("pons: launch subscription dropped", "err", err)
			return false
		case lg := <-logs:
			if l, ok := decodeLaunch(lg); ok {
				emit(ctx, out, l)
			}
		}
	}
}

// pollLaunches polls getLogs on a fixed cadence, advancing the from-block past
// what it has already seen.
func (c *Client) pollLaunches(ctx context.Context, q ethereum.FilterQuery, out chan<- Launch, log *slog.Logger) {
	from, err := c.eth.BlockNumber(ctx)
	if err != nil {
		log.Warn("pons: cannot read head block; starting from 0", "err", err)
		from = 0
	}
	log.Info("pons: polling factory launches", "factory", c.factory.Hex(), "from_block", from)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			head, err := c.eth.BlockNumber(ctx)
			if err != nil || head < from {
				continue
			}
			fq := q
			fq.FromBlock = new(big.Int).SetUint64(from)
			fq.ToBlock = new(big.Int).SetUint64(head)
			logs, err := c.eth.FilterLogs(ctx, fq)
			if err != nil {
				log.Debug("pons: FilterLogs failed", "err", err)
				continue
			}
			for _, lg := range logs {
				if l, ok := decodeLaunch(lg); ok {
					emit(ctx, out, l)
				}
			}
			from = head + 1
		}
	}
}

func emit(ctx context.Context, out chan<- Launch, l Launch) {
	select {
	case out <- l:
	case <-ctx.Done():
	}
}

// decodeLaunch turns a TokenLaunched log into a Launch. The three indexed
// address fields come from topics; the rest from the data section.
func decodeLaunch(lg types.Log) (Launch, bool) {
	if len(lg.Topics) < 4 || lg.Topics[0] != tokenLaunchedTopic {
		return Launch{}, false
	}
	var data struct {
		PairToken           common.Address
		LaunchConfigID      *big.Int `abi:"launchConfigId"`
		GraduationThreshold *big.Int
	}
	if err := factoryABI.UnpackIntoInterface(&data, "TokenLaunched", lg.Data); err != nil {
		return Launch{}, false
	}
	return Launch{
		Token:               common.HexToAddress(lg.Topics[1].Hex()),
		Curve:               common.HexToAddress(lg.Topics[2].Hex()),
		Deployer:            common.HexToAddress(lg.Topics[3].Hex()),
		PairToken:           data.PairToken,
		LaunchConfigID:      data.LaunchConfigID,
		GraduationThreshold: data.GraduationThreshold,
		Block:               lg.BlockNumber,
		ObservedAt:          time.Now(),
	}, true
}

// CurveTrade is one decoded v2 bonding-curve buy or sell.
type CurveTrade struct {
	IsBuy       bool
	TokenAmount *big.Int
	QuoteAmount *big.Int
	Trader      common.Address
	Recipient   common.Address
	Block       uint64
	LogIndex    uint
	TxHash      common.Hash
}

// FilterCurveTradeEvents returns curve trades in an inclusive block range.
// Monitors use it to recover trades that landed before a live subscription was
// established and to cover temporary subscription gaps.
func (c *Client) FilterCurveTradeEvents(ctx context.Context, curve common.Address, fromBlock, toBlock uint64) ([]CurveTrade, error) {
	if toBlock < fromBlock {
		return nil, nil
	}
	logs, err := c.eth.FilterLogs(ctx, ethereum.FilterQuery{
		Addresses: []common.Address{curve},
		Topics:    [][]common.Hash{{curveBuyTopic, curveSellTopic}},
		FromBlock: new(big.Int).SetUint64(fromBlock),
		ToBlock:   new(big.Int).SetUint64(toBlock),
	})
	if err != nil {
		return nil, err
	}
	trades := make([]CurveTrade, 0, len(logs))
	for _, lg := range logs {
		if trade, ok := decodeCurveTrade(lg); ok {
			trades = append(trades, trade)
		}
	}
	return trades, nil
}

// WatchCurveTradeEvents subscribes to decoded CurveBuy/CurveSell logs. It
// returns nil for an HTTP endpoint, allowing callers to use polling instead.
func (c *Client) WatchCurveTradeEvents(ctx context.Context, curve common.Address, log *slog.Logger) <-chan CurveTrade {
	if !c.isWebsocket() {
		return nil
	}
	q := ethereum.FilterQuery{
		Addresses: []common.Address{curve},
		Topics:    [][]common.Hash{{curveBuyTopic, curveSellTopic}},
	}
	logs := make(chan types.Log, 64)
	sub, err := c.eth.SubscribeFilterLogs(ctx, q, logs)
	if err != nil {
		log.Warn("pons: curve trade subscription failed; monitor will poll", "err", err)
		return nil
	}
	out := make(chan CurveTrade, 64)
	go func() {
		defer close(out)
		defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case err := <-sub.Err():
				if err != nil {
					log.Warn("pons: curve trade subscription dropped", "err", err)
				}
				return
			case lg := <-logs:
				trade, ok := decodeCurveTrade(lg)
				if !ok {
					continue
				}
				select {
				case out <- trade:
				default:
				}
			}
		}
	}()
	return out
}

// WatchCurveTrades is the lightweight reserve-change signal used by the
// existing sniper exit monitor.
func (c *Client) WatchCurveTrades(ctx context.Context, curve common.Address, log *slog.Logger) <-chan struct{} {
	events := c.WatchCurveTradeEvents(ctx, curve, log)
	if events == nil {
		return nil
	}
	out := make(chan struct{}, 64)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				return
			case _, ok := <-events:
				if !ok {
					return
				}
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}

func decodeCurveTrade(lg types.Log) (CurveTrade, bool) {
	if len(lg.Topics) < 3 {
		return CurveTrade{}, false
	}
	trade := CurveTrade{
		Trader:    common.BytesToAddress(lg.Topics[1].Bytes()),
		Recipient: common.BytesToAddress(lg.Topics[2].Bytes()),
		Block:     lg.BlockNumber,
		LogIndex:  lg.Index,
		TxHash:    lg.TxHash,
	}
	switch lg.Topics[0] {
	case curveBuyTopic:
		var data struct {
			QuoteIn   *big.Int
			TokensOut *big.Int
			Fee       *big.Int
			Tax       *big.Int
		}
		if err := curveABI.UnpackIntoInterface(&data, "CurveBuy", lg.Data); err != nil {
			return CurveTrade{}, false
		}
		trade.IsBuy, trade.TokenAmount, trade.QuoteAmount = true, data.TokensOut, data.QuoteIn
	case curveSellTopic:
		var data struct {
			TokensIn *big.Int
			QuoteOut *big.Int
			Fee      *big.Int
			Tax      *big.Int
		}
		if err := curveABI.UnpackIntoInterface(&data, "CurveSell", lg.Data); err != nil {
			return CurveTrade{}, false
		}
		trade.TokenAmount, trade.QuoteAmount = data.TokensIn, data.QuoteOut
	default:
		return CurveTrade{}, false
	}
	return trade, trade.TokenAmount != nil && trade.QuoteAmount != nil
}

// CurveTradesFromReceipt returns every v2 curve trade emitted by a confirmed
// transaction. Receipt decoding avoids a balance-difference race when an
// emergency sell is already queued behind the buy in the same wallet nonce
// sequence.
func CurveTradesFromReceipt(rcpt *types.Receipt) []CurveTrade {
	if rcpt == nil {
		return nil
	}
	var trades []CurveTrade
	for _, lg := range rcpt.Logs {
		if trade, ok := decodeCurveTrade(*lg); ok {
			trades = append(trades, trade)
		}
	}
	return trades
}
