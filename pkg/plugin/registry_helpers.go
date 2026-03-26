package plugin

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime/debug"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// reservedProtocolNames is the set of core Shurli protocol names that plugins
// must not shadow. Package-level to avoid allocation per call.
var reservedProtocolNames = map[string]bool{
	"relay-pair":   true,
	"relay-unseal": true,
	"relay-admin":  true,
	"relay-motd":   true,
	"peer-notify":  true,
	"zkp-auth":     true,
	"ping":         true,
	"kad":          true,
}

// isReservedProtocolName returns true if the name collides with a core Shurli protocol.
// G2 fix: defense-in-depth namespace check even for Layer 1 compiled-in plugins.
func isReservedProtocolName(name string) bool {
	return reservedProtocolNames[name]
}

// validateProtocolName checks that a protocol name is safe: alphanumeric + hyphens,
// reasonable length, no special characters (X3 fix).
func validateProtocolName(name string) error {
	if name == "" {
		return fmt.Errorf("protocol name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("protocol name too long: %d chars (max 64)", len(name))
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("protocol name %q contains invalid character %q (allowed: a-z, 0-9, -)", name, string(c))
		}
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("protocol name %q cannot start or end with a hyphen", name)
	}
	return nil
}

// registerProtocols registers a plugin's protocols with the service registry.
// P14: MUST be called with r.mu held (Lock or RLock). All call sites verified.
func (r *Registry) registerProtocols(entry *pluginEntry) error {
	if r.serviceRegistry == nil {
		return nil // no service registry (e.g., testing)
	}
	// Track successfully registered names so rollback only removes what THIS call added.
	// Prevents unregistering another plugin's protocol on partial failure.
	var registered []string
	for _, proto := range entry.plugin.Protocols() {
		// X3 fix: validate protocol names before registration.
		if err := validateProtocolName(proto.Name); err != nil {
			r.rollbackProtocols(registered)
			return fmt.Errorf("plugin %q: %w", entry.plugin.Name(), err)
		}
		// G2 fix: protocol namespace validation (defense-in-depth for Layer 1).
		// Layer 1 plugins are trusted compiled-in code, but we validate protocol
		// names don't collide with core Shurli protocols. Core protocols use
		// specific names that plugins must not shadow.
		if isReservedProtocolName(proto.Name) {
			r.rollbackProtocols(registered)
			return fmt.Errorf("plugin %q: protocol name %q is reserved for core Shurli protocols",
				entry.plugin.Name(), proto.Name)
		}
		pid := p2pnet.ProtocolID(proto.Name, proto.Version)
		policy := proto.Policy
		if policy == nil {
			policy = &p2pnet.PluginPolicy{AllowedTransports: p2pnet.DefaultTransport}
		}
		svc := &p2pnet.Service{
			Name:     proto.Name,
			Protocol: pid,
			Handler:  r.wrapHandler(entry.plugin.Name(), proto.Handler),
			Enabled:  true,
			Policy:   policy,
		}
		if err := r.serviceRegistry.RegisterService(svc); err != nil {
			r.rollbackProtocols(registered)
			return fmt.Errorf("register %s: %w", proto.Name, err)
		}
		registered = append(registered, proto.Name)
	}
	// Save registered names so unregisterProtocols works after Stop() nils plugin fields.
	entry.registeredProtos = registered
	return nil
}

// rollbackProtocols unregisters only the named protocols from the service registry.
// Used by registerProtocols to undo partial registration without touching other plugins.
func (r *Registry) rollbackProtocols(names []string) {
	if r.serviceRegistry == nil {
		return
	}
	for _, name := range names {
		if err := r.serviceRegistry.UnregisterService(name); err != nil {
			slog.Warn("plugin.rollback-protocol", "protocol", name, "error", err)
		}
	}
}

