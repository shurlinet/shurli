---
title: "QUIC Transport"
weight: 12
description: "QUIC transport platform behavior and known limitations: macOS UDP GSO, Linux buffer tuning, LNP, and measured performance data."
---
<!-- Auto-synced from docs/QUIC-TRANSPORT.md by sync-docs - do not edit directly -->


Shurli uses QUIC (RFC 9000) over UDP for all peer-to-peer data transport via libp2p and quic-go. QUIC provides mandatory encryption, multiplexed streams, and connection migration. It is the right transport for a P2P network.

However, QUIC is younger than TCP, and operating system support is uneven. This document records every platform-specific limitation, kernel tuning requirement, and performance characteristic we have measured. Each entry includes the root cause, evidence, and practical impact.

This is a living document. New findings are added as they are discovered.

---

## 1. macOS: UDP Send Path Lacks GSO (Sawtooth Throughput)

**Discovered**: 2026-04-23, investigated with pprof CPU profiling, iostat, and direction-reversal testing.

### Symptom

When a macOS machine sends data over a LAN via QUIC, throughput shows a periodic sawtooth pattern: speed ramps up to ~110 MB/s, drops sharply to ~40-60 MB/s, recovers over several seconds, and repeats. The cycle occurs roughly every 500 MB of transferred data. Average throughput for a 2 GB file lands around 80-90 MB/s.

The same transfer in the reverse direction (Linux sends, macOS receives) runs flat at ~103 MB/s with zero drops.

For comparison, scp (TCP) sustains a flat ~107 MB/s in both directions on the same hardware.

### Root Cause

QUIC runs on top of UDP. At 110 MB/s with QUIC's ~1200-byte packet size, the sender makes roughly 90,000 `sendmsg()` system calls per second. Each call requires a full kernel context switch.

Linux solves this with two kernel features:

- **GSO (Generic Segmentation Offload)**: batches multiple QUIC packets into a single kernel call. The kernel splits them at the NIC level.
- **`sendmmsg()`**: sends multiple UDP datagrams in one system call.

macOS supports neither for UDP. Every QUIC packet requires its own `sendmsg()` call.

quic-go explicitly disables GSO on macOS:

```go
// quic-go v0.48.2 - sys_conn_helper_darwin.go
func isGSOEnabled(syscall.RawConn) bool { return false }
```

TCP does not have this problem because macOS supports TSO (TCP Segmentation Offload), which offloads segment splitting to the NIC hardware.

### What Causes the Sawtooth

1. The sender bursts data at full speed (~110 MB/s)
2. The NIC transmit queue fills faster than it can drain (each packet individually queued via separate syscalls)
3. Packets are dropped at the kernel/NIC level
4. QUIC's congestion controller detects the loss and halves its congestion window
5. Throughput drops to ~40-60 MB/s
6. The congestion window recovers via slow start
7. Throughput climbs back to ~110 MB/s
8. The cycle repeats

### Evidence

**Direction test**: identical hardware, identical QUIC code, only the sender/receiver roles swapped.

| Direction | Transport | 2 GB Speed | Pattern |
|-----------|-----------|------------|---------|
| macOS -> Linux | QUIC (Shurli) | 80-90 MB/s avg | Sawtooth (drops every ~500 MB) |
| Linux -> macOS | QUIC (Shurli) | 103 MB/s avg | Flat, stable |
| Either direction | TCP (scp) | 107 MB/s avg | Flat, stable |

**Sender CPU profile** (macOS daemon, `go tool pprof`, 15-second window during 2 GB transfer):

| Where | % of CPU | Meaning |
|-------|----------|---------|
| `sendmsg()` (UDP write syscall) | 50.4% | Sender blocked writing individual QUIC packets to kernel |
| Go runtime idle | 18.5% | Goroutines waiting for sendmsg to unblock |
| ChunkReader + BLAKE3 | 24.8% | Actual data processing |
| Total CPU utilization | 26.1% | Process is 74% idle, I/O bound on syscalls |

**Elimination matrix** (all tested, none are the cause):

| Hypothesis | Test | Result |
|-----------|------|--------|
| Multi-stream CPU contention | `--streams=1` | Same sawtooth |
| Compression overhead | `--no-compress` | Same sawtooth |
| Receiver disk I/O | `iostat` on receiver | Disk util <3%, iowait ~0% |
| Receiver UDP buffer size | Increased from 208 KB to 7 MB | Same sawtooth |
| Go garbage collector | Heap profile | 55 MB heap, sub-ms pauses |
| Sender-side file read | pprof | ChunkReader is <15% of CPU |

### Practical Impact

- **macOS sending to any peer over LAN**: expect ~80-90 MB/s for large files (>500 MB) instead of ~107 MB/s. Small files complete before the first congestion event.
- **macOS sending over WAN**: not affected. WAN bandwidth is typically far below the per-packet syscall threshold.
- **macOS sending over relay**: not affected. Relay bandwidth is the bottleneck.
- **Linux sending to macOS over LAN**: no penalty. Full ~103 MB/s.
- **Linux to Linux over LAN**: no penalty. GSO active on both sides.
- **Any platform receiving**: unaffected. The receive path uses `recvmmsg` on Linux and per-packet reads on macOS, but reading is less syscall-intensive than sending.

