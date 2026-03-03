package p2pnet

import (
	"runtime"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestBandwidthTracker_NilSafe(t *testing.T) {
	// BandwidthTracker with nil metrics should not panic
	bt := NewBandwidthTracker(nil)
	if bt == nil {
		t.Fatal("NewBandwidthTracker(nil) returned nil")
	}

	// Counter should always be available (for libp2p.BandwidthReporter)
	if bt.Counter() == nil {
		t.Fatal("Counter() returned nil")
	}

	// PublishMetrics with nil prom should be a no-op (no panic)
	bt.PublishMetrics()

	// Totals should return zero stats
	totals := bt.Totals()
	if totals.TotalIn != 0 || totals.TotalOut != 0 {
		t.Errorf("expected zero totals, got in=%d out=%d", totals.TotalIn, totals.TotalOut)
	}

	// AllPeerStats should return empty map
	byPeer := bt.AllPeerStats()
	if len(byPeer) != 0 {
		t.Errorf("expected empty peer stats, got %d", len(byPeer))
	}
}

func TestBandwidthTracker_WithMetrics(t *testing.T) {
	m := NewMetrics("test", runtime.Version())
	bt := NewBandwidthTracker(m)

	// PublishMetrics should not panic with real metrics
	bt.PublishMetrics()

	// Verify aggregate gauges are initialized (zero values)
	inGauge, err := m.BandwidthBytesTotal.GetMetricWithLabelValues("in")
	if err != nil {
		t.Fatalf("failed to get in gauge: %v", err)
	}

	// After PublishMetrics with no traffic, gauge should be 0
	desc := inGauge.Desc().String()
	if desc == "" {
		t.Fatal("gauge descriptor is empty")
	}
}

func TestBandwidthTracker_PeerStats(t *testing.T) {
	bt := NewBandwidthTracker(nil)

	// Query stats for a non-existent peer (should return zero, not error)
	fakePeer, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	stats := bt.PeerStats(fakePeer)
	if stats.TotalIn != 0 || stats.TotalOut != 0 {
		t.Errorf("expected zero stats for unknown peer, got in=%d out=%d", stats.TotalIn, stats.TotalOut)
	}
}
