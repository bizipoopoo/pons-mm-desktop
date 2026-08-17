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
	ID                   string    `json:"id"`
	Name                 string    `json:"name"`
	Mode                 string    `json:"mode"`
	Protocol             string    `json:"protocol"`
	Enabled              bool      `json:"enabled"`
	TokenAddress         string    `json:"tokenAddress"`
	PoolAddress          string    `json:"poolAddress"`
	WalletIDs            []string  `json:"walletIds"`
	Token                TokenSpec `json:"token"`
	LaunchConfigID       uint64    `json:"launchConfigId"`
	DexID                uint64    `json:"dexId"`
	DevBuyETH            float64   `json:"devBuyEth"`
	BuyFraction          float64   `json:"buyFraction"`
	AccumulateIntervalMS int64     `json:"accumulateIntervalMs"`
	ConcurrentBuys       bool      `json:"concurrentBuys"`
	ChipTarget           float64   `json:"chipTarget"`
	Graduate             bool      `json:"graduate"`
	HighHold             float64   `json:"highHold"`
	OscillationBand      float64   `json:"oscillationBand"`
	SellIntervalMS       int64     `json:"sellIntervalMs"`
	SequentialSells      bool      `json:"sequentialSells"`
	SellTranche          float64   `json:"sellTranche"`
	SlippageBps          int64     `json:"slippageBps"`
	PriorityTipGwei      float64   `json:"priorityTipGwei"`
	GasReserveETH        float64   `json:"gasReserveEth"`
	CreatedAt            string    `json:"createdAt"`
	UpdatedAt            string    `json:"updatedAt"`
}

func NewStrategy() Strategy {
	now := time.Now().UTC().Format(time.RFC3339)
	return Strategy{
		Mode:                 ModeExisting,
		Protocol:             ponsmm.ProtocolV2,
		Enabled:              true,
		BuyFraction:          0.99,
		AccumulateIntervalMS: 1000,
		ConcurrentBuys:       false,
		ChipTarget:           0.9,
		Graduate:             true,
		HighHold:             0.60,
		OscillationBand:      0.20,
		SellIntervalMS:       1000,
		SequentialSells:      false,
		SellTranche:          0.25,
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
		BuyFraction:        s.BuyFraction,
		AccumulateInterval: time.Duration(s.AccumulateIntervalMS) * time.Millisecond,
		ConcurrentBuys:     s.ConcurrentBuys,
		ChipTarget:         s.ChipTarget,
		Graduate:           s.Graduate,
		HighHold:           s.HighHold,
		OscillationBand:    s.OscillationBand,
		SellInterval:       time.Duration(s.SellIntervalMS) * time.Millisecond,
		ConcurrentSells:    !s.SequentialSells,
		SellTranche:        s.SellTranche,
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
	if s.Mode == ModeExisting && (!common.IsHexAddress(s.TokenAddress) || !common.IsHexAddress(s.PoolAddress)) {
		return errors.New("existing strategy requires valid token and pool addresses")
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
	StrategyID  string `json:"strategyId"`
	State       string `json:"state"`
	Message     string `json:"message"`
	StartedAt   string `json:"startedAt"`
	Token       string `json:"token"`
	Pool        string `json:"pool"`
	LastUpdated string `json:"lastUpdated"`
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
	Settings   Settings    `json:"settings"`
	Strategies []Strategy  `json:"strategies"`
	Jobs       []JobStatus `json:"jobs"`
	Logs       []LogEntry  `json:"logs"`
	Vault      VaultState  `json:"vault"`
}
