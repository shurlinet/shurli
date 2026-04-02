package relay

import (
	"log/slog"
	"math"
	"sync"
	"sync/atomic"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/grants"
)

// BudgetTracker manages per-peer relay data budgets.
//
// Grant peers get cumulative tracking: all circuits for the same peer share
// one atomic counter. Reconnecting does NOT reset the budget (SEC2).
//
// Non-grant peers get per-circuit budgets (fresh counter per circuit = default).
// Admin peers bypass entirely (SEC4).
//
// Budget resets on: grant extend, new grant (C5). Revoke sets remaining to 0 (SEC7).
type BudgetTracker struct {
	mu           sync.RWMutex
	peers        map[peer.ID]*peerBudget
	grantStore   *grants.Store
	defaultLimit int64 // relay's configured session_data_limit (for non-grant peers per circuit)
}

// peerBudget tracks cumulative relay data usage for a granted peer.
// The remaining counter is shared across all limitedStreams for this peer (C4).
type peerBudget struct {
	remaining atomic.Int64 // bytes remaining, decremented by Read/Write
	total     int64        // initial budget (for logging)
}

// NewBudgetTracker creates a budget tracker wired to the grant store.
// defaultLimit is the relay's session_data_limit used for non-grant peers (per circuit).
func NewBudgetTracker(gs *grants.Store, defaultLimit int64) *BudgetTracker {
	return &BudgetTracker{
		peers:        make(map[peer.ID]*peerBudget),
		grantStore:   gs,
		defaultLimit: defaultLimit,
	}
}

// RemainingBudget returns the remaining bytes for a granted peer.
// Returns 0 if the peer has no budget entry (non-grant peers handled separately).
func (bt *BudgetTracker) RemainingBudget(p peer.ID) int64 {
	bt.mu.RLock()
	pb, ok := bt.peers[p]
	bt.mu.RUnlock()
	if !ok {
		return 0
	}
	r := pb.remaining.Load()
	if r < 0 {
		return 0
	}
	return r
}

// HasBudget returns true if this peer has a budget entry in the tracker.
// Non-grant peers won't have entries (they get per-circuit defaults).
func (bt *BudgetTracker) HasBudget(p peer.ID) bool {
	bt.mu.RLock()
	_, ok := bt.peers[p]
	bt.mu.RUnlock()
	return ok
}

// DefaultLimit returns the per-circuit budget for non-grant peers.
func (bt *BudgetTracker) DefaultLimit() int64 {
	return bt.defaultLimit
}

// ConsumeBytes decrements the budget for a granted peer.
// Called by limitedStream on each Read/Write.
// Returns the remaining bytes after this consumption.
func (bt *BudgetTracker) ConsumeBytes(p peer.ID, n int64) int64 {
	bt.mu.RLock()
	pb, ok := bt.peers[p]
	bt.mu.RUnlock()
	if !ok {
		return 0
	}
	return pb.remaining.Add(-n)
}

// OnGrantOrExtend is the callback for grant creation and extension (C5).
// Computes the budget from the grant's DataBudget field and resets/initializes
// the peer's counter. Called from grantStore.SetOnGrant.
func (bt *BudgetTracker) OnGrantOrExtend(p peer.ID, g *grants.Grant) {
	budget := bt.computeBudget(g.DataBudget)
	bt.mu.Lock()
	pb, ok := bt.peers[p]
	if !ok {
		pb = &peerBudget{total: budget}
		bt.peers[p] = pb
	} else {
		pb.total = budget
	}
	pb.remaining.Store(budget)
	bt.mu.Unlock()

	short := p.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	slog.Info("budget tracker: budget set",
		"peer", short,
		"budget_bytes", budget)
}

// OnRevoke sets remaining to 0 for the revoked peer (SEC7).
// Double protection with ClosePeer — even if ClosePeer races with an
// in-flight Read/Write, the next operation sees 0 remaining.
func (bt *BudgetTracker) OnRevoke(p peer.ID) {
	bt.mu.RLock()
	pb, ok := bt.peers[p]
	bt.mu.RUnlock()
	if ok {
		pb.remaining.Store(0)
	}
	// Remove from map on next grant/extend. Don't delete now — active
	// limitedStreams may still reference it during teardown.
}

// computeBudget resolves a grant's DataBudget field to actual bytes (I9).
//   - >0: use the grant's explicit budget
//   - ==0: use relay's configured default (session_data_limit)
//   - ==-1 (unlimited): use MaxInt64
func (bt *BudgetTracker) computeBudget(dataBudget int64) int64 {
	switch {
	case dataBudget > 0:
		return dataBudget
	case dataBudget == -1:
		return math.MaxInt64
	default:
		return bt.defaultLimit
	}
}
