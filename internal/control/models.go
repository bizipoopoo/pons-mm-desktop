package control

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/ponsmm"
	"github.com/bizipoopoo/pons-mm-desktop/internal/vault"
)

const (
	ModeLaunch   = "launch"
	ModeExisting = "existing"
)

type Settings struct {
	RPCEndpoint      string `json:"rpcEndpoint"`
	GMGNViewerWallet string `json:"gmgnViewerWallet"`
	// MMRouter is the block-height-limited buy router (PonsMMRouter proxy)
	// that bundled launch buys go through. Empty uses the built-in deployment.
	MMRouter string `json:"mmRouter"`
}

type Socials struct {
	Twitter   string `json:"twitter"`
	Telegram  string `json:"telegram"`
	Discord   string `json:"discord"`
	Website   string `json:"website"`
	Farcaster string `json:"farcaster"`
}

type TokenSpec struct {
	Name        string  `json:"name"`
	Symbol      string  `json:"symbol"`
	Logo        string  `json:"logo"`
	Description string  `json:"description"`
	FeeWallet   string  `json:"feeWallet"`
	Socials     Socials `json:"socials"`
}

// Strategy is the non-secret persisted definition of one independent pair.
// WalletIDs reference encrypted vault records; private keys never enter config.
type Strategy struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Mode           string    `json:"mode"`
	Protocol       string    `json:"protocol"`
	Enabled        bool      `json:"enabled"`
	TokenAddress   string    `json:"tokenAddress"`
	PoolAddress    string    `json:"poolAddress"`
	WalletIDs      []string  `json:"walletIds"`
	Token          TokenSpec `json:"token"`
	LaunchConfigID uint64    `json:"launchConfigId"`
	DexID          uint64    `json:"dexId"`
	DevBuyETH      float64   `json:"devBuyEth"`
	// BundleMode: "none", "window" or "atomic" (see ponsmm.Config.BundleMode).
	BundleMode           string  `json:"bundleMode"`
	BundleMaxBlocks      int     `json:"bundleMaxBlocks"`
	BuyFraction          float64 `json:"buyFraction"`
	AccumulateIntervalMS int64   `json:"accumulateIntervalMs"`
	ConcurrentBuys       bool    `json:"concurrentBuys"`
	ChipTarget           float64 `json:"chipTarget"`
	Graduate             bool    `json:"graduate"`
	SellIntervalMS       int64   `json:"sellIntervalMs"`
	SequentialSells      bool    `json:"sequentialSells"`
	RetailResponseMode   string  `json:"retailResponseMode"`
	RetailTargetRatio    float64 `json:"retailTargetRatio"`
	SlippageBps          int64   `json:"slippageBps"`
	PriorityTipGwei      float64 `json:"priorityTipGwei"`
	GasReserveETH        float64 `json:"gasReserveEth"`
	CreatedAt            string  `json:"createdAt"`
	UpdatedAt            string  `json:"updatedAt"`
}

func NewStrategy() Strategy {
	now := time.Now().UTC().Format(time.RFC3339)
	return Strategy{
		Mode:                 ModeExisting,
		Protocol:             ponsmm.ProtocolV2,
		Enabled:              true,
		BundleMode:           ponsmm.BundleOff,
		BundleMaxBlocks:      ponsmm.DefaultBundleMaxBlocks,
		BuyFraction:          0.99,
		AccumulateIntervalMS: 100,
		ConcurrentBuys:       true,
		ChipTarget:           0.9,
		Graduate:             true,
		SellIntervalMS:       1000,
		SequentialSells:      false,
		RetailResponseMode:   ponsmm.RetailResponseDistribute,
		RetailTargetRatio:    0,
		SlippageBps:          1500,
		PriorityTipGwei:      1,
		GasReserveETH:        0.002,
		CreatedAt:            now,
		UpdatedAt:            now,
	}
}

func (s Strategy) engineConfig(settings Settings) *ponsmm.Config {
	return &ponsmm.Config{
		Protocol:    s.protocolName(),
		RPCEndpoint: settings.RPCEndpoint,
		Token: ponsmm.TokenConfig{
			Name: s.Token.Name, Symbol: s.Token.Symbol, Logo: s.Token.Logo,
			Description: s.Token.Description, FeeWallet: s.Token.FeeWallet,
			Socials: ponsmm.SocialsConfig{
				Twitter: s.Token.Socials.Twitter, Telegram: s.Token.Socials.Telegram,
				Discord: s.Token.Socials.Discord, Website: s.Token.Socials.Website,
				Farcaster: s.Token.Socials.Farcaster,
			},
		},
		LaunchConfigID:     s.LaunchConfigID,
		DexID:              s.DexID,
		DevBuyETH:          s.DevBuyETH,
		BundleMode:         s.BundleMode,
		BundleMaxBlocks:    s.BundleMaxBlocks,
		MMRouter:           strings.TrimSpace(settings.MMRouter),
		BuyFraction:        s.BuyFraction,
		AccumulateInterval: time.Duration(s.AccumulateIntervalMS) * time.Millisecond,
		ConcurrentBuys:     s.ConcurrentBuys,
		ChipTarget:         s.ChipTarget,
		Graduate:           s.Graduate,
		SellInterval:       time.Duration(s.SellIntervalMS) * time.Millisecond,
		ConcurrentSells:    !s.SequentialSells,
		RetailResponse:     s.RetailResponseMode,
		RetailTargetRatio:  s.RetailTargetRatio,
		SlippageBps:        s.SlippageBps,
		PriorityTipGwei:    s.PriorityTipGwei,
		GasReserveETH:      s.GasReserveETH,
	}
}

