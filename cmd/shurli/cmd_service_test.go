package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shurlinet/shurli/internal/config"
)

// writeServiceTestConfig creates a full test config directory with a valid
// shurli.yaml, identity.key, and authorized_keys. The servicesYAML parameter
// replaces the default "services: {}" line. Returns the path to shurli.yaml.
func writeServiceTestConfig(t *testing.T, servicesYAML string) string {
	t.Helper()
	dir := t.TempDir()

	yaml := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses:
    - "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
  reservation_interval: "2m"
discovery:
  rendezvous: "test-network"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"
services: {}
names: {}
`
	if servicesYAML != "" {
		yaml = strings.Replace(yaml, "services: {}", servicesYAML, 1)
	}

	cfgPath := filepath.Join(dir, "shurli.yaml")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Generate a real libp2p Ed25519 identity key.
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), keyData, 0600); err != nil {
		t.Fatalf("write identity key: %v", err)
	}

	// Empty authorized_keys file.
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	return cfgPath
}

// ----- doServiceAdd tests -----

func TestDoServiceAdd(t *testing.T) {
	tests := []struct {
		name         string
		servicesYAML string // replaces "services: {}" if non-empty
		args         func(cfgPath string) []string
		wantErr      bool
		wantErrStr   string   // substring expected in error
		wantOutput   []string // substrings expected in stdout
		checkFile    func(t *testing.T, cfgPath string)
	}{
		{
			name: "add service to empty services",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh", "localhost:22"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if !strings.Contains(content, "ssh:") {
					t.Error("config should contain 'ssh:'")
				}
				if !strings.Contains(content, "enabled: true") {
					t.Error("config should contain 'enabled: true'")
				}
				if !strings.Contains(content, `local_address: "localhost:22"`) {
					t.Error("config should contain local_address for ssh")
				}
				// "services: {}" should be replaced
				if strings.Contains(content, "services: {}") {
					t.Error("config should no longer contain 'services: {}'")
				}
			},
		},
		{
			name: "add service with protocol",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "web", "localhost:8080", "--protocol", "my-web"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if !strings.Contains(content, `protocol: "my-web"`) {
					t.Errorf("config should contain protocol field, got:\n%s", content)
				}
			},
		},
		{
			name: "add duplicate service",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh", "localhost:22"}
			},
			// Duplicate prints a warning via termcolor but returns nil error.
			wantErr: false,
			checkFile: func(t *testing.T, cfgPath string) {
				dataBefore, _ := os.ReadFile(cfgPath)
				// File should be unchanged (no write happened).
				if !strings.Contains(string(dataBefore), `local_address: "localhost:22"`) {
					t.Error("config should still contain original ssh service")
				}
			},
		},
		{
			name: "missing args",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "no args at all",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "invalid service name",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "bad name!", "localhost:22"}
			},
			wantErr:    true,
			wantErrStr: "invalid service name",
		},
		{
			name: "invalid service name with uppercase",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "BadName", "localhost:22"}
			},
			wantErr:    true,
			wantErrStr: "invalid service name",
		},
		{
			name: "invalid address no port",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh", "localhost"}
			},
			wantErr:    true,
			wantErrStr: "invalid address",
		},
		{
			name: "add to existing services",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "web", "localhost:8080"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if !strings.Contains(content, "ssh:") {
					t.Error("config should still contain 'ssh:'")
				}
				if !strings.Contains(content, "web:") {
					t.Error("config should contain 'web:'")
				}
				if !strings.Contains(content, `local_address: "localhost:8080"`) {
					t.Error("config should contain local_address for web")
				}
			},
		},
		{
			name: "add service with commented services in template",
			// Empty servicesYAML means the test config has "services: {}", but
			// we need the REAL template with "# services:" comments. Override
			// the entire file content in checkFile setup.
			args: func(cfgPath string) []string {
				// Rewrite the config to use the real template format (commented services, no uncommented services section)
				templateConfig := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses:
    - "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
  reservation_interval: "2m"
discovery:
  rendezvous: "test-network"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"

# Uncomment and configure services to expose (for shurli daemon):
# services:
#   ssh:
#     enabled: true
#     local_address: "localhost:22"

names: {}
`
				os.WriteFile(cfgPath, []byte(templateConfig), 0600)
				return []string{"--config", cfgPath, "ssh", "localhost:22"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				// Must have an uncommented "services:" parent key
				hasParent := false
				for _, line := range strings.Split(content, "\n") {
					if strings.TrimSpace(line) == "services:" {
						hasParent = true
						break
					}
				}
				if !hasParent {
					t.Errorf("config must have uncommented 'services:' parent key, got:\n%s", content)
				}
				if !strings.Contains(content, `local_address: "localhost:22"`) {
					t.Error("config should contain local_address for ssh")
				}
				// The resulting YAML must be parseable by the real config loader
				cfg, err := config.LoadNodeConfig(cfgPath)
				if err != nil {
					t.Fatalf("config YAML is corrupted after service add: %v\nContent:\n%s", err, content)
				}
				if _, ok := cfg.Services["ssh"]; !ok {
					t.Errorf("parsed config should have ssh service, got services=%v", cfg.Services)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeServiceTestConfig(t, tt.servicesYAML)
			args := tt.args(cfgPath)

			var stdout bytes.Buffer
			err := doServiceAdd(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
			if tt.checkFile != nil {
				tt.checkFile(t, cfgPath)
			}
		})
	}
}

