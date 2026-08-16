package pons

import (
	"context"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// Client is a read/write wrapper over a Robinhood Chain RPC endpoint. It holds
// no key; signing lives in the Signer.
type Client struct {
	rpcURL  string
	rpc     *rpc.Client
	eth     *ethclient.Client
	chainID *big.Int
	factory common.Address

	// gas cache, refreshed by a background loop so the buy hot path spends no
	// RPC round trips fetching baseFee + suggested tip.
	gasMu      sync.RWMutex
	gasBase    *big.Int
	gasTip     *big.Int
	gasFetched time.Time
}

// Dial connects to the RPC endpoint and pins the pons factory address.
func Dial(ctx context.Context, rpcURL string) (*Client, error) {
	if rpcURL == "" {
		rpcURL = DefaultRPC
	}
	rc, err := rpc.DialContext(ctx, rpcURL)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", redact(rpcURL), err)
	}
	ec := ethclient.NewClient(rc)
	id, err := ec.ChainID(ctx)
	if err != nil {
		return nil, fmt.Errorf("chain id: %w", err)
	}
	return &Client{
		rpcURL:  rpcURL,
		rpc:     rc,
		eth:     ec,
		chainID: id,
		factory: common.HexToAddress(LaunchFactory),
	}, nil
}

// L1BlockNumber reads the parent-chain block number carried in the latest L2
// header. Robinhood Chain is an Arbitrum Orbit chain: Solidity block.number —
// the domain of pons v1's restrictionsEndBlock — is the L1 block number (~12s
// each), NOT the fast L2 block number that eth_blockNumber returns.
func (c *Client) L1BlockNumber(ctx context.Context) (uint64, error) {
	var head struct {
		L1BlockNumber hexutil.Uint64 `json:"l1BlockNumber"`
	}
	if err := c.rpc.CallContext(ctx, &head, "eth_getBlockByNumber", "latest", false); err != nil {
		return 0, fmt.Errorf("latest header: %w", err)
	}
	return uint64(head.L1BlockNumber), nil
}

func (c *Client) ChainID() *big.Int      { return c.chainID }
func (c *Client) Eth() *ethclient.Client { return c.eth }

// Close releases the underlying RPC connection. A desktop session may run
// several independent strategies, so each strategy owns and closes its client.
func (c *Client) Close() {
	if c.rpc != nil {
		c.rpc.Close()
	}
}

// LaunchInfo is the immutable per-launch data the sniper needs to price and
// route trades.
type LaunchInfo struct {
	Token         common.Address
	Curve         common.Address
	Deployer      common.Address
	PairToken     common.Address
	NativeQuote   bool
	FeeBps        int64
	CreatorTaxBps int64
	Name          string
	Symbol        string
	Decimals      uint8
}

// callView executes a view function on to and unpacks the result into out,
// which is a pointer to the single return value, or a pointer to a struct whose
// fields match multiple return values in order.
func (c *Client) callView(ctx context.Context, to common.Address, a *abi.ABI, out any, method string, args ...any) error {
	data, err := a.Pack(method, args...)
	if err != nil {
		return fmt.Errorf("pack %s: %w", method, err)
	}
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return fmt.Errorf("call %s: %w", method, err)
	}
	if err := a.UnpackIntoInterface(out, method, res); err != nil {
		return fmt.Errorf("unpack %s: %w", method, err)
	}
	return nil
}

