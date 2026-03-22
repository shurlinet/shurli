// Package grants manages macaroon-based capability tokens for per-peer
// data access control. The GrantStore is the node-level security boundary
// for relay data access - it decides which peers can use relay transport
// for plugin streams, independent of relay-side ACL.
//
// This is core infrastructure, not a plugin. The GrantStore is created
// by the daemon and accessed by both the daemon API and the stream
// enforcement layer (OpenPluginStream + inbound handler).
package grants

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/macaroon"
)

// Grant represents a macaroon-based data access grant for a specific peer.
type Grant struct {
	PeerID             peer.ID            `json:"-"`
	PeerIDStr          string             `json:"peer_id"`
	Token              *macaroon.Macaroon `json:"token"`
	Services           []string           `json:"services,omitempty"` // empty = all services
	ExpiresAt          time.Time          `json:"expires_at"`
	CreatedAt          time.Time          `json:"created_at"`
	Permanent          bool               `json:"permanent,omitempty"`
	MaxDelegations     int                `json:"max_delegations,omitempty"`      // 0=none (default), N=limited, -1=unlimited
	AutoRefresh        bool               `json:"auto_refresh,omitempty"`         // opt-in token refresh
	MaxRefreshes       int                `json:"max_refreshes,omitempty"`        // total allowed refreshes
	RefreshesUsed      int                `json:"refreshes_used,omitempty"`       // how many refreshes consumed
	MaxRefreshDuration time.Duration      `json:"max_refresh_duration,omitempty"` // absolute deadline from grant creation
	OriginalDuration   time.Duration      `json:"original_duration,omitempty"`    // stored for consistent refresh intervals
}

// Expired returns true if this grant has expired.
func (g *Grant) Expired() bool {
	if g.Permanent {
		return false
	}
	return time.Now().After(g.ExpiresAt)
}

// Remaining returns the time remaining until expiry. Negative means expired.
func (g *Grant) Remaining() time.Duration {
	if g.Permanent {
		return time.Duration(1<<63 - 1) // max duration
	}
	return time.Until(g.ExpiresAt)
}

// clone returns a shallow copy of the grant (safe for external use).
func (g *Grant) clone() *Grant {
	cp := *g
	if g.Services != nil {
		cp.Services = make([]string, len(g.Services))
		copy(cp.Services, g.Services)
	}
	return &cp
}

// persistedFile is the JSON structure written to disk.
type persistedFile struct {
	Version uint64  `json:"version"` // monotonic counter for replay protection (A4)
	Grants  []Grant `json:"grants"`
	HMAC    string  `json:"hmac,omitempty"`
}

// Store manages macaroon grants keyed by peer ID.
// Thread-safe. Persists to disk with HMAC integrity.
type Store struct {
	mu          sync.RWMutex
	grants      map[peer.ID]*Grant
	rootKey     []byte              // HMAC-SHA256 root key for creating/verifying macaroons
	hmacKey     []byte              // HMAC key for grants.json file integrity
	persistPath string              // file path for persistent storage
	version     uint64              // monotonic counter for replay protection
	cleanupDone    chan struct{} // closed when cleanup goroutine exits
	stopCleanup    chan struct{} // signal cleanup goroutine to stop
	cleanupStarted bool         // true after StartCleanup is called
	onRevoke       func(peer.ID)         // callback when a grant is revoked (close streams)
	onGrant        func(peer.ID, *Grant) // callback when a grant is created (P2P delivery)
	onRevokeNotify func(peer.ID)         // callback to notify peer of revocation (P2P delivery)
	onNotify       func(string, peer.ID, map[string]string) // Phase C: notification router callback (string = event type)
	deliveryWg     sync.WaitGroup       // tracks in-flight delivery goroutines for clean shutdown
	dummyToken     *macaroon.Macaroon   // pre-computed token for D1 constant-time check
	logger         *slog.Logger
}

// NewStore creates a new grant store.
// rootKey is used for creating and verifying macaroon tokens (derived from node identity).
// hmacKey is used for grants.json file integrity (derived from node identity, different domain).
func NewStore(rootKey, hmacKey []byte) *Store {
	return &Store{
		grants:      make(map[peer.ID]*Grant),
		rootKey:     rootKey,
		hmacKey:     hmacKey,
		cleanupDone: make(chan struct{}),
		stopCleanup: make(chan struct{}),
		dummyToken:  macaroon.New("shurli-node", rootKey, "dummy"),
		logger:      slog.Default(),
	}
}

