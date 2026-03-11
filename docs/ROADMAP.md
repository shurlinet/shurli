# Shurli Development Roadmap

This document outlines the multi-phase evolution of Shurli from a simple NAT traversal tool to a comprehensive decentralized P2P network infrastructure.

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

**Status**: ✅ Completed (keytool features merged into `shurli` subcommands in Phase 4C module consolidation; `cmd/keytool/` deleted)

**Deliverables**:
- [x] `cmd/keytool` with 5 commands: generate, peerid, validate, authorize, revoke
- [x] Comment-preserving parser for authorize/revoke
- [x] Color-coded terminal output
- [x] Integration with existing auth system
- [x] Comprehensive documentation in README

**Note**: All keytool functionality now lives in `shurli` subcommands: `shurli whoami` (peerid), `shurli auth add` (authorize), `shurli auth remove` (revoke), `shurli auth list`, `shurli auth validate` (validate). Key generation happens via `shurli init`.

---

## Phase 4: Service Exposure & Core Library

**Goal**: Transform Shurli into a reusable library and enable exposing local services through P2P connections.

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
  - [x] Single binary - merged home-node into `shurli daemon`
  - [x] Standard config path - auto-discovery (`./shurli.yaml` → `~/.config/shurli/config.yaml` → `/etc/shurli/config.yaml`)
  - [x] `shurli init` - interactive setup wizard (generates config, keys, authorized_keys)
  - [x] All commands support `--config <path>` flag
  - [x] Unified config type (one config format for all modes)

**Key Files**:
- `cmd/shurli/` - Single binary with subcommands: init, serve, proxy, ping
- `pkg/p2pnet/` - Reusable P2P networking library
- `internal/config/loader.go` - Config discovery, loading, path resolution

---

### Phase 4B: Frictionless Onboarding ✅ COMPLETE

**Timeline**: 1-2 weeks
**Status**: ✅ Completed

**Goal**: Eliminate manual key exchange and config editing. Get two machines connected in under 60 seconds.

**Rationale**: The current flow (generate key → share peer ID → edit authorized_keys → write config) has 4 friction points before anything works. This is the single biggest adoption barrier.

**Deliverables**:
- [x] `shurli invite` - generate short-lived invite code (encodes relay address + peer ID)
- [x] `shurli join <code>` - accept invite, exchange keys, auto-configure, connect
- [x] QR code output for `shurli invite` (scannable by mobile app later)
- [x] `shurli whoami` - show own peer ID and friendly name for sharing
- [x] `shurli auth add <peer-id> --comment "friend"` - append to authorized_keys
- [x] `shurli auth list` - show authorized peers
- [x] `shurli auth remove <peer-id>` - revoke access
- [x] `shurli relay add/list/remove` - manage relay addresses without editing YAML
- [x] Flexible relay address input - accept `IP:PORT` or bare `IP` (default port 7777) in addition to full multiaddr
- [x] QR code display in `shurli init` (peer ID) and `shurli invite` (invite code)
- [x] Relay connection info + QR code in `setup.sh --check`

**Security hardening** (done as part of 4B):
- [x] Sanitize authorized_keys comments (prevent newline injection)
- [x] Sanitize YAML names from remote peers (prevent config injection)
- [x] Limit invite/join stream reads to 512 bytes (prevent OOM DoS)
- [x] Validate multiaddr before writing to config YAML
- [x] Use `os.CreateTemp` for atomic writes (prevent symlink attacks)
- [x] Reject hostnames in relay input - only IP addresses accepted (no DNS resolution / SSRF)
- [x] Config files written with 0600 permissions

**Key Files**:
- `cmd/shurli/cmd_auth.go` - auth add/list/remove subcommands
- `cmd/shurli/cmd_whoami.go` - show peer ID
- `cmd/shurli/cmd_invite.go` - generate invite code + QR + P2P handshake
- `cmd/shurli/cmd_join.go` - decode invite, connect, auto-configure
- `cmd/shurli/cmd_relay.go` - relay add/list/remove subcommands
- `cmd/shurli/relay_input.go` - flexible relay address parsing (IP, IP:PORT, multiaddr)
- `internal/auth/manage.go` - shared AddPeer/RemovePeer/ListPeers with input sanitization
- `internal/invite/code.go` - binary invite code encoding/decoding (base32)

**User Experience**:
```bash
# Machine A (home server)
$ shurli invite --name home
=== Invite Code (expires in 10m0s) ===
AEQB-XJKZ-M4NP-...
[QR code displayed]
Waiting for peer to join...

# Machine B (laptop)
$ shurli join AEQB-XJKZ-M4NP-... --name laptop
=== Joined successfully! ===
Peer "home" authorized and added to names.
Try: shurli ping home

# Or use CLI auth commands directly:
$ shurli auth add 12D3KooW... --comment "friend"
$ shurli auth list
$ shurli auth remove 12D3KooW...

# Manage relay servers:
$ shurli relay add 203.0.113.50:7777 --peer-id 12D3KooW...
$ shurli relay list
$ shurli relay remove /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
```

**Security**:
- Invite codes are short-lived (configurable TTL, default 10 minutes)
- One-time use - code is invalidated after successful join
- Relay mediates the handshake but never sees private keys
- Both sides must be online simultaneously during join
- Stream reads capped at 512 bytes to prevent OOM attacks
- All user-facing inputs sanitized before writing to files

