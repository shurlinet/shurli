package relay

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

// Token size for relay pairing codes (128-bit).
const TokenSize = 16

// Maximum failed attempts per code before it is burned.
const maxAttempts = 3

var (
	ErrTokenNotFound = errors.New("pairing failed")
	ErrTokenUsed     = errors.New("pairing failed")
	ErrTokenBurned   = errors.New("pairing failed")
	ErrTokenExpired  = errors.New("pairing failed")
	ErrGroupNotFound = errors.New("group not found")
	ErrGroupExpired  = errors.New("group expired")
)

// CodeSlot represents a single pairing code within a group.
type CodeSlot struct {
	TokenHash  [32]byte  // SHA-256 of raw token
	RawToken   []byte    // kept for PAKE session key derivation; zeroized after use
	DepositID  string    // linked invite deposit ID (for macaroon delivery)
	PeerID     peer.ID   // filled after use
	Name       string    // peer's friendly name
	UsedAt     time.Time // zero = unused
	InProgress bool      // true while PAKE handshake is in flight (prevents TOCTOU)
	Attempts   int       // failed attempts (max 3)
	HMACProof  []byte    // HMAC-SHA256(token, groupID) computed at pairing time
}

// PairingGroup holds a set of related pairing codes.
type PairingGroup struct {
	ID        string
	Namespace string
	CreatedBy peer.ID       // peer that created this group (empty for local admin)
	CreatedAt time.Time
	ExpiresAt time.Time
	PeerTTL   time.Duration // authorization expiry for joined peers (0 = never)
	mu        sync.Mutex
	codes     []CodeSlot
}

// PeerInfo is a joined peer's identity and name.
type PeerInfo struct {
	PeerID    peer.ID
	Name      string
	HMACProof []byte // HMAC-SHA256(token, groupID) - proves token possession
}

// GroupInfo is a read-only snapshot of a pairing group for listing.
type GroupInfo struct {
	ID        string
	Namespace string
	CreatedBy peer.ID
	CreatedAt time.Time
	ExpiresAt time.Time
	Total     int
	Used      int
	Peers     []PeerInfo
}

// TokenStore manages in-memory pairing tokens for the relay.
// All tokens are lost on relay restart (by design).
type TokenStore struct {
	mu     sync.RWMutex
	groups map[string]*PairingGroup
}

// NewTokenStore creates an empty token store.
func NewTokenStore() *TokenStore {
	return &TokenStore{
		groups: make(map[string]*PairingGroup),
	}
}

// CreateGroup generates a pairing group with count codes using the default 16-byte tokens.
// Returns the raw tokens (caller encodes into invite codes) and the group ID.
func (ts *TokenStore) CreateGroup(count int, ttl time.Duration, ns string, peerTTL time.Duration, createdBy peer.ID) (tokens [][]byte, groupID string, err error) {
	return ts.CreateGroupWithTokenSize(count, ttl, ns, peerTTL, TokenSize, createdBy, 0)
}

// CreateGroupShort generates a pairing group with 10-byte tokens for short invite codes.
func (ts *TokenStore) CreateGroupShort(count int, ttl time.Duration, ns string, peerTTL time.Duration, createdBy peer.ID) (tokens [][]byte, groupID string, err error) {
	return ts.CreateGroupWithTokenSize(count, ttl, ns, peerTTL, 10, createdBy, 0)
}

// ErrQuotaExceeded is returned when a peer exceeds their group creation quota.
var ErrQuotaExceeded = fmt.Errorf("group creation quota exceeded")

