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
	PeerID    peer.ID            `json:"-"`
	PeerIDStr string             `json:"peer_id"`
	Token     *macaroon.Macaroon `json:"token"`
	Services  []string           `json:"services,omitempty"` // empty = all services
	ExpiresAt time.Time          `json:"expires_at"`
	CreatedAt time.Time          `json:"created_at"`
	Permanent bool               `json:"permanent,omitempty"`
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

// Grant creates a new data access grant for the given peer.
// Returns the created grant. If a grant already exists for this peer,
// it is replaced (new grant, new token).
func (s *Store) Grant(peerID peer.ID, duration time.Duration, services []string, permanent bool) (*Grant, error) {
	now := time.Now()
	var expiresAt time.Time
	if permanent {
		expiresAt = time.Time{} // zero value for permanent
	} else {
		expiresAt = now.Add(duration)
	}

	m := s.buildMacaroon(peerID, expiresAt, services, permanent)

	grant := &Grant{
		PeerID:    peerID,
		PeerIDStr: peerID.String(),
		Token:     m,
		Services:  services,
		ExpiresAt: expiresAt,
		CreatedAt: now,
		Permanent: permanent,
	}

	// Clone for delivery BEFORE storing in map, so no concurrent Extend() can race.
	var grantCopy *Grant
	s.mu.Lock()
	onGrant := s.onGrant
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
	_, exists := s.grants[peerID]
	if !exists {
		s.mu.Unlock()
		return fmt.Errorf("no grant found for peer %s", shortPeerID(peerID))
	}
	delete(s.grants, peerID)
	onRevoke := s.onRevoke
	onRevokeNotify := s.onRevokeNotify
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after revoke", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: revoked", "peer", shortPeerID(peerID))

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
	g.Token = s.buildMacaroon(peerID, newExpiry, g.Services, false)
	// Clone while holding the lock so concurrent operations can't race.
	var grantCopy *Grant
	onGrant := s.onGrant
	if onGrant != nil {
		grantCopy = g.clone()
	}
	s.mu.Unlock()

	if err := s.save(); err != nil {
		s.logger.Error("grants: failed to persist after extend", "peer", shortPeerID(peerID), "error", err)
	}

	s.logger.Info("grants: extended", "peer", shortPeerID(peerID), "new_expiry", newExpiry.Format(time.RFC3339))

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
	s.mu.Lock()
	var removed []peer.ID
	for pid, g := range s.grants {
		if g.Expired() {
			removed = append(removed, pid)
			delete(s.grants, pid)
		}
	}
	onRevoke := s.onRevoke
	s.mu.Unlock()

	if len(removed) > 0 {
		if err := s.save(); err != nil {
			s.logger.Error("grants: failed to persist after cleanup", "error", err)
		}
		for _, pid := range removed {
			s.logger.Info("grants: expired grant removed, closing connections", "peer", shortPeerID(pid))
			// B1 mitigation: close connections to peers whose grants expired.
			// This terminates any active transfers that outlived the grant.
			if onRevoke != nil {
				onRevoke(pid)
			}
		}
	}
}

// buildMacaroon creates a macaroon token with the standard caveat set.
// Used by both Grant() and Extend() to avoid duplication.
func (s *Store) buildMacaroon(peerID peer.ID, expiresAt time.Time, services []string, permanent bool) *macaroon.Macaroon {
	id := fmt.Sprintf("grant-%s-%d", shortPeerID(peerID), time.Now().UnixNano())
	m := macaroon.New("shurli-node", s.rootKey, id)
	m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatPeerID, peerID.String()))

	if !permanent {
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatExpires, expiresAt.Format(time.RFC3339)))
	}

	if len(services) > 0 {
		m.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatService, strings.Join(services, ",")))
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
