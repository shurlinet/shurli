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

**Status**: M1 complete (Phase 8B). M2-M5 moved to Phase 16.

### Phase 8D: Module Slots

**Status**: Moved to Phase 21.

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

Ship a native Swift SDK that wraps the daemon API. This is the foundation the Apple multiplatform app (Phase 22) will be built on. Building it before the app validates the API surface from a non-Go language and catches design issues early.

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

**Status**: 🔶 11A+11B DONE, 11C pending
**Prerequisite**: go-clatter v0.1.0 (DONE), go-clatter v0.2.0 (DONE)

**Goal**: Wire post-quantum key exchange into Shurli as a libp2p security transport, giving every connection quantum-resistant encryption alongside classical Noise.

**Rationale**: QUIC already negotiates X25519MLKEM768 for the transport layer (verified). Phase 11 adds PQ Noise at the application layer via go-clatter. First-mover advantage is time-limited - once PQ becomes standard, it stops being a differentiator. No other P2P project has shipped PQ node/peer identity authentication.

**Deliverables**:

**11A: PQ Noise Transport** ✅ DONE:
- [x] `/pq-noise/1` libp2p security transport using go-clatter HybridDualLayerHandshake
- [x] Fixed cipher suites (no negotiation - simplicity over flexibility)
- [x] Fallback to classical `/noise` for non-PQ peers
- [x] Key material cleanup on connection close
- [x] Downgrade enforcement via connection gater (InterceptUpgraded)
- [x] Config: `pqc_policy: mandatory|opportunistic|disabled`, per-peer override via authorized_keys
- [x] `shurli status` shows PQC status per connection (QUIC + Noise PQ)
- [x] Phase 3 adversarial security audit (167 findings, 3 bugs fixed)

**11B: Post-Quantum Signing (ML-DSA-65)** ✅ DONE:
- [x] ML-DSA-65 (FIPS 204, NIST Level 3) signing module in go-clatter v0.2.0
- [x] Library: `filippo.io/mldsa` (migrates to Go stdlib `crypto/mldsa` on Go 1.27)
- [x] GenerateKey, Sign/Verify with FIPS 204 context strings, Destroy with secret zeroing
- [x] 29 tests, 3 examples, concurrent stress test, accumulated regression vectors
- Hybrid Ed25519 + ML-DSA-65 identity proofs -> Phase 13 (PQ Identity Attestation)
- PQ-signed capability tokens and admin commands -> Phase 13+

**11C: Audit + Documentation**:
- [x] Full security audit (Phase 3 adversarial audit, 167 findings cross-checked)
- [ ] Architecture and roadmap updates
- [ ] Engineering journal entries
- [ ] Blog post on PQC implementation
- [ ] Katzenpost/nyquist comparison documentation (research DONE, write-up pending)

---

## Phase 12: Seed & Recovery Infrastructure

**Status**: 📋 Next
**Prerequisite**: Phase 11B (ML-DSA-65 in go-clatter, DONE)

**Goal**: Build the seed abstraction libraries that enable multi-identity derivation (BIP85) and Shamir secret sharing recovery (SLIP39). These are foundation dependencies for PQ identity attestation (Phase 13).

**Rationale**: PQ identity attestation requires deterministic multi-identity derivation from a single master seed. BIP85 provides industry-standard derivation with hardware wallet interop. SLIP39 provides threshold recovery (2-of-3, 3-of-5) without single-point-of-failure seed phrases. Both are standalone libraries usable beyond Shurli.

**Deliverables**:

**12A: go-bip85 Library** (separate repo: `shurlinet/go-bip85`):
- [ ] Full BIP85 spec implementation (9 applications, 12 test vectors)
- [ ] BIP32 HD key derivation via `dcrd/hdkeychain/v3`
- [ ] secp256k1 via `dcrd/dcrec/secp256k1/v4` (constant-time)
- [ ] Base85 RFC 1924 encoder (own implementation, ~40 lines)
- [ ] Cross-implementation verification against Python (bipsea) and JavaScript (bip85-js) references
- [ ] Shurli identity derivation path: `m/83696968'/128169'/32'/{identity_index}'`

