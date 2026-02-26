package p2pnet

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"

	"github.com/shurlinet/shurli/internal/config"
)

func TestPeerManager_SetWatchlist(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	netC := newListeningNetwork(t)

	pm := NewPeerManager(netA.Host(), nil, nil, nil)

	// Set watchlist with 2 peers.
	pm.SetWatchlist([]peer.ID{netB.Host().ID(), netC.Host().ID()})
	peers := pm.GetManagedPeers()
	if len(peers) != 2 {
		t.Fatalf("expected 2 managed peers, got %d", len(peers))
	}

	// Remove one peer.
	pm.SetWatchlist([]peer.ID{netB.Host().ID()})
	peers = pm.GetManagedPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 managed peer after removal, got %d", len(peers))
	}
	if peers[0].PeerID != netB.Host().ID().String() {
		t.Errorf("expected peer B, got %s", peers[0].PeerID)
	}

	// Self should be excluded.
	pm.SetWatchlist([]peer.ID{netA.Host().ID(), netB.Host().ID()})
	peers = pm.GetManagedPeers()
	if len(peers) != 1 {
		t.Fatalf("expected self excluded, got %d peers", len(peers))
	}
}

func TestPeerManager_SnapshotExisting(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)

	// Connect A to B first.
	connectNetworks(t, netA, netB)

	pm := NewPeerManager(netA.Host(), nil, nil, nil)
	pm.SetWatchlist([]peer.ID{netB.Host().ID()})

	// Start should snapshot B as connected.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.Start(ctx)
	defer pm.Close()

	peers := pm.GetManagedPeers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if !peers[0].Connected {
		t.Error("expected peer B to be connected after snapshot")
	}
}

func TestPeerManager_ConnectDisconnect(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)

	pm := NewPeerManager(netA.Host(), nil, nil, nil)
	pm.SetWatchlist([]peer.ID{netB.Host().ID()})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.Start(ctx)
	defer pm.Close()

	// Verify B starts as disconnected.
	peers := pm.GetManagedPeers()
	if peers[0].Connected {
		t.Fatal("expected peer B disconnected initially")
	}

	// Connect A to B.
	connectNetworks(t, netA, netB)
	time.Sleep(200 * time.Millisecond) // event propagation

	peers = pm.GetManagedPeers()
	if !peers[0].Connected {
		t.Error("expected peer B connected after connect")
	}

	// Disconnect by closing B's network.
	netB.Close()
	time.Sleep(500 * time.Millisecond) // event propagation

	peers = pm.GetManagedPeers()
	if peers[0].Connected {
		t.Error("expected peer B disconnected after close")
	}
}

func TestPeerManager_Backoff(t *testing.T) {
	netA := newListeningNetwork(t)

	// Create a peer ID from a network we immediately close (unreachable).
	dir := t.TempDir()
	unreachable, err := New(&Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create unreachable network: %v", err)
	}
	unreachablePID := unreachable.Host().ID()
	unreachable.Close()

	pd := NewPathDialer(netA.Host(), nil, nil, nil)
	pm := NewPeerManager(netA.Host(), pd, nil, nil)
	pm.SetWatchlist([]peer.ID{unreachablePID})

	// Directly attempt reconnection (don't start the loop).
	pm.ctx, pm.cancel = context.WithCancel(context.Background())
	defer pm.cancel()

	pm.attemptReconnect(unreachablePID)

	pm.mu.RLock()
	mp := pm.peers[unreachablePID]
	failures1 := mp.ConsecFailures
	backoff1 := mp.BackoffUntil
	pm.mu.RUnlock()

	if failures1 != 1 {
		t.Errorf("expected 1 failure, got %d", failures1)
	}
	if backoff1.IsZero() {
		t.Error("expected non-zero backoff after failure")
	}

	// Second failure should increase backoff.
	pm.attemptReconnect(unreachablePID)

	pm.mu.RLock()
	failures2 := mp.ConsecFailures
	backoff2 := mp.BackoffUntil
	pm.mu.RUnlock()

	if failures2 != 2 {
		t.Errorf("expected 2 failures, got %d", failures2)
	}
	if !backoff2.After(backoff1) {
		t.Error("expected backoff to increase after second failure")
	}
}