// SetPersistPath sets the file path for auto-saving grants.
func (s *Store) SetPersistPath(path string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persistPath = path
}

// SetOnRevoke sets a callback invoked when a grant is revoked.
// The callback receives the peer ID whose grant was revoked.
// Used to close active streams/connections to the revoked peer (C3 mitigation).
func (s *Store) SetOnRevoke(fn func(peer.ID)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRevoke = fn
}

// SetOnGrant sets a callback invoked after a grant is created.
// Used to deliver the token to the peer over P2P (Phase B).
func (s *Store) SetOnGrant(fn func(peer.ID, *Grant)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onGrant = fn
}

// SetOnRevokeNotify sets a callback invoked after a grant is revoked.
// Used to send a revocation notice to the peer over P2P (Phase B).
// This is separate from OnRevoke (which closes connections).
func (s *Store) SetOnRevokeNotify(fn func(peer.ID)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRevokeNotify = fn
}

// SetOnNotify sets a callback invoked on grant lifecycle events (Phase C).
// The callback receives the event type string, peer ID, and metadata.
// Event types: "grant_created", "grant_revoked", "grant_extended",
// "grant_refreshed", "grant_expired".
func (s *Store) SetOnNotify(fn func(string, peer.ID, map[string]string)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onNotify = fn
}

// GrantOptions holds optional parameters for creating a grant.
type GrantOptions struct {
	AutoRefresh        bool
	MaxRefreshes       int           // total allowed refreshes
	RefreshesUsed      int           // consumed so far (for accurate caveat: remaining = max - used)
	MaxRefreshDuration time.Duration // relative duration from creation (stored on Grant, used to compute deadline)
	RefreshDeadline    time.Time     // absolute deadline (computed once at creation, baked into macaroon)
}

// Grant creates a new data access grant for the given peer.
// Returns the created grant. If a grant already exists for this peer,
// it is replaced (new grant, new token).
// maxDelegations: 0=none (default), N=limited hops, -1=unlimited.
func (s *Store) Grant(peerID peer.ID, duration time.Duration, services []string, permanent bool, maxDelegations int, opts ...GrantOptions) (*Grant, error) {
	now := time.Now()
	var expiresAt time.Time
	if permanent {
		expiresAt = time.Time{} // zero value for permanent
	} else {
		expiresAt = now.Add(duration)
	}

	var opt GrantOptions
	if len(opts) > 0 {
		opt = opts[0]
	}
	// Compute absolute refresh deadline once at creation.
	if opt.AutoRefresh && opt.MaxRefreshDuration > 0 && opt.RefreshDeadline.IsZero() {
		opt.RefreshDeadline = now.Add(opt.MaxRefreshDuration)
	}

	m := s.buildMacaroon(peerID, expiresAt, services, permanent, maxDelegations, opt)

	grant := &Grant{
		PeerID:             peerID,
		PeerIDStr:          peerID.String(),
		Token:              m,
		Services:           services,
		ExpiresAt:          expiresAt,
		CreatedAt:          now,
		Permanent:          permanent,
		MaxDelegations:     maxDelegations,
		AutoRefresh:        opt.AutoRefresh,
		MaxRefreshes:       opt.MaxRefreshes,
		MaxRefreshDuration: opt.MaxRefreshDuration,
		OriginalDuration:   duration,
	}

	// Clone for delivery BEFORE storing in map, so no concurrent Extend() can race.
	var grantCopy *Grant
	s.mu.Lock()
	onGrant := s.onGrant
	onNotify := s.onNotify
	if onGrant != nil {
		grantCopy = grant.clone()
	}
	s.grants[peerID] = grant
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after grant", "peer", shortPeerID(peerID), "error", err)
	}

	logAttrs := []any{
		"peer", shortPeerID(peerID),
		"permanent", permanent,
		"services", services,
	}
	if !permanent {
		logAttrs = append(logAttrs, "expires", expiresAt.Format(time.RFC3339))
	}
	s.logger.Info("grants: created", logAttrs...)

	// Phase C: notification.
	if onNotify != nil {
		meta := map[string]string{"permanent": fmt.Sprintf("%v", permanent)}
		if !permanent {
			meta["expires_at"] = expiresAt.Format(time.RFC3339)
		}
		onNotify("grant_created", peerID, meta)
	}

	// Phase B: deliver token to peer over P2P.
	if onGrant != nil {
		s.deliveryWg.Add(1)
		go func() {
			defer s.deliveryWg.Done()
			onGrant(peerID, grantCopy)
		}()
	}

	return grant, nil
}

