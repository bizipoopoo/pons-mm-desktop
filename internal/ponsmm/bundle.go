package ponsmm

import (
	"context"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

const (
	// bundleGasLimit covers curve.buy (~200k observed) plus the router hop.
	bundleGasLimit = 400_000
	// routerLaunchGas is the router's own overhead on top of the official
	// launch: launchBlock write, event, refund.
	routerLaunchGas = 150_000
	// atomicBuyGas is one deposit-funded maker buy inside launchAndBuyAtomic
	// (curve.buy plus the deposit bookkeeping and event).
	atomicBuyGas = 250_000
	// depositGasLimit covers router.deposit.
	depositGasLimit = 120_000
	// routerWithdrawGas covers router.withdraw.
	routerWithdrawGas = 100_000
)

// launchBundle is a launch routed through PonsMMRouter together with the
// maker buys that belong to it. In window mode the buys are separate
// transactions broadcast in the launch's JSON-RPC batch and the router
// rejects any that land more than `window` L2 blocks after the launch. In
// atomic mode the buys execute inside the launch transaction itself.
type launchBundle struct {
	mode   string
	router common.Address
	curve  common.Address // CREATE2-predicted, deployer = router
	token  common.Address // predicted
	window uint64         // window mode: max blocks after launch
	buys   []bundleBuy
}

type bundleBuy struct {
	wallet *Wallet
	spend  *big.Int
	tx     *types.Transaction // window mode only
}

// prepareLaunchBundle checks the router, predicts the launch addresses with
// the router as deployer and sizes one buy per eligible maker. Nothing is
// broadcast here; in atomic mode the makers' deposits are topped up by
// ensureDeposits before the launch is sent.
func (e *Engine) prepareLaunchBundle(ctx context.Context, params pons.V2TokenParams, pairToken common.Address) (*launchBundle, error) {
	router := e.cfg.MMRouterAddr()
	if owner, err := e.client.RouterOwner(ctx, router); err != nil {
		return nil, fmt.Errorf("mm router %s is not reachable (wrong address or network?): %w", router.Hex(), err)
	} else if owner == (common.Address{}) {
		return nil, fmt.Errorf("mm router %s has no owner; not a PonsMMRouter proxy", router.Hex())
	}
	token, curve, err := e.client.PredictV2LaunchAddresses(ctx, params, e.cfg.LaunchConfigID, pairToken, router)
	if err != nil {
		return nil, fmt.Errorf("predict launch addresses: %w", err)
	}

	eligible := e.fundedMakers()
	if !e.cfg.ConcurrentBuys && len(eligible) > 1 {
		eligible = eligible[:1]
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	b := &launchBundle{mode: e.cfg.BundleModeName(), router: router, curve: curve, token: token,
		window: uint64(e.cfg.BundleMaxBlocksOrDefault())}
	for _, w := range eligible {
		spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction)
		if spend.Sign() <= 0 {
			continue
		}
		b.buys = append(b.buys, bundleBuy{wallet: w, spend: spend})
	}
	e.log.Info("launch bundle prepared", "mode", b.mode,
		"predicted_token", token.Hex(), "predicted_curve", curve.Hex(),
		"router", router.Hex(), "maker_buys", len(b.buys), "window_blocks", b.window)
	return b, nil
}

// launchGas is the treasury's gas limit for the launch transaction.
func (b *launchBundle) launchGas(base uint64) uint64 {
	base += routerLaunchGas
	if b.mode == BundleAtomic {
		base += uint64(len(b.buys)) * atomicBuyGas
	}
	return base
}

// atomicBuys is the deposit-funded buy list for launchAndBuyAtomic.
func (b *launchBundle) atomicBuys() []pons.RouterAtomicBuy {
	out := make([]pons.RouterAtomicBuy, 0, len(b.buys))
	for _, buy := range b.buys {
		out = append(out, pons.RouterAtomicBuy{Wallet: buy.wallet.Addr, QuoteIn: buy.spend})
	}
	return out
}

// ensureDeposits (atomic mode) makes sure every maker has at least its spend
// parked in the router with the treasury as operator, sending deposits for
// the shortfall and waiting for them to confirm. A wallet whose deposit is
// locked to another operator is dropped from the bundle.
func (e *Engine) ensureDeposits(ctx context.Context, b *launchBundle) error {
	operator := e.pool.Treasury.Addr
	type pending struct {
		buy  bundleBuy
		hash common.Hash
		need *big.Int
	}
	var (
		mu       sync.Mutex
		kept     = make([]bundleBuy, 0, len(b.buys))
		pendings []pending
		firstErr error
	)
	runWalletActions(true, wallets(b.buys), func(w *Wallet) {
		buy := b.findBuy(w)
		have, op, err := e.client.RouterDeposit(ctx, b.router, w.Addr)
		if err != nil {
			mu.Lock()
			if firstErr == nil {
				firstErr = fmt.Errorf("read router deposit of %s: %w", w.Addr.Hex(), err)
			}
			mu.Unlock()
			return
		}
		if have.Sign() > 0 && op != operator {
			e.log.Warn("maker deposit is locked to another operator; excluded from the atomic launch",
				"wallet", w.Addr.Hex(), "operator", op.Hex(), "treasury", operator.Hex())
			return
		}
		need := new(big.Int).Sub(buy.spend, have)
		if need.Sign() <= 0 {
			mu.Lock()
			kept = append(kept, buy)
			mu.Unlock()
			return
		}
		// The deposit itself costs gas from the same wallet; keep it under
		// what spendableWei already reserved by trimming the spend a little.
		w.txMu.Lock()
		defer w.txMu.Unlock()
		pr, err := e.pool.txParams(ctx, w, depositGasLimit, e.extraTipWei)
		if err != nil {
			e.log.Warn("deposit gas quote failed; maker excluded", "wallet", w.Addr.Hex(), "err", err)
			return
		}
		gasCost := new(big.Int).Mul(pr.FeeCap, new(big.Int).SetUint64(depositGasLimit))
		need.Sub(need, gasCost)
		if need.Sign() <= 0 {
			// Not worth a deposit transaction; buy with what is already parked.
			if have.Sign() > 0 {
				buy.spend = have
				mu.Lock()
				kept = append(kept, buy)
				mu.Unlock()
			}
			return
		}
		buy.spend = new(big.Int).Add(have, need)
		tx, err := w.Signer.BuildRouterDeposit(b.router, operator, need, pr)
		if err != nil {
			e.log.Warn("deposit build failed; maker excluded", "wallet", w.Addr.Hex(), "err", err)
			return
		}
		if err := e.pool.send(ctx, w, tx); err != nil {
			e.log.Warn("deposit send failed; maker excluded", "wallet", w.Addr.Hex(), "err", err)
			return
		}
		e.monitor.MarkOurTx(tx.Hash())
		mu.Lock()
		pendings = append(pendings, pending{buy: buy, hash: tx.Hash(), need: need})
		mu.Unlock()
	})
	if firstErr != nil {
		return firstErr
	}
	for _, p := range pendings {
		rcpt, err := e.client.WaitReceiptEvery(ctx, p.hash, 60*time.Second, 100*time.Millisecond)
		if err != nil {
			e.log.Warn("deposit confirm failed; maker excluded", "wallet", p.buy.wallet.Addr.Hex(), "err", err)
			continue
		}
		e.recordGas(rcpt)
		if rcpt.Status != types.ReceiptStatusSuccessful {
			e.log.Warn("deposit reverted; maker excluded", "wallet", p.buy.wallet.Addr.Hex(), "tx", p.hash.Hex())
			continue
		}
		e.log.Info("maker deposit parked in router", "wallet", p.buy.wallet.Addr.Hex(),
			"eth", weiToEthStr(p.need), "tx", p.hash.Hex())
		kept = append(kept, p.buy)
	}
	// Restore the original ordering so the atomic buys follow the pool order.
	ordered := make([]bundleBuy, 0, len(kept))
	for _, buy := range b.buys {
		for _, k := range kept {
			if k.wallet == buy.wallet {
				ordered = append(ordered, k)
			}
		}
	}
	b.buys = ordered
	if len(b.buys) == 0 {
		return fmt.Errorf("no maker deposit is available for the atomic launch")
	}
	return nil
}

func (b *launchBundle) findBuy(w *Wallet) bundleBuy {
	for _, buy := range b.buys {
		if buy.wallet == w {
			return buy
		}
	}
	return bundleBuy{wallet: w, spend: big.NewInt(0)}
}

func wallets(buys []bundleBuy) []*Wallet {
	out := make([]*Wallet, 0, len(buys))
	for _, buy := range buys {
		out = append(out, buy.wallet)
	}
	return out
}

// sendWindow (window mode) signs one buyAfterLaunch per maker against the
// predicted curve and broadcasts the launch and every buy in one JSON-RPC
// batch. The launch is element 0 so the sequencer sees it first; a buy the
// node rejects outright is dropped from the bundle.
func (b *launchBundle) sendWindow(ctx context.Context, e *Engine, launchTx *types.Transaction, launchPr pons.TxParams) error {
	locked := b.buys // b.buys is filtered below; unlock exactly what was locked
	for _, buy := range locked {
		buy.wallet.txMu.Lock()
	}
	defer func() {
		for _, buy := range locked {
			buy.wallet.txMu.Unlock()
		}
	}()

	txs := make([]*types.Transaction, 0, 1+len(b.buys))
	txs = append(txs, launchTx)
	kept := make([]bundleBuy, 0, len(b.buys))
	for _, buy := range b.buys {
		pr := pons.TxParams{Nonce: buy.wallet.Nonce, GasLimit: bundleGasLimit, TipCap: launchPr.TipCap, FeeCap: launchPr.FeeCap}
		tx, err := buy.wallet.Signer.BuildRouterBuyAfterLaunch(b.router, b.curve, b.window, buy.spend, pr)
		if err != nil {
			e.log.Warn("bundle buy build failed", "wallet", buy.wallet.Addr.Hex(), "err", err)
			continue
		}
		buy.tx = tx
		kept = append(kept, buy)
		txs = append(txs, tx)
	}
	b.buys = kept

	errs := e.client.SendRawBatch(ctx, txs)
	if errs[0] != nil {
		// The launch itself was refused: nothing else in the bundle can fill.
		if n, err := e.client.PendingNonce(ctx, e.pool.Treasury.Addr); err == nil {
			e.pool.Treasury.Nonce = n
		}
		return fmt.Errorf("send launch: %w", errs[0])
	}
	e.pool.Treasury.Nonce++
	accepted := make([]bundleBuy, 0, len(b.buys))
	for i, buy := range b.buys {
		if err := errs[i+1]; err != nil {
			e.log.Warn("bundle buy rejected by node", "wallet", buy.wallet.Addr.Hex(), "err", err)
			if n, e2 := e.client.PendingNonce(ctx, buy.wallet.Addr); e2 == nil {
				buy.wallet.Nonce = n
			}
			continue
		}
		buy.wallet.Nonce++
		accepted = append(accepted, buy)
	}
	b.buys = accepted
	e.log.Info("launch bundle broadcast", "launch_tx", launchTx.Hash().Hex(),
		"buys", len(b.buys), "window_blocks_after_launch", b.window)
	return nil
}

// burst adapts the accepted window-mode buys to the post-launch bookkeeping
// the burst path already performs.
func (b *launchBundle) burst() []launchBurstBuy {
	out := make([]launchBurstBuy, 0, len(b.buys))
	for _, buy := range b.buys {
		out = append(out, launchBurstBuy{wallet: buy.wallet, spend: buy.spend, hash: buy.tx.Hash(), bundled: true})
	}
	return out
}

// settleBundledBuy waits for one window-mode buy and reports it through the
// same settlement channel as ordinary buys. A revert is expected here — it is
// the window doing its job — so it is reported as a miss, not a failure, with
// the block distance that tells whether the window should be widened.
func (e *Engine) settleBundledBuy(ctx context.Context, b *launchBundle, w *Wallet, spend *big.Int, txHash common.Hash, launchBlock uint64) {
	settled := buySettlement{wallet: w, wethIn: new(big.Int).Set(spend), txHash: txHash}
	maxBlock := launchBlock + b.window
	rcpt, err := e.client.WaitReceiptEvery(ctx, txHash, 90*time.Second, 100*time.Millisecond)
	switch {
	case err != nil:
		settled.err = fmt.Errorf("bundle buy confirm: %w", err)
	case rcpt.Status != types.ReceiptStatusSuccessful:
		e.recordGas(rcpt)
		landed := rcpt.BlockNumber.Uint64()
		e.log.Warn("bundle buy missed its block window (reverted; ETH kept)",
			"wallet", w.Addr.Hex(), "tx", txHash.Hex(),
			"landed_block", landed, "launch_block", launchBlock, "max_l2_block", maxBlock,
			"blocks_after_launch", int64(landed)-int64(launchBlock),
			"blocks_past_window", int64(landed)-int64(maxBlock))
		settled.err = &pons.ExpiredError{Current: landed, Max: maxBlock}
		if bal, balErr := e.client.EthBalance(ctx, w.Addr); balErr == nil {
			w.setETHBalance(bal)
		}
	default:
		e.recordGas(rcpt)
		landed := rcpt.BlockNumber.Uint64()
		got := big.NewInt(0)
		if routed, ok := pons.RoutedBuyFromReceipt(rcpt, b.router, w.Addr); ok {
			got = routed.TokensOut
			if routed.Refunded != nil && routed.Refunded.Sign() > 0 {
				settled.wethIn.Sub(settled.wethIn, routed.Refunded)
			}
		} else {
			for _, trade := range pons.CurveTradesFromReceipt(rcpt) {
				if trade.IsBuy && trade.Recipient == w.Addr {
					got.Add(got, trade.TokenAmount)
				}
			}
		}
		e.log.Info("bundle buy landed", "wallet", w.Addr.Hex(), "tx", txHash.Hex(),
			"landed_block", landed, "blocks_after_launch", int64(landed)-int64(launchBlock),
			"max_l2_block", maxBlock, "eth_in", weiToEthStr(settled.wethIn), "tokens", got.String())
		settled.tokensGot = got
		settled.ethAfter, _ = e.client.EthBalance(ctx, w.Addr)
	}
	select {
	case e.buySettlements <- settled:
	case <-ctx.Done():
	}
}

// settleAtomicBuys (atomic mode) books every maker fill carried by the launch
// receipt itself and returns leftover deposits to the makers' wallets.
func (e *Engine) settleAtomicBuys(ctx context.Context, b *launchBundle, rcpt *types.Receipt) {
	routed := pons.RoutedBuysFromReceipt(rcpt, b.router)
	byWallet := make(map[common.Address]pons.RoutedBuy, len(routed))
	for _, rb := range routed {
		byWallet[rb.Recipient] = rb
	}
	for _, buy := range b.buys {
		w := buy.wallet
		rb, ok := byWallet[w.Addr]
		if !ok {
			e.log.Warn("atomic launch receipt carried no fill for maker", "wallet", w.Addr.Hex())
			continue
		}
		spent := new(big.Int).Set(rb.QuoteIn)
		if rb.Refunded != nil {
			spent.Sub(spent, rb.Refunded)
		}
		w.setTokenBalance(rb.TokensOut)
		e.monitor.RecordOurBuy(spent, rb.TokensOut)
		e.recordBuy(spent)
		e.log.Info("atomic maker buy filled inside the launch transaction",
			"wallet", w.Addr.Hex(), "eth_in", weiToEthStr(spent), "tokens", rb.TokensOut.String())
	}
	if err := e.pool.RefreshETH(ctx); err != nil {
		e.log.Warn("refresh wallet ETH after atomic launch failed", "err", err)
	}
	go e.withdrawDeposits(ctx, b)
}

// withdrawDeposits pulls back whatever the makers still have parked in the
// router (clamped-fill refunds, or the spend of a launch that reverted).
func (e *Engine) withdrawDeposits(ctx context.Context, b *launchBundle) {
	runWalletActions(true, wallets(b.buys), func(w *Wallet) {
		have, _, err := e.client.RouterDeposit(ctx, b.router, w.Addr)
		if err != nil || have.Sign() == 0 {
			return
		}
		w.txMu.Lock()
		defer w.txMu.Unlock()
		pr, err := e.pool.txParams(ctx, w, routerWithdrawGas, e.extraTipWei)
		if err != nil {
			return
		}
		tx, err := w.Signer.BuildRouterWithdraw(b.router, big.NewInt(0), pr)
		if err != nil {
			return
		}
		if err := e.pool.send(ctx, w, tx); err != nil {
			e.log.Warn("router deposit withdraw failed", "wallet", w.Addr.Hex(), "eth", weiToEthStr(have), "err", err)
			return
		}
		e.monitor.MarkOurTx(tx.Hash())
		if rcpt, err := e.client.WaitReceiptEvery(ctx, tx.Hash(), 60*time.Second, 100*time.Millisecond); err == nil {
			e.recordGas(rcpt)
			if bal, err := e.client.EthBalance(ctx, w.Addr); err == nil {
				w.setETHBalance(bal)
			}
		}
		e.log.Info("router deposit returned to maker", "wallet", w.Addr.Hex(), "eth", weiToEthStr(have), "tx", tx.Hash().Hex())
	})
}
