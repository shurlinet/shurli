# Phase 9: File Transfer Architecture

| | |
|---|---|
| **Date** | 2026-03-11 |
| **Status** | Complete |
| **ADRs** | ADR-R01 to ADR-R09 |

File transfer is the first production plugin built on the Phase 9A service infrastructure. It spans ~6,100 lines across 10 source files in `pkg/sdk/`, with full daemon integration, CLI commands, and a management API.

---

## ADR-R01: Own FastCDC Implementation

| | |
|---|---|
| **Date** | 2026-03-08 |
| **Status** | Accepted |

### Context

Content-defined chunking (CDC) is required for deduplication and resumable transfers. Options: use an existing Go CDC library, or write our own.

### Decision

Write our own FastCDC in `plugins/filetransfer/chunker.go` (180 lines). Single-pass streaming: each byte is hashed with BLAKE3 as the chunk boundary is found, so the chunk hash is available the moment the boundary is detected. No second pass.

Chunk sizes are adaptive based on file size:

| File size | Min | Avg | Max |
|-----------|-----|-----|-----|
| < 250 MB | 64 KB | 128 KB | 256 KB |
| < 1 GB | 128 KB | 256 KB | 512 KB |
| < 4 GB | 256 KB | 512 KB | 1 MB |
| >= 4 GB | 512 KB | 1 MB | 2 MB |

### Why Not a Library

Every Go CDC library we evaluated either required a second pass for hashing, pulled in unnecessary dependencies, or didn't support adaptive chunk sizes. 180 lines of self-contained code with zero dependencies (beyond BLAKE3 which we already use for Merkle) is simpler than managing an external dependency for marginal benefit.

**Reference**: `plugins/filetransfer/chunker.go`

---

## ADR-R02: BLAKE3 for All Hashing

| | |
|---|---|
| **Date** | 2026-03-08 |
| **Status** | Accepted |

### Context

File transfer needs hashing for: per-chunk integrity, Merkle tree root verification, and checkpoint matching.

### Decision

BLAKE3 everywhere. `zeebo/blake3` (CC0/public domain). Used for:
- Per-chunk hash during FastCDC (single-pass, computed as chunks are cut)
- Merkle tree nodes (`pkg/sdk/merkle.go`, 48 lines, binary tree with odd-node promotion)
- Transfer checkpoint filenames (`.shurli-ckpt-<root-hash>`)

### Why Not SHA-256

BLAKE3 is ~3-5x faster than SHA-256 on modern hardware. For large file transfers where every chunk is hashed, this matters. The CC0 license means zero legal overhead. SHA-256 would work correctly but slower for no benefit.

**Reference**: `pkg/sdk/merkle.go`, `plugins/filetransfer/chunker.go`

---

## ADR-R03: zstd On-By-Default with Bomb Protection

| | |
|---|---|
| **Date** | 2026-03-08 |
| **Status** | Accepted |

### Context

Compression reduces transfer time on all but already-compressed data. The question is whether to make it opt-in or opt-out.

### Decision

zstd compression on by default (`klauspost/compress/zstd`, BSD-3). Opt-out via `transfer.compress: false` in config.

Incompressible data is auto-detected: if compressed output is larger than input, the chunk is sent uncompressed (flagged in the wire format).

Bomb protection: `maxDecompressRatio = 10`. If decompressed output exceeds 10x compressed input size, decompression aborts immediately. This prevents a malicious peer from sending a tiny compressed payload that expands to fill disk or memory.

### Why On-By-Default

95%+ of real files (documents, source code, logs, databases) compress well. The 5% that don't (JPEG, MP4, ZIP) are detected automatically and sent uncompressed. The cost of attempting compression on incompressible data is negligible (one comparison). The benefit of not requiring users to remember a flag is significant.

**Reference**: `plugins/filetransfer/compress.go` (41 lines), `plugins/filetransfer/transfer.go` (maxDecompressRatio)

---

## ADR-R04: Reed-Solomon Stripe-Based Erasure Coding

| | |
|---|---|
| **Date** | 2026-03-09 |
| **Status** | Accepted |

### Context

WAN transfers lose chunks to network instability. Without forward error correction, every lost chunk requires a full round-trip retransmit.

### Decision

Reed-Solomon erasure coding via `klauspost/reedsolomon` (MIT). Stripe-based: file is divided into stripes of `defaultStripeSize = 100` data chunks each. Parity chunks are generated per stripe and appended to the manifest.

