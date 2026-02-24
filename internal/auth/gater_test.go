package auth

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// mockConnMultiaddrs satisfies network.ConnMultiaddrs for testing.
type mockConnMultiaddrs struct {
	local, remote multiaddr.Multiaddr
}

func (m *mockConnMultiaddrs) LocalMultiaddr() multiaddr.Multiaddr  { return m.local }
func (m *mockConnMultiaddrs) RemoteMultiaddr() multiaddr.Multiaddr { return m.remote }

func testConnMultiaddrs() network.ConnMultiaddrs {
	local, _ := multiaddr.NewMultiaddr("/ip4/127.0.0.1/tcp/1234")
	remote, _ := multiaddr.NewMultiaddr("/ip4/10.0.0.1/tcp/5678")
	return &mockConnMultiaddrs{local: local, remote: remote}
}

func genPeerID(t testing.TB) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer ID from key: %v", err)
	}
	return pid
}

func TestNewAuthorizedPeerGater(t *testing.T) {
	peers := map[peer.ID]bool{genPeerID(t): true}
	g := NewAuthorizedPeerGater(peers)

	if g == nil {
		t.Fatal("gater should not be nil")
	}
	if g.GetAuthorizedPeersCount() != 1 {
		t.Errorf("count = %d, want 1", g.GetAuthorizedPeersCount())
	}
}

func TestIsAuthorized(t *testing.T) {
	allowed := genPeerID(t)
	denied := genPeerID(t)

	g := NewAuthorizedPeerGater(map[peer.ID]bool{allowed: true})

	if !g.IsAuthorized(allowed) {
		t.Error("allowed peer should be authorized")
	}
	if g.IsAuthorized(denied) {
		t.Error("unknown peer should not be authorized")
	}
}

func TestInterceptPeerDialAlwaysAllows(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	unknown := genPeerID(t)

	if !g.InterceptPeerDial(unknown) {
		t.Error("outbound dial should always be allowed")
	}
}

func TestInterceptSecuredInbound(t *testing.T) {
	allowed := genPeerID(t)
	denied := genPeerID(t)

	g := NewAuthorizedPeerGater(map[peer.ID]bool{allowed: true})

	cm := testConnMultiaddrs()

	if !g.InterceptSecured(network.DirInbound, allowed, cm) {
		t.Error("authorized inbound should be allowed")
	}
	if g.InterceptSecured(network.DirInbound, denied, cm) {
		t.Error("unauthorized inbound should be denied")
	}
}

func TestInterceptSecuredOutbound(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	unknown := genPeerID(t)

	if !g.InterceptSecured(network.DirOutbound, unknown, testConnMultiaddrs()) {
		t.Error("outbound should always be allowed")
	}
}

func TestUpdateAuthorizedPeers(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	if g.GetAuthorizedPeersCount() != 0 {
		t.Fatal("should start empty")
	}

	p1 := genPeerID(t)
	p2 := genPeerID(t)
	g.UpdateAuthorizedPeers(map[peer.ID]bool{p1: true, p2: true})

	if g.GetAuthorizedPeersCount() != 2 {
		t.Errorf("count = %d, want 2", g.GetAuthorizedPeersCount())
	}
	if !g.IsAuthorized(p1) || !g.IsAuthorized(p2) {
		t.Error("updated peers should be authorized")
	}
}

func TestInterceptAddrDial(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	unknown := genPeerID(t)
	addr, _ := multiaddr.NewMultiaddr("/ip4/10.0.0.1/tcp/5678")

	if !g.InterceptAddrDial(unknown, addr) {
		t.Error("InterceptAddrDial should always allow outbound")
	}
}

func TestInterceptAccept(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})

	if !g.InterceptAccept(testConnMultiaddrs()) {
		t.Error("InterceptAccept should always allow (check happens in InterceptSecured)")
	}
}

func TestInterceptUpgraded(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	ok, reason := g.InterceptUpgraded(nil)
	if !ok {
		t.Error("InterceptUpgraded should always allow")
	}
	if reason != 0 {
		t.Errorf("reason = %d, want 0", reason)
	}
}

// --- Enrollment mode tests ---

func TestEnrollmentModeAdmitsProbation(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	g.SetEnrollmentMode(true, 5, 10*time.Second)

	unknown := genPeerID(t)
	cm := testConnMultiaddrs()

	if !g.InterceptSecured(network.DirInbound, unknown, cm) {
		t.Error("enrollment mode should admit unknown peer on probation")
	}
	if g.ProbationCount() != 1 {
		t.Errorf("probation count = %d, want 1", g.ProbationCount())
	}
}

