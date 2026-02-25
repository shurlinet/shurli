package main

import (
	"bytes"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/shurlinet/shurli/internal/config"
)

// ----- buildRelayResources tests -----

func TestBuildRelayResources(t *testing.T) {
	rc := &config.RelayResourcesConfig{
		MaxReservations:       128,
		MaxCircuits:           64,
		MaxReservationsPerIP:  8,
		MaxReservationsPerASN: 32,
		BufferSize:            4096,
		ReservationTTL:        "1h",
		SessionDuration:       "2m",
		SessionDataLimit:      "128KB",
	}

	resources, limit := buildRelayResources(rc)

	// Check resources fields
	if resources.MaxReservations != 128 {
		t.Errorf("MaxReservations = %d, want 128", resources.MaxReservations)
	}
	if resources.MaxCircuits != 64 {
		t.Errorf("MaxCircuits = %d, want 64", resources.MaxCircuits)
	}
	if resources.MaxReservationsPerIP != 8 {
		t.Errorf("MaxReservationsPerIP = %d, want 8", resources.MaxReservationsPerIP)
	}
	if resources.MaxReservationsPerASN != 32 {
		t.Errorf("MaxReservationsPerASN = %d, want 32", resources.MaxReservationsPerASN)
	}
	if resources.BufferSize != 4096 {
		t.Errorf("BufferSize = %d, want 4096", resources.BufferSize)
	}
	if resources.ReservationTTL != time.Hour {
		t.Errorf("ReservationTTL = %v, want 1h", resources.ReservationTTL)
	}
	if resources.MaxReservationsPerPeer != 1 {
		t.Errorf("MaxReservationsPerPeer = %d, want 1 (hardcoded)", resources.MaxReservationsPerPeer)
	}

	// Check limit
	if limit == nil {
		t.Fatal("limit should not be nil")
	}
	if limit.Duration != 2*time.Minute {
		t.Errorf("limit.Duration = %v, want 2m", limit.Duration)
	}
	// 128KB = 128 * 1024 = 131072
	if limit.Data != 131072 {
		t.Errorf("limit.Data = %d, want 131072", limit.Data)
	}

	// Check that resources.Limit matches the standalone limit
	if resources.Limit == nil {
		t.Fatal("resources.Limit should not be nil")
	}
	if resources.Limit.Duration != limit.Duration {
		t.Error("resources.Limit.Duration should match standalone limit")
	}
	if resources.Limit.Data != limit.Data {
		t.Error("resources.Limit.Data should match standalone limit")
	}
}

func TestBuildRelayResourcesDefaults(t *testing.T) {
	// Test with the typical default values
	rc := &config.RelayResourcesConfig{
		MaxReservations:       128,
		MaxCircuits:           16,
		MaxReservationsPerIP:  8,
		MaxReservationsPerASN: 32,
		BufferSize:            2048,
		ReservationTTL:        "30m",
		SessionDuration:       "10m",
		SessionDataLimit:      "64MB",
	}

	resources, limit := buildRelayResources(rc)

	if resources.ReservationTTL != 30*time.Minute {
		t.Errorf("ReservationTTL = %v, want 30m", resources.ReservationTTL)
	}
	if limit.Duration != 10*time.Minute {
		t.Errorf("limit.Duration = %v, want 10m", limit.Duration)
	}
	// 64MB = 64 * 1024 * 1024 = 67108864
	if limit.Data != 67108864 {
		t.Errorf("limit.Data = %d, want 67108864", limit.Data)
	}
}

// ----- detectPublicIPs tests -----

