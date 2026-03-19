package plugin

import (
	"fmt"
	"time"
)

// ValidTransition checks whether a state transition is allowed.
//
// Valid transitions:
//
//	LOADING  -> READY     (Init succeeded)
//	READY    -> ACTIVE    (Start succeeded, first enable)
//	ACTIVE   -> DRAINING  (Stop called)
//	DRAINING -> STOPPED   (drain complete)
//	STOPPED  -> ACTIVE    (re-enable, Start called)
//	READY    -> STOPPED   (never started, daemon shutting down)
//
// Invalid transitions (return error):
//
//	LOADING  -> ACTIVE    (skip Init)
//	DRAINING -> ACTIVE    (can't re-enable while draining)
//	ACTIVE   -> READY     (can't un-start)
//	Any      -> LOADING   (loading happens once)
//	STOPPED  -> READY     (ready only via Init, which is once)
//	READY    -> DRAINING  (never active, can't drain)
func ValidTransition(from, to State) error {
	switch {
	case from == StateLoading && to == StateReady:
		return nil
	case from == StateReady && to == StateActive:
		return nil
	case from == StateActive && to == StateDraining:
		return nil
	case from == StateDraining && to == StateStopped:
		return nil
	case from == StateStopped && to == StateActive:
		return nil
	case from == StateReady && to == StateStopped:
		return nil
	default:
		return fmt.Errorf("invalid state transition: %s -> %s", from, to)
	}
}

// Circuit breaker and drain constants.
const (
	circuitBreakerThreshold = 3 // panics before auto-disable
)

// Time-based constants used by the registry.
var (
	circuitBreakerWindowDuration = 5 * time.Minute  // reset crash counter after this
	drainTimeoutDuration         = 30 * time.Second // force-stop after this
	startTimeoutDuration         = 30 * time.Second // max time for Start() to return
	enableDisableCooldown        = 5 * time.Second  // G3 fix: min time between enable/disable
	maxConfigFileSize      int64 = 1 << 20         // M3 fix: 1MB max plugin config.yaml
	maxCheckpointSize            = 10 * 1024 * 1024 // 10MB max checkpoint data per plugin
)
