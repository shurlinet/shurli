package auth

import (
	"log"
	"testing"

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
	g := NewAuthorizedPeerGater(peers, nil)

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

	g := NewAuthorizedPeerGater(map[peer.ID]bool{allowed: true}, nil)

	if !g.IsAuthorized(allowed) {
		t.Error("allowed peer should be authorized")
	}
	if g.IsAuthorized(denied) {
		t.Error("unknown peer should not be authorized")
	}
}

func TestInterceptPeerDialAlwaysAllows(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{}, nil)
	unknown := genPeerID(t)

	if !g.InterceptPeerDial(unknown) {
		t.Error("outbound dial should always be allowed")
	}
}

func TestInterceptSecuredInbound(t *testing.T) {
	allowed := genPeerID(t)
	denied := genPeerID(t)

	g := NewAuthorizedPeerGater(map[peer.ID]bool{allowed: true}, log.Default())

	cm := testConnMultiaddrs()

	if !g.InterceptSecured(network.DirInbound, allowed, cm) {
		t.Error("authorized inbound should be allowed")
	}
	if g.InterceptSecured(network.DirInbound, denied, cm) {
		t.Error("unauthorized inbound should be denied")
	}
}

func TestInterceptSecuredOutbound(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{}, nil)
	unknown := genPeerID(t)

	if !g.InterceptSecured(network.DirOutbound, unknown, testConnMultiaddrs()) {
		t.Error("outbound should always be allowed")
	}
}

func TestUpdateAuthorizedPeers(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{}, nil)
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

func TestInterceptUpgraded(t *testing.T) {
	g := NewAuthorizedPeerGater(map[peer.ID]bool{}, nil)
	ok, reason := g.InterceptUpgraded(nil)
	if !ok {
		t.Error("InterceptUpgraded should always allow")
	}
	if reason != 0 {
		t.Errorf("reason = %d, want 0", reason)
	}
}
