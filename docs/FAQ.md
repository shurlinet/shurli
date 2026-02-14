# peer-up FAQ & Technical Comparison

## How does peer-up compare to Tailscale?

peer-up is not a cheaper Tailscale. It's the **self-sovereign alternative** for people who care about owning their network.

### Architecture

| Aspect | **peer-up** | **Tailscale** |
|--------|------------|---------------|
| **Foundation** | libp2p (circuit relay v2, DHT, QUIC) | WireGuard (kernel-level crypto) |
| **Topology** | Client → Relay → Server (with DCUtR upgrade to direct) | Full mesh, point-to-point |
| **NAT Traversal** | Circuit relay + hole-punching (DCUtR) | DERP relay servers + STUN/hole-punching |
| **Encryption** | libp2p Noise protocol (Ed25519) | WireGuard (Curve25519) |
| **Control Plane** | None — fully decentralized (DHT + config files) | Centralized coordination server |

### Privacy & Sovereignty

| | **peer-up** | **Tailscale** |
|---|---|---|
| **Accounts** | None — no email, no OAuth | Required (Google, GitHub, etc.) |
| **Telemetry** | Zero — no data leaves your network | Coordination server sees device graph |
| **Control plane** | None — relay only forwards bytes | Centralized coordination server |
| **Key custody** | You generate, you store, you control | Keys managed via their control plane |
| **Source** | Fully open, self-hosted | Open source client, proprietary control plane |

### Features

| Feature | **peer-up** | **Tailscale** |
|---------|------------|---------------|
| **Service tunneling** | SSH, XRDP, generic TCP | Full IP-layer VPN (any protocol) |
| **Auth model** | SSH-style `authorized_keys` (peer ID allowlist) | SSO (Google, Okta, GitHub), ACLs |
| **DNS** | Friendly names in config + private DNS on relay (planned) | MagicDNS (auto device names) |
| **Platforms** | Linux, macOS (Go binary) | Linux, Windows, macOS, iOS, Android, containers |
| **Setup** | `peerup init` wizard | Download → sign in → done |
| **Admin UI** | CLI only | Web dashboard, admin console |
| **Exit nodes** | Not yet | Yes |
| **Subnet routing** | Not yet | Yes |
| **Multi-user/team** | Invite/join flow + `peerup auth` CLI | Built-in team management, SSO |

### Where peer-up wins

- **No central authority** — No account, no coordination server, no vendor dependency
- **Importable library** — `pkg/p2pnet` can be embedded into any Go application
- **CGNAT/Starlink proven** — Relay-based architecture works through symmetric NAT
- **Self-hosted relay** — You run your own relay on a $5 VPS
- **GPU inference use case** — Purpose-built for exposing Ollama/vLLM through CGNAT

### Where Tailscale wins

- **IP-layer VPN** — Virtual network interface; any protocol works transparently
- **Mature ecosystem** — Mobile apps, web dashboard, ACLs, SSO, subnet routing, Funnel
- **Performance** — WireGuard is kernel-level and extremely fast
- **Scale** — Handles thousands of devices in an organization
- **Zero config** — "Install and sign in" onboarding
- **Platform coverage** — Runs everywhere including iOS, Android, containers

---

## How does peer-up compare to other P2P projects?

### Direct Competitors

#### Hyprspace — Most similar in the libp2p ecosystem

- **Stack**: Go + libp2p + IPFS DHT (same as peer-up)
- **What it does**: Lightweight VPN that creates TUN interfaces, uses DHT for discovery, NAT hole-punching via libp2p
- **Key features**: Virtual IP addresses, IPv6 routing, Service Network (subdomain-based service addressing)
- **Difference**: Hyprspace operates at the IP layer (TUN/TAP VPN), not TCP service proxy. No invite/onboarding flow, no relay-first architecture.
- **Link**: https://github.com/hyprspace/hyprspace

#### connet — Similar concept, different stack

- **Stack**: Go + QUIC (not libp2p)
- **What it does**: P2P reverse proxy with NAT traversal, inspired by frp/ngrok/rathole
- **Key features**: Source + destination clients, QUIC protocol, NAT-PMP support, certificate-based auth
- **Difference**: Uses QUIC directly instead of libp2p. No DHT discovery, no friendly naming, no init wizard.
- **Link**: https://github.com/connet-dev/connet

#### SomajitDey/tunnel — Simpler alternative

