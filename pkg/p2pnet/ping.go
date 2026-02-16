package p2pnet

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// PingResult holds the result of a single ping to a peer.
type PingResult struct {
	Seq    int     `json:"seq"`
	PeerID string  `json:"peer_id"`
	RttMs  float64 `json:"rtt_ms"`
	Path   string  `json:"path"`  // "DIRECT" or "RELAYED"
	Error  string  `json:"error"` // empty on success
}

// PingStats holds aggregate statistics for a ping session.
type PingStats struct {
	Sent     int     `json:"sent"`
	Received int     `json:"received"`
	Lost     int     `json:"lost"`
	LossPct  float64 `json:"loss_pct"`
	MinMs    float64 `json:"min_ms"`
	AvgMs    float64 `json:"avg_ms"`
	MaxMs    float64 `json:"max_ms"`
}

// PingPeer sends count pings to peerID using the given ping-pong protocol.
// Results are delivered on the returned channel. The channel is closed when
// all pings are sent or the context is cancelled.
//
// If count is 0, pings continuously until ctx is cancelled.
// The caller should read from the channel until it is closed.
func PingPeer(ctx context.Context, h host.Host, peerID peer.ID, protocolID string, count int, interval time.Duration) <-chan PingResult {
	ch := make(chan PingResult, 1)

	go func() {
		defer close(ch)

		seq := 0
		for {
			seq++

			// Check if we've sent enough
			if count > 0 && seq > count {
				return
			}

			result := doPing(ctx, h, peerID, protocolID, seq)

			select {
			case ch <- result:
			case <-ctx.Done():
				return
			}

			// Wait for interval (except after last ping)
			if count > 0 && seq >= count {
				return
			}
			select {
			case <-time.After(interval):
			case <-ctx.Done():
				return
			}
		}
	}()

	return ch
}

// doPing sends a single ping and measures RTT.
func doPing(ctx context.Context, h host.Host, peerID peer.ID, protocolID string, seq int) PingResult {
	result := PingResult{
		Seq:    seq,
		PeerID: peerID.String(),
	}

	// Open stream with timeout
	streamCtx, streamCancel := context.WithTimeout(ctx, 15*time.Second)
	defer streamCancel()

	s, err := h.NewStream(streamCtx, peerID, protocol.ID(protocolID))
	if err != nil {
		result.Error = fmt.Sprintf("stream: %s", truncateError(err.Error()))
		return result
	}
	defer s.Close()

	// Determine connection path
	addr := s.Conn().RemoteMultiaddr().String()
	if strings.Contains(addr, "/p2p-circuit") {
		result.Path = "RELAYED"
	} else {
		result.Path = "DIRECT"
	}

	// Send ping and measure RTT
	start := time.Now()

	if _, err := s.Write([]byte("ping\n")); err != nil {
		result.Error = fmt.Sprintf("write: %s", err)
		return result
	}

	reader := bufio.NewReader(s)
	response, err := reader.ReadString('\n')
	if err != nil {
		result.Error = fmt.Sprintf("read: %s", err)
		return result
	}

	rtt := time.Since(start)
	result.RttMs = float64(rtt.Microseconds()) / 1000.0

	response = strings.TrimSpace(response)
	if response != "pong" {
		result.Error = fmt.Sprintf("unexpected response: %q", response)
		return result
	}

	return result
}

// ComputePingStats computes aggregate statistics from a slice of ping results.
func ComputePingStats(results []PingResult) PingStats {
	stats := PingStats{
		Sent: len(results),
	}

	if len(results) == 0 {
		return stats
	}

	var sum float64
	first := true
	for _, r := range results {
		if r.Error != "" {
			stats.Lost++
			continue
		}
		stats.Received++
		sum += r.RttMs
		if first {
			stats.MinMs = r.RttMs
			stats.MaxMs = r.RttMs
			first = false
		}
		if r.RttMs < stats.MinMs {
			stats.MinMs = r.RttMs
		}
		if r.RttMs > stats.MaxMs {
			stats.MaxMs = r.RttMs
		}
	}

	if stats.Received > 0 {
		stats.AvgMs = sum / float64(stats.Received)
	}
	if stats.Sent > 0 {
		stats.LossPct = float64(stats.Lost) / float64(stats.Sent) * 100
	}

	return stats
}
