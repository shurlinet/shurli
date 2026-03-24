package p2pnet

import (
	"math"
	"testing"
)

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		// Valid sizes.
		{"100", 100, false},
		{"100B", 100, false},
		{"1KB", 1024, false},
		{"1K", 1024, false},
		{"1MB", 1 << 20, false},
		{"1M", 1 << 20, false},
		{"500MB", 500 * (1 << 20), false},
		{"1GB", 1 << 30, false},
		{"1G", 1 << 30, false},
		{"1TB", 1 << 40, false},
		{"1T", 1 << 40, false},
		{"0", 0, false},
		{"0MB", 0, false},

		// Case insensitive.
		{"1kb", 1024, false},
		{"1mb", 1 << 20, false},
		{"1gb", 1 << 30, false},
		{"500Mb", 500 * (1 << 20), false},

		// Whitespace handling.
		{"  1GB  ", 1 << 30, false},
		{"1 GB", 1 << 30, false},

		// Unlimited.
		{"unlimited", -1, false},
		{"UNLIMITED", -1, false},
		{"Unlimited", -1, false},

		// Errors.
		{"", 0, true},          // empty
		{"GB", 0, true},        // no number
		{"abc", 0, true},       // no number
		{"-5MB", 0, true},      // negative (dash not in digit set)
		{"1.5GB", 0, true},     // dot not in digit set, "1" parsed, ".5GB" is unknown suffix
		{"1XB", 0, true},       // unknown suffix
		{"1 2 MB", 0, true},    // multiple numbers
	}

	for _, tt := range tests {
		got, err := ParseByteSize(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseByteSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseByteSize_Overflow(t *testing.T) {
	// int64 max is ~9.2 exabytes. 9999999TB would overflow.
	huge := "9999999TB"
	_, err := ParseByteSize(huge)
	if err == nil {
		t.Errorf("ParseByteSize(%q) should overflow, got no error", huge)
	}
}

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
