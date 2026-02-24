# FAQ - Comparisons

> **Note on comparisons**: All technical comparisons in this document are based on publicly available documentation, specifications, and published benchmarks as of the date listed at the bottom. Software evolves - details may be outdated by the time you read this. If you spot an inaccuracy, corrections are welcome via [GitHub issues](https://github.com/satindergrewal/peer-up/issues) or pull requests.

## How does peer-up differ from centralized VPN tools?

The core architectural difference comes down to how coordination works.

Centralized VPN tools use a coordination server controlled by the vendor. Your devices register with that server, authenticate through it, and rely on it to broker connections. Your identity lives in their database, your device graph is visible to their infrastructure, and your ability to connect depends on their service being online and their terms remaining acceptable.

peer-up takes the opposite approach: fully decentralized coordination via a Kademlia DHT and local configuration files. Identity is an Ed25519 key pair generated on your machine. Authorization is a local `authorized_keys` file listing which peer IDs are allowed. There is no account, no sign-in, no external dependency for authentication or discovery.

### Architecture

| Aspect | **peer-up** | **Centralized VPN** |
|--------|------------|---------------------------------------|
| **Foundation** | libp2p (circuit relay v2, DHT, QUIC) | WireGuard (kernel-level crypto) |
| **Topology** | Client -> Relay -> Server (with DCUtR upgrade to direct) | Full mesh, point-to-point |
| **NAT Traversal** | Circuit relay + hole-punching (DCUtR) | DERP relay servers + STUN/hole-punching |
| **Encryption** | libp2p Noise protocol (Ed25519) | WireGuard (Curve25519) |
| **Control Plane** | None - fully decentralized (DHT + config files) | Centralized coordination server |

### Privacy & Sovereignty

| | **peer-up** | **Centralized VPN** |
|---|---|---|
| **Accounts** | None - no email, no OAuth | Required (Google, GitHub, etc.) |
| **Telemetry** | Zero - no data leaves your network | Coordination server sees device graph |
| **Control plane** | None - relay only forwards bytes | Centralized coordination server |
| **Key custody** | You generate, you store, you control | Keys managed via their control plane |
| **Source** | Fully open, self-hosted | Open source client, proprietary control plane |

### Features

| Feature | **peer-up** | **Centralized VPN** |
|---------|------------|---------------------------------------|
| **Service tunneling** | SSH, XRDP, generic TCP | Full IP-layer VPN (any protocol) |
| **Auth model** | SSH-style `authorized_keys` (peer ID allowlist) | SSO (Google, Okta, GitHub), ACLs |
| **DNS** | Friendly names in config + private DNS on relay (planned) | MagicDNS (auto device names) |
| **Platforms** | Linux, macOS (Go binary) | Linux, Windows, macOS, iOS, Android, containers |
| **Setup** | `peerup init` wizard | Download -> sign in -> done |
| **Admin UI** | CLI only | Web dashboard, admin console |
| **Exit nodes** | Not yet | Yes |
| **Subnet routing** | Not yet | Yes |
| **Multi-user/team** | Relay pairing codes + invite/join + `peerup verify` | Built-in team management, SSO |

### Strengths of the decentralized approach

- **No central authority** - No account, no coordination server, no vendor dependency
- **Importable library** - `pkg/p2pnet` can be embedded into any Go application
- **CGNAT/Starlink proven** - Relay-based architecture works through symmetric NAT
- **Self-hosted relay** - You run your own relay on a $5 VPS
- **GPU inference use case** - Purpose-built for exposing Ollama/vLLM through CGNAT

### Strengths of the centralized approach

- **IP-layer VPN** - Virtual network interface; any protocol works transparently
- **Mature ecosystem** - Mobile apps, web dashboard, ACLs, SSO, subnet routing, Funnel
- **Performance** - WireGuard is kernel-level and extremely fast
- **Scale** - Handles thousands of devices in an organization
- **Zero config** - "Install and sign in" onboarding
- **Platform coverage** - Runs everywhere including iOS, Android, containers

### Self-hosted control planes: a middle ground

Projects like [Headscale](https://github.com/juanfont/headscale) and [NetBird](https://github.com/netbirdio/netbird) offer self-hosted alternatives that eliminate the vendor dependency. You run the coordination server yourself, so there is no third-party account requirement and no external control over your network. However, the architecture still requires a coordination server - it is self-hosted rather than vendor-hosted, but not eliminated. The WireGuard transport layer remains the same, and you still manage a centralized piece of infrastructure. These sit in between: more sovereign than a vendor-hosted control plane, less decentralized than peer-up's DHT-based approach.

---

## How does peer-up differ from other P2P and mesh tools?

### Direct competitors

#### Hyprspace - Most similar in the libp2p ecosystem

- **Stack**: Go + libp2p + IPFS DHT (same as peer-up)
- **What it does**: Lightweight VPN that creates TUN interfaces, uses DHT for discovery, NAT hole-punching via libp2p
- **Key features**: Virtual IP addresses, IPv6 routing, Service Network (subdomain-based service addressing)
- **Difference**: Hyprspace operates at the IP layer (TUN/TAP VPN), not TCP service proxy. No invite/onboarding flow, no relay-first architecture.
- **Link**: https://github.com/hyprspace/hyprspace

#### connet - Similar concept, different stack

- **Stack**: Go + QUIC (not libp2p)
- **What it does**: P2P reverse proxy with NAT traversal, inspired by frp/ngrok/rathole
- **Key features**: Source + destination clients, QUIC protocol, NAT-PMP support, certificate-based auth
- **Difference**: Uses QUIC directly instead of libp2p. No DHT discovery, no friendly naming, no init wizard.
- **Link**: https://github.com/connet-dev/connet

#### SomajitDey/tunnel - Simpler alternative

- **Stack**: Bash scripts + HTTP relay (piping-server)
- **What it does**: P2P TCP/UDP port forwarding through an HTTP relay
- **Difference**: Much simpler (bash scripts), no libp2p, no DHT, no connection gating.
- **Link**: https://github.com/SomajitDey/tunnel

### Adjacent projects

#### Hyperswarm / Holepunch - DHT-assisted hole punching

- **Stack**: Node.js / C, HyperDHT, UTP + TCP
- **What it does**: P2P networking library powering [Keet](https://keet.io/) (encrypted P2P video/chat). DHT nodes actively assist with hole punching coordination.
- **Key features**: HyperDHT for discovery and relay-assisted hole punching, Noise protocol encryption, Hypercore for data replication
- **Difference**: Smaller ecosystem, fewer transports (no QUIC, no WebSocket), no anti-censorship story. Tightly coupled to the Hypercore/Dat ecosystem. Node.js-native (not Go). Hole punching may have higher success rates in some NAT scenarios because DHT nodes actively broker the handshake.
- **Link**: https://github.com/holepunchto/hyperswarm

#### Iroh - Library competitor to libp2p itself

- **Stack**: Rust, QUIC, custom relay protocol
- **What it does**: "Dial by public key" - P2P connectivity library with higher NAT traversal success rate than libp2p (~90%+ vs ~70%)
- **Difference**: A library, not an end-user tool. There's a `libp2p-iroh` transport adapter for using Iroh's NAT traversal within libp2p.
- **Link**: https://github.com/n0-computer/iroh

#### Nebula - Different stack, same goal

- **Stack**: Go, custom protocol (not WireGuard, not libp2p)
- **What it does**: P2P overlay network from Slack, full mesh with lighthouse nodes
- **Difference**: Certificate-authority model. **No relay fallback** - if hole-punching fails (e.g., CGNAT/Starlink), the connection simply doesn't work.
- **Link**: https://github.com/slackhq/nebula

#### Headscale / NetBird - Self-hosted coordination planes

- **Headscale**: Open source Tailscale control server - uses official Tailscale clients
- **NetBird**: Full self-hosted mesh with WireGuard, management service, signal server, relay
- **Difference**: Both are WireGuard-based, not libp2p. Different philosophy - they replicate Tailscale's architecture with self-hosted infrastructure, peer-up builds something structurally different.

### Comparison table

| Project | Stack | Layer | Relay fallback | CGNAT works | Onboarding | Self-sovereign |
|---------|-------|-------|---------------|-------------|------------|----------------|
| **peer-up** | Go + libp2p | TCP service proxy | Yes (circuit relay v2) | Yes | `init` wizard + invite/join | Yes |
| **Hyprspace** | Go + libp2p | IP layer (TUN) | Yes (circuit relay) | Yes | Manual config | Yes |
| **Hyperswarm** | Node.js + HyperDHT | Library | Yes (DHT-assisted) | Yes | API only | Yes |
| **connet** | Go + QUIC | TCP proxy | Yes (control server) | Partial | Manual config | Yes |
| **tunnel** | Bash + HTTP | TCP/UDP proxy | Yes (HTTP relay) | Yes | CLI flags | Yes |
| **Iroh** | Rust + QUIC | Library | Yes (home relay) | Yes | API only | No (uses Iroh's relays) |
| **Nebula** | Go + custom | IP layer (TUN) | No | No | Certificate CA | Yes |
| **Tailscale** | Go + WireGuard | IP layer (TUN) | Yes (DERP) | Yes | SSO sign-in | No |
| **Headscale** | Go + WireGuard | IP layer (TUN) | Yes (DERP) | Yes | SSO sign-in | Partial (self-hosted control) |
| **NetBird** | Go + WireGuard | IP layer (TUN) | Yes | Yes | Dashboard | Partial (self-hosted control) |

### Blockchain P2P networks as reference points

These are not competitors but useful reference points. Their P2P stacks solve different problems (block propagation, consensus) but share underlying technology with peer-up:

| Network | P2P Stack | Discovery | NAT Traversal | Encryption | Key Insight |
|---------|-----------|-----------|---------------|------------|-------------|
| **Bitcoin** | Custom (TCP only) | DNS seeds + addr gossip | None | BIP 324 (added 2023 - was plaintext for 14 years) | Simplicity is strength; 17 years of adversarial hardening |
| **Ethereum (execution)** | devp2p / RLPx | discv5 (UDP) | None (public IPs expected) | ECIES | Legacy layer, pre-Merge |
| **Ethereum (consensus)** | **libp2p** (same as peer-up) | discv5 (chose over Kademlia) | Minimal | Noise protocol | Validates libp2p for critical infrastructure |
| **Filecoin** | libp2p | Kademlia DHT | Circuit relay | Noise / TLS 1.3 | Largest libp2p deployment by data volume |
| **Polkadot** | libp2p (Rust) | Kademlia DHT | Circuit relay | Noise | Multi-chain P2P; validates rust-libp2p |

---

## How do relay architectures compare?

Three broad approaches to relay design exist in the P2P networking space: self-hosted relays where you control the infrastructure, vendor-operated relays where the service provider runs them, and hybrid approaches that blend elements of both.

### Hole-punching success (when no relay is needed)

| Protocol | NAT traversal success | Technique |
|----------|----------------------|-----------|
| **Circuit Relay v2 + DCUtR** (self-hosted) | ~70% | STUN-like, coordinate via relay, single punch attempt |
| **Iroh** (hybrid) | ~90%+ | Tailscale-inspired, aggressive probing, multiple strategies |
| **Tailscale DERP + STUN** (vendor-operated) | ~92-94% | Most mature, years of iteration, birthday attack techniques |
| **WireGuard alone** | ~0% behind CGNAT | No relay, no hole-punching |
| **Nebula** | ~60-70% | Lighthouse-based, no relay fallback |

**Important**: With Starlink CGNAT (symmetric NAT), hole-punching success is **0% for all of them**. Every single one falls back to relay. The hole-punch success rates only matter for regular NAT (home routers, etc.).

### Relay quality (when traffic stays on relay)

| | **Circuit Relay v2 (self-hosted)** | **Iroh relay (hybrid)** | **Tailscale DERP (vendor-operated)** |
|---|---|---|---|
| **Throughput** | Your VPS bandwidth | Iroh's servers | Tailscale's servers |
| **Latency** | Your VPS location | Nearest Iroh relay | Nearest DERP node |
| **Protocol overhead** | Minimal (libp2p framing) | Minimal (UDP-over-HTTP) | Minimal (DERP framing) |
| **Encryption** | Noise protocol (libp2p) | QUIC TLS | WireGuard (ChaCha20) |
| **You control limits** | Yes - unlimited duration/data | No | No |
| **Relay sees content** | No (end-to-end encrypted) | No (end-to-end encrypted) | No (end-to-end encrypted) |

All three are roughly equivalent in relay quality. The relay is a dumb pipe forwarding encrypted bytes. Performance depends on infrastructure, not protocol.

### Connection establishment speed

| Protocol | Time to first byte | Why |
|----------|-------------------|-----|
| **Circuit Relay v2** (self-hosted) | 5-15 seconds | Connect -> reserve -> DHT lookup -> peer connects -> DCUtR attempt |
| **Iroh** (hybrid) | 1-3 seconds | Persistent relay connection, peer dials by key, relay forwards immediately |
| **Tailscale DERP** (vendor-operated) | <1 second | Always-on DERP connection, peer dials by WireGuard key |

Circuit Relay v2 is slower because it involves a reservation step and DHT lookup. Iroh and Tailscale maintain persistent relay connections.

---

## How do P2P networking stacks compare?

These comparisons are reference points showing where peer-up's libp2p foundation sits in the broader P2P landscape. Bitcoin and Ethereum are not competitors - they solve different problems (block propagation, consensus) - but their P2P stacks share enough architectural overlap to be instructive.

### Bitcoin's P2P stack

Bitcoin's P2P protocol has **less overhead per message**, but it cannot do what peer-up needs.

#### The comparison

| | **Bitcoin P2P** | **libp2p (peer-up)** |
|---|---|---|
| **Transport** | Raw TCP only | TCP, QUIC, WebSocket, WebRTC |
| **Handshake** | 1.5-3 RTTs (~296 bytes) | 4+ RTTs (TCP) / 3 RTTs (QUIC) |
| **Per-message overhead** | 24 bytes (fixed header) | 12 bytes (Yamux) + encryption framing |
| **Encryption** | None | TLS 1.3 or Noise (mandatory) |
| **Multiplexing** | None (1 connection = 1 stream) | Yes (many streams per connection) |
| **NAT/CGNAT traversal** | No - requires port forwarding | Yes - relay, hole punching, AutoNAT |
| **Bulk data transfer** | Fast (minimal overhead) | Comparable once connected |

#### Why Bitcoin P2P is "faster"

It is simpler, not fundamentally faster. Bitcoin uses raw TCP with a 24-byte binary header and zero encryption. No protocol negotiation, no multiplexing, no security handshake. It is lean because it *trusts nothing* at the network layer - blocks are verified cryptographically after receipt anyway.

#### Why it does not matter for peer-up

**Bitcoin P2P cannot traverse NAT or CGNAT at all.** If both sides cannot directly reach each other, Bitcoin nodes simply cannot connect inbound. Users behind ISP CGNAT cannot run full Bitcoin nodes that accept inbound connections. Bitcoin originally had UPnP enabled by default but disabled it due to [miniupnpc vulnerabilities](https://bitcoin.org/en/alert/2015-10-12-upnp-vulnerability). It now uses PCP (Port Control Protocol), which ISP CGNAT equipment intentionally blocks.

NAT/CGNAT traversal is peer-up's entire reason for existing.

#### The key research finding

A 2021 study implemented Bitcoin's block exchange protocol on top of libp2p and found:

> *"Setting up communication channels is time-consuming, but data transfers are fast"*

Once the connection is established, **bulk throughput is comparable**. The overhead is in the handshake, not the data flow. For peer-up's use case (long-lived connections proxying SSH, Ollama, XRDP), connection setup latency is a one-time cost that becomes irrelevant.

**Source**: Barbara Guidi, Andrea Michienzi, Laura Ricci. *"A libP2P Implementation of the Bitcoin Block Exchange Protocol."* Proceedings of the 2nd International Workshop on Distributed Infrastructure for Common Good (DICG '21), ACM, 2021. DOI: [10.1145/3493426.3493822](https://dl.acm.org/doi/10.1145/3493426.3493822)

#### What peer-up does to close the gap

These optimizations shipped in Phase 4C:

1. **QUIC transport** (done) - saves 1 RTT on connection setup (3 RTTs vs 4 for TCP)
2. **DCUtR hole punching** (done) - bypass relay entirely for direct peer-to-peer
3. **Parallel dial racing** (done, Batch I) - race DHT and relay in parallel, first wins
4. **STUN probing** (done, Batch I) - classify NAT type, predict hole-punch success

Once hole punching succeeds, peer-up is essentially just encrypted TCP with 12 bytes of Yamux framing per frame - very close to Bitcoin's raw TCP speed but with encryption and NAT traversal. Connection warmup and stream pooling remain as future optimizations.

#### Bottom line

Bitcoin P2P is lean but primitive. It solved a different problem: broadcasting blocks to publicly-reachable nodes. peer-up needs relay + hole punching + encryption, and libp2p is the right tool for that. The performance gap narrows dramatically with QUIC + connection pooling + DCUtR direct connections.

### Ethereum's P2P stack

Ethereum is the most relevant comparison because **its consensus layer uses the same libp2p stack** that peer-up is built on. Ethereum actually runs two separate P2P networks.

#### Ethereum's two P2P layers

**Execution layer (devp2p/RLPx)** - the original Ethereum networking, predating The Merge:

| | **devp2p (Execution)** | **peer-up (libp2p)** |
|---|---|---|
| **Transport** | TCP only | QUIC + TCP + WebSocket |
| **Encryption** | ECIES (ECDH + AES) | Noise / TLS 1.3 |
| **Multiplexing** | Capability-based sub-protocols (eth, snap) | Yamux (any number of streams) |
| **Discovery** | discv5 (UDP-based DHT) | Kademlia DHT |
| **NAT traversal** | None - validators expected to have public IPs | AutoNAT v2 + circuit relay + DCUtR hole punching |
| **Identity** | ENR (Ethereum Node Records) | PeerID (Ed25519 multihash) |

**Consensus layer (libp2p)** - adopted for the Beacon Chain (post-Merge):

| | **Ethereum Consensus** | **peer-up** |
|---|---|---|
| **Stack** | libp2p (Go and Rust implementations) | libp2p (Go) |
| **libp2p version** | ~v0.30.x era | v0.47.0 (newer) |
| **Transports** | TCP primarily | QUIC -> TCP -> WebSocket |
| **Primary pattern** | gossipsub (topic-based pub/sub for blocks/attestations) | Point-to-point streams (service proxy) |
| **Discovery** | discv5 (custom, not libp2p Kademlia) | Kademlia DHT + relay bootstrap |
| **NAT traversal** | Minimal (validators run on servers) | Full: AutoNAT v2 + relay + hole punch |
| **Encryption** | Noise protocol | Noise / TLS 1.3 |

#### Why Ethereum chose libp2p for consensus

When Ethereum needed a P2P networking stack for the Beacon Chain - the system securing hundreds of billions of dollars - they evaluated their options and chose libp2p. The reasons:

1. **Modularity** - swap transports, security, multiplexers independently
2. **Multi-language support** - Go (Prysm), Rust (Lighthouse), Java (Teku), .NET (Nethermind) all have libp2p implementations
3. **Stream multiplexing** - essential for gossipsub topic subscriptions
4. **Noise protocol** - mutual authentication during handshake

#### Why Ethereum chose discv5 over libp2p's Kademlia for discovery

Ethereum's consensus layer uses libp2p for transport and encryption but **not** for peer discovery. They built discv5 instead:

| | **libp2p Kademlia DHT** | **Ethereum discv5** |
|---|---|---|
| **Protocol** | TCP-based | UDP-based |
| **Bandwidth** | Higher (DHT maintenance traffic) | Lower (lightweight probes) |
| **Topic advertisement** | Not built-in | Native topic-based discovery |
| **NAT handling** | Relies on relay/AutoNAT | Built-in PING/PONG with endpoint proof |
| **Purpose** | General content/peer routing | Pure peer discovery (minimal scope) |

The key reason: Kademlia DHT maintains routing tables and handles both content routing and peer discovery, which generates more background traffic than needed for pure discovery. discv5 does one thing - find peers - and does it with less bandwidth overhead.

**For peer-up**: Kademlia DHT is the right choice today because peer-up uses it for both peer discovery and rendezvous coordination, and the bandwidth overhead is negligible at current network sizes. The discv5 approach becomes interesting at larger scales where DHT maintenance traffic is measurable.

#### What this means for peer-up

peer-up's libp2p foundation is **validated by Ethereum's consensus layer** - the same networking stack secures one of the largest decentralized networks in existence. peer-up also benefits from improvements driven by Ethereum's scale: gossipsub optimizations, Noise protocol hardening, and transport upgrades all flow back to the shared libp2p codebase.

Where peer-up goes further than Ethereum's usage:
- **Full NAT traversal** (AutoNAT v2, circuit relay, DCUtR) - Ethereum validators do not need this
- **QUIC as preferred transport** - Ethereum consensus still primarily uses TCP
- **WebSocket for anti-censorship** - Ethereum has no DPI evasion story
- **Point-to-point service proxy** - different use pattern than gossipsub broadcast

---

## What does peer-up ship that others don't?

### Built-in observability

Most P2P tunnel tools ship with no metrics, no traces, and no structured audit logs. DevOps teams bolt on monitoring after the fact, poorly.

peer-up ships with Prometheus metrics (libp2p built-in + custom proxy/auth/holepunch counters), structured audit logging, and a pre-built Grafana dashboard with 29 panels out of the box. No other P2P tunnel tool ships with this level of built-in observability.

**What's next**: Distributed tracing (deferred - 35% CPU overhead not justified yet). OTLP export via Prometheus bridge when users request it.

---

## What are the open problems in P2P networking?

These are genuine gaps in every P2P/VPN/tunnel tool available today, including peer-up:

### 1. Zero-RTT proxy connection resume

When your network flickers (WiFi to cellular, WiFi dropout), every existing tool drops connections and requires a full reconnection handshake. QUIC 0-RTT session tickets could make reconnection instant - send encrypted data before the server processes the handshake.

**Who has it**: Nobody in the P2P tunnel space.
**Difficulty**: Medium (requires QUIC transport + session ticket caching).

### 2. Hardware-backed peer identity

No P2P tool stores peer private keys in TPM 2.0 (Linux servers) or Secure Enclave (macOS/iOS). Keys sit on disk, stealable by anyone with filesystem access.

**Who has it**: Nobody.
**Difficulty**: Medium (platform-specific APIs: `go-tpm`, `Security.framework`).

### 3. Kernel-bypass relay forwarding

Every relay server processes packets through the kernel network stack (syscalls per packet). eBPF/XDP or DPDK could forward relayed packets at line rate - benchmarks show [DPDK achieves 51% better throughput](https://talawah.io/blog/linux-kernel-vs-dpdk-http-performance-showdown/) than kernel stack, VPP uses 1/9th the CPUs.

**Who has it**: Nobody (Cloudflare uses XDP for DDoS, not for relay forwarding).
**Difficulty**: High (Linux-only, requires privileged access).

### 4. Formally verified protocol state machine

No P2P tool has mathematically proven that its handshake / invite / key exchange protocol is correct. Bugs in state machines cause security vulnerabilities. Formal verification tools like [Kani](https://github.com/model-checking/kani) (Rust) and [TLA+](https://lamport.azurewebsites.net/tla/tla.html) can prove correctness.

**Who has it**: AWS s2n-quic (QUIC only, not application layer). [Bert13](https://dl.acm.org/doi/10.1145/3719027.3765213) (first formally-verified post-quantum TLS 1.3 in Rust).
**Difficulty**: High (requires Rust migration for Kani, or TLA+ model of invite protocol).

### 5. Cryptographic agility (post-quantum ready)

No P2P tool supports cipher suite negotiation or hybrid classical + post-quantum handshakes. When ML-KEM mandates arrive (2026-2028), every tool will need emergency patches.

**Who has it**: Nobody in P2P. AWS and Microsoft are preparing at the infrastructure layer.
**Difficulty**: Medium (design cipher negotiation now, implement when libp2p adopts PQC).

---

**Last Updated**: 2026-02-24
