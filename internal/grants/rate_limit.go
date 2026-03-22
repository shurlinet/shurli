package grants

import (
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// DefaultOpsPerMinute is the max grant operations per peer per minute.
	DefaultOpsPerMinute = 10
)

// opsRateLimitEntry tracks ops from a single peer.
type opsRateLimitEntry struct {
	count    int
	windowAt time.Time
}

// OpsRateLimiter enforces per-peer rate limits on grant Store operations
// (create, revoke, extend, refresh). Separate from protocol-level rate
// limiting in protocol.go (which limits inbound P2P messages at 5/min).
type OpsRateLimiter struct {
	mu       sync.Mutex
	entries  map[peer.ID]*opsRateLimitEntry
	maxOps   int
	onNotify func(string, peer.ID, map[string]string) // Phase C notification callback
}

// NewOpsRateLimiter creates a per-peer ops rate limiter.
// onNotify is called when a rate limit is hit (can be nil).
func NewOpsRateLimiter(maxOps int, onNotify func(string, peer.ID, map[string]string)) *OpsRateLimiter {
	if maxOps <= 0 {
		maxOps = DefaultOpsPerMinute
	}
	return &OpsRateLimiter{
		entries:  make(map[peer.ID]*opsRateLimitEntry),
		maxOps:   maxOps,
		onNotify: onNotify,
	}
}

// SetOnNotify sets the notification callback. Called after router setup.
func (rl *OpsRateLimiter) SetOnNotify(fn func(string, peer.ID, map[string]string)) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.onNotify = fn
}

// Allow returns true if the peer is within rate limits. Must be called
// before each Store operation. Returns false and fires a notification if exceeded.
func (rl *OpsRateLimiter) Allow(peerID peer.ID) bool {
	var shouldNotify bool

	rl.mu.Lock()
	now := time.Now()

	// Prune stale entries.
	if len(rl.entries) > 100 {
		for pid, e := range rl.entries {
			if now.Sub(e.windowAt) > 2*time.Minute {
				delete(rl.entries, pid)
			}
		}
	}

	entry, exists := rl.entries[peerID]
	if !exists {
		rl.entries[peerID] = &opsRateLimitEntry{count: 1, windowAt: now}
		rl.mu.Unlock()
		return true
	}

	// Reset window after a minute.
	if now.Sub(entry.windowAt) > time.Minute {
		entry.count = 1
		entry.windowAt = now
		rl.mu.Unlock()
		return true
	}

	entry.count++
	allowed := entry.count <= rl.maxOps
	// Fire notification on first violation only.
	shouldNotify = !allowed && entry.count == rl.maxOps+1
	notifyFn := rl.onNotify
	maxOps := rl.maxOps
	rl.mu.Unlock()

	// Notification callback called OUTSIDE the lock to prevent blocking
	// all rate limit checks if the callback is slow (e.g., webhook sink).
	if shouldNotify && notifyFn != nil {
		notifyFn("grant_rate_limited", peerID, map[string]string{
			"limit":  fmt.Sprintf("%d", maxOps),
			"window": "1m",
		})
	}

	return allowed
}