### What Cannot Fix This

- **Increasing macOS socket buffer sizes**: macOS already allows 8 MB (`kern.ipc.maxsockbuf`). The problem is per-packet syscall overhead, not buffer capacity.
- **Reducing stream count**: same sawtooth with 1 stream. The bottleneck is at the UDP socket level, below the QUIC stream layer.
- **Disabling compression**: the sawtooth occurs even with `--no-compress`. Compression adds negligible overhead on incompressible data (short-circuits after 3 chunks).
- **Application-level pacing**: the QUIC congestion controller already paces. The issue is that macOS cannot physically send the paced packets efficiently.

### Mitigation

There is no application-level fix for the missing kernel GSO support. The options are:

1. **Accept the ceiling**: 80-90 MB/s on macOS-as-sender for LAN is still fast. Most real-world transfers are over WAN or relay where this limit does not apply.
2. **TCP transport for LAN** (planned): TCP gets full TSO offload on all platforms. Adding TCP as an alternative LAN transport would match scp's throughput.
3. **Wait for Apple**: macOS may eventually add GSO for UDP. When it does, quic-go will enable it automatically. No Shurli code changes would be needed.
4. **Wait for quic-go**: the quic-go project continues optimizing its packet sending path. Any improvements benefit Shurli automatically via dependency updates.

---

## 2. Linux: Default UDP Buffer Size Too Small

**Discovered**: 2026-04-23, confirmed via quic-go startup warning.

### Symptom

quic-go logs a warning on startup:

```
failed to sufficiently increase receive buffer size (was: 208 kiB, wanted: 7168 kiB, got: 416 kiB)
```

### Root Cause

Linux's default `net.core.rmem_max` and `net.core.wmem_max` are 212992 bytes (208 KB). quic-go requests 7 MB for its UDP socket buffers. The kernel caps the request to 416 KB (2x the default, which is the max an unprivileged process can request).

With a 208-416 KB receive buffer at 100+ MB/s, the buffer fills in ~2-4ms. Any application-level pause (goroutine scheduling, GC, disk write) during that window causes packet loss.

TCP does not have this problem because Linux auto-tunes TCP buffer sizes independently via `net.ipv4.tcp_rmem` and `net.ipv4.tcp_wmem`, with defaults up to 6 MB.

### Fix

Increase the kernel limits:

```bash
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
```

To persist across reboots, add to `/etc/sysctl.conf` or a file in `/etc/sysctl.d/`:

```
net.core.rmem_max=7500000
net.core.wmem_max=7500000
```

After changing the limits, restart the Shurli daemon so quic-go can allocate the full buffer. Verify by checking that the warning no longer appears in the daemon log.

### Impact Without the Fix

Without adequate buffers, the Linux node is more susceptible to packet loss during load spikes. This can compound with the macOS GSO limitation (section 1) to produce worse throughput than either issue alone.

### Reference

quic-go documents this requirement: https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes

---

## 3. macOS: Local Network Privacy (LNP) Silently Blocks LAN Connections

**Discovered**: 2026-03-14.

### Symptom

The daemon starts normally, connects to relays, but cannot reach any peer on the local network. mDNS discovery finds peers, but QUIC handshakes fail silently. No error is logged. `ping` (ICMP) to the same IP works fine.

### Root Cause

macOS 15+ (Sequoia) enforces Local Network Privacy (LNP). Each binary must be explicitly authorized to access the local network. When a binary is replaced (e.g., `sudo cp shurli /usr/local/bin/shurli`), macOS may revoke or require re-authorization.

LNP applies per-binary (by code signature), not per-process. ICMP is exempt (different permission class), which is why raw `ping` works but QUIC/UDP does not.

### Fix

1. Open **System Settings -> Privacy & Security -> Local Network**
2. Find `shurli` in the list
3. Toggle it **ON**

If `shurli` does not appear in the list, run any command that triggers a LAN connection (e.g., `shurli ping <peer>`). macOS will prompt for authorization on the first blocked attempt from a foreground process. The daemon (running as a launchd background service) may not trigger the prompt automatically.

### When This Recurs

- After replacing the binary with a new build
- After macOS upgrades
- After resetting privacy permissions

---

## Future Entries

This document will be updated as new QUIC transport limitations or platform-specific behaviors are discovered. Areas under investigation:

- **QUIC congestion control tuning for LAN**: QUIC's default congestion controller (Cubic/Reno) is tuned for WAN. LAN transfers may benefit from different parameters or BBR. Requires quic-go support.
- **TCP transport as LAN alternative**: hardware TSO offload would eliminate the macOS send overhead entirely.
- **Windows QUIC performance**: not yet tested. Windows has its own UDP stack characteristics (Winsock, IOCP) that may introduce different limitations.
- **ARM Linux (Raspberry Pi)**: not yet profiled at high throughput. ARM's memory model and lower clock speeds may affect QUIC differently than x86.
