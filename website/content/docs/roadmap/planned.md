---
title: "Planned & Future"
weight: 2
description: "Upcoming phases and future work: network intelligence, ZKP privacy, plugins, distribution, mobile, federation, and beyond."
---

## Phase 5: Network Intelligence

Smarter peer discovery, scoring, and communication. mDNS for instant LAN discovery, Bitcoin-inspired peer management for reliable connections, and PubSub broadcast for network-wide awareness. Includes relay decentralization groundwork.

### 5-K: mDNS Local Discovery

Zero-config peer discovery on the local network. When two Shurli nodes are on the same LAN, mDNS finds them in milliseconds without DHT lookups or relay bootstrap. Directly addresses the latency gap observed during Batch I live testing: LAN-connected peers currently route through the relay first, then upgrade to direct. With mDNS, they discover each other instantly.

- [ ] Enable libp2p mDNS discovery (`github.com/libp2p/go-libp2p/p2p/discovery/mdns`) - already in the dependency tree, zero binary size impact
- [ ] Integrate with existing peer authorization - mDNS-discovered peers still checked against `authorized_keys` (ConnectionGater enforces, no bypass)
- [ ] Combine with DHT discovery - mDNS for local, DHT for remote. Both feed into PathDialer
- [ ] Config option: `discovery.mdns_enabled: true` (default: true, disable for server-only nodes)
- [ ] Explicit DHT routing table refresh on network change events
- [ ] Test: two hosts on same LAN discover each other via mDNS within 5 seconds without relay

Quick win. One libp2p option on host construction + NetworkMonitor integration. Prerequisite: none. Zero new dependencies.

### 5-L: PeerManager / AddrMan

Bitcoin-inspired peer management, dimming star scoring, persistent peer table, peerstore metadata, bandwidth tracking, DHT refresh on network change, gossip discovery (PEX). Top priority after mDNS. Motivated by the "no re-upgrade from relay to direct after network change" finding from Batch I live testing.

### 5-M: GossipSub Network Intelligence

libp2p's built-in PubSub broadcast layer (GossipSub v1.1, already in the dependency tree). Currently all Shurli communication is point-to-point. GossipSub adds a broadcast channel where peers share network knowledge collectively. Scale-aware: direct PEX at <10 peers, GossipSub at 10+.

- [ ] **GossipSub topic per namespace** - `/shurli/<namespace>/gossip/1.0.0`
- [ ] **Address change broadcast** - immediate notification instead of waiting for DHT re-discovery
- [ ] **PEX transport upgrade** - PEX messages carried over GossipSub
- [ ] **PeerManager observation sharing** - peers share aggregated scoring observations
- [ ] **Scale-aware activation** - threshold at 10 connected peers

### Relay Decentralization

After Phase 5 PeerManager provides the data:

- [ ] `require_auth` relay service - enable Circuit Relay v2 service on home nodes with `require_auth: true`
- [ ] DHT-based relay discovery - authorized relays advertise on DHT under well-known CID
- [ ] Multi-relay failover - health-aware selection based on connection quality scores
- [ ] Per-peer bandwidth tracking - feeds into relay quota warnings, PeerManager scoring, smart relay selection
- [ ] Bootstrap decentralization - hardcoded seed peers -> DNS seeds -> DHT peer exchange -> fully self-sustaining (same pattern as Bitcoin)
- [ ] **End goal**: Relay VPS becomes **obsolete** - not just optional. Every publicly-reachable Shurli node relays for its authorized peers. No special nodes, no central coordination

---

## Batch N: ZKP Privacy Layer - STATUS: WATCHING

Zero-knowledge proofs applied to Shurli's identity and authorization model. Peers prove group membership, relay authorization, and reputation without revealing their identity.

**Status: Active Watch (2026-02-23)**

