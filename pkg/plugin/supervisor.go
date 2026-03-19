package plugin

import (
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"log/slog"
	"math/rand/v2"
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
//   1. RecordCrash() increments window counter + lifetime counter
//   2. ShouldAutoRestart() checks: disabled flag, lifetime limit (10 total),
//      window threshold (3 in 5 min)
//   3. TriggerRestart() acquires restart guard (atomic, one restart in flight)
//   4. Checkpoint (if Checkpointer, with timeout + HMAC) -> Disable ->
//      backoff sleep (with jitter) -> Enable -> Restore (with HMAC verify)
//
// Constants from lifecycle.go: circuitBreakerThreshold, circuitBreakerWindowDuration,
// lifetimeCrashLimit.
type supervisor struct {
	pluginName string

	// Crash tracking. Protected by Registry.mu (same as pluginEntry fields).
	crashCount     int
	firstCrash     time.Time
	lifetimeCrashes int // total crashes ever, never reset (resets only on daemon restart)

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
	s.lifetimeCrashes++
	return s.crashCount
}

// ShouldAutoRestart returns true if the plugin should be automatically restarted.
// False if: explicitly disabled, lifetime crashes >= 10, or window crashes >= 3.
// Reads crashCount/lifetimeCrashes which are protected by Registry.mu - caller must hold at least RLock.
func (s *supervisor) ShouldAutoRestart() bool {
	if s.disabled.Load() {
		return false
	}
	if s.lifetimeCrashes >= lifetimeCrashLimit {
		return false
	}
	return s.crashCount < circuitBreakerThreshold
}

// BackoffDuration returns the backoff wait before the next restart attempt.
// Includes 0-500ms random jitter to prevent timing-based crash oracles (P4/T5).
// Note: jitter is a weak mitigation - it obscures restart timing, not restart existence.
// The real defense against crash oracles is "never panic on untrusted input."
// Reads consecutiveRestarts which is protected by Registry.mu.
func (s *supervisor) BackoffDuration() time.Duration {
	jitter := time.Duration(rand.IntN(500)) * time.Millisecond
	switch s.consecutiveRestarts {
	case 0:
		return jitter
	case 1:
		return 1*time.Second + jitter
	default:
		// Circuit breaker should have fired before we get here,
		// but defend against it anyway.
		return 2*time.Second + jitter
	}
}

// Reset clears window crash counters and backoff state. Called on successful Enable().
// Increments generation to invalidate any in-flight restart goroutines.
// Does NOT clear lifetimeCrashes - that counter persists until daemon restart
// to prevent window gaming attacks (P1/T7).
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
		var keyDeriver func(string) []byte
		if ok && entry.ctx != nil {
			keyDeriver = entry.ctx.keyDeriver
		}
		s.registry.mu.RUnlock()
		if !ok {
			log.Warn("plugin.supervisor.restart-aborted", "reason", "plugin not found")
			return
		}

		// 1. Checkpoint (if Checkpointer).
		// P3: timeout prevents a plugin in bad state from blocking the restart goroutine.
		// NOTE: if Checkpoint() hangs past the timeout, the goroutine running it is leaked
		// (no cancellation mechanism). Same tradeoff as Start() timeout. Acceptable for
		// Layer 1 trusted code. Layer 2 WASM will use fuel metering to bound execution.
		var checkpointData []byte
		var checkpointMAC []byte
		if cp, ok := entry.plugin.(Checkpointer); ok {
			type cpResult struct {
				data []byte
				err  error
			}
			cpDone := make(chan cpResult, 1) // buffered: sender must not block if timeout fires first
			go func() {
				defer func() {
					if rec := recover(); rec != nil {
						log.Error("plugin.supervisor.checkpoint-panic",
							"panic", rec, "stack", string(debug.Stack()))
						cpDone <- cpResult{err: errors.New("checkpoint panicked")}
					}
				}()
				d, e := cp.Checkpoint()
				cpDone <- cpResult{data: d, err: e}
			}()

			var result cpResult
			select {
			case result = <-cpDone:
			case <-time.After(startTimeoutDuration):
				log.Warn("plugin.supervisor.checkpoint-timeout",
					"timeout", startTimeoutDuration.String())
				result = cpResult{err: errors.New("checkpoint timed out")}
			}

			if result.err != nil {
				if errors.Is(result.err, ErrSkipCheckpoint) {
					log.Info("plugin.supervisor.checkpoint-skipped")
				} else {
					log.Warn("plugin.supervisor.checkpoint-failed", "error", result.err)
				}
			} else if len(result.data) > maxCheckpointSize {
				log.Warn("plugin.supervisor.checkpoint-too-large",
					"bytes", len(result.data), "max", maxCheckpointSize)
			} else {
				checkpointData = result.data
				log.Info("plugin.supervisor.checkpoint-saved", "bytes", len(result.data))
				// P2: HMAC integrity for checkpoint data.
				// Null byte separator prevents HKDF domain collision: without it,
				// DeriveKey("checkpoint-foo") from plugin "bar" could derive the
				// same key as checkpoint HMAC for plugin "foo". Plugin names can't
				// contain null bytes (validated), so "checkpoint\x00name" is
				// unambiguous. Critical for Layer 2 untrusted plugin security.
				if keyDeriver == nil {
					log.Info("plugin.supervisor.checkpoint-hmac-skipped",
						"reason", "no key deriver configured")
				} else {
					key := keyDeriver("checkpoint\x00" + s.pluginName)
					// HMAC-SHA256 requires a 32-byte key for full security.
					// Shorter keys work but weaken the MAC. Skip HMAC rather
					// than silently operate with degraded security.
					if len(key) >= 32 {
						mac := hmac.New(sha256.New, key)
						mac.Write(result.data)
						checkpointMAC = mac.Sum(nil)
					} else if len(key) > 0 {
						log.Warn("plugin.supervisor.checkpoint-hmac-skipped",
							"reason", "key too short", "got", len(key), "need", 32)
					}
				}
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

		// 4. Check generation and state before re-enabling.
		// Abort if: generation changed (user intervention), crash limits hit,
		// or an external Disable/StopAll fired during backoff (disabled flag set
		// AND plugin state changed to STOPPED by someone else).
		s.registry.mu.Lock()
		if s.generation != startGen {
			s.registry.mu.Unlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "generation changed")
			return
		}
		if s.crashCount >= circuitBreakerThreshold || s.lifetimeCrashes >= lifetimeCrashLimit {
			s.registry.mu.Unlock()
			log.Warn("plugin.supervisor.restart-aborted", "reason", "circuit breaker",
				"crash_count", s.crashCount, "lifetime_crashes", s.lifetimeCrashes)
			return
		}
		// Clear disabled flag that our own Disable (step 2) set, so Enable can proceed.
		// KNOWN LIMITATION: StopAll during backoff is indistinguishable from our own
		// Disable (both set disabled=true, neither changes generation). The restart
		// may proceed briefly after StopAll. Bounded by backoff duration (max 2.5s)
		// before process exit. Fix requires daemon context on Registry (post-Batch 4).
		s.disabled.Store(false)
		s.registry.mu.Unlock()

		// 5. Re-enable.
		if err := s.registry.Enable(s.pluginName); err != nil {
			log.Error("plugin.supervisor.enable-failed", "error", err)
			return
		}

		// 6. Restore (if Checkpointer and we have data).
		if len(checkpointData) > 0 {
			// P2: verify HMAC before restoring. Mismatch -> stateless restart.
			hmacOK := true
			if len(checkpointMAC) > 0 && keyDeriver != nil {
				key := keyDeriver("checkpoint\x00" + s.pluginName)
				if len(key) >= 32 {
					mac := hmac.New(sha256.New, key)
					mac.Write(checkpointData)
					if !hmac.Equal(mac.Sum(nil), checkpointMAC) {
						log.Warn("plugin.supervisor.checkpoint-hmac-mismatch")
						hmacOK = false
					}
				}
			}

			if hmacOK {
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
			} else {
				log.Info("plugin.supervisor.stateless-restart", "reason", "hmac-mismatch")
			}
		}

		log.Info("plugin.supervisor.restart-complete")
	}()
}
