package p2pnet

import (
	"context"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/metrics"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// BandwidthTracker wraps libp2p's BandwidthCounter and bridges per-peer
// bandwidth stats to Prometheus metrics and the daemon API.
type BandwidthTracker struct {
	counter *metrics.BandwidthCounter
	prom    *Metrics // nil-safe
}

// NewBandwidthTracker creates a tracker. Pass nil for prom to disable
// Prometheus publishing (stats are still queryable via PeerStats/Totals).
func NewBandwidthTracker(prom *Metrics) *BandwidthTracker {
	return &BandwidthTracker{
		counter: metrics.NewBandwidthCounter(),
		prom:    prom,
	}
}

// Counter returns the underlying BandwidthCounter for wiring into the
// libp2p host via libp2p.BandwidthReporter().
func (bt *BandwidthTracker) Counter() *metrics.BandwidthCounter {
	return bt.counter
}

// PeerStats returns bandwidth stats for a single peer.
func (bt *BandwidthTracker) PeerStats(p peer.ID) metrics.Stats {
	return bt.counter.GetBandwidthForPeer(p)
}

// AllPeerStats returns bandwidth stats keyed by peer ID.
func (bt *BandwidthTracker) AllPeerStats() map[peer.ID]metrics.Stats {
	return bt.counter.GetBandwidthByPeer()
}

// ProtocolStats returns bandwidth stats for a single protocol.
func (bt *BandwidthTracker) ProtocolStats(proto protocol.ID) metrics.Stats {
	return bt.counter.GetBandwidthForProtocol(proto)
}

// Totals returns aggregate bandwidth stats across all peers and protocols.
func (bt *BandwidthTracker) Totals() metrics.Stats {
	return bt.counter.GetBandwidthTotals()
}

// PublishMetrics scrapes the BandwidthCounter and updates Prometheus gauges.
// Safe to call when prom is nil (no-op).
func (bt *BandwidthTracker) PublishMetrics() {
	if bt.prom == nil {
		return
	}

	// Aggregate totals
	totals := bt.counter.GetBandwidthTotals()
	bt.prom.BandwidthBytesTotal.WithLabelValues("in").Set(float64(totals.TotalIn))
	bt.prom.BandwidthBytesTotal.WithLabelValues("out").Set(float64(totals.TotalOut))

	// Per-peer stats
	byPeer := bt.counter.GetBandwidthByPeer()
	for pid, stats := range byPeer {
		short := pid.String()
		if len(short) > 16 {
			short = short[:16]
		}
		bt.prom.PeerBandwidthBytesTotal.WithLabelValues(short, "in").Set(float64(stats.TotalIn))
		bt.prom.PeerBandwidthBytesTotal.WithLabelValues(short, "out").Set(float64(stats.TotalOut))
		bt.prom.PeerBandwidthRate.WithLabelValues(short, "in").Set(stats.RateIn)
		bt.prom.PeerBandwidthRate.WithLabelValues(short, "out").Set(stats.RateOut)
	}

	// Per-protocol stats
	byProto := bt.counter.GetBandwidthByProtocol()
	for proto, stats := range byProto {
		bt.prom.ProtocolBandwidthBytesTotal.WithLabelValues(string(proto), "in").Set(float64(stats.TotalIn))
		bt.prom.ProtocolBandwidthBytesTotal.WithLabelValues(string(proto), "out").Set(float64(stats.TotalOut))
	}
}

// Start runs a background goroutine that publishes metrics every interval
// and trims idle peers from the counter. Stops when ctx is cancelled.
func (bt *BandwidthTracker) Start(ctx context.Context, interval time.Duration) {
	if bt.prom == nil {
		return // no metrics to publish
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	slog.Info("bandwidth tracker started", "interval", interval)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			bt.PublishMetrics()
			// Trim peers idle for more than 1 hour to bound memory
			bt.counter.TrimIdle(time.Now().Add(-1 * time.Hour))
		}
	}
}
