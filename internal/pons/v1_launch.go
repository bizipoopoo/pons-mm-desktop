package pons

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// pons v1 token creation. A creator calls launchToken on the factory, paying
// launchFee plus any excess as an atomic initial (dev) buy. The factory deploys
// a fixed-supply ERC-20 and a locked Uniswap V3 pool in the same transaction.
// Verified against the on-chain PonsLaunchFactory source at
// 0xA5aAb3F0c6EeadF30Ef1D3Eb997108E976351feB (Robinhood Chain, chain id 4663).

// v1LaunchABIJSON carries the write + config-read members the market maker
// needs. Trade/read members live in the existing v1FactoryABI (getLaunchedToken,
// graduationStatus, TokenLaunched).
const v1LaunchABIJSON = `[
{"inputs":[{"components":[
	{"name":"name","type":"string"},
	{"name":"symbol","type":"string"},
	{"name":"logo","type":"string"},
	{"name":"description","type":"string"},
	{"components":[
		{"name":"twitter","type":"string"},
		{"name":"telegram","type":"string"},
		{"name":"discord","type":"string"},
		{"name":"website","type":"string"},
		{"name":"farcaster","type":"string"}],
	"name":"socials","type":"tuple"},
	{"name":"feeWallet","type":"address"}],
"name":"params","type":"tuple"},
	{"name":"launchConfigId","type":"uint256"},
	{"name":"dexId","type":"uint256"},
	{"name":"salt","type":"bytes32"}],
"name":"launchToken","outputs":[{"name":"token","type":"address"}],"stateMutability":"payable","type":"function"},
{"inputs":[{"components":[
	{"name":"name","type":"string"},
	{"name":"symbol","type":"string"},
	{"name":"logo","type":"string"},
	{"name":"description","type":"string"},
	{"components":[
		{"name":"twitter","type":"string"},
		{"name":"telegram","type":"string"},
		{"name":"discord","type":"string"},
		{"name":"website","type":"string"},
		{"name":"farcaster","type":"string"}],
	"name":"socials","type":"tuple"},
	{"name":"feeWallet","type":"address"}],
"name":"params","type":"tuple"},
	{"name":"launchConfigId","type":"uint256"},
	{"name":"dexId","type":"uint256"},
	{"name":"salt","type":"bytes32"},
	{"name":"tokenDeployer","type":"address"}],
"name":"predictTokenAddress","outputs":[{"name":"","type":"address"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"launchFee","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"launchEnabled","outputs":[{"name":"","type":"bool"}],"stateMutability":"view","type":"function"},
{"inputs":[{"name":"launcher","type":"address"}],"name":"whitelistedLaunchers","outputs":[{"name":"enabled","type":"bool"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"launchConfigCount","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
{"inputs":[{"name":"id","type":"uint256"}],"name":"getLaunchConfig","outputs":[{"components":[
	{"name":"pairToken","type":"address"},
	{"name":"graduationThreshold","type":"uint256"},
	{"name":"initialTick","type":"int24"},
	{"name":"supply","type":"uint256"},
	{"name":"maxWalletBps","type":"uint16"},
	{"name":"maxTxBps","type":"uint16"},
	{"name":"restrictionBlocks","type":"uint32"},
	{"name":"reservedFee","type":"uint24"},
	{"name":"enabled","type":"bool"},
	{"name":"routerRequiresDeadline","type":"bool"}],
"name":"","type":"tuple"}],"stateMutability":"view","type":"function"},
{"inputs":[],"name":"dexConfigCount","outputs":[{"name":"","type":"uint256"}],"stateMutability":"view","type":"function"},
{"inputs":[{"name":"id","type":"uint256"}],"name":"getDexConfig","outputs":[{"components":[
	{"name":"name","type":"string"},
	{"name":"factory","type":"address"},
	{"name":"positionManager","type":"address"},
	{"name":"swapRouter","type":"address"},
	{"name":"poolFee","type":"uint24"},
	{"name":"tickSpacing","type":"int24"},
	{"name":"enabled","type":"bool"}],
"name":"","type":"tuple"}],"stateMutability":"view","type":"function"}
]`

