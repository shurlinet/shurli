---
title: "Go SDK"
weight: 10
description: "Shurli Go SDK reference: PeerNetwork, ServiceManager, Resolver, Authorizer interfaces, events, plugin policies, and network tools."
---

Package sdk provides the core P2P networking library for Shurli.

This package is the primary SDK surface for Go consumers. External code should depend on the interfaces defined here (PeerNetwork, Resolver, ServiceManager, Authorizer) rather than concrete types where possible.

```go
import "github.com/shurlinet/shurli/pkg/sdk"
```

## Architecture

The package is organized around these concepts:

- **Network**: creates and manages a libp2p host with relay, NAT traversal, DHT discovery, and resource limits. Entry point: `New()`.

- **Services**: the plugin system. TCP proxy services (`ExposeService`) forward streams to local ports. Plugin services (`RegisterHandler`) process streams directly. All services are managed through `ServiceRegistry`.

- **Plugin Policies**: transport-aware access control for plugins. By default, all plugins only operate over LAN and Direct connections. Relay transport is excluded unless explicitly allowed per-plugin. See `PluginPolicy` and `DefaultPluginPolicy`.

- **Events**: the `EventBus` dispatches connection, service, and authorization events to registered handlers.

- **Naming**: the `NameResolver` maps human-readable names to peer IDs, with support for fallback resolver chains.

- **Standalone**: `NewStandaloneHost` and `StandaloneResult.ResolveAndConnect` provide a consolidated path for one-shot CLI commands that operate without a running daemon.

- **Bootstrap**: `BootstrapAndConnect` handles DHT client-mode bootstrap, peer discovery, and relay circuit fallback.

- **Transfer**: `TransferService` implements chunked file transfer with the SHFT wire format (v2). Features: FastCDC content-defined chunking, BLAKE3 Merkle integrity, zstd compression (on by default), receive permissions (off/contacts/ask/open), disk space checks, atomic writes, per-chunk hash verification, resumable transfers (bitfield checkpoint persistence + sparse file writes for out-of-order chunks), ResumeRequest/ResumeResponse protocol, and Reed-Solomon erasure coding (auto-enabled on Direct WAN, configurable overhead, stripe-based encoding via klauspost/reedsolomon). Protocol: `/shurli/file-transfer/2.0.0`.

- **Protocol IDs**: `ProtocolID` and `MustValidateProtocolIDs` enforce valid protocol ID construction at init time.

### Relay Separation

Relay server protocols (pairing, admin, MOTD, unseal) live in `internal/relay` and register directly on the libp2p host. They are architecturally separate from the plugin system in this package. Plugins are peer-to-peer only.

### Thread Safety

All exported types are safe for concurrent use. The Network, ServiceRegistry, EventBus, NameResolver, and TransferService use internal locking.

---

## Constants

```go
const DHTProtocolPrefix = "/shurli"
const ProtocolPrefix = "/shurli"
const TransferProtocol = "/shurli/file-transfer/2.0.0"
const PresenceProtocol = "/shurli/presence/1.0.0"
const RTTProbeProtocol = "/shurli/rtt-probe/1.0.0"
```

### Transport Types

```go
const (
    TransportLAN    TransportType = 1 << iota // private/link-local IP
    TransportDirect                            // public internet, non-relay
    TransportRelay                             // mediated through relay (p2p-circuit)
)

const DefaultTransport = TransportLAN | TransportDirect
```

DefaultTransport permits LAN and Direct connections. Relay is excluded. This is the default for ALL plugins: no data flows through relays unless explicitly allowed per-plugin.

### Event Types

```go
const (
    EventPeerConnected    EventType = iota
    EventPeerDisconnected
    EventServiceRegistered
    EventServiceRemoved
    EventStreamOpened
    EventStreamClosed
    EventAuthAllow
    EventAuthDeny
    EventTransferPending
)
```

### NAT Types

```go
type NATType string

const (
    NATNone              NATType = "none"
    NATFullCone          NATType = "full-cone"
    NATAddressRestricted NATType = "address-restricted"
    NATPortRestricted    NATType = "port-restricted"
    NATSymmetric         NATType = "symmetric"
    NATUnknown           NATType = "unknown"
)
```

### Path Types

```go
type PathType string

const (
    PathDirect  PathType = "direct"
    PathRelayed PathType = "relayed"
)
```

---

## Variables

```go
var ErrServiceAlreadyRegistered = errors.New("service already registered")
var ErrServiceNotFound          = errors.New("service not found")
var ErrNameNotFound             = errors.New("name not found")
var ErrResponseTooLarge         = errors.New("response too large")
```

---

## Core Interfaces

### type PeerNetwork

```go
type PeerNetwork interface {
    PeerID() peer.ID
    ExposeService(name, localAddress string, allowedPeers map[peer.ID]struct{}) error
    UnexposeService(name string) error
    ListServices() []*Service
    ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error)
    ConnectToServiceContext(ctx context.Context, peerID peer.ID, serviceName string) (ServiceConn, error)
    ResolveName(name string) (peer.ID, error)
    RegisterName(name string, peerID peer.ID) error
    OnEvent(handler EventHandler) func()
    Close() error
}
```

