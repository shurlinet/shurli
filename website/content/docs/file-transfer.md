---
title: "File Transfer"
weight: 9
description: "Send files between Shurli peers with end-to-end integrity verification. Works over LAN (mDNS) and remote (relay) connections."
---

Shurli includes a built-in file transfer plugin that sends files directly between peers over the P2P network. Files are checksummed with SHA-256 and verified on receipt.

## Sending a File

```bash
# By peer name
shurli send photo.jpg home-server

# By peer ID
shurli send photo.jpg 12D3KooW...

# With JSON output (for scripting)
shurli send photo.jpg home-server --json
```

The CLI shows live progress during transfer:

```
Sending photo.jpg (2.4 MB) to home-server...
  1.2 MB / 2.4 MB (50%)
Complete 2.4 MB sent
```

## Receiving Files

Files are received automatically when the daemon is running. No action needed on the receiving end.

**Default receive directory:** `~/Downloads/shurli/`

Change it with:
```bash
shurli config set transfer.receive_dir /path/to/your/dir
```

If a file with the same name already exists, Shurli creates `photo (1).jpg`, `photo (2).jpg`, etc.

## Requirements

- Both peers must be running the daemon (`shurli daemon`)
- Peers must be paired (via `shurli invite` / `shurli join`)
- Works over any connection type: LAN (mDNS), direct, or relay

## Security

- **Integrity**: SHA-256 checksum computed before sending, verified after receiving. Corrupted transfers are rejected and the partial file is deleted.
- **Path traversal protection**: Filenames like `../../../etc/passwd` are sanitized to just `passwd`. Only the base filename is used.
- **Transport encryption**: All data travels over libp2p's encrypted transport (TLS 1.3 or Noise). No additional encryption layer needed for authorized peers.
- **Authorization**: Only paired peers can send files. Unauthorized peers are rejected at the connection gating layer.

## Daemon API

For programmatic use (SDK consumers, scripts, other applications):

### Send a file

```bash
curl -X POST --unix-socket ~/.shurli/daemon.sock \
  http://localhost/v1/send \
  -H "Cookie: auth=$(cat ~/.shurli/daemon.cookie)" \
  -H "Content-Type: application/json" \
  -d '{"file_path": "/absolute/path/to/file.pdf", "peer": "home-server"}'
```

Response:
```json
{
  "transfer_id": "xfer-1"
}
```

### Check transfer progress

```bash
curl --unix-socket ~/.shurli/daemon.sock \
  http://localhost/v1/transfer/xfer-1 \
  -H "Cookie: auth=$(cat ~/.shurli/daemon.cookie)"
```

Response:
```json
{
  "id": "xfer-1",
  "filename": "file.pdf",
  "size": 1048576,
  "sent": 524288,
  "peer_id": "12D3KooW...",
  "direction": "send",
  "done": false
}
```

### List all transfers

```bash
curl --unix-socket ~/.shurli/daemon.sock \
  http://localhost/v1/transfers \
  -H "Cookie: auth=$(cat ~/.shurli/daemon.cookie)"
```

## Limits

| Limit | Value |
|-------|-------|
| Max file size | 1 TB |
| Max filename length | 4,096 bytes |
| Copy buffer | 64 KB |
| Progress poll interval | 500 ms |

## Current Limitations

- **Single files only.** Directory transfer is not yet supported.
- **No pause/resume.** Interrupted transfers must be restarted.
- **No queuing.** Each `shurli send` initiates an immediate transfer.

These limitations will be addressed in future phases.
