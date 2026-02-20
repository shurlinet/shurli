# Engineering Journal

This document captures the **why** behind every significant architecture decision in peer-up. Each entry follows a lightweight ADR (Architecture Decision Record) format: what problem we faced, what options we considered, what we chose, and what trade-offs we accepted.

New developers, contributors, and future-us should be able to read this and understand not just what the code does, but why it's shaped the way it is.

## Reading Guide

- **ADR-0XX**: Core architecture decisions made before the batch system
- **ADR-X0Y**: Batch-specific decisions (A=reliability, B=code quality, etc.)
- Each ADR is self-contained - read any entry independently
- Entries link to source files and commits where relevant

---

## Core Architecture Decisions

### ADR-001: Why Go

**Context**: peer-up needs to compile to a single static binary, run on Linux/macOS/Windows, and interface with libp2p (which has mature Go and Rust implementations).

**Alternatives considered**:
- **Rust** - Better memory safety guarantees, smaller binaries. Rejected because rust-libp2p has less mature circuit relay v2 support, and compile times would slow iteration during early development.
- **Python/Node.js** - Faster prototyping. Rejected because distribution requires runtime dependencies, violating the "single binary, zero dependencies" principle.

**Decision**: Go. Single binary compilation, excellent cross-platform support, mature libp2p ecosystem, and fast compilation for rapid iteration.

**Consequences**: Larger binary size (~28MB stripped) compared to Rust. Accepted because distribution simplicity outweighs binary size for a CLI tool. Binary size is actively monitored and optimized (see `binary-optimization` practices).

**Reference**: `go.mod`, `cmd/peerup/main.go`

---

### ADR-002: Why libp2p (Not Raw QUIC, Not WireGuard)

**Context**: peer-up needs NAT traversal, encrypted transport, peer discovery, and circuit relay. Building these from scratch would take years.

**Alternatives considered**:
- **Raw QUIC + custom protocol** - Full control, smaller dependency tree. Rejected because we'd need to implement hole punching, relay, DHT, and peer routing from scratch.
- **WireGuard** - Excellent performance, kernel-level. Rejected because it requires root/admin privileges, doesn't solve discovery, and doesn't provide circuit relay for CGNAT.
- **Noise protocol + custom transport** - Lighter than libp2p. Rejected because discovery and relay still need to be built.

**Decision**: libp2p v0.47.0. Provides QUIC+TCP+WebSocket transports, circuit relay v2, hole punching (DCUtR), Kademlia DHT, peer identity (Ed25519), and connection gating - all battle-tested.

**Consequences**: Large dependency tree (100+ transitive deps). The binary includes WebRTC and other transports we don't directly use. Accepted because reliability > binary size, and we actively track CVEs in dependencies.

**Reference**: `go.mod`, `pkg/p2pnet/network.go`

---

### ADR-003: Why Private DHT `/peerup/kad/1.0.0`

**Context**: Initially used the public IPFS Amino DHT (`/ipfs/kad/1.0.0`). This worked but mixed peerup peers into the global IPFS routing table, leaking peer discovery to the public network.

**Alternatives considered**:
- **Keep IPFS Amino DHT** - Zero config, large bootstrap. Rejected because (a) privacy: peerup peers are discoverable by anyone on IPFS, (b) pollution: peerup's rendezvous strings pollute the global DHT, (c) reliability: depends on IPFS bootstrap nodes staying healthy.
- **No DHT, relay-only** - Simpler. Rejected because DHT enables peer discovery without centralized infrastructure.
- **mDNS only** - Local network discovery. Rejected because it doesn't work across networks.

**Decision**: Private Kademlia DHT with protocol prefix `/peerup/kad/1.0.0` (constant `p2pnet.DHTProtocolPrefix`). Peerup peers only discover and route to other peerup peers.

**Consequences**: Smaller routing table (only peerup peers), no IPFS bootstrap dependency, but requires at least one known peer (relay) to bootstrap into the DHT.

**Reference**: `pkg/p2pnet/network.go:27` (`DHTProtocolPrefix` constant), commit `d1d4336`

---

### ADR-004: Why Circuit Relay v2

**Context**: Users behind CGNAT (5G, carrier-grade NAT, double NAT) cannot receive inbound connections. This is the core problem peer-up solves.

**Alternatives considered**:
- **UPnP/NAT-PMP only** - Works for simple NAT, fails on CGNAT. Rejected as sole strategy.
- **TURN server** - WebRTC-style relay. Rejected because it's a separate protocol ecosystem; libp2p's circuit relay v2 integrates naturally with the existing transport stack.
- **Circuit relay v1** - Deprecated by libp2p. Rejected.