Key constraints:
- Max parity overhead: `maxParityOverhead = 0.50` (50% cap)
- Max total parity chunks: `maxParityCount = maxChunkCount / 2`
- Auto-enabled on Direct WAN only (disabled on LAN where loss is negligible)
- Configurable via `transfer.erasure_overhead` (default 0.2 = 20%)

### Why Stripe-Based

The alternative is whole-file RS encoding, which requires holding the entire file's chunk set in memory. Stripe-based encoding bounds memory to one stripe (100 chunks) regardless of file size. A 100 GB file uses the same memory as a 100 MB file.

**Reference**: `plugins/filetransfer/transfer_erasure.go` (384 lines)

---

## ADR-R05: RaptorQ Fountain Codes for Multi-Source

| | |
|---|---|
| **Date** | 2026-03-09 |
| **Status** | Accepted |

### Context

When multiple peers hold the same file, downloading from all of them simultaneously increases throughput. Traditional chunk-based multi-source requires coordination to avoid duplicates. Fountain codes solve this: each peer generates statistically independent symbols, so any combination of enough symbols from any peers reconstructs the data.

### Decision

RaptorQ via `xssnick/raptorq` (MIT). Constants:
- `raptorqSymbolSize = 1024` bytes
- `raptorqRepairRatio = 0.2` (20% repair symbols per peer)

Wire protocol: `/shurli/file-multi-peer/1.0.0` (`plugins/filetransfer/transfer_multipeer.go`, 874 lines). Requesting peer sends a manifest to each source, each source encodes independently and streams symbols back. The receiver collects symbols from all sources and decodes when it has enough.

### Why RaptorQ Over Plain Multi-Source

Plain multi-source (each peer sends different chunks) requires a coordinator to prevent duplicates and handle stragglers. RaptorQ eliminates coordination entirely: symbols are statistically independent, so peers can encode at their own pace. The receiver just needs "enough" symbols from any combination. This is the same approach TON uses for its DHT, battle-tested at scale.

**Reference**: `plugins/filetransfer/transfer_raptorq.go` (105 lines), `plugins/filetransfer/transfer_multipeer.go`

---

## ADR-R06: Adaptive Parallel Streams

| | |
|---|---|
| **Date** | 2026-03-09 |
| **Status** | Accepted |

### Context

A single QUIC stream underutilizes available bandwidth on high-BDP (bandwidth-delay product) links. Multiple streams allow the transport to fill the pipe.

### Decision

Parallel chunk transfer with transport-aware defaults (`plugins/filetransfer/transfer_parallel.go`, 592 lines):

| Transport | Default Streams | Max Streams |
|-----------|----------------|-------------|
| LAN | 8 | 32 |
| Direct WAN | 4 | 20 |
| Relay | 1 (single stream) | 1 |

Auto-reduction: if chunks < `minChunksPerStream * streamCount` (minimum 4 chunks per stream), stream count is reduced to avoid overhead exceeding benefit.

### Why Different Defaults

LAN has near-zero latency and high bandwidth. 8 streams is conservative for gigabit+. WAN has higher latency and congestion is likelier; 4 streams balances throughput against congestion. Relay is already bandwidth-limited (signaling-only by default); parallel streams through relay would multiply relay load for minimal gain.

**Reference**: `plugins/filetransfer/transfer_parallel.go`

---

## ADR-R07: AirDrop-Style Receive Permissions

| | |
|---|---|
| **Date** | 2026-03-08 |
| **Status** | Accepted |

### Context

Unsolicited file transfers are a spam vector. The system needs a permission model that balances convenience with control.

### Decision

Five receive modes, controlled via `transfer.receive_mode` config:

| Mode | Behavior |
|------|----------|
| `off` | Reject all incoming transfers |
| `contacts` | Auto-accept from authorized peers (default) |
| `ask` | Queue all transfers for manual approval |
| `open` | Accept from any authorized peer without prompt |
| `timed` | Temporarily open, reverts to previous mode after duration |

The `contacts` default means: if a peer passed the connection gater (is in `authorized_keys`), their transfers are accepted automatically. Unknown peers are rejected silently (no error message, no information leakage).

### Why This Model

Apple's AirDrop proved this UX works: most users want "contacts only" and occasionally switch to "everyone" for a specific situation. The `timed` mode handles the "open for 10 minutes" scenario without forgetting to turn it off. Silent rejection for unauthorized peers follows the same principle as the connection gater: don't reveal your existence to strangers.

