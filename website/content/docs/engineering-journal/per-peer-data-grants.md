---
title: "Phase 8B: Per-Peer Data Grants"
weight: 26
description: "Macaroon capability tokens for per-peer, time-limited access control. Node-level enforcement, delegation, notifications, tamper-evident audit log."
---
<!-- Auto-synced from docs/engineering-journal/per-peer-data-grants.md by sync-docs - do not edit directly -->


**Date**: 2026-03-22
**Status**: Complete (Phases A, R, B, C, D)
**ADRs**: ADR-T01 to ADR-T10

The first external user session (2026-03-15) exposed a fundamental flaw: relay-level ACLs (`relay_data=true`) are binary, permanent, and - critically - bypassable in multi-relay topologies. This journal documents the architecture decisions behind the per-peer data access grant system that replaced it.

---

## ADR-T01: Node-Level Enforcement as Primary Security Boundary

**Date**: 2026-03-16
**Status**: Accepted

### Context

Security thought experiment finding C2 (CRITICAL): In a multi-relay topology, relay ACLs alone are insufficient. Peer B connects to relay A (strict ACL) and relay B (permissive). Data circuits route through relay B, bypassing relay A's restrictions entirely.

### Decision

The NODE is the only enforcement point that sees all traffic regardless of relay path. Grant verification happens at stream open time in `OpenPluginStream` (outbound) and `handleServiceStreamInner` (inbound). Relay ACLs become defense-in-depth only.

### Consequences

- Every plugin stream verifies grants independently (no connection-level caching)
- Node can operate with any number of relays, none of which need grant awareness
- Relay ACLs can be relaxed without compromising node security

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/service.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/network.go`

---

## ADR-T02: Macaroon Tokens Over ACL Attributes

**Date**: 2026-03-16
**Status**: Accepted

### Context

Two approaches: (1) Add `relay_data_until=<timestamp>` to authorized_keys attributes (quick, ACL-based). (2) Use macaroon capability tokens with cryptographic integrity (longer, capability-based).

Research finding: Storj, Ceramic, and p2panda all moved from ACL to capability tokens. ACLs centralize control in a single file; capability tokens push security to the edge. OCapN (Spritely) standardizes inter-node capability delegation over libp2p.

Design principle: take the long route instead of shortcuts.

### Decision

Macaroon capability tokens. No interim ACL approach.

Key properties:
- **Attenuation-only**: holders can narrow permissions, never widen (HMAC chain enforces)
- **Offline verification**: any party with root key can verify (no network calls, no DB queries)
- **OCapN-compatible**: tokens move between nodes like capabilities in the Spritely protocol

### Consequences

- More code than ACL attributes, but cryptographically sound
- Foundation for broader ACL-to-macaroon migration across all 5 authorization layers
- Delegation becomes possible (Phase B3) - impossible with flat file attributes

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/macaroon.go`

---

## ADR-T03: Dual-Store Architecture (GrantStore + GrantPouch)

**Date**: 2026-03-20
**Status**: Accepted

### Context

The grant system has two perspectives: the issuer who creates grants, and the holder who receives them. These need different data models, different keys, and different persistence.

### Decision

Two separate stores:

| Store | Keyed By | Purpose | File |
|-------|----------|---------|------|
| `GrantStore` | Grantee peer ID | Issuer-side grant management | `grants.json` |
| `GrantPouch` | Issuer peer ID | Holder-side token storage | `grant_pouch.json` |

Both use HMAC-SHA256 integrity (separate HKDF sub-keys: `shurli/grants/store/v1` and `shurli/grants/pouch/v1`). Both have background cleanup loops for expired entries.

### Why Not a Single Store

A single store would conflate "grants I've issued" with "tokens I hold." Different keys mean different access patterns. The pouch also needs a background refresh loop (Phase B4) that has no analogue in the store.

### Consequences

