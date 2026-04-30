---
title: "Completed Work"
weight: 1
description: "All completed phases and batches: Configuration, Authentication, CLI, Core Library, Onboarding, Phase 4C hardening, Phase 5 Network Intelligence, Phase 6 ACL + Relay Security, Phase 7 ZKP Privacy Layer, Phase 8 Identity Security + Remote Admin, Phase 8B Per-Peer Data Grants, Phase 9A-9B, Chaos Testing, Plugin Architecture, E14, Bandwidth Budgets, Grant Receipt Protocol, Phase 10 Distribution (partial), FT-Y Speed Optimization."
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
- [x] Create `pkg/sdk/` as importable package
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
  - [x] Standard config path - auto-discovery (`./shurli.yaml` -> `~/.shurli/config.yaml` -> `/etc/shurli/config.yaml`)
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
$ shurli invite --as home
=== Invite Code (expires in 10m0s) ===
AEQB-XJKZ-M4NP-...
[QR code displayed]
Waiting for peer to join...

# Machine B (laptop)
$ shurli join AEQB-XJKZ-M4NP-... --as laptop
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
- [x] 18 API endpoints with JSON + plain text format negotiation
- [x] Auth hot-reload, dynamic proxy management
- [x] P2P ping, traceroute, resolve - standalone + daemon API
- [x] Service files: systemd + launchd

**Batch G - Test Coverage & Documentation**:
Combined coverage: **80.3%** (unit + Docker integration). Relay-server binary merged into shurli.
- [x] 96 test functions covering CLI commands
- [x] All 18 API handlers tested
- [x] Docker integration tests with coverage
- [x] Engineering journal with 43 ADRs
- [x] Website with Hugo + Hextra, 10 blog posts, 40+ SVG diagrams

**Batch H - Observability**:
- [x] Prometheus `/metrics` endpoint (opt-in via config)
- [x] libp2p built-in metrics exposed (swarm, hole-punch, AutoNAT, relay, rcmgr)
- [x] Custom shurli metrics (proxy bytes/connections/duration, auth counters, hole-punch stats, API timing)
- [x] Audit logging - structured JSON via slog for security events
- [x] Grafana dashboard - 56 panels across 11 sections

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

---

## Phase 7: ZKP Privacy Layer

Zero-knowledge proof privacy layer using gnark PLONK on BN254. Peers prove "I'm authorized" without the relay learning which peer they are. 27 new files, 18 modified, ~91 tests. Two new dependencies: gnark v0.14.0, gnark-crypto v0.19.0 (pure Go).

### 7-A: ZKP Foundation

Poseidon2 Merkle tree, membership circuit, prover/verifier, key management.

- [x] Native + circuit Poseidon2 hash wrappers (BN254 parameters: width=2, 6 full rounds, 50 partial)
- [x] Sorted Merkle tree builder with power-of-2 padding, max depth 20 (1M+ peers)
- [x] PLONK membership circuit: 22,784 SCS constraints, 520-byte proofs
- [x] Root extension for trees with depth < 20 (pad through unused levels)
- [x] KZG SRS generation with filesystem caching
- [x] Proving key (~2 MB) and verifying key (~33.5 KB) serialization
- [x] High-level prover and verifier with public-only witness support
- [x] 37 tests + 7 benchmarks across 5 test files
- [x] ZKPConfig added to SecurityConfig and RelaySecurityConfig

### 7-B: Anonymous Relay Authorization

Challenge-response protocol for anonymous authentication over libp2p streams.

- [x] `/shurli/zkp-auth/1.0.0` binary wire protocol (4-phase handshake)
- [x] Single-use challenge nonces with 30-second TTL, cryptographic randomness
- [x] Relay ZKP handler with stream processing and deadline management
- [x] Client-side ZKP auth (stream-based proof generation)
- [x] `POST /v1/zkp/tree-rebuild` admin endpoint (vault-gated)
- [x] `GET /v1/zkp/tree-info` admin endpoint (always available)
- [x] 9 new Prometheus metrics (prove, verify, auth, tree, challenges)
- [x] 15 tests (7 challenge store + 8 wire protocol)

