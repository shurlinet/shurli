# FT-Y: Verified-LAN Classification Migration

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-VL01 to ADR-VL05 |
| **Primary Commits** | ab9b758 |

Shurli classifies every peer connection as LAN, Direct, or Relay. That classification drives trust-making decisions: whether to apply Reed-Solomon erasure coding, whether to enforce per-peer bandwidth budgets, how many parallel streams to open, how often to save checkpoint state, and whether transport policy allows the connection at all.

The classification was wrong. Eight trust-making call sites used bare RFC 1918 mask checks to decide "is this peer on the LAN?" Any routed private IPv4 address passed the mask. Carrier-grade NAT deployments, Docker bridge networks, VPN tunnel overlays, and multi-WAN cross-links all present private addresses that match RFC 1918 but traverse routers, cross network boundaries, and suffer packet loss. Treating them as LAN disabled protections that exist for unreliable paths.

This journal documents the migration from bare-mask LAN classification to mDNS-verified LAN detection across all trust-making code. It should be read beside [ADR-Y09](streaming-protocol-rewrite.md) in the streaming protocol journal, which documents the original decision to disable Reed-Solomon on LAN, and [ADR-RS06 and ADR-RS07](reed-solomon-erasure-recovery.md) in the erasure recovery journal, which document why RS misconfiguration has memory and security consequences.

---

## ADR-VL01: Bare RFC 1918 Was the Wrong LAN Signal for Trust Decisions

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Accepted |
| **Commit** | ab9b758 |

### Context

`AnyConnIsLAN` and `ClassifyTransport` used Go's `net.IP.IsPrivate()` to decide whether a connection was on the local network. Any address in RFC 1918 (10.0.0.0/8, 172.16.0.0/12, 192.168.0.0/16) or RFC 6598 (100.64.0.0/10) passed the check and was classified as LAN.

Four real-world network topologies break this assumption:

1. **Carrier-grade NAT**: satellite and mobile providers present 10.x.x.x or 100.64.x.x source addresses that traverse the provider's NAT infrastructure before reaching the internet.
2. **Docker bridge networks**: container bridges use 172.17-21.x.x addresses that are local to the host but not on any physical LAN segment shared with peers.
3. **VPN tunnel overlays**: WireGuard, OpenVPN, and IKEv2 tunnels assign 10.x or 172.x overlay addresses. Traffic crosses the internet inside the tunnel.
4. **Multi-WAN cross-links**: a secondary uplink cabled into a different router creates a routed private path between two LANs. Both sides see RFC 1918 addresses, but the path crosses a router boundary with real latency and potential packet loss.

Eight trust-making call sites depended on this classification. When they misclassified a WAN peer as LAN, the consequences were:

- **Reed-Solomon erasure coding disabled** on a genuinely unreliable link, removing the ability to recover from packet loss without retransmission. This was the direct blocker for the G3 physical test (cross-session silent-corruption resume).
- **Per-peer bandwidth budgets bypassed**, letting WAN peers transfer without policy enforcement.
- **LAN-optimized stream counts** applied to WAN paths, where fewer streams would have been more appropriate.
- **Checkpoint save cadence set to 5 seconds** instead of 1 second, reducing resume granularity on flaky links where aggressive checkpointing matters most.

### Decision

Replace bare RFC 1918 mask checks with mDNS-verified LAN detection for every trust-making decision. The migration was not a new feature. `LANRegistry`, the mDNS-verified LAN signal, already existed and was already used for dial filtering, connection logging, and mDNS deduplication. The problem was that trust-making code in the file transfer plugin and SDK service registry had not been migrated to use it.

### Alternatives Considered

**Keep bare-mask and add exceptions for known CGNAT ranges** would turn LAN classification into a deny-list. New network topologies would require new exceptions. The approach cannot scale.

**Check round-trip time to distinguish LAN from WAN** would add a measurement dependency to a classification that needs to be instant. RTT also varies with load, making the boundary unstable.

