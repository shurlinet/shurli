package reputation

import (
	"math"
	"time"
)

// ComputeScore returns a deterministic reputation score in the range [0, 100].
// The score is composed of four equally-weighted components (0-25 each):
//
//   - Availability (0-25): ConnectionCount normalized against maxConnections
//   - Latency (0-25): inverse of AvgLatencyMs (lower latency = higher score)
//   - PathDiversity (0-25): ratio of unique path types to total connections
//   - Tenure (0-25): days since FirstSeen, capped at 365 days for full credit
//
// Parameters:
//   - record: the peer's interaction history (nil returns 0)
//   - maxConnections: the normalization ceiling for availability (must be > 0)
//   - now: current time (for tenure calculation)
//
// The score is deterministic: same inputs always produce the same output.
// This is critical for ZKP range proofs where the verifier must be able
// to independently compute (or commit to) the same score.
func ComputeScore(record *PeerRecord, maxConnections int, now time.Time) int {
	if record == nil || maxConnections <= 0 {
		return 0
	}

	avail := availabilityScore(record.ConnectionCount, maxConnections)
	latency := latencyScore(record.AvgLatencyMs)
	diversity := pathDiversityScore(record.PathTypes)
	tenure := tenureScore(record.FirstSeen, now)

	total := avail + latency + diversity + tenure

	// Clamp to [0, 100] (shouldn't exceed 100 with 4x25, but defensive).
	if total > 100 {
		return 100
	}
	if total < 0 {
		return 0
	}
	return total
}

// availabilityScore returns 0-25 based on connection count relative to max.
// Linear scaling: 0 connections = 0, maxConnections or more = 25.
func availabilityScore(connections, maxConnections int) int {
	if connections <= 0 {
		return 0
	}
	ratio := float64(connections) / float64(maxConnections)
	if ratio > 1.0 {
		ratio = 1.0
	}
	return int(math.Round(ratio * 25))
}

// latencyScore returns 0-25 based on average latency.
// Lower latency = higher score. Uses a decay curve:
//
//	<=10ms  = 25 (excellent)
//	~50ms   = ~22
//	~100ms  = ~20
//	~500ms  = ~10
//	~1000ms = ~5
//	>=5000ms = 0
func latencyScore(avgLatencyMs float64) int {
	if avgLatencyMs <= 0 {
		return 0 // no latency data
	}
	if avgLatencyMs <= 10 {
		return 25
	}
	if avgLatencyMs >= 5000 {
		return 0
	}

	// Logarithmic decay: score = 25 * (1 - log10(latency/10) / log10(500))
	// Maps [10ms, 5000ms] -> [25, 0]
	ratio := math.Log10(avgLatencyMs/10) / math.Log10(500)
	score := 25.0 * (1.0 - ratio)
	return clamp(int(math.Round(score)), 0, 25)
}

// pathDiversityScore returns 0-25 based on unique path types used.
// More diverse connectivity (direct, relay, mDNS) scores higher.
//
//	0 types = 0
//	1 type  = 8
//	2 types = 16
//	3+ types = 25
func pathDiversityScore(pathTypes map[string]int) int {
	n := len(pathTypes)
	switch {
	case n <= 0:
		return 0
	case n == 1:
		return 8
	case n == 2:
		return 16
	default:
		return 25
	}
}

// tenureScore returns 0-25 based on how long the peer has been known.
// Linear scaling: 0 days = 0, 365+ days = 25.
func tenureScore(firstSeen time.Time, now time.Time) int {
	if firstSeen.IsZero() || now.Before(firstSeen) {
		return 0
	}
	days := now.Sub(firstSeen).Hours() / 24
	if days >= 365 {
		return 25
	}
	return int(math.Round(days / 365.0 * 25.0))
}

// clamp restricts v to [lo, hi].
func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