PeerNetwork is the top-level interface for interacting with a Shurli P2P network. Create a concrete `*Network` with `sdk.New()`.

### type Resolver

```go
type Resolver interface {
    Resolve(name string) (peer.ID, error)
}
```

Resolver maps human-readable names to peer IDs. Supports chaining: local config, DNS, DHT, custom backends.

### type ServiceManager

```go
type ServiceManager interface {
    RegisterService(svc *Service) error
    UnregisterService(name string) error
    GetService(name string) (*Service, bool)
    ListServices() []*Service
    DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error)
    Use(middleware ...StreamMiddleware)
}
```

ServiceManager manages service registration, discovery, and dialing. Supports stream middleware for cross-cutting concerns.

### type Authorizer

```go
type Authorizer interface {
    IsAuthorized(p peer.ID) bool
}
```

Authorizer makes authorization decisions about peers. Default implementation uses Shurli's `authorized_keys` file.

### type ServiceConn

```go
type ServiceConn interface {
    io.ReadWriteCloser
    CloseWrite() error
}
```

ServiceConn is a connection to a remote service. Supports half-close for HTTP-style request/response patterns.

### type RelayGrantChecker

```go
type RelayGrantChecker interface {
    GrantStatus(relayID peer.ID) (remaining time.Duration, budget int64, sessionDuration time.Duration, ok bool)
    HasSufficientBudget(relayID peer.ID, fileSize int64, direction string) bool
    TrackCircuitBytes(relayID peer.ID, direction string, n int64)
    ResetCircuitCounters(relayID peer.ID)
}
```

RelayGrantChecker provides relay grant information for transfer budget/time checks. Used by the file transfer plugin to verify relay data grants before initiating transfers.

### type RelaySource

```go
type RelaySource interface {
    RelayAddrs() []string
}
```

RelaySource provides relay addresses for dial racing.

### type HalfCloseConn

```go
type HalfCloseConn interface {
    io.ReadWriteCloser
    CloseWrite() error
}
```

HalfCloseConn is a connection supporting half-close. Used by the proxy subsystem for bidirectional copy.

---

## Type Aliases

```go
type StreamMiddleware func(next network.StreamHandler) network.StreamHandler
type StreamHandler    func(serviceName string, s network.Stream)
type EventHandler     func(Event)
type EventType        int
type TransportType    int
type GrantChecker     func(peerID peer.ID, service string, transport TransportType) bool
type TokenVerifier    func(tokenBase64 string, peerID peer.ID, service string, transport TransportType) bool
type TokenLookup      func(peerID peer.ID, service string) string
type ConnectionRecorder func(peerID peer.ID)
type PeerFilter       func(peer.ID) bool
type NodeStateProvider func() *NodeAnnouncement
```

---

## Network

### type Config

```go
type Config struct {
    KeyFile              string
    KeyPassword          string
    AuthorizedKeys       string
    Gater                *auth.AuthorizedPeerGater
    Config               *config.Config
    UserAgent            string
    EnableRelay          bool
    RelayAddrs           []string
    ForcePrivate         bool
    EnableNATPortMap     bool
    EnableHolePunching   bool
    Namespace            string
    Resolver             Resolver
    ResourceLimitsEnabled bool
    Metrics              *Metrics
    BandwidthTracker     *BandwidthTracker
}
```

Config holds configuration for creating a new P2P network.

### type Network

```go
type Network struct {
    // contains unexported fields
}
```

Network is the core P2P host. Implements PeerNetwork. Created with `New()`.

#### func New

```go
func New(cfg *Config) (*Network, error)
```

New creates a new P2P network instance with the given configuration.

#### func (*Network) PeerID

```go
func (n *Network) PeerID() peer.ID
```

PeerID returns this node's peer ID.

#### func (*Network) Host

```go
func (n *Network) Host() host.Host
```

Host returns the underlying libp2p host.

#### func (*Network) ExposeService

```go
func (n *Network) ExposeService(name, localAddress string, allowedPeers map[peer.ID]struct{}) error
```

ExposeService exposes a local TCP service over the P2P network.

#### func (*Network) UnexposeService

```go
func (n *Network) UnexposeService(name string) error
```

UnexposeService removes a previously exposed service.

#### func (*Network) ListServices

```go
func (n *Network) ListServices() []*Service
```

ListServices returns all registered services.

#### func (*Network) ConnectToService

```go
func (n *Network) ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error)
```

ConnectToService connects to a remote peer's exposed service.

#### func (*Network) ConnectToServiceContext

```go
func (n *Network) ConnectToServiceContext(ctx context.Context, peerID peer.ID, serviceName string) (ServiceConn, error)
```

ConnectToServiceContext connects with a cancellable context.

#### func (*Network) ResolveName

```go
func (n *Network) ResolveName(name string) (peer.ID, error)
```

ResolveName resolves a human-readable name to a peer ID.

#### func (*Network) RegisterName

```go
func (n *Network) RegisterName(name string, peerID peer.ID) error
```

RegisterName registers a name-to-peer-ID mapping.

#### func (*Network) LoadNames

