package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// writeTestConfigWithNames creates a config with a name mapping and returns the config path.
func writeTestConfigWithNames(t *testing.T, names map[string]string) string {
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

	// Write authorized_keys
	if err := os.WriteFile(filepath.Join(dir, "authorized_keys"), nil, 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	// Build names YAML
	var namesYAML string
	if len(names) == 0 {
		namesYAML = "names: {}"
	} else {
		namesYAML = "names:\n"
		for k, v := range names {
			namesYAML += "  " + k + ": \"" + v + "\"\n"
		}
	}

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
` + namesYAML + "\n"

	if err := os.WriteFile(filepath.Join(dir, "peerup.yaml"), []byte(cfg), 0600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return filepath.Join(dir, "peerup.yaml")
}

func TestDoResolve(t *testing.T) {
	// Generate a test peer ID
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, 0)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	peerIDStr := pid.String()

	t.Run("resolve by name", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, map[string]string{"home": peerIDStr})

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath, "home"}, &stdout)
		if err != nil {
			t.Fatalf("doResolve: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "home") {
			t.Errorf("output should contain 'home', got: %s", out)
		}
		if !strings.Contains(out, peerIDStr) {
			t.Errorf("output should contain peer ID, got: %s", out)
		}
	})

	t.Run("resolve by peer ID", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, nil)

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath, peerIDStr}, &stdout)
		if err != nil {
			t.Fatalf("doResolve: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, peerIDStr) {
			t.Errorf("output should contain peer ID, got: %s", out)
		}
	})

	t.Run("resolve JSON output", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, map[string]string{"home": peerIDStr})

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath, "--json", "home"}, &stdout)
		if err != nil {
			t.Fatalf("doResolve: %v", err)
		}

		var resp struct {
			Name   string `json:"name"`
			PeerID string `json:"peer_id"`
			Source string `json:"source"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
			t.Fatalf("JSON decode: %v (output: %s)", err, stdout.String())
		}
		if resp.Name != "home" {
			t.Errorf("Name = %q, want 'home'", resp.Name)
		}
		if resp.PeerID != peerIDStr {
			t.Errorf("PeerID = %q, want %q", resp.PeerID, peerIDStr)
		}
		if resp.Source != "local_config" {
			t.Errorf("Source = %q, want 'local_config'", resp.Source)
		}
	})

	t.Run("resolve JSON peer_id source", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, nil)

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath, "--json", peerIDStr}, &stdout)
		if err != nil {
			t.Fatalf("doResolve: %v", err)
		}

		var resp struct {
			Source string `json:"source"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
			t.Fatalf("JSON decode: %v", err)
		}
		if resp.Source != "peer_id" {
			t.Errorf("Source = %q, want 'peer_id'", resp.Source)
		}
	})

	t.Run("name not found", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, nil)

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath, "nonexistent"}, &stdout)
		if err == nil {
			t.Fatal("expected error for unknown name")
		}
		if !strings.Contains(err.Error(), "cannot resolve") {
			t.Errorf("error = %q, want 'cannot resolve'", err.Error())
		}
	})

	t.Run("missing name arg", func(t *testing.T) {
		cfgPath := writeTestConfigWithNames(t, nil)

		var stdout bytes.Buffer
		err := doResolve([]string{"--config", cfgPath}, &stdout)
		if err == nil {
			t.Fatal("expected error for missing arg")
		}
		if !strings.Contains(err.Error(), "usage") {
			t.Errorf("error = %q, want 'usage'", err.Error())
		}
	})
}
