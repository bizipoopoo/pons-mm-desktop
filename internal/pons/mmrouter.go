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

// RouterLaunchTerms mirrors PonsMMRouter.LaunchTerms. Field names must match
// the ABI tuple so go-ethereum can pack it.
type RouterLaunchTerms struct {
	Params             V2TokenParams
	LaunchConfigId     *big.Int
	PairToken          common.Address
	QuoteIn            *big.Int
	Recipient          common.Address
	SnipeTaxExemptions []common.Address
}

// RouterAtomicBuy mirrors PonsMMRouter.AtomicBuy: one maker's deposit-funded
// buy inside an atomic launch.
type RouterAtomicBuy struct {
	Wallet  common.Address
	QuoteIn *big.Int
}

// BuildRouterLaunch signs router.launch(terms, factory, officialRouter). The
// router forwards the launch to the official pons contracts (which record the
// router as deployer), stores the L2 block it landed in, and hands any change
// back. value must be launchFee + terms.QuoteIn.
func (s *Signer) BuildRouterLaunch(router common.Address, terms RouterLaunchTerms, value *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("launch", terms, common.HexToAddress(LaunchFactory), common.HexToAddress(LaunchAndBuyRouter))
	if err != nil {
		return nil, fmt.Errorf("pack launch: %w", err)
	}
	return s.sign(router, value, data, p)
}

// BuildRouterLaunchAtomic signs router.launchAndBuyAtomic(terms, factory,
// officialRouter, buys): the launch plus every maker buy in one transaction,
// funded from the makers' router deposits. The signer must be each maker's
// registered operator.
func (s *Signer) BuildRouterLaunchAtomic(router common.Address, terms RouterLaunchTerms, buys []RouterAtomicBuy, value *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("launchAndBuyAtomic", terms, common.HexToAddress(LaunchFactory), common.HexToAddress(LaunchAndBuyRouter), buys)
	if err != nil {
		return nil, fmt.Errorf("pack launchAndBuyAtomic: %w", err)
	}
	return s.sign(router, value, data, p)
}

// BuildRouterBuyAfterLaunch signs router.buyAfterLaunch(curve,
// maxBlocksAfterLaunch, self): fills only while the chain is within that many
// L2 blocks of the block the curve launched in through the router.
func (s *Signer) BuildRouterBuyAfterLaunch(router, curve common.Address, maxBlocksAfterLaunch uint64, quoteIn *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("buyAfterLaunch", curve, new(big.Int).SetUint64(maxBlocksAfterLaunch), s.addr)
	if err != nil {
		return nil, fmt.Errorf("pack buyAfterLaunch: %w", err)
	}
	return s.sign(router, quoteIn, data, p)
}

// BuildRouterDeposit signs router.deposit(operator) carrying amount, parking
// ETH for an atomic launch that operator will run.
func (s *Signer) BuildRouterDeposit(router, operator common.Address, amount *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("deposit", operator)
	if err != nil {
		return nil, fmt.Errorf("pack deposit: %w", err)
	}
	return s.sign(router, amount, data, p)
}

// BuildRouterWithdraw signs router.withdraw(amount); zero withdraws everything.
func (s *Signer) BuildRouterWithdraw(router common.Address, amount *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := mmRouterABI.Pack("withdraw", amount)
	if err != nil {
		return nil, fmt.Errorf("pack withdraw: %w", err)
	}
	return s.sign(router, big.NewInt(0), data, p)
}

// RouterDeposit reads a wallet's parked ETH and its registered operator.
func (c *Client) RouterDeposit(ctx context.Context, router, wallet common.Address) (balance *big.Int, operator common.Address, err error) {
	if err = c.callView(ctx, router, &mmRouterABI, &balance, "deposits", wallet); err != nil {
		return nil, common.Address{}, err
	}
	if err = c.callView(ctx, router, &mmRouterABI, &operator, "operatorOf", wallet); err != nil {
		return nil, common.Address{}, err
	}
	return balance, operator, nil
}

// RouterLaunchBlock returns the L2 block a curve launched in through the
// router, or 0 if it was launched elsewhere.
func (c *Client) RouterLaunchBlock(ctx context.Context, router, curve common.Address) (uint64, error) {
	var n *big.Int
	if err := c.callView(ctx, router, &mmRouterABI, &n, "launchBlock", curve); err != nil {
		return 0, err
	}
	return n.Uint64(), nil
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
	QuoteIn, TokensOut       *big.Int
	Refunded                 *big.Int
	L2Block, MaxL2Block      uint64
}

var routedBuyTopic = mmRouterABI.Events["RoutedBuy"].ID

// RoutedBuysFromReceipt decodes every RoutedBuy the router emitted in a
// receipt — one per buyWithin/buyAfterLaunch, or one per maker inside an
// atomic launch.
func RoutedBuysFromReceipt(rcpt *types.Receipt, router common.Address) []RoutedBuy {
	var out []RoutedBuy
	for _, lg := range rcpt.Logs {
		if lg.Address != router || len(lg.Topics) < 4 || lg.Topics[0] != routedBuyTopic {
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
		out = append(out, RoutedBuy{
			Caller:     common.BytesToAddress(lg.Topics[1].Bytes()),
			Curve:      common.BytesToAddress(lg.Topics[2].Bytes()),
			Recipient:  common.BytesToAddress(lg.Topics[3].Bytes()),
			QuoteIn:    data.QuoteIn,
			TokensOut:  data.TokensOut,
			Refunded:   data.Refunded,
			L2Block:    data.L2Block.Uint64(),
			MaxL2Block: data.MaxL2Block.Uint64(),
		})
	}
	return out
}

// RoutedBuyFromReceipt returns the RoutedBuy credited to recipient. ok is
// false for a reverted or unrelated receipt.
func RoutedBuyFromReceipt(rcpt *types.Receipt, router, recipient common.Address) (RoutedBuy, bool) {
	for _, rb := range RoutedBuysFromReceipt(rcpt, router) {
		if rb.Recipient == recipient {
			return rb, true
		}
	}
	return RoutedBuy{}, false
}

// RouterLaunch is the router's Launched event: the L2 block it recorded for
// the curve, which buyAfterLaunch windows are measured from.
type RouterLaunch struct {
	Caller, Token, Curve common.Address
	L2Block              uint64
	QuoteIn, TokensOut   *big.Int
}

var routerLaunchedTopic = mmRouterABI.Events["Launched"].ID

// RouterLaunchFromReceipt decodes the router's Launched event.
func RouterLaunchFromReceipt(rcpt *types.Receipt, router common.Address) (RouterLaunch, bool) {
	for _, lg := range rcpt.Logs {
		if lg.Address != router || len(lg.Topics) < 4 || lg.Topics[0] != routerLaunchedTopic {
			continue
		}
		var data struct {
			L2Block   *big.Int
			QuoteIn   *big.Int
			TokensOut *big.Int
		}
		if err := mmRouterABI.UnpackIntoInterface(&data, "Launched", lg.Data); err != nil {
			continue
		}
		return RouterLaunch{
			Caller:    common.BytesToAddress(lg.Topics[1].Bytes()),
			Token:     common.BytesToAddress(lg.Topics[2].Bytes()),
			Curve:     common.BytesToAddress(lg.Topics[3].Bytes()),
			L2Block:   data.L2Block.Uint64(),
			QuoteIn:   data.QuoteIn,
			TokensOut: data.TokensOut,
		}, true
	}
	return RouterLaunch{}, false
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