func TestPeerManager_OnNetworkChange(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)

	pm := NewPeerManager(netA.Host(), nil, nil, nil)
	pm.SetWatchlist([]peer.ID{netB.Host().ID()})

	// Simulate accumulated backoff.
	pm.mu.Lock()
	mp := pm.peers[netB.Host().ID()]
	mp.ConsecFailures = 5
	mp.BackoffUntil = time.Now().Add(15 * time.Minute)
	pm.mu.Unlock()

	// Network change should reset everything.
	pm.OnNetworkChange()

	pm.mu.RLock()
	if mp.ConsecFailures != 0 {
		t.Errorf("expected 0 failures after network change, got %d", mp.ConsecFailures)
	}
	if !mp.BackoffUntil.IsZero() {
		t.Error("expected zero backoff after network change")
	}
	pm.mu.RUnlock()
}

func TestPeerManager_WithMetrics(t *testing.T) {
	netA := newListeningNetwork(t)

	// Create an unreachable peer.
	dir := t.TempDir()
	unreachable, err := New(&Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create unreachable network: %v", err)
	}
	unreachablePID := unreachable.Host().ID()
	unreachable.Close()

	metrics := NewMetrics("test", "go1.26")
	pd := NewPathDialer(netA.Host(), nil, nil, nil)
	pm := NewPeerManager(netA.Host(), pd, metrics, nil)
	pm.SetWatchlist([]peer.ID{unreachablePID})

	pm.ctx, pm.cancel = context.WithCancel(context.Background())
	defer pm.cancel()

	pm.attemptReconnect(unreachablePID)

	// Check that failure metric was incremented.
	val := testCounterValue(t, metrics.PeerManagerReconnectTotal, "failure")
	if val != 1 {
		t.Errorf("expected failure counter = 1, got %f", val)
	}
}

// testCounterValue reads the current value of a CounterVec for the given label.
func testCounterValue(t *testing.T, cv *prometheus.CounterVec, label string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := cv.WithLabelValues(label).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}

func TestExtractIPv6TCPAddr(t *testing.T) {
	tests := []struct {
		name     string
		addr     string
		wantIP6  string
		wantPort string
	}{
		{
			name:     "ipv6 with tcp",
			addr:     "/ip6/2001:db8::1/tcp/4001",
			wantIP6:  "2001:db8::1",
			wantPort: "4001",
		},
		{
			name:     "ipv6 with tcp and p2p",
			addr:     "/ip6/2001:db8::1/tcp/4001/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			wantIP6:  "2001:db8::1",
			wantPort: "4001",
		},
		{
			name:     "ipv4 only",
			addr:     "/ip4/203.0.113.1/tcp/4001",
			wantIP6:  "",
			wantPort: "",
		},
		{
			name:     "ipv6 without tcp",
			addr:     "/ip6/2001:db8::1/udp/4001/quic-v1",
			wantIP6:  "",
			wantPort: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr, err := ma.NewMultiaddr(tt.addr)
			if err != nil {
				t.Fatalf("bad test multiaddr: %v", err)
			}
			ip6, port := extractIPv6TCPAddr(addr)
			if ip6 != tt.wantIP6 {
				t.Errorf("ip6 = %q, want %q", ip6, tt.wantIP6)
			}
			if port != tt.wantPort {
				t.Errorf("port = %q, want %q", port, tt.wantPort)
			}
		})
	}
}

func TestAllConnsRelayed(t *testing.T) {
	// Empty slice should return false (no connections = not relayed).
	if allConnsRelayed(nil) {
		t.Error("expected false for nil conns")
	}
}

func TestPeerHasIPv6(t *testing.T) {
	v6, _ := ma.NewMultiaddr("/ip6/2001:db8::1/tcp/4001")
	v4, _ := ma.NewMultiaddr("/ip4/203.0.113.1/tcp/4001")

	if !peerHasIPv6([]ma.Multiaddr{v4, v6}) {
		t.Error("expected true when IPv6 addr present")
	}
	if peerHasIPv6([]ma.Multiaddr{v4}) {
		t.Error("expected false when only IPv4")
	}
	if peerHasIPv6(nil) {
		t.Error("expected false for nil addrs")
	}
}
