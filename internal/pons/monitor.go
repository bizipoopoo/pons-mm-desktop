package pons

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// autoExit holds a confirmed position and sells it on the first exit trigger:
// profit target, stop-loss, dev dump, curve graduation, or the hold timeout.
// Valuation is event-driven off the curve's trade logs, with a poll safety net.
func (s *Sniper) autoExit(ctx context.Context, res Result) error {
	log := s.logger()
	token, curve := res.Launch.Token, res.Launch.Curve

	target := scaleFrac(res.CostWei, 1+s.ProfitTarget)
	var stop *big.Int
	if s.StopLossFrac > 0 {
		stop = scaleFrac(res.CostWei, 1-s.StopLossFrac)
	}
	log.Info("pons monitor started",
		"token", token.Hex(), "cost_eth", weiToEth(res.CostWei),
		"take_profit_eth", weiToEth(target), "stop_loss_eth", weiToEthOrDash(stop),
		"hold_timeout", s.holdTimeout())

	// Pre-approve the curve to pull our tokens so the exit is a single sell tx
	// with no approve on the critical path.
	if err := s.ensureApproval(ctx, token, curve, res.TokensOut); err != nil {
		log.Warn("pre-approve failed; will approve at exit", "err", err)
	}

	// Follow-the-dev: arm only if the creator actually holds tokens now (on
	// pons the whole supply mints to the curve, so a creator with a bag bought
	// it post-launch — the case worth following).
	var devBaseline, devTrip *big.Int
	if s.FollowDevExit > 0 {
		if bal, err := s.Client.TokenBalance(ctx, token, res.Launch.Deployer); err == nil && bal.Sign() > 0 {
			devBaseline = bal
			devTrip = scaleFrac(bal, 1-s.FollowDevExit)
			log.Info("dev-dump watch armed", "creator", res.Launch.Deployer.Hex(),
				"baseline", weiToStr(bal), "exit_below", weiToStr(devTrip))
		}
	}

	trades := s.Client.WatchCurveTrades(ctx, curve, log)
	deadline := time.NewTimer(s.holdTimeout())
	defer deadline.Stop()
	poll := time.NewTicker(s.pollInterval())
	defer poll.Stop()

	evaluate := func() (done bool, reason string) {
		if g, err := s.Client.Graduated(ctx, curve); err == nil && g {
			return true, "graduated"
		}
		qr, tr, err := s.Client.Reserves(ctx, curve)
		if err != nil {
			log.Warn("valuation failed", "err", err)
			return false, ""
		}
		value := QuoteOutForTokens(qr, tr, res.TokensOut, res.Info.FeeBps, res.Info.CreatorTaxBps)
		log.Info("valuation",
			"value_eth", weiToEth(value), "target_eth", weiToEth(target),
			"profitable", value.Cmp(target) >= 0)
		switch {
		case value.Cmp(target) >= 0:
			return true, "target"
		case stop != nil && value.Cmp(stop) <= 0:
			return true, "stop-loss"
		}
		return false, ""
	}

	for {
		select {
		case <-ctx.Done():
			log.Warn("shutdown requested; liquidating position", "token", token.Hex())
			cleanup, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			return s.sellAll(cleanup, res, "shutdown")

		case <-deadline.C:
			log.Info("hold timeout reached; selling", "token", token.Hex())
			return s.sellAll(ctx, res, "timeout")

		case <-tradeSignal(trades):
			if done, reason := evaluate(); done {
				return s.finishExit(ctx, res, reason)
			}

		case <-poll.C:
			// When the trade subscription is unavailable, this is the only
			// valuation path; when it is available, it is the safety net.
			if devTrip != nil {
				if bal, err := s.Client.TokenBalance(ctx, token, res.Launch.Deployer); err == nil && bal.Cmp(devTrip) <= 0 {
					log.Warn("creator dumped their holding; following exit",
						"creator", res.Launch.Deployer.Hex(), "now", weiToStr(bal), "baseline", weiToStr(devBaseline))
					return s.finishExit(ctx, res, "dev-dump")
				}
			}
			if done, reason := evaluate(); done {
				return s.finishExit(ctx, res, reason)
			}
		}
	}
}

