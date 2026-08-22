package ponsmm

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// Wallet is one signing account in the pool with cached chain state.
type Wallet struct {
	Signer *pons.Signer
	Addr   common.Address

	// txMu serializes nonce allocation + broadcast for this wallet. A retail
	// exit may race an accumulation worker; the sell must be queued strictly
	// behind any buy that was already broadcast, never reuse its nonce.
	txMu sync.Mutex
	// sellMu keeps two exit passes from sizing sells against the same confirmed
	// balance while an earlier sell is still pending.
	sellMu sync.Mutex
	// balanceMu protects cached balances while buy confirmations are settled by
	// a background goroutine and the main strategy loop is selling.
	balanceMu sync.RWMutex

	// Cached on refresh; the engine keeps them roughly current itself.
	ETHWei   *big.Int
	TokenRaw *big.Int
	Nonce    uint64
}

// spendableWei is the ETH a wallet may spend on a buy after holding back the
// gas reserve.
func (w *Wallet) spendableWei(gasReserveWei *big.Int) *big.Int {
	w.balanceMu.RLock()
	defer w.balanceMu.RUnlock()
	if w.ETHWei == nil {
		return big.NewInt(0)
	}
	s := new(big.Int).Sub(w.ETHWei, gasReserveWei)
	if s.Sign() < 0 {
		return big.NewInt(0)
	}
	return s
}

func (w *Wallet) ethBalance() *big.Int {
	w.balanceMu.RLock()
	defer w.balanceMu.RUnlock()
	if w.ETHWei == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(w.ETHWei)
}

func (w *Wallet) setETHBalance(v *big.Int) {
	w.balanceMu.Lock()
	defer w.balanceMu.Unlock()
	if v == nil {
		w.ETHWei = big.NewInt(0)
		return
	}
	w.ETHWei = new(big.Int).Set(v)
}

func (w *Wallet) addETH(v *big.Int) {
	if v == nil {
		return
	}
	w.balanceMu.Lock()
	defer w.balanceMu.Unlock()
	if w.ETHWei == nil {
		w.ETHWei = big.NewInt(0)
	}
	w.ETHWei.Add(w.ETHWei, v)
}

func (w *Wallet) tokenBalance() *big.Int {
	w.balanceMu.RLock()
	defer w.balanceMu.RUnlock()
	if w.TokenRaw == nil {
		return big.NewInt(0)
	}
	return new(big.Int).Set(w.TokenRaw)
}

func (w *Wallet) setTokenBalance(v *big.Int) {
	w.balanceMu.Lock()
	defer w.balanceMu.Unlock()
	if v == nil {
		w.TokenRaw = big.NewInt(0)
		return
	}
	w.TokenRaw = new(big.Int).Set(v)
}

func (w *Wallet) subtractToken(v *big.Int) {
	if v == nil {
		return
	}
	w.balanceMu.Lock()
	defer w.balanceMu.Unlock()
	if w.TokenRaw == nil {
		w.TokenRaw = big.NewInt(0)
	}
	w.TokenRaw.Sub(w.TokenRaw, v)
	if w.TokenRaw.Sign() < 0 {
		w.TokenRaw.SetInt64(0)
	}
}

// Pool is the treasury wallet plus the market-making wallets.
type Pool struct {
	Client   *pons.Client
	Treasury *Wallet
	Makers   []*Wallet
	log      *slog.Logger

	byAddr map[common.Address]*Wallet
}

// LoadPool reads the keys file (one hex private key per line, first = treasury/
// deployer, rest = market makers) and builds a Pool bound to client's chain id.
func LoadPool(client *pons.Client, keysFile string, log *slog.Logger) (*Pool, error) {
	f, err := os.Open(keysFile)
	if err != nil {
		return nil, fmt.Errorf("open keys file: %w", err)
	}
	defer f.Close()

	var keys []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		keys = append(keys, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read keys file: %w", err)
	}
	if len(keys) < 1 {
		return nil, fmt.Errorf("keys file %s has no keys", keysFile)
	}

	return NewPool(client, keys, log)
}

