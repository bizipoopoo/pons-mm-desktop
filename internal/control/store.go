package control

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type persistedConfig struct {
	Version    int        `json:"version"`
	Settings   Settings   `json:"settings"`
	Strategies []Strategy `json:"strategies"`
}

type configStore struct {
	mu   sync.RWMutex
	path string
	data persistedConfig
}

func newConfigStore(path string) (*configStore, error) {
	s := &configStore{path: path, data: persistedConfig{Version: 1}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.Version != 1 {
		return nil, errors.New("unsupported config version")
	}
	return s, nil
}

func (s *configStore) snapshot() (Settings, []Strategy) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	strategies := append([]Strategy(nil), s.data.Strategies...)
	for i := range strategies {
		strategies[i].WalletIDs = append([]string(nil), strategies[i].WalletIDs...)
	}
	sort.SliceStable(strategies, func(i, j int) bool { return strategies[i].CreatedAt < strategies[j].CreatedAt })
	return s.data.Settings, strategies
}

func (s *configStore) settings() Settings {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Settings
}

func (s *configStore) saveSettings(settings Settings) error {
	settings.RPCEndpoint = strings.TrimSpace(settings.RPCEndpoint)
	settings.GMGNViewerWallet = strings.TrimSpace(settings.GMGNViewerWallet)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Settings = settings
	return s.writeLocked()
}

func (s *configStore) strategy(id string) (Strategy, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, strategy := range s.data.Strategies {
		if strategy.ID == id {
			strategy.WalletIDs = append([]string(nil), strategy.WalletIDs...)
			return strategy, true
		}
	}
	return Strategy{}, false
}

func (s *configStore) saveStrategy(strategy Strategy) (Strategy, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	strategy.Name = strings.TrimSpace(strategy.Name)
	strategy.TokenAddress = strings.TrimSpace(strategy.TokenAddress)
	strategy.PoolAddress = strings.TrimSpace(strategy.PoolAddress)
	strategy.UpdatedAt = now
	if strategy.ID == "" {
		strategy.ID = randomID()
		strategy.CreatedAt = now
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	updated := false
	for i := range s.data.Strategies {
		if s.data.Strategies[i].ID == strategy.ID {
			if strategy.CreatedAt == "" {
				strategy.CreatedAt = s.data.Strategies[i].CreatedAt
			}
			s.data.Strategies[i] = strategy
			updated = true
			break
		}
	}
	if !updated {
		s.data.Strategies = append(s.data.Strategies, strategy)
	}
	if err := s.writeLocked(); err != nil {
		return Strategy{}, err
	}
	return strategy, nil
}

func (s *configStore) deleteStrategy(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Strategies {
		if s.data.Strategies[i].ID == id {
			s.data.Strategies = append(s.data.Strategies[:i], s.data.Strategies[i+1:]...)
			return s.writeLocked()
		}
	}
	return errors.New("strategy not found")
}

func (s *configStore) writeLocked() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func randomID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}
