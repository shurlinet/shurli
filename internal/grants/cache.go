package grants

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// GrantReceipt is cached locally by the peer daemon.
// Populated from grant-receipt protocol messages delivered by relays.
type GrantReceipt struct {
	RelayPeerID      peer.ID       `json:"relay_peer_id"`
	GrantDuration    time.Duration `json:"grant_duration"`
	SessionDataLimit int64         `json:"session_data_limit"` // bytes per direction per session (0=unlimited)
	SessionDuration  time.Duration `json:"session_duration"`
	Permanent        bool          `json:"permanent"`
	ReceivedAt       time.Time     `json:"received_at"` // local clock when receipt arrived
	IssuedAt         time.Time     `json:"issued_at"`   // relay clock (for drift detection + ordering)
	HMAC             [32]byte      `json:"hmac"`        // relay's HMAC (stored for future verification)

	// Circuit budget tracking (H7). Per-session, NOT persisted.
	CircuitBytesSent     int64     `json:"-"`
	CircuitBytesReceived int64     `json:"-"`
	CircuitStartedAt     time.Time `json:"-"`
}

// ExpiresAt returns the local expiry time. Zero for permanent grants.
func (r *GrantReceipt) ExpiresAt() time.Time {
	if r.Permanent {
		return time.Time{}
	}
	return r.ReceivedAt.Add(r.GrantDuration)
}

// Expired checks if the grant has expired according to local clock.
func (r *GrantReceipt) Expired() bool {
	if r.Permanent {
		return false
	}
	return time.Now().After(r.ExpiresAt())
}

// Remaining returns time left on the grant. MaxInt64 for permanent.
func (r *GrantReceipt) Remaining() time.Duration {
	if r.Permanent {
		return time.Duration(math.MaxInt64)
	}
	rem := time.Until(r.ExpiresAt())
	if rem < 0 {
		return 0
	}
	return rem
}

// RemainingBudget returns bytes remaining in current session budget.
// Returns MaxInt64 for unlimited sessions.
func (r *GrantReceipt) RemainingBudget(direction string) int64 {
	if r.SessionDataLimit == 0 {
		return math.MaxInt64
	}
	used := r.CircuitBytesSent
	if direction == "recv" {
		used = r.CircuitBytesReceived
	}
	rem := r.SessionDataLimit - used
	if rem < 0 {
		return 0
	}
	return rem
}

// HasSufficientBudget checks if the session budget can handle fileSize bytes.
func (r *GrantReceipt) HasSufficientBudget(fileSize int64, direction string) bool {
	return r.RemainingBudget(direction) >= fileSize
}

// cacheFile is the JSON structure written to disk.
type cacheFile struct {
	Version uint64          `json:"version"`
	Entries []*GrantReceipt `json:"entries"`
	HMAC    string          `json:"hmac,omitempty"`
}

// GrantCache is the client-side cache of grant receipts from relays.
// Thread-safe. Persists to disk with HMAC integrity.
type GrantCache struct {
	mu          sync.RWMutex
	receipts    map[peer.ID]*GrantReceipt
	version     uint64
	persistPath string
	hmacKey     []byte // HKDF(client_identity_secret, "grant-cache/v1")

	// Cleanup goroutine control.
	stopCh         chan struct{}
	doneCh         chan struct{}
	cleanupStarted bool
}

// NewGrantCache creates a new empty grant cache.
func NewGrantCache(hmacKey []byte) *GrantCache {
	return &GrantCache{
		receipts: make(map[peer.ID]*GrantReceipt),
		hmacKey:  hmacKey,
		stopCh:   make(chan struct{}),
		doneCh:   make(chan struct{}),
	}
}

// SetPersistPath sets the file path for cache persistence.
func (c *GrantCache) SetPersistPath(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persistPath = path
}

// Get returns a copy of the cached receipt for a relay, or nil.
func (c *GrantCache) Get(relayID peer.ID) *GrantReceipt {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.receipts[relayID]
	if !ok {
		return nil
	}
	cp := *r
	return &cp
}

