---
title: "Planned & Future"
weight: 2
description: "Upcoming phases: plugins, distribution, desktop gateway, Apple multiplatform app, federation, and beyond."
---

## Relay Decentralization

After Phase 5 PeerManager provides the data:

- [ ] `require_auth` relay service - enable Circuit Relay v2 service on home nodes with `require_auth: true`
- [ ] DHT-based relay discovery - authorized relays advertise on DHT under well-known CID
- [ ] Multi-relay failover - health-aware selection based on connection quality scores
- [ ] Per-peer bandwidth tracking - feeds into relay quota warnings, PeerManager scoring, smart relay selection
- [ ] Bootstrap decentralization - hardcoded seed peers -> DNS seeds -> DHT peer exchange -> fully self-sustaining (same pattern as Bitcoin)
- [ ] **End goal**: Relay VPS becomes **obsolete** - not just optional. Every publicly-reachable Shurli node relays for its authorized peers. No special nodes, no central coordination

---

## ZKP Watching List

Checked after each phase completion:
1. **Halo 2 in Go** - if a native Go implementation appears, it would be a strict upgrade (removes ceremony dependency). Zero activity as of 2026-03-01.
2. **gnark Vortex** - ConsenSys's lattice-based transparent setup. Would remove ceremony dependency entirely if it reaches production.
3. **gnark IPA backend** - would enable Halo 2-style proofs in gnark.

**Deferred from Phase 7**:
- [ ] **Private DHT namespace membership** - prove namespace membership without revealing the namespace name (deferred to Phase 13 federation work)

---

## Phase 9: Plugin Architecture, SDK & First Plugins

**Goal**: Make Shurli extensible by third parties - and prove the architecture works by shipping real plugins. The plugins ARE the SDK examples.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring - without forking. Shipping real plugins alongside the architecture validates the design immediately and catches interface mistakes before third parties discover them.

### Phase 9A: Core Interfaces & Library Consolidation - DONE

**Timeline**: 1 week

Defined the public API contracts that third-party code depends on. Design-first: interfaces validated by the file transfer plugin (9B) before any third-party consumers.

**Core Interfaces** (`pkg/p2pnet/contracts.go`):
- [x] `PeerNetwork` - interface for core network operations (expose, connect, resolve, close, events)
- [x] `Resolver` - interface for name resolution with fallback chaining
- [x] `ServiceManager` - interface for service registration and dialing, with middleware support
- [x] `Authorizer` - interface for authorization decisions (pluggable auth)
- [x] `StreamMiddleware` / `StreamHandler` - functional middleware chain for stream handlers
- [x] `EventType` / `Event` / `EventHandler` - typed event system for network lifecycle
- Logger: uses Go stdlib `*slog.Logger` (no custom interface - deletion over addition)

**Extension Points**:
- [x] Constructor injection - `Network.Config` accepts optional `Resolver`
- [x] Event hook system - `OnEvent(handler)` with subscribe/unsubscribe, thread-safe `EventBus`
- [x] Stream middleware - `ServiceRegistry.Use(middleware)` wraps inbound stream handlers
- [x] Protocol ID formatter - `ProtocolID()` + `MustValidateProtocolIDs()` for init-time validation

**Library Consolidation** (completed in 9B):
- [x] `BootstrapAndConnect()` extracted to `pkg/p2pnet/bootstrap.go`
- [x] Centralized orchestration - `cmd_ping.go` and `cmd_traceroute.go` reduced by ~100 lines each
- [x] Package-level documentation in `pkg/p2pnet/doc.go`

### Phase 9B: File Transfer Plugin - DONE

**Timeline**: 3 weeks (core in 9B, hardened across FT-A through FT-H + audit-fix batches)

Chunked P2P file transfer with content-defined chunking, integrity verification, compression, erasure coding, multi-source download, parallel streams, and AirDrop-style receive permissions. 4 new P2P protocols, 15 new daemon API endpoints, 8 new CLI commands.

