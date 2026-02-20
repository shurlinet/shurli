---
title: "Smarter Connections Under the Hood"
date: 2026-02-14
tags: [release, batch-d]
image: /images/blog/batch-d-libp2p.svg
---

![Smarter Connections Under the Hood](/images/blog/batch-d-libp2p.svg)

## What's new

peer-up now automatically detects whether it's behind NAT, prefers QUIC over TCP for faster connections, and identifies itself to peers with its exact version. These are invisible improvements — your connections just work better.

## Why it matters

Not every network is the same. Some have full IPv6, some are behind CGNAT, some block UDP. peer-up needs to adapt. AutoNAT v2 detects your situation accurately, QUIC gives faster connection establishment when available, and TCP remains the universal fallback.

## Technical highlights

![Transport negotiation priority — QUIC first, TCP fallback, WebSocket for censorship resistance](/images/blog/batch-d-transport-stack.svg)

- **AutoNAT v2**: More reliable NAT detection than v1. Peers probe each other to determine reachability, enabling smarter relay decisions
- **QUIC-preferred transport ordering**: QUIC first (3 RTTs, native multiplexing), TCP second (universal fallback), WebSocket third (anti-censorship). First transport to succeed wins
- **Identify UserAgent**: Every peer sends `peerup/0.1.0` (or `relay-server/0.1.0`) in the libp2p Identify exchange. `peerup daemon peers` filters by this — showing only your network members by default
- **Smart dialing**: Relay circuit addresses are always in the peerstore alongside direct addresses. libp2p's dialer tries both in parallel and picks the fastest

## What's next

New user-facing capabilities: daemon status command, relay health endpoint, and headless invite/join for scripting.
