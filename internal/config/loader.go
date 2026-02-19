package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/satindergrewal/peer-up/internal/validate"
)

// checkConfigFilePermissions warns if a config file has overly permissive
// permissions (group/world readable). Config files may contain sensitive
// paths and network topology. Returns an error on multi-user systems
// where the file is world-readable.
func checkConfigFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return nil // file access errors are handled by the caller
	}
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return fmt.Errorf("config file %s has overly permissive mode %04o; expected 0600 — fix with: chmod 600 %s", path, mode, path)
	}
	return nil
}

// LoadHomeNodeConfig loads home node configuration from a YAML file
func LoadHomeNodeConfig(path string) (*HomeNodeConfig, error) {
	if err := checkConfigFilePermissions(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	// Parse YAML with custom unmarshaling for durations
	var rawConfig struct {
		Version   int             `yaml:"version,omitempty"`
		Identity  IdentityConfig  `yaml:"identity"`
		Network   NetworkConfig   `yaml:"network"`
		Relay     struct {
			Addresses           []string `yaml:"addresses"`
			ReservationInterval string   `yaml:"reservation_interval"`
		} `yaml:"relay"`
		Discovery DiscoveryConfig `yaml:"discovery"`
		Security  SecurityConfig  `yaml:"security"`
		Protocols ProtocolsConfig `yaml:"protocols"`
		Services  ServicesConfig  `yaml:"services,omitempty"`
		Names     NamesConfig     `yaml:"names,omitempty"`
	}

	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Default version to 1 for configs written before versioning was added
	version := rawConfig.Version
	if version == 0 {
		version = 1
	}
	if version > CurrentConfigVersion {
		return nil, fmt.Errorf("%w: version %d is newer than supported version %d; please upgrade peerup", ErrConfigVersionTooNew, version, CurrentConfigVersion)
	}

	// Parse duration
	reservationInterval, err := time.ParseDuration(rawConfig.Relay.ReservationInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid reservation_interval: %w", err)
	}

	config := &HomeNodeConfig{
		Version:   version,
		Identity:  rawConfig.Identity,
		Network:   rawConfig.Network,
		Discovery: rawConfig.Discovery,
		Security:  rawConfig.Security,
		Protocols: rawConfig.Protocols,
		Services:  rawConfig.Services,
		Names:     rawConfig.Names,
		Relay: RelayConfig{
			Addresses:           rawConfig.Relay.Addresses,
			ReservationInterval: reservationInterval,
		},
	}

	return config, nil
}

// LoadClientNodeConfig loads client node configuration from a YAML file
func LoadClientNodeConfig(path string) (*ClientNodeConfig, error) {
	if err := checkConfigFilePermissions(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	// Parse YAML with custom unmarshaling for durations
	var rawConfig struct {
		Identity  IdentityConfig  `yaml:"identity"`
		Network   NetworkConfig   `yaml:"network"`
		Relay     struct {
			Addresses           []string `yaml:"addresses"`
			ReservationInterval string   `yaml:"reservation_interval"`
		} `yaml:"relay"`
		Discovery DiscoveryConfig `yaml:"discovery"`
		Security  SecurityConfig  `yaml:"security"`
		Protocols ProtocolsConfig `yaml:"protocols"`
		Names     NamesConfig     `yaml:"names,omitempty"`
	}

	if err := yaml.Unmarshal(data, &rawConfig); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Parse duration
	reservationInterval, err := time.ParseDuration(rawConfig.Relay.ReservationInterval)
	if err != nil {
		return nil, fmt.Errorf("invalid reservation_interval: %w", err)
	}

	config := &ClientNodeConfig{
		Identity:  rawConfig.Identity,
		Network:   rawConfig.Network,
		Discovery: rawConfig.Discovery,
		Security:  rawConfig.Security,
		Protocols: rawConfig.Protocols,
		Names:     rawConfig.Names,
		Relay: RelayConfig{
			Addresses:           rawConfig.Relay.Addresses,
			ReservationInterval: reservationInterval,
		},
	}

	return config, nil
}

// LoadRelayServerConfig loads relay server configuration from a YAML file
func LoadRelayServerConfig(path string) (*RelayServerConfig, error) {
	if err := checkConfigFilePermissions(path); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var config RelayServerConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Default version to 1 for configs written before versioning was added
	if config.Version == 0 {
		config.Version = 1
	}
	if config.Version > CurrentConfigVersion {
		return nil, fmt.Errorf("%w: version %d is newer than supported version %d; please upgrade relay-server", ErrConfigVersionTooNew, config.Version, CurrentConfigVersion)
	}

	// Apply defaults for zero-valued resource fields
	applyRelayResourceDefaults(&config.Resources)

	// Apply health endpoint defaults
	if config.Health.Enabled && config.Health.ListenAddress == "" {
		config.Health.ListenAddress = "127.0.0.1:9090"
	}

	return &config, nil
}

// ValidateHomeNodeConfig validates home node configuration
func ValidateHomeNodeConfig(cfg *HomeNodeConfig) error {
	if cfg.Identity.KeyFile == "" {
		return fmt.Errorf("identity.key_file is required")
	}
	if len(cfg.Network.ListenAddresses) == 0 {
		return fmt.Errorf("network.listen_addresses must contain at least one address")
	}
	if len(cfg.Relay.Addresses) == 0 {
		return fmt.Errorf("relay.addresses must contain at least one address")
	}
	if cfg.Discovery.Rendezvous == "" {
		return fmt.Errorf("discovery.rendezvous is required")
	}
	if cfg.Protocols.PingPong.ID == "" {
		return fmt.Errorf("protocols.ping_pong.id is required")
	}
	if cfg.Security.EnableConnectionGating && cfg.Security.AuthorizedKeysFile == "" {
		return fmt.Errorf("security.authorized_keys_file is required when connection gating is enabled")
	}
	return nil
}

// ValidateClientNodeConfig validates client node configuration
func ValidateClientNodeConfig(cfg *ClientNodeConfig) error {
	if len(cfg.Network.ListenAddresses) == 0 {
		return fmt.Errorf("network.listen_addresses must contain at least one address")
	}
	if len(cfg.Relay.Addresses) == 0 {
		return fmt.Errorf("relay.addresses must contain at least one address")
	}
	if cfg.Discovery.Rendezvous == "" {
		return fmt.Errorf("discovery.rendezvous is required")
	}
	if cfg.Protocols.PingPong.ID == "" {
		return fmt.Errorf("protocols.ping_pong.id is required")
	}
	if cfg.Security.EnableConnectionGating && cfg.Security.AuthorizedKeysFile == "" {
		return fmt.Errorf("security.authorized_keys_file is required when connection gating is enabled")
	}
	return nil
}

// FindConfigFile searches for a peerup config file in standard locations.
// Search order: explicitPath (if given), ./peerup.yaml, ~/.config/peerup/config.yaml, /etc/peerup/config.yaml
func FindConfigFile(explicitPath string) (string, error) {
	if explicitPath != "" {
		if _, err := os.Stat(explicitPath); err != nil {
			return "", fmt.Errorf("%w: %s", ErrConfigNotFound, explicitPath)
		}
		return explicitPath, nil
	}

	searchPaths := []string{
		"peerup.yaml",
	}

	// ~/.config/peerup/config.yaml
	if home, err := os.UserHomeDir(); err == nil {
		searchPaths = append(searchPaths, filepath.Join(home, ".config", "peerup", "config.yaml"))
	}

	searchPaths = append(searchPaths, filepath.Join("/etc", "peerup", "config.yaml"))

	for _, path := range searchPaths {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("%w; searched:\n  %s\n\nRun 'peerup init' to create one, or use --config <path>", ErrConfigNotFound, strings.Join(searchPaths, "\n  "))
}

// LoadNodeConfig loads unified node configuration from a YAML file.
// This is the preferred loader for all peerup commands.
func LoadNodeConfig(path string) (*NodeConfig, error) {
	return LoadHomeNodeConfig(path)
}

// ResolveConfigPaths resolves relative file paths in the config to be relative
// to the config file's directory. This allows configs in ~/.config/peerup/ to
// reference key files and authorized_keys using relative paths.
func ResolveConfigPaths(cfg *NodeConfig, configDir string) {
	if cfg.Identity.KeyFile != "" && !filepath.IsAbs(cfg.Identity.KeyFile) {
		cfg.Identity.KeyFile = filepath.Join(configDir, cfg.Identity.KeyFile)
	}
	if cfg.Security.AuthorizedKeysFile != "" && !filepath.IsAbs(cfg.Security.AuthorizedKeysFile) {
		cfg.Security.AuthorizedKeysFile = filepath.Join(configDir, cfg.Security.AuthorizedKeysFile)
	}
}

// ValidateNodeConfig validates unified node configuration.
func ValidateNodeConfig(cfg *NodeConfig) error {
	if cfg.Identity.KeyFile == "" {
		return fmt.Errorf("identity.key_file is required")
	}
	if len(cfg.Network.ListenAddresses) == 0 {
		return fmt.Errorf("network.listen_addresses must contain at least one address")
	}
	if len(cfg.Relay.Addresses) == 0 {
		return fmt.Errorf("relay.addresses must contain at least one address")
	}
	if cfg.Discovery.Rendezvous == "" {
		return fmt.Errorf("discovery.rendezvous is required")
	}
	if cfg.Protocols.PingPong.ID == "" {
		return fmt.Errorf("protocols.ping_pong.id is required")
	}
	if cfg.Security.EnableConnectionGating && cfg.Security.AuthorizedKeysFile == "" {
		return fmt.Errorf("security.authorized_keys_file is required when connection gating is enabled")
	}
	// Validate service names (prevent protocol ID injection)
	for name := range cfg.Services {
		if err := validate.ServiceName(name); err != nil {
			return fmt.Errorf("services: %w", err)
		}
	}
	return nil
}

// DefaultConfigDir returns the default peerup config directory (~/.config/peerup).
func DefaultConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "peerup"), nil
}

// ValidateRelayServerConfig validates relay server configuration
func ValidateRelayServerConfig(cfg *RelayServerConfig) error {
	if cfg.Identity.KeyFile == "" {
		return fmt.Errorf("identity.key_file is required")
	}
	if len(cfg.Network.ListenAddresses) == 0 {
		return fmt.Errorf("network.listen_addresses must contain at least one address")
	}
	if cfg.Security.EnableConnectionGating && cfg.Security.AuthorizedKeysFile == "" {
		return fmt.Errorf("security.authorized_keys_file is required when connection gating is enabled")
	}
	// Validate resource durations if set
	if cfg.Resources.ReservationTTL != "" {
		if _, err := time.ParseDuration(cfg.Resources.ReservationTTL); err != nil {
			return fmt.Errorf("resources.reservation_ttl: %w", err)
		}
	}
	if cfg.Resources.SessionDuration != "" {
		if _, err := time.ParseDuration(cfg.Resources.SessionDuration); err != nil {
			return fmt.Errorf("resources.session_duration: %w", err)
		}
	}
	if cfg.Resources.SessionDataLimit != "" {
		if _, err := ParseDataSize(cfg.Resources.SessionDataLimit); err != nil {
			return fmt.Errorf("resources.session_data_limit: %w", err)
		}
	}
	return nil
}

// DefaultRelayResources returns the default relay resource configuration.
// Values are tuned for a private relay serving 2-10 peers with SSH/XRDP workloads.
func DefaultRelayResources() RelayResourcesConfig {
	return RelayResourcesConfig{
		MaxReservations:      128,
		MaxCircuits:          16,
		BufferSize:           2048,
		MaxReservationsPerIP: 8,
		MaxReservationsPerASN: 32,
		ReservationTTL:       "1h",
		SessionDuration:      "10m",
		SessionDataLimit:     "64MB",
	}
}

// applyRelayResourceDefaults fills zero-valued fields with defaults.
func applyRelayResourceDefaults(rc *RelayResourcesConfig) {
	defaults := DefaultRelayResources()
	if rc.MaxReservations == 0 {
		rc.MaxReservations = defaults.MaxReservations
	}
	if rc.MaxCircuits == 0 {
		rc.MaxCircuits = defaults.MaxCircuits
	}
	if rc.BufferSize == 0 {
		rc.BufferSize = defaults.BufferSize
	}
	if rc.MaxReservationsPerIP == 0 {
		rc.MaxReservationsPerIP = defaults.MaxReservationsPerIP
	}
	if rc.MaxReservationsPerASN == 0 {
		rc.MaxReservationsPerASN = defaults.MaxReservationsPerASN
	}
	if rc.ReservationTTL == "" {
		rc.ReservationTTL = defaults.ReservationTTL
	}
	if rc.SessionDuration == "" {
		rc.SessionDuration = defaults.SessionDuration
	}
	if rc.SessionDataLimit == "" {
		rc.SessionDataLimit = defaults.SessionDataLimit
	}
}

// ParseDataSize parses a human-readable data size string (e.g., "128KB", "64MB", "1GB")
// and returns the value in bytes. Supported suffixes: B, KB, MB, GB (case-insensitive).
func ParseDataSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty data size")
	}

	s = strings.ToUpper(s)
	var multiplier int64 = 1
	var numStr string

	switch {
	case strings.HasSuffix(s, "GB"):
		multiplier = 1024 * 1024 * 1024
		numStr = strings.TrimSuffix(s, "GB")
	case strings.HasSuffix(s, "MB"):
		multiplier = 1024 * 1024
		numStr = strings.TrimSuffix(s, "MB")
	case strings.HasSuffix(s, "KB"):
		multiplier = 1024
		numStr = strings.TrimSuffix(s, "KB")
	case strings.HasSuffix(s, "B"):
		numStr = strings.TrimSuffix(s, "B")
	default:
		// Try parsing as plain number (bytes)
		numStr = s
	}

	numStr = strings.TrimSpace(numStr)
	val, err := strconv.ParseInt(numStr, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid data size %q: %w", s, err)
	}
	if val < 0 {
		return 0, fmt.Errorf("data size must be non-negative: %s", s)
	}
	return val * multiplier, nil
}