```go
func (n *Network) LoadNames(names map[string]string) error
```

LoadNames loads peer name mappings from a string map.

#### func (*Network) OnEvent

```go
func (n *Network) OnEvent(handler EventHandler) func()
```

OnEvent subscribes to network events. Returns an unsubscribe function.

#### func (*Network) AddRelayAddressesForPeer

```go
func (n *Network) AddRelayAddressesForPeer(relayAddrs []string, target peer.ID) error
```

AddRelayAddressesForPeer adds relay circuit addresses for a target peer.

#### func (*Network) ParseRelayAddrs

```go
func (n *Network) ParseRelayAddrs(addrs []string) ([]peer.AddrInfo, error)
```

ParseRelayAddrs parses multiaddr relay addresses into AddrInfo.

#### func (*Network) Close

```go
func (n *Network) Close() error
```

Close shuts down the network and releases resources.

### func DHTProtocolPrefixForNamespace

```go
func DHTProtocolPrefixForNamespace(namespace string) string
```

DHTProtocolPrefixForNamespace returns DHT protocol prefix for a private namespace.

### func HumanizeError

```go
func HumanizeError(err string) string
```

HumanizeError translates libp2p stream errors into user-friendly messages.

### func ValidateServiceName

```go
func ValidateServiceName(name string) error
```

ValidateServiceName checks that a service name is safe for use in protocol IDs.

---

## Service Registry

### type Service

```go
type Service struct {
    Name         string
    Protocol     string
    LocalAddress string
    Handler      StreamHandler
    Enabled      bool
    AllowedPeers map[peer.ID]struct{}
    Policy       *PluginPolicy
}
```

Service represents a service exposed over P2P.

### type ServiceRegistry

```go
type ServiceRegistry struct {
    // contains unexported fields
}
```

ServiceRegistry manages service registration, stream routing, and connection dialing.

#### func NewServiceRegistry

```go
func NewServiceRegistry(h host.Host, metrics *Metrics) *ServiceRegistry
```

#### func (*ServiceRegistry) RegisterService

```go
func (r *ServiceRegistry) RegisterService(svc *Service) error
```

#### func (*ServiceRegistry) UnregisterService

```go
func (r *ServiceRegistry) UnregisterService(name string) error
```

#### func (*ServiceRegistry) GetService

```go
func (r *ServiceRegistry) GetService(name string) (*Service, bool)
```

#### func (*ServiceRegistry) ListServices

```go
func (r *ServiceRegistry) ListServices() []*Service
```

#### func (*ServiceRegistry) DialService

```go
func (r *ServiceRegistry) DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error)
```

#### func (*ServiceRegistry) Seal

```go
func (r *ServiceRegistry) Seal()
```

Seal prevents further service registration. Called after daemon startup.

#### func (*ServiceRegistry) SetGrantChecker

```go
func (r *ServiceRegistry) SetGrantChecker(checker GrantChecker)
```

#### func (*ServiceRegistry) SetTokenVerifier

```go
func (r *ServiceRegistry) SetTokenVerifier(v TokenVerifier)
```

#### func (*ServiceRegistry) SetTokenLookup

```go
func (r *ServiceRegistry) SetTokenLookup(l TokenLookup)
```

#### func (*ServiceRegistry) Use

```go
func (r *ServiceRegistry) Use(middleware ...StreamMiddleware)
```

Use adds stream middleware. Middleware wraps every stream handler in the order added (first added = outermost wrapper).

---

## Events

### type Event

```go
type Event struct {
    Type        EventType
    PeerID      peer.ID
    ServiceName string
    Detail      string
}
```

### type EventBus

```go
type EventBus struct {
    // contains unexported fields
}
```

#### func NewEventBus

```go
func NewEventBus() *EventBus
```

#### func (*EventBus) Subscribe

```go
func (b *EventBus) Subscribe(handler EventHandler) func()
```

Subscribe registers an event handler. Returns an unsubscribe function.

#### func (*EventBus) Emit

```go
func (b *EventBus) Emit(e Event)
```

Emit dispatches event to all registered handlers.

---

## Name Resolution

### type NameResolver

```go
type NameResolver struct {
    // contains unexported fields
}
```

#### func NewNameResolver

```go
func NewNameResolver() *NameResolver
```

#### func (*NameResolver) Register

```go
func (r *NameResolver) Register(name string, peerID peer.ID) error
```

#### func (*NameResolver) Unregister

```go
func (r *NameResolver) Unregister(name string)
```

#### func (*NameResolver) Resolve

```go
func (r *NameResolver) Resolve(name string) (peer.ID, error)
```

#### func (*NameResolver) List

```go
func (r *NameResolver) List() map[string]peer.ID
```

#### func (*NameResolver) LoadFromMap

```go
func (r *NameResolver) LoadFromMap(names map[string]string) error
```

LoadFromMap adds name mappings from string map (additive).

#### func (*NameResolver) ReplaceFromMap

```go
func (r *NameResolver) ReplaceFromMap(names map[string]string) error
```

ReplaceFromMap replaces all name mappings with the given map.

---

## Byte Size Utilities

### func ParseByteSize