**Decision**: Circuit relay v2 via `libp2p.EnableAutoRelayWithStaticRelays()`. The relay server makes reservations for peers, enabling them to be reached through the relay.

**Consequences**: All traffic flows through the relay when direct connection fails. Relay becomes a critical infrastructure component - must be hardened, monitored, and eventually made redundant (see Batch I: relay elimination research).

**Reference**: `pkg/p2pnet/network.go:140`, `cmd/relay-server/`

---

### ADR-005: Why Connection Gating via `authorized_keys`

**Context**: peer-up networks are private. Only explicitly authorized peers should connect. Needed an SSH-like trust model.

**Alternatives considered**:
- **Certificate authority** - More scalable, supports expiration. Rejected because it requires PKI infrastructure (CA key management, certificate issuance), which contradicts "no central authority."
- **Pre-shared keys** - Simpler than CA. Rejected because it doesn't provide per-peer identity.
- **No gating, encryption only** - Let any peer connect but encrypt traffic. Rejected because authorization is a core security requirement, not optional.

**Decision**: `authorized_keys` file containing one peer ID per line, checked by `auth.AuthorizedPeerGater` in `InterceptSecured()`. Only inbound connections are gated; outbound (to relay, DHT) are always allowed.

**Consequences**: Simple file-based auth that users already understand from SSH. Hot-reloadable at runtime. Scales to hundreds of peers. Does not support per-peer permissions (all-or-nothing access) - acceptable for current scope.

**Reference**: `internal/auth/gater.go`, `internal/auth/keys.go`

---

### ADR-006: Why Single Binary with Subcommands

**Context**: peer-up has many functions: daemon, ping, proxy, config management, relay server (separate binary). Needed a clean CLI structure.

**Alternatives considered**:
- **Separate binaries per function** - `peerup-daemon`, `peerup-ping`, etc. Rejected because it complicates distribution and PATH management.
- **cobra/urfave CLI framework** - Feature-rich. Rejected because they add dependency weight and complexity for what's essentially a dispatch table. Standard library `flag` + manual dispatch is lighter and fully sufficient.

**Decision**: Single `peerup` binary using `os.Args[1]` dispatch (`cmd/peerup/main.go`) with standard library `flag` for each subcommand. Relay server is a separate binary (`cmd/relay-server/`) because it has different deployment concerns (VPS vs local machine).

**Consequences**: The binary includes all functionality, so it's slightly larger than specialized binaries would be. Accepted because single-binary deployment is a core principle - `curl install | sh` drops one file.

**Reference**: `cmd/peerup/main.go`

---

### ADR-007: Why YAML Config

**Context**: peer-up needs configuration for identity, network, relay, discovery, security, services, and names. Needed a human-readable, editable format.

**Alternatives considered**:
- **TOML** - Good for flat config. Rejected because nested structures (services map, relay addresses) are more natural in YAML.
- **JSON** - Universal. Rejected because no comments, poor human editability for config files users need to hand-edit.
- **HCL** - HashiCorp's format. Rejected because it adds a dependency and is unfamiliar to most users.
- **Flags/env vars only** - Simpler. Rejected because the configuration is too complex for command-line flags alone.

**Decision**: YAML via `gopkg.in/yaml.v3`. Single config file with versioning (`version: 1`), duration strings (`10m`, `1h`), and relative path resolution.

**Consequences**: YAML is sensitive to indentation, which can confuse users. Mitigated by: (a) `peerup init` generates valid config automatically, (b) `peerup config validate` catches syntax errors, (c) config templates in `config_template.go` ensure consistency.

**Reference**: `internal/config/types.go`, `internal/config/loader.go`, `cmd/peerup/config_template.go`

---

### ADR-008: Why No External Dependencies Beyond libp2p

**Context**: Every dependency is an attack surface, a binary size cost, and a maintenance burden. peer-up is infrastructure software.

**Alternatives considered**: N/A - this is a constraint, not a choice between options.

**Decision**: The only direct dependencies are `go-libp2p`, `go-libp2p-kad-dht`, `go-multiaddr`, and `gopkg.in/yaml.v3`. Everything else (logging, config, auth, watchdog, QR codes) is implemented with Go standard library.

**Consequences**: More code to maintain (e.g., pure-Go sd_notify instead of using a systemd library), but complete control over behavior, smaller binary, and zero supply chain risk beyond the libp2p ecosystem.

**Reference**: `go.mod` (4 direct dependencies)

---

## Batch A: Reliability

### ADR-A01: TCP Timeout Strategy

**Context**: TCP proxy connections through circuit relay need appropriate timeouts. Too short = drops active SSH sessions. Too long = leaked connections consume relay resources.