// unregisterProtocols removes a plugin's protocols from the service registry.
// Uses saved registeredProtos list instead of calling Protocols(), because
// Stop() may nil plugin fields before this runs (making Protocols() return empty).
// P14: MUST be called with r.mu held. All call sites verified.
func (r *Registry) unregisterProtocols(entry *pluginEntry) {
	if r.serviceRegistry == nil {
		return
	}
	for _, name := range entry.registeredProtos {
		if err := r.serviceRegistry.UnregisterService(name); err != nil {
			slog.Warn("plugin.unregister-protocol", "name", entry.plugin.Name(),
				"protocol", name, "error", err)
		}
	}
	entry.registeredProtos = nil
}

// wrapHandler wraps a plugin's stream handler with state checking, panic recovery,
// and circuit breaker logic. Streams are only handled in ACTIVE state.
func (r *Registry) wrapHandler(pluginName string, handler p2pnet.StreamHandler) p2pnet.StreamHandler {
	return func(serviceName string, s network.Stream) {
		r.mu.RLock()
		entry, ok := r.plugins[pluginName]
		if !ok || entry.state != StateActive {
			r.mu.RUnlock()
			s.Reset()
			return
		}
		r.mu.RUnlock()

		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("plugin.panic", "name", pluginName, "method", "handler",
					"panic", rec, "stack", string(debug.Stack()))
				s.Reset()
				r.recordCrashAndMaybeRestart(pluginName)
			}
		}()

		handler(serviceName, s)
	}
}

// callWithRecovery calls fn, recovering any panic and converting it to an error.
// Also records crashes for circuit breaker logic.
// G12 SAFETY: callers MUST NOT hold r.mu when calling this, because the panic
// handler calls recordCrash which acquires r.mu.Lock(). All call sites (Register,
// Enable, Disable, NotifyNetworkReady) release the lock before calling this.
func (r *Registry) callWithRecovery(entry *pluginEntry, method string, fn func() error) (retErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("plugin.panic", "name", entry.plugin.Name(), "method", method,
				"panic", rec, "stack", string(debug.Stack()))
			retErr = fmt.Errorf("panic in %s: %v", method, rec)
			// Record crash for circuit breaker counting, but do NOT trigger
			// auto-restart. Lifecycle method panics (Init/Start/Stop) indicate
			// a broken plugin, not a transient handler failure. Auto-restart
			// is only triggered by wrapHandler (stream handler panics during ACTIVE).
			r.recordCrash(entry.plugin.Name())
		}
	}()
	return fn()
}

// applyCircuitBreaker is the shared circuit breaker logic: unregister protocols,
// set STOPPED, nil context fields, disable supervisor. MUST be called with r.mu held.
// Returns true if the circuit breaker fired.
func (r *Registry) applyCircuitBreaker(name string, entry *pluginEntry, sv *supervisor, crashCount int) bool {
	if crashCount < circuitBreakerThreshold && sv.lifetimeCrashes < lifetimeCrashLimit {
		return false
	}
	slog.Warn("plugin.circuit-breaker", "name", name, "crash_count", crashCount,
		"lifetime_crashes", sv.lifetimeCrashes)
	r.unregisterProtocols(entry)
	entry.state = StateStopped
	// F3 fix: nil PluginContext runtime fields so disabled plugin can't use network.
	entry.ctx.network = nil
	entry.ctx.nameResolver = nil
	entry.ctx.peerConnector = nil
	sv.SetDisabled()
	return true
}

// recordCrash increments the crash counter and applies the circuit breaker.
// Does NOT trigger auto-restart. Used by callWithRecovery for lifecycle panics
// (Init/Start/Stop/OnNetworkReady) where auto-restart would be counterproductive.
// When the circuit breaker fires, Stop() IS called to clean up plugin resources
// (goroutines, listeners started by Start()). This matters for the OnNetworkReady
// code path where the plugin is ACTIVE when the panic occurs.
func (r *Registry) recordCrash(name string) {
	r.mu.Lock()

	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return
	}

	sv := entry.supervisor
	wasActive := entry.state == StateActive
	count := sv.RecordCrash()
	if r.applyCircuitBreaker(name, entry, sv, count) {
		r.mu.Unlock()

		// Stop() is needed when the plugin was ACTIVE (e.g., OnNetworkReady panic).
		// For Init panics: plugin was never started, Stop() is a no-op.
		// For Start panics: Enable's error path calls Stop() separately.
		// For Stop panics: already stopping.
		// Wrapping in recovery: plugin is already STOPPED, this is best-effort cleanup.
		if wasActive {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Error("plugin.circuit-breaker-stop-panic", "name", name,
							"panic", rec, "stack", string(debug.Stack()))
					}
				}()
				if err := entry.plugin.Stop(); err != nil {
					slog.Warn("plugin.circuit-breaker-stop-error", "name", name, "error", err)
				}
			}()
		}
		return
	}
	r.mu.Unlock()
}

