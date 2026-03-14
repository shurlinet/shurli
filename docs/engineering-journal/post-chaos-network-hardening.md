# Post-Chaos Network Hardening

**Date**: 2026-03-11 to 2026-03-14
**Status**: Complete
**ADRs**: ADR-S01 to ADR-S07

Physical chaos testing (16 test cases, 5 ISPs, 3 VPN providers) exposed 11 root causes in libp2p's network transition handling and 8 post-chaos flags. All resolved. This journal covers the architectural decisions made during the fix and investigation phase.

---

### ADR-S01: Reset Black Hole Detectors on Network Change

**Date**: 2026-03-11
**Status**: Accepted

### Context

libp2p's `BlackHoleSuccessCounter` (N=100, MinSuccesses=5) tracks IPv6 and UDP dial success rates. After enough failures, it enters `Blocked` state and refuses further dials, allowing only 1 in 100 through as a probe. This state persists indefinitely until enough probes succeed.

After switching from a CGNAT network (IPv6 always fails) to a WiFi network with working IPv6, the detector remained in `Blocked` state. The daemon was stuck on relay despite having a working direct IPv6 path. Raw TCP probes confirmed reachability, but libp2p's swarm rejected the addresses before they reached the dialer.

### Decision

Create custom `BlackHoleSuccessCounter` instances via `libp2p.UDPBlackHoleSuccessCounter()` and `libp2p.IPv6BlackHoleSuccessCounter()`. Store references on the `Network` struct. Expose `ResetBlackHoles()` method. Call it from the network change callback in `serve_common.go` before any reconnection attempt.

### Why not let the detector self-recover?

Self-recovery requires 5 successful probes out of 100 attempts. At 1 probe per 100 dials, and with most dials happening through the reconnect loop at ~30s intervals, recovery could take hours. A network switch is a definitive event that invalidates all prior transport state.

### Consequences

- Black hole state resets within 500ms of any network change (debounce delay)
- False positives possible: a brief network flicker resets the detector, allowing a burst of UDP/IPv6 dials that will fail. The detector re-learns within 100 dials. Acceptable: the burst is short and bounded.

**Reference**: `pkg/p2pnet/network.go`, `cmd/shurli/serve_common.go`

---

### ADR-S02: ForceReachabilityPrivate for Permanent Relay Fallback

**Date**: 2026-03-11
**Status**: Accepted

### Context

libp2p's autorelay drops relay reservations when autonat classifies the host as "public" (has global IPv6, passes dial-back probes). When the host then switches to a CGNAT network, autonat needs several minutes of failed probes to reclassify as "private" and re-request reservations. During this window, there is no relay fallback.

### Decision

Set `libp2p.ForceReachabilityPrivate()` always in daemon mode. The daemon maintains relay reservations on every network, whether or not it has a public IP.

### Alternatives considered

- **Dynamic threshold**: Track how often we switch networks and only force private on mobile devices. Rejected: adds complexity for no benefit. The cost of one extra relay reservation is negligible.
- **Faster autonat re-probe**: Reduce autonat probe interval after network change. Rejected: autonat's probe timing is internal to libp2p, no clean override point.

### Consequences

- One relay reservation per relay server is maintained permanently, even on networks where it's not needed (e.g., same LAN as the peer)
- Relay reservations are always available as immediate fallback on any network switch
- Peer relay auto-enable/disable still works independently (based on public IP detection, not autonat state)

**Reference**: `cmd/shurli/serve_common.go:223`

---

### ADR-S03: Constrained Dial for Confirmed Path

**Date**: 2026-03-11
**Status**: Accepted

### Context

`probeAndUpgrade` confirms a specific TCP path works via raw `net.Dial`, then calls `DialPeer` with `ForceDirectDial`. But `DialPeer` tries ALL addresses from the peerstore, not just the confirmed one. With 16+ addresses (QUIC, TCP, IPv4, IPv6, ULA), the simultaneous dials cascade-fail: QUIC through VPN utun fails, ULA addresses fail, rate limits trigger on valid addresses.

### Decision

Before `DialPeer`, save all peerstore addresses, `ClearAddrs`, add ONLY the confirmed TCP multiaddr, dial, then restore all addresses regardless of outcome. The race window is small (10s dial timeout). Existing relay connections survive (independent of peerstore).

### Consequences

- `DialPeer` only sees the confirmed address, no cascade failures
- Brief peerstore manipulation window during which other subsystems see reduced addresses. Acceptable: the existing relay connection is unaffected, and the window is bounded by the 10s dial timeout.

