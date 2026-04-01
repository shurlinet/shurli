package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestRelayServiceCID_Deterministic(t *testing.T) {
	c1 := RelayServiceCID("")
	c2 := RelayServiceCID("")
	if c1 != c2 {
		t.Errorf("CID should be deterministic: %s != %s", c1, c2)
	}
}

func TestRelayServiceCID_NamespaceIsolation(t *testing.T) {
	global := RelayServiceCID("")
	private := RelayServiceCID("mynet")
	if global == private {
		t.Error("different namespaces should produce different CIDs")
	}

	// Two different namespaces should also differ
	net1 := RelayServiceCID("net1")
	net2 := RelayServiceCID("net2")
	if net1 == net2 {
		t.Error("net1 and net2 should produce different CIDs")
	}
}

func TestStaticRelaySource(t *testing.T) {
	s := &StaticRelaySource{Addrs: []string{"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"}}
	addrs := s.RelayAddrs()
	if len(addrs) != 1 {
		t.Fatalf("expected 1 addr, got %d", len(addrs))
	}
}

func TestStaticRelaySource_Empty(t *testing.T) {
	s := &StaticRelaySource{}
	addrs := s.RelayAddrs()
	if len(addrs) != 0 {
		t.Errorf("expected 0 addrs, got %d", len(addrs))
	}
}

func TestRelayDiscovery_NilDHT(t *testing.T) {
	rd := NewRelayDiscovery(nil, "", nil)

	// Discover with nil DHT should return nil
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peers := rd.Discover(ctx, 5)
	if peers != nil {
		t.Errorf("expected nil from Discover with nil DHT, got %d peers", len(peers))
	}

	// AllRelays with no statics and no DHT
	all := rd.AllRelays()
	if len(all) != 0 {
		t.Errorf("expected 0 relays, got %d", len(all))
	}

	// RelayAddrs with no relays
	addrs := rd.RelayAddrs()
	if len(addrs) != 0 {
		t.Errorf("expected 0 relay addrs, got %d", len(addrs))
	}
}

