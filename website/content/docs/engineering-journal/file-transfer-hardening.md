---
title: "File Transfer Hardening"
weight: 25
description: "DDoS defense (7 layers), queue persistence (HMAC), path privacy, checkpoint resume, queue backpressure, name normalization, relay transport."
---
<!-- Auto-synced from docs/engineering-journal/file-transfer-hardening.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Complete |
| **ADRs** | ADR-R10 to ADR-R16 |

Physical testing of the file transfer system on real hardware across multiple networks revealed 13 issues. This journal documents the architecture decisions behind the hardening fixes: DDoS defense layers, queue persistence, path privacy, resume integrity, and the audit process that verified them.

---

## ADR-R10: Seven-Layer DDoS Defense

| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Accepted |

### Context

File transfer endpoints are the widest attack surface in Shurli. A peer behind NAT with a relay circuit can send unlimited transfer requests, browse requests, and queue-filling payloads. The connection gater only verifies that the peer is authorized - it doesn't limit how aggressively they use the connection.

Physical testing confirmed: an authorized peer acting maliciously (or just a buggy client) could exhaust disk space, bandwidth, or queue slots.

### Decision

Seven independent defense layers, each with sensible defaults and config overrides:

| Layer | Default | What It Catches | Config Key |
|-------|---------|-----------------|------------|
| Browse rate limit | 10/min/peer | Browse spam (directory enumeration) | `transfer.rate_limit` |
| Global inbound rate | 30/min total | Coordinated multi-peer flood | `transfer.global_rate_limit` |
| Per-peer queue depth | 10 pending+active | Single peer monopolizing queue | `transfer.max_queued_per_peer` |
| Failure backoff | 3 fails in 5min = 60s block | Repeated bad requests | (hardcoded) |
| Min speed enforcement | 10 KB/s for 30s | Slowloris-style stalls | `transfer.min_speed`, `transfer.min_speed_seconds` |
| Temp file budget | configurable bytes | Disk exhaustion via abandoned transfers | `transfer.max_temp_size` |
| Bandwidth budget | configurable bytes/hr/peer | Single peer consuming all bandwidth | `transfer.max_bandwidth_per_peer` |

All rejections are silent (stream reset, no error message). Silent rejection prevents attackers from calibrating their approach.

### Why Seven Layers

No single defense is sufficient. Rate limiting doesn't prevent slow attacks. Queue depth doesn't prevent bandwidth exhaustion. Temp budget doesn't prevent in-memory queue flooding. Each layer catches what the others miss. The configuration defaults are tuned for a personal network (3-20 peers) but scale to larger deployments.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go` (defense subsystem initialization, lines 660-780)

---

## ADR-R11: Queue Persistence with HMAC Integrity

| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Accepted |

### Context

When the daemon restarts (upgrade, crash, system reboot), all queued outbound transfers are lost. Users expect queued work to survive restarts.

### Decision

Persisted queue file with HMAC-SHA256 integrity verification:

- **Format**: JSON with `version`, `hmac`, and `entries` fields
- **HMAC**: HMAC-SHA256 over the serialized entries array. Key is node-specific (derived from identity)
- **TTL**: 24-hour expiry per entry (stale entries filtered on load)
- **Bounds**: Maximum 1000 entries, maximum 10 MB file size
- **Permissions**: 0600 (owner-only read/write)
- **Atomic writes**: Write to temp file, then rename

On daemon startup, `RequeuePersisted()` loads valid entries and re-submits them through `SubmitSend()`. The queue file is deleted after successful requeue.

### Why HMAC

The queue file contains file paths. Without integrity verification, a tampered queue file could trick the daemon into sending arbitrary files. HMAC-SHA256 with a node-derived key means: only this node's daemon can write valid queue files. Tampered files are rejected silently.

### Why Not Encrypt

Encryption would hide the file paths, but the daemon needs to read them to re-submit. The paths are local-only (never sent to peers). HMAC integrity is the right tool: verify authenticity without hiding content from the legitimate reader.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go` (persistQueue, loadPersistedQueue, RequeuePersisted)

---

## ADR-R12: Path Privacy - Opaque Share IDs

| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Accepted |

### Context

When a peer browses or downloads files, the wire protocol must never reveal absolute filesystem paths. Leaking paths exposes: username, OS, directory structure, potentially sensitive folder names.

### Decision

Opaque share IDs replace absolute paths in all wire-visible contexts:

1. **Share ID generation**: `"share-" + randomHex(8)` - 8 random bytes, no path information encoded
2. **Browse responses**: `BrowseEntry.Path` is always relative (via `filepath.Rel()`), never absolute
3. **Download requests**: Client sends `shareID/relativePath`. Server rejects absolute paths with `filepath.IsAbs()` check
4. **Directory jailing**: `os.Root` atomic jailing prevents `../` traversal and symlink escapes
5. **Error messages**: Generic strings only ("access denied", "not found"). No path fragments
6. **Unauthorized peers**: Silent stream reset. No shares-exist confirmation

### Defense in Depth

Any single control could have a bypass. The combination of five controls means an attacker would need to defeat all five simultaneously:

- Even if share IDs were predictable, `os.Root` jailing prevents traversal
- Even if traversal worked, ACL checks reject unauthorized peers
- Even if ACL was bypassed, `filepath.IsAbs()` blocks absolute path injection
- Even if all path controls failed, error messages reveal nothing

Level 4 security audit (3 rounds) confirmed: zero path leakage vectors.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/share.go` (HandleBrowse, HandleDownload, LookupShareByID)

---

## ADR-R13: Checkpoint Resume with Bitfield Tracking

| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Accepted |

### Context