**Core Features**:
- [x] `shurli send <file> <peer>` - fire-and-forget (exits immediately), `--follow` for inline progress, `--priority` for queue priority
- [x] `shurli download <file> <peer>` - download from shared catalog, `--multi-peer` for RaptorQ multi-source
- [x] `shurli share add/remove/list` - manage shared files (`--to` for selective sharing)
- [x] `shurli browse <peer>` - browse peer's shared file catalog
- [x] `shurli transfers` - transfer inbox (`--watch`, `--history`, `--json`)
- [x] `shurli accept/reject <id>` - manage pending transfers (`--all` for batch)
- [x] `shurli cancel <id>` - cancel outbound transfer

**Architecture**:
- FastCDC content-defined chunking (own implementation, adaptive targets 128K-2M)
- BLAKE3 Merkle tree integrity (binary tree, odd-node promotion, root verification)
- zstd compression on by default with bomb protection (10x ratio cap)
- Reed-Solomon erasure coding (auto-enabled on Direct WAN, 50% max overhead)
- RaptorQ fountain codes for multi-source download from multiple peers
- Parallel QUIC streams (adaptive: 1 for LAN, up to 4 for WAN)
- Checkpoint-based resume (bitfield of received chunks, `.shurli-ckpt` files)
- Receive modes: off / contacts (default) / ask / open / timed
- Transfer queue with priority ordering and configurable concurrency
- Share registry with persistent storage (survives daemon restarts)
- Transfer event logging (JSON lines, rotation) and notifications (desktop/command)
- Per-peer rate limiting (10/min, silent rejection)
- `PluginPolicy` blocks relay transport by default (drives own-relay adoption)

**Security**: path traversal protection, resource exhaustion caps, disk space checks, random transfer IDs, compression bomb protection, command injection prevention in notifications.

**Dependencies**: zeebo/blake3 (CC0), klauspost/compress/zstd (BSD-3), klauspost/reedsolomon (MIT), xssnick/raptorq (MIT).

**Test status**: 1100 tests across 21 packages, race detector clean.

### Phase 9C: Service Discovery & Additional Plugins

**Timeline**: 1-2 weeks

Service discovery protocol and two more plugins that prove different interface patterns.

**Service Discovery Protocol**:
- [ ] New protocol `/shurli/discovery/1.0.0` - query a remote peer for their exposed services
- [ ] `shurli discover <peer>` CLI command
- [ ] Service tags in config: `tags: [gpu, inference]`