func TestRelayDiscovery_StaticRelaysFirst(t *testing.T) {
	// Create two static relay AddrInfos
	static1, err := peer.AddrInfoFromString("/ip4/203.0.113.1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse static1: %v", err)
	}
	static2, err := peer.AddrInfoFromString("/ip4/203.0.113.2/tcp/7777/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")
	if err != nil {
		t.Fatalf("parse static2: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*static1, *static2}, "", nil)

	all := rd.AllRelays()
	if len(all) != 2 {
		t.Fatalf("expected 2 relays, got %d", len(all))
	}
	if all[0].ID != static1.ID {
		t.Error("first relay should be static1")
	}
	if all[1].ID != static2.ID {
		t.Error("second relay should be static2")
	}
}

func TestRelayDiscovery_RelayAddrsFormat(t *testing.T) {
	ai, err := peer.AddrInfoFromString("/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*ai}, "", nil)
	addrs := rd.RelayAddrs()
	if len(addrs) != 1 {
		t.Fatalf("expected 1 addr, got %d", len(addrs))
	}
	// Should contain both the multiaddr and the peer ID
	expected := "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	if addrs[0] != expected {
		t.Errorf("unexpected addr format:\n  got:  %s\n  want: %s", addrs[0], expected)
	}
}

func TestRelayDiscovery_PeerSourceClosesChannel(t *testing.T) {
	ai, err := peer.AddrInfoFromString("/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	rd := NewRelayDiscovery([]peer.AddrInfo{*ai}, "", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ch := rd.PeerSource(ctx, 10)

	// Should get exactly 1 peer then channel closes
	var count int
	for range ch {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 peer from PeerSource, got %d", count)
	}
}

func TestRelayDiscovery_SetHostSetDHT(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	rd := NewRelayDiscovery(nil, "", nil)

	// SetHost and SetDHT should not panic with valid/nil values
	rd.SetHost(h)
	rd.SetDHT(nil) // nil DHT is valid

	// Discover should still return nil with nil DHT
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	peers := rd.Discover(ctx, 5)
	if peers != nil {
		t.Errorf("expected nil, got %d peers", len(peers))
	}
}

func TestRelayDiscovery_NilMetricsSafety(t *testing.T) {
	rd := NewRelayDiscovery(nil, "test", nil)
	// None of these should panic with nil metrics
	rd.AllRelays()
	rd.RelayAddrs()
}

// mockBudgetChecker implements RelayGrantChecker for testing budget-aware relay ranking.
type mockBudgetChecker struct {
	grants map[peer.ID]*mockGrant
}

func newMockBudgetChecker() *mockBudgetChecker {
	return &mockBudgetChecker{grants: make(map[peer.ID]*mockGrant)}
}

func (m *mockBudgetChecker) GrantStatus(relayID peer.ID) (time.Duration, int64, time.Duration, bool) {
	g, ok := m.grants[relayID]
	if !ok {
		return 0, 0, 0, false
	}
	return g.remaining, g.budget, g.sessionDuration, true
}

func (m *mockBudgetChecker) HasSufficientBudget(relayID peer.ID, fileSize int64, _ string) bool {
	g, ok := m.grants[relayID]
	if !ok {
		return false
	}
	return g.budget >= fileSize
}

func (m *mockBudgetChecker) TrackCircuitBytes(_ peer.ID, _ string, _ int64) {}
func (m *mockBudgetChecker) ResetCircuitCounters(_ peer.ID)                {}

func TestRelayDiscovery_BudgetAwareRanking(t *testing.T) {
	// Two relays: seed (64MB budget) and self-hosted (2GB budget).
	// Budget-aware ranking should put the 2GB relay first.
	seed, err := peer.AddrInfoFromString("/ip4/203.0.113.1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse seed: %v", err)
	}
	selfHosted, err := peer.AddrInfoFromString("/ip4/203.0.113.2/tcp/7777/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")
	if err != nil {
		t.Fatalf("parse self-hosted: %v", err)
	}

	// Seed relay first in static list (would normally be tried first).
	rd := NewRelayDiscovery([]peer.AddrInfo{*seed, *selfHosted}, "", nil)

	bc := newMockBudgetChecker()
	bc.grants[seed.ID] = &mockGrant{
		remaining: 2 * time.Hour,
		budget:    64 << 20, // 64 MB
	}
	bc.grants[selfHosted.ID] = &mockGrant{
		remaining: 24 * time.Hour,
		budget:    2 << 30, // 2 GB
	}
	rd.SetBudgetChecker(bc)

	addrs := rd.RelayAddrs()
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addrs, got %d", len(addrs))
	}
	// Self-hosted (2GB) should be ranked first due to higher budget.
	if addrs[0] != "/ip4/203.0.113.2/tcp/7777/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo" {
		t.Errorf("expected self-hosted relay first, got: %s", addrs[0])
	}
}

