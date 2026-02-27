---
title: "Technical Deep Dives"
weight: 5
description: "libp2p improvements, emerging technologies, and the Go vs Rust trade-off."
---
<!-- Auto-synced from docs/faq/technical-deep-dives.md by sync-docs - do not edit directly -->


## What libp2p improvements has Shurli adopted?

Shurli uses go-libp2p v0.47.0. Several improvements have shipped since then that would meaningfully improve performance, security, and reliability.

### AutoNAT v2 (go-libp2p v0.41.1+)

The old AutoNAT tested "is my node reachable?" as a binary yes/no. v2 tests **individual addresses**:

| | **AutoNAT v1** | **AutoNAT v2** |
|---|---|---|
| **Tests** | Whole node reachability | Each address independently |
| **Verification** | Trust the dialer's claim | Nonce-based proof (dial-back) |
| **Amplification risk** | Yes (could be spoofed) | No (client must transfer 30-100KB first) |
| **IPv4/IPv6** | Can't distinguish | Tests each separately |

A Shurli node could know "IPv4 is behind NAT but IPv6 is public" and make smarter connection decisions.

**Source**: [AutoNAT v2 Specification](https://github.com/libp2p/specs/blob/master/autonat/autonat-v2.md)

### Smart Dialing (go-libp2p v0.28.0+)

Old behavior: dial all peer addresses in parallel, abort on first success. Wasteful and creates network churn.

New behavior: ranks addresses intelligently, prioritizes QUIC over TCP, dials sequentially with fast failover. When a peer has both relay and direct addresses, smart dialing tries the direct path first.

### Resource Manager

DAG-based resource constraints at system, protocol, and per-peer levels. This is the proper replacement for Shurli's `WithInfiniteLimits()`:

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

### What Shurli has done (through Phase 5 - shipped)

| Optimization | Status |
|-------------|--------|
| **Upgraded go-libp2p** to v0.47.0 | Done |
| **Replaced `WithInfiniteLimits()`** with Resource Manager (auto-scaled limits) | Done |
| **Enabled DCUtR** in proxy command | Done (+ parallel dial racing in Batch I) |
| **Persistent relay reservation** | Done (periodic refresh in background goroutine) |
| **QUIC as default transport** | Done (3 RTTs vs 4 for TCP) |
| **Adaptive path selection** | Done (Batch I: interface discovery, STUN probing, every-peer-is-a-relay) |
| **Relay pairing codes** | Done (Post-I-1: relay admin generates codes, joiners connect in one command) |
| **SAS verification** | Done (Post-I-1: OMEMO-style 4-emoji fingerprint, persistent [UNVERIFIED] badge) |
| **Reachability grades** | Done (Post-I-1: A-F scale from interface discovery + STUN results) |
| **PAKE-secured invite** | Done (Pre-I-b: encrypted handshake, v1 cleartext deleted) |
| **Private DHT namespaces** | Done (Pre-I-c: `discovery.network` for isolated peer groups) |
| **Daemon-first commands** | Done (Post-I-1: ping/traceroute try daemon API first, fall back to standalone) |
| **Peer introduction delivery** | Done (Post-I-2: `/shurli/peer-notify/1.0.0`, relay pushes introductions with HMAC proofs) |
| **HMAC group commitment** | Done (Post-I-2: `HMAC-SHA256(token, groupID)` proves token possession) |
| **Relay admin socket** | Done (Post-I-2: Unix socket + cookie auth, `relay pair` is HTTP client) |
| **Sovereign interaction history** | Done (Post-I-2: per-peer `peer_history.json`, Welford's running average) |
| **Startup race fix** | Done (Pre-Phase 5: handlers registered before DHT bootstrap) |
| **Stale address detection** | Done (Pre-Phase 5: `[stale?]` labels after network change) |
| **systemd/launchd services** | Done (Pre-Phase 5: `shurli service install/start/stop/status`) |
| **Native mDNS via dns_sd.h** | Done (Phase 5: CGo binding to platform mDNS daemon, zeroconf fallback) |
| **PeerManager lifecycle** | Done (Phase 5: watchlist, reconnect loop, exponential backoff, event-driven state) |
| **Stale connection cleanup** | Done (Phase 5: match connection local IPs against removed interfaces, instant close) |
| **Immediate reconnect trigger** | Done (Phase 5: `reconnectNow` channel wakes loop after network change) |
| **IPv6 path probing** | Done (Phase 5: source-bound TCP probes bypass macOS utun, cross-ISP DIRECT at 23ms) |
| **mDNS LAN-first connect** | Done (Phase 5: private IPv4 subnet filter, peerstore ordering, ForceDirectDial) |
| **Relay-discard logic** | Done (Phase 5: PeerManager discards relay when mDNS direct exists) |
| **Automatic WiFi transition** | Done (Phase 5: no daemon restart on any network switch, 5-15s recovery) |

Connection setup: 3-10 seconds via parallel dial racing. WiFi transition: 5-15 seconds automatic recovery. Connection priority: LAN (mDNS) > Direct IPv6 (path probing) > Relay (fallback). Tested on 5 physical networks.

---

## What emerging technologies could benefit Shurli?

### Protocols to watch

| Protocol | What it gives Shurli | Status (2026) | Phase |
|----------|----------------------|---------------|-------|
| **MASQUE** ([RFC 9298](https://www.ietf.org/rfc/rfc9298.html)) | HTTP/3 relay that looks like HTTPS to deep packet inspection. 0-RTT session resumption for instant reconnection after network switch. | Production (Cloudflare deploys across 330+ datacenters) | Future |
| **Post-quantum Noise** (ML-KEM / FIPS 203) | Quantum-resistant handshakes. Regulatory mandates expected 2026-2028. | AWS KMS, Windows 11 shipping ML-KEM. libp2p not yet adopted. | Future |
| **QUIC v2** ([RFC 9369](https://datatracker.ietf.org/doc/rfc9369/)) | Anti-ossification - randomized version field prevents middleboxes from special-casing QUIC v1. | Finalized | 4C |
| **WebTransport** | Browser-native QUIC transport (replaces WebSocket for anti-censorship). Lower overhead, native datagrams. | Chrome/Firefox production, Safari flag-only | Future |
| **W3C DID v1.1** | Decentralized Identifiers - peer IDs in a standard, interoperable format (`did:key`, `did:peer`). | [First Public Draft 2025](https://www.w3.org/TR/did-1.1/) | Future |
| **eBPF / XDP** | Kernel-bypass packet filtering at millions of packets/sec. DDoS mitigation without userspace overhead. | Production (Cloudflare, Meta, Netflix) | 4C/Future |

### MASQUE: The next-generation relay transport

[MASQUE](https://www.ietf.org/rfc/rfc9298.html) (Multiplexed Application Substrate over QUIC Encryption) is an HTTP/3 proxying protocol with properties that directly address Circuit Relay v2's weaknesses:

| | **Circuit Relay v2** | **MASQUE** |
|---|---|---|
| **Looks like** | Custom libp2p protocol | Standard HTTPS traffic |
| **DPI evasion** | Requires WebSocket wrapping | Native - it IS HTTP/3 |
| **Session resume** | New reservation per connection | 0-RTT resume (TLS 1.3 tickets) |
| **Multiplexing** | Via Yamux (12-byte frames) | Native QUIC streams |
| **Infrastructure** | Self-hosted relay | Self-hosted or Cloudflare's global network |
| **Browser support** | No (requires native client) | Yes (WebTransport API) |

Shurli could offer MASQUE as an alternative relay transport alongside Circuit Relay v2 - giving users the choice between libp2p-native P2P and HTTP/3-based relay for environments where traffic must look like standard HTTPS.

### Post-quantum cryptography: The coming mandate

Shurli currently uses Noise protocol with Ed25519 (classical cryptography). Quantum computers could eventually break this. The industry is preparing:

- **NIST finalized** ML-KEM (FIPS 203) and ML-DSA (FIPS 204) as post-quantum standards
- **AWS** KMS, ACM, and Secrets Manager support ML-KEM (Nov 2025)
- **Windows 11/Server 2025** ship with built-in ML-KEM and ML-DSA
- **CRYSTALS-Kyber** being phased out in favor of ML-KEM (transition by 2026)
- **Hybrid approach**: Run classical + post-quantum in parallel during transition

For Shurli, the path is:
1. **Watch** libp2p's adoption of post-quantum Noise variants
2. **Design** cipher suite selection into the architecture (cryptographic agility)
3. **Implement** hybrid Noise + ML-KEM when libp2p support lands

**Sources**: [NIST PQC Standards](https://www.nist.gov/pqcrypto), [AWS ML-KEM Support](https://aws.amazon.com/blogs/security/ml-kem-post-quantum-tls-now-supported-in-aws-kms-acm-and-secrets-manager/)

### eBPF: Relay-server hardening at kernel speed

[eBPF](https://ebpf.io/) (extended Berkeley Packet Filter) allows running sandboxed programs in the Linux kernel without modifying kernel source. For Shurli's relay server:

- **XDP (eXpress Data Path)**: Process packets before they reach the network stack - millions of packets/sec DDoS mitigation
- **Rate limiting**: Per-IP connection throttling at kernel level (faster than iptables)
- **Runtime monitoring**: Detect exploitation attempts on the relay via syscall tracing (Falco, Tetragon)
- **Profiling**: Trace packet processing bottlenecks without instrumentation overhead

This complements the userspace hardening (Resource Manager, per-peer limits) with kernel-level defense. Requires Linux kernel >= 5.8.

### Zero-RTT proxy connection resume

**The problem**: When a laptop switches from WiFi to cellular (or WiFi flickers), all TCP connections through the proxy drop. The user must wait for reconnection (5-15 seconds with Circuit Relay v2).

**The solution**: QUIC 0-RTT session resumption. The client caches a session ticket from the previous connection. On reconnect, it sends encrypted data in the very first packet - before the server even processes the handshake.

**Who has this**: Cloudflare's MASQUE relays, QUIC-native applications.
**Who doesn't**: WireGuard (stateless, reconnects fast but not 0-RTT), all current P2P tunnel tools.

This is a future optimization for Shurli's QUIC transport - particularly valuable for mobile clients (Phase 9).

---

## Why does Shurli use Go instead of Rust?

### The trade-off

| Factor | **Go** | **Rust** |
|--------|--------|----------|
| Development speed | Fast - the reason Shurli exists today | 2-3x slower initial development |
| GC pauses at scale | 10s pauses observed at 600K connections | None - no garbage collector |
| Memory per connection | ~28KB (GC overhead, interface boxing) | ~4-8KB (zero-cost abstractions) |
| libp2p ecosystem | Mature (go-libp2p, most examples) | Growing (rust-libp2p, Iroh) |
| Formal verification | Limited | Strong (s2n-quic has 300+ Kani harnesses) |
| Binary size | ~25-28MB | ~5-10MB |
| Cross-compilation | Trivial (`GOOS=linux GOARCH=arm64`) | Requires target toolchain setup |
| Concurrency model | Goroutines (simple, GC-managed) | async/await (no runtime overhead) |

### Why Go is right for now

Go's simplicity enabled rapid iteration across 5 major development phases (14+ batches). The libp2p Go ecosystem is the most mature, with the most examples and documentation. For a project with 1-100 concurrent connections (typical home use), Go's performance is more than adequate.

### When Rust becomes worth it

At scale - when a relay server handles thousands of concurrent circuits, or when the proxy loop becomes CPU-bound. The hot paths (packet forwarding in the relay, bidirectional proxy loop, SOCKS5 gateway) are candidates for selective Rust rewrite via FFI, not a full project rewrite.

### Rust libraries to watch

| Library | What it does | Why it matters |
|---------|-------------|----------------|
| **[Iroh](https://github.com/n0-computer/iroh)** | Rust P2P library, QUIC-native | ~90% NAT traversal success, QUIC multipath, approaching 1.0 |
| **[Quinn](https://github.com/quinn-rs/quinn)** | Pure Rust QUIC implementation | Used by Iroh, high performance, no C FFI |
| **[s2n-quic](https://github.com/aws/s2n-quic)** | AWS's Rust QUIC | Formal verification with Kani, production-tested in AWS |
| **[tokio](https://github.com/tokio-rs/tokio)** | Async runtime | LTS until Sept 2026, powers hyper (HTTP/2 + HTTP/3) |

### The hybrid strategy

Shurli's planned approach:
1. **Now through Phase 7**: Ship in Go. Fix goroutine lifecycle, tune GC, add observability.
2. **Phase 8+**: Profile hot paths under load. Selectively rewrite proxy loop / relay forwarding in Rust via FFI if performance demands it.
3. **Long-term**: Re-evaluate full Rust migration only if market demands 100x throughput and there's engineering capacity for it.

**Sources**: [Rust vs Go (Bitfield)](https://bitfieldconsulting.com/posts/rust-vs-go), [Go GC Guide](https://tip.golang.org/doc/gc-guide), [Iroh roadmap](https://www.iroh.computer/roadmap)

---

## How does reachability grade computation work in detail?

The reachability grade combines two data sources: interface discovery and STUN probe results.

**Interface discovery** scans all network interfaces and classifies each address:
- Global unicast IPv6 -> public
- Public IPv4 (not RFC 1918 / RFC 6598) -> public
- RFC 6598 (`100.64.0.0/10`) -> CGNAT flag set
- `network.force_cgnat: true` in config -> CGNAT flag set (for RFC 1918 carriers)
- Everything else -> private/local

**STUN probing** uses Google's public STUN servers to determine NAT behavior. It reports the external IP, port allocation strategy, and filtering behavior.

**Grade computation logic**:

```
if no connectivity:           Grade F
if CGNAT detected:            Grade D (cap, overrides STUN)
if public IPv6:               Grade A
if public IPv4:               Grade B
if full-cone or addr-restricted: Grade B
if port-restricted:           Grade C
if symmetric:                 Grade D
```

The CGNAT cap at grade D is the critical design choice. STUN probes the inner NAT and can report "hole-punchable" when the outer CGNAT will drop the punched packets. The grade overrides this false optimism.

Grades update automatically on network change events (WiFi switch, cable plug/unplug, VPN up/down). The grade is exposed via `shurli daemon status` and the REST API.

---

## What is sovereign peer interaction history?

Each daemon maintains a local `peer_history.json` file tracking interaction data with every known peer. This data never leaves the machine - it's the foundation for future trust algorithms.

**What's tracked per peer**:

| Field | Purpose |
|-------|---------|
| `first_seen` | When this peer was first encountered |
| `last_seen` | Most recent connection |
| `connection_count` | Total successful connections |
| `avg_latency_ms` | Running average (Welford's online algorithm) |
| `path_types` | Map of `"direct": N, "relay": M` |
| `introduced_by` | Which relay or peer introduced this one |
| `intro_method` | `"relay-pairing"`, `"invite"`, or `"manual"` |

**Implementation details**:
- Thread-safe with `sync.RWMutex`
- Atomic file writes (temp file + rename) for crash safety
- Best-effort load on startup (missing file is not an error)
- Storage bounded by peer count (per-peer aggregates, not per-connection logs)

**Why collect now**: Future trust algorithms (EigenTrust, reputation scoring) need interaction data as input. Starting collection now means months of history will be ready when those algorithms ship. Waiting until algorithm implementation to start collecting means zero history to bootstrap from.

**Sovereignty**: Each peer controls its own history. No central reputation server. No gossip-based sharing. The data stays local until explicit trust algorithms decide how (and whether) to use it.

---

## How does automatic WiFi transition work?

When you switch WiFi networks (or plug/unplug Ethernet), Shurli automatically adapts the connection path. No daemon restart, no manual intervention.

### The sequence (under 500ms to start, 5-15s to complete)

1. **Network change detection** (~500ms): The NetworkMonitor polls interfaces and diffs against the previous snapshot. When IPs appear or disappear, it fires callbacks.

2. **Stale connection cleanup** (immediate): `CloseStaleConnections()` matches each connection's local IP against the removed IPs from the network change. Connections on the disappeared interface are closed instantly instead of waiting for TCP keepalive timeout (which can take minutes).

3. **Backoff reset + immediate reconnect** (immediate): `OnNetworkChange()` zeroes all backoff timers and sends on the `reconnectNow` channel, which wakes the reconnect loop immediately instead of waiting for the next 30-second tick.

4. **mDNS re-browse** (5-10s): `BrowseNow()` triggers immediate LAN discovery. If the new network has a peer on the same LAN, mDNS connects directly using private IPv4.

5. **IPv6 path probing** (3-10s, background): `ProbeAndUpgradeRelayed()` checks if any relayed peer is reachable via direct IPv6 through a secondary interface (e.g., USB LAN with public IPv6).

### Connection priority table

Shurli enforces a strict priority order:

```
LAN (mDNS, private IPv4)  >  Direct IPv6 (path probing)  >  Relay (fallback)
```

- Same LAN as peer: DIRECT via mDNS at ~23ms
- Different network with public IPv6: DIRECT via IPv6 probing
- No IPv6, behind CGNAT: RELAYED via VPS relay at ~180ms

The priority is enforced automatically. If you switch from a relay network to a LAN with the peer, mDNS discovers the peer and establishes direct. If PeerManager simultaneously establishes a relay connection, it detects the existing direct connection and discards the relay.

### What happens to active connections?

Active streams (ping, proxy, file transfer) will break during the transition. The underlying TCP/QUIC connection is gone when the interface disappears. After reconnection (5-15 seconds), new streams work normally.

Future optimization: QUIC 0-RTT session resumption could make this seamless by resuming encrypted sessions across network changes.

---

## How does mDNS LAN discovery filter addresses?

mDNS discovers all of a peer's addresses (typically 14: private IPv4, public IPv6, ULA, loopback, across TCP and QUIC). But "discovered via mDNS" means "same LAN," so most of those addresses are wrong for LAN communication.

### The problem with using all addresses

`host.Connect()` uses every address in the peerstore, not just what you pass in `pi.Addrs`. Adding all 14 addresses before connecting causes the swarm to try unreachable ones:
- Public IPv6 on satellite networks (client isolation blocks inter-client IPv6)
- ULA addresses (fd00::/8, also blocked by client isolation)
- Loopback (127.0.0.1, obviously wrong)

Each unreachable address burns a 5-second timeout. The LAN connection that should take milliseconds takes over a minute.

### The filter: private IPv4 on matching subnets

`filterLANAddrs()` keeps only multiaddrs whose first component is IPv4 and whose IP falls within a local interface's CIDR subnet. For example, if your Mac is on `10.1.226.144/16`, only peer addresses in `10.1.0.0/16` pass the filter.

Result: 14 addresses become 2 (one TCP, one QUIC on the LAN IPv4). Connect completes in milliseconds.

The full address set is added to the peerstore AFTER the connect succeeds, so other subsystems (identify exchange, path tracker) still have the complete picture.

---

**Last Updated**: 2026-02-27
