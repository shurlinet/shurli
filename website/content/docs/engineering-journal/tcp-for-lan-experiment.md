---
title: "FT-Y - TCP-for-LAN Experiment"
weight: 37
description: "Negative-result experiment proving that TCP plus yamux does not beat QUIC for Shurli LAN bulk transfers on macOS, despite TCP TSO avoiding QUIC sendmsg overhead."
---
<!-- Auto-synced from docs/engineering-journal/tcp-for-lan-experiment.md by sync-docs - do not edit directly -->


Each entry in this engineering journal is an Architecture Decision Record (ADR).

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Complete, not merged |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-TL01 to ADR-TL07 |
| **Primary Commits** | c388608 |

The TCP-for-LAN experiment tested whether Shurli file-transfer bulk streams should use [Transmission Control Protocol (TCP)](https://grokipedia.com/page/Transmission_Control_Protocol) on verified local-area-network peers instead of [QUIC](https://grokipedia.com/page/QUIC) ([RFC 9000](https://www.rfc-editor.org/rfc/rfc9000.html)) over UDP. The motivation was real: macOS senders do not get the same UDP packet batching path that Linux senders get, while raw TCP can use segmentation offload.

The result was negative and useful. TCP did not lose because raw TCP was slow. It lost because Shurli is not `scp`: Shurli transfers use [libp2p](https://grokipedia.com/page/libp2p) ([project site](https://libp2p.io/)) streams, and libp2p streams over TCP run through [yamux (HashiCorp's stream multiplexer)](https://github.com/hashicorp/yamux). The bottleneck moved from QUIC packet syscalls to TCP multiplexer behavior.

This record preserves the measurement, the implementation shape, and the deletion decision. The experiment code remains archived on the separate [`tcp-for-lan` branch](https://github.com/shurlinet/shurli/tree/tcp-for-lan). The production `dev` branch keeps the architecture lesson, not the experimental implementation.

---

## ADR-TL01: macOS QUIC LAN Ceiling Was Real

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Accepted |
| **Commit** | c388608 |

### Context

Shurli's current transport stack registers QUIC first, TCP second, and WebSocket last through go-libp2p. QUIC is the right default for peer-to-peer transport: it gives encrypted streams, native multiplexing, shorter handshakes, connection migration, and better alignment with libp2p's strategic direction. Shurli's transport notes already document the macOS-specific limitation.

The macOS send path has a real LAN ceiling because macOS lacks UDP [Generic Segmentation Offload (GSO)](https://docs.kernel.org/networking/segmentation-offloads.html) support in the path used by [quic-go](https://github.com/quic-go/quic-go). In the currently vendored quic-go version, the Darwin helper still returns GSO disabled. Without GSO, a high-rate QUIC sender emits one UDP packet per `sendmsg()` syscall. Physical profiling during large LAN sends showed about 50.4% of sender CPU samples in `sendmsg()`, while the process was mostly idle from the application's point of view.

The directional evidence mattered. macOS sending over Shurli QUIC measured about 80-90 MB/s and showed a sawtooth pattern. Linux sending to macOS measured about 103 MB/s and was flatter. Raw `scp` over TCP measured about 107 MB/s and was flat on the same kind of local transfer path. That proved the ceiling was in the macOS QUIC send path, not in receiver disk writes, file chunking, compression, Go garbage collection, or Shurli's stream count.

TCP avoided this specific kernel-side problem because TCP can use TCP Segmentation Offload (TSO), described with GSO in the [Linux kernel segmentation offloads documentation](https://docs.kernel.org/networking/segmentation-offloads.html). The experiment was therefore worth running.

### Decision

Treat the macOS QUIC send ceiling as a measured platform limitation, not a speculative tuning problem.

The experiment's job was not to replace QUIC globally. It was to test whether file-transfer bulk streams on verified LAN peers could route around the macOS UDP send path while preserving Shurli's QUIC-first core.

### Alternatives Considered

**Tune quic-go buffer sizes or pacing** would not help because the bottleneck is in the kernel UDP send path, not in quic-go's application-level buffering. The macOS kernel does not offer a GSO path for quic-go to use regardless of configuration.

**Wait for macOS to add UDP GSO** would leave the ceiling indefinitely. Apple has not signaled any plan to expose `UDP_SEGMENT` on Darwin.

**Accept the ceiling and stop** was the eventual outcome after the TCP experiment disproved the alternative, but the experiment had to run first to prove the ceiling was not trivially solvable.

### Consequences

- The baseline problem is real and remains documented.
- QUIC stays the default transport for core networking.
- Any workaround must beat the actual Shurli transfer stack, not raw TCP in isolation.

### Physical Verification

Physical validation observed QUIC LAN send from a macOS sender to a Linux receiver at about 85.2 MB/s over 25.2 seconds with sawtooth behavior. The raw TCP reference remained about 107 MB/s and flat. These measurements are validation data from a single physical setup, not formal benchmarks.

**Reference**: [QUIC-TRANSPORT.md](https://github.com/shurlinet/shurli/blob/tcp-for-lan/docs/QUIC-TRANSPORT.md), [network.go](https://github.com/shurlinet/shurli/blob/dev/pkg/sdk/network.go)

---

## ADR-TL02: TCP-for-LAN Had To Be Scoped To Bulk Transfer Streams

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Accepted |
| **Commit** | c388608 |

### Context

The first tempting implementation was a global dial-ranker or swarm-level preference for TCP on LAN. That would have been too broad. Core Shurli networking uses QUIC for ping, traceroute, [multicast DNS (mDNS)](https://grokipedia.com/page/Multicast_DNS)-discovered control paths, PeerManager reconnection, relay paths, service-query, proxy/control streams, and general plugin stream opening. Changing the core dial strategy would risk behavior that was unrelated to the file-transfer throughput question.

The corrected scope was narrower: only file-transfer bulk streams could prefer TCP, and only when the local configuration explicitly requested it. Core networking stayed unchanged.

The existing SDK already had the right primitive for this kind of scoped routing. `OpenPluginStreamOnConn` opens a protocol-negotiated stream on a specific libp2p connection. That made it possible to choose a connection for bulk data without teaching every caller in the core network stack about this experiment.

### Decision

Implement the experiment as an SDK capability for bulk streams:

- `network.lan_transport: tcp` opted into the experiment.
- The default remained `auto`, which preserved normal QUIC behavior.
- File-transfer send, single-peer download, failover retry, and persisted queue requeue could use `OpenBulkStream`.
- Core files such as `network.go`, `peermanager.go`, `mdns.go`, `pathdialer.go`, `hedged.go`, `connstream.go`, and `serve_common.go` stayed frozen.

### Alternatives Considered

**Global dial-ranker preference** was rejected because it would affect core networking, not only bulk data.

**Plugin-only ad hoc dialing** was rejected because stream-opening policy belongs in the SDK when more than one plugin may eventually need bulk-data semantics.

**Keep QUIC only and stop there** would preserve simplicity, but it would leave a plausible macOS LAN optimization untested.

### Consequences

- The experiment had a clear blast radius.
- Core QUIC behavior and relay behavior stayed untouched.
- The implementation still had to carry enough SDK complexity to prove TCP was actually used.

**Reference**: [connstream.go](https://github.com/shurlinet/shurli/blob/dev/pkg/sdk/connstream.go), [network.go](https://github.com/shurlinet/shurli/blob/dev/pkg/sdk/network.go), [handlers.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/plugins/filetransfer/handlers.go), [plugin.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/plugins/filetransfer/plugin.go)

---

## ADR-TL03: Peerstore Snapshot TCP Dialing Proved TCP Was Actually Used

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Accepted |
| **Commit** | c388608 |

### Context

A normal libp2p `host.Connect` could not make TCP win reliably. go-libp2p's default dial ranker explicitly prefers QUIC when QUIC and TCP addresses are both available. For private addresses, TCP dials are delayed by 30ms after QUIC dials. On a LAN, that is enough time for QUIC to win the race before TCP begins.

Even after a TCP connection exists, stream selection is not transport-aware by default. The go-libp2p swarm chooses the best connection by avoiding limited relayed connections, preferring direct connections, then preferring the connection with more streams, then falling back to connection order. If both QUIC and TCP are direct LAN connections, a normal stream open can choose either unless the caller pins the stream to a specific connection.

### Decision

The experimental branch used a peerstore snapshot and restore flow to force a TCP LAN dial only for bulk stream opening:

- Filter peerstore addresses to raw TCP LAN addresses.
- Exclude QUIC, UDP, WebSocket, secure WebSocket, and circuit relay addresses.
- Use `ForceDirectDial` so an existing relay connection could not short-circuit the direct TCP dial.
- Clear swarm backoff before the TCP attempt.
- Open the plugin stream on the selected TCP connection.
- Fall back to `HedgedOpenStream` if any TCP step failed.

The branch also added operator visibility:

- `OpenBulkStream` logged when a new TCP LAN connection was used.
- `TransferProgress` and `TransferSnapshot` gained a `Transport` field.
- CLI transfer detail, table, and watch views displayed the transport tag.
- Relay connections were reported as `relay`, not misleadingly as `tcp`.

### Alternatives Considered

**Normal `host.Connect` with both transports available** was rejected because the dial ranker gives QUIC a head start on LAN addresses. TCP would almost never win the dial race.

**Custom dial ranker that prefers TCP on LAN** was rejected in ADR-TL02 because it would affect core networking globally, not just bulk transfer streams.

**Connection tagging after dial** would avoid the peerstore snapshot, but go-libp2p does not expose a transport-aware stream selector. The caller cannot request "open a stream on a TCP connection" without pinning to a specific `network.Conn`.

### Consequences

- The experiment could prove that TCP was actually exercised.
- The implementation was correct for a measurement branch.
- The same complexity was too much to ship without a throughput win.

### Physical Verification

The real TCP test was run after a full daemon restart because config reload did not re-read the YAML `network` section for this path. Daemon logs then confirmed the TCP bulk-stream path opened a new TCP LAN connection. Only the post-restart TCP result is part of this record.

**Reference**: [bulkstream.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/pkg/sdk/bulkstream.go), [addrfilter.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/pkg/sdk/addrfilter.go), [transfer_grants.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/plugins/filetransfer/transfer_grants.go), [commands.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/plugins/filetransfer/commands.go)

---

## ADR-TL04: Physical Measurement Disproved TCP Plus Yamux

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Rejected for production |
| **Commit** | c388608 |

### Context

The hypothesis was clear: if macOS QUIC sends were syscall-bound, then sending file-transfer bulk streams over TCP on a verified LAN should let TCP TSO avoid that syscall overhead and approach raw `scp` throughput.

The experiment did not validate that hypothesis.

### Decision

Do not merge TCP-for-LAN bulk streams into `dev` or production.

### Alternatives Considered

**Merge behind config flag anyway** was rejected because the measured result was slower, not equal. Shipping a config option that makes transfers 6% slower adds complexity for negative value.

**Run more test iterations** was considered, but the single-run result was consistent with the yamux HOL blocking analysis in ADR-TL05. Additional runs would confirm the same structural limitation.

### Physical Verification

| Test | Transport | Duration | Observed Average | Result |
|------|-----------|----------|------------------|--------|
| T1 baseline | QUIC | 25.2s | 85.2 MB/s | Sawtooth, 60-144 MB/s oscillation |
| T2 TCP-for-LAN | TCP plus yamux | 26.8s | 80.1 MB/s | Sawtooth, about 6% slower than QUIC |
| scp reference | raw TCP | about 20s | 107 MB/s | Flat |

The first attempted T2 run was excluded because the daemon had not restarted, so it still used QUIC. The valid T2 run was the second one, after restart, with the TCP path confirmed by daemon logs.

### Consequences

- The macOS QUIC ceiling is real.
- TCP plus yamux did not solve it.
- The branch was preserved for reference and not merged.
- QUIC remains the better production default for Shurli's multiplexed file-transfer path.

**Reference**: [README.md on tcp-for-lan](https://github.com/shurlinet/shurli/blob/tcp-for-lan/README.md)

---

## ADR-TL05: Yamux Became The Bottleneck

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Accepted |
| **Commit** | c388608 |

### Context

Shurli file transfer is not a single raw byte stream. The transfer path can use multiple worker streams over the same peer connection. On QUIC, those are native QUIC streams. On TCP, those libp2p streams are multiplexed through yamux over one ordered TCP byte stream.

That changes the failure mode. A lost TCP segment or receive-window stall blocks the ordered TCP byte stream underneath every yamux stream. That creates [head-of-line (HOL) blocking](https://grokipedia.com/page/Head-of-line_blocking) across the workers. QUIC does not remove all congestion effects, but its native stream model avoids one lost stream's data blocking unrelated streams at the transport framing layer.

yamux also starts each stream with a 256 KB window and expands through window update frames. With several workers sending bulk data together, workers can fill windows together, stall together, receive updates together, and resume together. That can recreate the same sawtooth shape that the TCP experiment was supposed to remove.

### Decision

Treat TCP plus yamux as the wrong abstraction for this optimization.

The experiment did not prove that TCP is unsuitable for LAN bulk transfer. It proved that one TCP connection carrying multiple yamux streams is not better than QUIC for this Shurli workload.

### Alternatives Considered

**Replace yamux** was rejected as a strategic dead end. libp2p's own [specs#377 proposal](https://github.com/libp2p/specs/issues/377) imagined a better TCP stream multiplexer, but it has not become the ecosystem path. The practical libp2p direction is QUIC, not a new TCP multiplexer.

**Tune yamux windows** might reduce one symptom, but it would still keep all worker streams behind one ordered TCP byte stream.

**Keep TCP plus yamux behind a config flag** would preserve a choice that measured slower and added complexity.

### Consequences

- QUIC-first architecture is validated for multiplexed file transfer.
- The next bottleneck is clearly identified.
- Future TCP experiments must route around yamux, not merely switch Shurli streams onto TCP.

**Reference**: [bulkstream.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/pkg/sdk/bulkstream.go), [transfer_parallel.go](https://github.com/shurlinet/shurli/blob/dev/plugins/filetransfer/transfer_parallel.go), [libp2p specs#377](https://github.com/libp2p/specs/issues/377)

---

## ADR-TL06: Delete The Part, Why The Code Was Not Merged

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Accepted |
| **Commit** | c388608 |

### Context

The branch was not sloppy code. It was config-gated, default-off, scoped to file-transfer bulk streams, and audited heavily. The implementation went through 16 thought-experiment rounds with 104 findings, then 3 self-audit rounds that found and fixed 10 more issues.

Those self-audit fixes mattered:

- CGNAT detection needed `To4()` for IPv4-mapped IPv6.
- Transport fields were added and then correctly set at all required sites.
- CLI detail, table, and watch displays were wired.
- A retry-loop indentation issue was fixed.
- `BulkStreamOpener` reset logic was narrowed to closed connections.
- Tautological config tests were replaced with real validation tests.
- A watch-mode transport display variable was added before it could become a build break.
- `IsTCPConn` was exported for plugin-side transport detection.
- Relay connections were displayed as `relay`, not `tcp`.
- TCP address filtering was tightened to exclude WebSocket, secure WebSocket, and circuit relay addresses.

The code was plausible. The measurement made it negative value.

### Decision

Do not merge the implementation. Preserve the branch as an archived experiment and ship only the architecture record.

The project's engineering algorithm applies directly:

1. Challenge the requirement.
2. Delete the part.
3. Simplify.
4. Accelerate.
5. Automate last.

Step 2 applies here. Around 300 lines of new code and about 50 lines of changed integration code across 13 files did not improve throughput. Complexity that does not improve the product does not ship.

### Alternatives Considered

**Keep the code on `dev` behind a config flag** was rejected. A config surface that is slower in the only measured scenario is negative value. Users who discover `network.lan_transport: tcp` would expect an improvement and get a regression.

**Delete the branch entirely** was rejected. The code is correct, well-tested, and preserves reusable pieces. Archiving it on a named branch costs nothing and provides reference material for future experiments.

### Consequences

- `dev` avoids a config surface that would be slower in the measured case.
- The branch still preserves useful code for future reference.
- Reusable pieces can be cherry-picked later if a different experiment earns them.
- The public record shows that Shurli deletes disproven optimizations instead of carrying them as "maybe useful later" production paths.

### Reusable Pieces

If a future experiment warrants it, these pieces are candidates for selective reuse:

- `FilterTCPAddrs`
- `FilterLANAddrs`
- `IsTCPConn`
- `transportFromStream`
- the `Transport` field on transfer snapshots
- `network.lan_transport` validation patterns

**Reference**: [README.md on tcp-for-lan](https://github.com/shurlinet/shurli/blob/tcp-for-lan/README.md), [addrfilter.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/pkg/sdk/addrfilter.go), [bulkstream.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/pkg/sdk/bulkstream.go), [transfer_grants.go](https://github.com/shurlinet/shurli/blob/tcp-for-lan/plugins/filetransfer/transfer_grants.go)

---

## ADR-TL07: Per-Worker TCP Connections Remain Future Research

| | |
|---|---|
| **Date** | 2026-04-30 |
| **Status** | Future research |
| **Commit** | c388608 |

### Context

The negative result still leaves one future idea intact: one TCP connection per worker. That would not be eight yamux streams on one TCP connection. It would be multiple independent TCP connections, each carrying one worker stream, so kernel TCP behavior looks closer to several independent `scp`-style flows.

That is a different experiment. It routes around yamux rather than trying to make yamux a better bulk-data multiplexer.

### Decision

Do not present per-worker TCP connections as a plan or shipping direction. Treat it as future research only.

### Alternatives Considered

**Implement per-worker TCP now** was rejected because the current experiment already consumed a full thought-experiment cycle and produced a negative result. Starting a second experiment in the same cycle without validating the first result would compound risk.

**Replace yamux with a custom multiplexer** was rejected. libp2p's own specs#377 proposal for a yamux replacement has been stalled since 2021 with zero replies. The ecosystem's strategic path is QUIC, not a new TCP multiplexer. Building a custom muxer would be maintaining infrastructure that the ecosystem is moving away from.

### Open Questions

- How should connection logging classify several duplicate TCP connections to the same verified-LAN peer?
- Should worker TCP connections be actively closed after transfer completion or left for libp2p idle cleanup?
- How should resource-manager accounting handle one connection per worker?
- What receiver-side inbound connection load is acceptable?
- Does the idea help only macOS senders, or does it improve other local transfer shapes?
- Can it be implemented without changing libp2p core behavior or replacing yamux?

### Consequences

- The current production architecture stays QUIC-first.
- The failed TCP plus yamux path is not confused with the untested per-worker TCP idea.
- Any next experiment has a sharper hypothesis and a smaller target.

**Reference**: [transfer_parallel.go](https://github.com/shurlinet/shurli/blob/dev/plugins/filetransfer/transfer_parallel.go), [network.go](https://github.com/shurlinet/shurli/blob/dev/pkg/sdk/network.go)

---

## Public Notes

This journal omits private topology, node names, IP addresses, peer IDs, hostnames, network operator details, device names, usernames, local paths, and command outputs. Physical numbers are included only because they explain the architecture decision. The experiment code is archived on a separate branch and is not merged into `dev` or production. The measurements are validation data from one physical setup, not formal benchmarks.
