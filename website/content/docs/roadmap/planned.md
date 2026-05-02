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

## Phase 9: Plugin Architecture, SDK & First Plugins

**Goal**: Make Shurli extensible by third parties - and prove the architecture works by shipping real plugins. The plugins ARE the SDK examples.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring - without forking. Shipping real plugins alongside the architecture validates the design immediately and catches interface mistakes before third parties discover them.

Phase 8B (Per-Peer Data Grants), Phase 9A (Core Interfaces), Phase 9B (File Transfer), and the Plugin Architecture Shift are complete. See [Completed Work](../completed/) for details.

### Phase 8C: ACL-to-Macaroon Migration

**Status**: M1 complete (Phase 8B). M2-M5 moved to Phase 14.

### Phase 8D: Module Slots

**Status**: Moved to Phase 19.

---

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
- [ ] `docs/SDK.md` - guide for building on `pkg/sdk`
- [ ] Example walkthroughs: file transfer, service templates, custom resolver, auth middleware

### Phase 9E: Swift SDK

**Timeline**: 1-2 weeks
**Repository**: `shurlinet/shurli-sdk-swift` (separate repo, ships via Swift Package Manager)

Ship a native Swift SDK that wraps the daemon API. This is the foundation the Apple multiplatform app (Phase 20) will be built on. Building it before the app validates the API surface from a non-Go language and catches design issues early.

**Swift SDK** (`ShurliSDK`):
- [ ] Swift Package (SPM) wrapping daemon HTTP API (Unix socket + cookie auth)
- [ ] `Codable` model types matching all daemon API responses
- [ ] Core operations: connect, status, expose/unexpose services, discover, proxy, peer management
- [ ] Event streaming (SSE or WebSocket) for real-time peer status and network transitions
- [ ] Async/await native (Swift concurrency)
- [ ] Platform-adaptive transport: Unix socket on macOS, HTTP over localhost on iOS
- [ ] Example: connect to daemon and list peers in <10 lines of Swift

**Design Constraints**: Zero external dependencies (Foundation + Network framework only). Strict concurrency from day one. Works in App Extension context (Network Extensions have restricted APIs).

### Phase 9F: Layer 2 WASM Runtime

**Status**: Planned (research complete, design ready)

Third-party developers write plugins in any language (Rust, Python, JS, C), ship as `.wasm` files. wazero runtime (pure Go, zero CGo). Hardware-level sandbox isolation.

- [ ] WASM plugin loader with capability grants
- [ ] Host function bridge (Plugin interface becomes the host-side contract)
- [ ] Memory caps, context timeouts, pre-opened directory scoping
- [ ] Same CLI experience: `shurli plugin list` shows both compiled-in and WASM plugins

**Critical dependency**: WASI 0.3 (async I/O) expected mid-2026.

### Phase 9G: Layer 3 AI Plugin Generation

**Status**: Future (requires Layers 1-2 solid)

Skills.md describes plugin behavior in natural language. AI agent reads the spec, writes code, compiles to WASM. Zero-Human Network: the network generates its own extensions.

---

### SDK & App Repository Strategy

Non-Go SDKs and consumer apps each live in their own dedicated GitHub repository. The Go SDK (`pkg/sdk`) stays in this repo since it IS the core library.

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
**Status**: Partial (install script, release archives, relay-setup --prebuilt done. GoReleaser, Homebrew, APT planned)

**Goal**: Make Shurli installable without a Go toolchain, launch with compelling use-case content, and establish `shurli.io` as the stable distribution anchor.

**Rationale**: High impact, low effort. The domain `shurli.io` is the one thing no third party can take away - every user-facing URL routes through it, never hardcoded to `github.com` or any other host.

**Website & Documentation (shurli.io)**:
- [x] Hugo + Hextra site, automated docs sync, landing page, blog, CI/CD deploy
- [x] GitHub Pages hosting with custom domain, DNS provider + CDN + DDoS protection
- [x] AI-Agent discoverability: `/llms.txt` and `/llms-full.txt`
- [ ] `pkg/sdk` library reference (godoc-style)
- [ ] Use-case guides (GPU inference, IoT, game servers)
- [ ] Install page with platform-specific instructions

**Release Manifest & Upgrade Endpoint**:
- [ ] CI generates `releases/latest.json` on every tagged release
- [ ] `shurli upgrade` fetches manifest from `shurli.io` (not GitHub API directly)
- [ ] Fallback order: GitHub -> GitLab -> IPFS gateway

