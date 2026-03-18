package plugin

import (
	"os"
	"path/filepath"
	"strings"
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
	// G5 fix: the registry should return an error for unknown plugin names in config.
	r := newTestRegistry()
	states := map[string]bool{
		"nonexistent": true,
	}
	err := r.ApplyConfig(states)
	if err == nil {
		t.Fatal("ApplyConfig should return error for unknown plugin names")
	}
	if !strings.Contains(err.Error(), "unknown plugin") {
		t.Fatalf("error should mention unknown plugin, got: %v", err)
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
	dir := t.TempDir()

	// 0700 should be acceptable.
	pluginDir := filepath.Join(dir, "plugins")
	if err := os.MkdirAll(pluginDir, 0700); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(pluginDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0700 {
		t.Errorf("expected 0700, got %04o", info.Mode().Perm())
	}

	// 0755 should be detected as insecure.
	badDir := filepath.Join(dir, "plugins-bad")
	if err := os.MkdirAll(badDir, 0755); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(badDir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() == 0700 {
		t.Error("expected insecure permissions to differ from 0700")
	}
}
