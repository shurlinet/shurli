package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// LoadHomeNodeConfig loads home node configuration from a YAML file
func LoadHomeNodeConfig(path string) (*HomeNodeConfig, error) {
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
		Services  ServicesConfig  `yaml:"services,omitempty"`
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

	config := &HomeNodeConfig{
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var config RelayServerConfig
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("failed to parse YAML: %w", err)
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
			return "", fmt.Errorf("config file not found: %s", explicitPath)
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

	return "", fmt.Errorf("no config file found; searched:\n  %s\n\nRun 'peerup init' to create one, or use --config <path>", strings.Join(searchPaths, "\n  "))
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
	return nil
}
