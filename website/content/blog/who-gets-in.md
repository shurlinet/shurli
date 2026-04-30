---
title: "Who Gets In: Building Per-Peer Access Control with Macaroon Tokens"
date: 2026-03-22
tags: [release, security, architecture, grants, macaroons]
image: /images/blog/grants-hero.png
description: "How Shurli controls exactly who can access what, for how long, with cryptographic capability tokens that can be delegated but never widened."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![A key with layered rings representing attenuated permissions - each ring can only shrink, never grow](/images/blog/grants-hero.svg)

## The problem with "authorized" versus "allowed"

![Binary authorization vs granular capability grants - binary gives everything or nothing, capabilities give scoped, time-limited, delegatable access](/images/blog/grants-authorized-vs-allowed.svg)

Most P2P tools have a binary model: either a peer is authorized, or they're not. Once authorized, they can access everything - every service, every share, indefinitely. Revoking access means removing them entirely.

This works for a network of two people. It stops working the moment a third person connects.

What an admin actually needs:
- "You can browse files for 1 hour, but not download."
- "You can transfer data for 7 days, then it expires automatically."
- "You can give limited access to someone I haven't met."

These are **capability grants** - time-limited, service-scoped, delegatable permissions that the holder carries as a cryptographic token.

## Why relay ACLs are not enough

The first external user connection to Shurli exposed something fundamental. The relay controlled data circuit access with a binary flag: on or off, per peer, forever.

![Two paths between peers: one through a strict relay (blocked), one through a permissive relay (allowed). The node is the only point that sees both.](/images/blog/grants-relay-bypass.svg)

In a multi-relay topology, a peer can connect through any relay it has access to. A strict relay blocks the data circuit. A permissive relay allows it. The attacker routes through the permissive one.

This is not a relay bug. It is an architectural constraint: **no relay can know about all other relays.** The only enforcement point that sees all traffic, regardless of which relay carried it, is the node itself.

This single finding drove the entire grant system design: the node is the security boundary, not the relay.

## Macaroon tokens: the long route

![HMAC chain building: each caveat adds a new signature link, removing any caveat breaks the chain](/images/blog/grants-macaroon-chain.svg)

Two design approaches:
1. Add `relay_data_until=<timestamp>` to the authorized_keys file. Quick. ACL-based. Fragile.
2. Use macaroon capability tokens with cryptographic integrity. Longer. Capability-based. Sound.

The difference matters at scale. ACL entries sit in a single file - one point of compromise. Capability tokens travel with the holder and are verified mathematically.

A macaroon is an HMAC-chain bearer token. Every caveat (restriction) in the chain produces a new HMAC-SHA256 signature. Removing any caveat breaks the chain. A holder can add restrictions (shorter duration, fewer services, narrower scope) but can never widen permissions. This is enforced by mathematics, not policy.

```
Token creation:
  root_key -> HMAC(id) -> sig_0
  sig_0 + "peer_id=B" -> HMAC -> sig_1
  sig_1 + "expires=2026-03-22T14:00:00Z" -> HMAC -> sig_2
  sig_2 + "service=file-browse,file-download" -> HMAC -> sig_3

Verification:
  Rebuild the HMAC chain from root_key.
  If sig_3 matches, every caveat is intact.
  No network calls. No database. Pure math.
```

Thirteen caveat types are supported: peer identity, service scope, group scope, action scope, delegation depth, expiry, network scope, delegation target, auto-refresh policy, and refresh budget. Each one narrows the token's power.

## Two stores, two perspectives

The grant system has two sides: the admin who issues grants (GrantStore), and the peer who holds them (GrantPouch). These are separate data structures with separate persistence files, separate HMAC keys, and separate background loops.

![Issuer side (GrantStore) creates tokens and delivers via P2P. Holder side (Pouch) stores tokens and presents them on every stream.](/images/blog/grants-dual-store.svg)

**GrantStore** (issuer): keyed by grantee peer ID. Creates macaroon tokens, persists to disk with HMAC integrity and a monotonic version counter (prevents replay attacks via file restore). Auto-cleans expired entries. Fires callbacks on every mutation: P2P delivery, connection closure, notification events, audit log entries.

