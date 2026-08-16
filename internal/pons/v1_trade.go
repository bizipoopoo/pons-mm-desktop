package pons

import (
	"context"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// V1LaunchState is the factory's per-launch record (getLaunchedToken).
type V1LaunchState struct {
	Token                common.Address
	Deployer             common.Address
	PairedToken          common.Address
	PositionManager      common.Address
	PositionId           *big.Int
	DexId                *big.Int
	LaunchConfigId       *big.Int
	RestrictionsEndBlock *big.Int
	Supply               *big.Int
	IsToken0             bool
	PoolFee              *big.Int
	Exists               bool
	InitialBuyAmount     *big.Int
}

// V1LaunchInfo augments a detected launch with token metadata for filtering and
// logging.
type V1LaunchInfo struct {
	Name     string
	Symbol   string
	Decimals uint8
}

// LoadV1TokenMeta reads the launch token's ERC-20 metadata (best-effort).
func (c *Client) LoadV1TokenMeta(ctx context.Context, token common.Address) V1LaunchInfo {
	info := V1LaunchInfo{Decimals: 18}
	_ = c.callView(ctx, token, &erc20ABI, &info.Name, "name")
	_ = c.callView(ctx, token, &erc20ABI, &info.Symbol, "symbol")
	var dec uint8
	if err := c.callView(ctx, token, &erc20ABI, &dec, "decimals"); err == nil {
		info.Decimals = dec
	}
	return info
}

// GetV1Launch reads the factory's launch record for token. The single tuple
// return needs the abigen-style Unpack+ConvertType dance; UnpackIntoInterface
// panics on struct outputs.
func (c *Client) GetV1Launch(ctx context.Context, token common.Address) (V1LaunchState, error) {
	var st V1LaunchState
	data, err := v1FactoryABI.Pack("getLaunchedToken", token)
	if err != nil {
		return st, fmt.Errorf("pack getLaunchedToken: %w", err)
	}
	to := common.HexToAddress(V1Factory)
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return st, fmt.Errorf("call getLaunchedToken: %w", err)
	}
	out, err := v1FactoryABI.Unpack("getLaunchedToken", res)
	if err != nil || len(out) == 0 {
		return st, fmt.Errorf("unpack getLaunchedToken: %v", err)
	}
	st = *abi.ConvertType(out[0], new(V1LaunchState)).(*V1LaunchState)
	if !st.Exists {
		return st, fmt.Errorf("token %s not launched by the v1 factory", token.Hex())
	}
	return st, nil
}

// quoteV1 runs QuoterV2.quoteExactInputSingle as an eth_call and returns
// amountOut. Quoter calls are state-mutating by signature but read-only in
// effect, which is exactly what eth_call executes.
func (c *Client) quoteV1(ctx context.Context, tokenIn, tokenOut common.Address, amountIn *big.Int) (*big.Int, error) {
	type params struct {
		TokenIn           common.Address
		TokenOut          common.Address
		AmountIn          *big.Int
		Fee               *big.Int
		SqrtPriceLimitX96 *big.Int
	}
	data, err := v1QuoterABI.Pack("quoteExactInputSingle", params{
		TokenIn: tokenIn, TokenOut: tokenOut, AmountIn: amountIn,
		Fee: big.NewInt(V1PoolFee), SqrtPriceLimitX96: big.NewInt(0),
	})
	if err != nil {
		return nil, fmt.Errorf("pack quote: %w", err)
	}
	to := common.HexToAddress(V1QuoterV2)
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, fmt.Errorf("quoter call: %w", err)
	}
	var out struct {
		AmountOut               *big.Int
		SqrtPriceX96After       *big.Int
		InitializedTicksCrossed uint32
		GasEstimate             *big.Int
	}
	if err := v1QuoterABI.UnpackIntoInterface(&out, "quoteExactInputSingle", res); err != nil {
		return nil, fmt.Errorf("unpack quote: %w", err)
	}
	return out.AmountOut, nil
}

// QuoteV1Buy prices spending wethIn (ETH) for the launch token.
func (c *Client) QuoteV1Buy(ctx context.Context, token common.Address, wethIn *big.Int) (*big.Int, error) {
	return c.quoteV1(ctx, common.HexToAddress(V1WETH), token, wethIn)
}

// QuoteV1Sell prices selling tokensIn of the launch token back to WETH.
func (c *Client) QuoteV1Sell(ctx context.Context, token common.Address, tokensIn *big.Int) (*big.Int, error) {
	return c.quoteV1(ctx, token, common.HexToAddress(V1WETH), tokensIn)
}

// v1SwapParams is SwapRouter02's ExactInputSingleParams (no deadline field).
type v1SwapParams struct {
	TokenIn           common.Address
	TokenOut          common.Address
	Fee               *big.Int
	Recipient         common.Address
	AmountIn          *big.Int
	AmountOutMinimum  *big.Int
	SqrtPriceLimitX96 *big.Int
}

// BuildV1Buy signs a router exactInputSingle spending ethIn (sent as tx value;
// the router wraps it because tokenIn is WETH9) for at least minTokensOut.
func (s *Signer) BuildV1Buy(token common.Address, ethIn, minTokensOut *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := v1RouterABI.Pack("exactInputSingle", v1SwapParams{
		TokenIn:   common.HexToAddress(V1WETH),
		TokenOut:  token,
		Fee:       big.NewInt(V1PoolFee),
		Recipient: s.addr,
		AmountIn:  ethIn, AmountOutMinimum: minTokensOut,
		SqrtPriceLimitX96: big.NewInt(0),
	})
	if err != nil {
		return nil, fmt.Errorf("pack v1 buy: %w", err)
	}
	return s.sign(common.HexToAddress(V1SwapRouter), ethIn, data, p)
}

// BuildV1Sell signs a router exactInputSingle selling tokensIn for at least
// minWethOut, received as WETH (unwrap separately with BuildWethWithdraw).
// Requires a prior ERC-20 approval of the router.
func (s *Signer) BuildV1Sell(token common.Address, tokensIn, minWethOut *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := v1RouterABI.Pack("exactInputSingle", v1SwapParams{
		TokenIn:   token,
		TokenOut:  common.HexToAddress(V1WETH),
		Fee:       big.NewInt(V1PoolFee),
		Recipient: s.addr,
		AmountIn:  tokensIn, AmountOutMinimum: minWethOut,
		SqrtPriceLimitX96: big.NewInt(0),
	})
	if err != nil {
		return nil, fmt.Errorf("pack v1 sell: %w", err)
	}
	return s.sign(common.HexToAddress(V1SwapRouter), big.NewInt(0), data, p)
}

// BuildWethWithdraw signs WETH.withdraw(amount), unwrapping sell proceeds back
// to native ETH so the next buy can spend them.
func (s *Signer) BuildWethWithdraw(amount *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := v1WethABI.Pack("withdraw", amount)
	if err != nil {
		return nil, fmt.Errorf("pack weth withdraw: %w", err)
	}
	return s.sign(common.HexToAddress(V1WETH), big.NewInt(0), data, p)
}
