package control

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"math/rand"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

const (
	fundingTransferGas = 21_000
	// fundingSendWorkers bounds concurrent sweep transfers so a 500-wallet hop
	// does not flood the public RPC.
	fundingSendWorkers = 12
	fundingReceiptWait = 120 * time.Second
)

// fundingSigner pairs a signing key with its address for one routing wallet.
type fundingSigner struct {
	signer *pons.Signer
	addr   common.Address
}

// splitSpec sends one wallet's spendable balance to several recipients with
// randomized near-even amounts (single sender, sequential nonces).
type splitSpec struct {
	from fundingSigner
	to   []common.Address
}

// sweepSpec forwards one wallet's full spendable balance to one recipient.
type sweepSpec struct {
	from fundingSigner
	to   common.Address
}

// fundingHop is one settlement stage. All of a hop's transfers must confirm
// before the next hop starts, because later hops sweep the received balances.
type fundingHop struct {
	name   string
	splits []splitSpec
	sweeps []sweepSpec
}

func (h fundingHop) transferCount() int {
	n := len(h.sweeps)
	for _, sp := range h.splits {
		n += len(sp.to)
	}
	return n
}

// StartFundingTask launches (or resumes) a funding task. Every hop only moves
// balances that actually exist on chain, so restarting an interrupted task
// safely continues where it stopped: emptied wallets are skipped.
func (s *Service) StartFundingTask(id, confirmation string) error {
	if confirmation != "SEND" {
		return errors.New("funding confirmation phrase is required")
	}
	rec, ok := s.funding.task(id)
	if !ok {
		return errors.New("funding task not found")
	}
	cfg := s.funding.config()
	if !cfg.Complete() {
		return errors.New("funding wallets are not fully configured")
	}
	settings := s.config.settings()
	if settings.RPCEndpoint == "" {
		return errors.New("configure the RPC endpoint in Settings first")
	}

	// Resolve every signer up front while the vault is unlocked; the run
	// itself must not depend on vault state.
	routeIDs := []string{cfg.DepositCold.ID}
	for _, w := range cfg.DepositRelays {
		routeIDs = append(routeIDs, w.ID)
	}
	for _, w := range cfg.WithdrawRelays {
		routeIDs = append(routeIDs, w.ID)
	}
	routeKeys, err := s.vault.Keys(routeIDs)
	if err != nil {
		return err
	}
	var sourceKeys []string
	if rec.Kind == FundingKindWithdraw {
		if sourceKeys, err = s.vault.Keys(rec.SourceWalletIDs); err != nil {
			return err
		}
	}

	s.mu.Lock()
	if s.fundingRuns == nil {
		s.fundingRuns = make(map[string]context.CancelFunc)
	}
	if _, running := s.fundingRuns[id]; running {
		s.mu.Unlock()
		return errors.New("funding task is already running")
	}
	ctx, cancel := context.WithCancel(s.root)
	s.fundingRuns[id] = cancel
	s.mu.Unlock()

	rec.State, rec.Message = "running", "Connecting to Robinhood Chain"
	rec.HopsDone, rec.TransfersDone = 0, 0
	if err := s.funding.saveTask(rec); err != nil {
		s.clearFundingRun(id)
		return err
	}
	s.emitFundingTask(rec.FundingTask)
	go s.runFundingTask(ctx, rec, cfg, routeKeys, sourceKeys)
	return nil
}

func (s *Service) StopFundingTask(id string) error {
	s.mu.Lock()
	cancel := s.fundingRuns[id]
	s.mu.Unlock()
	if cancel == nil {
		return errors.New("funding task is not running")
	}
	cancel()
	return nil
}

func (s *Service) fundingTaskActive(id string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, running := s.fundingRuns[id]
	return running
}

func (s *Service) clearFundingRun(id string) {
	s.mu.Lock()
	if cancel := s.fundingRuns[id]; cancel != nil {
		cancel()
		delete(s.fundingRuns, id)
	}
	s.mu.Unlock()
}

