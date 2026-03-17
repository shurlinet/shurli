package plugin

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// SECURITY: Plugins cannot install, register, or discover other plugins.
// This is a hard-coded architectural constraint, not a permission that can be granted.
// See plugin-security-threat-analysis-2026-03-17.md, threat vector #43.
// The Registry.Register() method is called by the daemon startup code, never by plugins.
// PluginContext has no method for plugin installation or registration.

// Registry manages plugin lifecycle: registration, enable/disable, and introspection.
type Registry struct {
	mu              sync.RWMutex
	plugins         map[string]*pluginEntry
	provider        *ContextProvider
	serviceRegistry *p2pnet.ServiceRegistry
}

// pluginEntry tracks the runtime state of a single plugin.
type pluginEntry struct {
	plugin     Plugin
	ctx        *PluginContext
	state      State
	crashCount int
	firstCrash time.Time
}

// NewRegistry creates a plugin registry with the given runtime dependencies.
func NewRegistry(provider *ContextProvider) *Registry {
	return &Registry{
		plugins:         make(map[string]*pluginEntry),
		provider:        provider,
		serviceRegistry: provider.ServiceRegistry,
	}
}

// validatePluginName checks that a plugin name is safe for use in config keys,
// protocol namespaces, HTTP paths, and log output. Alphanumeric + hyphens only.
func validatePluginName(name string) error {
	if name == "" {
		return fmt.Errorf("plugin name cannot be empty")
	}
	if len(name) > 64 {
		return fmt.Errorf("plugin name too long: %d chars (max 64)", len(name))
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			return fmt.Errorf("plugin name %q contains invalid character %q (allowed: a-z, 0-9, -)", name, string(c))
		}
	}
	if name[0] == '-' || name[len(name)-1] == '-' {
		return fmt.Errorf("plugin name %q cannot start or end with a hyphen", name)
	}
	return nil
}

// Register adds a plugin to the registry and calls Init().
// Transitions: LOADING -> READY on success.
// Panics in Init() are recovered and returned as errors.
func (r *Registry) Register(p Plugin) error {
	name := p.Name()
	if err := validatePluginName(name); err != nil {
		return err
	}

	// Reserve the name with a LOADING placeholder to prevent concurrent
	// Register() calls from racing past the duplicate check.
	r.mu.Lock()
	if _, exists := r.plugins[name]; exists {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q already registered", name)
	}

	// Build declared protocols map for namespace validation.
	declaredProtos := make(map[string]bool)
	for _, proto := range p.Protocols() {
		pid := p2pnet.ProtocolID(proto.Name, proto.Version)
		declaredProtos[pid] = true
	}

	// Build PluginContext from provider.
	var configBytes []byte
	if r.provider != nil && r.provider.PluginConfigs != nil {
		configBytes = r.provider.PluginConfigs[name]
	}
	var nameResolver func(string) (peer.ID, error)
	var peerConnector func(context.Context, peer.ID) error
	if r.provider != nil {
		nameResolver = r.provider.NameResolver
		peerConnector = r.provider.PeerConnector
	}
	var net *p2pnet.Network
	if r.provider != nil {
		net = r.provider.Network
	}

	pctx := &PluginContext{
		pluginName:     name,
		logger:         slog.Default().With("plugin", name),
		network:        net,
		nameResolver:   nameResolver,
		peerConnector:  peerConnector,
		configBytes:    configBytes,
		declaredProtos: declaredProtos,
	}

	entry := &pluginEntry{
		plugin: p,
		ctx:    pctx,
		state:  StateLoading,
	}

	// Insert placeholder BEFORE releasing lock. Other Register() calls
	// for the same name will see the entry and fail the duplicate check.
	r.plugins[name] = entry
	r.mu.Unlock()

	// Call Init with panic recovery. Lock is NOT held here so that
	// callWithRecovery's panic handler can call recordCrash safely.
	if err := r.callWithRecovery(entry, "Init", func() error {
		return p.Init(pctx)
	}); err != nil {
		// Remove the placeholder on failure.
		r.mu.Lock()
		delete(r.plugins, name)
		r.mu.Unlock()
		return fmt.Errorf("plugin %q Init failed: %w", name, err)
	}

	r.mu.Lock()
	entry.state = StateReady
	r.mu.Unlock()

	slog.Info("plugin.registered", "name", name, "version", p.Version())
	return nil
}

