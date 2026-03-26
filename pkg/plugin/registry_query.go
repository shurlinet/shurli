package plugin

import (
	"fmt"
)

// List returns metadata for all registered plugins.
// X8 fix: snapshot entries under RLock, call plugin methods (Commands/Routes/Protocols)
// outside the lock so slow plugins don't block the registry.
func (r *Registry) List() []Info {
	r.mu.RLock()
	type snapshot struct {
		plugin          Plugin
		state           State
		crashCount      int
		lifetimeCrashes int
	}
	entries := make([]snapshot, 0, len(r.plugins))
	for _, entry := range r.plugins {
		entries = append(entries, snapshot{
			plugin:          entry.plugin,
			state:           entry.state,
			crashCount:      entry.supervisor.crashCount,
			lifetimeCrashes: entry.supervisor.lifetimeCrashes,
		})
	}
	r.mu.RUnlock()

	infos := make([]Info, 0, len(entries))
	for _, snap := range entries {
		infos = append(infos, buildInfoFromSnapshot(snap.plugin, snap.state, snap.crashCount, snap.lifetimeCrashes))
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
	crashes := entry.supervisor.crashCount
	lifetime := entry.supervisor.lifetimeCrashes
	r.mu.RUnlock()

	info := buildInfoFromSnapshot(p, state, crashes, lifetime)
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
