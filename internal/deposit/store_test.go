package deposit

import (
	"errors"
	"testing"
	"time"

	"github.com/shurlinet/shurli/internal/macaroon"
)

func testMacaroon() *macaroon.Macaroon {
	return macaroon.New("test-relay", []byte("root-key-secret-32-bytes-long!!"), "invite-1")
}

func TestCreateAndGet(t *testing.T) {
	s := NewDepositStore()
	m := testMacaroon()

	d, err := s.Create(m, "admin-peer-12D3...", 0)
	if err != nil {
		t.Fatal(err)
	}
	if d.ID == "" {
		t.Error("deposit ID should not be empty")
	}
	if d.Status != StatusPending {
		t.Errorf("status = %q, want %q", d.Status, StatusPending)
	}

	got, err := s.Get(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != d.ID {
		t.Errorf("got ID %q, want %q", got.ID, d.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	s := NewDepositStore()
	_, err := s.Get("nonexistent")
	if !errors.Is(err, ErrDepositNotFound) {
		t.Errorf("expected ErrDepositNotFound, got: %v", err)
	}
}

func TestConsume(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)

	m, err := s.Consume(d.ID, "joining-peer-12D3...")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Error("macaroon should not be nil")
	}

	// Verify status updated
	got, _ := s.Get(d.ID)
	if got.Status != StatusConsumed {
		t.Errorf("status = %q, want %q", got.Status, StatusConsumed)
	}
	if got.ConsumedBy != "joining-peer-12D3..." {
		t.Errorf("consumed_by = %q, want %q", got.ConsumedBy, "joining-peer-12D3...")
	}
}

func TestConsumeAlreadyConsumed(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)
	s.Consume(d.ID, "peer-1")

	_, err := s.Consume(d.ID, "peer-2")
	if !errors.Is(err, ErrDepositConsumed) {
		t.Errorf("expected ErrDepositConsumed, got: %v", err)
	}
}

func TestRevoke(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)

	if err := s.Revoke(d.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get(d.ID)
	if got.Status != StatusRevoked {
		t.Errorf("status = %q, want %q", got.Status, StatusRevoked)
	}

	// Consuming a revoked deposit should fail
	_, err := s.Consume(d.ID, "peer")
	if !errors.Is(err, ErrDepositRevoked) {
		t.Errorf("expected ErrDepositRevoked, got: %v", err)
	}
}

func TestRevokeConsumedFails(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)
	s.Consume(d.ID, "peer")

	err := s.Revoke(d.ID)
	if err == nil {
		t.Error("revoking consumed deposit should fail")
	}
}

func TestExpiry(t *testing.T) {
	s := NewDepositStore()
	// Create with 1ms TTL
	d, _ := s.Create(testMacaroon(), "admin", time.Millisecond)

	time.Sleep(5 * time.Millisecond)

	_, err := s.Consume(d.ID, "peer")
	if !errors.Is(err, ErrDepositExpired) {
		t.Errorf("expected ErrDepositExpired, got: %v", err)
	}
}

func TestAddCaveat(t *testing.T) {
	s := NewDepositStore()
	m := testMacaroon()
	originalCaveats := len(m.Caveats)

	d, _ := s.Create(m, "admin", 0)

	if err := s.AddCaveat(d.ID, "peers_max=3"); err != nil {
		t.Fatal(err)
	}

	got, _ := s.Get(d.ID)
	if len(got.Macaroon.Caveats) != originalCaveats+1 {
		t.Errorf("caveats = %d, want %d", len(got.Macaroon.Caveats), originalCaveats+1)
	}
	if got.Macaroon.Caveats[originalCaveats] != "peers_max=3" {
		t.Errorf("caveat = %q, want %q", got.Macaroon.Caveats[originalCaveats], "peers_max=3")
	}
}

func TestAddCaveatNonPending(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)
	s.Consume(d.ID, "peer")

	err := s.AddCaveat(d.ID, "peers_max=1")
	if err == nil {
		t.Error("adding caveat to consumed deposit should fail")
	}
}

func TestList(t *testing.T) {
	s := NewDepositStore()
	s.Create(testMacaroon(), "admin", 0)
	s.Create(testMacaroon(), "admin", 0)

	all := s.List("")
	if len(all) != 2 {
		t.Errorf("list all = %d, want 2", len(all))
	}

	pending := s.List(StatusPending)
	if len(pending) != 2 {
		t.Errorf("list pending = %d, want 2", len(pending))
	}

	consumed := s.List(StatusConsumed)
	if len(consumed) != 0 {
		t.Errorf("list consumed = %d, want 0", len(consumed))
	}
}

func TestCleanExpired(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", time.Millisecond)
	time.Sleep(5 * time.Millisecond)

	// Mark as expired by accessing it
	s.Get(d.ID)

	// Clean with zero cutoff (remove all expired immediately)
	removed := s.CleanExpired(0)
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}

	// Should be gone
	_, err := s.Get(d.ID)
	if !errors.Is(err, ErrDepositNotFound) {
		t.Errorf("expected ErrDepositNotFound after cleanup, got: %v", err)
	}
}

func TestCount(t *testing.T) {
	s := NewDepositStore()
	s.Create(testMacaroon(), "admin", 0)
	d2, _ := s.Create(testMacaroon(), "admin", 0)
	s.Consume(d2.ID, "peer")

	if c := s.Count(StatusPending); c != 1 {
		t.Errorf("pending count = %d, want 1", c)
	}
	if c := s.Count(StatusConsumed); c != 1 {
		t.Errorf("consumed count = %d, want 1", c)
	}
}

func TestNoExpiryNeverExpires(t *testing.T) {
	s := NewDepositStore()
	d, _ := s.Create(testMacaroon(), "admin", 0)

	// Should still be pending
	got, _ := s.Get(d.ID)
	if got.Status != StatusPending {
		t.Errorf("no-expiry deposit should remain pending, got %q", got.Status)
	}
}