**GrantPouch** (holder): keyed by issuing node's peer ID. Stores received tokens, serves them for stream-level presentation. Background refresh loop requests fresh tokens at 10% remaining duration. Delegation support creates attenuated sub-tokens.

Why not a single store? Because "grants I've issued" and "tokens I hold" have different access patterns, different keys, and different lifecycle needs. Compromising one doesn't affect the other.

## Every stream verifies independently

![Binary grant header: 4 bytes flow into HMAC chain verification, three outcomes - allow, deny (constant-time), or fallback to local store](/images/blog/grants-stream-verify.svg)

A binary header on every plugin stream open:

```
Byte 0: Version (0x01)
Byte 1: Flags  (0x01 = has token, 0x00 = no token)
Bytes 2-3: Token length (uint16 big-endian)
Bytes 4-N: Macaroon token
```

4 bytes overhead when no token. The holder presents their macaroon; the node verifies the full HMAC chain with its root key. No file reads. No database queries. Pure cryptographic verification on the hot path.

This is per-stream, not per-connection. A peer granted `file-browse` cannot open a `file-download` stream by recompiling their client. The stream handler checks independently.[^1]

[^1]: Security thought experiment finding C1: service restrictions checked only at connection time are bypassable by modified clients. Per-stream verification closes this gap.

**Constant-time rejection**: All deny paths (no token, expired token, forged token) perform the same HMAC work. An attacker probing grant state learns nothing from timing differences.

## Token delivery without touching the relay

![P2P grant delivery: issuer sends token directly to holder via /shurli/grant/1.0.0 protocol, offline queue flushes on reconnect](/images/blog/grants-token-delivery.svg)

Tokens travel directly between nodes via a P2P protocol (`/shurli/grant/1.0.0`). The relay never touches grant tokens - sovereignty preserved.

Four message types: token delivery, revocation notice, acknowledgement, and refresh request. If the peer is offline, the granting node queues the delivery locally and flushes it when the peer reconnects.

Rate limited at two layers: 5 messages per minute per peer at the protocol level, 10 operations per minute per peer at the store level. Queue depth capped at 100 items. Payload capped at 8 KB.

Revocations also travel via P2P. When an admin revokes a grant, the holder's pouch is proactively cleared and all connections from that peer are terminated immediately. Active relay circuits and open streams are closed.

## Delegation: permissions that flow

An admin grants Peer B with `--delegate 3`. Peer B can create a sub-token for Peer C with reduced permissions. Peer C presents the sub-token to the admin's node. The admin's node verifies the full HMAC chain and accepts it - even though the admin never explicitly granted Peer C.

Three delegation modes:
- **Disabled** (default): token is locked to the granted peer
- **Limited**: up to N further hops, each delegation decrements the counter
- **Unlimited**: free re-sharing (still attenuation-only - no widening)

![Delegation chain: Admin grants B (3 hops), B delegates to C (2 hops), C delegates to D (1 hop). Each hop can only narrow.](/images/blog/grants-delegation.svg)

The `delegate_to` caveat chain forms an audit trail. The original `peer_id` identifies who was first granted. Each `delegate_to` identifies subsequent holders. The admin can see the full chain by inspecting the token.

This is the same pattern as object capabilities in the Spritely OCapN protocol. When Shurli adopts OCapN as a wire protocol, these tokens are already in the right shape.

## Notifications: who needs to know

![Notification subsystem: grant events flow through router to LogSink (always on), DesktopSink (macOS/Linux), and WebhookSink (AI agents, bots)](/images/blog/grants-notifications.svg)

Grant lifecycle events route through a notification subsystem with three built-in channels:

**Log sink** (always on): structured log output for every event. This is the audit trail that log aggregators and AI agents consume. Cannot be disabled.

**Desktop sink**: native OS notifications (macOS, Linux). Auto-disabled on headless servers. Peer name in the title bar, not buried in the message body.

**Webhook sink**: HTTP POST with JSON payload, configurable auth headers, event filter, retry with exponential backoff. This is the universal integration point - messaging bots, automation platforms, AI agent endpoints all consume webhooks.