**Require explicit operator configuration for LAN peers** would push a network-topology decision onto every user. Shurli's design prefers automatic discovery over manual configuration.

### Consequences

- Trust-making code no longer depends on address-range heuristics.
- The same mDNS signal that proves discovery also proves LAN proximity.
- Peers on routed private paths are conservatively treated as WAN until mDNS verifies them.
- The G3 physical test (cross-session silent-corruption resume) became possible because RS erasure was correctly enabled on the routed-private path.

### Physical Verification

A sanity transfer (1 MB) to a peer on a routed-private path confirmed RS was correctly enabled after the migration: CLI output showed `[RS 10%, 1 parity]`. Pre-migration, the same path would have been misclassified as LAN with RS disabled. End-to-end validation of RS behavior on the corrected classification is in ADR-VL05.

**Reference**: `pkg/sdk/plugin_policy.go`, `pkg/sdk/peermanager.go`, `plugins/filetransfer/transfer.go`

---

## ADR-VL02: mDNS Multicast as Proof of LAN Proximity

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Accepted |
| **Commit** | ab9b758 |

### Context

LAN classification needs a signal that is true if and only if the peer is on the same link-local network segment. IP address ranges fail because private addresses can be routed. Interface names fail because naming conventions vary across operating systems. Latency thresholds fail because they are load-dependent and topology-dependent.

mDNS (multicast DNS) uses link-local multicast (224.0.0.251 for IPv4, ff02::fb for IPv6). Link-local multicast packets are not forwarded by routers. If a peer responds to an mDNS query, the response physically could not have crossed a router boundary. Reception of an mDNS response is therefore a proof of link-local proximity that no IP address check can provide.

### Decision

Use `LANRegistry` as the single authoritative LAN signal for all trust-making decisions:

1. When mDNS discovers a peer, `LANRegistry` records the peer ID and the verified remote IP.
2. `HasVerifiedLANConn` checks whether a peer has at least one live non-relay connection whose remote IP matches an mDNS-verified address.
3. All trust-making code queries this signal instead of checking address ranges.

Loopback addresses (127.0.0.0/8, ::1) and link-local unicast addresses (169.254.0.0/16, fe80::/10) are classified as LAN without mDNS verification. These address families cannot cross a router by definition, so they are not in the bare-RFC1918 false-positive class.

### Alternatives Considered

**ARP table inspection** would prove link-layer adjacency but requires platform-specific system calls and elevated privileges on some operating systems.

**Subnet mask comparison against local interfaces** would catch some cases but would still misclassify VPN and container interfaces that share a subnet mask with real LAN interfaces.

**Treat all private addresses as WAN unless explicitly allowed** would be safe but would disable LAN optimizations for every legitimate LAN peer, degrading the common case.

### Consequences

- LAN classification is physically grounded in link-local multicast propagation rules, not address-range conventions.
- Peers that have not been discovered via mDNS are conservatively classified as non-LAN.
- Two LAN machines communicating via public IPv6 (common when both have globally routable addresses) are correctly classified as LAN if mDNS has discovered the peer, even though the stream's IP address is public.
- The signal depends on mDNS discovery running. A daemon that has not completed mDNS browse will classify all peers as non-LAN until discovery completes.

### Physical Verification

The unit test matrix in ADR-VL03 validates that mDNS-verified peers classify as LAN (subtest "private IPv4 WITH mDNS verification is LAN") while unverified private addresses classify as Direct (subtest "routed private IPv4 WITHOUT mDNS verification is Direct"). The G3 and G2-4 physical tests in ADR-VL05 confirm the end-to-end behavior over a real routed-private path.

**Reference**: `pkg/sdk/peermanager.go`, `pkg/sdk/network.go`

---

## ADR-VL03: VerifiedTransport as the Trust-Making Classifier

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Accepted |
| **Commit** | ab9b758 |

### Context