func TestDetectPublicIPs(t *testing.T) {
	// detectPublicIPs depends on the machine's network interfaces,
	// so we can only test basic properties.
	ips := detectPublicIPs()

	for _, ip := range ips {
		parsed := net.ParseIP(ip)
		if parsed == nil {
			t.Errorf("returned unparseable IP: %s", ip)
			continue
		}
		if parsed.IsLoopback() {
			t.Errorf("loopback IP should not appear: %s", ip)
		}
		if parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() {
			t.Errorf("link-local IP should not appear: %s", ip)
		}
		// Check no private IPv4
		if ip4 := parsed.To4(); ip4 != nil {
			if ip4[0] == 10 ||
				(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
				(ip4[0] == 192 && ip4[1] == 168) {
				t.Errorf("private IPv4 should not appear: %s", ip)
			}
		}
	}
}

// ----- Relay server config test helpers -----

// writeRelayServerTestConfig creates a valid relay-server.yaml with identity key
// and authorized_keys in a temp directory. Returns the config file path.
func writeRelayServerTestConfig(t *testing.T) string {
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
	keyFile := filepath.Join(dir, "identity.key")
	if err := os.WriteFile(keyFile, data, 0600); err != nil {
		t.Fatalf("write identity key: %v", err)
	}

	// Write authorized_keys (empty)
	authKeysFile := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(authKeysFile, []byte("# authorized peers\n"), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	// Write relay-server.yaml
	cfg := `version: 1
identity:
  key_file: "` + keyFile + `"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: "` + authKeysFile + `"
  enable_connection_gating: true
resources:
  max_reservations: 128
  max_circuits: 16
  buffer_size: 2048
  max_reservations_per_ip: 8
  max_reservations_per_asn: 32
  reservation_ttl: "1h"
  session_duration: "10m"
  session_data_limit: "64MB"
`
	cfgFile := filepath.Join(dir, "relay-server.yaml")
	if err := os.WriteFile(cfgFile, []byte(cfg), 0600); err != nil {
		t.Fatalf("write relay config: %v", err)
	}
	return cfgFile
}

// ----- loadRelayAuthKeysPathErr tests -----

func TestLoadRelayAuthKeysPathErr(t *testing.T) {
	t.Run("valid config returns path", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)
		path, err := loadRelayAuthKeysPathErr(cfgFile)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if path == "" {
			t.Fatal("path should not be empty")
		}
	})

	t.Run("missing config returns error", func(t *testing.T) {
		_, err := loadRelayAuthKeysPathErr("/tmp/nonexistent-relay-cfg.yaml")
		if err == nil {
			t.Fatal("expected error for missing config")
		}
	})

	t.Run("config without auth_keys returns error", func(t *testing.T) {
		dir := t.TempDir()
		cfgFile := filepath.Join(dir, "relay.yaml")
		cfg := `version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: ""
  enable_connection_gating: false
`
		os.WriteFile(cfgFile, []byte(cfg), 0600)

		_, err := loadRelayAuthKeysPathErr(cfgFile)
		if err == nil {
			t.Fatal("expected error for empty authorized_keys_file")
		}
		if !strings.Contains(err.Error(), "no authorized_keys_file") {
			t.Errorf("error should mention authorized_keys_file: %v", err)
		}
	})
}

// ----- doRelayAuthorize tests -----

func TestDoRelayAuthorize(t *testing.T) {
	t.Run("authorize peer succeeds", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)
		pid := generateTestPeerID(t)

		var stdout bytes.Buffer
		err := doRelayAuthorize([]string{pid, "test-node"}, cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if !strings.Contains(out, "Authorized:") {
			t.Errorf("output should contain 'Authorized:', got:\n%s", out)
		}
		if !strings.Contains(out, "Comment:    test-node") {
			t.Errorf("output should contain comment, got:\n%s", out)
		}
	})

	t.Run("authorize without comment", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)
		pid := generateTestPeerID(t)

		var stdout bytes.Buffer
		err := doRelayAuthorize([]string{pid}, cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		out := stdout.String()
		if strings.Contains(out, "Comment:") {
			t.Errorf("output should not contain 'Comment:' when no comment given")
		}
	})

	t.Run("no args returns error", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		var stdout bytes.Buffer
		err := doRelayAuthorize(nil, cfgFile, &stdout)
		if err == nil {
			t.Fatal("expected error for missing args")
		}
		if !strings.Contains(err.Error(), "usage") {
			t.Errorf("error should mention usage: %v", err)
		}
	})

	t.Run("missing config returns error", func(t *testing.T) {
		var stdout bytes.Buffer
		err := doRelayAuthorize([]string{"12D3KooWTest"}, "/tmp/nonexistent.yaml", &stdout)
		if err == nil {
			t.Fatal("expected error for missing config")
		}
	})
}

// ----- doRelayDeauthorize tests -----

func TestDoRelayDeauthorize(t *testing.T) {
	t.Run("deauthorize peer succeeds", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)
		pid := generateTestPeerID(t)

		// First authorize
		var buf bytes.Buffer
		if err := doRelayAuthorize([]string{pid}, cfgFile, &buf); err != nil {
			t.Fatalf("setup authorize: %v", err)
		}

		// Then deauthorize
		var stdout bytes.Buffer
		err := doRelayDeauthorize([]string{pid}, cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(stdout.String(), "Deauthorized:") {
			t.Errorf("output should contain 'Deauthorized:', got:\n%s", stdout.String())
		}
	})

	t.Run("no args returns error", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		var stdout bytes.Buffer
		err := doRelayDeauthorize(nil, cfgFile, &stdout)
		if err == nil {
			t.Fatal("expected error for missing args")
		}
	})
}

