---
title: "Building File Transfer That Doesn't Trust Anyone"
date: 2026-03-15
tags: [release, file-transfer, security, architecture]
image: /images/blog/file-transfer-hero.svg
description: "How Shurli's file transfer handles integrity, privacy, DDoS defense, and resume - and what we learned from the gaps in existing tools."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![File transfer pipeline: every byte goes through chunking, hashing, compression, parity encoding, and atomic delivery](/images/blog/file-transfer-hero.svg)

## The gaps we found

Existing tools solve parts of the file transfer problem well. rsync has solid resume. Syncthing handles continuous sync. NextCloud provides a full self-hosted stack. But in the zero-trust P2P context - no server, no cloud, peers that might be compromised - the gaps compound. No single tool addresses all of them together.

We looked at what people actually complain about when transferring files peer-to-peer, and cross-referenced with published CVEs and security research across 9 tools. The patterns are consistent:

**No resume.** Many widely-used transfer tools have no resume capability by design. A 100 GB transfer that fails at 99 GB starts from scratch. Tools that do support resume typically use byte-offset approaches (like HTTP Range), which can't handle out-of-order delivery, parallel streams, or multi-source downloads. Chunk-level tracking is required for real resume, and most P2P tools don't have it.

**LAN-only or cloud-dependent.** Many popular file sharing tools only work on your local network by design. Tools that work over the internet route through someone else's servers. Direct, NAT-traversing, relay-capable file transfer without a cloud middleman is architecturally rare. Shurli's relay-first onboarding means every new node connects through a relay by default. Direct connections upgrade automatically when NAT traversal succeeds, but relay is the guaranteed baseline, not a fallback.

**Platform lock-in.** Some of the best file sharing experiences are locked to a single ecosystem by design. If your nodes span Linux servers, laptops, and phones across different platforms, your options shrink fast.

**No integrity verification.** Common transfer utilities send raw bytes and trust the transport layer entirely. If a byte flips in transit, you won't know until something breaks later. Cryptographic per-chunk verification during transfer is not a standard feature in most transfer tools.

**Zero abuse protection.** Most P2P transfer tools have no rate limiting, no queue depth protection, no bandwidth budgets. Once a peer is authorized to connect, there's no mechanism to limit what they can do. An authorized peer that goes rogue can spam transfer requests, exhaust disk space, or stall your bandwidth. Shurli enforces per-peer bandwidth budgets (`shurli auth set-attr <peer> bandwidth_budget 500MB`), seven-layer DDoS defense, and relay-level session limits with per-chunk byte tracking.

**Path leakage.** Most P2P file sharing protocols transmit filesystem paths as part of the transfer. The remote peer can see your username, directory structure, and operating system from the paths alone. This is an architectural choice, not a bug - most tools simply weren't designed with path privacy in mind.

## What we built instead

Shurli's file transfer was designed with one primary use case in mind: **AI agents will need to exchange heavy payloads autonomously across peer-to-peer networks**. Model weights, training datasets, inference results, configuration updates. The transfer layer must be rock-solid in its foundations because agents won't have a human to babysit failed transfers or verify file integrity manually.

This feature in its current state does not completely replace existing file sharing solutions. But it is inherently ready to serve as the transfer layer for autonomous AI agents. Every design decision was made from the agent's perspective first: verified integrity, resumable delivery, abuse protection, path privacy. Human users benefit from all of this too, and will eventually get UX and cosmetic improvements for a smoother experience. But the underlying technical layers are built for machines first, humans second.

For human users, some of this may feel like overkill for a P2P network tool. Its full significance will be felt when AI agents start operating on this network at scale. Every byte is verified. Every path is hidden. Every request is rate-limited.

### The transfer pipeline

A file goes through five stages before it reaches the other side:

1. **FastCDC chunking** - Content-defined chunking splits files at natural boundaries. Same chunk = same hash regardless of position. This enables deduplication and chunk-level resume.

2. **BLAKE3 hashing** - Every chunk is hashed as it's cut (single-pass, no re-read). A Merkle tree of chunk hashes produces a root hash that represents the entire file. Change one byte, and the root hash changes.