**Bug fixes (discovered during real-world testing)**:
- [x] Fixed invite code corruption when `--name` flag follows positional arg (`shurli join CODE --name laptop` - Go's `flag.Parse` stops at first non-flag, concatenating `--name` and `laptop` into the base32 code)
- [x] Added strict multihash length validation in invite decoder - Go's `base32.NoPadding` silently accepts trailing junk, so `Decode()` now re-encodes and compares multihash byte lengths
- [x] Fixed stream reset during invite join - inviter now flushes the OK response through the relay circuit before closing the stream
- [x] Added `reorderFlagsFirst()` to `runJoin()` so flags can appear after positional args (natural CLI usage)
- [x] First test file: `internal/invite/code_test.go` - round-trip, invalid input, and trailing junk rejection tests

---

### Phase 4C: Core Hardening & Security

**Timeline**: 6-8 weeks (batched)
**Status**: ✅ Complete (Batches A-I, Post-I-1, Post-I-2, Pre-Phase 5 Hardening)

**Goal**: Harden every component for production reliability. Fix critical security gaps, add self-healing resilience, implement test coverage, and make the system recover from failures automatically - before wider distribution puts binaries in more hands.

**Rationale**: The relay is a public-facing VPS with no resource limits. There are near-zero tests. Connections don't survive relay restarts. A bad config change on the relay can lock you out permanently. These are unacceptable for a mission-critical system that people depend on for remote access. Industry practice for hardened infrastructure (Juniper, Cisco, Kubernetes, systemd) demands: validated configs, automatic recovery, resource isolation, and health monitoring.

**Implementation Order** (batched for incremental value):
| Batch | Focus | Key Items |
|-------|-------|-----------|
| A | **Reliability** | Reconnection with backoff, TCP dial timeout, DHT in proxy, integration tests | ✅ DONE |
| B | **Code Quality** | Proxy dedup, structured logging (`log/slog`), sentinel errors, build version embedding | ✅ DONE |
| C | **Self-Healing** | Config validation/archive/rollback, commit-confirmed, systemd watchdog | ✅ DONE |
| D | **libp2p Features** | AutoNAT v2, smart dialing, QUIC preferred, version in Identify | ✅ DONE |
| E | **New Capabilities** | `shurli status`, `/healthz` endpoint, headless invite/join, UserAgent fix | ✅ DONE |
| F | **Daemon Mode** | `shurli daemon`, Unix socket API, ping/traceroute/resolve, dynamic proxies | ✅ DONE |
| G | **Test Coverage & Documentation** | 80.3% combined coverage, Docker integration tests, relay merge, engineering journal, website | ✅ DONE |
| H | **Observability** | Prometheus metrics, libp2p built-in metrics, custom shurli metrics, audit logging, Grafana dashboard | ✅ DONE |
| Pre-I-a | **Build & Deployment Tooling** | Makefile, service install (systemd/launchd), generic local checks runner | ✅ DONE |
| Pre-I-b | **PAKE-Secured Invite/Join** | Ephemeral DH + token-bound AEAD, relay-resistant pairing, v2 invite codes | ✅ DONE |
| Pre-I-c | **Private DHT Networks** | Configurable DHT namespace for isolated peer groups (gaming, family, org) | ✅ DONE |
| I | **Adaptive Path Selection** | Interface discovery, dial racing, path quality, network monitoring, STUN hole-punch, every-peer-relay | ✅ DONE |
| Post-I-1 | **Frictionless Relay Pairing** | Relay admin generates pairing codes, joiners connect in one command, SAS verification, expiring peers, reachability grading | ✅ DONE |
| Post-I-2 | **Peer Introduction Protocol** | Relay pushes peer introductions to daemons, HMAC group commitment, interaction history, relay admin socket | ✅ DONE |
| Pre-Phase 5 | **Cross-Network Hardening** | 8 bug fixes from live cross-network testing, CGNAT detection, stale address diagnostics, systemd/launchd service setup | ✅ DONE |
| **Phase 5** | **Network Intelligence** | |
| 5-K | mDNS Local Discovery | Zero-config LAN peer discovery, instant same-network detection, no DHT/relay needed for local peers, dedup + concurrency limiting | ✅ DONE |
| 5-L | PeerManager | Background reconnection of authorized peers, exponential backoff, event-bus driven connect/disconnect tracking, network-change backoff reset | ✅ DONE |
| 5-M | Network Intelligence (Presence) | Three-layer transport: direct push + gossip forwarding + future GossipSub. Peers exchange reachability grade, NAT type, IPv4/IPv6 flags, CGNAT status. `/shurli/presence/1.0.0` protocol | ✅ DONE |
| **Phase 6** | **ACL + Relay Security** | Role-based access, macaroon capability tokens, passphrase-sealed vault, async invite deposits, remote unseal, TOTP + Yubikey 2FA | ✅ DONE |
| **Phase 7** | **ZKP Privacy Layer** | Anonymous auth, anonymous relay, privacy-preserving reputation, private namespace membership. gnark PLONK + Ethereum KZG ceremony (141,416 participants). | ✅ DONE |
| **Phase 8** | **Identity Security + Remote Admin** | Unified BIP39 seed, encrypted identity (Argon2id), remote relay admin over P2P, MOTD/goodbye announcements, session tokens, lock/unlock, doctor, completion, man page | ✅ DONE |
| **Phase 9** | **Plugins, SDK & First Plugins** | 9A interfaces DONE, 9B file transfer DONE. 9C-9E (discovery, Python SDK, Swift SDK) planned |

**Deliverables**:

**Security (Critical)**:
- [x] Relay resource limits - replace `WithInfiniteLimits()` with configurable `WithResources()` + `WithLimit()`. Defaults tuned for SSH/XRDP (10min sessions, 64MB data). Configurable via `resources:` section in relay-server.yaml.
- [x] Auth hot-reload - daemon API `POST /v1/auth` and `DELETE /v1/auth/{peer_id}` reload `authorized_keys` at runtime. `GaterReloader` interface updates ConnectionGater in-place. *(Batch F)*
- [x] Per-service access control - `AllowedPeers` field on each service restricts which peers can connect. Config supports per-service `allowed_peers` list. nil = all authorized peers allowed (backward compatible). ACL check runs before TCP dial. *(Pre-Batch H)*
- [x] Rate limiting on incoming connections and streams - libp2p ResourceManager enabled (auto-scaled connection/stream/memory limits). Always-on for relay server; opt-in for home/client nodes via `resource_limits_enabled`. OS-level: iptables SYN flood protection (50/s) and UDP rate limiting (200/s) in setup.sh. *(Pre-Batch H)*
- [x] QUIC source address verification - reverse path filtering (rp_filter=1) enabled in setup.sh, SYN cookies for TCP flood protection. *(Pre-Batch H)*
- [x] OS-level rate limiting - iptables rules in `setup.sh` (TCP SYN 50/s burst 100, UDP 200/s burst 500), conntrack tuning (131072 max, tw_reuse, fin_timeout=30s), systemd cgroup limits (MemoryMax=512M, CPUQuota=200%, TasksMax=4096). *(Pre-Batch H)*
- [x] Config file permissions - write with 0600 (not 0644) *(done in Phase 4B)*
- [x] Key file permission check on load - refuse to load keys with permissions wider than 0600 (actionable error message with `chmod` fix)
- [x] Service name validation - DNS-label format enforced (1-63 lowercase alphanumeric + hyphens), prevents protocol ID injection
- [x] Relay address validation in `shurli init` - parse multiaddr before writing config *(done in Phase 4B)*

**libp2p Upgrade (Critical)**:
- [x] Upgrade main module go-libp2p to latest - gains AutoNAT v2, smart dialing, QUIC improvements, Resource Manager, per-IP rate limiting, source address verification *(already on v0.47.0)*
- [x] Upgrade relay-server go-libp2p to match main module *(v0.38.2 → v0.47.0, done via `go work sync`)*
- [x] Enable AutoNAT v2 - per-address reachability testing (know which specific addresses are publicly reachable; distinguish IPv4 vs IPv6 NAT state). Includes nonce-based dial verification and amplification attack prevention. *(Batch D)*
- [x] Enable smart dialing - address ranking, QUIC prioritization, sequential dial with fast failover (reduces connection churn vs old parallel-dial-all approach) *(built into v0.47.0; transport ordering set QUIC-first)*
- [x] QUIC as preferred transport - 1 fewer RTT on connection setup (3 RTTs vs 4 for TCP), native multiplexing, better for hole punching *(Batch D - transport order: QUIC → TCP → WebSocket)*
- [x] Version in Identify - `libp2p.UserAgent("shurli/<version>")` and `libp2p.UserAgent("relay-server/<version>")` set on all hosts. Peers exchange version info via Identify protocol. Integration test verifies exchange. *(Batch D)*
- [x] Private DHT - migrated from IPFS Amino DHT (`/ipfs/kad/1.0.0`) to private shurli DHT (`/shurli/kad/1.0.0`). All 3 `dht.New()` calls in shurli + relay-server now use `dht.ProtocolPrefix("/shurli")`. Relay server runs DHT in server mode as the bootstrap peer. No more polluting the IPFS routing table or getting rejected by ConnectionGater. *(Post-Batch F)*

**Self-Healing & Resilience** (inspired by Juniper JunOS, Cisco IOS, Kubernetes, systemd, MikroTik):
- [x] **Config validation command** - `shurli config validate` parses config, checks key file exists, verifies relay address reachable, dry-run before applying. Also validates relay config. *(Batch C)*
- [x] **Config archive** - `internal/config/archive.go` auto-saves last-known-good config (`.config.last-good.yaml`) on successful serve startup. Atomic write with temp+rename. *(Batch C)*
- [x] **Config rollback** - `shurli config rollback` restores from last-known-good archive. *(Batch C)*
- [x] **Commit-confirmed pattern** (Juniper JunOS / Cisco IOS) - `shurli config apply <new-config> --confirm-timeout 5m` applies a config change and auto-reverts if not confirmed via `shurli config confirm`. **Prevents permanent lockout on remote relay.** `internal/config/confirm.go` implements `ApplyCommitConfirmed()` and `EnforceCommitConfirmed()`. *(Batch C)*
- [x] **systemd watchdog integration** - `internal/watchdog/watchdog.go` sends `sd_notify("WATCHDOG=1")` every 30s with health check. `Ready()`, `Stopping()`, `Watchdog()` messages. Integrated into `serve_common.go`. Extended with Unix socket health check in Batch F. *(Batch C)*
- [x] **Health check HTTP endpoint** - relay exposes `/healthz` on a configurable port (default: disabled, `127.0.0.1:9090`). Returns JSON: peer ID, version, uptime, connected peers count, protocol count. Used by monitoring (Prometheus, UptimeKuma). *(Batch E)*
- [x] **`shurli status` command** - show local config at a glance: version, peer ID, config path, relay addresses, authorized peers, services, names. No network required - instant. *(Batch E)*

**Auto-Upgrade Groundwork** (full implementation in Phase 10):
- [x] **Build version embedding** - compile with `-ldflags "-X main.version=..."` so every binary knows its version. `shurli version` / `shurli --version` and `relay-server version` / `relay-server --version` print build version, commit hash, build date, and Go version. Version printed in relay-server startup banner. `setup.sh` injects version from git at build time.
- [x] **Version in libp2p Identify** - set `UserAgent` to `shurli/<version>` in libp2p host config. Peers learn each other's versions automatically on connect (no new protocol needed). *(Batch D - serve/proxy/ping; Batch E - invite/join)*
- [x] **Protocol versioning policy** - documented in engineering journal (ADR-D03). Wire protocols (`/shurli/proxy/1.0.0`) are backwards-compatible within major version. Version info exchanged via libp2p Identify UserAgent.

**Automation & Integration**:
- [x] **Daemon mode** - `shurli daemon` runs in foreground (systemd/launchd managed), exposes Unix socket API (`~/.config/shurli/shurli.sock`) with cookie-based auth. JSON + plain text responses. 23 endpoints: status, peers, services, auth (add/remove/hot-reload), paths, ping, traceroute, resolve, connect/disconnect (dynamic proxies), expose/unexpose, shutdown, lock/unlock. CLI client auto-reads cookie. *(Batch F)*
- [x] **Headless onboarding** - `shurli invite --non-interactive` skips QR, prints bare code to stdout, progress to stderr. `shurli join --non-interactive` reads invite code from CLI arg, `SHURLI_INVITE_CODE` env var, or stdin. No TTY prompts. Essential for containerized and automated deployments (Docker, systemd, scripts). *(Batch E)*

**Reliability**:
- [x] Reconnection with exponential backoff - `DialWithRetry()` wraps proxy dial with 3 retries (1s → 2s → 4s) to recover from transient relay drops
- [x] Connection warmup - addressed by `PathDialer.DialPeer()` pre-dial (Batch I) and daemon `ConnectToPeer()` + PeerManager keepalive (Batch F/5-L). Both modes pre-establish peer connection before TCP listener accepts.
- [x] Stream pooling - addressed by libp2p connection multiplexing (all streams share one peer connection) and Identify protocol caching (eliminates repeated negotiation). Per-stream overhead ~1-5ms.
- [x] Persistent relay reservation - `serve_common.go` keeps reservation alive with periodic `circuitv2client.Reserve()` at `cfg.Relay.ReservationInterval`. Runs as background goroutine during daemon lifetime.
- [x] DHT bootstrap in proxy command - Kademlia DHT (client mode) bootstrapped at proxy startup. Async `FindPeer()` discovers target's direct addresses, enabling DCUtR hole-punching (~70% bypass relay entirely).
- [x] Graceful shutdown - replace `os.Exit(0)` with proper cleanup, context cancellation stops background goroutines
- [x] Goroutine lifecycle - use `time.Ticker` + `select ctx.Done()` instead of bare `time.Sleep` loops
- [x] TCP dial timeout - `net.DialTimeout("tcp", addr, 10s)` for local service connections (serve side and proxy side). `ConnectToService()` uses 30s context timeout for P2P stream dial.
- [x] Fix data race in bootstrap peer counter (`atomic.Int32`)

**Observability** (Batch H): ✅ DONE

Prometheus metrics (not OpenTelemetry SDK - libp2p emits Prometheus natively, zero new dependencies, ~zero binary size impact):
- [x] Prometheus `/metrics` endpoint - opt-in via `telemetry.metrics.enabled` config, disabled by default. Daemon: separate TCP listener (`127.0.0.1:9091`). Relay: added to existing `/healthz` mux. `libp2p.DisableMetrics()` called when off to save CPU
- [x] libp2p built-in metrics exposed - swarm connections, hole-punch success/failure, autorelay reservations, AutoNAT reachability, Identify exchanges, resource manager limits, relay service stats. Free from libp2p, just needs `/metrics` endpoint
- [x] Resource manager stats tracer - `rcmgr.WithTraceReporter()` enables per-connection/stream/memory metrics on the rcmgr Grafana dashboard
- [x] Custom shurli metrics - proxy bytes/connections/duration per service, auth allow/deny counters, hole-punch counters/histograms (enhanced from existing tracer), daemon API request timing, build info gauge
- [x] Audit logging - structured JSON via slog for security events: auth allow/deny decisions, service ACL denials, daemon API access, auth changes via API. Opt-in via `telemetry.audit.enabled`
- [x] Grafana dashboard - pre-built JSON dashboard with 56 panels across 11 sections (Overview, Proxy Throughput, Security, Hole Punch, Daemon API, System, ZKP Privacy, ZKP Auth Overview, ZKP Proof Generation, ZKP Verification, ZKP Tree Operations) covering proxy throughput, auth decisions, vault seal state, pairing, invite deposits, admin socket, hole punch stats, API latency, ZKP operations, and system metrics. Import-ready for any Grafana instance.

Deferred from original Batch H scope (with reasoning):
- ~~OpenTelemetry SDK integration~~ - Replaced by Prometheus directly. libp2p uses Prometheus natively; adding OTel SDK would add ~4MB binary size, 35% CPU overhead for traces, and a translation layer for zero benefit. The Prometheus bridge (`go.opentelemetry.io/contrib/bridges/prometheus`) can forward metrics to any OTel backend later without changing instrumentation code
- ~~Connection quality scoring~~ - Moved to Batch I (Adaptive Path Selection). Needs the metrics data Batch H provides before path intelligence can be built
- ~~Trace correlation IDs~~ - Deferred to future. 35% CPU overhead from distributed tracing span management not justified for P2P tool where network is the bottleneck. Revisit when OTel Go SDK has zero-cost path
- ~~Per-path latency/jitter metrics~~ - Moved to Batch I. Feeds into path selection intelligence
- ~~OTLP export~~ - Deferred. Prometheus bridge can forward metrics to any OTel backend later without changing instrumentation code

**Pre-Batch I-a: Build & Deployment Tooling** ✅ DONE

Makefile + service management + generic local checks runner. Small standalone task, not part of any batch.

- [x] `make build` - optimized binary with `-ldflags="-s -w" -trimpath`
- [x] `make test` - `go test -race -count=1 ./...`
- [x] `make clean` - remove build artifacts
- [x] `make install` - build + copy binary to `/usr/local/bin` + install service
- [x] `make install-service` - detect OS (Linux systemd / macOS launchd), copy service file, enable
- [x] `make uninstall-service` - stop and remove service
- [x] `make uninstall` - remove service + binary
- [x] `make restart-service` - quick restart after rebuild
- [x] `make website` - Hugo build/serve for local preview
- [x] `make check` - generic local checks runner. Reads commands from `.checks` file (gitignored). Runs each command; fails if any returns non-zero. The Makefile target is entirely generic - no hint about what is being checked or why. Users create their own `.checks` with whatever patterns matter to them
- [x] `make push` - runs `make check && git push` (impossible to push without passing local checks)
- [x] Service install for Linux: copy `deploy/shurli-daemon.service` to systemd, `daemon-reload`, `enable`
- [x] Service install for macOS: copy `deploy/com.shurli.daemon.plist` to `~/Library/LaunchAgents/`, `launchctl load`
- [x] Clear messaging when elevated permissions required (no silent `sudo`)
- [x] `.checks` file documented in README (generic mechanism, user creates their own patterns)

**Pre-Batch I-b: PAKE-Secured Invite/Join Handshake** ✅ DONE

Upgraded the invite/join token exchange from cleartext to an encrypted handshake inspired by WPA3's SAE. The relay now sees only opaque encrypted bytes during pairing. Zero new dependencies.

Approach: Ephemeral X25519 DH + token-bound HKDF-SHA256 key derivation + XChaCha20-Poly1305 AEAD encryption. All Go PAKE libraries evaluated were experimental/unmaintained; this approach uses only `crypto/ecdh` (stdlib), `golang.org/x/crypto/hkdf`, and `golang.org/x/crypto/chacha20poly1305` (already in dep tree).

- [x] Replace cleartext token exchange with encrypted handshake: both sides prove knowledge of the invite code without transmitting it
- [x] Ephemeral X25519 key exchange with token-bound HKDF key derivation
- [x] XChaCha20-Poly1305 AEAD encryption for all messages after key exchange
- [x] Invite versioning: version byte 0x01 = PAKE-encrypted handshake, 0x02 = relay pairing code. Legacy v1 cleartext protocol deleted in Post-I-1 (zero downgrade surface)
- [x] v2 invite code format: includes namespace field for DHT network auto-inheritance
- [x] Future version detection: v3+ codes rejected with "please upgrade shurli" message
- [x] Joiner auto-inherits inviter's DHT namespace from v2 invite code
- [x] ADR-Ib01 (DH + AEAD over formal PAKE) and ADR-Ib02 (invite code versioning)
- [x] Tests: 19 PAKE tests (handshake, token mismatch, tampered ciphertext, oversized message, io.Pipe simulation, key confirmation MAC, EOF handling) + 11 invite code tests (v1/v2 round-trip, namespace, future version, trailing junk)

Security model after upgrade:
- Invite code = shared secret, never transmitted over the wire
- Ephemeral DH + token binding proves mutual knowledge without revelation
- Derived AEAD key encrypts all peer name exchange
- Relay sees only ephemeral public keys + encrypted bytes: no token, no peer names
- Single-use + TTL + transport-layer identity verification unchanged
- If tokens differ, AEAD decryption fails with no protocol details leaked

**Pre-Batch I-c: Private DHT Networks** ✅ DONE

Configurable DHT namespace so users can create completely isolated peer networks. A gaming group, family, or organization sets a network name and their nodes form a separate DHT, invisible to all other Shurli users.

Before: All Shurli nodes shared one DHT with protocol prefix `/shurli/kad/1.0.0`. The authorized_keys gater controlled who could communicate, but discovery was shared.

After: DHT prefix becomes `/shurli/<namespace>/kad/1.0.0`. Nodes with different namespaces are not firewalled - they literally speak different protocols and cannot discover each other.

- [x] Config option: `discovery.network: "my-crew"` in config YAML (optional, default = global shurli DHT)
- [x] CLI flag: `shurli init --network "my-crew"`
- [x] DHT protocol prefix derived from namespace: `DHTProtocolPrefixForNamespace()` in `pkg/p2pnet/network.go`
- [x] Default (no namespace set) remains `/shurli/kad/1.0.0` for backward compatibility
- [x] Relay supports namespace via `discovery.network` in relay config
- [x] `shurli status` displays current network namespace (or "global (default)")
- [x] Validation: namespace must be DNS-label safe (lowercase alphanumeric + hyphens, 1-63 chars) via `validate.NetworkName()`
- [x] All 4 DHT call sites updated (serve_common, relay_serve, traceroute, proxy)
- [x] Tests: namespace validation, DHT prefix generation, config template with/without namespace
- [x] ADR-Ic01 documenting protocol-level isolation decision
- [x] Invite codes carry namespace (completed in Pre-I-b: v2 invite codes encode namespace, joiner auto-inherits)

Bootstrap model: Each private network needs at least one well-known bootstrap node (typically the relay). One relay per namespace (simple, self-sovereign). Multi-namespace relay support deferred to future if demand exists.

Foundation for Phase 13 (Federation): each private network becomes a federation unit. Cross-network communication is federation between namespaces.

**Batch I: Adaptive Multi-Interface Path Selection** ✅ DONE

Probes all available network interfaces at startup, tests each path to peers, picks the best, and continuously monitors for network changes. Path ranking: direct IPv6 > direct IPv4 > STUN-punched > peer relay > VPS relay. Zero new dependencies.

- [x] **I-a: Interface Discovery & IPv6 Awareness** - `DiscoverInterfaces()` enumerates all network interfaces with global unicast classification. IPv6/IPv4 flags on daemon status. Prometheus `interface_count` gauge.
- [x] **I-b: Parallel Dial Racing** - `PathDialer.DialPeer()` replaces sequential 45s worst-case with parallel racing. Already-connected fast path, DHT + relay concurrent, first success wins. `PathType` classification (DIRECT/RELAYED). Old `ConnectToPeer()` preserved as fallback.
- [x] **I-c: Path Quality Visibility** - `PathTracker` subscribes to libp2p event bus (`EvtPeerConnectednessChanged`). Per-peer path info: type, transport (quic/tcp), IP version, RTT. `GET /v1/paths` API endpoint. Prometheus `connected_peers` gauge with path_type/transport/ip_version labels.
- [x] **I-d: Network Change Monitoring (Event-Driven)** - `NetworkMonitor` detects interface/address changes and fires callbacks. Triggers interface re-scan, PathDialer update, and status refresh on network change.
- [x] **I-e: STUN-Assisted Hole-Punching** - Zero-dependency RFC 5389 STUN client. Concurrent multi-server probing, NAT type classification (none/full-cone/address-restricted/port-restricted/symmetric). `HolePunchable()` helper. Background non-blocking probe at startup + re-probe on network change. NAT type and external addresses exposed in daemon status.
- [x] **I-f: Every-Peer-Is-A-Relay** - Any peer with a global IP auto-enables circuit relay v2 with conservative resource limits (4 reservations, 16 circuits, 128KB/direction, 10min sessions). Auto-detect on startup and network change. Leverages existing ConnectionGater for authorization. `is_relaying` flag in daemon status.

New files: `interfaces.go`, `pathdialer.go`, `pathtracker.go`, `netmonitor.go`, `stunprober.go`, `peerrelay.go` (all in `pkg/p2pnet/` with matching `_test.go` files).

**Post-I-1: Frictionless Relay Pairing** ✅ DONE

Eliminates manual SSH + peer ID exchange for relay onboarding. Relay admin generates pairing codes, each person joins with one command. Motivated by Batch I live testing revealing the relay setup UX barrier for non-technical users.

- [x] **v1 cleartext deleted** - zero downgrade surface. PAKE renumbered to v1 (0x01), relay pairing is v2 (0x02)
- [x] **Extended authorized_keys format** - key=value attributes: `expires=<RFC3339>`, `verified=sha256:<prefix>`. Backward compatible parsing. `ListPeers()` returns `PeerEntry` with all attributes. `SetPeerAttr()` for programmatic updates.
- [x] **In-memory token store** (relay-side) - `internal/relay/tokens.go`. Parameterized code count (`--count N`, default 1). SHA-256 hashed tokens, constant-time comparison, per-group mutex, max 3 failed attempts before burn, uniform "pairing failed" error for all failure modes. 20 tests including concurrency races.
- [x] **v2 invite code format** - 16-byte token (no inviter peer ID), relay address + namespace encoded. Shorter than v1 (126 vs 186 chars). `EncodeV2()`/`decodeV2()` with trailing junk detection.
- [x] **Connection gater enrollment mode** - probationary peers (max 10, 15s timeout) admitted during active pairing. `PromotePeer()` moves to authorized. `CleanupProbation()` evicts with disconnect callback. Auto-disable when no active groups. Expiring peer support via `expires=` attribute checked on every `InterceptSecured` call.
- [x] **SAS verification (OMEMO-style)** - `ComputeFingerprint()` produces 4-emoji + 6-digit numeric code from sorted peer ID pair hash. 256-entry emoji table. `shurli verify <peer>` command with interactive confirmation. Writes `verified=sha256:<prefix>` to authorized_keys. Persistent `[UNVERIFIED]` badge on ping, traceroute, and status until verified.
- [x] **Relay pairing protocol** - `/shurli/relay-pair/1.0.0` stream protocol. Wire format: 16-byte token + name. Status codes: OK, ERR, PEER_ARRIVED, GROUP_COMPLETE, TIMEOUT. `PairingHandler` authorizes peers, promotes from probation, sets expiry. Token expiry and probation cleanup goroutines.
- [x] **Relay pairing via admin socket** - generates pairing codes from relay config. `--count N`, `--ttl`, `--namespace`, `--expires`. `--list` and `--revoke` for management. Accessed via admin socket client, not standalone CLI subcommand.
- [x] **Join v2 pair-join** - detects v2 codes, connects to relay, sends pairing request, authorizes discovered peers with name conflict resolution (suffix -2, -3...), shows SAS verification fingerprints, auto-starts daemon via `exec.Command`.
- [x] **Daemon-first commands** - `shurli ping` and `shurli traceroute` try daemon API first (fast, no bootstrap). Falls back to standalone if daemon not running. Verification badge shown before ping/traceroute output.
- [x] **Reachability grade** - A (public IPv6), B (public IPv4 or hole-punchable NAT), C (port-restricted NAT), D (symmetric NAT/CGNAT), F (offline). Computed from interface discovery + STUN results. Exposed in daemon status response and text output. 12 tests.
- [x] **AuthEntry extended** - daemon API `GET /v1/auth` now returns `verified` and `expires_at` fields
- [x] **Status verification badges** - `shurli status` shows `[VERIFIED]` or `[UNVERIFIED]` per peer

New files: `internal/relay/tokens.go`, `internal/relay/pairing.go`, `pkg/p2pnet/verify.go`, `pkg/p2pnet/reachability.go`, `cmd/shurli/cmd_verify.go` (all with matching `_test.go` files).

Zero new dependencies. Binary size unchanged at 28MB.

**Post-I-2: Peer Introduction Protocol** ✅ DONE

Relay actively pushes peer introductions to connected daemons when new peers join a group. Eliminates the need for manual "restart daemon to discover peers" after pairing. Driven by live testing revealing that paired peers didn't discover each other until both restarted.

- [x] **Peer-notify protocol** - `/shurli/peer-notify/1.0.0` stream protocol. Relay sends `PeerIntroduction` messages (peer ID, name, group ID, HMAC proof) to all group members when a new peer completes pairing. Daemon handler auto-authorizes introduced peers and registers names in the live resolver.
- [x] **HMAC group commitment** - `HMAC-SHA256(token, groupID)` proves token possession during pairing without revealing the token. Stored as `hmac_proof=` attribute in authorized_keys. Verified on introduction delivery.
- [x] **Relay admin socket** - Unix socket + cookie auth (same pattern as daemon API). `internal/relay/admin.go` serves `/v1/pair` endpoint. Admin client (`internal/relay/admin_client.go`) is a fire-and-forget HTTP client. Decouples code generation from the relay server process.
- [x] **Reconnect notifier** - `internal/relay/notify.go`. When a previously-connected peer re-identifies (e.g., after network change), relay re-delivers introductions for their group. Deduplication prevents burst delivery on reconnect flap.
- [x] **Interaction history** - `internal/reputation/history.go`. Append-only interaction log per peer (connection events, protocol exchanges). Foundation for Phase 5-L PeerManager scoring.
- [x] **Attribute updates for existing peers** - peer-notify handler updates group and HMAC proof attributes even for already-authorized peers (re-pairing after restart).

New files: `internal/relay/notify.go`, `internal/relay/admin.go`, `internal/relay/admin_client.go`, `internal/reputation/history.go` (all with matching `_test.go` files).

**Pre-Phase 5 Hardening** ✅ DONE

Cross-network testing across multiple ISPs and NAT types exposed 8 bugs. All fixed and re-verified on live networks before starting Phase 5. "No compromises. Can't build on a broken foundation."

- [x] **Startup race condition** - moved `SetupPeerNotify()` and `SetupPingPong()` before `Bootstrap()` so protocol handlers are registered before relay connection triggers introductions
- [x] **Address label misclassification** - replaced string-based IP classification with proper `net.IP` parsing. All RFC 1918, CGNAT (100.64.0.0/10), and link-local ranges now correctly labeled `[local]`
- [x] **`auth list` missing attributes** - extended output to show `[group=...]`, `[UNVERIFIED]`/`[VERIFIED]`, and expiry per peer
- [x] **Peer-notify attribute skip** - fixed code path that skipped group/HMAC attribute updates for already-authorized peers
- [x] **Noisy reconnect notifier** - reduced to DEBUG level, added 30s deduplication window
- [x] **Health check false warning** - added 60s startup grace period before relay-reservation health check fires
- [x] **STUN CGNAT awareness** - `BehindCGNAT` field in `STUNResult`, grade capped at D when RFC 6598 CGNAT detected on local interfaces
- [x] **Stale address detection** - cross-checks `host.Addrs()` against `net.InterfaceAddrs()`, labels stale addresses in status output, delayed diagnostic log 10s after network change
- [x] **Duplicate config names** - `updateConfigNames()` now checks if name+peerID already exists before writing (prevents YAML parse errors from duplicate keys)
- [x] **Service deployment** - systemd services on relay VPS and home-node (enabled at boot, watchdog integrated), launchd plist for macOS client

**Module Consolidation** (completed - single Go module):
- [x] Merged three Go modules (main, relay-server, cmd/keytool) into a single `go.mod`
- [x] Deleted `go.work` - no workspace needed with one module
- [x] Moved relay-server source into `cmd/shurli/cmd_relay_serve.go`; deployment artifacts consolidated into `deploy/` and `tools/`
- [x] Extracted `internal/identity/` package (from `pkg/p2pnet/identity.go`) - `CheckKeyFilePermissions()`, `LoadOrCreateIdentity()`, `PeerIDFromKeyFile()` shared by shurli and relay-server
- [x] Extracted `internal/validate/` package - `ServiceName()` for DNS-label validation of service names
- [x] Deleted `cmd/keytool/` entirely - all features exist in `shurli` subcommands (`whoami`, `auth add/list/remove/validate`)
- [x] Added `shurli auth validate` (ported from keytool validate)
- [x] CI simplified to `go build ./...`, `go vet ./...`, `go test -race -count=1 ./...` from project root

**Pre-Refactoring Foundation** (completed before main 4C work):
- [x] GitHub Actions CI - build, vet, and test on every push to `main` and `dev/next-iteration`
- [x] Config version field - `version: 1` in all configs; loader defaults missing version to 1, rejects future versions. Enables safe schema migration.
- [x] Unit tests for config package - loader, validation, path resolution, version handling, relay config
- [x] Unit tests for auth package - gater (inbound/outbound/update), authorized_keys (load/parse/comments), manage (add/remove/list/duplicate/sanitize)
- [x] Integration tests - in-process libp2p hosts verify real stream connectivity, half-close semantics, P2P-to-TCP proxy, and `DialWithRetry` behavior (6 tests in `pkg/p2pnet/integration_test.go`)

**Batch A - Reliability** (completed):
- [x] `DialWithRetry()` - exponential backoff retry (1s → 2s → 4s) for proxy dial
- [x] TCP dial timeout - 10s for local service, 30s context for P2P stream
- [x] DHT bootstrap in proxy command - Kademlia DHT (client mode) for direct peer discovery
- [x] `[DIRECT]`/`[RELAYED]` connection path indicators in logs (checks `RemoteMultiaddr()` for `/p2p-circuit`)
- [x] DCUtR hole-punch event tracer - logs hole punch STARTED/SUCCEEDED/FAILED and direct dial events

**Batch B - Code Quality** (completed):
- [x] Deduplicated bidirectional proxy - `BidirectionalProxy()` + `HalfCloseConn` interface (was 4 copies, now 1)
- [x] Sentinel errors - 8 sentinel errors across 4 packages, all using `%w` wrapping for `errors.Is()`
- [x] Build version embedding - `shurli version`, `relay-server version`, ldflags injection in setup.sh
- [x] Structured logging with `log/slog` - library code migrated (~20 call sites), CLI output unchanged

**Batch E - New Capabilities** (completed):
- [x] `shurli status` - local-only info command (version, peer ID, config, relays, authorized peers, services, names)
- [x] `/healthz` HTTP endpoint on relay-server - JSON health check for monitoring (disabled by default, binds `127.0.0.1:9090`)
- [x] `shurli invite --non-interactive` - bare invite code to stdout, progress to stderr, skip QR
- [x] `shurli join --non-interactive` - reads code from CLI arg, `SHURLI_INVITE_CODE` env var, or stdin
- [x] UserAgent fix - added `shurli/<version>` UserAgent to invite/join hosts (was missing from Batch D)

**Batch F - Daemon Mode** (completed):
- [x] `shurli daemon` - long-running P2P host with Unix socket HTTP API
- [x] Cookie-based authentication (32-byte random hex, `0600` permissions, rotated per restart)
- [x] 23 API endpoints with JSON + plain text format negotiation (`?format=text` / `Accept: text/plain`)
- [x] `serve_common.go` - extracted shared P2P runtime (zero duplication between serve and daemon)
- [x] Auth hot-reload - `POST /v1/auth` and `DELETE /v1/auth/{peer_id}` take effect immediately
- [x] Dynamic proxy management - create/destroy TCP proxies at runtime via API
- [x] P2P ping - standalone (`shurli ping`) + daemon API, continuous/single-shot, stats summary
- [x] P2P traceroute - standalone (`shurli traceroute`) + daemon API, DIRECT vs RELAYED path analysis
- [x] P2P resolve - standalone (`shurli resolve`) + daemon API, name → peer ID
- [x] Stale socket detection (dial test, no PID files)
- [x] Daemon client library (`internal/daemon/client.go`) with auto cookie reading
- [x] CLI client commands: `shurli daemon status/stop/ping/services/peers/connect/disconnect`
- [x] Service files: `deploy/shurli-daemon.service` (systemd) + `deploy/com.shurli.daemon.plist` (launchd)
- [x] Watchdog extended with Unix socket health check
- [x] Tests: auth middleware, handlers, lifecycle, stale socket, integration, ping stats
- [x] Documentation: `docs/DAEMON-API.md` (full API reference), `docs/NETWORK-TOOLS.md` (diagnostic commands)

**Batch G - Test Coverage & Documentation** (completed):

Combined coverage: **80.3%** (unit + Docker integration). Relay-server binary merged into shurli (commit 5d167b3).

Priority areas (all hit or exceeded targets):
- [x] **cmd/shurli** (4% → 80%+) - 96 test functions covering CLI commands, flag handling, config template, daemon lifecycle, error paths. Relay serve commands merged and tested. *(relay-server binary merged into shurli)*
- [x] **internal/daemon** (12% → 70%+) - all 14 API handlers tested (status, ping, traceroute, resolve, connect/disconnect, auth CRUD, services, shutdown), format negotiation, cookie auth, proxy lifecycle, client library
- [x] **pkg/p2pnet** (23% → 84%) - naming, service registry, proxy half-close, relay address parsing, identity, ping, traceroute
- [x] **internal/config** (48% → 75%+) - archive/rollback, commit-confirmed timer, loader edge cases, benchmark tests
- [x] **internal/auth** (50% → 75%+) - hot-reload, concurrent access, malformed input, gater tests
- [x] **Docker integration tests** - `test/docker/integration_test.go` with relay container, invite/join, ping through circuit. Coverage-instrumented via `test/docker/coverage.sh`
- [x] **CI coverage reporting** - `.github/workflows/pages.yaml` merges unit + Docker coverage via `go tool covdata merge`, reports combined coverage
- [x] **Engineering journal** ([`docs/ENGINEERING-JOURNAL.md`](ENGINEERING-JOURNAL.md)) - 41 architecture decision records (ADRs) covering core architecture (8) and all batches A-I plus Pre-I. Not a changelog - documents *why* every design choice was made, what alternatives were considered, and what trade-offs were accepted.
- [x] **Website** - Hugo + Hextra site scaffolded with landing page, 7 retroactive blog posts (Batches A-G), `tools/sync-docs` (Go) for auto-transformation, GitHub Actions CI/CD for GitHub Pages deployment
- [x] **Security hardening** - post-audit fixes across 10 files (commit 83d02d3). CVE-2026-26014 resolved (pion/dtls v3.1.2). CI Actions pinned to commit SHAs.

**Service CLI** (completed - completes the CLI config management pattern):
- [x] `shurli service add <name> <address>` - add a service (enabled by default), optional `--protocol` flag
- [x] `shurli service remove <name>` - remove a service from config
- [x] `shurli service enable <name>` - enable a disabled service
- [x] `shurli service disable <name>` - disable a service without removing it
- [x] `shurli service list` - list configured services with status
- [x] All config sections (auth, relay, service) now manageable via CLI - no YAML editing required
- [x] `local_address` can point to any reachable host (e.g., `192.168.0.5:22`) - home node acts as LAN gateway

**Code Quality**:
- [x] Expand test coverage - 80.3% combined coverage. Naming, proxy, invite edge cases, relay input parsing all tested. *(Batch G)*
- [x] Structured logging - migrated library code (`pkg/p2pnet/`, `internal/auth/`) to `log/slog` with structured key-value fields and log levels (Info/Warn/Error). CLI commands remain `fmt.Println` for user output. *(Batch B)*
- [x] Sentinel errors - defined `ErrServiceAlreadyRegistered`, `ErrNameNotFound`, `ErrPeerAlreadyAuthorized`, `ErrPeerNotFound`, `ErrInvalidPeerID`, `ErrConfigNotFound`, `ErrConfigVersionTooNew`, `ErrInvalidServiceName` across 4 error files. All wrapped with `fmt.Errorf("%w: ...")` for `errors.Is()` support. *(Batch B)*
- [x] Deduplicate proxy pattern - extracted `BidirectionalProxy()` with `HalfCloseConn` interface and `tcpHalfCloser` adapter (was copy-pasted 4x, now single ~30-line function). *(Batch B)*
- [x] Consolidate config loaders - unified `LoadNodeConfig()` delegates to `LoadHomeNodeConfig()`, `LoadClientNodeConfig()` also delegates. Single `NodeConfig` struct.
- [x] Health/status endpoint - `/healthz` on relay (Batch E), `shurli status` (Batch E), daemon API `/v1/status` (Batch F) expose connection state, relay status, active streams.

**Industry References**:
- **Juniper JunOS `commit confirmed`**: Apply config, auto-revert if not confirmed. Standard in network equipment for 20+ years. Prevents lockout on remote devices - identical problem to a remote relay server.
- **Cisco IOS `configure replace`**: Atomic config replacement with automatic rollback on failure.
- **MikroTik Safe Mode**: Track all changes since entering safe mode; revert everything if connection drops.
- **Kubernetes liveness/readiness probes**: Health endpoints that trigger automatic restart on failure.

**libp2p Specification References**:
- **Circuit Relay v2**: [Specification](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md) - reservation-based relay with configurable resource limits
- **DCUtR**: [Specification](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md) - Direct Connection Upgrade through Relay (hole punching coordination)
- **AutoNAT v2**: [Specification](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md) - per-address reachability testing with amplification prevention
- **Hole Punching Measurement**: [Study](https://arxiv.org/html/2510.27500v1) - 4.4M traversal attempts, 85K+ networks, 167 countries, ~70% success rate
- **systemd WatchdogSec**: Process heartbeat - if the process stops responding, systemd restarts it. Used by PostgreSQL, nginx, and other production services.
- **Caddy atomic reload**: Start new config alongside old; if new config fails, keep old. Zero-downtime config changes.

---

## Phase 5: Network Intelligence

**Goal**: Smarter peer discovery, scoring, and communication. mDNS for instant LAN discovery, Bitcoin-inspired peer management for reliable connections, and PubSub broadcast for network-wide awareness. Includes relay decentralization groundwork.

**Status**: ✅ DONE

### 5-K: mDNS Local Discovery ✅ DONE

Zero-config peer discovery on the local network. When two Shurli nodes are on the same LAN, mDNS finds them in milliseconds without DHT lookups or relay bootstrap. Directly addresses the latency gap observed during Batch I live testing: LAN-connected peers currently route through the relay first, then upgrade to direct. With mDNS, they discover each other instantly.

- [x] Enable libp2p mDNS discovery (`github.com/libp2p/go-libp2p/p2p/discovery/mdns`) - already in the dependency tree, zero binary size impact
- [x] Integrate with existing peer authorization - mDNS-discovered peers still checked against `authorized_keys` (ConnectionGater enforces, no bypass)
- [x] Combine with DHT discovery - mDNS for local, DHT for remote. Both feed into PathDialer
- [x] Config option: `discovery.mdns_enabled: true` (default: true, disable for server-only nodes)
- [x] Explicit DHT routing table refresh on network change events - trigger `RefreshRoutingTable()` from NetworkMonitor callbacks (currently runs on internal timer only, can go stale in small private networks)
- [x] Test: two hosts on same LAN discover each other via mDNS within 5 seconds without relay

Quick win. One libp2p option on host construction + NetworkMonitor integration. Prerequisite: none. Zero new dependencies.

### 5-L: PeerManager / AddrMan ✅ DONE

Bitcoin-inspired peer management, dimming star scoring, persistent peer table, peerstore metadata, bandwidth tracking, DHT refresh on network change, gossip discovery (PEX). Top priority after mDNS. Motivated by the "no re-upgrade from relay to direct after network change" finding from Batch I live testing.

### 5-M: Network Intelligence (Presence) ✅ DONE

Three-layer presence transport: direct push + gossip forwarding + future GossipSub. Peers exchange reachability grade, NAT type, IPv4/IPv6 flags, and CGNAT status via the `/shurli/presence/1.0.0` protocol. Direct push handles current-scale networks; gossip forwarding propagates presence to peers-of-peers; GossipSub activation deferred until networks exceed 10+ peers (overhead exceeds utility below that threshold).

- [x] **Presence protocol** - `/shurli/presence/1.0.0`. Peers exchange network capability snapshots (reachability grade, NAT type, IPv4/IPv6 flags, CGNAT status) on connect and on network change.
- [x] **Direct push** - point-to-point presence delivery to all connected peers. Immediate propagation at current network scale.
- [x] **Gossip forwarding** - peers-of-peers receive presence updates via forwarding. Extends reach beyond direct connections without full PubSub overhead.
- [x] **Future GossipSub activation point** - GossipSub mesh management (GRAFT, PRUNE, IHAVE, IWANT) deferred until networks reach 10+ peers where broadcast overhead is justified. Architecture ready for upgrade.

Dependency: Requires PeerManager (5-L) for peer lifecycle data. GossipSub deferred (go-libp2p-pubsub pins go-libp2p v0.39.1, incompatible with our v0.47.0).

### Relay Decentralization

After Phase 5 observability and PeerManager provide the data:

- [x] `require_auth` relay service - peer relay with config knobs (`peer_relay.enabled`, `peer_relay.resources.*`). Auto-enables on public IP, config-driven forced enable/disable, OnStateChange callback for discovery integration. ConnectionGater enforces auth before relay protocol runs
- [x] DHT-based relay discovery - peer relays advertise on DHT via `dht.Provide()` under namespace-aware CID. NATted nodes discover relays via `FindProvidersAsync()`. RelaySource interface abstracts static vs dynamic relay addresses. AutoRelay PeerSource integration
- [x] Multi-relay failover with health-aware selection - RelayHealth tracks per-relay EWMA scores (success rate, RTT, freshness). RelayDiscovery returns health-ranked relay addresses. Background probing every 60s. Degraded relays deprioritized automatically. Prometheus metrics for relay health scores
- [x] Per-peer bandwidth tracking - BandwidthTracker wraps libp2p's BandwidthCounter. Per-peer, per-protocol, and aggregate stats via Prometheus gauges. Background publish loop (30s). Daemon API: `GET /v1/bandwidth`
- [x] Bootstrap decentralization - layered bootstrap: config peers > DNS seeds (`_dnsaddr.<domain>` TXT records) > hardcoded seeds > relay addresses. Same pattern as Bitcoin/IPFS. `seeds.go` + `dnsseed.go`
---

### Phase 6: ACL, Relay Security & Client Invites ✅ COMPLETE

**Status**: ✅ Complete

**Goal**: Production-ready access control, relay security, and async client-generated invites. Make the relay safe enough to run unattended and convenient enough to manage remotely. First real-world test: generate an invite code in NZ, send to a friend in AU, close laptop, friend joins when ready.

**Why before ZKP**: ZKP needs a proper authorization model to prove against. You cannot do "prove you're authorized without revealing identity" until authorization itself is well-defined. This phase builds the foundation ZKP (Phase 7) sits on.

**Access Control (Three-Tier Model)**:
- [x] `role` attribute on `authorized_keys` entries (`admin` / `member`) - `internal/auth/roles.go`
- [x] First peer paired with relay automatically gets `role=admin` (if `CountAdmins() == 0`)
- [x] Relay checks role on privileged operations
- [x] Invite policy config: `admin-only` (default) / `open` - `internal/config/config.go`
- [x] Role display in `shurli auth list` with `[admin]`/`[member]` badges

**Client-Deposit Invites ("Contact Card" Model)**:
- [x] `shurli relay invite create` generates macaroon-backed invite deposit
- [x] Relay stores deposit in `DepositStore` with macaroon, caveats, TTL
- [x] Joiner can consume deposit any time (inviter can be offline) - async model
- [x] `shurli relay invite modify <id> --add-caveat <k=v>` - add restrictions before consumption (attenuation-only)
- [x] `shurli relay invite revoke <id>` - kill pending invite
- [x] `shurli relay invite list` - list all deposits with status
- [x] Deposit states: pending, consumed, revoked, expired
- [x] Auto-expiry with configurable TTL, lazy expiration on access
- [x] `CleanExpired()` removes old deposits

**Macaroon Capability Tokens**:
- [x] HMAC-chain bearer tokens - `internal/macaroon/macaroon.go`
- [x] Attenuation: holders can add caveats (restrictions), never remove (cryptographic enforcement)
- [x] Caveat language: `service`, `group`, `action`, `peers_max`, `delegate`, `expires`, `network` - `internal/macaroon/caveat.go`
- [x] `DefaultVerifier()` with fail-closed design
- [x] JSON + Base64 encode/decode for wire transport
- [x] 22 tests (macaroon) + 10 tests (caveat)

**Relay Security (Passphrase-Sealed Vault)**:
- [x] Argon2id KDF (time=3, memory=64MB, threads=4) + XChaCha20-Poly1305 encryption - `internal/vault/vault.go`
- [x] Sealed/unsealed modes (watch-only when sealed)
- [x] Auto-reseal after configurable timeout
- [x] Hex-encoded seed phrase for identity recovery (32 bytes as 24 hex-pair words)
- [x] Root key zeroed from memory on seal (`crypto/subtle.XORBytes`)
- [x] 14 vault tests including create/save/load/unseal/seal cycle
- [x] Vault CLI: `shurli relay vault init`, `seal`, `unseal`, `status`

**Remote Unseal Over P2P**:
- [x] `/shurli/relay-unseal/1.0.0` protocol - `internal/relay/unseal.go`
- [x] Binary wire format: `[1 version] [2 BE passphrase-len] [N passphrase] [1 TOTP-len] [M TOTP]`
- [x] Admin-only check via `auth.IsAdmin()` before processing
- [x] iOS-style escalating lockout: 4 free attempts, then 1m/5m/15m/1h(x3), permanent block
- [x] Prometheus metrics: `shurli_vault_unseal_total{result}`, `shurli_vault_unseal_locked_peers` gauge
- [x] `shurli relay unseal --remote <name|peer-id|multiaddr>` for P2P unseal from client (short name resolution)
- [x] 11 unseal tests (wire format, lockout escalation, permanent block, message formatting)

**Two-Factor Authentication**:
- [x] TOTP (RFC 6238) - `internal/totp/totp.go` with skew window, 11 tests including RFC test vectors
- [x] Yubikey HMAC-SHA1 challenge-response - `internal/yubikey/challenge.go` via `ykman` CLI, 6 tests
- [x] Vault stores which 2FA methods are enabled
- [x] `otpauth://` provisioning URI for authenticator app setup

**New relay admin endpoints** (10 total):
- `POST /v1/unseal`, `POST /v1/seal`, `GET /v1/seal-status`
- `POST /v1/vault/init`, `GET /v1/vault/totp-uri`
- `POST /v1/invite`, `GET /v1/invite`, `DELETE /v1/invite/{id}`, `PATCH /v1/invite/{id}`
- `POST /v1/auth/reload` (hot-reload authorized_keys + ZKP tree)

**New P2P protocol**: `/shurli/relay-unseal/1.0.0`

**New files** (19 files, ~3,655 lines):
- `internal/auth/roles.go` + `roles_test.go`
- `internal/macaroon/macaroon.go` + `macaroon_test.go` + `caveat.go` + `caveat_test.go`
- `internal/totp/totp.go` + `totp_test.go`
- `internal/vault/vault.go` + `vault_test.go`
- `internal/deposit/store.go` + `store_test.go` + `errors.go`
- `internal/relay/unseal.go` + `unseal_test.go`
- `internal/yubikey/challenge.go` + `challenge_test.go`
- `cmd/shurli/cmd_relay_vault.go` + `cmd_relay_invite.go`

**Threat Model (Analyzed)**:
- Admin peer ID forgery: impossible (libp2p cryptographic identity)
- Stolen admin key: escalating lockout + permanent block + audit log + key rotation + mandatory 2FA
- Invite code bruteforce: 8-byte token (2^64 possibilities), rate limit, lockout, TTL
- Sybil attack on open relay: per-peer invite quota, total caps, admin revoke, cooldown
- Invite flooding DoS: TTL-based cleanup, per-peer and total caps
- MITM on deposit: non-issue (libp2p Noise encryption)
- Replay attack: unique tokens, duplicate rejection, single-use enforcement
- Server compromise while sealed: encrypted data at rest, master key not in memory
- Server compromise while unsealed: master key in memory (bounded by timeout)

**Design Influences** (full analysis in private memory):
- Studied centralized ACL models: policy tests, autogroups, deny-by-default patterns. Rejected centralized control planes (sovereignty violation)
- Studied certificate-based models: groups-in-identity concept, P2P verification. Rejected god-key CA and manual revocation patterns
- Macaroon HMAC chain: proven pattern for offline attenuation and compact bearer tokens
- UCANs: future evolution path when public-key-only verification is needed (Phase 7+ ZKP)
- Studied enterprise seal/unseal patterns vs offline key management: passphrase-sealed pattern wins for solo operator (timeout auto-lock, watch-only, seed recovery)

**Authorization Model Evolution**:
```
Phase 6 (done):  Macaroons for invites + authorized_keys for gating (coexist)
Phase 7+ (ZKP): Macaroons could become the primary auth token
Future:          authorized_keys becomes optional cache layer
```

---

### Phase 7: ZKP Privacy Layer ✅ COMPLETE

**Status**: ✅ Complete

**Goal**: Zero-knowledge proof authorization. Peers prove they hold valid capabilities (macaroons) without revealing their identity or specific permissions to the relay. Built on gnark (PLONK + KZG).

**Dependency**: Requires Phase 6 macaroon authorization model (now complete) as the capability system ZKP proves against.

---

### Phase 8: Identity Security + Remote Admin

**Status**: ✅ Complete

**Goal**: Unify all cryptographic material under one BIP39 seed phrase, encrypt identity keys at rest, enable full relay administration over P2P, and add operator announcement capabilities.

**Deliverables**:

**Unified Seed Architecture**:
- [x] Single BIP39 seed phrase (24 words) derives identity key, vault key, and ZKP SRS via HKDF domain separation
- [x] `shurli init` generates seed, confirms via quiz, derives and encrypts identity key
- [x] `shurli recover` reconstructs identity from seed phrase (hidden input, no echo)
- [x] `shurli recover --relay` also recovers vault and ZKP keys from same seed

**Encrypted Identity**:
- [x] All nodes encrypt identity.key at rest: Argon2id KDF + XChaCha20-Poly1305 (SHRL format)
- [x] `shurli change-password` re-encrypts with new password
- [x] Legacy unencrypted key files detected and user prompted to encrypt

**Remote Admin Protocol** (`/shurli/relay-admin/1.0.0`):
- [x] Full relay management over encrypted P2P connections (24+ admin API endpoints)
- [x] All relay admin commands support `--remote <addr>` for remote operation
- [x] Admin role check at stream open (non-admins rejected before data exchange)
- [x] Rate limiting (5 requests/second per peer)
- [x] Vault init is LOCAL ONLY (seed material never travels over the network)

**MOTD and Goodbye** (`/shurli/relay-motd/1.0.0`):
- [x] Signed operator announcements (Ed25519) with 3 message types: MOTD, goodbye, retract
- [x] MOTD: 280-char limit, deduped per-relay (24h), pushed on peer connect
- [x] Goodbye: persistent farewell, cached by clients, survives restarts
- [x] Retract: cancels a goodbye (relay is back)
- [x] Messages sanitized: URL/email stripping, non-ASCII removal, 280-char truncation
- [x] Timestamp validation: reject future (>5min) and stale (>7d) messages

**Session Tokens**:
- [x] Machine-bound session tokens for password-free daemon restarts
- [x] `shurli session refresh` / `shurli session destroy`
- [x] `shurli lock` / `shurli unlock` gate sensitive operations without destroying session

**CLI Enhancements**:
- [x] `shurli doctor [--fix]` - health check + auto-fix (completions, man page, config)
- [x] `shurli completion <bash|zsh|fish>` - shell completion scripts
- [x] `shurli man` - troff man page (display, install, uninstall)

**Key Files**: `internal/identity/seed.go`, `internal/identity/encrypted.go`, `internal/identity/session.go`, `internal/relay/remote_admin.go`, `internal/relay/remote_admin_client.go`, `internal/relay/motd.go`, `internal/relay/motd_client.go`, `cmd/shurli/cmd_lock.go`, `cmd/shurli/cmd_doctor.go`

---

### Phase 9: Plugin Architecture, SDK & First Plugins

**Goal**: Make Shurli extensible by third parties - and prove the architecture works by shipping real plugins. The plugins ARE the SDK examples.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring - without forking. But empty interfaces are worthless: shipping real plugins alongside the architecture validates the design immediately and catches interface mistakes before third parties discover them.

**Why This Existed**: Shurli's core (identity, auth, crypto, relay, ZKP) is solid through Phase 8. The next step is opening it up so others can build on it without forking. This phase transitions Shurli from a tool to a platform.

---

#### Phase 9A: Core Interfaces & Library Consolidation ✅ DONE

**Timeline**: 1-2 weeks
**Status**: ✅ Complete

**Goal**: Define the public API contracts that third-party code will depend on. This is design-first work - get the interfaces right before building implementations, because changing them later breaks downstream users.

**Core Interfaces** (`pkg/p2pnet/contracts.go`):
- [x] `PeerNetwork` - interface for core network operations (expose, connect, resolve, close, events)
- [x] `Resolver` - interface for name resolution with fallback chaining: local -> custom -> peer.Decode
- [x] `ServiceManager` - interface for service registration and dialing, with middleware support
- [x] `Authorizer` - interface for authorization decisions. Enables pluggable auth (certs, tokens, database)
- [x] `StreamMiddleware` / `StreamHandler` - functional middleware chain for stream handlers
- [x] `EventType` / `Event` / `EventHandler` - typed event system for network lifecycle
- Logger: uses Go stdlib `*slog.Logger` (no custom interface needed - deletion over addition)

**Extension Points**:
- [x] Constructor injection - `Network.Config` accepts optional `Resolver`
- [x] Event hook system - `OnEvent(handler)` with subscribe/unsubscribe, thread-safe `EventBus`
- [x] Stream middleware - `ServiceRegistry.Use(middleware)` wraps inbound stream handlers
- [x] Protocol ID formatter - `ProtocolID()` constructor + `MustValidateProtocolIDs()` for init-time validation *(completed in 9B)*

**Library Consolidation** (completed in 9B):
- [x] Extract DHT/relay bootstrap from CLI into `pkg/p2pnet/bootstrap.go` - `BootstrapAndConnect()` for standalone CLI commands
- [x] Centralize orchestration - `cmd_ping.go` and `cmd_traceroute.go` reduced by ~100 lines each
- [x] Package-level documentation for `pkg/p2pnet/` - `doc.go`

**Interface Preview**:
```go
// Third-party resolver
type DNSResolver struct { ... }
func (r *DNSResolver) Resolve(name string) (peer.ID, error) { ... }

// Wire it up with custom resolver
net, _ := p2pnet.New(&p2pnet.Config{
    Resolver: &DNSResolver{},
})

// React to events
unsub := net.OnEvent(func(e p2pnet.Event) {
    if e.Type == p2pnet.EventPeerConnected {
        metrics.PeerConnections.Inc()
    }
})
defer unsub()

// Add stream middleware (applied to all services)
net.ServiceRegistry().Use(func(next p2pnet.StreamHandler) p2pnet.StreamHandler {
    return func(svcName string, s network.Stream) {
        log.Printf("stream opened for %s", svcName)
        next(svcName, s)
    }
})
```

**Compile-time checks**: `var _ PeerNetwork = (*Network)(nil)` etc. enforce interface satisfaction.

**Exit Criteria**: All interfaces compile, compile-time satisfaction checks pass, zero regressions in test suite (21/21 packages PASS).

---

#### Phase 9B: File Transfer Plugin ✅ DONE

**Timeline**: 3 weeks
**Status**: ✅ Complete (core in 9B, hardened across FT-A through FT-H + audit-fix batches)

**Goal**: Build file transfer as the first real plugin. Validates the `ServiceManager` and stream middleware interfaces from 9A. Also includes bootstrap extraction and protocol ID helpers deferred from 9A.

**Core Transfer (9B)**:
- [x] `shurli send <file> <peer>` - fire-and-forget by default, `--follow` for inline progress, `--priority` for queue priority
- [x] `shurli transfers` - transfer inbox with `--watch` for live updates, `--history` for completed, `--json` for scripting
- [x] FastCDC content-defined chunking (own implementation, adaptive targets 128K-2M)
- [x] BLAKE3 Merkle tree integrity (binary tree, odd-node promotion, root verification)
- [x] zstd compression on by default with incompressible detection and bomb protection (10x ratio cap)
- [x] SHFT v2 wire format (magic + version + flags + manifest + chunk data)
- [x] Receive modes: off / contacts (default) / ask / open / timed
- [x] Disk space checks before each chunk write
- [x] Atomic writes (write to `.tmp`, rename on completion)
- [x] `PluginPolicy` - transport-aware access control (LAN + Direct only, relay blocked by default)
- [x] `BootstrapAndConnect()` extracted to `pkg/p2pnet/bootstrap.go` (standalone CLI commands)
- [x] `ProtocolID()` + `MustValidateProtocolIDs()` for init-time protocol validation

**Download Protocol (FT-A)**:
- [x] `shurli download <file> <peer>` - download from shared catalog
- [x] `shurli browse <peer>` - browse peer's shared files
- [x] Browse protocol (`/shurli/file-browse/1.0.0`) and download protocol (`/shurli/file-download/1.0.0`)

**Transfer Queue (FT-B)**:
- [x] `TransferQueue` with priority ordering and configurable concurrency (default: 3 active)
- [x] Queue status visible in `shurli transfers` and daemon API

**Share Persistence (FT-C)**:
- [x] `ShareRegistry` with persistent storage (`shares.json`)
- [x] `shurli share add/remove/list` - manage shared files
- [x] Shares survive daemon restarts

**RaptorQ Multi-Source (FT-D)**:
- [x] `shurli download --multi-peer --peers home,laptop` - download from multiple peers simultaneously
- [x] RaptorQ fountain codes: any sufficient subset of symbols reconstructs the file
- [x] Multi-peer wire protocol (`/shurli/file-multi-peer/1.0.0`)
- [x] Per-peer contribution tracking and garbage symbol detection

**Gaps & Hardening (FT-E)**:
- [x] Per-peer rate limiting: 10 transfer requests/minute, fixed-window, silent rejection
- [x] Peer ID prefix matching for CLI convenience
- [x] Erasure coding display in transfer progress

**Compression Ratio Display (FT-F)**:
- [x] Compression ratio shown in transfer output (e.g., "compressed 42%")

**Per-Stream Progress (FT-G)**:
- [x] Per-stream progress display for parallel transfers

**--to Alias (FT-H)**:
- [x] `shurli share add <path> --to <peer>` for selective sharing (restrict individual shares to specific peers)

**Resume + Checkpoint (9B-2)**:
- [x] Checkpoint files (`.shurli-ckpt-<hash>`) store bitfield of received chunks
- [x] Interrupted transfers resume from last checkpoint

**Erasure Coding (9B-3)**:
- [x] Reed-Solomon erasure coding with stripe-based layout
- [x] Auto-enabled on Direct WAN only (overhead not justified on LAN)
- [x] Parity bounded at 50% overhead max

**Parallel Streams (9B-4)**:
- [x] Adaptive parallel QUIC streams (1 for LAN, up to 4 for WAN)

**Directory Transfer (9B-5 + 3B)**:
- [x] Recursive directory transfer with path structure preserved
- [x] Path sanitization: strip `..`, absolute paths, null bytes, control chars

**Audit-Fix Batches (1A, 1B, 2A-2C, 3A-3C, 4A-4C)**:
- [x] Macaroon `Verify()` wired into pairing consume flow (1A)
- [x] Yubikey challenge-response wired into vault unseal (1B)
- [x] Transfer event logging - JSON-line log with rotation (2A)
- [x] Transfer notifications - desktop and command modes (2B)
- [x] Timed receive mode - temporarily open, reverts after duration (2C)
- [x] Batch accept/reject - `shurli accept --all`, `shurli reject --all` (3A)
- [x] Parallel receive streams (3C)
- [x] AllowStandalone config wiring (4A)
- [x] Erasure config gap fix (4C)

**Security hardening (FT-I, FT-J)**:
- [x] Full integration audit: all exported functions wired, config fields validated, CLI flags consistent
- [x] Privacy sweep: 17 checks all ZERO
- [x] Command injection fix in notification command templates
- [x] Multi-peer filename sanitization
- [x] Transfer IDs changed from sequential to random hex (`xfer-<12hex>`)
- [x] Rate limiter applied to multi-peer request path
- [x] `TransferService.Close()` cleanup on daemon shutdown

**New P2P protocols** (4):
- `/shurli/file-transfer/2.0.0` - core send/receive
- `/shurli/file-browse/1.0.0` - share browsing
- `/shurli/file-download/1.0.0` - file download
- `/shurli/file-multi-peer/1.0.0` - multi-source download

**New daemon API endpoints** (15 new, 38 total):
- `POST /v1/send`, `GET /v1/transfers`, `GET /v1/transfers/history`, `GET /v1/transfers/pending`, `GET /v1/transfers/{id}`, `POST /v1/transfers/{id}/accept`, `POST /v1/transfers/{id}/reject`, `POST /v1/transfers/{id}/cancel`, `GET /v1/shares`, `POST /v1/shares`, `DELETE /v1/shares`, `POST /v1/browse`, `POST /v1/download`, `POST /v1/config/reload`, `GET /v1/config/reload`

**New CLI commands** (8): `send`, `download`, `share`, `browse`, `transfers`, `accept`, `reject`, `cancel`

**Dependencies**: zeebo/blake3 (CC0), klauspost/compress/zstd (BSD-3), klauspost/reedsolomon (MIT), xssnick/raptorq (MIT)

**New files**: 22 source files (~10,000 lines) across `pkg/p2pnet/` and `cmd/shurli/` + matching test files.

**Test status**: 1100 tests across 21 packages, race detector clean.

**Exit Criteria**: File transfer works end-to-end on physical hardware (tested across CGNAT, LAN, and direct connections). Interface adjustments from this phase fed back into 9A interfaces.

**Future considerations** (not in scope for this phase):
- AI/neural compression: deferred to 2028-2029 when NNCP/neural codecs have stable Go implementations
- Network file sync: rsync-like continuous background sync mode
- TUI dashboard: ncurses-style transfer monitoring

---

#### Phase 9C: Service Discovery & Additional Plugins

**Timeline**: 1-2 weeks
**Status**: 📋 Planned

**Goal**: Service discovery protocol and two more plugins that prove different interface patterns: service templates (health middleware) and Wake-on-LAN (event hooks).

**Service Discovery Protocol**:
- [ ] New protocol `/shurli/discovery/1.0.0` - query a remote peer for their exposed services
- [ ] Response includes service names and optional tags (e.g., `gpu`, `storage`, `inference`)
- [ ] `shurli discover <peer>` CLI command - list services offered by a peer
- [ ] Service tags in config: `tags: [gpu, inference]` - categorize services for discovery

**Service Templates** (proves `ServiceManager` + health middleware):
- [ ] `shurli daemon --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `shurli daemon --vllm` shortcut (auto-detects vLLM on localhost:8000)
- [ ] `shurli daemon --openclaw` shortcut (auto-detects OpenClaw Gateway on localhost:18789, exposes with friendly name "openclaw-gateway")
- [ ] Health check middleware - verify local service is reachable before exposing
- [ ] Streaming response verification (chunked transfer for LLM output)

**Wake-on-LAN** (proves event hooks + new protocol):
- [ ] `shurli wake <peer>` - send magic packet before connecting
- [ ] Event hook: auto-wake peer on connection attempt (optional)

**Exit Criteria**: Discovery protocol tested across relay and direct. Templates auto-detect local services. Wake-on-LAN proven on physical hardware.

---

#### Phase 9D: Python SDK & Documentation

**Timeline**: 1-2 weeks
**Status**: 📋 Planned
**Repository**: `github.com/shurlinet/shurli-sdk-python` (separate repo, ships to PyPI)

**Goal**: Ship the Python SDK and comprehensive documentation. The plugins from 9B/9C ARE the SDK examples - no synthetic demos, real working code.

**Python SDK** (`shurli-sdk`):
- [ ] Thin wrapper around daemon Unix socket API (38 endpoints already implemented)
- [ ] `pip install shurli-sdk`
- [ ] Core operations: connect, expose_service, discover_services, proxy, status
- [ ] Async support (asyncio) for integration with event-driven applications
- [ ] Example: connect to a remote service in <10 lines of Python

**SDK Documentation** (the plugins above ARE the examples):
- [ ] `docs/SDK.md` - guide for building on `pkg/p2pnet`
- [ ] Example walkthrough: how file transfer was built as a plugin
- [ ] Example walkthrough: how service templates use health middleware
- [ ] Example: custom name resolver plugin
- [ ] Example: auth middleware (rate limiting, logging)

**Headless Onboarding Enhancements** (already complete):
- [x] `shurli invite --non-interactive` - bare code to stdout, no QR, progress to stderr *(Phase 4C Batch E)*
- [x] `shurli join --non-interactive` - reads code from CLI arg, `SHURLI_INVITE_CODE` env var, or stdin *(Phase 4C Batch E)*
- [x] Docker-friendly: `SHURLI_INVITE_CODE=xxx shurli join --non-interactive --name node-1` *(Phase 4C Batch E)*

**Exit Criteria**: SDK installable via pip, all examples runnable, docs reviewed for accuracy against current code.

---

#### Phase 9E: Swift SDK

**Timeline**: 1-2 weeks
**Status**: 📋 Planned
**Repository**: `github.com/shurlinet/shurli-sdk-swift` (separate repo, ships via Swift Package Manager)

**Goal**: Ship a native Swift SDK that wraps the daemon API. This is the foundation the Apple multiplatform app (Phase 12) will be built on. Building it here, before the app, validates the API surface from a non-Go language and catches design issues early. "Eat your own cooking" - if the SDK can't power the Apple app, it can't power anything.

**Swift SDK** (`ShurliSDK`):
- [ ] Swift Package (SPM) wrapping daemon HTTP API (Unix socket + cookie auth)
- [ ] `Codable` model types matching all daemon API responses
- [ ] Core operations: connect, status, expose/unexpose services, discover, proxy, peer management
- [ ] Event streaming (SSE or WebSocket) for real-time peer status, network transitions, transfer progress
- [ ] Async/await native (Swift concurrency, no callback chains)
- [ ] Platform-adaptive transport: Unix socket on macOS, HTTP over localhost on iOS (Network Extension context)
- [ ] Example: connect to daemon and list peers in <10 lines of Swift

**Design Constraints**:
- Zero external dependencies (Foundation + Network framework only). Sovereignty.
- Strict concurrency (`Sendable` compliance from day one)
- Works in App Extension context (Network Extensions have restricted APIs)
- Shared between macOS app (direct socket), iOS app (tunnel context), and any third-party Swift app

**Exit Criteria**: Package importable via SPM, all daemon API endpoints covered, async/await works, tested on macOS (direct) and iOS simulator (localhost transport). Phase 12 app can depend on this package with zero API plumbing in the app repo.

---

### SDK & App Repository Strategy

Non-Go SDKs and consumer apps each live in their own dedicated GitHub repository. The Go SDK (`pkg/p2pnet`) stays in this repo since it IS the core library.

**Rationale**: Different languages have different release cycles, CI pipelines, dependency ecosystems, and potential contributors. A Python SDK ships to PyPI. A Swift SDK ships via SPM. Forcing them into one repo means every PR touches CI configs for languages the author doesn't use. Separate repos also let each SDK version independently of the daemon.

| Repository | What | Ships To |
|-----------|------|----------|
| `shurlinet/shurli` | Core daemon + Go library + CLI + plugins | Homebrew, apt, binary releases |
| `shurlinet/shurli-sdk-python` | Python daemon client | PyPI (`shurli-sdk`) |
| `shurlinet/shurli-sdk-swift` | Swift daemon client | Swift Package Manager (`ShurliSDK`) |
| `shurlinet/shurli-ios` | Apple multiplatform app (consumer of Swift SDK) | App Store |
| Future: `shurlinet/shurli-sdk-js` | JS/TS daemon client | npm (`shurli`) |

The Go "SDK" is just `go get github.com/shurlinet/shurli/pkg/p2pnet` - no separate repo needed since Go consumers import the library directly from the daemon's module.

---

### Phase 10: Distribution & Launch

**Timeline**: 1-2 weeks
**Status**: 📋 Planned

**Goal**: Make Shurli installable without a Go toolchain, launch with compelling use-case content, and establish `shurli.io` as the stable distribution anchor - independent of any single hosting provider.

**Rationale**: High impact, low effort. Prerequisite for wider adoption. GPU inference, game streaming, and IoT use cases already work - they just need documentation and a distribution channel. The domain `shurli.io` is the one thing no third party can take away - every user-facing URL routes through it, never hardcoded to `github.com` or any other host.

**Deliverables**:

**Website & Documentation (shurli.io)**:
- [x] Static documentation site built with [Hugo](https://gohugo.io/) + [Hextra](https://imfing.github.io/hextra/) theme - Go-based SSG, fast builds, matches the project toolchain, built-in search and dark mode
- [x] Automated docs sync (`tools/sync-docs`, Go) - transforms `docs/*.md` into Hugo-ready content with front matter and link rewriting
- [x] Elegant landing page with visual storytelling - hero with problem-first hook, terminal demo section, 3-step "How It Works" grid, network diagram, tabbed install commands (macOS/Linux/source), bottom CTA grid *(enhanced post-Batch G)*
- [x] Seven retroactive blog posts for Batches A-G (outcomes-focused)
- [x] GitHub Actions CI/CD - build Hugo site and deploy to GitHub Pages on push to `main` or `dev/next-iteration` (see deployment note below)
- [x] GitHub Pages hosting with custom domain (`shurli.io`) - DNS provider configured, CNAME deployed, site live *(2026-02-20)*
- [x] DNS managed via DNS provider - A/AAAA records → GitHub Pages, CDN + DDoS protection enabled, SSL mode "Full" *(2026-02-20)*
- [ ] CNAME `get.shurli.io` → serves install script
- [x] Landing page - hero section, feature grid (NAT traversal, single binary, SSH trust, 60s pairing, TCP proxy, self-healing) *(Batch G)*
- [x] Existing docs rendered as site pages - `tools/sync-docs` transforms ARCHITECTURE, FAQ, TESTING, ROADMAP, DAEMON-API, NETWORK-TOOLS, ENGINEERING-JOURNAL into Hugo-ready content *(Batch G)*
- [x] Custom blog listing template - image cards with title overlay, gradient, responsive grid *(post-Batch G)*
- [x] Dark theme default + theme toggle in navbar *(post-Batch G)*
- [x] SVG images for terminal demo, how-it-works steps, network diagram *(post-Batch G)*
- [x] 40+ SVG diagrams across docs, blog posts, and architecture visuals - replacing ASCII art in Architecture (7), FAQ (2), Network Tools (1), Daemon API (1), plus blog post diagrams, philosophy visuals, and Batch I architecture diagrams *(post-Batch G, expanded through Batch I)*
- [x] Feature card icons (Heroicons), section title icons, doc index icons, about page icons *(post-Batch G)*
- [x] Doc sidebar reordered for user journey: Quick Start → Network Tools → FAQ → Trust & Security → Daemon API → Architecture → Roadmap → Testing → Engineering Journal *(post-Batch G)*
- [ ] `pkg/p2pnet` library reference (godoc-style or hand-written guides)
- [ ] Use-case guides integrated into the site (GPU inference, IoT, game servers - see Launch Content below)
- [ ] Install page with platform-specific instructions (curl, brew, apt, Docker, source)
- [x] Blog section - 7 retroactive blog posts for Batches A-G (outcomes-focused) *(Batch G)*

**Website Deployment Model** (note for maintainers):

The website deploys from both `main` and `dev/next-iteration` via `.github/workflows/pages.yaml`. This was a deliberate decision (2026-02-23) to solve a real workflow problem: documentation and website content often update alongside code changes on the dev branch, but the code needs live testing on real hardware before merging to `main`. Since Hugo only builds from `website/` and `docs/`, untested Go code in `cmd/`, `pkg/`, `internal/` is completely irrelevant to the website build pipeline. The CI workflow (`.github/workflows/ci.yml`) handles Go build/test separately on both branches.

Why this approach was chosen over alternatives:
- **Cherry-picking doc commits to main**: breaks down when a single commit touches both code and docs; manual overhead on every push.
- **Separate website branch**: adds workflow complexity for no real benefit at current scale.
- **Deploy from dev only**: the current approach keeps `main` as a deployment source too, so merging to main still triggers a deploy.

When to reconsider: if the project grows to have multiple active development branches (not just one dev branch), consider moving to a dedicated `website` branch or a separate Hugo deployment pipeline that pulls `docs/` and `website/` from whichever branch is most current. The Hugo build is fast (<5s) so running it on multiple branch pushes has negligible CI cost.

**AI-Agent Discoverability ([llms.txt](https://llmstxt.org/) spec)**:
- [x] `/llms.txt` - markdown index of the project: name, summary, links to detailed doc pages. ~200 tokens for an AI agent to understand the entire project. Hand-crafted static file in `website/static/llms.txt`. *(2026-02-20)*
- [x] `/llms-full.txt` - all site content concatenated into a single markdown file (243KB). Auto-generated by `tools/sync-docs` from README + all docs. One URL paste gives an AI agent full project context. *(2026-02-20)*
- [ ] `.md` variants of every page - any page URL + `.md` suffix returns clean markdown (Hugo already has the source, just serve it as a static file alongside the HTML)
- [ ] Adopted by 600+ sites including Anthropic, Cloudflare, Stripe, Cursor, Hugging Face
- [ ] **WebMCP** ([Google + Microsoft, W3C](https://developer.chrome.com/blog/webmcp-epp)) - watch for future relevance. Protocol for AI agents to *interact* with websites via structured tool contracts (Declarative API for HTML forms, Imperative API for JS). Early preview in Chrome 146 Canary (Feb 2026). Not immediately relevant for a docs site, but valuable if shurli.io adds interactive features (e.g., invite code generator, service discovery dashboard)

**Release Manifest & Upgrade Endpoint**:
- [ ] CI generates static `releases/latest.json` on every tagged release - deployed as part of the Hugo site
- [ ] Manifest contains version, commit, date, checksums, and per-platform download URLs for all mirrors:
  ```json
  {
    "version": "1.2.0",
    "commit": "abc1234",
    "date": "2026-03-15",
    "binaries": {
      "linux-amd64": {
        "github": "https://github.com/.../shurli-linux-amd64.tar.gz",
        "gitlab": "https://gitlab.com/.../shurli-linux-amd64.tar.gz",
        "ipfs": "bafybeiabc123...",
        "sha256": "..."
      }
    }
  }
  ```
- [ ] `shurli upgrade` fetches `shurli.io/releases/latest.json` (not GitHub API directly)
- [ ] Install script fetches the same manifest - one source of truth for all consumers
- [ ] Fallback order in binary and install script: GitHub → GitLab → IPFS gateway

**Distribution Resilience** (gradual rollout):

The domain (`shurli.io`) is the anchor. DNS is managed under our control. Every user-facing URL goes through the domain, never directly to a third-party host. If any host disappears, one DNS record change restores service.

| Layer | GitHub (primary) | GitLab (mirror) | IPFS (fallback) |
|-------|-----------------|-----------------|-----------------|
| Source code | Primary repo | Push-hook mirror | - |
| Release binaries | GitHub Releases | GitLab Releases (GoReleaser) | Pinned on Filebase |
| Static site | GitHub Pages | GitLab Pages | Pinned + DNSLink ready |
| DNS failover | CNAME → GitHub Pages | Manual flip to GitLab Pages | Manual flip to IPFS gateway |

Rollout phases:
1. **Phase 1**: GitHub Pages only. CNAME `shurli.io` → GitHub. Simple, free, fast.
2. **Phase 2**: Mirror site + releases to GitLab Pages + GitLab Releases. Same Hugo CI. Manual DNS failover if needed (CNAME swap on DNS provider).
3. **Phase 3**: IPFS pinning on every release. DNSLink TXT record pre-configured. Nuclear fallback if both GitHub and GitLab die - flip CNAME to IPFS gateway.

Deliverables:
- [ ] Git mirror to GitLab via push hook or CI (source code resilience)
- [ ] GoReleaser config to publish to both GitHub Releases and GitLab Releases
- [ ] GitLab Pages deployment (`.gitlab-ci.yml` for Hugo build)
- [ ] CI step: `ipfs add` release binaries + site → pin on [Filebase](https://filebase.com/) (S3-compatible, 5GB free)
- [ ] DNSLink TXT record at `_dnslink.shurli.io` pointing to IPNS key (pre-configured, activated on failover)
- [ ] Document failover runbook: which DNS records to change, in what order, for each failure scenario
- [ ] **Canonical source links** (`shurli.io/source/*`): Hugo redirect rules that map source file paths to the current primary repo. All docs and website pages link through `shurli.io/source/` instead of directly to any git host. When the primary moves (GitHub to GitLab, etc.), update one redirect config - all existing links (including search engine cached URLs and LLM training snapshots) resolve via the domain we control. sync-docs rewrite logic (`tools/sync-docs/config.go` `githubBase` constant) replaced with domain-relative `/source/` paths. GitHub repo stays alive as a read-only mirror so any old indexed URLs still resolve.

**Package Managers & Binaries**:
- [ ] Set up [GoReleaser](https://goreleaser.com/) config (`.goreleaser.yaml`) - publish to GitHub Releases + GitLab Releases
- [ ] GitHub Actions workflow: on tag push, build binaries for Linux/macOS/Windows (amd64 + arm64)
- [ ] Publish to GitHub Releases with Ed25519-signed checksums (release key in repo)
- [ ] Homebrew tap: `brew install shurlinet/tap/shurli`
- [ ] One-line install script: `curl -sSL get.shurli.io | sh` - fetches `releases/latest.json`, detects OS/arch, downloads binary (GitHub → GitLab → IPFS fallback), verifies checksum, installs to `~/.local/bin` or `/usr/local/bin`
- [ ] APT repository for Debian/Ubuntu
- [ ] AUR package for Arch Linux
- [ ] Docker image + `docker-compose.yml` for containerized deployment

**Embedded / Router Builds** (OpenWRT, Ubiquiti, GL.iNet, MikroTik):
- [ ] GoReleaser build profiles: `default` (servers/desktops, `-ldflags="-s -w"`, ~25MB) and `embedded` (routers, + UPX compression, ~8MB)
- [ ] Cross-compilation targets: `linux/mipsle` (OpenWRT), `linux/arm/v7` (Ubiquiti EdgeRouter, Banana Pi), `linux/arm64` (modern routers)
- [ ] Optional build tag `//go:build !webrtc` to exclude WebRTC/pion (~2MB savings) for router builds
- [ ] OpenWRT `.ipk` package generation for opkg install
- [ ] Guide: *"Running Shurli on your router"* - OpenWRT, Ubiquiti EdgeRouter, GL.iNet travel routers
- [ ] Binary size budget: default ≤25MB stripped, embedded ≤10MB compressed. Current: 34MB full → 25MB stripped → ~8MB UPX.

**Auto-Upgrade** (builds on commit-confirmed pattern from Phase 4C):
- [ ] `shurli upgrade --check` - fetch `shurli.io/releases/latest.json`, compare version with running binary, show changelog
- [ ] `shurli upgrade` - download binary from manifest (GitHub → GitLab → IPFS fallback), verify Ed25519 checksum, replace binary, restart. Manual confirmation required.
- [ ] `shurli upgrade --auto` - automatic upgrade via systemd timer or cron. Downloads, verifies, applies with commit-confirmed safety:
  1. Rename current binary to `shurli.rollback`
  2. Install new binary, start with `--confirm-timeout 120`
  3. New binary runs health check (relay reachable? peers connectable?)
  4. If healthy → auto-confirm, delete rollback
  5. If unhealthy or no confirmation → systemd watchdog restarts with rollback binary
  6. **Impossible to brick a remote node** - same pattern Juniper has used for 20+ years
- [ ] `relay-server upgrade --auto` - same pattern for relay VPS. Especially critical since relay is remote.
- [ ] Version mismatch warning - when `shurli status` shows peers running different versions, warn with upgrade instructions
- [ ] Relay version announcement - relay broadcasts its version to connected peers via libp2p Identify `UserAgent`. Peers see "relay running v1.2.0, you have v1.1.0, run `shurli upgrade`"

**Use-Case Guides & Launch Content**:
- [ ] Guide: OpenClaw Gateway - *"Remote Access to OpenClaw Gateway in 60 Seconds"* (one-command setup with `--openclaw`, no VPN account or port forwarding needed)
- [ ] Guide: GPU inference - *"Access your home GPU from anywhere through Starlink CGNAT"*
- [ ] Guide: IoT/smart home remote access (Home Assistant, cameras behind CGNAT)
- [ ] Guide: Media server sharing (Jellyfin/Plex with friends via invite flow)
- [ ] Guide: Game server hosting (Minecraft, Valheim through CGNAT)
- [ ] Guide: Game/media streaming (Moonlight/Sunshine tunneling, latency characteristics)
- [ ] Latency/throughput benchmarks (relay vs direct via DCUtR)
- [ ] Multi-GPU / distributed inference documentation (exo, llama.cpp RPC)
- [ ] Blog post / demo: phone → relay → home 5090 → streaming LLM response

**Automation & Integration Guides**:
- [ ] Guide: *"Scripting & Automation with Shurli"* - daemon API, headless onboarding, Python SDK usage
- [ ] Guide: *"Containerized Deployments"* - Docker, env-based config, non-interactive join
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
# Home: shurli daemon
# Remote: shurli proxy home ollama 11434
# Then: curl http://localhost:11434/api/generate -d '{"model":"llama3",...}'
```

**Result**: Zero-dependency install on any platform. Compelling use-case content drives adoption.

---

### Phase 11: Desktop Gateway Daemon + Private DNS

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Create multi-mode gateway daemon for transparent service access, backed by a private DNS zone on the relay that is never exposed to the public internet.

**Rationale**: Infrastructure-level features that make Shurli transparent - services accessed via real domain names, no manual proxy commands. The DNS resolver uses the `Resolver` interface from Phase 9.

**Deliverables**:

**Client-side Gateway**:
- [ ] `cmd/gateway/` - Gateway daemon with multiple modes
- [ ] **Mode 1**: SOCKS5 proxy (localhost:1080)
- [ ] **Mode 2**: Local DNS server (`.p2p` TLD)
- [ ] **Mode 3**: TUN/TAP virtual network interface (requires root)
- [ ] `/etc/hosts` integration for local name overrides
- [ ] Virtual IP assignment (10.64.0.0/16 range)
- [ ] Subnet routing - route entire LAN segments through tunnel (access printers, cameras, IoT without per-device install)
- [ ] Trusted network detection - auto-disable tunneling when already on home LAN

**Relay-side Private DNS** (pluggable `Resolver` backend from 4D):
- [ ] Lightweight DNS zone on the relay server (e.g., CoreDNS or custom)
- [ ] Exposed **only** via P2P protocol - never bound to public UDP/53
- [ ] Relay operator configures a real domain (e.g., `example.com`) pointing to the VPS IP
- [ ] Subdomains (`bob.example.com`, `home.example.com`) assigned on the relay, resolvable only within the P2P network
- [ ] Public DNS returns NXDOMAIN for all subdomains - they don't exist outside the network
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
6. Subdomains stay private - no DNS records ever created on public registrars

**Usage Examples**:
```bash
# Mode 1: SOCKS proxy (no root needed)
shurli-gateway --mode socks --port 1080
# Configure apps to use SOCKS proxy

# Mode 2: DNS server (queries relay's private DNS)
shurli-gateway --mode dns --port 53
# Resolves: home.example.com → virtual IP (via relay's private zone)

# Mode 3: Virtual network (requires root)
sudo shurli-gateway --mode tun --network 10.64.0.0/16
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

### Phase 12: Apple Multiplatform App

**Timeline**: 3-4 weeks
**Status**: 📋 In Progress
**Repository**: `github.com/shurlinet/shurli-ios` (separate repo, depends on `shurli-sdk-swift` from Phase 9E)

**Goal**: Native Apple multiplatform app (macOS/iOS/iPadOS/visionOS) with VPN-like functionality and beautiful visual pairing via dotbeam.

**Rationale**: Phone → relay → home GPU is the dream demo. Mobile closes the loop on "access your stuff from anywhere." The app consumes the Swift SDK (Phase 9E) - it contains zero daemon API plumbing, only UI and platform integration code.

**iOS Strategy**:
- **Primary**: NEPacketTunnelProvider (VPN mode)
  - Full TUN interface
  - Virtual network support
  - Frame as "self-hosted personal network" (like WireGuard)
- **Fallback**: SOCKS proxy app (if VPN rejected by Apple)
- **Apple Review Approach**: "Connect to your own devices via relay server"

**Visual Pairing: Constellation Code (dotbeam)**:
- Animated visual data transfer for invite code exchange between devices
- dotbeam library: colored dots in concentric rings, fountain-coded frames, camera decode
- Standalone repo: `github.com/satindergrewal/dotbeam` (Go + JS, ~75-80% camera decode accuracy achieved)
- Replaces boring QR with Apple-style flowing particle aesthetic
- Not required for functionality (text invite codes work), but elevates the pairing UX

**Deliverables**:
- [ ] SwiftUI app targeting macOS/iOS/iPadOS/visionOS
- [ ] NEPacketTunnelProvider (iOS/macOS)
- [ ] Mobile-optimized config UI
- [ ] QR code scanning for `shurli invite` codes
- [ ] dotbeam visual pairing (optional, beautiful alternative to QR)
- [ ] Background connection maintenance
- [ ] Battery optimization
- [ ] Per-app SDK for third-party integration

**User Experience**:
```
iOS/Android App Config:
├─ Scan QR Code (from shurli invite)
├─ Or enter invite code: ABCX-7KMN-P2P3
└─ Connect Button

Once connected:
- SSH clients work: ssh user@home
- Browsers work: http://laptop:8080
- Native apps work: Plex connects to home.grewal:32400
- Chat with home LLM via Ollama API
```

---

### Phase 13: Federation - Network Peering

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
- [ ] Multi-network client support - single client connected to multiple independent networks simultaneously

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

### Phase 14: Advanced Naming Systems (Optional)

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Pluggable naming architecture supporting multiple backends. Uses the `Resolver` interface from Phase 9.

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
# ~/.shurli/names.yaml
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
Register on Ethereum: shurli register grewal --chain ethereum
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

### Privacy Narrative - Shurli's Moat

Shurli is not a cheaper version of existing VPN tools. It's the **self-sovereign alternative** for people who care about owning their network.

| | **Shurli** | **Centralized VPN** |
|---|---|---|
| **Accounts** | None - no email, no OAuth | Required (Google, GitHub, etc.) |
| **Telemetry** | Zero - no data leaves your network | Coordination server sees device graph |
| **Control plane** | None - relay only forwards bytes | Centralized coordination server |
| **Key custody** | You generate, you store, you control | Keys managed via their control plane |
| **Source** | Fully open, self-hosted | Open source client, proprietary control plane |

> *"For people who don't want to trust a company with their network topology."*

### Target Audiences (in order of receptiveness)

1. **r/selfhosted** - Already run services at home, hate port forwarding, value self-sovereignty
2. **Starlink/CGNAT users** - Actively searching for solutions to reach home machines
3. **AI/ML hobbyists** - Home GPU + remote access is exactly their problem
4. **Privacy-conscious developers** - Won't use centralized VPN services because of the coordination server

### Launch Strategy

1. **Hacker News post**: *"Show HN: Shurli - self-hosted P2P tunnels through Starlink CGNAT (no accounts, no vendor)"*
2. **r/selfhosted post**: Focus on SSH + XRDP + GPU inference through CGNAT
3. **Blog post**: *"Access your home GPU from anywhere through Starlink CGNAT"*
4. **Demo video**: Phone → relay → home 5090 → streaming LLM response
5. **Comparisons**: Honest architectural comparison posts

### Community Infrastructure (set up at or before launch)

- [ ] **Discord server** - Real-time community channel for support, feedback, development discussion. Link from website nav bar and README
- [ ] **Showcase page** (`/showcase`) - Curated gallery of real-world Shurli deployments. Static JSON data file, rendered as cards. Add when users start sharing their setups (post-launch)
- [ ] **Shoutouts page** (`/shoutouts`) - Testimonials from users. Static JSON, rendered as quote cards with attribution. Add when genuine testimonials exist (post-launch)
- [ ] **Trust & Security page** (`/docs/trust`) - ✅ Created. Threat model, security controls, vulnerability reporting with response SLAs, audit history. Living document, community PRs welcome
- [ ] **Separate `Shurli-trust` repo** - Structured threat model in YAML format (MITRE ATLAS-based). Community can submit PRs to improve threat coverage. Rendered on the website. Fallback: if GitHub goes down, mirror to GitLab (same pattern as code distribution resilience)
- [ ] **Binary verification** - Ed25519-signed checksums + cosign/Sigstore signing for Go binaries. Stronger trust signal than most P2P projects offer
- [ ] **Integrations page** (`/integrations`) - Curated catalog of what works with Shurli: services (Ollama, Jellyfin, Home Assistant, Minecraft, Sunshine/Moonlight), platforms (Docker, systemd, launchd), clients (SSH, XRDP, any TCP). Each entry: name, category, one-liner, config snippet, "works out of the box" badge. Inspired by OpenClaw's integrations page. Add progressively as use-case guides ship.

---

## Phase 15+: Ecosystem & Polish

**Timeline**: Ongoing
**Status**: 📋 Conceptual

**Potential Features**:
- [ ] **Relay VPS obsolescence** - every publicly-reachable Shurli node relays for its authorized peers. No special nodes, no central coordination. The relay VPS becomes obsolete, not just optional.
- [ ] Multi-lingual website and documentation (Hugo i18n). Priority languages: Spanish, Chinese (Simplified), Hindi, Portuguese, Japanese, French, German. Community-contributed translations welcome.
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
- [ ] Admin endpoint IP privacy - redact or hash peer IPs in `/v1/peers/connected` response (show transport type or `/24` subnet only). Admin role inherently has this access, so this is a nice-to-have anonymity enhancement, not a security fix. May be unnecessary if admin trust model is accepted as-is.
- [ ] IPv6 transport testing and documentation
- [ ] Split tunneling (route only specific traffic through tunnel)
- [ ] Decentralized analytics - on-device network intelligence using statistical anomaly detection (moving average, z-score). No centralized data collection. Each node monitors its own connection quality, predicts relay degradation, and auto-switches paths before failure. Data never leaves the node. Inspired by Nokia AVA's "bring code to where the data is" philosophy. Implementation: gonum for statistics, pure Go, no ML frameworks needed for initial phases
- [ ] Store-carry-forward for offline peers (DTN pattern) - queue encrypted service requests at relay for delivery when target peer reconnects. Transforms "connection refused" into "delivery delayed." Not for interactive sessions (SSH), but valuable for commands, config pushes, and file transfers.

**Researched and Set Aside** (Feb 2026): The following techniques were evaluated through cross-network research (Bitcoin, Tor, I2P, Briar, Ethereum, biology, game theory, information theory) and consciously shelved. They have minimum viable network sizes (10-20+ peers) that exceed Shurli's typical 2-5 peer deployments. At small scale, they add overhead without benefit. Future maintainers: if Shurli grows to networks of 20+ peers with multiple relays, revisit these. Full analysis with sources preserved in project memory (`decentralized-network-research.md`).
- Vivaldi network coordinates (latency prediction - needs 20+ peers to converge)
- CRDTs for partition-tolerant peer state (needs frequent partitions to justify complexity)
- Slime mold relay optimization (needs dense relay graph, not 2-3 paths)
- Simultaneous multi-transport / Briar pattern (revisit when device compute makes keepalive negligible)
- Shannon entropy connection fingerprinting (useful concept, premature at current scale)
- Percolation threshold monitoring (meaningful only at 10+ peers)
- Pulse-coupled oscillator keepalive sync (valuable with many mobile peers)
- VRF-based fair relay assignment (needed only with multiple competing relays)
- Erlay / Minisketch set reconciliation (bandwidth savings only above 8+ peers)

**Phase 7: ZKP Privacy Layer** - STATUS: DONE

Zero-knowledge proofs applied to Shurli's identity and authorization model. Peers prove group membership, relay authorization, and reputation without revealing their identity.

**Implementation: gnark PLONK + Ethereum KZG ceremony (2026-02-26)**

The four use cases are confirmed and the architecture is designed. Implementation will use [gnark](https://github.com/Consensys/gnark) PLONK with Ethereum's KZG trusted setup ceremony (141,416 participants). gnark is production-ready, pure Go, and maintained by ConsenSys. The KZG ceremony's 1-of-141,416 honest participant assumption is practically secure for Shurli's use cases.

**Why gnark PLONK + KZG**: Pure Go, production-ready, actively maintained, no FFI dependencies. Preserves single-binary sovereignty. The Ethereum KZG ceremony (141,416 participants) provides a strong trust foundation. For Shurli's authorization proofs, the practical security of "1 of 141K participants was honest" is more than sufficient.

**What we evaluated**:
- **Ring signatures**: Metadata analysis can narrow down the signer. zk-SNARKs provide mathematically absolute privacy. Ring signatures are not zero-knowledge.
- **Halo 2 via FFI (Rust CGo bindings)**: Achieves trustless ZKPs (no ceremony), but introduces Rust toolchain dependency, cross-compilation complexity, and two-language audit surface. Violates sovereignty principle.
- **gnark Vortex**: ConsenSys's experimental lattice-based transparent setup. Not production-ready. Worth watching as a future upgrade path.

**What we're watching** (checked after each phase completion):
1. **Halo 2 in Go** - if a native Go implementation appears, it would be a strict upgrade (removes ceremony dependency). Zero activity as of 2026-02-26.
2. **gnark Vortex** - ConsenSys's lattice-based transparent setup. Would remove ceremony dependency entirely if it reaches production.
3. **gnark IPA backend** - ConsenSys has no current plans, but would enable Halo 2-style proofs.
4. **Any new trustless ZKP library in Go** - would be evaluated as a potential upgrade from KZG ceremony.

**The four use cases**:
- [x] **Anonymous authentication** - prove "I hold a key in the authorized set" without revealing which key. ConnectionGater validates the proof, never learns which peer connected. Eliminates peer ID as a tracking vector.
- [x] **Anonymous relay authorization** - prove relay access rights without revealing identity to the relay. Relay validates membership proof, routes traffic, builds no connection graph.
- [x] **Privacy-preserving reputation** - prove reputation above a threshold without revealing exact score, join date, or relay history. Prevents reputation data from becoming a surveillance tool. Builds on PeerManager scoring (Phase 5-L).
- [x] **Private DHT namespace membership** - prove "I belong to the same namespace as you" without revealing the namespace name to non-members. Narrowest use case (exposure only to directly-dialed peers), implemented last.

**Architecture decisions** (stable regardless of proving system):
- Hash-based membership: MiMC/Poseidon(ed25519_pubkey) as Merkle leaf. Avoids Ed25519 curve mismatch with SNARK-native fields (~600x overhead if Ed25519 arithmetic done inside circuit).
- Merkle tree of identity commitments = the authorized_keys set.
- ZK circuit proves: "I know a value pk such that Hash(pk) is a leaf in the tree with root R." Verifier sees root + proof only.
- For ~500 members: ~10K-50K constraints, ~100-500ms proof generation, ~500-1000 byte proofs, ~2-5ms verification.

**Background: Zcash trusted setup evolution** (context for the upgrade path we're watching):
- Sprout (2016): Groth16, 6 participants. Legitimate trust concern.
- Sapling (2018): Powers of Tau, 90+ participants. 1-of-90 honest assumption.
- Orchard/NU5 (2022): Halo 2 - NO trusted setup. IPA-based (Pedersen commitments). No toxic waste. Trust math only. If this arrives in Go, it's a strict upgrade from our KZG approach.

**RLN - Anonymous Relay Rate-Limiting** (eligible now - ZKP Privacy Layer complete):

Rate-Limiting Nullifier for anonymous anti-spam on relays. Based on Shamir's Secret Sharing: each member's secret defines a line; revealing 1 point per epoch = anonymous; revealing 2+ points = secret reconstructable, spammer auto-detected. No judge, no blockchain needed. Waku (Status.im) uses RLN; libp2p community discussion active (specs issue #374).

**Seam already in code**: `internal/zkp/rln_seam.go` defines the types and interface. Ready for circuit implementation.

- [ ] Reimplement RLN in pure Go using gnark primitives (avoids Rust FFI dependency from Waku's go-zerokit-rln)
- [ ] Anonymous relay reservation rate-limiting (relay validates ZK proof, doesn't know who's connecting)
- [ ] Off-chain membership tree (relay operator maintains from authorized_keys, no blockchain)
- [ ] Global spammer detection (nullifier exposure propagates across relays)

**Anonymous NetIntel** (future - requires pubsub):

Anonymous presence and network intelligence announcements. Peers share reachability grade, NAT type, and connection quality without revealing identity. Extends the existing `/shurli/presence/1.0.0` protocol with ZKP membership proofs.

**Dependency**: Requires go-libp2p-pubsub to catch up to go-libp2p v0.47.0+ (currently pinned to v0.39.1). Direct push works today; gossip forwarding for anonymous announcements needs pubsub.

- [ ] ZKP-authenticated presence announcements (prove membership without revealing peer ID)
- [ ] Anonymous reachability grade sharing (network health without identity exposure)
- [ ] Gossip-based anonymous forwarding (when pubsub dependency resolves)

**Protocol & Security Evolution**:
- [ ] MASQUE relay transport ([RFC 9298](https://www.ietf.org/rfc/rfc9298.html)) - HTTP/3 relay alternative to Circuit Relay v2. Looks like standard HTTPS to DPI, supports 0-RTT session resumption for instant reconnection. Could coexist with Circuit Relay v2 as user-selectable relay transport.
- [ ] Post-quantum cryptography - hybrid Noise + ML-KEM ([FIPS 203](https://csrc.nist.gov/pubs/fips/203/final)) handshakes for quantum-resistant key exchange. Implement when libp2p adopts PQC. Design cipher suite negotiation now (cryptographic agility).
- [ ] WebTransport transport - replace WebSocket anti-censorship layer with native QUIC-based WebTransport. Lower overhead, browser-compatible, native datagrams.
- [ ] Zero-RTT proxy connection resume - QUIC session tickets for instant reconnection after network switch (WiFi→cellular). No existing P2P tool provides this.
- [ ] Hardware-backed peer identity - store peer private keys in TPM 2.0 (Linux) or Secure Enclave (macOS/iOS). No existing P2P tool provides this.
- [ ] eBPF/XDP relay acceleration - kernel-bypass packet forwarding for high-throughput relay deployments. DDoS mitigation at millions of packets/sec.
- [ ] W3C DID-compatible identity - export peer IDs in [Decentralized Identifier](https://www.w3.org/TR/did-1.1/) format (`did:key`, `did:peer`) for interoperability with verifiable credential systems.
- [ ] Formal verification of invite/join protocol state machine - mathematically prove correctness of key exchange. Possible with TLA+ model or Kani (Rust).

**Security Hardening (deferred from 2026-03-06 audit)**:
- [ ] TOTP replay prevention - per-vault "last used TOTP counter" rejecting codes at or before last accepted. Closes the ~90s replay window. Standard RFC 6238 practice.
- [ ] Unseal wire protocol nonce - per-session nonce to prevent application-layer replay of recorded encrypted streams to different relay instances with same keys.
- [ ] Stronger session token machine binding - require `/etc/machine-id` or `IOPlatformUUID`, refuse sessions on systems with only hostname fallback, clear error message.
- [ ] Admin endpoint path allowlist - replace `isInvitePath()` prefix matching with explicit exact path allowlist. Prevents path traversal if new `/v1/pair/` endpoints are added.
- [ ] Circuit ACL denial log rate-limiting - under attack, Info-level denial logs flood. Rate-limit logging (every Nth after threshold).
- [ ] DHT routing table health check - periodic check that routing table contains expected seed peers. Alert if seeds disappear (eclipse attack indicator).
- [x] Per-network ephemeral identity - HKDF domain-separated per-namespace identities to prevent cross-network peer ID correlation. `DeriveNamespaceKey()` in `internal/identity/seed.go`.
- [ ] Challenge store memory-pressure backoff - at >800/1000 ZKP challenge capacity, increase per-peer rate from 5s to 30s for graceful degradation.
- [ ] Probation slot preemption - evict oldest probation peer at limit rather than denying new. Newest is more likely a legitimate pairing.
- [ ] Admin socket audit log - log PID/UID of socket connections via `SO_PEERCRED` for forensic traceability.
- [ ] Invite code channel binding - bind PAKE session to relay's peer ID in HKDF info to prevent relay swap attacks.
- [ ] Password complexity enforcement - minimum entropy check (12+ chars or breach list) for vault/identity passwords. Nice-to-have. Relay-pushed password policies considered but architecturally heavy (relay doesn't store passwords).
- [ ] Authorized_keys integrity monitoring - hash file after each mutation, verify on load to detect out-of-band tampering.
- [ ] Token store memory limit - configurable maximum on total groups to prevent unbounded memory growth from admin-created long-TTL tokens.

**Performance & Language**:
- [ ] Selective Rust rewrite of hot paths - proxy loop, relay forwarding, SOCKS5 gateway via FFI. Zero GC, zero-copy, ~1.5x throughput improvement. Evaluate when performance metrics justify it.
- [ ] Rust QUIC library evaluation - QUIC-based P2P libraries (QUIC multipath, ~90% NAT traversal), pure Rust QUIC implementations, formally verified QUIC (AWS s2n-quic)
- [ ] Go GC tuning - profile at 100+ concurrent proxies, set GOGC, evaluate memory allocation patterns in proxy loop

---

## Timeline Summary

| Phase | Duration | Status |
|-------|----------|--------|
| Phase 1: Configuration | ✅ 1 week | Complete |
| Phase 2: Authentication | ✅ 2 weeks | Complete |
| Phase 3: keytool CLI | ✅ 1 week | Complete |
| Phase 4A: Core Library + UX | ✅ 2-3 weeks | Complete |
| Phase 4B: Frictionless Onboarding | ✅ 1-2 weeks | Complete |
| **Phase 4C: Core Hardening & Security** | ✅ 6-8 weeks | Complete (Batches A-I, Post-I-1, Post-I-2, Pre-Phase 5 Hardening) |
| **Phase 5: Network Intelligence** | ✅ | Complete (5-K mDNS, 5-L PeerManager, 5-M Presence) |
| **Phase 6: ACL + Relay Security + Client Invites** | ✅ | Complete (Macaroons, passphrase-sealed vault, remote unseal, TOTP + Yubikey 2FA) |
| **Phase 7: ZKP Privacy Layer** | ✅ | Complete (gnark PLONK + Ethereum KZG, anonymous auth, reputation proofs, namespace membership) |
| **Phase 8: Identity Security + Remote Admin** | ✅ | Complete (Unified BIP39 seed, encrypted identity, remote admin over P2P, MOTD/goodbye, session tokens) |
| Phase 9: Plugins, SDK & First Plugins | 📋 5-8 weeks | Planned (9A-9E sub-phases) |
| Phase 10: Distribution & Launch | 📋 1-2 weeks | Planned |
| Phase 11: Desktop Gateway + Private DNS | 📋 2-3 weeks | Planned |
| Phase 12: Apple Multiplatform App | 📋 3-4 weeks | Planned (separate repo: `shurli-ios`) |
| Phase 13: Federation | 📋 2-3 weeks | Planned |
| Phase 14: Advanced Naming | 📋 2-3 weeks | Planned (Optional) |
| Phase 15+: Ecosystem | 📋 Ongoing | Conceptual |

**Priority logic**: Harden the core (done) -> network intelligence (done) -> ACL and relay security (done) -> ZKP privacy layer (done) -> identity security + remote admin (done) -> make it extensible with real plugins (Go interfaces + Python SDK + Swift SDK) -> distribute with use-case content (GPU, IoT, gaming) -> transparent access (gateway, DNS) -> expand (Apple app with visual pairing -> federation -> naming).

**Repository strategy**: Non-Go SDKs and consumer apps live in separate GitHub repos. See "SDK & App Repository Strategy" section under Phase 9 for the full table.

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
- `shurli validate` / `relay-server validate` catches bad configs before applying
- Config archive stores last 5 configs; `config rollback` restores any of them
- `relay-server apply --confirm-timeout` auto-reverts if not confirmed (no lockout)
- systemd watchdog restarts relay within 60s if health check fails
- `/healthz` endpoint returns relay status (monitorable by Prometheus/UptimeKuma)
- `shurli status` shows connection state, peer status, and latency
- `shurli daemon` runs in background; scripts can query status and list services via Unix socket
- `shurli join --non-interactive` works in Docker containers and CI/CD pipelines without TTY
- go-libp2p upgraded to latest in both main module and relay-server (no version gap)
- AutoNAT v2 enabled - node correctly identifies per-address reachability (IPv4 vs IPv6)
- Resource Manager replaces `WithInfiniteLimits()` - per-peer connection/bandwidth caps enforced
- Connection setup latency reduced from 5-15s toward 1-3s (persistent reservation + warmup)
- QUIC transport used by default (3 RTTs vs 4 for TCP)
- `shurli --version` shows build version, commit hash, and build date
- Peers exchange version info via libp2p Identify UserAgent - `shurli status` shows peer versions
- Protocol versioning policy documented (backwards-compatible within major version)
- Integration tests verify real libp2p host-to-host connectivity in `go test`

**Phase 5 Success**:
- mDNS discovers LAN peers within 5 seconds without relay
- PeerManager tracks and scores peers, persists across restarts
- Network change triggers re-upgrade from relay to direct (the Batch I finding)
- Presence protocol exchanges reachability data via direct push + gossip forwarding

**Phase 6 Success** (ACL + Relay Security + Client Invites):
- Client-generated invite code works async: generate in NZ, friend joins in AU hours later
- Macaroon capability tokens with attenuation: admin can delegate limited invite permission
- Relay sealed/unsealed with passphrase-sealed security pattern: watch-only when sealed, full ops when unsealed
- Remote unseal over P2P: no SSH needed for daily relay management
- TOTP + Yubikey challenge-response 2FA on relay unseal
- Seed phrase recovery: 24 words regenerate relay identity and all keys
- Admin/member roles enforced on authorized_keys

**Phase 7 Success** (ZKP Privacy Layer):
- Anonymous set membership proof for authorized_keys (gnark PLONK + Ethereum KZG)
- Peers prove they hold valid macaroon capabilities without revealing identity or specific permissions
- Anonymous relay authorization for peer relays
- Privacy-preserving reputation proofs (score above threshold without revealing exact score)

**Phase 8 Success** (Identity Security + Remote Admin):
- Single BIP39 seed derives identity, vault, and ZKP keys (one backup covers everything)
- Identity keys encrypted at rest on all nodes (Argon2id + XChaCha20-Poly1305)
- Full relay management over P2P (24+ admin endpoints, no SSH required for daily ops)
- MOTD/goodbye announcements signed and verified (Ed25519), defense against prompt injection
- Session tokens allow password-free daemon restarts, lock/unlock gates sensitive ops
- `shurli doctor` validates installation health and auto-fixes issues

**Phase 9 Success** (Plugins, SDK):
- Third-party code can implement custom `Resolver`, `Authorizer`, and stream middleware
- Event hooks fire for peer connect/disconnect and auth decisions
- New CLI commands require <30 lines of orchestration (bootstrap consolidated)
- File transfer works between authorized peers (first plugin)
- `shurli daemon --ollama` auto-detects and exposes Ollama (service template plugin)
- `shurli wake <peer>` sends magic packet (WoL plugin)
- Transfer speed saturates relay bandwidth; resume works after interruption
- SDK documentation published with working plugin examples
- `shurli discover <peer>` returns list of exposed services with tags
- Python SDK works: `pip install shurli-sdk` - connect to remote service in <10 lines
- Swift SDK works: SPM import `ShurliSDK` - connect to daemon and list peers in <10 lines of Swift
- Swift SDK validated as foundation for Phase 12 Apple app (zero API plumbing needed in app repo)
- `shurli invite --headless` outputs JSON; `shurli join --from-env` reads env vars

**Phase 10 Success** (Distribution & Launch):
- `shurli.io` serves a Hugo documentation site with landing page, guides, and install instructions
- Site auto-deploys on push to `main` via GitHub Actions
- `shurli.io/llms.txt` returns markdown index; `shurli.io/llms-full.txt` returns full site content - AI agents can understand the project in ~200 tokens
- `curl get.shurli.io | sh` installs the correct binary for the user's OS/arch
- `shurli.io/releases/latest.json` manifest is the single source of truth for all upgrade/install consumers
- Binary and install script try GitHub → GitLab → IPFS in order (three-tier fallback)
- Source code, releases, and site mirrored to GitLab (push hook + GoReleaser + GitLab Pages)
- Release binaries pinned on IPFS (Filebase); DNSLink pre-configured for emergency failover
- Failover runbook documented: which DNS records to change for each failure scenario
- GoReleaser builds binaries for 9+ targets (linux/mac/windows × amd64/arm64 + linux/mipsle + linux/arm/v7)
- Embedded builds ≤10MB (UPX compressed), default builds ≤25MB (stripped)
- Homebrew tap works: `brew install shurlinet/tap/shurli`
- Docker image available
- Install-to-running in under 30 seconds
- `shurli upgrade` fetches manifest from `shurli.io`, downloads with fallback, verifies checksum
- `shurli upgrade --auto` with commit-confirmed rollback - impossible to brick remote nodes
- Relay announces version to peers; version mismatch triggers upgrade warning
- GPU inference use-case guide published
- Router deployment guide published (OpenWRT, Ubiquiti, GL.iNet)
- Blog post / demo published
- Scripting & automation guide published
- Containerized deployment guide published with working Docker compose examples
- Python SDK available on PyPI

**Phase 11 Success** (Desktop Gateway + Private DNS):
- Gateway daemon works in all 3 modes (SOCKS, DNS, TUN)
- Private DNS on relay resolves subdomains only within P2P network
- Public DNS queries for subdomains return NXDOMAIN (zero leakage)
- Native apps connect using real domain names (e.g., `home.example.com`)

**Phase 12 Success** (Apple Multiplatform App):
- SwiftUI app runs on macOS/iOS/iPadOS/visionOS
- QR code invite flow works mobile → desktop
- dotbeam visual pairing as optional beautiful alternative to QR
- NEPacketTunnelProvider working on iOS

**Phase 13 Success** (Federation):
- Two independent networks successfully federate
- Cross-network routing works transparently
- Trust model prevents unauthorized access

**Phase 14 Success** (Advanced Naming):
- At least 3 naming backends working (local, DHT, one optional)
- Plugin API documented and usable
- Migration path demonstrated when one backend fails

---

**Last Updated**: 2026-03-08
**Current Phase**: Phase 8 Complete. Phase 9 (Plugins, SDK & First Plugins) next.
**Phases**: 1-8 (complete), 9-14 (planned), 15+ (ecosystem)
**Next Milestone**: Phase 9 - Plugin Architecture, SDK & First Plugins (9A-9E: interfaces, file transfer, discovery, Python SDK, Swift SDK)
**Relay elimination**: Every-peer-is-a-relay shipped (Batch I-f). `require_auth` peer relays -> DHT discovery -> VPS becomes obsolete
