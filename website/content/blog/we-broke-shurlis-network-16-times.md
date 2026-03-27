---
title: "We Broke Shurli's Network 16 Times. It Kept Running."
date: 2026-03-26
tags: [network, resilience, testing, AI-agents]
image: /images/blog/network-breaks-hero.svg
description: "We switched WiFi, tethered phones, plugged in cables, and connected VPNs. 16 physical network transitions across 5 network types. Shurli survived all of them without a single restart."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![16 network breaks, zero restarts: Shurli survives WiFi, cellular, ethernet, and VPN transitions without manual intervention](/images/blog/network-breaks-hero.svg)

## The problem you do not see

![The gap between cloud-mediated seamlessness and direct peer-to-peer connections that break on every network switch](/images/blog/network-breaks-the-problem.svg)

Your internet is resilient. Switch WiFi networks, tether to your phone, plug in a cable. Spotify keeps playing. Google Docs keeps syncing. Everything just works.

But that seamlessness is a trick. Every one of those services routes through a cloud server. Your device talks to the cloud. The cloud talks to the other end. When your network changes, your device opens a new connection to the same cloud endpoint. The cloud handles the rest. You never notice because you never left the cloud.

Now try connecting two devices directly. Your laptop to your home server. Your phone to your friend's node. No cloud in the middle. Switch WiFi, and the connection dies. There is no cloud server to reconnect to. The two devices have to find each other again, on a new network, with a new IP address, through a different NAT. Most peer-to-peer tools simply do not survive this.

That is the real problem. Not the internet dropping. The internet is fine. Direct connections between devices breaking every time the network underneath them changes.

And it gets worse when humans are not the ones using those connections.

## Why P2P, and why for AI agents

![A human recovers from a network drop easily. An AI agent could too, but every cycle spent on networking is wasted energy, especially on mobile.](/images/blog/network-breaks-agents.svg)

AI agents are moving off the cloud and onto your devices. Not because the cloud is bad, but because some things should not leave your machine. Your personal files. Your private data. Your home network. Your medical records. Your financial documents. An agent managing these things should not route them through someone else's server.

That means peer-to-peer. Direct connections between devices, under your control, with no intermediary that can read, throttle, revoke, or monetize your data.

But P2P has a hard problem that cloud does not: when the network changes, there is no central server to reconnect through. The devices have to find each other again, on a new network, through a different NAT, with a different IP.

Could an AI agent handle this itself? Technically, yes. It could detect the drop, re-discover peers, re-negotiate connections, manage transport state. But that is compute and energy spent on infrastructure instead of the agent's actual job. On a laptop with a large model, that might be tolerable. On a mobile device, where even small models drain the battery fast, every cycle spent managing the network is a cycle not spent doing useful work.

The proper solution is not to make agents smarter about networking. It is to make the network itself resilient at the protocol level, so agents never have to think about it. That is what Shurli does.

The network layer handles detection, cleanup, reconnection, and path upgrades. The agent just talks to it. As a direct consequence, humans building applications on top of Shurli get the same reliability for free.

## A Tuesday morning

![A day in the life: five network changes from home WiFi to cellular to wired LAN to VPN and back, with zero restarts and zero human intervention](/images/blog/network-breaks-tuesday.svg)

An AI agent on your home server manages your photo backups. It is syncing files from your laptop over your home WiFi. Direct connection, local network, fast.

You grab your laptop and head out. WiFi drops. Your phone's cellular hotspot picks up. Shurli detects the network change, cleans up the dead connection, and reroutes through a relay server automatically. The sync continues, a little slower, but uninterrupted. The agent did not notice anything.

You arrive at the office. Plug in an ethernet cable. Shurli detects the faster path and upgrades from the relay to a direct connection through the wired LAN. Sync speeds up. You did not do anything. The agent did not do anything. Shurli handled it.

Lunchtime, you connect a VPN for privacy. The tunnel changes every network path on your machine. Shurli detects the tunnel interface, adapts its connections in seconds. VPN off when you are done. Shurli adapts again.

Evening, you are back home. WiFi reconnects. Shurli finds your home server on the local network. Direct connection, no internet needed. Exactly where the morning started.

Five network changes. Zero restarts. Zero notifications. Zero human intervention. Shurli handled every transition. The agent never stopped working. Neither did you.

That is the target. Here is how we proved it works.

## The hard problem nobody wants to solve

![Quick solutions crack under real network switches. Shurli solves five layers: detect, clean, reset, reconnect, upgrade.](/images/blog/network-breaks-hard-problem.svg)

You can bolt together a P2P connection quickly. Libraries exist for it. Get two nodes talking over a relay, call it done.

But "connected" is not "stays connected." And "decentralized" is not "decentralized" if the connection only works because a centralized service is keeping it alive. Most quick P2P solutions stay up because they quietly depend on centralized signaling servers, managed TURN relays, or cloud infrastructure. Remove that, and the connection falls apart.

