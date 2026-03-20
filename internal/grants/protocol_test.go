package grants

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shurlinet/shurli/internal/macaroon"
)

// newTestPair creates two connected libp2p hosts with grant protocols.
// issuerTrusted controls whether the receiver trusts the issuer.
func newTestPair(t *testing.T, issuerTrusted bool) (issuerProto *GrantProtocol, receiverProto *GrantProtocol, issuerID peer.ID, receiverID peer.ID) {
	t.Helper()

	h1, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h1.Close() })

	h2, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h2.Close() })

	// Connect h1 -> h2.
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	h2.Peerstore().AddAddrs(h1.ID(), h1.Addrs(), time.Hour)

	_, hmacKey := genKeys(t)
	pouch := NewPouch(hmacKey)

	trustCheck := func(pid peer.ID) bool {
		if !issuerTrusted {
			return false
		}
		return pid == h1.ID()
	}

	issuerProto = NewGrantProtocol(h1, nil, nil, nil) // issuer doesn't need pouch/queue
	receiverProto = NewGrantProtocol(h2, pouch, nil, trustCheck)
	receiverProto.Register()

	return issuerProto, receiverProto, h1.ID(), h2.ID()
}

func TestDeliverGrant(t *testing.T) {
	issuerProto, receiverProto, _, receiverID := newTestPair(t, true)

	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-grant")
	expiresAt := time.Now().Add(1 * time.Hour)

	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, []string{"file-transfer"}, expiresAt, false)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// Verify token is in receiver's pouch.
	got := receiverProto.pouch.Get(issuerProto.host.ID(), "file-transfer")
	if got == nil {
		t.Fatal("receiver should have the token in pouch")
	}

	// Service filter should work.
	got2 := receiverProto.pouch.Get(issuerProto.host.ID(), "file-browse")
	if got2 != nil {
		t.Fatal("file-browse should NOT match (not in services list)")
	}
}

func TestDeliverGrantPermanent(t *testing.T) {
	issuerProto, receiverProto, _, receiverID := newTestPair(t, true)

	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-permanent")

	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Time{}, true)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	got := receiverProto.pouch.Get(issuerProto.host.ID(), "file-transfer")
	if got == nil {
		t.Fatal("receiver should have permanent token")
	}
}

func TestDeliverRevocation(t *testing.T) {
	issuerProto, receiverProto, issuerID, receiverID := newTestPair(t, true)

	// First deliver a grant.
	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-revoke")

	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Now().Add(1*time.Hour), false)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	// Verify it's there.
	if got := receiverProto.pouch.Get(issuerID, "file-transfer"); got == nil {
		t.Fatal("should have token before revocation")
	}

	// Send revocation.
	err = issuerProto.DeliverRevocation(context.Background(), receiverID, "admin revoked")
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// Token should be gone.
	if got := receiverProto.pouch.Get(issuerID, "file-transfer"); got != nil {
		t.Fatal("should NOT have token after revocation")
	}
}

func TestRejectUntrustedPeer(t *testing.T) {
	issuerProto, _, _, receiverID := newTestPair(t, false)

	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-untrusted")

	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Now().Add(1*time.Hour), false)
	if err == nil {
		t.Fatal("should reject delivery from untrusted peer")
	}
}

func TestRejectExpiredToken(t *testing.T) {
	issuerProto, receiverProto, issuerID, receiverID := newTestPair(t, true)

	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-expired")

	// Deliver a token that's already expired.
	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Now().Add(-1*time.Hour), false)
	if err == nil {
		t.Fatal("should reject already-expired token")
	}

	// Pouch should be empty.
	if got := receiverProto.pouch.Get(issuerID, "file-transfer"); got != nil {
		t.Fatal("should not store expired token")
	}
}

func TestRateLimit(t *testing.T) {
	issuerProto, _, _, receiverID := newTestPair(t, true)

	rootKey := make([]byte, 32)

	// Send 5 deliveries (within rate limit).
	for i := 0; i < grantMaxPerMinute; i++ {
		token := macaroon.New("test-node", rootKey, fmt.Sprintf("grant-%d", i))
		err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Now().Add(1*time.Hour), false)
		if err != nil {
			t.Fatalf("delivery %d should succeed: %v", i, err)
		}
	}

	// 6th should be rate limited.
	token := macaroon.New("test-node", rootKey, "grant-over-limit")
	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, nil, time.Now().Add(1*time.Hour), false)
	if err == nil {
		t.Fatal("should be rate limited")
	}
}

func TestDeliverGrantMultipleServices(t *testing.T) {
	issuerProto, receiverProto, issuerID, receiverID := newTestPair(t, true)

	rootKey := make([]byte, 32)
	token := macaroon.New("test-node", rootKey, "test-multi-svc")
	services := []string{"file-transfer", "file-browse"}

	err := issuerProto.DeliverGrant(context.Background(), receiverID, token, services, time.Now().Add(1*time.Hour), false)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}

	for _, svc := range services {
		if got := receiverProto.pouch.Get(issuerID, svc); got == nil {
			t.Fatalf("%s should match", svc)
		}
	}

	if got := receiverProto.pouch.Get(issuerID, "unknown-service"); got != nil {
		t.Fatal("unknown-service should NOT match")
	}
}

func TestRateLimitResetsAfterWindow(t *testing.T) {
	_, hmacKey := genKeys(t)
	pouch := NewPouch(hmacKey)

	gp := &GrantProtocol{
		rateLimit: make(map[peer.ID]*rateLimitEntry),
		pouch:     pouch,
		stopCh:    make(chan struct{}),
		doneCh:    make(chan struct{}),
	}

	pid := genPeerID(t)

	// Fill up rate limit.
	for i := 0; i < grantMaxPerMinute; i++ {
		if !gp.checkRateLimit(pid) {
			t.Fatalf("should allow delivery %d", i)
		}
	}
	if gp.checkRateLimit(pid) {
		t.Fatal("should be rate limited")
	}

	// Simulate window reset by backdating.
	gp.rateMu.Lock()
	gp.rateLimit[pid].windowAt = time.Now().Add(-2 * time.Minute)
	gp.rateMu.Unlock()

	if !gp.checkRateLimit(pid) {
		t.Fatal("should allow after window reset")
	}
}
