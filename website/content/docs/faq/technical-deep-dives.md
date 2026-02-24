---
title: "Technical Deep Dives"
weight: 5
description: "libp2p improvements, emerging technologies, and the Go vs Rust trade-off."
---
<!-- Auto-synced from docs/faq/technical-deep-dives.md by sync-docs - do not edit directly -->


## What libp2p improvements has peer-up adopted?

peer-up uses go-libp2p v0.47.0. Several improvements have shipped since then that would meaningfully improve performance, security, and reliability.

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

### What peer-up has done (Phase 4C - shipped)

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

These changes brought connection setup closer to 1-3 seconds via parallel dial racing, while keeping the self-sovereign architecture. Connection warmup and stream pooling remain as future optimizations.

---

## What emerging technologies could benefit peer-up?

### Protocols to watch

| Protocol | What it gives peer-up | Status (2026) | Phase |
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

peer-up could offer MASQUE as an alternative relay transport alongside Circuit Relay v2 - giving users the choice between libp2p-native P2P and HTTP/3-based relay for environments where traffic must look like standard HTTPS.

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

This is a future optimization for peer-up's QUIC transport - particularly valuable for mobile clients (Phase 9).

---

## Why does peer-up use Go instead of Rust?

### The trade-off

| Factor | **Go** | **Rust** |
|--------|--------|----------|
| Development speed | Fast - the reason peer-up exists today | 2-3x slower initial development |
| GC pauses at scale | 10s pauses observed at 600K connections | None - no garbage collector |
| Memory per connection | ~28KB (GC overhead, interface boxing) | ~4-8KB (zero-cost abstractions) |
| libp2p ecosystem | Mature (go-libp2p, most examples) | Growing (rust-libp2p, Iroh) |
| Formal verification | Limited | Strong (s2n-quic has 300+ Kani harnesses) |
| Binary size | ~25-28MB | ~5-10MB |
| Cross-compilation | Trivial (`GOOS=linux GOARCH=arm64`) | Requires target toolchain setup |
| Concurrency model | Goroutines (simple, GC-managed) | async/await (no runtime overhead) |

### Why Go is right for now

Go's simplicity enabled rapid iteration through 7 phases of development. The libp2p Go ecosystem is the most mature, with the most examples and documentation. For a project with 1-100 concurrent connections (typical home use), Go's performance is more than adequate.

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

peer-up's planned approach:
1. **Now through Phase 7**: Ship in Go. Fix goroutine lifecycle, tune GC, add observability.
2. **Phase 8+**: Profile hot paths under load. Selectively rewrite proxy loop / relay forwarding in Rust via FFI if performance demands it.
3. **Long-term**: Re-evaluate full Rust migration only if market demands 100x throughput and there's engineering capacity for it.

**Sources**: [Rust vs Go (Bitfield)](https://bitfieldconsulting.com/posts/rust-vs-go), [Go GC Guide](https://tip.golang.org/doc/gc-guide), [Iroh roadmap](https://www.iroh.computer/roadmap)

---

**Last Updated**: 2026-02-24
