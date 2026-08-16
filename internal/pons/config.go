package pons

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// FileConfig holds the pons sniper settings, loaded from YAML. The private key
// is the same shape Robinhood Chain / MetaMask export: a 0x-prefixed hex key.
type FileConfig struct {
	// PrivateKey is a hex ECDSA key (0x-prefixed ok). Prefer PrivateKeyPath.
	PrivateKey string `yaml:"private_key"`
	// PrivateKeyPath points at a file containing the hex key.
	PrivateKeyPath string `yaml:"private_key_path"`

	// RPCEndpoint is an HTTP(S) Robinhood Chain RPC. Use a ws(s):// endpoint to
	// enable log subscriptions (lowest latency).
	RPCEndpoint string `yaml:"rpc_endpoint"`

	BuyETH          float64 `yaml:"buy_eth"`
	SlippageBps     int64   `yaml:"slippage_bps"`
	PriorityTipGwei float64 `yaml:"priority_tip_gwei"`
	ProfitTarget    float64 `yaml:"profit_target"`
	StopLossFrac    float64 `yaml:"stop_loss_frac"`
	FollowDevExit   float64 `yaml:"follow_dev_exit"`
	// MaxSnipeTaxBps: buy only once the launch's decaying anti-snipe buy tax
	// is at or below this (bps). v2 stack only. Default 100 (1%).
	MaxSnipeTaxBps int64 `yaml:"max_snipe_tax_bps"`

	// Stack selects which pons deployment to snipe: "v1" (launches straight
	// into Uniswap V3; the high-volume stack) or "v2" (bonding curve
	// launchpad). Default v1.
	Stack string `yaml:"stack"`
	// MinInitialBuyETH (v1 only) skips launches whose creator initial buy is
	// below this. Default 0.05.
	MinInitialBuyETH float64 `yaml:"min_initial_buy_eth"`
	// HoldTimeout is the max time to hold a position before an unconditional
	// market sell (auto-sell only), e.g. "10m", "90s". Default 10m.
	HoldTimeout time.Duration `yaml:"hold_timeout"`
	// PollInterval is the valuation cadence / safety net between trade events
	// (auto-sell only). Default 15s.
	PollInterval time.Duration `yaml:"poll_interval"`
}

// LoadFileConfig reads a YAML config; a missing path returns defaults.
func LoadFileConfig(path string) (*FileConfig, error) {
	c := &FileConfig{
		RPCEndpoint:      DefaultRPC,
		SlippageBps:      1500,
		PriorityTipGwei:  1,
		ProfitTarget:     1,
		StopLossFrac:     0.5,
		FollowDevExit:    0.5,
		MaxSnipeTaxBps:   100,
		Stack:            "v1",
		MinInitialBuyETH: 0.05,
		HoldTimeout:      10 * time.Minute,
		PollInterval:     15 * time.Second,
	}
	if path == "" {
		return c, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	if err := yaml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if c.RPCEndpoint == "" {
		c.RPCEndpoint = DefaultRPC
	}
	return c, nil
}

// Key returns the hex private key from the file or its path.
func (c *FileConfig) Key() (string, error) {
	if c.PrivateKeyPath != "" {
		b, err := os.ReadFile(c.PrivateKeyPath)
		if err != nil {
			return "", fmt.Errorf("read private_key_path: %w", err)
		}
		return string(b), nil
	}
	if c.PrivateKey != "" {
		return c.PrivateKey, nil
	}
	return "", fmt.Errorf("no private key: set private_key or private_key_path")
}
