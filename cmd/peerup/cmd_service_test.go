package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
)

// writeServiceTestConfig creates a full test config directory with a valid
// peerup.yaml, identity.key, and authorized_keys. The servicesYAML parameter
// replaces the default "services: {}" line. Returns the path to peerup.yaml.
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

	cfgPath := filepath.Join(dir, "peerup.yaml")
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
