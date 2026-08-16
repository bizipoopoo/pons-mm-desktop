package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
	"github.com/bizipoopoo/pons-mm-desktop/internal/ponsmm"
	"github.com/bizipoopoo/pons-mm-desktop/internal/vault"
)

const maxLogs = 800

type runningJob struct {
	status    JobStatus
	cancel    context.CancelFunc
	walletIDs []string
	active    bool
}

// Service coordinates persistent config, encrypted wallets, and concurrent
// strategy engines. Each active strategy owns a client and a disjoint wallet set.
type Service struct {
	mu          sync.RWMutex
	root        context.Context
	config      *configStore
	vault       *vault.Store
	jobs        map[string]*runningJob
	usedWallets map[string]string
	logs        []LogEntry
	emit        func(string, any)
}

func NewService(dataDir string) (*Service, error) {
	if dataDir == "" {
		return nil, errors.New("application data directory is required")
	}
	config, err := newConfigStore(filepath.Join(dataDir, "config.json"))
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return &Service{
		root:        context.Background(),
		config:      config,
		vault:       vault.New(filepath.Join(dataDir, "wallets.vault")),
		jobs:        make(map[string]*runningJob),
		usedWallets: make(map[string]string),
	}, nil
}

func DefaultDataDir() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "PonsDesk"), nil
}

func (s *Service) SetRuntime(ctx context.Context, emit func(string, any)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.root = ctx
	s.emit = emit
}

func (s *Service) Bootstrap() Bootstrap {
	settings, strategies := s.config.snapshot()
	s.mu.RLock()
	jobs := make([]JobStatus, 0, len(s.jobs))
	for _, job := range s.jobs {
		jobs = append(jobs, job.status)
	}
	logs := append([]LogEntry(nil), s.logs...)
	s.mu.RUnlock()
	return Bootstrap{
		Settings: settings, Strategies: strategies, Jobs: jobs, Logs: logs,
		Vault: VaultState{Exists: s.vault.Exists(), Unlocked: s.vault.IsUnlocked(), Wallets: s.vault.Summaries()},
	}
}

func (s *Service) NewStrategy() Strategy { return NewStrategy() }

func (s *Service) SaveSettings(settings Settings) error {
	if settings.RPCEndpoint == "" {
		return errors.New("Robinhood Chain RPC endpoint is required")
	}
	if settings.GMGNViewerWallet != "" && !common.IsHexAddress(settings.GMGNViewerWallet) {
		return errors.New("GMGN viewer wallet is not a valid EVM address")
	}
	if err := s.config.saveSettings(settings); err != nil {
		return err
	}
	s.emitEvent("config-updated", nil)
	return nil
}

func (s *Service) SaveStrategy(strategy Strategy) (Strategy, error) {
	if strategy.ID != "" && s.isActive(strategy.ID) {
		return Strategy{}, errors.New("stop the strategy before editing it")
	}
	if strings.TrimSpace(strategy.Name) == "" {
		return Strategy{}, errors.New("strategy name is required")
	}
	if strategy.Mode != ModeLaunch && strategy.Mode != ModeExisting {
		return Strategy{}, errors.New("strategy mode is invalid")
	}
	saved, err := s.config.saveStrategy(strategy)
	if err == nil {
		s.emitEvent("strategy-updated", saved)
	}
	return saved, err
}

func (s *Service) DeleteStrategy(id string) error {
	if s.isActive(id) {
		return errors.New("stop the strategy before deleting it")
	}
	if err := s.config.deleteStrategy(id); err != nil {
		return err
	}
	s.emitEvent("strategy-deleted", id)
	return nil
}

func (s *Service) CreateVault(password string) error {
	if err := s.vault.Create(password); err != nil {
		return err
	}
	s.emitVault()
	return nil
}

func (s *Service) UnlockVault(password string) error {
	if err := s.vault.Unlock(password); err != nil {
		return err
	}
	s.emitVault()
	return nil
}

func (s *Service) LockVault() error {
	s.mu.RLock()
	active := false
	for _, job := range s.jobs {
		active = active || job.active
	}
	s.mu.RUnlock()
	if active {
		return errors.New("stop all strategies before locking the vault")
	}
	s.vault.Lock()
	s.emitVault()
	return nil
}

func (s *Service) ImportPrivateKeys(input, labelPrefix string) ([]vault.Summary, error) {
	added, err := s.vault.ImportPrivateKeys(input, labelPrefix)
	if err == nil {
		s.emitVault()
	}
	return added, err
}

func (s *Service) ImportMnemonic(mnemonic string, count int, labelPrefix string) ([]vault.Summary, error) {
	added, err := s.vault.ImportMnemonic(mnemonic, count, labelPrefix)
	if err == nil {
		s.emitVault()
	}
	return added, err
}