// Enable starts a plugin, registering its protocols with the service registry.
// Valid from READY or STOPPED state. Idempotent if already ACTIVE.
func (r *Registry) Enable(name string) error {
	r.mu.Lock()

	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}

	if entry.state == StateActive {
		r.mu.Unlock()
		return nil // idempotent
	}
	if err := ValidTransition(entry.state, StateActive); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q: %w", name, err)
	}

	// Set LOADING as a transitional state to prevent concurrent Enable() calls
	// from both passing the state check while the lock is released for Start().
	prevState := entry.state
	entry.state = StateLoading

	// Register protocols with service registry.
	if err := r.registerProtocols(entry); err != nil {
		entry.state = prevState // rollback transitional state
		r.mu.Unlock()
		return fmt.Errorf("plugin %q protocol registration failed: %w", name, err)
	}
	r.mu.Unlock()

	// Call Start with panic recovery. Lock is released so callWithRecovery's
	// panic handler can call recordCrash (which acquires the lock).
	if err := r.callWithRecovery(entry, "Start", func() error {
		return entry.plugin.Start()
	}); err != nil {
		// Rollback protocol registration and transitional state on failure.
		r.mu.Lock()
		r.unregisterProtocols(entry)
		entry.state = prevState
		r.mu.Unlock()
		return fmt.Errorf("plugin %q Start failed: %w", name, err)
	}

	// Reset circuit breaker on successful enable.
	r.mu.Lock()
	entry.crashCount = 0
	entry.firstCrash = time.Time{}
	entry.state = StateActive
	r.mu.Unlock()

	slog.Info("plugin.enabled", "name", name)
	return nil
}

// Disable stops a plugin, unregistering its protocols.
// Transitions: ACTIVE -> DRAINING -> STOPPED.
// Idempotent if already STOPPED. Stop() has a 30s timeout.
func (r *Registry) Disable(name string) error {
	r.mu.Lock()
	entry, ok := r.plugins[name]
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q not found", name)
	}

	if entry.state == StateStopped {
		r.mu.Unlock()
		return nil // already stopped
	}
	if entry.state == StateReady {
		// Transition READY -> STOPPED so StartAll() won't start this plugin.
		// This is how enabled=false in config prevents a plugin from starting.
		entry.state = StateStopped
		r.mu.Unlock()
		slog.Info("plugin.disabled", "name", name, "reason", "not-enabled")
		return nil
	}
	if err := ValidTransition(entry.state, StateDraining); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q: %w", name, err)
	}
	entry.state = StateDraining
	r.mu.Unlock()

	// Call Stop with drain timeout and panic recovery.
	// If Stop() exceeds the timeout, the goroutine is abandoned with a warning.
	// Layer 1 plugins are compiled-in trusted code - a blocking Stop() is a bug.
	// Layer 2 WASM plugins will use fuel-based execution to prevent this.
	var reason string
	done := make(chan error, 1)
	go func() {
		done <- r.callWithRecovery(entry, "Stop", func() error {
			return entry.plugin.Stop()
		})
	}()

	select {
	case err := <-done:
		if err != nil {
			reason = "stop-error"
			slog.Warn("plugin.stop-error", "name", name, "error", err)
		} else {
			reason = "requested"
		}
	case <-time.After(drainTimeoutDuration):
		reason = "timeout"
		slog.Warn("plugin.stop-timeout", "name", name, "timeout", drainTimeoutDuration.String())
	}

	// Unregister protocols and set final state.
	r.mu.Lock()
	r.unregisterProtocols(entry)
	entry.state = StateStopped
	r.mu.Unlock()

	slog.Info("plugin.disabled", "name", name, "reason", reason)
	return nil
}

