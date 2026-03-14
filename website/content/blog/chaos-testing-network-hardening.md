---
title: "From Broken to Bulletproof: How Chaos Testing Transformed Shurli's Network Layer"
date: 2026-03-14
tags: [release, network, chaos-testing]
image: /images/blog/chaos-testing-hero.svg
description: "4 days of physical chaos testing across 5 ISPs and 3 VPNs. 11 root causes found and fixed. The daemon now handles every network transition automatically."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Before and after: network transitions went from broken (manual restarts needed) to bulletproof (zero restarts, all automatic)](/images/blog/chaos-testing-hero.svg)

## The problem we solved

Before this work, switching WiFi networks could leave Shurli stuck on relay indefinitely. Connecting a VPN could isolate you completely. Switching between two mobile hotspots was invisible to the daemon. Every one of these scenarios required a manual daemon restart to recover.

After 4 days of physical chaos testing, live debugging, and targeted fixes: every network transition is now automatic. Zero restarts needed. The daemon detects changes, cleans up stale state, and re-establishes the best available path within seconds.

## How we tested it

No simulations. No mocks. Real hardware, real networks, real switches.

The setup: a laptop switching between 5 different ISPs (satellite WiFi, two mobile carriers, USB ethernet, and WiFi hotspots) and 3 VPN providers (Mullvad, ProtonVPN, ExpressVPN). A home server on the local network. Two relay servers in different regions. Three devices, real CGNAT, real IPv6, real VPN tunnels.

The test loop: switch networks, watch the logs, check if the daemon recovered, fix what broke, rebuild, restart, switch again. Repeat across 16 test cases covering every transition a real user would encounter.

This took roughly 40+ hours across 4 days. Not because the fixes were complex individually, but because each fix exposed the next layer. Fixing the black hole detector revealed the dial worker cache. Fixing the dial cache revealed the VPN blind spot. Fixing the VPN detection revealed the gateway tracking gap. Each layer peeled back to reveal something deeper.

## What changed

### Shurli now sees every network switch

The network monitor used to only watch for global IP address changes. Three entire categories of network switches were invisible:

1. **VPN tunnels**: Connecting a VPN adds a `utun` or `tun` interface with a private IPv4. No global IPs change. The daemon was blind.
2. **CGNAT carrier switches**: Switching between two mobile hotspots (both private IPv4, no IPv6). No global IPs change. Invisible.
3. **WiFi hotspot switches on macOS**: The operating system fires route table changes but not always address events. The daemon's route socket wasn't listening for the right messages.

Now: the monitor detects tunnel interface names, tracks the default gateway, and listens for route changes alongside address events. Every switch fires a recovery chain within 1-2 seconds.

### Stale connections are cleaned up correctly

After a network switch, old connections can linger. libp2p's swarm takes up to 57 seconds to fully remove a closed connection from its internal list. During that window, the reconnect logic sees the ghost connection and refuses to establish a new one.

We fixed this at multiple levels: connections on vanished interfaces are identified as dead regardless of swarm state. IPv6 addresses get a grace period during the DAD (Duplicate Address Detection) window so valid connections aren't killed prematurely. The recovery chain runs in a specific order: strip stale addresses first, reset transport state, clear backoffs, then reconnect.

### The daemon recovers in seconds, not minutes

The most frustrating issue was recovery speed. Even when the daemon detected a switch, it could take 30+ seconds to re-establish a relay connection. We traced this to libp2p's autorelay subsystem, which is tuned for DHT-discovered relays (1-hour backoff, 30-second query interval, 3-minute boot delay). For known static relays, every one of these defaults was wrong.

After tuning: relay reconnection takes 5-10 seconds. Direct LAN connection via mDNS takes under 5 seconds. IPv6 path probing takes 1-3 seconds.

### The subtlest bug: dial worker cache poisoning

This one took the longest to find. After a network switch, any subsystem (PeerManager, DHT, autonat) that dials a peer's stale LAN address gets a "no route to host" error. This error is cached in a shared hashmap. When mDNS discovers the peer on the new LAN and tries to dial, it gets the cached error from a hashmap lookup. It never actually dials. The "instant failure" (1-5ms) that looked like a transport bug was actually a hashmap lookup returning a cached error from a different subsystem's failed attempt.

The fix required three coordinated changes: strip stale addresses before any reconnect attempt, probe TCP reachability before calling into libp2p's dial path, and retry with backoff when the probe fails during WiFi settling. All three are needed. Removing any one re-opens the cache poisoning window.

## The hurdles

**macOS Local Network Privacy**: macOS 15+ silently blocks daemon TCP connections to private IPs with EHOSTUNREACH. The daemon was running fine, the network was fine, but TCP connects to LAN peers failed. Took hours to identify as a macOS privacy setting, not a code bug.

**Wrong assumptions about the network**: We initially assumed "60-80s ARP timeout" on satellite WiFi was the root cause of slow mDNS upgrades. Hours of investigation later: the real cause was inside libp2p's dial path (the hashmap cache poisoning). The ARP assumption led us down a wrong path before we found the real issue through source code analysis.

**Route socket blind spots**: The macOS route socket was listening for address events (RTM_NEWADDR/RTM_DELADDR), but WiFi hotspot switches fire route changes (RTM_ADD/RTM_DELETE) instead. Discovering this required adding diagnostic logging, deploying a test binary, switching networks, and reading the raw route socket message types. Then we discovered the `route` command needed a full path (`/sbin/route`) because launchd's PATH doesn't include `/sbin`. Two separate issues, discovered one after the other.

**Each fix exposed the next bug**: Fixing the black hole detector let the daemon attempt IPv6 dials, which revealed the ForceDirectDial shotgun problem. Fixing ForceDirectDial let mDNS connect, which revealed the relay cleanup fight. Fixing the relay fight exposed the VPN blind spot. This layered discovery pattern meant we couldn't plan all 11 fixes upfront. Each one had to be found through physical testing of the previous fix.

## The numbers

| Metric | Value |
|--------|-------|
| Test cases | 16 |
| ISPs tested | 5 |
| VPN providers | 3 |
| Root causes found | 11 |
| Post-chaos flags | 8 (6 fixed, 2 informational) |
| Total commits | 14 (FT-K through FT-X) |
| Days of testing | 4 |
| Daemon restarts needed | 0 |

## Before and after

| Transition | Before | After |
|-----------|--------|-------|
| WiFi to cellular | stuck RELAYED, restart needed | RELAYED in 5-15s, automatic |
| Cellular to WiFi (LAN) | stuck RELAYED, restart needed | DIRECT in <5s via mDNS, automatic |
| USB LAN insertion | no upgrade | DIRECT in <1s via IPv6, automatic |
| USB LAN removal | stranded 90s | RELAYED instant, automatic |
| VPN connect | total isolation | RELAYED in 5-15s, automatic |
| VPN disconnect | slow recovery | DIRECT in 5-30s, automatic |
| CGNAT to CGNAT (no IPv6) | invisible to daemon | detected + recovered, automatic |

## What's next

The network layer is now tested against every transition we can physically reproduce. File transfer testing across these same transitions is next. Then: wiring the reputation system for autonomous trust decisions.

