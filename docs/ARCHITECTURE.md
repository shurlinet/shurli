# peer-up Architecture

This document describes the technical architecture of peer-up, from current implementation to future vision.

## Table of Contents

- [Current Architecture (Phase 4B Complete)](#current-architecture-phase-4b-complete)
- [Target Architecture (Phase 4C+)](#target-architecture-phase-4c)
- [Core Concepts](#core-concepts)
- [Security Model](#security-model)
- [Naming System](#naming-system)
- [Federation Model](#federation-model)
- [Mobile Architecture](#mobile-architecture)

---

## Current Architecture (Phase 4C Batch F Complete)

### Component Overview

```
peer-up/
├── cmd/
│   ├── peerup/              # Single binary with subcommands
│   │   ├── main.go          # Command dispatch (daemon, ping, traceroute, resolve,
│   │   │                    #   proxy, whoami, auth, relay, config, service,
│   │   │                    #   invite, join, status, init, version)
│   │   ├── cmd_daemon.go    # Daemon mode + client subcommands (status, stop, ping, etc.)
│   │   ├── cmd_serve.go     # Alias for daemon (backward compat)
│   │   ├── serve_common.go  # Shared P2P runtime (serveRuntime) — used by daemon
│   │   ├── cmd_init.go      # Interactive setup wizard
│   │   ├── cmd_proxy.go     # TCP proxy client
│   │   ├── cmd_ping.go      # Standalone P2P ping (continuous, stats)
│   │   ├── cmd_traceroute.go # Standalone P2P traceroute
│   │   ├── cmd_resolve.go   # Standalone name resolution
│   │   ├── cmd_whoami.go    # Show own peer ID
│   │   ├── cmd_auth.go      # Auth add/list/remove/validate subcommands
│   │   ├── cmd_relay.go     # Relay add/list/remove subcommands
│   │   ├── cmd_service.go   # Service add/list/remove subcommands
│   │   ├── cmd_config.go    # Config validate/show/rollback/apply/confirm
│   │   ├── cmd_invite.go    # Generate invite code + QR + P2P handshake (--non-interactive)
│   │   ├── cmd_join.go      # Decode invite, connect, auto-configure (--non-interactive, env var)
│   │   ├── cmd_status.go    # Local status: version, peer ID, config, services, peers
│   │   ├── config_template.go # Shared node config YAML template (single source of truth)
│   │   └── relay_input.go   # Flexible relay address parsing (IP, IP:PORT, multiaddr)
│   └── relay-server/        # Circuit relay v2 source (builds relay binary)
│       └── main.go
│
├── pkg/p2pnet/              # Importable P2P library
│   ├── network.go           # Core network setup, relay helpers, name resolution
│   ├── service.go           # Service registry (register/unregister, expose/unexpose)
│   ├── proxy.go             # Bidirectional TCP↔Stream proxy with half-close
│   ├── naming.go            # Local name resolution (name → peer ID)
│   ├── identity.go          # Identity helpers (delegates to internal/identity)
│   ├── ping.go              # Shared P2P ping logic (PingPeer, ComputePingStats)
│   ├── traceroute.go        # Shared P2P traceroute (TracePeer, hop analysis)
│   └── errors.go            # Sentinel errors
│
├── internal/
│   ├── config/              # YAML configuration loading + self-healing
│   │   ├── config.go           # Config structs (HomeNode, Client, Relay, unified NodeConfig)
│   │   ├── loader.go           # Load, validate, resolve paths, find config
│   │   ├── archive.go          # Last-known-good archive/rollback (atomic writes)
│   │   ├── confirm.go          # Commit-confirmed pattern (apply/confirm/enforce)
│   │   └── errors.go           # Sentinel errors (ErrConfigNotFound, ErrNoArchive, etc.)
│   ├── auth/                # SSH-style authentication
│   │   ├── authorized_keys.go  # Parser + ConnectionGater loader
│   │   ├── gater.go            # ConnectionGater implementation
│   │   ├── manage.go           # AddPeer/RemovePeer/ListPeers (shared by CLI commands)
│   │   └── errors.go           # Sentinel errors
│   ├── daemon/              # Daemon API server + client
│   │   ├── types.go            # JSON request/response types (StatusResponse, PingRequest, etc.)
│   │   ├── server.go           # Unix socket HTTP server, cookie auth, proxy tracking
│   │   ├── handlers.go         # HTTP handlers, format negotiation (JSON + text)
│   │   ├── client.go           # Client library for CLI → daemon communication
│   │   ├── errors.go           # Sentinel errors (ErrDaemonAlreadyRunning, etc.)
│   │   └── daemon_test.go      # Tests (auth, handlers, lifecycle, integration)
│   ├── identity/            # Ed25519 identity management (shared by peerup + relay-server)
│   │   └── identity.go      # CheckKeyFilePermissions, LoadOrCreateIdentity, PeerIDFromKeyFile
│   ├── invite/              # Invite code encoding/decoding
│   │   └── code.go          # Binary → base32 with dash grouping
│   ├── qr/                  # QR Code encoder for terminal display (inlined from skip2/go-qrcode)
│   │   ├── qrcode.go        # Public API: New(), Bitmap(), ToSmallString()
│   │   ├── encoder.go       # Data encoding (numeric, alphanumeric, byte modes)
│   │   ├── symbol.go        # Module matrix, pattern placement, penalty scoring
│   │   ├── version.go       # All 40 QR versions × 4 recovery levels
│   │   ├── gf.go            # GF(2^8) arithmetic + Reed-Solomon encoding
│   │   └── bitset.go        # Append-only bit array operations
│   ├── termcolor/           # Minimal ANSI terminal colors (replaces fatih/color)
│   │   └── color.go         # Green, Red, Yellow, Faint — respects NO_COLOR
│   ├── validate/            # Input validation helpers
│   │   └── validate.go      # ServiceName() — DNS-label format for protocol IDs
│   └── watchdog/            # Health monitoring + systemd integration
│       └── watchdog.go      # Health check loop, sd_notify (Ready/Watchdog/Stopping)
│
├── relay-server/            # Deployment artifacts (not a Go module)
│   ├── setup.sh             # Deploy/verify/uninstall (builds from cmd/relay-server)
│   ├── relay-server.service # systemd unit file
│   └── relay-server.sample.yaml
│
├── deploy/                  # Service management files
│   ├── peerup-daemon.service   # systemd unit for daemon (Linux)
│   └── com.peerup.daemon.plist # launchd plist for daemon (macOS)
│
├── configs/                 # Sample configuration files
│   ├── peerup.sample.yaml
│   ├── relay-server.sample.yaml
│   └── authorized_keys.sample
│
├── docs/                    # Project documentation
│   ├── ARCHITECTURE.md      # This file
│   ├── DAEMON-API.md        # Daemon API reference
│   ├── NETWORK-TOOLS.md     # Network diagnostic tools guide
│   ├── FAQ.md
│   ├── ROADMAP.md
│   └── TESTING.md
│
└── examples/                # Example implementations
    └── basic-service/
```

### Network Topology (Current)

```
┌─────────────────────────────────────────────────────────────┐
│                      Internet                                │
└─────────────────────────────────────────────────────────────┘
                            │
              ┌─────────────┼─────────────┐
              │                           │
              ▼                           ▼
    ┌──────────────────┐        ┌──────────────────┐
    │   Relay Server   │        │   Client Node    │
    │      (VPS)       │        │  (Phone/Laptop)  │
    │   Public IP      │        │   CGNAT/Mobile   │
    └────────┬─────────┘        └─────────┬────────┘
             │                            │
             │ Circuit Relay v2           │
             │ (hop protocol)             │
             │                            │
             └────────────┬───────────────┘
                          │
                          ▼
                 ┌──────────────────┐
                 │    Home Node     │
                 │ (Behind Starlink)│
                 │   CGNAT + IPv6   │
                 │    Firewall      │
                 └──────────────────┘
```

**Connection Flow**:
1. Home node connects outbound to relay → makes reservation
2. Client connects outbound to relay
3. Client dials home via `/p2p-circuit` address
4. Relay bridges connection (both sides outbound-only)
5. DCUtR attempts hole-punching for direct upgrade

### Authentication Flow

```
Client Attempts Connection to Home Node
         │
         ▼
   ┌──────────────────────────────────┐
   │  libp2p Transport Handshake      │
   │  (Noise protocol, key exchange)  │
   └──────────────────┬───────────────┘
                      │
                      ▼
        ┌─────────────────────────────┐
        │  ConnectionGater.           │
        │  InterceptSecured()         │
        │                             │
        │  Check peer ID against      │
        │  authorized_keys            │
        └──────────┬──────────────────┘
                   │
         ┌─────────┴─────────┐
         │                   │
         ▼                   ▼
    ✅ Authorized      ❌ Unauthorized
    Connection         Connection
    Allowed            DENIED
         │
         ▼
   ┌──────────────────────────────────┐
   │  Protocol Handler                │
   │  (defense-in-depth check)        │
   │                                  │
   │  if !authorizer.IsAuthorized():  │
   │    close stream                  │
   └──────────────────────────────────┘
```

### Peer Authorization Methods

There are three ways to authorize peers:

**1. CLI — `peerup auth`**
```bash
peerup auth add <peer-id> --comment "label"
peerup auth list
peerup auth remove <peer-id>
```

**2. Invite/Join flow — zero-touch mutual authorization**
```
Machine A: peerup invite --name home     # Generates invite code + QR
Machine B: peerup join <code> --name laptop  # Decodes, connects, auto-authorizes both sides
```
The invite protocol uses a one-time token (16 random bytes, HMAC-verified) over a P2P stream. Both peers add each other to `authorized_keys` and `names` config automatically.

**3. Manual — edit `authorized_keys` file directly**
```bash
echo "12D3KooW... # home-server" >> ~/.config/peerup/authorized_keys
```

---

## Target Architecture (Phase 4C+)

### Planned Additions

Building on the current structure, future phases will add:

```
peer-up/
├── cmd/
│   ├── peerup/              # ✅ Single binary (daemon, serve, ping, traceroute, resolve,
│   │                        #   proxy, whoami, auth, relay, config, service, invite, join,
│   │                        #   status, init, version)
│   ├── relay-server/        # ✅ Circuit relay v2 source
│   └── gateway/             # 🆕 Phase 4F: Multi-mode daemon (SOCKS, DNS, TUN)
│
├── pkg/p2pnet/              # ✅ Core library (importable)
│   ├── ...existing...
│   ├── interfaces.go        # 🆕 Phase 4D: Plugin interfaces
│   └── federation.go        # 🆕 Phase 4H: Network peering
│
├── internal/
│   ├── config/              # ✅ Configuration + self-healing (archive, commit-confirmed)
│   ├── auth/                # ✅ Authentication
│   ├── identity/            # ✅ Shared identity management
│   ├── validate/            # ✅ Input validation (service names, etc.)
│   ├── watchdog/            # ✅ Health checks + sd_notify
│   ├── transfer/            # 🆕 Phase 4D: File transfer plugin
│   └── tun/                 # 🆕 Phase 4F: TUN/TAP interface
│
├── mobile/                  # 🆕 Phase 4G: Mobile apps
│   ├── ios/
│   └── android/
│
└── ...existing (relay-server/, configs, docs, examples)
```

### Service Exposure Architecture

```
┌──────────────────────────────────────────────────────────────┐
│  Application Layer (User's Services)                         │
│  ┌────────┐  ┌────────┐  ┌────────┐  ┌────────┐            │
│  │  SSH   │  │  HTTP  │  │  SMB   │  │ Custom │            │
│  │  :22   │  │  :80   │  │  :445  │  │ :9999  │            │
│  └───┬────┘  └───┬────┘  └───┬────┘  └───┬────┘            │
└──────┼───────────┼───────────┼───────────┼─────────────────┘
       │           │           │           │
       └───────────┴───────────┴───────────┘
                   │
                   ▼
       ┌────────────────────────────┐
       │   Service Registry         │
       │   (pkg/p2pnet/service.go)  │
       │                            │
       │   "ssh"  → localhost:22    │
       │   "web"  → localhost:80    │
       │   "smb"  → localhost:445   │
       │   "custom" → localhost:9999│
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   TCP ↔ Stream Proxy       │
       │   (pkg/p2pnet/proxy.go)    │
       │                            │
       │   Bidirectional relay:     │
       │   TCP socket ↔ libp2p      │
       │   stream                   │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   libp2p Network           │
       │   (with authentication)    │
       │                            │
       │   Protocol:                │
       │   /peerup/ssh/1.0.0        │
       │   /peerup/http/1.0.0       │
       │   /peerup/smb/1.0.0        │
       └────────────────────────────┘
```

### Gateway Daemon Modes

#### Mode 1: SOCKS Proxy (No Root Required)

```
┌─────────────────────────────────────────────────────────┐
│  Applications (configured to use SOCKS)                 │
│  ┌────────┐  ┌──────────┐  ┌──────────────┐           │
│  │  SSH   │  │  Browser │  │  Custom App  │           │
│  └───┬────┘  └────┬─────┘  └──────┬───────┘           │
└──────┼────────────┼────────────────┼─────────────────  ┘
       └────────────┴────────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   SOCKS5 Proxy             │
       │   localhost:1080           │
       │                            │
       │   Translates:              │
       │   "laptop.grewal:22"       │
       │   → peer ID                │
       │   → P2P connection         │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   P2P Network              │
       │   (pkg/p2pnet)             │
       └────────────────────────────┘
```

#### Mode 2: DNS Server

```
┌─────────────────────────────────────────────────────────┐
│  Applications (use system DNS)                          │
│  ┌────────┐  ┌──────────┐  ┌──────────────┐           │
│  │  SSH   │  │  Browser │  │  SMB Client  │           │
│  └───┬────┘  └────┬─────┘  └──────┬───────┘           │
└──────┼────────────┼────────────────┼─────────────────  ┘
       └────────────┴────────────────┘
                    │
              DNS Query:
              "laptop.grewal.p2p"
                    │
                    ▼
       ┌────────────────────────────┐
       │   Local DNS Server         │
       │   localhost:53             │
       │                            │
       │   Resolves:                │
       │   laptop.grewal.p2p        │
       │   → 10.64.1.5              │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   Virtual IP Router        │
       │                            │
       │   10.64.1.5 → peer ID      │
       │   → P2P connection         │
       └────────────────────────────┘
```

#### Mode 3: TUN/TAP Virtual Network (Requires Root)

```
┌─────────────────────────────────────────────────────────┐
│  Applications (completely transparent)                  │
│  ┌────────┐  ┌──────────┐  ┌──────────────┐           │
│  │  SSH   │  │  Browser │  │  ANY App     │           │
│  └───┬────┘  └────┬─────┘  └──────┬───────┘           │
└──────┼────────────┼────────────────┼─────────────────  ┘
       └────────────┴────────────────┘
                    │
              Normal TCP/UDP
              to 10.64.x.x
                    │
                    ▼
       ┌────────────────────────────┐
       │   Kernel Network Stack     │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   TUN Interface (peer0)    │
       │   10.64.0.1/16             │
       │                            │
       │   Intercepts all packets   │
       │   to 10.64.0.0/16          │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   Gateway Daemon           │
       │                            │
       │   Packet → Peer ID lookup  │
       │   → P2P stream             │
       │   → Forward data           │
       └────────────────────────────┘
```

---

## Daemon Architecture

### Daemon Architecture

`peerup daemon` is the single command for running a P2P host. It starts the full P2P lifecycle plus a Unix domain socket API for programmatic control (zero overhead if unused — it's just a listener).

### Shared P2P Runtime

To avoid code duplication, the P2P lifecycle is extracted into `serve_common.go`:

```go
// serveRuntime holds the shared P2P lifecycle state.
type serveRuntime struct {
    network    *p2pnet.Network
    config     *config.HomeNodeConfig
    configFile string
    gater      *auth.AuthorizedPeerGater  // nil if gating disabled
    authKeys   string                      // path to authorized_keys
    ctx        context.Context
    cancel     context.CancelFunc
    version    string
    startTime  time.Time
}
```

Methods: `newServeRuntime()`, `Bootstrap()`, `ExposeConfiguredServices()`, `SetupPingPong()`, `StartWatchdog()`, `StartStatusPrinter()`, `Shutdown()`.

### Daemon Server

The daemon server (`internal/daemon/`) is decoupled from the CLI via the `RuntimeInfo` interface:

```go
type RuntimeInfo interface {
    Network() *p2pnet.Network
    ConfigFile() string
    AuthKeysPath() string
    GaterForHotReload() GaterReloader  // nil if gating disabled
    Version() string
    StartTime() time.Time
    PingProtocolID() string
}
```

The `serveRuntime` struct implements this interface in `cmd_daemon.go`, keeping the daemon package importable without depending on CLI code.

### Cookie-Based Authentication

Every API request requires `Authorization: Bearer <token>`. The token is a 32-byte random hex string written to `~/.config/peerup/.daemon-cookie` with `0600` permissions. This follows the Bitcoin Core / Docker pattern — no plaintext passwords in config, token rotates on restart, same-user access only.

### Stale Socket Detection

No PID files. On startup, the daemon dials the existing socket:
- Connection succeeds → another daemon is alive → return error
- Connection fails → stale socket from a crash → remove and proceed

### Unix Socket API

14 HTTP endpoints over Unix domain socket. Every endpoint supports JSON (default) and plain text (`?format=text` or `Accept: text/plain`). Full API reference in [DAEMON-API.md](DAEMON-API.md).

### Dynamic Proxy Management

The daemon tracks active TCP proxies in memory. Scripts can create proxies via `POST /v1/connect` and tear them down via `DELETE /v1/connect/{id}`. All proxies are cleaned up on daemon shutdown.

### Auth Hot-Reload

`POST /v1/auth` and `DELETE /v1/auth/{peer_id}` modify the `authorized_keys` file and immediately reload the connection gater via the `GaterReloader` interface. Access grants and revocations take effect without restart.

---

## Concurrency Model

Background goroutines follow a consistent pattern for lifecycle management:

### Ticker + Select Pattern

All recurring background tasks (relay reservation, DHT advertising, status printing, stats logging) use `time.Ticker` with `select` on `ctx.Done()`:

```go
go func() {
    ticker := time.NewTicker(interval)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done():
            return
        case <-ticker.C:
            // do work
        }
    }
}()
```

This ensures goroutines exit cleanly when the parent context is cancelled (e.g., on Ctrl+C).

### Watchdog + sd_notify

Both `daemon` and `relay-server` run a watchdog goroutine (`internal/watchdog`) that performs health checks every 30 seconds:

- **peerup daemon**: Checks host has listen addresses, relay reservation is active, and Unix socket is responsive
- **relay-server**: Checks host has listen addresses and protocols are registered

On success, sends `WATCHDOG=1` to systemd via the `NOTIFY_SOCKET` unix datagram socket (pure Go, no CGo). On non-systemd systems (macOS), all sd_notify calls are no-ops. `READY=1` is sent after startup completes; `STOPPING=1` on shutdown.

The systemd service uses `Type=notify` and `WatchdogSec=90` (3x the 30s check interval) so systemd will restart the process if health checks stop succeeding.

### Health Check HTTP Endpoint (`/healthz`)

The relay server optionally exposes a `/healthz` HTTP endpoint for external monitoring (Prometheus, UptimeKuma, etc.). Disabled by default in config:

```yaml
health:
  enabled: true
  listen_address: "127.0.0.1:9090"
```

The endpoint returns JSON with: `status`, `peer_id`, `version`, `uptime_seconds`, `connected_peers`, `protocols`. Bound to localhost by default — not exposed to the internet. The HTTP server starts after the relay service is up and shuts down gracefully on SIGTERM.

### Commit-Confirmed Enforcement

When a commit-confirmed is active (`peerup config apply --confirm-timeout`), `serve` starts an `EnforceCommitConfirmed` goroutine that waits for the deadline. If `peerup config confirm` is not run before the timer fires, the goroutine reverts the config and calls `os.Exit(1)`. Systemd then restarts the process with the restored config.

### Graceful Shutdown

Long-running commands (`daemon`, `proxy`, `relay-server`) handle `SIGINT`/`SIGTERM` by calling `cancel()` on their root context, which propagates to all background goroutines. The daemon also accepts shutdown requests via the API (`POST /v1/shutdown`). Deferred cleanup (`net.Close()`, `listener.Close()`, socket/cookie removal) runs after goroutines stop.

### Atomic Counters

Shared counters accessed by concurrent goroutines (e.g., bootstrap peer count) use `atomic.Int32` instead of bare `int` to prevent data races.

---

## Core Concepts

### 1. Service Definition

Services are defined in configuration and registered at runtime:

```go
type Service struct {
    Name         string   // "ssh", "web", etc.
    Protocol     string   // "/peerup/ssh/1.0.0"
    LocalAddress string   // "localhost:22"
    Enabled      bool     // Enable/disable
}

type ServiceRegistry struct {
    services map[string]*Service
    host     host.Host
}

func (r *ServiceRegistry) RegisterService(svc *Service) error {
    // Set up stream handler for this service's protocol
    r.host.SetStreamHandler(svc.Protocol, func(s network.Stream) {
        // 1. Authorize peer
        if !r.isAuthorized(s.Conn().RemotePeer(), svc.Name) {
            s.Close()
            return
        }

        // 2. Dial local service
        localConn, err := net.Dial("tcp", svc.LocalAddress)
        if err != nil {
            s.Close()
            return
        }

        // 3. Bidirectional proxy
        go io.Copy(s, localConn)
        io.Copy(localConn, s)
    })
}
```

### 2. Bidirectional TCP↔Stream Proxy

```go
func ProxyStreamToTCP(stream network.Stream, tcpAddr string) error {
    // Connect to local TCP service
    tcpConn, err := net.Dial("tcp", tcpAddr)
    if err != nil {
        return err
    }
    defer tcpConn.Close()

    // Bidirectional copy
    errCh := make(chan error, 2)

    go func() {
        _, err := io.Copy(tcpConn, stream)
        errCh <- err
    }()

    go func() {
        _, err := io.Copy(stream, tcpConn)
        errCh <- err
    }()

    // Wait for either direction to finish
    return <-errCh
}
```

### 3. Name Resolution

```go
type NameResolver interface {
    Resolve(name string) (peer.ID, error)
}

type LocalFileResolver struct {
    names map[string]peer.ID
}

func (r *LocalFileResolver) Resolve(name string) (peer.ID, error) {
    if id, ok := r.names[name]; ok {
        return id, nil
    }
    return "", ErrNotFound
}

type DHTResolver struct {
    dht *dht.IpfsDHT
}

func (r *DHTResolver) Resolve(name string) (peer.ID, error) {
    // Query DHT for network's relay
    // Ask relay for peer name → ID mapping
    // Return peer ID
}

// Multi-tier resolution
func Resolve(name string, resolvers []NameResolver) (peer.ID, error) {
    for _, resolver := range resolvers {
        if id, err := resolver.Resolve(name); err == nil {
            return id, nil
        }
    }
    // If no resolver works, try to parse as direct peer ID
    return peer.Decode(name)
}
```

---

## Security Model

### Authentication Layers

**Layer 1: Network Level (ConnectionGater)**
- Executed during connection handshake
- Blocks unauthorized peers before any data exchange
- Fastest rejection (minimal resource usage)

**Layer 2: Protocol Level (Stream Handler)**
- Defense-in-depth validation
- Per-service authorization (optional)
- Can override global authorized_keys

### Per-Service Authorization

```yaml
# home-node.yaml
security:
  authorized_keys_file: "authorized_keys"  # Global default

services:
  ssh:
    enabled: true
    local_address: "localhost:22"
    authorized_keys: "ssh_authorized_keys"  # Override

  web:
    enabled: true
    local_address: "localhost:80"
    # Uses global authorized_keys
```

### Federation Trust Model

```yaml
# relay-server.yaml
federation:
  peers:
    - network_name: "alice"
      relay: "/ip4/.../p2p/..."
      trust_level: "full"      # Bidirectional routing

    - network_name: "bob"
      relay: "/ip4/.../p2p/..."
      trust_level: "one_way"   # Only alice → grewal, not grewal → alice
```

---

## Naming System

### Multi-Tier Resolution

```
User Request: ssh user@laptop.grewal
         │
         ▼
┌────────────────────────────────────┐
│  Tier 1: Local Override            │
│  Check: ~/.peerup/names.yaml       │
│  laptop.grewal → 12D3KooW...       │
└──────────┬─────────────────────────┘
           │ Not found
           ▼
┌────────────────────────────────────┐
│  Tier 2: Network-Scoped            │
│  Parse: laptop.grewal              │
│  Query: grewal relay for "laptop"  │
│  Response: 12D3KooW...             │
└──────────┬─────────────────────────┘
           │ Relay unreachable
           ▼
┌────────────────────────────────────┐
│  Tier 3: Blockchain (if enabled)   │
│  Query: Ethereum smart contract    │
│  grewal.register["laptop"]         │
│  Response: 12D3KooW...             │
└──────────┬─────────────────────────┘
           │ Not registered
           ▼
┌────────────────────────────────────┐
│  Tier 4: Direct Peer ID            │
│  Try: peer.Decode("laptop.grewal") │
│  Fails → Error: "Name not found"   │
└────────────────────────────────────┘
```

### Network-Scoped Name Format

```
Format: <hostname>.<network>[.<tld>]

Examples:
laptop.grewal           # Query grewal relay
desktop.alice           # Query alice relay
phone.bob.p2p           # Query bob relay (explicit .p2p TLD)
home.grewal.local       # mDNS compatible
```

---

## Federation Model

### Relay Peering

```
┌──────────────────────────────────────────────────────┐
│              Federated Networks                       │
│                                                       │
│  ┌─────────────┐      ┌─────────────┐               │
│  │   grewal    │◄────►│    alice    │               │
│  │   Network   │      │   Network   │               │
│  └──────┬──────┘      └──────┬──────┘               │
│         │                    │                       │
│         └────────┬───────────┘                       │
│                  │                                   │
│                  ▼                                   │
│         ┌─────────────┐                              │
│         │     bob     │                              │
│         │   Network   │                              │
│         └─────────────┘                              │
└──────────────────────────────────────────────────────┘

Routing Table (grewal relay):
- laptop.grewal     → direct (own network)
- desktop.alice     → peer via alice relay
- server.bob        → peer via bob relay
- phone.alice       → peer via alice relay

Cross-Network Connection:
laptop.grewal → server.bob

1. laptop connects to grewal relay
2. grewal relay forwards to bob relay (federation)
3. bob relay connects to server.bob
4. Connection established
```

---

## Mobile Architecture

### iOS (NEPacketTunnelProvider)

```
┌─────────────────────────────────────────────────────┐
│  iOS Application Layer                              │
│  ┌────────┐  ┌──────────┐  ┌──────────────┐       │
│  │  SSH   │  │  Safari  │  │  Plex App    │       │
│  └───┬────┘  └────┬─────┘  └──────┬───────┘       │
└──────┼────────────┼────────────────┼───────────────┘
       └────────────┴────────────────┘
                    │
              IP packets to
              10.64.x.x
                    │
                    ▼
       ┌────────────────────────────┐
       │   iOS Network Stack        │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │  NEPacketTunnelProvider    │
       │  (peer-up VPN extension)   │
       │                            │
       │  1. Capture packets        │
       │  2. Extract dest IP        │
       │  3. Map to peer ID         │
       │  4. Route via P2P          │
       └────────────┬───────────────┘
                    │
                    ▼
       ┌────────────────────────────┐
       │   libp2p-go (gomobile)     │
       │   P2P networking           │
       └────────────────────────────┘
```

### Android (VPNService)

Similar to iOS but with full VPNService API access:
- Create TUN interface
- Route all 10.64.0.0/16 traffic through app
- Full libp2p-go integration (easier than iOS)

---

## Performance Considerations

### Transport Preference

Both `peerup` and `relay-server` register transports in this order:

1. **QUIC** (preferred) — 3 RTTs to establish, native multiplexing, better for hole-punching. libp2p's smart dialing (built into v0.47.0) ranks QUIC addresses higher than TCP.
2. **TCP** — 4 RTTs, universal fallback for networks that block UDP.
3. **WebSocket** — Anti-censorship transport that looks like HTTPS to deep packet inspection (DPI). Commented out by default in sample configs.

### AutoNAT v2

Enabled on all hosts. AutoNAT v2 performs per-address reachability testing with nonce-based dial verification. This means the node knows which specific addresses (IPv4, IPv6, QUIC, TCP) are publicly reachable, rather than a single "public or private" determination. Also prevents amplification attacks by requiring the probing peer to prove it controls the claimed address.

### Version in Identify Protocol

All hosts set `libp2p.UserAgent()` so peers can discover each other's software version via the Identify protocol:
- **peerup nodes**: `peerup/<version>` (e.g., `peerup/0.1.0` or `peerup/dev`)
- **relay server**: `relay-server/<version>`

The UserAgent is stored in each peer's peerstore under the `AgentVersion` key after the Identify handshake completes (automatically on connect).

### Connection Optimization

1. **Relay vs Direct**:
   - Always attempt DCUtR for direct connection
   - Fall back to relay if hole-punching fails
   - Monitor connection quality and retry DCUtR periodically

2. **Connection Pooling**:
   - Reuse P2P streams for multiple requests
   - Multiplex services over single connection
   - Keep-alive mechanisms

3. **Bandwidth Management**:
   - QoS for different service types
   - Rate limiting per service
   - Bandwidth monitoring and alerts

### Caching

- DNS responses cached locally (TTL: 5 minutes)
- Peer ID → multiaddr mapping cached
- Federation routing table cached with periodic refresh

---

## Security Hardening

### Relay Resource Limits

The relay server enforces resource limits via libp2p's circuit relay v2 `WithResources()` and `WithLimit()` options. All limits are configurable in `relay-server.yaml` under the `resources:` section. Defaults are tuned for a private relay serving 2-10 peers with SSH/XRDP workloads:

| Parameter | Default | Description |
|-----------|---------|-------------|
| `max_reservations` | 128 | Total active relay slots |
| `max_circuits` | 16 | Open relay connections per peer |
| `max_reservations_per_ip` | 8 | Reservations per source IP |
| `max_reservations_per_asn` | 32 | Reservations per AS number |
| `reservation_ttl` | 1h | Reservation lifetime |
| `session_duration` | 10m | Max per-session duration |
| `session_data_limit` | 64MB | Max data per session per direction |

Session duration and data limits are raised from libp2p defaults (2min/128KB) to support real workloads (SSH, XRDP, file transfers). Zero-valued fields in config are filled with defaults at load time.

### Key File Permission Verification

Private key files are verified on load to ensure they are not readable by group or others. The shared `internal/identity` package provides `CheckKeyFilePermissions()` and `LoadOrCreateIdentity()`, used by both `peerup` and `relay-server`:

- **Expected**: `0600` (owner read/write only)
- **On violation**: Returns error with actionable fix: `chmod 600 <path>`
- **Windows**: Check is skipped (Windows uses ACLs, not POSIX permissions)

Keys are already created with `0600` permissions, but this check catches degradation from manual `chmod`, file copies across systems, or archive extraction.

### Config Self-Healing

The config system provides three layers of protection against bad configuration:

1. **Archive/Rollback** (`internal/config/archive.go`): On each successful `serve` or `relay-server` startup, the validated config is archived as `.{name}.last-good.yaml` next to the original. If a future edit breaks the config, `peerup config rollback` restores it. Archive writes are atomic (write temp file + rename).

2. **Commit-Confirmed** (`internal/config/confirm.go`): For remote config changes, `peerup config apply` backs up the current config, applies the new one, and writes a pending marker with a deadline. If `peerup config confirm` is not run before the deadline, the serve process reverts the config and exits. Systemd restarts with the restored config.

3. **Validation CLI** (`peerup config validate`): Check config syntax and required fields without starting the node. Useful before restarting a remote service.

### Service Name Validation

Service names are validated before use in protocol IDs to prevent injection attacks. Names flow into `fmt.Sprintf("/peerup/%s/1.0.0", name)` — without validation, a name like `ssh/../../evil` or `foo\nbar` creates ambiguous or invalid protocol IDs.

The validation logic lives in `internal/validate/validate.go` (`validate.ServiceName()`), shared by all callers.

**Validation rules** (DNS-label format):
- 1-63 characters
- Lowercase alphanumeric and hyphens only
- Must start and end with alphanumeric character
- Regex: `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`

Validated at four points:
1. `peerup service add` — rejects bad names at CLI entry
2. `ValidateNodeConfig()` — rejects bad names in config before startup
3. `ExposeService()` — rejects bad names at service registration time
4. `ConnectToService()` — rejects bad names at connection time

---

## Security Considerations

### Threat Model

**Threats Addressed**:
- ✅ Unauthorized peer access (ConnectionGater)
- ✅ Man-in-the-middle (libp2p Noise encryption)
- ✅ Replay attacks (Noise protocol nonces)
- ✅ Relay bandwidth theft (relay authentication + resource limits)
- ✅ Relay resource exhaustion (configurable per-peer/per-IP/per-ASN limits)
- ✅ Protocol ID injection (service name validation)
- ✅ Key file permission degradation (0600 check on load)
- ✅ Newline injection in authorized_keys (sanitized comments)
- ✅ YAML injection via peer names (allowlisted characters)
- ✅ OOM via unbounded stream reads (512-byte buffer limits)
- ✅ Symlink attacks on temp files (os.CreateTemp with random suffix)
- ✅ Multiaddr injection in config (validated before writing)

**Threats NOT Addressed** (out of scope):
- ❌ Relay compromise (relay can see metadata, not content)
- ❌ Peer key compromise (users must secure private keys)
- ❌ DoS attacks (rate limiting planned for future)

### Best Practices

1. **Key Management**:
   - Private keys: 0600 permissions
   - authorized_keys: 0600 permissions
   - Never commit keys to git

2. **Network Segmentation**:
   - Use per-service authorized_keys when needed
   - Limit service exposure (disable unused services)
   - Audit authorized_keys regularly

3. **Relay Security**:
   - Enable relay authentication in production
   - Monitor relay bandwidth usage
   - Use non-standard ports

---

## Scalability

### Current Limitations

- **Relay bandwidth**: Limited by VPS plan (~1TB/month)
- **Connections per relay**: Limited by file descriptors (~1000-10000)
- **DHT lookups**: Slow for large networks (10-30 seconds)

### Future Improvements

- Multiple relay failover/load balancing
- Relay-to-relay mesh for redundancy
- Optimized peer routing (shortest path)
- Distributed hash table optimization
- Connection multiplexing

---

## Technology Stack

**Core**:
- Go 1.25+
- libp2p v0.47.0 (networking)
- Private Kademlia DHT (`/peerup/kad/1.0.0` — isolated from IPFS Amino)
- Noise protocol (encryption)
- QUIC transport (preferred — 3 RTTs vs 4 for TCP)
- AutoNAT v2 (per-address reachability testing)

**Why libp2p**: peer-up's networking foundation is the same stack used by Ethereum's consensus layer (Beacon Chain), Filecoin, and Polkadot — networks collectively securing hundreds of billions in value. When Ethereum chose a P2P stack for their most critical infrastructure, they picked libp2p. Improvements driven by these ecosystems (transport optimizations, Noise hardening, gossipsub refinements) flow back to the shared codebase. See the [FAQ](FAQ.md#how-does-ethereums-p2p-network-compare-to-peer-ups) for detailed comparisons.

**Optional**:
- Ethereum (blockchain naming)
- IPFS (distributed storage)
- gomobile (iOS/Android)

---

**Last Updated**: 2026-02-16
**Architecture Version**: 2.8 (Daemon Mode — Unix socket API, cookie auth, network tools)
