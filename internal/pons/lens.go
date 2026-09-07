package pons

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"golang.org/x/time/rate"
)

// PonsBalanceLens runtime is injected at this dummy address only when the
// deployed BalanceLens has no code on the connected chain.
var balanceLensAddr = common.HexToAddress("0x0000000000000000000000000000000000Ba1a4e")

// Runtime bytecode of PonsBalanceLens (solc 0.8.28, shanghai). Keep in sync
// with contracts/out/PonsBalanceLens.sol/PonsBalanceLens.json deployedBytecode.
var balanceLensRuntime = common.FromHex("0x608060405234801561000f575f5ffd5b506004361061003f575f3560e01c806317c7013d146100435780633ad206cc1461006c5780633dfbc4fc1461007f575b5f5ffd5b61005661005136600461046d565b6100a0565b60405161006391906104e6565b60405180910390f35b61005661007a366004610527565b61015d565b61009261008d366004610527565b61020d565b604051610063929190610576565b6060818067ffffffffffffffff8111156100bc576100bc6105a3565b6040519080825280602002602001820160405280156100e5578160200160208202803683370190505b5091505f5b8181101561015557848482818110610104576101046105d0565b905060200201602081019061011991906105fd565b73ffffffffffffffffffffffffffffffffffffffff1631838281518110610142576101426105d0565b60209081029190910101526001016100ea565b505092915050565b6060818067ffffffffffffffff811115610179576101796105a3565b6040519080825280602002602001820160405280156101a2578160200160208202803683370190505b5091505f5b81811015610204576101df868686848181106101c5576101c56105d0565b90506020020160208101906101da91906105fd565b610368565b8382815181106101f1576101f16105d0565b60209081029190910101526001016101a7565b50509392505050565b606080828067ffffffffffffffff81111561022a5761022a6105a3565b604051908082528060200260200182016040528015610253578160200160208202803683370190505b5092508067ffffffffffffffff81111561026f5761026f6105a3565b604051908082528060200260200182016040528015610298578160200160208202803683370190505b50915073ffffffffffffffffffffffffffffffffffffffff861615155f5b8281101561035d578686828181106102d0576102d06105d0565b90506020020160208101906102e591906105fd565b73ffffffffffffffffffffffffffffffffffffffff163185828151811061030e5761030e6105d0565b602002602001018181525050811561035557610336888888848181106101c5576101c56105d0565b848281518110610348576103486105d0565b6020026020010181815250505b6001016102b6565b505050935093915050565b6040517f70a0823100000000000000000000000000000000000000000000000000000000815273ffffffffffffffffffffffffffffffffffffffff82811660048301525f91908416906370a0823190602401602060405180830381865afa925050508015610411575060408051601f3d9081017fffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffe016820190925261040e91810190610616565b60015b61041c57505f61041f565b90505b92915050565b5f5f83601f840112610435575f5ffd5b50813567ffffffffffffffff81111561044c575f5ffd5b6020830191508360208260051b8501011115610466575f5ffd5b9250929050565b5f5f6020838503121561047e575f5ffd5b823567ffffffffffffffff811115610494575f5ffd5b6104a085828601610425565b90969095509350505050565b5f8151808452602084019350602083015f5b828110156104dc5781518652602095860195909101906001016104be565b5093949350505050565b602081525f6104f860208301846104ac565b9392505050565b803573ffffffffffffffffffffffffffffffffffffffff81168114610522575f5ffd5b919050565b5f5f5f60408486031215610539575f5ffd5b610542846104ff565b9250602084013567ffffffffffffffff81111561055d575f5ffd5b61056986828701610425565b9497909650939450505050565b604081525f61058860408301856104ac565b828103602084015261059a81856104ac565b95945050505050565b7f4e487b71000000000000000000000000000000000000000000000000000000005f52604160045260245ffd5b7f4e487b71000000000000000000000000000000000000000000000000000000005f52603260045260245ffd5b5f6020828403121561060d575f5ffd5b6104f8826104ff565b5f60208284031215610626575f5ffd5b505191905056fea26469706673582212205eea74d9180b4413c85851813dd6e5c1aa782df4da9080751cbf3af8c1eb30bf64736f6c634300081c0033")

