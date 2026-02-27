# Phase 5: Network Resilience

mDNS native discovery, PeerManager lifecycle management, IPv6 path probing, stale connection cleanup, and automatic WiFi transition. Tested on 5 physical networks.

---

### ADR-L01: Native mDNS via dns_sd.h (Replace Pure-Go Multicast)

**Context**: The pure-Go zeroconf Browse implementation competes with the OS mDNS daemon for the multicast socket on port 5353. On macOS, mDNSResponder owns that port; on Linux, avahi does. Binding a raw socket alongside the system daemon causes silent failures: discoveries stop arriving after minutes of operation.

**Alternatives considered**:
- **Pure-Go zeroconf Browse** - Cross-platform but fights the OS daemon. Worked intermittently, failed under sustained use.
- **Shell out to dns-sd / avahi-browse** - Avoids socket conflict but adds process management overhead and output parsing.
- **CGo binding to dns_sd.h** - Uses the OS daemon via IPC. Cooperates instead of competing. Requires CGo build.

**Decision**: CGo binding to `dns_sd.h` with zeroconf fallback. `mdns_browse_native.go` (build tag `cgo && (darwin || linux)`) calls `DNSServiceBrowse` and `DNSServiceQueryRecord` via the platform's DNS-SD API. `mdns_browse_fallback.go` (build tag `!cgo || !(darwin || linux)`) uses zeroconf Browse. Registration stays on zeroconf for both paths (zeroconf's `RegisterProxy` works reliably for advertising).

**Consequences**: Native browse cooperates with mDNSResponder/avahi via IPC instead of competing for port 5353. Requires `libavahi-compat-libdnssd-dev` on Linux (CI updated). Cross-compilation without CGo falls back to zeroconf. Platform-specific code isolated behind build tags.

**Reference**: `pkg/p2pnet/mdns_browse_native.go`, `pkg/p2pnet/mdns_browse_fallback.go`, `pkg/p2pnet/mdns.go`

---

### ADR-L02: PeerManager Reconnect Loop with Exponential Backoff

**Context**: Before PeerManager, reconnection was ad-hoc. If a peer disconnected (WiFi switch, network outage), nothing automatically reconnected. The user had to restart the daemon.

**Alternatives considered**:
- **Application-level keepalive with manual reconnect** - Simple but reactive. No backoff, floods the network on outages.
- **libp2p AutoRelay only** - Handles relay connections but not direct path management. No visibility into peer lifecycle.

**Decision**: `PeerManager` with three goroutines: `eventLoop` (libp2p event bus subscriber), `reconnectLoop` (30s ticker + immediate trigger), and `probeLoop` (2-minute IPv6 probe cycle). Exponential backoff: 30s base, doubles per failure, capped at 15 minutes. Watchlist populated from `authorized_keys` via the gater. Max 3 concurrent dials. `PathDialer` races DHT vs relay for each attempt.

Key design choices:
- **Watchlist, not all peers**: only authorized peers get reconnected. Relay, bootstrap, and DHT peers are transient.
- **Event-driven state**: `EvtPeerConnectednessChanged` updates `ManagedPeer.Connected` immediately, no polling.
- **Callback bridge**: `ConnectionRecorder` callback bridges `pkg/p2pnet` to `internal/reputation` without import cycles.

**Consequences**: Automatic reconnection to all authorized peers. Backoff prevents network flooding. Reconnect time: 3-10 seconds for relay, 5-15 seconds for direct via probing.

**Reference**: `pkg/p2pnet/peermanager.go` (constants, `PeerManager`, `ManagedPeer`, `reconnectLoop`, `attemptReconnect`)

---

### ADR-L03: Stale Connection Cleanup on Interface Removal

**Context**: When WiFi switches (e.g., switching between cellular hotspots), the old network interface disappears. Connections bound to the old interface's IP are dead, but libp2p doesn't detect this for minutes (TCP keepalive timeout). During this window, `host.Connect()` returns early ("already connected"), the reconnect loop skips the peer, and the user sees no connectivity.

