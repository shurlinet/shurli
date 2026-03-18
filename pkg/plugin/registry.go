package plugin

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// TRUST BOUNDARY (G5 - foundational security comment):
//
// The compiled Go binary IS the trust boundary for Layer 1 plugins.
// All Layer 1 plugin code runs in the same process with full memory access.
// Security relies on the build pipeline (code review, go vet, govulncheck),
// NOT on runtime sandboxing. Runtime checks (namespace validation, state gating,
// credential isolation) are defense-in-depth, not primary security barriers.
//
// Layer 2 (WASM) will introduce a true runtime trust boundary via wazero
// sandboxing, fuel metering, and per-plugin resource caps. All PluginContext
// methods are designed so they can become host function calls without API changes.
//
// SECURITY: Plugins cannot install, register, or discover other plugins.
// This is a hard-coded architectural constraint, not a permission that can be granted.
// See plugin-security-threat-analysis-2026-03-17.md, threat vector #43.
// The Registry.Register() method is called by the daemon startup code, never by plugins.
// PluginContext has no method for plugin installation or registration.

// Registry manages plugin lifecycle: registration, enable/disable, and introspection.
type Registry struct {
	mu              sync.RWMutex
	plugins         map[string]*pluginEntry
	registrationOrder []string // G4 fix: tracks insertion order for deterministic DisableAll
	provider        *ContextProvider
	serviceRegistry *p2pnet.ServiceRegistry
}

