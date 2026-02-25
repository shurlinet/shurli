---
title: "Adaptive Path Selection: Real Numbers from 4 Networks"
date: 2026-02-25
tags: [release, batch-i]
description: "Parallel dial racing, zero-dependency STUN, reachability grades, and graceful network switching. Tested on satellite, terrestrial, 5G CGNAT, and wired Ethernet with real latency data."
image: /images/blog/batch-i-hero.svg
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Batch I: Adaptive Path Selection](/images/blog/batch-i-hero.svg)

## The numbers

Two cross-network test sessions. Four network types. Real measurements, not benchmarks.

| Network | Path | Latency | Notes |
|---------|------|---------|-------|
| Satellite WiFi | DIRECT | 22ms avg | IPv6 QUIC, same LAN |
| Terrestrial WiFi | DIRECT | 7-29ms | Cross-ISP via public IPv6 |
| 5G Cellular (CGNAT) | RELAYED | 112-200ms | Carrier NAT blocks direct |
| 5G Day 2 (hole-punch) | DIRECT | 68-160ms | DCUtR succeeded, 257ms punch |
| Wired Ethernet | FAILED | - | Daemon died on interface change |

Connection time dropped from 45 seconds worst-case to 3-10 seconds typical. Zero packet loss through a live WiFi-to-cellular network switch. One honest failure that we haven't fixed yet.

## What we tested

Four network types, two machines, one relay:

- **Network A (Satellite WiFi)**: Consumer satellite internet with stock router. Both peers on the same LAN. Full IPv6 with ULA addresses.
- **Network B (Terrestrial WiFi)**: Standard ISP with enterprise access points. Different ISP from Network A. Public IPv6 available.
- **Network C (5G Cellular CGNAT)**: Mobile hotspot through carrier NAT. Private `172.x.x.x` addresses. No inbound connections possible without hole-punching.
- **Network D (Wired Ethernet)**: USB-to-LAN adapter. Always-priority route. Used as baseline and for testing interface changes.

Methodology: the USB LAN adapter was unplugged before each WiFi switch to force all traffic through the network being tested. No split routing, no cheating.

## Latency across networks

![Cross-Network Latency: Real Measurements](/images/blog/batch-i-latency-chart.svg)

The chart tells the story. Satellite and terrestrial WiFi both achieve direct connections at 22ms or less via IPv6. The 5G cellular path has no choice but to relay at 157ms average because carrier NAT blocks direct connections.

The interesting result is day 2 on 5G: a hole-punch succeeded via DCUtR (Direct Connection Upgrade through Relay), dropping latency from 157ms relayed to 68ms direct. The carrier's NAT port mapping from the relay connection was still fresh. On subsequent attempts after switching away and back, the mapping had expired and all three hole-punch attempts failed. CGNAT is intermittently punchable, not reliably so.

## Parallel dial racing

![Dial Racing: Sequential vs Parallel](/images/blog/batch-i-dial-racing.svg)

Before Batch I, connection setup was sequential: try DHT discovery (15 second timeout), then fall back to relay (30 second timeout). Worst case: 45 seconds to connect.

Now both paths race in parallel. DHT discovery and relay connection start simultaneously. The first path to succeed wins. The loser gets cancelled. Typical connection time: 3-10 seconds.

The implementation uses Go's `context` cancellation. When the relay connects first (common on CGNAT networks), the DHT goroutine's context gets cancelled immediately. No wasted resources, no zombie connections.

After the initial connection, path upgrades happen in the background. A relayed connection can be upgraded to direct if hole-punching succeeds, without dropping the existing session.

## Zero-dependency STUN

STUN (Session Traversal Utilities for NAT) determines what kind of NAT sits between a peer and the internet. Most implementations pull in external libraries. Ours is ~150 lines of Go with zero new dependencies.

It uses Google's public STUN servers to determine the external IP and NAT behavior, then classifies the result into a reachability grade:

| Grade | Meaning | Direct connection? |
|-------|---------|-------------------|
| A | Public IPv6 detected | Yes, reliably |
| B | Public IPv4 or hole-punchable NAT | Yes, usually |
| C | Port-restricted NAT | Maybe, with effort |
| D | Symmetric NAT or CGNAT | Unlikely |
| F | No connectivity detected | No |

The critical design choice: CGNAT detection **caps the grade at D** regardless of what the inner NAT reports. STUN will happily say "hole-punchable" when a port-restricted NAT sits behind CGNAT, because STUN only sees the inner NAT. The outer CGNAT will still drop unsolicited inbound packets. Our grade computation overrides STUN's false optimism.

