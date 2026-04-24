---
title: "FT-Y - Streaming Protocol Rewrite"
weight: 31
description: "Streaming SHFT protocol rewrite with global chunk space, directory transfer, hash probe split, compression detection, adaptive chunks, and cross-session resume."
---
<!-- Auto-synced from docs/engineering-journal/streaming-protocol-rewrite.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-03-31 to 2026-04-03 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-Y01 to ADR-Y10 |

The FT-Y streaming protocol rewrite replaced Shurli's manifest-first file transfer path with a streaming, directory-aware, checkpointable pipeline. The goal was not only higher speed. The real architectural change was that the sender no longer had to finish building every chunk and every hash before the receiver could start accepting data.

Later FT-Y work built on this foundation, including multi-peer scheduling, path failover, larger chunks, and erasure recovery. Those systems have their own journals. This record stays focused on the streaming protocol rewrite itself: why the old shape had to go, how the new wire shape works, and what trade-offs were accepted.

---

## ADR-Y01: Replace the Accumulate-All Manifest Protocol

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commits** | 8f73963, db1f588 |

### Context

The old file transfer path was manifest-first. The sender chunked the whole input, computed every chunk hash, built a manifest containing the final Merkle root, then started moving payload bytes. That made the transfer wall clock roughly `chunking + hashing + compression + network`, instead of overlapping disk, CPU, and network work.

It also made directories awkward. Directory transfer was built out of per-file flows, which meant per-file stream setup, per-file approval semantics, and no content-defined chunking across small file boundaries. The protocol could transfer files, but it could not behave like one continuous content stream.

### Decision

Replace the manifest-first wire path with:

1. A header containing the file table, total size, flags, transfer ID, and optional erasure parameters.
2. Self-describing chunk frames carrying global offset, chunk index, BLAKE3 hash, decompressed size, and wire data.
3. A trailer containing the final chunk count, Merkle root, sparse hashes when needed, and erasure metadata when active.

The receiver starts after the header instead of waiting for a complete manifest. The final root still exists, but it moves to the trailer because it is only known after chunking completes.

### Alternatives Considered

**Keep the old manifest format and tune internals** would preserve compatibility, but it could not solve the serial dependency on a complete manifest.

**Two-pass transfer, hash first and send second** would make the manifest available before data, but it doubles disk reads and still prevents a true streaming pipeline.

**Add a channel between old chunking and old sending** was tested and rejected. The old shape still needed all hashes before the manifest could be sent, and the shared channel design showed heavy goroutine scheduling cost.

### Consequences

- The sender can pipeline production and transmission.
- The receiver can allocate and write files from a file table before the Merkle root is known.
- The root hash remains the final integrity boundary, but verification happens at trailer time.
- Wire compatibility with the old manifest path was intentionally dropped.
- The protocol became a better base for directory transfer, resume, and parallel worker streams.

### Physical Verification

Profiling before the rewrite showed that chunking and sending were not overlapping. A channel-based attempt under the old manifest model fell to 77 MB/s because scheduling overhead replaced the original bottleneck. After the streaming rewrite and physical retest, single-node LAN send measured 111 MB/s, single-stream download measured 125 MB/s, and relay transfer measured about 4 MB/s. The same retest verified hash matches on single files, real media files, nested directories, and edge-case filenames.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_parallel.go`

---

## ADR-Y02: No Backward Compatibility for the Rewrite

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | 8f73963 |

### Context

The wire format changed completely: manifest-first became header, stream chunks, trailer. A compatibility layer would have required the receiver to support two transfer grammars, two checkpoint interpretations, two worker-stream routing schemes, and old manifest parsing in the hot path.

The project was still pre-release, and all active deployments were controlled. The directive was explicit: `NO backward compat. Keep version 1.` In code terms, the FT-Y rewrite did not introduce a new negotiated file-transfer protocol generation or a second SHFT wire version for compatibility. It changed the active implementation in place.

### Decision

Do not implement mixed-version negotiation. Do not carry old manifest/chunk readers on the file-transfer wire path. Rebuild controlled nodes together and fail loudly if an old node speaks the old grammar.

Checkpoint compatibility was handled differently. Old or incompatible checkpoints are local disk state, not peer protocol state, so they are discarded by magic/version checks rather than negotiated over the network.

### Alternatives Considered

**Bump the protocol and keep both implementations** would reduce operator error during mixed deployments, but it would keep dead code alive in a security-sensitive path.

**Add automatic downgrade** would make behavior harder to reason about and would hide stale deployments until they hit a less obvious edge case.

**Keep the old parser only for reads** would still require the receiver to allocate and validate two formats.

### Consequences

- The active transfer path stayed small enough to audit.
- Old nodes fail fast instead of silently producing partial or corrupt transfers.
- The decision assumes controlled deployment discipline.
- Public docs must explain that this was a pre-release rewrite decision, not a stable-network compatibility promise.

### Physical Verification

The post-rewrite deployment was tested as a same-generation rollout. Physical retest covered LAN send, LAN download, relay transfer, directory transfer, compression detection, cancel, and resume. No mixed-version success path was attempted because accepting mixed wire generations was explicitly rejected.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_resume.go`

