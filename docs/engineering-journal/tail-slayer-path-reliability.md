# FT-Y: Tail Slayer Path Reliability

| | |
|---|---|
| **Date** | 2026-04-07 to 2026-04-16 |
| **Status** | Complete (TS-1 to TS-6 implemented and physically verified) |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-X01 to ADR-X07 |

Tail Slayer turned Shurli's path reliability work from "pick the best path" into "race independent paths and keep the winner." These Architecture Decision Records (ADRs) document how the pattern was adapted from Laurie Kirk's (LaurieWired) [Tail Slayer](https://www.youtube.com/watch?v=KKbgulTp3FE) work: do not predict stalls, route around them; keep hedged workers independent; push deduplication and cleanup to the edge.

For Shurli, the important constraint was scope. Hedging is excellent for cheap, independent control operations. It is dangerous for bulk file data unless the receiver has explicit deduplication and backpressure. This journal documents where the Tail Slayer pattern was applied, where it was rejected, and how the implementation became a transfer-survival mechanism instead of only a faster dial path.

---

## ADR-X01: Tail Slayer as a Network Reliability Primitive

| | |
|---|---|
| **Date** | 2026-04-09 |
| **Status** | Accepted |

### Context

Network stalls are not reliably predictable. A peer-to-peer (P2P) connection can look healthy until a relay, interface, firewall, or transport path stalls. Sequential fallback makes the first slow path the critical path. Predictive scoring helps after enough history exists, but it cannot eliminate first-contact stalls.

Shurli already had pieces of this idea: parallel direct versus relay dialing, relay health tracking, and checkpoint resume. The missing primitive was a consistent rule for control operations: if several independent paths are cheap enough to try, try them in parallel with bounded fanout and accept the first valid result.

### Decision

Adopt Tail Slayer as a bounded hedging pattern:

1. Group work by independent failure domain.
2. Start hedged workers with a small stagger.
3. Accept the first valid result.
4. Cancel or reset losing work at the edge.
5. Keep every worker independent enough that one stalled path cannot block another.

The implementation applies this to relay candidate dialing, relay bootstrap, multi-peer manifest exchange, stream opening, cancel propagation, managed relay backup paths, checkpoint/resume failover, and zero-sync block coordination for multi-peer downloads.

Bulk file data is intentionally not duplicated through the same mechanism. File payload hedging belongs only where the receiver can prove chunk identity, deduplicate safely, enforce backpressure, and account for relay budget correctly.

### Alternatives Considered

**Sequential fallback** was simple, but it made slow relays and slow peers part of the happy path.

**Predictive scoring only** was useful but insufficient. It improves future choices; it does not solve a fresh stall.

**Duplicate all data over multiple paths** looked attractive, but it wastes bandwidth and relay budget. It also creates correctness and abuse risks unless the receiver is explicitly designed for duplicate chunks.

### Consequences

- Hedging became a shared reliability pattern instead of one-off retry logic.
- Fanout and stagger caps prevent a stalled path from turning into unbounded work.
- The same primitive can help both fast-path latency and path-loss survival.
- The public contract stays conservative: duplicate control work, not duplicate bulk data.

**Reference**: `pkg/sdk/pathdialer.go`, `pkg/sdk/hedged.go`, `pkg/sdk/connstream.go`

---

## ADR-X02: Hedged Relay Establishment (TS-1 and TS-2)

| | |
|---|---|
| **Date** | 2026-04-09 |
| **Status** | Accepted |
| **Commit** | f903d6f |

### Context

Relay fallback had a hidden sequential step. `PathDialer` raced the Distributed Hash Table (DHT) leg against a relay leg, but the relay leg could still walk relay addresses one at a time. Bootstrap had the same shape: connect to relay servers in order, then continue once a relay was available.

That meant one slow relay could delay the whole fallback path, even when another relay could have worked.

### Decision

Treat each relay server as an independent channel:

- Group relay addresses by relay peer ID.
- Race relay candidates with staggered starts.
- Bound fanout before dialing.
- Cancel losing relay attempts after the first successful path.
- In bootstrap, let the first successful relay unblock routing while other bounded relay attempts may still finish in the background.

Relay budget and health filtering happen before the race. Hedging only races candidates that are already allowed to be tried.

### Alternatives Considered

**Keep relay dialing sequential** preserved old behavior, but kept the tail latency problem.

**Try every known relay at once** would reduce latency in some cases, but it would overuse local resources and relay capacity.

**Add a user-facing relay race knob** added configuration surface without changing the real architecture problem.

### Consequences

- Slow relay candidates no longer gate the entire relay fallback path.
- Relay bootstrap can become useful after the first success instead of after the whole list completes.
- Relay racing remains bounded by fanout, stagger, policy, and budget checks.
- Relay identity, not address string count, defines the failure-domain grouping.

### Physical Verification

| Test | Result |
|------|--------|
| Both relays healthy | First relay won in 0.1s wall time. Second relay cancelled cleanly. |
| Primary relay down, failover to secondary | Secondary relay connected in 2.5s wall time. Sequential fallback would have waited 30s for the dead relay to timeout first. |
| Standalone bootstrap with relay racing | Relay connected in 466ms. Routing unblocked immediately after first success. |

**Reference**: `pkg/sdk/pathdialer.go`, `pkg/sdk/bootstrap.go`

---

## ADR-X03: Hedged Multi-Peer Manifest Exchange (TS-3)

| | |
|---|---|
| **Date** | 2026-04-10 |
| **Status** | Accepted and physically verified |
| **Commit** | f903d6f |

### Context

Multi-peer download needs metadata before data can start. A receiver must learn the file name, size, chunk layout, and Merkle root before scheduling chunk requests. The old shape could wait behind a slow metadata responder even when another authorized peer had the same file ready.

This is the perfect Tail Slayer case: manifest requests are cheap, independent, and naturally deduplicated by the Merkle root.

### Decision

Request manifests from all candidate peers in parallel:

1. Start a manifest request to each candidate peer.
2. Accept the first valid manifest immediately.
3. Drain remaining manifest results in the background.
4. Cross-verify late manifests against the winning file name, size, and root.
5. Close loser streams once they are no longer needed.

The first valid manifest starts the transfer. Later manifests are still useful because they verify that other peers are serving the same content before the scheduler assigns them chunks.

### Alternatives Considered

**Sequential manifest fetch** avoided concurrency, but it made the slowest early peer control transfer startup.

**Wait for all manifests before starting** increased confidence but punished the normal case. The Merkle root already gives a strong correctness boundary.

**Trust peer-provided metadata without cross-verification** would make startup faster, but it would weaken multi-source safety.

### Consequences

- One slow manifest responder no longer delays the transfer.
- Late manifest responses still improve correctness checks.
- Bulk data is not duplicated; only metadata is hedged.
- The design uses the existing Merkle root as the deduplication and identity boundary.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`

---

## ADR-X04: Connection-Pinned Hedged Streams and Cancel Fan-Out (TS-4)

| | |
|---|---|
| **Date** | 2026-04-10 |
| **Status** | Accepted and physically verified |
| **Commits** | 26a9d75, 5ec2c54 |

### Context

Opening a stream through `host.NewStream` leaves connection choice to libp2p. That is normally good, but it prevents Shurli from racing independent paths on purpose. If a peer has both direct and relay connectivity, the caller needs a way to open a protocol stream on a specific connection group.

The Shurli File Transfer (SHFT) protocol is also stream-ordered. Hedging individual in-band messages would require a protocol redesign because both sides must agree which duplicate message won and which stream owns the session.

### Decision

Add connection-pinned stream opening:

- `ConnGroup` groups direct connections and relay connections by failure domain.
- `OpenStreamOnConn` opens a multistream-selected protocol stream on a specific libp2p connection.
- `HedgedOpenStream` races connection groups with bounded fanout.
- Direct groups are sorted first so a healthy direct path is preferred.
- Losing streams are reset after a winner is selected.
- Cancel propagation fans out across available direct, relay, and managed relay connections.

This keeps hedging at the operation boundary: browse open, download open, transfer open, and cancel propagation. It does not try to hedge arbitrary SHFT frames inside a running stream.

### Alternatives Considered

**Rely on `host.NewStream`** kept the API simple, but it removed control over path independence.

**Hedge every SHFT wire message** was rejected because stream ordering and session ownership would become ambiguous.

**Create separate plugin protocols per path** would add complexity without improving the core primitive.

### Consequences

- File transfer callers can intentionally race direct and relay paths.
- Control operations can recover from a stuck connection group without waiting for libp2p to rediscover a path.
- Direct connectivity remains preferred when it is healthy.
- Cancel messages have a better chance of reaching the sender during failover cleanup.
- Protocol security remains unchanged because plugin authorization and service negotiation still run for the selected stream.

### Physical Verification

| Metric | Value |
|--------|-------|
| Cancel API latency (direct LAN) | 11.1ms including multi-path cancel fire-and-forget |
| In-flight data drained after cancel | 1.3 MB (QUIC buffers flushed, then stream reset) |
| HedgedOpenStream single-group overhead | Zero. Fast path: no goroutines, no channels, no allocation. |
| Browse via direct LAN | <10ms (no regression from pre-hedging baseline) |
| Browse via relay | ~500ms (relay latency dominated, hedging added zero overhead) |

**Reference**: `pkg/sdk/connstream.go`, `pkg/sdk/hedged.go`, `pkg/sdk/network.go`, `plugins/filetransfer/transfer_cancel.go`, `plugins/filetransfer/handlers.go`

---

## ADR-X05: PathProtector and Managed Relay Backup Paths (TS-5)

| | |
|---|---|
| **Date** | 2026-04-11 |
| **Status** | Accepted and physically verified |
| **Commits** | 0a28689, 6af265c |

### Context

Hedged stream opening only helps when there is more than one path to race. During real transfers, Shurli could lose relay backup paths because cleanup logic removed relay connections once a direct connection existed. The connection manager protect tag was not enough because parts of Shurli intentionally closed relay connections during cleanup.

There was a second issue: libp2p generally deduplicates connections to a peer. When a direct connection already exists, dialing an extra relay path through the normal swarm path may collapse back to the existing connection instead of producing an independent relay circuit.

### Decision

Add `PathProtector` as the transfer-time owner of backup paths:

- Transfer code protects a peer for the lifetime of active send and receive work.
- Cleanup code checks the protector before closing relay paths.
- `PathProtector` can establish a managed relay circuit through libp2p's public transport API.
- Managed relay connections live outside the normal swarm connection list, but are exposed as `ConnGroup` candidates for hedging and cancel fan-out.
- Managed paths are capped, cooled down, health-checked, and reaped when stale.
- Local Area Network (LAN) paths are skipped where a managed relay backup is unnecessary.

The important design choice was to use public libp2p APIs, not a fork and not a raw relay protocol. `PathProtector` asks the circuit transport to dial a relay circuit directly, then wraps that capable connection as a Shurli-managed backup path.

### Alternatives Considered

**Connection manager protect tags only** were insufficient because Shurli cleanup code can close connections directly.

**Fork libp2p** would provide more control, but it would create a long-term maintenance burden.

**Implement raw circuit relay protocol handling** would duplicate subtle transport behavior that libp2p already owns.

**Build path scoring first** was the wrong order. A backup path must exist before it can be scored or raced.

### Consequences

- Long-running transfers can keep a relay backup path alive while a direct path is active.
- Hedged stream opening now has real independent paths to race.
- Managed relay paths stay owned, bounded, and observable instead of becoming hidden swarm side effects.
- The ownership model does not depend on path independence scoring.
- Physical testing found a relay classification bug that became the root cause of link saturation in TS-5b. Relay connections whose multiaddr lacked `/p2p-circuit` were classified as direct, causing hedged stream opening to race them as if they were fast LAN paths. The fix treats `conn.Stat().Limited` as the primary relay signal, with circuit-address metadata as a fallback for relay peer ID extraction.

### Physical Verification

| Metric | Value |
|--------|-------|
| Managed circuit establishment time | 289-780ms (varies by relay round-trip time) |
| HedgedOpenStream groups raced | 3 (direct + relay + managed-relay). Direct wins in ~14ms. |
| Managed circuit lifetime | 302ms (1 MB fast transfer) to 16.7s (50 MB transfer) |
| Deauth to circuit closure | <1ms (synchronous callback) |
| Safety reaper orphan detection | 38.4s (30s timeout + reaper interval) |
| Relay budget impact per managed circuit | <1 KB (Noise handshake only, zero data bytes, backup path unused) |
| B-side bidirectional hedging | Remote peer raced 3 groups with zero B-side code. Managed relay path inherited from A-side's circuit for free. |

**Reference**: `pkg/sdk/pathprotector.go`, `pkg/sdk/managed_conn.go`, `pkg/sdk/connstream.go`, `pkg/sdk/peermanager.go`

---

## ADR-X06: Automatic Failover Through Checkpoint/Resume (TS-5b)

| | |
|---|---|
| **Date** | 2026-04-12 to 2026-04-16 |
| **Status** | Accepted and physically verified |
| **Commits** | 1414f06, 7334699, c0261b0 |

### Context

Keeping a backup path alive is only half the problem. A transfer also needs to survive when the active stream dies. File transfer already had checkpoint resume, but manual resume is not enough for path reliability. The receiver needed to detect retryable network loss, open a new stream through the hedged path primitive, renegotiate resume, and continue without losing already received chunks.

Physical verification exposed a deeper issue: the ConnGroup classification bug from TS-5 (relay connections misclassified as direct) was the real root cause of link saturation during failover. Hedged stream opening was selecting relay paths thinking they were direct, causing the transfer to saturate a relay circuit instead of preferring the actual direct connection. Fixing ConnGroup classification (commit 7334699) resolved both the saturation and the failover path selection in one change.

### Decision

Add automatic failover around the receive loop:

1. Classify transfer errors into retryable network loss, relay-session retry, and terminal failure.
2. Preserve progress through in-memory state and checkpoint files.
3. Send multi-path cancel for the old sender session.
4. Wait for a usable connection group when needed.
5. Open the next stream with `HedgedOpenStream`.
6. Renegotiate resume from the current bitfield or checkpoint.
7. Reopen temporary files after cleanup.
8. Continue transfer with failover counters, status, and progress intact.

Retry counts and backoff prevent infinite loops. Relay budget checks remain awareness-only during failover because relay enforcement belongs on the relay side, but the transfer layer still logs the condition and continues when a partial transfer can be saved.

### Alternatives Considered

**User-triggered resume only** preserved simpler code, but path failure would still interrupt the transfer.

**Restart from byte zero** wasted bandwidth and discarded the checkpoint system already built for this problem.

**Treat all stream errors as retryable** would hide real protocol and authorization failures behind pointless retries.

### Consequences

- A file transfer can survive a retryable path loss without user intervention.
- The same checkpoint mechanism handles manual resume and automatic failover.
- Sender cleanup is explicit through multi-path cancel before the replacement stream takes over.
- The failover loop records path failover events without marking the transfer failed during retry.
- Physical testing verified recovery, finalization, and progress continuity across path loss.
- ConnGroup misclassification was the most significant debugging finding: a relay connection without `/p2p-circuit` in its multiaddr was treated as direct, causing hedged stream opening to pick the wrong path during failover. The fix in `classifyConnGroup` resolved both link saturation and failover path selection.

### Physical Verification

**Failover test**: 1 GB download, started on direct WiFi LAN path, WiFi switched mid-transfer to force relay fallback.

| Phase | Metric | Value |
|-------|--------|-------|
| Direct (before failover) | Speed | ~42 MB/s |
| Direct (before failover) | Progress at failure | 61.7% (chunk 1265 of 2048) |
| Failover | Reconnection time | 14 seconds (network switch + peer rediscovery) |
| Failover | Hedge race time | 83ms (2 groups raced: relay + managed-relay) |
| Relay (after failover) | Speed | ~8.5 MB/s |
| Overall | Total duration | 1m15.5s |
| Overall | Data loss | 0 bytes |

**ConnGroup fix impact** (same file, same network, before vs after classification fix):

| Metric | Before fix | After fix |
|--------|-----------|-----------|
| Download speed (50 MB, direct WiFi) | 781 KB/s | 25.0 MB/s |
| Duration | 1m5.5s | 2.0s |
| 10 GB unlimited (direct LAN) | 85 MB/s, 100% packet loss, internet dead | 79.8 MB/s, 0% packet loss, internet usable |

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_errors.go`, `plugins/filetransfer/transfer_resume.go`, `plugins/filetransfer/transfer_stream.go`

---

## ADR-X07: Zero-Sync Block Coordination for Multi-Peer Downloads (TS-6)

| | |
|---|---|
| **Date** | 2026-04-07 |
| **Status** | Accepted and physically verified |
| **Commits** | 42d58f7, f2de5b1 |

### Context

Multi-peer download originally assigned block ranges statically: peer 0 gets blocks 0-99, peer 1 gets blocks 100-199, and so on. When a slow peer held a large range, fast peers sat idle after finishing their own blocks. The slow peer became the critical path for the entire transfer.

This is the classic prediction failure the Tail Slayer pattern exists to avoid. Static assignment predicts that all peers will transfer at the same speed. They never do.

### Decision

Replace static assignment with a channel-based work-stealing queue (`blockQueue`):

- All block indices are pre-loaded into a buffered channel. Peers claim work by reading from the channel.
- `claim()` blocks until a block is available, the transfer completes, or the context is cancelled. Fast peers naturally claim more blocks because they finish faster and return to the channel sooner.
- `tryClaim()` is the non-blocking variant, used when the peer pipeline has in-flight requests to prevent deadlock.
- Failed blocks go to a priority retry channel. `claim()` checks retry before primary, so failures are re-served quickly by whichever peer is free.
- Slow peers are restricted to retry-only work (`slowPeer` flag). They cannot take primary blocks from fast peers, but they still contribute by re-serving blocks that other peers failed.
- `markComplete()` atomically updates the bitfield and a transferred-bytes counter. When the last block completes, the done channel closes and all blocked `claim()` calls return.
- `requeue()` is non-blocking (buffered retry channel) to prevent a failing peer from blocking the requeue path.
- `checkpointSnapshot()` takes a mutex-protected copy of the bitfield for safe checkpoint saving from a separate goroutine.

No coordination messages are exchanged between peers. Each peer independently races for work through the shared channel. The channel itself is the synchronization primitive.

### Alternatives Considered

**Static block ranges** were simple but made the slowest peer the bottleneck. A 2-peer test showed 7.6 MB/s (slower than single-peer 8.8 MB/s) because the slow peer took 56 seconds to chunk 500 MB while the fast peer sat idle.

**Explicit coordinator protocol** would add a message exchange layer between peers to assign and reassign blocks. This adds latency, creates a single point of failure in the coordinator, and requires consensus on block ownership.

**Interleaved IDs without work-stealing** (peer i gets blocks i, i+N, i+2N) distributes work evenly but still cannot adapt when one peer is slower than expected mid-transfer.

### Consequences

- Fast peers naturally absorb more work without any prediction or scoring.
- Failed blocks are retried with priority by the next available peer, not queued behind fresh work.
- Slow peers contribute to retry work without stealing primary blocks from fast peers.
- Zero coordination messages between peers. The Go channel is the only synchronization mechanism.
- Checkpoint resume works correctly through the bitfield snapshot, regardless of which peer served which block.
- Physical testing confirmed: 95.7 MB/s with 2 peers on a 28 GB file (93.3% from fast peer, 6.7% from slow peer), compared to 7.6 MB/s with the old static assignment.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`

---

## Public Notes

This journal omits private topology, node names, peer IDs, addresses, file names, and provider details. Performance numbers are included where they demonstrate architectural impact: relay failover latency (ADR-X02), control signal overhead (ADR-X04), backup path cost (ADR-X05), failover continuity (ADR-X06), and work-stealing efficiency (ADR-X07). All numbers are from physical testing across real network transitions. A formal benchmark report with controlled methodology and reproducible conditions is separate from this architecture record.