**Alternatives considered**:
- **No explicit timeouts** (rely on libp2p defaults) - Rejected because libp2p's default stream timeouts are too short for interactive SSH sessions.
- **Configurable per-service timeouts** - Considered for future, but adds complexity for a problem that has reasonable defaults.

**Decision**: 10-second dial timeout for initial TCP connection (`net.DialTimeout("tcp", addr, 10*time.Second)`), 30-second context timeout for service connections. No idle timeout - SSH sessions can be long-lived; the half-close proxy (`BidirectionalProxy`) cleanly handles EOF propagation.

**Consequences**: Long-lived connections are supported, but a peer that disappears without closing the stream will hold resources until the relay's session duration limit (default 10 minutes) kicks in.

**Reference**: `pkg/p2pnet/proxy.go:66`

---

### ADR-A02: Retry with Exponential Backoff

**Context**: P2P connections through relays are inherently unreliable. A single dial failure shouldn't kill a proxy session.

**Alternatives considered**:
- **No retry** - Fail immediately. Rejected because relay connections often fail transiently.
- **Fixed delay retry** - Simpler but can cause thundering herd and doesn't adapt to load.

**Decision**: `DialWithRetry()` wraps any dial function with exponential backoff: 1s, 2s, 4s, ..., capped at 60s. Default 3 retries for daemon-created proxies.

**Consequences**: A failing connection takes up to ~7 seconds before giving up (1+2+4), which is acceptable for interactive use. The cap at 60s prevents runaway delays.

**Reference**: `pkg/p2pnet/proxy.go:130-155`

---

### ADR-A03: DHT in Proxy Path

**Context**: When the daemon receives a proxy connect request, the target peer might not be directly connected. Need to find and reach them first.

**Alternatives considered**:
- **Require pre-existing connection** - Simpler but fragile. Rejected because peers reconnect through DHT discovery, and the user shouldn't need to manually reconnect before proxying.
- **DNS-based discovery** - Rejected because it requires external infrastructure.

**Decision**: `ConnectToPeer()` in the daemon runtime performs DHT lookup + relay address injection before establishing the service stream. Every proxy and ping operation calls this first.

**Consequences**: First connection to a peer may be slow (DHT walk + relay reservation). Subsequent connections reuse the existing link. This is the correct behavior - find the peer, then talk to them.

**Reference**: `cmd/peerup/serve_common.go` (`ConnectToPeer` method), `internal/daemon/handlers.go:338`

---

### ADR-A04: In-Process Integration Tests

**Context**: Need integration tests that verify multi-peer P2P scenarios without requiring Docker, LAN access, or actual network infrastructure.

**Alternatives considered**:
- **Docker-only tests** - Realistic but slow and requires Docker installed. Added later as a complement (Batch G), not a replacement.
- **Mock libp2p hosts** - Too much mocking makes tests unreliable.

**Decision**: Create real libp2p hosts in the same process, connecting through an in-process relay. Tests in `pkg/p2pnet/` create 2-3 hosts that communicate through circuit relay within a single test binary.

**Consequences**: Tests are fast (~2s) and run anywhere (`go test ./...`). They don't test actual network conditions (latency, packet loss), which is why Docker integration tests were added later as a complement.

**Reference**: `pkg/p2pnet/network_test.go`, `pkg/p2pnet/service_test.go`

---

## Batch B: Code Quality

### ADR-B01: Proxy Deduplication

**Context**: `ParseRelayAddrs()` could receive duplicate relay addresses (same peer, different multiaddrs). Without dedup, libp2p would make redundant connections.

**Alternatives considered**:
- **Let libp2p handle it** - libp2p does some dedup, but passing duplicates to `EnableAutoRelayWithStaticRelays` wastes resources.

**Decision**: `ParseRelayAddrs()` deduplicates by peer ID and merges addresses for the same relay peer. If the same relay appears twice with different addresses, all addresses are collected under one `peer.AddrInfo`.

**Consequences**: Clean relay configuration. Users can list multiple addresses for the same relay (e.g., IPv4 and IPv6) without issues.

**Reference**: `pkg/p2pnet/network.go:280-309`

---

### ADR-B02: `log/slog` over zerolog/zap

**Context**: Needed structured logging throughout the project. Many Go projects use zerolog or zap for performance.

**Alternatives considered**:
- **zerolog** - Zero-allocation, fast. Rejected because it's another dependency, and peer-up doesn't produce enough log volume to need zero-allocation logging.
- **zap** - Uber's logger, excellent performance. Rejected for the same reason - adds dependency weight for no measurable benefit.
- **log/slog** - Go 1.21+ standard library structured logging. Built-in, no dependency, sufficient performance.

**Decision**: `log/slog` everywhere. `slog.Info`, `slog.Warn`, `slog.Error` with structured key-value pairs. Default handler writes to stderr.