### 7-C: Private Reputation

Range proofs on peer reputation scores. Prove "my score >= threshold" without revealing the exact score.

- [x] Deterministic `ComputeScore`: 0-100, four components (availability, latency, path diversity, tenure)
- [x] Range proof circuit: 27,004 SCS constraints (+4,220 over membership for range comparison)
- [x] `AnonymousMode` and `ZKPProof` fields on `NodeAnnouncement`
- [x] RLN extension point: types + interface for future anonymous rate limiting
- [x] 5 new Prometheus metrics (range prove/verify, anonymous announcements)
- [x] 25 tests (14 scoring + 11 range proof including 2 end-to-end PLONK)

### 7-D: BIP39 Seed-Derived Deterministic Keys

Deterministic PLONK key generation from BIP39 seed phrases. Solves the key incompatibility problem discovered during physical testing.

- [x] Pure-stdlib BIP39: generate, validate, seed derivation (256-bit entropy, 24-word mnemonic)
- [x] `SetupKeysFromSeed`: `SHA256(mnemonic)` -> gnark `WithToxicSeed` -> deterministic SRS -> same keys anywhere
- [x] `shurli relay zkp-setup` command with `--seed` flag and interactive prompt
- [x] `GET /v1/zkp/proving-key` and `GET /v1/zkp/verifying-key` relay API endpoints
- [x] ProvingKey/VerifyingKey naming convention (renamed from PK/VK throughout)
- [x] 14 tests (11 BIP39 + 3 seed determinism)

### Phase 7 Key Numbers

| Metric | Value |
|--------|-------|
| Membership circuit constraints | 22,784 SCS |
| Range proof circuit constraints | 27,004 SCS |
| Proof size | 520 bytes |
| Full auth round-trip (internet) | ~1.8s proving + 2-3ms verification |
| Prove time | ~1.8s |
| Verify time | ~2-3ms |
| Circuit compile | ~70ms |
| Proving key | ~2 MB |
| Verifying key | ~33.5 KB |
| New Prometheus metrics | 14 |
| New/modified files | 27 new, 18 modified |
| Tests | ~91 |

---

## Phase 8: Identity Security + Remote Admin

Unified BIP39 seed architecture, encrypted identity keys, session tokens, full remote admin over P2P, and MOTD/goodbye protocol. 22+ new files, 27+ modified files. Physically tested across 3 nodes (laptop, home-node, relay VPS).

### 8-A: Unified BIP39 Seed

One 24-word mnemonic derives everything via HKDF domain separation.

- [x] BIP39 24-word mnemonic generation (256-bit entropy)
- [x] HKDF domain separation: `shurli/identity/v1` -> Ed25519 key, `shurli/vault/v1` -> vault root key
- [x] SRS derivation from seed -> ZKP keys (deterministic PLONK setup)
- [x] `shurli recover --seed` recovers identity from seed phrase
- [x] `shurli relay recover` recovers relay identity + vault + session from seed

### 8-B: SHRL Encrypted Identity

All identity keys are password-encrypted at rest. No unencrypted keys.

- [x] SHRL format: `[SHRL][version:1][salt:16][nonce:24][ciphertext]`
- [x] Argon2id KDF (time=3, memory=64MB, threads=4) + XChaCha20-Poly1305
- [x] Old raw `identity.key` files rejected with clear error message
- [x] `shurli change-password` re-encrypts with new password (atomic write)
- [x] Same-password rejection on change-password

### 8-C: Session Tokens

Machine-bound auto-decrypt for daemon auto-start without password.

- [x] SHRS format with machine-bound encryption (HKDF from install-random + machine ID)
- [x] `shurli lock` - runtime daemon state only, does NOT delete .session
- [x] `shurli unlock` - verify password, unlock sensitive ops
- [x] `shurli session refresh` - rotate token with fresh crypto material
- [x] `shurli session destroy` - revoke auto-start on this machine
- [x] macOS machine ID via IOPlatformUUID (ioreg), Linux via /etc/machine-id