Large file transfers over unstable networks (WiFi switching, relay timeouts, VPN reconnects) need to resume from where they left off, not restart from zero.

### Decision

Bitfield-based checkpoint tracking:

- **Checkpoint file**: `.shurli-ckpt-<root-hash>` stores which chunks have been received
- **Bitfield**: Compact bit array (1 bit per chunk). 1 million chunks = 125 KB checkpoint
- **Resume protocol**: On reconnect, receiver sends bitfield to sender. Sender skips already-received chunks
- **Integrity**: SHA-256 verification of resumed file against original manifest hash
- **Cleanup**: Checkpoint files deleted on successful completion. Stale checkpoints expire via `temp_file_expiry`

### Why Bitfield Over Byte Offset

Byte offset resume (like HTTP Range) requires sequential transfer. Bitfield allows out-of-order chunk delivery, which is essential for: parallel streams (ADR-R06), multi-peer download (ADR-R05), and network-interrupted transfers where chunks arrive from different paths at different times.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer_resume.go` (bitfield), `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go` (checkpoint save/load)

---

## ADR-R14: Queue Backpressure - Global and Per-Peer Limits

| | |
|---|---|
| **Date** | 2026-03-16 |
| **Status** | Accepted |

### Context

Level 4 audit found: the outbound transfer queue (`TransferQueue`) had no upper bound. A user or script could enqueue unlimited transfers, growing the in-memory slice unbounded. Additionally, a single peer could monopolize all queue slots.

### Decision

Two-tier backpressure:

- **Global limit**: `maxPending = 1000` items. `Enqueue()` returns `ErrQueueFull` when reached
- **Per-peer limit**: `maxPerPeer = 100` items per target peer. Returns `ErrPeerQueueFull` when reached
- **SubmitSend()** propagates both errors to the CLI caller with actionable messages

The global limit matches the persistence limit (ADR-R11), ensuring the in-memory queue never exceeds what can be persisted.

Additionally, a 5-minute cleanup ticker in the queue processor evicts stale `pendingJobs` entries older than 24 hours, preventing accumulation from abandoned jobs.

### Why 100 Per-Peer

100 queued transfers to a single peer is generous for real use (batch file operations) but prevents one peer from consuming all 1000 slots. Other peers can always enqueue their transfers.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/share.go` (TransferQueue.Enqueue), `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/transfer.go` (runQueueProcessor cleanup ticker)

---

## ADR-R15: Case-Insensitive Name Resolution

| | |
|---|---|
| **Date** | 2026-03-16 |
| **Status** | Accepted |

### Context

Peer names (like `home-node`, `relay`) are used in share commands, browse, download, and all peer-targeting operations. Users naturally type names in different cases (`Home-Node`, `HOME-NODE`). The original implementation stored and matched names case-sensitively, causing silent failures.

### Decision

All name operations normalize to lowercase:

- **Register()**: `strings.ToLower(strings.TrimSpace(name))` before storing
- **Resolve()**: Same normalization, then direct O(1) map lookup
- **Unregister()**: Same normalization, direct `delete()`
- **LoadFromMap()**: Same normalization on config-loaded names

This prevents duplicate map entries (`"Home"` and `"home"` as separate keys) and ensures any casing variant resolves correctly.

### Why Normalize on Store, Not Just on Lookup

The first attempt normalized only on lookup (case-insensitive iteration). This allowed `Register("Home")` followed by `Register("home")` to create two map entries. The audit caught this: normalize on store makes the map key canonical, and all operations become O(1) direct lookups instead of O(n) iterations.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/naming.go`

---

## ADR-R16: Relay Transport for File Transfer Plugins

| | |
|---|---|
| **Date** | 2026-03-15 |
| **Status** | Accepted |

### Context

The default `PluginPolicy` (ADR-R09 summary) blocks relay transport for file transfer. This is correct for resource conservation but prevents NAT-to-NAT peers (who can only communicate via relay circuit) from transferring files at all.

First external user testing (NZ-AU relay circuit) confirmed: browse and download fail with "plugin does not allow relay" when both peers are behind NAT.

### Decision

File transfer plugins (`file-transfer`, `file-browse`, `file-download`, `file-multi-peer`) now allow relay transport. The relay's own bandwidth limits (64 MB per session, 10-minute session duration) provide the resource conservation that the plugin policy originally enforced.

Additionally, the relay ACL requires an active time-limited grant for at least one peer in the circuit. Grants are issued via `shurli relay grant <peer-id> --duration 1h` and expire automatically. This is a deliberate admin decision, not automatic - the relay operator controls which peers can use data circuits and for how long.

### Why Not Auto-Grant Relay Access

Auto-granting relay data access to every peer that joins via invite would remove the relay operator's control. In a personal relay (the current deployment model), the operator should explicitly decide which peers consume relay bandwidth for data transfer, and for what duration. Time-limited grants enforce this without requiring manual revocation.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/plugins/filetransfer/share.go` (plugin registration), `https://github.com/shurlinet/shurli/blob/main/internal/relay/circuit_acl.go` (AllowConnect)

---

## Summary: Audit Process

The hardening was verified through a 3-round Level 4 audit:

- **Round 1**: 5 items audited (I-6, I-7, I-8/I-9, I-10, I-12). 2 medium + 14 low findings. All fixed.
- **Round 2**: Re-audit found 3 regressions (naming normalization asymmetry). Fixed.
- **Round 3**: Re-audit found 1 critical regression (case-duplicate map keys). Fixed with lowercase normalization.
- **Physical retest**: 5/5 pass on real hardware (client-node to home-node over LAN)
- **Test suite**: 21/21 packages pass with `-race` detector, 3 consecutive runs, zero failures
