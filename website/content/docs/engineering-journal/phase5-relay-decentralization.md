---
title: "Phase 5 - Relay Decentralization"
weight: 16
description: "Peer relay service, DHT relay discovery, health-aware EWMA selection, bandwidth tracking, layered bootstrap (config > DNS seeds > hardcoded > relay)."
---
<!-- Auto-synced from docs/engineering-journal/phase5-relay-decentralization.md by sync-docs - do not edit directly -->


Peer relay service, DHT-based relay discovery, health-aware selection, per-peer bandwidth tracking, and layered bootstrap (config > DNS seeds > hardcoded > relay). Deferred from Phase 5 core and built after PeerManager and observability were in place.

---

### ADR-RD01: Every-Peer-Is-A-Relay (PeerRelay)

**Context**: The VPS relay is a single point of failure. If it goes down, NATted peers cannot reach each other. Any publicly-reachable Shurli peer should be able to serve as a relay for its authorized peers, reducing dependence on central infrastructure.

**Alternatives considered**:
- **Require users to self-host VPS relays** - High barrier. Most users cannot or will not operate a VPS.
- **Multiple VPS relays** - Better availability but still central infrastructure. Operator cost scales linearly.
- **Auto-enable relay on peers with public IP** - Zero operator overhead. Every peer with a routable address contributes relay capacity. The existing ConnectionGater enforces auth before relay protocol runs.

**Decision**: `PeerRelay` in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/peerrelay.go`. Three modes via `peer_relay.enabled` config: `"auto"` (default), `"true"`, `"false"`. Auto mode calls `AutoDetect()` after interface discovery: if `InterfaceSummary.HasGlobalIPv4` or `HasGlobalIPv6`, the relay enables. Resource limits are configurable: `MaxReservations` (4), `MaxCircuits` (16), `CircuitDuration` (10 min), `CircuitDataLimit` (128KB). `OnStateChange` callback bridges to RelayDiscovery for DHT advertisement.

Security: ConnectionGater applies to relay protocol. Only peers in `authorized_keys` can make reservations or create circuits. No anonymous relay traffic.

**Consequences**: Every peer with a public IP becomes a relay automatically. The VPS relay transitions from "required infrastructure" to "bootstrap convenience." Resource limits prevent abuse. Config knobs allow operators to tune or disable.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/peerrelay.go`, `https://github.com/shurlinet/shurli/blob/main/internal/config/config.go` (PeerRelayConfig)

---

### ADR-RD02: DHT-Based Relay Discovery

**Context**: With PeerRelay, multiple peers can serve as relays. But NATted nodes need to find them. Static relay lists in config don't scale and go stale when peer IPs change.

**Alternatives considered**:
- **Gossip relay advertisements** - Requires PubSub (incompatible, go-libp2p-pubsub pins v0.39.1). Good long-term but premature.
- **Central registry API** - Defeats the purpose of decentralization.
- **DHT provider records** - Standard libp2p pattern. Relay peers call `dht.Provide()`, NATted nodes call `FindProvidersAsync()`. Namespace-aware via deterministic CID derivation. Zero new dependencies.

**Decision**: `RelayDiscovery` in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/relaydiscovery.go`. Three components:

1. **RelaySource interface** - `RelayAddrs() []string`. Abstracts static vs dynamic relay sources. `StaticRelaySource` wraps a fixed list for backward compatibility. `RelayDiscovery` implements `RelaySource` with combined static + DHT results.

2. **Namespace-aware CID** - `RelayServiceCID(namespace)` generates a deterministic CID from `sha256("/shurli/<namespace>/relay/v1")`. Different private networks get different CIDs. Peers only discover relays in their own namespace.

3. **Advertise/Discover loops** - `Advertise()` calls `dht.Provide()` every 10 minutes (triggered by PeerRelay's `OnStateChange` callback). `StartDiscoveryLoop()` calls `FindProvidersAsync()` every 5 minutes. Discovered relays are registered with RelayHealth for scoring.

**AutoRelay integration**: `PeerSource()` returns a channel compatible with libp2p's `autorelay.PeerSource`. Returns all known relays (static first, then DHT-discovered). Safe to call before DHT is set (returns static relays only).

**Consequences**: NATted nodes automatically discover relay peers through the DHT. No static config needed beyond bootstrap. Namespace isolation prevents cross-network relay discovery. AllRelays() deduplicates by peer ID (static relays take priority over DHT-discovered duplicates).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/relaydiscovery.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/relaydiscovery_test.go`

---

### ADR-RD03: Health-Aware Relay Selection (EWMA Scoring)

**Context**: With multiple relays (VPS + peer relays), the node needs to prefer healthy, low-latency relays over degraded or distant ones. A relay that responded 5 minutes ago at 30ms should rank higher than one last seen 20 minutes ago at 500ms.

**Alternatives considered**:
- **Round-robin** - No quality signal. A dead relay gets the same traffic as a healthy one.
- **Latency-only ranking** - Misses reliability. A relay with 20ms latency that fails 50% of the time is worse than one at 100ms with 99% success.
- **Composite EWMA score** - Weights success rate (reliability), RTT (latency), and freshness (recency). EWMA smooths outliers while tracking trends.