// Revoke removes a grant for the given peer and closes their connections.
func (s *Store) Revoke(peerID peer.ID) error {
	s.mu.Lock()
	g, exists := s.grants[peerID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("no grant found for peer %s", shortPeerID(peerID))
	}
	// Capture metadata before deletion for the notification.
	var notifyMeta map[string]string
	if s.onNotify != nil {
		notifyMeta = map[string]string{}
		if !g.Permanent {
			notifyMeta["was_remaining"] = g.Remaining().Round(time.Second).String()
			notifyMeta["expires_at"] = g.ExpiresAt.Format(time.RFC3339)
		}
		if len(g.Services) > 0 {
			notifyMeta["services"] = strings.Join(g.Services, ",")
		}
	}
	delete(s.grants, peerID)
	onRevoke := s.onRevoke
	onRevokeNotify := s.onRevokeNotify
	onNotify := s.onNotify
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after revoke", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: revoked", "peer", shortPeerID(peerID))

	// Phase C: notification.
	if onNotify != nil {
		onNotify("grant_revoked", peerID, notifyMeta)
	}

	// Phase B: notify peer of revocation over P2P.
	if onRevokeNotify != nil {
		s.deliveryWg.Add(1)
		go func() {
			defer s.deliveryWg.Done()
			onRevokeNotify(peerID)
		}()
	}

	// C3 mitigation: close all connections to the revoked peer.
	if onRevoke != nil {
		onRevoke(peerID)
	}

	return nil
}

// Extend extends an existing grant by the given duration.
// The new expiry is calculated from NOW + duration, not from the old expiry.
func (s *Store) Extend(peerID peer.ID, duration time.Duration) error {
	s.mu.Lock()
	g, exists := s.grants[peerID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("no grant found for peer %s", shortPeerID(peerID))
	}

	newExpiry := time.Now().Add(duration)
	g.ExpiresAt = newExpiry
	g.Permanent = false // extending makes it time-limited again
	var refreshDeadline time.Time
	if g.AutoRefresh && g.MaxRefreshDuration > 0 {
		refreshDeadline = g.CreatedAt.Add(g.MaxRefreshDuration)
	}
	g.Token = s.buildMacaroon(peerID, newExpiry, g.Services, false, g.MaxDelegations, GrantOptions{
		AutoRefresh:     g.AutoRefresh,
		MaxRefreshes:    g.MaxRefreshes,
		RefreshesUsed:   g.RefreshesUsed,
		RefreshDeadline: refreshDeadline,
	})
	// Clone while holding the lock so concurrent operations can't race.
	var grantCopy *Grant
	onGrant := s.onGrant
	onNotify := s.onNotify
	if onGrant != nil {
		grantCopy = g.clone()
	}
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after extend", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: extended", "peer", shortPeerID(peerID), "new_expiry", newExpiry.Format(time.RFC3339))

	// Phase C: notification.
	if onNotify != nil {
		onNotify("grant_extended", peerID, map[string]string{
			"expires_at": newExpiry.Format(time.RFC3339),
		})
	}

	// Phase B: deliver updated token to peer (new expiry, new macaroon).
	if onGrant != nil {
		s.deliveryWg.Add(1)
		go func() {
			defer s.deliveryWg.Done()
			onGrant(peerID, grantCopy)
		}()
	}

	return nil
}