**Consequences**: No external logging dependency. Standard library compatibility means any future handler (JSON, OpenTelemetry) can be swapped in without changing call sites. Slightly more verbose than zerolog's fluent API, but consistency with stdlib is worth it.

**Reference**: `cmd/peerup/main.go:20-22` (handler setup), used throughout all packages

---

### ADR-B03: Sentinel Errors

**Context**: Error handling was using `fmt.Errorf("service not found")` strings that callers couldn't programmatically check.

**Alternatives considered**:
- **String matching** - `strings.Contains(err.Error(), "not found")`. Rejected because it's fragile and breaks on message changes.
- **Custom error types** - `type NotFoundError struct { Name string }`. Considered for complex errors, but sentinel variables are simpler for the common case.

**Decision**: Package-level sentinel errors using `errors.New()`: `ErrServiceNotFound`, `ErrNameNotFound`, `ErrConfigNotFound`, `ErrNoArchive`, `ErrCommitConfirmedPending`, `ErrNoPending`, `ErrDaemonAlreadyRunning`, `ErrProxyNotFound`. Callers use `errors.Is()` to check.

**Consequences**: Clean error checking, wrappable with `fmt.Errorf("%w: ...", ErrFoo)`. Error messages in two packages: `pkg/p2pnet/errors.go` and `internal/config/errors.go`.

**Reference**: `pkg/p2pnet/errors.go`, `internal/config/errors.go`, `internal/daemon/errors.go`

---

### ADR-B04: Build Version Embedding

**Context**: Need to know exactly which version and commit is running, especially when debugging relay issues remotely.

**Alternatives considered**:
- **Version file** - Read from embedded file. Rejected because it's another artifact to maintain.
- **Git describe at runtime** - Call `git describe` at startup. Rejected because the binary might not be in a git repo.

**Decision**: `ldflags` injection at build time: `-X main.version=... -X main.commit=... -X main.buildDate=...`. Defaults to `dev` and `unknown` for development builds. Also sent as libp2p Identify UserAgent (`peerup/0.1.0`).

**Consequences**: Every binary is self-identifying. `peerup version` shows exact build info. The UserAgent appears in `peerup daemon peers --all`, making it easy to verify what version each peer runs.

**Reference**: `cmd/peerup/main.go:10-17`, `pkg/p2pnet/network.go:121-123`

---

## Batch C: Self-Healing

### ADR-C01: Config Archive/Rollback (Juniper-Inspired)

**Context**: A bad config change on a remote node (e.g., wrong relay address) could make it permanently unreachable. Need a recovery mechanism.

**Alternatives considered**:
- **Git-based config history** - Track config in a git repo. Rejected because it requires git installed and adds complexity.
- **Numbered backups** (config.1, config.2, ...) - More history but harder to manage cleanup.

**Decision**: Juniper-style last-known-good: `Archive()` copies current config to `.config.last-good.yaml` with atomic write (temp file + rename). `Rollback()` restores it. Single backup slot - simple, sufficient.

**Consequences**: Only one rollback level (no multi-step undo). Accepted because the common case is "my last change broke it, undo that one change." The archive is created before daemon start and before config apply.

**Reference**: `internal/config/archive.go`

---

### ADR-C02: Commit-Confirmed Pattern

**Context**: Changing config on a remote node is dangerous - if the new config prevents connectivity, you're locked out. Network engineers solve this with "commit confirmed" - apply the change, and if you don't confirm within N minutes, it auto-reverts.

**Alternatives considered**:
- **Manual rollback only** - User must SSH in (if they can) and run `peerup config rollback`. Rejected because if the config broke SSH access, there's no way in.
- **Two-phase commit** - More complex, requires coordination. Rejected as over-engineering for a single-node config change.

**Decision**: `peerup config apply <new> --confirm-timeout 5m` backs up current config, applies new config, starts a timer. If `peerup config confirm` isn't run within the timeout, the daemon reverts to the backup and restarts via `exitFunc(1)` (systemd restarts it with the restored config).

**Consequences**: Requires systemd (or equivalent) to restart on exit. The `exitFunc` is injectable for testing (`EnforceCommitConfirmed` takes `func(int)` instead of calling `os.Exit` directly).

**Reference**: `internal/config/confirm.go`, `cmd/peerup/cmd_config.go`

---

### ADR-C03: Watchdog + sd_notify (Pure Go)

**Context**: The daemon needs to report health to systemd and restart on failure. Most Go projects use `coreos/go-systemd` for sd_notify.

