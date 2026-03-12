---
title: "File Transfer"
weight: 9
description: "Chunked P2P file transfer with BLAKE3 integrity, zstd compression, erasure coding, multi-source download, and AirDrop-style receive permissions."
---

Shurli includes a built-in file transfer plugin that sends files directly between peers over the P2P network. Files are chunked with FastCDC, compressed with zstd, and verified with a BLAKE3 Merkle tree. Relay is blocked for file transfer by default (drives own-relay adoption).

## Sending a File

```bash
# By peer name (fire-and-forget - exits immediately)
shurli send photo.jpg home-server

# With live progress
shurli send photo.jpg home-server --follow

# With priority (jumps the queue)
shurli send photo.jpg home-server --priority

# Send a directory
shurli send ./folder home-server

# JSON output (for scripting)
shurli send photo.jpg home-server --json
```

`shurli send` is fire-and-forget by default. The daemon handles the transfer in the background. Use `--follow` to watch progress inline.

## Receiving Files

Receive behavior is controlled by the **receive mode** (AirDrop-style):

| Mode | Behavior |
|------|----------|
| `off` | Reject all incoming transfers |
| `contacts` | Auto-accept from authorized peers (default) |
| `ask` | Queue for manual approval via `shurli accept`/`shurli reject` |
| `open` | Accept from any authorized peer without prompting |
| `timed` | Temporarily open, reverts to previous mode after duration |

Set the receive mode:
```bash
shurli config set transfer.receive_mode ask

# Timed mode: open for 10 minutes then revert
shurli config set transfer.receive_mode timed --duration 10m
```

**Default receive directory:** `~/Downloads/shurli/`

Change it with:
```bash
shurli config set transfer.receive_dir /path/to/your/dir
```

If a file with the same name already exists, Shurli creates `photo (1).jpg`, `photo (2).jpg`, etc.

## Managing Transfers

```bash
# View transfer inbox (pending + active)
shurli transfers

# Watch live (auto-refresh)
shurli transfers --watch

# View completed transfers
shurli transfers --history

# Accept a pending transfer
shurli accept <transfer-id>

# Accept all pending
shurli accept --all

# Reject a pending transfer
shurli reject <transfer-id>

# Cancel an outbound transfer
shurli cancel <transfer-id>
```

## Sharing Files

Share files for other peers to browse and download on demand:

```bash
# Share a file with all authorized peers
shurli share add /path/to/file.pdf

# Share with a specific peer only
shurli share add /path/to/file.pdf --to home-server

# List your shares
shurli share list

# Remove a share
shurli share remove /path/to/file.pdf

# Browse a peer's shared files
shurli browse home-server

# Download a specific file from a peer's shares
shurli download document.pdf home-server
```

Shares persist across daemon restarts (stored in `~/.config/shurli/shares.json`).

## Multi-Source Download

Download a file from multiple peers simultaneously using RaptorQ fountain codes:

```bash
shurli download large-file.zip home-server --multi-peer --peers home-server,laptop
```

Each peer contributes RaptorQ symbols. Any sufficient subset of symbols reconstructs the file. Faster than single-source for large files across multiple peers.

## Requirements

- Both peers must be running the daemon (`shurli daemon`)
- Peers must be paired (via `shurli invite` / `shurli join`)
- Works over LAN (mDNS) or direct connections. Relay is blocked by default.

## How It Works

**Chunking**: FastCDC content-defined chunking with adaptive target sizes (128KB-2MB based on file size). Single-pass with BLAKE3 hash per chunk.

**Integrity**: BLAKE3 Merkle tree over all chunk hashes. Root hash verified after all chunks received. Each chunk verified before writing to disk.

**Compression**: zstd compression on by default. Auto-detects incompressible data and skips re-compression. Bomb protection: decompression aborted if output exceeds 10x compressed size. Opt-out via `shurli config set transfer.compress false`.

**Erasure Coding**: Reed-Solomon erasure coding, auto-enabled on Direct WAN connections only. Recovers from lost chunks without retransmission.

**Parallel Streams**: Adaptive parallel QUIC streams per transfer. Defaults: 1 stream on LAN, up to 4 on WAN. Configurable via `transfer.parallel_streams`.

**Resume**: Checkpoint files (`.shurli-ckpt-<hash>`) store a bitfield of received chunks. Interrupted transfers resume from the last checkpoint. Checkpoints cleaned up on successful completion.

## Security

- **Integrity**: BLAKE3 Merkle tree verification. Corrupted chunks are rejected before writing to disk.
- **Path traversal**: Filenames like `../../../etc/passwd` are sanitized. Only the base filename is used. Receive directory is a jail.
- **Transport encryption**: All data travels over libp2p's encrypted transport (TLS 1.3 or Noise).
- **Authorization**: Only paired peers can send files. Unauthorized peers are silently rejected at the connection gating layer.
- **Resource limits**: Max 3 pending transfers per peer, 5 concurrent active, 1M chunk limit, 64MB manifest limit, 1h timeout.
- **Disk space**: Re-checked before each chunk write, not just at accept time.
- **Transfer IDs**: Random hex (`xfer-<12hex>`), not sequential (prevents enumeration).
- **Compression bombs**: zstd decompression capped at 10x ratio per chunk.
- **No symlink following** in share paths. Regular files only.

## Configuration

| Key | Default | Description |
|-----|---------|-------------|
| `transfer.receive_mode` | `contacts` | Receive mode: off, contacts, ask, open, timed |
| `transfer.receive_dir` | `~/Downloads/shurli/` | Directory for received files |
| `transfer.compress` | `true` | Enable zstd compression |
| `transfer.erasure_overhead` | `0.1` | Reed-Solomon parity ratio (0.0-0.5) |
| `transfer.max_concurrent` | `5` | Max concurrent outbound transfers |
| `transfer.max_file_size` | `0` (unlimited) | Max file size to accept (bytes) |
| `transfer.timed_duration` | `10m` | Default duration for timed receive mode |
| `transfer.notify` | `none` | Notification mode: none, desktop, command |
| `transfer.notify_command` | `""` | Command template with {from}, {file}, {size} |
| `transfer.log_path` | `~/.config/shurli/logs/transfers.log` | Transfer event log path |
| `transfer.multi_peer_enabled` | `true` | Enable multi-peer swarming downloads |
| `transfer.multi_peer_max_peers` | `4` | Max peers for multi-source download |

## Daemon API

For programmatic use (SDK consumers, scripts, other applications):

### Send a file

```bash
curl -X POST --unix-socket ~/.config/shurli/shurli.sock \
  http://localhost/v1/send \
  -H "Cookie: auth=$(cat ~/.config/shurli/.daemon-cookie)" \
  -H "Content-Type: application/json" \
  -d '{"file_path": "/absolute/path/to/file.pdf", "peer": "home-server"}'
```

Response:
```json
{
  "transfer_id": "xfer-a1b2c3d4e5f6"
}
```

### Check transfer progress

```bash
curl --unix-socket ~/.config/shurli/shurli.sock \
  "http://localhost/v1/transfers/xfer-a1b2c3d4e5f6" \
  -H "Cookie: auth=$(cat ~/.config/shurli/.daemon-cookie)"
```

### List all transfers

```bash
curl --unix-socket ~/.config/shurli/shurli.sock \
  http://localhost/v1/transfers \
  -H "Cookie: auth=$(cat ~/.config/shurli/.daemon-cookie)"
```

See the [Daemon API reference](/docs/daemon-api/) for the full list of 15 file transfer endpoints.