```go
func ParseByteSize(s string) (int64, error)
```

ParseByteSize parses a human-readable byte size string (e.g., "500MB", "1GB", "unlimited").

### func FormatBytes

```go
func FormatBytes(b int64) string
```

FormatBytes formats a byte count for user-facing display (e.g. "1.2 GB", "500 MB").

---

## Cryptographic Utilities

### func Blake3Sum

```go
func Blake3Sum(data []byte) [32]byte
```

Blake3Sum computes the BLAKE3-256 hash of data.

### func MerkleRoot

```go
func MerkleRoot(hashes [][32]byte) [32]byte
```

MerkleRoot computes the BLAKE3 Merkle root hash from a list of chunk hashes. Leaf nodes are chunk hashes (already BLAKE3). Internal nodes are BLAKE3(left || right). Odd nodes are promoted unchanged. Single hash is returned as-is. Empty list returns zero hash.

---

## Relay Utilities

### func RelayPeerFromAddr

```go
func RelayPeerFromAddr(addr ma.Multiaddr) peer.ID
```

RelayPeerFromAddr extracts the relay peer ID from a circuit relay multiaddr. Returns empty peer.ID if the address is not a circuit relay address.

### func RelayPeerFromAddrStr

```go
func RelayPeerFromAddrStr(addrStr string) string
```

RelayPeerFromAddrStr extracts the relay peer ID string from a circuit relay multiaddr string. Returns empty string if the address is not a relay circuit.

### func VerifiedTransport

```go
func VerifiedTransport(s network.Stream, hasVerifiedLANConn func(peer.ID) bool) TransportType
```

VerifiedTransport classifies a stream using mDNS-verified LAN detection. Use for any trust-making decision (transport policy, erasure coding, bandwidth budgets).

Precedence: Limited (relay circuit) → TransportRelay; loopback or link-local remote → TransportLAN (cannot traverse routers); `hasVerifiedLANConn` returns true → TransportLAN; otherwise → TransportDirect.

Unlike `ClassifyTransport`, routable private IPv4 addresses (RFC 1918 / RFC 6598) are NOT classified as LAN unless mDNS has verified the peer. This avoids false positives from CGNAT, Docker bridge networks, VPN tunnels, and multi-WAN routed-private cross-links — only mDNS multicast (which cannot traverse routers) proves real LAN proximity.

A nil `hasVerifiedLANConn` callback still classifies loopback and link-local as LAN; for routable addresses it falls back to TransportDirect (conservative).

### func (*Network) HasVerifiedLANConn

```go
func (n *Network) HasVerifiedLANConn(id peer.ID) bool
```

Nil-safe convenience wrapper over the underlying mDNS-verified LAN registry. Returns true if the peer has at least one live non-relay connection whose remote IP was confirmed via mDNS multicast. This is the authoritative "is this peer on our LAN?" check; use it as the second argument to `VerifiedTransport`.

---

## Plugin Transport Policy

### type PluginPolicy

```go
type PluginPolicy struct {
    AllowedTransports TransportType
    AllowPeers        map[peer.ID]struct{}
    DenyPeers         map[peer.ID]struct{}
}
```

PluginPolicy defines transport restrictions and peer access control for plugin protocols.

#### func DefaultPluginPolicy

```go
func DefaultPluginPolicy() *PluginPolicy
```

DefaultPluginPolicy returns a policy allowing LAN + Direct, no relay.

#### func (*PluginPolicy) RelayAllowed

```go
func (p *PluginPolicy) RelayAllowed() bool
```

#### func (*PluginPolicy) PeerAllowed

```go
func (p *PluginPolicy) PeerAllowed(id peer.ID) bool
```

#### func (*PluginPolicy) TransportAllowed

```go
func (p *PluginPolicy) TransportAllowed(t TransportType) bool
```

### func ClassifyTransport

```go
func ClassifyTransport(s network.Stream) TransportType
```

ClassifyTransport determines the transport type of a stream. Limited connections are classified as TransportRelay. Private/loopback/link-local IPs are TransportLAN. Everything else is TransportDirect.

---

## Protocol IDs

### func ProtocolID

```go
func ProtocolID(name, version string) string
```

ProtocolID constructs a validated Shurli protocol identifier: `/shurli/<name>/<version>`. Panics on invalid input (intended for init-time registration).

### func ValidateProtocolID

```go
func ValidateProtocolID(id string) error
```

ValidateProtocolID checks if a protocol ID is well-formed.

### func MustValidateProtocolIDs

```go
func MustValidateProtocolIDs(ids ...string)
```

MustValidateProtocolIDs validates a batch of protocol IDs at init time. Panics if any are malformed.

---

## Bootstrap

### type BootstrapConfig

```go
type BootstrapConfig struct {
    Namespace      string
    BootstrapPeers []string
    RelayAddrs     []string
}
```

### func BootstrapAndConnect

```go
func BootstrapAndConnect(ctx context.Context, h host.Host, net *Network, target peer.ID, cfg BootstrapConfig) error
```

BootstrapAndConnect bootstraps the DHT and connects to a target peer.

---

## Standalone Host

### type StandaloneConfig

