package config

import (
	"os"
	"path/filepath"
	"testing"
)

// Minimal valid YAML for loading tests.
const testConfigYAML = `
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
  force_private_reachability: false
relay:
  addresses:
    - "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An"
  reservation_interval: "2m"
discovery:
  rendezvous: "peerup-test-net"
  bootstrap_peers: []
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"
services:
  ssh:
    enabled: true
    local_address: "localhost:22"
names:
  home: "12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt"
`

func writeTestConfig(t testing.TB, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write test config: %v", err)
	}
	return path
}

func TestLoadNodeConfig(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, testConfigYAML)

	cfg, err := LoadNodeConfig(path)
	if err != nil {
		t.Fatalf("LoadNodeConfig: %v", err)
	}

	if cfg.Identity.KeyFile != "identity.key" {
		t.Errorf("KeyFile = %q, want %q", cfg.Identity.KeyFile, "identity.key")
	}
	if len(cfg.Network.ListenAddresses) != 1 {
		t.Errorf("ListenAddresses count = %d, want 1", len(cfg.Network.ListenAddresses))
	}
	if cfg.Relay.ReservationInterval.Minutes() != 2 {
		t.Errorf("ReservationInterval = %v, want 2m", cfg.Relay.ReservationInterval)
	}
	if cfg.Discovery.Rendezvous != "peerup-test-net" {
		t.Errorf("Rendezvous = %q, want %q", cfg.Discovery.Rendezvous, "peerup-test-net")
	}
	if !cfg.Security.EnableConnectionGating {
		t.Error("EnableConnectionGating should be true")
	}
	if cfg.Services["ssh"].LocalAddress != "localhost:22" {
		t.Errorf("SSH local_address = %q, want %q", cfg.Services["ssh"].LocalAddress, "localhost:22")
	}
	if cfg.Names["home"] != "12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt" {
		t.Errorf("Names[home] = %q", cfg.Names["home"])
	}
}

