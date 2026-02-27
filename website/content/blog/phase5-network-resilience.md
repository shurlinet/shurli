---
title: "Automatic WiFi Transition: Real Hardware, Real Networks, Zero Restarts"
date: 2026-02-27
tags: [release, phase-5]
description: "Switch WiFi, unplug cables, hop between 5G and satellite. Shurli detects the change and reconnects in seconds. Tested on 5 physical networks with 7 transition scenarios."
image: /images/blog/phase5-network-resilience.svg
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Phase 5: Automatic Network Resilience](/images/blog/phase5-network-resilience.svg)

## The problem we solved

Switch your laptop from home WiFi to a phone hotspot. Your P2P connection drops. You restart the daemon. Switch back to WiFi. Restart again. Every network change requires manual intervention.

This was Shurli before Phase 5. The daemon detected network changes but couldn't recover from them automatically. Connections on disappeared interfaces sat dead for minutes (TCP keepalive timeout). The reconnect loop waited up to 30 seconds before trying. mDNS couldn't find LAN peers fast enough. And when it did, PeerManager would stomp the LAN connection with a relay.

Phase 5 fixes all of this. Seven transition scenarios tested on five physical networks. Every single one recovers automatically. Zero daemon restarts.

## The numbers

| Transition | Path change | Recovery time |
|------------|-------------|---------------|
| Cellular (CGNAT) to 5G hotspot | RELAYED to DIRECT | ~5s |
| 5G hotspot to Cellular (CGNAT) | DIRECT to RELAYED | ~35s |
| Cellular (CGNAT) to Satellite WiFi (LAN) | RELAYED to DIRECT (LAN) | ~10-15s |
| Satellite WiFi to Cellular (CGNAT) | DIRECT to RELAYED | ~5s |
| Cellular (CGNAT) + USB LAN plug | RELAYED to DIRECT (IPv6) | ~8s |
| USB LAN unplug | DIRECT to RELAYED | ~5s |
| Terrestrial WiFi to Satellite WiFi | DIRECT to DIRECT | ~5s |

The worst-case transition (5G hotspot to cellular CGNAT) takes ~35 seconds because relay reservation needs to be re-established after losing the direct connection. The typical case is 5-15 seconds.

## Connection priority table

Shurli now enforces a strict priority order. Higher priority paths always win:

```
1. LAN (mDNS, private IPv4)     ~23ms
2. Direct IPv6 (path probing)   ~23ms
3. Relay (fallback)             ~180ms
```

If you're on the same LAN as your peer, you get a direct connection at LAN latency. If you're on a different network with public IPv6, you get a direct connection via IPv6 probing. If you have no IPv6 and you're behind CGNAT, you fall back to relay.

The priority isn't just a suggestion. When PeerManager establishes a relay connection but mDNS has already connected directly, PeerManager detects the existing direct connection and immediately discards the relay. The relay never gets used.

## What shipped

Seven components working together:

### 1. Stale connection cleanup

When a network interface disappears (WiFi switch, cable unplug), connections bound to that interface's IP are dead. But libp2p doesn't know yet. TCP keepalive takes minutes to detect. During that window, `host.Connect()` returns "already connected" and the reconnect loop skips the peer.

`CloseStaleConnections()` matches each connection's local IP against the removed IPs from the network change. Dead connections are closed within 500ms of the interface disappearing. The reconnect loop can immediately dial through the new interface.

### 2. Immediate reconnect trigger

Before Phase 5, `OnNetworkChange()` reset backoff timers but the reconnect loop ran on a 30-second ticker. After a WiFi switch, you'd wait up to 30 seconds even though the backoffs were cleared.

Now `OnNetworkChange()` sends on a `reconnectNow` channel that wakes the loop immediately. Combined with stale cleanup, the full sequence is: interface disappears, dead connections close, reconnect triggers, new connection established. All within seconds.

### 3. Native mDNS via dns_sd.h

The pure-Go mDNS implementation competed with the OS mDNS daemon for the multicast socket on port 5353. On macOS, mDNSResponder owns that port. Two processes fighting over the same socket causes silent discovery failures.

Phase 5 replaces the browse implementation with CGo bindings to `dns_sd.h`. This talks to the OS daemon via IPC instead of competing with it. Registration stays on zeroconf (works reliably for advertising). Platforms without CGo fall back to the original implementation.

### 4. LAN-first address filtering

