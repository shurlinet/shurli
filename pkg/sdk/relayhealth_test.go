package sdk

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func mustPeerID(s string) peer.ID {
	pid, err := peer.Decode(s)
	if err != nil {
		panic(err)
	}
	return pid
}

// Two distinct test peer IDs (generated, not from real nodes).
var (
	testPeer1 = mustPeerID("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	testPeer2 = mustPeerID("12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")
)

// timeNow returns time.Now() for use in test score calculations.
func timeNow() time.Time { return time.Now() }

func TestRelayHealth_NilSafety(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, true)
	rh.RecordSuccess(testPeer1, 50)
	rh.RecordFailure(testPeer1)
	_ = rh.Score(testPeer1)
	_ = rh.Ranked()
}

func TestRelayHealth_DefaultScore(t *testing.T) {
	rh := NewRelayHealth(nil, nil)

	// Unknown peer should return default score
	score := rh.Score(testPeer1)
	if score != defaultScore {
		t.Errorf("unknown peer score = %f, want %f", score, defaultScore)
	}

	// Registered but unprobed peer should start above zero
	rh.RegisterRelay(testPeer1, true)
	score = rh.Score(testPeer1)
	if score == 0 {
		t.Error("registered peer should not have zero score")
	}
}

func TestRelayHealth_EWMAConvergence(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, false)

	for i := 0; i < 20; i++ {
		rh.RecordSuccess(testPeer1, 50)
	}

	score := rh.Score(testPeer1)
	if score < 0.85 {
		t.Errorf("after 20 successes at 50ms, score should be near 1.0, got %f", score)
	}
}

func TestRelayHealth_EWMADegradation(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, false)

	for i := 0; i < 10; i++ {
		rh.RecordSuccess(testPeer1, 50)
	}
	goodScore := rh.Score(testPeer1)

	for i := 0; i < 10; i++ {
		rh.RecordFailure(testPeer1)
	}
	badScore := rh.Score(testPeer1)

	if badScore >= goodScore {
		t.Errorf("score should degrade after failures: good=%f bad=%f", goodScore, badScore)
	}
	if badScore > 0.5 {
		t.Errorf("score after 10 failures should be below 0.5, got %f", badScore)
	}
}

func TestRelayHealth_Ranked(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, true)
	rh.RegisterRelay(testPeer2, false)

	for i := 0; i < 10; i++ {
		rh.RecordSuccess(testPeer1, 30)
		rh.RecordFailure(testPeer2)
	}

	ranked := rh.Ranked()
	if len(ranked) != 2 {
		t.Fatalf("expected 2 relays, got %d", len(ranked))
	}
	if ranked[0].PeerID != testPeer1 {
		t.Error("peer1 should be ranked first (healthier)")
	}
	if ranked[1].PeerID != testPeer2 {
		t.Error("peer2 should be ranked second (degraded)")
	}
	if ranked[0].Score <= ranked[1].Score {
		t.Errorf("peer1 score (%f) should be > peer2 score (%f)", ranked[0].Score, ranked[1].Score)
	}
}

func TestRelayHealth_RegisterIdempotent(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, true)

	for i := 0; i < 5; i++ {
		rh.RecordSuccess(testPeer1, 40)
	}
	score := rh.Score(testPeer1)

	// Re-register should not reset score
	rh.RegisterRelay(testPeer1, true)
	if rh.Score(testPeer1) != score {
		t.Error("re-register should not reset score")
	}
}

func TestRelayHealth_RecordUnknownPeer(t *testing.T) {
	rh := NewRelayHealth(nil, nil)

	// Recording for unregistered peers should not panic
	rh.RecordSuccess(testPeer1, 100)
	rh.RecordFailure(testPeer2)

	if rh.Score(testPeer1) != defaultScore {
		t.Error("unregistered peer should return default score")
	}
}

func TestRelayHealth_HighLatencyLowScore(t *testing.T) {
	rh := NewRelayHealth(nil, nil)
	rh.RegisterRelay(testPeer1, false)
	rh.RegisterRelay(testPeer2, false)

	for i := 0; i < 15; i++ {
		rh.RecordSuccess(testPeer1, 30)
		rh.RecordSuccess(testPeer2, 1500)
	}

	ranked := rh.Ranked()
	if ranked[0].PeerID != testPeer1 {
		t.Error("low-latency peer should rank higher")
	}
}

func TestComputeScore_Bounds(t *testing.T) {
	now := timeNow()

	// Perfect conditions
	score := computeScore(1.0, 0, now, now)
	if score > 1.0 || score < 0.9 {
		t.Errorf("perfect score should be near 1.0, got %f", score)
	}

	// Worst conditions
	score = computeScore(0.0, 2000, now, now)
	if score > 0.15 {
		t.Errorf("worst score should be near 0.0, got %f", score)
	}

	// Score should always be in [0, 1]
	for _, sr := range []float64{0, 0.5, 1.0} {
		for _, rtt := range []float64{0, 100, 500, 2000, 5000} {
			s := computeScore(sr, rtt, now, now)
			if s < 0 || s > 1.01 {
				t.Errorf("score out of bounds: sr=%f rtt=%f -> %f", sr, rtt, s)
			}
		}
	}
}

func TestRelayHealth_WithMetrics(t *testing.T) {
	m := NewMetrics("test", "go1.26")
	rh := NewRelayHealth(nil, m)
	rh.RegisterRelay(testPeer1, true)

	// These should not panic with metrics enabled
	rh.RecordSuccess(testPeer1, 42)
	rh.RecordFailure(testPeer1)
}