- **Stack**: Bash scripts + HTTP relay (piping-server)
- **What it does**: P2P TCP/UDP port forwarding through an HTTP relay
- **Difference**: Much simpler (bash scripts), no libp2p, no DHT, no connection gating.
- **Link**: https://github.com/SomajitDey/tunnel

### Adjacent Projects

#### Iroh — Library competitor to libp2p itself

- **Stack**: Rust, QUIC, custom relay protocol
- **What it does**: "Dial by public key" — P2P connectivity library with higher NAT traversal success rate than libp2p (~90%+ vs ~70%)
- **Difference**: A library, not an end-user tool. There's a `libp2p-iroh` transport adapter for using Iroh's NAT traversal within libp2p.
- **Link**: https://github.com/n0-computer/iroh

#### Nebula — Different stack, same goal

- **Stack**: Go, custom protocol (not WireGuard, not libp2p)
- **What it does**: P2P overlay network from Slack, full mesh with lighthouse nodes
- **Difference**: Certificate-authority model. **No relay fallback** — if hole-punching fails (e.g., CGNAT/Starlink), the connection simply doesn't work.
- **Link**: https://github.com/slackhq/nebula

#### Headscale / NetBird — Self-hosted Tailscale alternatives

- **Headscale**: Open source Tailscale control server — uses official Tailscale clients
- **NetBird**: Full self-hosted mesh with WireGuard, management service, signal server, relay
- **Difference**: Both are WireGuard-based, not libp2p. Different philosophy — they replicate Tailscale's architecture, peer-up builds something different.

### Comparison Table

