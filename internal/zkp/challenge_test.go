package zkp

import (
	"context"
	"testing"
	"time"
)

func TestChallengeStore_IssueAndConsume(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	c, err := store.Issue(root)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if c.Nonce == 0 {
		t.Fatal("nonce should not be zero")
	}
	if store.Pending() != 1 {
		t.Fatalf("expected 1 pending, got %d", store.Pending())
	}

	// Consume should succeed.
	consumed, err := store.Consume(c.Nonce, root)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if consumed.Nonce != c.Nonce {
		t.Fatalf("wrong nonce: got %d, want %d", consumed.Nonce, c.Nonce)
	}
	if store.Pending() != 0 {
		t.Fatalf("expected 0 pending after consume, got %d", store.Pending())
	}
}

func TestChallengeStore_ReplayRejected(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	c, err := store.Issue(root)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// First consume succeeds.
	if _, err := store.Consume(c.Nonce, root); err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	// Second consume with same nonce must fail.
	if _, err := store.Consume(c.Nonce, root); err != ErrNonceReused {
		t.Fatalf("expected ErrNonceReused, got %v", err)
	}
}

func TestChallengeStore_UnknownNonce(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	if _, err := store.Consume(999999, root); err != ErrNonceReused {
		t.Fatalf("expected ErrNonceReused for unknown nonce, got %v", err)
	}
}

func TestChallengeStore_Expired(t *testing.T) {
	// 1ms TTL so it expires immediately.
	store := NewChallengeStore(1 * time.Millisecond)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	c, err := store.Issue(root)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	time.Sleep(5 * time.Millisecond)

	if _, err := store.Consume(c.Nonce, root); err != ErrProofExpired {
		t.Fatalf("expected ErrProofExpired, got %v", err)
	}

	// Expired nonce should be gone (single-use even on expiry).
	if store.Pending() != 0 {
		t.Fatalf("expired nonce should be removed, got %d pending", store.Pending())
	}
}

func TestChallengeStore_RootMismatch(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	rootA := []byte("fake-merkle-root-32-bytes-AAAA!!")
	rootB := []byte("fake-merkle-root-32-bytes-BBBB!!")

	c, err := store.Issue(rootA)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// Consuming with a different root must fail.
	if _, err := store.Consume(c.Nonce, rootB); err != ErrRootMismatch {
		t.Fatalf("expected ErrRootMismatch, got %v", err)
	}

	// Nonce is consumed (single-use) even on root mismatch.
	if store.Pending() != 0 {
		t.Fatalf("nonce should be deleted after root mismatch, got %d pending", store.Pending())
	}
}

func TestChallengeStore_MaxPending(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	// Fill to capacity.
	for i := 0; i < MaxPendingChallenges; i++ {
		if _, err := store.Issue(root); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}

	// Next issue should fail.
	if _, err := store.Issue(root); err != ErrTooManyChallenges {
		t.Fatalf("expected ErrTooManyChallenges, got %v", err)
	}
}

func TestChallengeStore_CleanExpired(t *testing.T) {
	store := NewChallengeStore(1 * time.Millisecond)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	for i := 0; i < 10; i++ {
		if _, err := store.Issue(root); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}
	if store.Pending() != 10 {
		t.Fatalf("expected 10 pending, got %d", store.Pending())
	}

	time.Sleep(5 * time.Millisecond)

	removed := store.CleanExpired()
	if removed != 10 {
		t.Fatalf("expected 10 removed, got %d", removed)
	}
	if store.Pending() != 0 {
		t.Fatalf("expected 0 pending after cleanup, got %d", store.Pending())
	}
}

func TestChallengeStore_StartCleanup(t *testing.T) {
	store := NewChallengeStore(1 * time.Millisecond)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store.StartCleanup(ctx)

	for i := 0; i < 5; i++ {
		if _, err := store.Issue(root); err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
	}

	// Wait for cleanup tick (TTL is 1ms, ticker fires every 1ms).
	time.Sleep(10 * time.Millisecond)

	if pending := store.Pending(); pending != 0 {
		t.Fatalf("expected 0 pending after cleanup goroutine, got %d", pending)
	}
}

func TestChallengeStore_MultipleNonces(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	// Issue 5 nonces, consume them in reverse order.
	challenges := make([]*Challenge, 5)
	for i := range challenges {
		c, err := store.Issue(root)
		if err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
		challenges[i] = c
	}
	if store.Pending() != 5 {
		t.Fatalf("expected 5 pending, got %d", store.Pending())
	}

	for i := len(challenges) - 1; i >= 0; i-- {
		if _, err := store.Consume(challenges[i].Nonce, root); err != nil {
			t.Fatalf("Consume %d: %v", i, err)
		}
	}
	if store.Pending() != 0 {
		t.Fatalf("expected 0 pending, got %d", store.Pending())
	}
}

func TestChallengeStore_NonceUniqueness(t *testing.T) {
	store := NewChallengeStore(30 * time.Second)
	root := []byte("fake-merkle-root-32-bytes-long!!")

	seen := make(map[uint64]bool)
	for i := 0; i < 100; i++ {
		c, err := store.Issue(root)
		if err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
		if seen[c.Nonce] {
			t.Fatalf("duplicate nonce at iteration %d: %d", i, c.Nonce)
		}
		seen[c.Nonce] = true
	}
}
