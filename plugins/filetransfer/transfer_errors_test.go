package filetransfer

import (
	"context"
	"fmt"
	"testing"
)

// TestClassifyTransferError is a table-driven test covering every matched string
// in the error classifier plus unknown errors (R4-F8). Safety-critical: misclassification
// = either silent retry loops or premature failure.
func TestClassifyTransferError(t *testing.T) {
	bgCtx := context.Background()

	tests := []struct {
		name     string
		err      error
		parentOK bool // true = parent context is alive, false = parent cancelled
		want     retryCategory
	}{
		// --- nil error ---
		{"nil error", nil, true, notRetryable},

		// --- Context errors ---
		{"context cancelled", context.Canceled, true, notRetryable},
		{"deadline exceeded, parent alive", context.DeadlineExceeded, true, retryableNetwork},
		{"deadline exceeded, parent cancelled", context.DeadlineExceeded, false, notRetryable},

		// --- Network errors (retryableNetwork) ---
		{"stream reset", fmt.Errorf("stream reset"), true, retryableNetwork},
		{"connection reset by peer", fmt.Errorf("connection reset by peer"), true, retryableNetwork},
		{"i/o timeout", fmt.Errorf("i/o timeout"), true, retryableNetwork},
		{"EOF", fmt.Errorf("EOF"), true, retryableNetwork},
		{"transfer incomplete: 5 chunks missing (first: 3)", fmt.Errorf("transfer incomplete: 5 chunks missing (first: 3)"), true, retryableNetwork},
		{"control read: stream reset", fmt.Errorf("control read: stream reset"), true, retryableNetwork},
		{"remote: busy", fmt.Errorf("remote: busy"), true, retryableNetwork},
		{"remote: rate limit exceeded", fmt.Errorf("remote: rate limit exceeded"), true, retryableNetwork},
		{"remote: internal error", fmt.Errorf("remote: internal error"), true, retryableNetwork},
		{"resource limit exceeded", fmt.Errorf("resource limit exceeded"), true, retryableNetwork},

		// --- Relay errors (retryableRelay) ---
		{"relay budget exhausted", fmt.Errorf("relay budget exhausted"), true, retryableRelay},
		{"relay session limit reached", fmt.Errorf("relay session limit reached"), true, retryableRelay},
		{"session expired", fmt.Errorf("session expired"), true, retryableRelay},

		// --- NOT retryable ---
		{"Merkle root mismatch", fmt.Errorf("Merkle root mismatch: transfer corrupted"), true, notRetryable},
		{"hash mismatch", fmt.Errorf("chunk hash mismatch at index 5"), true, notRetryable},
		{"access denied", fmt.Errorf("access denied"), true, notRetryable},
		{"permission denied", fmt.Errorf("permission denied: /etc/shadow"), true, notRetryable},
		{"remote: access denied", fmt.Errorf("remote: access denied"), true, notRetryable},
		{"remote: not found", fmt.Errorf("remote: not found"), true, notRetryable},
		{"grant expired", fmt.Errorf("grant expired for peer"), true, notRetryable},
		{"not authorized", fmt.Errorf("not authorized"), true, notRetryable},
		{"peer rejected transfer", fmt.Errorf("peer rejected transfer"), true, notRetryable},
		{"insufficient disk space", fmt.Errorf("insufficient disk space"), true, notRetryable},
		{"file too large", fmt.Errorf("file too large: 2TB"), true, notRetryable},
		{"finalize: rename error", fmt.Errorf("finalize: rename: permission denied"), true, notRetryable},
		{"cancelled string", fmt.Errorf("cancelled"), true, notRetryable},
		{"content changed on sender", fmt.Errorf("content changed on sender"), true, notRetryable},

		// --- Unknown errors default to NOT retryable (R3-F11) ---
		{"unknown weird error", fmt.Errorf("some completely unknown error xyz"), true, notRetryable},
		{"empty error message", fmt.Errorf(""), true, notRetryable},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var parentCtx context.Context
			if tt.parentOK {
				parentCtx = bgCtx
			} else {
				ctx, cancel := context.WithCancel(bgCtx)
				cancel() // simulate daemon shutdown
				parentCtx = ctx
			}

			got := classifyTransferError(tt.err, parentCtx)
			if got != tt.want {
				wantName := categoryName(tt.want)
				gotName := categoryName(got)
				t.Errorf("classifyTransferError(%q) = %s, want %s", tt.err, gotName, wantName)
			}
		})
	}
}