**Alternatives considered**:
- **Reduce TCP keepalive timeout** - Faster detection but still minutes, not seconds. Also affects healthy connections.
- **Close ALL connections on network change** - Too aggressive. Disrupts connections on interfaces that didn't change.
- **Match connection local IPs against removed IPs** - Surgical. Only closes connections on the disappeared interface.

**Decision**: `CloseStaleConnections(removedIPs []string)` iterates all connections to watched peers, extracts the local IP via `extractIPFromMultiaddrObj()`, and closes any whose local IP matches a removed address. Called from the network change handler BEFORE `OnNetworkChange()` resets backoffs.

**Consequences**: Dead connections are removed within 500ms of interface disappearance (network change debounce time). The subsequent `OnNetworkChange()` triggers immediate reconnect through the new active interface. Tested: WiFi switch from DIRECT to RELAYED completes in ~5 seconds.

**Reference**: `pkg/p2pnet/peermanager.go` (`CloseStaleConnections`, `extractIPFromMultiaddrObj`), `cmd/shurli/serve_common.go` (wiring)

---

### ADR-L04: Immediate Reconnect Trigger (reconnectNow Channel)

**Context**: `OnNetworkChange()` reset backoff timers, but the reconnect loop runs on a 30-second ticker. After a WiFi switch, the user waits up to 30 seconds for reconnection even though backoffs are cleared.

**Decision**: Added `reconnectNow chan struct{}` (buffered capacity 1). `OnNetworkChange()` sends on this channel after resetting backoffs. `reconnectLoop()` selects on both the ticker and `reconnectNow`. Non-blocking send: if a trigger is already pending, the second one is dropped.

**Consequences**: Reconnection starts within milliseconds of network change detection instead of waiting for the next 30s tick. Combined with stale connection cleanup, the full WiFi-switch-to-reconnect cycle completes in 5-15 seconds.

**Reference**: `pkg/p2pnet/peermanager.go` (`reconnectNow` field, `OnNetworkChange`, `reconnectLoop`)

---

### ADR-L05: IPv6 Path Probing with Source-Bound TCP

**Context**: When a machine has USB LAN (public IPv6) plugged in alongside WiFi (CGNAT, no IPv6), a direct IPv6 path to the home-node exists through USB LAN. But libp2p doesn't try it: the peer is already connected via relay, and `host.Connect()` returns early.

Additionally, macOS utun interfaces (iCloud Private Relay, disconnected VPNs) can claim the default IPv6 route but don't forward public traffic. An unbound dial picks the utun and fails silently.

**Alternatives considered**:
- **Rely on libp2p's address sorting** - libp2p prefers QUIC over TCP and IPv6 over IPv4, but won't close an existing relay to try direct.
- **Unbound TCP probe** - Works when utun isn't present. Fails when utun claims the default route.
- **Source-bound TCP probe per local IPv6** - Explicitly binds to each global IPv6 address. Forces the kernel to route through the correct interface regardless of utun.

**Decision**: `ProbeAndUpgradeRelayed()` runs after every network change and on a 2-minute timer. For each relayed peer with IPv6 in the peerstore:
1. Collect all local global IPv6 addresses from `DiscoverInterfaces()`
2. For each peer IPv6 target, for each local IPv6 source: `net.Dialer{LocalAddr: &net.TCPAddr{IP: localIP}}` with 3s timeout
3. If probe succeeds: `ForceDirectDial` to establish direct alongside relay, then sweep relay connections for 90 seconds

The 90-second relay sweep (`closeRelayConns`) handles the remote peer's reconnect loop re-establishing relay. It checks every 10 seconds and stops early if the direct connection is lost.

DHT `FindPeer` refreshes addresses for peers missing IPv6 in the peerstore.

