# FT-Y: Reed-Solomon Erasure Recovery

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-24 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-RS01 to ADR-RS08 |
| **Primary Commits** | 0444aa5, 188e421, a4391f4, 831f93d |

Batch 2, Batch 2b, and Batch 2c moved Shurli from "fast streaming works when all chunks arrive" to "large transfers can recover bounded loss without restarting the whole transfer or holding the whole file in memory." The important decision was not merely "use Reed-Solomon." Shurli already had RS code. The architectural decision was to make recovery fit the streaming transfer shape.

Whole-file repair and ad hoc retries were the wrong abstraction for FT-Y. Whole-file repair made memory scale with transfer size. Retry-only repair forced the receiver to reopen transport work for missing chunks even when enough parity had already arrived. Per-stripe Reed-Solomon recovery gives the receiver a deterministic, bounded repair unit: enough data and parity for one stripe reconstructs that stripe, then the receiver can release the parity state.

This journal should be read beside [ADR-Y01 to ADR-Y10](streaming-protocol-rewrite.md), which documents the SHFT streaming protocol, and [ADR-MP01 to ADR-MP10](multi-peer-adaptive-transfer.md), which documents multi-peer scheduling. RS recovery is a reliability layer above stream delivery. It does not replace path selection, hedging, grant receipts, or relay budget enforcement.

---

## ADR-RS01: Missing Chunks Needed Bounded Repair

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-19 |
| **Status** | Accepted |
| **Commits** | 0444aa5, 188e421, a4391f4 |

### Context

The streaming rewrite let the sender produce chunks while the receiver wrote them. That removed the old manifest-first bottleneck, but it made late loss more visible. A large transfer could run for many minutes, arrive mostly intact, then discover at trailer time that a few chunks were corrupt or absent.

Restarting the whole transfer was too expensive. Treating every miss as a transport retry also left reliability coupled to the path that had just failed. The receiver already had a stronger signal: if enough parity for the affected data was present, it could repair the loss without restarting the payload stream.

### Decision

Model RS recovery as bounded repair work inside the file-transfer reliability layer:

- Data chunks remain the primary payload unit.
- Parity chunks are extra repair material, not duplicate payload streams.
- The receiver tracks corrupt and missing data by chunk index.
- Reconstruction is attempted only where the parity budget can cover the missing or corrupt shards.
- Transport retries and path selection remain responsible for opening usable streams; RS only repairs bounded data loss after chunks and parity have arrived.

This placed recovery above raw stream delivery and below the final Merkle verification. The receiver can accept a stream with some damage, reconstruct damaged chunks, verify them against claimed BLAKE3 hashes, and then let the final root check prove the assembled transfer.

### Alternatives Considered

**Restart the whole transfer** was simple, but it made a single late missing chunk as expensive as the original transfer.

**Rely only on transport retries** kept RS out of the receive path, but it did not use parity already sent over the wire.

**Duplicate payload across paths** would have consumed more relay budget and blurred the boundary between reliability and scheduling. Shurli uses Tail Slayer hedging and budget-aware relay selection for path choice, not full payload duplication across every relay.

### Consequences

- Recovery cost is proportional to the affected stripe, not the whole transfer.
- The receiver has a deterministic reason to retry or fail: missing shards must fit within available parity.
- Final integrity remains hash-based. RS reconstruction is not trusted until reconstructed bytes match the expected chunk hash and the transfer root verifies.
- Path selection remains independent. A bad path may create loss; RS repairs only bounded loss.

### Physical Verification