---

## ADR-Y03: Streaming File Table and Global Chunk Space

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | 8f73963 |

### Context

Directory transfer needs one approval decision, one content identity, and one continuous chunk stream. Per-file streams make small directories inefficient, and per-file content-defined chunking prevents chunks from crossing file boundaries. That is exactly where content-defined chunking is most useful: many small files can be packed into fewer chunks.

The receiver also needs to write a chunk that may span more than one file. A chunk frame cannot simply say "file index plus local offset" when its bytes cross a boundary.

### Decision

Represent a file or directory as one global byte stream:

1. Build a sorted file table with path, size, mode, and modification time.
2. Use `multiFileReader` to present all regular files as one `io.Reader`.
3. Chunk the concatenated stream with FastCDC.
4. Put global offset and decompressed size in every chunk frame.
5. Use `globalToLocal` on the receiver to split each global chunk range into per-file writes.

The `fileIdx` in a chunk frame is a hint for the first file touched by the chunk. The authoritative placement comes from global offset plus the file table's cumulative offsets.

### Alternatives Considered

**One stream per file** was easy to understand but kept directory transfer as a set of many small transfers.

**One manifest per directory but per-file chunks** would reduce stream setup but would still waste wire overhead on many small files.

**Put per-file slice metadata in every chunk frame** would avoid recomputing placement, but it would enlarge every frame and duplicate information already derivable from the file table.

### Consequences

- Directory transfer uses one continuous piece flow.
- Chunks can cross file boundaries without corrupting placement.
- Empty files still exist in the file table and are finalized as zero-byte files.
- File ordering must be deterministic, so the sender sorts the table and checkpoint keys depend on that order.
- Selective rejection becomes more subtle because a boundary chunk may include accepted and rejected file bytes.

### Physical Verification

A nested 27-file directory transferred at 54.6 MB/s on the post-rewrite LAN retest. Edge cases also passed: empty file creation, Unicode filenames, spaced filenames, and nested subdirectories. Later cross-file corruption testing verified that a chunk spanning file boundaries could be mapped back to the correct per-file outputs.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`

---

## ADR-Y04: Parallel Producer/Consumer Streaming Pipeline

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | 8f73963 |

### Context

Once the wire format no longer needed a complete manifest up front, the sender needed an actual pipeline. The producer reads, chunks, hashes, optionally compresses, and emits chunk frames. The sender side then distributes those frames to one control stream and optional worker streams. The receiver side must merge control and worker chunks without accepting trailer verification before the workers are drained.

The key risk was ordering. The trailer lives on the control stream. Worker streams may still have buffered chunks when the trailer arrives.

### Decision

Use bounded producer and worker queues:

- `chunkProducer` emits `streamChunk` values into a bounded channel.
- `sendParallel` opens worker streams, sends a transfer-ID worker hello, and round-robins chunks across per-stream channels.
- A `sync.WaitGroup` waits for every worker to flush before the trailer is written.
- `receiveParallel` registers a session by transfer ID, verifies worker peer identity, merges worker chunks into the shared receive state, and drains worker streams before trailer verification.
- Queue depths are deliberately bounded to preserve backpressure.

### Alternatives Considered

**Single stream only** was simpler, but it left LAN capacity unused on transfers with enough chunks.

**One shared channel consumed by all workers** was measured and abandoned because scheduler overhead became a bottleneck.

**Write the trailer as soon as the producer finishes** was unsafe because worker streams could still be flushing chunks.

### Consequences

- The sender can overlap file reading, chunking, hashing, compression, and network writes.
- Worker streams are optional. The code falls back to the control stream when parallel setup fails.
- Backpressure is explicit, not accidental.
- Receiver progress must count unique chunks, not highest chunk index, because chunks arrive out of order.
- Cancellation and cleanup must reset both control and worker streams.

### Physical Verification

Post-rewrite LAN send measured 111 MB/s with eight streams. Single-stream download measured 125 MB/s, which showed that the new single-stream path remained efficient while the parallel path could use multiple streams when useful. Physical testing also found and fixed a worker flush bug where tiny compressed chunks could remain buffered unless worker streams used `CloseWrite` before close.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_parallel.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`