**Service Templates** (proves health middleware):
- [ ] `shurli daemon --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `shurli daemon --vllm` shortcut (auto-detects vLLM on localhost:8000)
- [ ] Health check middleware - verify local service is reachable before exposing

**Wake-on-LAN** (proves event hooks):
- [ ] `shurli wake <peer>` - send magic packet before connecting
- [ ] Event hook: auto-wake peer on connection attempt (optional)

### Phase 9D: Python SDK & Documentation

**Timeline**: 1-2 weeks
**Repository**: `shurlinet/shurli-sdk-python` (separate repo, ships to PyPI)

Ship the Python SDK and comprehensive documentation. The plugins from 9B/9C ARE the SDK examples.

**Python SDK** (`shurli-sdk`):
- [ ] Thin wrapper around daemon Unix socket API
- [ ] `pip install shurli-sdk`
- [ ] Async support (asyncio)
- [ ] Example: connect to a remote service in <10 lines of Python

**SDK Documentation**:
- [ ] `docs/SDK.md` - guide for building on `pkg/p2pnet`
- [ ] Example walkthroughs: file transfer, service templates, custom resolver, auth middleware

### Phase 9E: Swift SDK

**Timeline**: 1-2 weeks
**Repository**: `shurlinet/shurli-sdk-swift` (separate repo, ships via Swift Package Manager)

Ship a native Swift SDK that wraps the daemon API. This is the foundation the Apple multiplatform app (Phase 12) will be built on. Building it before the app validates the API surface from a non-Go language and catches design issues early.

**Swift SDK** (`ShurliSDK`):
- [ ] Swift Package (SPM) wrapping daemon HTTP API (Unix socket + cookie auth)
- [ ] `Codable` model types matching all daemon API responses
- [ ] Core operations: connect, status, expose/unexpose services, discover, proxy, peer management
- [ ] Event streaming (SSE or WebSocket) for real-time peer status and network transitions
- [ ] Async/await native (Swift concurrency)
- [ ] Platform-adaptive transport: Unix socket on macOS, HTTP over localhost on iOS
- [ ] Example: connect to daemon and list peers in <10 lines of Swift

**Design Constraints**: Zero external dependencies (Foundation + Network framework only). Strict concurrency from day one. Works in App Extension context (Network Extensions have restricted APIs).

---

### SDK & App Repository Strategy

Non-Go SDKs and consumer apps each live in their own dedicated GitHub repository. The Go SDK (`pkg/p2pnet`) stays in this repo since it IS the core library.

| Repository | What | Ships To |
|-----------|------|----------|
| `shurlinet/shurli` | Core daemon + Go library + CLI + plugins | Homebrew, apt, binary releases |
| `shurlinet/shurli-sdk-python` | Python daemon client | PyPI (`shurli-sdk`) |
| `shurlinet/shurli-sdk-swift` | Swift daemon client | Swift Package Manager (`ShurliSDK`) |
| `shurlinet/shurli-ios` | Apple multiplatform app (consumer of Swift SDK) | App Store |

Different languages have different release cycles, CI pipelines, and dependency ecosystems. Separate repos let each SDK version independently of the daemon.

---

## Phase 10: Distribution & Launch

**Timeline**: 1-2 weeks

**Goal**: Make Shurli installable without a Go toolchain, launch with compelling use-case content, and establish `shurli.io` as the stable distribution anchor.

**Rationale**: High impact, low effort. The domain `shurli.io` is the one thing no third party can take away - every user-facing URL routes through it, never hardcoded to `github.com` or any other host.

**Website & Documentation (shurli.io)**:
- [x] Hugo + Hextra site, automated docs sync, landing page, blog, CI/CD deploy
- [x] GitHub Pages hosting with custom domain, Cloudflare DNS + CDN + DDoS protection
- [x] AI-Agent discoverability: `/llms.txt` and `/llms-full.txt`
- [ ] `pkg/p2pnet` library reference (godoc-style)
- [ ] Use-case guides (GPU inference, IoT, game servers)
- [ ] Install page with platform-specific instructions

**Release Manifest & Upgrade Endpoint**:
- [ ] CI generates `releases/latest.json` on every tagged release
- [ ] `shurli upgrade` fetches manifest from `shurli.io` (not GitHub API directly)
- [ ] Fallback order: GitHub -> GitLab -> IPFS gateway

**Distribution Resilience** (gradual rollout):

The domain (`shurli.io`) is the anchor. DNS is on Cloudflare under our control. If any host disappears, one DNS record change restores service.

| Layer | GitHub (primary) | GitLab (mirror) | IPFS (fallback) |
|-------|-----------------|-----------------|-----------------|
| Source code | Primary repo | Push-hook mirror | - |
| Release binaries | GitHub Releases | GitLab Releases (GoReleaser) | Pinned on Filebase |
| Static site | GitHub Pages | GitLab Pages | Pinned + DNSLink ready |
| DNS failover | CNAME -> GitHub Pages | Manual flip to GitLab Pages | Manual flip to Cloudflare IPFS gateway |
| Source links | `shurli.io/source/*` redirects | Same URLs, different target | Same URLs, different target |

All documentation source references (code paths in engineering journal, architecture docs, etc.) link through `shurli.io/source/` instead of directly to any git host. When the primary moves, update one redirect config. Old search engine cached URLs and LLM training snapshots still resolve through the domain we control.

**Package Managers & Binaries**:
- [ ] GoReleaser: Linux/macOS/Windows (amd64 + arm64)
- [ ] Ed25519-signed checksums
- [ ] Homebrew tap: `brew install shurlinet/tap/shurli`
- [ ] One-line install: `curl -sSL get.shurli.io | sh`
- [ ] APT, AUR, Docker image

**Embedded / Router Builds** (OpenWRT, Ubiquiti, GL.iNet, MikroTik):
- [ ] Cross-compilation targets: `linux/mipsle`, `linux/arm/v7`, `linux/arm64`
- [ ] Binary size budget: default <=25MB stripped, embedded <=10MB compressed

**Auto-Upgrade** (builds on commit-confirmed pattern from Phase 4C):
- [ ] `shurli upgrade --check` - compare version, show changelog
- [ ] `shurli upgrade` - download, verify, replace binary, restart
- [ ] `shurli upgrade --auto` - automatic with commit-confirmed rollback safety. **Impossible to brick a remote node.**

**Use-Case Guides & Launch Content**:
- [ ] GPU inference - *"Access your home GPU from anywhere through Starlink CGNAT"*
- [ ] IoT/smart home remote access (Home Assistant, cameras behind CGNAT)
- [ ] Media server sharing (Jellyfin/Plex with friends via invite flow)
- [ ] Game server hosting (Minecraft, Valheim through CGNAT)
- [ ] Game/media streaming (Moonlight/Sunshine tunneling)

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

---

## Phase 11: Desktop Gateway Daemon + Private DNS

**Timeline**: 2-3 weeks

**Goal**: Multi-mode gateway daemon for transparent service access, backed by a private DNS zone on the relay.

**Client-side Gateway**:
- [ ] Gateway daemon with multiple modes: SOCKS5 proxy, Local DNS server (`.p2p` TLD), TUN/TAP virtual network interface
- [ ] Virtual IP assignment, subnet routing, trusted network detection

**Relay-side Private DNS**:

{{< figure src="/images/docs/roadmap-private-dns.svg" alt="Private DNS Architecture" >}}

- [ ] Lightweight DNS zone on the relay server (e.g., CoreDNS or custom)
- [ ] Exposed **only** via P2P protocol - never bound to public UDP/53
- [ ] Subdomains assigned on the relay, resolvable only within the P2P network
- [ ] Public DNS returns NXDOMAIN for all subdomains - they don't exist outside the network

**Usage Examples**:
```bash
# Mode 1: SOCKS proxy (no root needed)
shurli-gateway --mode socks --port 1080

# Mode 2: DNS server (queries relay's private DNS)
shurli-gateway --mode dns --port 53

# Mode 3: Virtual network (requires root)
sudo shurli-gateway --mode tun --network 10.64.0.0/16
```

---

## Phase 12: Apple Multiplatform App

**Timeline**: 3-4 weeks
**Repository**: `shurlinet/shurli-ios` (separate repo, depends on `shurli-sdk-swift` from Phase 9E)

**Goal**: Native Apple multiplatform app (macOS, iOS, iPadOS, visionOS) with VPN-like functionality and dotbeam visual pairing. The app consumes the Swift SDK (Phase 9E) - it contains zero daemon API plumbing, only UI and platform integration code.

**iOS Strategy**:
- **Primary**: NEPacketTunnelProvider (VPN mode) - full TUN interface, virtual network support
- **Fallback**: SOCKS proxy app (if VPN rejected by Apple)

**Deliverables**:
- [ ] SwiftUI app targeting macOS, iOS, iPadOS, visionOS
- [ ] NEPacketTunnelProvider for iOS/iPadOS
- [ ] QR code scanning for `shurli invite` codes
- [ ] Background connection maintenance + battery optimization
- [ ] dotbeam visual pairing - animated visual verification replacing emoji fingerprints

{{< figure src="/images/docs/roadmap-mobile-flow.svg" alt="Mobile App Config Flow" >}}

---

## Phase 13: Federation - Network Peering

**Timeline**: 2-3 weeks

**Goal**: Enable relay-to-relay federation for cross-network communication.

**Deliverables**:
- [ ] Relay federation configuration
- [ ] Network-scoped naming (`host.network`)
- [ ] Cross-network routing protocol
- [ ] Trust/authorization between networks
- [ ] Multi-network client support

{{< figure src="/images/docs/roadmap-federation.svg" alt="Federation Architecture" >}}

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
```

**Usage**:
```bash
# From your network, access friend's services:
ssh user@laptop.alice
curl http://desktop.bob:8080
```

---

## Phase 14: Advanced Naming Systems + Peer ID Prefix (Optional)

**Timeline**: 2-3 weeks

**Goal**: Pluggable naming architecture supporting multiple backends.

**Name Resolution Tiers**:

| Tier | Backend | Cost | Speed |
|------|---------|------|-------|
| 1 | Local Override (YAML) | Free | Instant |
| 2 | Network-Scoped (relay) | Free | Federated |
| 3 | Blockchain-Anchored (Ethereum) | ~$10-50 one-time | Guaranteed |
| 4 | Existing Blockchain DNS (ENS) | $5-640/year | Premium |

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

### Target Audiences (in order of receptiveness)

1. **r/selfhosted** - Already run services at home, hate port forwarding, value self-sovereignty
2. **Starlink/CGNAT users** - Actively searching for solutions to reach home machines
3. **AI/ML hobbyists** - Home GPU + remote access is exactly their problem
4. **Privacy-conscious developers** - Won't use centralized VPN services because of the coordination server

### Community Infrastructure (set up at or before launch)

- [ ] **Discord server** - real-time community channel
- [ ] **Showcase page** (`/showcase`) - curated gallery of real-world deployments
- [ ] **Trust & Security page** (`/docs/trust`) - threat model, vulnerability reporting
- [ ] **Integrations page** (`/integrations`) - curated catalog of compatible services

---

## Phase 15+: Ecosystem & Polish

**Status**: Conceptual

**Potential Features**:
- [ ] Multi-lingual website and documentation (Hugo i18n)
- [ ] Web-based dashboard for network management
- [ ] Protocol marketplace (community-contributed service templates)
- [ ] Bandwidth optimization and QoS per peer/service
- [ ] Integration with existing VPN clients (OpenVPN, WireGuard)
- [ ] Desktop apps (macOS, Windows, Linux)
- [ ] Browser extension for `.p2p` domain resolution
- [ ] Community relay network
- [ ] Split tunneling (route only specific traffic through tunnel)
- [ ] Store-carry-forward for offline peers (DTN pattern)

**Researched and Set Aside** (Feb 2026): The following techniques were evaluated through cross-network research and consciously shelved. They have minimum viable network sizes (10-20+ peers) that exceed Shurli's typical 2-5 peer deployments. At small scale, they add overhead without benefit. Revisit when Shurli grows to networks of 20+ peers.

- Vivaldi network coordinates (latency prediction)
- CRDTs for partition-tolerant peer state
- Slime mold relay optimization
- Shannon entropy connection fingerprinting
- Percolation threshold monitoring
- VRF-based fair relay assignment
- Erlay / Minisketch set reconciliation

---

## Protocol & Security Evolution

- [ ] MASQUE relay transport (RFC 9298) - HTTP/3 relay alternative to Circuit Relay v2
- [ ] Post-quantum cryptography - hybrid Noise + ML-KEM (FIPS 203) handshakes
- [ ] WebTransport transport - native QUIC-based, browser-compatible
- [ ] Zero-RTT proxy connection resume - QUIC session tickets for instant reconnection
- [ ] Hardware-backed peer identity - TPM 2.0 (Linux) or Secure Enclave (macOS/iOS)
- [ ] eBPF/XDP relay acceleration - kernel-bypass packet forwarding
- [ ] W3C DID-compatible identity - `did:key`, `did:peer` format
- [ ] Formal verification of invite/join protocol state machine

---

## Success Metrics

**Phase 4A**: Library importable, 3+ services documented, SSH/XRDP/TCP working

**Phase 4B**: Two machines connected via invite code in under 60 seconds, zero manual file editing

**Phase 4C**: CI on every push, >60% overall coverage, relay resource limits, auto-recovery within 30s, commit-confirmed prevents lockout, QUIC transport default

**Phase 6**: Client-generated async invites work, relay sealed/unsealed with auto-lock, remote unseal over P2P, admin/member roles enforced

**Phase 7**: ZKP proves membership without revealing identity

**Phase 8**: Unified BIP39 seed, encrypted identity, remote admin over P2P, MOTD/goodbye

**Phase 9**: Third-party plugins work, file transfer between peers, Python + Swift SDKs published (separate repos)

**Phase 10**: One-line install, `shurli upgrade --auto` with rollback safety, GPU inference guide published

**Phase 11**: Gateway works in all 3 modes, private DNS resolves only within P2P network

**Phase 12**: Apple multiplatform app approved, QR invite flow works, dotbeam visual pairing live

**Phase 13**: Two networks federate, cross-network routing works

**Phase 14**: 3+ naming backends working, plugin API documented
