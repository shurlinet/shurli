# FT-Y: Budget-Aware Relay Selection

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-04 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-BR01 to ADR-BR09 |
| **Primary Commits** | b574033, f8f9c7f, 97109c5, c833728, c78101e, 82b3466 |

Shurli already had health-aware relay ranking and grant receipts. That solved two different problems: which relays looked reachable, and what budget a relay had advertised to the client. The missing FT-Y step was to combine those signals before and during file-transfer relay use, so a transfer would not discover too late that the chosen relay could not carry the payload.

This journal documents that routing decision. It depends on the Grant Receipt Protocol in [ADR-V01 to ADR-V05](grant-receipt-protocol.md), the relay circuit investigation in [ADR-W03](relay-circuit-investigation.md#adr-w03-seed-relay-churn-and-budget-aware-selection-rc3), and relay-side per-peer budget enforcement in `internal/relay/`. It does not replace any of them. Relay enforcement remains authoritative; client-side budget state is an advisory routing signal.

---

## ADR-BR01: Health-Only Relay Selection Was Insufficient For Transfers

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | f8f9c7f |

### Context

Before FT-Y budget-aware relay selection, `RelayDiscovery` could order static and discovered relays by health. Health ranking answers "which relay looks reachable and responsive?" It does not answer "can this relay carry the next transfer?"

[ADR-W03](relay-circuit-investigation.md#adr-w03-seed-relay-churn-and-budget-aware-selection-rc3) documented the old gap. Shurli could fail fast once a transfer opened a relay path and checked the grant receipt, but the relay ordering itself still did not prefer a relay with enough budget.

### Decision

Move relay budget into the relay address ordering used by the dialer. `RelayDiscovery` remains the source of relay addresses, but when it has a grant checker it ranks relays by a composite score instead of static order alone.

This solves a routing problem, not an enforcement problem. The relay still decides whether a circuit is allowed and when its budget is exhausted. The client only uses cached grant state to choose a better first path and to retry away from a low-budget path.

### Alternatives Considered

**Health-only ranking plus pre-transfer failure** kept the implementation simple, but it still selected relays that were known to be too small for the transfer.

**Manual relay ordering** pushed routing decisions to operators and made the configured order matter too much.

**Client-side enforcement** was rejected. A client can be stale, buggy, or malicious. Relay-side enforcement is the only authoritative boundary.

### Consequences

- Transfer routing can prefer a relay that has enough known budget before payload data starts.
- The path dialer does not need file-transfer-specific knowledge; it consumes the ranked relay source.
- Stale cache state can still be wrong, so later checks and relay enforcement remain required.
- Budget-aware routing is backward compatible because relays without cached grants still stay in the candidate list.

### Physical Verification

On 2026-04-03, a relay-only transfer test used two data relays with different cached budgets. The higher-budget relay had a 2 GB grant; the lower-budget relay had a 100 MB grant. The higher-budget relay was selected first. A 1 MB transfer succeeded at about 1022 KB/s. Status after the transfer showed about 1.2 MB used on the selected relay, while the lower-budget relay remained unchanged at 100 MB.

**Reference**: `pkg/sdk/relaydiscovery.go`, `pkg/sdk/pathdialer.go`, `cmd/shurli/cmd_daemon.go`

---

## ADR-BR02: Grant Receipts Became Advisory Routing Signals

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commits** | b574033, f8f9c7f, c78101e |

### Context

The Grant Receipt Protocol already exposed relay grant duration, session data limit, session duration, expiry, and per-circuit byte counters to the client. That information was originally used for pre-transfer checks and status display.

Budget-aware relay selection needed the same information earlier in the path decision. The client did not need a new authority. It needed a read-only signal derived from the existing receipt cache.

### Decision

Use `GrantCache` through the `sdk.RelayGrantChecker` interface:

- `GrantStatus` tells the caller whether a cached grant exists, how long it remains valid, and what budget is visible.
- `HasSufficientBudget` checks the cached budget for a candidate transfer size.
- `TrackCircuitBytes` keeps local usage display current.
- `ResetCircuitCounters` starts a fresh local circuit accounting window after reconnect.

The daemon wires the same cache into two consumers: the file-transfer plugin for transfer pre-flight checks, and `RelayDiscovery` for relay ordering.

### Alternatives Considered

**Duplicate receipt parsing in relay discovery** would split one protocol into two implementations.

**Synchronous relay queries before every transfer** would add a network round trip and still would not replace relay enforcement.

**Trust the cache as authority** was rejected. The cache is a routing hint; the relay is the source of truth.

### Consequences

- Receipt freshness matters for routing, not just for display.
- Existing receipt integrity and expiry logic remain centralized in `internal/grants/cache.go`.
- Relay-side enforcement can reject stale or incorrect client assumptions.
- Client-side missing-grant state is not treated as proof that the relay has no grant.

### Physical Verification

The 2026-04-03 budget reduction test proved why this signal must be advisory. After one relay was reduced from 2 GB to 10 MB while another relay still had 100 MB, the client missed the new receipt and displayed the reduced relay as "no grant." A 1 MB transfer still succeeded because the relay had the actual grant and enforced it server-side. That exposed the receipt freshness bug fixed later in c78101e.

**Reference**: `internal/grants/cache.go`, `pkg/sdk/relay_utils.go`, `plugins/filetransfer/transfer_grants.go`, `cmd/shurli/cmd_daemon.go`

---

## ADR-BR03: Composite Relay Score Uses Health Baseline Plus Budget Bonus

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | f8f9c7f |

### Context

Relay ranking needed to preserve the old health signal while adding budget awareness. A relay with a large grant but poor health should not automatically beat a healthy relay that has no cached grant. At the same time, relays with active grants should be preferred when health is otherwise similar.

### Decision

`RelayDiscovery.SetBudgetChecker` stores a `RelayGrantChecker`. `RelayAddrs()` sorts known relays when either health or budget data is present. The score is:

```text
healthScore                     when no budget checker exists
healthScore                     when no active cached grant exists
healthScore + budgetScore * 0.5 when an active cached grant exists
```

`healthScore` comes from `RelayHealth.Score`, or the default relay score of 0.5 when health data is absent.

`relayBudgetScore` returns:

- `0.0` when no grant exists or the grant is expired.
- `1.0` for unlimited grants.
- `1.0` for budgets at or above 2 GB.
- `budget / 2 GB` below that threshold.

That means a 500 MB active grant adds about 0.122 to the relay score. A 2 GB or unlimited grant adds 0.5.

### Alternatives Considered

**Budget-only ordering** would choose capacity without considering whether the relay is working well.

**Weighted health plus weighted budget for every relay** was tested and rejected because it could penalize healthy no-grant relays below failing granted relays.

**Hard-filter no-grant relays** would break signaling-only deployments and stale-cache recovery.

### Consequences

- Active grants are a bonus, not an absolute override.
- With default health, a 500 MB active grant scores about 0.622 while a no-grant relay scores 0.5.
- With real health data, a healthy no-grant relay can still outrank a failing granted relay. The regression test covers a no-grant relay at 0.95 outranking a 2 GB granted relay at 0.7.
- Relays with no cached grant remain ordered by health and are not demoted below unhealthy relays solely because the cache has no receipt.

### Physical Verification

The 2026-04-03 initial selection test matched the scoring model. With both candidate data relays reachable and active grants cached, the relay with the larger cached budget was selected first. The transfer used that path and left the lower-budget relay unchanged.

**Reference**: `pkg/sdk/relaydiscovery.go`, `pkg/sdk/relaydiscovery_test.go`, `pkg/sdk/relayhealth.go`

---

## ADR-BR04: No Cached Grant Does Not Mean Bad Relay

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-04 |
| **Status** | Accepted |
| **Commits** | f8f9c7f, c78101e |

### Context

A relay can lack a cached grant for several valid reasons:

- It is a signaling-only relay and should still help peers find each other.
- The client has not yet received a fresh receipt.
- A revoke and re-grant sequence temporarily invalidated the local cache.
- The relay grant exists server-side, but client-side receipt delivery failed.

Treating "no cached grant" as "bad relay" would make stale cache state more damaging than it needs to be.

### Decision

Relays without cached grants are scored by health only. They are not assigned a negative budget score. The file-transfer pre-flight check also does not fail a transfer solely because no cached grant exists; it warns that the transfer may fail and lets relay-side enforcement decide.

### Alternatives Considered

**Penalize no-grant relays below all granted relays** would make cached receipt presence more important than health.

**Block all no-grant transfer attempts** would fail stale-cache cases that the relay could still authorize.

**Ignore no-grant relays entirely** would remove useful signaling and reduce compatibility with deployments that do not use relay data grants.

### Consequences

- Existing deployments without grant caches still behave sensibly.
- A missing cached receipt does not prevent a relay-side grant from working.
- A stale or missing receipt may still choose a suboptimal route, which is why receipt re-delivery was fixed.
- Actual data permission remains enforced at the relay and at the destination peer's plugin policy.

### Physical Verification

In the 2026-04-03 stale-cache test, the client displayed one relay as "no grant" after a revoke and re-grant sequence. A 1 MB transfer still succeeded because the relay had an active server-side grant. That confirmed the client must not treat missing cache state as authoritative denial.

**Reference**: `pkg/sdk/relaydiscovery.go`, `plugins/filetransfer/transfer_grants.go`, `internal/grants/cache.go`, `internal/relay/notify.go`

---

## ADR-BR05: Plugin-Layer Pre-Flight Bridges Selection And Enforcement

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-03 |
| **Status** | Accepted |
| **Commits** | b574033, f8f9c7f, 82b3466 |

### Context

Relay discovery can choose a likely relay before dialing, but the actual path is only known after a stream exists. The stream might use direct transport, a relay circuit through the expected relay, or a different relay after retry. File-transfer code therefore needs a second check after stream open and before data transfer begins.

This is the plugin-layer pre-flight. It sits after SDK relay selection and before SHFT payload transfer.

### Decision

`checkRelayGrant` runs against the actual stream:

1. Confirm the stream is relayed with `Conn().Stat().Limited`.
2. Extract the relay peer ID from the circuit multiaddr using `sdk.RelayPeerFromAddr`.
3. Read cached grant state through `GrantStatus`.
4. Compare file or directory size with `HasSufficientBudget`.
5. Estimate transfer time at a conservative 200 KB/s.
6. Check both grant remaining time and circuit session duration.

File sends use `os.Stat` for the file size. Directory sends open a probe stream first, compute the total regular-file size by walking the directory, run the same grant check, close the probe stream, and only then start the real transfer stream.

### Alternatives Considered

**Rely only on `RelayDiscovery` scoring** would miss the actual relay chosen by libp2p after connection reuse or retry.

**Check only relay grant expiry** would allow known oversize transfers to start and fail mid-stream.

**Duplicate SHFT wire logic in the pre-flight** was rejected. This stage only checks path and budget. The transfer protocol remains documented in the streaming protocol journal.

### Consequences

- File and directory sends get the same relay budget and expiry checks.
- Directory sends avoid starting payload data when the total directory size is already known to exceed the cached budget.
- The check is still advisory. Stale client state can be wrong, and relay-side enforcement remains final.
- Transfers with no cached grant warn and continue, preserving stale-cache recovery.

### Physical Verification

The 2026-04-03 file-transfer relay test exercised the pre-flight path on a 1 MB transfer. The dynamic directory-budget branch is verified by current code inspection rather than a separate measured physical run in the available test record.

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_grants.go`, `pkg/sdk/relay_utils.go`, `plugins/filetransfer/plugin.go`

---

## ADR-BR06: Low-Budget Paths Trigger Reconnect Through The Ranked Relay List

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | f8f9c7f |

### Context

A selected relay can have an active grant but not enough visible budget for the transfer. A simple pre-flight failure would be correct but weak: if another configured relay has enough budget, Shurli should try that path before returning an error.

### Decision

The file-transfer queue processor retries away from low-budget relay paths:

- If the selected relay has an active grant but insufficient budget, reset the local circuit counters and reopen the stream.
- If the reopened stream returns to the same low-budget relay, close relay connections through that relay and open again.
- The next open stream goes through the normal path dialer, which consumes the `RelayDiscovery.RelayAddrs()` ranked list.
- Directory sends use the same idea with a probe stream before the real directory transfer.
- If the final checked relay still lacks enough budget, return a human-readable error saying the file or directory exceeds the relay session limit on available relays.

### Alternatives Considered

**Fail immediately on insufficient budget** was simpler, but it left usable relay grants idle.

**Reset counters without closing the relay connection** did not force libp2p to choose a different relay when the same low-budget circuit was reused.

**Close all connections to the peer** would be too aggressive and could kill healthy direct paths.

### Consequences

- Low-budget relays can be bypassed without manual relay reordering.
- Direct connections are left alone when closing relay paths.
- The retry path still respects relay health and budget ranking.
- The number of retry steps is bounded, so a transfer does not loop forever across insufficient relays.

### Physical Verification

The first physical budget-aware selection test verified the ranked relay choice. The same 2026-04-03 test run could not complete dynamic budget-switch verification after budget reduction because receipt delivery became stale. That failure directly produced the receipt freshness fix in c78101e.

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_grants.go`, `pkg/sdk/pathdialer.go`, `pkg/sdk/relaydiscovery.go`

---

## ADR-BR07: Receipt Freshness Bugs Became Routing Bugs

| | |
|---|---|
| **Date** | 2026-04-04 |
| **Status** | Accepted |
| **Commits** | c78101e, c833728 |

### Context

Budget-aware routing only works when the cache is fresh enough to be useful. Physical testing found two cache freshness failures:

- After revoke and re-grant, the client did not receive the new receipt on reconnect.
- When bytes flowed through a relay without a cached receipt, local byte tracking silently dropped those bytes.

Both bugs mattered to relay selection because the cache was now part of route ordering and pre-transfer decisions.

### Decision

Fix receipt freshness at two points:

1. `RunReconnectNotifier` now sends grant receipts on every reconnect when the relay has an active grant for that peer. The 30-second dedupe window still applies to peer introductions, not to grant receipt delivery.
2. `GrantCache.TrackCircuitBytes` creates a placeholder receipt when bytes are tracked before the real receipt arrives. `GrantCache.Put` preserves those counters when the real receipt later replaces the placeholder.

The follow-up UX hardening in c833728 made the state visible through status output and humanized transfer errors.

### Alternatives Considered

**Deliver receipts only when a grant is created** misses the exact revoke and re-grant window found during testing.

**Deduplicate grant receipts like peer introductions** hides legitimate state changes.

**Ignore byte tracking without a receipt** keeps the cache clean, but makes status and later routing decisions less trustworthy.

### Consequences

- Reconnecting clients receive current grant state instead of stale create-time state.
- Byte counters survive the placeholder-to-real-receipt transition.
- Status output can show used and remaining budget even across circuit resets.
- Cache freshness improves routing, but still does not become authority.

### Physical Verification

The triggering test reduced one relay from 2 GB to 10 MB while another relay still had 100 MB. The client removed the expired entry, displayed the reduced relay as "no grant," and still sent a 1 MB file through that relay. That exposed both the missed receipt re-delivery and the missing placeholder byte accounting. c78101e fixed those two routing-relevant bugs.

**Reference**: `internal/relay/notify.go`, `internal/grants/cache.go`, `internal/daemon/handlers.go`, `cmd/shurli/cmd_status.go`

---

## ADR-BR08: Budget Exhaustion Recovery Must Not Require A Daemon Restart

| | |
|---|---|
| **Date** | 2026-04-03 |
| **Status** | Accepted |
| **Commit** | 97109c5 |

### Context

Relay-side enforcement can exhaust a grant and kill the circuit. After an operator extends the grant, the client should be able to reconnect and transfer again. Physical testing found that stale libp2p connection objects and backoff state could block that recovery until the daemon restarted.

That made budget-aware selection less useful. Choosing a better or refilled relay is not enough if the client cannot actually redial it.

### Decision

On a failed `ConnectToPeer` attempt that may be stuck behind exhausted relay state, Shurli clears the state that can prevent a fresh circuit:

- Close stale limited relay connections to the target peer.
- Clear swarm dial backoffs for the target and, in current code, configured relay servers.
- Reset the `PeerManager` backoff for the target peer.
- Reset UDP and IPv6 black-hole detector state.
- Re-add fresh relay circuit addresses before retrying.
- Make `PathDialer` pass circuit addresses directly to `host.Connect` instead of relying on the peerstore alone.

The retry happens once. If it still fails, the error is returned.

### Alternatives Considered

**Require daemon restart** was operationally unacceptable and hid the actual stale-state problem.

**Close every connection globally** would recover more cases but would disrupt unrelated peers.

**Only reset local counters** would fix display state, not the swarm and peer-manager state preventing a new circuit.

### Consequences

- Grant extension can restore transfer ability without restarting the daemon.
- Relay selection has a clean way to retry with fresh circuit addresses.
- Backoff clearing is targeted enough to avoid a full network reset.
- This remains a recovery path, not a guarantee that a relay has budget.

### Physical Verification

The verified outcome for 97109c5 was: exhaust relay budget, extend the grant, then send successfully without restarting the daemon. That is the behavior this ADR preserves.

**Reference**: `cmd/shurli/serve_common.go`, `pkg/sdk/pathdialer.go`, `pkg/sdk/peermanager.go`, `pkg/sdk/network.go`

---

## ADR-BR09: Boundaries And Operational Feedback

| | |
|---|---|
| **Date** | 2026-04-03 to 2026-04-04 |
| **Status** | Accepted |
| **Commits** | c833728, f8f9c7f |

### Context

Budget-aware routing can otherwise look mysterious. A transfer might choose one relay over another, warn about missing grants, retry through a different relay, or fail because every visible relay is too small. Operators need enough feedback to understand what happened without exposing private topology.

At the same time, this feature has strict boundaries. It must not become duplicate-payload hedging, client-side enforcement, or a replacement for the grant and relay enforcement layers.

### Decision

Expose budget-aware routing state through existing operational surfaces:

- `shurli status` shows active relay grants, no-grant relays, session budget, used bytes, remaining bytes, and session duration.
- Client-side grant receipt and revocation notifications are emitted when the daemon receives relay grant state.
- File-transfer CLI errors are humanized so low-level stream resets can point to relay budget exhaustion, circuit expiry, or peer offline states.

Keep the boundaries explicit:

- Selection is advisory. Relay enforcement remains authoritative.
- Budget-aware selection does not buy relay capacity; it chooses among allowed relay paths.
- It does not replace health scoring, Tail Slayer hedging, grant receipts, or per-peer relay data budgets.
- It does not duplicate payload data across relays to spend multiple budgets.

### Alternatives Considered

**Keep budget routing silent** would make correct behavior look arbitrary.

**Log only low-level stream errors** would leave operators guessing whether a relay budget, session expiry, or peer availability issue caused the failure.

**Duplicate payloads across multiple relays** was rejected. It would consume multiple grants, complicate accounting, and belongs to a different data-plane design.

### Consequences

- Operators can see whether a relay is signaling-only, active, exhausted, or missing visible grant state.
- Errors can point to an actionable next step without exposing private peer or network details in public docs.
- Budget-aware selection remains a routing layer built on top of authoritative relay policy.
- Future relay routing work can extend the score, but must preserve the same authority boundary.

### Physical Verification

The 2026-04-03 physical test used `shurli status` to confirm visible budgets before transfer and usage after transfer: the selected 2 GB relay showed about 1.2 MB used after a 1 MB send, while the 100 MB relay stayed unchanged. c833728 then hardened status display so used and remaining budget are visible across circuit resets.

**Reference**: `cmd/shurli/cmd_status.go`, `internal/daemon/handlers.go`, `plugins/filetransfer/commands.go`, `pkg/sdk/network.go`, `plugins/filetransfer/transfer_grants.go`

---

## Public Notes

This journal omits private topology, node names, peer IDs, addresses, and provider details. Performance numbers are included where they demonstrate architectural impact: relay selection ranking (ADR-BR03), stale-cache recovery (ADR-BR04, ADR-BR07), low-budget retry (ADR-BR06), and budget exhaustion recovery without restart (ADR-BR08). All numbers are from physical testing across real relay circuits. A formal benchmark report with controlled methodology and reproducible conditions is separate from this architecture record.
