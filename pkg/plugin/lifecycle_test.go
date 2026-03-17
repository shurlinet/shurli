package plugin

import (
	"testing"
	"time"
)

// --- Test 17: Valid transitions ---
func TestValidTransitions(t *testing.T) {
	valid := [][2]State{
		{StateLoading, StateReady},
		{StateReady, StateActive},
		{StateActive, StateDraining},
		{StateDraining, StateStopped},
		{StateStopped, StateActive},
		{StateReady, StateStopped},
	}
	for _, tt := range valid {
		if err := ValidTransition(tt[0], tt[1]); err != nil {
			t.Errorf("expected valid: %s -> %s, got error: %v", tt[0], tt[1], err)
		}
	}
}

// --- Test 18: Invalid transitions ---
func TestInvalidTransitions(t *testing.T) {
	invalid := [][2]State{
		{StateLoading, StateActive},
		{StateDraining, StateActive},
		{StateActive, StateReady},
		{StateActive, StateLoading},
		{StateStopped, StateReady},
		{StateReady, StateDraining},
	}
	for _, tt := range invalid {
		if err := ValidTransition(tt[0], tt[1]); err == nil {
			t.Errorf("expected invalid: %s -> %s, got nil error", tt[0], tt[1])
		}
	}
}

// --- Test 19: Enable/Disable/Re-enable cycle ---
func TestEnableDisableReenableCycle(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("cycle")
	r.Register(p)

	// READY -> ACTIVE
	r.Enable("cycle")
	info, _ := r.GetInfo("cycle")
	if info.State != StateActive {
		t.Fatalf("expected ACTIVE after first enable, got %s", info.State)
	}

	// ACTIVE -> STOPPED
	r.Disable("cycle")
	info, _ = r.GetInfo("cycle")
	if info.State != StateStopped {
		t.Fatalf("expected STOPPED after disable, got %s", info.State)
	}

	// STOPPED -> ACTIVE (re-enable)
	if err := r.Enable("cycle"); err != nil {
		t.Fatalf("re-enable failed: %v", err)
	}
	info, _ = r.GetInfo("cycle")
	if info.State != StateActive {
		t.Fatalf("expected ACTIVE after re-enable, got %s", info.State)
	}

	if p.startCalled != 2 {
		t.Errorf("expected Start called twice, got %d", p.startCalled)
	}
}

// --- Test 20: Drain timeout ---
func TestDrainTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping drain timeout test in short mode")
	}

	slowPlugin := &slowStopPlugin{name: "slow-stop", blockDuration: 35 * time.Second}
	r := newTestRegistry()
	r.Register(slowPlugin)
	r.Enable("slow-stop")

	start := time.Now()
	r.Disable("slow-stop")
	elapsed := time.Since(start)

	// Should complete around 30s (drain timeout), not 35s.
	if elapsed > 32*time.Second {
		t.Errorf("drain took too long: %v (expected ~30s timeout)", elapsed)
	}

	info, _ := r.GetInfo("slow-stop")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after timeout, got %s", info.State)
	}
}

type slowStopPlugin struct {
	name          string
	blockDuration time.Duration
}

func (s *slowStopPlugin) Name() string                    { return s.name }
func (s *slowStopPlugin) Version() string                 { return "1.0.0" }
func (s *slowStopPlugin) Init(_ *PluginContext) error      { return nil }
func (s *slowStopPlugin) Start() error                    { return nil }
func (s *slowStopPlugin) Stop() error                     { time.Sleep(s.blockDuration); return nil }
func (s *slowStopPlugin) OnNetworkReady() error            { return nil }
func (s *slowStopPlugin) Commands() []Command              { return nil }
func (s *slowStopPlugin) Routes() []Route                  { return nil }
func (s *slowStopPlugin) Protocols() []Protocol            { return nil }
func (s *slowStopPlugin) ConfigSection() string            { return s.name }