**12B: SLIP39 Fork + Hardening** (fork to `satindergrewal`):
- [ ] Fork and audit existing Go SLIP39 implementation
- [ ] Memory zeroing for secret material
- [ ] Constant-time operations where applicable
- [ ] 2-level group support (e.g., 2-of-3 groups, each group 2-of-3 shares)

**12C: Seed Abstraction Integration**:
- [ ] `SeedSource` interface: `Entropy() []byte`, `Type() string`, `DisplayBackup() string`
- [ ] BIP85 multi-identity derivation wired into identity package
- [ ] SHRL envelope redesign: N-key modular format (ERC-2335 pattern)
- [ ] `shurli init` / `shurli recover` updates for multiple SeedSource types

---

## Phase 13: PQ Identity Attestation

**Status**: 📋 Planned
**Prerequisite**: Phase 12 (BIP85 for multi-identity derivation), Phase 11A (PQ Noise transport)

**Goal**: Add ML-DSA-65 identity attestation to the PQ Noise handshake, proving each peer's post-quantum identity alongside the existing Ed25519 identity. Design offline master key architecture for passwordless daemon operation.

**Rationale**: Every P2P project uses PQ for key exchange while keeping classical crypto for identity. Shurli shipping PQ identity attestation is a genuine first-mover position. ML-DSA-65 adds ~390us per handshake - negligible.

**Deliverables**:

**13A: Core PQ Attestation**:
- [ ] HKDF domain `shurli/pq-identity/v1` for ML-DSA-65 seed derivation
- [ ] Own protobuf (field-compatible with libp2p, PQ attestation at field 3)
- [ ] ML-DSA-65 SignWithContext/VerifyWithContext in handshake
- [ ] Buffer constant increases for PQ payload sizes
- [ ] PQCStatus extended with attestation reporting

**13B: Gater Enforcement**:
- [ ] Config: `require_pq_identity: true|false`
- [ ] `pq_id=sha256:<hex>` attribute in authorized_keys
- [ ] TOFU for first contact, reject on mismatch

**13C: Offline Master Key Design** (Tor model):
- [ ] Certificate chain: master Ed25519 signs medium-term signing key
- [ ] Wire format accommodates direct attestation AND cert chain

**13D: Signing Agent Process**:
- [ ] ssh-agent model: separate process holds private keys
- [ ] Unix socket communication, main daemon never has raw key material

---

## Phase 14: Topic-Based Pub/Sub

**Status**: 📋 Planned
**Prerequisite**: Phase 13 (for PQ-signed messages), or can start in parallel

**Goal**: Integrate GossipSub for topic-based publish/subscribe, replacing the custom presence forwarding layer and enabling agent capability advertisement.

**Rationale**: Shurli's custom NetIntel system handles peer presence but is not designed for arbitrary topic pub/sub. Agent features (Phase 17+) need topic-based messaging for capability advertisement, skill announcements, and task broadcasts. The NetIntel Layer 3 slot was explicitly designed as a GossipSub drop-in point.

**Deliverables**:

**14A: GossipSub Integration**:
- [ ] Fork and update go-libp2p-pubsub to match Shurli's go-libp2p version (upstream pinned 9 versions behind)
- [ ] Wire GossipSub into NetIntel Layer 3 slot
- [ ] Private topic namespace: `/shurli/` prefix (like private DHT)
- [ ] Peer scoring enabled, message signing required (Ed25519, future: ML-DSA-65)
- [ ] Topic validators for capability claims

**14B: Service Event Broadcasting**:
- [ ] Service add/remove events propagated via pub/sub
- [ ] MOTD propagation to all connected peers
- [ ] Network-wide maintenance announcements
- [ ] Config: `pubsub.enabled`, topic configuration

---

## Phase 15: Naming Standards (SNR)

**Status**: 📋 Planned
**Prerequisite**: Phase 14 (pub/sub for name broadcasting)

**Goal**: Pluggable naming architecture with 5 identity layers, W3C DID support, local petname store, and a resolution pipeline that any plugin can extend.