**Consequences**: Cross-ISP DIRECT connections via secondary interfaces. Tested: USB LAN (IPv6) to satellite home-node, RELAYED 180ms to DIRECT 23ms via IPv6 QUIC. Source binding bypasses macOS utun route hijacking.

**Reference**: `pkg/p2pnet/peermanager.go` (`ProbeAndUpgradeRelayed`, `probeAndUpgrade`, `probeLoop`, `closeRelayConns`)

---

### ADR-L06: mDNS LAN-First Address Filtering

**Context**: When mDNS discovers a peer on the LAN, it receives all 14 of the peer's multiaddrs (private IPv4, public IPv6, ULA, loopback). Adding all 14 to the peerstore before `host.Connect()` causes the swarm to try every address, including unreachable ones. On satellite WiFi (and similar consumer routers with client isolation), inter-client IPv6/ULA is blocked, so 12 of 14 addresses timeout (5s each). The LAN connection takes over a minute instead of seconds.

Worse, `host.Connect()` uses ALL peerstore addresses, not just the `pi.Addrs` field. Adding addresses to the peerstore "pollutes" the dial attempt.

**Alternatives considered**:
- **Add all addresses, let libp2p sort** - libp2p's smart dialing helps but still tries unreachable IPv6/ULA, burning connection timeout budget.
- **Add only the first IPv4** - Too restrictive. Misses valid TCP and QUIC addresses on the same IPv4.
- **Filter to private IPv4 on matching subnets** - mDNS = same LAN. Private IPv4 is the universal LAN signal. Subnet matching prevents cross-LAN false positives.

**Decision**: `filterLANAddrs()` returns only multiaddrs whose first component is IPv4 and whose IP falls within a `localIPv4Subnets()` CIDR. Only these LAN addrs are added to the peerstore before `host.Connect()`. The full address set is added AFTER connect succeeds.

Why IPv4 only: many consumer routers (satellite ISPs, etc.) give all WiFi clients the same IPv6 prefix but block inter-client IPv6 traffic (client isolation). ULA (fd00::/8) has the same problem. Private IPv4 (10.x, 192.168.x) is the one reliable LAN signal across all consumer routers.

**Consequences**: mDNS connect drops from 14 addresses (60+ second timeout budget) to 2 addresses (~2 second budget). LAN connection at 23ms in testing. Full address set still available after connect for identify exchange.

**Reference**: `pkg/p2pnet/mdns.go` (`filterLANAddrs`, `localIPv4Subnets`, `HandlePeerFound`)

---

### ADR-L07: Relay-Discard in PeerManager (mDNS Priority)

**Context**: mDNS connects to a LAN peer directly using `ForceDirectDial`. But PeerManager's reconnect loop was already mid-dial when mDNS triggered. PeerManager establishes relay, mDNS sweeps relay, PeerManager re-establishes relay on next tick. Cat-and-mouse: direct and relay fight each other.

**Alternatives considered**:
- **Global lock between mDNS and PeerManager** - Prevents concurrent access but adds complexity and deadlock risk.
- **Priority flag in PeerManager** - Skip reconnect if mDNS is active. Requires cross-component coordination.
- **Check-on-completion** - After PeerManager dials relay, check if direct already exists. Discard relay if so.

**Decision**: Two changes:
1. **PeerManager relay-discard**: in `attemptReconnect`, after `DialPeer` returns a RELAYED result, check `allConnsRelayed()`. If a non-limited (direct) connection exists, close the relay connections and return without recording the relay as a successful reconnect.
2. **PeerManager skip-on-connected**: if `DialPeer` returns an error but `mp.Connected` is already true (set by mDNS via the event bus), don't count it as a failure.

Combined with mDNS's 30-second relay sweep and `ForceDirectDial`, this eliminates the race. The priority table is enforced: LAN direct always wins over relay.

**Consequences**: On a LAN with both mDNS direct and relay available, the pattern is: PeerManager tries relay, gets it, sees direct exists, discards relay. Logs show "peermanager: discarded relay (direct already active)" every ~30 seconds. Correct behavior: direct is stable, relay attempts are harmless.