The large-file verification for this work transferred 8 GB over a direct cross-network IPv6 WAN path with RS enabled. The repaired transfer completed with matching sha256. The same verification window measured the memory change that made bounded repair viable: the older accumulate-all design peaked at 2074 MB RSS, while the per-stripe design peaked at 96 MB RSS.

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_erasure.go`, `plugins/filetransfer/transfer_parallel.go`

---

## ADR-RS02: Whole-File Erasure Coding Was Rejected

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-19 |
| **Status** | Accepted |
| **Commits** | 0444aa5, a4391f4 |

### Context

The pre-Batch 2 shape buffered too much RS state. Commit 0444aa5 replaced the `rawForRS` plus batched `encodeErasure` path with an incremental `erasureEncoder`. Commit a4391f4 then removed the receiver-side accumulate-all parity lifecycle in favor of per-stripe parity slots.

Whole-file erasure coding was attractive because it was conceptually simple: collect all data, encode all parity, and reconstruct after the complete manifest is known. That simplicity worked against Shurli's streaming goals. Large files should not require the sender or receiver to retain repair state proportional to total transfer size.

### Decision

Reject whole-file RS as the active FT-Y recovery model. Shurli now treats the transfer as a stream of independent stripes:

- The sender encodes parity as each stripe fills.
- Full-stripe parity can be emitted before the whole transfer is chunked.
- The receiver can store parity per stripe and free it when the stripe is clean or repaired.
- Trailer-time reconstruction is a sweep over remaining damaged stripes, not a whole-file decode phase.

### Alternatives Considered

**Whole-file encode and decode** minimized conceptual moving parts, but it delayed repair decisions and made memory scale with the transfer.

**Keep legacy batched encode with receiver-side improvements only** would have improved one side of the transfer while leaving sender memory and latency coupled to total file size.

**Lower the maximum file size or chunk tier** would hide the symptom by avoiding large RS workloads. FT-Y needed the large-file path to be viable, not smaller by policy.

### Consequences

- The sender's encoder holds only the current stripe's raw chunks before producing parity.
- The receiver no longer needs a flat parity map for all parity in the transfer.
- The old `encodeErasure` path and old manifest-shaped `rsReconstruct(*transferManifest)` design were replaced by streaming-state reconstruction.
- Repair decisions can happen as data arrives for full stripes, with a trailer sweep for partial or deferred stripes.

### Physical Verification

The 8 GB WAN+RS verification was the key rejection test for whole-file-style repair. The older accumulate-all design peaked at 2074 MB RSS. The per-stripe design peaked at 96 MB RSS and completed with sha256 match. Throughput for the verified 8 GB run was 6.7 MB/s over 24m24s. Those numbers prove the memory property, not a universal network benchmark.

**Reference**: `plugins/filetransfer/transfer_erasure.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_erasure_test.go`

---

## ADR-RS03: Per-Stripe Reed-Solomon Became The Recovery Unit

| | |
|---|---|
| **Date** | 2026-04-19 |
| **Status** | Accepted |
| **Commit** | a4391f4 |

### Context

Once whole-file repair was rejected, the recovery unit still needed to be explicit. The receiver needed to know which parity belongs to which data, when enough shards exist, and when that state can be released. Without a recovery unit, parity storage either leaks until trailer time or becomes hard to reason about under out-of-order delivery.

Current code uses a default full stripe of 100 data chunks. At the default 10% overhead, that produces 10 parity chunks for a full stripe. The exact parity count is computed by `stripeParityCount`, which is shared by sender and receiver so the layout cannot drift.

### Decision

Use one RS stripe as the recovery unit. Each stripe has independent lifecycle state:

- `paritySlot` stores parity by local parity index for one stripe.
- `stripeDataCounts` tracks how many data chunks for that stripe have arrived.
- `tryEagerReconstruct` runs when a full stripe has both all data positions accounted for and enough parity.
- Clean stripes free parity without decoding.
- Damaged stripes call `reconstructSingleStripe`, which uses `ReconstructSome` to rebuild only required data shards.
- `rsReconstruct` performs a trailer-time sweep for damaged stripes that were not eagerly handled.

### Alternatives Considered

**One global parity pool** was easy to index, but it forced the receiver to retain repair state across the transfer.

**Per-chunk retry records only** described what was missing but not what parity could repair it.

**Reconstruct every stripe at trailer time** avoided eager concurrency questions but kept memory high for clean stripes and delayed repair decisions.

### Consequences

- Memory is bounded by active parity slots and parity bytes, not total parity for the file.
- Recovery failures are local. If one stripe exceeds its parity budget, the error identifies that stripe instead of invalidating unrelated work.
- Full stripes can be released during transfer; the partial final stripe is handled by the trailer sweep.
- The code can reuse an RS encoder for full-stripe reconstruction while creating fresh encoders for partial stripes when needed.

### Physical Verification

The per-stripe receiver design is what produced the 21x RSS improvement in the 8 GB WAN+RS test: 2074 MB peak RSS before, 96 MB peak RSS after, with sha256 match. The result validates the stripe as the right memory boundary. It does not claim the same throughput on every network path.

**Reference**: `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_erasure.go`, `plugins/filetransfer/transfer_parallel.go`

---

## ADR-RS04: Recovery Stayed Compatible With Streaming Transfer

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-24 |
| **Status** | Accepted |
| **Commits** | 0444aa5, 188e421, a4391f4, 831f93d |

### Context

ADR-Y moved Shurli to a streaming SHFT protocol: header first, chunk frames in the middle, trailer last. RS recovery had to fit that pipeline. A separate whole-file repair phase would have undone the streaming rewrite by forcing the receiver to accumulate state until the end.

The active implementation keeps the streaming shape intact. The sender writes erasure stripe parameters in the header when RS is active. Data and parity then travel as chunk frames. The trailer carries final integrity data and RS metadata that is only known after chunk production finishes.

### Decision

Keep RS as an in-band extension of the streaming transfer:

- `writeHeader` and `readHeader` carry `StripeSize` and `OverheadPerMille` when `flagErasureCoded` is set.
- `chunkProducer` feeds the incremental `erasureEncoder`.
- Parity chunks travel as normal chunk frames using the `parityFileIdx` sentinel.
- `processIncomingChunk` is the single receive path for data and parity from the control stream or parallel worker streams.
- `readChunkGlobal` reads intact stripe-mates from temporary files for reconstruction, so the receiver does not need to keep all data chunks in memory.
- Final Merkle verification remains after reconstruction.

### Alternatives Considered

**Trailer-only erasure parameters** were too late for bounded parity storage. The receiver needs stripe configuration before the first parity chunk arrives.

**A separate repair stream after the transfer** would preserve a clean data path, but it would create another protocol phase and lose the benefit of parity already received.

**Inline RS decode on every chunk** would be wasteful. Most stripes are clean and only need parity release, not decoding.

### Consequences

- RS recovery is compatible with single-stream and parallel-stream receive paths.
- The receiver can route parity immediately because the header already provided stripe metadata.
- Eager reconstruction remains optional. Trailer-time sweep provides a deterministic fallback.
- Final transfer integrity is still anchored by chunk hashes and the Merkle root.

### Physical Verification

Both physical recovery tests used the streaming path. The 8 GB WAN+RS test completed with sha256 match while retaining per-stripe memory behavior. The Batch 2c relay test transferred 50 MB through a relay with test hooks dropping chunks 1 and 3; the receiver populated two missing chunks from the trailer manifest, reconstructed them, and completed with sha256 match.

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_parallel.go`, `plugins/filetransfer/transfer_erasure.go`

