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
| **Multi-user/team** | Manual peer ID exchange (invite flow planned) | Built-in team management, SSO |

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
| **peer-up** | Go + libp2p | TCP service proxy | Yes (circuit relay v2) | Yes | `init` wizard + invite (planned) | Yes |
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

**Last Updated**: 2026-02-13
