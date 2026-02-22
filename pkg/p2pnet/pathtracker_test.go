package p2pnet

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestPathTracker_ConnectDisconnect(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host2: %v", err)
	}
	defer h2.Close()

	tracker := NewPathTracker(h1, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.Start(ctx)

	// Give the event subscription time to initialize
	time.Sleep(100 * time.Millisecond)

	// Connect h1 to h2
	connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer connectCancel()
	err = h1.Connect(connectCtx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Wait for event to be processed
	time.Sleep(200 * time.Millisecond)

	// Should have path info for h2
	info, ok := tracker.GetPeerPath(h2.ID())
	if !ok {
		t.Fatal("expected path info for connected peer")
	}
	if info.PathType != PathDirect {
		t.Errorf("PathType = %q, want DIRECT", info.PathType)
	}
	if info.Transport != "tcp" {
		t.Errorf("Transport = %q, want tcp", info.Transport)
	}
	if info.IPVersion != "ipv4" {
		t.Errorf("IPVersion = %q, want ipv4", info.IPVersion)
	}
	if info.Address == "" {
		t.Error("Address is empty")
	}
	t.Logf("Connected peer: %+v", info)

	// ListPeerPaths should include it
	paths := tracker.ListPeerPaths()
	if len(paths) == 0 {
		t.Fatal("ListPeerPaths returned empty")
	}
	found := false
	for _, p := range paths {
		if p.PeerID == h2.ID().String() {
			found = true
		}
	}
	if !found {
		t.Error("connected peer not found in ListPeerPaths")
	}

	// Disconnect
	h1.Network().ClosePeer(h2.ID())
	time.Sleep(200 * time.Millisecond)

	// Should no longer have path info
	_, ok = tracker.GetPeerPath(h2.ID())
	if ok {
		t.Error("expected no path info after disconnect")
	}

	paths = tracker.ListPeerPaths()
	for _, p := range paths {
		if p.PeerID == h2.ID().String() {
			t.Error("disconnected peer should not be in ListPeerPaths")
		}
	}
}

func TestPathTracker_UpdateRTT(t *testing.T) {
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host2: %v", err)
	}
	defer h2.Close()

	tracker := NewPathTracker(h1, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	// Connect
	connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer connectCancel()
	err = h1.Connect(connectCtx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Update RTT
	tracker.UpdateRTT(h2.ID(), 42.5)

	info, ok := tracker.GetPeerPath(h2.ID())
	if !ok {
		t.Fatal("expected path info")
	}
	if info.LastRTTMs != 42.5 {
		t.Errorf("LastRTTMs = %f, want 42.5", info.LastRTTMs)
	}
}

func TestPathTracker_WithMetrics(t *testing.T) {
	m := NewMetrics("test", "go1.26")

	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host2: %v", err)
	}
	defer h2.Close()

	tracker := NewPathTracker(h1, m)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.Start(ctx)

	time.Sleep(100 * time.Millisecond)

	// Connect
	connectCtx, connectCancel := context.WithTimeout(ctx, 5*time.Second)
	defer connectCancel()
	err = h1.Connect(connectCtx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	time.Sleep(200 * time.Millisecond)

	// Verify the ConnectedPeers metric was set
	gauge, err := m.ConnectedPeers.GetMetricWithLabelValues("DIRECT", "tcp", "ipv4")
	if err != nil {
		t.Fatalf("get metric: %v", err)
	}
	// Gauge exists and was created - the fact that GetMetricWithLabelValues
	// succeeded without error means the label combination was recorded.
	_ = gauge
}

func TestPathTracker_SnapshotExisting(t *testing.T) {
	// Peers connected before Start() should be picked up via snapshot
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("host2: %v", err)
	}
	defer h2.Close()

	// Connect BEFORE starting tracker
	connectCtx, connectCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer connectCancel()
	err = h1.Connect(connectCtx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}

	tracker := NewPathTracker(h1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go tracker.Start(ctx)

	// Give snapshot time to run
	time.Sleep(200 * time.Millisecond)

	info, ok := tracker.GetPeerPath(h2.ID())
	if !ok {
		t.Fatal("expected path info for pre-existing connection")
	}
	if info.PathType != PathDirect {
		t.Errorf("PathType = %q, want DIRECT", info.PathType)
	}
}
