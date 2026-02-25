package p2pnet

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// testStateProvider returns a NodeStateProvider that produces a fixed announcement.
func testStateProvider(grade, natType string) NodeStateProvider {
	return func() *NodeAnnouncement {
		return &NodeAnnouncement{
			Version:   1,
			Grade:     grade,
			NATType:   natType,
			HasIPv4:   true,
			HasIPv6:   false,
			UptimeSec: 300,
			PeerCount: 2,
			Timestamp: time.Now().Unix(),
		}
	}
}

// acceptAll is a PeerFilter that accepts every peer.
func acceptAll(_ peer.ID) bool { return true }

func TestNetIntel_PushAndReceive(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	niA := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("B", "full-cone"), 100*time.Millisecond)
	niB := NewNetIntel(netB.Host(), nil, acceptAll, testStateProvider("A", "symmetric"), 100*time.Millisecond)
	niA.Start(ctx)
	niB.Start(ctx)
	defer niA.Close()
	defer niB.Close()

	// Wait for at least one publish cycle.
	time.Sleep(400 * time.Millisecond)

	// B should have A's announcement cached.
	pa := niB.GetPeerState(netA.Host().ID())
	if pa == nil {
		t.Fatal("expected B to have A's announcement cached")
	}
	if pa.Announcement.Grade != "B" {
		t.Errorf("grade = %q, want %q", pa.Announcement.Grade, "B")
	}
	if pa.Announcement.NATType != "full-cone" {
		t.Errorf("nat_type = %q, want %q", pa.Announcement.NATType, "full-cone")
	}

	// A should have B's announcement cached.
	pb := niA.GetPeerState(netB.Host().ID())
	if pb == nil {
		t.Fatal("expected A to have B's announcement cached")
	}
	if pb.Announcement.Grade != "A" {
		t.Errorf("grade = %q, want %q", pb.Announcement.Grade, "A")
	}
}

func TestNetIntel_PeerFilter(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// A pushes to all.
	niA := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("C", "unknown"), 100*time.Millisecond)
	niA.Start(ctx)
	defer niA.Close()

	// B rejects A's peer ID.
	rejectA := func(pid peer.ID) bool {
		return pid != netA.Host().ID()
	}
	niB := NewNetIntel(netB.Host(), nil, rejectA, testStateProvider("A", "full-cone"), 100*time.Millisecond)
	niB.Start(ctx)
	defer niB.Close()

	time.Sleep(400 * time.Millisecond)

	// B should NOT have A's announcement (rejected by filter).
	if pa := niB.GetPeerState(netA.Host().ID()); pa != nil {
		t.Error("expected B to reject A's announcement, but found it cached")
	}
}

func TestNetIntel_CacheExpiry(t *testing.T) {
	netA := newListeningNetwork(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ni := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("A", "full-cone"), time.Hour)
	ni.Start(ctx)
	defer ni.Close()

	// Manually inject a stale announcement.
	fakePeerID := netA.Host().ID() // doesn't matter, just need a valid ID
	// Create a different peer ID for the cache entry.
	netB := newListeningNetwork(t)
	staleTime := time.Now().Add(-announceTTL - time.Minute)

	ni.mu.Lock()
	ni.cache[netB.Host().ID()] = &PeerAnnouncement{
		PeerID: netB.Host().ID(),
		Announcement: NodeAnnouncement{
			Version:   1,
			Grade:     "D",
			Timestamp: staleTime.Unix(),
		},
		ReceivedAt: staleTime,
	}
	ni.mu.Unlock()
	_ = fakePeerID

	// Verify it's there.
	if pa := ni.GetPeerState(netB.Host().ID()); pa == nil {
		t.Fatal("expected stale entry to be in cache before cleanup")
	}

	// Run cleanup.
	ni.evictStale()

	// Should be gone now.
	if pa := ni.GetPeerState(netB.Host().ID()); pa != nil {
		t.Error("expected stale entry to be evicted after cleanup")
	}
}

func TestNetIntel_AnnounceNow(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use a very long interval so the timer doesn't fire naturally.
	niA := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("B", "full-cone"), time.Hour)
	niB := NewNetIntel(netB.Host(), nil, acceptAll, testStateProvider("A", "symmetric"), time.Hour)
	niA.Start(ctx)
	niB.Start(ctx)
	defer niA.Close()
	defer niB.Close()

	// B should not have A's announcement yet (interval is 1 hour).
	time.Sleep(100 * time.Millisecond)
	if pa := niB.GetPeerState(netA.Host().ID()); pa != nil {
		t.Fatal("expected no announcement before AnnounceNow")
	}

	// Trigger immediate announce.
	niA.AnnounceNow()
	time.Sleep(200 * time.Millisecond)

	// Now B should have it.
	pa := niB.GetPeerState(netA.Host().ID())
	if pa == nil {
		t.Fatal("expected B to have A's announcement after AnnounceNow")
	}
	if pa.Announcement.Grade != "B" {
		t.Errorf("grade = %q, want %q", pa.Announcement.Grade, "B")
	}
}

