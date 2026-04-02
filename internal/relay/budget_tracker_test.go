package relay

import (
	"crypto/rand"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/grants"
)

func genTestPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func newTestBudgetTracker(t *testing.T) (*BudgetTracker, *grants.Store) {
	t.Helper()
	rootKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	rand.Read(rootKey)
	rand.Read(hmacKey)
	gs := grants.NewStore(rootKey, hmacKey)
	bt := NewBudgetTracker(gs, 64*1024*1024) // 64MB default
	return bt, gs
}

func TestBudgetTrackerCumulative(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	// Create grant with 1MB budget.
	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate onGrant callback.
	bt.OnGrantOrExtend(pid, g)

	if !bt.HasBudget(pid) {
		t.Fatal("should have budget after grant")
	}

	remaining := bt.RemainingBudget(pid)
	if remaining != 1024*1024 {
		t.Fatalf("expected 1MB remaining, got %d", remaining)
	}

	// Consume 500KB (512000 bytes).
	bt.ConsumeBytes(pid, 500*1024)
	remaining = bt.RemainingBudget(pid)
	expected := int64(1024*1024 - 500*1024) // 536576
	if remaining != expected {
		t.Fatalf("expected %d remaining, got %d", expected, remaining)
	}

	// Consume the rest.
	bt.ConsumeBytes(pid, remaining)
	remaining = bt.RemainingBudget(pid)
	if remaining != 0 {
		t.Fatalf("expected 0 remaining, got %d", remaining)
	}
}

func TestBudgetTrackerUnlimited(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	// Grant with unlimited budget (-1).
	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	remaining := bt.RemainingBudget(pid)
	if remaining <= 0 {
		t.Fatal("unlimited should have huge remaining")
	}

	// Consume 1GB, should still have plenty.
	bt.ConsumeBytes(pid, 1024*1024*1024)
	if bt.RemainingBudget(pid) <= 0 {
		t.Fatal("unlimited should not exhaust")
	}
}

func TestBudgetTrackerDefaultBudget(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	// Grant with DataBudget=0 (use default).
	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	remaining := bt.RemainingBudget(pid)
	if remaining != 64*1024*1024 { // default 64MB
		t.Fatalf("expected 64MB default, got %d", remaining)
	}
}

func TestBudgetTrackerOnRevoke(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	if bt.RemainingBudget(pid) == 0 {
		t.Fatal("should have budget")
	}

	// Revoke sets remaining to 0.
	bt.OnRevoke(pid)
	if bt.RemainingBudget(pid) != 0 {
		t.Fatal("remaining should be 0 after revoke")
	}
}

func TestBudgetTrackerRefillOnExtend(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	// Grant 1MB.
	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	// Consume 800KB.
	bt.ConsumeBytes(pid, 800*1024)
	if bt.RemainingBudget(pid) >= 1024*1024 {
		t.Fatal("should have consumed some")
	}

	// Extend with 2MB budget — refills.
	extGrant := &grants.Grant{DataBudget: 2 * 1024 * 1024}
	bt.OnGrantOrExtend(pid, extGrant)

	remaining := bt.RemainingBudget(pid)
	if remaining != 2*1024*1024 {
		t.Fatalf("expected 2MB after extend, got %d", remaining)
	}
}

func TestBudgetTrackerNonGrantPeer(t *testing.T) {
	bt, _ := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	// No grant — should not have budget entry.
	if bt.HasBudget(pid) {
		t.Fatal("non-grant peer should not have budget entry")
	}
	if bt.RemainingBudget(pid) != 0 {
		t.Fatal("non-grant peer remaining should be 0")
	}

	// DefaultLimit for per-circuit fallback.
	if bt.DefaultLimit() != 64*1024*1024 {
		t.Fatalf("expected 64MB default limit, got %d", bt.DefaultLimit())
	}
}

func TestBudgetTrackerReconnectDoesNotReset(t *testing.T) {
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	g, err := gs.Grant(pid, 1*time.Hour, nil, false, 0, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	// Consume 500KB.
	bt.ConsumeBytes(pid, 500*1024)
	after := bt.RemainingBudget(pid)

	// Simulate reconnect — just query again, should NOT reset (SEC2).
	reconnectRemaining := bt.RemainingBudget(pid)
	if reconnectRemaining != after {
		t.Fatalf("reconnect should NOT reset: before=%d, after=%d", after, reconnectRemaining)
	}
}