// finishExit routes graduation (no curve sell possible) versus a normal curve
// sell.
func (s *Sniper) finishExit(ctx context.Context, res Result, reason string) error {
	if reason == "graduated" {
		s.logger().Warn("launch graduated to Uniswap; curve sell no longer possible — holding position",
			"token", res.Launch.Token.Hex(),
			"note", "sell manually on the v4 pool or extend this tool with a router path")
		return nil
	}
	return s.sellAll(ctx, res, reason)
}

// sellAll sells 100% of the holding back to the curve.
func (s *Sniper) sellAll(ctx context.Context, res Result, reason string) error {
	log := s.logger()
	token, curve := res.Launch.Token, res.Launch.Curve
	me := s.Signer.Address()

	held, err := s.Client.TokenBalance(ctx, token, me)
	if err != nil {
		return fmt.Errorf("read holding: %w", err)
	}
	if held.Sign() <= 0 {
		log.Info("nothing to sell (already flat)", "token", token.Hex(), "reason", reason)
		return nil
	}
	if err := s.ensureApproval(ctx, token, curve, held); err != nil {
		return fmt.Errorf("approve before sell: %w", err)
	}

	qr, tr, err := s.Client.Reserves(ctx, curve)
	if err != nil {
		return fmt.Errorf("reserves: %w", err)
	}
	expOut := QuoteOutForTokens(qr, tr, held, res.Info.FeeBps, res.Info.CreatorTaxBps)
	minOut := applySlippageDown(expOut, s.SlippageBps)

	nonce, err := s.Client.PendingNonce(ctx, me)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		return fmt.Errorf("gas: %w", err)
	}
	tx, err := s.Signer.BuildSell(curve, held, minOut, TxParams{
		Nonce: nonce, GasLimit: sellGasLimit, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		return err
	}
	if err := s.Client.Send(ctx, tx); err != nil {
		return fmt.Errorf("send sell: %w", err)
	}
	log.Info("sell submitted", "token", token.Hex(), "reason", reason, "tx", tx.Hash().Hex(),
		"exp_out_eth", weiToEth(expOut), "min_out_eth", weiToEth(minOut))

	rcpt, err := s.waitReceipt(ctx, tx.Hash(), 90*time.Second)
	if err != nil {
		return fmt.Errorf("sell submitted (%s) but not confirmed: %w", tx.Hash().Hex(), err)
	}
	if rcpt.Status != types.ReceiptStatusSuccessful {
		return fmt.Errorf("sell tx reverted (%s)", tx.Hash().Hex())
	}
	log.Info("position exited", "token", token.Hex(), "reason", reason, "sell_tx", tx.Hash().Hex())
	return nil
}

// ensureApproval grants the curve an allowance covering amount if it does not
// already have one (approving the max so a re-sell needs no second approve).
func (s *Sniper) ensureApproval(ctx context.Context, token, curve common.Address, amount *big.Int) error {
	me := s.Signer.Address()
	cur, err := s.Client.Allowance(ctx, token, me, curve)
	if err == nil && cur.Cmp(amount) >= 0 {
		return nil
	}
	nonce, err := s.Client.PendingNonce(ctx, me)
	if err != nil {
		return fmt.Errorf("nonce: %w", err)
	}
	tip, feeCap, err := s.Client.SuggestGas(ctx, gweiToWei(s.PriorityTipGwei))
	if err != nil {
		return fmt.Errorf("gas: %w", err)
	}
	tx, err := s.Signer.BuildApprove(token, curve, maxUint256(), TxParams{
		Nonce: nonce, GasLimit: approveGasLimit, TipCap: tip, FeeCap: feeCap,
	})
	if err != nil {
		return err
	}
	if err := s.Client.Send(ctx, tx); err != nil {
		return fmt.Errorf("send approve: %w", err)
	}
	if _, err := s.waitReceipt(ctx, tx.Hash(), 60*time.Second); err != nil {
		return fmt.Errorf("approve not confirmed: %w", err)
	}
	return nil
}

// tradeSignal returns a channel that never fires when trades is nil (poll-only
// mode), so the select falls through to the poll ticker.
func tradeSignal(trades <-chan struct{}) <-chan struct{} {
	if trades == nil {
		return nil
	}
	return trades
}
