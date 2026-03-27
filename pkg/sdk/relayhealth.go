package sdk

import (
	"context"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
)

// RelayHealthScore tracks the health of a single relay peer.
type RelayHealthScore struct {
	PeerID      peer.ID   `json:"peer_id"`
	Score       float64   `json:"score"`        // 0.0 (dead) to 1.0 (perfect)
	RTTMs       float64   `json:"rtt_ms"`       // EWMA of ping RTT
	SuccessRate float64   `json:"success_rate"` // EWMA of probe success (0-1)
	LastProbe   time.Time `json:"last_probe"`
	LastSuccess time.Time `json:"last_success"`
	ProbeCount  int       `json:"probe_count"`
	IsStatic    bool      `json:"is_static"`
}

// ewmaAlpha is the smoothing factor for EWMA updates.
// Higher = more weight on recent observations.
const ewmaAlpha = 0.3

// defaultScore is the initial score for unknown relays.
const defaultScore = 0.5

// RelayHealth tracks relay peer health using EWMA scoring.
// Probes relays periodically and scores them on success rate, RTT, and freshness.
type RelayHealth struct {
	host    host.Host
	metrics *Metrics

	mu     sync.RWMutex
	relays map[peer.ID]*RelayHealthScore
}

// NewRelayHealth creates a new relay health tracker.
func NewRelayHealth(h host.Host, m *Metrics) *RelayHealth {
	return &RelayHealth{
		host:    h,
		metrics: m,
		relays:  make(map[peer.ID]*RelayHealthScore),
	}
}

// RegisterRelay adds a relay to the health tracker. Safe to call multiple times.
func (rh *RelayHealth) RegisterRelay(peerID peer.ID, isStatic bool) {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	if _, exists := rh.relays[peerID]; !exists {
		rh.relays[peerID] = &RelayHealthScore{
			PeerID:      peerID,
			Score:       defaultScore,
			SuccessRate: defaultScore,
			RTTMs:       500, // assume 500ms until first probe
			IsStatic:    isStatic,
		}
	}
}

// RecordSuccess records a successful probe with the measured RTT.
func (rh *RelayHealth) RecordSuccess(peerID peer.ID, rttMs float64) {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	s, ok := rh.relays[peerID]
	if !ok {
		return
	}

	now := time.Now()
	s.RTTMs = s.RTTMs*(1-ewmaAlpha) + rttMs*ewmaAlpha
	s.SuccessRate = s.SuccessRate*(1-ewmaAlpha) + 1.0*ewmaAlpha
	s.LastProbe = now
	s.LastSuccess = now
	s.ProbeCount++
	s.Score = computeScore(s.SuccessRate, s.RTTMs, now, s.LastProbe)

	rh.publishMetric(s)
}

// RecordFailure records a failed probe.
func (rh *RelayHealth) RecordFailure(peerID peer.ID) {
	rh.mu.Lock()
	defer rh.mu.Unlock()

	s, ok := rh.relays[peerID]
	if !ok {
		return
	}

	now := time.Now()
	s.SuccessRate = s.SuccessRate * (1 - ewmaAlpha)
	s.LastProbe = now
	s.ProbeCount++
	s.Score = computeScore(s.SuccessRate, s.RTTMs, now, s.LastProbe)

	rh.publishMetric(s)
}

// Score returns the health score for a relay. Returns defaultScore for unknown relays.
func (rh *RelayHealth) Score(peerID peer.ID) float64 {
	rh.mu.RLock()
	defer rh.mu.RUnlock()

	if s, ok := rh.relays[peerID]; ok {
		return s.Score
	}
	return defaultScore
}

// Ranked returns all relay health scores sorted highest-first.
func (rh *RelayHealth) Ranked() []RelayHealthScore {
	rh.mu.RLock()
	defer rh.mu.RUnlock()

	result := make([]RelayHealthScore, 0, len(rh.relays))
	for _, s := range rh.relays {
		result = append(result, *s)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Score > result[j].Score
	})
	return result
}

// ProbeAll pings all registered relays and updates their scores.
// Returns the number of successful probes.
func (rh *RelayHealth) ProbeAll(ctx context.Context) int {
	rh.mu.RLock()
	peerIDs := make([]peer.ID, 0, len(rh.relays))
	for pid := range rh.relays {
		peerIDs = append(peerIDs, pid)
	}
	rh.mu.RUnlock()

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		success int
	)

	for _, pid := range peerIDs {
		wg.Add(1)
		go func(pid peer.ID) {
			defer wg.Done()

			probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()

			start := time.Now()

			// Use libp2p's ping: connect + open stream
			if err := rh.host.Connect(probeCtx, peer.AddrInfo{ID: pid}); err != nil {
				rh.RecordFailure(pid)
				rh.recordProbeMetric("failure")
				return
			}

			rttMs := float64(time.Since(start).Milliseconds())
			rh.RecordSuccess(pid, rttMs)
			rh.recordProbeMetric("success")
			mu.Lock()
			success++
			mu.Unlock()
		}(pid)
	}

	wg.Wait()
	return success
}

// Start runs periodic health probes in the background.
func (rh *RelayHealth) Start(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		rh.ProbeAll(ctx)

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// computeScore calculates the composite health score.
// score = (success_rate * 0.6) + (latency_factor * 0.3) + (freshness * 0.1)
func computeScore(successRate, rttMs float64, now, lastProbe time.Time) float64 {
	// Latency factor: 1.0 for 0ms, 0.0 for >= 2000ms
	latencyFactor := 1.0 - math.Min(rttMs/2000.0, 1.0)

	// Freshness: 1.0 if probed recently, exponential decay
	freshness := 1.0
	if !lastProbe.IsZero() {
		age := now.Sub(lastProbe).Minutes()
		freshness = math.Exp(-age / 30.0) // half-life ~20 minutes
	}

	return successRate*0.6 + latencyFactor*0.3 + freshness*0.1
}

// publishMetric publishes a relay's health score to Prometheus.
func (rh *RelayHealth) publishMetric(s *RelayHealthScore) {
	if rh.metrics == nil {
		return
	}
	isStatic := "false"
	if s.IsStatic {
		isStatic = "true"
	}
	short := s.PeerID.String()
	if len(short) > 16 {
		short = short[:16]
	}
	rh.metrics.RelayHealthScore.WithLabelValues(short, isStatic).Set(s.Score)
}

// recordProbeMetric increments the probe counter.
func (rh *RelayHealth) recordProbeMetric(result string) {
	if rh.metrics == nil {
		return
	}
	rh.metrics.RelayProbeTotal.WithLabelValues(result).Inc()
}