func (s *Service) GenerateMnemonic() (string, error) { return vault.GenerateMnemonic() }

// Preflight performs only RPC reads and transaction construction checks.
func (s *Service) Preflight(id string) (string, error) {
	strategy, ok := s.config.strategy(id)
	if !ok {
		return "", errors.New("strategy not found")
	}
	settings := s.config.settings()
	if err := strategy.validate(settings); err != nil {
		return "", err
	}
	keys, err := s.vault.Keys(strategy.WalletIDs)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(s.root, 45*time.Second)
	defer cancel()
	logger := slog.New(newTaskHandler(s, id))
	client, pool, cfg, err := setupEngine(ctx, strategy, settings, keys, logger)
	if err != nil {
		return "", err
	}
	defer client.Close()
	eng := ponsmm.NewEngine(cfg, client, pool, logger)
	if strategy.Mode == ModeLaunch {
		if err := eng.Launch(ctx, true); err != nil {
			return "", err
		}
		return "Launch preflight passed; no transaction was sent.", nil
	}
	if err := eng.Bind(ctx, common.HexToAddress(strategy.TokenAddress), common.HexToAddress(strategy.PoolAddress)); err != nil {
		return "", err
	}
	return "Token, pool, RPC, and wallet binding checks passed.", nil
}

// Start begins a live strategy. The exact confirmation phrase prevents an
// accidental invocation through stale UI state or direct Wails calls.
func (s *Service) Start(id, confirmation string) error {
	if confirmation != "LIVE" {
		return errors.New("live confirmation phrase is required")
	}
	strategy, ok := s.config.strategy(id)
	if !ok {
		return errors.New("strategy not found")
	}
	settings := s.config.settings()
	if err := strategy.validate(settings); err != nil {
		return err
	}
	keys, err := s.vault.Keys(strategy.WalletIDs)
	if err != nil {
		return err
	}

	s.mu.Lock()
	if job := s.jobs[id]; job != nil && job.active {
		s.mu.Unlock()
		return errors.New("strategy is already running")
	}
	for _, walletID := range strategy.WalletIDs {
		if owner := s.usedWallets[walletID]; owner != "" {
			s.mu.Unlock()
			return fmt.Errorf("a selected wallet is already used by strategy %s", owner)
		}
	}
	ctx, cancel := context.WithCancel(s.root)
	now := time.Now().UTC().Format(time.RFC3339)
	job := &runningJob{
		status: JobStatus{StrategyID: id, State: "starting", Message: "Connecting to Robinhood Chain", StartedAt: now, LastUpdated: now},
		cancel: cancel, walletIDs: append([]string(nil), strategy.WalletIDs...), active: true,
	}
	s.jobs[id] = job
	for _, walletID := range strategy.WalletIDs {
		s.usedWallets[walletID] = id
	}
	s.mu.Unlock()
	s.emitJob(job.status)
	go s.runStrategy(ctx, strategy, settings, keys)
	return nil
}

func (s *Service) Stop(id string) error {
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil || !job.active {
		s.mu.Unlock()
		return errors.New("strategy is not running")
	}
	job.status.State = "stopping"
	job.status.Message = "Waiting for the current operation to stop"
	job.status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	status := job.status
	cancel := job.cancel
	s.mu.Unlock()
	s.emitJob(status)
	cancel()
	return nil
}

func (s *Service) Shutdown() {
	s.mu.Lock()
	for _, job := range s.jobs {
		if job.active {
			job.cancel()
		}
	}
	s.mu.Unlock()
}

func (s *Service) runStrategy(ctx context.Context, strategy Strategy, settings Settings, keys []string) {
	logger := slog.New(newTaskHandler(s, strategy.ID))
	client, pool, cfg, err := setupEngine(ctx, strategy, settings, keys, logger)
	if err != nil {
		s.finish(strategy.ID, "error", err.Error(), strategy.TokenAddress, strategy.PoolAddress)
		return
	}
	defer client.Close()
	client.WarmGas(ctx)
	eng := ponsmm.NewEngine(cfg, client, pool, logger)
	if strategy.Mode == ModeLaunch {
		if err := eng.Launch(ctx, false); err != nil {
			s.finish(strategy.ID, "error", err.Error(), "", "")
			return
		}
		token, poolAddr := eng.Binding()
		strategy.Mode = ModeExisting
		strategy.TokenAddress = token.Hex()
		strategy.PoolAddress = poolAddr.Hex()
		if saved, err := s.config.saveStrategy(strategy); err == nil {
			s.emitEvent("strategy-updated", saved)
		} else {
			logger.Error("launch succeeded but persistence failed", "err", err)
		}
	} else if err := eng.Bind(ctx, common.HexToAddress(strategy.TokenAddress), common.HexToAddress(strategy.PoolAddress)); err != nil {
		s.finish(strategy.ID, "error", err.Error(), strategy.TokenAddress, strategy.PoolAddress)
		return
	}
	token, poolAddr := eng.Binding()
	s.updateJob(strategy.ID, "running", "Market maker is active", token.Hex(), poolAddr.Hex())
	if err := eng.Run(ctx); err != nil {
		s.finish(strategy.ID, "error", err.Error(), token.Hex(), poolAddr.Hex())
		return
	}
	s.finish(strategy.ID, "stopped", "Strategy stopped", token.Hex(), poolAddr.Hex())
}

