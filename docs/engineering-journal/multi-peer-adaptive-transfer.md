# FT-Y: Multi-Peer Adaptive Transfer Scheduling

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-16 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-MP01 to ADR-MP10 |
| **Primary Commits** | f8f9c7f, 42d58f7, f2de5b1, 674b662, 007ce63, 8dfe669, f9ee530 |

Multi-peer transfer started as a RaptorQ fountain-code design, became an interleaved adaptive symbol design, and then moved to receiver-owned work-stealing raw chunks. That arc matters. The intermediate design had a good instinct: fast peers should be able to contribute more than slow peers. Physical testing showed the implementation still made every peer pay the full file chunking and encoding cost, so the slow peer stayed on the critical path.

This journal documents the multi-peer scheduling and wire-protocol decisions that are not already covered by the Tail Slayer journal. Hedged manifest exchange is covered in [ADR-X03](tail-slayer-path-reliability.md#adr-x03-hedged-multi-peer-manifest-exchange-ts-3). `blockQueue` internals, retry priority, and zero-sync coordination are covered in [ADR-X07](tail-slayer-path-reliability.md#adr-x07-zero-sync-block-coordination-for-multi-peer-downloads-ts-6). The focus here is the evolution from static RaptorQ symbols to raw block requests, the per-peer worker stream protocol, manifest verification, checkpoint shape, and the boundary between retained RaptorQ library code and the active protocol.

---

## ADR-MP01: Static Multi-Peer Partitioning Failed

| | |
|---|---|
| **Date** | 2026-03-25 to 2026-04-01 |
| **Status** | Superseded |
| **Commits** | bfd4b06, b824271, f8f9c7f |

### Context

The original multi-peer download path used RaptorQ fountain symbols. Each file chunk became a RaptorQ block, and peers were assigned symbol ranges. The intent was that symbols from multiple peers could reconstruct the same content, but the actual scheduling was static: each peer owned a slice of symbol IDs.

Static partitioning predicted that peers would behave similarly. Real peer-to-peer transfers do not behave that way. A fast peer could finish its range and then sit idle while a slow peer still held required symbols. The transfer became bounded by the slowest useful source, not by aggregate available bandwidth.

### Decision

Treat static range assignment as a failed architecture for Shurli multi-peer download. It was useful enough to prove the protocol shape, but it did not meet the FT-Y performance goal.

The replacement direction was adaptive scheduling: make the receiver decide what remains unfinished, and let faster peers take more work.

### Alternatives Considered

**Increase repair overhead** would give the receiver more symbols, but it would also add bandwidth and CPU overhead without removing the slow-peer dependency.

**Tune symbol ranges manually** would only work for a known topology. Shurli cannot assume stable or symmetric peers.

**Wait for all peers to finish their ranges** was the original behavior, and physical testing rejected it.

### Consequences

- Static symbol ownership was removed from the active design.
- The multi-peer problem was reframed as scheduling, not only erasure recovery.
- Later designs focused on "who should serve the next unit of work?" instead of "which peer owns this fixed range?"

### Physical Verification

An early two-source RaptorQ test transferred 100 MB at 3.1 MB/s. That was about slowest-peer behavior, not aggregate bandwidth. The result was enough to prove the static partitioning problem before the later FT-Y redesign.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_raptorq.go`, `plugins/filetransfer/transfer_raptorq_test.go`

---

## ADR-MP02: Interleaved Adaptive RaptorQ Symbol IDs Were An Intermediate Step

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-03 |
| **Status** | Superseded |
| **Commit** | f8f9c7f |

### Context

Commit f8f9c7f replaced contiguous symbol ranges with interleaved RaptorQ symbol IDs. Peer `i` generated IDs `i, i+N, i+2N...`, where `N` was the number of peers. That eliminated direct overlap between peers and let the receiver decode from any `K` symbols.

The good idea was real: if one peer was faster, it should be able to deliver enough symbols to decode blocks before a slower peer caught up. This matched the "any K symbols" property of fountain codes.

### Decision

Keep the lesson, not the implementation. Interleaving fixed the most obvious static-range mistake, but it still made every peer serve symbols for every block. The slow peer still had to chunk and encode the full file. On reliable point-to-point streams, this was expensive work that did not buy enough resilience.

### Alternatives Considered

**Keep interleaving and add more diagnostics** would have made the failure easier to observe, but it would not change the work each peer had to do.

**Add per-block locks to the RaptorQ decoder path** would reduce mutex contention, but all peers would still chunk and encode all blocks.

**Terminate slow peers once enough symbols arrive** would help some transfers, but the sender-side full-file chunking cost would still be paid before a slow peer could contribute.

### Consequences

- Interleaved symbols are documented as a transitional design, not as the winning architecture.
- The receiver-owned scheduling idea survived.
- The RaptorQ active path was replaced instead of optimized further.

### Physical Verification

A correct two-peer interleaved-symbol test on a 500 MB file measured 7.6 MB/s. The faster single source measured 142.8 MB/s, and the slower single source measured 8.8 MB/s. Multi-peer was slower than the slower source alone because the slow peer still had to chunk and encode the full file before the transfer could finish.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_raptorq.go`, `plugins/filetransfer/transfer_multipeer_test.go`

---

## ADR-MP03: Rewrite The Wire Protocol In Place

| | |
|---|---|
| **Date** | 2026-04-06 |
| **Status** | Accepted |
| **Commit** | 42d58f7 |

### Context

The RaptorQ and raw-block protocols are not compatible. The old request header carried peer index and peer count for symbol interleaving. The new request header carries a root hash and feature flags. The old data stream carried `msgFountainSymbol`; the new data stream carries explicit block request, block data, block error, and done frames.

The current protocol ID remains `/shurli/file-multi-peer/1.0.0`. The plugin registration also exposes `file-multi-peer` version `1.0.0`. The code was rewritten in a pre-release deployment window where nodes were controlled and rebuilt together.

### Decision

Do not implement mixed-version negotiation for this rewrite. Keep the protocol ID at `1.0.0`, replace the active grammar in place, and rely on same-generation deployments.

This mirrors the FT-Y streaming rewrite decision: compatibility code would keep a dead, security-sensitive parser alive in the hot path. In this case, the active multi-peer protocol was still pre-release and had already proven incorrect under physical testing.

### Alternatives Considered

**Bump to `/shurli/file-multi-peer/2.0.0` and keep both handlers** would be cleaner for public mixed deployments, but it would preserve the RaptorQ active path solely for compatibility.

**Auto-detect old versus new messages** would make the first byte dispatch more complicated and create more malformed-input cases.

**Reject all old nodes with a negotiated capability probe** would still require supporting an additional negotiation layer without improving the active transfer.

### Consequences

- The active wire parser stayed small and auditable.
- Mixed-generation nodes fail rather than silently degrading.
- Public docs must describe the current raw-block protocol, not the old RaptorQ symbol protocol.

### Physical Verification

The rewrite was verified as a same-generation rollout. Subsequent physical tests used nodes running the rewritten protocol and did not attempt mixed-version interoperability. Current code confirms the active protocol ID is still `/shurli/file-multi-peer/1.0.0`, while the frame grammar is the raw-block grammar.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/plugin.go`, `plugins/filetransfer/transfer.go`

---

## ADR-MP04: Verify Every Peer Manifest

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-09 |
| **Status** | Accepted |
| **Commits** | f8f9c7f, 42d58f7, f2de5b1, 8dfe669 |

### Context

Multi-peer download trusts multiple peers for the same content. The receiver starts from a root hash discovered through the download probe, then asks candidate peers for a multi-peer manifest containing chunk hashes, sizes, filename, file size, and chunk count.

Root hash verification is necessary, but it is not sufficient. A malicious or buggy manifest could provide a valid-looking hash list while lying about chunk sizes or total file size. Those fields affect allocation, disk reservation, offsets, and `WriteAt` placement.

### Decision

Verify every manifest before assigning that peer data work:

- Compute the Merkle root from the peer's chunk hashes and compare it to the requested root.
- Check the sum of `ChunkSizes` equals `FileSize`.
- Bound every chunk size before allocation.
- Check peer chunk count and chunk sizes against the winning manifest before launching that peer worker.
- Cross-check late manifests from hedged manifest exchange against the winner.

### Alternatives Considered

**Trust the first peer's manifest** made peer 0 a stronger trust anchor than necessary.

**Only verify the Merkle root** ignored fields that control memory and disk behavior.

**Wait for all manifests before starting** would maximize agreement, but it would duplicate ADR-X03's startup latency problem. Late manifests are verified in the background instead.

### Consequences

- A peer can only serve blocks after its manifest matches the requested content shape.
- Allocation and disk writes are bounded before block data is read.
- Manifest verification works with hedged startup without waiting for slow peers.

### Physical Verification

The healthy physical runs did not produce manifest mismatches. Code-level verification is covered by manifest validation tests for size sums, per-chunk bounds, manifest marshal/unmarshal, and request wire format. The 2026-04-16 multi-peer manifest run verified that both peers could pass manifest exchange and then serve blocks in the same transfer.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_multipeer_test.go`, `plugins/filetransfer/share.go`, `plugins/filetransfer/transfer.go`

---

## ADR-MP05: Remove RaptorQ From Active Multi-Peer Transfer

| | |
|---|---|
| **Date** | 2026-04-04 to 2026-04-06 |
| **Status** | Accepted |
| **Commit** | 42d58f7 |

### Context

RaptorQ is useful when a receiver needs any `K` symbols from a larger set, especially on lossy or broadcast-style delivery. Shurli's active multi-peer transfer path is different. It uses reliable QUIC streams between authorized peers. QUIC already retransmits lost packets, and each peer can be asked for a specific chunk.

The physical failure was not "RaptorQ cannot decode." It was that RaptorQ made the transfer unit a symbol instead of a chunk. That forced all peers to work on all blocks, and it added encoding, decoding, repair-symbol overhead, and decoder state.

### Decision

Remove RaptorQ from the active multi-peer protocol. Use raw chunks as the transfer unit. Keep `transfer_raptorq.go` and its tests as a library boundary for future scenarios where fountain codes make sense, such as unreliable or broadcast transport.

### Alternatives Considered

**Optimize the RaptorQ path** would reduce some overhead, but it would not align the transfer unit with the scheduler.

**Use Reed-Solomon on the multi-peer path** was unnecessary for the active reliable-stream case and overlaps with the single-peer erasure path.

**Delete RaptorQ entirely** would lose tested library code that may still be useful later.

### Consequences

- The active path no longer generates or decodes symbols.
- Multi-peer progress is measured in completed chunks.
- Per-block hash verification happens before disk writes.
- RaptorQ remains available as non-active library code.

### Physical Verification

After the raw-chunk rewrite and follow-up fixes, a 562 MB multi-peer download measured 86.4 MB/s with two peers. The fast peer served 96 percent of blocks and the slower peer served 4 percent. The earlier interleaved RaptorQ test on a 500 MB file measured 7.6 MB/s and was slower than the slower source alone.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_raptorq.go`, `plugins/filetransfer/transfer_raptorq_test.go`

---

## ADR-MP06: Receiver-Owned Work Stealing With Raw Block Requests

| | |
|---|---|
| **Date** | 2026-04-06 to 2026-04-09 |
| **Status** | Accepted and physically verified |
| **Commits** | 42d58f7, f2de5b1, 007ce63, 8dfe669 |

### Context

Once raw chunks became the transfer unit, the receiver needed a way to tell each peer exactly which block to send. ADR-X07 covers the internal `blockQueue` coordination. The missing protocol decision was how a receiver-owned queue maps to per-peer streams.

### Decision

Use one multi-peer stream per serving peer. After manifest verification, each peer stream follows a request/response protocol:

- Receiver sends `msgBlockRequest` with a block index.
- Sender validates the index and rejects duplicate requests on that stream.
- Sender reads the requested chunk from disk using boundary metadata and `ReadAt`.
- Sender replies with `msgBlockData`, including block index, flags, decompressed size, data length, and payload.
- Sender can reply with `msgBlockError` for a specific requested block.
- Receiver sends `msgMultiPeerDone` when all blocks are complete.

The sender uses a boundary scan cache and keeps the file descriptor open for the session. It stores offsets, sizes, and hashes, not full chunk data. Blocks are read on demand.

### Alternatives Considered

**Sender-pushed chunks** would let each peer choose work, but peers cannot know the receiver's global completion state.

**A central coordinator protocol between peers** would add network coordination that is unnecessary. The receiver already owns the desired output file and can schedule work locally.

**Reuse the single-peer SHFT stream grammar** would mix two different protocols. Multi-peer needs random-access block requests, while single-peer SHFT is an ordered streaming protocol.

### Consequences

- Fast peers naturally receive more block requests because they drain their pipeline faster.
- Slow or failed peers do not own a fixed range.
- Sender memory drops from whole-file chunk storage to boundary metadata plus one block buffer.
- The wire format is explicit enough to bound allocation before payload reads.

### Physical Verification

Physical tests after the rewrite showed the intended distribution. A 562 MB transfer reached 86.4 MB/s with the fast peer serving 96 percent of blocks. A later 562 MB run with one direct peer and one relay-path peer measured 93.7 MB/s by the client and 95.7 MB/s in daemon logs, with a 91.3 percent / 8.7 percent block split.

The zero-sync queue mechanics and 28 GB distribution result are documented in ADR-X07. This ADR records the wire-level request/response protocol that feeds that queue.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_multipeer_test.go`, `docs/engineering-journal/tail-slayer-path-reliability.md`

---

## ADR-MP07: Fixed-Depth Per-Peer Pipelines And Backpressure

| | |
|---|---|
| **Date** | 2026-04-06 to 2026-04-09 |
| **Status** | Accepted |
| **Commits** | 42d58f7, 007ce63 |

### Context

A pure request/response loop would pay one round trip per block. A peer with 2,000 blocks and non-trivial round trip time could waste seconds waiting between requests. But unbounded in-flight requests would increase memory pressure and make cancellation/requeue behavior harder to reason about.

The first work-stealing implementation also exposed a deadlock shape: a worker could block trying to claim more work while it still had pipeline responses waiting to be drained.

### Decision

Use a fixed per-peer pipeline depth of 4:

- Pre-fill up to four requested blocks for a peer.
- Process responses in request order.
- Requeue all in-flight pipeline blocks when the worker exits.
- Use non-blocking `tryClaim()` while responses remain in the pipeline.
- Use blocking `claim()` only when the pipeline is empty.
- On reconnect, re-exchange and verify the manifest, then resend in-flight requests.

Depth 4 was chosen as the conservative first implementation. Adaptive depth remains a future tuning option, not part of this ADR.

### Alternatives Considered

**Depth 1** would be simpler but too sensitive to round trip time.

**Unbounded pipelining** would hide latency but could turn a slow or failing peer into unbounded buffered work.

**Adaptive depth immediately** was deferred because the fixed-depth design needed physical validation first.

### Consequences

- Each worker has bounded in-flight work.
- Backpressure stays local to the peer stream.
- Failed or cancelled workers do not lose block ownership, because in-flight blocks are requeued.
- The non-blocking claim path prevents a worker from blocking while unread responses remain on its stream.

### Physical Verification

During physical testing, an early work-stealing run stuck near the end of a transfer because workers could block while pipeline responses remained. Commit 007ce63 added `tryClaim()` and removed the false speed-based slow-demotion path. After that fix chain, multi-peer transfers completed, including kill-one-peer recovery and checkpoint resume.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_multipeer_test.go`

---

## ADR-MP08: Peer Health Uses Strikes, Reconnects, And Error-Triggered Retry-Only Demotion

| | |
|---|---|
| **Date** | 2026-04-06 to 2026-04-09 |
| **Status** | Accepted |
| **Commits** | 42d58f7, f2de5b1, 007ce63 |

### Context

Multi-peer download must tolerate peers that disconnect, serve a wrong block, fail decompression, return an error for the wrong block, or repeatedly fail block requests. The response should be technical and bounded: retry work elsewhere, reduce trust in that peer for this session, and finish with remaining peers when possible.

The design intentionally avoids threat theater. Authorized peers can have transient disk corruption, stale files, relay loss, or process restarts. The system should isolate bad blocks and bad streams without assuming every failure is malicious.

### Decision

Track per-peer state inside the receiver worker:

- Reconnect a failed stream up to three times.
- Re-exchange and re-verify the manifest after reconnect.
- Add strikes for out-of-order responses, wrong-block errors, decompression failure, size mismatch, and block hash mismatch.
- Put suspect peers on parole after a bad block. A clean block clears parole.
- Ban a peer for the session after the strike threshold.
- Track consecutive block errors. At five consecutive errors, demote the peer to retry-only work. At ten, disconnect it.

Speed-based slow-peer demotion was removed. Work-stealing already lets fast peers claim more primary blocks, and the average-speed comparison created false positives during physical testing. Retry-only demotion remains error-triggered.

### Alternatives Considered

**Fail the whole transfer on first bad block** would be safe but wastes the core benefit of multi-peer.

**Keep retrying a bad peer indefinitely** risks wasting time and bandwidth.

**Use speed scoring as a demotion trigger** was tried and removed after it could classify healthy peers incorrectly.

### Consequences

- Healthy peers are not punished for being slower.
- Bad blocks are requeued for another peer.
- Reconnects are bounded and manifest-checked.
- Session-level bans do not create permanent peer reputation side effects.

### Physical Verification

Healthy physical runs completed with zero strikes and no bans. A kill-one-peer test passed: one peer was stopped at about 25 percent progress, and the remaining peer completed the transfer at 92.8 MB/s. Unit tests cover strike state transitions and retry-only queue behavior.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_multipeer_test.go`

---

## ADR-MP09: Checkpoint, Resume, And Final Integrity Are Root-Hash Scoped

| | |
|---|---|
| **Date** | 2026-04-06 to 2026-04-21 |
| **Status** | Accepted and physically verified |
| **Commits** | 42d58f7, 674b662, f9ee530 |

### Context

Single-peer checkpoints derive their content key from the file table. Multi-peer starts from a Merkle root and a manifest served by peers, so it needs a checkpoint key that cannot collide with single-peer state and does not depend on the same file-table derivation.

The receiver also writes blocks out of order. A checkpoint must record which blocks are already present, and finalization must prove that the sparse temp file now matches the requested root.

### Decision

Use a multi-peer-specific content key:

- Derive checkpoint identity from `BLAKE3("shurli-multi-peer-v1" || rootHash)`.
- Store a bitfield of completed blocks.
- Store chunk hashes, chunk sizes, and a temp file path.
- Preallocate the temp file and write blocks with `WriteAt` at offsets computed from chunk sizes.
- Save checkpoints periodically, on cancel, on incomplete exit, and before finalization.
- Before final rename, re-read the temp file through FastCDC and verify the Merkle root.
- `fsync`, close, rename atomically, register the root hash, then remove the checkpoint.

Commit 674b662 removed a single-peer-only content-key recomputation from checkpoint loading because it always rejected multi-peer checkpoints.

### Alternatives Considered

**Use the single-peer content key** would collide conceptually with a different transfer identity model.

**Trust the bitfield at completion** is not enough. The temp file may contain corrupted resumed data.

**Assemble blocks in memory and write once** was the old memory-heavy model.

### Consequences

- Resume can skip already completed blocks.
- Final integrity does not depend on which peer served which block.
- Corrupt resumed state is detected before rename.
- Same-size re-delivery overwrites the existing final file instead of creating a duplicate suffix, matching the later duplicate-send fix.

### Physical Verification

Multi-peer cancel and resume passed after the content-key fix. One physical run cancelled at 50 percent and resumed from 73 percent, reported as 1,594 of 2,169 blocks already complete, then finished at 199.8 MB/s average. The checkpoint content-key mismatch fix was also verified with a cancel and resume sequence that loaded existing completed blocks instead of starting fresh.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_resume.go`, `plugins/filetransfer/transfer.go`

---

## ADR-MP10: Keep RaptorQ As Library Code, Not Active Protocol Behavior

| | |
|---|---|
| **Date** | 2026-03-08 to 2026-04-06 |
| **Status** | Accepted |
| **Commits** | bfd4b06, b824271, 42d58f7, 82b3466 |

### Context

RaptorQ entered Shurli as part of the original multi-source transfer work. The raw-block rewrite removed its use from `transfer_multipeer.go`, but the RaptorQ wrapper and tests still exist. That can be confusing in public docs because the repository contains working RaptorQ code while the active multi-peer path no longer calls it.

Current code search shows `newRaptorQEncoder`, `newRaptorQDecoder`, and symbol helpers are referenced by `transfer_raptorq.go` and `transfer_raptorq_test.go`, not by the active multi-peer transfer path.

### Decision

Keep the RaptorQ library and tests, but draw a hard documentation boundary:

- Do not describe RaptorQ as active multi-peer behavior.
- Do not delete the wrapper while it remains a useful tested component for future unreliable or broadcast-style transports.
- Treat `transfer_multipeer.go` as the active protocol source of truth.

### Alternatives Considered

**Delete RaptorQ immediately** would reduce confusion, but it would discard tested code that may be useful later.

**Keep RaptorQ wired as fallback** would keep a known-slower architecture in the protocol.

**Document both as active choices** would be inaccurate.

### Consequences

- Public docs can explain the historical arc without implying the active protocol uses symbols.
- Future work can reuse the RaptorQ wrapper deliberately.
- Code reviewers have a clear boundary: `transfer_raptorq.go` is retained library code; `transfer_multipeer.go` is the active raw-block protocol.

### Physical Verification

No physical speed test is attached to retaining the library. The verification is code-level: the active multi-peer file contains the raw block request/response protocol, while RaptorQ calls are confined to the RaptorQ wrapper and tests.

**Reference**: `plugins/filetransfer/transfer_multipeer.go`, `plugins/filetransfer/transfer_raptorq.go`, `plugins/filetransfer/transfer_raptorq_test.go`

---

## Public Notes

This journal intentionally omits private topology, node names, peer IDs, addresses, provider names, device identifiers, and local file paths. Performance numbers are included only where they explain an architectural decision: static RaptorQ behaving like the slowest peer, interleaved RaptorQ failing under asymmetric peers, raw-block work stealing shifting most blocks to the fast peer, kill-one-peer recovery, and checkpoint resume.
