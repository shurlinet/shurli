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
	CLI       CLIConfig       `yaml:"cli,omitempty"`
	Telemetry TelemetryConfig `yaml:"telemetry,omitempty"`
}

// CLIConfig holds settings for CLI subcommand behavior.
type CLIConfig struct {
	// AllowStandalone permits subcommands (proxy, ping, traceroute) to create
	// their own P2P host when no daemon is running. Default: false (daemon required).
	// This is a debug/development option. In normal use, the daemon manages
	// all connections and subcommands talk to it via the local API.
	AllowStandalone bool `yaml:"allow_standalone,omitempty"`
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
	Discovery RelayDiscoveryConfig `yaml:"discovery,omitempty"`
	Security  RelaySecurityConfig  `yaml:"security"`
	Resources RelayResourcesConfig `yaml:"resources,omitempty"`
	Health    HealthConfig         `yaml:"health,omitempty"`
	Telemetry TelemetryConfig      `yaml:"telemetry,omitempty"`
}

// TelemetryConfig holds observability settings.
// All features are disabled by default (opt-in).
type TelemetryConfig struct {
	Metrics MetricsConfig `yaml:"metrics,omitempty"`
	Audit   AuditConfig   `yaml:"audit,omitempty"`
}

// MetricsConfig controls Prometheus metrics exposure.
type MetricsConfig struct {
	Enabled       bool   `yaml:"enabled"`
	ListenAddress string `yaml:"listen_address"` // default: "127.0.0.1:9091"
}

// AuditConfig controls structured audit logging.
type AuditConfig struct {
	Enabled bool `yaml:"enabled"`
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
	ForceCGNAT               bool     `yaml:"force_cgnat,omitempty"`
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
	Rendezvous       string        `yaml:"rendezvous"`
	Network          string        `yaml:"network,omitempty"`            // DHT namespace for private networks (empty = global)
	BootstrapPeers   []string      `yaml:"bootstrap_peers"`
	MDNSEnabled      *bool         `yaml:"mdns_enabled,omitempty"`      // LAN peer discovery (default: true)
	NetIntelEnabled  *bool         `yaml:"net_intel_enabled,omitempty"` // Presence announcements (default: true)
	AnnounceInterval time.Duration `yaml:"announce_interval,omitempty"` // How often to push state (default: 5m)
}

// IsMDNSEnabled returns whether mDNS local discovery is enabled.
// Defaults to true when not explicitly set in config.
func (d *DiscoveryConfig) IsMDNSEnabled() bool {
	if d.MDNSEnabled == nil {
		return true
	}
	return *d.MDNSEnabled
}

// IsNetIntelEnabled returns whether network intelligence presence
// announcements are enabled. Defaults to true when not explicitly set.
func (d *DiscoveryConfig) IsNetIntelEnabled() bool {
	if d.NetIntelEnabled == nil {
		return true
	}
	return *d.NetIntelEnabled
}

// RelayDiscoveryConfig holds relay server discovery configuration
type RelayDiscoveryConfig struct {
	Network string `yaml:"network,omitempty"` // DHT namespace (must match connecting nodes)
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

// NodeConfig is the unified configuration for all shurli modes.
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
