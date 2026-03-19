package plugin

import (
	"errors"
	"log/slog"
	"runtime/debug"
	"sync/atomic"
	"time"
)

// supervisor manages per-plugin crash recovery: crash counting, exponential
// backoff, circuit breaking, and auto-restart with optional checkpoint/restore.
//
// One supervisor per pluginEntry. Created in Register(), wired in Enable/Disable.
//
// Restart flow (triggered by wrapHandler or callWithRecovery panic recovery):
//   1. RecordCrash() increments counter
//   2. ShouldAutoRestart() checks circuit breaker (3 panics in 5 min = dead)
//   3. TriggerRestart() acquires restart guard (atomic, one restart in flight)
//   4. Checkpoint (if Checkpointer) -> Disable -> backoff sleep -> Enable -> Restore
//
// The circuit breaker threshold and window use the existing constants from
// lifecycle.go (circuitBreakerThreshold, circuitBreakerWindowDuration).
type supervisor struct {
	pluginName string

	// Crash tracking. Protected by Registry.mu (same as pluginEntry fields).
	crashCount int
	firstCrash time.Time

	// Backoff state. Protected by Registry.mu.
	consecutiveRestarts int

	// generation increments on every Reset(). TriggerRestart captures the
	// generation at launch. If it changes (e.g. user re-enabled the plugin),
	// the restart goroutine bails instead of interfering. Protected by Registry.mu.
	generation uint64

	// Restart guard. Atomic: prevents concurrent restart attempts.
	restarting atomic.Bool

	// disabled is set by Disable()/circuit breaker to prevent auto-restart.
	disabled atomic.Bool

	// registry back-reference for triggering Enable/Disable.
	registry *Registry
}

// newSupervisor creates a supervisor for the given plugin.
func newSupervisor(name string, registry *Registry) *supervisor {
	return &supervisor{
		pluginName: name,
		registry:   registry,
	}
}

// RecordCrash increments the crash counter within the circuit breaker window.
// If the window has expired, resets the counter. Returns the new crash count.
// MUST be called with Registry.mu held (Lock).
func (s *supervisor) RecordCrash() int {
	now := time.Now()
	if s.firstCrash.IsZero() || now.Sub(s.firstCrash) > circuitBreakerWindowDuration {
		s.crashCount = 1
		s.firstCrash = now
	} else {
		s.crashCount++
	}
	return s.crashCount
}

// ShouldAutoRestart returns true if the plugin should be automatically restarted.
// False if circuit breaker tripped (>= threshold crashes) or explicitly disabled.
// Reads crashCount which is protected by Registry.mu - caller must hold at least RLock.
func (s *supervisor) ShouldAutoRestart() bool {
	if s.disabled.Load() {
		return false
	}
	return s.crashCount < circuitBreakerThreshold
}

// BackoffDuration returns the backoff wait before the next restart attempt.
// 0s for first restart, 1s for second. Third crash hits circuit breaker (no restart).
// Reads consecutiveRestarts which is protected by Registry.mu.
func (s *supervisor) BackoffDuration() time.Duration {
	switch s.consecutiveRestarts {
	case 0:
		return 0
	case 1:
		return 1 * time.Second
	default:
		// Circuit breaker should have fired before we get here,
		// but defend against it anyway.
		return 2 * time.Second
	}
}

// Reset clears crash counters and backoff state. Called on successful Enable().
// Increments generation to invalidate any in-flight restart goroutines.
// MUST be called with Registry.mu held (Lock).
func (s *supervisor) Reset() {
	s.crashCount = 0
	s.firstCrash = time.Time{}
	s.consecutiveRestarts = 0
	s.generation++
	s.disabled.Store(false)
}

// SetDisabled marks the supervisor as explicitly disabled (user-initiated Disable
// or circuit breaker). Prevents auto-restart from firing.
func (s *supervisor) SetDisabled() {
	s.disabled.Store(true)
}