### 8-D: Remote Admin over P2P

All 24 relay admin endpoints accessible over libp2p streams.

- [x] `/shurli/relay-admin/1.0.0` protocol (replaces relay-unseal protocol)
- [x] `--remote <peer-id|name|multiaddr>` flag on all relay subcommands
- [x] Admin role check via authorized_keys
- [x] Local-only path blocklist (vault-init and totp-uri blocked over P2P)
- [x] Relay auto-generates BIP39 seed on first `relay serve`

### 8-E: MOTD/Goodbye Protocol

Ed25519-signed relay operator announcements.

- [x] `/shurli/relay-motd/1.0.0` protocol
- [x] Wire format: `[version][type][msg-len][msg][timestamp][Ed25519 signature]`
- [x] 3-stage goodbye lifecycle: set, retract, shutdown (with grace period)
- [x] Client-side signature verification, timestamp bounds, sanitization
- [x] Persisted goodbyes with signature re-verification on load
- [x] Auto-push to newly connected peers

### 8-F: CLI Enhancements

- [x] `shurli doctor` - health check + auto-fix (completions, man page, config)
- [x] `shurli completion` - bash, zsh, fish (user-local install by default)
- [x] `shurli man` - troff man page (user-local install by default)
- [x] `--skip-seed-confirm` flag (skips quiz, keeps mandatory password)

### Phase 8 Physical Testing

34 verification items across 3 sessions on 3 physical nodes:
- 30 physically tested (ALL PASS)
- 4 covered by unit tests only
- 2 bugs found and fixed (same-password acceptance, completion/man sudo requirement)

---

## Phase 8B: Per-Peer Data Grants

**Timeline**: 2026-03-20 to 2026-03-22

Replaced binary `relay_data=true` with time-limited, per-peer capability grants using macaroon tokens. Node-level enforcement as the true security boundary.

### Phase A - Node-Level Grant Store

- [x] `GrantStore` with HMAC-integrity persistence, monotonic version counter
- [x] Stream-level enforcement in `OpenPluginStream` and `handleServiceStreamInner`
- [x] CLI: `shurli auth grant/revoke/extend/grants` with man pages and completions
- [x] 30s re-verify during active transfers
- [x] Share-grant separation with CLI warnings
- [x] L4 audit (4 rounds, 12 fixes). Physical retest 10/10 PASS

### Phase R - Relay Time-Limited Grants

- [x] Replaces binary `relay_data=true` with time-limited grant store on relay
- [x] Physical retest 9/9 PASS

### Phase B - Token Delivery + Presentation

- [x] B1: GrantPouch (holder-side), P2P delivery protocol (`/shurli/grant/1.0.0`), offline queue
- [x] B2: Binary grant header on plugin streams (4-byte overhead). Physical retest 12/12 PASS
- [x] B3: Multi-hop delegation with attenuation-only model. Physical retest 8/8 PASS
- [x] B4: Auto-refresh protocol (background refresh at 10% remaining). 5 rounds L4 audit

### Phase C - Notification Subsystem

- [x] `NotificationSink` interface with non-blocking router and event dedup
- [x] 8 event types, 3 built-in sinks (LogSink, DesktopSink, WebhookSink)
- [x] Pre-expiry warnings, `shurli notify test/list`

### Phase D - Hardening

- [x] Integrity-chained audit log (HMAC-SHA256 chain). `shurli auth audit [--verify]`
- [x] Configurable cleanup interval, per-peer ops rate limiter (10/min)
- [x] Protocol version on wire messages (downgrade protection)
- [x] 3 rounds self-review, 8 bugs fixed. 25/25 PASS -race
- [x] D1: Cancel propagation fix (physical test PASS) *(2026-03-24)*
- [x] D3: `SanitizeForDisplay()` applied to 8 display points, `sanitizeComment`/`sanitizeAttrValue` hardened *(2026-03-24)*

### Post-D UX + AI Agent CLI

