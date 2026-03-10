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
	PeerRelay PeerRelayConfig `yaml:"peer_relay,omitempty"`
	Transfer  TransferConfig  `yaml:"transfer,omitempty"`
}

// TransferConfig controls peer-to-peer file transfer.
type TransferConfig struct {
	// ReceiveDir is the directory where received files are saved.
	// Default: ~/Downloads/shurli/
	ReceiveDir string `yaml:"receive_dir,omitempty"`

	// MaxFileSize is the maximum file size to accept (in bytes).
	// 0 means unlimited (up to protocol max of 1 TB).
	MaxFileSize int64 `yaml:"max_file_size,omitempty"`

	// ReceiveMode controls how incoming transfers are handled.
	// Values: "off", "contacts" (default), "ask", "open"
	ReceiveMode string `yaml:"receive_mode,omitempty"`

	// Compress enables zstd compression for outgoing transfers.
	// Default: true. Set to false to disable.
	Compress *bool `yaml:"compress,omitempty"`

	// LogPath is the file path for structured transfer event logging.
	// Default: ~/.config/shurli/logs/transfers.log
	// Set to empty string to disable transfer logging.
	LogPath string `yaml:"log_path,omitempty"`

	// Notify controls notifications for incoming transfers.
	// Values: "none" (default), "desktop" (OS-native), "command" (custom).
	// Runtime reloadable.
	Notify string `yaml:"notify,omitempty"`

	// NotifyCommand is the command template for "command" notify mode.
	// Placeholders: {from} = peer ID, {file} = filename, {size} = size in bytes.
	// Runtime reloadable.
	NotifyCommand string `yaml:"notify_command,omitempty"`

	// TimedDuration is the default duration for timed receive mode.
	// Go duration string (e.g. "10m", "1h"). Default: "10m".
	// Runtime reloadable.
	TimedDuration string `yaml:"timed_duration,omitempty"`

	// ErasureOverhead controls Reed-Solomon parity overhead for transfers.
	// 0.10 = 10% parity chunks. 0 = disabled. Default: 0.1 (10%).
	// Auto-enabled on Direct WAN connections.
	ErasureOverhead *float64 `yaml:"erasure_overhead,omitempty"`
}

// PeerRelayConfig controls whether this node acts as a circuit relay for
// other authorized peers. When enabled, peers behind NAT can use this node
// as an alternative to the VPS relay.
type PeerRelayConfig struct {
	// Enabled controls relay behavior: "auto" (default) enables relay when a
	// public IP is detected, "true" forces relay on, "false" forces relay off.
	Enabled string `yaml:"enabled,omitempty"`

	// Resources overrides the default relay resource limits.
	Resources PeerRelayResourcesConfig `yaml:"resources,omitempty"`
}

// PeerRelayResourcesConfig allows tuning peer relay resource limits.
// Zero values use conservative defaults.
type PeerRelayResourcesConfig struct {
	MaxReservations        int    `yaml:"max_reservations,omitempty"`          // default: 4
	MaxCircuits            int    `yaml:"max_circuits,omitempty"`              // default: 16
	MaxReservationsPerPeer int    `yaml:"max_reservations_per_peer,omitempty"` // default: 1
	MaxReservationsPerIP   int    `yaml:"max_reservations_per_ip,omitempty"`   // default: 2
	MaxReservationsPerASN  int    `yaml:"max_reservations_per_asn,omitempty"`  // default: 4
	BufferSize             int    `yaml:"buffer_size,omitempty"`               // default: 4096
	CircuitDuration        string `yaml:"circuit_duration,omitempty"`          // default: "10m"
	CircuitDataLimit       string `yaml:"circuit_data_limit,omitempty"`        // default: "128KB"
}

// CLIConfig holds settings for CLI subcommand behavior.
type CLIConfig struct {
	// AllowStandalone permits subcommands (proxy, ping, traceroute) to create
	// their own P2P host when no daemon is running. Default: false (daemon required).
	// This is a debug/development option. In normal use, the daemon manages
	// all connections and subcommands talk to it via the local API.
	AllowStandalone bool `yaml:"allow_standalone,omitempty"`

	// Color controls terminal color output. Default: true (colors enabled).
	// Set to false to disable all ANSI color codes in CLI output.
	// Can also be disabled via the NO_COLOR environment variable.
	Color *bool `yaml:"color,omitempty"`
}

// IsColorEnabled returns whether CLI color output is enabled.
// Defaults to true when not explicitly set in config.
func (c *CLIConfig) IsColorEnabled() bool {
	if c.Color == nil {
		return true
	}
	return *c.Color
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
	Name      string               `yaml:"name,omitempty"`
	Identity  IdentityConfig       `yaml:"identity"`
	Network   RelayNetworkConfig   `yaml:"network"`
	Discovery RelayDiscoveryConfig `yaml:"discovery,omitempty"`
	Security  RelaySecurityConfig  `yaml:"security"`
	Resources RelayResourcesConfig `yaml:"resources,omitempty"`
	Health    HealthConfig         `yaml:"health,omitempty"`
	Telemetry TelemetryConfig      `yaml:"telemetry,omitempty"`
	CLI       CLIConfig            `yaml:"cli,omitempty"`
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
	DNSSeedDomain    string        `yaml:"dns_seed_domain,omitempty"`   // DNS seed domain (default: seeds.shurli.io)
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
	AuthorizedKeysFile     string    `yaml:"authorized_keys_file"`
	EnableConnectionGating bool      `yaml:"enable_connection_gating"`
	InvitePolicy           string    `yaml:"invite_policy,omitempty"` // "admin-only" (default) or "open"
	ZKP                    ZKPConfig `yaml:"zkp,omitempty"`
}

// ZKPConfig holds zero-knowledge proof configuration.
type ZKPConfig struct {
	Enabled      bool   `yaml:"enabled"`                   // master toggle (default: false)
	SRSCacheDir  string `yaml:"srs_cache_dir,omitempty"`   // KZG SRS cache (default: ~/.shurli/zkp/)
	MaxTreeDepth int    `yaml:"max_tree_depth,omitempty"`  // Merkle tree depth (default: 20, supports ~1M peers)
}

// RelaySecurityConfig holds relay server security configuration
type RelaySecurityConfig struct {
	AuthorizedKeysFile     string    `yaml:"authorized_keys_file"`
	EnableConnectionGating bool      `yaml:"enable_connection_gating"`
	EnableDataRelay        bool      `yaml:"enable_data_relay,omitempty"` // allow circuit data relay (default: false, signaling-only)
	InvitePolicy           string    `yaml:"invite_policy,omitempty"`     // "admin-only" (default) or "open"
	VaultFile              string    `yaml:"vault_file,omitempty"`        // path to sealed vault JSON (empty = no vault)
	RequireTOTP            bool      `yaml:"require_totp,omitempty"`      // require TOTP for vault unseal
	AutoSealMinutes        int       `yaml:"auto_seal_minutes,omitempty"` // auto-seal after N minutes (0 = disabled)
	ZKP                    ZKPConfig `yaml:"zkp,omitempty"`
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