- Clear separation of concerns
- Each file has independent integrity (compromising one doesn't affect the other)
- Monotonic version counter in GrantStore prevents expired-grant replay via file restore

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/grants/store.go`, `https://github.com/shurlinet/shurli/blob/main/internal/grants/pouch.go`

---

## ADR-T04: Binary Grant Header on Plugin Streams

**Date**: 2026-03-21
**Status**: Accepted

### Context

Phase A verified grants via local store lookup (issuer checks its own GrantStore). Phase B flips the model: the holder presents the token, the issuer verifies cryptographically. This requires a header on every plugin stream.

### Decision

4-byte binary header on every plugin stream open:

```
Byte 0: Version (0x01)
Byte 1: Flags (0x01 = has token, 0x00 = no token)
Bytes 2-3: Token length (uint16 big-endian)
Bytes 4-N: Base64-encoded macaroon token
```

Binary (not JSON) because this is the hot path. Stack-allocated 4-byte fast path when no token. 2-second read/write deadline prevents slowloris.

### Alternatives Considered

- **JSON envelope**: Simpler parsing, but allocates on every stream (heap). Binary is 4 bytes on the common path (no token on LAN).
- **Token in connection metadata**: Would require re-verification on every stream anyway (C1 mitigation). Per-stream is correct.
- **Negotiate via protocol version**: Protocol IDs are fixed at registration time. Dynamic per-stream behavior needs headers.

### Consequences

- 4 bytes overhead on every plugin stream (negligible vs typical transfer payload)
- Token-from-header verification is pure math (no disk I/O) - the Phase B performance win
- Constant-time HMAC work on all paths (valid, invalid, malformed) prevents timing oracles (D1 mitigation)

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/grant_header.go`

---

## ADR-T05: P2P Grant Delivery Protocol

**Date**: 2026-03-21
**Status**: Accepted

### Context

Tokens need to reach the holder. Three options: (1) Out-of-band (email, messaging). (2) Via relay storage. (3) Direct P2P delivery.

Design principle: relay never touches grant tokens. Sovereignty preserved.

### Decision

P2P protocol `/shurli/grant/1.0.0` with 4 message types: deliver, revoke, ack, refresh. Binary type byte + uint32 length + JSON payload. Max 8192 bytes, 10-second timeout.

Offline queue on the granting node (not relay). Flushed on peer reconnect event. Queue TTL configurable (default 7 days), max 100 items.

Trust check: only accept deliveries from peers in authorized_keys (prevents random nodes pushing tokens).

### Defense Layers

- Protocol-level rate limit: 5 deliveries per minute per peer
- Store-level rate limit: 10 operations per minute per peer (Phase D3)
- Payload size cap: 8192 bytes
- Queue depth cap: 100 items total

### Consequences

- No relay involvement in token lifecycle (sovereignty)
- Offline peers eventually receive tokens (queue + reconnect flush)
- Revocations also delivered via P2P (holder's pouch is cleared proactively)

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/grants/protocol.go`, `https://github.com/shurlinet/shurli/blob/main/internal/grants/delivery_queue.go`

---

## ADR-T06: Multi-Hop Delegation via Caveat Attenuation

**Date**: 2026-03-22
**Status**: Accepted

### Context

Admin grants Peer B. Peer B wants to give Peer C limited access. Without delegation, every grant requires the admin.

### Decision

Macaroon attenuation makes this straightforward:

1. Peer B has token T with caveats `[peer_id=B, max_delegations=5]`
2. Peer B clones T, adds `delegate_to=C` + `max_delegations=4`
3. Sub-token T' has all of T's caveats plus the new restrictions
4. Peer C presents T' to admin's node. HMAC chain verifies with root key.

Three modes: disabled (default, `--delegate 0`), limited (`--delegate N`), unlimited (`--delegate unlimited`).

The `delegate_to` caveat chain forms an audit trail. Original `peer_id` identifies the first grantee; each `delegate_to` identifies subsequent holders.

### Bug Fixed

Zero-duration delegation: when delegating without `--duration`, the duration defaults to zero. Zero `time.Time` formats as year-0001 (instant expiry). Fix: `ExtractEarliestExpires()` extracts parent's expiry. Zero duration inherits remaining parent lifetime.

### Consequences

- Admin doesn't need to know about every downstream holder
- Delegated tokens can only be narrowed (shorter duration, fewer services, fewer hops)
- OCapN-compatible: tokens are capabilities that flow through the network

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/caveat.go`, `https://github.com/shurlinet/shurli/blob/main/internal/grants/pouch.go` (Delegate method)

---

## ADR-T07: Notification Subsystem (Sink Pattern)

**Date**: 2026-03-22
**Status**: Accepted

### Context

Admins need to know when grants expire. Three requirements: (1) Works on desktop (macOS/Linux), (2) Works on headless servers, (3) AI agents can consume events.

### Decision

`NotificationSink` interface with a non-blocking router:

```go
type NotificationSink interface {
    Name() string
    Notify(event Event) error
}
```

Three built-in sinks:
- **LogSink**: Always on. Structured slog output. Cannot be disabled. This is the audit trail that log aggregators and AI agents consume.
- **DesktopSink**: macOS (`osascript`), Linux (`notify-send`). Auto-disabled on headless.
- **WebhookSink**: HTTP POST JSON. Retry with exponential backoff. Configurable auth headers and event filter.

Router dispatches events to all sinks in goroutines. Sink failures/panics are logged but never propagate. Event dedup via ID (5-minute TTL). Pre-expiry warnings via background ticker.

### Security

AppleScript injection via unsanitized peer name in DesktopSink. Both title and body are sanitized before passing to `osascript`.

### Consequences

- WebhookSink is the universal integration point (Slack, Telegram, n8n, AI agent endpoints)
- Future mobile push sink fits the same interface (P2P delivery, no APNs/FCM)
- AI agents receive structured JSON, act via `shurli auth extend --json`, audit trail captures both

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/notify/`

---

## ADR-T08: Integrity-Chained Audit Log

**Date**: 2026-03-22
**Status**: Accepted

### Context

Thought experiment finding D3: attacker with filesystem access deletes or modifies audit logs to hide data access history.

### Decision

Append-only log with HMAC-SHA256 chain. Each entry: `HMAC(key, prev_hash + entry_data)`. Tampering at any point breaks the chain and is detected by `shurli auth audit --verify`.

Separate file (`grant_audit.log`), separate HKDF sub-key (`shurli/grants/audit/v1`). Symlink rejection on write. Write + fsync before updating in-memory chain state (crash safety).

### Consequences

- Tamper-evident (not tamper-proof - an attacker with the HMAC key can rebuild the chain)
- Defense: identity key on separate storage (Yubikey) makes key extraction hard
- `shurli auth audit --tail 20` shows recent activity. `--verify` validates full chain.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/grants/audit.go`

---

## ADR-T09: Per-Peer Ops Rate Limiter

**Date**: 2026-03-22
**Status**: Accepted

### Context

Thought experiment finding B4: rapid grant-revoke cycling creates confused state in the daemon. Protocol-level rate limit (5/min) catches P2P spam but not local CLI abuse.

### Decision

10 operations per minute per peer (configurable). Applied to ALL Store mutations: Grant, Revoke, Extend, Refresh, UpdateMaxRefreshes. Fires `grant_rate_limited` notification on first violation per window (once, not spamming the notification channel).

Notification callback called OUTSIDE lock (prevents webhook response time from blocking other rate checks).

### Consequences

- Two-layer rate limiting: protocol (5/min inbound P2P) + store ops (10/min local)
- Rate limit violations are visible in notifications and audit log
- Stale entries pruned when map exceeds 100 peers

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/grants/rate_limit.go`

---

## ADR-T10: Grant-Aware Backoff Reset

**Date**: 2026-03-22
**Status**: Accepted

### Context

After a relay grants data access to a client, the client's swarm may have accumulated exponential backoff timers from previous failed dial attempts (when the grant didn't exist). The client sits in backoff while the relay is ready to serve.

### Decision

Two mechanisms:

1. **Automatic**: Relay sends `/shurli/grant-changed/1.0.0` to client on grant creation. Client calls `OnNetworkChange()` to clear all swarm backoffs.

2. **Manual**: `shurli reconnect <peer> [--json]` clears dial backoff for a specific peer and forces immediate redial. Designed for AI agent control loops.

### Consequences

- Grants take effect immediately (no waiting for backoff timers)
- AI agents can programmatically trigger reconnection after granting access

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/peermanager.go`