// Put stores a receipt, replacing any existing one for that relay.
func (c *GrantCache) Put(receipt *GrantReceipt) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := *receipt
	c.receipts[cp.RelayPeerID] = &cp
	c.version++
	c.persistLocked()
}

// Delete removes a receipt for a relay.
func (c *GrantCache) Delete(relayID peer.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.receipts[relayID]; !ok {
		return
	}
	delete(c.receipts, relayID)
	c.version++
	c.persistLocked()
}

// HandleRevocation processes a revocation with issued_at ordering (H12).
// A stale revocation (issued before the current cached grant) is ignored.
func (c *GrantCache) HandleRevocation(relayID peer.ID, revokedAt time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.receipts[relayID]
	if !ok {
		return
	}
	// H12: only process revocation if it was issued at or after the current receipt.
	if cached.IssuedAt.After(revokedAt) {
		slog.Info("grant-cache: ignoring stale revocation (newer grant exists)",
			"relay", shortPeerID(relayID),
			"grant_issued", cached.IssuedAt.Unix(),
			"revoke_issued", revokedAt.Unix())
		return
	}
	delete(c.receipts, relayID)
	c.version++
	c.persistLocked()
	slog.Info("grant-cache: cleared on revocation", "relay", shortPeerID(relayID))
}

// TrackCircuitBytes increments the cumulative byte counter for a relay circuit.
// Clamps to MaxInt64 to prevent overflow/wrap-around.
func (c *GrantCache) TrackCircuitBytes(relayID peer.ID, direction string, n int64) {
	if n <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.receipts[relayID]
	if !ok {
		return
	}
	if direction == "recv" {
		if r.CircuitBytesReceived > math.MaxInt64-n {
			r.CircuitBytesReceived = math.MaxInt64
		} else {
			r.CircuitBytesReceived += n
		}
	} else {
		if r.CircuitBytesSent > math.MaxInt64-n {
			r.CircuitBytesSent = math.MaxInt64
		} else {
			r.CircuitBytesSent += n
		}
	}
}

// ResetCircuitCounters resets the per-circuit byte counters (called on new circuit).
func (c *GrantCache) ResetCircuitCounters(relayID peer.ID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	r, ok := c.receipts[relayID]
	if !ok {
		return
	}
	r.CircuitBytesSent = 0
	r.CircuitBytesReceived = 0
	r.CircuitStartedAt = time.Now()
}

// HasSufficientBudget checks if the cached receipt for relayID can handle fileSize.
func (c *GrantCache) HasSufficientBudget(relayID peer.ID, fileSize int64, direction string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	r, ok := c.receipts[relayID]
	if !ok {
		return false
	}
	return r.HasSufficientBudget(fileSize, direction)
}

// All returns a snapshot of all cached receipts (copies).
func (c *GrantCache) All() []*GrantReceipt {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]*GrantReceipt, 0, len(c.receipts))
	for _, r := range c.receipts {
		cp := *r
		out = append(out, &cp)
	}
	return out
}

// Len returns the number of cached receipts.
func (c *GrantCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.receipts)
}