3. **zstd compression** - On by default. Incompressible data is detected automatically (if compressed output is larger than input, the chunk is sent raw). Compression bomb protection: if decompressed output exceeds 10x compressed size, decompression aborts immediately.

4. **Reed-Solomon parity** - Forward error correction. 10% parity overhead means 10% of chunks can be lost and the file still reconstructs without retransmission. Essential for unstable WAN connections.

5. **Atomic delivery** - File lands as a `.tmp` file first. Only renamed to its final path after integrity verification passes. Interrupted transfers leave a checkpoint, not a corrupt file.

![Path privacy: what the remote peer sees in Shurli vs typical P2P tools](/images/blog/file-transfer-path-privacy.svg)

### Path privacy: zero filesystem leakage

When you share a directory, the remote peer never sees your filesystem paths. Not in the browse response, not in error messages, not anywhere.

How it works:

- **Opaque share IDs**: `share-a1b2c3d4` (random hex). The ID encodes zero information about where the directory lives on your disk.
- **Relative paths only**: The hosting node strips absolute paths before writing to the stream using `filepath.Rel()`. The full path never touches the wire. The remote peer sees `photos/vacation.jpg`, never `/home/user/Documents/photos/vacation.jpg`. Even a modified client cannot retrieve absolute paths because they are never sent.
- **Absolute path rejection**: If a remote peer sends an absolute path in a download request, it's rejected before any file lookup.
- **Directory jailing**: `os.Root` atomically jails all file access within the share directory. Path traversal (`../../../etc/passwd`) is impossible at the OS level.
- **Silent rejection**: Unauthorized peers get a stream reset. No error message, no "share not found", no confirmation that shares exist.

We audited this through three rounds of Level 4 security review. Zero path leakage vectors.

![Seven-layer DDoS defense: each layer catches what the others miss](/images/blog/file-transfer-ddos.svg)

### Seven layers of DDoS defense

An authorized peer that goes rogue (or gets compromised) could try to exhaust your disk, bandwidth, or queue. Seven independent defense layers handle this:

| Layer | What it catches |
|-------|----------------|
| Browse rate limit (10/min/peer) | Directory enumeration spam |
| Global inbound rate (30/min) | Coordinated multi-peer flood |
| Per-peer queue depth (10 max) | Single peer monopolizing queue |
| Failure backoff (3 fails = 60s block) | Repeated bad requests |
| Minimum speed (10 KB/s for 30s) | Slowloris-style stall attacks |
| Temp file budget | Disk exhaustion via abandoned transfers |
| Bandwidth budget per peer | One peer consuming all bandwidth |

Every rejection is silent. No "rate limited, try again in X seconds" - that helps attackers calibrate. A stream reset is indistinguishable from a network failure.

All thresholds are configurable. The defaults are tuned for a personal network but scale to larger deployments.