func TestNetIntel_GossipForwarding(t *testing.T) {
	// A -- B -- C  (A and C are NOT directly connected)
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	netC := newListeningNetwork(t)
	connectNetworks(t, netA, netB) // A <-> B
	connectNetworks(t, netB, netC) // B <-> C

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	niA := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("A", "full-cone"), time.Hour)
	niB := NewNetIntel(netB.Host(), nil, acceptAll, testStateProvider("B", "symmetric"), time.Hour)
	niC := NewNetIntel(netC.Host(), nil, acceptAll, testStateProvider("C", "unknown"), time.Hour)
	niA.Start(ctx)
	niB.Start(ctx)
	niC.Start(ctx)
	defer niA.Close()
	defer niB.Close()
	defer niC.Close()

	// A announces. B receives directly (hop 0) and forwards to C (hop 1).
	niA.AnnounceNow()
	time.Sleep(500 * time.Millisecond)

	// C should have A's announcement via gossip forwarding through B.
	pa := niC.GetPeerState(netA.Host().ID())
	if pa == nil {
		t.Fatal("expected C to receive A's announcement via gossip forwarding through B")
	}
	if pa.Announcement.Grade != "A" {
		t.Errorf("grade = %q, want %q", pa.Announcement.Grade, "A")
	}
	if pa.Announcement.Hops < 1 {
		t.Errorf("hops = %d, want >= 1 (forwarded)", pa.Announcement.Hops)
	}
}

func TestNetIntel_HopLimit(t *testing.T) {
	// Chain: A -- B -- C -- D
	// With maxHops=3, D should receive A's announcement (hops: A->B=0, B->C=1, C->D=2).
	// But if we use a shorter chain for the negative test, let's verify hop counting.
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	netC := newListeningNetwork(t)
	netD := newListeningNetwork(t)
	connectNetworks(t, netA, netB)
	connectNetworks(t, netB, netC)
	connectNetworks(t, netC, netD)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	niA := NewNetIntel(netA.Host(), nil, acceptAll, testStateProvider("A", "full-cone"), time.Hour)
	niB := NewNetIntel(netB.Host(), nil, acceptAll, testStateProvider("B", "symmetric"), time.Hour)
	niC := NewNetIntel(netC.Host(), nil, acceptAll, testStateProvider("C", "unknown"), time.Hour)
	niD := NewNetIntel(netD.Host(), nil, acceptAll, testStateProvider("D", "full-cone"), time.Hour)
	niA.Start(ctx)
	niB.Start(ctx)
	niC.Start(ctx)
	niD.Start(ctx)
	defer niA.Close()
	defer niB.Close()
	defer niC.Close()
	defer niD.Close()

	niA.AnnounceNow()
	time.Sleep(800 * time.Millisecond)

	// B should have it at hops=0 (direct from A).
	pbAnn := niB.GetPeerState(netA.Host().ID())
	if pbAnn == nil {
		t.Fatal("expected B to have A's announcement")
	}
	if pbAnn.Announcement.Hops != 0 {
		t.Errorf("B saw hops=%d, want 0", pbAnn.Announcement.Hops)
	}

	// C should have it at hops=1 (forwarded by B).
	pcAnn := niC.GetPeerState(netA.Host().ID())
	if pcAnn == nil {
		t.Fatal("expected C to have A's announcement")
	}
	if pcAnn.Announcement.Hops != 1 {
		t.Errorf("C saw hops=%d, want 1", pcAnn.Announcement.Hops)
	}

	// D should have it at hops=2 (forwarded by C).
	pdAnn := niD.GetPeerState(netA.Host().ID())
	if pdAnn == nil {
		t.Fatal("expected D to have A's announcement (within maxHops)")
	}
	if pdAnn.Announcement.Hops != 2 {
		t.Errorf("D saw hops=%d, want 2", pdAnn.Announcement.Hops)
	}
}

func TestNetIntel_Metrics(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	metrics := NewMetrics("test", "go1.26")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	niA := NewNetIntel(netA.Host(), metrics, acceptAll, testStateProvider("B", "full-cone"), 100*time.Millisecond)
	niB := NewNetIntel(netB.Host(), metrics, acceptAll, testStateProvider("A", "symmetric"), 100*time.Millisecond)
	niA.Start(ctx)
	niB.Start(ctx)
	defer niA.Close()
	defer niB.Close()

	time.Sleep(400 * time.Millisecond)

	// Check that sent metric was incremented.
	sentVal := readCounterVec(t, metrics.NetIntelSentTotal, "success")
	if sentVal < 1 {
		t.Errorf("expected sent success counter >= 1, got %f", sentVal)
	}

	// Check that received metric was incremented.
	recvVal := readCounterVec(t, metrics.NetIntelReceivedTotal, "accepted")
	if recvVal < 1 {
		t.Errorf("expected received accepted counter >= 1, got %f", recvVal)
	}
}

// readCounterVec reads the current value of a CounterVec for the given label.
func readCounterVec(t *testing.T, cv *prometheus.CounterVec, label string) float64 {
	t.Helper()
	m := &dto.Metric{}
	if err := cv.WithLabelValues(label).Write(m); err != nil {
		t.Fatalf("read counter: %v", err)
	}
	return m.GetCounter().GetValue()
}