---

## ADR-RS05: Recovery Metadata Needed Explicit Structure

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-24 |
| **Status** | Accepted |
| **Commits** | 188e421, a4391f4, 831f93d |

### Context

The receiver cannot reconstruct a missing data chunk from parity unless it knows two things about that chunk: the claimed hash and the decompressed size. Corrupted chunks already have that metadata because `recordChunk` runs before hash verification. Truly missing chunks have no frame, so they need metadata from somewhere else.

Batch 2b also found that the receiver must use the sender's configured overhead, not a hardcoded 10% assumption. Option C moved stripe configuration to the header. Batch 2c added per-data-chunk metadata to the erasure trailer.

### Decision

Make RS recovery metadata explicit and split it by when the receiver needs it:

- Header: `StripeSize` and `OverheadPerMille`, available before any chunks arrive.
- Chunk frames: data hash, decompressed size, and chunk index for chunks that arrive.
- Trailer: parity count, parity hashes, parity sizes, plus `ChunkHashes` and `ChunkSizes` for data chunks.
- Receive state: `corruptedChunks`, `hashes`, `sizes`, `receivedBitfield`, and per-stripe parity slots.

For missing chunks, `transfer_parallel.go` populates hash and size from the trailer manifest before the missing-chunk gate. Those chunks are marked as corrupted so the same RS path handles both bit-flip corruption and absent frames.

### Alternatives Considered

**Hardcode erasure overhead on the receiver** failed for non-default configurations and was fixed by carrying `OverheadPerMille`.

**Recover only chunks whose frames arrived corrupted** was Batch 2b's honest boundary, but it could not repair dropped frames. Batch 2c added the trailer chunk manifest to close that gap.

**Put all recovery metadata in the trailer** would delay stripe setup and force the receiver back toward accumulate-all parity storage.