**Reference**: `pkg/p2pnet/peermanager.go` (`attemptReconnect` relay-discard block), `pkg/p2pnet/mdns.go` (relay sweep goroutine)

---

### ADR-L08: Network Change Orchestration Sequence

**Context**: A network change (WiFi switch) triggers multiple subsystems: stale connection cleanup, PeerManager backoff reset, mDNS re-browse, IPv6 probe, STUN re-probe, NetIntel re-announce. The order matters: stale connections must close before reconnect triggers, and mDNS must re-browse before probes fire.

**Decision**: Fixed sequence in the network change callback in `serve_common.go`:
1. `CloseStaleConnections(change.Removed)` - kill dead connections
2. `OnNetworkChange()` - reset backoffs + trigger immediate reconnect
3. `BrowseNow()` - mDNS re-browse for LAN peers
4. `ProbeAndUpgradeRelayed()` (goroutine) - IPv6 probing in background
5. `AnnounceNow()` - NetIntel state update
6. STUN re-probe (goroutine) - external address detection

Steps 1-3 are synchronous (fast, <1ms each). Steps 4-6 are async (seconds to complete).

**Consequences**: Full WiFi switch recovery in 5-15 seconds. No dependency issues between subsystems. Each subsystem handles its own error cases independently.

**Reference**: `cmd/shurli/serve_common.go` (network change callback, lines 476-520)

---

## Hardware Test Results (2026-02-27)

The Phase 5 features were validated through 5+ hours of physical testing across 5 networks:

| Transition | Result | Time |
|------------|--------|------|
| Cellular (CGNAT) to 5G hotspot | RELAYED to DIRECT | ~5s |
| 5G hotspot to Cellular (CGNAT) | DIRECT to RELAYED | ~35s |
| Cellular (CGNAT) to Satellite WiFi (LAN) | RELAYED to DIRECT (mDNS) | ~10-15s |
| Satellite WiFi to Cellular (CGNAT) | DIRECT to RELAYED | ~5s |
| Cellular (CGNAT) to USB LAN (IPv6) | RELAYED to DIRECT (IPv6) | ~8s |
| USB LAN unplug to Cellular (CGNAT) | DIRECT to RELAYED | ~5s |
| Terrestrial WiFi to Satellite WiFi | DIRECT to DIRECT | ~5s |

All transitions automatic, no daemon restart needed. Connection priority table enforced: LAN (mDNS) > Direct IPv6 (path probing) > Relay (fallback).

---

### ADR-L09: Daemon-Mediated Subcommands (Eliminate Standalone P2P Hosts)

**Context**: Subcommands (`proxy`, `ping`, `traceroute`) created their own standalone libp2p hosts when connecting to peers. This caused a critical bug: when the remote daemon restarted, a standalone proxy reconnected via relay and stayed there permanently, even though the daemon had a direct path available. The standalone host has no PeerManager, no mDNS, no IPv6 probing, no path upgrades. It's blind to the network. Additionally, each standalone invocation burns 5-15 seconds bootstrapping a temporary DHT client and creates redundant connections to the same peer.

**Alternatives considered**:
- **Keep standalone as primary** - Every subcommand manages its own P2P stack. Simple but fundamentally broken: no path management, no connection reuse, blind to network changes.
- **Daemon required, no fallback** - All subcommands refuse to work without daemon. Clean but removes the ability to debug when the daemon itself is broken.
- **Daemon-first with gated fallback** - Try daemon API first. Standalone only if explicitly enabled via config (`cli.allow_standalone: true`) or CLI flag (`--standalone`).

**Decision**: Daemon-first with gated standalone fallback. All network subcommands now:
1. Try the daemon's REST API first (via Unix socket)
2. If daemon isn't running and `--standalone` flag or `cli.allow_standalone` config is set, fall back to standalone
3. Otherwise, error with "daemon not running" and clear instructions

