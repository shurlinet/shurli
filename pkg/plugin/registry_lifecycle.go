package plugin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/shurlinet/shurli/pkg/sdk"
)

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
	if !entry.lastTransition.IsZero() && time.Since(entry.lastTransition) < r.enableDisableCooldown {
		r.mu.Unlock()
		return fmt.Errorf("plugin %q: enable/disable cooldown (%s remaining)",
			name, r.enableDisableCooldown-time.Since(entry.lastTransition))
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
	// callWithRecovery's panic handler can call recordCrashAndMaybeRestart (which acquires the lock).
	// A blocking Start() must not permanently block the daemon.
	// G11 fix: pass context to Start() so plugins can detect cancellation.
	startDone := make(chan error, 1) // buffered: sender must not block if timeout fires first
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
		pid := sdk.ProtocolID(proto.Name, proto.Version)
		declaredProtos[pid] = true
	}
	entry.ctx.declaredProtos = declaredProtos

	// Reset circuit breaker on successful enable.
	entry.supervisor.Reset()
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
	done := make(chan error, 1) // buffered: sender must not block if drain timeout fires first
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
	// Mark supervisor as disabled to prevent auto-restart after user-initiated disable.
	// The supervisor's TriggerRestart clears this flag before its own re-enable.
	entry.supervisor.SetDisabled()
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
		return count, fmt.Errorf("disable-all completed with %d errors: %w", len(errs), errors.Join(errs...))
	}
	return count, nil
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
		// A6 fix: per-plugin timeout prevents a hanging OnNetworkReady from
		// blocking all subsequent plugins. Uses same timeout as Start().
		done := make(chan error, 1) // buffered: sender must not block if timeout fires first
		go func() {
			done <- r.callWithRecovery(entry, "OnNetworkReady", func() error {
				return entry.plugin.OnNetworkReady()
			})
		}()

		select {
		case err := <-done:
			if err != nil {
				slog.Warn("plugin.network-ready-failed", "name", name, "error", err)
			} else {
				slog.Info("plugin.network-ready", "name", name)
			}
		case <-time.After(startTimeoutDuration):
			slog.Warn("plugin.network-ready-timeout", "name", name, "timeout", startTimeoutDuration.String())
		}
	}
	return nil
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
	// A7 fix: panic recovery so one panicking callback doesn't prevent subsequent reloads.
	for _, item := range items {
		r.mu.Lock()
		item.entry.ctx.configBytes = item.newBytes
		cb := item.entry.ctx.configReloadCb
		name := item.entry.plugin.Name()
		r.mu.Unlock()

		if cb != nil {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						slog.Error("plugin.config-reload-panic", "name", name,
							"panic", rec, "stack", string(debug.Stack()))
					}
				}()
				cb(item.newBytes)
			}()
		}
	}
}