**Reference**: `pkg/p2pnet/peermanager.go`

---

### ADR-S04: VPN Tunnel Interface Detection

**Date**: 2026-03-13
**Status**: Accepted

### Context

VPN activation adds a tunnel interface (`utun` on macOS, `tun`/`wg`/`ppp` on Linux) with a private IPv4 address (10.x, 100.64.x). The network monitor only diffed global IP addresses, so VPN connect was invisible. VPN also intercepts existing IPv6 routing, breaking relay connections established over IPv6, but the daemon didn't know anything changed.

### Decision

Track tunnel interface names in `InterfaceSummary.TunnelInterfaces`. In `diffSummaries`, compare the set of tunnel interface names between snapshots. If any appear or disappear, fire a network change event with `TunnelChanged=true` regardless of global IP changes.

Interface name patterns: `utun[0-9]+` (macOS: WireGuard, IKEv2, LightWay), `tun[0-9]+` (Linux: OpenVPN), `wg[0-9]+` (WireGuard), `ppp[0-9]+` (L2TP).

### Consequences

- VPN connect/disconnect fires network change events, triggering the full recovery chain
- Edge case: VPN setting changes without interface add/remove (e.g., toggling local network sharing) are not detected. Acceptable: the connection state doesn't change in this case.

**Reference**: `pkg/p2pnet/interfaces.go`, `pkg/p2pnet/netmonitor.go`

---

### ADR-S05: Default Gateway Tracking for Private IPv4 Switches

**Date**: 2026-03-14
**Status**: Accepted

### Context

Switching between two CGNAT carriers (e.g., two mobile hotspots, both private IPv4, no IPv6) produced zero network change events. No global IPs changed, no tunnel interfaces changed. The daemon was blind to the switch. Stale connections lingered, backoffs weren't reset, probes weren't triggered.

Additionally, the macOS route socket only listened for `RTM_NEWADDR`/`RTM_DELADDR`/`RTM_IFINFO`. WiFi hotspot switches on macOS fire route changes (`RTM_ADD`/`RTM_DELETE`) but not always address events when only private IPs change on the same interface.

### Decision

Two changes:

1. **Gateway detection**: `DiscoverInterfaces()` calls platform-specific `defaultGateway()` (macOS: `/sbin/route -n get default`, Linux: `/sbin/ip route show default`). The gateway IP is stored in `InterfaceSummary.DefaultGateway`. `diffSummaries` fires `GatewayChanged=true` when the gateway changes. Gate: current must be non-empty (suppresses intermittent lookup failures), but old may be empty (prevents permanent blindness if initial lookup fails).

2. **Route socket expansion (macOS)**: Added `RTM_ADD`, `RTM_DELETE`, `RTM_CHANGE` to the BSD route socket listener alongside existing address and interface events.

### Why `/sbin/route` not `route`?

The daemon runs under launchd on macOS. launchd's PATH doesn't include `/sbin`. Using the bare command name failed silently. Full path eliminates PATH dependency. Same reasoning for `/sbin/ip` on Linux (systemd services may have minimal PATH).

### Why not track all private IPv4 addresses instead?

Option 2 in the original plan. Rejected: higher false-positive rate from DHCP renewals assigning different IPs on the same network. Gateway change is a definitive signal that the network changed. Known blind spot: two networks with the same gateway IP (e.g., two iPhone hotspots both using 172.20.10.1). Impact is low: both are CGNAT RELAYED, and stale connections die via TCP keepalive within 30-60s.

### Consequences

- Private IPv4-only network switches are now detected within ~2s (500ms debounce + gateway lookup)
- Platform-specific exec (`route`/`ip`) adds ~10-20ms per check. Acceptable: runs at most once per debounced network event.
- Feature degrades gracefully on unsupported platforms (returns empty string, gateway detection disabled, other detection methods still work)

**Reference**: `pkg/p2pnet/interfaces.go`, `pkg/p2pnet/gateway_darwin.go`, `pkg/p2pnet/gateway_linux.go`, `pkg/p2pnet/gateway_other.go`, `pkg/p2pnet/netmonitor.go`, `pkg/p2pnet/netmonitor_darwin.go`

---

### ADR-S06: Dial Worker Cache Poisoning Workaround

**Date**: 2026-03-13
**Status**: Accepted

### Context