Shurli has relays too. But their role is intentionally minimal. At the current stage, relays behave somewhat like VPN servers: they create an independent network for you, where your traffic stays within your own infrastructure. You can also connect through Shurli's public seed node relays if you do not have your own, but those are deliberately restrictive. The entire trajectory of this project is to reduce relay dependence over time. Eliminating relays entirely is not technically possible today, but that is the end goal: a network where nodes find each other directly, without any intermediary.

The gap between a working demo and infrastructure that survives real network conditions is enormous. Most tools stop at "connected." Shurli lives in the space between "connected" and "stays connected and decentralized through anything."

This is a focus-driven project solving a fundamental problem: making the network layer so reliable that agents never have to think about it. Not a quick integration on top of existing libraries. A systematic, tested, hardened network layer where every fix exists because a real failure was found on real hardware.

Instead of riding the AI hype and building flashy agent demos, Shurli picked the hard problem underneath: reliable, genuinely decentralized peer-to-peer connectivity that does not break when the network changes. Because if the network layer breaks, nothing built on top of it matters.

## We broke it on purpose

![Five network types tested with real hardware: satellite WiFi, cellular, wired LAN, 5G hotspot, and VPN tunnels. All survived.](/images/blog/network-breaks-testing.svg)

No simulations. A laptop switching between real networks, a server node on a local network, relay servers in different regions. Real satellite WiFi, real cellular hotspots, real VPN tunnels.

Five network types:
- **Satellite WiFi**: high latency, local LAN to home server, CGNAT
- **Cellular hotspot**: carrier NAT, no public IP, always relayed
- **Wired LAN**: terrestrial ISP, public IPv6, direct connections
- **5G hotspot**: carrier NAT, variable IPv6 availability
- **VPN tunnels**: three different providers, each with different tunnel behavior

16 distinct transitions. Switch a network, watch the daemon logs, check if it recovered, fix what broke, rebuild, switch again.

The method was simple: break it, fix it, break it again. If it survives the second time, move to the next transition.

This is a follow-up to the [original engineering blog](/blog/from-broken-to-bulletproof/) written during the testing campaign on March 14, 2026. That post is the raw technical record. This one tells the bigger story: what it means, what changed since, and why it matters for the future of AI-native infrastructure.

## 11 things that break underneath

![Four categories of root causes discovered in layers: transport memory, invisible switches, stale state, and wrong defaults](/images/blog/network-breaks-root-causes.svg)

Every root cause fell into one of four categories.

**Transport memory.** The networking library tracks which connection types work and which fail. After enough failures on one network (say, IPv6 on a cellular hotspot), it blocks that type entirely. Switch to a network where IPv6 works perfectly, and the library still refuses to try it. Old memories poisoning new reality. Three root causes here: transport state persisting across switches, a shared dial cache returning stale errors to fresh callers, and direct dial attempts trying every address instead of just the confirmed one.

**Invisible switches.** The network monitor only watched for changes in global IP addresses. Three kinds of switches were completely invisible: VPN tunnels (which add a private IPv4 interface, no global change), switching between two cellular hotspots (both behind carrier NAT, no global IPs on either), and WiFi hotspot changes on macOS (which fire route table events, not address events). The daemon simply did not know the network had changed.

**Stale state.** After a network switch, dead connections linger. The networking library reports a closed connection as "active" for up to 57 seconds. The reconnect logic sees this ghost connection and decides "we already have a direct path, skip the relay." Meanwhile, the real connection is dead. Three root causes in this category: zombie connections, reconnect cooldowns that block recovery, and LAN discovery aggressively closing relay connections while the remote peer tries to reconnect through them.

**Wrong defaults.** The upstream library is tuned for discovering relays via a distributed hash table: 1-hour retry backoff, 30-second query interval, 3-minute boot delay. For known static relays, these defaults mean 30+ seconds of silence after a network switch. And on networks with public IPv6, the library drops relay reservations entirely, assuming "public IP means publicly reachable." Both assumptions are wrong for real-world deployments behind satellite internet and carrier NAT.

Every fix exposed the next problem. Fixing transport memory revealed the invisible switches. Fixing detection revealed the stale state. Fixing stale connections revealed the wrong defaults. Four days, 11 root causes, each one peeled back to reveal the layer underneath.

