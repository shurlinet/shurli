# TCP-for-LAN Experimental Branch

> **This branch is archived experimental code. It is NOT merged to dev or main.**
> The feature was implemented, physically tested, and disproven by measurement.
> The code is preserved here for reference and future exploration.

## What This Is

Item #19: TCP-for-LAN bulk transfer streams. Config-gated behind `network.lan_transport: tcp`.

When enabled, the file-transfer plugin prefers TCP connections to verified-LAN peers for bulk data streams (send, download, TS-5b retry, RequeuePersisted). Core networking (ping, mDNS, relay, control signals) stays on QUIC. Falls back to QUIC via HedgedOpenStream on any TCP failure.

## Why It Was Built

macOS lacks UDP GSO (`UDP_SEGMENT` socket option). Every QUIC packet on macOS requires a separate `sendmsg()` syscall. At 110 MB/s LAN throughput with ~1200-byte packets, that's ~90K syscalls/sec. pprof confirmed 50.4% CPU in `sendmsg()`. Result: sawtooth throughput on macOS-as-sender over LAN (80-90 MB/s avg vs 107 MB/s for scp over raw TCP with TSO offload).

The hypothesis: TCP gets TSO offload on macOS, so routing file-transfer bulk streams through TCP should match scp's ~107 MB/s.

## Physical Test Results (2026-04-30)

macOS (darwin/arm64) sender to Linux (amd64) receiver over wired LAN (<2ms RTT).

| Test | Transport | Duration | Avg Speed | Result |
|------|-----------|----------|-----------|--------|
| T1 (baseline) | QUIC | 25.2s | **85.2 MB/s** | Sawtooth (60-144 MB/s oscillation) |
| T2 (TCP-for-LAN) | TCP+yamux | 26.8s | **80.1 MB/s** | Sawtooth, **6% SLOWER** |
| scp (reference) | raw TCP | ~20s | **107 MB/s** | Flat, no sawtooth |

Daemon logs confirmed TCP was used: `bulk-stream: opened stream on new TCP LAN connection`.

## Why It Failed: yamux Head-of-Line Blocking

The hypothesis was wrong. TCP TSO offload saves syscalls, but yamux multiplexing 8 worker streams over one TCP connection introduces worse problems:

**1. Head-of-line blocking.** TCP is one byte stream. yamux frames all 8 streams into it. If a TCP packet is lost, the kernel retransmits it. During retransmission, ALL 8 streams are blocked - even streams whose data wasn't in the lost packet. QUIC's native per-stream multiplexing doesn't have this problem.

**2. Window stalls.** yamux starts each stream with a 256 KB receive window. The receiver must ACK and grow the window before the sender can send more. With 8 streams, all 8 fill their windows simultaneously, all 8 stall waiting for window updates, all 8 resume simultaneously. This creates the sawtooth pattern - bursts of data between coordinated stalls.

**3. Why scp doesn't have this problem.** scp sends one file over one raw TCP stream. No multiplexer. No window coordination. No HOL blocking. The kernel's TCP stack manages flow control directly with TSO offload. That's why scp gets 107 MB/s flat.

**The real bottleneck is the multiplexer, not the transport.**

## What Would Actually Work

**Per-worker TCP connections**: instead of 8 streams over 1 TCP+yamux connection, open 8 SEPARATE TCP connections - one per worker. Each gets its own kernel TSO offload, congestion window, and retransmission. No HOL blocking. Each worker behaves like its own scp session. Expected aggregate: ~107+ MB/s.

