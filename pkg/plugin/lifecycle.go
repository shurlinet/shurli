package plugin

import "fmt"

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

// Circuit breaker constants.
const (
	circuitBreakerThreshold = 3                // panics before auto-disable
	circuitBreakerWindow    = 5 * 60           // seconds (5 minutes)
	drainTimeoutSeconds     = 30               // seconds before force-stop
)