func setupEngine(ctx context.Context, strategy Strategy, settings Settings, keys []string, logger *slog.Logger) (*pons.Client, *ponsmm.Pool, *ponsmm.Config, error) {
	client, err := pons.Dial(ctx, settings.RPCEndpoint)
	if err != nil {
		return nil, nil, nil, err
	}
	if client.ChainID() == nil || client.ChainID().Cmp(big.NewInt(pons.RobinhoodChainID)) != 0 {
		client.Close()
		return nil, nil, nil, fmt.Errorf("connected chain ID %v; Robinhood Chain requires %d", client.ChainID(), pons.RobinhoodChainID)
	}
	pool, err := ponsmm.NewPool(client, keys, logger)
	if err != nil {
		client.Close()
		return nil, nil, nil, err
	}
	return client, pool, strategy.engineConfig(settings), nil
}

func (s *Service) isActive(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	job := s.jobs[id]
	return job != nil && job.active
}

func (s *Service) updateJob(id, state, message, token, pool string) {
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil {
		s.mu.Unlock()
		return
	}
	job.status.State, job.status.Message = state, message
	job.status.Token, job.status.Pool = token, pool
	job.status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	status := job.status
	s.mu.Unlock()
	s.emitJob(status)
}

func (s *Service) finish(id, state, message, token, pool string) {
	s.mu.Lock()
	job := s.jobs[id]
	if job == nil {
		s.mu.Unlock()
		return
	}
	job.status.State, job.status.Message = state, message
	job.status.Token, job.status.Pool = token, pool
	job.status.LastUpdated = time.Now().UTC().Format(time.RFC3339)
	job.active = false
	for _, walletID := range job.walletIDs {
		if s.usedWallets[walletID] == id {
			delete(s.usedWallets, walletID)
		}
	}
	status := job.status
	s.mu.Unlock()
	s.emitJob(status)
}

func (s *Service) appendLog(entry LogEntry) {
	s.mu.Lock()
	s.logs = append(s.logs, entry)
	if len(s.logs) > maxLogs {
		s.logs = append([]LogEntry(nil), s.logs[len(s.logs)-maxLogs:]...)
	}
	s.mu.Unlock()
	s.emitEvent("strategy-log", entry)
}

func (s *Service) emitVault() {
	s.emitEvent("vault-updated", VaultState{Exists: s.vault.Exists(), Unlocked: s.vault.IsUnlocked(), Wallets: s.vault.Summaries()})
}

func (s *Service) emitJob(status JobStatus) { s.emitEvent("job-updated", status) }

func (s *Service) emitEvent(name string, data any) {
	s.mu.RLock()
	emit := s.emit
	s.mu.RUnlock()
	if emit != nil {
		emit(name, data)
	}
}

// GMGNImport returns a secret-free bulk import payload for one strategy.
func (s *Service) GMGNImport(id string) (string, error) {
	strategy, ok := s.config.strategy(id)
	if !ok {
		return "", errors.New("strategy not found")
	}
	all := s.vault.Summaries()
	byID := make(map[string]vault.Summary, len(all))
	for _, wallet := range all {
		byID[wallet.ID] = wallet
	}
	marks := make([]ponsmm.GmgnMark, 0, len(strategy.WalletIDs))
	for i, id := range strategy.WalletIDs {
		wallet, ok := byID[id]
		if !ok {
			return "", fmt.Errorf("wallet %s is not available", id)
		}
		name := fmt.Sprintf("%s-%02d", strategy.Name, i)
		if i == 0 {
			name = strategy.Name + "-deployer"
		}
		marks = append(marks, ponsmm.GmgnMark{Address: wallet.Address, Name: name, Emoji: "MM"})
	}
	b, err := json.MarshalIndent(marks, "", "  ")
	if err != nil {
		return "", err
	}
	return string(append(b, '\n')), nil
}