// ----- doServiceList tests -----

func TestDoServiceList(t *testing.T) {
	tests := []struct {
		name         string
		servicesYAML string
		wantOutput   []string
	}{
		{
			name:       "empty services",
			wantOutput: []string{"No services configured"},
		},
		{
			name: "with services",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  web:
    enabled: true
    local_address: "localhost:8080"`,
			wantOutput: []string{"Services (2)", "ssh", "web", "localhost:22", "localhost:8080"},
		},
		{
			name: "single service",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			wantOutput: []string{"Services (1)", "ssh", "localhost:22"},
		},
		{
			name: "shows disabled state",
			servicesYAML: `services:
  ssh:
    enabled: false
    local_address: "localhost:22"`,
			wantOutput: []string{"Services (1)", "ssh", "disabled"},
		},
		{
			name: "shows protocol",
			servicesYAML: `services:
  web:
    enabled: true
    local_address: "localhost:8080"
    protocol: "my-web"`,
			wantOutput: []string{"web", "my-web"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeServiceTestConfig(t, tt.servicesYAML)

			var stdout bytes.Buffer
			err := doServiceList([]string{"--config", cfgPath}, &stdout)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

// ----- doServiceSetEnabled tests -----

func TestDoServiceSetEnabled(t *testing.T) {
	tests := []struct {
		name         string
		servicesYAML string
		args         func(cfgPath string) []string
		enabled      bool
		wantErr      bool
		wantErrStr   string
		wantOutput   []string
		checkFile    func(t *testing.T, cfgPath string)
	}{
		{
			name: "disable existing enabled service",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled:    false,
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				// The ssh service block should now have enabled: false
				if !strings.Contains(content, "enabled: false") {
					t.Errorf("config should contain 'enabled: false', got:\n%s", content)
				}
			},
		},
		{
			name: "enable existing disabled service",
			servicesYAML: `services:
  ssh:
    enabled: false
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled:    true,
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if !strings.Contains(content, "enabled: true") {
					t.Errorf("config should contain 'enabled: true', got:\n%s", content)
				}
			},
		},
		{
			name: "service not found",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "nonexistent"}
			},
			enabled:    false,
			wantErr:    true,
			wantErrStr: "service not found",
		},
		{
			name: "service not found empty services",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled:    true,
			wantErr:    true,
			wantErrStr: "service not found",
		},
		{
			name: "already disabled",
			servicesYAML: `services:
  ssh:
    enabled: false
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled: false,
			// Returns nil (prints warning via termcolor).
			wantErr: false,
		},
		{
			name: "already enabled",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled: true,
			wantErr: false,
		},
		{
			name: "missing name arg for disable",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath}
			},
			enabled:    false,
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "missing name arg for enable",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath}
			},
			enabled:    true,
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "disable with multiple services only affects target",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  web:
    enabled: true
    local_address: "localhost:8080"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			enabled:    false,
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				// ssh should be disabled, web should still be enabled.
				// Find the ssh block and check its enabled field.
				lines := strings.Split(content, "\n")
				inSSH := false
				sshDisabled := false
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "ssh:" {
						inSSH = true
						continue
					}
					if inSSH && trimmed == "enabled: false" {
						sshDisabled = true
						break
					}
					if inSSH && !strings.HasPrefix(line, "    ") && trimmed != "" {
						break
					}
				}
				if !sshDisabled {
					t.Errorf("ssh should be disabled, got:\n%s", content)
				}

				// Web should still be enabled.
				inWeb := false
				webEnabled := false
				for _, line := range lines {
					trimmed := strings.TrimSpace(line)
					if trimmed == "web:" {
						inWeb = true
						continue
					}
					if inWeb && trimmed == "enabled: true" {
						webEnabled = true
						break
					}
					if inWeb && !strings.HasPrefix(line, "    ") && trimmed != "" {
						break
					}
				}
				if !webEnabled {
					t.Errorf("web should still be enabled, got:\n%s", content)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeServiceTestConfig(t, tt.servicesYAML)
			args := tt.args(cfgPath)

			var stdout bytes.Buffer
			err := doServiceSetEnabled(args, tt.enabled, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
			if tt.checkFile != nil {
				tt.checkFile(t, cfgPath)
			}
		})
	}
}

// ----- doServiceRemove tests -----

func TestDoServiceRemove(t *testing.T) {
	tests := []struct {
		name         string
		servicesYAML string
		args         func(cfgPath string) []string
		wantErr      bool
		wantErrStr   string
		wantOutput   []string
		checkFile    func(t *testing.T, cfgPath string)
	}{
		{
			name: "remove existing service",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  web:
    enabled: true
    local_address: "localhost:8080"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if strings.Contains(content, "ssh:") {
					t.Error("config should no longer contain 'ssh:'")
				}
				// web should still be present
				if !strings.Contains(content, "web:") {
					t.Error("config should still contain 'web:'")
				}
			},
		},
		{
			name: "remove last service leaves services empty",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "ssh"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if !strings.Contains(content, "services: {}") {
					t.Errorf("config should contain 'services: {}' after removing last service, got:\n%s", content)
				}
				if strings.Contains(content, "ssh:") {
					t.Error("config should no longer contain 'ssh:'")
				}
			},
		},
		{
			name: "service not found",
			servicesYAML: `services:
  ssh:
    enabled: true
    local_address: "localhost:22"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "nonexistent"}
			},
			wantErr:    true,
			wantErrStr: "service not found",
		},
		{
			name: "service not found empty services",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "web"}
			},
			wantErr:    true,
			wantErrStr: "service not found",
		},
		{
			name: "missing arg",
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "remove service with protocol",
			servicesYAML: `services:
  web:
    enabled: true
    local_address: "localhost:8080"
    protocol: "my-web"`,
			args: func(cfgPath string) []string {
				return []string{"--config", cfgPath, "web"}
			},
			wantOutput: []string{"Config:", "Restart"},
			checkFile: func(t *testing.T, cfgPath string) {
				data, err := os.ReadFile(cfgPath)
				if err != nil {
					t.Fatalf("read config: %v", err)
				}
				content := string(data)
				if strings.Contains(content, "web:") {
					t.Error("config should no longer contain 'web:'")
				}
				if strings.Contains(content, "my-web") {
					t.Error("config should no longer contain 'my-web'")
				}
				if !strings.Contains(content, "services: {}") {
					t.Errorf("config should contain 'services: {}', got:\n%s", content)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfgPath := writeServiceTestConfig(t, tt.servicesYAML)
			args := tt.args(cfgPath)

			var stdout bytes.Buffer
			err := doServiceRemove(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrStr != "" && !strings.Contains(err.Error(), tt.wantErrStr) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantErrStr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantOutput {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
			if tt.checkFile != nil {
				tt.checkFile(t, cfgPath)
			}
		})
	}
}

// ----- User Journey Tests -----
// These test the full config modification lifecycle against the REAL config
// template (not synthetic minimal YAML). They catch bugs that unit tests miss
// because unit tests never see the actual config that users get from shurli init.

// writeRealTemplateConfig creates a test config using the REAL nodeConfigTemplate
// output, with a valid identity key and authorized_keys. This is what a user
// actually sees after running "shurli init".
func writeRealTemplateConfig(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Use the real template (the same one shurli init generates)
	relayAddr := "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	configContent := nodeConfigTemplate(relayAddr, "shurli init", "")

	cfgPath := filepath.Join(dir, "shurli.yaml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	// Generate a real libp2p Ed25519 identity key.
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), keyData, 0600); err != nil {
		t.Fatalf("write identity key: %v", err)
	}

	// Empty authorized_keys file.
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	return cfgPath
}