Eight event types: grant created, expiring (pre-expiry warning), expired, revoked, extended, refreshed, rate-limited, and test. Pre-expiry warnings fire at a configurable threshold (default: 10 minutes before expiry) with dedup to prevent notification spam.

The AI agent integration pattern: webhook delivers structured JSON event. Agent receives it, decides to extend the grant, sends `shurli auth extend <peer> --duration 2h --json`. The grant audit trail captures both the notification and the response. No special AI API needed - the webhook + CLI is the integration surface.

## Tamper-evident audit trail

![Integrity-chained audit log: each entry's HMAC covers the previous entry's hash, tampering one entry breaks the entire downstream chain](/images/blog/grants-audit-trail.svg)

Every grant operation (create, revoke, extend, refresh, expire) is recorded in an integrity-chained audit log. Each entry includes an HMAC-SHA256 commitment to the previous entry's hash plus the current entry data. Breaking the chain at any point is detectable.

`shurli auth audit --verify` walks the full chain and reports any tampering. `--tail 20` shows the last 20 entries. Works without the daemon running.

The audit log uses a separate HMAC key (derived via HKDF). Symlink rejection prevents redirecting writes. Write + fsync completes before updating the in-memory chain state, so a crash mid-write leaves a consistent (possibly one-entry-short) chain rather than a corrupted one.

## 20 attack vectors, all mitigated

![20 attack vectors across 5 categories: filesystem, race conditions, protocol, info leaks, design - all analyzed and mitigated before code was written](/images/blog/grants-attack-vectors.svg)

Before writing any code, a security thought experiment analyzed 20 attack vectors across 5 categories:

**Filesystem attacks** (4 vectors): Grant store tampering, symlink races, inotify write-reload-restore, expired grant replay via file restore. Mitigated by HMAC integrity, symlink rejection, monotonic version counter.

**Race conditions** (4 vectors): Transfer outliving grant expiry, concurrent modification, cleanup-vs-extension race, rapid grant-revoke cycling. Mitigated by 30-second re-verify during transfers, in-memory mutex, per-peer ops rate limiter.

**Protocol attacks** (4 vectors): Service restriction bypass via modified client, multi-relay bypass (the critical finding), circuit reuse after revocation, protocol downgrade. Mitigated by per-stream verification, node-level enforcement, ClosePeer on revoke, fail-closed defaults.

**Information leaks** (4 vectors): Timing oracles on grant state, log injection via peer name, log tampering, browse metadata exposure. Mitigated by constant-time rejection, structured logging, integrity-chained audit, separate browse/download grants.

**Design vulnerabilities** (4 vectors): Confused deputy (share list implying grant), ACL vs capability model, admin notification gap, permission fatigue. Mitigated by explicit grant-share separation, macaroon tokens, notification subsystem, short default duration with confirmation for permanent grants.

The critical finding (multi-relay bypass) is the reason the entire grant system exists at the node level. Every other design decision flows from that constraint.

## What this means

![A mesh of nodes with tokens orbiting between them: narrowable, offline-verified, P2P-delivered, delegatable](/images/blog/grants-what-this-means.svg)

Shurli peers now carry cryptographic proof of what they're allowed to do. That proof can be narrowed but never widened, verified offline with just a root key, delivered directly between peers without touching any relay, and traced through multi-hop delegation chains.

The system is designed for a future where AI agents grant, extend, revoke, and monitor access autonomously - the CLI's `--json` mode and webhook notifications make every operation machine-consumable. The admin sets policy; the network enforces it.

This is one layer of what a self-sovereign P2P network needs. The grant system joins the plugin architecture, file transfer hardening, and zero-knowledge anonymous auth as foundation pieces. Each one adds value independently. Together, they form the infrastructure for an AI-native network where zero humans are required to operate it.

---

*Built with [Claude Code](https://claude.com/claude-code) by Anthropic - intent-based development where the direction is the hard part, and the code follows. [Read more about the philosophy](/blog/how-we-build-shurli/).*