---

## ADR-Y05: Split Hash Probe from Download Request

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commit** | 8f73963 |

### Context

The old multi-peer probe path expected to read a manifest and extract its root hash before starting a transfer. Streaming moved the root hash to the trailer, so a normal download stream cannot provide the root until after all data has moved. That broke the probe abstraction.

The old request helper also mixed control flow and data flow by returning "ready" error values. This made success states look like errors, which made the download protocol harder to reason about.

### Decision

Add an explicit request type to the download protocol:

- `requestType=0x01`: full download. The sharer calls `SendFile`.
- `requestType=0x02`: hash probe. The sharer chunks the file, computes the Merkle root, and returns a fixed 45-byte response.

`RequestDownload` and `RequestProbe` now return normal Go values and errors. `handleHashProbe` opens files through `os.Root`, chunks within the jailed root, checks context cancellation, and rate-limits probe requests because a probe can require a full file read.

### Alternatives Considered

**Let probes start a full transfer and stop after the trailer** defeats the purpose of a metadata probe.

**Keep returning special error values** preserved the old helper shape, but it kept an errors-as-values anti-pattern in a security-sensitive protocol.

**Precompute and cache every shared hash** could make probes fast, but it adds invalidation complexity and does not solve the protocol boundary.

### Consequences

- Metadata queries and data transfers are separate operations.
- Multi-peer code can ask "what root hash is this file?" without starting a payload transfer.
- The hash-probe path pays chunking cost, but it is bounded, rate-limited, and explicit.
- `os.Root` removes the stat-then-open time-of-check/time-of-use gap in probe handling.

### Physical Verification

The probe split was verified through the post-rewrite multi-peer and download paths: probe requests return a root hash without streaming payload bytes, while download requests continue into SHFT streaming. The response is fixed size: one marker byte, 32 bytes of root hash, 8 bytes of total size, and 4 bytes of chunk count.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/share.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`

---

## ADR-Y06: Three-Chunk Incompressible Detection

| | |
|---|---|
| **Date** | 2026-03-31 |
| **Status** | Accepted |
| **Commit** | 6c57d15 |

### Context

Profiling showed zstd work on data that did not compress. Random data, archives, and media often produce a 1.0:1 ratio, but the old path still paid encoder cost chunk after chunk. On fast local transfers, that CPU cost was visible in throughput.

Compression still needed to stay on by default because zero-fill, logs, and text-like data compress extremely well.

### Decision

Use an early probe:

1. Try zstd on the first three chunks.
2. Treat any chunk with compressed length greater than or equal to 95 percent of raw length as not worth compressing.
3. If all three probe chunks fail to compress, disable compression for the rest of the transfer.
4. Keep per-chunk framing self-describing so the receiver can handle compressed and raw chunks correctly.

### Alternatives Considered

**Disable compression by default** would improve random/media transfer speed but would waste bandwidth on highly compressible data.

**Add a user-facing compression knob as the primary solution** would move an implementation detail to the operator and make defaults worse.

**Probe the entire file first** would make the decision more accurate but would reintroduce a serial pre-transfer pass.

### Consequences

- Incompressible transfers stop paying zstd cost after three chunks.
- Compressible transfers still benefit from zstd.
- Compression remains automatic and does not add a CLI tuning surface.
- The decision is local to each transfer, so mixed workloads behave correctly.

### Physical Verification

The pprof session measured 107 MB/s with pooled zstd still compressing random data. Three-chunk incompressible detection raised the average to 134 MB/s, with a 167 MB/s peak. That was 78 percent of the 214-220 MB/s SCP comparison in that test environment. Compression detection also distinguished the two extremes correctly: zero-fill compressed at 8456:1, while random data stayed at 1.0:1 and disabled zstd after the probe.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/compress.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`

---

## ADR-Y07: Adaptive Chunk Sizes Without a Config Knob

| | |
|---|---|
| **Date** | 2026-04-17 |
| **Status** | Accepted |
| **Commit** | de2ded7 |

### Context