### Consequences

- The receiver can initialize stripe state before payload frames arrive.
- Missing chunk recovery is deterministic when the trailer manifest exists.
- Legacy or absent chunk manifests still fail cleanly for truly missing data.
- The trailer manifest is not authoritative by itself. Reconstructed bytes must hash to the claimed chunk hash, and the final Merkle root must verify.

### Physical Verification

Batch 2c physically verified the metadata path with a 50 MB relay transfer. Test hooks dropped chunks 1 and 3. The receiver logged that it populated missing chunks from the trailer manifest with count=2, RS reconstructed the data, and the final sha256 matched. The run completed in 13s at 3.8 MB/s. This relay-path test is separate from the 8 GB direct WAN memory test.

**Reference**: `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_parallel.go`, `plugins/filetransfer/transfer_erasure.go`

---

## ADR-RS06: Receiver Memory Was A Design Constraint

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-19 |
| **Status** | Accepted |
| **Commits** | 0444aa5, a4391f4 |

### Context

Large transfer recovery is only useful if recovery state does not become the reason the daemon dies. RS can allocate large shard buffers during encode and decode, especially with the top adaptive chunk tier. The code therefore treats receiver memory as a first-class protocol constraint, not an implementation afterthought.

The receiver stores parity in per-stripe `paritySlot` entries. `initPerStripeState` computes a dynamic inflight cap from parity-per-stripe and the global parity byte budget. `processIncomingChunk` enforces both an active-stripe limit and a parity-byte limit before accepting more parity.

### Decision

Bound receiver memory through layered constraints:

- Accept stripe sizes only in the range 2 to `maxAcceptedStripeSize`.
- Cap `maxAcceptedStripeSize` at 200, which is 2x the default stripe size.
- Cap erasure overhead at 50%.
- Bound parity chunk indexes by `maxParityCount`.
- Bound active parity slots through `maxInflightStripes`.
- Bound aggregate parity bytes through `maxParityBudgetBytes`.
- Free parity immediately when a clean stripe completes or reconstruction succeeds.

### Alternatives Considered

**Trust the sender's declared stripe size** would let a malicious or buggy sender force large receiver allocations.

**Use only a byte budget** would bound total bytes but still allow pathological stripe layouts that are expensive to reconstruct.

**Keep parity until final verification** would simplify cleanup but would keep memory proportional to transfer length for clean data.

### Consequences

- Receiver memory scales with active stripes and shard size, not file size.
- Malicious stripe sizes and parity floods fail before reconstruction allocation.
- Clean stripes are cheap: once all data arrives and no corruption exists, parity is dropped in O(1).
- Reconstructing damaged stripes spends memory only for the affected stripe.

### Physical Verification

The 8 GB direct WAN+RS run verified the receiver-memory goal. The previous accumulate-all design peaked at 2074 MB RSS. The per-stripe receiver peaked at 96 MB RSS, a 21x improvement, while completing with sha256 match. The measured result validates bounded receiver state for the tested workload and path.

**Reference**: `plugins/filetransfer/transfer_erasure.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_parallel.go`

---

## ADR-RS07: Recovery Needed Physical And Adversarial Verification

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-24 |
| **Status** | Accepted |
| **Commits** | 188e421, a4391f4, 831f93d |

### Context

RS recovery sits on a trust boundary. The sender declares stripe parameters, emits parity, and later sends recovery metadata. The receiver must use that metadata to repair data without letting malformed metadata become a memory or integrity attack.

Batch 2b self-audit found three security issues that had to be fixed before treating RS recovery as production-ready: corrupted checkpoint have-bits could cause cross-session silent corruption, out-of-range corrupted indexes could kill recovery, and oversized stripe declarations could force dangerous reconstruction allocations.

### Decision

Promote RS recovery only after both physical and adversarial verification:

- Checkpoint save clears have-bits for corrupted chunks so resume retransmits them instead of trusting empty or stale temp bytes.
- `rsReconstruct` filters out-of-range corrupted indexes before grouping by stripe.
- `readHeader` rejects stripe sizes above `maxAcceptedStripeSize`.
- `processIncomingChunk` enforces parity count and byte budgets before accepting parity.
- Reconstruction verifies RS output with the encoder, verifies each reconstructed chunk hash, then relies on final Merkle verification for transfer-level integrity.