const (
	// One eth_call can scan this many accounts. 50 makers is one call; a
	// 500-wallet vault is four. BALANCE + balanceOf stays well under node
	// eth_call gas caps at this size.
	balanceLensChunk = 128
	// Nonce cannot be read from the lens contract. Individual
	// eth_getTransactionCount calls stay well under QuickNode's 50/s cap.
	// JSON-RPC batches are avoided: some providers (including QuickNode on
	// HTTP) answer a batch with a single error object or a truncated array,
	// and go-ethereum then reports "response batch did not contain a
	// response to this call" for the unmatched IDs.
	nonceRPCRate  = 20
	nonceRPCBurst = 8
)

// EthBalances returns native balances for addrs in one (or few) eth_call(s)
// through PonsBalanceLens, instead of one eth_getBalance per wallet.
func (c *Client) EthBalances(ctx context.Context, addrs []common.Address) ([]*big.Int, error) {
	if len(addrs) == 0 {
		return []*big.Int{}, nil
	}
	out := make([]*big.Int, len(addrs))
	for i := 0; i < len(addrs); i += balanceLensChunk {
		end := i + balanceLensChunk
		if end > len(addrs) {
			end = len(addrs)
		}
		part, err := c.lensUintArray(ctx, "ethBalances", addrs[i:end])
		if err != nil {
			return nil, err
		}
		copy(out[i:], part)
	}
	return out, nil
}

// TokenBalances returns ERC-20 balances for addrs in one (or few) eth_call(s).
func (c *Client) TokenBalances(ctx context.Context, token common.Address, addrs []common.Address) ([]*big.Int, error) {
	if len(addrs) == 0 {
		return []*big.Int{}, nil
	}
	out := make([]*big.Int, len(addrs))
	for i := 0; i < len(addrs); i += balanceLensChunk {
		end := i + balanceLensChunk
		if end > len(addrs) {
			end = len(addrs)
		}
		part, err := c.lensTokenArray(ctx, token, addrs[i:end])
		if err != nil {
			return nil, err
		}
		copy(out[i:], part)
	}
	return out, nil
}

// SnapshotBalances returns native and (when token is non-zero) ERC-20 balances
// for addrs in a single eth_call per chunk.
func (c *Client) SnapshotBalances(ctx context.Context, token common.Address, addrs []common.Address) (native, tokens []*big.Int, err error) {
	if len(addrs) == 0 {
		return []*big.Int{}, []*big.Int{}, nil
	}
	native = make([]*big.Int, len(addrs))
	tokens = make([]*big.Int, len(addrs))
	for i := 0; i < len(addrs); i += balanceLensChunk {
		end := i + balanceLensChunk
		if end > len(addrs) {
			end = len(addrs)
		}
		n, t, err := c.lensSnapshot(ctx, token, addrs[i:end])
		if err != nil {
			return nil, nil, err
		}
		copy(native[i:], n)
		copy(tokens[i:], t)
	}
	return native, tokens, nil
}

// PendingNonces reads pending transaction counts one RPC at a time, paced
// below typical provider request caps. Batching is intentionally not used
// here; see nonceRPCRate.
func (c *Client) PendingNonces(ctx context.Context, addrs []common.Address) ([]uint64, error) {
	out := make([]uint64, len(addrs))
	if len(addrs) == 0 {
		return out, nil
	}
	lim := rate.NewLimiter(rate.Limit(nonceRPCRate), nonceRPCBurst)
	for i, addr := range addrs {
		if err := lim.Wait(ctx); err != nil {
			return nil, err
		}
		n, err := c.PendingNonce(ctx, addr)
		if err != nil {
			return nil, fmt.Errorf("nonce %s: %w", addr.Hex(), err)
		}
		out[i] = n
	}
	return out, nil
}