// NewPool constructs a wallet pool from keys already decrypted in memory. The
// desktop app uses this path so private keys never need a plaintext temp file.
func NewPool(client *pons.Client, keys []string, log *slog.Logger) (*Pool, error) {
	if len(keys) < 1 {
		return nil, fmt.Errorf("at least one treasury key is required")
	}
	p := &Pool{Client: client, log: log, byAddr: map[common.Address]*Wallet{}}
	for i, k := range keys {
		s, err := pons.LoadSigner(k, client.ChainID())
		if err != nil {
			return nil, fmt.Errorf("key %d: %w", i, err)
		}
		w := &Wallet{Signer: s, Addr: s.Address(), ETHWei: big.NewInt(0), TokenRaw: big.NewInt(0)}
		if i == 0 {
			p.Treasury = w
		} else {
			p.Makers = append(p.Makers, w)
		}
		p.byAddr[w.Addr] = w
	}
	return p, nil
}

// All returns treasury + makers.
func (p *Pool) All() []*Wallet {
	out := make([]*Wallet, 0, len(p.Makers)+1)
	out = append(out, p.Treasury)
	out = append(out, p.Makers...)
	return out
}

// IsOurs reports whether addr belongs to the pool (used to tell our own trades
// apart from retail flow).
func (p *Pool) IsOurs(addr common.Address) bool {
	_, ok := p.byAddr[addr]
	return ok
}