- [x] `shurli auth pouch [--json]` (receiver-side grant visibility)
- [x] `--json` on ALL grant + notify commands
- [x] `shurli reconnect <peer> [--json]` (AI agent control)
- [x] Grant-aware backoff reset (relay notifies client on grant create)
- [x] Security: AppleScript injection defense, Router thread safety

---

## Phase 9A: Core Interfaces & Library Consolidation

**Goal**: Define public API contracts for third-party extensibility. Design-first: get interfaces right before building implementations.

**Core Interfaces** (`pkg/sdk/contracts.go`):
- [x] `PeerNetwork` - interface for core network operations (expose, connect, resolve, close, events)
- [x] `Resolver` - interface for name resolution with fallback chaining
- [x] `ServiceManager` - interface for service registration and dialing, with middleware support
- [x] `Authorizer` - interface for authorization decisions (pluggable auth)
- [x] `StreamMiddleware` / `StreamHandler` - functional middleware chain for stream handlers
- [x] `EventType` / `Event` / `EventHandler` - typed event system for network lifecycle
- Logger: Go stdlib `*slog.Logger` (no custom interface - deletion over addition)

**Extension Points**:
- [x] Constructor injection - `Network.Config` accepts optional `Resolver`
- [x] Event hook system - `OnEvent(handler)` with subscribe/unsubscribe, thread-safe `EventBus`
- [x] Stream middleware - `ServiceRegistry.Use(middleware)` wraps inbound stream handlers
- [x] Protocol ID formatter - `ProtocolID()` + `MustValidateProtocolIDs()` for init-time validation

**Library Consolidation** (completed in 9B):
- [x] `BootstrapAndConnect()` extracted to `pkg/sdk/bootstrap.go`
- [x] Centralized orchestration - `cmd_ping.go` and `cmd_traceroute.go` reduced by ~100 lines each
- [x] Package-level documentation in `pkg/sdk/doc.go`

---

## Phase 9B: File Transfer Plugin

**Goal**: Build file transfer as the first real plugin. Validates the `ServiceManager` and stream middleware interfaces from 9A. Also includes bootstrap extraction deferred from 9A.

Chunked P2P file transfer with content-defined chunking, integrity verification, compression, erasure coding, multi-source download, parallel streams, and AirDrop-style receive permissions. Hardened across FT-A through FT-H + audit-fix batches (1A, 1B, 2A-2C, 3A-3C, 4A, 4C).

### Core Transfer

- [x] `shurli send <file> <peer>` - fire-and-forget by default, `--follow` for inline progress, `--priority` for queue priority
- [x] `shurli transfers` - transfer inbox with `--watch` for live updates, `--history` for completed
- [x] FastCDC content-defined chunking (own implementation, adaptive targets 128K-2M)
- [x] BLAKE3 Merkle tree integrity (binary tree, odd-node promotion, root verification)
- [x] zstd compression on by default with incompressible detection and bomb protection (10x ratio cap)
- [x] SHFT v2 wire format (magic + version + flags + manifest + chunk data)
- [x] Receive modes: off / contacts (default) / ask / open / timed
- [x] Disk space checks before each chunk write
- [x] Atomic writes (write to `.tmp`, rename on completion)
- [x] `PluginPolicy` - transport-aware access control (LAN + Direct only, relay blocked by default)

### Download, Share & Browse

- [x] `shurli download <file> <peer>` - download from shared catalog (`--multi-peer` for RaptorQ)
- [x] `shurli browse <peer>` - browse peer's shared files
- [x] `shurli share add/remove/list` - manage shared files (`--to` for selective sharing)
- [x] `ShareRegistry` with persistent storage (`shares.json`, survives daemon restarts)
- [x] Browse protocol (`/shurli/file-browse/1.0.0`) and download protocol (`/shurli/file-download/1.0.0`)

### Transfer Queue & Management

- [x] `TransferQueue` with priority ordering and configurable concurrency (default: 3 active)
- [x] `shurli accept/reject <id>` - manage pending transfers (`--all` for batch)
- [x] `shurli cancel <id>` - cancel outbound transfer

### Advanced Features

