---
title: "Completed Work"
weight: 1
description: "All completed phases and batches: Configuration, Authentication, CLI, Core Library, Onboarding, and full Phase 4C hardening."
---

## Phase 1: Configuration Infrastructure

**Goal**: Externalize all hardcoded values to YAML configuration files.

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

## Phase 2: Key-Based Authentication

**Goal**: Implement SSH-style authentication using ConnectionGater and authorized_keys files.

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

## Phase 3: Enhanced Usability - keytool CLI (superseded)

**Goal**: Create production-ready CLI tool for managing Ed25519 keypairs and authorized_keys.

**Status**: Completed (keytool features merged into `shurli` subcommands in Phase 4C module consolidation; `cmd/keytool/` deleted)

All keytool functionality now lives in `shurli` subcommands: `shurli whoami` (peerid), `shurli auth add` (authorize), `shurli auth remove` (revoke), `shurli auth list`, `shurli auth validate` (validate). Key generation happens via `shurli init`.

---

## Phase 4A: Core Library & Service Registry

**Goal**: Transform Shurli into a reusable library and enable exposing local services through P2P connections.

**Deliverables**:
- [x] Create `pkg/p2pnet/` as importable package
  - [x] `network.go` - Core P2P network setup, relay helpers, name resolution
  - [x] `service.go` - Service registry and management
  - [x] `proxy.go` - Bidirectional TCP-to-Stream proxy with half-close
  - [x] `naming.go` - Local name resolution (name to peer ID)
  - [x] `identity.go` - Ed25519 identity management
- [x] Extend config structs for service definitions
- [x] Update sample YAML configs with service examples
- [x] Refactor to `cmd/` layout with single Go module
- [x] Tested: SSH, XRDP, generic TCP proxy all working across LAN and 5G
- [x] **UX Streamlining**:
  - [x] Single binary - merged home-node into `shurli daemon`
  - [x] Standard config path - auto-discovery (`./shurli.yaml` -> `~/.config/shurli/config.yaml` -> `/etc/shurli/config.yaml`)
  - [x] `shurli init` - interactive setup wizard (generates config, keys, authorized_keys)
  - [x] All commands support `--config <path>` flag
  - [x] Unified config type (one config format for all modes)

---

## Phase 4B: Frictionless Onboarding

**Goal**: Eliminate manual key exchange and config editing. Get two machines connected in under 60 seconds.

**Deliverables**:
- [x] `shurli invite` - generate short-lived invite code (encodes relay address + peer ID)
- [x] `shurli join <code>` - accept invite, exchange keys, auto-configure, connect
- [x] QR code output for `shurli invite` (scannable by mobile app later)
- [x] `shurli whoami` - show own peer ID and friendly name for sharing
- [x] `shurli auth add/list/remove` - manage authorized peers
- [x] `shurli relay add/list/remove` - manage relay addresses without editing YAML
- [x] Flexible relay address input - accept `IP:PORT` or bare `IP` (default port 7777) in addition to full multiaddr
- [x] QR code display in `shurli init` (peer ID) and `shurli invite` (invite code)