func TestLoadNodeConfigMissingFile(t *testing.T) {
	_, err := LoadNodeConfig("/nonexistent/path.yaml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadNodeConfigInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "not: [valid: yaml: {{{")

	_, err := LoadNodeConfig(path)
	if err == nil {
		t.Error("expected error for invalid YAML")
	}
}

func TestLoadNodeConfigInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	yaml := `
identity:
  key_file: "key"
network:
  listen_addresses: ["/ip4/0.0.0.0/tcp/0"]
relay:
  addresses: ["/ip4/1.2.3.4/tcp/7777/p2p/12D3KooWTest"]
  reservation_interval: "not-a-duration"
discovery:
  rendezvous: "test"
security:
  authorized_keys_file: ""
  enable_connection_gating: false
protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"
`
	path := writeTestConfig(t, dir, yaml)

	_, err := LoadNodeConfig(path)
	if err == nil {
		t.Error("expected error for invalid duration")
	}
}

func TestValidateNodeConfig(t *testing.T) {
	valid := &NodeConfig{
		Identity:  IdentityConfig{KeyFile: "key"},
		Network:   NetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/0"}},
		Relay:     RelayConfig{Addresses: []string{"/ip4/1.2.3.4/tcp/7777/p2p/X"}},
		Discovery: DiscoveryConfig{Rendezvous: "test"},
		Security:  SecurityConfig{EnableConnectionGating: false},
		Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "/pingpong/1.0.0"}},
	}

	if err := ValidateNodeConfig(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestValidateNodeConfigMissingFields(t *testing.T) {
	tests := []struct {
		name string
		cfg  NodeConfig
	}{
		{"no key_file", NodeConfig{
			Network:   NetworkConfig{ListenAddresses: []string{"x"}},
			Relay:     RelayConfig{Addresses: []string{"x"}},
			Discovery: DiscoveryConfig{Rendezvous: "x"},
			Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
		}},
		{"no listen_addresses", NodeConfig{
			Identity:  IdentityConfig{KeyFile: "x"},
			Relay:     RelayConfig{Addresses: []string{"x"}},
			Discovery: DiscoveryConfig{Rendezvous: "x"},
			Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
		}},
		{"no relay_addresses", NodeConfig{
			Identity:  IdentityConfig{KeyFile: "x"},
			Network:   NetworkConfig{ListenAddresses: []string{"x"}},
			Discovery: DiscoveryConfig{Rendezvous: "x"},
			Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
		}},
		{"no rendezvous", NodeConfig{
			Identity:  IdentityConfig{KeyFile: "x"},
			Network:   NetworkConfig{ListenAddresses: []string{"x"}},
			Relay:     RelayConfig{Addresses: []string{"x"}},
			Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
		}},
		{"no pingpong_id", NodeConfig{
			Identity:  IdentityConfig{KeyFile: "x"},
			Network:   NetworkConfig{ListenAddresses: []string{"x"}},
			Relay:     RelayConfig{Addresses: []string{"x"}},
			Discovery: DiscoveryConfig{Rendezvous: "x"},
		}},
		{"gating without auth_keys", NodeConfig{
			Identity:  IdentityConfig{KeyFile: "x"},
			Network:   NetworkConfig{ListenAddresses: []string{"x"}},
			Relay:     RelayConfig{Addresses: []string{"x"}},
			Discovery: DiscoveryConfig{Rendezvous: "x"},
			Security:  SecurityConfig{EnableConnectionGating: true, AuthorizedKeysFile: ""},
			Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateNodeConfig(&tt.cfg); err == nil {
				t.Error("expected validation error")
			}
		})
	}
}

func TestResolveConfigPaths(t *testing.T) {
	cfg := &NodeConfig{
		Identity: IdentityConfig{KeyFile: "identity.key"},
		Security: SecurityConfig{AuthorizedKeysFile: "authorized_keys"},
	}

	ResolveConfigPaths(cfg, "/home/user/.config/peerup")

	want := "/home/user/.config/peerup/identity.key"
	if cfg.Identity.KeyFile != want {
		t.Errorf("KeyFile = %q, want %q", cfg.Identity.KeyFile, want)
	}

	want = "/home/user/.config/peerup/authorized_keys"
	if cfg.Security.AuthorizedKeysFile != want {
		t.Errorf("AuthorizedKeysFile = %q, want %q", cfg.Security.AuthorizedKeysFile, want)
	}
}

func TestResolveConfigPathsAbsolute(t *testing.T) {
	cfg := &NodeConfig{
		Identity: IdentityConfig{KeyFile: "/absolute/path/key"},
		Security: SecurityConfig{AuthorizedKeysFile: "/absolute/auth"},
	}

	ResolveConfigPaths(cfg, "/home/user/.config/peerup")

	if cfg.Identity.KeyFile != "/absolute/path/key" {
		t.Errorf("absolute path should not change: %q", cfg.Identity.KeyFile)
	}
	if cfg.Security.AuthorizedKeysFile != "/absolute/auth" {
		t.Errorf("absolute path should not change: %q", cfg.Security.AuthorizedKeysFile)
	}
}

func TestFindConfigFileExplicit(t *testing.T) {
	dir := t.TempDir()
	path := writeTestConfig(t, dir, "identity:\n  key_file: x")

	found, err := FindConfigFile(path)
	if err != nil {
		t.Fatalf("FindConfigFile: %v", err)
	}
	if found != path {
		t.Errorf("found = %q, want %q", found, path)
	}
}

func TestFindConfigFileExplicitMissing(t *testing.T) {
	_, err := FindConfigFile("/nonexistent/config.yaml")
	if err == nil {
		t.Error("expected error for missing explicit path")
	}
}

func TestFindConfigFileLocalDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "peerup.yaml")
	if err := os.WriteFile(configPath, []byte("identity:\n  key_file: x"), 0600); err != nil {
		t.Fatal(err)
	}

	// Change to that dir temporarily
	origDir, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(origDir)

	found, err := FindConfigFile("")
	if err != nil {
		t.Fatalf("FindConfigFile: %v", err)
	}
	if found != "peerup.yaml" {
		t.Errorf("found = %q, want %q", found, "peerup.yaml")
	}
}

func TestLoadRelayServerConfig(t *testing.T) {
	dir := t.TempDir()
	yaml := `
identity:
  key_file: "relay.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
`
	path := filepath.Join(dir, "relay.yaml")
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := LoadRelayServerConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayServerConfig: %v", err)
	}

	if cfg.Identity.KeyFile != "relay.key" {
		t.Errorf("KeyFile = %q", cfg.Identity.KeyFile)
	}
	if len(cfg.Network.ListenAddresses) != 1 {
		t.Errorf("ListenAddresses count = %d", len(cfg.Network.ListenAddresses))
	}
}

func TestConfigVersionDefaultsTo1(t *testing.T) {
	dir := t.TempDir()
	// Config without version field — should default to 1
	path := writeTestConfig(t, dir, testConfigYAML)

	cfg, err := LoadNodeConfig(path)
	if err != nil {
		t.Fatalf("LoadNodeConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1 (default)", cfg.Version)
	}
}

func TestConfigVersionExplicit(t *testing.T) {
	dir := t.TempDir()
	yaml := "version: 1\n" + testConfigYAML
	path := writeTestConfig(t, dir, yaml)

	cfg, err := LoadNodeConfig(path)
	if err != nil {
		t.Fatalf("LoadNodeConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1", cfg.Version)
	}
}

func TestConfigVersionFutureRejected(t *testing.T) {
	dir := t.TempDir()
	yaml := "version: 999\n" + testConfigYAML
	path := writeTestConfig(t, dir, yaml)

	_, err := LoadNodeConfig(path)
	if err == nil {
		t.Error("expected error for future config version")
	}
}

func TestRelayConfigVersionDefaultsTo1(t *testing.T) {
	dir := t.TempDir()
	yaml := `
identity:
  key_file: "relay.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  enable_connection_gating: false
`
	path := filepath.Join(dir, "relay.yaml")
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := LoadRelayServerConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayServerConfig: %v", err)
	}
	if cfg.Version != 1 {
		t.Errorf("Version = %d, want 1 (default)", cfg.Version)
	}
}