**Rationale**: Every peer command currently requires a raw peer ID or a simple name from `names.yaml`. The naming standard provides a proper resolution pipeline (petnames, DIDs, external resolvers) that agent features build on - Agent Cards need DIDs, skill discovery needs name resolution, task authorization needs name-scoped capabilities.

**Design Principles**:
- No blockchain required in core. Blockchain naming via resolver plugins only.
- No central authority. The relay MUST NOT become a name authority.
- Plugin-extensible. External systems (ENS, DNS, VerusID, social handles) via resolver plugins.
- Agent-native. AI agents work with cryptographic identifiers; human names are a UX layer on top.
- Offline-first. Local resolution works without network access.
- Standards-aligned. W3C DIDs + petname system principles (RFC 9498).

**Identity Layers**:

| Layer | Global | Secure | Memorable | Description |
|-------|--------|--------|-----------|-------------|
| PeerID | Yes | Yes | No | Cryptographic ground truth (already exists) |
| DID | Yes | Yes | No | W3C standard identity (`did:peer`, `did:key`) |
| Petname | No | Yes | Yes | Local, user-assigned, per-node |
| Nickname | Yes | No | Yes | Self-chosen, travels in invite codes |
| External | Yes | Varies | Yes | Plugin-resolved (ENS, DNS, etc.) |

**Deliverables**:

**15A: Core Resolution Engine + Petname Store**:
- [ ] Name format parsing (`shurli:<type>:<value>` URI scheme + CLI shorthand)
- [ ] Resolution pipeline (route to resolver, validate, cache, return)
- [ ] Local petname store (`~/.shurli/names.db`)
- [ ] Name validation (1-63 chars, `a-z0-9-_.`, strict anti-homoglyph constraints)
- [ ] `PluginContext.ResolveName()` and `PluginContext.RegisterResolver()` methods
- [ ] Plugin resolver registry with priority ordering, timeout, conflict resolution
- [ ] Name cache with disk persistence and TTL
- [ ] CLI: `shurli name add/remove/rename/list/search/resolve`
- [ ] All existing commands that accept PeerID also accept any resolvable name

**15B: DID Integration**:
- [ ] Built-in `did:peer` and `did:key` resolution
- [ ] DID Document generation from node identity
- [ ] DID in invite codes (alongside PeerID)
- [ ] CLI: `shurli name did` (show DID document)

**15C: Invite Integration + Nickname System**:
- [ ] Nickname field in invite codes (self-chosen, advisory)
- [ ] Auto-petname from nickname on invite accept (collision handling)
- [ ] Nickname broadcast in peer metadata

**15D: Built-in + Plugin Resolvers**:
- [ ] Built-in resolvers:
  - [ ] Local petname store (YAML/DB)
  - [ ] DHT-based (federated, network-scoped)
  - [ ] mDNS (.local)
  - [ ] DID (`did:peer`, `did:key`)
- [ ] Optional plugin resolvers (community-built):
  - [ ] DNS TXT records (reference implementation)
  - [ ] ENS (.eth domains)
  - [ ] Ethereum smart contract
  - [ ] VerusID
  - [ ] Nostr npub identifiers
  - [ ] Social media handles (X, Telegram)
  - [ ] Active Directory / LDAP (enterprise)
  - [ ] IPFS/Arweave archiving for redundancy
  - [ ] Bitcoin OP_RETURN

**15E: Documentation**:
- [ ] NameResolver interface documentation for community plugin authors
- [ ] Architecture docs update
- [ ] Engineering journal entry

**Name Resolution Tiers**:

**Tier 1: Local Petname** (Free, Instant, Offline)
```yaml
# ~/.shurli/names.db (or names.yaml fallback)
sat       -> 12D3KooWAbCdEf...
home-pi   -> 12D3KooWXyZaBc...
work-mac  -> 12D3KooWDeFgHi...
```

**Tier 2: Network-Scoped** (Free, Federated)
```
Format: <hostname>.<network>
Examples: laptop.grewal, desktop.alice
Resolution: Ask relay for peer ID (Phase 19D federation)
```

**Tier 3: DID** (Free, Standards-Based)
```
did:peer:0z6MkhaXgBZDvotDkL5257faiztiGiC2QtKLGpbnnEGta2doK
did:key:z6MkhaXgBZDvotDkL5257faiztiGiC2QtKLGpbnnEGta2doK
```