- [x] Reed-Solomon erasure coding (auto-enabled on Direct WAN, 50% max overhead)
- [x] RaptorQ fountain codes for multi-source download (`/shurli/file-multi-peer/1.0.0`)
- [x] Parallel QUIC streams (adaptive: 1 for LAN, up to 4 for WAN)
- [x] Checkpoint-based resume (bitfield of received chunks, `.shurli-ckpt` files)
- [x] Recursive directory transfer with path sanitization
- [x] Transfer event logging (JSON lines, rotation) and notifications (desktop/command)
- [x] Per-peer rate limiting (10/min, fixed-window, silent rejection)
- [x] Compression ratio display in transfer output

### Audit-Fix Batches

- [x] Macaroon `Verify()` wired into pairing consume flow (1A)
- [x] Yubikey challenge-response wired into vault unseal (1B)
- [x] Transfer event logging with file rotation (2A)
- [x] Transfer notifications - desktop and command modes (2B)
- [x] Timed receive mode - temporarily open, reverts after duration (2C)
- [x] Batch accept/reject with `--all` flag (3A)
- [x] Parallel receive streams (3C)
- [x] AllowStandalone config wiring (4A)
- [x] Erasure config gap fix (4C)

### Security Hardening (FT-I, FT-J)

- [x] Full integration audit: all exported functions wired, config fields validated, CLI flags consistent
- [x] Command injection fix in notification command templates
- [x] Multi-peer filename sanitization
- [x] Transfer IDs changed from sequential to random hex (`xfer-<12hex>`)
- [x] Rate limiter applied to multi-peer request path
- [x] `TransferService.Close()` cleanup on daemon shutdown

**New P2P protocols** (4): `/shurli/file-transfer/2.0.0`, `/shurli/file-browse/1.0.0`, `/shurli/file-download/1.0.0`, `/shurli/file-multi-peer/1.0.0`

**New daemon API endpoints** (15 new, 38 total)

**Dependencies**: zeebo/blake3 (CC0), klauspost/compress/zstd (BSD-3), klauspost/reedsolomon (MIT), xssnick/raptorq (MIT)

**Test status**: 1100 tests across 21 packages, race detector clean.

---

## Post-9B: Chaos Testing and Network Hardening

**Timeline**: 4 days (2026-03-11 to 2026-03-14)
**Goal**: Physical chaos testing of all network transitions. Verify the daemon handles real-world network switches without restarts.

16 test cases across 5 ISPs and 3 VPN providers. 11 root causes found and fixed. 8 post-chaos flags investigated (6 fixed, 2 informational).

### Root Causes Fixed (FT-K through FT-P)

- [x] Black hole detector blocks valid transports after network switch
- [x] Probe targets relay server IP instead of peer IP
- [x] ForceDirectDial tries all peerstore addresses, cascade failure
- [x] mDNS relay cleanup fights remote PeerManager reconnect
- [x] CloseStaleConnections misses private IPs
- [x] Autorelay drops reservations on public networks
- [x] mDNS upgrade poisoned by UDP black hole state
- [x] CloseStaleConnections kills valid IPv6 during DAD window
- [x] Autorelay 1-hour backoff prevents re-reservation
- [x] ProbeUntil cooldown blocks reconnect after direct death
- [x] Swarm reports closed connection as live for 57s

### Post-Chaos Investigation (FT-R through FT-X)

- [x] Flag #1: TOCTOU race in mDNS + idle relay cleanup
- [x] Flag #5: Default gateway tracking for private IPv4-only switches
- [x] Flag #7: Dial worker cache poisoning workaround (3-part fix)
- [x] Flag #8: VPN tunnel interface detection
- [x] Autorelay tuning for static relays (faster reconnection)
- [x] ARCHITECTURE.md libp2p upstream overrides section (10 overrides documented)
- [x] Engineering journal ADR-S01 through ADR-S07

**libp2p upstream overrides** (10): TCP source binding, black hole reset, autorelay tuning (backoff/minInterval/bootDelay/minCandidates), ForceReachabilityPrivate, global IPv6 address factory, custom mDNS, route socket expansion (macOS), VPN tunnel detection, default gateway tracking.

