# Phase 9: Relay Circuit Investigation

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Complete (3 bugs found, 2 fixed, 1 superseded by Grant Receipt Protocol) |
| **Phase** | 9 (Plugins, SDK & First Plugins) |
| **ADRs** | ADR-W01 to ADR-W03 |

During file transfer physical retesting (2026-03-25), relay circuit transfers failed intermittently. Investigation revealed three distinct issues, none of which were protocol bugs. The root causes were configuration defaults, missing retry logic, and a relay selection gap.

---

## ADR-W01: Tier-Aware Session Limit Defaults (RC1)

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |
| **Commit** | 572d086 |

### Context

Seed relays (public, shared, `enable_data_relay: false` by default) and self-hosted relays (private, admin-controlled) were using identical session defaults: 64 MB data limit, 10-minute session duration. A 174 MB file transfer through a self-hosted relay failed at 64 MB because the self-hosted relay inherited seed relay limits.

This is not a bug in the relay protocol. The relay correctly enforced its configured limit. The problem was that the default configuration did not distinguish between seed relays (which should be conservative) and self-hosted relays (which should be generous).

### Decision

Two tiers of session defaults:

| Parameter | Seed relay | Self-hosted relay |
|-----------|-----------|-------------------|
| Session data limit | 64 MB | 2 GB |
| Session duration | 10 minutes | 2 hours |

Detection: if `enable_data_relay` is false (seed relay behavior), use conservative defaults. If true (self-hosted relay), use generous defaults. Explicit config values always override tier defaults.

### Consequences

- Self-hosted relays now support large file transfers out of the box
- Seed relays remain conservative (shared resource protection)
- Existing explicit configs are not affected
- Relay operators who want custom limits can still set them

**Reference**: `internal/relay/`

---

## ADR-W02: Receiver Busy Retry with Exponential Backoff (RC2)

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Accepted |
| **Commit** | 572d086 |

### Context

When a receiver was processing another transfer, the sender got a "receiver busy" rejection. The transfer queue treated this as a permanent failure and dropped the job. For relay transfers (where reconnection is expensive), this was especially wasteful.

### Decision

The transfer queue now distinguishes "receiver busy" from other rejections:

- **Receiver busy**: transient, retryable. Requeue with exponential backoff (2s, 4s, 8s, 16s, 32s, max 5 attempts)
- **Other rejections**: permanent, fail immediately

`Requeue()` method moves active jobs back to the pending queue with updated retry state.

### Consequences

- Busy peers get automatic retry without user intervention
- Exponential backoff prevents hammering the receiver
- Max 5 attempts prevents infinite retry loops
- Works for both direct and relayed transfers

**Reference**: `plugins/filetransfer/transfer.go`

---

## ADR-W03: Seed Relay Churn and Budget-Aware Selection (RC3)

| | |
|---|---|
| **Date** | 2026-03-26 |
| **Status** | Superseded by Grant Receipt Protocol (ADR-V01 to ADR-V05) |

### Context

The original 0.4-second circuit drop observed during testing was caused by transfers routed through seed-only relays. The seed relay correctly denied the data circuit (ACL enforcement), and the circuit was torn down. This was not a bug. The issue was that PeerManager picks the first available circuit, not the best one for the transfer.

A 174 MB file routed through a seed relay with a 64 MB session limit will always fail. The client needs to select a relay with sufficient budget.

### Decision

This issue was superseded by the Grant Receipt Protocol (Batches 1-4). The Grant Receipt Protocol provides:

1. **Client-side visibility**: clients know each relay's session budget via cached receipts
2. **Pre-transfer checks**: transfers blocked before wasting bandwidth on an insufficient relay
3. **Smart error messages**: "file size (174 MB) exceeds relay session limit (64 MB)" instead of a generic circuit failure

Budget-aware relay *selection* (choosing the best relay before dialing) is tracked as FT-Y #7 for post-merge optimization. The current approach is: try the relay, check the budget pre-transfer, fail fast with a clear message if insufficient. The next step is: check all cached receipts first, select the relay with sufficient budget, then dial.

### Consequences

- The immediate problem (wasted bandwidth on insufficient relays) is solved by pre-transfer checks
- The optimization (proactive relay selection) is deferred to FT-Y #7
- No relay protocol changes were needed; the solution is entirely client-side

**Reference**: `internal/grants/cache.go`, `plugins/filetransfer/transfer_grants.go`