func (s Strategy) validate(settings Settings) error {
	if strings.TrimSpace(s.Name) == "" {
		return errors.New("strategy name is required")
	}
	if len(s.WalletIDs) == 0 {
		return errors.New("select at least one wallet; the first is treasury")
	}
	if s.Mode != ModeLaunch && s.Mode != ModeExisting {
		return errors.New("strategy mode must be launch or existing")
	}
	protocol := s.protocolName()
	if protocol != ponsmm.ProtocolV1 && protocol != ponsmm.ProtocolV2 {
		return errors.New("strategy protocol must be v1 or v2")
	}
	if s.Mode == ModeLaunch && protocol == ponsmm.ProtocolV2 && len(s.WalletIDs) > 33 {
		return errors.New("pons v2 launch supports one deployer plus at most 32 maker wallets")
	}
	bundling := false
	if mode := strings.ToLower(strings.TrimSpace(s.BundleMode)); mode != "" && mode != ponsmm.BundleOff {
		bundling = true
	}
	if bundling && len(s.WalletIDs) > 32 {
		// Behind the router the treasury takes one of the 32 exemption slots.
		return errors.New("a bundled launch supports the treasury plus at most 31 maker wallets")
	}
	if s.Mode == ModeExisting && (!common.IsHexAddress(s.TokenAddress) || !common.IsHexAddress(s.PoolAddress)) {
		return errors.New("existing strategy requires valid token and pool addresses")
	}
	if bundling && s.Mode != ModeLaunch {
		return errors.New("bundled launch buys only apply to a launch strategy")
	}
	if r := strings.TrimSpace(settings.MMRouter); r != "" && !common.IsHexAddress(r) {
		return fmt.Errorf("settings: block-limited router %q is not a valid address", r)
	}
	cfg := s.engineConfig(settings)
	if err := cfg.Validate(s.Mode == ModeLaunch); err != nil {
		return err
	}
	seen := make(map[string]bool, len(s.WalletIDs))
	for _, id := range s.WalletIDs {
		if seen[id] {
			return fmt.Errorf("wallet %s is selected more than once", id)
		}
		seen[id] = true
	}
	return nil
}

func (s Strategy) protocolName() string {
	if s.Protocol == "" {
		return ponsmm.ProtocolV1
	}
	return s.Protocol
}

type JobStatus struct {
	StrategyID  string    `json:"strategyId"`
	State       string    `json:"state"`
	Message     string    `json:"message"`
	StartedAt   string    `json:"startedAt"`
	Token       string    `json:"token"`
	Pool        string    `json:"pool"`
	LastUpdated string    `json:"lastUpdated"`
	Stats       *JobStats `json:"stats,omitempty"`
}

// JobStats is the per-strategy execution dashboard: trade counts, volumes,
// total overhead paid, and the round's realized profit once the run ends.
// All ETH values are decimal strings.
type JobStats struct {
	BuyCount     int64  `json:"buyCount"`
	SellCount    int64  `json:"sellCount"`
	EthSpent     string `json:"ethSpent"`     // ETH paid into buys
	EthReceived  string `json:"ethReceived"`  // ETH received from sells
	TokensSold   string `json:"tokensSold"`   // whole tokens sold
	TotalCost    string `json:"totalCost"`    // gas + priority tips + launch fee
	StartBalance string `json:"startBalance"` // summed wallet ETH at start
	EndBalance   string `json:"endBalance"`   // summed wallet ETH at finish; empty while running
	Profit       string `json:"profit"`       // end - start; empty while running
}

// InitStatus is the result of the application's startup initialization check.
type InitStatus struct {
	Checked    bool   `json:"checked"`
	OK         bool   `json:"ok"`
	BalanceETH string `json:"balanceEth"`
	Message    string `json:"message"`
	CheckedAt  string `json:"checkedAt"`
}

// LaunchPreset is the metadata of the most recent factory launch, used to
// prefill a test strategy's token settings.
type LaunchPreset struct {
	TokenAddress string  `json:"tokenAddress"`
	CurveAddress string  `json:"curveAddress"`
	Block        uint64  `json:"block"`
	Name         string  `json:"name"`
	Symbol       string  `json:"symbol"`
	Logo         string  `json:"logo"`
	Description  string  `json:"description"`
	Socials      Socials `json:"socials"`
}

type LogEntry struct {
	At         string `json:"at"`
	StrategyID string `json:"strategyId"`
	Level      string `json:"level"`
	Message    string `json:"message"`
}

type VaultState struct {
	Exists   bool            `json:"exists"`
	Unlocked bool            `json:"unlocked"`
	Wallets  []vault.Summary `json:"wallets"`
}

type Bootstrap struct {
	Settings   Settings     `json:"settings"`
	Strategies []Strategy   `json:"strategies"`
	Jobs       []JobStatus  `json:"jobs"`
	Logs       []LogEntry   `json:"logs"`
	Vault      VaultState   `json:"vault"`
	Init       InitStatus   `json:"init"`
	Funding    FundingState `json:"funding"`
}