This approach doesn't fight the muxer - it routes around it entirely. Replacing yamux is a dead end - libp2p's own proposal ([specs#377](https://github.com/libp2p/specs/issues/377)) has been stalled with zero replies since November 2021. The champion (marten-seemann) left the project. The strategic direction is QUIC, not a better TCP muxer.

The per-worker-conn approach needs a thought experiment for: (1) connLogger interaction with duplicate TCP conns, (2) connection cleanup after transfer, (3) rcmgr accounting (8 of 32 per-peer slots), (4) receiver-side impact.

## Files Changed (13 files, ~300 lines new + ~50 changed)

### New Files
- `pkg/sdk/addrfilter.go` - `FilterTCPAddrs`, `FilterLANAddrs`, `isCGNAT` (pure multiaddr filters)
- `pkg/sdk/bulkstream.go` - `OpenBulkStream`, `BulkStreamOpener`, `dialTCPLAN`, `IsTCPConn`, per-peer dial mutex
- `pkg/sdk/bulkstream_test.go` - 6 test cases (TCP/LAN filter, CGNAT, loopback, WS exclusion, multiaddr walk)

### Modified Files
- `internal/config/config.go` - `LANTransport` field on `NetworkConfig`
- `internal/config/loader.go` - Validation in 3 `Validate*` funcs + `validateLANTransport`
- `internal/config/loader_test.go` - LANTransport validation tests (invalid + 3 valid values)
- `cmd/shurli/cmd_config.go` - `network.lan_transport` in `validConfigKeys`
- `plugins/filetransfer/handlers.go` - Send uses `BulkStreamOpener`, download uses `OpenBulkStream`
- `plugins/filetransfer/plugin.go` - `bulkStreamEnabled` field, RequeuePersisted uses `OpenBulkStream`
- `plugins/filetransfer/transfer.go` - `Transport` field on TransferProgress/Snapshot, TS-5b retry uses `OpenBulkStream`
- `plugins/filetransfer/transfer_grants.go` - `transportFromStream` helper (tcp/quic/relay detection)
- `plugins/filetransfer/commands.go` - Transport display in `shurli transfers` (detail + table + watch)
- `docs/QUIC-TRANSPORT.md` - TCP-for-LAN config documented with RST caveat + platform note

### Core Files NOT Changed (frozen)
network.go, peermanager.go, mdns.go, pathdialer.go, hedged.go, connstream.go, serve_common.go

## Thought Experiment Summary

16 rounds, 104 findings (7 CRITICAL, 15 IMPORTANT, 82 SAFE), zero deferrals. Full plan: `memory/plan-item-19-tcp-for-lan.md`.

Key findings addressed in code:
- R11-F1/F12: per-peer dial mutex prevents ClearAddrs races with mDNS
- R11-F10: correct TTL restore (ConnectedAddrTTL on success, RecentlyConnectedAddrTTL on failure)
- R14-I1: ForceDirectDial prevents short-circuit to existing relay
- R15-F1/F2: TS-5b retry wired to OpenBulkStream
- R15-F3: BulkStreamOpener resets captured conn on IsClosed
- R15-F11: Transport field for operator visibility

## 3 Self-Audit Rounds

10 issues found and fixed across 3 audit rounds:
1. isCGNAT needed To4() for 16-byte IPv4-mapped IPv6
2. Transport field never set (4 set sites added)
3. commands.go display missing (detail + table + watch modes)
4. Indentation bug in TS-5b reconnect loop
5. BulkStreamOpener reset logic refined (only reset on IsClosed, not transient stream errors)
6. Tautological test replaced with real config validation tests
7. Watch-mode Printf missing transportTag variable
8. isTCPConn exported as IsTCPConn for plugin access
9. transportFromStream returns "relay" for Limited connections (not misleading "tcp")
10. FilterTCPAddrs tightened to exclude WebSocket/WSS/circuit relay addrs

## Decision: Not Merged

The code is correct, well-audited, and harmless (config-gated, default=auto). But it provides no throughput improvement due to yamux HOL blocking. Per Musk's 5-step algorithm: **delete the part.** Complexity that doesn't improve the product doesn't ship.

The reusable pieces (`FilterTCPAddrs`, `FilterLANAddrs`, `IsTCPConn`, `transportFromStream`, `Transport` field) may be cherry-picked if the per-worker-conn approach is pursued later.