**Tier 4: External Resolver Plugins** (Varies)
```
ens:name.eth                    # Ethereum Name Service plugin
dns:node.example.com            # DNS TXT record plugin
verusid:sat@                    # VerusID plugin
nostr:npub1abc...               # Nostr plugin
x:@handle                       # Social media plugin
ad:jsmith                       # Active Directory plugin (enterprise)
```

**Name Resolution Example**:
```bash
# All resolve to the same peer:
shurli send sat ./file.txt                    # petname lookup
shurli send 12D3KooWAbCdEf... ./file.txt      # raw PeerID
shurli send did:peer:0z6Mkh... ./file.txt      # DID
shurli send ens:name.eth ./file.txt            # external resolver plugin
shurli send dns:node.example.com ./file.txt    # DNS TXT resolver plugin
```

---

## Phase 16: ACL-to-Macaroon Migration (M2-M5)

**Status**: 📋 Planned
**Prerequisite**: Phase 15 (name-scoped capability caveats)

**Goal**: Replace all ACL-based access control with macaroon capability tokens. M1 (plugin/service layer) is DONE and proven. This phase extends to the remaining 4 layers.

**Rationale**: Macaroons enable offline attenuation (delegate restricted access without contacting the authority), name-scoped capabilities, and composable authorization. The proving ground (M1, 10/10 physical tests PASS) confirmed the pattern works.

**Deliverables**:
- [ ] **M2 - Share Layer**: File share access via macaroon tokens (share-scoped caveats)
- [ ] **M3 - Relay Layer**: Relay access via macaroon tokens (bandwidth budget, time limit caveats)
- [ ] **M4 - Connection Layer**: Connection gating via macaroon presentation in Noise handshake
- [ ] **M5 - Role Layer**: Admin/member roles via macaroon caveats (delegation via attenuation)
- [ ] Deferred ACL items from relay-first onboarding (auto peer ID discovery, instant disconnect on deauth)

---

## Phase 17: Agent Foundation - MCP + Service Templates

**Status**: 📋 Planned (partially urgent)
**Prerequisite**: Phase 15 (naming for agent identity), Phase 14 (pub/sub for advertisement)

**Goal**: Make Shurli's existing TCP tunneling explicitly agent-friendly with MCP service templates, agent-readable capability documents, and distribution channel listings.

**Rationale**: `shurli proxy` already tunnels any TCP service through P2P - including MCP servers. The gap is MCP-specific documentation, service templates, and agent discoverability. Low effort, high impact.

**Deliverables**:
- [ ] `mcp` as well-known service name in service registry
- [ ] `shurli service add mcp --port 8080` with auto-detection of common MCP ports
- [ ] `shurli proxy <peer> mcp` (auto-resolve port from service registry)
- [ ] MCP health check in `shurli doctor`
- [ ] Machine-readable skill document for agent discovery platforms
- [ ] Documentation: "MCP Server in 60 Seconds"
- [ ] Identity management backend APIs - RPC/CLI for managing agent egos (create, list, revoke, rotate)
- [ ] Per-identity permission policies - each agent ego gets own authorized_keys scope

---

## Phase 18: Agent-to-Agent Task Protocol

**Status**: 📋 Planned
**Prerequisite**: Phases 14-17

**Goal**: Implement the A2A task protocol as a Shurli plugin, making Shurli the sovereign, E2E encrypted, PQC-ready, grant-controlled transport for agent-to-agent communication.

**Rationale**: The A2A protocol defines how agents discover each other, exchange tasks, and negotiate outcomes. Shurli implements it as a transport - the protocol is a communication standard, Shurli is infrastructure. Every other implementation lacks ZKP, PQC, grants, sealed vault, and file transfer.

**Deliverables**:

**18A: Agent Card + Skill Registry**:
- [ ] Generate Agent Card from Shurli config (peer ID, DID, services, capabilities)
- [ ] Serve Agent Card via daemon API and private DHT
- [ ] Advertise skills via pub/sub topics
- [ ] CLI: `shurli agent card`, `shurli agent skills`

