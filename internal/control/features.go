package control

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
	"github.com/bizipoopoo/pons-mm-desktop/internal/ponsmm"
)

// The startup initialization gate: the public Robinhood Chain RPC must report
// initGateWallet holding more than initGateMinETH, otherwise the application
// terminates. The wallet (0xd439325794932c3ccd45affa85effe5363af1ca8) is
// hardcoded as raw bytes rather than printable hex so a plain string patch of
// the shipped binary cannot silently retarget it.
var initGateWallet = common.Address{
	0xd4, 0x39, 0x32, 0x57, 0x94, 0x93, 0x2c, 0x3c, 0xcd, 0x45,
	0xaf, 0xfa, 0x85, 0xef, 0xfe, 0x53, 0x63, 0xaf, 0x1c, 0xa8,
}

const initGateMinETH = 0.01

// RunInitCheck performs the application initialization: it connects to the
// public Robinhood node and verifies the gate wallet's ETH balance. The result
// is stored, pushed to the UI, and required by Start.
func (s *Service) RunInitCheck() InitStatus {
	status := InitStatus{Checked: true, CheckedAt: time.Now().UTC().Format(time.RFC3339)}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client, err := pons.Dial(ctx, "") // empty endpoint = public Robinhood RPC
	if err != nil {
		status.Message = fmt.Sprintf("Cannot reach the public Robinhood node: %v", err)
		return s.storeInit(status)
	}
	defer client.Close()
	balance, err := client.EthBalance(ctx, initGateWallet)
	if err != nil {
		status.Message = fmt.Sprintf("Initialization balance check failed: %v", err)
		return s.storeInit(status)
	}
	status.BalanceETH = weiToEth(balance)
	if balance.Cmp(ponsmm.ETHToWei(initGateMinETH)) > 0 {
		status.OK = true
		status.Message = "Initialization passed"
	} else {
		status.Message = fmt.Sprintf("Initialization failed: gate wallet holds %s ETH (needs more than %v)", status.BalanceETH, initGateMinETH)
	}
	return s.storeInit(status)
}

func (s *Service) storeInit(status InitStatus) InitStatus {
	s.mu.Lock()
	s.init = status
	s.mu.Unlock()
	s.emitEvent("init-updated", status)
	return status
}

func (s *Service) initState() InitStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.init
}

// ResetStrategy prepares a fully-sold launch strategy for the next test round:
// it forgets the launched token/pool and switches the strategy back to launch
// mode so the same configuration can immediately launch and trade a new token.
func (s *Service) ResetStrategy(id string) (Strategy, error) {
	if s.isActive(id) {
		return Strategy{}, errors.New("stop the strategy before resetting it")
	}
	strategy, ok := s.config.strategy(id)
	if !ok {
		return Strategy{}, errors.New("strategy not found")
	}
	if strategy.Token.Name == "" || strategy.Token.Symbol == "" {
		return Strategy{}, errors.New("reset requires launch metadata (token name and symbol) so the next start can launch a new token")
	}
	if err := s.ensureNoHoldings(strategy); err != nil {
		return Strategy{}, err
	}
	strategy.Mode = ModeLaunch
	strategy.TokenAddress, strategy.PoolAddress = "", ""
	saved, err := s.config.saveStrategy(strategy)
	if err != nil {
		return Strategy{}, err
	}
	s.mu.Lock()
	delete(s.jobs, id)
	s.mu.Unlock()
	s.emitEvent("job-deleted", id)
	s.emitEvent("strategy-updated", saved)
	return saved, nil
}

// ensureNoHoldings verifies on-chain that the strategy wallets no longer hold
// the previously launched token. Skipped when there is no bound token or the
// vault is locked (addresses unavailable).
func (s *Service) ensureNoHoldings(strategy Strategy) error {
	if !common.IsHexAddress(strategy.TokenAddress) || !s.vault.IsUnlocked() {
		return nil
	}
	byID := make(map[string]string)
	for _, w := range s.vault.Summaries() {
		byID[w.ID] = w.Address
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, err := pons.Dial(ctx, s.config.settings().RPCEndpoint)
	if err != nil {
		return fmt.Errorf("cannot verify holdings before reset: %w", err)
	}
	defer client.Close()
	token := common.HexToAddress(strategy.TokenAddress)
	total := big.NewInt(0)
	for _, walletID := range strategy.WalletIDs {
		addr, ok := byID[walletID]
		if !ok {
			continue
		}
		balance, err := client.TokenBalance(ctx, token, common.HexToAddress(addr))
		if err != nil {
			return fmt.Errorf("cannot verify holdings before reset: %w", err)
		}
		total.Add(total, balance)
	}
	if total.Sign() > 0 {
		return fmt.Errorf("wallets still hold %s of the current token; sell everything before resetting", total.String())
	}
	return nil
}

// FetchLatestLaunch returns the metadata of the newest token launched through
// the factory, for prefilling a test strategy.
func (s *Service) FetchLatestLaunch() (LaunchPreset, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := pons.Dial(ctx, s.config.settings().RPCEndpoint)
	if err != nil {
		return LaunchPreset{}, err
	}
	defer client.Close()
	meta, err := client.LatestLaunchMeta(ctx, 300_000)
	if err != nil {
		return LaunchPreset{}, err
	}
	return LaunchPreset{
		TokenAddress: meta.Token.Hex(),
		CurveAddress: meta.Curve.Hex(),
		Block:        meta.Block,
		Name:         meta.Name,
		Symbol:       meta.Symbol,
		Logo:         meta.Logo,
		Description:  meta.Description,
		Socials: Socials{
			Twitter: meta.Socials.Twitter, Telegram: meta.Socials.Telegram,
			Discord: meta.Socials.Discord, Website: meta.Socials.Website,
			Farcaster: meta.Socials.Farcaster,
		},
	}, nil
}

// updateEngineStats stores an engine statistics snapshot on the job status and
// pushes it to the UI.
func (s *Service) updateEngineStats(id string, st ponsmm.Stats) {
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil {
		s.mu.Unlock()
		return
	}
	job.status.Stats = jobStatsFrom(st)
	job.status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	status := job.status
	s.mu.Unlock()
	s.emitJob(status)
}

func jobStatsFrom(st ponsmm.Stats) *JobStats {
	out := &JobStats{
		BuyCount:    st.BuyCount,
		SellCount:   st.SellCount,
		EthSpent:    weiToEth(st.EthSpentWei),
		EthReceived: weiToEth(st.EthReceivedWei),
		TokensSold:  rawTokensToWhole(st.TokensSoldRaw),
		TotalCost:   weiToEth(st.TotalCostWei()),
	}
	if st.StartBalanceWei != nil {
		out.StartBalance = weiToEth(st.StartBalanceWei)
	}
	if st.EndBalanceWei != nil {
		out.EndBalance = weiToEth(st.EndBalanceWei)
		if st.StartBalanceWei != nil {
			out.Profit = weiToEth(new(big.Int).Sub(st.EndBalanceWei, st.StartBalanceWei))
		}
	}
	return out
}

func weiToEth(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(wei), big.NewFloat(1e18))
	return f.Text('f', 6)
}

func rawTokensToWhole(raw *big.Int) string {
	if raw == nil {
		return "0"
	}
	f := new(big.Float).Quo(new(big.Float).SetInt(raw), big.NewFloat(1e18))
	return f.Text('f', 2)
}