// Refresh creates a new token for a peer whose grant has auto-refresh enabled.
// The new token has the same caveats but a fresh expiry. RefreshesUsed is incremented.
// Returns the refreshed grant (with new token) or an error if refresh is not allowed.
func (s *Store) Refresh(peerID peer.ID) (*Grant, error) {
	now := time.Now()

	s.mu.Lock()
	g, exists := s.grants[peerID]
	if !exists {
		s.mu.Unlock()
		return nil, fmt.Errorf("no grant found for peer %s", shortPeerID(peerID))
	}

	if g.Permanent || now.After(g.ExpiresAt) {
		s.mu.Unlock()
		return nil, fmt.Errorf("grant for peer %s has expired", shortPeerID(peerID))
	}

	if !g.AutoRefresh {
		s.mu.Unlock()
		return nil, fmt.Errorf("grant for peer %s does not have auto-refresh enabled", shortPeerID(peerID))
	}

	if g.MaxRefreshes > 0 && g.RefreshesUsed >= g.MaxRefreshes {
		s.mu.Unlock()
		return nil, fmt.Errorf("max refreshes exhausted for peer %s (%d/%d)", shortPeerID(peerID), g.RefreshesUsed, g.MaxRefreshes)
	}

	// Check absolute refresh deadline.
	if g.MaxRefreshDuration > 0 {
		deadline := g.CreatedAt.Add(g.MaxRefreshDuration)
		if now.After(deadline) {
			s.mu.Unlock()
			return nil, fmt.Errorf("refresh deadline exceeded for peer %s", shortPeerID(peerID))
		}
	}

	// Compute new expiry using the stored original duration.
	if g.OriginalDuration <= 0 {
		s.mu.Unlock()
		return nil, fmt.Errorf("grant for peer %s has no original duration stored", shortPeerID(peerID))
	}
	newExpiry := now.Add(g.OriginalDuration)

	// Cap at refresh deadline if set.
	if g.MaxRefreshDuration > 0 {
		deadline := g.CreatedAt.Add(g.MaxRefreshDuration)
		if newExpiry.After(deadline) {
			newExpiry = deadline
		}
	}

	g.ExpiresAt = newExpiry
	g.RefreshesUsed++
	var refreshDeadline time.Time
	if g.MaxRefreshDuration > 0 {
		refreshDeadline = g.CreatedAt.Add(g.MaxRefreshDuration)
	}
	g.Token = s.buildMacaroon(peerID, newExpiry, g.Services, false, g.MaxDelegations, GrantOptions{
		AutoRefresh:     g.AutoRefresh,
		MaxRefreshes:    g.MaxRefreshes,
		RefreshesUsed:   g.RefreshesUsed,
		RefreshDeadline: refreshDeadline,
	})

	result := g.clone()
	refreshesUsed := g.RefreshesUsed
	maxRefreshes := g.MaxRefreshes
	onNotify := s.onNotify
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after refresh", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: refreshed", "peer", shortPeerID(peerID),
		"new_expiry", newExpiry.Format(time.RFC3339),
		"refreshes_used", refreshesUsed,
		"max_refreshes", maxRefreshes)

	// Phase C: notification.
	if onNotify != nil {
		onNotify("grant_refreshed", peerID, map[string]string{
			"expires_at":     newExpiry.Format(time.RFC3339),
			"refreshes_used": fmt.Sprintf("%d", refreshesUsed),
			"max_refreshes":  fmt.Sprintf("%d", maxRefreshes),
		})
	}

	// NOTE: No onGrant callback here. The refreshed token is returned directly
	// to the requesting peer via the refresh response on the same stream.
	// Firing onGrant would cause duplicate delivery (new stream + response).

	return result, nil
}

// UpdateMaxRefreshes updates the max refresh count for an existing grant.
// Rebuilds the token so caveat values are accurate, and delivers the update.
// Admin can increase or decrease at any time.
func (s *Store) UpdateMaxRefreshes(peerID peer.ID, maxRefreshes int) error {
	s.mu.Lock()
	g, exists := s.grants[peerID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("no grant found for peer %s", shortPeerID(peerID))
	}
	g.MaxRefreshes = maxRefreshes

	// Rebuild token so the max_refreshes caveat reflects the new value.
	var refreshDeadline time.Time
	if g.AutoRefresh && g.MaxRefreshDuration > 0 {
		refreshDeadline = g.CreatedAt.Add(g.MaxRefreshDuration)
	}
	g.Token = s.buildMacaroon(peerID, g.ExpiresAt, g.Services, g.Permanent, g.MaxDelegations, GrantOptions{
		AutoRefresh:     g.AutoRefresh,
		MaxRefreshes:    maxRefreshes,
		RefreshesUsed:   g.RefreshesUsed,
		RefreshDeadline: refreshDeadline,
	})

	var grantCopy *Grant
	onGrant := s.onGrant
	if onGrant != nil {
		grantCopy = g.clone()
	}
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after max-refreshes update", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: updated max_refreshes", "peer", shortPeerID(peerID), "max_refreshes", maxRefreshes)

	// Deliver updated token to peer.
	if onGrant != nil {
		s.deliveryWg.Add(1)
		go func() {
			defer s.deliveryWg.Done()
			onGrant(peerID, grantCopy)
		}()
	}

	return nil
}