---

## Post-9B: Plugin Architecture Shift

**Timeline**: 5 days (2026-03-16 to 2026-03-20)
**Goal**: Extract file transfer from inline code into a proper plugin. Build the Plugin interface that all future features follow.

This was a foundational restructuring. Every future feature (service discovery, Wake-on-LAN, gateway, console) drops in as a plugin implementing the same interface. No more wiring into core.

### Batch 1 - Plugin Framework (`pkg/plugin/`)

- [x] `Plugin` interface: Name, Version, Init, Start, Stop, Commands, Routes, Protocols, ConfigSection
- [x] `PluginContext` with capability grants (no raw Network/Host/credential access)
- [x] Registry: discovery, load, enable/disable
- [x] Lifecycle state machine: LOADING -> READY -> ACTIVE -> DRAINING -> STOPPED
- [x] `shurli plugin list/enable/disable/info/disable-all` CLI commands
- [x] Hot reload: enable/disable without daemon restart
- [x] Kill switch: `shurli plugin disable-all`
- [x] Plugin directory 0700 permission check

### Batch 2 - File Transfer Extraction (`plugins/filetransfer/`)

- [x] 9 CLI commands moved to plugin (send, download, browse, share, transfers, accept, reject, cancel, clean)
- [x] 14 daemon API endpoints moved to plugin
- [x] 12 types moved to plugin
- [x] 4 P2P protocols registered through plugin Start()
- [x] Plugin owns config section, state files (queue.json, shares.json with HMAC)
- [x] Core untouched: network, auth, identity, relay, ZKP all unchanged

### Batch 2.5 - Fix Tracked Findings

- [x] All 67 findings from 5 audit rounds resolved (none deferred)

### Batch 3 - Tests + Supervisor + Checkpointer

- [x] 81 test artifacts (unit + integration + fuzz)
- [x] Supervisor auto-restart with circuit breaker (3 crashes = auto-disable)
- [x] Transfer checkpoint/resume persistence
- [x] 7 fuzz targets, 209M total executions, zero crashes

### Batch 4a - Security Hardening

- [x] 43-vector threat analysis (traditional, build-time, AI-era)
- [x] 4-round audit with 12 additional fixes
- [x] Credential isolation verified (daemon keys, vault never in PluginContext)
- [x] Plugins cannot install other plugins (propagation chain break)

### Batch 4b - Physical Retest

- [x] 11/11 physical tests PASS (LAN send, relay send, browse, download, transfers, share, plugin list/enable/disable/disable-all, protocol unregister)
- [x] Smoke tests PASS (auth, resolve, traceroute, ping, services, plugins, status)
- [x] Performance baselines: LAN 3.3 MB/s, relay 682 KB/s, ping 21ms LAN / 186ms relay

**Architecture after shift**:
```
shurli binary
  core (network, auth, identity, daemon, CLI framework)
  pkg/plugin/          - Plugin interface + registry + supervisor
  pkg/sdk/          - Protocol library code (unchanged)
  plugins/filetransfer/ - First plugin (CLI, handlers, protocols)
```

**Three-layer evolution**: Layer 1 (compiled-in Go, current), Layer 2 (WASM via wazero, next), Layer 3 (AI-driven plugin generation, future).

**Test status**: 24/24 packages PASS, zero races. 7 fuzz targets clean.

### E14: Relay-First Onboarding

**Timeline**: 2026-03-23

Restructured onboarding so relay pairing is the primary path. Simplifies first-time setup.

- [x] Relay-first onboarding flow (relay pairing before peer-to-peer)
- [x] 12 commits on dev branch
- [x] 5 ACL issues deferred to macaroon migration

### Per-Peer Bandwidth Budgets

**Timeline**: 2026-03-24

Per-peer `bandwidth_budget` auth attribute overrides global default. LAN peers always exempt.

