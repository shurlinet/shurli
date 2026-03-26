// GrantPouch stores macaroon capability tokens received from other nodes.
// It is the holder's view of grants: keyed by issuing node's peer ID.
// The GrantStore is the issuer's view (keyed by grantee peer ID).
// These are separate types with separate persistence files.
package grants

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/macaroon"
)

// PouchEntry is a single received grant token from an issuing node.
type PouchEntry struct {
	IssuerID    peer.ID            `json:"-"`
	IssuerIDStr string             `json:"issuer_id"`
	Token       *macaroon.Macaroon `json:"token"`
	Services    []string           `json:"services,omitempty"` // empty = all services
	ExpiresAt   time.Time          `json:"expires_at"`
	ReceivedAt  time.Time          `json:"received_at"`
	Permanent   bool               `json:"permanent,omitempty"`
}

// Expired returns true if this entry has expired.
func (e *PouchEntry) Expired() bool {
	if e.Permanent {
		return false
	}
	return time.Now().After(e.ExpiresAt)
}

// clone returns a shallow copy of the entry (safe for external use).
func (e *PouchEntry) clone() *PouchEntry {
	cp := *e
	if e.Services != nil {
		cp.Services = make([]string, len(e.Services))
		copy(cp.Services, e.Services)
	}
	if e.Token != nil {
		cp.Token = e.Token.Clone()
	}
	return &cp
}

// pouchFile is the JSON structure written to disk.
type pouchFile struct {
	Version uint64       `json:"version"`
	Entries []PouchEntry `json:"entries"`
	HMAC    string       `json:"hmac,omitempty"`
}

// refreshRequester is the interface the Pouch uses to request token refreshes.
// Implemented by GrantProtocol.RequestRefresh. Decoupled to avoid import cycle.
type refreshRequester interface {
	RequestRefresh(ctx context.Context, issuerID peer.ID, tokenID string) (*GrantRefreshResponse, error)
}

// Pouch stores received grant tokens keyed by issuing node's peer ID.
// Thread-safe. Persists to disk with HMAC integrity.
type Pouch struct {
	mu             sync.RWMutex
	entries        map[peer.ID]*PouchEntry
	hmacKey        []byte
	persistPath    string
	version        uint64
	stopCleanup    chan struct{}
	cleanupDone    chan struct{}
	cleanupStarted bool // true after StartCleanup is called
	refresher      refreshRequester // set via SetRefresher; nil = no refresh support
	logger         *slog.Logger
}

// NewPouch creates a new grant pouch.
// hmacKey is used for grant_pouch.json file integrity.
func NewPouch(hmacKey []byte) *Pouch {
	return &Pouch{
		entries:     make(map[peer.ID]*PouchEntry),
		hmacKey:     hmacKey,
		stopCleanup: make(chan struct{}),
		cleanupDone: make(chan struct{}),
		logger:      slog.Default(),
	}
}

// StartCleanup starts a background goroutine that removes expired entries
// every interval, persists the result, and checks for tokens needing refresh.
func (p *Pouch) StartCleanup(interval time.Duration) {
	p.cleanupStarted = true
	go func() {
		defer close(p.cleanupDone)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				p.CleanExpired()
				p.checkRefreshNeeded()
			case <-p.stopCleanup:
				return
			}
		}
	}()
}

// Stop stops the cleanup goroutine and waits for it to exit.
// Safe to call even if StartCleanup was never called.
func (p *Pouch) Stop() {
	if !p.cleanupStarted {
		return
	}
	close(p.stopCleanup)
	<-p.cleanupDone
}

// SetPersistPath sets the file path for auto-saving the pouch.
func (p *Pouch) SetPersistPath(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.persistPath = path
}

// SetRefresher sets the protocol handler used to request token refreshes.
// Must be called before StartCleanup to enable background refresh.
func (p *Pouch) SetRefresher(r refreshRequester) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refresher = r
}

