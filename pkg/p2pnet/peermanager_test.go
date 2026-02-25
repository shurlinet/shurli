package p2pnet

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
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