Current limitation: CGNAT detection only identifies RFC 6598 addresses (`100.64.0.0/10`) on local interfaces. Mobile carriers that use RFC 1918 addresses (like `172.x.x.x`) for CGNAT cannot be distinguished from regular home networks. The grade for Network C was technically "hole-punchable" even though the carrier NAT blocked most attempts.

## Graceful network switching

![Network Switch: Graceful Degradation](/images/blog/batch-i-network-switch.svg)

During testing, we switched from Satellite WiFi (direct, 22ms) to 5G Cellular (relayed, 157ms) while a connection was active. Zero packet loss through the entire transition.

The network change monitor detects interface changes and triggers three things:

1. **Stale address detection** - old addresses get flagged with `[stale?]` labels within 10 seconds
2. **STUN re-probe** - determines reachability on the new network
3. **Path re-evaluation** - if the current path is dead, falls back to relay immediately

The known gap: plugging or unplugging a wired Ethernet adapter killed the daemon entirely. Interface addition/removal (as opposed to WiFi switching) isn't handled yet. This is a Phase 5-L fix.

## Every peer is a relay

Most P2P systems have dedicated relay infrastructure. Tailscale runs DERP relay servers. Iroh uses relay nodes. These require someone to operate and pay for servers.

In Shurli, every peer with a public IP automatically becomes a relay for peers that need one. If your home server has a public IPv6 address (increasingly common with IPv6 adoption at ~49% globally), it serves as a relay for your mobile devices behind CGNAT. No configuration. No extra software. No monthly server bill.

The relay capability activates when the daemon detects a public IP during startup. It uses libp2p's circuit relay v2 protocol with resource limits: bounded connections, bounded bandwidth, TTL on reservations. A compromised relay can't amplify traffic or hold connections indefinitely.

This matters because relay dependence is temporary. As IPv6 adoption continues (projected 80% by 2030-2032), more peers will have direct-capable addresses and fewer will need relaying at all.

## The surprise: CGNAT hole-punch

Day 2 testing on the 5G cellular network produced an unexpected result. The first ping went through the relay at 157ms. The second ping came back direct at 68ms. DCUtR (Direct Connection Upgrade through Relay) had succeeded.

What happened: the relay connection created a NAT port mapping on the carrier's CGNAT. DCUtR exploited that existing mapping to establish a direct connection, bypassing the relay entirely. The hole-punch took 257ms.

This isn't reliable. On day 1, the same network stayed relayed for the entire session. After switching to Satellite WiFi and back, three subsequent hole-punch attempts all failed (the mapping had expired). The carrier's NAT behavior is inconsistent.

But it demonstrates the system working exactly as designed: try everything, take whatever works, be honest about what doesn't.

## What's still broken

Honesty about failures matters more than marketing about successes.

1. **Wired Ethernet plug/unplug kills the daemon.** Interface addition/removal triggers a fatal error. WiFi switching works fine because the interface stays present. Fix: Phase 5-L PeerManager with proper address lifecycle management.

2. **No path re-upgrade after network switch.** After returning to Satellite WiFi from 5G, the connection stayed relayed even though both peers were back on the same LAN. The daemon has no mechanism to detect that a better path became available. Fix: Phase 5-L PeerManager with periodic path probing.

3. **CGNAT detection misses RFC 1918 carriers.** Mobile carriers using `172.x.x.x` for CGNAT look identical to home networks. Only `100.64.0.0/10` (RFC 6598) triggers the CGNAT grade cap. No clean fix exists without active probing.

4. **SSH proxy always relayed.** Even on networks where ping achieved direct connections, SSH proxy sessions went through the relay. The proxy doesn't benefit from DCUtR upgrades on existing connections. Fix: needs investigation in Phase 5-L.

## Impact

| Metric | Before Batch I | After Batch I |
|--------|---------------|---------------|
| Connection time (worst case) | 45 seconds | 3-10 seconds |
| Connection time (same LAN) | 15 seconds | 3 seconds |
| NAT awareness | None | A-F grade with CGNAT detection |
| Network change handling | None | Graceful degradation, 0% packet loss |
| Relay infrastructure | VPS only | Every peer with public IP |
| New dependencies added | - | 0 |
| Networks tested | 0 | 4 (satellite, terrestrial, 5G, wired) |

Six components shipped in Batch I: interface discovery, parallel dial racing, STUN NAT detection, path quality tracking, network change monitoring, and every-peer-is-a-relay. Zero new dependencies. Tested on real networks with real measurements, including the failures.

Next: Phase 5. mDNS local discovery, PeerManager for intelligent path management, and GossipSub for network intelligence.