**18B: Task Protocol**:
- [ ] Task lifecycle FSM (submitted -> working -> input-needed -> completed/failed/canceled)
- [ ] `/shurli/a2a/1.0.0` protocol over libp2p streams (E2E encrypted, NAT traversed)
- [ ] Task authorization via macaroon grants
- [ ] CLI: `shurli agent task submit <peer> <skill>`, `shurli agent tasks`

**18C: HTTP Bridge + Interop**:
- [ ] Daemon API endpoints for standard HTTP-based agent clients
- [ ] Agent Card discovery from HTTPS endpoints
- [ ] Bidirectional: Shurli agents appear as standard HTTP agents

**18D: MCP Protocol Bridge**:
- [ ] Expose MCP servers as agent skills
- [ ] Expose agent skills as MCP tools
- [ ] Tool discovery across P2P network via pub/sub

**18E: Agent Authorization + Key Isolation**:
- [ ] Agent-to-agent authorization between co-located egos on same machine
- [ ] Agent ego ML-DSA-65 attestation via protocol handlers (`/shurli/agent/<name>/1.0.0`)
- [ ] Full multi-Host isolation (ONLY if Option B sub-identity model proves insufficient)

---

## Phase 19: Agent Discovery + Federation

**Status**: 📋 Planned
**Prerequisite**: Phase 18

**Goal**: Capability-based agent discovery via pub/sub topics, relay as opt-in federated capability index, and relay-to-relay federation protocol.

**Rationale**: Discovery is what makes agent networks useful at scale. Without it, agents can only talk to peers they already know. Federation extends this across independent relay networks.

**Deliverables**:

**19A: Capability Discovery**:
- [ ] Topic-based agent discovery via pub/sub (`/shurli/skill/<category>/1.0.0`)
- [ ] Anycast routing: "find an agent that can do X" -> network routes to capable peer
- [ ] Privacy-aware: optional ZKP membership proof before skill advertisement

**19B: Relay Agent Registry**:
- [ ] Relay hosts opt-in capability index
- [ ] Cross-relay capability queries

**19C: Discovery Protocol Compatibility**:
- [ ] Export capability records in standard formats
- [ ] Registration with external discovery indices (opt-in)

**19D: Relay-to-Relay Federation**:

Enable relay-to-relay federation for cross-network communication. Only matters once you have multiple users with their own networks.

