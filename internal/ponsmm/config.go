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
	"time"

	"gopkg.in/yaml.v3"

	"github.com/ethereum/go-ethereum/common"
)

const (
	ProtocolV1 = "v1"
	ProtocolV2 = "v2"
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

	// DevBuyETH is an optional v1-only atomic initial buy performed inside the
	// launch transaction (paid on top of the launch fee). v2 requires zero.
	DevBuyETH float64 `yaml:"dev_buy_eth"`

	// Accumulation.
	BuyFraction        float64       `yaml:"buy_fraction"`        // fraction of a wallet's spendable ETH per buy (default 0.99)
	AccumulateInterval time.Duration `yaml:"accumulate_interval"` // idle cadence between accumulation buys (default 3s)
	ChipTarget         float64       `yaml:"chip_target"`         // target fraction of circulating supply to hold before easing off (default 0.9)
	Graduate           bool          `yaml:"graduate"`            // keep buying until the pool reaches the graduation threshold

	// Market making.
	HighHold        float64       `yaml:"high_hold"`        // our holding of circulating at/above which we oscillate instead of distributing (default 0.60)
	OscillationBand float64       `yaml:"oscillation_band"` // +/- price band around the retail cost to churn (default 0.20)
	SellInterval    time.Duration `yaml:"sell_interval"`    // cadence between slow-sell tranches (default 4s)
	SellTranche     float64       `yaml:"sell_tranche"`     // fraction of remaining holding sold per distribute tranche (default 0.25)

	// Execution.
	SlippageBps     int64   `yaml:"slippage_bps"`      // per-trade slippage tolerance in bps (default 1500 = 15%)
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
		AccumulateInterval: 3 * time.Second,
		ChipTarget:         0.9,
		Graduate:           true,
		HighHold:           0.60,
		OscillationBand:    0.20,
		SellInterval:       4 * time.Second,
		SellTranche:        0.25,
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
	if c.HighHold <= 0 || c.HighHold > 1 {
		return fmt.Errorf("high_hold must be in (0, 1]")
	}
	if c.OscillationBand <= 0 || c.OscillationBand >= 1 {
		return fmt.Errorf("oscillation_band must be in (0, 1)")
	}
	if c.SellTranche <= 0 || c.SellTranche > 1 {
		return fmt.Errorf("sell_tranche must be in (0, 1]")
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
	if requireLaunchMetadata && protocol == ProtocolV2 && c.DevBuyETH != 0 {
		return fmt.Errorf("v2 launch does not support an atomic initial buy yet; set dev_buy_eth to 0")
	}
	return nil
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
