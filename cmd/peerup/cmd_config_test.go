package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"

	"github.com/satindergrewal/peer-up/internal/config"
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

// validConfigYAML returns a minimal valid peerup config as a YAML string.
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

				// New config — valid, with a different rendezvous string.
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

				// New config missing relay addresses — will fail validation.
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