func TestRelayConfigVersionFutureRejected(t *testing.T) {
	dir := t.TempDir()
	yaml := `
version: 999
identity:
  key_file: "relay.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  enable_connection_gating: false
`
	path := filepath.Join(dir, "relay.yaml")
	os.WriteFile(path, []byte(yaml), 0600)

	_, err := LoadRelayServerConfig(path)
	if err == nil {
		t.Error("expected error for future relay config version")
	}
}

func TestValidateRelayServerConfig(t *testing.T) {
	valid := &RelayServerConfig{
		Identity: IdentityConfig{KeyFile: "key"},
		Network:  RelayNetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/7777"}},
		Security: RelaySecurityConfig{EnableConnectionGating: false},
	}

	if err := ValidateRelayServerConfig(valid); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	invalid := &RelayServerConfig{
		Network:  RelayNetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/7777"}},
		Security: RelaySecurityConfig{EnableConnectionGating: false},
	}

	if err := ValidateRelayServerConfig(invalid); err == nil {
		t.Error("expected error for missing key_file")
	}
}

func TestValidateNodeConfigServiceNames(t *testing.T) {
	base := NodeConfig{
		Identity:  IdentityConfig{KeyFile: "x"},
		Network:   NetworkConfig{ListenAddresses: []string{"x"}},
		Relay:     RelayConfig{Addresses: []string{"x"}},
		Discovery: DiscoveryConfig{Rendezvous: "x"},
		Protocols: ProtocolsConfig{PingPong: PingPongConfig{ID: "x"}},
	}

	// Valid service names should pass
	valid := base
	valid.Services = ServicesConfig{
		"ssh":  {Enabled: true, LocalAddress: "localhost:22"},
		"xrdp": {Enabled: true, LocalAddress: "localhost:3389"},
	}
	if err := ValidateNodeConfig(&valid); err != nil {
		t.Errorf("valid service names rejected: %v", err)
	}

	// Invalid service name should fail
	invalid := base
	invalid.Services = ServicesConfig{
		"foo/bar": {Enabled: true, LocalAddress: "localhost:8080"},
	}
	if err := ValidateNodeConfig(&invalid); err == nil {
		t.Error("expected error for service name 'foo/bar'")
	}

	// Service name with newline should fail
	invalid2 := base
	invalid2.Services = ServicesConfig{
		"foo\nbar": {Enabled: true, LocalAddress: "localhost:8080"},
	}
	if err := ValidateNodeConfig(&invalid2); err == nil {
		t.Error("expected error for service name with newline")
	}

	// Uppercase service name should fail
	invalid3 := base
	invalid3.Services = ServicesConfig{
		"SSH": {Enabled: true, LocalAddress: "localhost:22"},
	}
	if err := ValidateNodeConfig(&invalid3); err == nil {
		t.Error("expected error for uppercase service name 'SSH'")
	}
}

func TestParseDataSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"128KB", 128 * 1024},
		{"64MB", 64 * 1024 * 1024},
		{"1GB", 1024 * 1024 * 1024},
		{"1024B", 1024},
		{"100", 100},
		{"0B", 0},
		{"128kb", 128 * 1024},
		{"64mb", 64 * 1024 * 1024},
	}
	for _, tc := range tests {
		got, err := ParseDataSize(tc.input)
		if err != nil {
			t.Errorf("ParseDataSize(%q) error = %v", tc.input, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseDataSize(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}

	// Error cases
	invalid := []string{"", "abc", "-1MB", "MB", "1.5MB"}
	for _, s := range invalid {
		if _, err := ParseDataSize(s); err == nil {
			t.Errorf("ParseDataSize(%q) should fail", s)
		}
	}
}

func TestLoadRelayServerConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	yaml := `
identity:
  key_file: "relay.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  enable_connection_gating: false
`
	path := filepath.Join(dir, "relay.yaml")
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := LoadRelayServerConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayServerConfig: %v", err)
	}

	// Verify resource defaults were applied
	if cfg.Resources.MaxReservations != 128 {
		t.Errorf("MaxReservations = %d, want 128", cfg.Resources.MaxReservations)
	}
	if cfg.Resources.MaxCircuits != 16 {
		t.Errorf("MaxCircuits = %d, want 16", cfg.Resources.MaxCircuits)
	}
	if cfg.Resources.SessionDuration != "10m" {
		t.Errorf("SessionDuration = %q, want %q", cfg.Resources.SessionDuration, "10m")
	}
	if cfg.Resources.SessionDataLimit != "64MB" {
		t.Errorf("SessionDataLimit = %q, want %q", cfg.Resources.SessionDataLimit, "64MB")
	}
}

func TestLoadRelayServerConfigCustomResources(t *testing.T) {
	dir := t.TempDir()
	yaml := `
identity:
  key_file: "relay.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/7777"
security:
  enable_connection_gating: false
resources:
  max_reservations: 64
  session_duration: "30m"
  session_data_limit: "256MB"
`
	path := filepath.Join(dir, "relay.yaml")
	os.WriteFile(path, []byte(yaml), 0600)

	cfg, err := LoadRelayServerConfig(path)
	if err != nil {
		t.Fatalf("LoadRelayServerConfig: %v", err)
	}

	if cfg.Resources.MaxReservations != 64 {
		t.Errorf("MaxReservations = %d, want 64", cfg.Resources.MaxReservations)
	}
	if cfg.Resources.SessionDuration != "30m" {
		t.Errorf("SessionDuration = %q, want %q", cfg.Resources.SessionDuration, "30m")
	}
	if cfg.Resources.SessionDataLimit != "256MB" {
		t.Errorf("SessionDataLimit = %q, want %q", cfg.Resources.SessionDataLimit, "256MB")
	}
	// Defaults should fill in unset fields
	if cfg.Resources.MaxCircuits != 16 {
		t.Errorf("MaxCircuits = %d, want 16 (default)", cfg.Resources.MaxCircuits)
	}
}

func TestValidateRelayServerConfigBadDuration(t *testing.T) {
	cfg := &RelayServerConfig{
		Identity:  IdentityConfig{KeyFile: "key"},
		Network:   RelayNetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/7777"}},
		Security:  RelaySecurityConfig{EnableConnectionGating: false},
		Resources: RelayResourcesConfig{SessionDuration: "not-a-duration"},
	}

	if err := ValidateRelayServerConfig(cfg); err == nil {
		t.Error("expected error for invalid session_duration")
	}
}

func TestValidateRelayServerConfigBadDataSize(t *testing.T) {
	cfg := &RelayServerConfig{
		Identity:  IdentityConfig{KeyFile: "key"},
		Network:   RelayNetworkConfig{ListenAddresses: []string{"/ip4/0.0.0.0/tcp/7777"}},
		Security:  RelaySecurityConfig{EnableConnectionGating: false},
		Resources: RelayResourcesConfig{SessionDataLimit: "abc"},
	}

	if err := ValidateRelayServerConfig(cfg); err == nil {
		t.Error("expected error for invalid session_data_limit")
	}
}
