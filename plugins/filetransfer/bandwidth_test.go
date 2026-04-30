package filetransfer

import (
	"math"
	"testing"
)

func TestBandwidthBudgetTracker_PerPeerOverride(t *testing.T) {
	globalBudget := int64(100 * 1024 * 1024) // 100MB
	bt := newBandwidthTracker(globalBudget)

	peer1 := "12D3KooWTestPeer1AAAAAAAAAAAAAAA"
	peer2 := "12D3KooWTestPeer2BBBBBBBBBBBBBBB"

	// Global budget: 100MB file should pass.
	if !bt.check(peer1, 100*1024*1024, 0) {
		t.Error("100MB should fit in 100MB global budget")
	}

	// Record the transfer.
	bt.record(peer1, 100*1024*1024)

	// Same peer, global budget: now over budget.
	if bt.check(peer1, 1, 0) {
		t.Error("peer1 should be over global budget after 100MB")
	}

	// Same peer, with unlimited override: should pass.
	if !bt.check(peer1, 1, -1) {
		t.Error("unlimited override should always pass")
	}

	// Same peer, with 200MB per-peer override: should pass (100MB used of 200MB).
	if !bt.check(peer1, 50*1024*1024, 200*1024*1024) {
		t.Error("50MB should fit in 200MB per-peer budget with 100MB used")
	}

	// Same peer, with 200MB override: 100MB used + 150MB = 250MB > 200MB, should fail.
	if bt.check(peer1, 150*1024*1024, 200*1024*1024) {
		t.Error("150MB should NOT fit in 200MB per-peer budget with 100MB used")
	}

	// Different peer, global budget: should pass (no usage yet).
	if !bt.check(peer2, 50*1024*1024, 0) {
		t.Error("peer2 should have full global budget")
	}

	// Zero-size check with 0 override (global): should always pass.
	if !bt.check(peer2, 0, 0) {
		t.Error("zero-size transfer should always pass")
	}

	// Verify overflow edge: max int64 budget, should not overflow.
	if !bt.check(peer2, 1, math.MaxInt64) {
		t.Error("max int64 budget should accept 1 byte")
	}
}

func TestBandwidthBudgetTracker_UnlimitedGlobalWithPerPeerOverride(t *testing.T) {
	// Simulates: config bandwidth_budget = "unlimited" -> global budget = MaxInt64.
	// Per-peer override should still be enforced.
	bt := newBandwidthTracker(math.MaxInt64)

	peer1 := "12D3KooWTestPeer1AAAAAAAAAAAAAAA"

	// Global is effectively unlimited. 1TB should pass with global (0 = use global).
	if !bt.check(peer1, 1<<40, 0) {
		t.Error("1TB should pass with unlimited global budget")
	}

	// Per-peer override of 500MB should block 1GB transfer.
	if bt.check(peer1, 1<<30, 500*1024*1024) {
		t.Error("1GB should NOT fit in 500MB per-peer budget")
	}

	// Per-peer override of 500MB should allow 400MB transfer.
	if !bt.check(peer1, 400*1024*1024, 500*1024*1024) {
		t.Error("400MB should fit in 500MB per-peer budget")
	}

	// Record 400MB, then 200MB more should fail.
	bt.record(peer1, 400*1024*1024)
	if bt.check(peer1, 200*1024*1024, 500*1024*1024) {
		t.Error("200MB should NOT fit after 400MB used (500MB per-peer budget)")
	}

	// But unlimited per-peer (-1) should still pass.
	if !bt.check(peer1, 200*1024*1024, -1) {
		t.Error("unlimited per-peer override should always pass")
	}
}