var v1LaunchABI = mustABI(v1LaunchABIJSON)

// V1Socials mirrors the factory's Socials tuple (all optional).
type V1Socials struct {
	Twitter   string
	Telegram  string
	Discord   string
	Website   string
	Farcaster string
}

// V1TokenParams is the launchToken params tuple. FeeWallet, when non-zero,
// receives the atomic initial buy and the locked-position LP fees; a zero value
// sends the initial buy to the launching wallet.
type V1TokenParams struct {
	Name        string
	Symbol      string
	Logo        string
	Description string
	Socials     V1Socials
	FeeWallet   common.Address
}

// V1LaunchConfig is one owner-managed launch config (getLaunchConfig). The
// market maker reads GraduationThreshold, Supply, MaxWalletBps and
// RestrictionBlocks from the active config rather than hardcoding them.
type V1LaunchConfig struct {
	PairToken              common.Address
	GraduationThreshold    *big.Int
	InitialTick            *big.Int
	Supply                 *big.Int
	MaxWalletBps           uint16
	MaxTxBps               uint16
	RestrictionBlocks      uint32
	ReservedFee            *big.Int
	Enabled                bool
	RouterRequiresDeadline bool
}

// LaunchFee reads the current v1 launch fee in wei.
func (c *Client) LaunchFee(ctx context.Context) (*big.Int, error) {
	var fee *big.Int
	if err := c.callView(ctx, common.HexToAddress(V1Factory), &v1LaunchABI, &fee, "launchFee"); err != nil {
		return nil, err
	}
	return fee, nil
}

// CanLaunch reports whether who may launch right now: launches are open to all
// when launchEnabled is true, otherwise only to whitelisted addresses. Mirrors
// the factory's own gate so a create flow can preflight instead of reverting
// with NotWhitelisted.
func (c *Client) CanLaunch(ctx context.Context, who common.Address) (bool, error) {
	var enabled bool
	if err := c.callView(ctx, common.HexToAddress(V1Factory), &v1LaunchABI, &enabled, "launchEnabled"); err != nil {
		return false, err
	}
	if enabled {
		return true, nil
	}
	var wl bool
	if err := c.callView(ctx, common.HexToAddress(V1Factory), &v1LaunchABI, &wl, "whitelistedLaunchers", who); err != nil {
		return false, err
	}
	return wl, nil
}

// GetLaunchConfig reads config id from the factory.
func (c *Client) GetLaunchConfig(ctx context.Context, id uint64) (V1LaunchConfig, error) {
	var cfg V1LaunchConfig
	data, err := v1LaunchABI.Pack("getLaunchConfig", new(big.Int).SetUint64(id))
	if err != nil {
		return cfg, fmt.Errorf("pack getLaunchConfig: %w", err)
	}
	to := common.HexToAddress(V1Factory)
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return cfg, fmt.Errorf("call getLaunchConfig: %w", err)
	}
	out, err := v1LaunchABI.Unpack("getLaunchConfig", res)
	if err != nil || len(out) == 0 {
		return cfg, fmt.Errorf("unpack getLaunchConfig: %v", err)
	}
	cfg = *abi.ConvertType(out[0], new(V1LaunchConfig)).(*V1LaunchConfig)
	return cfg, nil
}

// GraduationStatus reads how much paired principal (WETH) sits against the
// token, the graduation threshold, and whether it has graduated. The market
// maker uses this to know when the pool has been pushed past 4.2 ETH.
func (c *Client) GraduationStatus(ctx context.Context, token common.Address) (principal, threshold *big.Int, graduated bool, err error) {
	data, err := v1FactoryABI.Pack("graduationStatus", token)
	if err != nil {
		return nil, nil, false, fmt.Errorf("pack graduationStatus: %w", err)
	}
	to := common.HexToAddress(V1Factory)
	res, err := c.eth.CallContract(ctx, ethereum.CallMsg{To: &to, Data: data}, nil)
	if err != nil {
		return nil, nil, false, fmt.Errorf("call graduationStatus: %w", err)
	}
	var out struct {
		PairedPrincipal *big.Int
		Threshold       *big.Int
		Graduated       bool
	}
	if err := v1FactoryABI.UnpackIntoInterface(&out, "graduationStatus", res); err != nil {
		return nil, nil, false, fmt.Errorf("unpack graduationStatus: %w", err)
	}
	return out.PairedPrincipal, out.Threshold, out.Graduated, nil
}