// StartCleanup runs a background goroutine that removes expired entries.
func (c *GrantCache) StartCleanup(interval time.Duration) {
	c.mu.Lock()
	c.cleanupStarted = true
	c.mu.Unlock()
	go func() {
		defer close(c.doneCh)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				c.cleanExpired()
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop halts the cleanup goroutine and persists final state.
// Safe to call even if StartCleanup was never called.
func (c *GrantCache) Stop() {
	c.mu.Lock()
	started := c.cleanupStarted
	c.mu.Unlock()

	if !started {
		// No goroutine to stop. Just persist.
		c.mu.Lock()
		defer c.mu.Unlock()
		c.persistLocked()
		return
	}
	close(c.stopCh)
	<-c.doneCh
	c.mu.Lock()
	defer c.mu.Unlock()
	c.persistLocked()
}

// cleanExpired removes entries whose grant has expired.
func (c *GrantCache) cleanExpired() {
	c.mu.Lock()
	defer c.mu.Unlock()
	changed := false
	for id, r := range c.receipts {
		if r.Expired() {
			delete(c.receipts, id)
			changed = true
			slog.Info("grant-cache: expired entry removed", "relay", shortPeerID(id))
		}
	}
	if changed {
		c.version++
		c.persistLocked()
	}
}

// persistLocked writes the cache to disk. Must be called with mu held.
func (c *GrantCache) persistLocked() {
	if c.persistPath == "" {
		return
	}

	entries := make([]*GrantReceipt, 0, len(c.receipts))
	for _, r := range c.receipts {
		entries = append(entries, r)
	}

	cf := cacheFile{
		Version: c.version,
		Entries: entries,
	}

	// HMAC over marshaled entries (same pattern as pouch.go).
	if len(c.hmacKey) > 0 {
		entriesJSON, marshalErr := json.Marshal(entries)
		if marshalErr != nil {
			slog.Warn("grant-cache: marshal entries for HMAC failed", "error", marshalErr)
			return
		}
		cf.HMAC = computeFileHMAC(c.hmacKey, c.version, entriesJSON)
	}

	data, err := json.MarshalIndent(cf, "", "  ")
	if err != nil {
		slog.Warn("grant-cache: marshal failed", "error", err)
		return
	}

	// Symlink check (defense-in-depth).
	if info, statErr := os.Lstat(c.persistPath); statErr == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			slog.Warn("grant-cache: refusing to write to symlink", "path", c.persistPath)
			return
		}
	}

	if writeErr := atomicWriteFile(c.persistPath, data, 0600); writeErr != nil {
		slog.Warn("grant-cache: persist failed", "error", writeErr)
	}
}

// LoadGrantCache reads the cache from disk and verifies HMAC integrity.
func LoadGrantCache(path string, hmacKey []byte) (*GrantCache, error) {
	gc := NewGrantCache(hmacKey)
	gc.SetPersistPath(path)

	// Stat first to reject oversized files (defense-in-depth).
	// Cache file should be a few KB at most (one entry per relay, ~200 bytes each).
	const maxCacheFileSize = 1 << 20 // 1MB
	info, statErr := os.Stat(path)
	if os.IsNotExist(statErr) {
		return gc, nil
	}
	if statErr != nil {
		return nil, fmt.Errorf("stat grant cache file: %w", statErr)
	}
	if info.Size() > maxCacheFileSize {
		return nil, fmt.Errorf("grant cache file too large: %d bytes (max %d)", info.Size(), maxCacheFileSize)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read grant cache file: %w", err)
	}

	var cf cacheFile
	if err := json.Unmarshal(data, &cf); err != nil {
		return nil, fmt.Errorf("unmarshal grant cache: %w", err)
	}

	// HMAC verification over marshaled entries (same pattern as pouch.go).
	entriesJSON, _ := json.Marshal(cf.Entries)
	if err := verifyFileHMAC(hmacKey, cf.HMAC, cf.Version, entriesJSON, len(cf.Entries) > 0); err != nil {
		return nil, fmt.Errorf("grant cache: %w", err)
	}

	gc.version = cf.Version
	for _, entry := range cf.Entries {
		if entry.RelayPeerID == "" {
			continue
		}
		// Validate peer ID is decodable (rejects corrupted entries).
		if _, err := peer.Decode(entry.RelayPeerID.String()); err != nil {
			slog.Warn("grant-cache: skipping entry with invalid relay ID",
				"relay_id", truncateStr(entry.RelayPeerID.String(), 16), "error", err)
			continue
		}
		gc.receipts[entry.RelayPeerID] = entry
	}

	return gc, nil
}

// ClockDrift returns the estimated clock drift between local and relay clocks.
// Positive means local clock is ahead. Returns zero if receipt is nil.
func (r *GrantReceipt) ClockDrift() time.Duration {
	return r.ReceivedAt.Sub(r.IssuedAt)
}