// DisableAll stops every active plugin. Errors are collected but never stop iteration.
// This is the kill switch for incident response.
func (r *Registry) DisableAll() (int, error) {
	r.mu.RLock()
	var activeNames []string
	for name, entry := range r.plugins {
		if entry.state == StateActive {
			activeNames = append(activeNames, name)
		}
	}
	r.mu.RUnlock()

	var errs []error
	for _, name := range activeNames {
		if err := r.Disable(name); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}

	count := len(activeNames)
	slog.Info("plugin.disable-all", "count", count)

	if len(errs) > 0 {
		return count, fmt.Errorf("disable-all completed with %d errors: %v", len(errs), errs)
	}
	return count, nil
}

// List returns metadata for all registered plugins.
func (r *Registry) List() []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()

	infos := make([]Info, 0, len(r.plugins))
	for _, entry := range r.plugins {
		infos = append(infos, r.buildInfo(entry))
	}
	return infos
}

// GetInfo returns metadata for a single plugin.
func (r *Registry) GetInfo(name string) (*Info, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.plugins[name]
	if !ok {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	info := r.buildInfo(entry)
	return &info, nil
}

// ApplyConfig enables or disables plugins based on config state.
// key = plugin name, value = enabled. Unknown names are logged and skipped.
func (r *Registry) ApplyConfig(pluginStates map[string]bool) error {
	for name, enabled := range pluginStates {
		r.mu.RLock()
		_, exists := r.plugins[name]
		r.mu.RUnlock()

		if !exists {
			slog.Warn("plugin.config-unknown", "name", name)
			continue
		}
		if enabled {
			if err := r.Enable(name); err != nil {
				slog.Warn("plugin.config-enable-failed", "name", name, "error", err)
			}
		} else {
			if err := r.Disable(name); err != nil {
				slog.Warn("plugin.config-disable-failed", "name", name, "error", err)
			}
		}
	}
	return nil
}

// StartAll starts all plugins that are in READY state (post-registration, pre-bootstrap).
func (r *Registry) StartAll() error {
	r.mu.RLock()
	var readyNames []string
	for name, entry := range r.plugins {
		if entry.state == StateReady {
			readyNames = append(readyNames, name)
		}
	}
	r.mu.RUnlock()

	for _, name := range readyNames {
		if err := r.Enable(name); err != nil {
			slog.Warn("plugin.start-all-failed", "name", name, "error", err)
		}
	}
	return nil
}

// StopAll stops all active plugins during daemon shutdown.
func (r *Registry) StopAll() error {
	_, err := r.DisableAll()
	// Also transition READY plugins to STOPPED.
	r.mu.Lock()
	for _, entry := range r.plugins {
		if entry.state == StateReady {
			entry.state = StateStopped
		}
	}
	r.mu.Unlock()
	return err
}

// NotifyNetworkReady fans out OnNetworkReady() to all ACTIVE plugins.
// Called after bootstrap completes and relay is connected.
func (r *Registry) NotifyNetworkReady() error {
	r.mu.RLock()
	var activeEntries []*pluginEntry
	var activeNames []string
	for name, entry := range r.plugins {
		if entry.state == StateActive {
			activeEntries = append(activeEntries, entry)
			activeNames = append(activeNames, name)
		}
	}
	r.mu.RUnlock()

	for i, entry := range activeEntries {
		name := activeNames[i]
		if err := r.callWithRecovery(entry, "OnNetworkReady", func() error {
			return entry.plugin.OnNetworkReady()
		}); err != nil {
			slog.Warn("plugin.network-ready-failed", "name", name, "error", err)
		} else {
			slog.Info("plugin.network-ready", "name", name)
		}
	}
	return nil
}

// AllCommands returns commands from all ACTIVE plugins.
func (r *Registry) AllCommands() []Command {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var cmds []Command
	for _, entry := range r.plugins {
		if entry.state == StateActive {
			cmds = append(cmds, entry.plugin.Commands()...)
		}
	}
	return cmds
}

// AllRoutes returns routes from all ACTIVE plugins.
func (r *Registry) AllRoutes() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var routes []Route
	for _, entry := range r.plugins {
		if entry.state == StateActive {
			routes = append(routes, entry.plugin.Routes()...)
		}
	}
	return routes
}

