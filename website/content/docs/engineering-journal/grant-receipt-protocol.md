---
title: "Phase 9 - Grant Receipt Protocol"
weight: 28
description: "Relay-issued receipts with session budgets, client-side grant cache, smart pre-transfer checks, per-chunk circuit byte tracking, smart reconnection."
---
<!-- Auto-synced from docs/engineering-journal/grant-receipt-protocol.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Complete (4 batches + physical retest + docs) |
| **Phase** | 9 (Plugins, SDK & First Plugins) |
| **ADRs** | ADR-V01 to ADR-V05 |

The relay circuit investigation (2026-03-26) revealed that seed relays correctly denied large transfers (64 MB session limit), but clients had no way to know this before attempting the transfer. The Grant Receipt Protocol gives clients pre-transfer visibility into relay budgets, enabling smart transfer decisions.

---

## ADR-V01: Relay-Issued Grant Receipts

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |

### Context

After RC1 (tier-aware session limits) and RC2 (receiver busy retry) were fixed, a fundamental visibility gap remained: clients connecting through relays had no knowledge of the relay's session data limit, session duration, or grant expiry. A 174 MB file routed through a seed relay with a 64 MB limit would fail after consuming bandwidth. The client needed to know the limits before starting.

### Decision

Relays issue a **Grant Receipt** to clients upon circuit establishment. The receipt is a 62-byte binary message containing session parameters, signed with HMAC-SHA256.

Wire format:
```
Byte 0:     Version (0x01)
Bytes 1-8:  Grant duration (seconds, uint64 big-endian, 0=permanent)
Bytes 9-16: Session data limit (bytes, uint64 big-endian, 0=unlimited)
Bytes 17-20: Session duration (seconds, uint32 big-endian)
Byte 21:   Permanent flag (0x00/0x01)
Bytes 22-29: Issued-at timestamp (Unix seconds, uint64 big-endian)
Bytes 30-61: HMAC-SHA256 over canonical payload
```

Protocol ID: `/shurli/grant-receipt/1.0.0`

### Consequences

- Clients receive relay budget information at circuit setup time, not after transfer failure
- 62 bytes of overhead per circuit establishment (negligible)
- HMAC prevents receipt forgery (client cannot inflate budgets)
- Issued-at timestamp enables clock drift detection between relay and client

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/grant_receipt.go`

---

## ADR-V02: Client-Side Grant Cache

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |

### Context

Grant receipts arrive at circuit establishment. The client needs to persist this information across reconnections and make it available to the transfer layer for pre-transfer checks.

### Decision

`GrantCache` (`https://github.com/shurlinet/shurli/blob/main/internal/grants/cache.go`) stores receipts keyed by relay peer ID with:
- Thread-safe map with per-receipt fields: grant duration, session data limit, session duration, permanent flag, timestamps
- JSON persistence to disk with HMAC integrity and symlink rejection
- Max cache file size: 1 MB (DoS defense)
- Per-circuit tracking fields (not persisted): `CircuitBytesSent`, `CircuitBytesReceived`, `CircuitStartedAt`
- Background cleanup goroutine removes expired entries
- Revocation handling with issued-at ordering (rejects stale revocations)

### Consequences

- Transfer layer can check budgets without network calls
- Circuit byte counters reset on each new circuit (fresh session budget)
- Overflow clamping (MaxInt64) prevents integer overflow on long-lived circuits
- Cache survives daemon restarts; circuit counters intentionally do not (fresh session on restart)

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/grants/cache.go`

---

## ADR-V03: Smart Pre-Transfer Check

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |

### Context

Clients should not attempt transfers that will exceed the relay's session budget. Wasting bandwidth on a transfer that will be rejected at 64 MB of a 174 MB file is unacceptable.

### Decision

`checkRelayGrant()` (`https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`) runs before every relay transfer:

1. Extract relay peer ID from the circuit multiaddr
2. Query grant cache for receipt: `GrantStatus(relayID)`
3. Check budget: `HasSufficientBudget(relayID, fileSize, direction)`
4. Estimate transfer time at conservative 200 KB/s
5. Verify grant remaining time covers the estimated transfer duration
6. Verify session duration covers the estimated transfer duration (transfer must fit in one session)

Returns a `relayTransferInfo` struct with: `IsRelayed`, `RelayPeerID`, `GrantActive`, `GrantRemaining`, `SessionBudget`, `SessionDuration`, `BudgetOK`, `TimeOK`.

### Consequences

- Transfers that would exceed relay budget are blocked before any data flows
- User sees clear error: "file size (X) exceeds relay session limit (Y)" for budget failures
- Conservative 200 KB/s estimate means the check errs on the side of caution
- Session duration check (H11) ensures the transfer fits within a single circuit session, not just the grant lifetime

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`

---

## ADR-V04: Per-Chunk Circuit Byte Tracking

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |

### Context

Pre-transfer checks validate the total file size against the budget, but the actual bytes on the wire differ from file size due to compression, protocol overhead, and chunking. Accurate budget tracking requires counting actual bytes written to the relay circuit.

### Decision

`makeChunkTracker()` (`https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`) creates a callback function for relayed streams:

1. For direct connections: returns nil (no tracking needed)
2. For relayed streams: extracts relay peer ID from circuit multiaddr
3. Returns closure that calls `TrackCircuitBytes(relayID, direction, bytesOnWire)` after each chunk write
4. Called inside `addWireBytes()` in the transfer progress tracker, outside the mutex

The progress tracker calls `tracker(n)` for every chunk frame written, counting compressed bytes (actual wire usage, not original file size).

### Consequences

- Budget tracking reflects actual bandwidth consumption, not file size
- Compression savings are correctly reflected in budget usage
- Tracking happens at the chunk level, not file level, so budget overruns are caught within one chunk of the limit
- Zero overhead for direct connections (nil tracker)

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer.go`

---

## ADR-V05: Smart Reconnection with App-Error Exclusion

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |

### Context

Relay sessions expire. When a transfer fails mid-stream due to session expiry, the client should retry with a fresh circuit. But not all failures are session expiry. Application-level errors (peer rejected, file too large, disk space, access denied) should not trigger reconnection because the retry would also fail.

### Decision

`isRelaySessionExpiry()` (`https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`) classifies transfer errors:

**Do not retry** (application errors):
- "rejected", "file too large", "disk space"
- "open file", "stat file", "chunk file" (local I/O)
- "cancelled", "grant expires", "access denied"

**Retry** (likely session expiry):
- Grant is still active AND error is transport-level

Reconnection flow:
1. Increment `job.relayReconnects` (max 5 attempts)
2. Calculate exponential backoff: 2s, 4s, 8s, 16s, 32s
3. Call `ResetCircuitCounters(relayID)` for fresh session budget
4. Requeue job with "relay-reconnecting" status

### Consequences

- Transport failures get automatic retry with fresh session budget
- Application errors fail immediately without wasting relay bandwidth
- Exponential backoff prevents rapid circuit churn
- Budget counters reset per circuit (each session gets its own budget)
- Max 5 reconnection attempts prevents infinite loops

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer_grants.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/transfer.go`
