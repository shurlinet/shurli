---
title: "Chaos Testing: 16 Network Transitions, 11 Root Causes, Zero Daemon Restarts"
date: 2026-03-14
tags: [release, network, chaos-testing]
image: /images/blog/chaos-testing-hero.svg
description: "How physical chaos testing across 5 ISPs and 3 VPNs exposed 11 root causes in libp2p's network transition handling - and how we fixed all of them."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Chaos testing overview: 5 network types, automatic recovery chain, 11 root causes fixed, 16 test cases passed, 0 daemon restarts](/images/blog/chaos-testing-hero.svg)

## What happened

We put Shurli's network layer through physical chaos testing: switching between real networks on real hardware, watching what breaks, fixing it live, and switching again. No simulations. No mocks. Real WiFi switches, real mobile hotspots, real VPN tunnels.

**16 test cases** across 5 ISPs and 3 VPN providers. Every transition a user might hit in daily life: WiFi to cellular, cellular to LAN, LAN insertion while on cellular, VPN connect while on WiFi, VPN disconnect, rapid switching between carriers.

The result: **11 root causes** found in how libp2p handles network transitions, all fixed. Then 8 additional flags from extended chaos testing, all investigated and resolved. The daemon now handles every network transition automatically. No restarts.

## Why it matters

A P2P daemon that can't survive a WiFi switch is a toy. Real devices move between networks constantly: home WiFi, office WiFi, cellular, VPN, tethering. Each transition changes IP addresses, routing tables, NAT behavior, and transport availability. If any of these transitions require a daemon restart, the network isn't ready for production.

After this work, Shurli handles all of these transitions automatically:

| Transition | Detection | Recovery |
|-----------|-----------|----------|
| WiFi to cellular | < 1s | 5-15s to RELAYED |
| Cellular to WiFi (same LAN as peer) | < 1s | < 5s to DIRECT via mDNS |
| USB LAN insertion | instant | < 1s to DIRECT via IPv6 |
| USB LAN removal | instant | 5-10s to RELAYED |
| VPN connect (hard tunnel) | < 1s | 5-15s to RELAYED |
| VPN disconnect | < 1s | 5-30s to DIRECT |
| Between two CGNAT carriers (no IPv6) | < 2s | 5-15s to RELAYED |

## What we found

### The black hole detector problem

libp2p tracks IPv6 and UDP dial success rates. After enough failures (5 out of 100), it enters a "Blocked" state and refuses further dials of that type. Designed to avoid wasting resources on known-bad transports.

The problem: this state persists across network switches. Switch from a CGNAT network (where IPv6 always fails) to a WiFi network with working IPv6, and the detector still blocks IPv6. The daemon is stuck on relay even though a direct path exists.

Fix: custom `BlackHoleSuccessCounter` instances with a `ResetBlackHoles()` method, called on every network change event.

### Relay connections that refuse to die

After switching networks, old relay connections stay in the swarm's connection list even after `conn.Close()` is called. libp2p's swarm removes closed connections asynchronously, and the closing connection can linger for up to 57 seconds. During this window, the reconnect loop sees it and thinks "direct connection is still active" - so it discards the new relay connection. The peer is stranded with no working path.

Fix: `hasLiveDirectConnection()` checks whether a connection's local IP still exists on any active interface before treating it as "live". Closed connections on vanished interfaces are correctly identified as dead.

### The dial worker cache

This was the subtlest bug. libp2p creates one dial worker per peer. All concurrent `DialPeer` calls share it. The worker caches results in a hashmap. After a network switch, PeerManager (or DHT, or autonat) dials the peer's stale LAN address. WiFi is still settling, the dial fails. This error is cached. When mDNS discovers the peer on the new LAN and calls `DialPeer`, it gets the cached error from a hashmap lookup - never actually dials.

Fix: three-part approach. (1) Strip all private/LAN addresses from the peerstore before triggering reconnect. (2) mDNS runs a TCP readiness probe before `DialPeer` to avoid self-poisoning. (3) Retry with backoff handles probe failure on first attempt.

### Invisible network switches

The network monitor only diffed global IP addresses. Switching between two CGNAT carriers (both private IPv4, no IPv6) produced zero change events. The daemon was blind.

Two fixes: (1) VPN tunnel detection - watch for `utun`/`tun`/`wg`/`ppp` interface names appearing or disappearing. (2) Default gateway tracking - parse the routing table to detect gateway changes even when no global IPs change.

On macOS, we also discovered that WiFi hotspot switches fire route table changes (`RTM_ADD`/`RTM_DELETE`) but not always address events (`RTM_NEWADDR`/`RTM_DELADDR`). The route socket listener now watches both.

### Autorelay defaults that don't fit

libp2p's autorelay is tuned for DHT-discovered relay networks: 1-hour backoff, 30-second peer source rate limit, 3-minute boot delay waiting for 4 candidates. For static relays (known VPS addresses), every one of these defaults is wrong. The peer source returns a hardcoded list instantly, there's no discovery phase, and there are exactly 2 known relays.

Fix: backoff 30s, peer source interval 5s, boot delay 0, min candidates 1. Reconnection after relay loss dropped from ~30s to ~5-10s.

## The full list

| # | Root Cause | Fix |
|---|-----------|-----|
| 1 | Black hole detector blocks valid transports after network switch | Custom instances with reset on network change |
| 2 | Probe targets relay server IP instead of peer IP (circuit addr parsing) | Skip `/p2p-circuit` addresses in probe target extraction |
| 3 | ForceDirectDial tries all peerstore addresses, cascade failure | Constrain dial to confirmed address only, restore after |
| 4 | mDNS relay cleanup fights remote PeerManager reconnect | Remove aggressive relay closing from mDNS, let coexist |
| 5 | CloseStaleConnections misses private IPs (only checked global) | Read all interface IPs, close any with vanished local IP |
| 6 | Autorelay drops reservations on public networks | ForceReachabilityPrivate always in daemon mode |
| 7 | mDNS upgrade poisoned by UDP black hole state | Filter to TCP-only addresses for LAN upgrade |
| 8 | CloseStaleConnections kills valid IPv6 during DAD window | Three-tier close logic with DAD grace for IPv6 |
| 9 | Autorelay 1-hour backoff prevents re-reservation after network change | WithBackoff(30s) |
| 10 | ProbeUntil cooldown blocks reconnect after direct connection dies | Clear cooldown on disconnect and network change |
| 11 | Swarm reports closed connection as live for up to 57s | hasLiveDirectConnection checks interface membership |

### Post-chaos investigation (8 additional flags)

| Flag | Issue | Resolution |
|------|-------|-----------|
| #1 | Short-lived direct connection death (13s) | TOCTOU race in mDNS + idle relay cleanup |
| #2 | Time-to-DIRECT 4min vs 74-82s baseline | Resolved by flag #1 fix |
| #5 | Network monitor blind to private IPv4 switches | Default gateway tracking + route socket expansion |
| #7 | mDNS TCP outbound fails on settling WiFi | Dial worker cache poisoning workaround (3-part fix) |
| #8 | VPN tunnel invisible to network monitor | Tunnel interface name detection |
| #4 | "Peer relay disabled" log on CGNAT switch | Cosmetic - peer relay correctly disables without public IP |

Flags #3 and #6 were informational (Starlink IPv6 path diversity, carrier IPv6 availability).

## What's next

The network layer is now tested against every transition we can physically reproduce. The blog post on Shurli's file transfer plugin is coming after physical network testing of file transfers across these same transitions. Reputation system wiring is next after that.

