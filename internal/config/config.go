package config

import (
	"time"
)

// HomeNodeConfig represents configuration for the home node
type HomeNodeConfig struct {
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
	Identity IdentityConfig       `yaml:"identity"`
	Network  RelayNetworkConfig   `yaml:"network"`
	Security RelaySecurityConfig  `yaml:"security"`
}

// IdentityConfig holds identity-related configuration
type IdentityConfig struct {
	KeyFile string `yaml:"key_file"`
}

// NetworkConfig holds network-related configuration
type NetworkConfig struct {
	ListenAddresses          []string `yaml:"listen_addresses"`
	ForcePrivateReachability bool     `yaml:"force_private_reachability"`
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
	Enabled      bool   `yaml:"enabled"`
	LocalAddress string `yaml:"local_address"`
	Protocol     string `yaml:"protocol,omitempty"` // Optional custom protocol ID
}

// NamesConfig holds name resolution configuration
type NamesConfig map[string]string // name → peer ID

// NodeConfig is the unified configuration for all peerup modes.
// HomeNodeConfig already has all fields (Identity, Network, Relay, Discovery,
// Security, Protocols, Services, Names). ClientNodeConfig is a strict subset.
type NodeConfig = HomeNodeConfig

// Config is a unified configuration structure for all components
type Config struct {
	Identity  IdentityConfig  `yaml:"identity"`
	Network   NetworkConfig   `yaml:"network"`
	Relay     RelayConfig     `yaml:"relay,omitempty"`
	Discovery DiscoveryConfig `yaml:"discovery,omitempty"`
	Security  SecurityConfig  `yaml:"security"`
	Protocols ProtocolsConfig `yaml:"protocols,omitempty"`
	Services  ServicesConfig  `yaml:"services,omitempty"`
	Names     NamesConfig     `yaml:"names,omitempty"`
}