The migration needed a function that plugin code and SDK service code could call with the same semantics. `ClassifyTransport` could not be modified in place because non-trust callers (logging, display, metrics, future WASM plugins) still needed the simpler bare-mask classifier. Adding an mDNS callback parameter to `ClassifyTransport` would change its signature and break every existing caller for a semantic change that only trust-making code needs.

### Decision

Add `VerifiedTransport` as a new function with explicit precedence:

1. **Relay**: if the connection is limited (circuit relay), return `TransportRelay`.
2. **Loopback or link-local**: if the remote address is loopback or link-local unicast, return `TransportLAN`. These addresses cannot traverse routers.
3. **mDNS-verified**: if the `hasVerifiedLANConn` callback returns true for the peer, return `TransportLAN`.
4. **Otherwise**: return `TransportDirect`.

A nil callback degrades gracefully: loopback and link-local are still classified as LAN, but all other addresses fall through to `TransportDirect`. This makes the function safe to call before mDNS wiring is complete or in test environments without a full network stack.

A connection-level variant, `verifiedClassifyConnTransport`, mirrors the same precedence for `OpenPluginStreamOnConn`, which receives a `network.Conn` instead of a `network.Stream`.

`Network.HasVerifiedLANConn` wraps the `LANRegistry` lookup with nil-safety so callers do not need to know about the registry type or host binding.

### Alternatives Considered

**Modify ClassifyTransport to accept a callback** would change the function signature and force every caller to pass a callback or nil. Non-trust callers do not need mDNS verification and should not pay the API complexity.

**Add a method on Network instead of a free function** would tie the classifier to the SDK type. Plugin code needs a function that takes a stream and a callback, not a method on a type the plugin does not own.

**Use a global registry lookup inside VerifiedTransport** would hide the dependency and make testing difficult. The explicit callback keeps the dependency visible and injectable.

### Consequences

- Trust-making code calls `VerifiedTransport`. Non-trust code continues to call `ClassifyTransport`.
- The two classifiers agree on relay and loopback/link-local. They diverge only on routable private addresses, which is exactly the bug class being fixed.
- Plugin code receives the callback through `TransferConfig.HasVerifiedLANConn`, wired at plugin startup.
- The 13-subtest unit matrix plus 2 nil-safety tests and a dedicated G3 regression test pin the classification behavior for every address class.

### Physical Verification

The unit test matrix covers 13 address-class combinations plus 2 nil-safety tests plus 1 regression test pinning the G3 bug class:

| Address class | mDNS verified | Expected | Description |
|---|---|---|---|
| Relay (limited) | n/a | Relay | Circuit relay regardless of IP |
| Loopback IPv4 | no | LAN | Cannot cross router |
| Loopback IPv6 | no | LAN | Cannot cross router |
| Link-local IPv4 | no | LAN | RFC 3927, cannot cross router |
| Link-local IPv6 | no | LAN | fe80::/10, cannot cross router |
| Routed private IPv4 | no | Direct | The G3 blocker: bare-mask would say LAN |
| Private IPv4 | yes | LAN | mDNS-verified, real LAN |
| Public IPv4 | no | Direct | Public address, no mDNS |
| Public IPv6 | yes | LAN | Two LAN machines on public IPv6 |
| Public IPv6 | no | Direct | Public IPv6 without mDNS |
| Private IPv4 (nil callback) | n/a | Direct | Conservative fallback |
| Loopback (nil callback) | n/a | LAN | Loopback honored even without callback |
| CGNAT range (100.64.x) | no | Direct | RFC 6598, must be WAN without mDNS |

All test addresses use RFC 5737, RFC 3849, and obviously-fake RFC 1918 documentation ranges.

**Reference**: `pkg/sdk/plugin_policy.go`, `pkg/sdk/plugin_policy_test.go`, `pkg/sdk/network.go`

---

## ADR-VL04: Dead Code Deletion and Public API Preservation

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Accepted |
| **Commit** | ab9b758 |

### Context

The migration left several functions with zero callers. Each needed a decision: delete, keep as public API, or rename with updated semantics.