// TriggerRestart initiates an asynchronous plugin restart with checkpoint/restore.
// Goroutine-safe: only one restart runs at a time per plugin (atomic guard).
// The restart runs in a separate goroutine so handler/callWithRecovery can return.
func (s *supervisor) TriggerRestart() {
	// Atomic guard: only one restart in flight.
	if !s.restarting.CompareAndSwap(false, true) {
		return
	}

	// Capture generation under lock so we can detect if Reset() was called
	// (e.g. user manually re-enabled) while we're sleeping in backoff.
	s.registry.mu.RLock()
	startGen := s.generation
	s.registry.mu.RUnlock()

	go func() {
		defer s.restarting.Store(false)

		log := slog.With("plugin", s.pluginName, "component", "supervisor")
		log.Info("plugin.supervisor.restart-starting")

		// Gate check: verify we're still allowed to restart under the lock.
		// This prevents races with circuit breaker (which sets disabled=true)
		// and user-initiated operations (which change generation).
		s.registry.mu.RLock()
		if s.generation != startGen || !s.ShouldAutoRestart() {
			s.registry.mu.RUnlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "state changed before start")
			return
		}
		entry, ok := s.registry.plugins[s.pluginName]
		s.registry.mu.RUnlock()
		if !ok {
			log.Warn("plugin.supervisor.restart-aborted", "reason", "plugin not found")
			return
		}

		// 1. Checkpoint (if Checkpointer).
		var checkpointData []byte
		if cp, ok := entry.plugin.(Checkpointer); ok {
			data, err := func() (d []byte, retErr error) {
				defer func() {
					if rec := recover(); rec != nil {
						log.Error("plugin.supervisor.checkpoint-panic",
						"panic", rec, "stack", string(debug.Stack()))
						retErr = errors.New("checkpoint panicked")
					}
				}()
				return cp.Checkpoint()
			}()
			if err != nil {
				if errors.Is(err, ErrSkipCheckpoint) {
					log.Info("plugin.supervisor.checkpoint-skipped")
				} else {
					log.Warn("plugin.supervisor.checkpoint-failed", "error", err)
				}
			} else if len(data) > maxCheckpointSize {
				log.Warn("plugin.supervisor.checkpoint-too-large",
					"bytes", len(data), "max", maxCheckpointSize)
			} else {
				checkpointData = data
				log.Info("plugin.supervisor.checkpoint-saved", "bytes", len(data))
			}
		}

		// 2. Disable (Stop the crashed plugin).
		// Re-check under lock: circuit breaker may have fired during checkpoint.
		s.registry.mu.RLock()
		if s.generation != startGen || !s.ShouldAutoRestart() {
			s.registry.mu.RUnlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "state changed before disable")
			return
		}
		s.registry.mu.RUnlock()
		if err := s.registry.Disable(s.pluginName); err != nil {
			log.Warn("plugin.supervisor.disable-failed", "error", err)
		}

		// 3. Backoff sleep.
		s.registry.mu.Lock()
		backoff := s.BackoffDuration()
		s.consecutiveRestarts++
		s.registry.mu.Unlock()

		if backoff > 0 {
			log.Info("plugin.supervisor.backoff", "duration", backoff.String())
			time.Sleep(backoff)
		}

		// 4. Check generation before re-enabling.
		// Clear the disabled flag that Disable() set, so Enable can proceed.
		// But only if generation hasn't changed (no user intervention) and
		// crash count is still below circuit breaker threshold.
		s.registry.mu.Lock()
		if s.generation != startGen {
			s.registry.mu.Unlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "generation changed")
			return
		}
		if s.crashCount >= circuitBreakerThreshold {
			s.registry.mu.Unlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "circuit breaker")
			return
		}
		s.disabled.Store(false) // clear so Enable's Reset works and ShouldAutoRestart succeeds
		s.registry.mu.Unlock()

		// 5. Re-enable.
		if err := s.registry.Enable(s.pluginName); err != nil {
			log.Error("plugin.supervisor.enable-failed", "error", err)
			return
		}

		// 6. Restore (if Checkpointer and we have data).
		if len(checkpointData) > 0 {
			s.registry.mu.RLock()
			entry, ok = s.registry.plugins[s.pluginName]
			s.registry.mu.RUnlock()
			if ok {
				if cp, ok := entry.plugin.(Checkpointer); ok {
					func() {
						defer func() {
							if rec := recover(); rec != nil {
								log.Error("plugin.supervisor.restore-panic",
								"panic", rec, "stack", string(debug.Stack()))
							}
						}()
						if err := cp.Restore(checkpointData); err != nil {
							log.Warn("plugin.supervisor.restore-failed", "error", err)
						} else {
							log.Info("plugin.supervisor.restore-complete")
						}
					}()
				}
			}
		}

		log.Info("plugin.supervisor.restart-complete")
	}()
}