// LoadLaunchInfo fills in the immutable curve/token facts (fees, quote asset,
// metadata) for a detected launch.
func (c *Client) LoadLaunchInfo(ctx context.Context, li *LaunchInfo) error {
	var feeBps, taxBps *big.Int
	if err := c.callView(ctx, li.Curve, &curveABI, &feeBps, "feeBps"); err != nil {
		return err
	}
	if err := c.callView(ctx, li.Curve, &curveABI, &taxBps, "creatorTaxBps"); err != nil {
		return err
	}
	li.FeeBps = feeBps.Int64()
	li.CreatorTaxBps = taxBps.Int64()
	li.NativeQuote = li.PairToken == (common.Address{})

	// Metadata is best-effort: a launch is still tradable if a string read fails.
	_ = c.callView(ctx, li.Token, &erc20ABI, &li.Name, "name")
	_ = c.callView(ctx, li.Token, &erc20ABI, &li.Symbol, "symbol")
	var dec uint8
	if err := c.callView(ctx, li.Token, &erc20ABI, &dec, "decimals"); err == nil {
		li.Decimals = dec
	} else {
		li.Decimals = 18
	}
	return nil
}

// Reserves returns the curve's (quoteReserve, tokenReserve). quoteReserve
// includes the phantom amount, so quoteReserve/tokenReserve is the marginal
// price.
func (c *Client) Reserves(ctx context.Context, curve common.Address) (quote, token *big.Int, err error) {
	var out struct {
		QuoteReserve *big.Int
		TokenReserve *big.Int
	}
	if err := c.callView(ctx, curve, &curveABI, &out, "getReserves"); err != nil {
		return nil, nil, err
	}
	return out.QuoteReserve, out.TokenReserve, nil
}

// SnipeTaxBps returns the launch's current anti-snipe buy tax, in bps, for
// tokens bought FOR recipient (exemptions are keyed on the recipient). Every
// launch opens at ~9900 and decays exponentially to 0 over the first ~15s;
// after that it reads 0 forever.
func (c *Client) SnipeTaxBps(ctx context.Context, curve, recipient common.Address) (int64, error) {
	var bps *big.Int
	if err := c.callView(ctx, curve, &curveABI, &bps, "currentSnipeTaxBps", recipient); err != nil {
		return 0, err
	}
	return bps.Int64(), nil
}

// Graduated reports whether the curve has closed (trading moved to Uniswap).
func (c *Client) Graduated(ctx context.Context, curve common.Address) (bool, error) {
	var g bool
	if err := c.callView(ctx, curve, &curveABI, &g, "graduated"); err != nil {
		return false, err
	}
	return g, nil
}

// CurveGraduationThreshold returns the real quote amount required before a v2
// curve graduates to Uniswap.
func (c *Client) CurveGraduationThreshold(ctx context.Context, curve common.Address) (*big.Int, error) {
	var threshold *big.Int
	if err := c.callView(ctx, curve, &curveABI, &threshold, "graduationThreshold"); err != nil {
		return nil, err
	}
	return threshold, nil
}

// CurvePairToken returns the quote asset for a v2 curve. The zero address means
// native ETH; other values require ERC-20 funding and approval.
func (c *Client) CurvePairToken(ctx context.Context, curve common.Address) (common.Address, error) {
	var pair common.Address
	if err := c.callView(ctx, curve, &curveABI, &pair, "pairToken"); err != nil {
		return common.Address{}, err
	}
	return pair, nil
}

// CurveSellableTokens returns the fixed portion of supply allocated to v2
// curve trading. It excludes tokens reserved for graduation liquidity.
func (c *Client) CurveSellableTokens(ctx context.Context, curve common.Address) (*big.Int, error) {
	var tokens *big.Int
	if err := c.callView(ctx, curve, &curveABI, &tokens, "sellableTokens"); err != nil {
		return nil, err
	}
	return tokens, nil
}

// TokenBalance returns the ERC-20 balance of owner for the launch token.
func (c *Client) TokenBalance(ctx context.Context, token, owner common.Address) (*big.Int, error) {
	var bal *big.Int
	if err := c.callView(ctx, token, &erc20ABI, &bal, "balanceOf", owner); err != nil {
		return nil, err
	}
	return bal, nil
}