// recordCrashAndMaybeRestart uses the supervisor to record a crash, apply the
// circuit breaker, and trigger auto-restart if allowed. Used by wrapHandler
// for stream handler panics where the plugin was ACTIVE and a transient
// failure warrants automatic recovery.
// Circuit breaker: 3 panics within 5 minutes OR lifetime limit (10) -> auto-disable, no restart.
func (r *Registry) recordCrashAndMaybeRestart(name string) {
	r.mu.Lock()

	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return
	}

	sv := entry.supervisor
	count := sv.RecordCrash()

	if r.applyCircuitBreaker(name, entry, sv, count) {
		r.mu.Unlock()

		// Call Stop() outside the lock to clean up plugin resources (goroutines, listeners).
		// Wrapped in recovery: if Stop() panics here, we've already set STOPPED.
		func() {
			defer func() {
				if rec := recover(); rec != nil {
					slog.Error("plugin.circuit-breaker-stop-panic", "name", name,
						"panic", rec, "stack", string(debug.Stack()))
				}
			}()
			if err := entry.plugin.Stop(); err != nil {
				slog.Warn("plugin.circuit-breaker-stop-error", "name", name, "error", err)
			}
		}()
		return
	}

	shouldRestart := sv.ShouldAutoRestart()
	r.mu.Unlock()

	if shouldRestart {
		sv.TriggerRestart()
	}
}

// buildInfoFromSnapshot constructs an Info struct for introspection.
// X8 fix: takes pre-snapshotted values so it can be called outside the registry lock.
func buildInfoFromSnapshot(p Plugin, state State, crashCount, lifetimeCrashes int) Info {
	cmds := make([]string, 0, len(p.Commands()))
	for _, c := range p.Commands() {
		cmds = append(cmds, c.Name)
	}
	routes := make([]string, 0, len(p.Routes()))
	for _, rt := range p.Routes() {
		routes = append(routes, fmt.Sprintf("%s %s", rt.Method, rt.Path))
	}
	protos := make([]string, 0, len(p.Protocols()))
	for _, pr := range p.Protocols() {
		protos = append(protos, fmt.Sprintf("%s/%s", pr.Name, pr.Version))
	}

	return Info{
		Name:       p.Name(),
		Version:    p.Version(),
		Type:       "built-in",
		State:      state,
		Enabled:    state == StateActive,
		Commands:   cmds,
		Routes:     routes,
		Protocols:  protos,
		ConfigKey:       p.ConfigSection(),
		CrashCount:      crashCount,
		LifetimeCrashes: lifetimeCrashes,
	}
}

// readFileLimited reads a file up to maxBytes. Returns error if file exceeds limit (M3 fix).
// Reads from the already-open handle to avoid TOCTOU between stat and read.
func readFileLimited(path string, maxBytes int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if info.Size() > maxBytes {
		return nil, fmt.Errorf("file %s exceeds max size (%d > %d)", path, info.Size(), maxBytes)
	}
	// Read from the open handle (not os.ReadFile) to prevent TOCTOU:
	// file could grow between Stat and a second Open.
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return nil, err
	}
	// Defense: if the file grew between Stat and Read, LimitReader caps at maxBytes+1.
	// Reject if we got more than maxBytes (TOCTOU race with file growth).
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %s grew during read (%d > %d)", path, len(data), maxBytes)
	}
	return data, nil
}