**Distribution Resilience** (gradual rollout):

The domain (`shurli.io`) is the anchor. DNS is managed under our control. If any host disappears, one DNS record change restores service.

| Layer | GitHub (primary) | GitLab (mirror) | IPFS (fallback) |
|-------|-----------------|-----------------|-----------------|
| Source code | Primary repo | Push-hook mirror | - |
| Release binaries | GitHub Releases | GitLab Releases (GoReleaser) | Pinned on Filebase |
| Static site | GitHub Pages | GitLab Pages | Pinned + DNSLink ready |
| DNS failover | CNAME -> GitHub Pages | Manual flip to GitLab Pages | Manual flip to IPFS gateway |
| Source links | `shurli.io/source/*` redirects | Same URLs, different target | Same URLs, different target |

All documentation source references (code paths in engineering journal, architecture docs, etc.) link through `shurli.io/source/` instead of directly to any git host. When the primary moves, update one redirect config. Old search engine cached URLs and LLM training snapshots still resolve through the domain we control.

**Package Managers & Binaries**:
- [x] One-line install: `curl -sSL get.shurli.io | sh` *(2026-03-24)*
- [x] Colored ANSI output, `--help`, `--yes`/`-y`, `--upgrade`, env vars *(2026-03-24)*
- [x] Release archives (GitHub Actions, tar.gz per platform) *(2026-03-24)*
- [x] `relay-setup.sh --prebuilt` (install from release archive) *(2026-03-24)*
- [ ] GoReleaser: Linux/macOS/Windows (amd64 + arm64)
- [ ] Ed25519-signed checksums
- [ ] Homebrew tap: `brew install shurlinet/tap/shurli`
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

## Phase 11: Post-Quantum Cryptography

**Status**: Next
**Prerequisite**: go-clatter v0.1.0 (DONE)

**Goal**: Wire post-quantum key exchange into Shurli as a libp2p security transport, giving every connection quantum-resistant encryption alongside classical Noise.

**Deliverables**:
- [ ] `/pq-noise/1` libp2p security transport using go-clatter HybridDualLayerHandshake
- [ ] Fixed cipher suites (no negotiation - simplicity over flexibility)
- [ ] Fallback to classical `/noise` for non-PQ peers
- [ ] Downgrade enforcement via connection gater
- [ ] ML-DSA-65 (FIPS 204) signing module: hybrid Ed25519 + ML-DSA-65 identity proofs
- [ ] PQ-signed capability tokens and admin commands
- [ ] `shurli status` shows PQC status per connection
- [ ] Full security audit, architecture docs, engineering journal, blog post

---

## Phase 12: Topic-Based Pub/Sub

**Status**: Planned
**Prerequisite**: Phase 11 (for PQ-signed messages), or can start in parallel

**Goal**: Integrate GossipSub for topic-based publish/subscribe, replacing the custom presence forwarding layer and enabling agent capability advertisement.

**Deliverables**:
- [ ] Fork and update go-libp2p-pubsub to match Shurli's go-libp2p version (upstream pinned 9 versions behind)
- [ ] Wire GossipSub into NetIntel Layer 3 slot
- [ ] Private topic namespace: `/shurli/` prefix (like private DHT)
- [ ] Peer scoring enabled, message signing required (Ed25519, future: ML-DSA-65)
- [ ] Topic validators for capability claims
- [ ] Service add/remove events propagated via pub/sub
- [ ] MOTD propagation to all connected peers

---

## Phase 13: Naming Standards (SNR)

**Status**: Planned
**Prerequisite**: Phase 12 (pub/sub for name broadcasting)

**Goal**: Pluggable naming architecture with 5 identity layers, W3C DID support, local petname store, and a resolution pipeline that any plugin can extend.

**Identity Layers**:

| Layer | Global | Secure | Memorable | Description |
|-------|--------|--------|-----------|-------------|
| PeerID | Yes | Yes | No | Cryptographic ground truth (already exists) |
| DID | Yes | Yes | No | W3C standard identity (`did:peer`, `did:key`) |
| Petname | No | Yes | Yes | Local, user-assigned, per-node |
| Nickname | Yes | No | Yes | Self-chosen, travels in invite codes |
| External | Yes | Varies | Yes | Plugin-resolved (ENS, DNS, etc.) |

**Name Resolution Tiers**:

| Tier | Backend | Cost | Speed |
|------|---------|------|-------|
| 1 | Local Petname (YAML/DB) | Free | Instant, offline |
| 2 | Network-Scoped (relay, Phase 17D) | Free | Federated |
| 3 | DID (`did:peer`, `did:key`) | Free | Standards-based |
| 4 | External Plugins (ENS, DNS TXT, VerusID, Nostr, social, AD/LDAP) | Varies | Plugin-dependent |

**Deliverables**:
- [ ] Name format parsing (`shurli:<type>:<value>` URI scheme + CLI shorthand)
- [ ] Resolution pipeline (route to resolver, validate, cache, return)
- [ ] Local petname store (`~/.shurli/names.db`)
- [ ] Built-in `did:peer` and `did:key` resolution + DID Document generation
- [ ] DID in invite codes (alongside PeerID)
- [ ] Plugin resolver registry with priority ordering, timeout, conflict resolution
- [ ] CLI: `shurli name add/remove/rename/list/search/resolve/did`
- [ ] All existing commands that accept PeerID also accept any resolvable name
- [ ] DNS TXT record resolver as reference implementation
- [ ] Plugin resolvers: ENS, Ethereum smart contract, VerusID, Nostr, social, AD/LDAP, IPFS/Arweave, Bitcoin OP_RETURN

---

## Phase 14: ACL-to-Macaroon Migration (M2-M5)

**Status**: Planned
**Prerequisite**: Phase 13 (name-scoped capability caveats)

**Goal**: Replace all ACL-based access control with macaroon capability tokens. M1 (plugin/service layer) is DONE and proven (10/10 physical tests PASS).

| Phase | Layer | Current | Replacement | Risk |
|-------|-------|---------|-------------|------|
| **M1** | Plugin/Service | GrantStore + GrantPouch | Macaroon caveats | **DONE** (Phase 8B) |
| **M2** | Share | `shares.json` peer lists | `share_id` caveat | Low |
| **M3** | Relay | `relay_authorized_keys` | Token for circuit auth | Medium |
| **M4** | Connection | `authorized_keys` + ConnectionGater | Token in handshake | **High** |
| **M5** | Role | `role=admin/member` attribute | `action` caveat | Low |

---

## Phase 15: Agent Foundation - MCP + Service Templates

**Status**: Planned
**Prerequisite**: Phase 13 (naming for agent identity), Phase 12 (pub/sub for advertisement)

**Goal**: Make Shurli's existing TCP tunneling explicitly agent-friendly with MCP service templates, agent-readable capability documents, and distribution channel listings.

**Deliverables**:
- [ ] `mcp` as well-known service name in service registry
- [ ] `shurli service add mcp --port 8080` with auto-detection of common MCP ports
- [ ] `shurli proxy <peer> mcp` (auto-resolve port from service registry)
- [ ] MCP health check in `shurli doctor`
- [ ] Machine-readable skill document for agent discovery platforms
- [ ] Documentation: "MCP Server in 60 Seconds"

---

## Phase 16: Agent-to-Agent Task Protocol

**Status**: Planned
**Prerequisite**: Phases 12-15

**Goal**: Implement the A2A task protocol as a Shurli plugin, making Shurli the sovereign, E2E encrypted, PQC-ready, grant-controlled transport for agent-to-agent communication.

**Deliverables**:
- [ ] Agent Card generation from Shurli config (peer ID, DID, services, capabilities)
- [ ] Agent Card served via daemon API and private DHT
- [ ] Skills advertised via pub/sub topics
- [ ] Task lifecycle FSM (submitted -> working -> input-needed -> completed/failed/canceled)
- [ ] `/shurli/a2a/1.0.0` protocol over libp2p streams (E2E encrypted, NAT traversed)
- [ ] Task authorization via macaroon grants
- [ ] HTTP Bridge for interop with standard HTTP-based agents
- [ ] MCP Protocol Bridge: expose MCP servers as agent skills and vice versa
- [ ] CLI: `shurli agent card`, `shurli agent skills`, `shurli agent task submit/tasks`

---

## Phase 17: Agent Discovery + Federation

**Status**: Planned
**Prerequisite**: Phase 16

**Goal**: Capability-based agent discovery via pub/sub topics, relay as opt-in federated capability index, and relay-to-relay federation protocol.

**Discovery**:
- [ ] Topic-based agent discovery via pub/sub (`/shurli/skill/<category>/1.0.0`)
- [ ] Anycast routing: "find an agent that can do X" -> network routes to capable peer
- [ ] Privacy-aware: optional ZKP membership proof before skill advertisement
- [ ] Relay hosts opt-in capability index with cross-relay queries
- [ ] Export capability records in standard formats for external discovery indices

