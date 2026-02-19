---
title: "Roadmap"
weight: 6
---
<!-- Auto-synced from docs/ROADMAP.md by sync-docs.sh — do not edit directly -->


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

## Phase 3: Enhanced Usability - keytool CLI ✅ COMPLETE (superseded)

**Goal**: Create production-ready CLI tool for managing Ed25519 keypairs and authorized_keys.

**Status**: ✅ Completed (keytool features merged into `peerup` subcommands in Phase 4C module consolidation; `cmd/keytool/` deleted)

**Deliverables**:
- [x] `cmd/keytool` with 5 commands: generate, peerid, validate, authorize, revoke
- [x] Comment-preserving parser for authorize/revoke
- [x] Color-coded terminal output
- [x] Integration with existing auth system
- [x] Comprehensive documentation in README

**Note**: All keytool functionality now lives in `peerup` subcommands: `peerup whoami` (peerid), `peerup auth add` (authorize), `peerup auth remove` (revoke), `peerup auth list`, `peerup auth validate` (validate). Key generation happens via `peerup init`.

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
  - [x] Single binary — merged home-node into `peerup daemon`
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

**Timeline**: 6-8 weeks (batched)
**Status**: ✅ Batches A–G Complete (6 items deferred to future batches)

**Goal**: Harden every component for production reliability. Fix critical security gaps, add self-healing resilience, implement test coverage, and make the system recover from failures automatically — before wider distribution puts binaries in more hands.

**Rationale**: The relay is a public-facing VPS with no resource limits. There are near-zero tests. Connections don't survive relay restarts. A bad config change on the relay can lock you out permanently. These are unacceptable for a mission-critical system that people depend on for remote access. Industry practice for hardened infrastructure (Juniper, Cisco, Kubernetes, systemd) demands: validated configs, automatic recovery, resource isolation, and health monitoring.

**Implementation Order** (batched for incremental value):
| Batch | Focus | Key Items |
|-------|-------|-----------|
| A | **Reliability** | Reconnection with backoff, TCP dial timeout, DHT in proxy, integration tests | ✅ DONE |
| B | **Code Quality** | Proxy dedup, structured logging (`log/slog`), sentinel errors, build version embedding | ✅ DONE |
| C | **Self-Healing** | Config validation/archive/rollback, commit-confirmed, systemd watchdog | ✅ DONE |
| D | **libp2p Features** | AutoNAT v2, smart dialing, QUIC preferred, version in Identify | ✅ DONE |
| E | **New Capabilities** | `peerup status`, `/healthz` endpoint, headless invite/join, UserAgent fix | ✅ DONE |
| F | **Daemon Mode** | `peerup daemon`, Unix socket API, ping/traceroute/resolve, dynamic proxies | ✅ DONE |
| G | **Test Coverage & Documentation** | 80.3% combined coverage, Docker integration tests, relay merge, engineering journal, website | ✅ DONE |
| H | **Observability** | OpenTelemetry, metrics, audit logging, trace IDs |

**Deliverables**:

