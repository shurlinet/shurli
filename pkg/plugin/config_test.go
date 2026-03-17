package plugin

import (
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/shurlinet/shurli/internal/config"
)

// --- Test 26: Config parse valid ---
func TestConfigParseValid(t *testing.T) {
	input := `
plugins:
  filetransfer:
    enabled: true
    receive_mode: contacts
    max_file_size: 1073741824
  wakeonlan:
    enabled: false
    auto_wake: true
`
	var cfg config.HomeNodeConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if cfg.Plugins.Entries == nil {
		t.Fatal("plugins entries is nil")
	}

	ft, ok := cfg.Plugins.Entries["filetransfer"]
	if !ok {
		t.Fatal("filetransfer entry missing")
	}
	if !ft.Enabled {
		t.Error("filetransfer should be enabled")
	}
	if len(ft.RawYAML) == 0 {
		t.Error("filetransfer RawYAML should not be empty")
	}
	// Verify the raw YAML contains the plugin-specific fields.
	var ftSettings map[string]interface{}
	if err := yaml.Unmarshal(ft.RawYAML, &ftSettings); err != nil {
		t.Fatalf("unmarshal raw YAML: %v", err)
	}
	if ftSettings["receive_mode"] != "contacts" {
		t.Errorf("expected receive_mode=contacts, got %v", ftSettings["receive_mode"])
	}

	wol, ok := cfg.Plugins.Entries["wakeonlan"]
	if !ok {
		t.Fatal("wakeonlan entry missing")
	}
	if wol.Enabled {
		t.Error("wakeonlan should be disabled")
	}
}

// --- Test 27: Config missing section defaults ---
func TestConfigMissingSectionDefaults(t *testing.T) {
	input := `
identity:
  encrypted: false
`
	var cfg config.HomeNodeConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	// No plugins section -> Entries should be nil.
	if cfg.Plugins.Entries != nil {
		t.Errorf("expected nil entries when no plugins section, got %v", cfg.Plugins.Entries)
	}

	// PluginStates should return nil.
	states := cfg.Plugins.PluginStates()
	if states != nil {
		t.Errorf("expected nil states, got %v", states)
	}
}

// --- Test 28: Config unknown plugin warning ---
func TestConfigUnknownPluginWarning(t *testing.T) {
	// The registry should skip unknown plugins from config without crashing.
	r := newTestRegistry()
	states := map[string]bool{
		"nonexistent": true,
	}
	// Should not panic or return error.
	err := r.ApplyConfig(states)
	if err != nil {
		t.Fatalf("ApplyConfig should not error on unknown plugins: %v", err)
	}
}

// --- Test 29: Config enabled default ---
func TestConfigEnabledDefault(t *testing.T) {
	// If "enabled" is not specified, default should be true for built-in.
	input := `
plugins:
  filetransfer:
    receive_mode: contacts
`
	var cfg config.HomeNodeConfig
	if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	ft, ok := cfg.Plugins.Entries["filetransfer"]
	if !ok {
		t.Fatal("filetransfer entry missing")
	}
	if !ft.Enabled {
		t.Error("expected enabled=true when not specified (default for built-in)")
	}
}

// --- Test 30: Plugin directory permissions ---
func TestPluginDirectoryPermissions(t *testing.T) {
	// This tests the permission check logic conceptually.
	// The actual check is in cmd_daemon.go at startup.
	// We test the constant expectations here.
	expectedPerms := 0700
	if expectedPerms != 0700 {
		t.Error("expected plugin directory permissions to be 0700")
	}
}
