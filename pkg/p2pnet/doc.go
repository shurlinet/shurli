// Package p2pnet provides the core P2P networking library for Shurli.
//
// This package is the primary SDK surface for Go consumers. External code
// should depend on the interfaces defined here (PeerNetwork, Resolver,
// ServiceManager, Authorizer) rather than concrete types where possible.
//
// # Architecture
//
// The package is organized around these concepts:
//
//   - Network: creates and manages a libp2p host with relay, NAT traversal,
//     DHT discovery, and resource limits. Entry point: [New].
//
//   - Services: the plugin system. TCP proxy services (ExposeService) forward
//     streams to local ports. Plugin services (RegisterHandler) process streams
//     directly. All services are managed through [ServiceRegistry].
//
//   - Plugin Policies: transport-aware access control for plugins. By default,
//     all plugins only operate over LAN and Direct connections. Relay transport
//     is excluded unless explicitly allowed per-plugin. See [PluginPolicy] and
//     [DefaultPluginPolicy].
//
//   - Events: the [EventBus] dispatches connection, service, and authorization
//     events to registered handlers.
//
//   - Naming: the [NameResolver] maps human-readable names to peer IDs, with
//     support for fallback resolver chains.
//
//   - Standalone: [NewStandaloneHost] and [StandaloneResult.ResolveAndConnect]
//     provide a consolidated path for one-shot CLI commands that operate without
//     a running daemon.
//
//   - Bootstrap: [BootstrapAndConnect] handles DHT client-mode bootstrap,
//     peer discovery, and relay circuit fallback.
//
//   - Transfer: [TransferService] implements chunked file transfer with the
//     SHFT wire format (v2). Features: FastCDC content-defined chunking,
//     BLAKE3 Merkle integrity, zstd compression (on by default), receive
//     permissions (off/contacts/ask/open), disk space checks, atomic writes,
//     per-chunk hash verification, resumable transfers (bitfield checkpoint
//     persistence + sparse file writes for out-of-order chunks), and
//     ResumeRequest/ResumeResponse protocol. Protocol: /shurli/file-transfer/2.0.0.
//
//   - Protocol IDs: [ProtocolID] and [MustValidateProtocolIDs] enforce valid
//     protocol ID construction at init time.
//
// # Relay Separation
//
// Relay server protocols (pairing, admin, MOTD, unseal) live in internal/relay
// and register directly on the libp2p host. They are architecturally separate
// from the plugin system in this package. Plugins are peer-to-peer only.
//
// # Thread Safety
//
// All exported types are safe for concurrent use. The Network, ServiceRegistry,
// EventBus, NameResolver, and TransferService use internal locking.
package p2pnet
