package vault

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/crypto"
	hdwallet "github.com/miguelmota/go-ethereum-hdwallet"
	"github.com/tyler-smith/go-bip39"
	"golang.org/x/crypto/scrypt"
)

const (
	vaultVersion = 1
	maxWallets   = 2_000
)

var vaultAAD = []byte("ponsdesk-vault-v1")

// Wallet is the secret record stored only inside the encrypted vault payload.
type Wallet struct {
	ID         string `json:"id"`
	Address    string `json:"address"`
	Label      string `json:"label"`
	PrivateKey string `json:"privateKey"`
}

// Summary is safe to send to the UI and logs.
type Summary struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Label   string `json:"label"`
}

type fileEnvelope struct {
	Version    int    `json:"version"`
	Salt       string `json:"salt"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
}

type payload struct {
	Wallets []Wallet `json:"wallets"`
}

// Store keeps decrypted keys only while unlocked in this process.
type Store struct {
	mu       sync.RWMutex
	path     string
	wallets  []Wallet
	key      []byte
	salt     []byte
	unlocked bool
}

func New(path string) *Store { return &Store{path: path} }

func (s *Store) Exists() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, err := os.Stat(s.path)
	return err == nil
}

func (s *Store) IsUnlocked() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.unlocked
}

func (s *Store) Unlock(password string) error {
	if password == "" {
		return errors.New("vault password is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		return fmt.Errorf("read wallet vault: %w", err)
	}
	var env fileEnvelope
	if err := json.Unmarshal(b, &env); err != nil || env.Version != vaultVersion {
		return errors.New("wallet vault format is invalid or unsupported")
	}
	salt, err := base64.StdEncoding.DecodeString(env.Salt)
	if err != nil {
		return errors.New("wallet vault salt is invalid")
	}
	nonce, err := base64.StdEncoding.DecodeString(env.Nonce)
	if err != nil {
		return errors.New("wallet vault nonce is invalid")
	}
	ciphertext, err := base64.StdEncoding.DecodeString(env.Ciphertext)
	if err != nil {
		return errors.New("wallet vault ciphertext is invalid")
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	plain, err := decrypt(key, nonce, ciphertext)
	if err != nil {
		zero(key)
		return errors.New("incorrect password or damaged wallet vault")
	}
	var data payload
	if err := json.Unmarshal(plain, &data); err != nil {
		zero(key)
		zero(plain)
		return errors.New("wallet vault payload is invalid")
	}
	zero(plain)
	s.clearLocked()
	s.wallets = data.Wallets
	s.key = key
	s.salt = salt
	s.unlocked = true
	return nil
}

// Create initializes a new empty vault. Existing files are never overwritten.
func (s *Store) Create(password string) error {
	if len(password) < 8 {
		return errors.New("vault password must contain at least 8 characters")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Stat(s.path); err == nil {
		return errors.New("wallet vault already exists")
	} else if !os.IsNotExist(err) {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := deriveKey(password, salt)
	if err != nil {
		return err
	}
	s.clearLocked()
	s.key, s.salt, s.unlocked = key, salt, true
	if err := s.saveLocked(); err != nil {
		s.clearLocked()
		return err
	}
	return nil
}

func (s *Store) Lock() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearLocked()
}

func (s *Store) clearLocked() {
	for i := range s.wallets {
		s.wallets[i].PrivateKey = ""
	}
	s.wallets = nil
	zero(s.key)
	s.key = nil
	s.salt = nil
	s.unlocked = false
}

func (s *Store) Summaries() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.unlocked {
		return nil
	}
	out := make([]Summary, 0, len(s.wallets))
	for _, w := range s.wallets {
		out = append(out, Summary{ID: w.ID, Address: w.Address, Label: w.Label})
	}
	return out
}

// Keys returns selected private keys in the requested order.
func (s *Store) Keys(ids []string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.unlocked {
		return nil, errors.New("wallet vault is locked")
	}
	byID := make(map[string]Wallet, len(s.wallets))
	for _, w := range s.wallets {
		byID[w.ID] = w
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		w, ok := byID[id]
		if !ok {
			return nil, fmt.Errorf("wallet %s is not in the vault", id)
		}
		out = append(out, w.PrivateKey)
	}
	return out, nil
}

// ImportPrivateKeys accepts newline/comma/space separated EVM private keys.
func (s *Store) ImportPrivateKeys(input, labelPrefix string) ([]Summary, error) {
	parts := strings.FieldsFunc(input, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';' || r == ' ' || r == '\t'
	})
	if len(parts) == 0 {
		return nil, errors.New("no private keys supplied")
	}
	return s.importKeys(parts, labelPrefix)
}

// ImportMnemonic derives standard EVM accounts at m/44'/60'/0'/0/i. The
// mnemonic itself is never persisted; only the derived keys enter the vault.
func (s *Store) ImportMnemonic(mnemonic string, count int, labelPrefix string) ([]Summary, error) {
	mnemonic = strings.Join(strings.Fields(mnemonic), " ")
	if !bip39.IsMnemonicValid(mnemonic) {
		return nil, errors.New("mnemonic is invalid")
	}
	if count < 1 || count > maxWallets {
		return nil, fmt.Errorf("derivation count must be between 1 and %d", maxWallets)
	}
	w, err := hdwallet.NewFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("open mnemonic wallet: %w", err)
	}
	keys := make([]string, 0, count)
	for i := 0; i < count; i++ {
		path := append(accounts.DerivationPath(nil), accounts.DefaultBaseDerivationPath...)
		path[len(path)-1] = uint32(i)
		account, err := w.Derive(path, false)
		if err != nil {
			return nil, fmt.Errorf("derive wallet %d: %w", i, err)
		}
		key, err := w.PrivateKeyHex(account)
		if err != nil {
			return nil, fmt.Errorf("read derived wallet %d: %w", i, err)
		}
		keys = append(keys, key)
	}
	return s.importKeys(keys, labelPrefix)
}

func GenerateMnemonic() (string, error) {
	entropy, err := bip39.NewEntropy(256)
	if err != nil {
		return "", err
	}
	return bip39.NewMnemonic(entropy)
}

func (s *Store) importKeys(keys []string, labelPrefix string) ([]Summary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.unlocked {
		return nil, errors.New("wallet vault is locked")
	}
	if len(s.wallets)+len(keys) > maxWallets {
		return nil, fmt.Errorf("wallet vault is limited to %d addresses", maxWallets)
	}
	if strings.TrimSpace(labelPrefix) == "" {
		labelPrefix = "Wallet"
	}
	existing := make(map[string]bool, len(s.wallets))
	for _, w := range s.wallets {
		existing[strings.ToLower(w.Address)] = true
	}
	added := make([]Summary, 0, len(keys))
	for _, raw := range keys {
		raw = strings.TrimPrefix(strings.TrimSpace(raw), "0x")
		key, err := crypto.HexToECDSA(raw)
		if err != nil {
			return nil, errors.New("one or more private keys are invalid")
		}
		address := crypto.PubkeyToAddress(key.PublicKey).Hex()
		if existing[strings.ToLower(address)] {
			continue
		}
		canonical := hex.EncodeToString(crypto.FromECDSA(key))
		id := strings.ToLower(address)
		label := fmt.Sprintf("%s %02d", strings.TrimSpace(labelPrefix), len(s.wallets)+1)
		record := Wallet{ID: id, Address: address, Label: label, PrivateKey: canonical}
		s.wallets = append(s.wallets, record)
		added = append(added, Summary{ID: id, Address: address, Label: label})
		existing[id] = true
	}
	if len(added) == 0 {
		return nil, errors.New("all supplied wallets already exist")
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return added, nil
}

func (s *Store) saveLocked() error {
	if !s.unlocked || len(s.key) == 0 {
		return errors.New("wallet vault is locked")
	}
	plain, err := json.Marshal(payload{Wallets: s.wallets})
	if err != nil {
		return err
	}
	nonce, ciphertext, err := encrypt(s.key, plain)
	zero(plain)
	if err != nil {
		return err
	}
	env := fileEnvelope{
		Version:    vaultVersion,
		Salt:       base64.StdEncoding.EncodeToString(s.salt),
		Nonce:      base64.StdEncoding.EncodeToString(nonce),
		Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
	}
	b, err := json.MarshalIndent(env, "", "  ")
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

func deriveKey(password string, salt []byte) ([]byte, error) {
	return scrypt.Key([]byte(password), salt, 1<<15, 8, 1, 32)
}

func encrypt(key, plain []byte) ([]byte, []byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return nonce, gcm.Seal(nil, nonce, plain, vaultAAD), nil
}

func decrypt(key, nonce, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return gcm.Open(nil, nonce, ciphertext, vaultAAD)
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