func categoryName(c retryCategory) string {
	switch c {
	case notRetryable:
		return "notRetryable"
	case retryableNetwork:
		return "retryableNetwork"
	case retryableRelay:
		return "retryableRelay"
	default:
		return fmt.Sprintf("unknown(%d)", c)
	}
}

// TestFailoverBackoff verifies the backoff schedule (F9).
func TestFailoverBackoff(t *testing.T) {
	tests := []struct {
		attempt int
		want    string
	}{
		{0, "500ms"},
		{1, "500ms"},
		{2, "2s"},
		{3, "5s"},
		{4, "5s"},
		{100, "5s"},
	}
	for _, tt := range tests {
		got := failoverBackoff(tt.attempt).String()
		if got != tt.want {
			t.Errorf("failoverBackoff(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

// TestQueuedJobTotalAttempts verifies the R5-F8 cap calculation.
func TestQueuedJobTotalAttempts(t *testing.T) {
	job := &queuedJob{
		retryCount:       3,
		relayReconnects:  2,
		failoverAttempts: 4,
	}
	if got := job.totalAttempts(); got != 9 {
		t.Errorf("totalAttempts() = %d, want 9", got)
	}
}

// TestReceivedBitfieldGetter verifies the R3-F2 getter returns a copy.
func TestReceivedBitfieldGetter(t *testing.T) {
	state := newStreamReceiveState([]fileEntry{{Path: "test.bin", Size: 1024}}, 1024, 0, []int64{0})
	state.initReceivedBitfield(10)

	// Set some bits.
	state.recordChunk(0, [32]byte{1}, 100)
	state.recordChunk(3, [32]byte{2}, 200)
	state.recordChunk(7, [32]byte{3}, 300)

	// Get copy.
	bf := state.ReceivedBitfield()
	if bf == nil {
		t.Fatal("ReceivedBitfield() returned nil")
	}
	if bf.count() != 3 {
		t.Errorf("ReceivedBitfield().count() = %d, want 3", bf.count())
	}
	if !bf.has(0) || !bf.has(3) || !bf.has(7) {
		t.Error("ReceivedBitfield missing expected bits")
	}
	if bf.has(1) || bf.has(5) {
		t.Error("ReceivedBitfield has unexpected bits")
	}

	// Verify it's a COPY: mutating the copy doesn't affect original.
	bf.set(9)
	original := state.ReceivedBitfield()
	if original.has(9) {
		t.Error("ReceivedBitfield() returned reference, not copy")
	}
}

// TestFailoversProgressField verifies the Failovers field on TransferProgress (F10).
func TestFailoversProgressField(t *testing.T) {
	p := &TransferProgress{}
	if p.Failovers != 0 {
		t.Errorf("initial Failovers = %d, want 0", p.Failovers)
	}
	p.Failovers = 3
	snap := p.Snapshot()
	if snap.Failovers != 3 {
		t.Errorf("snapshot Failovers = %d, want 3", snap.Failovers)
	}
}

// TestEventLogPathFailoverConstant verifies the event constant exists (F10).
func TestEventLogPathFailoverConstant(t *testing.T) {
	if EventLogPathFailover != "path_failover" {
		t.Errorf("EventLogPathFailover = %q, want %q", EventLogPathFailover, "path_failover")
	}
}