**Alternatives considered**:
- **`coreos/go-systemd`** - Mature library. Rejected because it's another dependency for 30 lines of socket code. Also pulls in dbus bindings we don't need.
- **No watchdog** - Let systemd's simple restart handle failures. Rejected because watchdog provides proactive health checking, not just crash recovery.

**Decision**: Pure Go sd_notify implementation in `internal/watchdog/watchdog.go`. Three functions: `Ready()` (READY=1), `Watchdog()` (WATCHDOG=1), `Stopping()` (STOPPING=1). All send datagrams to `$NOTIFY_SOCKET`. No-op when not running under systemd (macOS, manual launch).

The watchdog loop runs configurable health checks (default 30s interval) and only sends WATCHDOG=1 when all checks pass. The daemon adds a socket health check to verify the API is still accepting connections.

**Consequences**: Zero dependency for systemd integration. Works on both Linux (systemd) and macOS (launchd, where sd_notify is a no-op). The health check framework is extensible - Batch H will add libp2p connection health.

**Reference**: `internal/watchdog/watchdog.go`, `cmd/peerup/cmd_daemon.go:158-166`

---

## Batch D: libp2p Features

### ADR-D01: AutoNAT v2

**Context**: Peers need to know if they're behind NAT to decide whether to use relay. libp2p's AutoNAT v1 had accuracy issues.

**Alternatives considered**:
- **Manual reachability flag only** (`force_private_reachability: true`) - Works but requires users to know their NAT situation.
- **AutoNAT v1** - Older protocol, less accurate with CGNAT.

**Decision**: Enable AutoNAT v2 via `libp2p.EnableAutoNATv2()` alongside the manual flag. AutoNAT v2 uses a more reliable probing mechanism to determine reachability.

**Consequences**: Slightly more network chatter (AutoNAT probes), but more accurate reachability detection. The manual `force_private_reachability` flag remains as an override for cases where AutoNAT can't determine the correct state.

**Reference**: `pkg/p2pnet/network.go:118`

---

### ADR-D02: QUIC Preferred Transport Ordering

**Context**: libp2p supports multiple transports. The order they're specified affects which is tried first during connection establishment.

**Alternatives considered**:
- **TCP first** - Most compatible, works through all middleboxes. But slower connection establishment (4 RTTs for TCP+TLS+mux vs 3 for QUIC).
- **WebSocket first** - Anti-censorship benefit. But highest overhead.

**Decision**: Transport order is QUIC first, TCP second, WebSocket third. QUIC has native multiplexing (no yamux needed), faster handshake (1-RTT after initial), and better hole-punching characteristics. TCP is the universal fallback. WebSocket is for DPI/censorship evasion.

**Consequences**: Environments that block UDP (some corporate networks) will fall back to TCP automatically. The ordering is declarative in `New()` - first transport to succeed wins.

**Reference**: `pkg/p2pnet/network.go:113-117`

---

### ADR-D03: Identify UserAgent

**Context**: When multiple peers are connected, it's hard to tell which are peerup peers vs DHT neighbors, relay servers, or random libp2p nodes.

**Alternatives considered**:
- **Custom protocol handshake** - Send version info in a custom protocol. Rejected because libp2p's Identify protocol already does this.

**Decision**: Set `libp2p.UserAgent("peerup/" + version)` on every host. The daemon's peer list filters by UserAgent prefix (`peerup/` or `relay-server/`) by default, showing only network members. `--all` flag shows everything.

**Consequences**: Version info is visible to any connected peer (including non-peerup peers). Accepted because version strings are not sensitive - they aid debugging and interoperability.

**Reference**: `pkg/p2pnet/network.go:121-123`, `internal/daemon/handlers.go:78-80`

---

### ADR-D04: Smart Dialing

**Context**: libp2p tries all known addresses for a peer simultaneously. With relay addresses in the peerstore, it might waste time on direct addresses that will fail for CGNAT peers.

**Alternatives considered**:
- **Relay-only dialing** - Only use relay. Rejected because direct connections should be preferred when available.

**Decision**: Let libp2p's default smart dialing handle address selection, but ensure relay circuit addresses are always in the peerstore via `AddRelayAddressesForPeer()`. This gives the dialer both direct and relay options, and it picks the fastest.

**Consequences**: Relies on libp2p's dialing heuristics, which generally prefer direct connections. Future Batch I work will add explicit address ranking (direct IPv6 > direct IPv4 > peer relay > VPS relay).

**Reference**: `pkg/p2pnet/network.go:260-270`

---

## Batch E: New Capabilities

### ADR-E01: `/healthz` on Relay

**Context**: The relay server is a critical public-facing service. Monitoring systems need a health endpoint.

**Alternatives considered**:
- **TCP port check only** - Just verify the port is open. Rejected because it doesn't verify the relay is actually functional.
- **Full metrics endpoint** - Prometheus-style. Planned for Batch H, but `/healthz` needed now for basic monitoring.

