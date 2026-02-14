# peer-up Development Roadmap

This document outlines the multi-phase evolution of peer-up from a simple NAT traversal tool to a comprehensive decentralized P2P network infrastructure.

## Philosophy

> **Build for 1-5 years. Make it adaptable. Don't predict 2074.**

- ✅ **Modular architecture** - Easy to add/swap components
- ✅ **Library-first** - Core logic reusable in other projects
- ✅ **Progressive enhancement** - Each phase adds value independently
- ✅ **No hard dependencies** - Works without optional features (naming, blockchain, etc.)
- ✅ **Local-first** - Offline-capable, no central services required
- ✅ **Self-sovereign** - No accounts, no telemetry, no vendor dependency
- ✅ **Automation-friendly** - Daemon API, headless onboarding, multi-language SDKs

---

## Phase 1: Configuration Infrastructure ✅ COMPLETE

**Goal**: Externalize all hardcoded values to YAML configuration files.

**Status**: ✅ Completed

**Deliverables**:
- [x] `internal/config` package for loading YAML configs
- [x] Sample configuration files in `configs/`
- [x] Updated `.gitignore` for config files
- [x] Refactored home-node/client-node/relay-server to use configs

**Key Files**:
- `internal/config/config.go` - Configuration structs
- `internal/config/loader.go` - YAML parsing
- `configs/*.sample.yaml` - Sample configurations

---

## Phase 2: Key-Based Authentication ✅ COMPLETE

**Goal**: Implement SSH-style authentication using ConnectionGater and authorized_keys files.

**Status**: ✅ Completed

**Deliverables**:
- [x] `internal/auth/gater.go` - ConnectionGater implementation (primary defense)
- [x] `internal/auth/authorized_keys.go` - Parser for authorized_keys
- [x] Integration into home-node and client-node
- [x] Protocol-level validation (defense-in-depth)
- [x] Relay server authentication (optional)

**Security Model**:
- **Layer 1**: ConnectionGater (network level - earliest rejection)
- **Layer 2**: Protocol handler validation (application level - secondary check)

---

## Phase 3: Enhanced Usability - keytool CLI ✅ COMPLETE

**Goal**: Create production-ready CLI tool for managing Ed25519 keypairs and authorized_keys.

**Status**: ✅ Completed

**Deliverables**:
- [x] `cmd/keytool` with 5 commands: generate, peerid, validate, authorize, revoke
- [x] Comment-preserving parser for authorize/revoke
- [x] Color-coded terminal output
- [x] Integration with existing auth system
- [x] Comprehensive documentation in README

**Commands**:
- `keytool generate` - Create new Ed25519 keypair
- `keytool peerid` - Extract peer ID from key file
- `keytool validate` - Check authorized_keys format
- `keytool authorize` - Add peer to authorized_keys
- `keytool revoke` - Remove peer from authorized_keys

---

## Phase 4: Service Exposure & Core Library

**Goal**: Transform peer-up into a reusable library and enable exposing local services through P2P connections.

### Phase 4A: Core Library & Service Registry ✅ COMPLETE

**Timeline**: 2-3 weeks
**Status**: ✅ Completed

**Deliverables**:
- [x] Create `pkg/p2pnet/` as importable package
  - [x] `network.go` - Core P2P network setup, relay helpers, name resolution
  - [x] `service.go` - Service registry and management
  - [x] `proxy.go` - Bidirectional TCP↔Stream proxy with half-close
  - [x] `naming.go` - Local name resolution (name → peer ID)
  - [x] `identity.go` - Ed25519 identity management
- [x] Extend config structs for service definitions
- [x] Update sample YAML configs with service examples
- [x] Refactor to `cmd/` layout with single Go module
- [x] Tested: SSH, XRDP, generic TCP proxy all working across LAN and 5G
- [x] **UX Streamlining**:
  - [x] Single binary — merged home-node into `peerup serve`
  - [x] Standard config path — auto-discovery (`./peerup.yaml` → `~/.config/peerup/config.yaml` → `/etc/peerup/config.yaml`)
  - [x] `peerup init` — interactive setup wizard (generates config, keys, authorized_keys)
  - [x] All commands support `--config <path>` flag
  - [x] Unified config type (one config format for all modes)

**Key Files**:
- `cmd/peerup/` - Single binary with subcommands: init, serve, proxy, ping
- `pkg/p2pnet/` - Reusable P2P networking library
- `internal/config/loader.go` - Config discovery, loading, path resolution

---

### Phase 4B: Frictionless Onboarding ✅ COMPLETE

**Timeline**: 1-2 weeks
**Status**: ✅ Completed

**Goal**: Eliminate manual key exchange and config editing. Get two machines connected in under 60 seconds.

**Rationale**: The current flow (generate key → share peer ID → edit authorized_keys → write config) has 4 friction points before anything works. This is the single biggest adoption barrier.

**Deliverables**:
- [x] `peerup invite` — generate short-lived invite code (encodes relay address + peer ID)
- [x] `peerup join <code>` — accept invite, exchange keys, auto-configure, connect
- [x] QR code output for `peerup invite` (scannable by mobile app later)
- [x] `peerup whoami` — show own peer ID and friendly name for sharing
- [x] `peerup auth add <peer-id> --comment "friend"` — append to authorized_keys
- [x] `peerup auth list` — show authorized peers
- [x] `peerup auth remove <peer-id>` — revoke access
- [x] `peerup relay add/list/remove` — manage relay addresses without editing YAML
- [x] Flexible relay address input — accept `IP:PORT` or bare `IP` (default port 7777) in addition to full multiaddr
- [x] QR code display in `peerup init` (peer ID) and `peerup invite` (invite code)
- [x] Relay connection info + QR code in `setup.sh --check`