- [x] `shurli auth set-attr <peer> bandwidth_budget <value>` (local + relay admin API)
- [x] Pipeline: authorized_keys attr -> PeerAttrFunc -> PeerBudgetFunc -> bandwidthTracker override
- [x] Values: `unlimited`, `500MB`, `1GB`, etc. Config accepts human-readable strings
- [x] 3 audit rounds, 23 tests
- [x] Docs: COMMANDS.md, managing-network.md updated

---

## Phase 10: Distribution (partial)

**Timeline**: 2026-03-24
**Status**: Install script, release archives, relay-setup --prebuilt complete. GoReleaser, Homebrew, APT planned.

- [x] `tools/install.sh` - one-line installer (`curl -sSL get.shurli.io | sh`)
- [x] Colored ANSI output (terminal-aware), `--help`, `--yes`/`-y`, `--upgrade` flags
- [x] `SHURLI_METHOD`/`SHURLI_ROLE`/`SHURLI_UPGRADE`/`SHURLI_YES` env vars
- [x] `get.shurli.io` DNS redirect to `shurli.io/install`
- [x] GitHub Actions release archives (tar.gz per platform)
- [x] `relay-setup.sh --prebuilt` (install from release archive instead of source build)
- [x] `~/.shurli/` config path (migrated from `~/.config/shurli/`)
- [x] Website onboarding redesign (Homebrew-style install in hero, dual URLs)
- [x] Auto-generated release notes from conventional commits

---

## Grant Receipt Protocol

**Timeline**: 2026-03-26
**Status**: Complete

Relay-issued grant receipts give clients pre-transfer visibility into relay session budgets.

- [x] 62-byte binary grant receipts (HMAC-SHA256 signed, protocol: `/shurli/grant-receipt/1.0.0`)
- [x] Client-side grant cache with per-circuit byte tracking
- [x] Smart pre-transfer budget check (`checkRelayGrant()`)
- [x] Tier-aware session defaults (seed: 64 MB/10 min, self-hosted: 2 GB/2 hours)
- [x] Receiver busy retry with exponential backoff
- [x] CLI visibility via `shurli status`

---

## FT-Y: Transfer Speed Optimization

**Timeline**: 2026-03-31 to 2026-04-30
**Status**: Complete

Closed the speed gap between Shurli file transfer and SCP. Before: 5 MB/s. After: 110 MB/s send, 142 MB/s download (USB LAN). Multi-peer: 95.7 MB/s with 2 peers.

**Streaming Protocol (SHFT v2)**:
- [x] Streaming pipeline (zero full-file buffering)
- [x] 5-tier adaptive chunks (64K-4M based on file size)
- [x] Incompressible detection (3-chunk probe)
- [x] 8-stream parallel workers with work-stealing
- [x] Per-stripe Reed-Solomon erasure coding
- [x] Missing-chunk recovery from trailer manifest
- [x] Checkpoint/resume with crash recovery
- [x] Selective file rejection (--files/--exclude)
- [x] Progress bar (yt-dlp-style, EWMA speed/ETA)

**Multi-Peer Download**:
- [x] Interleaved symbol IDs with dynamic block claiming
- [x] Manifest verification across all peers
- [x] Slow-peer demotion

**Tail Slayer (Hedged Connections)**:
- [x] TS-1 through TS-6: hedged relay, bootstrap, manifest, control signals, path maintenance, failover, block coordination
- [x] Cancel protocol with 11 ms latency

**Budget-Aware Relay**:
- [x] Per-peer relay data budget (Host Wrapper, no fork)
- [x] Grant-ranked relay selection

**Persistent Proxy Service**:
- [x] CLI: add/list/remove/enable/disable with GATETIME stability detection

**Networking Fixes** (22 bugs fixed during physical testing):
- [x] mDNS dial worker pollution, IPv6 reconnect loop, resource manager exhaustion, relay backoff, PathDialer zombies, hole punch black hole, receiver busy, and more

**Engineering Journals**: 8 published (streaming protocol, multi-peer, Tail Slayer, budget-aware relay, Reed-Solomon, verified-LAN, persistent proxy, TCP-for-LAN experiment)