// Add stores a received grant token from an issuing node.
// If a token from this issuer already exists, it is replaced.
func (p *Pouch) Add(issuerID peer.ID, token *macaroon.Macaroon, services []string, expiresAt time.Time, permanent bool) {
	var svcCopy []string
	if len(services) > 0 {
		svcCopy = make([]string, len(services))
		copy(svcCopy, services)
	}

	entry := &PouchEntry{
		IssuerID:    issuerID,
		IssuerIDStr: issuerID.String(),
		Token:       token.Clone(), // clone to prevent caller from mutating internal state
		Services:    svcCopy,
		ExpiresAt:   expiresAt,
		ReceivedAt:  time.Now(),
		Permanent:   permanent,
	}

	p.mu.Lock()
	p.entries[issuerID] = entry
	p.mu.Unlock()

	if err := p.save(); err != nil {
		p.logger.Error("pouch: failed to persist after add", "issuer", shortPeerID(issuerID), "error", err)
	}

	p.logger.Info("pouch: stored grant", "issuer", shortPeerID(issuerID), "services", services, "permanent", permanent)
}

// Get retrieves the best matching token for a given issuer and service.
// Returns nil if no valid token exists.
func (p *Pouch) Get(issuerID peer.ID, service string) *macaroon.Macaroon {
	p.mu.RLock()
	entry, exists := p.entries[issuerID]
	if !exists || entry.Expired() {
		p.mu.RUnlock()
		return nil
	}

	// Check service restriction.
	if len(entry.Services) > 0 {
		found := false
		for _, s := range entry.Services {
			if s == service {
				found = true
				break
			}
		}
		if !found {
			p.mu.RUnlock()
			return nil
		}
	}

	token := entry.Token.Clone()
	p.mu.RUnlock()
	return token
}

// Remove removes tokens from a specific issuer (used on revocation notice).
func (p *Pouch) Remove(issuerID peer.ID) bool {
	p.mu.Lock()
	_, exists := p.entries[issuerID]
	if !exists {
		p.mu.Unlock()
		return false
	}
	delete(p.entries, issuerID)
	p.mu.Unlock()

	if err := p.save(); err != nil {
		p.logger.Error("pouch: failed to persist after remove", "issuer", shortPeerID(issuerID), "error", err)
	}

	p.logger.Info("pouch: removed grant", "issuer", shortPeerID(issuerID))
	return true
}

// List returns copies of all non-expired entries. Safe for external use.
func (p *Pouch) List() []*PouchEntry {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var result []*PouchEntry
	for _, e := range p.entries {
		if !e.Expired() {
			result = append(result, e.clone())
		}
	}
	return result
}

// Delegate creates an attenuated sub-token from an existing pouch entry.
// The sub-token has all of the original caveats PLUS new restrictions:
// - delegate_to=targetPeerID (binds the sub-token to the target)
// - max_delegations decremented (or set to the requested value if lower)
// - optional: shorter duration, fewer services
// Returns the attenuated macaroon for delivery to the target peer.
func (p *Pouch) Delegate(issuerID peer.ID, targetPeerID peer.ID, duration time.Duration, services []string, maxDelegations int) (*macaroon.Macaroon, error) {
	p.mu.RLock()
	entry, exists := p.entries[issuerID]
	if !exists || entry.Expired() {
		p.mu.RUnlock()
		return nil, fmt.Errorf("no valid token from issuer %s", shortPeerID(issuerID))
	}

	// Check the original token allows delegation via the most restrictive caveat.
	origMaxDel := extractMaxDelegations(entry.Token.Caveats)
	if origMaxDel == 0 {
		p.mu.RUnlock()
		return nil, fmt.Errorf("token from issuer %s does not allow delegation (max_delegations=0)", shortPeerID(issuerID))
	}

	// Also verify the legacy delegate caveat isn't blocking.
	for _, c := range entry.Token.Caveats {
		key, value, err := macaroon.ParseCaveat(c)
		if err != nil {
			continue
		}
		if key == macaroon.CaveatDelegate && value == "false" {
			p.mu.RUnlock()
			return nil, fmt.Errorf("token from issuer %s has delegate=false", shortPeerID(issuerID))
		}
	}

	// Clone the token so we don't mutate the stored one.
	subToken := entry.Token.Clone()
	p.mu.RUnlock()

	// Add delegate_to caveat binding this sub-token to the target peer.
	subToken.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatDelegateTo, targetPeerID.String()))

	// Compute new max_delegations: must be strictly less than original.
	newMaxDel := maxDelegations
	if origMaxDel > 0 {
		// Limited delegation: decrement by 1, or use requested value if lower.
		decremented := origMaxDel - 1
		if newMaxDel < 0 {
			// Caller requested unlimited, but original is limited. Cap at decremented.
			newMaxDel = decremented
		} else if newMaxDel > decremented {
			newMaxDel = decremented
		}
	}
	// origMaxDel == -1 (unlimited): use whatever the caller requested.
	subToken.AddFirstPartyCaveat(fmt.Sprintf("%s=%d", macaroon.CaveatMaxDelegations, newMaxDel))

	// Optional: add tighter duration (attenuate, never widen).
	if duration > 0 {
		subToken.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatExpires, time.Now().Add(duration).Format(time.RFC3339)))
	}

	// Optional: add tighter service restriction.
	if len(services) > 0 {
		subToken.AddFirstPartyCaveat(fmt.Sprintf("%s=%s", macaroon.CaveatService, strings.Join(services, ",")))
	}

	p.logger.Info("pouch: delegated token",
		"issuer", shortPeerID(issuerID),
		"target", shortPeerID(targetPeerID),
		"max_delegations", newMaxDel)

	return subToken, nil
}