### Alternatives Considered

**Rely on final Merkle verification only** would catch some bad output too late and would not prevent memory exhaustion.

**Persist corrupted chunk state across sessions** would add checkpoint complexity. Clearing have-bits is simpler and makes resume ask for plain retransmission.

**Treat malformed recovery metadata as ordinary loss** would hide active abuse. Metadata that violates bounds fails the transfer.

### Consequences

- Recovery cannot silently bless corrupted temp data across sessions.
- A malicious frame cannot use an impossible chunk index to make otherwise recoverable data fail.
- Sender-provided stripe size is bounded before allocation.
- Physical verification covers both the large-file memory story and the missing-frame recovery story.

### Physical Verification

Two separate physical tests anchored this decision. The 8 GB direct WAN+RS test verified large-file per-stripe behavior: 96 MB peak RSS after the redesign, sha256 match, 24m24s at 6.7 MB/s. The Batch 2c relay test verified missing-frame recovery: 50 MB through a relay, chunks 1 and 3 dropped by test hooks, trailer manifest populated count=2, RS reconstruction succeeded, sha256 matched, 13s at 3.8 MB/s. These tests used different network paths and should not be compared as throughput benchmarks.

**Reference**: `plugins/filetransfer/transfer_resume.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_erasure.go`, `plugins/filetransfer/transfer_parallel.go`

---

## ADR-RS08: Boundaries And Non-Goals

| | |
|---|---|
| **Date** | 2026-04-17 to 2026-04-24 |
| **Status** | Accepted |
| **Commits** | 0444aa5, 188e421, a4391f4, 831f93d |

### Context

RS recovery is powerful enough that it can be mistaken for a general replacement for transport reliability, path selection, and relay policy. That would make the design worse. RS has a narrow job: repair bounded data loss when enough validated parity and metadata exist.

Other FT-Y systems solve different problems. Tail Slayer hedging opens viable paths. Multi-peer scheduling decides who serves work. Grant receipts and budget-aware relay selection decide whether a relay path is affordable and allowed. RS should not absorb those responsibilities.

### Decision

Keep these boundaries explicit:

- RS recovery does not replace health scoring, Tail Slayer hedging, grant receipts, budget-aware relay selection, or multi-peer scheduling.
- RS recovery does not duplicate the full payload across every relay to use all available paths.
- RS recovery does not guarantee repair beyond the parity budget.
- RS recovery does not make stale, malformed, or corrupted metadata authoritative.
- Selective file rejection remains constrained when erasure is active because RS uses global chunk layout and cross-file stripe mates.

### Alternatives Considered

**Let RS drive path choice** would conflate repair with routing. The receiver can repair only after data and parity arrive; it cannot choose the best path before transfer.

**Use parity as a substitute for relay budget awareness** would waste relay capacity. Parity is overhead, not free bandwidth.

**Treat trailer metadata as final truth** would skip the integrity model. Metadata helps reconstruction, but chunk hashes and the Merkle root remain the proof.

### Consequences

- RS stays small enough to reason about and test.
- Scheduling and routing features can evolve without changing the RS recovery contract.
- Operators can understand failures: parity budget exceeded, metadata invalid, path failed, or relay budget exhausted are separate states.
- Future erasure work must preserve validation before trusting recovered bytes.

### Physical Verification

The two physical tests demonstrate the boundary. The 8 GB direct WAN test proves large-file recovery memory and integrity. The 50 MB relay test proves missing-frame reconstruction using trailer metadata. Neither test claims that RS replaces relay selection or multi-peer scheduling, and neither duplicates full payload across all paths.

**Reference**: `plugins/filetransfer/transfer_grants.go`, `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_stream.go`, `plugins/filetransfer/transfer_parallel.go`, `pkg/sdk/service.go`

---

## Public Notes

This journal omits private topology, node names, peer IDs, addresses, hostnames, provider details, device names, file names, usernames, and local paths. Performance numbers are included only where they explain architecture: the 8 GB direct WAN+RS run proves per-stripe receiver memory behavior, and the 50 MB relay run proves missing-frame reconstruction through the trailer manifest. These tests used different network paths, so their throughput numbers are contextual validation data, not comparative benchmarks. A formal benchmark report with controlled methodology and reproducible conditions is separate from this architecture record.