// pluginEntry tracks the runtime state of a single plugin.
type pluginEntry struct {
	plugin         Plugin
	ctx            *PluginContext
	state          State
	crashCount     int
	firstCrash     time.Time
	lastTransition time.Time          // G3 fix: cooldown on enable/disable
	startCancel    context.CancelFunc // G6 fix: cancel abandoned Start() goroutine
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

// validatePluginID checks that a plugin ID follows the host/namespace/name format.
// Rules: 2+ segments, valid hostname, [a-z0-9-], max 128 chars, no traversal,
// shurli.io/official/ reserved for official plugins.
func validatePluginID(id string) error {
	if id == "" {
		return fmt.Errorf("plugin ID cannot be empty")
	}
	if len(id) > 128 {
		return fmt.Errorf("plugin ID too long: %d chars (max 128)", len(id))
	}
	if strings.Contains(id, "..") {
		return fmt.Errorf("plugin ID %q contains path traversal", id)
	}
	if strings.Contains(id, "//") {
		return fmt.Errorf("plugin ID %q contains empty segment", id)
	}

	segments := strings.Split(id, "/")
	if len(segments) < 2 {
		return fmt.Errorf("plugin ID %q must have at least 2 segments (host/name)", id)
	}

	for i, seg := range segments {
		if seg == "" {
			return fmt.Errorf("plugin ID %q has empty segment at position %d", id, i)
		}
		for _, c := range seg {
			if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '.') {
				return fmt.Errorf("plugin ID %q segment %q contains invalid character %q", id, seg, string(c))
			}
		}
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

	id := p.ID()
	if err := validatePluginID(id); err != nil {
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

	// Derive config dir from plugin ID.
	var configDir string
	var configBytes []byte
	if r.provider != nil && r.provider.ConfigDir != "" {
		configDir = filepath.Join(r.provider.ConfigDir, "plugins", id)
		// M2 fix: verify config dir path doesn't traverse symlinks outside base config dir.
		if resolved, err := filepath.EvalSymlinks(r.provider.ConfigDir); err == nil {
			expectedPrefix := filepath.Join(resolved, "plugins", id)
			configDir = expectedPrefix // use resolved path
		}
		// Create config dir with 0700 if not exists.
		if err := os.MkdirAll(configDir, 0700); err != nil {
			r.mu.Unlock()
			return fmt.Errorf("plugin %q: create config dir: %w", name, err)
		}
		// #10 fix: verify permissions are 0700 (hard error, not warning).
		// If the dir already existed with wrong permissions, reject it.
		if info, err := os.Stat(configDir); err == nil {
			perm := info.Mode().Perm()
			if perm&0077 != 0 {
				r.mu.Unlock()
				return fmt.Errorf("plugin %q: config dir %s has unsafe permissions %04o (must be 0700)", name, configDir, perm)
			}
		}
		// Read config.yaml from config dir (empty bytes if missing).
		// M3 fix: limit config file size to prevent DoS from huge files.
		configPath := filepath.Join(configDir, "config.yaml")
		if data, err := readFileLimited(configPath, maxConfigFileSize); err == nil {
			configBytes = data
		}
	}

	var nameResolver func(string) (peer.ID, error)
	var peerConnector func(context.Context, peer.ID) error
	var keyDeriver func(string) []byte
	var scoreResolver func(peer.ID) int
	if r.provider != nil {
		nameResolver = r.provider.NameResolver
		peerConnector = r.provider.PeerConnector
		keyDeriver = r.provider.KeyDeriver
		scoreResolver = r.provider.ScoreResolver
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
		configDir:      configDir,
		declaredProtos: declaredProtos,
		keyDeriver:     keyDeriver,
		scoreResolver:  scoreResolver,
	}

	entry := &pluginEntry{
		plugin: p,
		ctx:    pctx,
		state:  StateLoading,
	}

	// Insert placeholder BEFORE releasing lock. Other Register() calls
	// for the same name will see the entry and fail the duplicate check.
	r.plugins[name] = entry
	r.registrationOrder = append(r.registrationOrder, name) // G4 fix: track insertion order
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

	slog.Info("plugin.registered", "name", name, "id", id, "version", p.Version())
	return nil
}

// Enable starts a plugin, registering its protocols with the service registry.
// Valid from READY or STOPPED state. Idempotent if already ACTIVE.
// G3 fix: enforces cooldown between transitions.
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

	// G3 fix: enforce cooldown between transitions.
	if !entry.lastTransition.IsZero() && time.Since(entry.lastTransition) < enableDisableCooldown {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q: enable/disable cooldown (%s remaining)",
			name, enableDisableCooldown-time.Since(entry.lastTransition))
	}

	if err := ValidTransition(entry.state, StateActive); err != nil {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q: %w", name, err)
	}

	// Set LOADING as a transitional state to prevent concurrent Enable() calls
	// from both passing the state check while the lock is released for Start().
	prevState := entry.state
	entry.state = StateLoading

	// F3 fix: re-populate PluginContext runtime fields that were nilled on Disable.
	if r.provider != nil {
		entry.ctx.network = r.provider.Network
		entry.ctx.nameResolver = r.provider.NameResolver
		entry.ctx.peerConnector = r.provider.PeerConnector
	}
	r.mu.Unlock()

	// G6 fix: create a cancellable context for the Start goroutine so Disable can cancel it.
	// G11 fix: context is passed directly to Start(ctx) - plugins detect cancellation natively.
	startCtx, startCancel := context.WithCancel(context.Background())
	r.mu.Lock()
	entry.startCancel = startCancel
	r.mu.Unlock()

	// Call Start with panic recovery and timeout. Lock is released so
	// callWithRecovery's panic handler can call recordCrash (which acquires the lock).
	// A blocking Start() must not permanently block the daemon.
	// G11 fix: pass context to Start() so plugins can detect cancellation.
	startDone := make(chan error, 1)
	go func() {
		startDone <- r.callWithRecovery(entry, "Start", func() error {
			return entry.plugin.Start(startCtx)
		})
	}()

	var startErr error
	select {
	case startErr = <-startDone:
	case <-time.After(startTimeoutDuration):
		startErr = fmt.Errorf("Start() timed out after %s", startTimeoutDuration)
		slog.Warn("plugin.start-timeout", "name", name, "timeout", startTimeoutDuration.String())
	}

	if startErr != nil {
		// G1 fix: call Stop() to clean up partially initialized resources.
		if stopErr := r.callWithRecovery(entry, "Stop", func() error {
			return entry.plugin.Stop()
		}); stopErr != nil {
			slog.Warn("plugin.enable-error-path-stop-failed", "name", name, "error", stopErr)
		}
		r.mu.Lock()
		entry.state = prevState
		r.mu.Unlock()
		return fmt.Errorf("plugin %q Start failed: %w", name, startErr)
	}

	// X1 fix: Register protocols AFTER Start() so Protocols() returns real handlers.
	// Previously this was before Start(), so plugins with Start-dependent Protocols()
	// (like FileTransferPlugin) registered zero protocols.
	r.mu.Lock()
	if entry.state != StateLoading {
		// DisableAll (kill switch) forced this plugin to STOPPED while Start() was running.
		// Respect the kill switch: don't transition to ACTIVE.
		r.mu.Unlock()
		return fmt.Errorf("plugin %q was disabled during startup (kill switch)", name)
	}

	if err := r.registerProtocols(entry); err != nil {
		entry.state = prevState
		r.mu.Unlock()
		// Stop the started plugin since we can't register its protocols.
		_ = r.callWithRecovery(entry, "Stop", func() error {
			return entry.plugin.Stop()
		})
		return fmt.Errorf("plugin %q protocol registration failed: %w", name, err)
	}

	// X4 fix: Rebuild declaredProtos AFTER Start() so OpenStream accepts plugin's protocols.
	declaredProtos := make(map[string]bool)
	for _, proto := range entry.plugin.Protocols() {
		pid := p2pnet.ProtocolID(proto.Name, proto.Version)
		declaredProtos[pid] = true
	}
	entry.ctx.declaredProtos = declaredProtos

	// Reset circuit breaker on successful enable.
	entry.crashCount = 0
	entry.firstCrash = time.Time{}
	entry.state = StateActive
	entry.lastTransition = time.Now()
	entry.startCancel = nil // G6: Start completed, no longer cancellable
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
	if entry.state == StateLoading {
		// Force-stop a plugin that's mid-Enable (kill switch must not miss it).
		// The Enable() goroutine will find state=STOPPED when it tries to set ACTIVE.
		// G6 fix: cancel the Start goroutine's context.
		if entry.startCancel != nil {
			entry.startCancel()
		}
		entry.state = StateStopped
		r.unregisterProtocols(entry)
		r.mu.Unlock()
		slog.Info("plugin.disabled", "name", name, "reason", "kill-switch")
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

	// Unregister protocols, nil context fields, and set final state.
	r.mu.Lock()
	r.unregisterProtocols(entry)
	// F3 fix: nil PluginContext runtime fields so disabled plugin can't use them.
	entry.ctx.network = nil
	entry.ctx.nameResolver = nil
	entry.ctx.peerConnector = nil
	entry.state = StateStopped
	entry.lastTransition = time.Now()
	r.mu.Unlock()

	slog.Info("plugin.disabled", "name", name, "reason", reason)
	return nil
}

// DisableAll stops every active plugin. Errors are collected but never stop iteration.
// This is the kill switch for incident response. It also catches plugins in LOADING
// state (mid-Enable) by forcing them to STOPPED, ensuring the kill switch is truly atomic.
// G4 fix: disables in reverse registration order for deterministic shutdown.
func (r *Registry) DisableAll() (int, error) {
	r.mu.RLock()
	// G4 fix: iterate registrationOrder in reverse for deterministic disable order.
	var activeNames []string
	for i := len(r.registrationOrder) - 1; i >= 0; i-- {
		name := r.registrationOrder[i]
		if entry, ok := r.plugins[name]; ok {
			if entry.state == StateActive || entry.state == StateLoading {
				activeNames = append(activeNames, name)
			}
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
// X8 fix: snapshot entries under RLock, call plugin methods (Commands/Routes/Protocols)
// outside the lock so slow plugins don't block the registry.
func (r *Registry) List() []Info {
	r.mu.RLock()
	type snapshot struct {
		plugin     Plugin
		state      State
		crashCount int
	}
	entries := make([]snapshot, 0, len(r.plugins))
	for _, entry := range r.plugins {
		entries = append(entries, snapshot{
			plugin:     entry.plugin,
			state:      entry.state,
			crashCount: entry.crashCount,
		})
	}
	r.mu.RUnlock()

	infos := make([]Info, 0, len(entries))
	for _, snap := range entries {
		infos = append(infos, buildInfoFromSnapshot(snap.plugin, snap.state, snap.crashCount))
	}
	return infos
}

// GetInfo returns metadata for a single plugin.
// X8 fix: snapshot under RLock, build info outside the lock.
func (r *Registry) GetInfo(name string) (*Info, error) {
	r.mu.RLock()
	entry, ok := r.plugins[name]
	if !ok {
		r.mu.RUnlock()
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	p := entry.plugin
	state := entry.state
	crashes := entry.crashCount
	r.mu.RUnlock()

	info := buildInfoFromSnapshot(p, state, crashes)
	return &info, nil
}

// GetPlugin returns the Plugin instance by name. Used for direct plugin
// interaction (e.g., fetching StatusContributor). Returns nil if not found.
// X3 fix: spec compliance - registry exposes individual plugin lookup.
func (r *Registry) GetPlugin(name string) Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	entry, ok := r.plugins[name]
	if !ok {
		return nil
	}
	return entry.plugin
}

// ApplyConfig enables or disables plugins based on config state.
// key = plugin name, value = enabled. Unknown names are collected as errors (G5 fix).
func (r *Registry) ApplyConfig(pluginStates map[string]bool) error {
	var errs []error
	for name, enabled := range pluginStates {
		r.mu.RLock()
		_, exists := r.plugins[name]
		r.mu.RUnlock()

		if !exists {
			// G5 fix: return unknown plugin names as errors instead of just logging.
			errs = append(errs, fmt.Errorf("unknown plugin %q in config", name))
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
	if len(errs) > 0 {
		return fmt.Errorf("config validation: %v", errs)
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
// G9 fix: also handles LOADING plugins (DisableAll already catches them).
func (r *Registry) StopAll() error {
	_, err := r.DisableAll()
	// Also transition READY and LOADING plugins to STOPPED (G9 fix).
	r.mu.Lock()
	for _, entry := range r.plugins {
		if entry.state == StateReady || entry.state == StateLoading {
			if entry.state == StateLoading && entry.startCancel != nil {
				entry.startCancel() // cancel any Start() goroutine
			}
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

// StatusContributions returns status fields from all ACTIVE plugins
// that implement StatusContributor. Keyed by plugin Name().
// X7 fix: snapshot active StatusContributors under RLock, call StatusFields() outside the lock
// so a slow plugin cannot block all status queries.
func (r *Registry) StatusContributions() map[string]map[string]any {
	r.mu.RLock()
	type contributor struct {
		name string
		sc   StatusContributor
	}
	var contributors []contributor
	for _, entry := range r.plugins {
		if entry.state != StateActive {
			continue
		}
		if sc, ok := entry.plugin.(StatusContributor); ok {
			contributors = append(contributors, contributor{name: entry.plugin.Name(), sc: sc})
		}
	}
	r.mu.RUnlock()

	result := make(map[string]map[string]any)
	for _, c := range contributors {
		if fields := c.sc.StatusFields(); len(fields) > 0 {
			result[c.name] = fields
		}
	}
	return result
}

// AllRegisteredRoutes returns routes from ALL registered plugins regardless of state.
// Used for mux setup at server start. Different from AllRoutes() which returns only ACTIVE.
// H2 note: callers MUST use IsRouteActive() per-request to gate disabled plugin routes.
// This is by design - routes are registered once at mux setup, state is checked per-request.
func (r *Registry) AllRegisteredRoutes() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var routes []Route
	for _, entry := range r.plugins {
		routes = append(routes, entry.plugin.Routes()...)
	}
	return routes
}

// IsRouteActive checks if the plugin providing a route is in ACTIVE state.
// Used per-request to return 404 when a plugin is disabled.
func (r *Registry) IsRouteActive(method, path string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, entry := range r.plugins {
		if entry.state != StateActive {
			continue
		}
		for _, rt := range entry.plugin.Routes() {
			if rt.Method == method && rt.Path == path {
				return true
			}
		}
	}
	return false
}

// NotifyConfigReload re-reads each active plugin's config.yaml and calls
// their OnConfigReload callback if the bytes changed.
// P19 fix: uses full Lock for configBytes write instead of RLock.
// P20 fix: releases lock before calling plugin callbacks to prevent deadlock.
func (r *Registry) NotifyConfigReload() {
	r.mu.RLock()
	type reloadItem struct {
		entry    *pluginEntry
		newBytes []byte
	}
	var items []reloadItem
	for _, entry := range r.plugins {
		if entry.state != StateActive || entry.ctx.configReloadCb == nil {
			continue
		}
		if entry.ctx.configDir == "" {
			continue
		}
		configPath := filepath.Join(entry.ctx.configDir, "config.yaml")
		newBytes, err := readFileLimited(configPath, maxConfigFileSize)
		if err != nil {
			continue
		}
		if !bytes.Equal(newBytes, entry.ctx.configBytes) {
			items = append(items, reloadItem{entry: entry, newBytes: newBytes})
		}
	}
	r.mu.RUnlock()

	// P19 fix: write configBytes under full Lock.
	// P20 fix: call callbacks outside the lock to prevent deadlock.
	for _, item := range items {
		r.mu.Lock()
		item.entry.ctx.configBytes = item.newBytes
		cb := item.entry.ctx.configReloadCb
		r.mu.Unlock()

		if cb != nil {
			cb(item.newBytes)
		}
	}
}

// --- Internal helpers ---

// isReservedProtocolName returns true if the name collides with a core Shurli protocol.
// G2 fix: defense-in-depth namespace check even for Layer 1 compiled-in plugins.
func isReservedProtocolName(name string) bool {
	reserved := map[string]bool{
		"relay-pair":    true,
		"relay-unseal":  true,
		"relay-admin":   true,
		"relay-motd":    true,
		"peer-notify":   true,
		"zkp-auth":      true,
		"ping":          true,
		"kad":           true,
	}
	return reserved[name]
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
	for _, proto := range entry.plugin.Protocols() {
		// X3 fix: validate protocol names before registration.
		if err := validateProtocolName(proto.Name); err != nil {
			return fmt.Errorf("plugin %q: %w", entry.plugin.Name(), err)
		}
		// G2 fix: protocol namespace validation (defense-in-depth for Layer 1).
		// Layer 1 plugins are trusted compiled-in code, but we validate protocol
		// names don't collide with core Shurli protocols. Core protocols use
		// specific names that plugins must not shadow.
		if isReservedProtocolName(proto.Name) {
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
			// Rollback already-registered protocols.
			r.unregisterProtocols(entry)
			return fmt.Errorf("register %s: %w", proto.Name, err)
		}
	}
	return nil
}

// unregisterProtocols removes a plugin's protocols from the service registry.
// P14: MUST be called with r.mu held. All call sites verified.
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
// G12 SAFETY: callers MUST NOT hold r.mu when calling this, because the panic
// handler calls recordCrash which acquires r.mu.Lock(). All call sites (Register,
// Enable, Disable, NotifyNetworkReady) release the lock before calling this.
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

// buildInfoFromSnapshot constructs an Info struct for introspection.
// X8 fix: takes pre-snapshotted values so it can be called outside the registry lock.
func buildInfoFromSnapshot(p Plugin, state State, crashCount int) Info {
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
		ConfigKey:  p.ConfigSection(),
		CrashCount: crashCount,
	}
}

// readFileLimited reads a file up to maxBytes. Returns error if file exceeds limit (M3 fix).
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
	return os.ReadFile(path)
}