func (s *Service) runFundingTask(ctx context.Context, rec fundingTaskRecord, cfg FundingConfig, routeKeys, sourceKeys []string) {
	finish := func(state, message string) {
		s.clearFundingRun(rec.ID)
		rec.State, rec.Message = state, message
		if err := s.funding.saveTask(rec); err == nil {
			s.emitFundingTask(rec.FundingTask)
		}
	}

	client, err := pons.Dial(ctx, s.config.settings().RPCEndpoint)
	if err != nil {
		finish("error", err.Error())
		return
	}
	defer client.Close()
	if client.ChainID() == nil || client.ChainID().Cmp(big.NewInt(pons.RobinhoodChainID)) != 0 {
		finish("error", fmt.Sprintf("connected chain ID %v; Robinhood Chain requires %d", client.ChainID(), pons.RobinhoodChainID))
		return
	}
	client.WarmGas(ctx)

	hops, err := buildFundingHops(rec, cfg, routeKeys, sourceKeys, client.ChainID())
	if err != nil {
		finish("error", err.Error())
		return
	}
	total := 0
	for _, hop := range hops {
		total += hop.transferCount()
	}
	rec.HopsTotal, rec.TransfersTotal = len(hops), total

	progress := func() {
		rec.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		s.emitFundingTask(rec.FundingTask)
	}
	for i, hop := range hops {
		rec.Message = fmt.Sprintf("Hop %d/%d: %s", i+1, len(hops), hop.name)
		progress()
		if err := s.execFundingHop(ctx, client, hop, &rec, progress); err != nil {
			if ctx.Err() != nil {
				finish("stopped", "Stopped; start again to continue from the current chain state")
				return
			}
			finish("error", fmt.Sprintf("%s failed: %v — start again to retry from the current chain state", hop.name, err))
			return
		}
		rec.HopsDone = i + 1
		if err := s.funding.saveTask(rec); err == nil {
			progress()
		}
	}
	finish("done", "All transfers confirmed")
}

