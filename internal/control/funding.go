package control

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"
)

const (
	// fundingBatchCount is the number of temporary relay batches every task
	// routes through; each batch holds one wallet per target.
	fundingBatchCount = 5
	// fundingRelayCount is the fixed size of the deposit and withdraw relay sets.
	fundingRelayCount = 10
	maxFundingTargets = 500

	FundingKindDistribute = "distribute"
	FundingKindWithdraw   = "withdraw"

	FundingRoleDepositCold    = "deposit-cold"
	FundingRoleDepositRelays  = "deposit-relays"
	FundingRoleWithdrawRelays = "withdraw-relays"
)

// FundingWallet references one routing wallet whose key lives in the vault.
type FundingWallet struct {
	ID      string `json:"id"`
	Address string `json:"address"`
}

// FundingConfig is the fixed routing-wallet layout for native-coin funding.
// The withdraw cold destination is intentionally address-only: the app never
// holds its key.
type FundingConfig struct {
	DepositCold    *FundingWallet  `json:"depositCold,omitempty"`
	DepositRelays  []FundingWallet `json:"depositRelays,omitempty"`
	WithdrawRelays []FundingWallet `json:"withdrawRelays,omitempty"`
	WithdrawCold   string          `json:"withdrawCold,omitempty"`
}

func (c FundingConfig) Complete() bool {
	return c.DepositCold != nil && len(c.DepositRelays) == fundingRelayCount &&
		len(c.WithdrawRelays) == fundingRelayCount && c.WithdrawCold != ""
}

// FundingTask is the UI-safe view of one distribution or withdrawal task.
// Secrets (batch mnemonics, source wallet references) stay in the record.
type FundingTask struct {
	ID             string   `json:"id"`
	Kind           string   `json:"kind"`
	Targets        []string `json:"targets"`
	State          string   `json:"state"`
	Message        string   `json:"message"`
	HopsDone       int      `json:"hopsDone"`
	HopsTotal      int      `json:"hopsTotal"`
	TransfersDone  int      `json:"transfersDone"`
	TransfersTotal int      `json:"transfersTotal"`
	CreatedAt      string   `json:"createdAt"`
	UpdatedAt      string   `json:"updatedAt"`
}

// fundingTaskRecord is the persisted form: the view plus per-task secrets.
type fundingTaskRecord struct {
	FundingTask
	BatchMnemonics  []string `json:"batchMnemonics"`
	SourceWalletIDs []string `json:"sourceWalletIds,omitempty"`
}

// FundingState is the funding slice of Bootstrap.
type FundingState struct {
	Config FundingConfig `json:"config"`
	Tasks  []FundingTask `json:"tasks"`
}

// FundingExport is a downloadable secret payload produced by wallet generation
// or batch export. The caller is responsible for writing it to a user-chosen
// location; it is never persisted outside the vault.
type FundingExport struct {
	Filename string `json:"filename"`
	Payload  string `json:"payload"`
}

type persistedFunding struct {
	Version int                 `json:"version"`
	Config  FundingConfig       `json:"config"`
	Tasks   []fundingTaskRecord `json:"tasks"`
}

type fundingStore struct {
	mu   sync.RWMutex
	path string
	data persistedFunding
}

func newFundingStore(path string) (*fundingStore, error) {
	s := &fundingStore{path: path, data: persistedFunding{Version: 1}}
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
		return nil, errors.New("unsupported funding store version")
	}
	// A run interrupted by an app quit is resumable; surface it as stopped.
	for i := range s.data.Tasks {
		if s.data.Tasks[i].State == "running" {
			s.data.Tasks[i].State = "stopped"
			s.data.Tasks[i].Message = "Interrupted by app shutdown; start again to continue"
		}
	}
	return s, nil
}

func (s *fundingStore) config() FundingConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Config
}

func (s *fundingStore) updateConfig(mutate func(*FundingConfig) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.data.Config
	if err := mutate(&next); err != nil {
		return err
	}
	s.data.Config = next
	return s.writeLocked()
}

func (s *fundingStore) tasks() []FundingTask {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]FundingTask, 0, len(s.data.Tasks))
	for _, t := range s.data.Tasks {
		out = append(out, t.FundingTask)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

func (s *fundingStore) task(id string) (fundingTaskRecord, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, t := range s.data.Tasks {
		if t.ID == id {
			t.Targets = append([]string(nil), t.Targets...)
			t.BatchMnemonics = append([]string(nil), t.BatchMnemonics...)
			t.SourceWalletIDs = append([]string(nil), t.SourceWalletIDs...)
			return t, true
		}
	}
	return fundingTaskRecord{}, false
}

func (s *fundingStore) saveTask(rec fundingTaskRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == rec.ID {
			s.data.Tasks[i] = rec
			return s.writeLocked()
		}
	}
	s.data.Tasks = append(s.data.Tasks, rec)
	return s.writeLocked()
}