func TestEnrollmentModeDisabledDenies(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	// Enrollment mode is off by default.

	unknown := genPeerID(t)
	cm := testConnMultiaddrs()

	if g.InterceptSecured(network.DirInbound, unknown, cm) {
		t.Error("should deny unknown peer when enrollment is off")
	}
}

func TestEnrollmentModeLimitEnforced(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	g.SetEnrollmentMode(true, 2, 10*time.Second)

	cm := testConnMultiaddrs()
	// Admit 2 peers (limit).
	g.InterceptSecured(network.DirInbound, genPeerID(t), cm)
	g.InterceptSecured(network.DirInbound, genPeerID(t), cm)

	// Third should be denied.
	third := genPeerID(t)
	if g.InterceptSecured(network.DirInbound, third, cm) {
		t.Error("should deny when probation limit reached")
	}
}

func TestPromotePeer(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	g.SetEnrollmentMode(true, 5, 10*time.Second)

	p := genPeerID(t)
	cm := testConnMultiaddrs()

	g.InterceptSecured(network.DirInbound, p, cm)
	if g.ProbationCount() != 1 {
		t.Fatal("should be on probation")
	}

	g.PromotePeer(p)

	if g.ProbationCount() != 0 {
		t.Error("probation count should be 0 after promotion")
	}
	if !g.IsAuthorized(p) {
		t.Error("promoted peer should be authorized")
	}
}

func TestCleanupProbation(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	g.SetEnrollmentMode(true, 5, 10*time.Millisecond)

	p := genPeerID(t)
	cm := testConnMultiaddrs()
	g.InterceptSecured(network.DirInbound, p, cm)

	time.Sleep(20 * time.Millisecond)

	var evicted []peer.ID
	g.CleanupProbation(func(id peer.ID) {
		evicted = append(evicted, id)
	})

	if len(evicted) != 1 || evicted[0] != p {
		t.Errorf("should evict 1 peer, got %d", len(evicted))
	}
	if g.ProbationCount() != 0 {
		t.Error("probation should be empty after cleanup")
	}
}

func TestDisableEnrollmentClearsProbation(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{})
	g.SetEnrollmentMode(true, 5, 10*time.Second)

	cm := testConnMultiaddrs()
	g.InterceptSecured(network.DirInbound, genPeerID(t), cm)
	g.InterceptSecured(network.DirInbound, genPeerID(t), cm)

	g.SetEnrollmentMode(false, 0, 0)

	if g.ProbationCount() != 0 {
		t.Error("disabling enrollment should clear probation peers")
	}
	if g.IsEnrollmentEnabled() {
		t.Error("enrollment should be disabled")
	}
}

// --- Expiry tests ---

func TestExpiredPeerDenied(t *testing.T) {
	p := genPeerID(t)
	g := NewAuthorizedPeerGater(map[peer.ID]bool{p: true})
	g.SetPeerExpiry(p, time.Now().Add(-time.Hour)) // expired 1 hour ago

	cm := testConnMultiaddrs()
	if g.InterceptSecured(network.DirInbound, p, cm) {
		t.Error("expired peer should be denied")
	}
}

func TestNonExpiredPeerAllowed(t *testing.T) {
	p := genPeerID(t)
	g := NewAuthorizedPeerGater(map[peer.ID]bool{p: true})
	g.SetPeerExpiry(p, time.Now().Add(time.Hour)) // expires in 1 hour

	cm := testConnMultiaddrs()
	if !g.InterceptSecured(network.DirInbound, p, cm) {
		t.Error("non-expired peer should be allowed")
	}
}

func TestNoExpiryPeerAllowed(t *testing.T) {
	p := genPeerID(t)
	g := NewAuthorizedPeerGater(map[peer.ID]bool{p: true})
	// No SetPeerExpiry call = no expiry

	cm := testConnMultiaddrs()
	if !g.InterceptSecured(network.DirInbound, p, cm) {
		t.Error("peer with no expiry should be allowed")
	}
}

func TestClearExpiry(t *testing.T) {
	p := genPeerID(t)
	g := NewAuthorizedPeerGater(map[peer.ID]bool{p: true})
	g.SetPeerExpiry(p, time.Now().Add(-time.Hour)) // expired

	cm := testConnMultiaddrs()
	if g.InterceptSecured(network.DirInbound, p, cm) {
		t.Error("should be denied while expired")
	}

	// Clear expiry.
	g.SetPeerExpiry(p, time.Time{})
	if !g.InterceptSecured(network.DirInbound, p, cm) {
		t.Error("should be allowed after clearing expiry")
	}
}
