package config

import (
	"time"
)

// CurrentConfigVersion is the latest configuration schema version.
// Bump this when adding fields that require migration.
const CurrentConfigVersion = 1

// HomeNodeConfig represents configuration for the home node
type HomeNodeConfig struct {
	Version   int             `yaml:"version,omitempty"`
	Identity  IdentityConfig  `yaml:"identity"`
	Network   NetworkConfig   `yaml:"network"`
	Relay     RelayConfig     `yaml:"relay"`
	Discovery DiscoveryConfig `yaml:"discovery"`
	Security  SecurityConfig  `yaml:"security"`
	Protocols ProtocolsConfig `yaml:"protocols"`
	Services  ServicesConfig  `yaml:"services,omitempty"`
	Names     NamesConfig     `yaml:"names,omitempty"`
}

// ClientNodeConfig represents configuration for the client node
type ClientNodeConfig struct {
	Identity  IdentityConfig  `yaml:"identity"`
	Network   NetworkConfig   `yaml:"network"`
	Relay     RelayConfig     `yaml:"relay"`
	Discovery DiscoveryConfig `yaml:"discovery"`
	Security  SecurityConfig  `yaml:"security"`
	Protocols ProtocolsConfig `yaml:"protocols"`
	Names     NamesConfig     `yaml:"names,omitempty"`
}

// RelayServerConfig represents configuration for the relay server
type RelayServerConfig struct {
	Version   int                  `yaml:"version,omitempty"`
	Identity  IdentityConfig       `yaml:"identity"`
	Network   RelayNetworkConfig   `yaml:"network"`
	Security  RelaySecurityConfig  `yaml:"security"`
	Resources RelayResourcesConfig `yaml:"resources,omitempty"`
	Health    HealthConfig         `yaml:"health,omitempty"`
}

// HealthConfig holds HTTP health check endpoint configuration.
type HealthConfig struct {
	Enabled       bool   `yaml:"enabled"`
	ListenAddress string `yaml:"listen_address"`
}

// IdentityConfig holds identity-related configuration
type IdentityConfig struct {
	KeyFile string `yaml:"key_file"`
}

// NetworkConfig holds network-related configuration
type NetworkConfig struct {
	ListenAddresses          []string `yaml:"listen_addresses"`
	ForcePrivateReachability bool     `yaml:"force_private_reachability"`
	ResourceLimitsEnabled    bool     `yaml:"resource_limits_enabled"`
}

// RelayNetworkConfig holds relay server network configuration
type RelayNetworkConfig struct {
	ListenAddresses []string `yaml:"listen_addresses"`
}

// RelayConfig holds relay-related configuration
type RelayConfig struct {
	Addresses           []string      `yaml:"addresses"`
	ReservationInterval time.Duration `yaml:"reservation_interval"`
}

// DiscoveryConfig holds DHT discovery configuration
type DiscoveryConfig struct {
	Rendezvous     string   `yaml:"rendezvous"`
	BootstrapPeers []string `yaml:"bootstrap_peers"`
}

// SecurityConfig holds security-related configuration
type SecurityConfig struct {
	AuthorizedKeysFile     string `yaml:"authorized_keys_file"`
	EnableConnectionGating bool   `yaml:"enable_connection_gating"`
}

// RelaySecurityConfig holds relay server security configuration
type RelaySecurityConfig struct {
	AuthorizedKeysFile     string `yaml:"authorized_keys_file"`
	EnableConnectionGating bool   `yaml:"enable_connection_gating"`
}

// RelayResourcesConfig holds relay v2 resource limit configuration.
// Zero values are replaced with defaults at load time.
type RelayResourcesConfig struct {
	MaxReservations      int    `yaml:"max_reservations"`         // default: 128
	MaxCircuits          int    `yaml:"max_circuits"`             // default: 16
	BufferSize           int    `yaml:"buffer_size"`              // default: 2048
	MaxReservationsPerIP int    `yaml:"max_reservations_per_ip"`  // default: 8
	MaxReservationsPerASN int   `yaml:"max_reservations_per_asn"` // default: 32
	ReservationTTL       string `yaml:"reservation_ttl"`          // default: "1h"
	SessionDuration      string `yaml:"session_duration"`         // default: "10m"
	SessionDataLimit     string `yaml:"session_data_limit"`       // default: "64MB"
}

// ProtocolsConfig holds protocol-specific configuration
type ProtocolsConfig struct {
	PingPong PingPongConfig `yaml:"ping_pong"`
}

// PingPongConfig holds ping-pong protocol configuration
type PingPongConfig struct {
	Enabled bool   `yaml:"enabled"`
	ID      string `yaml:"id"`
}

// ServicesConfig holds service exposure configuration
type ServicesConfig map[string]ServiceConfig

// ServiceConfig holds configuration for a single exposed service
type ServiceConfig struct {
	Enabled      bool     `yaml:"enabled"`
	LocalAddress string   `yaml:"local_address"`
	Protocol     string   `yaml:"protocol,omitempty"`        // Optional custom protocol ID
	AllowedPeers []string `yaml:"allowed_peers,omitempty"`   // Restrict to specific peer IDs (nil = all authorized peers)
}

// NamesConfig holds name resolution configuration
type NamesConfig map[string]string // name → peer ID

// NodeConfig is the unified configuration for all peerup modes.
// HomeNodeConfig already has all fields (Identity, Network, Relay, Discovery,
// Security, Protocols, Services, Names). ClientNodeConfig is a strict subset.
type NodeConfig = HomeNodeConfig

// Config is a unified configuration structure for all components
type Config struct {
	Version   int             `yaml:"version,omitempty"`
	Identity  IdentityConfig  `yaml:"identity"`
	Network   NetworkConfig   `yaml:"network"`
	Relay     RelayConfig     `yaml:"relay,omitempty"`
	Discovery DiscoveryConfig `yaml:"discovery,omitempty"`
	Security  SecurityConfig  `yaml:"security"`
	Protocols ProtocolsConfig `yaml:"protocols,omitempty"`
	Services  ServicesConfig  `yaml:"services,omitempty"`
	Names     NamesConfig     `yaml:"names,omitempty"`
}