// Check returns true if the given peer has a valid grant for the given service.
// This is the primary enforcement point. Called by OpenPluginStream and inbound handler.
//
// D1 mitigation: all code paths execute the same operations (map lookup, expiry check,
// HMAC verification) to prevent timing oracles that leak grant state. When no grant
// exists or it's expired, a dummy verification runs against a pre-computed token so the
// timing is dominated by HMAC regardless of outcome.
//
// The RLock is held through verification to prevent a data race with concurrent
// Extend() which can mutate ExpiresAt and Token on the same Grant pointer.
func (s *Store) Check(peerID peer.ID, service string) bool {
	verifier := macaroon.DefaultVerifier(macaroon.VerifyContext{
		PeerID:  peerID.String(),
		Service: service,
		Now:     time.Now(),
	})

	s.mu.RLock()
	g, exists := s.grants[peerID]

	if !exists || g.Expired() {
		s.mu.RUnlock()
		// D1: run a dummy HMAC verification so all paths have equal timing.
		s.dummyToken.Verify(s.rootKey, verifier)
		return false
	}

	err := g.Token.Verify(s.rootKey, verifier)
	s.mu.RUnlock()

	if err != nil {
		s.logger.Warn("grants: token verification failed",
			"peer", shortPeerID(peerID),
			"service", service,
			"error", err)
		return false
	}

	return true
}

// List returns copies of all non-expired grants. Safe for external use.
func (s *Store) List() []*Grant {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Grant
	for _, g := range s.grants {
		if !g.Expired() {
			result = append(result, g.clone())
		}
	}
	return result
}

// ExpiringWithin returns copies of grants that expire within the given duration.
// Used for MOTD notifications (E3 mitigation).
func (s *Store) ExpiringWithin(d time.Duration) []*Grant {
	s.mu.RLock()
	defer s.mu.RUnlock()

	var result []*Grant
	cutoff := time.Now().Add(d)
	for _, g := range s.grants {
		if !g.Permanent && !g.Expired() && g.ExpiresAt.Before(cutoff) {
			result = append(result, g.clone())
		}
	}
	return result
}

// StartCleanup starts a background goroutine that removes expired grants
// every interval. Logs each removal for audit trail (Q2 answer: auto-clean with logs).
func (s *Store) StartCleanup(interval time.Duration) {
	s.cleanupStarted = true
	go func() {
		defer close(s.cleanupDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				s.cleanExpired()
			case <-s.stopCleanup:
				return
			}
		}
	}()
}

// Stop stops the cleanup goroutine, waits for in-flight delivery goroutines,
// and waits for cleanup to exit. Safe to call even if StartCleanup was never called.
func (s *Store) Stop() {
	if s.cleanupStarted {
		close(s.stopCleanup)
		<-s.cleanupDone
	}
	s.deliveryWg.Wait()
}

func (s *Store) cleanExpired() {
	type expiredEntry struct {
		pid  peer.ID
		meta map[string]string
	}
	s.mu.Lock()
	var removed []expiredEntry
	captureNotify := s.onNotify != nil
	for pid, g := range s.grants {
		if g.Expired() {
			var meta map[string]string
			if captureNotify {
				meta = map[string]string{
					"expires_at": g.ExpiresAt.Format(time.RFC3339),
				}
				if len(g.Services) > 0 {
					meta["services"] = strings.Join(g.Services, ",")
				}
			}
			removed = append(removed, expiredEntry{pid: pid, meta: meta})
			delete(s.grants, pid)
		}
	}
	onRevoke := s.onRevoke
	onNotify := s.onNotify
	s.mu.Unlock()

	if len(removed) > 0 {
		if err := s.save(); err != nil {
			s.logger.Error("grants: failed to persist after cleanup", "error", err)
		}
		for _, entry := range removed {
			s.logger.Info("grants: expired grant removed, closing connections", "peer", shortPeerID(entry.pid))
			// Phase C: notification.
			if onNotify != nil {
				onNotify("grant_expired", entry.pid, entry.meta)
			}
			// B1 mitigation: close connections to peers whose grants expired.
			// This terminates any active transfers that outlived the grant.
			if onRevoke != nil {
				onRevoke(entry.pid)
			}
		}
	}
}