| Project | Stack | Layer | Relay fallback | CGNAT works | Onboarding | Self-sovereign |
|---------|-------|-------|---------------|-------------|------------|----------------|
| **peer-up** | Go + libp2p | TCP service proxy | Yes (circuit relay v2) | Yes | `init` wizard + invite/join | Yes |
| **Hyprspace** | Go + libp2p | IP layer (TUN) | Yes (circuit relay) | Yes | Manual config | Yes |
| **connet** | Go + QUIC | TCP proxy | Yes (control server) | Partial | Manual config | Yes |
| **tunnel** | Bash + HTTP | TCP/UDP proxy | Yes (HTTP relay) | Yes | CLI flags | Yes |
| **Iroh** | Rust + QUIC | Library | Yes (home relay) | Yes | API only | No (uses Iroh's relays) |
| **Nebula** | Go + custom | IP layer (TUN) | No | No | Certificate CA | Yes |
| **Tailscale** | Go + WireGuard | IP layer (TUN) | Yes (DERP) | Yes | SSO sign-in | No |
| **Headscale** | Go + WireGuard | IP layer (TUN) | Yes (DERP) | Yes | SSO sign-in | Partial (self-hosted control) |
| **NetBird** | Go + WireGuard | IP layer (TUN) | Yes | Yes | Dashboard | Partial (self-hosted control) |

---

## Circuit Relay v2 vs Iroh vs Tailscale DERP

### Hole-punching success (when no relay is needed)

| Protocol | NAT traversal success | Technique |
|----------|----------------------|-----------|
| **Circuit Relay v2 + DCUtR** | ~70% | STUN-like, coordinate via relay, single punch attempt |
| **Iroh** | ~90%+ | Tailscale-inspired, aggressive probing, multiple strategies |
| **Tailscale (DERP + STUN)** | ~92-94% | Most mature, years of iteration, birthday attack techniques |
| **WireGuard alone** | ~0% behind CGNAT | No relay, no hole-punching |
| **Nebula** | ~60-70% | Lighthouse-based, no relay fallback |

**Important**: With Starlink CGNAT (symmetric NAT), hole-punching success is **0% for all of them**. Every single one falls back to relay. The hole-punch success rates only matter for regular NAT (home routers, etc.).

### Relay quality (when traffic stays on relay)

| | **Circuit Relay v2 (self-hosted)** | **Iroh relay** | **Tailscale DERP** |
|---|---|---|---|
| **Throughput** | Your VPS bandwidth | Iroh's servers | Tailscale's servers |
| **Latency** | Your VPS location | Nearest Iroh relay | Nearest DERP node |
| **Protocol overhead** | Minimal (libp2p framing) | Minimal (UDP-over-HTTP) | Minimal (DERP framing) |
| **Encryption** | Noise protocol (libp2p) | QUIC TLS | WireGuard (ChaCha20) |
| **You control limits** | Yes — unlimited duration/data | No | No |
| **Relay sees content** | No (end-to-end encrypted) | No (end-to-end encrypted) | No (end-to-end encrypted) |

All three are roughly equivalent in relay quality. The relay is a dumb pipe forwarding encrypted bytes. Performance depends on infrastructure, not protocol.

### Connection establishment speed

| Protocol | Time to first byte | Why |
|----------|-------------------|-----|
| **Circuit Relay v2** | 5-15 seconds | Connect → reserve → DHT lookup → peer connects → DCUtR attempt |
| **Iroh** | 1-3 seconds | Persistent relay connection, peer dials by key, relay forwards immediately |
| **Tailscale DERP** | <1 second | Always-on DERP connection, peer dials by WireGuard key |

Circuit Relay v2 is slower because it involves a reservation step and DHT lookup. Iroh and Tailscale maintain persistent relay connections.

---

## Can I use public IPFS relay servers instead of my own?

Yes, public IPFS relays exist — thousands of them. Since Circuit Relay v2, every public IPFS node runs a relay by default. libp2p's AutoRelay can discover and use them automatically.

**But there's a catch.** Public relays have strict resource limits:

| Constraint | Public IPFS relay (v2 defaults) | Your self-hosted relay |
|-----------|-------------------------------|------------|
| **Duration** | 2 minutes per connection | Unlimited (you configure) |
| **Data cap** | 128 KB per relay session | Unlimited (you configure) |
| **Bandwidth** | ~1 Kbps (intentionally throttled) | Your VPS bandwidth |
| **Purpose** | Coordinate hole-punch, then disconnect | Full traffic relay |
| **Uptime** | Random node, could disappear | Your VPS, 99.9% uptime |
| **SSH session** | Drops after 2 min or 128 KB | Works indefinitely |

Public relays are designed as a **trampoline** — they help two peers find each other, attempt a hole-punch, and then drop off. They were never meant for sustained traffic like SSH sessions, XRDP, or LLM inference.

### Hybrid approach (possible future optimization)

```
1. Peer discovery → Use public IPFS relays (free, no VPS needed)
2. Hole-punch attempt → DCUtR via public relay
3. If hole-punch succeeds → Direct connection (no relay at all)
4. If hole-punch fails → Fall back to YOUR relay for sustained traffic
```

This would mean users who aren't behind symmetric NAT wouldn't need your relay at all. Only Starlink/CGNAT users would need the self-hosted VPS.

---

## Are Iroh's public relays the same as IPFS's public relays?

Conceptually yes — both are "someone else's relay you use for free." But the implementation differs significantly:

| | **IPFS public relays** | **Iroh's relays** |
|---|---|---|
| **Operator** | Thousands of random IPFS peers | n0 team (Iroh's company) |
| **Architecture** | Decentralized — any public node can be a relay | Centralized — Iroh runs them |
| **Data limit** | 128 KB per session | No hard cap |
| **Time limit** | 2 minutes | Persistent connection |
| **Purpose** | Trampoline for hole-punch coordination | Actual traffic fallback (like Tailscale's DERP) |
| **Reliability** | Random node could vanish anytime | Operated infrastructure |
| **Protocol** | libp2p Circuit Relay v2 | Custom protocol (UDP-over-HTTP) |

Iroh's relays are essentially **Tailscale's DERP servers for the Iroh ecosystem** — meant to carry real traffic when hole-punching fails. IPFS's public relays are just for the initial handshake.

---

## Why does peer-up use its own relay instead of public relays?

For Starlink/CGNAT (symmetric NAT) users, hole-punching **always fails**. Traffic must stay on the relay for the entire session. This means:

1. **Public IPFS relays** — Connection drops after 2 minutes or 128 KB. Unusable.
2. **Iroh's relays** — Would work, but you depend on Iroh's infrastructure and lose sovereignty.
3. **Tailscale's DERP** — Would work, but requires a Tailscale account and their control plane.
4. **Your own relay** — Works indefinitely, unlimited data, you control everything.

peer-up's self-hosted relay ($5/month VPS) is the only option that provides **both** unlimited traffic **and** full sovereignty.

---

## Why does Nebula fail with CGNAT?

Nebula uses **lighthouse nodes** (like STUN servers) to help peers discover each other's public IP:port. Then it attempts direct hole-punching.

With symmetric NAT (Starlink CGNAT), the mapped port **changes for every destination**:

```
Your machine → Lighthouse  =  public 100.64.x.x:5000
Your machine → Peer B      =  public 100.64.x.x:7832  ← DIFFERENT port
```

The port the lighthouse tells Peer B to use was allocated for the lighthouse connection, not Peer B. The hole-punch fails.

**Nebula has no relay fallback.** If hole-punching fails, the connection simply doesn't work. Tailscale falls back to DERP. peer-up falls back to circuit relay. Nebula has nothing.

---

## Why Circuit Relay v2 is the right choice for peer-up

1. **Symmetric NAT** — Hole-punch success rates are irrelevant (all protocols fail against symmetric NAT, all fall back to relay)
2. **Self-hosted relay** — You control limits, so the 128KB/2min public relay caps don't apply
3. **No vendor dependency** — Matches the self-sovereign philosophy
4. **Native to libp2p** — No additional dependencies in the Go codebase
5. **Battle-tested** — Millions of IPFS nodes use it daily
6. **Configurable** — When you run your own relay, you set your own resource limits

The only area where alternatives genuinely outperform Circuit Relay v2:
- **Connection speed**: Iroh (1-3s) and Tailscale (<1s) are faster than Circuit Relay v2 (5-15s) due to persistent relay connections
- **Hole-punch success for regular NAT**: Iroh (~90%) and Tailscale (~92%) beat DCUtR (~70%) — but this doesn't matter for symmetric NAT

For Starlink CGNAT with a self-hosted relay, Circuit Relay v2 is **functionally equivalent** to Iroh and Tailscale in relay quality.

---

## What is Circuit Relay v2?

Circuit Relay v2 is libp2p's protocol for routing traffic through an intermediary relay node when peers can't connect directly (NAT, CGNAT, firewalls). It replaced v1 in 2021.

### How it works

```
1. Peer A → Relay: "RESERVE" (request a slot)
2. Relay → A: "OK" + expiration + voucher (cryptographic proof)
3. Peer B → Relay: "CONNECT to A"
4. Relay → A: "B wants to connect"
5. A → Relay: "OK"
6. Relay bridges the two streams — data flows bidirectionally
```

The protocol splits into two sub-protocols:
- **Hop** (`/libp2p/circuit/relay/0.2.0/hop`) — client ↔ relay (reserve, connect)
- **Stop** (`/libp2p/circuit/relay/0.2.0/stop`) — relay ↔ target peer (deliver connection)

### Why v1 was replaced

v1 had no resource reservation — relays got overloaded with no way to limit usage. v2 introduced explicit reservations with configurable limits (duration, data caps, bandwidth), making it cheap to run "an army of relays for extreme horizontal scaling." Relays can reject connections with status codes like `RESOURCE_LIMIT_EXCEEDED` or `RESERVATION_REFUSED`.

### Known limitations

| Limitation | Detail |
|-----------|--------|
| **Setup latency** | 5-15 seconds (reservation + handshake + DHT lookup) |
| **No persistent connections** | Connections have hard TTL; each dial requires new reservation |
| **Reservation overhead** | Every peer must explicitly reserve before receiving relayed connections |
| **Throughput asymmetry** | Limited by relay's aggregate bandwidth, not peer bandwidth |
| **Default public limits** | 128 KB data cap, 2-minute duration (configurable on self-hosted) |

### Is there a Circuit Relay v3?

**No.** No v3 exists or is planned. libp2p's strategy is to reduce *dependence* on relays through better hole punching ([DCUtR](https://github.com/libp2p/specs/blob/master/relay/DCUtR.md) improvements, [AutoNAT v2](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md)), not to replace the relay protocol itself.

The improvements come from upgrading everything *around* the relay — see the next FAQ entry.

**Source**: [Circuit Relay v2 Specification](https://github.com/libp2p/specs/blob/master/relay/circuit-v2.md)

---

## What libp2p improvements should peer-up adopt?

peer-up currently uses go-libp2p v0.47.0 (relay-server uses v0.38.2). Several improvements have shipped since then that would meaningfully improve performance, security, and reliability.

### AutoNAT v2 (go-libp2p v0.41.1+)

The old AutoNAT tested "is my node reachable?" as a binary yes/no. v2 tests **individual addresses**:

| | **AutoNAT v1** | **AutoNAT v2** |
|---|---|---|
| **Tests** | Whole node reachability | Each address independently |
| **Verification** | Trust the dialer's claim | Nonce-based proof (dial-back) |
| **Amplification risk** | Yes (could be spoofed) | No (client must transfer 30-100KB first) |
| **IPv4/IPv6** | Can't distinguish | Tests each separately |

A peer-up node could know "IPv4 is behind NAT but IPv6 is public" and make smarter connection decisions.

**Source**: [AutoNAT v2 Specification](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md)

### Smart Dialing (go-libp2p v0.28.0+)

Old behavior: dial all peer addresses in parallel, abort on first success. Wasteful and creates network churn.

New behavior: ranks addresses intelligently, prioritizes QUIC over TCP, dials sequentially with fast failover. When a peer has both relay and direct addresses, smart dialing tries the direct path first.

### Resource Manager

DAG-based resource constraints at system, protocol, and per-peer levels. This is the proper replacement for peer-up's `WithInfiniteLimits()`:

- Per-peer connection and stream limits
- Per-peer bandwidth caps
- Memory and file descriptor budgets
- Rate limiting (1 connection per 5s per IP, 16-burst default)
- Prevents one peer from exhausting all relay resources

### QUIC Source Address Verification

Validates that the peer's source IP isn't spoofed. Prevents relay from being used as a DDoS reflector. Built into go-libp2p's QUIC transport since quic-go v0.54.0.

### DCUtR Hole Punching Improvements

No v2 of DCUtR, but continuous refinement:
- RTT measurement retries on each attempt (prevents one bad measurement from ruining all retries)
- TCP hole punching now achieves "statistically indistinguishable success rates" from UDP
- Measured success: **70% ± 7.1%** across 4.4M attempts from 85K+ networks in 167 countries

**Source**: [Large Scale NAT Traversal Measurement Study](https://arxiv.org/html/2510.27500v1), [libp2p Hole Punching blog](https://blog.ipfs.tech/2022-01-20-libp2p-hole-punching/)

### What peer-up plans to do (Phase 4C)

| Optimization | Impact | Effort |
|-------------|--------|--------|
| **Upgrade go-libp2p** to latest | Gains all of the above automatically | Low |
| **Replace `WithInfiniteLimits()`** with Resource Manager scopes | Eliminates relay resource exhaustion vulnerability | Medium |
| **Enable DCUtR** in proxy command | Bypasses relay entirely when hole punch succeeds | Low |
| **Connection warmup** | Pre-establish relay connection at startup (eliminates 5-15s per-session setup) | Low |
| **Stream pooling** | Reuse streams instead of fresh ones per TCP connection | Medium |
| **Persistent relay reservation** | Keep reservation alive with periodic refresh instead of re-reserving per connection | Medium |
| **QUIC as default transport** | 1 fewer RTT on connection setup (3 vs 4 for TCP) | Low |

Together, these changes would bring connection setup from 5-15 seconds closer to 1-3 seconds, matching Iroh's performance while keeping the self-sovereign architecture.

---

## Is Bitcoin's P2P faster than libp2p?

Bitcoin's P2P protocol has **less overhead per message**, but it can't do what peer-up needs.

### The Comparison

| | **Bitcoin P2P** | **libp2p** |
|---|---|---|
| **Transport** | Raw TCP only | TCP, QUIC, WebSocket, WebRTC |
| **Handshake** | 1.5-3 RTTs (~296 bytes) | 4+ RTTs (TCP) / 3 RTTs (QUIC) |
| **Per-message overhead** | 24 bytes (fixed header) | 12 bytes (Yamux) + encryption framing |
| **Encryption** | None | TLS 1.3 or Noise (mandatory) |
| **Multiplexing** | None (1 connection = 1 stream) | Yes (many streams per connection) |
| **NAT/CGNAT traversal** | No — requires port forwarding | Yes — relay, hole punching, AutoNAT |
| **Bulk data transfer** | Fast (minimal overhead) | Comparable once connected |

### Why Bitcoin P2P is "faster"

It's simpler — not fundamentally faster. Bitcoin uses raw TCP with a 24-byte binary header and zero encryption. No protocol negotiation, no multiplexing, no security handshake. It's lean because it *trusts nothing* at the network layer — blocks are verified cryptographically after receipt anyway.

### Why it doesn't matter for peer-up

**Bitcoin P2P cannot traverse NAT or CGNAT at all.** If both sides can't directly reach each other, Bitcoin nodes simply can't connect inbound. Users behind ISP CGNAT cannot run full Bitcoin nodes that accept inbound connections. Bitcoin originally had UPnP enabled by default but disabled it due to [miniupnpc vulnerabilities](https://bitcoin.org/en/alert/2015-10-12-upnp-vulnerability). It now uses PCP (Port Control Protocol), which ISP CGNAT equipment intentionally blocks.

NAT/CGNAT traversal is peer-up's entire reason for existing.

### The key research finding

A 2021 study implemented Bitcoin's block exchange protocol on top of libp2p and found:

> *"Setting up communication channels is time-consuming, but data transfers are fast"*

Once the connection is established, **bulk throughput is comparable**. The overhead is in the handshake, not the data flow. For peer-up's use case (long-lived connections proxying SSH, Ollama, XRDP), connection setup latency is a one-time cost that becomes irrelevant.

**Source**: Barbara Guidi, Andrea Michienzi, Laura Ricci. *"A libP2P Implementation of the Bitcoin Block Exchange Protocol."* Proceedings of the 2nd International Workshop on Distributed Infrastructure for Common Good (DICG '21), ACM, 2021. DOI: [10.1145/3493426.3493822](https://dl.acm.org/doi/10.1145/3493426.3493822)

### What peer-up does to close the gap

These optimizations are planned in Phase 4C (Core Hardening):

1. **QUIC transport** — saves 1 RTT on connection setup (3 RTTs vs 4 for TCP)
2. **Connection warmup** — pre-establish connection at `peerup proxy` startup
3. **Stream pooling** — reuse streams instead of fresh ones per TCP connection
4. **DCUtR hole punching** — bypass relay entirely for direct peer-to-peer (approaches Bitcoin-like raw TCP speed)

Once hole punching succeeds, peer-up is essentially just encrypted TCP with 12 bytes of Yamux framing per frame — very close to Bitcoin's raw TCP speed but with encryption and NAT traversal.

### Bottom line

Bitcoin P2P is lean but primitive. It solved a different problem: broadcasting blocks to publicly-reachable nodes. peer-up needs relay + hole punching + encryption — and libp2p is the right tool for that. The performance gap narrows dramatically with QUIC + connection pooling + DCUtR direct connections.

---

## What emerging technologies could benefit peer-up?

### Protocols to watch

| Protocol | What it gives peer-up | Status (2026) | Phase |
|----------|----------------------|---------------|-------|
| **MASQUE** ([RFC 9298](https://www.ietf.org/rfc/rfc9298.html)) | HTTP/3 relay that looks like HTTPS to deep packet inspection. 0-RTT session resumption for instant reconnection after network switch. | Production (Cloudflare deploys across 330+ datacenters) | Future |
| **Post-quantum Noise** (ML-KEM / FIPS 203) | Quantum-resistant handshakes. Regulatory mandates expected 2026-2028. | AWS KMS, Windows 11 shipping ML-KEM. libp2p not yet adopted. | Future |
| **QUIC v2** ([RFC 9369](https://datatracker.ietf.org/doc/rfc9369/)) | Anti-ossification — randomized version field prevents middleboxes from special-casing QUIC v1. | Finalized | 4C |
| **WebTransport** | Browser-native QUIC transport (replaces WebSocket for anti-censorship). Lower overhead, native datagrams. | Chrome/Firefox production, Safari flag-only | Future |
| **W3C DID v1.1** | Decentralized Identifiers — peer IDs in a standard, interoperable format (`did:key`, `did:peer`). | [First Public Draft 2025](https://www.w3.org/TR/did-1.1/) | Future |
| **eBPF / XDP** | Kernel-bypass packet filtering at millions of packets/sec. DDoS mitigation without userspace overhead. | Production (Cloudflare, Meta, Netflix) | 4C/Future |

### MASQUE: The next-generation relay transport

[MASQUE](https://www.ietf.org/rfc/rfc9298.html) (Multiplexed Application Substrate over QUIC Encryption) is an HTTP/3 proxying protocol with properties that directly address Circuit Relay v2's weaknesses:

| | **Circuit Relay v2** | **MASQUE** |
|---|---|---|
| **Looks like** | Custom libp2p protocol | Standard HTTPS traffic |
| **DPI evasion** | Requires WebSocket wrapping | Native — it IS HTTP/3 |
| **Session resume** | New reservation per connection | 0-RTT resume (TLS 1.3 tickets) |
| **Multiplexing** | Via Yamux (12-byte frames) | Native QUIC streams |
| **Infrastructure** | Self-hosted relay | Self-hosted or Cloudflare's global network |
| **Browser support** | No (requires native client) | Yes (WebTransport API) |

peer-up could offer MASQUE as an alternative relay transport alongside Circuit Relay v2 — giving users the choice between libp2p-native P2P and HTTP/3-based relay for environments where traffic must look like standard HTTPS.

### Post-quantum cryptography: The coming mandate

peer-up currently uses Noise protocol with Ed25519 (classical cryptography). Quantum computers could eventually break this. The industry is preparing:

- **NIST finalized** ML-KEM (FIPS 203) and ML-DSA (FIPS 204) as post-quantum standards
- **AWS** KMS, ACM, and Secrets Manager support ML-KEM (Nov 2025)
- **Windows 11/Server 2025** ship with built-in ML-KEM and ML-DSA
- **CRYSTALS-Kyber** being phased out in favor of ML-KEM (transition by 2026)
- **Hybrid approach**: Run classical + post-quantum in parallel during transition

For peer-up, the path is:
1. **Watch** libp2p's adoption of post-quantum Noise variants
2. **Design** cipher suite selection into the architecture (cryptographic agility)
3. **Implement** hybrid Noise + ML-KEM when libp2p support lands

**Sources**: [NIST PQC Standards](https://www.nist.gov/pqcrypto), [AWS ML-KEM Support](https://aws.amazon.com/blogs/security/ml-kem-post-quantum-tls-now-supported-in-aws-kms-acm-and-secrets-manager/)

### eBPF: Relay-server hardening at kernel speed

[eBPF](https://ebpf.io/) (extended Berkeley Packet Filter) allows running sandboxed programs in the Linux kernel without modifying kernel source. For peer-up's relay server:

- **XDP (eXpress Data Path)**: Process packets before they reach the network stack — millions of packets/sec DDoS mitigation
- **Rate limiting**: Per-IP connection throttling at kernel level (faster than iptables)
- **Runtime monitoring**: Detect exploitation attempts on the relay via syscall tracing (Falco, Tetragon)
- **Profiling**: Trace packet processing bottlenecks without instrumentation overhead

This complements the userspace hardening (Resource Manager, per-peer limits) with kernel-level defense. Requires Linux kernel >= 5.8.

### Zero-RTT proxy connection resume

**The problem**: When a laptop switches from WiFi to cellular (or WiFi flickers), all TCP connections through the proxy drop. The user must wait for reconnection (5-15 seconds with Circuit Relay v2).

**The solution**: QUIC 0-RTT session resumption. The client caches a session ticket from the previous connection. On reconnect, it sends encrypted data in the very first packet — before the server even processes the handshake.

**Who has this**: Cloudflare's MASQUE relays, QUIC-native applications.
**Who doesn't**: WireGuard (stateless, reconnects fast but not 0-RTT), all current P2P tunnel tools.

This is a future optimization for peer-up's QUIC transport — particularly valuable for mobile clients (Phase 4G).

---

## Why does peer-up use Go instead of Rust?

### The trade-off

| Factor | **Go** | **Rust** |
|--------|--------|----------|
| Development speed | Fast — the reason peer-up exists today | 2-3x slower initial development |
| GC pauses at scale | 10s pauses observed at 600K connections | None — no garbage collector |
| Memory per connection | ~28KB (GC overhead, interface boxing) | ~4-8KB (zero-cost abstractions) |
| libp2p ecosystem | Mature (go-libp2p, most examples) | Growing (rust-libp2p, Iroh) |
| Formal verification | Limited | Strong (s2n-quic has 300+ Kani harnesses) |
| Binary size | ~15-20MB | ~5-10MB |
| Cross-compilation | Trivial (`GOOS=linux GOARCH=arm64`) | Requires target toolchain setup |
| Concurrency model | Goroutines (simple, GC-managed) | async/await (no runtime overhead) |

### Why Go is right for now

Go's simplicity enabled rapid iteration through 7 phases of development. The libp2p Go ecosystem is the most mature, with the most examples and documentation. For a project with 1-100 concurrent connections (typical home use), Go's performance is more than adequate.

### When Rust becomes worth it

At scale — when a relay server handles thousands of concurrent circuits, or when the proxy loop becomes CPU-bound. The hot paths (packet forwarding in the relay, bidirectional proxy loop, SOCKS5 gateway) are candidates for selective Rust rewrite via FFI, not a full project rewrite.

### Rust libraries to watch

| Library | What it does | Why it matters |
|---------|-------------|----------------|
| **[Iroh](https://github.com/n0-computer/iroh)** | Rust P2P library, QUIC-native | ~90% NAT traversal success, QUIC multipath, approaching 1.0 |
| **[Quinn](https://github.com/quinn-rs/quinn)** | Pure Rust QUIC implementation | Used by Iroh, high performance, no C FFI |
| **[s2n-quic](https://github.com/aws/s2n-quic)** | AWS's Rust QUIC | Formal verification with Kani, production-tested in AWS |
| **[tokio](https://github.com/tokio-rs/tokio)** | Async runtime | LTS until Sept 2026, powers hyper (HTTP/2 + HTTP/3) |

### The hybrid strategy

peer-up's planned approach:
1. **Now through Phase 4E**: Ship in Go. Fix goroutine lifecycle, tune GC, add observability.
2. **Phase 4F+**: Profile hot paths under load. Selectively rewrite proxy loop / relay forwarding in Rust via FFI if performance demands it.
3. **Long-term**: Re-evaluate full Rust migration only if market demands 100x throughput and there's engineering capacity for it.

**Sources**: [Rust vs Go (Bitfield)](https://bitfieldconsulting.com/posts/rust-vs-go), [Go GC Guide](https://tip.golang.org/doc/gc-guide), [Iroh roadmap](https://www.iroh.computer/roadmap)

---

## What features does no existing P2P tool provide?

These are genuine gaps in every P2P/VPN/tunnel tool available today:

### 1. Zero-RTT proxy connection resume

When your network flickers (WiFi→cellular, WiFi dropout), every existing tool drops connections and requires a full reconnection handshake. QUIC 0-RTT session tickets could make reconnection instant — send encrypted data before the server processes the handshake.

**Who has it**: Nobody in the P2P tunnel space.
**Difficulty**: Medium (requires QUIC transport + session ticket caching).

### 2. Hardware-backed peer identity

No P2P tool stores peer private keys in TPM 2.0 (Linux servers) or Secure Enclave (macOS/iOS). Keys sit on disk, stealable by anyone with filesystem access.

**Who has it**: Nobody.
**Difficulty**: Medium (platform-specific APIs: `go-tpm`, `Security.framework`).

### 3. Kernel-bypass relay forwarding

Every relay server processes packets through the kernel network stack (syscalls per packet). eBPF/XDP or DPDK could forward relayed packets at line rate — benchmarks show [DPDK achieves 51% better throughput](https://talawah.io/blog/linux-kernel-vs-dpdk-http-performance-showdown/) than kernel stack, VPP uses 1/9th the CPUs.

**Who has it**: Nobody (Cloudflare uses XDP for DDoS, not for relay forwarding).
**Difficulty**: High (Linux-only, requires privileged access).

### 4. Built-in observability (OpenTelemetry)

No P2P tunnel tool ships with metrics, traces, or structured audit logs. DevOps teams bolt on monitoring after the fact, poorly. [OpenTelemetry](https://opentelemetry.io/) is table stakes for production infrastructure.

**Who has it**: Nobody in the P2P space.
**Difficulty**: Low (OpenTelemetry Go SDK, instrument key paths).

### 5. Formally verified protocol state machine

No P2P tool has mathematically proven that its handshake / invite / key exchange protocol is correct. Bugs in state machines cause security vulnerabilities. Formal verification tools like [Kani](https://github.com/model-checking/kani) (Rust) and [TLA+](https://lamport.azurewebsites.net/tla/tla.html) can prove correctness.

**Who has it**: AWS s2n-quic (QUIC only, not application layer). [Bert13](https://dl.acm.org/doi/10.1145/3719027.3765213) (first formally-verified post-quantum TLS 1.3 in Rust).
**Difficulty**: High (requires Rust migration for Kani, or TLA+ model of invite protocol).

### 6. Cryptographic agility (post-quantum ready)

No P2P tool supports cipher suite negotiation or hybrid classical + post-quantum handshakes. When ML-KEM mandates arrive (2026-2028), every tool will need emergency patches.

**Who has it**: Nobody in P2P. AWS and Microsoft are preparing at the infrastructure layer.
**Difficulty**: Medium (design cipher negotiation now, implement when libp2p adopts PQC).

---

**Last Updated**: 2026-02-14