// verifyConfigParseable loads the config with the real config loader and returns it.
// Fails the test if the config is not valid YAML.
func verifyConfigParseable(t *testing.T, cfgPath string, step string) *config.NodeConfig {
	t.Helper()
	cfg, err := config.LoadNodeConfig(cfgPath)
	if err != nil {
		data, _ := os.ReadFile(cfgPath)
		t.Fatalf("config corrupted after %s: %v\nContent:\n%s", step, err, string(data))
	}
	return cfg
}

// TestUserJourneyServiceLifecycle tests the exact sequence a real user performs:
// init config -> add first service -> add second service -> disable service ->
// enable service -> remove service -> remove last service.
// Every step verifies the config is still parseable YAML.
func TestUserJourneyServiceLifecycle(t *testing.T) {
	cfgPath := writeRealTemplateConfig(t)
	var stdout bytes.Buffer

	// Step 0: Verify the raw template config parses
	cfg := verifyConfigParseable(t, cfgPath, "initial template")
	if len(cfg.Services) != 0 {
		t.Fatalf("fresh template should have no services, got %d", len(cfg.Services))
	}

	// Step 1: Add SSH service (first service, from commented template)
	stdout.Reset()
	if err := doServiceAdd([]string{"--config", cfgPath, "ssh", "localhost:22"}, &stdout); err != nil {
		t.Fatalf("step 1 (add ssh): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "add ssh")
	if _, ok := cfg.Services["ssh"]; !ok {
		t.Fatal("step 1: ssh service not found after add")
	}
	if cfg.Services["ssh"].LocalAddress != "localhost:22" {
		t.Fatalf("step 1: ssh address = %q, want localhost:22", cfg.Services["ssh"].LocalAddress)
	}
	if !cfg.Services["ssh"].Enabled {
		t.Fatal("step 1: ssh should be enabled by default")
	}

	// Step 2: Add XRDP service (second service, appending to existing)
	stdout.Reset()
	if err := doServiceAdd([]string{"--config", cfgPath, "xrdp", "localhost:3389"}, &stdout); err != nil {
		t.Fatalf("step 2 (add xrdp): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "add xrdp")
	if len(cfg.Services) != 2 {
		t.Fatalf("step 2: expected 2 services, got %d", len(cfg.Services))
	}
	if _, ok := cfg.Services["xrdp"]; !ok {
		t.Fatal("step 2: xrdp service not found after add")
	}

	// Step 3: Disable SSH
	stdout.Reset()
	if err := doServiceSetEnabled([]string{"--config", cfgPath, "ssh"}, false, &stdout); err != nil {
		t.Fatalf("step 3 (disable ssh): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "disable ssh")
	if cfg.Services["ssh"].Enabled {
		t.Fatal("step 3: ssh should be disabled")
	}
	if !cfg.Services["xrdp"].Enabled {
		t.Fatal("step 3: xrdp should still be enabled")
	}

	// Step 4: Enable SSH back
	stdout.Reset()
	if err := doServiceSetEnabled([]string{"--config", cfgPath, "ssh"}, true, &stdout); err != nil {
		t.Fatalf("step 4 (enable ssh): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "enable ssh")
	if !cfg.Services["ssh"].Enabled {
		t.Fatal("step 4: ssh should be re-enabled")
	}

	// Step 5: Add a third service with custom protocol
	stdout.Reset()
	if err := doServiceAdd([]string{"--config", cfgPath, "ollama", "localhost:11434", "--protocol", "my-ollama"}, &stdout); err != nil {
		t.Fatalf("step 5 (add ollama): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "add ollama")
	if len(cfg.Services) != 3 {
		t.Fatalf("step 5: expected 3 services, got %d", len(cfg.Services))
	}
	if cfg.Services["ollama"].Protocol != "my-ollama" {
		t.Fatalf("step 5: ollama protocol = %q, want my-ollama", cfg.Services["ollama"].Protocol)
	}

	// Step 6: List services
	stdout.Reset()
	if err := doServiceList([]string{"--config", cfgPath}, &stdout); err != nil {
		t.Fatalf("step 6 (list): %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "Services (3)") {
		t.Fatalf("step 6: expected 'Services (3)' in output, got:\n%s", out)
	}

	// Step 7: Remove ollama
	stdout.Reset()
	if err := doServiceRemove([]string{"--config", cfgPath, "ollama"}, &stdout); err != nil {
		t.Fatalf("step 7 (remove ollama): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "remove ollama")
	if len(cfg.Services) != 2 {
		t.Fatalf("step 7: expected 2 services after remove, got %d", len(cfg.Services))
	}

	// Step 8: Remove ssh
	stdout.Reset()
	if err := doServiceRemove([]string{"--config", cfgPath, "ssh"}, &stdout); err != nil {
		t.Fatalf("step 8 (remove ssh): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "remove ssh")
	if len(cfg.Services) != 1 {
		t.Fatalf("step 8: expected 1 service after remove, got %d", len(cfg.Services))
	}

	// Step 9: Remove last service (xrdp) - should leave "services: {}"
	stdout.Reset()
	if err := doServiceRemove([]string{"--config", cfgPath, "xrdp"}, &stdout); err != nil {
		t.Fatalf("step 9 (remove xrdp): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "remove last service")
	if len(cfg.Services) != 0 {
		t.Fatalf("step 9: expected 0 services after removing last, got %d", len(cfg.Services))
	}

	// Step 10: Re-add a service after all were removed
	stdout.Reset()
	if err := doServiceAdd([]string{"--config", cfgPath, "web", "localhost:8080"}, &stdout); err != nil {
		t.Fatalf("step 10 (re-add after empty): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "re-add after empty")
	if _, ok := cfg.Services["web"]; !ok {
		t.Fatal("step 10: web service not found after re-add")
	}

	// Final: verify the entire config is still structurally sound
	if cfg.Identity.KeyFile == "" {
		t.Error("final: identity key_file should not be empty")
	}
	if len(cfg.Relay.Addresses) == 0 {
		t.Error("final: relay addresses should not be empty")
	}
	if !cfg.Security.EnableConnectionGating {
		t.Error("final: connection gating should still be enabled")
	}
}

// TestUserJourneyWithNetworkNamespace tests service add on a config generated
// with a private DHT namespace (from shurli join with a namespace invite).
func TestUserJourneyWithNetworkNamespace(t *testing.T) {
	dir := t.TempDir()

	// Use template with network namespace (as generated by shurli join)
	relayAddr := "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	configContent := nodeConfigTemplate(relayAddr, "shurli join", "my-private-network")

	cfgPath := filepath.Join(dir, "shurli.yaml")
	if err := os.WriteFile(cfgPath, []byte(configContent), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	keyData, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), keyData, 0600); err != nil {
		t.Fatalf("write identity key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	// Verify the namespace template parses
	cfg := verifyConfigParseable(t, cfgPath, "namespace template")
	if cfg.Discovery.Network != "my-private-network" {
		t.Fatalf("namespace should be 'my-private-network', got %q", cfg.Discovery.Network)
	}

	// Add a service
	var stdout bytes.Buffer
	if err := doServiceAdd([]string{"--config", cfgPath, "ssh", "localhost:22"}, &stdout); err != nil {
		t.Fatalf("add ssh to namespace config: %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "add ssh to namespace config")
	if _, ok := cfg.Services["ssh"]; !ok {
		t.Fatal("ssh service not found after add")
	}
	// Namespace should be preserved
	if cfg.Discovery.Network != "my-private-network" {
		t.Fatalf("namespace lost after service add: %q", cfg.Discovery.Network)
	}
}

// TestUpdateConfigNamesRealTemplate tests that updateConfigNames works correctly
// against the real config template (which has "names: {}" and commented examples).
func TestUpdateConfigNamesRealTemplate(t *testing.T) {
	cfgPath := writeRealTemplateConfig(t)
	configDir := filepath.Dir(cfgPath)

	// Step 1: Add first name to empty names
	updateConfigNames(cfgPath, configDir, "home-node", "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	cfg := verifyConfigParseable(t, cfgPath, "add first name")
	if cfg.Names == nil || cfg.Names["home-node"] != "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN" {
		t.Fatalf("step 1: expected home-node in names, got %v", cfg.Names)
	}

	// Step 2: Add second name
	updateConfigNames(cfgPath, configDir, "laptop", "12D3KooWQe1FfrYP5LsLnzEhK9uu4JSYhKauCpCjnkshYcNiMRTt")
	cfg = verifyConfigParseable(t, cfgPath, "add second name")
	if len(cfg.Names) != 2 {
		t.Fatalf("step 2: expected 2 names, got %d: %v", len(cfg.Names), cfg.Names)
	}
	if cfg.Names["laptop"] != "12D3KooWQe1FfrYP5LsLnzEhK9uu4JSYhKauCpCjnkshYcNiMRTt" {
		t.Fatal("step 2: laptop name not found or wrong peer ID")
	}

	// Step 3: Service add should still work after names modification
	var stdout bytes.Buffer
	if err := doServiceAdd([]string{"--config", cfgPath, "ssh", "localhost:22"}, &stdout); err != nil {
		t.Fatalf("step 3 (service add after names): %v", err)
	}
	cfg = verifyConfigParseable(t, cfgPath, "service add after names")
	if _, ok := cfg.Services["ssh"]; !ok {
		t.Fatal("step 3: ssh service not found")
	}
	if len(cfg.Names) != 2 {
		t.Fatalf("step 3: names should still have 2 entries, got %d", len(cfg.Names))
	}

	// Final: all original config fields preserved
	if len(cfg.Relay.Addresses) == 0 {
		t.Error("final: relay addresses should not be empty")
	}
	if !cfg.Security.EnableConnectionGating {
		t.Error("final: connection gating should still be enabled")
	}
}

// TestSanitizeYAMLNameRoundTrip verifies that names from remote peers
// (potentially malicious) don't corrupt the config when added via updateConfigNames.
func TestSanitizeYAMLNameRoundTrip(t *testing.T) {
	cfgPath := writeRealTemplateConfig(t)
	configDir := filepath.Dir(cfgPath)

	// Malicious names that could break YAML
	maliciousNames := []struct {
		input    string
		expected string
	}{
		{"normal-name", "normal-name"},
		{"name with spaces", "namewithspaces"},
		{"name: injection", "nameinjection"},
		{"name\nwith\nnewlines", "namewithnewlines"},
		{"name\"with\"quotes", "namewithquotes"},
		{"../../../etc/passwd", "......etcpasswd"},
		{"", ""},                 // empty after sanitize
		{"---", "---"},           // YAML document separator chars are allowed (hyphen)
		{"a" + strings.Repeat("b", 200), "a" + strings.Repeat("b", 63)}, // truncated to 64
	}

	for _, tt := range maliciousNames {
		sanitized := sanitizeYAMLName(tt.input)
		if sanitized != tt.expected {
			t.Errorf("sanitizeYAMLName(%q) = %q, want %q", tt.input, sanitized, tt.expected)
		}
	}

	// Test that sanitized names don't break the config
	updateConfigNames(cfgPath, configDir, "good-name", "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	verifyConfigParseable(t, cfgPath, "add good name")

	// Try adding after a YAML injection attempt (empty after sanitize - should be skipped)
	updateConfigNames(cfgPath, configDir, "   ", "12D3KooWQe1FfrYP5LsLnzEhK9uu4JSYhKauCpCjnkshYcNiMRTt")
	cfg := verifyConfigParseable(t, cfgPath, "add sanitized-to-empty name")
	// Should still only have 1 name (the empty one was skipped)
	if len(cfg.Names) != 1 {
		t.Fatalf("expected 1 name (empty should be skipped), got %d: %v", len(cfg.Names), cfg.Names)
	}
}