**Security hardening** (done as part of 4B):
- [x] Sanitize authorized_keys comments (prevent newline injection)
- [x] Sanitize YAML names from remote peers (prevent config injection)
- [x] Limit invite/join stream reads to 512 bytes (prevent OOM DoS)
- [x] Validate multiaddr before writing to config YAML
- [x] Use `os.CreateTemp` for atomic writes (prevent symlink attacks)
- [x] Reject hostnames in relay input — only IP addresses accepted (no DNS resolution / SSRF)
- [x] Config files written with 0600 permissions

**Key Files**:
- `cmd/peerup/cmd_auth.go` — auth add/list/remove subcommands
- `cmd/peerup/cmd_whoami.go` — show peer ID
- `cmd/peerup/cmd_invite.go` — generate invite code + QR + P2P handshake
- `cmd/peerup/cmd_join.go` — decode invite, connect, auto-configure
- `cmd/peerup/cmd_relay.go` — relay add/list/remove subcommands
- `cmd/peerup/relay_input.go` — flexible relay address parsing (IP, IP:PORT, multiaddr)
- `internal/auth/manage.go` — shared AddPeer/RemovePeer/ListPeers with input sanitization
- `internal/invite/code.go` — binary invite code encoding/decoding (base32)

**User Experience**:
```bash
# Machine A (home server)
$ peerup invite --name home
=== Invite Code (expires in 10m0s) ===
AEQB-XJKZ-M4NP-...
[QR code displayed]
Waiting for peer to join...

# Machine B (laptop)
$ peerup join AEQB-XJKZ-M4NP-... --name laptop
=== Joined successfully! ===
Peer "home" authorized and added to names.
Try: peerup ping home

# Or use CLI auth commands directly:
$ peerup auth add 12D3KooW... --comment "friend"
$ peerup auth list
$ peerup auth remove 12D3KooW...

# Manage relay servers:
$ peerup relay add 203.0.113.50:7777 --peer-id 12D3KooW...
$ peerup relay list
$ peerup relay remove /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
```

**Security**:
- Invite codes are short-lived (configurable TTL, default 10 minutes)
- One-time use — code is invalidated after successful join
- Relay mediates the handshake but never sees private keys
- Both sides must be online simultaneously during join
- Stream reads capped at 512 bytes to prevent OOM attacks
- All user-facing inputs sanitized before writing to files

