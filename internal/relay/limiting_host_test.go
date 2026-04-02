package relay

import (
	"io"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p/core/protocol"
)

func TestLimitedStreamLocalBudgetExhaustion(t *testing.T) {
	// Create a local-mode limitedStream with 100 bytes budget.
	budget := int64(100)
	ls := &limitedStream{
		remaining: &atomic.Int64{},
		budget:    budget,
	}
	ls.remaining.Store(budget)

	// Simulate consuming 60 bytes via Write.
	ls.consumeBytes(60)
	if ls.budgetRemaining() != 40 {
		t.Fatalf("expected 40 remaining, got %d", ls.budgetRemaining())
	}

	// Consume 40 more.
	ls.consumeBytes(40)
	if ls.budgetRemaining() != 0 {
		t.Fatalf("expected 0 remaining, got %d", ls.budgetRemaining())
	}

	// Read should return io.EOF when budget exhausted (C7).
	_, err := ls.Read(make([]byte, 10))
	if err != io.EOF {
		t.Fatalf("expected io.EOF on exhausted Read, got %v", err)
	}

	// Write should return io.ErrClosedPipe when budget exhausted (C7).
	_, err = ls.Write(make([]byte, 10))
	if err != io.ErrClosedPipe {
		t.Fatalf("expected io.ErrClosedPipe on exhausted Write, got %v", err)
	}
}

func TestLimitedStreamCumulativeMode(t *testing.T) {
	// Create a tracker with known budget.
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	g, err := gs.Grant(pid, 60_000_000_000, nil, false, 0, 1000) // 1000 bytes
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	// Create cumulative limitedStream.
	ls := newLimitedStreamCumulative(nil, pid, bt)

	// Consume 600 bytes.
	ls.consumeBytes(600)
	if bt.RemainingBudget(pid) != 400 {
		t.Fatalf("expected 400 remaining in tracker, got %d", bt.RemainingBudget(pid))
	}

	// Consume 400 more — exhausted.
	ls.consumeBytes(400)
	if bt.RemainingBudget(pid) != 0 {
		t.Fatalf("expected 0 remaining, got %d", bt.RemainingBudget(pid))
	}

	// Budget check should return 0.
	if ls.budgetRemaining() != 0 {
		t.Fatalf("expected 0 from budgetRemaining, got %d", ls.budgetRemaining())
	}
}

func TestLimitedStreamOvershootBounded(t *testing.T) {
	// SEC3: concurrent overshoot is bounded. Test that negative remaining
	// is handled correctly (no wrap-around).
	bt, gs := newTestBudgetTracker(t)
	pid := genTestPeerID(t)

	g, err := gs.Grant(pid, 60_000_000_000, nil, false, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	bt.OnGrantOrExtend(pid, g)

	// Consume 150 bytes (simulates overshoot from concurrent goroutines).
	bt.ConsumeBytes(pid, 150)
	remaining := bt.RemainingBudget(pid)
	if remaining != 0 {
		t.Fatalf("negative remaining should return 0, got %d", remaining)
	}
}

func TestIsStopProtocol(t *testing.T) {
	tests := []struct {
		name   string
		pids   []string
		expect bool
	}{
		{"stop only", []string{"/libp2p/circuit/relay/0.2.0/stop"}, true},
		{"hop only", []string{"/libp2p/circuit/relay/0.2.0/hop"}, false},
		{"mixed", []string{"/shurli/grant-receipt/1.0.0", "/libp2p/circuit/relay/0.2.0/stop"}, true},
		{"empty", []string{}, false},
		{"other", []string{"/shurli/admin/1.0.0"}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var pids []protocol.ID
			for _, p := range tc.pids {
				pids = append(pids, protocol.ID(p))
			}
			if got := isStopProtocol(pids); got != tc.expect {
				t.Fatalf("isStopProtocol(%v) = %v, want %v", tc.pids, got, tc.expect)
			}
		})
	}
}