![Resume: bitfield tracks received chunks, sends only what's missing on reconnect](/images/blog/file-transfer-resume.svg)

### Resume that actually works

Switch from WiFi to mobile? Transfer resumes. VPN reconnects? Transfer resumes. Daemon restarts? Queued transfers survive (HMAC-verified persistence).

The resume system uses a bitfield - one bit per chunk. When a transfer is interrupted:

1. The checkpoint file records which chunks were received
2. On reconnect, the receiver sends its bitfield to the sender
3. The sender skips already-received chunks
4. The completed file is SHA-256 verified against the original manifest

This works with out-of-order delivery, parallel streams, and multi-peer downloads. A byte-offset-based resume (like HTTP Range) couldn't handle any of these.

### Queue processor: your transfers don't disappear

`shurli send` returns immediately. The daemon handles the transfer in the background. You can close the terminal, close the SSH session, close the laptop lid. The transfer continues.

The queue handles priority ordering (high, normal, low), per-peer limits (100 queued per peer), global limits (1000 total), and survives daemon restarts with HMAC-SHA256 integrity verification on the persisted queue file.

## The honest numbers

Early testing confirmed the full transfer pipeline works end to end: LAN transfers between controlled devices, and relay transfers across countries (NZ to AU, 72ms latency). Files arrive intact, verified, and resumable.

We're not publishing specific throughput numbers yet. The current test environment (WiFi at distance over satellite backhaul) constrains both Shurli and traditional tools like SCP equally, making any comparison misleading. Proper benchmarking on dedicated wired infrastructure is planned before we publish performance data.

What we can say: Shurli does more work per byte than tools like SCP. Every byte gets chunked, hashed, optionally compressed, parity-encoded, and verified. SCP sends raw bytes over an encrypted pipe. No chunking, no integrity verification, no compression, no erasure coding, no resume. If your SCP transfer dies at 90%, you start over. If a byte flips in transit, you won't know. If someone intercepts the stream, they see your exact file paths.

The question isn't "is it as fast as SCP?" - it's "is the integrity worth the overhead?" For files that matter (a database backup, a model checkpoint, a legal document), cryptographic proof that what arrived is exactly what was sent is worth the extra cycles.

### Room to grow

Speed optimization is planned and real. We haven't profiled yet. There are likely quick wins: skipping compression for incompressible data (already detected, not yet skipped in the hot path), pipelining chunk writes, buffer pool reuse. The architecture supports parallel streams (up to 32 on LAN), which we're not fully utilizing yet.

This is the starting point, not the ceiling. The foundation is integrity-first, and speed will follow as we profile and tune on proper infrastructure. Things are evolving fast, and every design decision follows first principles: if something doesn't pull its weight, it gets deleted. If it needs to come back, it comes back stronger.

## Why this matters beyond file transfer

Shurli is being built as an AI agentic native network. File transfer isn't a feature - it's infrastructure.

AI agents need to move data between nodes: model weights, training datasets, inference results, configuration updates. They need to do this without a cloud middleman, without trusting the transport, and without human intervention.

The transfer pipeline - verified integrity, resumable delivery, DDoS-resistant endpoints, privacy-preserving paths - is the foundation for autonomous agent-to-agent data exchange. When combined with Shurli's reputation system (scored peer trust, zero-human-intervention trust decisions), agents can autonomously decide: which peers to transfer data with, which to avoid, and how much bandwidth to allocate.

File transfer is the first concrete step toward a Zero-Human Network - not zero humans using it, but zero humans required to operate it.

And this is just one direction. The P2P foundation opens doors we haven't fully explored yet. Some are already taking shape as separate projects. The network is the platform, and what gets built on it will emerge as the foundation proves itself.

## What's next

- **Speed optimization**: Profile first, optimize second. Match or exceed SCP throughput while keeping integrity guarantees.

**Since this post was published**:

- [Per-peer data access control](/blog/who-gets-in/) is now live - time-limited macaroon capability grants with delegation, notifications, and tamper-evident audit logs. Share management (add/remove peers) and human-readable error messages are also shipped.
- **Grant Receipt Protocol**: Relay circuits now issue cryptographic receipts with session data limits, duration, and per-chunk byte tracking. The client caches these receipts and runs smart pre-transfer checks before sending: if the file exceeds the relay's session budget, the transfer is blocked before wasting bandwidth. Smart reconnection retries transport failures with exponential backoff while excluding application-level errors (rejections, disk space, access denied).
- **Per-peer bandwidth budgets**: Admins can set per-peer transfer limits (`shurli auth set-attr <peer> bandwidth_budget 500MB`). LAN peers are always exempt. The budget overrides the global default, and the value is enforced at the transfer layer.
- **Relay-first onboarding**: `shurli init` now defaults to relay mode. Every new node connects through a relay immediately. Direct connections upgrade automatically when NAT traversal succeeds. This means file transfer works for CGNAT users (roughly half the internet) out of the box.
- **Plugin architecture**: File transfer is now a plugin, not a monolithic feature. The transfer pipeline runs inside a supervisor with crash detection, restart, backoff, and checkpoint/restore. 43-vector security threat analysis, 209 million fuzz executions, zero crashes.
- **16-test chaos campaign**: Physical testing across satellite, cellular, terrestrial WiFi, USB LAN, and VPN. 15 PASS, 1 PARTIAL PASS, zero regressions. WiFi switching, VPN toggling, cellular handoffs, interface removal - all tested on real hardware.

The file transfer system is one piece of a larger architecture. Every component - network resilience, chaos-tested reconnection, relay circuits, identity and encryption, file transfer, reputation - builds toward the same goal: infrastructure that never betrays its users.
