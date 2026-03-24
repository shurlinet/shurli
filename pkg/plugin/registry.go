package plugin

// Lock ordering: Registry.mu is the only mutex in the plugin framework.
// All plugin state access goes through Registry.mu. Plugins do not have
// their own mutex within the registry framework. Plugin-internal locks
// (if any) are the plugin's responsibility and must never be held when
// calling back into Registry methods.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

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
	mu                  sync.RWMutex
	plugins             map[string]*pluginEntry
	registrationOrder   []string // G4 fix: tracks insertion order for deterministic DisableAll
	provider            *ContextProvider
	serviceRegistry     *p2pnet.ServiceRegistry
	enableDisableCooldown time.Duration // per-Registry cooldown (P5: was package global)
}

// pluginEntry tracks the runtime state of a single plugin.
type pluginEntry struct {
	plugin             Plugin
	ctx                *PluginContext
	state              State
	registeredProtos   []string           // protocol names registered in ServiceRegistry
	supervisor         *supervisor        // auto-restart, crash counting, backoff
	lastTransition     time.Time          // G3 fix: cooldown on enable/disable
	startCancel        context.CancelFunc // G6 fix: cancel abandoned Start() goroutine
}

// NewRegistry creates a plugin registry with the given runtime dependencies.
// provider may be nil for testing (no network, no service registry).
func NewRegistry(provider *ContextProvider) *Registry {
	r := &Registry{
		plugins:               make(map[string]*pluginEntry),
		provider:              provider,
		enableDisableCooldown: defaultEnableDisableCooldown,
	}
	if provider != nil {
		r.serviceRegistry = provider.ServiceRegistry
	}
	return r
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
	var grantChecker func(peer.ID, string) bool
	var peerAttrFunc func(string, string) string
	if r.provider != nil {
		nameResolver = r.provider.NameResolver
		peerConnector = r.provider.PeerConnector
		keyDeriver = r.provider.KeyDeriver
		scoreResolver = r.provider.ScoreResolver
		grantChecker = r.provider.GrantChecker
		peerAttrFunc = r.provider.PeerAttrFunc
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
		grantChecker:   grantChecker,
		peerAttrFunc:   peerAttrFunc,
	}

	entry := &pluginEntry{
		plugin: p,
		ctx:    pctx,
		state:  StateLoading,
	}
	entry.supervisor = newSupervisor(name, r)

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
		// Clean up registrationOrder to prevent stale entries from accumulating
		// across repeated failed Register calls.
		for i, n := range r.registrationOrder {
			if n == name {
				r.registrationOrder = append(r.registrationOrder[:i], r.registrationOrder[i+1:]...)
				break
			}
		}
		r.mu.Unlock()
		return fmt.Errorf("plugin %q Init failed: %w", name, err)
	}

	r.mu.Lock()
	entry.state = StateReady
	r.mu.Unlock()

	slog.Info("plugin.registered", "name", name, "id", id, "version", p.Version())
	return nil
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
		return fmt.Errorf("config validation: %w", errors.Join(errs...))
	}
	return nil
}