```go
type StandaloneConfig struct {
    ConfigPath string
    Password   string
    UserAgent  string
}
```

### type StandaloneResult

```go
type StandaloneResult struct {
    Network    *Network
    NodeConfig *config.NodeConfig
    ConfigDir  string
}
```

#### func (*StandaloneResult) ResolveAndConnect

```go
func (r *StandaloneResult) ResolveAndConnect(ctx context.Context, target string) (peer.ID, error)
```

ResolveAndConnect resolves a peer name or ID and connects.

### func NewStandaloneHost

```go
func NewStandaloneHost(cfg StandaloneConfig) (*StandaloneResult, error)
```

NewStandaloneHost creates a P2P Network from a config file. Used by CLI commands that need a network connection without running the full daemon.

---

## Network Tools

### Ping

#### type PingResult

```go
type PingResult struct {
    Seq    int     `json:"seq"`
    PeerID string  `json:"peer_id"`
    RttMs  float64 `json:"rtt_ms"`
    Path   string  `json:"path"`
    Error  string  `json:"error,omitempty"`
}
```

#### type PingStats

```go
type PingStats struct {
    Sent     int     `json:"sent"`
    Received int     `json:"received"`
    Lost     int     `json:"lost"`
    LossPct  float64 `json:"loss_pct"`
    MinMs    float64 `json:"min_ms"`
    AvgMs    float64 `json:"avg_ms"`
    MaxMs    float64 `json:"max_ms"`
}
```

#### func PingPeer

```go
func PingPeer(ctx context.Context, h host.Host, peerID peer.ID, protocolID string, count int, interval time.Duration) <-chan PingResult
```

PingPeer sends pings to a peer and streams results on the returned channel.

#### func ComputePingStats

```go
func ComputePingStats(results []PingResult) PingStats
```

### Traceroute

#### type TraceHop

```go
type TraceHop struct {
    Hop     int     `json:"hop"`
    PeerID  string  `json:"peer_id"`
    Name    string  `json:"name,omitempty"`
    Address string  `json:"address"`
    RttMs   float64 `json:"rtt_ms"`
    Error   string  `json:"error,omitempty"`
}
```

#### type TraceResult

```go
type TraceResult struct {
    Target   string     `json:"target"`
    TargetID string     `json:"target_id"`
    Path     string     `json:"path"`
    Hops     []TraceHop `json:"hops"`
}
```

#### func TracePeer

```go
func TracePeer(ctx context.Context, h host.Host, targetPeerID peer.ID) (*TraceResult, error)
```

TracePeer traces the network path to a peer.

---

## Peer Management

### type PeerManager

```go
type PeerManager struct {
    // contains unexported fields
}
```

PeerManager maintains connections to watched peers with exponential backoff reconnection.

#### func NewPeerManager

```go
func NewPeerManager(h host.Host, pd *PathDialer, m *Metrics, onReconnect ConnectionRecorder) *PeerManager
```

#### func (*PeerManager) Start

```go
func (pm *PeerManager) Start(ctx context.Context)
```

#### func (*PeerManager) Close

```go
func (pm *PeerManager) Close()
```

#### func (*PeerManager) SetWatchlist

```go
func (pm *PeerManager) SetWatchlist(peerIDs []peer.ID)
```

#### func (*PeerManager) OnNetworkChange

```go
func (pm *PeerManager) OnNetworkChange()
```

OnNetworkChange triggers immediate reconnection attempts.

#### func (*PeerManager) GetPeer

```go
func (pm *PeerManager) GetPeer(id peer.ID) (*ManagedPeerInfo, bool)
```

#### func (*PeerManager) ListPeers

```go
func (pm *PeerManager) ListPeers() []ManagedPeerInfo
```

### type ManagedPeerInfo

```go
type ManagedPeerInfo struct {
    PeerID         string `json:"peer_id"`
    Connected      bool   `json:"connected"`
    LastSeen       string `json:"last_seen"`
    LastDialError  string `json:"last_dial_error,omitempty"`
    ConsecFailures int    `json:"consec_failures"`
    BackoffUntil   string `json:"backoff_until,omitempty"`
}
```

---

## Path Dialer

### type PathDialer

```go
type PathDialer struct {
    // contains unexported fields
}
```

PathDialer connects to peers using parallel path racing across direct and relay paths.

#### func NewPathDialer

```go
func NewPathDialer(h host.Host, kdht *dht.IpfsDHT, relaySource RelaySource, m *Metrics) *PathDialer
```

#### func (*PathDialer) DialPeer

```go
func (pd *PathDialer) DialPeer(ctx context.Context, peerID peer.ID) (*DialResult, error)
```

### type DialResult

```go
type DialResult struct {
    PathType PathType
    Duration time.Duration
    Address  string
}
```

### func PeerConnInfo

```go
func PeerConnInfo(h host.Host, peerID peer.ID) (pathType string, addr string)
```

### func ClassifyMultiaddr

```go
func ClassifyMultiaddr(addr string) (pathType PathType, transport string, ipVersion string)
```

### func AddRelayAddressesForPeerFunc

```go
func AddRelayAddressesForPeerFunc(h host.Host, relayAddrs []string, target peer.ID) error
```