// extractMaxDelegations returns the most restrictive max_delegations from a token's caveats.
// In a delegation chain, multiple max_delegations caveats accumulate. The minimum
// non-negative value wins. If any is 0, delegation is blocked. -1 (unlimited) only
// applies when ALL max_delegations caveats are -1.
// Returns 0 if not found (default: no delegation).
func extractMaxDelegations(caveats []string) int {
	found := false
	result := -1 // start with unlimited, narrow down
	for _, c := range caveats {
		key, value, err := macaroon.ParseCaveat(c)
		if err != nil {
			continue
		}
		if key != macaroon.CaveatMaxDelegations {
			continue
		}
		v, err := strconv.Atoi(value)
		if err != nil {
			return 0 // malformed = deny
		}
		found = true
		if v == 0 {
			return 0 // any zero blocks all delegation
		}
		if v == -1 {
			continue // unlimited doesn't narrow the result
		}
		// v > 0: take the minimum
		if result == -1 || v < result {
			result = v
		}
	}
	if !found {
		return 0 // no caveat = no delegation
	}
	return result
}

// CleanExpired removes expired entries from the pouch.
func (p *Pouch) CleanExpired() int {
	p.mu.Lock()
	var removed int
	for id, e := range p.entries {
		if e.Expired() {
			delete(p.entries, id)
			removed++
			p.logger.Info("pouch: expired grant removed", "issuer", shortPeerID(id))
		}
	}
	p.mu.Unlock()

	if removed > 0 {
		if err := p.save(); err != nil {
			p.logger.Error("pouch: failed to persist after cleanup", "error", err)
		}
	}
	return removed
}

// hasAutoRefreshCaveat checks if a token's caveats include auto_refresh=true.
func hasAutoRefreshCaveat(caveats []string) bool {
	for _, c := range caveats {
		key, value, err := macaroon.ParseCaveat(c)
		if err != nil {
			continue
		}
		if key == macaroon.CaveatAutoRefresh && value == "true" {
			return true
		}
	}
	return false
}

