package reputation

import (
	"testing"
	"time"
)

func TestComputeScore_NilRecord(t *testing.T) {
	if got := ComputeScore(nil, 100, time.Now()); got != 0 {
		t.Fatalf("nil record should score 0, got %d", got)
	}
}

func TestComputeScore_ZeroMaxConnections(t *testing.T) {
	r := &PeerRecord{ConnectionCount: 10}
	if got := ComputeScore(r, 0, time.Now()); got != 0 {
		t.Fatalf("zero maxConnections should score 0, got %d", got)
	}
}

func TestComputeScore_ZeroEverything(t *testing.T) {
	r := &PeerRecord{}
	if got := ComputeScore(r, 100, time.Now()); got != 0 {
		t.Fatalf("zero-value record should score 0, got %d", got)
	}
}

func TestComputeScore_MaxEverything(t *testing.T) {
	now := time.Now()
	r := &PeerRecord{
		ConnectionCount: 200,
		AvgLatencyMs:    5.0,
		PathTypes:       map[string]int{"direct": 10, "relay": 5, "mdns": 3},
		FirstSeen:       now.Add(-400 * 24 * time.Hour), // 400 days ago
	}

	got := ComputeScore(r, 100, now)
	if got != 100 {
		t.Fatalf("max-everything should score 100, got %d", got)
	}
}

func TestComputeScore_Deterministic(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	r := &PeerRecord{
		ConnectionCount: 50,
		AvgLatencyMs:    100.0,
		PathTypes:       map[string]int{"direct": 30, "relay": 20},
		FirstSeen:       now.Add(-180 * 24 * time.Hour),
	}

	score1 := ComputeScore(r, 100, now)
	score2 := ComputeScore(r, 100, now)
	if score1 != score2 {
		t.Fatalf("determinism: got %d and %d", score1, score2)
	}
}

func TestAvailabilityScore(t *testing.T) {
	tests := []struct {
		conns, max int
		want       int
	}{
		{0, 100, 0},
		{50, 100, 13},  // 50% = round(12.5) = 13
		{100, 100, 25}, // 100% = 25
		{200, 100, 25}, // capped at 25
	}
	for _, tt := range tests {
		got := availabilityScore(tt.conns, tt.max)
		if got != tt.want {
			t.Errorf("availabilityScore(%d, %d) = %d, want %d", tt.conns, tt.max, got, tt.want)
		}
	}
}

func TestLatencyScore(t *testing.T) {
	tests := []struct {
		latency float64
		min     int // at least this score
		max     int // at most this score
	}{
		{0, 0, 0},       // no data
		{5, 25, 25},     // excellent
		{10, 25, 25},    // boundary
		{50, 18, 21},    // good
		{100, 14, 18},   // decent
		{500, 6, 12},    // mediocre
		{1000, 2, 7},    // poor
		{5000, 0, 0},    // boundary
		{10000, 0, 0},   // terrible
	}
	for _, tt := range tests {
		got := latencyScore(tt.latency)
		if got < tt.min || got > tt.max {
			t.Errorf("latencyScore(%v) = %d, want [%d, %d]", tt.latency, got, tt.min, tt.max)
		}
	}
}

func TestPathDiversityScore(t *testing.T) {
	tests := []struct {
		name  string
		paths map[string]int
		want  int
	}{
		{"nil", nil, 0},
		{"empty", map[string]int{}, 0},
		{"one", map[string]int{"direct": 5}, 8},
		{"two", map[string]int{"direct": 5, "relay": 3}, 16},
		{"three", map[string]int{"direct": 5, "relay": 3, "mdns": 1}, 25},
		{"four", map[string]int{"direct": 5, "relay": 3, "mdns": 1, "other": 1}, 25},
	}
	for _, tt := range tests {
		got := pathDiversityScore(tt.paths)
		if got != tt.want {
			t.Errorf("%s: pathDiversityScore = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestTenureScore(t *testing.T) {
	now := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		days float64
		want int
	}{
		{"zero", 0, 0},
		{"one_day", 1, 0},     // 1/365 * 25 = 0.07, rounds to 0
		{"one_month", 30, 2},  // 30/365 * 25 = 2.05, rounds to 2
		{"half_year", 183, 13}, // 183/365 * 25 = 12.53, rounds to 13
		{"one_year", 365, 25},
		{"two_years", 730, 25}, // capped
	}
	for _, tt := range tests {
		firstSeen := now.Add(-time.Duration(tt.days*24) * time.Hour)
		got := tenureScore(firstSeen, now)
		if got != tt.want {
			t.Errorf("%s: tenureScore = %d, want %d", tt.name, got, tt.want)
		}
	}
}

func TestTenureScore_FutureFirstSeen(t *testing.T) {
	now := time.Now()
	future := now.Add(24 * time.Hour)
	if got := tenureScore(future, now); got != 0 {
		t.Fatalf("future FirstSeen should score 0, got %d", got)
	}
}

func TestTenureScore_ZeroFirstSeen(t *testing.T) {
	if got := tenureScore(time.Time{}, time.Now()); got != 0 {
		t.Fatalf("zero FirstSeen should score 0, got %d", got)
	}
}

func TestComputeScore_MidRange(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	r := &PeerRecord{
		ConnectionCount: 25,
		AvgLatencyMs:    200.0,
		PathTypes:       map[string]int{"relay": 25},
		FirstSeen:       now.Add(-90 * 24 * time.Hour),
	}

	got := ComputeScore(r, 100, now)
	// availability: 25/100 = 6.25 -> 6
	// latency: ~200ms -> somewhere around 14-17
	// diversity: 1 type -> 8
	// tenure: 90/365 * 25 -> 6.16 -> 6
	// total: ~34-37
	if got < 25 || got > 45 {
		t.Fatalf("mid-range score %d outside expected [25, 45]", got)
	}
	t.Logf("Mid-range score: %d", got)
}