**Decision**: `RelayHealth` in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/relayhealth.go`. Per-relay `RelayHealthScore` tracks:

- **Score**: composite 0.0-1.0 from `computeScore(successRate, rttMs, now, lastProbe)`
- **Formula**: `successRate * 0.6 + latencyFactor * 0.3 + freshness * 0.1`
- **Latency factor**: `1.0 - min(rttMs/2000, 1.0)` (linear decay, 0ms = 1.0, 2000ms = 0.0)
- **Freshness**: `exp(-age/30)` (exponential decay, half-life ~20 minutes)
- **EWMA alpha**: 0.3 (recent observations weighted 30%)

Background probing: `Start()` runs `ProbeAll()` every 60 seconds. Each probe connects to the relay and measures RTT. Probes run concurrently with 10s timeout per relay.

Integration: `RelayDiscovery.SetHealth()` wires the health tracker. When set, `RelayAddrs()` sorts relays by health score (best first). PathDialer gets health-ranked relay addresses automatically.

Prometheus: `shurli_relay_health_score` gauge (per relay, labeled static/dynamic) and `shurli_relay_probe_total` counter (success/failure).

**Consequences**: Relays are ranked by measured quality. Degraded relays drop in ranking automatically. New relays start at 0.5 (neutral) and converge after 3-4 probes. Stale relays decay via freshness factor. Zero manual intervention.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/relayhealth.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`

---

### ADR-RD04: Per-Peer Bandwidth Tracking

**Context**: Observability gap: no visibility into how much data flows through each peer or protocol. Without bandwidth data, operators cannot identify heavy consumers, detect anomalies, or tune resource limits.

**Decision**: `BandwidthTracker` in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/bandwidth.go`. Wraps libp2p's `metrics.BandwidthCounter` (wired via `libp2p.BandwidthReporter()`). Exposes:

- `PeerStats(peer.ID)` - per-peer bytes in/out, rates
- `AllPeerStats()` - all peers keyed by ID
- `ProtocolStats(protocol.ID)` - per-protocol bytes
- `Totals()` - aggregate across everything

`PublishMetrics()` bridges to Prometheus:
- `shurli_bandwidth_bytes_total{direction}` - aggregate in/out
- `shurli_peer_bandwidth_bytes_total{peer,direction}` - per-peer
- `shurli_peer_bandwidth_rate{peer,direction}` - per-peer rate
- `shurli_protocol_bandwidth_bytes_total{protocol,direction}` - per-protocol

`Start()` runs a background loop (30s interval) that publishes metrics and trims peers idle > 1 hour (`TrimIdle`) to bound memory.

Daemon API: `GET /v1/bandwidth` returns aggregate + per-peer + per-protocol stats.

**Consequences**: Full bandwidth visibility. Prometheus dashboards show traffic patterns. Idle trimming prevents unbounded memory growth. Daemon API exposes stats for CLI tooling.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/bandwidth.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/bandwidth_test.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/handlers.go`

---

### ADR-RD05: Layered Bootstrap Decentralization

**Context**: Shurli hardcoded relay addresses in config files. If the VPS changes IP or goes down, every node needs a config update. Bitcoin solved this decades ago with layered bootstrap: try local config, then DNS seeds, then hardcoded seeds, then peer exchange. Each layer is more stale but more available.

**Decision**: Four-layer bootstrap with graceful degradation:

1. **Config peers** (`bootstrap_peers` in config.yaml) - highest priority. User-specified, always tried first. If set, DNS seeds are skipped (user knows what they want).

2. **DNS seeds** (`_dnsaddr.<domain>` TXT records) - default: `seeds.shurli.io`. Uses the dnsaddr multiaddr convention from IPFS. `ResolveDNSSeeds()` in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/dnsseed.go` queries TXT records with 10s timeout. Records format: `dnsaddr=/ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...`. Merges addresses for same peer ID. DNS failures are logged but not fatal.

3. **Hardcoded seeds** (`https://github.com/shurlinet/shurli/blob/main/cmd/shurli/seeds.go`) - compiled into the binary. Ultimate fallback when DNS is unavailable (censorship, network partition, misconfigured DNS). Populated after seed node VPS setup with actual multiaddrs.

4. **Relay addresses** - from RelayDiscovery (static config + DHT-discovered). Lowest priority but always available after initial bootstrap.

Config overrides: `dns_seed_domain` changes the DNS lookup domain. Users running private networks set their own domain. Setting `bootstrap_peers` explicitly bypasses DNS entirely.

**Consequences**: New users need zero config - DNS seeds resolve automatically. DNS update is a Cloudflare TXT record change (seconds to propagate). Hardcoded seeds survive DNS failure. Private networks override with their own domain. Four layers, four levels of staleness, four levels of availability. Same pattern that keeps Bitcoin running since 2009.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/dnsseed.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/dnsseed_test.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/seeds.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_seed_helpers.go`