---

## Path Tracker

### type PathTracker

```go
type PathTracker struct {
    // contains unexported fields
}
```

PathTracker monitors peer connections and tracks per-peer path quality.

#### func NewPathTracker

```go
func NewPathTracker(h host.Host, m *Metrics) *PathTracker
```

#### func (*PathTracker) Start

```go
func (pt *PathTracker) Start(ctx context.Context)
```

#### func (*PathTracker) UpdateRTT

```go
func (pt *PathTracker) UpdateRTT(pid peer.ID, rttMs float64)
```

#### func (*PathTracker) GetPeerPath

```go
func (pt *PathTracker) GetPeerPath(pid peer.ID) (*PeerPathInfo, bool)
```

#### func (*PathTracker) ListPeerPaths

```go
func (pt *PathTracker) ListPeerPaths() []*PeerPathInfo
```

### type PeerPathInfo

```go
type PeerPathInfo struct {
    PeerID      string   `json:"peer_id"`
    PathType    PathType `json:"path_type"`
    Address     string   `json:"address"`
    ConnectedAt string   `json:"connected_at"`
    Transport   string   `json:"transport"`
    IPVersion   string   `json:"ip_version"`
    LastRTTMs   float64  `json:"last_rtt_ms"`
}
```

---

## LAN Discovery

### type MDNSDiscovery

```go
type MDNSDiscovery struct {
    // contains unexported fields
}
```

MDNSDiscovery discovers peers on the local network via mDNS/Zeroconf.

---

## Network Intelligence

### type NetIntel

```go
type NetIntel struct {
    // contains unexported fields
}
```

NetIntel manages the presence announcement protocol. Peers exchange presence info (NAT type, addresses, capabilities, uptime) at regular intervals.

#### func NewNetIntel

```go
func NewNetIntel(h host.Host, m *Metrics, pf PeerFilter, sp NodeStateProvider, interval time.Duration) *NetIntel
```

#### func (*NetIntel) Start

```go
func (ni *NetIntel) Start(ctx context.Context)
```

#### func (*NetIntel) Close

```go
func (ni *NetIntel) Close()
```

#### func (*NetIntel) AnnounceNow

```go
func (ni *NetIntel) AnnounceNow()
```

AnnounceNow triggers an immediate presence announcement to all connected peers.

#### func (*NetIntel) GetPeerState

```go
func (ni *NetIntel) GetPeerState(pid peer.ID) *PeerAnnouncement
```

#### func (*NetIntel) GetAllPeerState

```go
func (ni *NetIntel) GetAllPeerState() []PeerAnnouncement
```

### type NodeAnnouncement

```go
type NodeAnnouncement struct {
    Version       int    `json:"version"`
    From          string `json:"from"`
    Grade         string `json:"grade"`
    NATType       string `json:"nat_type"`
    HasIPv4       bool   `json:"has_ipv4"`
    HasIPv6       bool   `json:"has_ipv6"`
    BehindCGNAT   bool   `json:"behind_cgnat"`
    UptimeSec     int64  `json:"uptime_sec"`
    PeerCount     int    `json:"peer_count"`
    Timestamp     int64  `json:"timestamp"`
    Hops          int    `json:"hops"`
    AnonymousMode bool   `json:"anonymous_mode"`
    ZKPProof      []byte `json:"zkp_proof,omitempty"`
}
```

### type PeerAnnouncement

```go
type PeerAnnouncement struct {
    PeerID       peer.ID
    Announcement NodeAnnouncement
    ReceivedAt   time.Time
}
```

---

## Network Monitor

### type NetworkMonitor

```go
type NetworkMonitor struct {
    // contains unexported fields
}
```

NetworkMonitor watches for OS-level network interface changes (WiFi switches, cable plug/unplug, VPN up/down).

#### func NewNetworkMonitor

```go
func NewNetworkMonitor(onChange func(*NetworkChange), m *Metrics) *NetworkMonitor
```

#### func (*NetworkMonitor) Run

```go
func (nm *NetworkMonitor) Run(ctx context.Context)
```

### type NetworkChange

```go
type NetworkChange struct {
    Added          []string
    Removed        []string
    IPv6Changed    bool
    IPv4Changed    bool
    TunnelChanged  bool
    GatewayChanged bool
}
```

---

## Interface Discovery

### type InterfaceSummary

```go
type InterfaceSummary struct {
    Interfaces       []InterfaceInfo
    HasGlobalIPv6    bool
    HasGlobalIPv4    bool
    GlobalIPv6Addrs  []string
    GlobalIPv4Addrs  []string
    TunnelInterfaces []string
    DefaultGateway   string
}
```

### type InterfaceInfo

```go
type InterfaceInfo struct {
    Name       string
    IPv4Addrs  []string
    IPv6Addrs  []string
    IsLoopback bool
}
```

### func DiscoverInterfaces

```go
func DiscoverInterfaces() (*InterfaceSummary, error)
```

DiscoverInterfaces enumerates all network interfaces with their addresses, detecting IPv4/IPv6 global addresses, tunnels, and the default gateway.

---