func (s *fundingStore) deleteTask(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.data.Tasks {
		if s.data.Tasks[i].ID == id {
			s.data.Tasks = append(s.data.Tasks[:i], s.data.Tasks[i+1:]...)
			return s.writeLocked()
		}
	}
	return errors.New("funding task not found")
}

func (s *fundingStore) writeLocked() error {
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

// ---- Service surface ----------------------------------------------------

func (s *Service) FundingState() FundingState {
	return FundingState{Config: s.funding.config(), Tasks: s.funding.tasks()}
}

// GenerateFundingWallets creates the fixed routing wallets for one role,
// stores their keys in the encrypted vault, pins them into the funding
// config, and returns the download payload. A configured role is fixed and
// can never be regenerated from the UI.
func (s *Service) GenerateFundingWallets(role string) (FundingExport, error) {
	count, label, err := fundingRoleSpec(role)
	if err != nil {
		return FundingExport{}, err
	}
	if err := s.fundingRoleAvailable(role); err != nil {
		return FundingExport{}, err
	}
	if !s.vault.IsUnlocked() {
		return FundingExport{}, errors.New("unlock the wallet vault first; funding keys are stored encrypted")
	}
	type generated struct {
		Address    string `json:"address"`
		PrivateKey string `json:"privateKey"`
	}
	keys := make([]string, 0, count)
	wallets := make([]generated, 0, count)
	for i := 0; i < count; i++ {
		key, err := crypto.GenerateKey()
		if err != nil {
			return FundingExport{}, err
		}
		hexKey := hex.EncodeToString(crypto.FromECDSA(key))
		keys = append(keys, hexKey)
		wallets = append(wallets, generated{
			Address:    crypto.PubkeyToAddress(key.PublicKey).Hex(),
			PrivateKey: "0x" + hexKey,
		})
	}
	added, err := s.vault.ImportPrivateKeys(strings.Join(keys, "\n"), label)
	if err != nil {
		return FundingExport{}, err
	}
	if len(added) != count {
		return FundingExport{}, errors.New("vault rejected part of the generated wallet set")
	}
	refs := make([]FundingWallet, count)
	for i, summary := range added {
		refs[i] = FundingWallet{ID: summary.ID, Address: summary.Address}
	}
	err = s.funding.updateConfig(func(c *FundingConfig) error {
		switch role {
		case FundingRoleDepositCold:
			if c.DepositCold != nil {
				return errors.New("the deposit cold wallet is already configured")
			}
			c.DepositCold = &refs[0]
		case FundingRoleDepositRelays:
			if len(c.DepositRelays) > 0 {
				return errors.New("deposit relay wallets are already configured")
			}
			c.DepositRelays = refs
		case FundingRoleWithdrawRelays:
			if len(c.WithdrawRelays) > 0 {
				return errors.New("withdraw relay wallets are already configured")
			}
			c.WithdrawRelays = refs
		}
		return nil
	})
	if err != nil {
		return FundingExport{}, err
	}
	s.emitVault()
	s.emitFunding()
	payload, err := json.MarshalIndent(map[string]any{
		"role": role, "generatedAt": time.Now().UTC().Format(time.RFC3339), "wallets": wallets,
	}, "", "  ")
	if err != nil {
		return FundingExport{}, err
	}
	return FundingExport{Filename: FundingExportFilename(role), Payload: string(append(payload, '\n'))}, nil
}

// FundingExportFilename gives every downloadable secret file a distinct,
// role-tagged, timestamped name so exports can never overwrite each other.
func FundingExportFilename(role string) string {
	return fmt.Sprintf("ponsdesk-funding-%s-%s.json", role, time.Now().UTC().Format("20060102-150405"))
}

func fundingRoleSpec(role string) (count int, label string, err error) {
	switch role {
	case FundingRoleDepositCold:
		return 1, "Fund deposit cold", nil
	case FundingRoleDepositRelays:
		return fundingRelayCount, "Fund deposit relay", nil
	case FundingRoleWithdrawRelays:
		return fundingRelayCount, "Fund withdraw relay", nil
	default:
		return 0, "", fmt.Errorf("unknown funding wallet role %q", role)
	}
}

func (s *Service) fundingRoleAvailable(role string) error {
	c := s.funding.config()
	switch role {
	case FundingRoleDepositCold:
		if c.DepositCold != nil {
			return errors.New("the deposit cold wallet is already configured and fixed")
		}
	case FundingRoleDepositRelays:
		if len(c.DepositRelays) > 0 {
			return errors.New("deposit relay wallets are already configured and fixed")
		}
	case FundingRoleWithdrawRelays:
		if len(c.WithdrawRelays) > 0 {
			return errors.New("withdraw relay wallets are already configured and fixed")
		}
	}
	return nil
}

// SetFundingWithdrawCold pins the address-only withdrawal destination. Like
// the generated sets it is fixed once configured.
func (s *Service) SetFundingWithdrawCold(address string) error {
	address = strings.TrimSpace(address)
	if !common.IsHexAddress(address) {
		return errors.New("withdraw cold wallet is not a valid EVM address")
	}
	err := s.funding.updateConfig(func(c *FundingConfig) error {
		if c.WithdrawCold != "" {
			return errors.New("the withdraw cold address is already configured and fixed")
		}
		c.WithdrawCold = common.HexToAddress(address).Hex()
		return nil
	})
	if err != nil {
		return err
	}
	s.emitFunding()
	return nil
}

// CreateFundingTask registers a task and generates its five temporary relay
// batches (one mnemonic per batch, one derived wallet per target).
//
// Distribute input: one destination address per line.
// Withdraw input: one source private key per line, or the address of a wallet
// already stored in the vault.
func (s *Service) CreateFundingTask(kind, input string) (FundingTask, error) {
	if !s.funding.config().Complete() {
		return FundingTask{}, errors.New("configure all funding wallets in Settings before creating tasks")
	}
	var (
		targets   []string
		sourceIDs []string
		err       error
	)
	switch kind {
	case FundingKindDistribute:
		targets, err = parseFundingAddresses(input)
	case FundingKindWithdraw:
		targets, sourceIDs, err = s.resolveWithdrawSources(input)
	default:
		return FundingTask{}, fmt.Errorf("unknown funding task kind %q", kind)
	}
	if err != nil {
		return FundingTask{}, err
	}
	mnemonics := make([]string, fundingBatchCount)
	for i := range mnemonics {
		entropy, err := bip39.NewEntropy(256)
		if err != nil {
			return FundingTask{}, err
		}
		if mnemonics[i], err = bip39.NewMnemonic(entropy); err != nil {
			return FundingTask{}, err
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rec := fundingTaskRecord{
		FundingTask: FundingTask{
			ID: randomID(), Kind: kind, Targets: targets,
			State: "ready", Message: "Ready to start",
			HopsTotal:      fundingBatchCount + 2,
			TransfersTotal: fundingRelayCount + (fundingBatchCount+1)*len(targets),
			CreatedAt:      now, UpdatedAt: now,
		},
		BatchMnemonics:  mnemonics,
		SourceWalletIDs: sourceIDs,
	}
	if err := s.funding.saveTask(rec); err != nil {
		return FundingTask{}, err
	}
	s.emitFundingTask(rec.FundingTask)
	return rec.FundingTask, nil
}

// ExportFundingBatches returns the five batch mnemonics with their derived
// addresses, so the temporary route can be audited or recovered externally.
func (s *Service) ExportFundingBatches(taskID string) (FundingExport, error) {
	rec, ok := s.funding.task(taskID)
	if !ok {
		return FundingExport{}, errors.New("funding task not found")
	}
	type batchOut struct {
		Batch     int      `json:"batch"`
		Mnemonic  string   `json:"mnemonic"`
		Addresses []string `json:"addresses"`
	}
	out := make([]batchOut, 0, len(rec.BatchMnemonics))
	for i, mnemonic := range rec.BatchMnemonics {
		addrs, err := deriveBatchAddresses(mnemonic, len(rec.Targets))
		if err != nil {
			return FundingExport{}, err
		}
		hexAddrs := make([]string, len(addrs))
		for j, a := range addrs {
			hexAddrs[j] = a.Hex()
		}
		out = append(out, batchOut{Batch: i + 1, Mnemonic: mnemonic, Addresses: hexAddrs})
	}
	payload, err := json.MarshalIndent(map[string]any{
		"taskId": rec.ID, "kind": rec.Kind, "targets": rec.Targets, "batches": out,
	}, "", "  ")
	if err != nil {
		return FundingExport{}, err
	}
	return FundingExport{
		Filename: fmt.Sprintf("ponsdesk-funding-task-%s-batches.json", rec.ID),
		Payload:  string(append(payload, '\n')),
	}, nil
}

func (s *Service) DeleteFundingTask(id string) error {
	if s.fundingTaskActive(id) {
		return errors.New("stop the funding task before deleting it")
	}
	if err := s.funding.deleteTask(id); err != nil {
		return err
	}
	s.emitEvent("funding-task-deleted", id)
	return nil
}

func parseFundingAddresses(input string) ([]string, error) {
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, errors.New("no destination addresses supplied")
	}
	if len(fields) > maxFundingTargets {
		return nil, fmt.Errorf("a task supports at most %d wallets", maxFundingTargets)
	}
	seen := make(map[string]bool, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if !common.IsHexAddress(f) {
			return nil, fmt.Errorf("%q is not a valid EVM address", f)
		}
		addr := common.HexToAddress(f).Hex()
		if seen[strings.ToLower(addr)] {
			return nil, fmt.Errorf("address %s appears more than once", addr)
		}
		seen[strings.ToLower(addr)] = true
		out = append(out, addr)
	}
	return out, nil
}

// resolveWithdrawSources accepts private keys (imported into the vault) and
// addresses of wallets already in the vault, returning the source addresses
// plus their vault IDs in matching order.
func (s *Service) resolveWithdrawSources(input string) (addresses, ids []string, err error) {
	if !s.vault.IsUnlocked() {
		return nil, nil, errors.New("unlock the wallet vault first; withdrawal sources must sign")
	}
	fields := strings.FieldsFunc(input, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	if len(fields) == 0 {
		return nil, nil, errors.New("no source wallets supplied")
	}
	if len(fields) > maxFundingTargets {
		return nil, nil, fmt.Errorf("a task supports at most %d wallets", maxFundingTargets)
	}
	inVault := make(map[string]bool)
	for _, w := range s.vault.Summaries() {
		inVault[strings.ToLower(w.Address)] = true
	}
	var newKeys []string
	seen := make(map[string]bool, len(fields))
	for _, f := range fields {
		var addr string
		if common.IsHexAddress(f) {
			addr = common.HexToAddress(f).Hex()
			if !inVault[strings.ToLower(addr)] {
				return nil, nil, fmt.Errorf("wallet %s is not in the vault; paste its private key instead", addr)
			}
		} else {
			key, err := crypto.HexToECDSA(strings.TrimPrefix(strings.TrimSpace(f), "0x"))
			if err != nil {
				return nil, nil, fmt.Errorf("%q is neither a valid address nor a private key", short8(f))
			}
			addr = crypto.PubkeyToAddress(key.PublicKey).Hex()
			if !inVault[strings.ToLower(addr)] {
				newKeys = append(newKeys, hex.EncodeToString(crypto.FromECDSA(key)))
			}
		}
		if seen[strings.ToLower(addr)] {
			return nil, nil, fmt.Errorf("source wallet %s appears more than once", addr)
		}
		seen[strings.ToLower(addr)] = true
		addresses = append(addresses, addr)
	}
	if len(newKeys) > 0 {
		if _, err := s.vault.ImportPrivateKeys(strings.Join(newKeys, "\n"), "Withdraw source"); err != nil {
			return nil, nil, err
		}
		s.emitVault()
	}
	ids = make([]string, len(addresses))
	for i, addr := range addresses {
		ids[i] = strings.ToLower(addr)
	}
	return addresses, ids, nil
}

func short8(v string) string {
	if len(v) > 8 {
		return v[:8] + "…"
	}
	return v
}

// deriveBatchAddresses derives the batch's wallets at m/44'/60'/0'/0/i.
func deriveBatchAddresses(mnemonic string, count int) ([]common.Address, error) {
	keys, err := deriveBatchKeys(mnemonic, count)
	if err != nil {
		return nil, err
	}
	out := make([]common.Address, len(keys))
	for i, k := range keys {
		key, err := crypto.HexToECDSA(k)
		if err != nil {
			return nil, err
		}
		out[i] = crypto.PubkeyToAddress(key.PublicKey)
	}
	return out, nil
}

func deriveBatchKeys(mnemonic string, count int) ([]string, error) {
	w, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("open batch mnemonic: %w", err)
	}
	keys := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := append(accounts.DerivationPath(nil), accounts.DefaultBaseDerivationPath...)
		path[len(path)-1] = uint32(i)
		account, err := w.Derive(path, false)
		if err != nil {
			return nil, fmt.Errorf("derive batch wallet %d: %w", i, err)
		}
		key, err := w.PrivateKeyHex(account)
		if err != nil {
			return nil, fmt.Errorf("read batch wallet %d: %w", i, err)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

func (s *Service) emitFunding() { s.emitEvent("funding-updated", s.FundingState()) }

func (s *Service) emitFundingTask(task FundingTask) { s.emitEvent("funding-task-updated", task) }
