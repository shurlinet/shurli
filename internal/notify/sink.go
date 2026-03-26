package notify

// Sink receives notification events. Implementations must be safe
// for concurrent use - the Router calls Notify from goroutines.
type Sink interface {
	// Name returns a human-readable identifier for this sink (e.g. "log", "desktop", "webhook").
	Name() string

	// Notify delivers an event to this sink. Errors are logged by the Router
	// but never propagate to the event source. Implementations should not block
	// for extended periods.
	Notify(event Event) error
}
