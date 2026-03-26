package plugin

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

// L1: Zero goroutine leaks after panic + supervisor restart cycle.
func TestLeak_PanicRestartCycle(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreCurrent(),
	)

	r := NewRegistry(&ContextProvider{})
	r.enableDisableCooldown = 0

	p := newMinimalPlugin("leak-restart")
	r.Register(p)
	r.Enable("leak-restart")

	// Trigger a crash -> supervisor restarts the plugin.
	r.recordCrashAndMaybeRestart("leak-restart")

	// Wait for restart to complete.
	if !waitForState(r, "leak-restart", StateActive, 5*time.Second) {
		t.Fatal("plugin should restart after single crash")
	}

	// Clean shutdown.
	r.StopAll()

	// Give goroutines time to wind down.
	time.Sleep(100 * time.Millisecond)
}

// L2: Zero goroutine leaks after circuit breaker fires.
func TestLeak_CircuitBreakerShutdown(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreCurrent(),
	)

	r := NewRegistry(&ContextProvider{})
	r.enableDisableCooldown = 0

	p := newMinimalPlugin("leak-cb")
	r.Register(p)
	r.Enable("leak-cb")

	// Trip circuit breaker: 3 crashes.
	for i := 0; i < 3; i++ {
		r.recordCrashAndMaybeRestart("leak-cb")
	}

	// Wait for the plugin to reach STOPPED.
	if !waitForState(r, "leak-cb", StateStopped, 5*time.Second) {
		t.Fatal("plugin should be stopped after circuit breaker")
	}

	// Give goroutines time to wind down.
	time.Sleep(100 * time.Millisecond)
}

// L3: Zero goroutine leaks after rapid enable/disable cycling.
func TestLeak_RapidEnableDisable(t *testing.T) {
	defer goleak.VerifyNone(t,
		goleak.IgnoreCurrent(),
	)

	r := NewRegistry(&ContextProvider{})
	r.enableDisableCooldown = 0

	p := newMinimalPlugin("leak-cycle")
	r.Register(p)

	for i := 0; i < 50; i++ {
		r.Enable("leak-cycle")
		r.Disable("leak-cycle")
	}

	// Final shutdown.
	r.StopAll()

	// Give goroutines time to wind down.
	time.Sleep(100 * time.Millisecond)
}
