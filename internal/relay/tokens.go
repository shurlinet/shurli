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
	TokenHash [32]byte  // SHA-256 of raw token
	PeerID    peer.ID   // filled after use
	Name      string    // peer's friendly name
	UsedAt    time.Time // zero = unused
	Attempts  int       // failed attempts (max 3)
}

// PairingGroup holds a set of related pairing codes.
type PairingGroup struct {
	ID        string
	Namespace string
	CreatedAt time.Time
	ExpiresAt time.Time
	PeerTTL   time.Duration // authorization expiry for joined peers (0 = never)
	mu        sync.Mutex
	codes     []CodeSlot
}

// PeerInfo is a joined peer's identity and name.
type PeerInfo struct {
	PeerID peer.ID
	Name   string
}

// GroupInfo is a read-only snapshot of a pairing group for listing.
type GroupInfo struct {
	ID        string
	Namespace string
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

// CreateGroup generates a pairing group with count codes.
// Returns the raw tokens (caller encodes into invite codes) and the group ID.
func (ts *TokenStore) CreateGroup(count int, ttl time.Duration, ns string, peerTTL time.Duration) (tokens [][]byte, groupID string, err error) {
	if count < 1 {
		return nil, "", fmt.Errorf("count must be at least 1")
	}

	// Generate group ID (8-char hex)
	idBytes := make([]byte, 4)
	if _, err := rand.Read(idBytes); err != nil {
		return nil, "", fmt.Errorf("failed to generate group ID: %w", err)
	}
	groupID = fmt.Sprintf("%x", idBytes)

	now := time.Now()
	group := &PairingGroup{
		ID:        groupID,
		Namespace: ns,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
		PeerTTL:   peerTTL,
		codes:     make([]CodeSlot, count),
	}

	tokens = make([][]byte, count)
	for i := 0; i < count; i++ {
		token := make([]byte, TokenSize)
		if _, err := rand.Read(token); err != nil {
			return nil, "", fmt.Errorf("failed to generate token: %w", err)
		}
		tokens[i] = token
		group.codes[i] = CodeSlot{
			TokenHash: sha256.Sum256(token),
		}
	}

	ts.mu.Lock()
	ts.groups[groupID] = group
	ts.mu.Unlock()

	return tokens, groupID, nil
}

// ValidateAndUse atomically validates a token and marks it as used.
// Returns the group and the slot index on success.
func (ts *TokenStore) ValidateAndUse(token []byte, peerID peer.ID, name string) (*PairingGroup, int, error) {
	if len(token) != TokenSize {
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

// RecordFailedAttempt increments the attempt counter for a token.
// Used when authentication succeeds (token found) but downstream steps fail.
func (ts *TokenStore) RecordFailedAttempt(token []byte) {
	if len(token) != TokenSize {
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
			PeerID: slot.PeerID,
			Name:   slot.Name,
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
