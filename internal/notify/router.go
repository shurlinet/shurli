package notify

import (
	"log/slog"
	"sync"
	"time"
)

const (
	// dedupTTL is how long event IDs are remembered for deduplication.
	dedupTTL = 5 * time.Minute
	// dedupCleanInterval is how often stale dedup entries are swept.
	dedupCleanInterval = 1 * time.Minute
)

// ExpiryChecker provides grants that are expiring soon.
// Implemented by grants.Store.
type ExpiryChecker interface {
	ExpiringWithin(d time.Duration) []ExpiryInfo
}

// ExpiryInfo is the minimal grant info needed for pre-expiry warnings.
// Avoids importing grants package into notify.
type ExpiryInfo struct {
	PeerID    string
	PeerName  string
	ExpiresAt time.Time
	Remaining time.Duration
}

// Router dispatches notification events to all registered sinks.
// Sinks are called concurrently in goroutines. Failures and panics
// are logged but never block the caller or affect daemon stability.
type Router struct {
	mu    sync.RWMutex
	sinks []Sink

	// Dedup: event ID -> expiry time.
	dedupMu sync.Mutex
	seen    map[string]time.Time

	// Pre-expiry warning state.
	expiryChecker   ExpiryChecker
	expiryThreshold time.Duration // fire grant_expiring when remaining <= this
	expiryInterval  time.Duration // how often to check

	// NameResolver resolves peer IDs to human names.
	nameResolver func(peerID string) string

	started bool
	stopCh  chan struct{}
	done    chan struct{}
	logger  *slog.Logger
}

// NewRouter creates a notification router. The LogSink is always added first.
// logLevel filters the log sink: "" or "info" logs everything, "warn" logs only warnings.
func NewRouter(logger *slog.Logger, logLevel Severity) *Router {
	if logger == nil {
		logger = slog.Default()
	}
	r := &Router{
		seen:            make(map[string]time.Time),
		expiryThreshold: 10 * time.Minute,
		expiryInterval:  60 * time.Second,
		stopCh:          make(chan struct{}),
		done:            make(chan struct{}),
		logger:          logger,
	}
	// LogSink is always first and always present.
	r.sinks = append(r.sinks, NewLogSink(logger, logLevel))
	return r
}

// AddSink registers an additional notification sink.
func (r *Router) AddSink(s Sink) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sinks = append(r.sinks, s)
}

// Sinks returns the names of all registered sinks.
func (r *Router) Sinks() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, len(r.sinks))
	for i, s := range r.sinks {
		names[i] = s.Name()
	}
	return names
}

// SetExpiryChecker configures the source for pre-expiry warning checks.
// Must be called before Start().
func (r *Router) SetExpiryChecker(checker ExpiryChecker, threshold, interval time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expiryChecker = checker
	if threshold > 0 {
		r.expiryThreshold = threshold
	}
	if interval > 0 {
		r.expiryInterval = interval
	}
}

// SetNameResolver sets the function used to resolve peer IDs to names.
func (r *Router) SetNameResolver(fn func(peerID string) string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nameResolver = fn
}

// Emit dispatches an event to all sinks. Non-blocking: each sink
// is called in its own goroutine. Duplicate events (same ID) are dropped.
func (r *Router) Emit(event Event) {
	if r.isDuplicate(event.ID) {
		return
	}

	r.mu.RLock()
	resolver := r.nameResolver
	sinks := make([]Sink, len(r.sinks))
	copy(sinks, r.sinks)
	r.mu.RUnlock()

	// Resolve peer name if not already set.
	if event.PeerName == "" && event.PeerID != "" && resolver != nil {
		event.PeerName = resolver(event.PeerID)
	}

	for _, s := range sinks {
		go func(sink Sink) {
			defer func() {
				if p := recover(); p != nil {
					r.logger.Error("notify: sink panicked",
						"sink", sink.Name(),
						"event_type", string(event.Type),
						"panic", p)
				}
			}()
			if err := sink.Notify(event); err != nil {
				r.logger.Warn("notify: sink failed",
					"sink", sink.Name(),
					"event_type", string(event.Type),
					"error", err)
			}
		}(s)
	}
}

// Start begins the pre-expiry warning ticker and dedup cleanup.
// Must be called after SetExpiryChecker. Safe to call only once.
func (r *Router) Start() {
	r.mu.Lock()
	r.started = true
	r.mu.Unlock()
	go func() {
		defer close(r.done)

		expiryTicker := time.NewTicker(r.expiryInterval)
		defer expiryTicker.Stop()

		dedupTicker := time.NewTicker(dedupCleanInterval)
		defer dedupTicker.Stop()

		for {
			select {
			case <-expiryTicker.C:
				r.checkExpiring()
			case <-dedupTicker.C:
				r.cleanDedup()
			case <-r.stopCh:
				return
			}
		}
	}()
}

// Stop shuts down the pre-expiry ticker and waits for it to exit.
// Safe to call even if Start was never called. Must not be called twice after Start.
func (r *Router) Stop() {
	r.mu.RLock()
	started := r.started
	r.mu.RUnlock()
	if !started {
		return
	}
	close(r.stopCh)
	<-r.done
}

// isDuplicate returns true if this event ID was already seen.
// Marks the ID as seen if new.
func (r *Router) isDuplicate(id string) bool {
	if id == "" {
		return false // events without IDs are never deduped
	}
	r.dedupMu.Lock()
	defer r.dedupMu.Unlock()
	if _, ok := r.seen[id]; ok {
		return true
	}
	r.seen[id] = time.Now().Add(dedupTTL)
	return false
}

// cleanDedup removes expired entries from the dedup map.
func (r *Router) cleanDedup() {
	now := time.Now()
	r.dedupMu.Lock()
	defer r.dedupMu.Unlock()
	for id, expiry := range r.seen {
		if now.After(expiry) {
			delete(r.seen, id)
		}
	}
}

// ExpiryCheckerFunc adapts a function into the ExpiryChecker interface.
// Use this to bridge GrantStore.ExpiringWithin without importing grants into notify.
type ExpiryCheckerFunc func(d time.Duration) []ExpiryInfo

func (f ExpiryCheckerFunc) ExpiringWithin(d time.Duration) []ExpiryInfo { return f(d) }

// checkExpiring queries the ExpiryChecker and emits grant_expiring events.
// Dedup ensures each grant only fires one warning per expiry cycle.
func (r *Router) checkExpiring() {
	r.mu.RLock()
	checker := r.expiryChecker
	threshold := r.expiryThreshold
	r.mu.RUnlock()

	if checker == nil {
		return
	}

	expiring := checker.ExpiringWithin(threshold)
	for _, info := range expiring {
		// Use a deterministic ID so the same grant doesn't fire repeatedly.
		// Includes the expiry time so extending a grant generates a new warning.
		dedupID := "expiring-" + info.PeerID + "-" + info.ExpiresAt.Format(time.RFC3339)
		remaining := info.Remaining.Round(time.Second).String()

		event := Event{
			ID:        dedupID,
			Type:      EventGrantExpiring,
			Severity:  SeverityWarn,
			PeerID:    info.PeerID,
			PeerName:  info.PeerName,
			Message:   "relay data access expiring in " + remaining,
			Timestamp: time.Now(),
			Metadata: map[string]string{
				"remaining":  remaining,
				"expires_at": info.ExpiresAt.Format(time.RFC3339),
			},
		}
		r.Emit(event)
	}
}