### Decision

**Deleted** (zero callers, superseded by mDNS-verified equivalents):

- `ClassifyPeerTransport`: checked all connections to a peer for any private IPv4 address. Same bare-mask bug as `ClassifyTransport`. Zero callers after migration. The commit that introduced it (`ab9b758~1`) documented it as a helper for "LAN detection across all connections to a peer," but the detection was unreliable.
- `AnyConnIsLAN`: returned true if any non-relay connection used a private IPv4 remote address. Superseded by `LANRegistry.HasVerifiedLANConn`.
- `HasLiveLANConnection`: zombie-safe version of `AnyConnIsLAN` that also checked whether the local IP was on an active interface. Superseded by `LANRegistry.HasVerifiedLANConn`.
- `IsLANPeer` callback on `TransferConfig` and `TransferService`: renamed to `HasVerifiedLANConn` with stricter semantics (connection-IP-verified instead of peer-seen-recently).

**Kept as public API** (documented, has non-trust use cases):

- `ClassifyTransport`: the simpler bare-mask classifier. Zero internal trust-making callers after migration, but it is a documented export in `docs/SDK.md` for non-trust uses: logging, display, metrics, and future Layer 2 WASM plugins that will not have `LANRegistry` access. The commit that deleted `ClassifyPeerTransport` deliberately kept `ClassifyTransport` with an updated docstring redirecting trust-making callers to `VerifiedTransport`.
- `IsLANMultiaddr`: returns true if a multiaddr starts with a private IPv4 address. Used by `StripNonLANAddrs` in mDNS peerstore filtering and by mDNS address deduplication, where no live connection exists yet and mDNS verification is not possible. Address-level filtering is a different problem from connection-level trust classification.

### Alternatives Considered

**Delete ClassifyTransport too** would remove a documented public API that non-trust code relies on. Its bare-mask behavior is correct for display and logging purposes, where misclassification has no security consequence.

**Keep all functions and mark old ones as deprecated** would leave unreliable code available for accidental use. The functions were not deprecated; they were wrong for their stated purpose.

**Rename ClassifyTransport to ClassifyTransportUnsafe** would stigmatize a function that is correct for its non-trust use cases.

### Consequences

- Trust-making code has exactly one path: `VerifiedTransport` or `HasVerifiedLANConn`.
- Non-trust code has `ClassifyTransport` and `IsLANMultiaddr`, both documented with clear guidance on when to use them.
- No deprecated shims or backward-compatibility wrappers. The old functions are deleted, not hidden.
- The `TransferConfig` callback rename from `IsLANPeer` to `HasVerifiedLANConn` makes the semantic change visible at the API boundary.

### Physical Verification

Code-verified. `grep -rn 'ClassifyPeerTransport\|AnyConnIsLAN\|HasLiveLANConnection' pkg/ plugins/` returns zero matches in production code (only historical references in comments and the `ClassifyTransport` docstring). `grep -rn 'IsLANPeer' plugins/filetransfer/` returns zero matches, confirming the callback rename is complete. All 27 packages pass `go test -race -count=1`.

**Reference**: `pkg/sdk/plugin_policy.go`, `pkg/sdk/peermanager.go`, `plugins/filetransfer/transfer.go`, `plugins/filetransfer/plugin.go`

---

## ADR-VL05: What Verified LAN Classification Does Not Replace

| | |
|---|---|
| **Date** | 2026-04-18 |
| **Status** | Accepted |
| **Commit** | ab9b758 |

### Context

The migration scope was trust-making code: decisions where misclassifying a WAN peer as LAN has security, correctness, or reliability consequences. Several other uses of address classification exist in the codebase and were intentionally left unchanged.

### Decision

The following are explicitly outside the scope of verified-LAN classification:

1. **Non-trust display and logging**: `ClassifyTransport` remains the classifier for log messages, CLI status output, Prometheus labels, and any context where a wrong LAN label has no behavioral consequence.

