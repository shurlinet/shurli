# Phase 11: Post-Quantum Cryptography

| | |
|---|---|
| **Date** | 2026-04-28 to 2026-05-10 |
| **Status** | Complete (11A+11B+11C) |
| **Phase** | Phase 11 (Post-Quantum Cryptography) |
| **ADRs** | ADR-PQ01 to ADR-PQ06 |
| **Primary Commits** | b271bf4, 36ceca0, 3eedc6c, 9ff2ff7, b2cf954, ed1718e |

Dual-layer post-quantum protection: QUIC X25519MLKEM768 at transport, PQ Noise via go-clatter at application. PQ Noise is a Go port of the Rust [Clatter](https://github.com/jmlepisto/clatter) library, enhanced with Go-native generics, secret zeroing, and ML-DSA-65 signing. 408 interop vectors cross-validated against the Rust original.

This journal covers the integration into Shurli. The go-clatter library itself (github.com/shurlinet/go-clatter) is an independent repo with its own release cycle.

---

### ADR-PQ01: Dual-Layer PQ Strategy

| | |
|---|---|
| **Commit** | b271bf4 (Phase 0 QUIC verification), 36ceca0 (Phase 2 Batch 1 PQ Noise) |

**Context**: Quantum computers threaten classical Diffie-Hellman (X25519) and ECDSA. "Harvest now, decrypt later" attacks mean PQ protection is urgent for infrastructure that carries long-lived secrets. Shurli needs PQ protection on every connection type.

**Problem**: QUIC connections get PQ automatically via Go's TLS 1.3 (X25519MLKEM768 since Go 1.24). But TCP/WebSocket connections use libp2p's Noise protocol (XX pattern, X25519 only). Relay circuits often fall back to TCP. A single-layer approach leaves gaps.

**Alternatives considered**:
- **Single layer (QUIC only)**: Leaves TCP/WS connections (relay circuits, fallback transports) unprotected. Not acceptable for infrastructure that routes through relays.
- **Replace Noise entirely**: Would break compatibility with all existing peers. Gradual rollout is essential.
- **Wait for libp2p upstream**: No upstream PQ Noise transport exists or is planned. Waiting means shipping without PQ on relay circuits indefinitely.

**Decision**: Two independent PQ layers:
1. **QUIC TLS**: X25519MLKEM768 negotiated automatically by Go's crypto/tls. Zero code needed.
2. **PQ Noise** (`/pq-noise/1`): Custom libp2p security transport using go-clatter's HybridDualLayerHandshake on TCP/WS.

Both layers use hybrid schemes (classical + PQ). An attacker must break both X25519 AND ML-KEM-768 to compromise a connection.

**Consequences**: Every Shurli connection has PQ protection regardless of transport. Relay circuits (TCP) get the same protection as direct QUIC connections. Binary size impact: ~2.3 MB from go-clatter + ML-KEM.

### Physical Verification

QUIC PQ verified on live network with `shurli status` showing `X25519MLKEM768` curve on all QUIC connections to relays. PQ Noise not yet verified on live relay circuits (relays need binary upgrade).

**Reference**: `pkg/sdk/network.go` (transport registration), `pkg/sdk/pqnoise/transport.go` (PQ Noise transport)

---

### ADR-PQ02: go-clatter as PQ Noise Library

| | |
|---|---|
| **Commit** | 36ceca0 (initial integration) |

**Context**: Need a Noise-framework library that supports hybrid PQ handshakes with ML-KEM-768. Must be pure Go (single-binary constraint), must support the DualLayer pattern (outer classical + inner PQ), must handle identity binding.

**Alternatives considered**:
- **flynn/noise** - Classical Noise only. No KEM support. Would require forking and adding PQ primitives.
- **libp2p/go-libp2p-noise** - Tightly coupled to libp2p's Noise XX. No extension points for PQ. Modifying it would break upstream compatibility.
- **Write from scratch** - Risky without a reference. Noise protocol is subtle (nonce management, ratcheting, payload encryption).

**Decision**: Port the Rust [Clatter](https://github.com/jmlepisto/clatter) library to Go as go-clatter (github.com/shurlinet/go-clatter). Clatter already implemented the DualLayer hybrid pattern with KEM support - the exact capability we needed. The port preserved the architecture and protocol logic, then enhanced it with Go-native improvements: generics for cipher/hash/DH/KEM primitives, structured errors, automatic secret zeroing, and ML-DSA-65 signing (not in the Rust original). Independent, MIT-licensed. Supports 5 handshake modes including HybridDualLayerHandshake (outer X25519 XX + inner ML-KEM-768 XX). Full test suite: 233+ tests, 408 interop vectors cross-validated against the Rust implementation.

**Consequences**: Pure Go library with zero CGo dependencies. Single `go get` adds the entire PQ Noise stack. The 408 interop vectors ensure wire-compatible output with the Rust original.

**Reference**: `go.mod` (go-clatter v0.1.0), `pkg/sdk/pqnoise/transport.go`

---

### ADR-PQ03: Transport Integration Pattern

| | |
|---|---|
| **Commit** | 9ff2ff7 (libp2p integration, gater enforcement, config) |

**Context**: libp2p's security transport interface (`sec.SecureTransport`) requires implementing `SecureInbound`, `SecureOutbound`, and `ID`. The PQ Noise transport must integrate cleanly alongside classical Noise without breaking existing connections.

**Alternatives considered**:
- **Replace classical Noise entirely**: Would break all non-upgraded peers. Unacceptable for gradual rollout.
- **Muxer-level negotiation**: Would require changes to libp2p's upgrader. Invasive and fragile.
- **Application-layer handshake after Noise**: Would add a second round-trip. Performance penalty on every connection.

**Decision**: Register PQ Noise as an additional security transport with higher priority than classical Noise:

```go
libp2p.Security(pqnoise.ID, pqnoise.New),  // priority 1
libp2p.Security(noise.ID, noise.New),       // priority 2 (fallback)
```

The libp2p upgrader negotiates security protocols in registration order. Peers that support PQ Noise will select it. Peers that don't will fall back to classical Noise (in opportunistic mode).

**Consequences**: Zero breaking changes for existing peers. Gradual rollout: as nodes upgrade, connections automatically upgrade to PQ. The `pqc_policy` config controls enforcement level.

### Physical Verification

Live daemon with both transports registered. Classical Noise peers connect successfully (fallback works). `shurli status` shows `/noise` for classical peers.

**Reference**: `pkg/sdk/network.go` (Security option registration), `pkg/sdk/pqnoise/transport.go` (New constructor matching libp2p Fx pattern)

---

### ADR-PQ04: Policy Enforcement Architecture

| | |
|---|---|
| **Commit** | 9ff2ff7 (gater enforcement + config) |

**Context**: Different deployment scenarios need different PQ requirements. A home network with all-upgraded nodes can mandate PQ. A mixed network with legacy peers needs graceful fallback.

**Alternatives considered**:
- **Binary toggle (on/off)**: Too coarse. No path for mixed networks during transition.
- **Transport-only enforcement**: Relies entirely on negotiation order. Edge cases (relay circuits stripping metadata) could bypass PQ.
- **Per-connection config**: Too complex. Policy should be network-wide with per-peer exceptions.

**Decision**: Three-tier policy with gater enforcement:

| Policy | Transport Registration | Gater Enforcement |
|--------|----------------------|-------------------|
| `mandatory` | PQ Noise + classical Noise | Reject non-PQ TCP/WS in InterceptUpgraded |
| `opportunistic` (default) | PQ Noise + classical Noise | Allow all, log relay downgrades |
| `disabled` | Classical Noise only | No PQ checks |

Per-peer override via `authorized_keys` attribute (`pqc=mandatory`). Allows requiring PQ from specific high-value peers while allowing classical from others.

Belt-and-suspenders: transport priority handles the common case (negotiation prefers PQ). The gater handles edge cases (connection through relay that strips security negotiation metadata).

**Consequences**: Three clear modes. Per-peer granularity. Belt-and-suspenders catches edge cases. Config validated at startup (`PQCPolicyEffective()` defaults unknown values to `opportunistic`).

### Physical Verification

Tested on live network with `opportunistic` policy (default). All connections establish successfully. Status shows `not verified` for PQ Noise (expected - relays not yet upgraded). Mandatory mode tested in unit tests (`TestInterceptUpgraded_PQCPolicy` in `internal/auth/authorized_keys_test.go`).

**Reference**: `internal/auth/gater.go` (InterceptUpgraded, effectivePQCPolicy), `internal/auth/authorized_keys.go` (`pqc` attribute parsing), `internal/config/config.go` (PQCPolicyEffective)

---

### ADR-PQ05: PQC Status Introspection

| | |
|---|---|
| **Commit** | b271bf4 (QUIC PQ inspection), b2cf954 (PQ Noise status, per-connection breakdown) |

**Context**: Operators need to verify their connections are actually PQ-protected. "Trust but verify" - the daemon must report what security each connection actually negotiated.

**Alternatives considered**:
- **Log-only**: Operators would have to grep logs. Not acceptable for real-time visibility.
- **Prometheus metrics only**: Good for dashboards but not for quick CLI checks.
- **Separate `shurli pqc` command**: Unnecessary command proliferation. Status is the right place.

**Decision**: `InspectPQC()` examines every active connection:
- QUIC: Uses `conn.As(*quic.Conn)` to access TLS ConnectionState, checks CurveID for PQ curves.
- TCP/WS: Checks `ConnState().Security` for `/pq-noise/1` protocol ID.

Exposed via `shurli status` (CLI) and daemon API endpoint. Per-layer summary (QUIC PQ, Noise PQ) plus per-connection breakdown.

First-connection logging: the daemon logs the first PQ-verified connection (once per daemon lifetime) to confirm PQ is working without spamming logs.

Relay downgrade warning: in opportunistic mode, logs a warning when relay circuits use classical Noise instead of PQ Noise.

**Consequences**: Real-time visibility into PQ state. No guessing. Downgrade warnings surface relay upgrade needs.

### Physical Verification

`shurli status` on live network shows per-layer and per-connection PQC state. All connections show classical `/noise` because relays are not yet upgraded. QUIC PQ would show `X25519MLKEM768` on direct peer-to-peer connections.

**Reference**: `pkg/sdk/pqc.go` (InspectPQC, PQCStatus, PQCConnInfo, LogIfPQ, LogRelayDowngrade), `cmd/shurli/cmd_status.go` (PQC status display)

---

### ADR-PQ06: ML-DSA-65 Signing (go-clatter v0.2.0)

| | |
|---|---|
| **Commit** | N/A (library-only change, not yet integrated into Shurli binary) |

**Context**: PQ key exchange protects data in transit. But peer identity is still Ed25519. A quantum attacker could forge identity signatures. Completing the PQ story requires PQ identity authentication.

**Alternatives considered**:
- **SPHINCS+ (SLH-DSA, FIPS 205)**: Hash-based, conservative security assumptions. But signatures are 7-41 KB (vs 3.3 KB for ML-DSA-65). Too large for per-handshake attestation.
- **FALCON (FN-DSA)**: Compact signatures (~666 bytes) but requires constant-time floating-point or double-precision integer arithmetic. Implementation complexity and side-channel risk.
- **Wait for Go stdlib**: `crypto/mldsa` is internal in Go 1.26. Public API expected in Go 1.27. Waiting means no PQ signing capability until then.

**Decision**: Added ML-DSA-65 (FIPS 204, NIST Level 3) to go-clatter v0.2.0. Uses `filippo.io/mldsa` (pre-release wrapper around Go's internal `crypto/mldsa`). Automatic secret zeroing on key disposal.

ML-DSA-65 provides ~143-bit post-quantum security. Key sizes: public 1952 bytes, secret 4032 bytes, signature 3309 bytes. Signing/verification adds ~390us per handshake (benchmarked on Apple M3 Max).

**Consequences**: PQ signing ready in the library. Phase 13 will wire it into the handshake. When Go 1.27 releases, swap `filippo.io/mldsa` for stdlib `crypto/mldsa` (drop-in replacement by design).

**Reference**: go-clatter v0.2.0+ (`crypto/sign/mldsa65/`), tracking issue golang/go#77626

---

## Public Notes

- PQ Noise transport is registered and negotiated but has no effect until remote peers also support it. Current relay fleet runs classical Noise only. Relay binary upgrade is the next deployment step.
- QUIC PQ (X25519MLKEM768) is automatic for direct peer-to-peer connections on Go 1.24+. No Shurli code needed.
- ML-DSA-65 signing exists in go-clatter but is not yet wired into Shurli's handshake. Phase 13 (PQ Identity Attestation) delivers the integration.
- The `pqc_policy` config key and per-peer `pqc` authorized_keys attribute are stable API. The three modes (mandatory/opportunistic/disabled) are final.