func (c *Client) lensUintArray(ctx context.Context, method string, addrs []common.Address) ([]*big.Int, error) {
	data, err := balanceLensABI.Pack(method, addrs)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", method, err)
	}
	res, err := c.callLens(ctx, data)
	if err != nil {
		return nil, err
	}
	var out []*big.Int
	if err := balanceLensABI.UnpackIntoInterface(&out, method, res); err != nil {
		return nil, fmt.Errorf("unpack %s: %w", method, err)
	}
	if len(out) != len(addrs) {
		return nil, fmt.Errorf("%s: got %d balances, want %d", method, len(out), len(addrs))
	}
	return copyInts(out), nil
}

func (c *Client) lensTokenArray(ctx context.Context, token common.Address, addrs []common.Address) ([]*big.Int, error) {
	data, err := balanceLensABI.Pack("tokenBalances", token, addrs)
	if err != nil {
		return nil, fmt.Errorf("pack tokenBalances: %w", err)
	}
	res, err := c.callLens(ctx, data)
	if err != nil {
		return nil, err
	}
	var out []*big.Int
	if err := balanceLensABI.UnpackIntoInterface(&out, "tokenBalances", res); err != nil {
		return nil, fmt.Errorf("unpack tokenBalances: %w", err)
	}
	if len(out) != len(addrs) {
		return nil, fmt.Errorf("tokenBalances: got %d balances, want %d", len(out), len(addrs))
	}
	return copyInts(out), nil
}

func (c *Client) lensSnapshot(ctx context.Context, token common.Address, addrs []common.Address) (native, tokens []*big.Int, err error) {
	data, err := balanceLensABI.Pack("snapshot", token, addrs)
	if err != nil {
		return nil, nil, fmt.Errorf("pack snapshot: %w", err)
	}
	res, err := c.callLens(ctx, data)
	if err != nil {
		return nil, nil, err
	}
	vals, err := balanceLensABI.Unpack("snapshot", res)
	if err != nil {
		return nil, nil, fmt.Errorf("unpack snapshot: %w", err)
	}
	if len(vals) != 2 {
		return nil, nil, fmt.Errorf("unpack snapshot: got %d values", len(vals))
	}
	native, _ = vals[0].([]*big.Int)
	tokens, _ = vals[1].([]*big.Int)
	if len(native) != len(addrs) || len(tokens) != len(addrs) {
		return nil, nil, fmt.Errorf("snapshot: got %d/%d balances, want %d", len(native), len(tokens), len(addrs))
	}
	return copyInts(native), copyInts(tokens), nil
}

func (c *Client) callLens(ctx context.Context, data []byte) ([]byte, error) {
	to := common.HexToAddress(BalanceLens)
	arg := map[string]any{
		"to":   to,
		"data": hexutil.Bytes(data),
	}
	var hex hexutil.Bytes
	if err := c.rpc.CallContext(ctx, &hex, "eth_call", arg, "latest"); err == nil && len(hex) > 0 {
		return hex, nil
	}
	// Fallback: inject the same bytecode if the RPC has no code at BalanceLens
	// (wrong chain, a fork, or an older node).
	dummy := balanceLensAddr
	arg["to"] = dummy
	overrides := map[common.Address]ethereum.OverrideAccount{
		dummy: {Code: balanceLensRuntime},
	}
	if err := c.rpc.CallContext(ctx, &hex, "eth_call", arg, "latest", overrides); err != nil {
		return nil, fmt.Errorf("balance lens eth_call: %w", err)
	}
	return hex, nil
}

func copyInts(in []*big.Int) []*big.Int {
	out := make([]*big.Int, len(in))
	for i, v := range in {
		if v == nil {
			out[i] = big.NewInt(0)
			continue
		}
		out[i] = new(big.Int).Set(v)
	}
	return out
}