// buildFundingHops lays out the full route. Distribution:
//
//	deposit cold → 10 relays → batch 1 → … → batch 5 → targets
//
// Withdrawal:
//
//	sources → batch 1 → … → batch 5 → 10 relays → withdraw cold
//
// Index i keeps the same slot across every batch, so each incoming amount
// follows one fixed path end to end.
func buildFundingHops(rec fundingTaskRecord, cfg FundingConfig, routeKeys, sourceKeys []string, chainID *big.Int) ([]fundingHop, error) {
	n := len(rec.Targets)
	load := func(hexKey string) (fundingSigner, error) {
		signer, err := pons.LoadSigner(hexKey, chainID)
		if err != nil {
			return fundingSigner{}, err
		}
		return fundingSigner{signer: signer, addr: signer.Address()}, nil
	}
	cold, err := load(routeKeys[0])
	if err != nil {
		return nil, err
	}
	depositRelays := make([]fundingSigner, fundingRelayCount)
	withdrawRelays := make([]fundingSigner, fundingRelayCount)
	for i := 0; i < fundingRelayCount; i++ {
		if depositRelays[i], err = load(routeKeys[1+i]); err != nil {
			return nil, err
		}
		if withdrawRelays[i], err = load(routeKeys[1+fundingRelayCount+i]); err != nil {
			return nil, err
		}
	}
	batches := make([][]fundingSigner, fundingBatchCount)
	for b, mnemonic := range rec.BatchMnemonics {
		keys, err := deriveBatchKeys(mnemonic, n)
		if err != nil {
			return nil, err
		}
		batches[b] = make([]fundingSigner, n)
		for i, k := range keys {
			if batches[b][i], err = load(k); err != nil {
				return nil, err
			}
		}
	}
	addrsOf := func(ws []fundingSigner) []common.Address {
		out := make([]common.Address, len(ws))
		for i, w := range ws {
			out[i] = w.addr
		}
		return out
	}
	sweepHop := func(name string, from []fundingSigner, to []common.Address) fundingHop {
		hop := fundingHop{name: name}
		for i := range from {
			hop.sweeps = append(hop.sweeps, sweepSpec{from: from[i], to: to[i]})
		}
		return hop
	}
	targetAddrs := make([]common.Address, n)
	for i, t := range rec.Targets {
		targetAddrs[i] = common.HexToAddress(t)
	}

	var hops []fundingHop
	switch rec.Kind {
	case FundingKindDistribute:
		// Relay r owns the contiguous batch slice [r*n/10, (r+1)*n/10). Only
		// relays with a non-empty slice receive money from the cold wallet, so
		// nothing strands on unused relays when there are few targets.
		var activeRelays []common.Address
		relaySplits := fundingHop{name: "relays split into batch 1"}
		for r := 0; r < fundingRelayCount; r++ {
			start, end := relaySlice(r, n)
			if start == end {
				continue
			}
			activeRelays = append(activeRelays, depositRelays[r].addr)
			relaySplits.splits = append(relaySplits.splits, splitSpec{
				from: depositRelays[r], to: addrsOf(batches[0][start:end]),
			})
		}
		hops = append(hops, fundingHop{
			name:   "deposit cold splits into relays",
			splits: []splitSpec{{from: cold, to: activeRelays}},
		})
		hops = append(hops, relaySplits)
		for b := 0; b < fundingBatchCount-1; b++ {
			hops = append(hops, sweepHop(fmt.Sprintf("batch %d forwards to batch %d", b+1, b+2),
				batches[b], addrsOf(batches[b+1])))
		}
		hops = append(hops, sweepHop("batch 5 pays the targets", batches[fundingBatchCount-1], targetAddrs))
	case FundingKindWithdraw:
		sources := make([]fundingSigner, n)
		for i, k := range sourceKeys {
			if sources[i], err = load(k); err != nil {
				return nil, err
			}
		}
		hops = append(hops, sweepHop("sources move into batch 1", sources, addrsOf(batches[0])))
		for b := 0; b < fundingBatchCount-1; b++ {
			hops = append(hops, sweepHop(fmt.Sprintf("batch %d forwards to batch %d", b+1, b+2),
				batches[b], addrsOf(batches[b+1])))
		}
		aggregate := fundingHop{name: "batch 5 gathers into relays"}
		for r := 0; r < fundingRelayCount; r++ {
			start, end := relaySlice(r, n)
			for i := start; i < end; i++ {
				aggregate.sweeps = append(aggregate.sweeps, sweepSpec{
					from: batches[fundingBatchCount-1][i], to: withdrawRelays[r].addr,
				})
			}
		}
		hops = append(hops, aggregate)
		coldAddr := common.HexToAddress(cfg.WithdrawCold)
		relaysToCold := fundingHop{name: "relays pay the withdraw cold wallet"}
		for r := 0; r < fundingRelayCount; r++ {
			relaysToCold.sweeps = append(relaysToCold.sweeps, sweepSpec{from: withdrawRelays[r], to: coldAddr})
		}
		hops = append(hops, relaysToCold)
	default:
		return nil, fmt.Errorf("unknown funding task kind %q", rec.Kind)
	}
	return hops, nil
}

// relaySlice partitions n one-to-one route slots contiguously and near-evenly
// across the ten relays.
func relaySlice(r, n int) (start, end int) {
	return r * n / fundingRelayCount, (r + 1) * n / fundingRelayCount
}

