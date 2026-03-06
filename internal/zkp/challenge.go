package zkp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"log/slog"
	"sync"
	"time"
)

// DefaultChallengeTTL is how long a nonce is valid after issuance.
const DefaultChallengeTTL = 30 * time.Second

// MaxPendingChallenges is the maximum number of unconsumed challenges.
// Prevents memory exhaustion from unanswered challenge requests.
const MaxPendingChallenges = 1000

// Challenge represents a single-use nonce issued for ZKP authentication.
type Challenge struct {
	Nonce      uint64
	MerkleRoot []byte // Merkle root snapshot at issuance
	IssuedAt   time.Time
	ExpiresAt  time.Time
}

// ChallengeStore manages single-use challenge nonces for the ZKP auth protocol.
// Nonces are cryptographically random, expire after TTL, and can only be consumed once.
// Under memory pressure (>80% capacity), callers should increase per-peer rate limits
// for graceful degradation.
type ChallengeStore struct {
	mu         sync.Mutex
	challenges map[uint64]*Challenge
	ttl        time.Duration
	maxPending int
}

// UnderPressure returns true when the store is at >80% capacity.
// Callers should increase per-peer rate limits (e.g., 5s -> 30s) during pressure
// to slow the fill rate and give TTL expiry time to reclaim slots.
func (s *ChallengeStore) UnderPressure() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.challenges) > s.maxPending*4/5
}

// NewChallengeStore creates a store with the given nonce TTL.
func NewChallengeStore(ttl time.Duration) *ChallengeStore {
	return &ChallengeStore{
		challenges: make(map[uint64]*Challenge),
		ttl:        ttl,
		maxPending: MaxPendingChallenges,
	}
}

// Issue generates a fresh challenge nonce bound to the current Merkle root.
// Returns ErrTooManyChallenges if the store has reached its capacity.
func (s *ChallengeStore) Issue(merkleRoot []byte) (*Challenge, error) {
	nonce, err := randomNonce()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	c := &Challenge{
		Nonce:      nonce,
		MerkleRoot: merkleRoot,
		IssuedAt:   now,
		ExpiresAt:  now.Add(s.ttl),
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.challenges) >= s.maxPending {
		return nil, ErrTooManyChallenges
	}

	// Guard against nonce collision (astronomically unlikely with 64-bit random,
	// but correctness requires the check).
	if _, exists := s.challenges[nonce]; exists {
		return nil, ErrTooManyChallenges
	}

	s.challenges[nonce] = c
	return c, nil
}

// Consume validates and atomically removes a nonce. Returns the challenge
// if valid, or an error if the nonce is unknown, expired, or the Merkle root
// does not match the root at challenge issuance.
func (s *ChallengeStore) Consume(nonce uint64, merkleRoot []byte) (*Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	c, exists := s.challenges[nonce]
	if !exists {
		return nil, ErrNonceReused
	}

	// Delete unconditionally: single-use regardless of outcome.
	delete(s.challenges, nonce)

	if time.Now().After(c.ExpiresAt) {
		return nil, ErrProofExpired
	}

	// Verify that the Merkle root has not changed since challenge issuance.
	if !bytes.Equal(merkleRoot, c.MerkleRoot) {
		return nil, ErrRootMismatch
	}

	return c, nil
}

// Pending returns the number of unconsumed challenges.
func (s *ChallengeStore) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.challenges)
}

// CleanExpired removes all expired challenges. Returns the number removed.
func (s *ChallengeStore) CleanExpired() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	removed := 0
	for nonce, c := range s.challenges {
		if now.After(c.ExpiresAt) {
			delete(s.challenges, nonce)
			removed++
		}
	}
	return removed
}

// StartCleanup runs a periodic cleanup goroutine that removes expired
// challenges. Stops when the context is cancelled. Call this once at startup.
func (s *ChallengeStore) StartCleanup(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(s.ttl)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if removed := s.CleanExpired(); removed > 0 {
					slog.Debug("zkp: cleaned expired challenges", "removed", removed, "remaining", s.Pending())
				}
			}
		}
	}()
}

// randomNonce generates a cryptographically random uint64.
func randomNonce() (uint64, error) {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint64(buf[:]), nil
}