**Decision**: HTTP `/healthz` endpoint on configurable address (default `127.0.0.1:9090`). Returns JSON with `status`, `uptime_seconds`, and `connected_peers`. Restricted to loopback by default - reverse proxy or SSH tunnel for remote access.

**Consequences**: Minimal information exposure (no peer IDs, no version, no protocol list in the health response - hardened in the post-phase audit). Loopback-only prevents information disclosure to the public internet.

**Reference**: `cmd/peerup/cmd_relay_serve.go`

---

### ADR-E02: Headless Invite/Join

**Context**: Docker containers, CI/CD pipelines, and scripts need to create/accept invites without interactive prompts or QR codes.

**Alternatives considered**:
- **Separate CLI for scripting** - A `peerup-cli` tool. Rejected because it fragments the tool.
- **Environment variables only** - `PEERUP_INVITE_CODE=xxx peerup join`. Supported alongside the flag.

**Decision**: `--non-interactive` flag on both `invite` and `join`. In non-interactive mode: invite prints bare code to stdout (progress to stderr), join reads code from positional arg or `PEERUP_INVITE_CODE` env var. No QR code, no prompts, no color.

**Consequences**: Docker integration tests can create and exchange invite codes programmatically. The flag reuses the same code paths as interactive mode - just different I/O routing.

**Reference**: `cmd/peerup/cmd_invite.go:34`, `cmd/peerup/cmd_join.go`

---

## Batch F: Daemon Mode

### ADR-F01: Unix Socket (Not TCP)

**Context**: The daemon needs a control API for CLI subcommands (`peerup daemon status`, `peerup daemon ping`, etc.). Need an IPC mechanism.

**Alternatives considered**:
- **TCP on localhost** - Universal, works on all platforms. Rejected because (a) any local process can connect (no filesystem permissions), (b) port conflicts with other services, (c) potentially exposed if firewall misconfigured.
- **Named pipes** - Windows-friendly. Rejected because they don't support HTTP natively and complicate the implementation.
- **gRPC** - Type-safe, bi-directional streaming. Rejected because it adds protobuf dependency, code generation, and binary size. HTTP+JSON is simpler and sufficient.

**Decision**: Unix domain socket at `~/.config/peerup/peerup.sock` with HTTP/1.1 over it. Socket created with `umask(0077)` to ensure `0700` permissions atomically (no TOCTOU race between `Listen()` and `Chmod()`). Stale socket detection: try connecting first, only remove if connection fails.

**Consequences**: Unix-only (no Windows support for now). Accepted because peer-up's target users are Linux/macOS. Socket permissions enforce that only the owning user can connect. The HTTP layer means standard tools (`curl --unix-socket`) work for debugging.

**Reference**: `internal/daemon/server.go:86-138`

---

### ADR-F02: Cookie Auth (Not mTLS)

**Context**: Even with socket permissions, the API needs authentication to prevent attacks via symlink races or debugger attachment.

**Alternatives considered**:
- **mTLS** - Strong mutual authentication. Rejected because it requires certificate management, key generation, and trust store configuration - too complex for a local IPC mechanism.
- **Token in socket filename** - Embed the token in the path. Rejected because path-based auth is fragile and leaks the token in `ps` output and logs.
- **No auth** (rely on socket permissions) - Rejected because defense-in-depth requires authentication even when filesystem permissions are correct.

**Decision**: 32-byte random hex cookie written to `~/.config/peerup/.daemon-cookie` with `0600` permissions. CLI reads the cookie and sends it as `Authorization: Bearer <token>`. Cookie is rotated every daemon restart. Written AFTER socket is secured (ordering prevents clients from reading cookie before socket is ready).

**Consequences**: Simple, fast, no crypto libraries needed. The cookie file is the single secret - protect it like an SSH private key. If compromised, restart the daemon to rotate.

**Reference**: `internal/daemon/server.go:88-116`, `internal/daemon/client.go`

---

### ADR-F03: RuntimeInfo Interface

**Context**: The daemon server needs access to the P2P network, config paths, version info, and connection methods. But the daemon package shouldn't import `cmd/peerup`.

**Alternatives considered**:
- **Pass individual fields** - `NewServer(network, configPath, authKeys, version, ...)`. Rejected because the parameter list would grow with every new feature.
- **Share a struct directly** - Import the runtime struct from cmd. Rejected because it creates a circular dependency between `internal/daemon` and `cmd/peerup`.