// CreateGroupWithTokenSize generates a pairing group with configurable token size.
// If maxGroupsPerPeer > 0 and createdBy is non-empty, atomically checks the
// per-peer quota under the write lock to prevent TOCTOU race conditions.
func (ts *TokenStore) CreateGroupWithTokenSize(count int, ttl time.Duration, ns string, peerTTL time.Duration, tokenSize int, createdBy peer.ID, maxGroupsPerPeer int) (tokens [][]byte, groupID string, err error) {
	if count < 1 {
		return nil, "", fmt.Errorf("count must be at least 1")
	}
	if tokenSize < 8 || tokenSize > 32 {
		return nil, "", fmt.Errorf("token size must be 8-32 bytes, got %d", tokenSize)
	}

	// Generate group ID (16-char hex, 64-bit entropy).
	// Collision check under write lock prevents silent overwrites.
	idBytes := make([]byte, 8)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate group ID: %w", err)
	}
	groupID = fmt.Sprintf("%x", idBytes)

	now := time.Now()
	group := &PairingGroup{
		ID:        groupID,
		Namespace: ns,
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		PeerTTL:   peerTTL,
		codes:     make([]CodeSlot, count),
	}

	tokens = make([][]byte, count)
	for i := 0; i < count; i++ {
		token := make([]byte, tokenSize)
		if _, err := rand.Read(token); err != nil {
			return nil, "", fmt.Errorf("failed to generate token: %w", err)
		}
		tokens[i] = token
		group.codes[i] = CodeSlot{
			TokenHash: sha256.Sum256(token),
			RawToken:  append([]byte(nil), token...), // separate copy for PAKE
		}
	}

	ts.mu.Lock()
	// Atomic per-peer quota check under write lock (prevents TOCTOU race).
	if maxGroupsPerPeer > 0 && createdBy != "" {
		peerCount := 0
		for _, g := range ts.groups {
			if g.CreatedBy == createdBy && now.Before(g.ExpiresAt) {
				peerCount++
			}
		}
		if peerCount >= maxGroupsPerPeer {
			ts.mu.Unlock()
			return nil, "", ErrQuotaExceeded
		}
	}
	// Collision check (extremely unlikely with 64-bit IDs, but fail-safe).
	if _, exists := ts.groups[groupID]; exists {
		ts.mu.Unlock()
		return nil, "", fmt.Errorf("group ID collision (retry)")
	}
	ts.groups[groupID] = group
	ts.mu.Unlock()

	return tokens, groupID, nil
}

// ValidateAndUse atomically validates a token and marks it as used.
// Returns the group and the slot index on success.
func (ts *TokenStore) ValidateAndUse(token []byte, peerID peer.ID, name string) (*PairingGroup, int, error) {
	if len(token) < 8 || len(token) > 32 {
		return nil, -1, ErrTokenNotFound
	}

	hash := sha256.Sum256(token)

	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, group := range ts.groups {
		group.mu.Lock()

		if time.Now().After(group.ExpiresAt) {
			group.mu.Unlock()
			continue
		}

		for i := range group.codes {
			slot := &group.codes[i]

			if subtle.ConstantTimeCompare(slot.TokenHash[:], hash[:]) != 1 {
				continue
			}

			// Found the matching slot.
			if slot.Attempts >= maxAttempts {
				group.mu.Unlock()
				return nil, -1, ErrTokenBurned
			}

			if !slot.UsedAt.IsZero() {
				group.mu.Unlock()
				return nil, -1, ErrTokenUsed
			}

			slot.PeerID = peerID
			slot.Name = name
			slot.UsedAt = time.Now()
			group.mu.Unlock()
			return group, i, nil
		}

		group.mu.Unlock()
	}

	return nil, -1, ErrTokenNotFound
}

// SetDepositID links a deposit to all code slots in a group.
func (ts *TokenStore) SetDepositID(groupID, depositID string) {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	for i := range group.codes {
		group.codes[i].DepositID = depositID
	}
}

// ErrTokenInProgress is returned when a PAKE handshake is already in flight for this token.
var ErrTokenInProgress = errors.New("pairing failed")