func (s *Service) execFundingHop(ctx context.Context, client *pons.Client, hop fundingHop, rec *fundingTaskRecord, progress func()) error {
	tip, feeCap, err := client.SuggestGas(ctx, nil)
	if err != nil {
		return fmt.Errorf("gas estimate: %w", err)
	}
	gasPerTx := new(big.Int).Mul(feeCap, big.NewInt(fundingTransferGas))

	var mu sync.Mutex
	var errs []error
	var hashes []common.Hash
	bump := func() {
		mu.Lock()
		rec.TransfersDone++
		mu.Unlock()
		progress()
	}
	fail := func(err error) {
		mu.Lock()
		errs = append(errs, err)
		mu.Unlock()
	}
	send := func(from fundingSigner, to common.Address, amount *big.Int, nonce uint64) (common.Hash, error) {
		tx, err := from.signer.BuildTransfer(to, amount, pons.TxParams{
			Nonce: nonce, GasLimit: fundingTransferGas, TipCap: tip, FeeCap: feeCap,
		})
		if err != nil {
			return common.Hash{}, err
		}
		if err := client.Send(ctx, tx); err != nil {
			return common.Hash{}, err
		}
		return tx.Hash(), nil
	}

	// Splits: one sender fans out sequentially-nonced transfers. Different
	// senders run concurrently.
	var wg sync.WaitGroup
	for _, spec := range hop.splits {
		wg.Add(1)
		go func(spec splitSpec) {
			defer wg.Done()
			balance, err := client.EthBalance(ctx, spec.from.addr)
			if err != nil {
				fail(fmt.Errorf("balance of %s: %w", spec.from.addr.Hex(), err))
				return
			}
			spendable := new(big.Int).Sub(balance, new(big.Int).Mul(gasPerTx, big.NewInt(int64(len(spec.to)))))
			if spendable.Sign() <= 0 {
				// Already forwarded on a previous run (or never funded): skip.
				for range spec.to {
					bump()
				}
				return
			}
			amounts := randomNearEvenSplit(spendable, len(spec.to))
			nonce, err := client.PendingNonce(ctx, spec.from.addr)
			if err != nil {
				fail(fmt.Errorf("nonce of %s: %w", spec.from.addr.Hex(), err))
				return
			}
			for i, to := range spec.to {
				hash, err := send(spec.from, to, amounts[i], nonce)
				if err != nil {
					fail(fmt.Errorf("split from %s: %w", spec.from.addr.Hex(), err))
					return
				}
				nonce++
				mu.Lock()
				hashes = append(hashes, hash)
				mu.Unlock()
				bump()
			}
		}(spec)
	}

	// Sweeps: independent senders, bounded concurrency.
	sem := make(chan struct{}, fundingSendWorkers)
	for _, spec := range hop.sweeps {
		wg.Add(1)
		go func(spec sweepSpec) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			balance, err := client.EthBalance(ctx, spec.from.addr)
			if err != nil {
				fail(fmt.Errorf("balance of %s: %w", spec.from.addr.Hex(), err))
				return
			}
			amount := new(big.Int).Sub(balance, gasPerTx)
			if amount.Sign() <= 0 {
				bump() // nothing to move: already swept or never funded
				return
			}
			nonce, err := client.PendingNonce(ctx, spec.from.addr)
			if err != nil {
				fail(fmt.Errorf("nonce of %s: %w", spec.from.addr.Hex(), err))
				return
			}
			hash, err := send(spec.from, spec.to, amount, nonce)
			if err != nil {
				fail(fmt.Errorf("sweep from %s: %w", spec.from.addr.Hex(), err))
				return
			}
			mu.Lock()
			hashes = append(hashes, hash)
			mu.Unlock()
			bump()
		}(spec)
	}
	wg.Wait()
	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	// The next hop sweeps what this hop delivered, so every receipt must land.
	sem = make(chan struct{}, fundingSendWorkers)
	for _, hash := range hashes {
		wg.Add(1)
		go func(hash common.Hash) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			rcpt, err := client.WaitReceipt(ctx, hash, fundingReceiptWait)
			if err != nil {
				fail(fmt.Errorf("confirm %s: %w", hash.Hex(), err))
				return
			}
			if rcpt.Status != 1 {
				fail(fmt.Errorf("transfer %s reverted", hash.Hex()))
			}
		}(hash)
	}
	wg.Wait()
	return errors.Join(errs...)
}

// randomNearEvenSplit divides total into n random but near-even amounts
// (each within ±15% of the mean); the last slot absorbs rounding dust.
func randomNearEvenSplit(total *big.Int, n int) []*big.Int {
	weights := make([]int64, n)
	var sum int64
	for i := range weights {
		weights[i] = 850 + rand.Int63n(301) // 850..1150 ≈ ±15%
		sum += weights[i]
	}
	out := make([]*big.Int, n)
	rest := new(big.Int).Set(total)
	for i := 0; i < n-1; i++ {
		part := new(big.Int).Mul(total, big.NewInt(weights[i]))
		part.Div(part, big.NewInt(sum))
		out[i] = part
		rest.Sub(rest, part)
	}
	out[n-1] = rest
	return out
}