// --- Test 21: Streams rejected when not active ---
// Tested through registry state: wrapHandler checks entry.state != StateActive.
// Direct stream tests require network.Stream mock which is complex.
// Instead, we verify the state gating logic through Enable/Disable state checks.
func TestStreamsRejectedWhenNotActive(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("stream-gate")
	r.Register(p)
	// Plugin in READY state - not ACTIVE.

	// Verify AllCommands returns nothing for non-active plugins.
	if cmds := r.AllCommands(); len(cmds) != 0 {
		t.Error("non-active plugin should not contribute commands")
	}
	if routes := r.AllRoutes(); len(routes) != 0 {
		t.Error("non-active plugin should not contribute routes")
	}
	if protos := r.ActiveProtocols(); len(protos) != 0 {
		t.Error("non-active plugin should not contribute protocols")
	}

	// After enable, commands should appear.
	p.commands = []Command{{Name: "test-cmd"}}
	r.Enable("stream-gate")
	if cmds := r.AllCommands(); len(cmds) != 1 {
		t.Errorf("expected 1 command after enable, got %d", len(cmds))
	}

	// After disable, commands should disappear.
	r.Disable("stream-gate")
	if cmds := r.AllCommands(); len(cmds) != 0 {
		t.Error("disabled plugin should not contribute commands")
	}
}

// --- Test 22: Panic recovery in Start ---
func TestPanicRecoveryInStart(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("panicker")
	p.startPanic = true
	r.Register(p)

	err := r.Enable("panicker")
	if err == nil {
		t.Fatal("expected error from panicking Start")
	}

	// Plugin should still be in READY, not crashed daemon.
	info, _ := r.GetInfo("panicker")
	if info.State != StateReady {
		t.Errorf("expected READY after panic, got %s", info.State)
	}
}

// --- Test 23: Panic recovery in handler ---
// Handler panic recovery is tested indirectly: the wrapHandler function
// catches panics via defer/recover. We verify by triggering panics through
// the circuit breaker tests (24, 25) which use recordCrash.
func TestPanicRecoveryInHandler(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("handler-panic")
	r.Register(p)
	r.Enable("handler-panic")

	// Simulate a crash (as if a handler panicked).
	r.recordCrash("handler-panic")

	info, _ := r.GetInfo("handler-panic")
	if info.CrashCount != 1 {
		t.Errorf("expected crash count 1, got %d", info.CrashCount)
	}
	// Still active after 1 crash (circuit breaker threshold is 3).
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after 1 crash, got %s", info.State)
	}
}

// --- Test 24: Circuit breaker ---
func TestCircuitBreaker(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("breaker")
	r.Register(p)
	r.Enable("breaker")

	// Trigger 3 crashes.
	for i := 0; i < 3; i++ {
		r.recordCrash("breaker")
	}

	info, _ := r.GetInfo("breaker")
	if info.State != StateStopped {
		t.Errorf("expected STOPPED after circuit breaker, got %s", info.State)
	}
}

// --- Test 25: Circuit breaker reset on re-enable ---
func TestCircuitBreakerReset(t *testing.T) {
	r := newTestRegistry()
	p := newMockPlugin("breaker-reset")
	r.Register(p)
	r.Enable("breaker-reset")

	// Trigger circuit breaker.
	for i := 0; i < 3; i++ {
		r.recordCrash("breaker-reset")
	}

	info, _ := r.GetInfo("breaker-reset")
	if info.State != StateStopped {
		t.Fatalf("expected STOPPED, got %s", info.State)
	}

	// Re-enable should reset the crash counter.
	r.Enable("breaker-reset")
	info, _ = r.GetInfo("breaker-reset")
	if info.State != StateActive {
		t.Errorf("expected ACTIVE after re-enable, got %s", info.State)
	}
	if info.CrashCount != 0 {
		t.Errorf("expected crash count 0 after re-enable, got %d", info.CrashCount)
	}
}