**Decision**: `daemon.RuntimeInfo` interface with methods: `Network()`, `ConfigFile()`, `AuthKeysPath()`, `GaterForHotReload()`, `Version()`, `StartTime()`, `PingProtocolID()`, `ConnectToPeer()`. The `serveRuntime` struct in `cmd/peerup/cmd_daemon.go` implements it.

**Consequences**: Clean dependency direction (daemon depends on interface, not concrete type). Easy to mock in tests (`mockRuntime`). Adding new runtime capabilities means adding methods to the interface - intentionally explicit.

**Reference**: `internal/daemon/server.go:23-32`, `cmd/peerup/cmd_daemon.go:23-28`

---

### ADR-F04: Hot-Reload `authorized_keys`

**Context**: Adding or removing peers via `peerup daemon auth add/remove` should take effect immediately without restarting the daemon.

**Alternatives considered**:
- **File watcher (fsnotify)** - Watch the file for changes. Rejected because it adds a dependency and doesn't help with API-triggered changes (where we already know when to reload).
- **Restart required** - Simpler but terrible UX. Rejected.

**Decision**: `GaterReloader` interface with `ReloadFromFile()` method. When the daemon API adds/removes a peer from the `authorized_keys` file, it immediately calls `ReloadFromFile()`, which re-reads the file and calls `gater.UpdateAuthorizedPeers()` with the new map. The gater uses `sync.RWMutex` for concurrent safety.

**Consequences**: Changes are atomic (read file, swap map under lock). No file watching needed. The gater's `authorizedPeers` map is replaced entirely - no incremental updates. This is fine because the authorized_keys file is small (typically <100 entries).

**Reference**: `cmd/peerup/cmd_daemon.go:37-51`, `internal/auth/gater.go:74-79`

---

## Batch G: Test Coverage

### ADR-G01: Coverage-Instrumented Docker Tests

**Context**: Docker integration tests verify real binaries in containers but didn't contribute to coverage metrics. Needed to merge Docker test coverage with unit test coverage.

**Alternatives considered**:
- **Separate coverage reports** - Track Docker and unit coverage independently. Rejected because it gives an incomplete picture.
- **Coverage at the Go test level only** - Skip Docker coverage. Rejected because the Docker tests exercise critical paths (relay, invite/join) that unit tests can't.

**Decision**: Build binaries with `-cover -covermode=atomic`, set `GOCOVERDIR` in containers, extract coverage data after tests, merge with unit test profiles using `go tool covdata`. Combined coverage reported in CI.

**Consequences**: Docker tests are slower (coverage instrumentation adds overhead), but we get accurate end-to-end coverage numbers. The merged profile reveals which code paths are only exercised by integration tests.

**Reference**: `test/docker/integration_test.go`, `.github/workflows/ci.yml`

---

### ADR-G02: Relay-Server Binary in Integration Tests

**Context**: Docker integration tests need to run the relay server. The relay server is built from `cmd/relay-server/`.

**Alternatives considered**:
- **Use a public relay** - Test against a real relay. Rejected because tests must be self-contained and reproducible.
- **Mock relay in-process** - Use libp2p relay transport directly. Rejected because we want to test the actual relay-server binary.

**Decision**: Build `relay-server` binary alongside `peerup` binary for Docker tests. The compose file starts a relay container, and node containers use it for circuit relay.

**Consequences**: Tests verify the actual deployment path (binary → container → relay → circuit). Takes longer to build but catches real integration issues.

**Reference**: `test/docker/compose.yaml`, `test/docker/Dockerfile`

---

### ADR-G03: Injectable `osExit` for Testability

**Context**: Several commands call `os.Exit()` on error. This kills the test process, making those code paths untestable.

**Alternatives considered**:
- **Panic + recover** - Use `panic` instead of `os.Exit` and recover in tests. Rejected because panics have different semantics (stack traces, deferred functions).
- **Return error codes** - Refactor all commands to return errors. Considered for future, but too large a refactor for a testing improvement.

**Decision**: Package-level `var osExit = os.Exit` that tests override with a function that records the exit code instead of terminating. Applied to `cmd/peerup/` (the main binary) and `cmd/relay-server/`.

**Consequences**: Minimal code change (one variable + one test helper), enables testing of all exit paths. The variable is package-level, so tests must be careful about parallel execution (each test restores the original `osExit`).

**Reference**: `cmd/peerup/run.go`, `cmd/peerup/run_test.go`

---

### ADR-G04: Post-Phase Audit Protocol

**Context**: After completing each batch, need a systematic review to catch issues before moving to the next phase. Ad-hoc reviews miss things.

**Alternatives considered**:
- **Ad-hoc review** - Review when something feels wrong. Rejected because it's inconsistent and misses systematic issues.
- **External audit** - Hire security auditors. Planned for later stages, but too expensive for every batch.

