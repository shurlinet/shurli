package daemon

import (
	"encoding/json"
	"net/http"
)

// handlePluginList returns all registered plugins.
// GET /v1/plugins
func (s *Server) handlePluginList(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		RespondError(w, http.StatusServiceUnavailable, "plugin system not available")
		return
	}

	infos := s.registry.List()
	result := make([]PluginInfoResponse, 0, len(infos))
	for _, info := range infos {
		result = append(result, PluginInfoResponse{
			Name:       info.Name,
			Version:    info.Version,
			Type:       info.Type,
			State:      info.State.String(),
			Enabled:    info.Enabled,
			Commands:   info.Commands,
			Routes:     info.Routes,
			Protocols:  info.Protocols,
			ConfigKey:  info.ConfigKey,
			CrashCount: info.CrashCount,
		})
	}

	RespondJSON(w, http.StatusOK, result)
}

// handlePluginInfo returns details for a single plugin.
// GET /v1/plugins/{name}
func (s *Server) handlePluginInfo(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		RespondError(w, http.StatusServiceUnavailable, "plugin system not available")
		return
	}

	name := r.PathValue("name")
	info, err := s.registry.GetInfo(name)
	if err != nil {
		RespondError(w, http.StatusNotFound, err.Error())
		return
	}

	RespondJSON(w, http.StatusOK, PluginInfoResponse{
		Name:       info.Name,
		Version:    info.Version,
		Type:       info.Type,
		State:      info.State.String(),
		Enabled:    info.Enabled,
		Commands:   info.Commands,
		Routes:     info.Routes,
		Protocols:  info.Protocols,
		ConfigKey:  info.ConfigKey,
		CrashCount: info.CrashCount,
	})
}

// handlePluginEnable enables a plugin.
// POST /v1/plugins/{name}/enable
func (s *Server) handlePluginEnable(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		RespondError(w, http.StatusServiceUnavailable, "plugin system not available")
		return
	}

	name := r.PathValue("name")
	if err := s.registry.Enable(name); err != nil {
		RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{"status": "enabled", "name": name})
}

// handlePluginDisable disables a plugin.
// POST /v1/plugins/{name}/disable
func (s *Server) handlePluginDisable(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		RespondError(w, http.StatusServiceUnavailable, "plugin system not available")
		return
	}

	name := r.PathValue("name")
	if err := s.registry.Disable(name); err != nil {
		RespondError(w, http.StatusBadRequest, err.Error())
		return
	}

	RespondJSON(w, http.StatusOK, map[string]string{"status": "disabled", "name": name})
}

// handlePluginDisableAll disables all active plugins (kill switch).
// POST /v1/plugins/disable-all
func (s *Server) handlePluginDisableAll(w http.ResponseWriter, r *http.Request) {
	if s.registry == nil {
		RespondError(w, http.StatusServiceUnavailable, "plugin system not available")
		return
	}

	count, err := s.registry.DisableAll()
	if err != nil {
		// Still return the count since some plugins may have been disabled.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(DataResponse{Data: PluginDisableAllResponse{
			Disabled: count,
			Error:    err.Error(),
		}})
		return
	}

	RespondJSON(w, http.StatusOK, PluginDisableAllResponse{Disabled: count})
}
