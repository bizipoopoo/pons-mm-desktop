package pons

import (
	"context"
	"errors"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/rpc"
)

// BuildRouterBuy signs router.buyWithin(curve, maxL2Block, self) carrying
// quoteIn as value. The router forwards the whole value into curve.buy with
// minTokensOut = 0 and reverts with Expired once the L2 height passes
// maxL2Block, so the block window — not slippage — bounds the fill.
func (s *Signer) BuildRouterBuy(router, curve common.Address, maxL2Block uint64, quoteIn *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("buyWithin", curve, new(big.Int).SetUint64(maxL2Block), s.addr)
	if err != nil {
		return nil, fmt.Errorf("pack buyWithin: %w", err)
	}
	return s.sign(router, quoteIn, data, p)
}

// RouterCurrentL2Block reads ArbSys.arbBlockNumber() through the router, i.e.
// the exact clock buyWithin checks against.
func (c *Client) RouterCurrentL2Block(ctx context.Context, router common.Address) (uint64, error) {
	var n *big.Int
	if err := c.callView(ctx, router, &mmRouterABI, &n, "currentL2Block"); err != nil {
		return 0, err
	}
	return n.Uint64(), nil
}

// RouterOwner returns the router proxy's owner, used as a liveness/identity
// preflight for a Settings-supplied address.
func (c *Client) RouterOwner(ctx context.Context, router common.Address) (common.Address, error) {
	var owner common.Address
	if err := c.callView(ctx, router, &mmRouterABI, &owner, "owner"); err != nil {
		return common.Address{}, err
	}
	return owner, nil
}

// SendRawBatch broadcasts every tx in one JSON-RPC batch request. On this
// chain ordering is first-come-first-served at the sequencer and every RPC
// round trip is about a block, so a launch plus N maker buys must leave in a
// single request rather than N+1. The returned slice is index-aligned with
// txs; a nil entry means the node accepted that tx.
func (c *Client) SendRawBatch(ctx context.Context, txs []*types.Transaction) []error {
	errs := make([]error, len(txs))
	if len(txs) == 0 {
		return errs
	}
	batch := make([]rpc.BatchElem, len(txs))
	for i, tx := range txs {
		raw, err := tx.MarshalBinary()
		if err != nil {
			errs[i] = fmt.Errorf("encode tx: %w", err)
			continue
		}
		var hash common.Hash
		batch[i] = rpc.BatchElem{
			Method: "eth_sendRawTransaction",
			Args:   []any{hexutil.Encode(raw)},
			Result: &hash,
		}
	}
	if err := c.rpc.BatchCallContext(ctx, batch); err != nil {
		for i := range errs {
			if errs[i] == nil {
				errs[i] = fmt.Errorf("batch send: %w", err)
			}
		}
		return errs
	}
	for i := range batch {
		if errs[i] == nil && batch[i].Error != nil {
			errs[i] = batch[i].Error
		}
	}
	return errs
}

// RoutedBuy is the router's receipt of one block-limited buy.
type RoutedBuy struct {
	Caller, Curve, Recipient common.Address
	QuoteIn, TokensOut      *big.Int
	Refunded                *big.Int
	L2Block, MaxL2Block     uint64
}

var routedBuyTopic = mmRouterABI.Events["RoutedBuy"].ID

// RoutedBuyFromReceipt decodes the RoutedBuy event a successful buyWithin
// emitted for recipient. ok is false for a reverted or unrelated receipt.
func RoutedBuyFromReceipt(rcpt *types.Receipt, router, recipient common.Address) (RoutedBuy, bool) {
	for _, lg := range rcpt.Logs {
		if lg.Address != router || len(lg.Topics) < 4 || lg.Topics[0] != routedBuyTopic {
			continue
		}
		if common.BytesToAddress(lg.Topics[3].Bytes()) != recipient {
			continue
		}
		var data struct {
			QuoteIn    *big.Int
			TokensOut  *big.Int
			Refunded   *big.Int
			L2Block    *big.Int
			MaxL2Block *big.Int
		}
		if err := mmRouterABI.UnpackIntoInterface(&data, "RoutedBuy", lg.Data); err != nil {
			continue
		}
		return RoutedBuy{
			Caller:     common.BytesToAddress(lg.Topics[1].Bytes()),
			Curve:      common.BytesToAddress(lg.Topics[2].Bytes()),
			Recipient:  recipient,
			QuoteIn:    data.QuoteIn,
			TokensOut:  data.TokensOut,
			Refunded:   data.Refunded,
			L2Block:    data.L2Block.Uint64(),
			MaxL2Block: data.MaxL2Block.Uint64(),
		}, true
	}
	return RoutedBuy{}, false
}

// ExpiredError is the router's Expired(currentL2Block, maxL2Block) revert.
type ExpiredError struct {
	Current, Max uint64
}

func (e *ExpiredError) Error() string {
	return fmt.Sprintf("router window expired: L2 block %d > max %d", e.Current, e.Max)
}

// DecodeRouterRevert recognizes the router's custom errors in eth_call /
// estimateGas revert data. It returns nil when data is not a router error.
func DecodeRouterRevert(data []byte) error {
	if len(data) < 4 {
		return nil
	}
	for name, e := range mmRouterABI.Errors {
		if string(e.ID[:4]) != string(data[:4]) {
			continue
		}
		vals, err := e.Inputs.Unpack(data[4:])
		if err != nil {
			return fmt.Errorf("router %s (undecodable args)", name)
		}
		if name == "Expired" && len(vals) == 2 {
			return &ExpiredError{Current: vals[0].(*big.Int).Uint64(), Max: vals[1].(*big.Int).Uint64()}
		}
		return fmt.Errorf("router %s", name)
	}
	return nil
}

// RevertData pulls the ABI-encoded revert payload out of a go-ethereum RPC
// error, if present.
func RevertData(err error) []byte {
	var de rpc.DataError
	if !errors.As(err, &de) {
		return nil
	}
	s, ok := de.ErrorData().(string)
	if !ok {
		return nil
	}
	b, decErr := hexutil.Decode(s)
	if decErr != nil {
		return nil
	}
	return b
}