**Decision**: Mandatory 6-category audit after every phase: source code audit, bad code scan, bug hunting, QA testing, security audit, and relay hardening review. Each category has specific checklists. Findings are compiled into a report, and fixes require explicit approval before implementation.

The Batch G audit found 10 issues (CVE in pion/dtls, TOCTOU on Unix socket, cookie ordering, body size limits, CI SHA pinning, etc.) - all fixed in commit `83d02d3`.

**Consequences**: Adds time between batches, but catches real issues. The audit that found the pion/dtls nonce-reuse CVE justified the entire protocol - that vulnerability could have compromised encrypted relay traffic.

**Reference**: Audit findings tracked in project memory, fixes in commit `83d02d3`

---

## Batch H: Observability

### ADR-H01: Prometheus over OpenTelemetry

**Context**: Batch H adds metrics and audit logging. The original roadmap said "OpenTelemetry integration." Research revealed that libp2p v0.47.0 emits metrics natively via `prometheus/client_golang`, not OpenTelemetry.

**Alternatives considered**:
- **OpenTelemetry SDK** - Industry standard, supports traces + metrics + logs. Rejected because: +4MB binary size, 35% CPU overhead from span management on every stream, and libp2p already speaks Prometheus natively. Adding OTel would require a translation layer (Prometheus -> OTel) for zero benefit.
- **OpenTelemetry bridge only** (`go.opentelemetry.io/contrib/bridges/prometheus`) - Forward Prometheus metrics to OTel backends. Deferred to a future release when users request it. The bridge can be added later without changing any instrumentation code.
- **StatsD/Graphite** - Simpler push model. Rejected because Prometheus is already in our dependency tree as an indirect dep of libp2p.

**Decision**: Use `prometheus/client_golang` directly with an isolated `prometheus.Registry`. When metrics enabled, pass the registry to libp2p via `libp2p.PrometheusRegisterer(reg)` to get all built-in libp2p metrics for free. When disabled, call `libp2p.DisableMetrics()` for zero overhead.

**Consequences**: No distributed tracing (deferred). No OTLP export (can be added via bridge later). Binary size increase: ~1MB (28MB total). Any Prometheus-compatible tool (Grafana, Datadog, etc.) works out of the box.

**Reference**: `pkg/p2pnet/metrics.go`, `pkg/p2pnet/network.go`

---

### ADR-H02: Nil-Safe Observability Pattern

**Context**: Metrics and audit logging are opt-in. Every call site that records a metric or audit event needs to work correctly when observability is disabled.

**Alternatives considered**:
- **Feature flags with conditional compilation** - Build tags to exclude metrics code entirely. Rejected because it creates two binaries with different behavior, complicating testing.
- **No-op implementations** (interface-based) - Create `NullMetrics` / `NullAuditLogger` implementations. More idiomatic but adds interface overhead and boilerplate.
- **Global singleton with init check** - Single global metrics instance. Rejected to maintain testability (isolated registries per test).

**Decision**: Nil pointer checks at every call site. `*Metrics` and `*AuditLogger` are nil when disabled. All methods on `AuditLogger` check `if a == nil { return }`. Metrics call sites check `if metrics != nil` before recording. The `InstrumentHandler` middleware returns the handler unchanged when both are nil.

**Consequences**: Slightly verbose call sites (`if m != nil { m.Counter.Inc() }`). But: zero allocations when disabled, zero interface overhead, testable with isolated registries, and trivially verifiable (grep for nil checks).

**Reference**: `pkg/p2pnet/audit.go`, `internal/daemon/middleware.go`, `cmd/peerup/serve_common.go`

---

### ADR-H03: Auth Decision Callback (Avoiding Circular Imports)

**Context**: Auth decisions happen in `internal/auth/gater.go`. Metrics live in `pkg/p2pnet/metrics.go`. Go forbids circular imports: `internal/auth` cannot import `pkg/p2pnet`.

**Alternatives considered**:
- **Move gater to pkg/p2pnet** - Would work but breaks the `internal/` boundary. The gater is an internal implementation detail.
- **Shared interface package** - Create a `pkg/observe` package with metric recording interfaces. Adds complexity for a single callback.

**Decision**: Define `AuthDecisionFunc func(peerID, result string)` as a callback type in `internal/auth`. The gater calls this callback on every inbound decision. The wiring layer (`cmd/peerup/serve_common.go`) creates a closure that feeds both the Prometheus counter and the audit logger.

**Consequences**: Clean dependency graph. The auth package has zero knowledge of Prometheus or audit logging. The callback is nil-safe (checked before calling). Easy to add more observers later by extending the closure.

**Reference**: `internal/auth/gater.go:SetDecisionCallback`, `cmd/peerup/serve_common.go`