// ----- doRelayListPeers tests -----

func TestDoRelayListPeers(t *testing.T) {
	t.Run("empty list", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		var stdout bytes.Buffer
		err := doRelayListPeers(cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, "(none)") {
			t.Errorf("output should contain '(none)' for empty list, got:\n%s", out)
		}
		if !strings.Contains(out, "Total: 0") {
			t.Errorf("output should contain 'Total: 0', got:\n%s", out)
		}
	})

	t.Run("with authorized peers", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)
		pid := generateTestPeerID(t)

		// Authorize a peer first
		var buf bytes.Buffer
		if err := doRelayAuthorize([]string{pid, "my-laptop"}, cfgFile, &buf); err != nil {
			t.Fatalf("setup authorize: %v", err)
		}

		var stdout bytes.Buffer
		err := doRelayListPeers(cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		out := stdout.String()
		if !strings.Contains(out, pid) {
			t.Errorf("output should contain peer ID, got:\n%s", out)
		}
		if !strings.Contains(out, "my-laptop") {
			t.Errorf("output should contain comment, got:\n%s", out)
		}
		if !strings.Contains(out, "Total: 1") {
			t.Errorf("output should contain 'Total: 1', got:\n%s", out)
		}
	})

	t.Run("missing config returns error", func(t *testing.T) {
		var stdout bytes.Buffer
		err := doRelayListPeers("/tmp/nonexistent.yaml", &stdout)
		if err == nil {
			t.Fatal("expected error for missing config")
		}
	})
}

// ----- doRelayServerConfigValidate tests -----

func TestDoRelayServerConfigValidate(t *testing.T) {
	t.Run("valid config", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		var stdout bytes.Buffer
		err := doRelayServerConfigValidate(cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(stdout.String(), "OK:") {
			t.Errorf("output should contain 'OK:', got:\n%s", stdout.String())
		}
	})

	t.Run("missing config returns error", func(t *testing.T) {
		var stdout bytes.Buffer
		err := doRelayServerConfigValidate("/tmp/nonexistent.yaml", &stdout)
		if err == nil {
			t.Fatal("expected error for missing config")
		}
		if !strings.Contains(err.Error(), "FAIL") {
			t.Errorf("error should contain 'FAIL': %v", err)
		}
	})

	t.Run("invalid config returns error", func(t *testing.T) {
		dir := t.TempDir()
		cfgFile := filepath.Join(dir, "bad.yaml")
		os.WriteFile(cfgFile, []byte("not: valid: yaml: ["), 0600)

		var stdout bytes.Buffer
		err := doRelayServerConfigValidate(cfgFile, &stdout)
		if err == nil {
			t.Fatal("expected error for invalid config")
		}
	})
}

// ----- doRelayServerConfigRollback tests -----

func TestDoRelayServerConfigRollback(t *testing.T) {
	t.Run("no archive returns error", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		var stdout bytes.Buffer
		err := doRelayServerConfigRollback(cfgFile, &stdout)
		if err == nil {
			t.Fatal("expected error when no archive exists")
		}
		if !strings.Contains(err.Error(), "no last-known-good archive") {
			t.Errorf("error should mention archive: %v", err)
		}
	})

	t.Run("rollback with archive succeeds", func(t *testing.T) {
		cfgFile := writeRelayServerTestConfig(t)

		// Create an archive by calling config.Archive
		if err := config.Archive(cfgFile); err != nil {
			t.Fatalf("create archive: %v", err)
		}

		// Modify the config (simulate a bad change)
		os.WriteFile(cfgFile, []byte("broken config"), 0600)

		// Rollback should restore the original
		var stdout bytes.Buffer
		err := doRelayServerConfigRollback(cfgFile, &stdout)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(stdout.String(), "Restored") {
			t.Errorf("output should mention 'Restored', got:\n%s", stdout.String())
		}

		// Verify the config was restored (should be valid YAML, not "broken config")
		data, _ := os.ReadFile(cfgFile)
		if strings.Contains(string(data), "broken config") {
			t.Error("config should have been restored from archive")
		}
	})
}