// ValidateForPAKE looks up a slot by token hash and atomically marks it as
// in-progress to prevent TOCTOU races. Returns the group, slot index, and
// raw token for PAKE session key derivation.
// The slot is NOT consumed; call MarkUsed after PAKE succeeds, or
// ClearInProgress if PAKE fails.
func (ts *TokenStore) ValidateForPAKE(tokenHash [32]byte) (*PairingGroup, int, []byte, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, group := range ts.groups {
		group.mu.Lock()

		if time.Now().After(group.ExpiresAt) {
			group.mu.Unlock()
			continue
		}

		for i := range group.codes {
			slot := &group.codes[i]

			if subtle.ConstantTimeCompare(slot.TokenHash[:], tokenHash[:]) != 1 {
				continue
			}

			if slot.Attempts >= maxAttempts {
				group.mu.Unlock()
				return nil, -1, nil, ErrTokenBurned
			}

			if !slot.UsedAt.IsZero() {
				group.mu.Unlock()
				return nil, -1, nil, ErrTokenUsed
			}

			if slot.InProgress {
				group.mu.Unlock()
				return nil, -1, nil, ErrTokenInProgress
			}

			if len(slot.RawToken) == 0 {
				group.mu.Unlock()
				return nil, -1, nil, fmt.Errorf("raw token not available")
			}

			// Atomically claim the slot for this PAKE handshake.
			slot.InProgress = true
			rawToken := make([]byte, len(slot.RawToken))
			copy(rawToken, slot.RawToken)
			group.mu.Unlock()
			return group, i, rawToken, nil
		}

		group.mu.Unlock()
	}

	return nil, -1, nil, ErrTokenNotFound
}

// ClearInProgress releases the in-progress flag on a slot after a failed PAKE.
func (ts *TokenStore) ClearInProgress(groupID string, idx int) {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if idx >= 0 && idx < len(group.codes) {
		group.codes[idx].InProgress = false
	}
}

// MarkUsed marks a code slot as consumed after successful PAKE.
// Zeroizes the raw token after marking.
func (ts *TokenStore) MarkUsed(groupID string, idx int, peerID peer.ID, name string) error {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return ErrGroupNotFound
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	if idx < 0 || idx >= len(group.codes) {
		return fmt.Errorf("invalid slot index: %d", idx)
	}

	slot := &group.codes[idx]
	if !slot.UsedAt.IsZero() {
		return ErrTokenUsed
	}

	slot.PeerID = peerID
	slot.Name = name
	slot.UsedAt = time.Now()
	slot.InProgress = false

	// Zeroize raw token: no longer needed after PAKE.
	for j := range slot.RawToken {
		slot.RawToken[j] = 0
	}
	slot.RawToken = nil

	return nil
}

// RecordFailedAttemptByHash increments the attempt counter for a token by its hash.
func (ts *TokenStore) RecordFailedAttemptByHash(tokenHash [32]byte) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, group := range ts.groups {
		group.mu.Lock()
		for i := range group.codes {
			if subtle.ConstantTimeCompare(group.codes[i].TokenHash[:], tokenHash[:]) == 1 {
				group.codes[i].Attempts++
				group.mu.Unlock()
				return
			}
		}
		group.mu.Unlock()
	}
}

// GetDepositID returns the deposit ID for a code slot.
func (ts *TokenStore) GetDepositID(groupID string, idx int) string {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return ""
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if idx < 0 || idx >= len(group.codes) {
		return ""
	}
	return group.codes[idx].DepositID
}

// RecordFailedAttempt increments the attempt counter for a token.
// Used when authentication succeeds (token found) but downstream steps fail.
func (ts *TokenStore) RecordFailedAttempt(token []byte) {
	if len(token) < 8 || len(token) > 32 {
		return
	}

	hash := sha256.Sum256(token)

	ts.mu.RLock()
	defer ts.mu.RUnlock()

	for _, group := range ts.groups {
		group.mu.Lock()
		for i := range group.codes {
			if subtle.ConstantTimeCompare(group.codes[i].TokenHash[:], hash[:]) == 1 {
				group.codes[i].Attempts++
				group.mu.Unlock()
				return
			}
		}
		group.mu.Unlock()
	}
}

// SetHMACProof stores the HMAC commitment proof for a code slot.
func (ts *TokenStore) SetHMACProof(groupID string, slotIdx int, proof []byte) {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	if slotIdx >= 0 && slotIdx < len(group.codes) {
		group.codes[slotIdx].HMACProof = proof
	}
}