// checkRefreshNeeded finds tokens approaching expiry (last 10% of duration)
// and sends refresh requests to the issuing node.
func (p *Pouch) checkRefreshNeeded() {
	p.mu.RLock()
	refresher := p.refresher
	if refresher == nil {
		p.mu.RUnlock()
		return
	}

	now := time.Now()
	type refreshTarget struct {
		issuerID peer.ID
		tokenID  string
	}
	var targets []refreshTarget

	for _, e := range p.entries {
		if e.Permanent || e.Expired() {
			continue
		}
		// Only attempt refresh on tokens that have auto_refresh=true caveat.
		if !hasAutoRefreshCaveat(e.Token.Caveats) {
			continue
		}
		// Check if in the last 10% of duration.
		total := e.ExpiresAt.Sub(e.ReceivedAt)
		if total <= 0 {
			continue
		}
		remaining := e.ExpiresAt.Sub(now)
		threshold := total / 10
		if remaining <= threshold {
			targets = append(targets, refreshTarget{
				issuerID: e.IssuerID,
				tokenID:  e.Token.ID,
			})
		}
	}
	p.mu.RUnlock()

	for _, t := range targets {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		resp, err := refresher.RequestRefresh(ctx, t.issuerID, t.tokenID)
		cancel()
		if err != nil {
			p.logger.Warn("pouch: refresh request failed", "issuer", shortPeerID(t.issuerID), "error", err)
			continue
		}
		if resp.Status != "refreshed" || resp.Token == "" {
			p.logger.Info("pouch: refresh rejected", "issuer", shortPeerID(t.issuerID), "reason", resp.Reason)
			continue
		}

		// Decode and store the refreshed token.
		token, err := macaroon.DecodeBase64(resp.Token)
		if err != nil {
			p.logger.Warn("pouch: invalid refreshed token", "issuer", shortPeerID(t.issuerID), "error", err)
			continue
		}

		// Extract new expiry from token caveats.
		newExpiry := macaroon.ExtractEarliestExpires(token.Caveats)

		p.mu.Lock()
		if entry, exists := p.entries[t.issuerID]; exists {
			entry.Token = token
			entry.ExpiresAt = newExpiry
			entry.ReceivedAt = time.Now() // fresh timestamp per refresh, not stale loop-start time
		}
		p.mu.Unlock()

		if err := p.save(); err != nil {
			p.logger.Error("pouch: failed to persist after refresh", "issuer", shortPeerID(t.issuerID), "error", err)
		}

		p.logger.Info("pouch: token refreshed", "issuer", shortPeerID(t.issuerID), "new_expiry", newExpiry.Format(time.RFC3339))
	}
}

// save persists the pouch to disk with HMAC integrity.
func (p *Pouch) save() error {
	p.mu.Lock()
	path := p.persistPath
	if path == "" {
		p.mu.Unlock()
		return nil
	}

	p.version++
	version := p.version

	var entries []PouchEntry
	for _, e := range p.entries {
		if !e.Expired() {
			entries = append(entries, *e)
		}
	}
	hmacKey := p.hmacKey
	p.mu.Unlock()

	pf := pouchFile{
		Version: version,
		Entries: entries,
	}

	if len(hmacKey) > 0 {
		entriesJSON, _ := json.Marshal(entries)
		pf.HMAC = computeFileHMAC(hmacKey, version, entriesJSON)
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal pouch: %w", err)
	}

	// A2 mitigation: reject symlinks.
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("pouch file is a symlink, refusing to write")
		}
	}

	return atomicWriteFile(path, data, 0600)
}

// LoadPouch reads the pouch from disk and verifies HMAC integrity.
func LoadPouch(path string, hmacKey []byte) (*Pouch, error) {
	p := NewPouch(hmacKey)
	p.SetPersistPath(path)

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read pouch file: %w", err)
	}

	var file pouchFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse pouch file: %w", err)
	}

	// Verify HMAC integrity.
	entriesJSON, _ := json.Marshal(file.Entries)
	if err := verifyFileHMAC(hmacKey, file.HMAC, file.Version, entriesJSON, len(file.Entries) > 0); err != nil {
		return nil, fmt.Errorf("pouch file: %w", err)
	}

	p.version = file.Version

	for _, e := range file.Entries {
		if e.Expired() {
			p.logger.Info("pouch: skipping expired entry on load", "issuer", truncateStr(e.IssuerIDStr, 16))
			continue
		}

		pid, err := peer.Decode(e.IssuerIDStr)
		if err != nil {
			p.logger.Warn("pouch: skipping entry with invalid issuer ID", "issuer_id", truncateStr(e.IssuerIDStr, 16), "error", err)
			continue
		}
		e.IssuerID = pid
		p.entries[pid] = &e
	}

	p.logger.Info("pouch: loaded", "count", len(p.entries), "version", p.version)
	return p, nil
}