libp2p's `dial_sync.go` creates one dial worker per peer. All concurrent `DialPeer` calls share the worker. The worker caches dial results in `trackedDials` (`dial_worker.go:215-237`). After a network switch, PeerManager (or DHT, or autonat) dials the peer's stale LAN IPv4. WiFi is settling, the dial fails with "no route to host". This error is cached. When mDNS discovers the peer on the new LAN and calls `DialPeer`, it joins the existing worker and gets the cached error from a hashmap lookup - never actually dials.

Root cause confirmed via source analysis: the "instant no route to host" (1-5ms) that initially looked like a transport bug was actually a hashmap lookup returning a cached error, not a real TCP connect attempt.

### Decision

Three-part fix (all three required - removing any one re-opens the cache poisoning window):

1. **Strip in OnNetworkChange**: `PeerManager.StripPrivateAddrs()` removes all `isStaleOnNetworkChange()` addresses (RFC 1918 + RFC 6598 CGNAT + ULA + loopback) from watched peers' peerstore BEFORE triggering reconnectNow/BrowseNow. Order: strip first, then reset black holes, clear backoffs, close stale, reconnect, browse. No restore needed: mDNS re-populates from fresh multicast discovery.

2. **TCP readiness probe in mDNS**: `probeAddr()` calls context-aware `probeTCPReachable()` (3s timeout, 500ms retry intervals, `net.Dialer.DialContext`) BEFORE `DialPeer`. Uses its own timeout, does not consume `DialPeer`'s 5s context. Prevents mDNS from self-poisoning the cache if WiFi is settling.

3. **scheduleRetry (10s backup)**: Handles probe failure on first attempt (~375ms after network switch). Now acquires the mDNS semaphore (was missing, creating an asymmetry with HandlePeerFound).

### Why `isStaleOnNetworkChange` instead of `manet.IsPrivateAddr`?

`manet.IsPrivateAddr` misses RFC 6598 CGNAT range (100.64.0.0/10), used by Starlink. `isStaleOnNetworkChange` unifies the check to cover all private ranges.

### Consequences

- Stale LAN addresses are removed before any subsystem can cache errors from them
- mDNS independently validates reachability before calling into libp2p's dial path
- Additional 3s worst-case delay on first mDNS upgrade attempt (probe timeout). Acceptable: the alternative is a 30s+ wait for the next mDNS browse cycle.
- Probe TCP connection creates a brief aborted Noise handshake on the target peer. No backoff impact (outbound-only, peer sees it as a normal failed connection attempt).

**Reference**: `pkg/p2pnet/peermanager.go`, `pkg/p2pnet/mdns.go`, `cmd/shurli/serve_common.go`

---

### ADR-S07: Autorelay Static Relay Tuning

**Date**: 2026-03-14
**Status**: Accepted

### Context

libp2p's autorelay defaults are designed for DHT-discovered relay networks with many candidates:
- `backoff=1h`: Don't retry failed relays aggressively
- `minInterval=30s`: Rate-limit peer source queries (network cost)
- `bootDelay=3min`: Wait for enough candidates before connecting
- `minCandidates=4`: Compare quality across multiple candidates

Shurli uses static relay configuration (known VPS addresses in config). The peer source returns a hardcoded list instantly (zero network cost). There are exactly 2 known relays. Every default is wrong for this use case.

The `backoff` was already reduced to 30s (root cause #9). Investigation of the ~15s relay reconnection delay after network change revealed `minInterval=30s` as the primary bottleneck: the peer source can only be queried every 30s, so after relay disconnection, the daemon waits up to 30s before it can even learn about available candidates.

### Decision

Add three options alongside the existing `WithBackoff(30s)`:
- `WithMinInterval(5s)`: Peer source queried every 5s instead of 30s. Zero cost for static list.
- `WithBootDelay(0)`: No waiting period. Connect to first available relay immediately.
- `WithMinCandidates(1)`: Don't wait for multiple candidates. We know which relays we want.

### Consequences

- Relay reconnection after network change: ~5-10s (was ~30-35s)
- Boot time relay reservation: immediate (was up to 3 minutes, masked by manual `h.Connect` + `time.Sleep(5s)` workaround in startup code)
- If DHT-discovered relays are added later, these values should be reviewed (minCandidates=1 skips quality selection)
- No relay VPS overload risk: `backoff=30s` still rate-limits actual reservation attempts per relay

**Reference**: `pkg/p2pnet/network.go`