The original chunk targets were too small for large files. More chunks means more frame headers, more hash map entries, more bitfield work, more progress updates, and more goroutine scheduling. But making every file use large chunks would hurt small-file granularity and resume efficiency.

### Decision

Make chunk sizing adaptive by file size inside `ChunkTarget`:

| File Size | Min | Avg | Max |
|-----------|-----|-----|-----|
| < 64 MB | 64 KB | 128 KB | 256 KB |
| < 512 MB | 128 KB | 256 KB | 512 KB |
| < 2 GB | 256 KB | 512 KB | 1 MB |
| < 8 GB | 512 KB | 1 MB | 2 MB |
| >= 8 GB | 1 MB | 2 MB | 4 MB |

No user-facing config knob was added. The file size already contains the signal needed to choose the tier, and exposing a knob would create compatibility and support problems without solving an operator-facing issue.

### Alternatives Considered

**One global larger chunk size** would reduce overhead for large files but punish small files and small resumes.

**Expose chunk size in config** would make performance depend on local tuning and would create mismatched expectations across peers.

**Keep the old size until a full benchmark suite existed** would leave a known overhead in place after the streaming rewrite had already made larger chunks safe.

### Consequences

- Large files use fewer chunks and less per-chunk overhead.
- Small files keep finer granularity.
- `maxChunkWireSize`, zstd window size, worker buffer depth, producer buffer depth, and checkpoint versioning are coupled to chunk tiers and must be reviewed together.
- Resume granularity becomes at worst 4 MB in the largest tier, which is acceptable for both LAN and relay paths.

### Physical Verification

Regression testing after the chunk-tier change transferred 100 MB, 1 GB, and 2 GB random files end-to-end with matching hashes. Receiver memory stayed flat relative to file size: a 2 GB transfer peaked around 66 MB resident set size, roughly 12 MB above idle baseline. The result confirmed that larger chunks did not reintroduce accumulate-all receiver memory behavior.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/chunker.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_parallel.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_resume.go`

---

## ADR-Y08: Checkpoint Format for Cross-Session Resume

| | |
|---|---|
| **Date** | 2026-04-01 to 2026-04-17 |
| **Status** | Accepted |
| **Commits** | 8f73963, de2ded7, 188e421 |

### Context

The old checkpoint key used the root hash. Streaming does not know the root hash until the trailer, but the receiver needs to decide whether it can resume immediately after reading the header. That makes a root-hash checkpoint key unusable for the new protocol.

Chunk boundaries also changed during FT-Y. A checkpoint written under one chunking scheme can be actively harmful under another because the receiver may claim it has chunks that no longer align with the sender's current chunk stream.

### Decision

Use an explicit checkpoint format:

- Magic bytes `SHCK` plus a checkpoint version.
- `contentKey` derived with BLAKE3 from deterministic file table data.
- File table, total size, flags, received bitfield, per-chunk hashes, per-chunk sizes, and temp paths.
- Atomic save through tmp file plus rename.
- Version mismatch discard rather than best-effort interpretation.
- Temp-file cleanup from the checkpoint's own recorded paths when a checkpoint is discarded.

The content key is not a security proof. It is a stable local resume key that lets a new session find the same partially received content before the final Merkle root is known.

### Alternatives Considered

**Keep root hash as the checkpoint key** would make resume unavailable until after the transfer was already complete.

**Use transfer ID as the key** would only resume within one session. A reconnect creates a new transfer ID.

**Try to interpret old checkpoints** risks mapping old chunk boundaries onto new chunk boundaries. Discarding is safer and simpler.

### Consequences

- Resume works across sessions and across retry streams.
- Old checkpoints fail cleanly instead of corrupting new transfers.
- Merkle verification remains the final integrity check.
- Checkpoint files store enough per-chunk metadata to verify resumed transfers without re-reading every completed chunk.
- Later features can bump the checkpoint version again when checkpoint semantics change.

### Physical Verification

Cancel and resume was physically verified on a 500 MB transfer. The receiver preserved temp data and checkpoint state, then resumed by sending only the remaining portion: 160.4 MB on wire, 249.9 MB/s effective resume speed. The same test confirmed content-key matching across sessions. Later corruption/resume testing verified that corrupted chunks are cleared from the checkpoint have-bit so a resumed session asks the sender for clean data instead of trusting bad temp-file state.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_resume.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`

---

## ADR-Y09: Pool zstd and Disable Reed-Solomon on Verified LAN