## STUN Prober

### type STUNProber

```go
type STUNProber struct {
    // contains unexported fields
}
```

STUNProber discovers external addresses via STUN servers and classifies NAT type.

#### func NewSTUNProber

```go
func NewSTUNProber(servers []string, m *Metrics) *STUNProber
```

#### func (*STUNProber) Probe

```go
func (sp *STUNProber) Probe(ctx context.Context) (*STUNResult, error)
```

#### func (*STUNProber) Result

```go
func (sp *STUNProber) Result() *STUNResult
```

Result returns the most recent probe result.

### type STUNResult

```go
type STUNResult struct {
    Probes        []ProbeResult
    NATType       NATType
    ExternalAddrs []string
    ProbedAt      time.Time
    BehindCGNAT   bool
    CGNATNote     string
}
```

### type ProbeResult

```go
type ProbeResult struct {
    ServerAddr   string
    ExternalAddr string
    ExternalIP   string
    ExternalPort int
    Latency      time.Duration
    Error        string
}
```

#### func (NATType) HolePunchable

```go
func (n NATType) HolePunchable() bool
```

HolePunchable returns true if the NAT type supports hole punching (full-cone, address-restricted, or port-restricted).

---

## Relay Discovery

### type RelayDiscovery

```go
type RelayDiscovery struct {
    // contains unexported fields
}
```

RelayDiscovery manages static + DHT relay discovery with health-ranked ordering.

#### func NewRelayDiscovery

```go
func NewRelayDiscovery(staticRelays []peer.AddrInfo, namespace string, m *Metrics) *RelayDiscovery
```

### type StaticRelaySource

```go
type StaticRelaySource struct {
    Addrs []string
}
```

StaticRelaySource wraps a fixed relay address list.

#### func (*StaticRelaySource) RelayAddrs

```go
func (s *StaticRelaySource) RelayAddrs() []string
```

---

## Relay Health

### type RelayHealth

```go
type RelayHealth struct {
    // contains unexported fields
}
```

RelayHealth tracks relay liveness and quality using EWMA-based health scoring.

#### func NewRelayHealth

```go
func NewRelayHealth(h host.Host, m *Metrics) *RelayHealth
```

---

## Peer Relay

### type PeerRelay

```go
type PeerRelay struct {
    // contains unexported fields
}
```

PeerRelay auto-enables relay functionality when a node has a public IP.

#### func NewPeerRelay

```go
func NewPeerRelay(h host.Host, m *Metrics, cfg PeerRelayConfig) *PeerRelay
```

---

## Proxy

### type TCPListener

```go
type TCPListener struct {
    // contains unexported fields
}
```

TCPListener is a TCP listener that forwards connections to a P2P service.

#### func NewTCPListener

```go
func NewTCPListener(localAddr string, dialFunc func() (ServiceConn, error)) (*TCPListener, error)
```

#### func (*TCPListener) Serve

```go
func (l *TCPListener) Serve() error
```

#### func (*TCPListener) Close

```go
func (l *TCPListener) Close() error
```

#### func (*TCPListener) Addr

```go
func (l *TCPListener) Addr() net.Addr
```

### func BidirectionalProxy

```go
func BidirectionalProxy(a, b HalfCloseConn, logPrefix string)
```

BidirectionalProxy copies data between two connections with half-close propagation.

### func InstrumentedBidirectionalProxy

```go
func InstrumentedBidirectionalProxy(a, b HalfCloseConn, service string, metrics *Metrics)
```

### func ProxyStreamToTCP

```go
func ProxyStreamToTCP(stream network.Stream, tcpAddr string) error
```

ProxyStreamToTCP proxies a libp2p stream to a local TCP address.

### func DialWithRetry

```go
func DialWithRetry(dialFunc func() (ServiceConn, error), maxRetries int) func() (ServiceConn, error)
```

DialWithRetry wraps a dial function with exponential backoff retry.

---

## Metrics

### type Metrics