**Reference**: `plugins/filetransfer/transfer.go` (ReceiveMode constants)

---

## ADR-R08: Fixed-Window Rate Limiting with Silent Rejection

| | |
|---|---|
| **Date** | 2026-03-09 |
| **Status** | Accepted |

### Context

A malicious or buggy peer could flood transfer requests. Rate limiting is needed, but the choice of algorithm affects complexity and information leakage.

### Decision

Fixed-window rate limiter: 10 transfer requests per minute per peer. 60-second window. Excess requests are silently rejected (stream reset, no error message).

Implementation: `transferRateLimiter` struct in `plugins/filetransfer/transfer.go`. Per-peer counters with periodic cleanup of stale entries.

Also applied to multi-peer requests in `HandleMultiPeerRequest` (same limiter instance).

### Why Fixed-Window Over Sliding Window

Fixed-window is simpler (a counter and a timestamp per peer) and sufficient for anti-spam. Sliding window adds complexity (sorted event lists or ring buffers) for marginal accuracy improvement at window boundaries. The 2x worst-case burst at window edges is acceptable: 20 requests in 2 seconds instead of 10 is not a meaningful attack vector when each request still requires connection gater approval.

### Why Silent Rejection

Informative error messages ("rate limited, try again in X seconds") help attackers calibrate their request rate. Silent stream resets are indistinguishable from network failures. The legitimate peer experience is unaffected: 10 transfers per minute is generous for real use.

**Reference**: `plugins/filetransfer/transfer.go` (transferRateLimiter)

---

## ADR-R09: AI Compression Deferral

| | |
|---|---|
| **Date** | 2026-03-09 |
| **Status** | Accepted (Revisit 2028-2029) |

### Context

Neural compression achieves better ratios than classical algorithms for some data types. Should Shurli use AI-based compression for file transfer?

### Decision

No. Classical zstd is the right choice today. AI compression deferred with a 2028-2029 checkpoint.

Technologies assessed:
- **DZip** (neural lossless): ~10-30x slower than zstd. Compute cost is prohibitive for real-time P2P transfer where both sides need to encode/decode.
- **DCVC-RT / Cool-Chic** (neural video): inference requires GPU. Most Shurli nodes are headless Linux boxes or phones.
- **NVIDIA NTC** (GPU neural textures): CUDA-only. Not portable.

### Revisit Criteria

Re-evaluate when:
1. Hardware accelerators for neural codecs ship in consumer devices (NPUs, dedicated silicon)
2. A neural lossless codec achieves within 2x of zstd encode speed on CPU
3. A cross-platform (CPU + GPU) implementation exists with a permissive license

Until then, zstd's combination of speed, ratio, and universality is unmatched for general-purpose P2P file transfer. No point adding 100x compute overhead for 10-20% better ratios.

---

## Summary: Transport Policy

All file transfer operations are gated by `PluginPolicy` (`pkg/sdk/plugin_policy.go`, 106 lines):

| Transport | Bitmask | File Transfer |
|-----------|---------|---------------|
| LAN | `TransportLAN` (1) | Allowed |
| Direct WAN | `TransportDirect` (2) | Allowed |
| Relay | `TransportRelay` (4) | Blocked by default |

Default: `TransportLAN | TransportDirect`. Relay is excluded because file transfer through relay would consume relay bandwidth that should be reserved for signaling. This drives adoption of direct connectivity and own-relay deployment.

## Summary: Wire Protocols

| Protocol | ID | Purpose |
|----------|----|---------|
| File Transfer | `/shurli/file-transfer/2.0.0` | Send/receive files |
| File Browse | `/shurli/file-browse/1.0.0` | Browse shared files |
| File Download | `/shurli/file-download/1.0.0` | Download shared files |
| Multi-Peer | `/shurli/file-multi-peer/1.0.0` | RaptorQ multi-source |

## Summary: Fire-and-Forget Daemon Model

`shurli send <file> <peer>` POSTs to the daemon's `/v1/send` endpoint, receives a transfer ID, and exits. The daemon manages the transfer in the background. No terminal needs to stay open. Users check progress with `shurli transfers` or opt into inline progress with `shurli send --follow`.

This is deliberate: a CLI that blocks until transfer completion ties up a terminal and fails if the terminal is closed. The daemon-mediated model means transfers survive terminal disconnection, SSH timeouts, and laptop lid closes.