**Security (Critical)**:
- [x] Relay resource limits — replace `WithInfiniteLimits()` with configurable `WithResources()` + `WithLimit()`. Defaults tuned for SSH/XRDP (10min sessions, 64MB data). Configurable via `resources:` section in relay-server.yaml.
- [x] Auth hot-reload — daemon API `POST /v1/auth` and `DELETE /v1/auth/{peer_id}` reload `authorized_keys` at runtime. `GaterReloader` interface updates ConnectionGater in-place. *(Batch F)*
- [ ] Per-service access control — allow granting specific peers access to specific services only. Critical when home node acts as LAN gateway (e.g., `local_address: "192.168.0.5:22"` exposes another machine's SSH). Config supports per-service `authorized_keys` override. CLI: `peerup service acl <name> add/remove <peer-id>`. Without this, every authorized peer can reach every service.
- [ ] Rate limiting on incoming connections and streams — leverage go-libp2p's built-in per-IP rate limiting (1 connection per 5s default, 16-burst). Add per-peer stream throttling.
- [ ] QUIC source address verification — validate peer source IPs aren't spoofed, prevents relay from being used as DDoS reflector (built into quic-go v0.54.0+)
- [ ] OS-level rate limiting — iptables/nftables rules in `setup.sh` (SYN flood protection, `--connlimit-above` per source IP)
- [x] Config file permissions — write with 0600 (not 0644) *(done in Phase 4B)*
- [x] Key file permission check on load — refuse to load keys with permissions wider than 0600 (actionable error message with `chmod` fix)
- [x] Service name validation — DNS-label format enforced (1-63 lowercase alphanumeric + hyphens), prevents protocol ID injection
- [x] Relay address validation in `peerup init` — parse multiaddr before writing config *(done in Phase 4B)*

**libp2p Upgrade (Critical)**:
- [x] Upgrade main module go-libp2p to latest — gains AutoNAT v2, smart dialing, QUIC improvements, Resource Manager, per-IP rate limiting, source address verification *(already on v0.47.0)*
- [x] Upgrade relay-server go-libp2p to match main module *(v0.38.2 → v0.47.0, done via `go work sync`)*
- [x] Enable AutoNAT v2 — per-address reachability testing (know which specific addresses are publicly reachable; distinguish IPv4 vs IPv6 NAT state). Includes nonce-based dial verification and amplification attack prevention. *(Batch D)*
- [x] Enable smart dialing — address ranking, QUIC prioritization, sequential dial with fast failover (reduces connection churn vs old parallel-dial-all approach) *(built into v0.47.0; transport ordering set QUIC-first)*
- [x] QUIC as preferred transport — 1 fewer RTT on connection setup (3 RTTs vs 4 for TCP), native multiplexing, better for hole punching *(Batch D — transport order: QUIC → TCP → WebSocket)*
- [x] Version in Identify — `libp2p.UserAgent("peerup/<version>")` and `libp2p.UserAgent("relay-server/<version>")` set on all hosts. Peers exchange version info via Identify protocol. Integration test verifies exchange. *(Batch D)*
- [x] Private DHT — migrated from IPFS Amino DHT (`/ipfs/kad/1.0.0`) to private peerup DHT (`/peerup/kad/1.0.0`). All 3 `dht.New()` calls in peerup + relay-server now use `dht.ProtocolPrefix("/peerup")`. Relay server runs DHT in server mode as the bootstrap peer. No more polluting the IPFS routing table or getting rejected by ConnectionGater. *(Post-Batch F)*

**Self-Healing & Resilience** (inspired by Juniper JunOS, Cisco IOS, Kubernetes, systemd, MikroTik):
- [x] **Config validation command** — `peerup config validate` parses config, checks key file exists, verifies relay address reachable, dry-run before applying. Also validates relay config. *(Batch C)*
- [x] **Config archive** — `internal/config/archive.go` auto-saves last-known-good config (`.config.last-good.yaml`) on successful serve startup. Atomic write with temp+rename. *(Batch C)*
- [x] **Config rollback** — `peerup config rollback` restores from last-known-good archive. *(Batch C)*
- [x] **Commit-confirmed pattern** (Juniper JunOS / Cisco IOS) — `peerup config apply <new-config> --confirm-timeout 5m` applies a config change and auto-reverts if not confirmed via `peerup config confirm`. **Prevents permanent lockout on remote relay.** `internal/config/confirm.go` implements `ApplyCommitConfirmed()` and `EnforceCommitConfirmed()`. *(Batch C)*
- [x] **systemd watchdog integration** — `internal/watchdog/watchdog.go` sends `sd_notify("WATCHDOG=1")` every 30s with health check. `Ready()`, `Stopping()`, `Watchdog()` messages. Integrated into `serve_common.go`. Extended with Unix socket health check in Batch F. *(Batch C)*
- [x] **Health check HTTP endpoint** — relay exposes `/healthz` on a configurable port (default: disabled, `127.0.0.1:9090`). Returns JSON: peer ID, version, uptime, connected peers count, protocol count. Used by monitoring (Prometheus, UptimeKuma). *(Batch E)*
- [x] **`peerup status` command** — show local config at a glance: version, peer ID, config path, relay addresses, authorized peers, services, names. No network required — instant. *(Batch E)*

**Auto-Upgrade Groundwork** (full implementation in Phase 4E):
- [x] **Build version embedding** — compile with `-ldflags "-X main.version=..."` so every binary knows its version. `peerup version` / `peerup --version` and `relay-server version` / `relay-server --version` print build version, commit hash, build date, and Go version. Version printed in relay-server startup banner. `setup.sh` injects version from git at build time.
- [x] **Version in libp2p Identify** — set `UserAgent` to `peerup/<version>` in libp2p host config. Peers learn each other's versions automatically on connect (no new protocol needed). *(Batch D — serve/proxy/ping; Batch E — invite/join)*
- [x] **Protocol versioning policy** — documented in engineering journal (ADR-D03). Wire protocols (`/peerup/proxy/1.0.0`) are backwards-compatible within major version. Version info exchanged via libp2p Identify UserAgent.

**Automation & Integration**:
- [x] **Daemon mode** — `peerup daemon` runs in foreground (systemd/launchd managed), exposes Unix socket API (`~/.config/peerup/peerup.sock`) with cookie-based auth. JSON + plain text responses. 14 endpoints: status, peers, services, auth (add/remove/hot-reload), ping, traceroute, resolve, connect/disconnect (dynamic proxies), expose/unexpose, shutdown. CLI client auto-reads cookie. *(Batch F)*
- [x] **Headless onboarding** — `peerup invite --non-interactive` skips QR, prints bare code to stdout, progress to stderr. `peerup join --non-interactive` reads invite code from CLI arg, `PEERUP_INVITE_CODE` env var, or stdin. No TTY prompts. Essential for containerized and automated deployments (Docker, systemd, scripts). *(Batch E)*

**Reliability**:
- [x] Reconnection with exponential backoff — `DialWithRetry()` wraps proxy dial with 3 retries (1s → 2s → 4s) to recover from transient relay drops
- [ ] Connection warmup — pre-establish connection to target peer at `peerup proxy` startup (eliminates 5-15s per-session setup latency)
- [ ] Stream pooling — reuse streams instead of creating fresh ones per TCP connection (eliminates per-connection protocol negotiation)
- [x] Persistent relay reservation — `serve_common.go` keeps reservation alive with periodic `circuitv2client.Reserve()` at `cfg.Relay.ReservationInterval`. Runs as background goroutine during daemon lifetime.
- [x] DHT bootstrap in proxy command — Kademlia DHT (client mode) bootstrapped at proxy startup. Async `FindPeer()` discovers target's direct addresses, enabling DCUtR hole-punching (~70% bypass relay entirely).
- [x] Graceful shutdown — replace `os.Exit(0)` with proper cleanup, context cancellation stops background goroutines
- [x] Goroutine lifecycle — use `time.Ticker` + `select ctx.Done()` instead of bare `time.Sleep` loops
- [x] TCP dial timeout — `net.DialTimeout("tcp", addr, 10s)` for local service connections (serve side and proxy side). `ConnectToService()` uses 30s context timeout for P2P stream dial.
- [x] Fix data race in bootstrap peer counter (`atomic.Int32`)

**Observability** (Batch H):
- [ ] OpenTelemetry integration — instrument key paths with traces and metrics (invite/join flow, proxy setup, relay connection). Users pick their backend (Jaeger, Honeycomb, Prometheus, etc.)
- [ ] Metrics export — peer count, proxy throughput, relay latency, connection counts, stream utilization
- [ ] Connection quality scoring — per-path metrics (latency, jitter, throughput, stability) for direct, relayed, and multi-relay paths. Exposed via `peerup status --health` and daemon API
- [ ] Hole-punch success tracking — record success/failure/elapsed for every DCUtR attempt. Aggregate into `peerup status` summary (success rate, average RTT, failure reasons). Feeds connection quality scoring
- [ ] Audit logging — every peer auth decision logged with peer ID, action, timestamp, result (structured JSON for SIEM integration)
- [ ] Trace correlation IDs — propagate through relay path for debugging multi-hop connections

**Relay Decentralization** (future — after Batch H observability provides the data needed):
- [ ] `require_auth` relay service — enable Circuit Relay v2 service on home nodes with `require_auth: true` (only authorized peers can reserve). Config: `relay_service.enabled`, `relay_service.require_auth`, `relay_service.resources.*`. ConnectionGater enforces auth before relay protocol runs
- [ ] DHT-based relay discovery — authorized relays advertise on DHT under well-known CID. NATted nodes discover peer relays via AutoRelay. No central endpoint
- [ ] Multi-relay failover — try multiple known relays in order; health-aware selection based on connection quality scores from observability data
- [ ] Bootstrap decentralization — hardcoded seed peers in binary (ultimate fallback) → DNS seeds at `peerup.dev` → DHT peer exchange → fully self-sustaining. Same pattern as Bitcoin
- [ ] **End goal**: Relay VPS becomes **obsolete** — not just optional. Every publicly-reachable peer-up node relays for its authorized peers. No special nodes, no central coordination

**Module Consolidation** (completed — single Go module):
- [x] Merged three Go modules (main, relay-server, cmd/keytool) into a single `go.mod`
- [x] Deleted `go.work` — no workspace needed with one module
- [x] Moved relay-server source from `relay-server/main.go` to `cmd/relay-server/main.go`; `relay-server/` is now a deployment directory (setup.sh, configs, systemd)
- [x] Extracted `internal/identity/` package (from `pkg/p2pnet/identity.go`) — `CheckKeyFilePermissions()`, `LoadOrCreateIdentity()`, `PeerIDFromKeyFile()` shared by peerup and relay-server
- [x] Extracted `internal/validate/` package — `ServiceName()` for DNS-label validation of service names
- [x] Deleted `cmd/keytool/` entirely — all features exist in `peerup` subcommands (`whoami`, `auth add/list/remove/validate`)
- [x] Added `peerup auth validate` (ported from keytool validate)
- [x] CI simplified to `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` from project root

**Pre-Refactoring Foundation** (completed before main 4C work):
- [x] GitHub Actions CI — build, vet, and test on every push to `main` and `dev/next-iteration`
- [x] Config version field — `version: 1` in all configs; loader defaults missing version to 1, rejects future versions. Enables safe schema migration.
- [x] Unit tests for config package — loader, validation, path resolution, version handling, relay config
- [x] Unit tests for auth package — gater (inbound/outbound/update), authorized_keys (load/parse/comments), manage (add/remove/list/duplicate/sanitize)
- [x] Integration tests — in-process libp2p hosts verify real stream connectivity, half-close semantics, P2P-to-TCP proxy, and `DialWithRetry` behavior (6 tests in `pkg/p2pnet/integration_test.go`)

**Batch A — Reliability** (completed):
- [x] `DialWithRetry()` — exponential backoff retry (1s → 2s → 4s) for proxy dial
- [x] TCP dial timeout — 10s for local service, 30s context for P2P stream
- [x] DHT bootstrap in proxy command — Kademlia DHT (client mode) for direct peer discovery
- [x] `[DIRECT]`/`[RELAYED]` connection path indicators in logs (checks `RemoteMultiaddr()` for `/p2p-circuit`)
- [x] DCUtR hole-punch event tracer — logs hole punch STARTED/SUCCEEDED/FAILED and direct dial events

**Batch B — Code Quality** (completed):
- [x] Deduplicated bidirectional proxy — `BidirectionalProxy()` + `HalfCloseConn` interface (was 4 copies, now 1)
- [x] Sentinel errors — 8 sentinel errors across 4 packages, all using `%w` wrapping for `errors.Is()`
- [x] Build version embedding — `peerup version`, `relay-server version`, ldflags injection in setup.sh
- [x] Structured logging with `log/slog` — library code migrated (~20 call sites), CLI output unchanged

**Batch E — New Capabilities** (completed):
- [x] `peerup status` — local-only info command (version, peer ID, config, relays, authorized peers, services, names)
- [x] `/healthz` HTTP endpoint on relay-server — JSON health check for monitoring (disabled by default, binds `127.0.0.1:9090`)
- [x] `peerup invite --non-interactive` — bare invite code to stdout, progress to stderr, skip QR
- [x] `peerup join --non-interactive` — reads code from CLI arg, `PEERUP_INVITE_CODE` env var, or stdin
- [x] UserAgent fix — added `peerup/<version>` UserAgent to invite/join hosts (was missing from Batch D)

**Batch F — Daemon Mode** (completed):
- [x] `peerup daemon` — long-running P2P host with Unix socket HTTP API
- [x] Cookie-based authentication (32-byte random hex, `0600` permissions, rotated per restart)
- [x] 14 API endpoints with JSON + plain text format negotiation (`?format=text` / `Accept: text/plain`)
- [x] `serve_common.go` — extracted shared P2P runtime (zero duplication between serve and daemon)
- [x] Auth hot-reload — `POST /v1/auth` and `DELETE /v1/auth/{peer_id}` take effect immediately
- [x] Dynamic proxy management — create/destroy TCP proxies at runtime via API
- [x] P2P ping — standalone (`peerup ping`) + daemon API, continuous/single-shot, stats summary
- [x] P2P traceroute — standalone (`peerup traceroute`) + daemon API, DIRECT vs RELAYED path analysis
- [x] P2P resolve — standalone (`peerup resolve`) + daemon API, name → peer ID
- [x] Stale socket detection (dial test, no PID files)
- [x] Daemon client library (`internal/daemon/client.go`) with auto cookie reading
- [x] CLI client commands: `peerup daemon status/stop/ping/services/peers/connect/disconnect`
- [x] Service files: `deploy/peerup-daemon.service` (systemd) + `deploy/com.peerup.daemon.plist` (launchd)
- [x] Watchdog extended with Unix socket health check
- [x] Tests: auth middleware, handlers, lifecycle, stale socket, integration, ping stats
- [x] Documentation: `docs/DAEMON-API.md` (full API reference), `docs/NETWORK-TOOLS.md` (diagnostic commands)

**Batch G — Test Coverage & Documentation** (completed):

Combined coverage: **80.3%** (unit + Docker integration). Relay-server binary merged into peerup (commit 5d167b3).

Priority areas (all hit or exceeded targets):
- [x] **cmd/peerup** (4% → 80%+) — 96 test functions covering CLI commands, flag handling, config template, daemon lifecycle, error paths. Relay serve commands merged and tested. *(relay-server binary merged into peerup)*
- [x] **internal/daemon** (12% → 70%+) — all 14 API handlers tested (status, ping, traceroute, resolve, connect/disconnect, auth CRUD, services, shutdown), format negotiation, cookie auth, proxy lifecycle, client library
- [x] **pkg/p2pnet** (23% → 84%) — naming, service registry, proxy half-close, relay address parsing, identity, ping, traceroute
- [x] **internal/config** (48% → 75%+) — archive/rollback, commit-confirmed timer, loader edge cases, benchmark tests
- [x] **internal/auth** (50% → 75%+) — hot-reload, concurrent access, malformed input, gater tests
- [x] **Docker integration tests** — `test/docker/integration_test.go` with relay container, invite/join, ping through circuit. Coverage-instrumented via `test/docker/coverage.sh`
- [x] **CI coverage reporting** — `.github/workflows/pages.yaml` merges unit + Docker coverage via `go tool covdata merge`, reports combined coverage
- [x] **Engineering journal** ([`docs/ENGINEERING-JOURNAL.md`]../engineering-journal/) — 28 architecture decision records (ADRs) covering core architecture (8) and all batches A-G. Not a changelog — documents *why* every design choice was made, what alternatives were considered, and what trade-offs were accepted.
- [x] **Website** — Hugo + Hextra site scaffolded with landing page, 7 retroactive blog posts (Batches A-G), sync-docs.sh for auto-transformation, GitHub Actions CI/CD for GitHub Pages deployment
- [x] **Security hardening** — post-audit fixes across 10 files (commit 83d02d3). CVE-2026-26014 resolved (pion/dtls v3.1.2). CI Actions pinned to commit SHAs.

**Service CLI** (completed — completes the CLI config management pattern):
- [x] `peerup service add <name> <address>` — add a service (enabled by default), optional `--protocol` flag
- [x] `peerup service remove <name>` — remove a service from config
- [x] `peerup service enable <name>` — enable a disabled service
- [x] `peerup service disable <name>` — disable a service without removing it
- [x] `peerup service list` — list configured services with status
- [x] All config sections (auth, relay, service) now manageable via CLI — no YAML editing required
- [x] `local_address` can point to any reachable host (e.g., `192.168.0.5:22`) — home node acts as LAN gateway

**Code Quality**:
- [x] Expand test coverage — 80.3% combined coverage. Naming, proxy, invite edge cases, relay input parsing all tested. *(Batch G)*
- [x] Structured logging — migrated library code (`pkg/p2pnet/`, `internal/auth/`) to `log/slog` with structured key-value fields and log levels (Info/Warn/Error). CLI commands remain `fmt.Println` for user output. *(Batch B)*
- [x] Sentinel errors — defined `ErrServiceAlreadyRegistered`, `ErrNameNotFound`, `ErrPeerAlreadyAuthorized`, `ErrPeerNotFound`, `ErrInvalidPeerID`, `ErrConfigNotFound`, `ErrConfigVersionTooNew`, `ErrInvalidServiceName` across 4 error files. All wrapped with `fmt.Errorf("%w: ...")` for `errors.Is()` support. *(Batch B)*
- [x] Deduplicate proxy pattern — extracted `BidirectionalProxy()` with `HalfCloseConn` interface and `tcpHalfCloser` adapter (was copy-pasted 4x, now single ~30-line function). *(Batch B)*
- [x] Consolidate config loaders — unified `LoadNodeConfig()` delegates to `LoadHomeNodeConfig()`, `LoadClientNodeConfig()` also delegates. Single `NodeConfig` struct.
- [x] Health/status endpoint — `/healthz` on relay (Batch E), `peerup status` (Batch E), daemon API `/v1/status` (Batch F) expose connection state, relay status, active streams.

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
- [ ] `peerup daemon --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `peerup daemon --vllm` shortcut (auto-detects vLLM on localhost:8000)
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
- [ ] Thin wrapper around daemon Unix socket API (14 endpoints already implemented in Batch F)
- [ ] `pip install peerup-sdk`
- [ ] Core operations: connect, expose_service, discover_services, proxy, status
- [ ] Async support (asyncio) for integration with event-driven applications
- [ ] Example: connect to a remote service in <10 lines of Python

**Headless Onboarding Enhancements**:
- [x] `peerup invite --non-interactive` — bare code to stdout, no QR, progress to stderr *(Phase 4C Batch E)*
- [x] `peerup join --non-interactive` — reads code from CLI arg, `PEERUP_INVITE_CODE` env var, or stdin *(Phase 4C Batch E)*
- [x] Docker-friendly: `PEERUP_INVITE_CODE=xxx peerup join --non-interactive --name node-1` *(Phase 4C Batch E)*

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

**Goal**: Make peer-up installable without a Go toolchain, launch with compelling use-case content, and establish `peerup.dev` as the stable distribution anchor — independent of any single hosting provider.

**Rationale**: High impact, low effort. Prerequisite for wider adoption. GPU inference, game streaming, and IoT use cases already work — they just need documentation and a distribution channel. The domain `peerup.dev` is the one thing no third party can take away — every user-facing URL routes through it, never hardcoded to `github.com` or any other host.

**Deliverables**:

**Website & Documentation (peerup.dev)**:
- [x] Static documentation site built with [Hugo](https://gohugo.io/) + [Hextra](https://imfing.github.io/hextra/) theme — Go-based SSG, fast builds, matches the project toolchain, built-in search and dark mode
- [x] Automated docs sync (`website/sync-docs.sh`) — transforms `docs/*.md` into Hugo-ready content with front matter and link rewriting
- [x] Elegant landing page with hero section and feature grid
- [x] Seven retroactive blog posts for Batches A-G (outcomes-focused)
- [x] GitHub Actions CI/CD — build Hugo site and deploy to GitHub Pages on every push to `main`
- [ ] GitHub Pages hosting with custom domain (`peerup.dev`) — DNS pointing pending
- [ ] DNS managed on Cloudflare — CNAME `peerup.dev` → GitHub Pages, CNAME `get.peerup.dev` → serves install script
- [x] Landing page — hero section, feature grid (NAT traversal, single binary, SSH trust, 60s pairing, TCP proxy, self-healing) *(Batch G)*
- [x] Existing docs rendered as site pages — `sync-docs.sh` transforms ARCHITECTURE, FAQ, TESTING, ROADMAP, DAEMON-API, NETWORK-TOOLS, ENGINEERING-JOURNAL into Hugo-ready content *(Batch G)*
- [ ] `pkg/p2pnet` library reference (godoc-style or hand-written guides)
- [ ] Use-case guides integrated into the site (GPU inference, IoT, game servers — see Launch Content below)
- [ ] Install page with platform-specific instructions (curl, brew, apt, Docker, source)
- [x] Blog section — 7 retroactive blog posts for Batches A-G (outcomes-focused) *(Batch G)*

**AI-Agent Discoverability ([llms.txt](https://llmstxt.org/) spec)**:
- [ ] `/llms.txt` — markdown index of the project: name, summary, links to detailed doc pages. ~200 tokens for an AI agent to understand the entire project. Hugo build step generates this from site content.
- [ ] `/llms-full.txt` — all site content concatenated into a single markdown file. One URL paste gives an AI agent full project context without HTML/CSS/JS token overhead.
- [ ] `.md` variants of every page — any page URL + `.md` suffix returns clean markdown (Hugo already has the source, just serve it as a static file alongside the HTML)
- [ ] Adopted by 600+ sites including Anthropic, Cloudflare, Stripe, Cursor, Hugging Face
- [ ] **WebMCP** ([Google + Microsoft, W3C](https://developer.chrome.com/blog/webmcp-epp)) — watch for future relevance. Protocol for AI agents to *interact* with websites via structured tool contracts (Declarative API for HTML forms, Imperative API for JS). Early preview in Chrome 146 Canary (Feb 2026). Not immediately relevant for a docs site, but valuable if peerup.dev adds interactive features (e.g., invite code generator, service discovery dashboard)

**Release Manifest & Upgrade Endpoint**:
- [ ] CI generates static `releases/latest.json` on every tagged release — deployed as part of the Hugo site
- [ ] Manifest contains version, commit, date, checksums, and per-platform download URLs for all mirrors:
  ```json
  {
    "version": "1.2.0",
    "commit": "abc1234",
    "date": "2026-03-15",
    "binaries": {
      "linux-amd64": {
        "github": "https://github.com/.../peerup-linux-amd64.tar.gz",
        "gitlab": "https://gitlab.com/.../peerup-linux-amd64.tar.gz",
        "ipfs": "bafybeiabc123...",
        "sha256": "..."
      }
    }
  }
  ```
- [ ] `peerup upgrade` fetches `peerup.dev/releases/latest.json` (not GitHub API directly)
- [ ] Install script fetches the same manifest — one source of truth for all consumers
- [ ] Fallback order in binary and install script: GitHub → GitLab → IPFS gateway

**Distribution Resilience** (gradual rollout):

The domain (`peerup.dev`) is the anchor. DNS is on Cloudflare under our control. Every user-facing URL goes through the domain, never directly to a third-party host. If any host disappears, one DNS record change restores service.

| Layer | GitHub (primary) | GitLab (mirror) | IPFS (fallback) |
|-------|-----------------|-----------------|-----------------|
| Source code | Primary repo | Push-hook mirror | — |
| Release binaries | GitHub Releases | GitLab Releases (GoReleaser) | Pinned on Filebase |
| Static site | GitHub Pages | GitLab Pages | Pinned + DNSLink ready |
| DNS failover | CNAME → GitHub Pages | Manual flip to GitLab Pages | Manual flip to Cloudflare IPFS gateway |

Rollout phases:
1. **Phase 1**: GitHub Pages only. CNAME `peerup.dev` → GitHub. Simple, free, fast.
2. **Phase 2**: Mirror site + releases to GitLab Pages + GitLab Releases. Same Hugo CI. Manual DNS failover if needed (CNAME swap on Cloudflare).
3. **Phase 3**: IPFS pinning on every release. DNSLink TXT record pre-configured. Nuclear fallback if both GitHub and GitLab die — flip CNAME to Cloudflare IPFS gateway.

Deliverables:
- [ ] Git mirror to GitLab via push hook or CI (source code resilience)
- [ ] GoReleaser config to publish to both GitHub Releases and GitLab Releases
- [ ] GitLab Pages deployment (`.gitlab-ci.yml` for Hugo build)
- [ ] CI step: `ipfs add` release binaries + site → pin on [Filebase](https://filebase.com/) (S3-compatible, 5GB free)
- [ ] DNSLink TXT record at `_dnslink.peerup.dev` pointing to IPNS key (pre-configured, activated on failover)
- [ ] Document failover runbook: which DNS records to change, in what order, for each failure scenario

**Package Managers & Binaries**:
- [ ] Set up [GoReleaser](https://goreleaser.com/) config (`.goreleaser.yaml`) — publish to GitHub Releases + GitLab Releases
- [ ] GitHub Actions workflow: on tag push, build binaries for Linux/macOS/Windows (amd64 + arm64)
- [ ] Publish to GitHub Releases with Ed25519-signed checksums (release key in repo)
- [ ] Homebrew tap: `brew install satindergrewal/tap/peerup`
- [ ] One-line install script: `curl -sSL get.peerup.dev | sh` — fetches `releases/latest.json`, detects OS/arch, downloads binary (GitHub → GitLab → IPFS fallback), verifies checksum, installs to `~/.local/bin` or `/usr/local/bin`
- [ ] APT repository for Debian/Ubuntu
- [ ] AUR package for Arch Linux
- [ ] Docker image + `docker-compose.yml` for containerized deployment

**Embedded / Router Builds** (OpenWRT, Ubiquiti, GL.iNet, MikroTik):
- [ ] GoReleaser build profiles: `default` (servers/desktops, `-ldflags="-s -w"`, ~25MB) and `embedded` (routers, + UPX compression, ~8MB)
- [ ] Cross-compilation targets: `linux/mipsle` (OpenWRT), `linux/arm/v7` (Ubiquiti EdgeRouter, Banana Pi), `linux/arm64` (modern routers)
- [ ] Optional build tag `//go:build !webrtc` to exclude WebRTC/pion (~2MB savings) for router builds
- [ ] OpenWRT `.ipk` package generation for opkg install
- [ ] Guide: *"Running peer-up on your router"* — OpenWRT, Ubiquiti EdgeRouter, GL.iNet travel routers
- [ ] Binary size budget: default ≤25MB stripped, embedded ≤10MB compressed. Current: 34MB full → 25MB stripped → ~8MB UPX.

**Auto-Upgrade** (builds on commit-confirmed pattern from Phase 4C):
- [ ] `peerup upgrade --check` — fetch `peerup.dev/releases/latest.json`, compare version with running binary, show changelog
- [ ] `peerup upgrade` — download binary from manifest (GitHub → GitLab → IPFS fallback), verify Ed25519 checksum, replace binary, restart. Manual confirmation required.
- [ ] `peerup upgrade --auto` — automatic upgrade via systemd timer or cron. Downloads, verifies, applies with commit-confirmed safety:
  1. Rename current binary to `peerup.rollback`
  2. Install new binary, start with `--confirm-timeout 120`
  3. New binary runs health check (relay reachable? peers connectable?)
  4. If healthy → auto-confirm, delete rollback
  5. If unhealthy or no confirmation → systemd watchdog restarts with rollback binary
  6. **Impossible to brick a remote node** — same pattern Juniper has used for 20+ years
- [ ] `relay-server upgrade --auto` — same pattern for relay VPS. Especially critical since relay is remote.
- [ ] Version mismatch warning — when `peerup status` shows peers running different versions, warn with upgrade instructions
- [ ] Relay version announcement — relay broadcasts its version to connected peers via libp2p Identify `UserAgent`. Peers see "relay running v1.2.0, you have v1.1.0, run `peerup upgrade`"

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
# Home: peerup daemon
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

> *Comparison based on publicly available documentation as of 2026-02. Details may be outdated — corrections welcome via [GitHub issues](https://github.com/satindergrewal/peer-up/issues).*

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
- [ ] Decentralized analytics — on-device network intelligence using statistical anomaly detection (moving average, z-score). No centralized data collection. Each node monitors its own connection quality, predicts relay degradation, and auto-switches paths before failure. Data never leaves the node. Inspired by Nokia AVA's "bring code to where the data is" philosophy. Implementation: gonum for statistics, pure Go, no ML frameworks needed for initial phases

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
| **Phase 4C: Core Hardening & Security** | ✅ 6-8 weeks | Complete (Batches A–G) |
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
- CI pipeline runs on every push (build + vet + test with `-race`)
- Config versioning enables safe schema migration across deployments
- `go test -race ./...` passes with >60% overall coverage; >70% on daemon, auth, config; >40% on CLI commands
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
- `peerup --version` shows build version, commit hash, and build date
- Peers exchange version info via libp2p Identify UserAgent — `peerup status` shows peer versions
- Protocol versioning policy documented (backwards-compatible within major version)
- Integration tests verify real libp2p host-to-host connectivity in `go test`

**Phase 4D Success**:
- Third-party code can implement custom `Resolver`, `Authorizer`, and stream middleware
- Event hooks fire for peer connect/disconnect and auth decisions
- New CLI commands require <30 lines of orchestration (bootstrap consolidated)
- File transfer works between authorized peers (first plugin)
- `peerup daemon --ollama` auto-detects and exposes Ollama (service template plugin)
- `peerup wake <peer>` sends magic packet (WoL plugin)
- Transfer speed saturates relay bandwidth; resume works after interruption
- SDK documentation published with working plugin examples
- `peerup discover <peer>` returns list of exposed services with tags
- Python SDK works: `pip install peerup-sdk` → connect to remote service in <10 lines
- `peerup invite --headless` outputs JSON; `peerup join --from-env` reads env vars

**Phase 4E Success**:
- `peerup.dev` serves a Hugo documentation site with landing page, guides, and install instructions
- Site auto-deploys on push to `main` via GitHub Actions
- `peerup.dev/llms.txt` returns markdown index; `peerup.dev/llms-full.txt` returns full site content — AI agents can understand the project in ~200 tokens
- `curl get.peerup.dev | sh` installs the correct binary for the user's OS/arch
- `peerup.dev/releases/latest.json` manifest is the single source of truth for all upgrade/install consumers
- Binary and install script try GitHub → GitLab → IPFS in order (three-tier fallback)
- Source code, releases, and site mirrored to GitLab (push hook + GoReleaser + GitLab Pages)
- Release binaries pinned on IPFS (Filebase); DNSLink pre-configured for emergency failover
- Failover runbook documented: which DNS records to change for each failure scenario
- GoReleaser builds binaries for 9+ targets (linux/mac/windows × amd64/arm64 + linux/mipsle + linux/arm/v7)
- Embedded builds ≤10MB (UPX compressed), default builds ≤25MB (stripped)
- Homebrew tap works: `brew install satindergrewal/tap/peerup`
- Docker image available
- Install-to-running in under 30 seconds
- `peerup upgrade` fetches manifest from `peerup.dev`, downloads with fallback, verifies checksum
- `peerup upgrade --auto` with commit-confirmed rollback — impossible to brick remote nodes
- Relay announces version to peers; version mismatch triggers upgrade warning
- GPU inference use-case guide published
- Router deployment guide published (OpenWRT, Ubiquiti, GL.iNet)
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

**Last Updated**: 2026-02-20
**Current Phase**: 4C Complete (Batches A–G all shipped, tested, merged to main)
**Phase count**: 4C–4I (7 phases, down from 9 — file sharing and service templates merged into plugin architecture)
**Next Milestone**: Phase 4C Batch H (Observability) — OpenTelemetry, metrics, connection quality scoring, hole-punch tracking, audit logging
**Relay elimination**: Planned post-Batch H — `require_auth` peer relays → DHT discovery → VPS becomes obsolete
