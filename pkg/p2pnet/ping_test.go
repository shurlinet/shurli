package p2pnet

import (
	"context"
	"testing"
	"time"
)

func TestComputePingStats(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		stats := ComputePingStats(nil)
		if stats.Sent != 0 || stats.Received != 0 || stats.Lost != 0 {
			t.Errorf("empty stats: %+v", stats)
		}
	})

	t.Run("all success", func(t *testing.T) {
		results := []PingResult{
			{Seq: 1, RttMs: 10.0, Path: "DIRECT"},
			{Seq: 2, RttMs: 20.0, Path: "DIRECT"},
			{Seq: 3, RttMs: 5.0, Path: "DIRECT"},
		}
		stats := ComputePingStats(results)
		if stats.Sent != 3 {
			t.Errorf("Sent = %d, want 3", stats.Sent)
		}
		if stats.Received != 3 {
			t.Errorf("Received = %d, want 3", stats.Received)
		}
		if stats.Lost != 0 {
			t.Errorf("Lost = %d, want 0", stats.Lost)
		}
		if stats.LossPct != 0 {
			t.Errorf("LossPct = %f, want 0", stats.LossPct)
		}
		if stats.MinMs != 5.0 {
			t.Errorf("MinMs = %f, want 5.0", stats.MinMs)
		}
		if stats.MaxMs != 20.0 {
			t.Errorf("MaxMs = %f, want 20.0", stats.MaxMs)
		}
		wantAvg := (10.0 + 20.0 + 5.0) / 3.0
		if stats.AvgMs != wantAvg {
			t.Errorf("AvgMs = %f, want %f", stats.AvgMs, wantAvg)
		}
	})

	t.Run("all errors", func(t *testing.T) {
		results := []PingResult{
			{Seq: 1, Error: "timeout"},
			{Seq: 2, Error: "stream reset"},
		}
		stats := ComputePingStats(results)
		if stats.Sent != 2 {
			t.Errorf("Sent = %d, want 2", stats.Sent)
		}
		if stats.Received != 0 {
			t.Errorf("Received = %d, want 0", stats.Received)
		}
		if stats.Lost != 2 {
			t.Errorf("Lost = %d, want 2", stats.Lost)
		}
		if stats.LossPct != 100 {
			t.Errorf("LossPct = %f, want 100", stats.LossPct)
		}
	})

	t.Run("mixed", func(t *testing.T) {
		results := []PingResult{
			{Seq: 1, RttMs: 15.0},
			{Seq: 2, Error: "timeout"},
			{Seq: 3, RttMs: 25.0},
			{Seq: 4, Error: "stream reset"},
		}
		stats := ComputePingStats(results)
		if stats.Sent != 4 {
			t.Errorf("Sent = %d, want 4", stats.Sent)
		}
		if stats.Received != 2 {
			t.Errorf("Received = %d, want 2", stats.Received)
		}
		if stats.Lost != 2 {
			t.Errorf("Lost = %d, want 2", stats.Lost)
		}
		if stats.LossPct != 50 {
			t.Errorf("LossPct = %f, want 50", stats.LossPct)
		}
		if stats.MinMs != 15.0 {
			t.Errorf("MinMs = %f, want 15.0", stats.MinMs)
		}
		if stats.MaxMs != 25.0 {
			t.Errorf("MaxMs = %f, want 25.0", stats.MaxMs)
		}
	})

	t.Run("single success", func(t *testing.T) {
		results := []PingResult{
			{Seq: 1, RttMs: 42.0},
		}
		stats := ComputePingStats(results)
		if stats.MinMs != 42.0 || stats.MaxMs != 42.0 || stats.AvgMs != 42.0 {
			t.Errorf("single: min=%f max=%f avg=%f", stats.MinMs, stats.MaxMs, stats.AvgMs)
		}
	})
}

func TestPingPeer_ContextCancelled(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	// Cancel immediately  - PingPeer should return quickly
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	ch := PingPeer(ctx, netA.Host(), netB.Host().ID(), "/peerup/ping/1.0.0", 0, time.Second)

	// Channel should close without hanging
	count := 0
	for range ch {
		count++
	}
	// With cancelled context, we may get 0 or 1 results
	if count > 1 {
		t.Errorf("expected at most 1 result with cancelled context, got %d", count)
	}
}

func TestPingPeer_CountedPings(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	// No ping handler on B, so all pings will error  - but we still test the flow
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := PingPeer(ctx, netA.Host(), netB.Host().ID(), "/peerup/ping/1.0.0", 2, 100*time.Millisecond)

	var results []PingResult
	for r := range ch {
		results = append(results, r)
	}

	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Seq != i+1 {
			t.Errorf("result[%d].Seq = %d, want %d", i, r.Seq, i+1)
		}
	}
}
