---
title: "Architecture"
weight: 8
description: "Technical architecture of Shurli: libp2p foundation, circuit relay v2, DHT peer discovery, daemon design, connection gating, and naming system."
---
<!-- Auto-synced from docs/ARCHITECTURE.md by sync-docs - do not edit directly -->


This document describes the technical architecture of Shurli, from current implementation to future vision.

## Table of Contents

- [Current Architecture (Phase 5 Complete)](#current-architecture-phase-5-complete) - what's built and working
- [Target Architecture (Phase 8+)](#target-architecture-phase-8) - planned additions
- [Observability (Batch H)](#observability-batch-h) - Prometheus metrics, audit logging
- [Adaptive Path Selection (Batch I)](#adaptive-path-selection-batch-i) - interface discovery, dial racing, STUN, peer relay
- [Core Concepts](#core-concepts) - implemented patterns
- [Security Model](#security-model) - implemented + planned extensions
  - [Role-Based Access Control (Phase 6)](#role-based-access-control-phase-6) - admin/member tiers
  - [Macaroon Capability Tokens (Phase 6)](#macaroon-capability-tokens-phase-6) - HMAC-chain bearer tokens
  - [Passphrase-Sealed Vault (Phase 6)](#passphrase-sealed-vault-phase-6) - relay key protection
  - [Async Invite Deposits (Phase 6)](#async-invite-deposits-phase-6) - client-deposit invites
- [Naming System](#naming-system) - local names implemented, network-scoped and blockchain planned
- [Federation Model](#federation-model) - planned (Phase 12)
- [Mobile Architecture](#mobile-architecture) - planned (Phase 11)

---

## Current Architecture (Phase 5 Complete)

### Component Overview

```
Shurli/
├── cmd/
│   ├── shurli/              # Single binary with subcommands
│   │   ├── main.go          # Command dispatch (daemon, ping, traceroute, resolve,
│   │   │                    #   proxy, whoami, auth, relay, config, service,
│   │   │                    #   invite, join, verify, status, init, version)
│   │   ├── cmd_daemon.go    # Daemon mode + client subcommands (status, stop, ping, etc.)
│   │   ├── serve_common.go  # Shared P2P runtime (serveRuntime) - used by daemon
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
│   │   ├── cmd_verify.go    # SAS verification (4-emoji fingerprint)
│   │   ├── cmd_relay_serve.go # Relay server: serve/authorize/info/config
│   │   ├── cmd_relay_pair.go  # Relay pairing code generation
│   │   ├── cmd_relay_vault.go # Vault CLI: init/seal/unseal/status
│   │   ├── cmd_relay_invite.go # Invite CLI: create/list/revoke/modify
│   │   ├── cmd_relay_setup.go # Relay interactive setup wizard
│   │   ├── config_template.go # Shared node config YAML template (single source of truth)
│   │   ├── relay_input.go   # Flexible relay address parsing (IP, IP:PORT, multiaddr)
│   │   ├── flag_helpers.go  # Shared CLI flag parsing helpers
│   │   └── exit.go          # Testable os.Exit wrapper
│
├── pkg/p2pnet/              # Importable P2P library
│   ├── network.go           # Core network setup, relay helpers, name resolution
│   ├── service.go           # Service registry (register/unregister, expose/unexpose)
│   ├── proxy.go             # Bidirectional TCP↔Stream proxy with half-close + byte counting
│   ├── naming.go            # Local name resolution (name → peer ID)
│   ├── identity.go          # Identity helpers (delegates to internal/identity)
│   ├── ping.go              # Shared P2P ping logic (PingPeer, ComputePingStats)
│   ├── traceroute.go        # Shared P2P traceroute (TracePeer, hop analysis)
│   ├── verify.go            # SAS verification helpers (emoji fingerprints)
│   ├── reachability.go      # Reachability grade calculation (A-F scale)
│   ├── interfaces.go        # Interface discovery, IPv6/IPv4 classification
│   ├── pathdialer.go        # Parallel dial racing (direct + relay, first wins)
│   ├── pathtracker.go       # Per-peer path quality tracking (event-bus driven)
│   ├── netmonitor.go        # Network change monitoring (event-driven)
│   ├── stunprober.go        # RFC 5389 STUN client, NAT type classification
│   ├── peerrelay.go         # Every-peer-is-a-relay (auto-enable with public IP)
│   ├── mdns.go              # mDNS LAN discovery (dedup, concurrency limiting)
│   ├── mdns_browse_native.go # Native DNS-SD via dns_sd.h (macOS/Linux CGo)
│   ├── mdns_browse_fallback.go # Pure-Go zeroconf fallback (other platforms)
│   ├── peermanager.go       # Background reconnection with exponential backoff
│   ├── netintel.go          # Presence protocol (/shurli/presence/1.0.0, gossip forwarding)
│   ├── metrics.go           # Prometheus metrics (custom registry, all shurli collectors)
│   ├── audit.go             # Structured audit logger (nil-safe, slog-based)
│   └── errors.go            # Sentinel errors
│
├── internal/
│   ├── config/              # YAML configuration loading + self-healing
│   │   ├── config.go           # Config structs (HomeNode, Client, Relay, unified NodeConfig)
│   │   ├── loader.go           # Load, validate, resolve paths, find config
│   │   ├── archive.go          # Last-known-good archive/rollback (atomic writes)
│   │   ├── confirm.go          # Commit-confirmed pattern (apply/confirm/enforce)
│   │   ├── snapshot.go         # TimeMachine-style config snapshots
│   │   └── errors.go           # Sentinel errors (ErrConfigNotFound, ErrNoArchive, etc.)
│   ├── auth/                # SSH-style authentication + role-based access
│   │   ├── authorized_keys.go  # Parser + ConnectionGater loader
│   │   ├── gater.go            # ConnectionGater implementation
│   │   ├── manage.go           # AddPeer/RemovePeer/ListPeers (shared by CLI commands)
│   │   ├── roles.go            # Role-based access control (admin/member)
│   │   └── errors.go           # Sentinel errors
│   ├── daemon/              # Daemon API server + client
│   │   ├── types.go            # JSON request/response types (StatusResponse, PingRequest, etc.)
│   │   ├── server.go           # Unix socket HTTP server, cookie auth, proxy tracking
│   │   ├── handlers.go         # HTTP handlers, format negotiation (JSON + text)
│   │   ├── middleware.go       # HTTP instrumentation (request timing, path sanitization)
│   │   ├── client.go           # Client library for CLI → daemon communication
│   │   ├── errors.go           # Sentinel errors (ErrDaemonAlreadyRunning, etc.)
│   │   └── daemon_test.go      # Tests (auth, handlers, lifecycle, integration)
│   ├── identity/            # Ed25519 identity management (shared by shurli + relay-server)
│   │   └── identity.go      # CheckKeyFilePermissions, LoadOrCreateIdentity, PeerIDFromKeyFile
│   ├── invite/              # Invite code encoding + PAKE handshake
│   │   ├── code.go          # Binary -> base32 with dash grouping
│   │   └── pake.go          # PAKE key exchange (X25519 DH + HKDF-SHA256 + XChaCha20-Poly1305)
│   ├── macaroon/            # HMAC-chain capability tokens
│   │   ├── macaroon.go      # Macaroon struct, HMAC chaining, verify, encode/decode
│   │   └── caveat.go        # Caveat language parser (7 types: service, group, action, etc.)
│   ├── totp/                # RFC 6238 time-based one-time passwords
│   │   └── totp.go          # Generate, Validate (with skew), NewSecret, provisioning URI
│   ├── vault/               # Passphrase-sealed relay key vault
│   │   └── vault.go         # Argon2id KDF + XChaCha20-Poly1305, seal/unseal, seed recovery
│   ├── deposit/             # Macaroon-backed async invite deposits
│   │   ├── store.go         # DepositStore: create, consume, revoke, add caveat, cleanup
│   │   └── errors.go        # ErrDepositNotFound, Consumed, Revoked, Expired
│   ├── yubikey/             # Yubikey HMAC-SHA1 challenge-response
│   │   └── challenge.go     # ykman CLI integration (IsAvailable, ChallengeResponse)
│   ├── relay/               # Relay pairing, admin socket, peer introductions, vault unseal
│   │   ├── tokens.go        # Token store (v2 pairing codes, TTL, namespace)
│   │   ├── pairing.go       # Relay pairing protocol (/shurli/relay-pair/1.0.0)
│   │   ├── notify.go        # Reconnect notifier + peer introduction delivery (/shurli/peer-notify/1.0.0)
│   │   ├── admin.go         # Relay admin Unix socket server (cookie auth, /v1/pair, vault, invites)
│   │   ├── admin_client.go  # HTTP client for relay admin socket (fire-and-forget)
│   │   └── unseal.go        # Remote unseal P2P protocol (/shurli/relay-unseal/1.0.0)
│   ├── reputation/           # Peer interaction tracking
│   │   └── history.go       # Append-only interaction log per peer (foundation for PeerManager)
│   ├── qr/                  # QR Code encoder for terminal display (inlined from skip2/go-qrcode)
│   │   ├── qrcode.go        # Public API: New(), Bitmap(), ToSmallString()
│   │   ├── encoder.go       # Data encoding (numeric, alphanumeric, byte modes)
│   │   ├── symbol.go        # Module matrix, pattern placement, penalty scoring
│   │   ├── version.go       # All 40 QR versions × 4 recovery levels
│   │   ├── gf.go            # GF(2^8) arithmetic + Reed-Solomon encoding
│   │   └── bitset.go        # Append-only bit array operations
│   ├── termcolor/           # Minimal ANSI terminal colors (replaces fatih/color)
│   │   └── color.go         # Green, Red, Yellow, Faint - respects NO_COLOR
│   ├── validate/            # Input validation helpers
│   │   ├── service.go        # ServiceName() - DNS-label format for protocol IDs
│   │   ├── network.go        # Network address validation (multiaddr, IP, port)
│   │   └── errors.go         # Sentinel errors
│   └── watchdog/            # Health monitoring + systemd integration
│       └── watchdog.go      # Health check loop, sd_notify (Ready/Watchdog/Stopping)
│
├── relay-server/            # Deployment artifacts
│   ├── setup.sh             # Deploy/verify/uninstall (builds shurli, runs relay serve)
│   └── relay-server.service # systemd unit template (installed as shurli-relay.service)
│
├── deploy/                  # Service management files
│   ├── shurli-daemon.service   # systemd unit for daemon (Linux)
│   └── com.shurli.daemon.plist # launchd plist for daemon (macOS)
│
├── configs/                 # Sample configuration files
│   ├── shurli.sample.yaml
│   ├── relay-server.sample.yaml
│   └── authorized_keys.sample
│
├── docs/                    # Project documentation
│   ├── ARCHITECTURE.md      # This file
│   ├── DAEMON-API.md        # Daemon API reference
│   ├── ENGINEERING-JOURNAL.md # Phase-by-phase engineering decisions
│   ├── MONITORING.md        # Prometheus + Grafana monitoring guide
│   ├── NETWORK-TOOLS.md     # Network diagnostic tools guide
│   ├── ROADMAP.md
│   ├── TESTING.md
│   ├── engineering-journal/ # Detailed per-phase journal entries
│   └── faq/               # FAQ sub-pages (comparisons, security, relay, design, deep dives)
│
└── examples/                # Example implementations
    └── basic-service/
```

### Network Topology (Current)

![Network topology: Client and Home Node behind NAT, connected through Relay with optional direct path via DCUtR hole-punching](/images/docs/arch-network-topology.svg)

### Authentication Flow

![Authentication flow: Client → Noise handshake → ConnectionGater check → authorized or denied → protocol handler defense-in-depth](/images/docs/arch-auth-flow.svg)

### Peer Authorization Methods

There are three ways to authorize peers:

**1. CLI - `shurli auth`**
```bash
shurli auth add <peer-id> --comment "label"
shurli auth list
shurli auth remove <peer-id>
```

**2. Invite/Join flow - zero-touch mutual authorization**
```
Machine A: shurli invite --name home     # Generates invite code + QR
Machine B: shurli join <code> --name laptop  # Decodes, connects, auto-authorizes both sides
```
The invite protocol uses PAKE-secured key exchange: ephemeral X25519 DH + token-bound HKDF-SHA256 key derivation + XChaCha20-Poly1305 AEAD encryption. The relay sees only opaque encrypted bytes during pairing. Both peers add each other to `authorized_keys` and `names` config automatically. Version byte: 0x01 = PAKE-encrypted invite, 0x02 = relay pairing code. Legacy cleartext protocol was deleted (zero downgrade surface).

**3. Manual - edit `authorized_keys` file directly**
```bash
echo "12D3KooW... # home-server" >> ~/.config/shurli/authorized_keys
```

---

## Target Architecture (Phase 8+)

### Planned Additions

Building on the current structure, future phases will add:

```
Shurli/
├── cmd/
│   ├── shurli/              # ✅ Single binary (daemon, serve, ping, traceroute, resolve,
│   │                        #   proxy, whoami, auth, relay, config, service, invite, join,
│   │                        #   status, init, version)
│   └── gateway/             # 🆕 Phase 10: Multi-mode daemon (SOCKS, DNS, TUN)
│
├── pkg/p2pnet/              # ✅ Core library (importable)
│   ├── ...existing...
│   ├── interfaces.go        # 🆕 Phase 8: Plugin interfaces (note: pkg/p2pnet/interfaces.go already exists for Batch I interface discovery)
│   └── federation.go        # 🆕 Phase 12: Network peering
│
├── internal/
│   ├── config/              # ✅ Configuration + self-healing (archive, commit-confirmed)
│   ├── auth/                # ✅ Authentication
│   ├── identity/            # ✅ Shared identity management
│   ├── validate/            # ✅ Input validation (service names, etc.)
│   ├── watchdog/            # ✅ Health checks + sd_notify
│   ├── transfer/            # 🆕 Phase 8: File transfer plugin
│   └── tun/                 # 🆕 Phase 10: TUN/TAP interface
│
├── mobile/                  # 🆕 Phase 11: Mobile apps
│   ├── ios/
│   └── android/
│
└── ...existing (relay-server/, configs, docs, examples)
```

### Service Exposure Architecture

![Service exposure: 4-layer stack from Application (SSH/HTTP/SMB/Custom) through Service Registry and TCP-Stream Proxy to libp2p Network](/images/docs/arch-service-exposure.svg)

### Gateway Daemon Modes

> **Status: Planned (Phase 10)** - not yet implemented. See [Roadmap Phase 10](../roadmap/) for details.

![Gateway daemon modes: SOCKS Proxy (no root, app must be configured), DNS Server (resolve peer names to virtual IPs), and TUN/TAP (fully transparent, requires root)](/images/docs/arch-gateway-modes.svg)

---

## Daemon Architecture

![Daemon architecture: P2P Runtime (relay, DHT, services, watchdog) connected bidirectionally to Unix Socket API (HTTP/1.1, cookie auth, 15 endpoints), with P2P Network below left and CLI/Scripts below right](/images/docs/daemon-api-architecture.svg)

`shurli daemon` is the single command for running a P2P host. It starts the full P2P lifecycle plus a Unix domain socket API for programmatic control (zero overhead if unused - it's just a listener).

### Shared P2P Runtime

To avoid code duplication, the P2P lifecycle is extracted into `serve_common.go`:

```go
// serveRuntime holds the shared P2P lifecycle state.
type serveRuntime struct {
    network          *p2pnet.Network
    config           *config.HomeNodeConfig
    configFile       string
    gater            *auth.AuthorizedPeerGater // nil if gating disabled
    authKeys         string                    // path to authorized_keys
    ctx              context.Context
    cancel           context.CancelFunc
    version          string
    startTime        time.Time
    kdht             *dht.IpfsDHT             // peer discovery from daemon API
    ifSummary        *p2pnet.InterfaceSummary  // interface discovery (IPv4/IPv6)
    pathDialer       *p2pnet.PathDialer        // parallel dial racing
    pathTracker      *p2pnet.PathTracker       // per-peer path quality tracking
    stunProber       *p2pnet.STUNProber        // NAT type detection
    mdnsDiscovery    *p2pnet.MDNSDiscovery     // LAN discovery (nil when disabled)
    peerManager      *p2pnet.PeerManager       // background reconnection with backoff
    netIntel         *p2pnet.NetIntel          // presence protocol (nil when disabled)
    peerRelay        *p2pnet.PeerRelay         // auto-enabled with public IP
    metrics          *p2pnet.Metrics           // nil when telemetry disabled
    peerHistory      *reputation.PeerHistory   // per-peer interaction tracking
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
    GaterForHotReload() GaterReloader            // nil if gating disabled
    Version() string
    StartTime() time.Time
    PingProtocolID() string
    ConnectToPeer(ctx context.Context, peerID peer.ID) error
    Interfaces() *p2pnet.InterfaceSummary        // nil before discovery
    PathTracker() *p2pnet.PathTracker             // nil before bootstrap
    STUNResult() *p2pnet.STUNResult               // nil before probe
    IsRelaying() bool                             // true if peer relay enabled
}
```

The `serveRuntime` struct implements this interface in `cmd_daemon.go`, keeping the daemon package importable without depending on CLI code.

### Cookie-Based Authentication

Every API request requires `Authorization: Bearer <token>`. The token is a 32-byte random hex string written to `~/.config/shurli/.daemon-cookie` with `0600` permissions. This follows the Bitcoin Core / Docker pattern - no plaintext passwords in config, token rotates on restart, same-user access only.

### Stale Socket Detection

No PID files. On startup, the daemon dials the existing socket:
- Connection succeeds → another daemon is alive → return error
- Connection fails → stale socket from a crash → remove and proceed

### Unix Socket API

15 HTTP endpoints over Unix domain socket. Every endpoint supports JSON (default) and plain text (`?format=text` or `Accept: text/plain`). Full API reference in [Daemon API](../daemon-api/).

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

Both `shurli daemon` and `shurli relay serve` run a watchdog goroutine (`internal/watchdog`) that performs health checks every 30 seconds:

- **shurli daemon**: Checks host has listen addresses, relay reservation is active, and Unix socket is responsive
- **shurli relay serve**: Checks host has listen addresses and protocols are registered

On success, sends `WATCHDOG=1` to systemd via the `NOTIFY_SOCKET` unix datagram socket (pure Go, no CGo). On non-systemd systems (macOS), all sd_notify calls are no-ops. `READY=1` is sent after startup completes; `STOPPING=1` on shutdown.

The systemd service uses `Type=notify` and `WatchdogSec=90` (3x the 30s check interval) so systemd will restart the process if health checks stop succeeding.

### Health Check HTTP Endpoint (`/healthz`)

The relay server optionally exposes a `/healthz` HTTP endpoint for external monitoring (Prometheus, UptimeKuma, etc.). Disabled by default in config:

```yaml
health:
  enabled: true
  listen_address: "127.0.0.1:9090"
```

The endpoint returns JSON with: `status`, `peer_id`, `version`, `uptime_seconds`, `connected_peers`, `protocols`. Bound to localhost by default - not exposed to the internet. The HTTP server starts after the relay service is up and shuts down gracefully on SIGTERM.

### Commit-Confirmed Enforcement

When a commit-confirmed is active (`shurli config apply --confirm-timeout`), `serve` starts an `EnforceCommitConfirmed` goroutine that waits for the deadline. If `shurli config confirm` is not run before the timer fires, the goroutine reverts the config and calls `os.Exit(1)`. Systemd then restarts the process with the restored config.

### Graceful Shutdown

Long-running commands (`daemon`, `proxy`, `relay serve`) handle `SIGINT`/`SIGTERM` by calling `cancel()` on their root context, which propagates to all background goroutines. The daemon also accepts shutdown requests via the API (`POST /v1/shutdown`). Deferred cleanup (`net.Close()`, `listener.Close()`, socket/cookie removal) runs after goroutines stop.

### Atomic Counters

Shared counters accessed by concurrent goroutines (e.g., bootstrap peer count) use `atomic.Int32` instead of bare `int` to prevent data races.

### Observability (Batch H)

> **Status: Implemented** - opt-in Prometheus metrics + structured audit logging.

![Observability data flow - from metric sources through Prometheus registry to /metrics endpoint](/images/docs/observability-flow.svg)

All observability features are disabled by default and opt-in via config:

```yaml
telemetry:
  metrics:
    enabled: true
    listen_address: "127.0.0.1:9091"
  audit:
    enabled: true
```

**Prometheus Metrics** (`pkg/p2pnet/metrics.go`): Uses an isolated `prometheus.Registry` (not the global default) for testability and collision-free operation. When enabled, `libp2p.PrometheusRegisterer(reg)` exposes all built-in libp2p metrics (swarm, holepunch, autonat, rcmgr, relay) alongside custom shurli metrics. When disabled, `libp2p.DisableMetrics()` is called for zero CPU overhead.

Custom shurli metrics (30 total):
- `shurli_proxy_bytes_total{direction, service}` - bytes transferred through proxy
- `shurli_proxy_connections_total{service}` - proxy connections established
- `shurli_proxy_active_connections{service}` - currently active proxy sessions
- `shurli_proxy_duration_seconds{service}` - proxy session duration
- `shurli_auth_decisions_total{decision}` - auth allow/deny counts
- `shurli_holepunch_total{result}` - hole punch success/failure
- `shurli_holepunch_duration_seconds{result}` - hole punch timing
- `shurli_daemon_requests_total{method, path, status}` - API request counts
- `shurli_daemon_request_duration_seconds{method, path, status}` - API latency
- `shurli_path_dial_total{path_type, result}` - path dial attempts
- `shurli_path_dial_duration_seconds{path_type}` - path dial timing
- `shurli_connected_peers{path_type, transport, ip_version}` - connected peer count
- `shurli_network_change_total{change_type}` - network interface changes
- `shurli_stun_probe_total{result}` - STUN probe results
- `shurli_mdns_discovered_total{result}` - mDNS discovery events
- `shurli_peermanager_reconnect_total{result}` - reconnection attempts
- `shurli_netintel_sent_total{result}` - presence announcements sent
- `shurli_netintel_received_total{result}` - presence announcements received
- `shurli_interface_count{ip_version}` - network interface count
- `shurli_vault_sealed` - vault seal state (1=sealed, 0=unsealed)
- `shurli_vault_seal_operations_total{trigger}` - seal/unseal transitions by trigger
- `shurli_vault_unseal_total{result}` - remote unseal attempts
- `shurli_vault_unseal_locked_peers` - peers in lockout or permanently blocked
- `shurli_deposit_operations_total{operation}` - invite deposit lifecycle
- `shurli_deposit_pending` - pending unconsumed deposits
- `shurli_pairing_total{result}` - relay-mediated pairing attempts
- `shurli_macaroon_verify_total{result}` - macaroon token verifications
- `shurli_admin_request_total{endpoint, status}` - admin socket request counts
- `shurli_admin_request_duration_seconds{endpoint}` - admin socket latency
- `shurli_info{version, go_version}` - build information

**Audit Logger** (`pkg/p2pnet/audit.go`): Structured JSON events via `log/slog` with an `audit` group. All methods are nil-safe (no-op when audit is disabled). Events: auth decisions, service ACL denials, daemon API access, auth changes.

**Daemon Middleware** (`internal/daemon/middleware.go`): Wraps the HTTP handler chain (outside auth middleware) to capture request timing and status codes. Path parameters are sanitized (e.g., `/v1/auth/12D3KooW...` becomes `/v1/auth/:id`) to prevent high cardinality in metrics labels.

**Auth Decision Callback**: Uses a callback pattern (`auth.AuthDecisionFunc`) to decouple `internal/auth` from `pkg/p2pnet`, avoiding circular imports. The callback is wired in `serve_common.go` to feed both metrics counters and audit events.

**Relay Metrics**: When both health and metrics are enabled on the relay, `/metrics` is added to the existing `/healthz` HTTP mux. When only metrics is enabled, a dedicated HTTP server is started.

**Grafana Dashboard**: A pre-built dashboard (`grafana/shurli-dashboard.json`) ships with the project. Import it into any Grafana instance to visualize proxy throughput, auth decisions, hole punch success rates, API latency, and system metrics. 37 panels (31 visualizations + 6 row headers) across 6 sections: Overview, Proxy Throughput, Security, Hole Punch, Daemon API, and System.

**Reference**: `pkg/p2pnet/metrics.go`, `pkg/p2pnet/audit.go`, `internal/daemon/middleware.go`, `cmd/shurli/serve_common.go`, `grafana/shurli-dashboard.json`

### Adaptive Path Selection (Batch I)

> **Status: Implemented** - interface discovery, parallel dial racing, path tracking, network change monitoring, STUN probing, every-peer-is-a-relay.

![Adaptive Path Selection: 6 components (interface discovery, STUN probing, peer relay, parallel dial racing, path tracking, network monitoring) working together with path ranking from Direct IPv6 to VPS Relay](/images/docs/arch-adaptive-path.svg)

Six components work together to find and maintain the best connection path to each peer:

**Interface Discovery** (`pkg/p2pnet/interfaces.go`): `DiscoverInterfaces()` enumerates all network interfaces and classifies addresses as global IPv4, global IPv6, or loopback. Returns an `InterfaceSummary` with convenience flags (`HasGlobalIPv6`, `HasGlobalIPv4`). Called at startup and on every network change.

**Parallel Dial Racing** (`pkg/p2pnet/pathdialer.go`): `PathDialer.DialPeer()` replaces the old sequential connect (DHT 15s then relay 30s = 45s worst case) with parallel racing. If the peer is already connected, returns immediately. Otherwise fires DHT and relay strategies concurrently; first success wins, loser is cancelled. Classifies winning path as `DIRECT` or `RELAYED` based on multiaddr inspection.

![Dial Racing Flow: entry point checks if already connected (instant return), otherwise launches DHT discovery and relay circuit in parallel, first success wins with path classification](/images/docs/arch-dial-racing.svg)

**Path Quality Tracking** (`pkg/p2pnet/pathtracker.go`): `PathTracker` subscribes to libp2p's event bus (`EvtPeerConnectednessChanged`) for connect/disconnect events. Maintains per-peer path info: path type, transport (quic/tcp), IP version, connected time, last RTT. Exposed via `GET /v1/paths` daemon API. Prometheus labels: `path_type`, `transport`, `ip_version`.

**Network Change Monitoring** (`pkg/p2pnet/netmonitor.go`): `NetworkMonitor` watches for interface/address changes by polling `DiscoverInterfaces()` and diffing against the previous snapshot. On change, fires registered callbacks. Triggers: interface re-scan, STUN re-probe, peer relay auto-detect update.

**STUN NAT Detection** (`pkg/p2pnet/stunprober.go`): Zero-dependency RFC 5389 STUN client. Probes multiple STUN servers concurrently, collects external addresses, classifies NAT type (none, full-cone, address-restricted, port-restricted, symmetric). `HolePunchable()` indicates whether DCUtR hole-punching is likely to succeed. Runs in background at startup (non-blocking) and re-probes on network change.

**Every-Peer-Is-A-Relay** (`pkg/p2pnet/peerrelay.go`): Any peer with a detected global IP auto-enables circuit relay v2 with conservative resource limits (4 reservations, 16 circuits, 128KB/direction, 10min sessions). Uses the existing `ConnectionGater` for authorization (no new ACL needed). Auto-detects on startup and network changes. Disables when public IP is lost.

**Path Ranking**: direct IPv6 > direct IPv4 > STUN-punched > peer relay > VPS relay. If all paths fail, the system falls back to relay and tells the user honestly.

**Reference**: `pkg/p2pnet/interfaces.go`, `pkg/p2pnet/pathdialer.go`, `pkg/p2pnet/pathtracker.go`, `pkg/p2pnet/netmonitor.go`, `pkg/p2pnet/stunprober.go`, `pkg/p2pnet/peerrelay.go`, `cmd/shurli/serve_common.go`

---

## Core Concepts

### 1. Service Definition

Services are defined in configuration and registered at runtime:

```go
type Service struct {
    Name         string   // "ssh", "web", etc.
    Protocol     string   // "/shurli/ssh/1.0.0"
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

**Currently implemented**: `LocalFileResolver` resolves friendly names (configured via `shurli invite`/`shurli join` or manual YAML) to peer IDs. Direct peer ID strings are always accepted as fallback.

```go
type LocalFileResolver struct {
    names map[string]peer.ID
}

func (r *LocalFileResolver) Resolve(name string) (peer.ID, error) {
    if id, ok := r.names[name]; ok {
        return id, nil
    }
    return "", ErrNotFound
}
```

> **Planned (Phase 8/13)**: The `NameResolver` interface, `DHTResolver`, multi-tier chaining, and blockchain naming are planned extensions. See [Naming System](#naming-system) below and [Roadmap Phase 13](../roadmap/).

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

> **Status: Implemented** (Pre-Batch H)

Each service can optionally restrict access to specific peer IDs via `allowed_peers`. When set, only listed peers can connect to that service. When omitted (nil), all globally authorized peers can access it.

```yaml
services:
  ssh:
    enabled: true
    local_address: "localhost:22"
    allowed_peers: ["12D3KooW..."]  # Only these peers can access SSH

  web:
    enabled: true
    local_address: "localhost:80"
    # No allowed_peers = all authorized peers can access
```

The ACL check runs in the stream handler before dialing the local TCP service, so rejected peers never trigger a connection to the backend.

### Role-Based Access Control (Phase 6)

> **Status: Implemented**

Three-tier access model for relay operations:

- **Tier 0 (Relay Operator)**: Unix socket access. Full control via admin endpoints.
- **Tier 1 (Network Admin)**: First peer paired with relay auto-promoted to `role=admin`. Can create/revoke invites, unseal relay remotely.
- **Tier 2 (Member)**: Standard authorized peer. Can use relay services but cannot create invites (unless invite policy is `open`).

Roles are stored as `role=admin` or `role=member` attributes in `authorized_keys`. The first peer paired with a relay is automatically promoted to admin if no admins exist.

**Reference**: `internal/auth/roles.go`, `internal/auth/manage.go`, `internal/relay/pairing.go`

### Macaroon Capability Tokens (Phase 6)

> **Status: Implemented**

HMAC-chain bearer tokens for invite permissions. Each caveat in the chain produces a new HMAC-SHA256 signature, making caveat removal cryptographically impossible.

Key properties:
- **Attenuation-only**: holders can add restrictions (caveats), never remove them
- **Offline verification**: any party with the root key can verify without network calls
- **Compact**: base64-encoded JSON, suitable for CLI and QR codes

Supported caveat types: `service`, `group`, `action`, `peers_max`, `delegate`, `expires`, `network`.

**Reference**: `internal/macaroon/macaroon.go`, `internal/macaroon/caveat.go`

### Passphrase-Sealed Vault (Phase 6)

> **Status: Implemented**

![Vault seal/unseal lifecycle: sealed (watch-only) at startup, unseal with passphrase + optional 2FA, auto-reseal on timeout](/images/docs/arch-vault-lifecycle.svg)

The relay's root key material (used for macaroon minting) is protected by a passphrase-sealed vault. Two operational modes:

**Sealed (default after restart)**:
- Routes circuit relay traffic for existing peers
- Serves existing peer introductions
- Cannot authorize new peers or process invite deposits

**Unsealed (time-bounded)**:
- All sealed-mode operations plus new peer authorization
- Processes invite deposits and join requests
- Auto-reseals after configurable timeout

**Crypto stack**:
- KDF: Argon2id (time=3, memory=64MB, threads=4, keyLen=32)
- Encryption: XChaCha20-Poly1305
- 2FA: TOTP (RFC 6238) and/or Yubikey HMAC-SHA1

**Seed recovery**: hex-encoded 32-byte root key (24 words). Reconstructs vault with new passphrase.

**Remote unseal**: `/shurli/relay-unseal/1.0.0` P2P protocol. Admin-only (role check), iOS-style escalating lockout (4 free attempts, then 1m/5m/15m/1h, permanent block). Prometheus metrics: `shurli_vault_sealed`, `shurli_vault_seal_operations_total{trigger}`, `shurli_vault_unseal_total{result}`, `shurli_vault_unseal_locked_peers`.

**Reference**: `internal/vault/vault.go`, `internal/relay/unseal.go`, `internal/totp/totp.go`, `internal/yubikey/challenge.go`

### Async Invite Deposits (Phase 6)

> **Status: Implemented**

![Invite deposit lifecycle: admin creates deposit, deposit sits on relay, joiner consumes asynchronously](/images/docs/arch-invite-deposit.svg)

Client-deposit invites ("contact card" model). Admin creates an invite deposit on the relay and walks away. The joining peer consumes it later, without the admin needing to be online.

**Attenuation-only model**: the invite code is the authentication (immutable token). Permissions are mutable caveats on the deposit macaroon. Admins can restrict or revoke before consumption, but can never widen permissions (HMAC chain enforces this cryptographically).

Deposit states: `pending` -> `consumed` | `revoked` | `expired`

**Relay admin endpoints**: `POST /v1/invite` (create), `GET /v1/invite` (list), `DELETE /v1/invite/{id}` (revoke), `PATCH /v1/invite/{id}` (add caveats).

**Reference**: `internal/deposit/store.go`, `cmd/shurli/cmd_relay_invite.go`

### Federation Trust Model

> **Status: Planned (Phase 12)** - not yet implemented. See [Federation Model](#federation-model) and [Roadmap Phase 12](../roadmap/).

```yaml
# relay-server.yaml (planned config format)
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

> **What works today**: Tier 1 (Local Override) - friendly names configured via `shurli invite`/`join` or manual YAML - and the Direct Peer ID fallback. Tiers 2-3 (Network-Scoped, Blockchain) are planned for Phase 10/13.

![Name resolution waterfall: Local Override → Network-Scoped → Blockchain → Direct Peer ID, with fallthrough on each tier](/images/docs/arch-naming-system.svg)

### Network-Scoped Name Format

> **Status: Planned (Phase 10/13)** - not yet implemented. Currently only simple names work (e.g., `home`, `laptop` as configured in local YAML). The dotted network format below is a future design.

```
Format: <hostname>.<network>[.<tld>]

Examples (planned):
laptop.grewal           # Query grewal relay
desktop.alice           # Query alice relay
phone.bob.p2p           # Query bob relay (explicit .p2p TLD)
home.grewal.local       # mDNS compatible
```

---

## Federation Model

> **Status: Planned (Phase 12)** - not yet implemented. See [Roadmap Phase 12](../roadmap/).

### Relay Peering

![Federation model: three networks (A, B, C) with relay peering - cross-network connections routed through federated relays](/images/docs/arch-federation.svg)

---

## Mobile Architecture

> **Status: Planned (Phase 11)** - not yet implemented. See [Roadmap Phase 11](../roadmap/).

![Mobile architecture: iOS uses NEPacketTunnelProvider, Android uses VPNService - both embed libp2p-go via gomobile](/images/docs/arch-mobile.svg)

---

## Performance Considerations

### Transport Preference

Both `shurli daemon` and `shurli relay serve` register transports in this order:

1. **QUIC** (preferred) - 3 RTTs to establish, native multiplexing, better for hole-punching. libp2p's smart dialing (built into v0.47.0) ranks QUIC addresses higher than TCP.
2. **TCP** - 4 RTTs, universal fallback for networks that block UDP.
3. **WebSocket** - Anti-censorship transport that looks like HTTPS to deep packet inspection (DPI). Commented out by default in sample configs.

### AutoNAT v2

Enabled on all hosts. AutoNAT v2 performs per-address reachability testing with nonce-based dial verification. This means the node knows which specific addresses (IPv4, IPv6, QUIC, TCP) are publicly reachable, rather than a single "public or private" determination. Also prevents amplification attacks by requiring the probing peer to prove it controls the claimed address.

### Version in Identify Protocol

All hosts set `libp2p.UserAgent()` so peers can discover each other's software version via the Identify protocol:
- **shurli nodes**: `shurli/<version>` (e.g., `shurli/0.1.0` or `shurli/dev`)
- **relay server**: `relay-server/<version>`

The UserAgent is stored in each peer's peerstore under the `AgentVersion` key after the Identify handshake completes (automatically on connect).

### Connection Optimization

1. **Relay vs Direct** (implemented):
   - Always attempt DCUtR for direct connection
   - Fall back to relay if hole-punching fails

2. **Connection Pooling** (planned):
   - Reuse P2P streams for multiple requests
   - Multiplex services over single connection
   - Keep-alive mechanisms

3. **Bandwidth Management** (planned):
   - QoS for different service types
   - Rate limiting per service
   - Bandwidth monitoring and alerts

> Items marked "planned" are tracked in the [Roadmap](../roadmap/) under Phase 4C deferred items and Phase 14+.

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

Private key files are verified on load to ensure they are not readable by group or others. The shared `internal/identity` package provides `CheckKeyFilePermissions()` and `LoadOrCreateIdentity()`, used by both `shurli daemon` and `shurli relay serve`:

- **Expected**: `0600` (owner read/write only)
- **On violation**: Returns error with actionable fix: `chmod 600 <path>`
- **Windows**: Check is skipped (Windows uses ACLs, not POSIX permissions)

Keys are already created with `0600` permissions, but this check catches degradation from manual `chmod`, file copies across systems, or archive extraction.

### Config Self-Healing

The config system provides three layers of protection against bad configuration:

1. **Archive/Rollback** (`internal/config/archive.go`): On each successful `daemon` or `relay serve` startup, the validated config is archived as `.{name}.last-good.yaml` next to the original. If a future edit breaks the config, `shurli config rollback` restores it. Archive writes are atomic (write temp file + rename).

2. **Commit-Confirmed** (`internal/config/confirm.go`): For remote config changes, `shurli config apply` backs up the current config, applies the new one, and writes a pending marker with a deadline. If `shurli config confirm` is not run before the deadline, the serve process reverts the config and exits. Systemd restarts with the restored config.

3. **Validation CLI** (`shurli config validate`): Check config syntax and required fields without starting the node. Useful before restarting a remote service.

### Service Name Validation

Service names are validated before use in protocol IDs to prevent injection attacks. Names flow into `fmt.Sprintf("/shurli/%s/1.0.0", name)` - without validation, a name like `ssh/../../evil` or `foo\nbar` creates ambiguous or invalid protocol IDs.

The validation logic lives in `internal/validate/validate.go` (`validate.ServiceName()`), shared by all callers.

**Validation rules** (DNS-label format):
- 1-63 characters
- Lowercase alphanumeric and hyphens only
- Must start and end with alphanumeric character
- Regex: `^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`

Validated at four points:
1. `shurli service add` - rejects bad names at CLI entry
2. `ValidateNodeConfig()` - rejects bad names in config before startup
3. `ExposeService()` - rejects bad names at service registration time
4. `ConnectToService()` - rejects bad names at connection time

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
- ✅ Per-service access control (AllowedPeers ACL on each service)
- ✅ Host resource exhaustion (libp2p ResourceManager with auto-scaled limits)
- ✅ SYN/UDP flood on relay (iptables rate limiting, SYN cookies, conntrack tuning)
- ✅ IP spoofing on relay (reverse path filtering via rp_filter)
- ✅ Runaway relay process (systemd cgroup limits: memory, CPU, tasks)
- ✅ Unauthorized admin operations (role-based access control + HMAC chain)
- ✅ Root key exposure at rest (Argon2id + XChaCha20-Poly1305 vault)
- ✅ Root key exposure in memory (auto-reseal timeout, explicit zeroing)
- ✅ Invite code bruteforce (8-byte deposit ID, rate limiting)
- ✅ Permission escalation on invites (HMAC chain attenuation-only, cryptographic enforcement)
- ✅ Remote unseal bruteforce (iOS-style escalating lockout: 4 free, 1m/5m/15m/1h, permanent block, admin-only)

**Threats NOT Addressed** (out of scope):
- ❌ Relay compromise (relay can see metadata, not content)
- ❌ Peer key compromise (users must secure private keys)

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
- Go 1.26+
- libp2p v0.47.0 (networking)
- Private Kademlia DHT (`/shurli/kad/1.0.0` - isolated from IPFS Amino). Optional namespace isolation: `discovery.network: "my-crew"` produces `/shurli/my-crew/kad/1.0.0`, creating protocol-level separation between peer groups
- Noise protocol (encryption)
- QUIC transport (preferred - 3 RTTs vs 4 for TCP)
- AutoNAT v2 (per-address reachability testing)

**Why libp2p**: Shurli's networking foundation is the same stack used by Ethereum's consensus layer (Beacon Chain), Filecoin, and Polkadot - networks collectively securing hundreds of billions in value. When Ethereum chose a P2P stack for their most critical infrastructure, they picked libp2p. Improvements driven by these ecosystems (transport optimizations, Noise hardening, gossipsub refinements) flow back to the shared codebase. See the [FAQ comparisons](../faq/comparisons/#how-do-p2p-networking-stacks-compare) for detailed comparisons.

**Optional**:
- Ethereum (blockchain naming)
- IPFS (distributed storage)
- gomobile (iOS/Android)

---

**Last Updated**: 2026-02-28
**Architecture Version**: 3.4 (Phase 6: ACL, Relay Security, Client Invites)