// buildMacaroon creates a macaroon token with the standard caveat set.
// Used by both Grant(), Extend(), and Refresh() to avoid duplication.
func (s *Store) buildMacaroon(peerID peer.ID, expiresAt time.Time, services []string, permanent bool, maxDelegations int, opts ...GrantOptions) *macaroon.Macaroon {
	id := fmt.Sprintf("grant-%s-%d", shortPeerID(peerID), time.Now().UnixNano())
	m := macaroon.New("shurli-node", s.rootKey, id)
	m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatPeerID, peerID.String()))

	if !permanent {
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatExpires, expiresAt.Format(time.RFC3339)))
	}

	if len(services) > 0 {
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatService, strings.Join(services, ",")))
	}

	// Always emit max_delegations, even when 0.
	m.AddFirstPartyCaveat(fmt.Sprintf("%s=%d", macaroon.CaveatMaxDelegations, maxDelegations))

	// Auto-refresh caveats (B4).
	if len(opts) > 0 && opts[0].AutoRefresh {
		opt := opts[0]
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=true", macaroon.CaveatAutoRefresh))
		remaining := opt.MaxRefreshes - opt.RefreshesUsed
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=%d", macaroon.CaveatMaxRefreshes, remaining))
		if !opt.RefreshDeadline.IsZero() {
			m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatMaxRefreshDuration, opt.RefreshDeadline.Format(time.RFC3339)))
		}
	}

	return m
}

// save persists the grant store to disk with HMAC integrity.
// Must not be called with s.mu held (it acquires its own lock).
func (s *Store) save() error {
	s.mu.Lock()
	path := s.persistPath
	if path == "" {
		s.mu.Unlock()
		return nil
	}

	// Increment version counter for replay protection (A4).
	s.version++
	version := s.version

	// Collect non-expired grants.
	var entries []Grant
	for _, g := range s.grants {
		if !g.Expired() {
			entries = append(entries, *g)
		}
	}
	hmacKey := s.hmacKey
	s.mu.Unlock()

	pf := persistedFile{
		Version: version,
		Grants:  entries,
	}

	// Compute HMAC over grants JSON if key is set (A4: replay protection via version).
	if len(hmacKey) > 0 {
		grantsJSON, _ := json.Marshal(entries)
		pf.HMAC = computeFileHMAC(hmacKey, version, grantsJSON)
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal grants: %w", err)
	}

	// A2 mitigation: reject symlinks before writing.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("grants file is a symlink, refusing to write (A2 mitigation)")
		}
	}

	return atomicWriteFile(path, data, 0600)
}

// Load reads the grant store from disk and verifies HMAC integrity.
func Load(path string, rootKey, hmacKey []byte) (*Store, error) {
	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil // empty store, not an error
	}
	if err != nil {
		return nil, fmt.Errorf("read grants file: %w", err)
	}

	var file persistedFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse grants file: %w", err)
	}

	// Verify HMAC integrity. When hmacKey is set, a valid HMAC is required
	// for any file that contains grants (rejects unsigned tampered files).
	grantsJSON, _ := json.Marshal(file.Grants)
	if err := verifyFileHMAC(hmacKey, file.HMAC, file.Version, grantsJSON, len(file.Grants) > 0); err != nil {
		return nil, fmt.Errorf("grants file: %w", err)
	}

	s.version = file.Version

	// Load grants, skipping expired ones.
	for _, g := range file.Grants {
		if g.Expired() {
			s.logger.Info("grants: skipping expired grant on load", "peer", truncateStr(g.PeerIDStr, 16))
			continue
		}

		pid, err := peer.Decode(g.PeerIDStr)
		if err != nil {
			s.logger.Warn("grants: skipping grant with invalid peer ID", "peer_id", g.PeerIDStr, "error", err)
			continue
		}
		g.PeerID = pid

		// Verify token integrity.
		if err := g.Token.Verify(rootKey, nil); err != nil {
			s.logger.Warn("grants: skipping grant with invalid token", "peer", shortPeerID(pid), "error", err)
			continue
		}

		s.grants[pid] = &g
	}

	s.logger.Info("grants: loaded", "count", len(s.grants), "version", s.version)
	return s, nil
}

// shortPeerID returns the first 16 chars of a peer ID string for logging.
func shortPeerID(pid peer.ID) string {
	s := pid.String()
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// truncateStr safely truncates a string to maxLen characters.
// Used for logging untrusted strings from persisted files.
func truncateStr(s string, maxLen int) string {
	if len(s) > maxLen {
		return s[:maxLen]
	}
	return s
}

// atomicWriteFile writes data to a file atomically using temp file + fsync + rename.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	base := filepath.Base(path)

	tmp, err := os.CreateTemp(dir, "."+base+".tmp.*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()

	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Chmod(perm); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}

	// Fsync directory for durability.
	if dirFd, err := os.Open(dir); err == nil {
		dirFd.Sync()
		dirFd.Close()
	}

	success = true
	return nil
}