// PredictV1Token computes the CREATE2 token address a launch would deploy to,
// for the given deployer/salt. Useful for vanity addresses; the engine normally
// reads the address back from the launch receipt instead.
func (c *Client) PredictV1Token(ctx context.Context, params V1TokenParams, launchConfigID, dexID uint64, salt [32]byte, deployer common.Address) (common.Address, error) {
	var addr common.Address
	if err := c.callView(ctx, common.HexToAddress(V1Factory), &v1LaunchABI, &addr,
		"predictTokenAddress", params,
		new(big.Int).SetUint64(launchConfigID), new(big.Int).SetUint64(dexID), salt, deployer); err != nil {
		return common.Address{}, err
	}
	return addr, nil
}

// BuildV1Launch signs a factory.launchToken(params, configId, dexId, salt). The
// tx value must be launchFee + the intended initial buy in wei; the factory
// forwards the excess over the fee as an atomic dev buy for the launcher (or
// params.FeeWallet when set).
func (s *Signer) BuildV1Launch(params V1TokenParams, launchConfigID, dexID uint64, salt [32]byte, value *big.Int, p TxParams) (*types.Transaction, error) {
	data, err := v1LaunchABI.Pack("launchToken", params,
		new(big.Int).SetUint64(launchConfigID), new(big.Int).SetUint64(dexID), salt)
	if err != nil {
		return nil, fmt.Errorf("pack launchToken: %w", err)
	}
	return s.sign(common.HexToAddress(V1Factory), value, data, p)
}

// V1Launched is the token + pool a launch produced, read back from the receipt.
type V1Launched struct {
	Token                common.Address
	Pool                 common.Address
	PairToken            common.Address
	RestrictionsEndBlock uint64
	InitialBuyWei        *big.Int
	TxHash               common.Hash
	Block                uint64
}

// WaitReceipt polls for a transaction receipt until it is mined or ctx ends,
// and fails on a reverted (status 0) transaction.
func (c *Client) WaitReceipt(ctx context.Context, hash common.Hash, timeout time.Duration) (*types.Receipt, error) {
	return c.WaitReceiptEvery(ctx, hash, timeout, 500*time.Millisecond)
}

// WaitReceiptEvery polls at a caller-chosen interval. Latency-critical paths
// (the launch receipt that gates the first maker buys) poll much faster than
// the default; Robinhood Chain produces blocks roughly every 100ms.
func (c *Client) WaitReceiptEvery(ctx context.Context, hash common.Hash, timeout, poll time.Duration) (*types.Receipt, error) {
	deadline := time.Now().Add(timeout)
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		rcpt, err := c.eth.TransactionReceipt(ctx, hash)
		if err == nil && rcpt != nil {
			if rcpt.Status == types.ReceiptStatusFailed {
				return rcpt, fmt.Errorf("transaction %s reverted on-chain", hash.Hex())
			}
			return rcpt, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("receipt for %s not found within %s", hash.Hex(), timeout)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

// LaunchedFromReceipt extracts the token + pool from a launch receipt's
// TokenLaunched log. Reuses the v1 factory event decoder.
func LaunchedFromReceipt(rcpt *types.Receipt) (V1Launched, bool) {
	for _, lg := range rcpt.Logs {
		if l, ok := decodeV1Launch(*lg); ok {
			return V1Launched{
				Token:                l.Token,
				Pool:                 l.Pool,
				PairToken:            l.PairToken,
				RestrictionsEndBlock: l.RestrictionsEndBlock,
				InitialBuyWei:        l.InitialBuyWei,
				TxHash:               rcpt.TxHash,
				Block:                rcpt.BlockNumber.Uint64(),
			}, true
		}
	}
	return V1Launched{}, false
}