2. **Address-level filtering without a live connection**: `IsLANMultiaddr` is used by `StripNonLANAddrs` (mDNS peerstore hygiene) and mDNS address deduplication. These operate on multiaddrs from the peerstore, not on live connections. mDNS verification requires a connection, so address-level code cannot use it.

3. **IPv6-only LAN peers without mDNS**: two machines on the same LAN segment communicating via globally routable IPv6 addresses will classify as `TransportDirect` until mDNS discovers the peer. This is a known gap. mDNS browse runs on a 30-second cycle, so the gap is transient. Once mDNS verifies the peer, subsequent trust decisions classify correctly. Making this gap smaller would require either continuous mDNS probing (expensive) or a parallel LAN detection mechanism (complexity without clear benefit for a transient condition).

4. **Reed-Solomon, bandwidth budgets, hedging, and relay grant receipts**: these systems consume the `TransportType` output. The verified-LAN migration changed how `TransportType` is determined, not what those systems do with it. Their architecture is documented in their own journals.

### Alternatives Considered

**Migrate all address classification to VerifiedTransport** would force address-level code to carry a callback it cannot use. The distinction between connection-level trust and address-level filtering is architecturally correct.

**Add a secondary LAN detection mechanism for the IPv6 gap** would add complexity for a condition that self-resolves within one mDNS cycle. The conservative default (treat as WAN) is safe.

### Consequences

- Non-trust code is unaffected by the migration.
- Address-level filtering continues to use bare-mask checks where no connection exists.
- The IPv6-only LAN gap is documented and accepted as transient.
- Future work on LAN classification (if needed) can focus on the IPv6 gap without re-examining the trust-making migration.

### Physical Verification

The verified-LAN migration was physically tested through the G3 and G2-4 test suite, which validated the most critical trust-making call site: the RS erasure gate.

**Sanity transfer** (1 MB to a peer on a routed-private path): CLI output showed `[RS 10%, 1 parity]`, confirming RS was correctly enabled on a path that pre-migration would have been misclassified as LAN with RS disabled.

**G3: cross-session silent-corruption resume** (100 MB random data, chunk 42 corrupted by sender-side test hook):

| Metric | Value |
|--------|-------|
| RS overhead observed | 17.8% (117.8 MB wire / 100 MB data) |
| Corrupted chunk | 42 (XOR bit-flip on wire) |
| Checkpoint have-bit for chunk 42 | Cleared (hex verified: offset 0x65 = 0xfb, bit 2 = 0) |
| Resume retransmit | 1 chunk out of 437 (0.23%) |
| Final sha256 | Match (source and receiver identical) |

**G2-4: multi-file boundary corruption** (60 MB directory, 3 files, chunk 175 at file boundary corrupted):

| Metric | Value |
|--------|-------|
| Total chunks | 523 |
| Corrupted chunk | 175 (straddles file1-file2 boundary) |
| Recovery method | Silent RS reconstruction within stripe (no resume needed) |
| Per-file sha256 | All 3 match source |

Both tests ran over a routed-private path that would have been misclassified as LAN before the migration. RS being correctly enabled on that path was the precondition for both tests to succeed.

**Reference**: `pkg/sdk/plugin_policy.go`, `pkg/sdk/peermanager.go`, `plugins/filetransfer/transfer.go`, `plugins/filetransfer/transfer_parallel.go`, `plugins/filetransfer/transfer_multipeer.go`, `pkg/sdk/service.go`, `pkg/sdk/network.go`

---

## Public Notes

This journal omits private node names, peer IDs, addresses, providers, hardware identifiers, and topology details. The routed-private path used in G3 and G2-4 testing is described generically. Performance numbers are included only where they demonstrate architectural impact: RS overhead confirming erasure was active, checkpoint bit-level verification confirming corruption handling, and resume efficiency confirming checkpoint correctness. All test addresses in the unit matrix use RFC 5737, RFC 3849, and obviously-fake RFC 1918 documentation ranges.
