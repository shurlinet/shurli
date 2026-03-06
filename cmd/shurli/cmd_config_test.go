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

// writeTestIdentityKey creates a valid libp2p Ed25519 identity key file in dir.
func writeTestIdentityKey(t *testing.T, dir string) {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatalf("generate key pair: %v", err)
	}
	data, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "identity.key"), data, 0600); err != nil {
		t.Fatalf("write identity key: %v", err)
	}
}

// validConfigYAML returns a minimal valid shurli config as a YAML string.
func validConfigYAML() string {
	return `version: 1
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
names: {}
`
}

// writeValidConfig writes a valid config YAML into dir/config.yaml and returns
// the full path. It also creates the identity.key file so validation passes.
func writeValidConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(validConfigYAML()), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	writeTestIdentityKey(t, dir)
	// Create an empty authorized_keys file so validation doesn't complain.
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}
	return cfgPath
}

// ----- doConfigValidate tests -----

func TestDoConfigValidate(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string // returns args
		wantErr    bool
		wantOutput string // substring expected in stdout
		wantErrStr string // substring expected in error
	}{
		{
			name: "valid config",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantOutput: "Valid",
		},
		{
			name: "invalid YAML",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := filepath.Join(dir, "config.yaml")
				os.WriteFile(cfgPath, []byte("{{{{not yaml"), 0600)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "invalid config",
		},
		{
			name: "missing relay addresses",
			setup: func(t *testing.T, dir string) []string {
				yaml := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses: []
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
names: {}
`
				cfgPath := filepath.Join(dir, "config.yaml")
				os.WriteFile(cfgPath, []byte(yaml), 0600)
				writeTestIdentityKey(t, dir)
				os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "validation failed",
		},
		{
			name: "nonexistent file",
			setup: func(t *testing.T, dir string) []string {
				return []string{"--config", filepath.Join(dir, "does-not-exist.yaml")}
			},
			wantErr:    true,
			wantErrStr: "config error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doConfigValidate(args, &stdout)

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
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

// ----- doConfigShow tests -----

func TestDoConfigShow(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantSubstr []string // substrings expected in stdout
	}{
		{
			name: "valid config shows key fields",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantSubstr: []string{
				"identity",
				"relay",
				"listen_addresses",
				"Resolved config from",
			},
		},
		{
			name: "with archive shows archive status",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				// Create an archive so HasArchive returns true.
				if err := config.Archive(cfgPath); err != nil {
					t.Fatalf("create archive: %v", err)
				}
				return []string{"--config", cfgPath}
			},
			wantSubstr: []string{
				"Last-known-good archive",
				"last-good",
			},
		},
		{
			name: "without archive shows no archive message",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantSubstr: []string{
				"No last-known-good archive",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doConfigShow(args, &stdout)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			out := stdout.String()
			for _, sub := range tt.wantSubstr {
				if !strings.Contains(out, sub) {
					t.Errorf("output should contain %q, got:\n%s", sub, out)
				}
			}
		})
	}
}

// ----- doConfigRollback tests -----

func TestDoConfigRollback(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantErrStr string
		wantOutput string
	}{
		{
			name: "no archive returns error",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "no last-known-good archive",
		},
		{
			name: "with archive succeeds",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				if err := config.Archive(cfgPath); err != nil {
					t.Fatalf("create archive: %v", err)
				}
				return []string{"--config", cfgPath}
			},
			wantOutput: "Restored",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doConfigRollback(args, &stdout)

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
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

// ----- doConfigApply tests -----

func TestDoConfigApply(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantErrStr string
		wantOutput string
	}{
		{
			name: "valid new config applies successfully",
			setup: func(t *testing.T, dir string) []string {
				// Current config.
				cfgPath := writeValidConfig(t, dir)

				// New config  - valid, with a different rendezvous string.
				newDir := filepath.Join(dir, "new")
				os.MkdirAll(newDir, 0755)
				newYAML := strings.Replace(validConfigYAML(), "test-network", "updated-network", 1)
				newCfgPath := filepath.Join(newDir, "new-config.yaml")
				os.WriteFile(newCfgPath, []byte(newYAML), 0600)
				writeTestIdentityKey(t, newDir)
				os.WriteFile(filepath.Join(newDir, "authorized_keys"), []byte(""), 0600)

				return []string{"--config", cfgPath, newCfgPath}
			},
			wantOutput: "Applied",
		},
		{
			name: "invalid new config returns error",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)

				// New config missing relay addresses  - will fail validation.
				invalidYAML := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses: []
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
names: {}
`
				newDir := filepath.Join(dir, "new")
				os.MkdirAll(newDir, 0755)
				newCfgPath := filepath.Join(newDir, "bad-config.yaml")
				os.WriteFile(newCfgPath, []byte(invalidYAML), 0600)
				writeTestIdentityKey(t, newDir)
				os.WriteFile(filepath.Join(newDir, "authorized_keys"), []byte(""), 0600)

				return []string{"--config", cfgPath, newCfgPath}
			},
			wantErr:    true,
			wantErrStr: "validation errors",
		},
		{
			name: "no new config path returns usage error",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout, stderr bytes.Buffer
			err := doConfigApply(args, &stdout, &stderr)

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
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

// ----- doConfigShow commit-confirmed pending path -----

func TestDoConfigShow_WithPendingCommitConfirmed(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	// Create a valid new config and apply it to create pending state
	newDir := filepath.Join(dir, "new")
	os.MkdirAll(newDir, 0755)
	newYAML := strings.Replace(validConfigYAML(), "test-network", "pending-net", 1)
	newCfgPath := filepath.Join(newDir, "new-config.yaml")
	os.WriteFile(newCfgPath, []byte(newYAML), 0600)
	writeTestIdentityKey(t, newDir)
	os.WriteFile(filepath.Join(newDir, "authorized_keys"), []byte(""), 0600)

	var applyOut, applyErr bytes.Buffer
	if err := doConfigApply(
		[]string{"--config", cfgPath, newCfgPath},
		&applyOut, &applyErr,
	); err != nil {
		t.Fatalf("apply setup failed: %v", err)
	}

	// Now show should mention commit-confirmed pending
	var stdout bytes.Buffer
	err := doConfigShow([]string{"--config", cfgPath}, &stdout)
	if err != nil {
		t.Fatalf("doConfigShow: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Commit-confirmed pending") && !strings.Contains(out, "commit-confirmed") {
		t.Errorf("output should mention commit-confirmed pending, got:\n%s", out)
	}
}

func TestDoConfigShow_WithValidationWarning(t *testing.T) {
	dir := t.TempDir()

	// Create config with empty relay addresses (will trigger validation warning)
	yaml := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
relay:
  addresses: []
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
names: {}
`
	cfgPath := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfgPath, []byte(yaml), 0600)
	writeTestIdentityKey(t, dir)
	os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(""), 0600)

	var stdout bytes.Buffer
	err := doConfigShow([]string{"--config", cfgPath}, &stdout)
	if err != nil {
		t.Fatalf("doConfigShow: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "WARNING") {
		t.Errorf("output should contain validation WARNING, got:\n%s", out)
	}
}

// ----- doConfigConfirm tests -----

func TestDoConfigConfirm(t *testing.T) {
	tests := []struct {
		name       string
		setup      func(t *testing.T, dir string) []string
		wantErr    bool
		wantErrStr string
		wantOutput string
	}{
		{
			name: "no pending returns error",
			setup: func(t *testing.T, dir string) []string {
				cfgPath := writeValidConfig(t, dir)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "no commit-confirmed pending",
		},
		{
			name: "with pending created by apply succeeds",
			setup: func(t *testing.T, dir string) []string {
				// Set up a current config.
				cfgPath := writeValidConfig(t, dir)

				// Create a valid new config to apply.
				newDir := filepath.Join(dir, "new")
				os.MkdirAll(newDir, 0755)
				newYAML := strings.Replace(validConfigYAML(), "test-network", "confirmed-net", 1)
				newCfgPath := filepath.Join(newDir, "new-config.yaml")
				os.WriteFile(newCfgPath, []byte(newYAML), 0600)
				writeTestIdentityKey(t, newDir)
				os.WriteFile(filepath.Join(newDir, "authorized_keys"), []byte(""), 0600)

				// Apply the new config to create a pending state.
				var applyOut, applyErr bytes.Buffer
				if err := doConfigApply(
					[]string{"--config", cfgPath, newCfgPath},
					&applyOut, &applyErr,
				); err != nil {
					t.Fatalf("apply setup failed: %v", err)
				}

				return []string{"--config", cfgPath}
			},
			wantOutput: "confirmed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			args := tt.setup(t, dir)

			var stdout bytes.Buffer
			err := doConfigConfirm(args, &stdout)

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
			if tt.wantOutput != "" && !strings.Contains(out, tt.wantOutput) {
				t.Errorf("output %q should contain %q", out, tt.wantOutput)
			}
		})
	}
}

// ----- doConfigSet tests -----

func TestDoConfigSet_BasicKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	// Flags must come before positional args for Go's flag parser
	err := doConfigSet([]string{"--config", cfgPath, "network.force_private_reachability", "true"}, &stdout)
	if err != nil {
		t.Fatalf("doConfigSet failed: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	got := string(data)
	if !strings.Contains(got, "force_private_reachability") {
		t.Errorf("config should contain 'force_private_reachability' after set, got:\n%s", got)
	}
	if !strings.Contains(stdout.String(), "Set network.force_private_reachability = true") {
		t.Errorf("output should confirm the set, got: %s", stdout.String())
	}
}

func TestDoConfigSet_CreatesNestedKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "network.force_cgnat", "true"}, &stdout)
	if err != nil {
		t.Fatalf("doConfigSet failed: %v", err)
	}

	data, _ := os.ReadFile(cfgPath)
	if !strings.Contains(string(data), "force_cgnat") {
		t.Errorf("config should contain 'force_cgnat' after set, got:\n%s", data)
	}
}

func TestDoConfigSet_MissingArgs(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "only_key"}, &stdout)
	if err == nil {
		t.Fatal("expected error for missing value")
	}
	if !strings.Contains(err.Error(), "usage:") {
		t.Errorf("error should contain usage hint, got: %v", err)
	}
}

func TestDoConfigSet_NoArgs(t *testing.T) {
	var stdout bytes.Buffer
	err := doConfigSet(nil, &stdout)
	if err == nil {
		t.Fatal("expected error for no args")
	}
}

func TestDoConfigSet_RejectsUnknownKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "netwrk.force_cgnat", "true"}, &stdout)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("error should mention unknown key, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Did you mean") {
		t.Errorf("error should suggest similar key, got: %v", err)
	}
}

func TestDoConfigSet_RejectsCompletelyUnknownKey(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "zzz.totally.bogus", "true"}, &stdout)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "unknown config key") {
		t.Errorf("error should mention unknown key, got: %v", err)
	}
}

func TestDoConfigSet_AllowsServiceKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "services.myservice.enabled", "true"}, &stdout)
	if err != nil {
		t.Fatalf("services.* keys should be allowed: %v", err)
	}
}

func TestDoConfigSet_AllowsNameKeys(t *testing.T) {
	dir := t.TempDir()
	cfgPath := writeValidConfig(t, dir)

	var stdout bytes.Buffer
	err := doConfigSet([]string{"--config", cfgPath, "names.laptop", "12D3KooW..."}, &stdout)
	if err != nil {
		t.Fatalf("names.* keys should be allowed: %v", err)
	}
}

func TestValidateConfigKey(t *testing.T) {
	tests := []struct {
		key     string
		wantErr bool
		wantMsg string
	}{
		{"network.force_cgnat", false, ""},
		{"discovery.rendezvous", false, ""},
		{"security.zkp.enabled", false, ""},
		{"services.ssh.enabled", false, ""},
		{"names.laptop", false, ""},
		{"netwrk.force_cgnat", true, "Did you mean"},
		{"totally.unknown", true, "unknown config key"},
		{"version", false, ""},
	}

	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			err := validateConfigKey(tt.key)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if tt.wantMsg != "" && !strings.Contains(err.Error(), tt.wantMsg) {
					t.Errorf("error %q should contain %q", err.Error(), tt.wantMsg)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestLevenshtein(t *testing.T) {
	tests := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"netwrk", "network", 1},
	}
	for _, tt := range tests {
		got := levenshtein(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}