**Bug fixes (discovered during real-world testing)**:
- [x] Fixed invite code corruption when `--name` flag follows positional arg (`peerup join CODE --name laptop` — Go's `flag.Parse` stops at first non-flag, concatenating `--name` and `laptop` into the base32 code)
- [x] Added strict multihash length validation in invite decoder — Go's `base32.NoPadding` silently accepts trailing junk, so `Decode()` now re-encodes and compares multihash byte lengths
- [x] Fixed stream reset during invite join — inviter now flushes the OK response through the relay circuit before closing the stream
- [x] Added `reorderFlagsFirst()` to `runJoin()` so flags can appear after positional args (natural CLI usage)
- [x] First test file: `internal/invite/code_test.go` — round-trip, invalid input, and trailing junk rejection tests

---

### Phase 4C: Core Hardening & Security

**Timeline**: 3-4 weeks
**Status**: 📋 Planned

**Goal**: Harden every component for production reliability. Fix critical security gaps, add self-healing resilience, implement test coverage, and make the system recover from failures automatically — before wider distribution puts binaries in more hands.

**Rationale**: The relay is a public-facing VPS with no resource limits. There are near-zero tests. Connections don't survive relay restarts. A bad config change on the relay can lock you out permanently. These are unacceptable for a mission-critical system that people depend on for remote access. Industry practice for hardened infrastructure (Juniper, Cisco, Kubernetes, systemd) demands: validated configs, automatic recovery, resource isolation, and health monitoring.

**Deliverables**:

**Security (Critical)**:
- [ ] Relay resource limits — replace `WithInfiniteLimits()` with libp2p Resource Manager scopes: per-peer connection limits, per-peer bandwidth caps, memory and file descriptor budgets. DAG-based constraints at system, protocol, and peer levels.
- [ ] Auth hot-reload — file watcher or SIGHUP to reload `authorized_keys` without restart (revoke access immediately)
- [ ] Per-service access control — allow granting specific peers access to specific services only
- [ ] Rate limiting on incoming connections and streams — leverage go-libp2p's built-in per-IP rate limiting (1 connection per 5s default, 16-burst). Add per-peer stream throttling.
- [ ] QUIC source address verification — validate peer source IPs aren't spoofed, prevents relay from being used as DDoS reflector (built into quic-go v0.54.0+)
- [ ] OS-level rate limiting — iptables/nftables rules in `setup.sh` (SYN flood protection, `--connlimit-above` per source IP)
- [x] Config file permissions — write with 0600 (not 0644) *(done in Phase 4B)*
- [ ] Key file permission check on load — warn/refuse if not 0600
- [ ] Service name validation — reject special characters that could create ambiguous protocol IDs
- [x] Relay address validation in `peerup init` — parse multiaddr before writing config *(done in Phase 4B)*

**libp2p Upgrade (Critical)**:
- [ ] Upgrade main module go-libp2p to latest — gains AutoNAT v2, smart dialing, QUIC improvements, Resource Manager, per-IP rate limiting, source address verification
- [ ] Upgrade relay-server go-libp2p to match main module (currently v0.38.2, many versions behind — potential unpatched CVEs)
- [ ] Enable AutoNAT v2 — per-address reachability testing (know which specific addresses are publicly reachable; distinguish IPv4 vs IPv6 NAT state). Includes nonce-based dial verification and amplification attack prevention.
- [ ] Enable smart dialing — address ranking, QUIC prioritization, sequential dial with fast failover (reduces connection churn vs old parallel-dial-all approach)
- [ ] QUIC as preferred transport — 1 fewer RTT on connection setup (3 RTTs vs 4 for TCP), native multiplexing, better for hole punching

**Self-Healing & Resilience** (inspired by Juniper JunOS, Cisco IOS, Kubernetes, systemd, MikroTik):
- [ ] **Config validation command** — `peerup validate` / `relay-server validate` — parse config, check key file exists, verify relay address reachable, dry-run before applying. Catches errors before they cause downtime.
- [ ] **Config archive** — automatically save last 5 configs to `~/.config/peerup/config.d/` with timestamps on every change (init, join, relay add/remove, auth add/remove). Enables rollback.
- [ ] **Config rollback** — `peerup config rollback [N]` / `relay-server config rollback [N]` — restore Nth previous config from archive. Critical for recovering from bad changes.
- [ ] **Commit-confirmed pattern** (Juniper JunOS / Cisco IOS) — `relay-server apply --confirm-timeout 120` applies a config change and auto-reverts to previous config if not confirmed within N seconds. **Prevents permanent lockout on remote relay.** No P2P networking tool implements this.
- [ ] **systemd watchdog integration** — relay-server sends `sd_notify("WATCHDOG=1")` every 30s with internal health check (relay service alive, listening, at least 1 protocol registered). If health check fails, stop notifying → systemd auto-restarts. Add `WatchdogSec=60` to service file.
- [ ] **Health check HTTP endpoint** — relay exposes `/healthz` on a configurable port (default: disabled). Returns JSON: peer ID, uptime, connected peers count, reservation count, memory usage. Used by monitoring (Prometheus, UptimeKuma) and `setup.sh --check`.
- [ ] **`peerup status` command** — show connection state: relay connected/disconnected, peer online status, connection type (relay/direct), latency, uptime. Replaces guessing with observability.

**Automation & Integration**:
- [ ] **Daemon mode** — `peerup daemon` runs in background, exposes Unix socket API (`~/.config/peerup/peerup.sock`) for programmatic control. Enables scripting, automation, and third-party integration without spawning CLI subprocesses. JSON-based request/response. Operations: status, list-peers, list-services, connect, expose, authorize.
- [ ] **Headless onboarding** — `peerup join --non-interactive` reads invite code from stdin or `PEERUP_INVITE_CODE` env var, writes config, exits. No TTY prompts, no QR rendering. Essential for containerized and automated deployments (Docker, systemd, scripts).

**Reliability**:
- [ ] Reconnection with exponential backoff — recover from relay drops automatically (1s → 2s → 4s → ... → 60s cap)
- [ ] Connection warmup — pre-establish connection to target peer at `peerup proxy` startup (eliminates 5-15s per-session setup latency)
- [ ] Stream pooling — reuse streams instead of creating fresh ones per TCP connection (eliminates per-connection protocol negotiation)
- [ ] Persistent relay reservation — keep reservation alive with periodic refresh instead of re-reserving per connection. Reduces connection setup toward 1-3s (matching Iroh's performance).
- [ ] DHT bootstrap in proxy command — enable DCUtR hole-punching (currently proxy always relays). With hole punch success (~70%), many connections bypass relay entirely.
- [ ] Graceful shutdown — replace `os.Exit(0)` with proper cleanup, drain active connections
- [ ] Goroutine lifecycle — use `select` + `context.Done()` instead of bare `time.Sleep` loops
- [ ] TCP dial timeout — `net.DialTimeout(5s)` for local service connections
- [ ] Fix data race in bootstrap peer counter (`atomic.AddInt32`)

**Observability**:
- [ ] OpenTelemetry integration — instrument key paths with traces and metrics (invite/join flow, proxy setup, relay connection). Users pick their backend (Jaeger, Honeycomb, Prometheus, etc.)
- [ ] Metrics export — peer count, proxy throughput, relay latency, connection counts, stream utilization
- [ ] Audit logging — every peer auth decision logged with peer ID, action, timestamp, result (structured JSON for SIEM integration)
- [ ] Trace correlation IDs — propagate through relay path for debugging multi-hop connections

**Code Quality**:
- [ ] Unit test suite — auth (gater, authorized_keys), config (loader, validation), naming, proxy
- [ ] Structured logging — migrate to `log/slog` with levels and structured fields
- [ ] Sentinel errors — define `ErrServiceNotFound`, `ErrPeerNotAuthorized`, etc.
- [ ] Deduplicate proxy pattern — extract single `bidirectionalProxy()` function (currently copy-pasted 4x)
- [ ] Consolidate config loaders — unify `LoadHomeNodeConfig`/`LoadClientNodeConfig`
- [ ] Health/status endpoint — expose connection state, relay status, active streams

**Industry References**:
- **Juniper JunOS `commit confirmed`**: Apply config, auto-revert if not confirmed. Standard in network equipment for 20+ years. Prevents lockout on remote devices — identical problem to a remote relay server.
- **Cisco IOS `configure replace`**: Atomic config replacement with automatic rollback on failure.
- **MikroTik Safe Mode**: Track all changes since entering safe mode; revert everything if connection drops.
- **Kubernetes liveness/readiness probes**: Health endpoints that trigger automatic restart on failure.

**libp2p Specification References**:
- **Circuit Relay v2**: [Specification](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md) — reservation-based relay with configurable resource limits
- **DCUtR**: [Specification](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md) — Direct Connection Upgrade through Relay (hole punching coordination)
- **AutoNAT v2**: [Specification](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md) — per-address reachability testing with amplification prevention
- **Hole Punching Measurement**: [Study](https://arxiv.org/html/2510.27500v1) — 4.4M traversal attempts, 85K+ networks, 167 countries, ~70% success rate
- **systemd WatchdogSec**: Process heartbeat — if the process stops responding, systemd restarts it. Used by PostgreSQL, nginx, and other production services.
- **Caddy atomic reload**: Start new config alongside old; if new config fails, keep old. Zero-downtime config changes.

---

### Phase 4D: Plugin Architecture, SDK & First Plugins

**Timeline**: 3-4 weeks
**Status**: 📋 Planned

**Goal**: Make peer-up extensible by third parties — and prove the architecture works by shipping real plugins: file transfer, service templates, and Wake-on-LAN. The plugins ARE the SDK examples.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring — without forking. But empty interfaces are worthless: shipping real plugins alongside the architecture validates the design immediately and catches interface mistakes before third parties discover them. File sharing is the perfect first plugin — universal use case, builds on existing streams, proves the full `ServiceManager` lifecycle.

**Deliverables**:

**Core Interfaces** (new file: `pkg/p2pnet/interfaces.go`):
- [ ] `PeerNetwork` — interface for core network operations (expose, connect, resolve, close)
- [ ] `Resolver` — interface for name resolution (resolve, register). Enables chaining: local → DNS → DHT → blockchain
- [ ] `ServiceManager` — interface for service registration and dialing. Enables middleware.
- [ ] `Authorizer` — interface for authorization decisions. Enables pluggable auth (certs, tokens, database)
- [ ] `Logger` — interface for structured logging injection

**Extension Points**:
- [ ] Constructor injection — `Network.Config` accepts optional `Resolver`, `ConnectionGater`, `Logger`
- [ ] Event hook system — `OnEvent(handler)` for peer connected/disconnected, auth allow/deny, service registered, stream opened
- [ ] Stream middleware — `ServiceRegistry.Use(middleware)` for compression, bandwidth limiting, audit trails
- [ ] Protocol ID formatter — configurable protocol namespace and versioning

**Library Consolidation**:
- [ ] Extract DHT/relay bootstrap from CLI into `pkg/p2pnet/bootstrap.go`
- [ ] Centralize orchestration — new commands become ~20 lines instead of ~200
- [ ] Package-level documentation for `pkg/p2pnet/`

**Built-in Plugin: File Transfer** (proves `ServiceManager` + stream middleware):
- [ ] `peerup send <file> --to <peer>` — send a file to an authorized peer
- [ ] `peerup receive` — listen for incoming file transfers
- [ ] Auto-accept from authorized peers (configurable)
- [ ] Progress bar and transfer speed display (stream middleware)
- [ ] Resume interrupted transfers
- [ ] Directory transfer support (`peerup send ./folder --to laptop`)

**Built-in Plugin: Service Templates** (proves `ServiceManager` + health middleware):
- [ ] `peerup serve --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `peerup serve --vllm` shortcut (auto-detects vLLM on localhost:8000)
- [ ] Health check middleware — verify local service is reachable before exposing
- [ ] Streaming response verification (chunked transfer for LLM output)

**Built-in Plugin: Wake-on-LAN** (proves event hooks + new protocol):
- [ ] `peerup wake <peer>` — send magic packet before connecting
- [ ] Event hook: auto-wake peer on connection attempt (optional)

**Service Discovery Protocol**:
- [ ] New protocol `/peerup/discovery/1.0.0` — query a remote peer for their exposed services
- [ ] Response includes service names and optional tags (e.g., `gpu`, `storage`, `inference`)
- [ ] `peerup discover <peer>` CLI command — list services offered by a peer
- [ ] Service tags in config: `tags: [gpu, inference]` — categorize services for discovery

**Python SDK** (`peerup-sdk`):
- [ ] Thin wrapper around daemon Unix socket API
- [ ] `pip install peerup-sdk`
- [ ] Core operations: connect, expose_service, discover_services, proxy, status
- [ ] Async support (asyncio) for integration with event-driven applications
- [ ] Example: connect to a remote service in <10 lines of Python

**Headless Onboarding Enhancements**:
- [ ] `peerup invite --headless` — outputs invite data as JSON (no QR, no interactive wait)
- [ ] `peerup join --from-env` — reads config from environment variables
- [ ] Docker-friendly: `PEERUP_INVITE_CODE=xxx peerup join --non-interactive --name node-1`

**SDK Documentation** (the plugins above ARE the examples):
- [ ] `docs/SDK.md` — guide for building on `pkg/p2pnet`
- [ ] Example walkthrough: how file transfer was built as a plugin
- [ ] Example walkthrough: how service templates use health middleware
- [ ] Example: custom name resolver plugin
- [ ] Example: auth middleware (rate limiting, logging)

**Plugin Interface Preview**:
```go
// Third-party resolver
type DNSResolver struct { ... }
func (r *DNSResolver) Resolve(name string) (peer.ID, error) { ... }

// Third-party auth
type DatabaseAuthorizer struct { ... }
func (a *DatabaseAuthorizer) IsAuthorized(p peer.ID) bool { ... }

// Wire it up
net, _ := p2pnet.New(&p2pnet.Config{
    Resolver:        &DNSResolver{},
    ConnectionGater: &DatabaseAuthorizer{},
    Logger:          slog.Default(),
})

// React to events
net.OnEvent(func(e p2pnet.Event) {
    if e.Type == p2pnet.EventPeerConnected {
        metrics.PeerConnections.Inc()
    }
})
```

**File Transfer Usage**:
```bash
# Send a file
$ peerup send photo.jpg --to laptop
Sending photo.jpg (4.2 MB) to laptop...
████████████████████████████ 100% — 4.2 MB/s
✓ Transfer complete

# Send to multiple peers
$ peerup send presentation.pdf --to home --to phone

# Receive mode (optional — auto-accept if peer is authorized)
$ peerup receive --save-to ~/Downloads/
Waiting for transfers...
```

---

### Phase 4E: Distribution & Launch

**Timeline**: 1-2 weeks
**Status**: 📋 Planned

**Goal**: Make peer-up installable without a Go toolchain, and launch with compelling use-case content. Nobody will try later features if they can't `curl | sh` the binary.

**Rationale**: High impact, low effort. Prerequisite for wider adoption. GPU inference, game streaming, and IoT use cases already work — they just need documentation and a distribution channel.

**Deliverables**:

**Distribution**:
- [ ] Set up [GoReleaser](https://goreleaser.com/) config (`.goreleaser.yaml`)
- [ ] GitHub Actions workflow: on tag push, build binaries for Linux/macOS/Windows (amd64 + arm64)
- [ ] Publish to GitHub Releases with checksums
- [ ] Homebrew tap: `brew install satindergrewal/tap/peerup`
- [ ] One-line install script: `curl -sSL https://get.peerup.dev | sh`
- [ ] APT repository for Debian/Ubuntu
- [ ] AUR package for Arch Linux
- [ ] Docker image + `docker-compose.yml` for containerized deployment

**Use-Case Guides & Launch Content**:
- [ ] Guide: GPU inference — *"Access your home GPU from anywhere through Starlink CGNAT"*
- [ ] Guide: IoT/smart home remote access (Home Assistant, cameras behind CGNAT)
- [ ] Guide: Media server sharing (Jellyfin/Plex with friends via invite flow)
- [ ] Guide: Game server hosting (Minecraft, Valheim through CGNAT)
- [ ] Guide: Game/media streaming (Moonlight/Sunshine tunneling, latency characteristics)
- [ ] Latency/throughput benchmarks (relay vs direct via DCUtR)
- [ ] Multi-GPU / distributed inference documentation (exo, llama.cpp RPC)
- [ ] Blog post / demo: phone → relay → home 5090 → streaming LLM response

**Automation & Integration Guides**:
- [ ] Guide: *"Scripting & Automation with peer-up"* — daemon API, headless onboarding, Python SDK usage
- [ ] Guide: *"Containerized Deployments"* — Docker, env-based config, non-interactive join
- [ ] Docker compose examples for multi-service setups (GPU inference, media server, development environment)
- [ ] Python SDK published to PyPI alongside binary releases

**GPU Inference Config (already works today)**:
```yaml
services:
  ollama:
    enabled: true
    local_address: "localhost:11434"
```

```bash
# Home: peerup serve
# Remote: peerup proxy home ollama 11434
# Then: curl http://localhost:11434/api/generate -d '{"model":"llama3",...}'
```

**Result**: Zero-dependency install on any platform. Compelling use-case content drives adoption.

---

### Phase 4F: Desktop Gateway Daemon + Private DNS

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Create multi-mode gateway daemon for transparent service access, backed by a private DNS zone on the relay that is never exposed to the public internet.

**Rationale**: Infrastructure-level features that make peer-up transparent — services accessed via real domain names, no manual proxy commands. The DNS resolver uses the `Resolver` interface from Phase 4D.

**Deliverables**:

**Client-side Gateway**:
- [ ] `cmd/gateway/` - Gateway daemon with multiple modes
- [ ] **Mode 1**: SOCKS5 proxy (localhost:1080)
- [ ] **Mode 2**: Local DNS server (`.p2p` TLD)
- [ ] **Mode 3**: TUN/TAP virtual network interface (requires root)
- [ ] `/etc/hosts` integration for local name overrides
- [ ] Virtual IP assignment (10.64.0.0/16 range)
- [ ] Subnet routing — route entire LAN segments through tunnel (access printers, cameras, IoT without per-device install)
- [ ] Trusted network detection — auto-disable tunneling when already on home LAN

**Relay-side Private DNS** (pluggable `Resolver` backend from 4D):
- [ ] Lightweight DNS zone on the relay server (e.g., CoreDNS or custom)
- [ ] Exposed **only** via P2P protocol — never bound to public UDP/53
- [ ] Relay operator configures a real domain (e.g., `example.com`) pointing to the VPS IP
- [ ] Subdomains (`bob.example.com`, `home.example.com`) assigned on the relay, resolvable only within the P2P network
- [ ] Public DNS returns NXDOMAIN for all subdomains — they don't exist outside the network
- [ ] Gateway daemon queries relay's private DNS as upstream resolver

**Private DNS Architecture**:
```
Public Internet:
  example.com → 123.123.123.123 (relay VPS)    ← public, A record
  bob.example.com → NXDOMAIN                    ← not in public DNS
  home.example.com → NXDOMAIN                   ← not in public DNS

Inside P2P network (via relay's private DNS):
  bob.example.com → Bob's peer ID → Bob's services
  home.example.com → Home's peer ID → SSH, XRDP, Ollama
```

**How it works**:
1. Relay operator owns `example.com`, points it to the relay VPS
2. Relay runs a private DNS zone mapping `<name>.example.com` → peer ID
3. Peers register their friendly name with the relay on connect
4. Client gateway daemon queries the relay's DNS over a P2P stream (not raw UDP)
5. Gateway translates the response into a local DNS answer for the OS
6. Subdomains stay private — no DNS records ever created on public registrars

**Usage Examples**:
```bash
# Mode 1: SOCKS proxy (no root needed)
peerup-gateway --mode socks --port 1080
# Configure apps to use SOCKS proxy

# Mode 2: DNS server (queries relay's private DNS)
peerup-gateway --mode dns --port 53
# Resolves: home.example.com → virtual IP (via relay's private zone)

# Mode 3: Virtual network (requires root)
sudo peerup-gateway --mode tun --network 10.64.0.0/16
# Creates virtual interface, transparent routing
```

**Connection Examples**:
```bash
# After gateway is running:
ssh user@home.example.com        # resolved privately via relay
curl http://bob.example.com:8080 # never touches public DNS
mount -t cifs //home.example.com/media /mnt/media
```

---

### Phase 4G: Mobile Applications

**Timeline**: 3-4 weeks
**Status**: 📋 Planned

**Goal**: Native iOS and Android apps with VPN-like functionality.

**Rationale**: Phone → relay → home GPU is the dream demo. Mobile closes the loop on "access your stuff from anywhere."

**iOS Strategy**:
- **Primary**: NEPacketTunnelProvider (VPN mode)
  - Full TUN interface
  - Virtual network support
  - Frame as "self-hosted personal network" (like WireGuard)
- **Fallback**: SOCKS proxy app (if VPN rejected by Apple)
- **Apple Review Approach**: "Connect to your own devices via relay server"

**Android Strategy**:
- VPNService API (full feature parity)
- TUN interface
- No approval process limitations

**Deliverables**:
- [ ] iOS app with NEPacketTunnelProvider
- [ ] Android app with VPNService
- [ ] Mobile-optimized config UI
- [ ] QR code scanning for `peerup invite` codes
- [ ] Background connection maintenance
- [ ] Battery optimization
- [ ] Per-app SDK for third-party integration

**User Experience**:
```
iOS/Android App Config:
├─ Scan QR Code (from peerup invite)
├─ Or enter invite code: ABCX-7KMN-P2P3
└─ Connect Button

Once connected:
- SSH clients work: ssh user@home
- Browsers work: http://laptop:8080
- Native apps work: Plex connects to home.grewal:32400
- Chat with home LLM via Ollama API
```

---

### Phase 4H: Federation - Network Peering

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Enable relay-to-relay federation for cross-network communication.

**Rationale**: Only matters once you have multiple users with their own networks. Deferred until adoption features ship first.

**Deliverables**:
- [ ] Relay federation configuration
- [ ] Network-scoped naming (`host.network`)
- [ ] Cross-network routing protocol
- [ ] Trust/authorization between networks
- [ ] Route advertisement and discovery
- [ ] Multi-network client support — single client connected to multiple independent networks simultaneously

**Federation Config Example**:
```yaml
# relay-server.yaml
network:
  name: "grewal"

federation:
  enabled: true
  peers:
    - network_name: "alice"
      relay: "/ip4/45.67.89.12/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"

    - network_name: "bob"
      relay: "/dns4/bob-relay.com/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"

  routing:
    allow_transit: true  # Let alice → bob via your relay
```

**Usage**:
```bash
# From your network, access friend's services:
ssh user@laptop.alice
curl http://desktop.bob:8080
```

**Architecture**:
```
┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
│  Your Network   │      │  Alice Network  │      │   Bob Network   │
│    "grewal"     │◄────►│     "alice"     │◄────►│      "bob"      │
│                 │      │                 │      │                 │
│  ├─ laptop      │      │  ├─ desktop     │      │  ├─ server      │
│  └─ relay.      │      │  └─ relay.      │      │  └─ relay.      │
│     grewal      │      │     alice       │      │     bob         │
└─────────────────┘      └─────────────────┘      └─────────────────┘
```

---

### Phase 4I: Advanced Naming Systems (Optional)

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Pluggable naming architecture supporting multiple backends. Uses the `Resolver` interface from Phase 4D.

**Deliverables**:
- [ ] Built-in resolvers:
  - [ ] Local file (YAML/JSON)
  - [ ] DHT-based (federated)
  - [ ] mDNS (.local)
- [ ] Optional blockchain resolvers:
  - [ ] Ethereum smart contract
  - [ ] Bitcoin OP_RETURN
  - [ ] ENS (.eth domains)
- [ ] IPFS/Arweave archiving for redundancy

**Name Resolution Tiers**:

**Tier 1: Local Override** (Free, Instant)
```yaml
# ~/.peerup/names.yaml
names:
  home: "12D3KooWHome..."
  laptop: "12D3KooWLaptop..."
```

**Tier 2: Network-Scoped** (Free, Federated)
```
Format: <hostname>.<network>
Examples: laptop.grewal, desktop.alice
Resolution: Ask relay for peer ID
```

**Tier 3: Blockchain-Anchored** (Paid, Guaranteed)
```
Register on Ethereum: peerup register grewal --chain ethereum
Cost: ~$10-50 one-time
Format: <hostname>.grewal (globally unique)
```

**Tier 4: Existing Blockchain DNS** (Premium)
```
Use ENS: grewal.eth ($5-640/year)
Format: laptop.grewal.eth
```

---

## Positioning & Community

### Privacy Narrative — peer-up's Moat

peer-up is not a cheaper Tailscale. It's the **self-sovereign alternative** for people who care about owning their network.

| | **peer-up** | **Tailscale** |
|---|---|---|
| **Accounts** | None — no email, no OAuth | Required (Google, GitHub, etc.) |
| **Telemetry** | Zero — no data leaves your network | Coordination server sees device graph |
| **Control plane** | None — relay only forwards bytes | Centralized coordination server |
| **Key custody** | You generate, you store, you control | Keys managed via their control plane |
| **Source** | Fully open, self-hosted | Open source client, proprietary control plane |

> *"Tailscale for people who don't want to trust a company with their network topology."*

### Target Audiences (in order of receptiveness)

1. **r/selfhosted** — Already run services at home, hate port forwarding, value self-sovereignty
2. **Starlink/CGNAT users** — Actively searching for solutions to reach home machines
3. **AI/ML hobbyists** — Home GPU + remote access is exactly their problem
4. **Privacy-conscious developers** — Won't use Tailscale because of the coordination server

### Launch Strategy

1. **Hacker News post**: *"Show HN: peer-up — self-hosted P2P tunnels through Starlink CGNAT (no accounts, no vendor)"*
2. **r/selfhosted post**: Focus on SSH + XRDP + GPU inference through CGNAT
3. **Blog post**: *"Access your home GPU from anywhere through Starlink CGNAT"*
4. **Demo video**: Phone → relay → home 5090 → streaming LLM response
5. **Comparisons**: Honest peer-up vs Tailscale / Zerotier / Netbird posts

---

## Phase 5+: Ecosystem & Polish

**Timeline**: Ongoing
**Status**: 📋 Conceptual

**Potential Features**:
- [ ] Web-based dashboard for network management
- [ ] Protocol marketplace (community-contributed service templates)
- [ ] Performance monitoring and analytics (Prometheus metrics)
- [ ] Automatic relay failover/redundancy
- [ ] Bandwidth optimization and QoS per peer/service
- [ ] Multi-relay routing for redundancy
- [ ] Integration with existing VPN clients (OpenVPN, WireGuard)
- [ ] Desktop apps (macOS, Windows, Linux)
- [ ] Browser extension for `.p2p` domain resolution
- [ ] Community relay network
- [ ] IPv6 transport testing and documentation
- [ ] Split tunneling (route only specific traffic through tunnel)

**Protocol & Security Evolution**:
- [ ] MASQUE relay transport ([RFC 9298](https://www.ietf.org/rfc/rfc9298.html)) — HTTP/3 relay alternative to Circuit Relay v2. Looks like standard HTTPS to DPI, supports 0-RTT session resumption for instant reconnection. Could coexist with Circuit Relay v2 as user-selectable relay transport.
- [ ] Post-quantum cryptography — hybrid Noise + ML-KEM ([FIPS 203](https://csrc.nist.gov/pubs/fips/203/final)) handshakes for quantum-resistant key exchange. Implement when libp2p adopts PQC. Design cipher suite negotiation now (cryptographic agility).
- [ ] WebTransport transport — replace WebSocket anti-censorship layer with native QUIC-based WebTransport. Lower overhead, browser-compatible, native datagrams.
- [ ] Zero-RTT proxy connection resume — QUIC session tickets for instant reconnection after network switch (WiFi→cellular). No existing P2P tool provides this.
- [ ] Hardware-backed peer identity — store peer private keys in TPM 2.0 (Linux) or Secure Enclave (macOS/iOS). No existing P2P tool provides this.
- [ ] eBPF/XDP relay acceleration — kernel-bypass packet forwarding for high-throughput relay deployments. DDoS mitigation at millions of packets/sec.
- [ ] W3C DID-compatible identity — export peer IDs in [Decentralized Identifier](https://www.w3.org/TR/did-1.1/) format (`did:key`, `did:peer`) for interoperability with verifiable credential systems.
- [ ] Formal verification of invite/join protocol state machine — mathematically prove correctness of key exchange. Possible with TLA+ model or Kani (Rust).

**Performance & Language**:
- [ ] Selective Rust rewrite of hot paths — proxy loop, relay forwarding, SOCKS5 gateway via FFI. Zero GC, zero-copy, ~1.5x throughput improvement. Evaluate when performance metrics justify it.
- [ ] Rust QUIC library evaluation — [Iroh](https://github.com/n0-computer/iroh) (QUIC multipath, ~90% NAT traversal), [Quinn](https://github.com/quinn-rs/quinn) (pure Rust), [s2n-quic](https://github.com/aws/s2n-quic) (AWS, formally verified)
- [ ] Go GC tuning — profile at 100+ concurrent proxies, set GOGC, evaluate memory allocation patterns in proxy loop

---

## Timeline Summary

| Phase | Duration | Status |
|-------|----------|--------|
| Phase 1: Configuration | ✅ 1 week | Complete |
| Phase 2: Authentication | ✅ 2 weeks | Complete |
| Phase 3: keytool CLI | ✅ 1 week | Complete |
| Phase 4A: Core Library + UX | ✅ 2-3 weeks | Complete |
| Phase 4B: Frictionless Onboarding | ✅ 1-2 weeks | Complete |
| **Phase 4C: Core Hardening & Security** | 📋 3-4 weeks | **Next** |
| Phase 4D: Plugins, SDK & First Plugins | 📋 3-4 weeks | Planned |
| Phase 4E: Distribution & Launch | 📋 1-2 weeks | Planned |
| Phase 4F: Desktop Gateway + Private DNS | 📋 2-3 weeks | Planned |
| Phase 4G: Mobile Apps | 📋 3-4 weeks | Planned |
| Phase 4H: Federation | 📋 2-3 weeks | Planned |
| Phase 4I: Advanced Naming | 📋 2-3 weeks | Planned (Optional) |
| Phase 5+: Ecosystem | 📋 Ongoing | Conceptual |

**Total estimated time for Phase 4**: 18-26 weeks (5-6 months)

**Priority logic**: Onboarding first (remove friction) → harden the core (security, self-healing, reliability, tests) → make it extensible with real plugins (file sharing, service templates, WoL prove the architecture) → distribute with use-case content (GPU, IoT, gaming) → transparent access (gateway, DNS) → expand (mobile → federation → naming).

---

## Contributing

This roadmap is a living document. Phases may be reordered, combined, or adjusted based on:
- User feedback and demand
- Technical challenges discovered during implementation
- Emerging technologies (AI, quantum, blockchain alternatives)
- Community contributions

**Adaptability over perfection.** We build for the next 1-5 years, not 50.

---

## Success Metrics

**Phase 4A Success**:
- Library can be imported and used in external projects
- Services can be exposed and consumed via P2P
- At least 3 example services documented (SSH, HTTP, custom)

**Phase 4B Success**:
- Two machines connected via invite code in under 60 seconds
- Zero manual file editing required
- Invite codes expire and are single-use

**Phase 4C Success**:
- `go test -race ./...` passes with >60% coverage on auth, config, naming, proxy
- Relay has explicit resource limits (not infinite)
- `authorized_keys` changes take effect without restart
- Proxy command attempts DCUtR direct connection before falling back to relay
- Relay reconnection recovers automatically within 30 seconds
- `peerup validate` / `relay-server validate` catches bad configs before applying
- Config archive stores last 5 configs; `config rollback` restores any of them
- `relay-server apply --confirm-timeout` auto-reverts if not confirmed (no lockout)
- systemd watchdog restarts relay within 60s if health check fails
- `/healthz` endpoint returns relay status (monitorable by Prometheus/UptimeKuma)
- `peerup status` shows connection state, peer status, and latency
- `peerup daemon` runs in background; scripts can query status and list services via Unix socket
- `peerup join --non-interactive` works in Docker containers and CI/CD pipelines without TTY
- go-libp2p upgraded to latest in both main module and relay-server (no version gap)
- AutoNAT v2 enabled — node correctly identifies per-address reachability (IPv4 vs IPv6)
- Resource Manager replaces `WithInfiniteLimits()` — per-peer connection/bandwidth caps enforced
- Connection setup latency reduced from 5-15s toward 1-3s (persistent reservation + warmup)
- QUIC transport used by default (3 RTTs vs 4 for TCP)

**Phase 4D Success**:
- Third-party code can implement custom `Resolver`, `Authorizer`, and stream middleware
- Event hooks fire for peer connect/disconnect and auth decisions
- New CLI commands require <30 lines of orchestration (bootstrap consolidated)
- File transfer works between authorized peers (first plugin)
- `peerup serve --ollama` auto-detects and exposes Ollama (service template plugin)
- `peerup wake <peer>` sends magic packet (WoL plugin)
- Transfer speed saturates relay bandwidth; resume works after interruption
- SDK documentation published with working plugin examples
- `peerup discover <peer>` returns list of exposed services with tags
- Python SDK works: `pip install peerup-sdk` → connect to remote service in <10 lines
- `peerup invite --headless` outputs JSON; `peerup join --from-env` reads env vars

**Phase 4E Success**:
- GoReleaser builds binaries for 6 targets (linux/mac/windows × amd64/arm64)
- Homebrew tap works: `brew install satindergrewal/tap/peerup`
- Docker image available
- Install-to-running in under 30 seconds
- GPU inference use-case guide published
- Blog post / demo published
- Scripting & automation guide published
- Containerized deployment guide published with working Docker compose examples
- Python SDK available on PyPI

**Phase 4F Success**:
- Gateway daemon works in all 3 modes (SOCKS, DNS, TUN)
- Private DNS on relay resolves subdomains only within P2P network
- Public DNS queries for subdomains return NXDOMAIN (zero leakage)
- Native apps connect using real domain names (e.g., `home.example.com`)

**Phase 4G Success**:
- iOS app approved by Apple
- Android app published on Play Store
- QR code invite flow works mobile → desktop

**Phase 4H Success**:
- Two independent networks successfully federate
- Cross-network routing works transparently
- Trust model prevents unauthorized access

**Phase 4I Success**:
- At least 3 naming backends working (local, DHT, one optional)
- Plugin API documented and usable
- Migration path demonstrated when one backend fails

---

**Last Updated**: 2026-02-14
**Current Phase**: 4B Complete, 4C Next
**Phase count**: 4C–4I (7 phases, down from 9 — file sharing and service templates merged into plugin architecture)
**Next Milestone**: Core Hardening & Security (relay limits, self-healing, config validation, daemon mode, tests, reconnection)