**Federation**:
- [ ] `/shurli/relay-federation/1.0.0` protocol
- [ ] Relay federation configuration
- [ ] Network-scoped naming (`host.network`)
- [ ] Cross-network routing protocol
- [ ] Trust/authorization between networks
- [ ] Multi-network client support - single client connected to multiple independent networks simultaneously

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
      relay: "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"
```

**Usage**:
```bash
# From your network, access friend's services:
ssh user@laptop.alice
curl http://desktop.bob:8080
```

---

## Phase 18: Payments

**Status**: Planned
**Prerequisite**: Phases 14, 16

**Goal**: Machine-to-machine payment protocol integration for paid relay capacity, pay-per-agent-query, and streaming payments - without coupling to any centralized payment processor.

**Deliverables**:
- [ ] Payment server (402 challenge generation, credential verification, receipt issuance)
- [ ] Payment client (402 detection, intent selection, credential construction, retry)
- [ ] First settlement intent: stablecoin on cheapest L2
- [ ] Macaroon integration (payment receipt caveats)
- [ ] Agent payment mandate support (signed permission statements)
- [ ] Session-based payments for streamed delivery
- [ ] MCP servers can charge per tool call

---

## Phase 19: Reputation / Module Slots

**Status**: Deferred (until traffic demands)
**Prerequisite**: Phases 14, 17

**Goal**: Complete reputation scoring pipeline with hot-swappable algorithms and connected identity trust.

**Deliverables**:
- [ ] Community-Notes-style matrix factorization scoring
- [ ] Hot-swappable scoring algorithm module slots
- [ ] Connected identity trust: external profiles feed into reputation as tiered signal
- [ ] Two trust dimensions: identity trust (from verified profiles) vs performance trust (from behavior)

---

## Phase 20: Apple Multiplatform App

**Status**: In Progress (separate repo)
**Repository**: `shurlinet/shurli-ios` (depends on Swift SDK from Phase 9E)

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

## Phase 21: Desktop Gateway + Private DNS

**Status**: Deferred
**Prerequisite**: Phases 13, 17

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

## Phase 22+: Ecosystem & Polish

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
- [x] Post-quantum cryptography - go-clatter v0.1.0 (PQ Noise framework) DONE. Phase 11 integrates into Shurli as `/pq-noise/1` transport. ML-DSA-65 signing planned.
- [ ] WebTransport transport - native QUIC-based, browser-compatible
- [ ] Zero-RTT proxy connection resume - QUIC session tickets for instant reconnection
- [ ] Hardware-backed peer identity - TPM 2.0 (Linux) or Secure Enclave (macOS/iOS)
- [ ] eBPF/XDP relay acceleration - kernel-bypass packet forwarding
- [ ] W3C DID-compatible identity - Phase 13 (Naming Standards) implements `did:peer` and `did:key` with DID Document generation
- [ ] Formal verification of invite/join protocol state machine

---

## ZKP Watching List

Checked after each phase completion:
1. **Halo 2 in Go** - if a native Go implementation appears, it would be a strict upgrade (removes ceremony dependency). Zero activity as of 2026-03-01.
2. **gnark Vortex** - ConsenSys's lattice-based transparent setup. Would remove ceremony dependency entirely if it reaches production.
3. **gnark IPA backend** - would enable Halo 2-style proofs in gnark.

**Deferred from Phase 7**:
- [ ] **Private DHT namespace membership** - prove namespace membership without revealing the namespace name (deferred to Phase 17 federation work)

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

**Phase 11**: `/pq-noise/1` transport negotiates HybridDualLayer handshakes, classical Noise fallback works, ML-DSA-65 signs capability tokens

**Phase 12**: GossipSub integrated via NetIntel Layer 3 slot, service events propagated via pub/sub

**Phase 13**: Petname store works offline, DID Documents generated, at least 1 external resolver demonstrated

**Phase 14**: All 5 ACL layers replaced with macaroon capability tokens

**Phase 15**: MCP servers tunneled with zero extra config beyond `shurli service add mcp`

**Phase 16**: Agent Card published to DHT, task lifecycle works E2E over libp2p, MCP <-> A2A bridge working

**Phase 17**: Two independent relay networks federate, agents discoverable by capability

**Phase 20**: Apple multiplatform app approved, QR invite flow works, dotbeam visual pairing live