| | |
|---|---|
| **Date** | 2026-03-31 |
| **Status** | Accepted |
| **Commit** | 6c57d15 |

### Context

The first pprof session found two independent performance bugs. First, zstd encoders and decoders were being recreated per chunk, causing extreme allocation churn. Second, Reed-Solomon erasure coding was active on a local path because transport classification treated a direct non-relay path as WAN even when another connection proved the peers were on the same local network.

Erasure is useful on unreliable WAN and relay paths. On verified LAN it is unnecessary overhead.

### Decision

Pool zstd encoders and decoders with `sync.Pool`, and make erasure transport-aware:

- Use pooled zstd encoder and decoder instances.
- Keep decompression output bounded to defend against compression bombs.
- Ask the network layer whether a peer has a verified LAN connection.
- Disable erasure when the peer is verified LAN.
- Treat unverified paths conservatively as non-LAN for correctness.

Later LAN classification work refined this from bare private-address checks to mDNS-verified LAN signal, which prevents routed-private and tunnel paths from being misclassified as local.

### Alternatives Considered

**Disable zstd entirely** would avoid encoder cost but lose huge wins on compressible data.

**Keep Reed-Solomon on every direct path** preserved redundancy but wasted CPU and wire bytes on stable local transfers.

**Classify by private IP address alone** was rejected because private ranges can appear in routed, carrier, tunnel, and container networks.

### Consequences

- The fast local path stops paying erasure overhead.
- zstd allocation churn is removed from the hot path.
- Transport classification becomes a correctness dependency, not just an optimization.
- When LAN verification is unavailable, the system chooses safety over speed and keeps erasure available.

### Physical Verification

The pprof session improved 500 MB incompressible transfer speed from 44.1 MB/s to 107 MB/s after zstd pooling, a 2.4x gain. After erasure was correctly disabled for verified LAN, local speed remained about 106 MB/s without unnecessary parity overhead. Later memory profiling found that zstd's default 8 MB window could bloat pooled encoder memory, so the window was bounded at 1 MB while keeping the pool.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/compress.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/plugin.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/plugin_policy.go`

---

## ADR-Y10: Delete the Old Wire Path Instead of Parking It

| | |
|---|---|
| **Date** | 2026-04-01 |
| **Status** | Accepted |
| **Commits** | 8f73963, db1f588 |

### Context

After the streaming path was wired end to end, the old manifest/chunk functions were no longer the active file-transfer wire path. Leaving them in place would create two problems: future audits would have to reason about dead code, and tests might accidentally keep validating the old design instead of the new protocol.

The remaining `transferManifest` type still has a job in the independent multi-peer manifest protocol. That is separate from the single-peer SHFT wire path.

### Decision

Delete the old single-peer wire helpers from the active path:

- Remove `rawChunk`.
- Remove old chunk-frame read/write helpers.
- Remove `sendChunked`.
- Remove the dead streaming receive helper after `receiveParallel` became the only receive path.
- Keep manifest serialization only where the multi-peer protocol still uses it.

### Alternatives Considered

**Keep old helpers for reference** makes code archaeology easier, but git already preserves history.

**Leave old tests and mark them legacy** would keep test coverage for behavior the product no longer ships.

**Hide the old path behind a flag** would violate the no-backward-compatibility decision and create an unsupported mode.

### Consequences

- The active transfer path is smaller and easier to audit.
- Tests exercise the streaming protocol, not the retired format.
- Multi-peer manifest code remains because it is a different protocol with a current caller.
- Future contributors do not have to guess which wire path is authoritative.

### Physical Verification

The dead-code sweep removed roughly 240 lines of old single-peer wire code and rewrote tests around `writeStreamChunkFrame`, `readStreamChunkFrame`, `writeTrailer`, `readTrailer`, and `receiveParallel`. A code search now finds the old manifest constants only as legacy message IDs or as data structures used by the separate multi-peer manifest protocol, not as the active single-peer SHFT send path. Post-migration retest after moving the engine into `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer` showed no protocol regression: LAN send, LAN download, relay transfer, directory transfer, compression detection, and resume all passed.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_stream.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_parallel.go`, `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_multipeer.go`

---

## Public Notes

This journal intentionally omits private node names, peer IDs, addresses, providers, hardware identifiers, file paths, and topology details. Performance numbers are included only where they explain architectural decisions: eliminating accumulate-all behavior, avoiding wasted compression, disabling erasure on verified LAN, proving directory streaming, and proving checkpoint resume.