**Security hardening** (done as part of 4B):
- [x] Sanitize authorized_keys comments (prevent newline injection)
- [x] Sanitize YAML names from remote peers (prevent config injection)
- [x] Limit invite/join stream reads to 512 bytes (prevent OOM DoS)
- [x] Validate multiaddr before writing to config YAML
- [x] Use `os.CreateTemp` for atomic writes (prevent symlink attacks)
- [x] Reject hostnames in relay input - only IP addresses accepted (no DNS resolution / SSRF)
- [x] Config files written with 0600 permissions

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
```

---

## Phase 4C: Core Hardening & Security

**Goal**: Harden every component for production reliability. Fix critical security gaps, add self-healing resilience, implement test coverage, and make the system recover from failures automatically.

### Security (Critical)

- [x] Relay resource limits - replace `WithInfiniteLimits()` with configurable `WithResources()` + `WithLimit()`. Defaults tuned for SSH/XRDP (10min sessions, 64MB data).
- [x] Auth hot-reload - daemon API `POST /v1/auth` and `DELETE /v1/auth/{peer_id}` reload `authorized_keys` at runtime.
- [x] Per-service access control - `AllowedPeers` field on each service restricts which peers can connect.
- [x] Rate limiting on incoming connections and streams - libp2p ResourceManager enabled. OS-level: iptables SYN flood protection (50/s) and UDP rate limiting (200/s).
- [x] QUIC source address verification - reverse path filtering (rp_filter=1), SYN cookies for TCP flood protection.
- [x] Key file permission check on load - refuse to load keys with permissions wider than 0600
- [x] Service name validation - DNS-label format enforced (1-63 lowercase alphanumeric + hyphens)

### libp2p Upgrade (Critical)

- [x] go-libp2p v0.47.0 - AutoNAT v2, smart dialing, QUIC improvements, Resource Manager
- [x] AutoNAT v2 - per-address reachability testing with nonce-based dial verification
- [x] Smart dialing - address ranking, QUIC prioritization, sequential dial with fast failover
- [x] QUIC as preferred transport - 1 fewer RTT on connection setup (3 RTTs vs 4 for TCP)
- [x] Version in Identify - `libp2p.UserAgent("shurli/<version>")` set on all hosts
- [x] Private DHT - migrated from IPFS Amino DHT to private shurli DHT (`/shurli/kad/1.0.0`)

### Self-Healing & Resilience

Inspired by Juniper JunOS, Cisco IOS, Kubernetes, systemd, MikroTik:

- [x] **Config validation** - `shurli config validate` parses config, checks key file, verifies relay address
- [x] **Config archive** - auto-saves last-known-good config on successful startup. Atomic write.
- [x] **Config rollback** - `shurli config rollback` restores from last-known-good archive
- [x] **Commit-confirmed pattern** (Juniper JunOS / Cisco IOS) - `shurli config apply <new-config> --confirm-timeout 5m` applies config and auto-reverts if not confirmed. **Prevents permanent lockout on remote relay.**
- [x] **systemd watchdog integration** - `sd_notify("WATCHDOG=1")` every 30s with health check
- [x] **Health check HTTP endpoint** - relay exposes `/healthz` with JSON: peer ID, version, uptime, connected peers
- [x] **`shurli status` command** - version, peer ID, config path, relay addresses, authorized peers, services, names

### Batch Deliverables

**Batch A - Reliability**:
- [x] `DialWithRetry()` - exponential backoff retry (1s -> 2s -> 4s) for proxy dial
- [x] TCP dial timeout - 10s for local service, 30s context for P2P stream
- [x] DHT bootstrap in proxy command - Kademlia DHT (client mode) for direct peer discovery
- [x] `[DIRECT]`/`[RELAYED]` connection path indicators in logs
- [x] DCUtR hole-punch event tracer

**Batch B - Code Quality**:
- [x] Deduplicated bidirectional proxy - `BidirectionalProxy()` + `HalfCloseConn` interface (was 4 copies, now 1)
- [x] Sentinel errors - 8 sentinel errors across 4 packages
- [x] Build version embedding - `shurli version`, ldflags injection
- [x] Structured logging with `log/slog`

**Batch E - New Capabilities**:
- [x] `shurli status` - local-only info command
- [x] `/healthz` HTTP endpoint on relay-server
- [x] `shurli invite --non-interactive` - bare invite code to stdout, progress to stderr
- [x] `shurli join --non-interactive` - reads code from CLI arg, env var, or stdin

**Batch F - Daemon Mode**:
- [x] `shurli daemon` - long-running P2P host with Unix socket HTTP API
- [x] Cookie-based authentication (32-byte random hex, `0600` permissions, rotated per restart)
- [x] 15 API endpoints with JSON + plain text format negotiation
- [x] Auth hot-reload, dynamic proxy management
- [x] P2P ping, traceroute, resolve - standalone + daemon API
- [x] Service files: systemd + launchd

**Batch G - Test Coverage & Documentation**:
Combined coverage: **80.3%** (unit + Docker integration). Relay-server binary merged into shurli.
- [x] 96 test functions covering CLI commands
- [x] All 15 API handlers tested
- [x] Docker integration tests with coverage
- [x] Engineering journal with 43 ADRs
- [x] Website with Hugo + Hextra, 10 blog posts, 40+ SVG diagrams

**Batch H - Observability**:
- [x] Prometheus `/metrics` endpoint (opt-in via config)
- [x] libp2p built-in metrics exposed (swarm, hole-punch, AutoNAT, relay, rcmgr)
- [x] Custom shurli metrics (proxy bytes/connections/duration, auth counters, hole-punch stats, API timing)
- [x] Audit logging - structured JSON via slog for security events
- [x] Grafana dashboard - 37 panels across 6 sections

### Pre-Batch I Items

**Pre-I-a: Build & Deployment Tooling**:
- [x] Makefile with build, test, clean, install, service management
- [x] Service install for Linux (systemd) and macOS (launchd)
- [x] `make check` - generic local checks runner from `.checks` file
- [x] `make push` - runs checks before git push

**Pre-I-b: PAKE-Secured Invite/Join Handshake**:
Upgraded the invite/join token exchange from cleartext to an encrypted handshake inspired by WPA3's SAE. The relay sees only opaque encrypted bytes during pairing. Zero new dependencies.

- [x] Ephemeral X25519 DH + token-bound HKDF-SHA256 key derivation + XChaCha20-Poly1305 AEAD
- [x] Invite versioning: v1 = PAKE-encrypted, v2 = relay pairing code
- [x] v2 invite codes encode namespace for DHT network auto-inheritance
- [x] 19 PAKE tests + 11 invite code tests

**Pre-I-c: Private DHT Networks**:
- [x] Config option: `discovery.network: "my-crew"` for isolated peer groups
- [x] DHT prefix becomes `/shurli/<namespace>/kad/1.0.0`
- [x] Nodes with different namespaces speak different protocols and cannot discover each other
- [x] Validation: DNS-label safe (lowercase alphanumeric + hyphens, 1-63 chars)

### Batch I: Adaptive Multi-Interface Path Selection

Probes all available network interfaces at startup, tests each path to peers, picks the best, and continuously monitors for network changes. Path ranking: direct IPv6 > direct IPv4 > STUN-punched > peer relay > VPS relay. Zero new dependencies.

- [x] **I-a: Interface Discovery & IPv6 Awareness** - `DiscoverInterfaces()` enumerates all network interfaces with global unicast classification
- [x] **I-b: Parallel Dial Racing** - parallel racing replaces sequential 45s worst-case. First success wins.
- [x] **I-c: Path Quality Visibility** - `PathTracker` with per-peer path info: type, transport, IP version, RTT. `GET /v1/paths` API endpoint.
- [x] **I-d: Network Change Monitoring** - event-driven detection of interface/address changes with callbacks
- [x] **I-e: STUN-Assisted Hole-Punching** - zero-dependency RFC 5389 STUN client. NAT type classification (none/full-cone/address-restricted/port-restricted/symmetric).
- [x] **I-f: Every-Peer-Is-A-Relay** - any peer with a global IP auto-enables circuit relay v2 with conservative limits

### Post-I-1: Frictionless Relay Pairing

Eliminates manual SSH + peer ID exchange for relay onboarding. Relay admin generates pairing codes, each person joins with one command.

- [x] **v1 cleartext deleted** - zero downgrade surface
- [x] **Extended authorized_keys format** - key=value attributes: `expires=<RFC3339>`, `verified=sha256:<prefix>`
- [x] **In-memory token store** (relay-side) - SHA-256 hashed tokens, constant-time comparison, max 3 failed attempts before burn
- [x] **v2 invite code format** - 16-byte token, relay address + namespace encoded. Shorter than v1 (126 vs 186 chars)
- [x] **Connection gater enrollment mode** - probationary peers (max 10, 15s timeout) during active pairing
- [x] **SAS verification (OMEMO-style)** - 4-emoji + 6-digit numeric fingerprint. Persistent `[UNVERIFIED]` badge until verified.
- [x] **Relay pairing protocol** - `/shurli/relay-pair/1.0.0` stream protocol. 8-step flow.
- [x] **`shurli relay pair`** - generates pairing codes with `--count N`, `--ttl`, `--namespace`, `--expires`
- [x] **Daemon-first commands** - `shurli ping` and `shurli traceroute` try daemon API first, fall back to standalone
- [x] **Reachability grade** - A (public IPv6), B (public IPv4 or hole-punchable NAT), C (port-restricted NAT), D (symmetric NAT/CGNAT), F (offline)

Zero new dependencies. Binary size unchanged at 28MB.

### Post-I-2: Peer Introduction Protocol

Relay-mediated peer introduction with HMAC group commitment. When a new peer joins via relay pairing, the relay pushes introductions to existing peers in the same group.

- [x] `/shurli/peer-notify/1.0.0` protocol for relay-pushed introductions
- [x] HMAC-SHA256(token, groupID) proves token possession during pairing
- [x] Relay notifies all group members when a new peer joins

### Pre-Phase 5 Hardening

8 cross-network bug fixes discovered during live hardware testing. Fixed before Phase 5 implementation.

- [x] 5 NoDaemon test isolation fixes (tests conflicting with live daemon)
- [x] 3 stale Homebrew tap references updated (satindergrewal -> shurlinet)
- [x] Cross-network connectivity verified across 3 machines

---

## Phase 5: Network Intelligence

Smarter peer discovery, lifecycle management, and network-wide presence. Three sub-phases: mDNS for instant LAN discovery, PeerManager for reliable reconnection, and NetIntel for presence announcements with gossip forwarding.

### 5-K: mDNS Local Discovery

Zero-config peer discovery on the local network. When two Shurli nodes are on the same LAN, mDNS finds them without DHT lookups or relay bootstrap. Uses platform-native DNS-SD APIs (dns_sd.h via CGo on macOS/Linux) to cooperate with the system mDNS daemon instead of competing for the multicast socket.

- [x] zeroconf.RegisterProxy for mDNS service advertising (dnsaddr= TXT records)
- [x] Platform-native browse via dns_sd.h (mDNSResponder on macOS, avahi on Linux)
- [x] Zeroconf fallback for Windows, FreeBSD, and CGO_ENABLED=0 builds
- [x] mDNS-discovered peers checked against authorized_keys (ConnectionGater enforces)
- [x] Config option: `discovery.mdns_enabled: true` (default: true)
- [x] Explicit DHT routing table refresh on network change events
- [x] Dedup, semaphore-limited concurrent connects, 10-minute address TTL

### 5-L: PeerManager

Background reconnection of authorized peers with exponential backoff. Watches the authorized_keys file for changes and maintains connections to all known peers.

- [x] Periodic sweep of authorized peers with configurable interval (default: 30s)
- [x] Exponential backoff per peer (30s -> 60s -> 120s -> 300s max)
- [x] Authorized_keys file watcher with debounced reload
- [x] Graceful shutdown with in-flight connection draining

### 5-M: NetIntel (Network Intelligence Presence)

Lightweight presence protocol using direct streams. Each peer publishes its presence (addresses, capabilities, uptime) to connected peers at regular intervals. Gossip forwarding with TTL propagates presence through the network without requiring direct connections to every peer.

- [x] `/shurli/netintel/1.0.0` stream protocol for presence announcements
- [x] Periodic publish (default: 5 minutes) with immediate publish on address change
- [x] Gossip forwarding: fanout=3, maxHops=3, dedup by message hash
- [x] In-memory peer presence table with 15-minute TTL
- [x] GossipSub activation deferred until go-libp2p-pubsub supports go-libp2p v0.47+

### Industry References

- **Juniper JunOS `commit confirmed`**: Apply config, auto-revert if not confirmed. Prevents lockout on remote devices.
- **Cisco IOS `configure replace`**: Atomic config replacement with automatic rollback on failure.
- **MikroTik Safe Mode**: Track all changes; revert everything if connection drops.
- **Kubernetes liveness/readiness probes**: Health endpoints that trigger automatic restart on failure.
- **systemd WatchdogSec**: Process heartbeat - systemd restarts if process stops responding.

### libp2p Specification References

- **Circuit Relay v2**: [Specification](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md) - reservation-based relay with configurable resource limits
- **DCUtR**: [Specification](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md) - Direct Connection Upgrade through Relay (hole punching coordination)
- **AutoNAT v2**: [Specification](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md) - per-address reachability testing with amplification prevention
- **Hole Punching Measurement**: [Study](https://arxiv.org/html/2510.27500v1) - 4.4M traversal attempts, 85K+ networks, 167 countries, ~70% success rate

---

## Phase 6: ACL + Relay Security + Client Invites

Production-ready access control, relay security hardening, and async client-generated invites. 7 batches, 19 new files, ~3,655 lines of new code. The relay can now be sealed at rest, unsealed remotely, and invite permissions are cryptographically attenuation-only.

### 6-A: Role-Based Access Control

Three-tier access model for relay operations:

- [x] `role` attribute on `authorized_keys` entries (`admin` / `member`)
- [x] First peer paired with relay auto-promoted to `role=admin` (if no admins exist)
- [x] Role display in `shurli auth list` with `[admin]`/`[member]` badges
- [x] Invite policy config: `admin-only` (default) / `open`

### 6-B: Macaroon Core Library

HMAC-chain capability tokens. Zero external dependencies (stdlib `crypto/hmac`, `crypto/sha256`).

- [x] `Macaroon` struct with New, AddFirstPartyCaveat, Verify, Clone, Encode/Decode
- [x] Caveat language parser with 7 types: `service`, `group`, `action`, `peers_max`, `delegate`, `expires`, `network`
- [x] `DefaultVerifier()` with fail-closed design
- [x] Attenuation-only: each caveat chains a new HMAC-SHA256, making removal cryptographically impossible
- [x] 22 macaroon tests + 10 caveat tests

### 6-C: Macaroon Integration + Attenuation-Only Invites

Async invite deposits with attenuation-only permissions.

- [x] `DepositStore` with Create/Get/Consume/Revoke/AddCaveat/List/CleanExpired/Count
- [x] Deposit states: pending, consumed, revoked, expired (with auto-expiry on access)
- [x] 4 new relay admin endpoints for invite management
- [x] CLI: `shurli relay invite create/list/revoke/modify`
- [x] Attenuation-only: admin can restrict or revoke before consumption, but can never widen permissions

### 6-D: TOTP Library

RFC 6238 time-based one-time passwords. Zero external dependencies.

- [x] Generate, Validate (with skew window), NewSecret, FormatProvisioningURI
- [x] 11 tests including RFC 6238 test vectors

### 6-E: Passphrase-Sealed Relay Vault

Protects relay root key material at rest.

- [x] Argon2id KDF (time=3, memory=64MB, threads=4) + XChaCha20-Poly1305 encryption
- [x] Sealed (watch-only): routes traffic, serves introductions, cannot authorize new peers
- [x] Unsealed (time-bounded): full operations, processes invite deposits, auto-reseals on timeout
- [x] Hex-encoded seed phrase recovery (32 bytes as 24 hex-pair words)
- [x] Root key zeroed from memory on seal
- [x] 5 new relay admin endpoints for vault management
- [x] CLI: `shurli relay vault init/seal/unseal/status`
- [x] 14 vault tests

### 6-F: Remote Unseal Over P2P

Admin can unseal the relay remotely without SSH.

- [x] `/shurli/relay-unseal/1.0.0` P2P protocol
- [x] Admin-only access check, iOS-style escalating lockout (4 free, 1m/5m/15m/1h, permanent block)
- [x] Prometheus metrics: `shurli_vault_unseal_total{result}`, `shurli_vault_unseal_locked_peers` gauge
- [x] CLI: `shurli relay unseal --remote <name|peer-id|multiaddr>` (short name resolution)
- [x] 11 unseal tests (wire format, lockout escalation, permanent block, message formatting)

### 6-G: Yubikey HMAC-SHA1

Optional hardware 2FA via ykman CLI (zero C dependencies).

- [x] Availability detection, challenge-response, graceful fallback
- [x] 6 tests
