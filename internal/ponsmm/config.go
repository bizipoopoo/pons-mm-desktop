// Package ponsmm is an automated launch-and-market-make engine for the pons v1
// direct-to-Uniswap stack and the pons v2 bonding-curve stack on Robinhood Chain.
//
// This is a WRITE system: every action spends real ETH and gas. It trades a
// creator's own token against arriving retail flow. Use it deliberately.
package ponsmm

import (
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

const (
	ProtocolV1 = "v1"
	ProtocolV2 = "v2"
)

// Retail-response strategies for the unprofitable retail-buy case.
const (
	RetailResponseDistribute = "distribute"
	RetailResponseTarget     = "target"
)

// Config is the full ponsmm configuration, loaded from YAML.
type Config struct {
	// Protocol selects the pons launch stack. Empty is treated as v1 for
	// backward compatibility with configurations written before v2 support.
	Protocol string `yaml:"protocol"`
	// RPCEndpoint is the Robinhood Chain RPC. Use a wss:// endpoint so the
	// engine can subscribe to live pool trades; an http(s) endpoint forces
	// slower poll-only monitoring.
	RPCEndpoint string `yaml:"rpc_endpoint"`
	// KeysFile holds one hex private key per line. The FIRST key is the
	// treasury/deployer (it launches the token and funds the others); the rest
	// are the market-making wallets.
	KeysFile string `yaml:"keys_file"`

	// Token metadata written at launch.
	Token TokenConfig `yaml:"token"`

	// LaunchConfigID / DexID select the factory config (both default 0, the
	// only live config: WETH pair, 1% fee, 1B supply, 4.2 ETH threshold).
	LaunchConfigID uint64 `yaml:"launch_config_id"`
	DexID          uint64 `yaml:"dex_id"`

	// DevBuyETH is an optional atomic initial buy performed inside the launch
	// transaction (paid on top of the launch fee). On v2 it routes the launch
	// through the official launch-and-buy router so the treasury's first buy
	// cannot be front-run by launch snipers.
	DevBuyETH float64 `yaml:"dev_buy_eth"`

	// BundleMode (v2 only) routes the launch through our PonsMMRouter so the
	// chain itself, not this client, decides which maker buys count as "at
	// launch". The official contracts then record the router as the token's
	// deployer; the creator fee recipient and every token stay on our wallets.
	//   ""/"none":  launch directly from the treasury, makers buy after the receipt.
	//   "window":   the router records the launch's L2 block; maker buys are
	//               pre-signed against the CREATE2-predicted curve, broadcast in
	//               the same JSON-RPC batch, and revert on-chain unless they land
	//               within BundleMaxBlocks blocks of the launch. No slippage.
	//   "atomic":   makers first park their spend in the router; the launch and
	//               every maker buy then execute in ONE transaction. Nothing can
	//               trade between the launch and our fills.
	BundleMode      string `yaml:"bundle_mode"`
	BundleMaxBlocks int    `yaml:"bundle_max_blocks"` // window mode: blocks after launch, default 3
	MMRouter        string `yaml:"mm_router"`         // PonsMMRouter proxy; empty uses the built-in default

	// Accumulation.
	BuyFraction        float64       `yaml:"buy_fraction"`        // fraction of a wallet's spendable ETH per buy (default 0.99)
	AccumulateInterval time.Duration `yaml:"accumulate_interval"` // minimum cadence between accumulation rounds (default 100ms)
	ConcurrentBuys     bool          `yaml:"concurrent_buys"`     // buy from every funded maker in the same round (default true)
	ChipTarget         float64       `yaml:"chip_target"`         // target fraction of total supply to hold before easing off (default 0.9)
	Graduate           bool          `yaml:"graduate"`            // keep buying until the pool reaches the graduation threshold

	// Liquidation. A profitable retail buy (full-exit quote covering every fee
	// paid) always triggers an immediate concurrent full exit; otherwise the
	// engine clears 4-6 wallets per batch at SellInterval cadence.
	SellInterval    time.Duration `yaml:"sell_interval"`    // minimum cadence between sell rounds (default 1s)
	ConcurrentSells bool          `yaml:"concurrent_sells"` // clear wallets in each batch concurrently (default true)

	// RetailResponse switches what happens when a retail buy arrives while the
	// full-exit quote does NOT yet cover total costs (the profitable case
	// always exits everything immediately, in both modes):
	//   - "distribute" (default): the original strategy — clear wallets in
	//     slow batches of 4-6 until retail sells or nothing is left.
	//   - "target": act by RetailTargetRatio relative to the retail buyers'
	//     average buy price (v2 curves only).
	RetailResponse string `yaml:"retail_response"`
	// RetailTargetRatio parameterizes the "target" response:
	//   0     do nothing;
	//   -0.1  sell whole wallets, adding one at a time, until the projected
	//         curve price is pushed to avg*(1-0.1), then execute the batch;
	//   +0.1  buy just enough to lift the curve price to avg*(1+0.1).
	RetailTargetRatio float64 `yaml:"retail_target_ratio"`

	// Execution.
	SlippageBps     int64   `yaml:"slippage_bps"`      // buy slippage tolerance in bps (default 1500 = 15%); sells use 9999 bps
	PriorityTipGwei float64 `yaml:"priority_tip_gwei"` // extra priority tip added to suggested gas (default 1)
	GasReserveETH   float64 `yaml:"gas_reserve_eth"`   // ETH held back per wallet for gas, never spent on buys (default 0.002)
}

// TokenConfig is the launch metadata.
type TokenConfig struct {
	Name        string        `yaml:"name"`
	Symbol      string        `yaml:"symbol"`
	Logo        string        `yaml:"logo"`
	Description string        `yaml:"description"`
	Socials     SocialsConfig `yaml:"socials"`
	// FeeWallet receives the atomic initial buy and the locked-position LP
	// fees. Empty defaults to the deployer.
	FeeWallet string `yaml:"fee_wallet"`
}

// SocialsConfig mirrors the factory's Socials tuple.
type SocialsConfig struct {
	Twitter   string `yaml:"twitter"`
	Telegram  string `yaml:"telegram"`
	Discord   string `yaml:"discord"`
	Website   string `yaml:"website"`
	Farcaster string `yaml:"farcaster"`
}

// DefaultConfig returns conservative operational defaults for a new strategy.
func DefaultConfig() Config {
	return Config{
		Protocol:           ProtocolV2,
		BuyFraction:        0.99,
		AccumulateInterval: 100 * time.Millisecond,
		ConcurrentBuys:     true,
		BundleMaxBlocks:    DefaultBundleMaxBlocks,
		ChipTarget:         0.9,
		Graduate:           true,
		SellInterval:       time.Second,
		ConcurrentSells:    true,
		SlippageBps:        1500,
		PriorityTipGwei:    1,
		GasReserveETH:      0.002,
	}
}

// LoadConfig reads and validates a ponsmm YAML config, filling defaults.
func LoadConfig(path string) (*Config, error) {
	defaults := DefaultConfig()
	c := &defaults
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if err := c.Validate(true); err != nil {
		return nil, err
	}
	return c, nil
}

// Validate checks execution parameters. requireLaunchMetadata should be true
// for a launch and false when binding an already launched token/pool.
func (c *Config) Validate(requireLaunchMetadata bool) error {
	protocol := c.Protocol
	if protocol == "" {
		protocol = ProtocolV1
	}
	if protocol != ProtocolV1 && protocol != ProtocolV2 {
		return fmt.Errorf("protocol must be v1 or v2")
	}
	if c.RPCEndpoint == "" {
		return fmt.Errorf("rpc_endpoint is required")
	}
	if requireLaunchMetadata && (c.Token.Name == "" || c.Token.Symbol == "") {
		return fmt.Errorf("token.name and token.symbol are required")
	}
	if c.FeeWalletAddr() == nil && c.Token.FeeWallet != "" {
		return fmt.Errorf("token.fee_wallet is not a valid address: %q", c.Token.FeeWallet)
	}
	if c.BuyFraction <= 0 || c.BuyFraction > 1 {
		return fmt.Errorf("buy_fraction must be in (0, 1]")
	}
	if c.AccumulateInterval <= 0 || c.SellInterval <= 0 {
		return fmt.Errorf("accumulate_interval and sell_interval must be > 0")
	}
	if c.SlippageBps < 0 || c.SlippageBps >= 10_000 {
		return fmt.Errorf("slippage_bps must be in [0, 10000)")
	}
	if c.GasReserveETH < 0 || c.PriorityTipGwei < 0 || c.DevBuyETH < 0 {
		return fmt.Errorf("ETH and gas values must not be negative")
	}
	switch c.RetailResponseName() {
	case RetailResponseDistribute:
	case RetailResponseTarget:
		if protocol != ProtocolV2 {
			return fmt.Errorf("the price-target retail response requires the v2 bonding curve")
		}
		if c.RetailTargetRatio <= -1 || c.RetailTargetRatio > 10 {
			return fmt.Errorf("retail_target_ratio must be in (-1, 10]")
		}
	default:
		return fmt.Errorf("retail_response must be %q or %q", RetailResponseDistribute, RetailResponseTarget)
	}
	switch mode := c.BundleModeName(); mode {
	case BundleOff:
	case BundleWindow, BundleAtomic:
		if protocol != ProtocolV2 {
			return fmt.Errorf("bundled launch buys require the v2 bonding curve")
		}
		if c.MMRouter != "" && !common.IsHexAddress(c.MMRouter) {
			return fmt.Errorf("mm_router is not a valid address: %q", c.MMRouter)
		}
		if mode == BundleWindow {
			if n := c.BundleMaxBlocksOrDefault(); n < 1 || n > MaxBundleMaxBlocks {
				return fmt.Errorf("bundle_max_blocks must be in [1, %d]", MaxBundleMaxBlocks)
			}
		}
	default:
		return fmt.Errorf("bundle_mode must be %q, %q or %q", BundleOff, BundleWindow, BundleAtomic)
	}
	return nil
}

// Bundle modes; see Config.BundleMode.
const (
	BundleOff    = "none"
	BundleWindow = "window"
	BundleAtomic = "atomic"
)

// Bundle window bounds. One L2 block is ~100ms on Robinhood Chain; a window
// longer than 50 blocks (~5s) after the launch no longer protects anything.
const (
	DefaultBundleMaxBlocks = 3
	MaxBundleMaxBlocks     = 50
)

// BundleModeName normalises BundleMode, treating empty as off.
func (c *Config) BundleModeName() string {
	switch m := strings.ToLower(strings.TrimSpace(c.BundleMode)); m {
	case "", BundleOff, "off", "false":
		return BundleOff
	default:
		return m
	}
}

// Bundling reports whether the launch goes through our router at all.
func (c *Config) Bundling() bool {
	return c.BundleModeName() != BundleOff
}

// BundleMaxBlocksOrDefault returns the bundle window, defaulting to 3 blocks.
func (c *Config) BundleMaxBlocksOrDefault() int {
	if c.BundleMaxBlocks <= 0 {
		return DefaultBundleMaxBlocks
	}
	return c.BundleMaxBlocks
}

// MMRouterAddr returns the block-limited router to route bundled buys through:
// the configured override or the built-in deployment.
func (c *Config) MMRouterAddr() common.Address {
	if common.IsHexAddress(c.MMRouter) {
		return common.HexToAddress(c.MMRouter)
	}
	return common.HexToAddress(pons.MMRouter)
}

// RetailResponseName returns the normalized retail response strategy; empty
// selects the original slow-distribution behavior.
func (c *Config) RetailResponseName() string {
	if c.RetailResponse == "" {
		return RetailResponseDistribute
	}
	return c.RetailResponse
}

// ProtocolName returns the normalized protocol value.
func (c *Config) ProtocolName() string {
	if c.Protocol == "" {
		return ProtocolV1
	}
	return c.Protocol
}

// FeeWalletAddr returns the configured fee wallet, or nil when unset (defaults
// to the deployer) or malformed.
func (c *Config) FeeWalletAddr() *common.Address {
	if c.Token.FeeWallet == "" {
		return nil
	}
	if !common.IsHexAddress(c.Token.FeeWallet) {
		return nil
	}
	a := common.HexToAddress(c.Token.FeeWallet)
	return &a
}

// ethToWei converts a float ETH amount to wei. Small helper used across the
// package; goes through big.Float to keep 18-decimal precision.
func ethToWei(eth float64) *big.Int {
	if eth <= 0 {
		return big.NewInt(0)
	}
	wei := new(big.Float).Mul(big.NewFloat(eth), big.NewFloat(1e18))
	out, _ := wei.Int(nil)
	return out
}
