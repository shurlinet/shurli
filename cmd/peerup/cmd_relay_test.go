package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// writeTestConfigDir creates a full test config directory with a valid
// peerup.yaml, identity.key, and authorized_keys. Returns the path to
// peerup.yaml.
func writeTestConfigDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	// Write identity key
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

	// Write authorized_keys (empty)
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), nil, 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	// Write config
	cfg := `version: 1
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
	if err := os.WriteFile(filepath.Join(dir, "peerup.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return filepath.Join(dir, "peerup.yaml")
}

// ----- doRelayList tests -----

func TestDoRelayList(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantErr    bool
		wantErrStr string
		wantOutput []string
	}{
		{
			name: "with relay addresses",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath}
			},
			wantOutput: []string{
				"Relay addresses (1)",
				"/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			},
		},
		{
			name: "config error with nonexistent file",
			args: func(t *testing.T) []string {
				return []string{"--config", "/tmp/nonexistent-test-dir-peerup/peerup.yaml"}
			},
			wantErr:    true,
			wantErrStr: "config error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args(t)

			var stdout bytes.Buffer
			err := doRelayList(args, &stdout)

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
		})
	}
}

// ----- doRelayRemove tests -----

func TestDoRelayRemove(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantErr    bool
		wantErrStr string
	}{
		{
			name: "not found returns error",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath, "/ip4/9.9.9.9/tcp/1234/p2p/12D3KooWABCDEFGHIJKLMNOPQRSTUVWXYZ1234567890abcdefgh"}
			},
			wantErr:    true,
			wantErrStr: "not found",
		},
		{
			name: "last relay cannot be removed",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath, "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"}
			},
			wantErr:    true,
			wantErrStr: "cannot remove the last",
		},
		{
			name: "missing arg returns usage error",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args(t)

			var stdout bytes.Buffer
			err := doRelayRemove(args, &stdout)

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
		})
	}
}

// ----- doRelayAdd tests -----

func TestDoRelayAdd(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantErr    bool
		wantErrStr string
	}{
		{
			name: "missing args returns usage error",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath}
			},
			wantErr:    true,
			wantErrStr: "usage",
		},
		{
			name: "invalid multiaddr returns error",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath, "/not/a/valid/multiaddr"}
			},
			wantErr:    true,
			wantErrStr: "invalid multiaddr",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args(t)

			var stdout bytes.Buffer
			err := doRelayAdd(args, &stdout)

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
		})
	}
}

// ----- truncateAddr tests -----

func TestTruncateAddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want string
	}{
		{
			name: "short address unchanged",
			addr: "/ip4/1.2.3.4/tcp/7777",
			want: "/ip4/1.2.3.4/tcp/7777",
		},
		{
			name: "long address with p2p truncates peer ID",
			addr: "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN",
			want: "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7B...",
		},
		{
			name: "long address without p2p unchanged",
			addr: "/ip4/192.168.100.200/tcp/7777/quic-v1/webtransport/certhash/uEiA0abcdefghijklmnopqrstuvwxyz",
			want: "/ip4/192.168.100.200/tcp/7777/quic-v1/webtransport/certhash/uEiA0abcdefghijklmnopqrstuvwxyz",
		},
		{
			name: "empty address unchanged",
			addr: "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateAddr(tt.addr)
			if got != tt.want {
				t.Errorf("truncateAddr(%q) = %q, want %q", tt.addr, got, tt.want)
			}
		})
	}
}

// ----- sanitizeYAMLName tests -----

func TestSanitizeYAMLName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "regular name unchanged",
			input: "laptop",
			want:  "laptop",
		},
		{
			name:  "name with spaces stripped",
			input: "my laptop",
			want:  "mylaptop",
		},
		{
			name:  "name with special chars stripped",
			input: "home@server!#$%",
			want:  "homeserver",
		},
		{
			name:  "empty stays empty",
			input: "",
			want:  "",
		},
		{
			name:  "hyphens kept",
			input: "home-server",
			want:  "home-server",
		},
		{
			name:  "underscores kept",
			input: "home_server",
			want:  "home_server",
		},
		{
			name:  "dots kept",
			input: "node.local",
			want:  "node.local",
		},
		{
			name:  "mixed alphanumeric",
			input: "Server2024",
			want:  "Server2024",
		},
		{
			name:  "only special chars becomes empty",
			input: "!@#$%^&*()",
			want:  "",
		},
		{
			name:  "yaml injection attempt stripped",
			input: "name: {evil: true}",
			want:  "nameeviltrue",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeYAMLName(tt.input)
			if got != tt.want {
				t.Errorf("sanitizeYAMLName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// ----- doWhoami tests -----

func TestDoWhoami(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantErr    bool
		wantErrStr string
		wantOutput string
	}{
		{
			name: "valid config outputs peer ID",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath}
			},
			wantOutput: "12D3KooW",
		},
		{
			name: "missing config returns error",
			args: func(t *testing.T) []string {
				return []string{"--config", "/tmp/nonexistent-test-dir-peerup/peerup.yaml"}
			},
			wantErr:    true,
			wantErrStr: "config error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args(t)

			var stdout bytes.Buffer
			err := doWhoami(args, &stdout)

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
				t.Errorf("output should contain %q, got:\n%s", tt.wantOutput, out)
			}
		})
	}
}

// ----- extractTCPPort tests -----

func TestExtractTCPPort(t *testing.T) {
	tests := []struct {
		name   string
		addrs  []string
		want   string
	}{
		{
			name:  "finds TCP port",
			addrs: []string{"/ip4/0.0.0.0/tcp/7777"},
			want:  "7777",
		},
		{
			name:  "finds first TCP port from multiple",
			addrs: []string{"/ip4/0.0.0.0/udp/9999/quic-v1", "/ip4/0.0.0.0/tcp/8888"},
			want:  "8888",
		},
		{
			name:  "no TCP returns default 7777",
			addrs: []string{"/ip4/0.0.0.0/udp/9999/quic-v1"},
			want:  "7777",
		},
		{
			name:  "empty list returns default 7777",
			addrs: nil,
			want:  "7777",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractTCPPort(tt.addrs)
			if got != tt.want {
				t.Errorf("extractTCPPort(%v) = %q, want %q", tt.addrs, got, tt.want)
			}
		})
	}
}

// ----- buildPublicMultiaddrs tests -----

func TestBuildPublicMultiaddrs(t *testing.T) {
	// Generate a test peer ID
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		listen     []string
		publicIPs  []string
		wantCount  int
		wantSubstr []string
	}{
		{
			name:       "IPv4 listen with IPv4 public IP",
			listen:     []string{"/ip4/0.0.0.0/tcp/7777"},
			publicIPs:  []string{"203.0.113.5"},
			wantCount:  1,
			wantSubstr: []string{"/ip4/203.0.113.5/tcp/7777/p2p/"},
		},
		{
			name:       "IPv6 listen with IPv6 public IP",
			listen:     []string{"/ip6/::/tcp/7777"},
			publicIPs:  []string{"2001:db8::1"},
			wantCount:  1,
			wantSubstr: []string{"/ip6/2001:db8::1/tcp/7777/p2p/"},
		},
		{
			name:      "IPv4 listen skips IPv6 IP",
			listen:    []string{"/ip4/0.0.0.0/tcp/7777"},
			publicIPs: []string{"2001:db8::1"},
			wantCount: 0,
		},
		{
			name:      "IPv6 listen skips IPv4 IP",
			listen:    []string{"/ip6/::/tcp/7777"},
			publicIPs: []string{"203.0.113.5"},
			wantCount: 0,
		},
		{
			name:       "multiple listen with multiple IPs",
			listen:     []string{"/ip4/0.0.0.0/tcp/7777", "/ip6/::/tcp/7777"},
			publicIPs:  []string{"203.0.113.5", "2001:db8::1"},
			wantCount:  2,
			wantSubstr: []string{"/ip4/203.0.113.5/", "/ip6/2001:db8::1/"},
		},
		{
			name:      "empty IPs",
			listen:    []string{"/ip4/0.0.0.0/tcp/7777"},
			publicIPs: nil,
			wantCount: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildPublicMultiaddrs(tt.listen, tt.publicIPs, pid)
			if len(got) != tt.wantCount {
				t.Errorf("got %d addrs, want %d: %v", len(got), tt.wantCount, got)
			}
			for _, sub := range tt.wantSubstr {
				found := false
				for _, addr := range got {
					if strings.Contains(addr, sub) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("no addr contains %q in %v", sub, got)
				}
			}
		})
	}
}

// ----- doStatus tests -----

func TestDoStatus(t *testing.T) {
	tests := []struct {
		name       string
		args       func(t *testing.T) []string
		wantErr    bool
		wantErrStr string
		wantOutput []string
	}{
		{
			name: "valid config shows status",
			args: func(t *testing.T) []string {
				cfgPath := writeTestConfigDir(t)
				return []string{"--config", cfgPath}
			},
			wantOutput: []string{
				"peerup",
				"Peer ID:",
				"Config:",
				"Relay addresses:",
				"Services:",
			},
		},
		{
			name: "missing config returns error",
			args: func(t *testing.T) []string {
				return []string{"--config", "/tmp/nonexistent-test-dir-peerup/peerup.yaml"}
			},
			wantErr:    true,
			wantErrStr: "config not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := tt.args(t)

			var stdout bytes.Buffer
			err := doStatus(args, &stdout)

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
		})
	}
}

// ----- doStatus extended tests (branch coverage) -----

func TestDoStatus_WithServicesAndNames(t *testing.T) {
	cfgPath := writeTestConfigWithNames(t, map[string]string{"home": generateTestPeerID(t)})

	// Add a service to the config
	data, _ := os.ReadFile(cfgPath)
	content := strings.Replace(string(data), "services: {}", `services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  web:
    enabled: false
    local_address: "localhost:8080"`, 1)
	os.WriteFile(cfgPath, []byte(content), 0600)

	var stdout bytes.Buffer
	err := doStatus([]string{"--config", cfgPath}, &stdout)
	if err != nil {
		t.Fatalf("doStatus: %v", err)
	}

	out := stdout.String()
	// Check services section with entries
	if !strings.Contains(out, "ssh") {
		t.Error("output should contain 'ssh' service")
	}
	if !strings.Contains(out, "web") {
		t.Error("output should contain 'web' service")
	}
	if !strings.Contains(out, "enabled") {
		t.Error("output should show enabled state")
	}
	if !strings.Contains(out, "disabled") {
		t.Error("output should show disabled state")
	}
	// Check names section with entries
	if !strings.Contains(out, "Names:") {
		t.Error("output should contain 'Names:' section")
	}
	if !strings.Contains(out, "home") {
		t.Error("output should contain name 'home'")
	}
}

func TestDoStatus_WithAuthorizedPeers(t *testing.T) {
	cfgPath := writeTestConfigDir(t)
	dir := filepath.Dir(cfgPath)

	// Add authorized peers
	pid1 := generateTestPeerID(t)
	pid2 := generateTestPeerID(t)
	akContent := pid1 + "  # my laptop\n" + pid2 + "\n"
	os.WriteFile(filepath.Join(dir, "authorized_keys"), []byte(akContent), 0600)

	var stdout bytes.Buffer
	err := doStatus([]string{"--config", cfgPath}, &stdout)
	if err != nil {
		t.Fatalf("doStatus: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "Authorized peers (2)") {
		t.Errorf("output should contain 'Authorized peers (2)', got:\n%s", out)
	}
	if !strings.Contains(out, "my laptop") {
		t.Error("output should contain peer comment")
	}
}

func TestDoStatus_EmptyAuthorizedKeys(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	var stdout bytes.Buffer
	err := doStatus([]string{"--config", cfgPath}, &stdout)
	if err != nil {
		t.Fatalf("doStatus: %v", err)
	}

	out := stdout.String()
	if !strings.Contains(out, "(none)") {
		t.Errorf("output should indicate no authorized peers, got:\n%s", out)
	}
}

// ----- resolveAuthKeysPathErr via config -----

func TestResolveAuthKeysPathErr_ViaConfig(t *testing.T) {
	cfgPath := writeTestConfigDir(t)

	path, err := resolveAuthKeysPathErr("", cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path == "" {
		t.Fatal("path should not be empty")
	}
	if !strings.Contains(path, "authorized_keys") {
		t.Errorf("path should contain 'authorized_keys', got: %s", path)
	}
}

func TestResolveAuthKeysPathErr_FileFlagPriority(t *testing.T) {
	dir := t.TempDir()
	akPath := filepath.Join(dir, "my_custom_keys")
	os.WriteFile(akPath, nil, 0600)

	// --file should take priority over --config
	path, err := resolveAuthKeysPathErr(akPath, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if path != akPath {
		t.Errorf("path = %q, want %q", path, akPath)
	}
}

func TestResolveAuthKeysPathErr_MissingConfig(t *testing.T) {
	_, err := resolveAuthKeysPathErr("", "/tmp/nonexistent-peerup-config/peerup.yaml")
	if err == nil {
		t.Fatal("expected error for missing config")
	}
	if !strings.Contains(err.Error(), "config error") {
		t.Errorf("error should contain 'config error': %v", err)
	}
}

// ----- doRelayAdd success path tests -----

func TestDoRelayAddSuccess(t *testing.T) {
	t.Run("add full multiaddr", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)
		pid := generateTestPeerID(t)
		newAddr := "/ip4/5.6.7.8/tcp/8888/p2p/" + pid

		var stdout bytes.Buffer
		err := doRelayAdd([]string{"--config", cfgPath, newAddr}, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "Config:") {
			t.Errorf("output should contain 'Config:', got:\n%s", out)
		}

		// Verify address written to config
		data, err := os.ReadFile(cfgPath)
		if err != nil {
			t.Fatalf("read config: %v", err)
		}
		if !strings.Contains(string(data), newAddr) {
			t.Errorf("config should contain new relay address:\n%s", string(data))
		}
	})

	t.Run("add IP:PORT with peer-id", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)
		pid := generateTestPeerID(t)

		var stdout bytes.Buffer
		err := doRelayAdd([]string{"--config", cfgPath, "5.6.7.8:8888", "--peer-id", pid}, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// Verify multiaddr was constructed and written
		data, _ := os.ReadFile(cfgPath)
		expected := "/ip4/5.6.7.8/tcp/8888/p2p/" + pid
		if !strings.Contains(string(data), expected) {
			t.Errorf("config should contain constructed multiaddr %q:\n%s", expected, string(data))
		}
	})

	t.Run("duplicate is no-op", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)
		existing := "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"

		var stdout bytes.Buffer
		err := doRelayAdd([]string{"--config", cfgPath, existing}, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "No new relay addresses") {
			t.Errorf("output should indicate no-op, got:\n%s", out)
		}
	})

	t.Run("short format missing peer-id returns error", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)

		var stdout bytes.Buffer
		err := doRelayAdd([]string{"--config", cfgPath, "5.6.7.8:8888"}, &stdout)
		if err == nil {
			t.Fatal("expected error for missing --peer-id")
		}
		if !strings.Contains(err.Error(), "peer-id") {
			t.Errorf("error should mention peer-id: %v", err)
		}
	})

	t.Run("invalid peer-id returns error", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)

		var stdout bytes.Buffer
		err := doRelayAdd([]string{"--config", cfgPath, "5.6.7.8:8888", "--peer-id", "not-valid"}, &stdout)
		if err == nil {
			t.Fatal("expected error for invalid peer-id")
		}
		if !strings.Contains(err.Error(), "invalid peer ID") {
			t.Errorf("error should mention invalid peer ID: %v", err)
		}
	})
}

// ----- doRelayRemove success path tests -----

func TestDoRelayRemoveSuccess(t *testing.T) {
	t.Run("remove one of two relays", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)

		// Add a second relay first
		pid := generateTestPeerID(t)
		secondAddr := "/ip4/5.6.7.8/tcp/8888/p2p/" + pid
		var buf bytes.Buffer
		if err := doRelayAdd([]string{"--config", cfgPath, secondAddr}, &buf); err != nil {
			t.Fatalf("setup: add second relay: %v", err)
		}

		// Remove the original relay
		originalAddr := "/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
		var stdout bytes.Buffer
		err := doRelayRemove([]string{"--config", cfgPath, originalAddr}, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "Config:") {
			t.Errorf("output should contain 'Config:', got:\n%s", out)
		}

		// Verify original is gone, second remains
		data, _ := os.ReadFile(cfgPath)
		content := string(data)
		if strings.Contains(content, "1.2.3.4") {
			t.Error("config should not contain removed relay address")
		}
		if !strings.Contains(content, secondAddr) {
			t.Error("config should still contain the second relay address")
		}
	})
}

// ----- validatePeerID tests -----

func TestValidatePeerID(t *testing.T) {
	validPID := generateTestPeerID(t)

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid Ed25519 peer ID", validPID, false},
		{"empty string", "", true},
		{"random garbage", "not-a-peer-id", true},
		{"partial peer ID prefix", "12D3KooW", true},
		{"too short base58", "12D3KooWABC", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validatePeerID(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validatePeerID(%q) err=%v, wantErr=%v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// ----- resolveConfigFileErr tests -----

func TestResolveConfigFileErr(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfgPath := writeTestConfigDir(t)

		path, cfg, err := resolveConfigFileErr(cfgPath)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if path != cfgPath {
			t.Errorf("path = %q, want %q", path, cfgPath)
		}
		if cfg == nil {
			t.Fatal("config should not be nil")
		}
		if cfg.Discovery.Rendezvous != "test-network" {
			t.Errorf("rendezvous = %q, want %q", cfg.Discovery.Rendezvous, "test-network")
		}
	})

	t.Run("missing config", func(t *testing.T) {
		_, _, err := resolveConfigFileErr("/tmp/nonexistent-test-dir-peerup/peerup.yaml")
		if err == nil {
			t.Fatal("expected error for missing config")
		}
		if !strings.Contains(err.Error(), "config error") {
			t.Errorf("error should mention config error: %v", err)
		}
	})
}
