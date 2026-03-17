package plugin

// ParsePluginStates converts a PluginsConfig (from internal/config) into the
// simple map[string]bool format used by Registry.ApplyConfig.
// This function lives here to avoid pkg/plugin importing internal/config.
// The caller (cmd_daemon.go) extracts the data and passes it through.
//
// This is intentionally a standalone helper that works on primitive types,
// keeping the boundary between config parsing and plugin management clean.
func ParsePluginStates(entries map[string]bool) map[string]bool {
	if entries == nil {
		return make(map[string]bool)
	}
	// Pass through - the conversion from config types to map[string]bool
	// is done by the caller (cmd_daemon.go). This function exists as a
	// documentation anchor and for future validation logic.
	result := make(map[string]bool, len(entries))
	for k, v := range entries {
		result[k] = v
	}
	return result
}