| # | Root Cause | Category | Fix |
|---|-----------|----------|-----|
| 1 | Transport blocker persists across network switches | Transport memory | Reset on every network change |
| 2 | Probe tests relay server IP instead of peer IP | Transport memory | Skip circuit addresses in probe targets |
| 3 | Direct dial tries all addresses, cascade failure | Transport memory | Constrain to confirmed address only |
| 4 | LAN cleanup fights remote node reconnect | Stale state | Let direct and relay coexist |
| 5 | Stale connection checker misses private IPs | Stale state | Check all interface IPs, not just global |
| 6 | Relay dropped on networks with public IPv6 | Wrong defaults | Permanent relay reservation in daemon mode |
| 7 | LAN upgrade poisoned by transport blocker | Transport memory | TCP-only filter for LAN paths |
| 8 | Valid IPv6 killed during address verification window | Stale state | Grace period for IPv6 during DAD |
| 9 | 1-hour relay retry backoff | Wrong defaults | Reduced to 30 seconds |
| 10 | Reconnect cooldown blocks recovery after disconnect | Stale state | Clear cooldown on disconnect and network change |
| 11 | Closed connection reported as live for 57 seconds | Stale state | Verify local IP still exists on active interface |

## What changed since the first blog

![Timeline from March 14 to v0.3.0: 164 commits, all open flags resolved, four major features built on top of the network layer](/images/blog/network-breaks-since-then.svg)

The [original blog](/blog/from-broken-to-bulletproof/) was written on March 14, 2026, during the testing campaign. At that time, 8 investigation flags were open. Some fixes were fresh. The network layer was stable but not yet proven under sustained development pressure.

Since then: **164 commits merged to main as v0.3.0.** Every open flag resolved.

What got resolved:
- **Gateway detection** shipped: the daemon now detects switches between two cellular hotspots (both private IPv4, no visible IP change) by watching the default gateway.
- **VPN tunnel detection** shipped: the daemon recognizes tunnel interface patterns and fires a network change event when a VPN connects or disconnects.
- **TCP readiness probe** shipped: before attempting a LAN connection after a WiFi switch, the daemon probes TCP reachability first. This prevents the networking library from caching a stale error during the settling period.
- The **"relay disabled" message** that appeared during some transitions stopped reproducing after the cumulative fixes.

What was built on top of this foundation:
- **Plugin architecture** (43 attack vectors analyzed, all mitigated). The network layer survived it.
- **Grant system** with macaroon capability tokens (per-peer, time-limited, delegatable access). The network layer survived it.
- **Per-peer bandwidth budgets** (per-peer transfer limits with LAN exemption). The network layer survived it.
- **Relay-first onboarding** (new peers connect and operate immediately through the relay). The network layer survived it.

The point: when the foundation holds, everything built on top of it holds too. That is the payoff of solving the hard problem first.

## What this means

![A mesh of interconnected nodes with AI agents, servers, phones, IoT, and laptops: infrastructure that agents can depend on](/images/blog/network-breaks-what-this-means.svg)

Shurli is building infrastructure for a world where AI agents operate autonomously on peer-to-peer networks. Not chatbots calling cloud APIs. Agents running on your devices, talking directly to each other, managing real tasks with real data.

That world needs a network layer that never breaks. Not one that works 95% of the time. Not one that recovers "usually." One that handles every network transition, every edge case, every platform quirk, without any human touching anything.

Nobody is building this with this level of focus. You can piece together a quick P2P solution, and it will work on a demo stage with stable WiFi. But the moment real users walk between real networks with real VPNs and real carrier NATs, it breaks. Finding and fixing those breaks is the work nobody wants to do. It is tedious, platform-specific, and invisible when it works.

Until now, self-hosting was mostly for tech enthusiasts who cared about their data, privacy, and sovereignty. That is still true. But before [OpenClaw](https://openclaw.ai/), there was no real demand for a network layer specifically crafted for AI agents. OpenClaw changed that. Self-hosted personal AI agents are real, they are running on real hardware, and they need to talk to each other across real networks.

This is only going to accelerate. The demand for self-hosted tools around personal AI agents will grow over the coming months and years. More agents, more devices, more networks, more transitions. Shurli is filling the gap it sees in that space: a peer-to-peer networking layer built from the ground up with AI agents as the first user base. Not retrofitted. Not bolted on. The entire architecture, from network resilience to grant-based access control to plugin isolation, exists because agents need it.

Shurli does the tedious, platform-specific, invisible work that makes this possible. Not because it is exciting, but because it is necessary. AI agents deserve infrastructure as reliable as the cloud, without the cloud. Humans get that same reliability as a consequence: a tool that just works, on every network, without restarts, without manual intervention.

The network layer is one piece of a larger system. The [grant system](/blog/who-gets-in/) controls who can access what. The plugin architecture makes it extensible. The file transfer pipeline moves data reliably. Together, they form the foundation for an AI-native network where zero humans are required to operate it. Slowly, continuously heading toward the Zero-Human Network vision.

16 network breaks. Zero restarts. That is the standard.

---

*Built with [Claude Code](https://claude.com/claude-code) by Anthropic - intent-based development where the direction is the hard part, and the code follows. [Read more about the philosophy](/blog/how-we-build-shurli/).*
