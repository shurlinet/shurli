// Package deposit implements an in-memory store for macaroon-backed invite deposits.
//
// An invite deposit is a "contact card" left by an admin. It contains a macaroon
// that grants specific permissions. The deposit can be consumed by a joining peer
// (async: the admin does not need to be online). Deposits support attenuation-only
// permission management: admins can add restrictions (caveats) or revoke before
// consumption, but can never widen permissions after creation (HMAC chain enforces this).
package deposit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/shurlinet/shurli/internal/macaroon"
)

// Status represents the lifecycle state of an invite deposit.
type Status string

const (
	StatusPending  Status = "pending"   // awaiting consumption
	StatusConsumed Status = "consumed"  // successfully used
	StatusRevoked  Status = "revoked"   // admin revoked before use
	StatusExpired  Status = "expired"   // TTL exceeded
)

// InviteDeposit is a macaroon-backed async invite.
type InviteDeposit struct {
	ID          string             `json:"id"`
	Macaroon    *macaroon.Macaroon `json:"macaroon"`
	CreatedBy   string             `json:"created_by"`    // admin peer ID (short)
	CreatedAt   time.Time          `json:"created_at"`
	ExpiresAt   time.Time          `json:"expires_at"`    // zero = never
	Status      Status             `json:"status"`
	ConsumedBy  string             `json:"consumed_by,omitempty"` // peer ID that used it
	ConsumedAt  time.Time          `json:"consumed_at,omitempty"`
}

// DepositStore manages invite deposits in memory.
type DepositStore struct {
	mu       sync.RWMutex
	deposits map[string]*InviteDeposit
}

// NewDepositStore creates an empty deposit store.
func NewDepositStore() *DepositStore {
	return &DepositStore{
		deposits: make(map[string]*InviteDeposit),
	}
}

// Create generates a new invite deposit with the given macaroon.
func (s *DepositStore) Create(m *macaroon.Macaroon, createdBy string, ttl time.Duration) (*InviteDeposit, error) {
	id, err := generateDepositID()
	if err != nil {
		return nil, fmt.Errorf("failed to generate deposit ID: %w", err)
	}

	deposit := &InviteDeposit{
		ID:        id,
		Macaroon:  m,
		CreatedBy: createdBy,
		CreatedAt: time.Now(),
		Status:    StatusPending,
	}
	if ttl > 0 {
		deposit.ExpiresAt = deposit.CreatedAt.Add(ttl)
	}

	s.mu.Lock()
	s.deposits[id] = deposit
	s.mu.Unlock()

	return deposit, nil
}

// Get returns a deposit by ID. Returns ErrDepositNotFound if not found.
// Uses write lock because auto-expiry may mutate status.
func (s *DepositStore) Get(id string) (*InviteDeposit, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deposits[id]
	if !ok {
		return nil, ErrDepositNotFound
	}

	// Check expiry
	if !d.ExpiresAt.IsZero() && time.Now().After(d.ExpiresAt) && d.Status == StatusPending {
		d.Status = StatusExpired
	}

	return d, nil
}

// Consume marks a deposit as used by the given peer. Returns the macaroon
// for verification, or an error if the deposit is not consumable.
func (s *DepositStore) Consume(id, peerID string) (*macaroon.Macaroon, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deposits[id]
	if !ok {
		return nil, ErrDepositNotFound
	}

	// Check expiry first
	if !d.ExpiresAt.IsZero() && time.Now().After(d.ExpiresAt) && d.Status == StatusPending {
		d.Status = StatusExpired
	}

	switch d.Status {
	case StatusConsumed:
		return nil, ErrDepositConsumed
	case StatusRevoked:
		return nil, ErrDepositRevoked
	case StatusExpired:
		return nil, ErrDepositExpired
	case StatusPending:
		// OK to consume
	default:
		return nil, fmt.Errorf("unexpected deposit status: %s", d.Status)
	}

	d.Status = StatusConsumed
	d.ConsumedBy = peerID
	d.ConsumedAt = time.Now()

	return d.Macaroon, nil
}

// Revoke marks a pending deposit as revoked. Already consumed deposits
// cannot be revoked (the peer is already authorized).
func (s *DepositStore) Revoke(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deposits[id]
	if !ok {
		return ErrDepositNotFound
	}

	if d.Status == StatusConsumed {
		return fmt.Errorf("cannot revoke: deposit already consumed by %s", d.ConsumedBy)
	}

	d.Status = StatusRevoked
	return nil
}

// AddCaveat adds a restriction (caveat) to a pending deposit's macaroon.
// Attenuation-only: permissions can only be narrowed, never widened.
// Returns an error if the deposit is not pending.
func (s *DepositStore) AddCaveat(id, caveat string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok := s.deposits[id]
	if !ok {
		return ErrDepositNotFound
	}

	if d.Status != StatusPending {
		return fmt.Errorf("cannot modify deposit in %s state", d.Status)
	}

	d.Macaroon.AddFirstPartyCaveat(caveat)
	return nil
}

// List returns all deposits, optionally filtered by status.
// Pass empty string to list all.
// Uses write lock because auto-expiry may mutate status.
func (s *DepositStore) List(filterStatus Status) []*InviteDeposit {
	s.mu.Lock()
	defer s.mu.Unlock()

	var result []*InviteDeposit
	now := time.Now()

	for _, d := range s.deposits {
		// Auto-expire pending deposits
		if d.Status == StatusPending && !d.ExpiresAt.IsZero() && now.After(d.ExpiresAt) {
			d.Status = StatusExpired
		}

		if filterStatus != "" && d.Status != filterStatus {
			continue
		}
		result = append(result, d)
	}
	return result
}

// CleanExpired removes expired and revoked deposits older than the cutoff.
func (s *DepositStore) CleanExpired(olderThan time.Duration) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-olderThan)
	removed := 0

	for id, d := range s.deposits {
		if d.Status == StatusExpired || d.Status == StatusRevoked {
			if d.CreatedAt.Before(cutoff) {
				delete(s.deposits, id)
				removed++
			}
		}
	}
	return removed
}

// Count returns the number of deposits by status.
func (s *DepositStore) Count(status Status) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	count := 0
	for _, d := range s.deposits {
		if d.Status == status {
			count++
		}
	}
	return count
}

func generateDepositID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