func TestRelayDiscovery_BudgetAwareRanking_NoGrant(t *testing.T) {
	// Relay with grant should rank above relay without grant.
	withGrant, err := peer.AddrInfoFromString("/ip4/203.0.113.1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	noGrant, err := peer.AddrInfoFromString("/ip4/203.0.113.2/tcp/7777/p2p/12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// No-grant relay first in static list.
	rd := NewRelayDiscovery([]peer.AddrInfo{*noGrant, *withGrant}, "", nil)

	bc := newMockBudgetChecker()
	bc.grants[withGrant.ID] = &mockGrant{
		remaining: 1 * time.Hour,
		budget:    500 << 20, // 500 MB
	}
	// noGrant has no entry = no active grant.
	rd.SetBudgetChecker(bc)

	addrs := rd.RelayAddrs()
	if len(addrs) != 2 {
		t.Fatalf("expected 2 addrs, got %d", len(addrs))
	}
	// With-grant relay should be first.
	if addrs[0] != "/ip4/203.0.113.1/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN" {
		t.Errorf("expected with-grant relay first, got: %s", addrs[0])
	}
}

func TestRelayBudgetScore(t *testing.T) {
	bc := newMockBudgetChecker()
	pid1, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	pid2, _ := peer.Decode("12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")

	// No grant = 0.0
	score := relayBudgetScore(pid1, bc)
	if score != 0.0 {
		t.Errorf("no grant score: got %f, want 0.0", score)
	}

	// 2GB grant = 1.0
	bc.grants[pid1] = &mockGrant{remaining: 1 * time.Hour, budget: 2 << 30}
	score = relayBudgetScore(pid1, bc)
	if score != 1.0 {
		t.Errorf("2GB grant score: got %f, want 1.0", score)
	}

	// 64MB grant = ~0.03 (64MB / 2GB)
	bc.grants[pid2] = &mockGrant{remaining: 1 * time.Hour, budget: 64 << 20}
	score = relayBudgetScore(pid2, bc)
	expected := float64(64<<20) / float64(2<<30)
	if score < expected-0.01 || score > expected+0.01 {
		t.Errorf("64MB grant score: got %f, want ~%f", score, expected)
	}
}

func TestRelayScore_NoGrantUsesHealthOnly(t *testing.T) {
	// Relay without a grant must score on pure health, not be penalized.
	rd := NewRelayDiscovery(nil, "", nil)
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	bc := newMockBudgetChecker()
	// No grant for pid → budgetScore = 0.0
	// relayScore should return pure health (defaultScore=0.5 with nil health).
	score := rd.relayScore(pid, nil, bc)
	if score != defaultScore {
		t.Errorf("no-grant score: got %f, want %f (defaultScore)", score, defaultScore)
	}
}

func TestRelayScore_WithGrantGetsBonus(t *testing.T) {
	rd := NewRelayDiscovery(nil, "", nil)
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")

	bc := newMockBudgetChecker()
	bc.grants[pid] = &mockGrant{remaining: 1 * time.Hour, budget: 2 << 30} // 2GB → bs=1.0

	// With grant: score = health + bs*0.5 = 0.5 + 1.0*0.5 = 1.0
	score := rd.relayScore(pid, nil, bc)
	expected := defaultScore + 1.0*0.5
	if score < expected-0.01 || score > expected+0.01 {
		t.Errorf("with-grant score: got %f, want %f", score, expected)
	}
}

func TestRelayScore_RankInversionPrevented(t *testing.T) {
	// Healthy relay without grant must rank ABOVE failing relay with grant.
	// This was the round 4 fix: old formula (health*0.4 + budget*0.6) caused
	// failing-with-grant (0.68) to outrank healthy-no-grant (0.38).
	rd := NewRelayDiscovery(nil, "", nil)

	healthyPid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	failingPid, _ := peer.Decode("12D3KooWQYhTNQdmr3ArTeUHRYzFg94BKyTkoWBDWez9kSCVe4Xo")

	// Mock health: healthy=0.95, failing=0.2
	health := NewRelayHealth(nil, nil)
	health.RegisterRelay(healthyPid, true)
	health.RegisterRelay(failingPid, true)
	// Set scores via direct manipulation of the relay map.
	health.mu.Lock()
	health.relays[healthyPid].Score = 0.95
	health.relays[healthyPid].SuccessRate = 0.95
	health.relays[failingPid].Score = 0.2
	health.relays[failingPid].SuccessRate = 0.2
	health.mu.Unlock()

	bc := newMockBudgetChecker()
	// Only failing relay has a grant (2GB).
	bc.grants[failingPid] = &mockGrant{remaining: 1 * time.Hour, budget: 2 << 30}

	healthyScore := rd.relayScore(healthyPid, health, bc)
	failingScore := rd.relayScore(failingPid, health, bc)

	// Healthy no-grant: score = 0.95 (pure health, no penalty)
	// Failing with-grant: score = 0.2 + 1.0*0.5 = 0.7
	// 0.95 > 0.7 → healthy relay ranks first
	if healthyScore <= failingScore {
		t.Errorf("rank inversion: healthy no-grant (%f) should rank above failing with-grant (%f)",
			healthyScore, failingScore)
	}
}

func TestRelayDiscovery_AdvertiseNilDHT(t *testing.T) {
	rd := NewRelayDiscovery(nil, "", nil)

	// Advertise with nil DHT should return immediately
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		rd.Advertise(ctx, time.Hour)
		close(done)
	}()

	select {
	case <-done:
		// Good - returned immediately because no DHT
	case <-time.After(2 * time.Second):
		t.Error("Advertise with nil DHT should return immediately")
	}
}
