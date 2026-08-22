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

// V1Launch is a newly detected pons v1 launch, normalized from the factory's
// TokenLaunched log. The pool is live and tradable from this moment (subject to
// the short launch-protection window).
type V1Launch struct {
	Token    common.Address
	Deployer common.Address
	Pool     common.Address
	// PairToken is WETH on every current v1 launch.
	PairToken common.Address
	// RestrictionsEndBlock is the last L1 (parent-chain) block where launch
	// limits apply. The launch itself happened at RestrictionsEndBlock-2; on
	// that first L1 block only the creator's initial buy can execute.
	RestrictionsEndBlock uint64
	// InitialBuyWei is the creator's initial buy, in ETH wei. The strongest
	// available signal of creator commitment (analogous to pump's dev buy).
	InitialBuyWei *big.Int
	Block         uint64
	ObservedAt    time.Time
}

// WatchV1Launches streams TokenLaunched events from the v1 factory, preferring
// a websocket subscription and falling back to polling. The channel closes when
// ctx ends.
func (c *Client) WatchV1Launches(ctx context.Context, log *slog.Logger) <-chan V1Launch {
	out := make(chan V1Launch, 64)
	q := ethereum.FilterQuery{
		Addresses: []common.Address{common.HexToAddress(V1Factory)},
		Topics:    [][]common.Hash{{v1TokenLaunchedTopic}},
	}
	go func() {
		defer close(out)
		if c.isWebsocket() {
			if c.subscribeV1Launches(ctx, q, out, log) {
				return
			}
			log.Warn("pons v1: log subscription unavailable, falling back to polling")
		}
		c.pollV1Launches(ctx, q, out, log)
	}()
	return out
}

func (c *Client) subscribeV1Launches(ctx context.Context, q ethereum.FilterQuery, out chan<- V1Launch, log *slog.Logger) bool {
	logs := make(chan types.Log, 64)
	sub, err := c.eth.SubscribeFilterLogs(ctx, q, logs)
	if err != nil {
		log.Warn("pons v1: SubscribeFilterLogs failed", "err", err)
		return false
	}
	defer sub.Unsubscribe()
	log.Info("pons v1: subscribed to factory launches", "factory", V1Factory)
	for {
		select {
		case <-ctx.Done():
			return true
		case err := <-sub.Err():
			log.Warn("pons v1: launch subscription dropped", "err", err)
			return false
		case lg := <-logs:
			if l, ok := decodeV1Launch(lg); ok {
				emitV1(ctx, out, l)
			}
		}
	}
}

func (c *Client) pollV1Launches(ctx context.Context, q ethereum.FilterQuery, out chan<- V1Launch, log *slog.Logger) {
	from, err := c.eth.BlockNumber(ctx)
	if err != nil {
		log.Warn("pons v1: cannot read head block; starting from 0", "err", err)
		from = 0
	}
	log.Info("pons v1: polling factory launches", "factory", V1Factory, "from_block", from)
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
				log.Debug("pons v1: FilterLogs failed", "err", err)
				continue
			}
			for _, lg := range logs {
				if l, ok := decodeV1Launch(lg); ok {
					emitV1(ctx, out, l)
				}
			}
			from = head + 1
		}
	}
}

func emitV1(ctx context.Context, out chan<- V1Launch, l V1Launch) {
	select {
	case out <- l:
	case <-ctx.Done():
	}
}

func decodeV1Launch(lg types.Log) (V1Launch, bool) {
	if len(lg.Topics) < 4 || lg.Topics[0] != v1TokenLaunchedTopic {
		return V1Launch{}, false
	}
	var data struct {
		PairToken            common.Address
		Pool                 common.Address
		DexId                *big.Int
		LaunchConfigId       *big.Int
		PositionId           *big.Int
		RestrictionsEndBlock *big.Int
		InitialBuyAmount     *big.Int
	}
	if err := v1FactoryABI.UnpackIntoInterface(&data, "TokenLaunched", lg.Data); err != nil {
		return V1Launch{}, false
	}
	return V1Launch{
		Token:                common.HexToAddress(lg.Topics[1].Hex()),
		Deployer:             common.HexToAddress(lg.Topics[2].Hex()),
		Pool:                 data.Pool,
		PairToken:            data.PairToken,
		RestrictionsEndBlock: data.RestrictionsEndBlock.Uint64(),
		InitialBuyWei:        data.InitialBuyAmount,
		Block:                lg.BlockNumber,
		ObservedAt:           time.Now(),
	}, true
}

// PoolTrade is one decoded Uniswap V3 Swap on a launch pool, normalized to the
// launch token's perspective. IsBuy is true when the token left the pool (a
// buyer received tokens for WETH); TokenAmount and WethAmount are absolute
// (unsigned) magnitudes in raw units. Recipient is who received the swap output
// (the trader for a normal router swap); Sender is the router/caller.
type PoolTrade struct {
	Sender      common.Address
	Recipient   common.Address
	IsBuy       bool
	TokenAmount *big.Int
	WethAmount  *big.Int
	Block       uint64
	LogIndex    uint
	TxHash      common.Hash
}

