package ponsmm

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/bizipoopoo/pons-mm-desktop/internal/pons"
)

// bundleGasLimit covers curve.buy (~200k observed) plus the router hop.
const bundleGasLimit = 400_000

// headReadyTimeout bounds how long a launch waits for the first L2 height.
const headReadyTimeout = 5 * time.Second

// launchBundle is a launch plus the maker buys that ride in the same JSON-RPC
// batch. Every buy targets the CREATE2-predicted curve through the
// block-limited router, so it fills within the window or reverts.
type launchBundle struct {
	router   common.Address
	curve    common.Address // predicted
	token    common.Address // predicted
	maxBlock uint64
	sentAt   uint64 // head height when the batch left
	buys     []bundleBuy
	heads    *pons.HeadTracker
	stop     context.CancelFunc
}

type bundleBuy struct {
	wallet *Wallet
	spend  *big.Int
	tx     *types.Transaction
}

// prepareLaunchBundle predicts the launch addresses, starts the head tracker
// and pre-signs one router buy per eligible maker. Nothing is broadcast here.
// Wallet nonces are read under txMu and bumped only once the batch is accepted.
func (e *Engine) prepareLaunchBundle(ctx context.Context, params pons.V2TokenParams, pairToken, deployer common.Address) (*launchBundle, error) {
	router := e.cfg.MMRouterAddr()
	if owner, err := e.client.RouterOwner(ctx, router); err != nil {
		return nil, fmt.Errorf("mm router %s is not reachable (wrong address or network?): %w", router.Hex(), err)
	} else if owner == (common.Address{}) {
		return nil, fmt.Errorf("mm router %s has no owner; not a PonsMMRouter proxy", router.Hex())
	}
	token, curve, err := e.client.PredictV2LaunchAddresses(ctx, params, e.cfg.LaunchConfigID, pairToken, deployer)
	if err != nil {
		return nil, fmt.Errorf("predict launch addresses: %w", err)
	}

	headCtx, stop := context.WithCancel(ctx)
	heads := pons.NewHeadTracker(e.client, e.log)
	go heads.Run(headCtx)
	readyCtx, cancelReady := context.WithTimeout(ctx, headReadyTimeout)
	defer cancelReady()
	if err := heads.WaitReady(readyCtx); err != nil {
		stop()
		return nil, fmt.Errorf("no L2 head within %s; cannot pin a block window", headReadyTimeout)
	}

	eligible := e.fundedMakers()
	if !e.cfg.ConcurrentBuys && len(eligible) > 1 {
		eligible = eligible[:1]
	}
	gasReserve := ethToWei(e.cfg.GasReserveETH)
	b := &launchBundle{router: router, curve: curve, token: token, heads: heads, stop: stop}
	for _, w := range eligible {
		spend := scaleWei(w.spendableWei(gasReserve), e.cfg.BuyFraction)
		if spend.Sign() <= 0 {
			continue
		}
		b.buys = append(b.buys, bundleBuy{wallet: w, spend: spend})
	}
	e.log.Info("launch bundle prepared",
		"predicted_token", token.Hex(), "predicted_curve", curve.Hex(),
		"router", router.Hex(), "maker_buys", len(b.buys), "window_blocks", e.cfg.BundleMaxBlocksOrDefault())
	return b, nil
}

// send signs the buys against the freshest head and broadcasts the launch and
// every buy in one batch. The launch is element 0 so the sequencer sees it
// first; a buy the node rejects outright is dropped from the bundle.
func (b *launchBundle) send(ctx context.Context, e *Engine, launchTx *types.Transaction, launchPr pons.TxParams) error {
	locked := b.buys // b.buys is filtered below; unlock exactly what was locked
	for _, buy := range locked {
		buy.wallet.txMu.Lock()
	}
	defer func() {
		for _, buy := range locked {
			buy.wallet.txMu.Unlock()
		}
	}()

	head, seenAt, ok := b.heads.Latest()
	if !ok {
		return errors.New("head tracker lost the L2 height")
	}
	b.sentAt = head
	b.maxBlock = head + uint64(e.cfg.BundleMaxBlocksOrDefault())

	txs := make([]*types.Transaction, 0, 1+len(b.buys))
	txs = append(txs, launchTx)
	kept := make([]bundleBuy, 0, len(b.buys))
	for _, buy := range b.buys {
		pr := pons.TxParams{Nonce: buy.wallet.Nonce, GasLimit: bundleGasLimit, TipCap: launchPr.TipCap, FeeCap: launchPr.FeeCap}
		tx, err := buy.wallet.Signer.BuildRouterBuy(b.router, b.curve, b.maxBlock, buy.spend, pr)
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
		"buys", len(b.buys), "head_at_send", head, "head_age_ms", time.Since(seenAt).Milliseconds(),
		"max_l2_block", b.maxBlock)
	return nil
}

// close releases the head tracker.
func (b *launchBundle) close() {
	if b.stop != nil {
		b.stop()
	}
}

// burst adapts the accepted buys to the post-launch bookkeeping the burst
// path already performs.
func (b *launchBundle) burst() []launchBurstBuy {
	out := make([]launchBurstBuy, 0, len(b.buys))
	for _, buy := range b.buys {
		out = append(out, launchBurstBuy{wallet: buy.wallet, spend: buy.spend, hash: buy.tx.Hash(), bundled: true})
	}
	return out
}

// settleBundledBuy waits for one bundled buy and reports it through the same
// settlement channel as ordinary buys. A revert is expected here — it is the
// window doing its job — so it is reported as a miss, not a failure, with the
// block distances that tell whether the window should be widened or tightened.
func (e *Engine) settleBundledBuy(ctx context.Context, b *launchBundle, w *Wallet, spend *big.Int, txHash common.Hash, launchBlock uint64) {
	settled := buySettlement{wallet: w, wethIn: new(big.Int).Set(spend), txHash: txHash}
	rcpt, err := e.client.WaitReceiptEvery(ctx, txHash, 90*time.Second, 100*time.Millisecond)
	switch {
	case err != nil:
		settled.err = fmt.Errorf("bundle buy confirm: %w", err)
	case rcpt.Status != types.ReceiptStatusSuccessful:
		e.recordGas(rcpt)
		landed := rcpt.BlockNumber.Uint64()
		e.log.Warn("bundle buy missed its block window (reverted; ETH kept)",
			"wallet", w.Addr.Hex(), "tx", txHash.Hex(),
			"landed_block", landed, "max_l2_block", b.maxBlock, "sent_at_head", b.sentAt,
			"blocks_after_launch", int64(landed)-int64(launchBlock),
			"blocks_past_window", int64(landed)-int64(b.maxBlock))
		settled.err = &pons.ExpiredError{Current: landed, Max: b.maxBlock}
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
			"blocks_after_send", int64(landed)-int64(b.sentAt), "max_l2_block", b.maxBlock,
			"eth_in", weiToEthStr(settled.wethIn), "tokens", got.String())
		settled.tokensGot = got
		settled.ethAfter, _ = e.client.EthBalance(ctx, w.Addr)
	}
	select {
	case e.buySettlements <- settled:
	case <-ctx.Done():
	}
}