// GetGroupPeers returns all joined peers in the group except the one at excludeIdx.
func (ts *TokenStore) GetGroupPeers(groupID string, excludeIdx int) []PeerInfo {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()

	if !ok {
		return nil
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	var peers []PeerInfo
	for i, slot := range group.codes {
		if i == excludeIdx || slot.UsedAt.IsZero() {
			continue
		}
		peers = append(peers, PeerInfo{
			PeerID:    slot.PeerID,
			Name:      slot.Name,
			HMACProof: slot.HMACProof,
		})
	}
	return peers
}

// IsGroupComplete returns true if all codes in the group have been used.
func (ts *TokenStore) IsGroupComplete(groupID string) bool {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()

	if !ok {
		return false
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	for _, slot := range group.codes {
		if slot.UsedAt.IsZero() {
			return false
		}
	}
	return true
}

// GroupCount returns the total number of codes in a group.
func (ts *TokenStore) GroupCount(groupID string) int {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()

	if !ok {
		return 0
	}

	group.mu.Lock()
	defer group.mu.Unlock()

	return len(group.codes)
}

// CleanExpired removes all expired groups and returns how many were removed.
func (ts *TokenStore) CleanExpired() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	now := time.Now()
	removed := 0
	for id, group := range ts.groups {
		if now.After(group.ExpiresAt) {
			delete(ts.groups, id)
			removed++
		}
	}
	return removed
}

// List returns a snapshot of all groups (active and expired).
func (ts *TokenStore) List() []GroupInfo {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	infos := make([]GroupInfo, 0, len(ts.groups))
	for _, group := range ts.groups {
		group.mu.Lock()
		info := GroupInfo{
			ID:        group.ID,
			Namespace: group.Namespace,
			CreatedBy: group.CreatedBy,
			CreatedAt: group.CreatedAt,
			ExpiresAt: group.ExpiresAt,
			Total:     len(group.codes),
		}
		for _, slot := range group.codes {
			if !slot.UsedAt.IsZero() {
				info.Used++
				info.Peers = append(info.Peers, PeerInfo{
					PeerID: slot.PeerID,
					Name:   slot.Name,
				})
			}
		}
		group.mu.Unlock()
		infos = append(infos, info)
	}
	return infos
}

// ActiveGroupCount returns the number of non-expired groups.
func (ts *TokenStore) ActiveGroupCount() int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now()
	count := 0
	for _, group := range ts.groups {
		if now.Before(group.ExpiresAt) {
			count++
		}
	}
	return count
}

// AllGroupsUsed returns true if every code in every active group has been used.
// Used to auto-disable enrollment mode when all invites are consumed.
func (ts *TokenStore) AllGroupsUsed() bool {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now()
	activeCount := 0
	for _, group := range ts.groups {
		if now.After(group.ExpiresAt) {
			continue
		}
		activeCount++
		group.mu.Lock()
		for _, slot := range group.codes {
			if slot.UsedAt.IsZero() {
				group.mu.Unlock()
				return false
			}
		}
		group.mu.Unlock()
	}
	return activeCount > 0
}

// GroupCreator returns the peer that created a group (empty for local admin).
func (ts *TokenStore) GroupCreator(groupID string) peer.ID {
	ts.mu.RLock()
	group, ok := ts.groups[groupID]
	ts.mu.RUnlock()
	if !ok {
		return ""
	}
	return group.CreatedBy
}

// ActiveGroupCountByPeer returns the number of non-expired groups created by a specific peer.
func (ts *TokenStore) ActiveGroupCountByPeer(peerID peer.ID) int {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	now := time.Now()
	count := 0
	for _, group := range ts.groups {
		if group.CreatedBy == peerID && now.Before(group.ExpiresAt) {
			count++
		}
	}
	return count
}

// Revoke removes a pairing group by ID.
func (ts *TokenStore) Revoke(groupID string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	if _, ok := ts.groups[groupID]; !ok {
		return ErrGroupNotFound
	}
	delete(ts.groups, groupID)
	return nil
}