// FilterPoolTrades returns decoded pool swaps in an inclusive block range.
// It complements the live subscription so launch-block swaps cannot be missed.
func (c *Client) FilterPoolTrades(ctx context.Context, pool common.Address, tokenIsToken0 bool, fromBlock, toBlock uint64) ([]PoolTrade, error) {
	if toBlock < fromBlock {
		return nil, nil
	}
	logs, err := c.eth.FilterLogs(ctx, ethereum.FilterQuery{
		Addresses: []common.Address{pool},
		Topics:    [][]common.Hash{{v1SwapTopic}},
		FromBlock: new(big.Int).SetUint64(fromBlock),
		ToBlock:   new(big.Int).SetUint64(toBlock),
	})
	if err != nil {
		return nil, err
	}
	trades := make([]PoolTrade, 0, len(logs))
	for _, lg := range logs {
		if trade, ok := decodePoolTrade(lg, tokenIsToken0); ok {
			trades = append(trades, trade)
		}
	}
	return trades, nil
}

// WatchPoolTrades subscribes to a pool's Swap logs and delivers each one decoded
// and classified as buy/sell from the launch token's perspective. tokenIsToken0
// is launch.isToken0 (read from getLaunchedToken): it fixes which signed swap
// amount is the token leg. Returns nil when the endpoint cannot subscribe.
func (c *Client) WatchPoolTrades(ctx context.Context, pool common.Address, tokenIsToken0 bool, log *slog.Logger) <-chan PoolTrade {
	if !c.isWebsocket() {
		return nil
	}
	q := ethereum.FilterQuery{
		Addresses: []common.Address{pool},
		Topics:    [][]common.Hash{{v1SwapTopic}},
	}
	logs := make(chan types.Log, 64)
	sub, err := c.eth.SubscribeFilterLogs(ctx, q, logs)
	if err != nil {
		log.Warn("pons v1: pool trade subscription failed", "err", err)
		return nil
	}
	out := make(chan PoolTrade, 128)
	go func() {
		defer close(out)
		defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Err():
				return
			case lg := <-logs:
				if t, ok := decodePoolTrade(lg, tokenIsToken0); ok {
					select {
					case out <- t:
					case <-ctx.Done():
						return
					}
				}
			}
		}
	}()
	return out
}

// decodePoolTrade turns a V3 Swap log into a PoolTrade. The Swap event carries
// signed amount0/amount1 (positive = into the pool, negative = out of the pool).
// The launch token leaving the pool (its amount negative) is a buy.
func decodePoolTrade(lg types.Log, tokenIsToken0 bool) (PoolTrade, bool) {
	if len(lg.Topics) < 3 || lg.Topics[0] != v1SwapTopic {
		return PoolTrade{}, false
	}
	var data struct {
		Amount0      *big.Int
		Amount1      *big.Int
		SqrtPriceX96 *big.Int
		Liquidity    *big.Int
		Tick         *big.Int
	}
	if err := v1PoolABI.UnpackIntoInterface(&data, "Swap", lg.Data); err != nil {
		return PoolTrade{}, false
	}
	tokenAmt, wethAmt := data.Amount1, data.Amount0
	if tokenIsToken0 {
		tokenAmt, wethAmt = data.Amount0, data.Amount1
	}
	// Token amount negative => tokens left the pool => a buy.
	isBuy := tokenAmt.Sign() < 0
	return PoolTrade{
		Sender:      common.HexToAddress(lg.Topics[1].Hex()),
		Recipient:   common.HexToAddress(lg.Topics[2].Hex()),
		IsBuy:       isBuy,
		TokenAmount: new(big.Int).Abs(tokenAmt),
		WethAmount:  new(big.Int).Abs(wethAmt),
		Block:       lg.BlockNumber,
		LogIndex:    lg.Index,
		TxHash:      lg.TxHash,
	}, true
}

// PoolTradesFromReceipt returns every v1 pool swap emitted by a confirmed
// transaction. It is the receipt-safe counterpart of the live subscription.
func PoolTradesFromReceipt(rcpt *types.Receipt, pool common.Address, tokenIsToken0 bool) []PoolTrade {
	if rcpt == nil {
		return nil
	}
	var trades []PoolTrade
	for _, lg := range rcpt.Logs {
		if lg.Address != pool {
			continue
		}
		if trade, ok := decodePoolTrade(*lg, tokenIsToken0); ok {
			trades = append(trades, trade)
		}
	}
	return trades
}

// WatchPoolSwaps subscribes to a single V3 pool's Swap logs so the v1 exit
// monitor revalues immediately on any trade. Nil when the endpoint cannot
// subscribe (poll-only mode).
func (c *Client) WatchPoolSwaps(ctx context.Context, pool common.Address, log *slog.Logger) <-chan struct{} {
	if !c.isWebsocket() {
		return nil
	}
	q := ethereum.FilterQuery{
		Addresses: []common.Address{pool},
		Topics:    [][]common.Hash{{v1SwapTopic}},
	}
	logs := make(chan types.Log, 64)
	sub, err := c.eth.SubscribeFilterLogs(ctx, q, logs)
	if err != nil {
		log.Warn("pons v1: pool swap subscription failed; monitor will poll", "err", err)
		return nil
	}
	out := make(chan struct{}, 64)
	go func() {
		defer close(out)
		defer sub.Unsubscribe()
		for {
			select {
			case <-ctx.Done():
				return
			case <-sub.Err():
				return
			case <-logs:
				select {
				case out <- struct{}{}:
				default:
				}
			}
		}
	}()
	return out
}