The four use cases are confirmed and the architecture is designed. Implementation is deferred until a trustless (no ceremony) ZKP proving system exists in Go. We will not compromise on the trust model by using systems that require a trusted setup ceremony, and we will not introduce FFI/CGo dependencies to call Rust libraries (violates single-binary sovereignty).

**Why waiting**: Halo 2 (Zcash's IPA-based proving system) achieves true zero-trust ZKPs with no ceremony. But it exists only in Rust. No Go implementation, port, binding, or proposal exists. Nobody is working on one (verified 2026-02-23). The alternative (gnark PLONK + Ethereum KZG ceremony with 141,416 participants) is practically secure but still relies on a trust assumption. Halo 2 is mathematically superior: trust math only, not participants.

**What we considered and rejected**:
- **Ring signatures**: Equivalent to a complex card-shuffling game. Metadata analysis can narrow down the signer. zk-SNARKs provide mathematically absolute privacy. Ring signatures are not zero-knowledge.
- **gnark PLONK + Ethereum KZG**: Production-ready in pure Go, 141K-participant ceremony. Practically secure but requires trusting that 1 of 141K participants was honest. Not mathematically zero-trust.
- **Halo 2 via FFI (Rust CGo bindings)**: Technically possible but introduces Rust toolchain dependency, cross-compilation complexity, two-language audit surface. Violates sovereignty principle.
- **gnark Vortex**: ConsenSys's experimental lattice-based transparent setup. Not production-ready. Worth watching.

**What we're watching** (checked after each phase completion):
1. gnark IPA/Halo 2 backend (ConsenSys has no current plans, but Vortex is in development)
2. Native Go Halo 2 implementation (zero activity as of 2026-02-23)
3. Rust halo2 CGo bindings (zero activity as of 2026-02-23)
4. Any new trustless ZKP library in Go

**The four use cases** (ready to implement when the right tool arrives):
- [ ] **Anonymous authentication** - prove "I hold a key in the authorized set" without revealing which key
- [ ] **Anonymous relay authorization** - prove relay access rights without revealing identity
- [ ] **Privacy-preserving reputation** - prove reputation above threshold without revealing exact score
- [ ] **Private DHT namespace membership** - prove namespace membership without revealing the namespace name

**Architecture decisions** (stable regardless of proving system):
- Hash-based membership: MiMC/Poseidon(ed25519_pubkey) as Merkle leaf. Avoids Ed25519 curve mismatch with SNARK-native fields.
- Merkle tree of identity commitments = the authorized_keys set.
- ZK circuit proves: "I know a value pk such that Hash(pk) is a leaf in the tree with root R." Verifier sees root + proof only.

**Background: Zcash trusted setup evolution** (context for why we insist on trustless):
- Sprout (2016): Groth16, 6 participants. Legitimate trust concern.
- Sapling (2018): Powers of Tau, 90+ participants. 1-of-90 honest assumption.
- Orchard/NU5 (2022): Halo 2 - NO trusted setup. IPA-based (Pedersen commitments). No toxic waste. Trust math only. This is the standard we're waiting for in Go.

---

## Phase 6: Plugin Architecture, SDK & First Plugins

**Timeline**: 3-4 weeks

**Goal**: Make Shurli extensible by third parties - and prove the architecture works by shipping real plugins: file transfer, service templates, and Wake-on-LAN.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring - without forking. Shipping real plugins alongside the architecture validates the design immediately and catches interface mistakes before third parties discover them.

**Core Interfaces** (new file: `pkg/p2pnet/interfaces.go`):
- [ ] `PeerNetwork` - interface for core network operations
- [ ] `Resolver` - interface for name resolution (enables chaining: local -> DNS -> DHT -> blockchain)
- [ ] `ServiceManager` - interface for service registration and dialing (enables middleware)
- [ ] `Authorizer` - interface for authorization decisions (enables pluggable auth)
- [ ] `Logger` - interface for structured logging injection

**Extension Points**:
- [ ] Constructor injection - optional `Resolver`, `ConnectionGater`, `Logger`
- [ ] Event hook system - `OnEvent(handler)` for peer connected/disconnected, auth allow/deny
- [ ] Stream middleware - `ServiceRegistry.Use(middleware)` for compression, bandwidth limiting
- [ ] Protocol ID formatter - configurable protocol namespace and versioning

**Built-in Plugin: File Transfer**:
- [ ] `shurli send <file> --to <peer>` and `shurli receive`
- [ ] Auto-accept from authorized peers (configurable)
- [ ] Progress bar, resume interrupted transfers, directory support

**Built-in Plugin: Service Templates**:
- [ ] `shurli daemon --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `shurli daemon --vllm` shortcut (auto-detects vLLM on localhost:8000)
- [ ] Health check middleware - verify local service is reachable before exposing

**Built-in Plugin: Wake-on-LAN**:
- [ ] `shurli wake <peer>` - send magic packet before connecting
- [ ] Event hook: auto-wake peer on connection attempt (optional)

**Service Discovery Protocol**:
- [ ] New protocol `/shurli/discovery/1.0.0` - query a remote peer for their exposed services
- [ ] `shurli discover <peer>` CLI command

**Python SDK** (`shurli-sdk`):
- [ ] Thin wrapper around daemon Unix socket API
- [ ] `pip install shurli-sdk`
- [ ] Async support (asyncio)

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

---

## Phase 7: Distribution & Launch

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

## Phase 8: Desktop Gateway Daemon + Private DNS

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

## Phase 9: Mobile Applications

**Timeline**: 3-4 weeks

**Goal**: Native iOS and Android apps with VPN-like functionality.

**iOS Strategy**:
- **Primary**: NEPacketTunnelProvider (VPN mode) - full TUN interface, virtual network support
- **Fallback**: SOCKS proxy app (if VPN rejected by Apple)

**Android Strategy**:
- VPNService API (full feature parity), TUN interface

**Deliverables**:
- [ ] iOS app with NEPacketTunnelProvider
- [ ] Android app with VPNService
- [ ] QR code scanning for `shurli invite` codes
- [ ] Background connection maintenance + battery optimization

{{< figure src="/images/docs/roadmap-mobile-flow.svg" alt="Mobile App Config Flow" >}}

---

## Phase 10: Federation - Network Peering

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

## Phase 11: Advanced Naming Systems (Optional)

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

Shurli is not a cheaper Tailscale. It's the **self-sovereign alternative** for people who care about owning their network.

> *Comparison based on publicly available documentation as of 2026-02. Details may be outdated - corrections welcome via [GitHub issues](https://github.com/shurlinet/shurli/issues).*

| | **Shurli** | **Tailscale** |
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
4. **Privacy-conscious developers** - Won't use Tailscale because of the coordination server

### Community Infrastructure (set up at or before launch)

- [ ] **Discord server** - real-time community channel
- [ ] **Showcase page** (`/showcase`) - curated gallery of real-world deployments
- [ ] **Trust & Security page** (`/docs/trust`) - threat model, vulnerability reporting
- [ ] **Integrations page** (`/integrations`) - curated catalog of compatible services

---

## Phase 12+: Ecosystem & Polish

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

**Phase 5**: mDNS discovers LAN peers within 5 seconds, PeerManager scores and persists peers, network change triggers re-upgrade from relay to direct, GossipSub broadcasts address changes within seconds

**Phase 6**: Third-party plugins work, file transfer between peers, SDK published

**Phase 7**: One-line install, `shurli upgrade --auto` with rollback safety, GPU inference guide published

**Phase 8**: Gateway works in all 3 modes, private DNS resolves only within P2P network

**Phase 9**: iOS app approved, Android app published, QR invite flow works

**Phase 10**: Two networks federate, cross-network routing works

**Phase 11**: 3+ naming backends working, plugin API documented