- [ ] `/shurli/relay-federation/1.0.0` protocol
- [ ] Relay federation configuration
- [ ] Network-scoped naming (`host.network`)
- [ ] Cross-network routing protocol
- [ ] Trust/authorization between networks
- [ ] Route advertisement and discovery
- [ ] Multi-network client support - single client connected to multiple independent networks simultaneously
- [ ] Relay peering agreements (mutual trust, config-driven)
- [ ] Cross-network service discovery
- [ ] Federated capability index sync via pub/sub
- [ ] CLI: `shurli relay federation list/add/remove`

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

    - network_name: "bob"
      relay: "/dns4/relay.example.com/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"

  routing:
    allow_transit: true  # Let alice -> bob via your relay
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
│    "grewal"     │<---->│     "alice"     │<---->│      "bob"      │
│                 │      │                 │      │                 │
│  ├─ laptop      │      │  ├─ desktop     │      │  ├─ server      │
│  └─ relay       │      │  └─ relay       │      │  └─ relay       │
└─────────────────┘      └─────────────────┘      └─────────────────┘
```

---

## Phase 20: Payments

**Status**: 📋 Planned
**Prerequisite**: Phases 16, 18

**Goal**: Machine-to-machine payment protocol integration for paid relay capacity, pay-per-agent-query, and streaming payments - without coupling to any centralized payment processor.

**Rationale**: Payment-method neutral wire-level protocol (IETF-track, HTTP 402). Composes with existing layers: DSP gates trust -> macaroons gate authorization -> payments gate paid resource access. Unlocks relay marketplace economics and agent bounty patterns.

**Deliverables**:
- [ ] Payment server (402 challenge generation, credential verification, receipt issuance)
- [ ] Payment client (402 detection, intent selection, credential construction, retry)
- [ ] First settlement intent: stablecoin on cheapest L2
- [ ] Macaroon integration (payment receipt caveats)
- [ ] User-configurable spending caps per peer, per resource, per time window
- [ ] Agent payment mandate support (signed permission statements)
- [ ] Session-based payments for streamed delivery
- [ ] MCP servers can charge per tool call

---

## Phase 21: Reputation / Module Slots

**Status**: 📋 Deferred (until traffic demands)
**Prerequisite**: Phases 16, 19

**Goal**: Complete reputation scoring pipeline with hot-swappable algorithms and connected identity trust from the naming standard.

**Deliverables**:
- [ ] Community-Notes-style matrix factorization scoring
- [ ] Hot-swappable scoring algorithm module slots
- [ ] Connected identity trust: external profiles feed into reputation as tiered signal
- [ ] Two trust dimensions: identity trust (from verified profiles) vs performance trust (from behavior)

---

## Phase 22: Apple Multiplatform App

**Status**: 📋 In Progress (separate repo)
**Repository**: `github.com/shurlinet/shurli-ios` (depends on Swift SDK from Phase 9E)

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

## Phase 23: Desktop Gateway Daemon + Private DNS

**Timeline**: 2-3 weeks
**Status**: 📋 Deferred
**Prerequisite**: Phases 15, 19

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

## Phase 24+: Ecosystem & Polish

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
- [x] Post-quantum cryptography - go-clatter v0.1.0+v0.2.0 DONE. Phase 11A+11B shipped (PQ Noise transport + ML-DSA-65 signing). Phase 13 adds PQ identity attestation.
- [ ] WebTransport transport - native QUIC-based, browser-compatible
- [ ] Zero-RTT proxy connection resume - QUIC session tickets for instant reconnection
- [ ] Hardware-backed peer identity - TPM 2.0 (Linux) or Secure Enclave (macOS/iOS)
- [ ] eBPF/XDP relay acceleration - kernel-bypass packet forwarding
- [ ] W3C DID-compatible identity - Phase 15 (Naming Standards) implements `did:peer` and `did:key` with DID Document generation
- [ ] Formal verification of invite/join protocol state machine

---

## ZKP Watching List

Checked after each phase completion:
1. **Halo 2 in Go** - if a native Go implementation appears, it would be a strict upgrade (removes ceremony dependency). Zero activity as of 2026-03-01.
2. **gnark Vortex** - ConsenSys's lattice-based transparent setup. Would remove ceremony dependency entirely if it reaches production.
3. **gnark IPA backend** - would enable Halo 2-style proofs in gnark.

**Deferred from Phase 7**:
- [ ] **Private DHT namespace membership** - prove namespace membership without revealing the namespace name (deferred to Phase 19 federation work)

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

**Phase 11**: `/pq-noise/1` transport negotiates HybridDualLayer handshakes, classical Noise fallback works, PQC status shown per connection (QUIC + Noise PQ). Phase 3 adversarial audit passed.

**Phase 12**: go-bip85 byte-identical to reference implementations, SLIP39 share generation+reconstruction works, SeedSource interface supports BIP39/SLIP39/hex, SHRL envelope stores N keys per identity.

**Phase 13**: ML-DSA-65 attestation in PQ Noise handshake, gater enforces pq_id with TOFU, offline master key cert chain designed in wire format, signing agent process isolates key material.

**Phase 14**: GossipSub integrated via NetIntel Layer 3 slot, service events propagated via pub/sub

**Phase 15**: Petname store works offline, DID Documents generated, at least 1 external resolver demonstrated

**Phase 16**: All 5 ACL layers replaced with macaroon capability tokens

**Phase 17**: MCP servers tunneled with zero extra config beyond `shurli service add mcp`, identity management APIs ready

**Phase 18**: Agent Card published to DHT, task lifecycle works E2E over libp2p, MCP <-> A2A bridge working, agent-to-agent authorization

**Phase 19**: Two independent relay networks federate, agents discoverable by capability

**Phase 22**: Apple multiplatform app approved, QR invite flow works, dotbeam visual pairing live