// forEachWallet runs fn for every wallet concurrently and returns the first
// error. Serial per-wallet RPC round trips used to cost 1-2s each; with ten
// wallets that added tens of seconds between a launch and the monitor loop
// starting — a window where retail trades went unanswered.
func (p *Pool) forEachWallet(fn func(w *Wallet) error) error {
	wallets := p.All()
	errs := make([]error, len(wallets))
	var wg sync.WaitGroup
	for i, w := range wallets {
		wg.Add(1)
		go func(i int, w *Wallet) {
			defer wg.Done()
			errs[i] = fn(w)
		}(i, w)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

// RefreshETH reads every wallet's native balance and pending nonce.
func (p *Pool) RefreshETH(ctx context.Context) error {
	return p.forEachWallet(func(w *Wallet) error {
		bal, err := p.Client.EthBalance(ctx, w.Addr)
		if err != nil {
			return fmt.Errorf("balance %s: %w", w.Addr.Hex(), err)
		}
		w.setETHBalance(bal)
		n, err := p.Client.PendingNonce(ctx, w.Addr)
		if err != nil {
			return fmt.Errorf("nonce %s: %w", w.Addr.Hex(), err)
		}
		// Only ever advance the cached nonce. A launch fires the burst buys and
		// their sell approvals asynchronously, so a refresh that races those
		// in-flight sends can read a pending nonce that has not yet caught up —
		// writing it back verbatim would rewind the counter and make the next
		// send collide with an already-used nonce ("nonce too low").
		w.txMu.Lock()
		if n > w.Nonce {
			w.Nonce = n
		}
		w.txMu.Unlock()
		return nil
	})
}

// RefreshToken reads every wallet's launch-token balance.
func (p *Pool) RefreshToken(ctx context.Context, token common.Address) error {
	return p.forEachWallet(func(w *Wallet) error {
		bal, err := p.Client.TokenBalance(ctx, token, w.Addr)
		if err != nil {
			return fmt.Errorf("token balance %s: %w", w.Addr.Hex(), err)
		}
		w.setTokenBalance(bal)
		return nil
	})
}

// totalETH sums the cached native balances across all wallets.
func (p *Pool) totalETH() *big.Int {
	sum := big.NewInt(0)
	for _, w := range p.All() {
		sum.Add(sum, w.ethBalance())
	}
	return sum
}

// TotalTokens sums the pool's launch-token holdings across all wallets.
func (p *Pool) TotalTokens() *big.Int {
	sum := big.NewInt(0)
	for _, w := range p.All() {
		sum.Add(sum, w.tokenBalance())
	}
	return sum
}

// txParams builds gas + nonce for a wallet's next transaction, bumping the
// cached nonce so back-to-back sends from the same wallet do not collide.
func (p *Pool) txParams(ctx context.Context, w *Wallet, gasLimit uint64, extraTipWei *big.Int) (pons.TxParams, error) {
	tip, feeCap, err := p.Client.SuggestGas(ctx, extraTipWei)
	if err != nil {
		return pons.TxParams{}, fmt.Errorf("suggest gas: %w", err)
	}
	pr := pons.TxParams{Nonce: w.Nonce, GasLimit: gasLimit, TipCap: tip, FeeCap: feeCap}
	return pr, nil
}

// send broadcasts a signed tx and advances the sending wallet's cached nonce.
// Callers hold w.txMu across txParams+build+send so the nonce read here matches
// the one that was signed.
func (p *Pool) send(ctx context.Context, w *Wallet, tx *types.Transaction) error {
	if err := p.Client.Send(ctx, tx); err != nil {
		// Re-sync the cached nonce from the chain on failure. Without this a
		// wallet whose counter fell behind (e.g. a refresh raced an in-flight
		// send) would resubmit the same stale nonce forever, and a distribution
		// batch would spin on it instead of clearing the wallet.
		if n, e2 := p.Client.PendingNonce(ctx, w.Addr); e2 == nil {
			w.Nonce = n
		}
		return err
	}
	w.Nonce++
	return nil
}

// Fund tops up each maker wallet to at least perWalletWei by transferring the
// shortfall from the treasury. Sequential so the treasury nonce is linear.
func (p *Pool) Fund(ctx context.Context, perWalletWei, extraTipWei *big.Int) error {
	if err := p.RefreshETH(ctx); err != nil {
		return err
	}
	const transferGas = 21_000
	for _, w := range p.Makers {
		current := w.ethBalance()
		if current.Cmp(perWalletWei) >= 0 {
			continue
		}
		need := new(big.Int).Sub(perWalletWei, current)
		pr, err := p.txParams(ctx, p.Treasury, transferGas, extraTipWei)
		if err != nil {
			return err
		}
		tx, err := p.Treasury.Signer.BuildTransfer(w.Addr, need, pr)
		if err != nil {
			return fmt.Errorf("build fund transfer: %w", err)
		}
		if err := p.send(ctx, p.Treasury, tx); err != nil {
			return fmt.Errorf("fund %s: %w", w.Addr.Hex(), err)
		}
		p.log.Info("funded wallet", "wallet", w.Addr.Hex(), "wei", need.String(), "tx", tx.Hash().Hex())
		if _, err := p.Client.WaitReceipt(ctx, tx.Hash(), 60*time.Second); err != nil {
			return fmt.Errorf("fund %s confirm: %w", w.Addr.Hex(), err)
		}
	}
	return nil
}

// CollectETH sweeps each maker's ETH back to the treasury, holding back
// gasReserveWei plus the transfer's own gas cost.
func (p *Pool) CollectETH(ctx context.Context, gasReserveWei, extraTipWei *big.Int) error {
	if err := p.RefreshETH(ctx); err != nil {
		return err
	}
	const transferGas = 21_000
	tip, feeCap, err := p.Client.SuggestGas(ctx, extraTipWei)
	if err != nil {
		return fmt.Errorf("suggest gas: %w", err)
	}
	gasCost := new(big.Int).Mul(feeCap, big.NewInt(transferGas))
	keep := new(big.Int).Add(gasReserveWei, gasCost)
	for _, w := range p.Makers {
		current := w.ethBalance()
		if current.Sign() == 0 {
			continue
		}
		amount := new(big.Int).Sub(current, keep)
		if amount.Sign() <= 0 {
			continue
		}
		pr := pons.TxParams{Nonce: w.Nonce, GasLimit: transferGas, TipCap: tip, FeeCap: feeCap}
		tx, err := w.Signer.BuildTransfer(p.Treasury.Addr, amount, pr)
		if err != nil {
			return fmt.Errorf("build collect transfer: %w", err)
		}
		if err := p.send(ctx, w, tx); err != nil {
			return fmt.Errorf("collect %s: %w", w.Addr.Hex(), err)
		}
		p.log.Info("collected wallet ETH", "wallet", w.Addr.Hex(), "wei", amount.String(), "tx", tx.Hash().Hex())
	}
	return nil
}