The `proxy` command was the last holdout: it always created a standalone host. Now it uses `POST /v1/connect` to create TCP proxies through the daemon's managed connection. `ConnectResponse` was extended with `path_type` and `address` fields so the CLI can display connection info.

New config section:
```yaml
cli:
  allow_standalone: false  # default: daemon required
```

**Consequences**:
- Proxy traffic automatically benefits from PeerManager's path upgrades (relay to direct)
- No more stuck-on-relay proxy sessions after remote daemon restart
- Zero bootstrap latency for subcommands (daemon is already connected)
- One connection per peer (daemon's), not N from N subcommands
- Standalone code preserved behind config gate for debugging the daemon itself
- Continuous ping (count=0) still requires standalone because daemon HTTP API can't stream

**Reference**: `cmd/shurli/cmd_proxy.go` (daemon-first pattern), `internal/daemon/handlers.go` (path info in ConnectResponse), `internal/config/config.go` (CLIConfig), `pkg/p2pnet/pathdialer.go` (PeerConnInfo helper)

---

### ADR-L10: CLI Standalone Gating (Config + Flag Override)

**Context**: After ADR-L09, standalone mode is disabled by default. But developers and advanced users need an escape hatch for debugging connectivity without a running daemon.

**Decision**: Two override mechanisms:
1. **Config**: `cli.allow_standalone: true` in `config.yaml` - persistent setting for development environments
2. **CLI flag**: `--standalone` on `proxy`, `ping`, `traceroute` - one-off override without editing config

The `--standalone` flag also skips the daemon check entirely (useful for testing standalone behavior even when daemon is running).

**Consequences**: Default is "daemon required" for all users. Developers set the config flag. One-off debugging uses `--standalone`. No accidental standalone sessions that create blind P2P hosts.

**Reference**: `cmd/shurli/cmd_proxy.go`, `cmd/shurli/cmd_ping.go`, `cmd/shurli/cmd_traceroute.go` (standalone gates), `internal/config/config.go` (CLIConfig)

---

### ADR-L11: Explicit CGNAT Override (force_cgnat Config)

**Context**: `DetectCGNAT()` checks local interfaces for RFC 6598 addresses (100.64.0.0/10), the only reliable client-side signal for carrier-grade NAT. But some mobile carriers assign RFC 1918 addresses (e.g., 172.16-31.x.x) for their CGNAT. These addresses are indistinguishable from a regular home network. On those carriers, auto-detection fails silently: STUN classifies the NAT as port-restricted or address-restricted, reachability reports Grade B/C, and the node wastes time attempting hole-punches that will never succeed through the outer carrier NAT.

**Alternatives considered**:
- **Active probing** (traceroute to STUN, count hops) - Unreliable. Many carriers block ICMP/traceroute.
- **External service lookup** (query IP geolocation for ISP type) - Privacy violation. Requires external API call.
- **Heuristic** (symmetric NAT + private IP = CGNAT) - High false positive rate. Symmetric NAT exists on enterprise networks too.

**Decision**: `network.force_cgnat: true` config option. Same pattern as the existing `force_private_reachability` flag. When set, `DetectCGNAT()` immediately sets `BehindCGNAT = true` before checking interfaces. This caps reachability at Grade D and tells PeerManager not to waste cycles on hole-punch attempts.

Config-level (not CLI flag) because CGNAT is a property of the network the node sits on, not a per-command setting.

```yaml
network:
  force_cgnat: true
```

**Consequences**: Users on RFC 1918 mobile carriers can correctly signal their CGNAT status. Reachability grade accurately reflects Grade D. Hole-punch attempts are skipped. The node goes straight to relay, saving connection time. Auto-detection still works for RFC 6598 carriers when the flag is false (default).

**Reference**: `pkg/p2pnet/stunprober.go` (`DetectCGNAT`), `cmd/shurli/serve_common.go` (passes config flag), `internal/config/config.go` (`NetworkConfig.ForceCGNAT`)