```go
type Metrics struct {
    Registry                       *prometheus.Registry
    ProxyBytesTotal                *prometheus.CounterVec
    ProxyConnectionsTotal          *prometheus.CounterVec
    ProxyActiveConns               *prometheus.GaugeVec
    ProxyDurationSeconds           *prometheus.HistogramVec
    AuthDecisionsTotal             *prometheus.CounterVec
    HolePunchTotal                 *prometheus.CounterVec
    HolePunchDurationSeconds       *prometheus.HistogramVec
    DaemonRequestsTotal            *prometheus.CounterVec
    DaemonRequestDurationSeconds   *prometheus.HistogramVec
    PathDialTotal                  *prometheus.CounterVec
    PathDialDurationSeconds        *prometheus.HistogramVec
    ConnectedPeers                 *prometheus.GaugeVec
    NetworkChangeTotal             *prometheus.CounterVec
    STUNProbeTotal                 *prometheus.CounterVec
    MDNSDiscoveredTotal            *prometheus.CounterVec
    PeerManagerReconnectTotal      *prometheus.CounterVec
    NetIntelSentTotal              *prometheus.CounterVec
    NetIntelReceivedTotal          *prometheus.CounterVec
    InterfaceCount                 *prometheus.GaugeVec
    VaultSealed                    prometheus.Gauge
    VaultSealOpsTotal              *prometheus.CounterVec
    VaultUnsealTotal               *prometheus.CounterVec
    VaultUnsealLockedPeers         prometheus.Gauge
    DepositOpsTotal                *prometheus.CounterVec
    DepositPending                 prometheus.Gauge
    PairingTotal                   *prometheus.CounterVec
    MacaroonVerifyTotal            *prometheus.CounterVec
    AdminRequestTotal              *prometheus.CounterVec
    AdminRequestDurationSeconds    *prometheus.HistogramVec
    ZKPProveTotal                  *prometheus.CounterVec
    ZKPProveDurationSeconds        *prometheus.HistogramVec
    ZKPVerifyTotal                 *prometheus.CounterVec
    ZKPVerifyDurationSeconds       *prometheus.HistogramVec
    ZKPAuthTotal                   *prometheus.CounterVec
    ZKPTreeRebuildTotal            *prometheus.CounterVec
    ZKPTreeRebuildDurationSeconds  *prometheus.HistogramVec
    ZKPTreeLeaves                  prometheus.Gauge
    ZKPChallengesPending           prometheus.Gauge
    ZKPRangeProveTotal             *prometheus.CounterVec
    ZKPRangeProveDuration          *prometheus.HistogramVec
    ZKPRangeVerifyTotal            *prometheus.CounterVec
    ZKPRangeVerifyDuration         *prometheus.HistogramVec
    ZKPAnonAnnouncementsTotal      *prometheus.CounterVec
    PeerBandwidthBytesTotal        *prometheus.GaugeVec
    PeerBandwidthRate              *prometheus.GaugeVec
    ProtocolBandwidthBytesTotal    *prometheus.GaugeVec
    BandwidthBytesTotal            *prometheus.GaugeVec
    RelayHealthScore               *prometheus.GaugeVec
    RelayProbeTotal                *prometheus.CounterVec
    BuildInfo                      *prometheus.GaugeVec
}
```

Metrics holds all custom Shurli Prometheus metrics (50+ counters, gauges, histograms).

#### func NewMetrics

```go
func NewMetrics(version, goVersion string) *Metrics
```

---

## Audit Logger

### type AuditLogger

```go
type AuditLogger struct {
    // contains unexported fields
}
```

AuditLogger writes structured audit events for security-relevant actions. Nil-safe: all methods are no-ops on a nil receiver.

#### func NewAuditLogger

```go
func NewAuditLogger(handler slog.Handler) *AuditLogger
```

#### func (*AuditLogger) AuthDecision

```go
func (a *AuditLogger) AuthDecision(peerID, direction, result string)
```

#### func (*AuditLogger) ServiceACLDenied

```go
func (a *AuditLogger) ServiceACLDenied(peerID, service string)
```

#### func (*AuditLogger) DaemonAPIAccess

```go
func (a *AuditLogger) DaemonAPIAccess(method, path string, status int)
```

#### func (*AuditLogger) AuthChange

```go
func (a *AuditLogger) AuthChange(action, peerID string)
```

---

## Bandwidth Tracker

### type BandwidthTracker

```go
type BandwidthTracker struct {
    // contains unexported fields
}
```

BandwidthTracker wraps libp2p's BandwidthCounter and bridges to Prometheus metrics.

#### func NewBandwidthTracker

```go
func NewBandwidthTracker(prom *Metrics) *BandwidthTracker
```

#### func (*BandwidthTracker) Counter

```go
func (bt *BandwidthTracker) Counter() *metrics.BandwidthCounter
```

Counter returns the underlying libp2p bandwidth counter for host construction.

#### func (*BandwidthTracker) PeerStats

```go
func (bt *BandwidthTracker) PeerStats(p peer.ID) metrics.Stats
```

#### func (*BandwidthTracker) AllPeerStats

```go
func (bt *BandwidthTracker) AllPeerStats() map[peer.ID]metrics.Stats
```

#### func (*BandwidthTracker) ProtocolStats

```go
func (bt *BandwidthTracker) ProtocolStats(proto protocol.ID) metrics.Stats
```

#### func (*BandwidthTracker) Totals

```go
func (bt *BandwidthTracker) Totals() metrics.Stats
```

#### func (*BandwidthTracker) PublishMetrics

```go
func (bt *BandwidthTracker) PublishMetrics()
```

#### func (*BandwidthTracker) Start

```go
func (bt *BandwidthTracker) Start(ctx context.Context, interval time.Duration)
```

Start begins periodic metric publishing.

---

## Errors

### type RemoteError

```go
type RemoteError struct {
    Message string
}
```

RemoteError wraps an error message received from a remote peer.

#### func (*RemoteError) Error

```go
func (e *RemoteError) Error() string
```

---

## Go Documentation

Full API reference with doc comments is available locally via pkgsite:

```bash
go install golang.org/x/pkgsite/cmd/pkgsite@latest
cd /path/to/shurli
pkgsite -open .
```

Then navigate to `pkg/sdk` and `pkg/plugin` for browsable documentation with full source links.