mDNS discovers 14 multiaddrs for a LAN peer: private IPv4, public IPv6, ULA, loopback, across TCP and QUIC. Adding all 14 to the peerstore before connecting causes the swarm to try every address, including unreachable ones. On satellite networks with client isolation (inter-client IPv6 blocked), 12 of 14 addresses timeout at 5 seconds each.

`filterLANAddrs()` reduces 14 addresses to 2: only private IPv4 addresses on matching local subnets. The connect completes in milliseconds instead of minutes. The full address set is added to the peerstore after the connect succeeds.

### 5. IPv6 path probing with source binding

When your Mac has USB LAN (public IPv6) plugged in alongside WiFi (CGNAT, no IPv6), a direct IPv6 path exists through USB LAN. But libp2p won't try it because the peer is already connected via relay.

`ProbeAndUpgradeRelayed()` does raw TCP probes to the peer's IPv6 addresses. Each probe is source-bound to a specific local IPv6 address using `net.Dialer{LocalAddr: ...}`. This is critical on macOS where disconnected VPN utun interfaces can hijack the default IPv6 route. Source binding forces the kernel to route through the correct physical interface.

If the probe succeeds, `ForceDirectDial` establishes direct alongside relay, then relay connections are swept for 90 seconds to cover the remote peer's reconnect loop.

### 6. Relay-discard logic

The race condition that took the longest to find: mDNS connects directly to a LAN peer. But PeerManager's reconnect loop was already mid-dial. It finishes and establishes relay. Now both direct and relay exist. mDNS sweeps the relay. PeerManager's next tick re-establishes it. Cat-and-mouse.

The fix is simple: after `DialPeer` returns a RELAYED result, check if any non-relay connections exist. If direct is already active, close the relay and return. Direct always wins.

### 7. Network change orchestration

All subsystems fire in a specific order after a network change:

1. `CloseStaleConnections(removed)` - kill dead connections
2. `OnNetworkChange()` - reset backoffs, trigger immediate reconnect
3. `BrowseNow()` - mDNS re-browse for LAN peers
4. `ProbeAndUpgradeRelayed()` (goroutine) - IPv6 probing
5. `AnnounceNow()` - NetIntel state update
6. STUN re-probe (goroutine) - external address detection

Steps 1-3 are synchronous and complete in under a millisecond. Steps 4-6 run in background goroutines. The full recovery completes in 5-15 seconds.

## What's been resolved since initial release

1. ~~**CGNAT detection for RFC 1918 carriers.**~~ **Fixed.** `network.force_cgnat: true` config option lets users on carriers using RFC 1918 addresses for CGNAT correctly signal their status. Auto-detection still handles RFC 6598 (`100.64.0.0/10`) automatically.

## What's still open

1. **Active stream continuity.** When a network change kills the underlying connection, active streams (proxy sessions, file transfers) break. PeerManager reconnects in 5-15 seconds and new streams work immediately, but in-flight data on the old stream is lost. QUIC 0-RTT session resumption could make this seamless in a future phase.

## How it was tested

This wasn't tested in a lab or with mocked interfaces. Five physical networks, real hardware, real ISPs:

- **Satellite WiFi** - satellite internet, same LAN as home-node
- **Terrestrial WiFi** - different ISP, enterprise access points
- **5G Hotspot** - mobile hotspot, different carrier
- **Cellular (CGNAT)** - phone tethering, CGNAT, no IPv6, relay-only
- **USB LAN (IPv6)** - wired Ethernet adapter, public IPv6

The testing methodology: switch WiFi on the laptop, report what you switched to, check daemon logs, run pings. If something breaks, fix it in the code, rebuild, restart daemon, retest. Six bugs were found and fixed in a single 5+ hour session. Every fix was validated on real hardware before moving to the next scenario.

## Impact

| Metric | Before Phase 5 | After Phase 5 |
|--------|----------------|---------------|
| WiFi switch recovery | Manual restart | 5-15 seconds, automatic |
| LAN peer connection | 60+ seconds (address timeout) | 2-3 seconds (filtered) |
| Cross-ISP IPv6 direct | Not attempted | 23ms via USB LAN |
| Relay-to-direct upgrade | Not detected | Automatic via mDNS + probing |
| mDNS reliability | Intermittent (socket conflict) | Stable (native dns_sd.h) |
| Networks tested | 4 | 5 |
| Transition scenarios verified | 0 | 7 |
| New dependencies added | - | 0 |