// ActiveProtocols returns protocols from all ACTIVE plugins (for introspection).
func (r *Registry) ActiveProtocols() []Protocol {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var protos []Protocol
	for _, entry := range r.plugins {
		if entry.state == StateActive {
			protos = append(protos, entry.plugin.Protocols()...)
		}
	}
	return protos
}

// --- Internal helpers ---

// registerProtocols registers a plugin's protocols with the service registry.
func (r *Registry) registerProtocols(entry *pluginEntry) error {
	if r.serviceRegistry == nil {
		return nil // no service registry (e.g., testing)
	}
	for _, proto := range entry.plugin.Protocols() {
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
			// Rollback already-registered protocols.
			r.unregisterProtocols(entry)
			return fmt.Errorf("register %s: %w", proto.Name, err)
		}
	}
	return nil
}

// unregisterProtocols removes a plugin's protocols from the service registry.
func (r *Registry) unregisterProtocols(entry *pluginEntry) {
	if r.serviceRegistry == nil {
		return
	}
	for _, proto := range entry.plugin.Protocols() {
		if err := r.serviceRegistry.UnregisterService(proto.Name); err != nil {
			slog.Warn("plugin.unregister-protocol", "name", entry.plugin.Name(),
				"protocol", proto.Name, "error", err)
		}
	}
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
				slog.Error("plugin.panic", "name", pluginName, "method", "handler", "panic", rec)
				s.Reset()
				r.recordCrash(pluginName)
			}
		}()

		handler(serviceName, s)
	}
}

// callWithRecovery calls fn, recovering any panic and converting it to an error.
// Also records crashes for circuit breaker logic.
func (r *Registry) callWithRecovery(entry *pluginEntry, method string, fn func() error) (retErr error) {
	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("plugin.panic", "name", entry.plugin.Name(), "method", method, "panic", rec)
			retErr = fmt.Errorf("panic in %s: %v", method, rec)
			r.recordCrash(entry.plugin.Name())
		}
	}()
	return fn()
}

// recordCrash increments the crash counter and triggers the circuit breaker if needed.
// Circuit breaker: 3 panics within 5 minutes -> auto-disable.
func (r *Registry) recordCrash(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.plugins[name]
	if !ok {
		return
	}

	now := time.Now()
	if entry.firstCrash.IsZero() || now.Sub(entry.firstCrash) > circuitBreakerWindowDuration {
		// Reset window.
		entry.crashCount = 1
		entry.firstCrash = now
		return
	}

	entry.crashCount++
	if entry.crashCount >= circuitBreakerThreshold {
		slog.Warn("plugin.circuit-breaker", "name", name, "crash_count", entry.crashCount)
		// Force to stopped. Protocol unregistration happens inline.
		r.unregisterProtocols(entry)
		entry.state = StateStopped
	}
}

// buildInfo constructs an Info struct for introspection.
func (r *Registry) buildInfo(entry *pluginEntry) Info {
	p := entry.plugin

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
		State:      entry.state,
		Enabled:    entry.state == StateActive,
		Commands:   cmds,
		Routes:     routes,
		Protocols:  protos,
		ConfigKey:  p.ConfigSection(),
		CrashCount: entry.crashCount,
	}
}
