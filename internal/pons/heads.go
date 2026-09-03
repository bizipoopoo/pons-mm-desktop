package pons

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
)

// HeadTracker keeps the latest L2 block height hot. Robinhood Chain (Arbitrum
// Nitro) seals a block roughly every 100ms and has no pending block worth
// reading — the sequencer's output IS the chain — so "current height" means
// the newest newHeads header. A launch bundle pins each buy's maxL2Block from
// Latest(), which is why this must never fall back to a chain read on the hot
// path: over a websocket it is pushed, over http it is polled every 100ms.
type HeadTracker struct {
	client *Client
	log    *slog.Logger

	mu     sync.RWMutex
	height uint64
	seenAt time.Time
	ready  chan struct{}
	once   sync.Once
}

// headPollInterval matches the chain's block cadence so an http-only setup is
// at most one block behind.
const headPollInterval = 100 * time.Millisecond

// NewHeadTracker constructs an idle tracker; call Run to start it.
func NewHeadTracker(client *Client, log *slog.Logger) *HeadTracker {
	if log == nil {
		log = slog.Default()
	}
	return &HeadTracker{client: client, log: log, ready: make(chan struct{})}
}

// Run tracks heads until ctx ends. It subscribes over websocket when possible,
// re-subscribing after drops, and polls otherwise.
func (h *HeadTracker) Run(ctx context.Context) {
	// Prime synchronously so callers that wait on Ready() get a real height
	// even before the first pushed header arrives.
	if n, err := h.client.BlockNumber(ctx); err == nil {
		h.set(n)
	}
	for ctx.Err() == nil {
		if h.client.isWebsocket() && h.subscribe(ctx) {
			continue // clean ctx exit or a drop; loop re-subscribes
		}
		h.poll(ctx)
		return
	}
}

// subscribe streams newHeads until ctx ends (returns true) or the subscription
// drops (returns true after a short pause so the caller re-subscribes). It
// returns false when the subscription could not be opened at all.
func (h *HeadTracker) subscribe(ctx context.Context) bool {
	heads := make(chan *types.Header, 64)
	sub, err := h.client.eth.SubscribeNewHead(ctx, heads)
	if err != nil {
		h.log.Warn("head tracker: newHeads subscription failed; polling", "err", err)
		return false
	}
	defer sub.Unsubscribe()
	h.log.Info("head tracker: subscribed to newHeads")
	for {
		select {
		case <-ctx.Done():
			return true
		case err := <-sub.Err():
			if err != nil {
				h.log.Warn("head tracker: newHeads subscription dropped; re-subscribing", "err", err)
			}
			select {
			case <-ctx.Done():
			case <-time.After(250 * time.Millisecond):
			}
			return true
		case hd := <-heads:
			if hd != nil && hd.Number != nil {
				h.set(hd.Number.Uint64())
			}
		}
	}
}

func (h *HeadTracker) poll(ctx context.Context) {
	h.log.Warn("head tracker: http endpoint; polling eth_blockNumber every 100ms (a wss endpoint is faster)")
	t := time.NewTicker(headPollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n, err := h.client.BlockNumber(ctx); err == nil {
				h.set(n)
			}
		}
	}
}

func (h *HeadTracker) set(n uint64) {
	h.mu.Lock()
	if n >= h.height {
		h.height, h.seenAt = n, time.Now()
	}
	h.mu.Unlock()
	h.once.Do(func() { close(h.ready) })
}

// Latest returns the newest height seen and when it was seen. ok is false
// before the first head arrives.
func (h *HeadTracker) Latest() (height uint64, seenAt time.Time, ok bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.height, h.seenAt, !h.seenAt.IsZero()
}

// Ready closes once the first height is known.
func (h *HeadTracker) Ready() <-chan struct{} { return h.ready }

// WaitReady blocks until the first height is known or ctx ends.
func (h *HeadTracker) WaitReady(ctx context.Context) error {
	select {
	case <-h.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
