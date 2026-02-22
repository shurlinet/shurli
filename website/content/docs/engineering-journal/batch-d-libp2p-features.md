---
title: "Batch D - libp2p Features"
weight: 6
description: "AutoNAT v2, QUIC transport ordering, Identify UserAgent, smart dialing."
---
<!-- Auto-synced from docs/engineering-journal/batch-d-libp2p-features.md by sync-docs.sh - do not edit directly -->


AutoNAT v2, QUIC transport ordering, Identify UserAgent, and smart dialing.

---

### ADR-D01: AutoNAT v2

**Context**: Peers need to know if they're behind NAT to decide whether to use relay. libp2p's AutoNAT v1 had accuracy issues.

**Alternatives considered**:
- **Manual reachability flag only** (`force_private_reachability: true`) - Works but requires users to know their NAT situation.
- **AutoNAT v1** - Older protocol, less accurate with CGNAT.

**Decision**: Enable AutoNAT v2 via `libp2p.EnableAutoNATv2()` alongside the manual flag. AutoNAT v2 uses a more reliable probing mechanism to determine reachability.

**Consequences**: Slightly more network chatter (AutoNAT probes), but more accurate reachability detection. The manual `force_private_reachability` flag remains as an override for cases where AutoNAT can't determine the correct state.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:118`

---

### ADR-D02: QUIC Preferred Transport Ordering

**Context**: libp2p supports multiple transports. The order they're specified affects which is tried first during connection establishment.

**Alternatives considered**:
- **TCP first** - Most compatible, works through all middleboxes. But slower connection establishment (4 RTTs for TCP+TLS+mux vs 3 for QUIC).
- **WebSocket first** - Anti-censorship benefit. But highest overhead.

**Decision**: Transport order is QUIC first, TCP second, WebSocket third. QUIC has native multiplexing (no yamux needed), faster handshake (1-RTT after initial), and better hole-punching characteristics. TCP is the universal fallback. WebSocket is for DPI/censorship evasion.

**Consequences**: Environments that block UDP (some corporate networks) will fall back to TCP automatically. The ordering is declarative in `New()` - first transport to succeed wins.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:113-117`

---

### ADR-D03: Identify UserAgent

**Context**: When multiple peers are connected, it's hard to tell which are peerup peers vs DHT neighbors, relay servers, or random libp2p nodes.

**Alternatives considered**:
- **Custom protocol handshake** - Send version info in a custom protocol. Rejected because libp2p's Identify protocol already does this.

**Decision**: Set `libp2p.UserAgent("peerup/" + version)` on every host. The daemon's peer list filters by UserAgent prefix (`peerup/` or `relay-server/`) by default, showing only network members. `--all` flag shows everything.

**Consequences**: Version info is visible to any connected peer (including non-peerup peers). Accepted because version strings are not sensitive - they aid debugging and interoperability.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:121-123`, `https://github.com/satindergrewal/peer-up/blob/main/internal/daemon/handlers.go:78-80`

---

### ADR-D04: Smart Dialing

**Context**: libp2p tries all known addresses for a peer simultaneously. With relay addresses in the peerstore, it might waste time on direct addresses that will fail for CGNAT peers.

**Alternatives considered**:
- **Relay-only dialing** - Only use relay. Rejected because direct connections should be preferred when available.

**Decision**: Let libp2p's default smart dialing handle address selection, but ensure relay circuit addresses are always in the peerstore via `AddRelayAddressesForPeer()`. This gives the dialer both direct and relay options, and it picks the fastest.

**Consequences**: Relies on libp2p's dialing heuristics, which generally prefer direct connections. Batch I added explicit address ranking via `PathDialer` (direct IPv6 > direct IPv4 > STUN-punched > peer relay > VPS relay) and parallel dial racing.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:260-270`
