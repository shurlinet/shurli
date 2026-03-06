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

**Goal**: Native Apple multiplatform app (macOS, iOS, iPadOS, visionOS) with VPN-like functionality and dotbeam visual pairing.

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

**Phase 9**: Third-party plugins work, file transfer between peers, SDK published

**Phase 10**: One-line install, `shurli upgrade --auto` with rollback safety, GPU inference guide published

**Phase 11**: Gateway works in all 3 modes, private DNS resolves only within P2P network

**Phase 12**: Apple multiplatform app approved, QR invite flow works, dotbeam visual pairing live

**Phase 13**: Two networks federate, cross-network routing works

**Phase 14**: 3+ naming backends working, plugin API documented