// TokenSupply returns an ERC-20 token's total supply.
func (c *Client) TokenSupply(ctx context.Context, token common.Address) (*big.Int, error) {
	var supply *big.Int
	if err := c.callView(ctx, token, &erc20ABI, &supply, "totalSupply"); err != nil {
		return nil, err
	}
	return supply, nil
}

// Allowance returns owner's ERC-20 allowance granted to spender for token.
func (c *Client) Allowance(ctx context.Context, token, owner, spender common.Address) (*big.Int, error) {
	var a *big.Int
	if err := c.callView(ctx, token, &erc20ABI, &a, "allowance", owner, spender); err != nil {
		return nil, err
	}
	return a, nil
}

// EthBalance returns the account's native ETH balance in wei.
func (c *Client) EthBalance(ctx context.Context, who common.Address) (*big.Int, error) {
	return c.eth.BalanceAt(ctx, who, nil)
}

// SuggestGas returns a tip cap and fee cap for an EIP-1559 tx, bumping the tip
// by extraTipWei so a snipe outbids ordinary transactions. It reads baseFee and
// the node's suggested tip from the background cache when fresh (< 6s), falling
// back to a live fetch otherwise — so the buy hot path normally costs zero RPC
// round trips here.
func (c *Client) SuggestGas(ctx context.Context, extraTipWei *big.Int) (tip, feeCap *big.Int, err error) {
	base, baseTip, ok := c.cachedGas()
	if !ok {
		base, baseTip, err = c.fetchGas(ctx)
		if err != nil {
			return nil, nil, err
		}
		c.storeGas(base, baseTip)
	}
	tip = new(big.Int).Set(baseTip)
	if extraTipWei != nil {
		tip.Add(tip, extraTipWei)
	}
	// feeCap = 2*base + tip, the standard headroom for one base-fee doubling.
	feeCap = new(big.Int).Add(new(big.Int).Mul(base, big.NewInt(2)), tip)
	return tip, feeCap, nil
}

// fetchGas reads baseFee and the suggested tip live (two round trips).
func (c *Client) fetchGas(ctx context.Context) (base, tip *big.Int, err error) {
	head, err := c.eth.HeaderByNumber(ctx, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("header: %w", err)
	}
	base = head.BaseFee
	if base == nil {
		base = big.NewInt(0)
	}
	tip, err = c.eth.SuggestGasTipCap(ctx)
	if err != nil || tip == nil {
		tip = big.NewInt(1_000_000_000) // 1 gwei fallback
	}
	return base, tip, nil
}

func (c *Client) cachedGas() (base, tip *big.Int, ok bool) {
	c.gasMu.RLock()
	defer c.gasMu.RUnlock()
	if c.gasBase == nil || c.gasTip == nil || time.Since(c.gasFetched) > 6*time.Second {
		return nil, nil, false
	}
	return new(big.Int).Set(c.gasBase), new(big.Int).Set(c.gasTip), true
}

func (c *Client) storeGas(base, tip *big.Int) {
	c.gasMu.Lock()
	defer c.gasMu.Unlock()
	c.gasBase, c.gasTip, c.gasFetched = base, tip, time.Now()
}

// WarmGas keeps the gas cache fresh in the background until ctx ends, so the
// buy/sell hot paths never block on a baseFee/tip fetch.
func (c *Client) WarmGas(ctx context.Context) {
	if base, tip, err := c.fetchGas(ctx); err == nil {
		c.storeGas(base, tip)
	}
	go func() {
		t := time.NewTicker(3 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if base, tip, err := c.fetchGas(ctx); err == nil {
					c.storeGas(base, tip)
				}
			}
		}
	}()
}

// PendingNonce returns the next nonce for who (pending, so back-to-back sends
// do not collide).
func (c *Client) PendingNonce(ctx context.Context, who common.Address) (uint64, error) {
	return c.eth.PendingNonceAt(ctx, who)
}

func redact(u string) string {
	if i := strings.LastIndex(u, "/v2/"); i >= 0 {
		return u[:i+4] + "…"
	}
	return u
}
