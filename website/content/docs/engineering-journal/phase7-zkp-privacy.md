---
title: "Phase 7 - ZKP Privacy Layer"
weight: 18
description: "Zero-knowledge membership proofs using gnark PLONK on BN254. Poseidon2 Merkle tree, role-aware proofs, range proofs, BIP39 key management."
---
<!-- Auto-synced from docs/engineering-journal/phase7-zkp-privacy.md by sync-docs - do not edit directly -->


Zero-knowledge membership proofs using gnark PLONK on BN254. Poseidon2 Merkle tree of authorized peers, role-aware proofs, universal setup via KZG SRS.

---

### ADR-N01: gnark as ZKP Library

**Context**: Phase 7 introduces zero-knowledge proofs to let peers prove "I'm authorized" without revealing which peer they are. The ZKP library must: (1) support PLONK with universal setup, (2) provide Poseidon2 as a circuit gadget, (3) be pure Go (single-binary constraint), (4) be production-tested.

**Alternatives considered**:
- **bellman (Rust)** - Mature Groth16 library. Requires CGo or FFI bridge, breaks single-binary. Not Go-native.
- **gnark-crypto standalone** - Only provides field arithmetic and native hashes. No circuit compiler, no proving system.
- **arkworks (Rust)** - Comprehensive but same CGo problem as bellman. Community-driven without a single production deployment at gnark's scale.

**Decision**: gnark v0.14.0 (ConsenSys). Pure Go, audited, production-proven on Linea L2 (processes millions of transactions). Provides PLONK + Groth16, circuit compiler, KZG setup, and Poseidon2 gadget. gnark-crypto v0.19.0 for native field arithmetic.

**Consequences**: Single `go get` adds the entire ZKP stack. Binary size impact is ~0.5 MB when wired into the binary (measured via nm analysis). Trade-off: gnark's circuit API requires careful parameter matching between native and circuit hash functions (see ADR-N02).

**Reference**: `go.mod` (gnark v0.14.0, gnark-crypto v0.19.0)

---

### ADR-N02: Poseidon2 Hash - Native/Circuit Consistency

**Context**: The membership proof requires identical hashing in two contexts: (1) native Go code that builds the Merkle tree and computes leaf hashes, and (2) the gnark circuit that verifies these hashes inside the proof. If native and circuit hashes diverge, proofs fail silently.

**Problem discovered**: gnark's `std/hash/poseidon2.NewMerkleDamgardHasher(api)` calls `NewPoseidon2(api)` internally, which only has a case for BLS12-377 - not BN254. Using it on BN254 produces wrong results or panics.

**Decision**: Build both native and circuit hashers from explicit parameters:
- **Native**: `poseidon2.NewMerkleDamgardHasher()` for leaf hashing (Merkle-Damgard construction), `poseidon2.NewPermutation(2, 6, 50).Compress(left, right)` for tree node hashing.
- **Circuit**: `circuitperm.NewPoseidon2FromParameters(api, 2, 6, 50)` + `stdhash.NewMerkleDamgardHasher(api, perm, 0)` for leaf hashing, `perm.Compress(left, right)` for tree nodes.
- Parameters locked: width=2, 6 full rounds, 50 partial rounds. These match BN254's security requirements.

The `poseidon2_test.go` file includes 3 native-circuit consistency tests that compile an actual gnark circuit, prove, and verify that native hash outputs match circuit outputs for: single MD hash, Compress pairs, and full 34-element leaf hashes (pubkey[32] + role + score).

**Consequences**: Hash consistency is tested, not assumed. The explicit parameter approach is more verbose but immune to gnark's default-parameter bugs. If gnark fixes their BN254 defaults in a future version, we can simplify but don't need to.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/poseidon2.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/poseidon2_test.go`

---

### ADR-N03: Merkle Tree Design

**Context**: Each authorized peer becomes a leaf in a Poseidon2 Merkle tree. The tree must be: (1) deterministic regardless of insertion order, (2) efficient for ~1M peers, (3) compatible with a fixed-depth circuit.

**Decision**:
- **Leaf hash**: `Poseidon2(pubkey_bytes[0..31], role_encoding, score)` - 34 field elements. Ed25519 pubkey encoded byte-by-byte (one field element per byte, no overflow risk). Role: admin=1, member=2. Score: reputation 0-100, committed in leaf for binding guarantees.
- **Determinism**: Leaves sorted by leaf hash bytes before tree construction. Same peers in any order produce identical root.
- **Padding**: Non-power-of-2 leaf counts padded to next power of 2 with `zeroLeafHash = Poseidon2(0, 0, ..., 0)` (34 zeros).
- **Max depth**: 20 levels, supporting 2^20 = 1,048,576 peers.
- **Root extension**: Trees with depth < 20 get their root "extended" by hashing through unused levels with zero siblings. This lets the circuit always walk exactly 20 levels.

Performance (benchmarked on M-series Apple Silicon):
- 100-peer tree build: ~0.5ms
- 500-peer tree build: ~2.2ms
- Proof generation: ~177ns
- Proof verification: ~24us

**Consequences**: Deterministic roots mean no coordination protocol needed. Every peer with the same authorized_keys file computes the same Merkle root independently. Trade-off: the sorted-leaf approach means adding/removing a peer rebuilds the entire tree. At ~2ms for 500 peers, this is negligible.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/merkle.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/merkle_test.go`

---

### ADR-N04: Membership Circuit - Constraint Budget

**Context**: The circuit must prove Merkle membership with role checking, using PLONK on BN254. Constraint count directly impacts proving time.

**Initial estimate**: ~3,600 constraints based on R1CS counting (where additions are free).

**Actual**: 22,784 SCS constraints. In PLONK's SCS (Sparse Constraint System), additions also cost constraints, unlike R1CS. Each Poseidon2 permutation is ~420 SCS constraints. The circuit has: 33 leaf-hash compressions (33 x ~420 = ~13,860) + 20 Merkle-path compressions (20 x ~420 = ~8,400) + boolean assertions + role check overhead.

**Decision**: Accept 22,784 constraints. Proving time is ~1.8s end-to-end (compile + prove + serialize), verification is ~2-3ms. This is practical for a challenge-response protocol where the prover has seconds to respond. Constraint limit set at 30,000 with a lower bound of 100 to catch compilation errors.

**Performance** (benchmarked):
- Circuit compile: ~70ms (deterministic, not cached to disk)
- Proof generation: ~1.8s (including witness creation)
- Proof verification: ~2-3ms
- Proof size: 520 bytes
- Proving key: ~2 MB
- Verifying key: ~33.5 KB

**Consequences**: The circuit is practical for session authentication (prove once per connection, not per message). For per-message proving, a lighter construction would be needed (future work if required).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/membership.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/membership_test.go`

---

### ADR-N05: KZG SRS and Key Management

**Context**: PLONK requires a Structured Reference String (KZG SRS) for the universal setup. The SRS must be generated once and cached. Proving and verifying keys are derived from the SRS + circuit.

**Decision**:
- **SRS generation**: `unsafekzg.NewSRS()` with filesystem caching at `~/.shurli/zkp/`. For an authorized-pool model, unsafekzg provides equivalent security to a ceremony SRS: the toxic value is generated randomly, used for setup, and security relies on it not being recoverable from the keys.
- **Key persistence**: Proving key (~2 MB) and verifying key (~33.5 KB) serialized to `provingKey.bin` and `verifyingKey.bin` via gnark's `WriteTo`/`ReadFrom`.
- **CCS not serialized**: gnark's CBOR deserialization of SparseR1CS panics on Go 1.26 with cbor v2.9.0 (`reflect.Value.Set using unaddressable value`). Circuit compilation is deterministic from code (~70ms), so the CCS is recompiled on demand. This is the correct approach: the circuit definition lives in code, not in a serialized blob.

**Consequences**: First startup takes ~3s (SRS generation + key derivation). Subsequent startups load cached keys in <100ms. The CCS recompilation adds 70ms but eliminates a fragile serialization dependency.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/srs.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/keys.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/prover.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/verifier.go`

---

### ADR-N06: Binary Size Analysis

**Context**: Satinder requested a detailed breakdown of what contributes to binary size, with justification for each component.

**Method**: `go tool nm -size` on an unstripped build. Symbol sizes aggregated by package origin. Measured with Go 1.26.0 on darwin/arm64 after Phase 7 (gnark wired into `cmd/`).

**Current size**: 54 MB debug, 37 MB stripped.

**Breakdown**:

| Component | Debug Size | Why It Exists |
|-----------|-----------|---------------|
| Go FIPS 140 crypto | 32.5 MB (60%) | Go 1.24+ embeds the full FIPS-validated crypto module. Non-optional. Contains Ed25519, AES, SHA, TLS, X.509 - all required for P2P crypto. Nearly doubled from Go 1.24 to Go 1.26. |
| Go runtime | 14.9 MB (28%) | GC, goroutine scheduler, memory allocator. Non-negotiable for any Go binary |
| gnark (ZKP) | 3.8 MB (7%) | ConsenSys gnark PLONK prover/verifier, gnark-crypto field arithmetic, Poseidon2. The entire ZKP stack for Phase 7 |
| QUIC + Protobuf + DNS + Metrics | 1.9 MB (3%) | QUIC transport, protobuf serialization, DNS resolution, Prometheus |
| libp2p ecosystem | 1.8 MB (3%) | go-libp2p core, Kademlia DHT, yamux multiplexer, routing helpers. The entire P2P networking stack |
| WebRTC (pion) | 1.3 MB (2%) | ICE, DTLS, SCTP, SRTP. Required for browser-compatible NAT traversal |
| **Shurli application code** | **0.4 MB (0.8%)** | **p2pnet, relay, daemon, auth, config, invite, vault, zkp, reputation, macaroon, etc.** |

**Key insight**: ~88% of binary size is Go stdlib (FIPS crypto + runtime). This grew significantly from Go 1.24 (40 MB debug) to Go 1.26 (54 MB debug) due to FIPS 140 module expansion. gnark adds 3.8 MB debug (~2 MB stripped) - a reasonable cost for a full ZKP proving system. Shurli's own code is under 1%.

**Why nothing can be cut**:
- **FIPS crypto**: Mandatory in Go 1.24+. Cannot be disabled. Nearly doubled in Go 1.26 - this is Go's cost, not ours.
- **Runtime**: Structural Go overhead. Grew proportionally with FIPS.
- **gnark**: The ZKP stack. Pure Go, audited, production-proven. 3.8 MB for PLONK + Poseidon2 + BN254 field arithmetic is compact.
- **libp2p**: The networking foundation. Every sub-package serves a specific protocol function.
- **WebRTC/QUIC**: Transport protocols for NAT traversal.
- **Protobuf**: Required by libp2p's wire format.
- **Prometheus**: Observability. Worth every byte.

**Size history**: 27.6 MB stripped (Go 1.24, pre-Phase 7) -> 37 MB stripped (Go 1.26, Phase 7 complete). The +9.4 MB breaks down as: ~7 MB Go stdlib growth (FIPS + runtime), ~2 MB gnark.

See: [binary-size-breakdown.svg](/images/docs/binary-size-breakdown.svg)

**Reference**: `go tool nm -size` analysis, `docs/images/binary-size-breakdown.svg`

---

### Phase 7-A Test Summary

37 tests across 5 test files, all passing with race detector:

| File | Tests | What's Covered |
|------|-------|----------------|
| `poseidon2_test.go` | 13 | Hash determinism, role encoding, native-circuit consistency, benchmarks |
| `merkle_test.go` | 13 | Determinism, single/power-of-2/non-power-of-2 trees, proof round-trips, 500-peer large tree |
| `membership_test.go` | 6 | Circuit solving, role checks, wrong pubkey/root rejection, constraint count |
| `prover_test.go` | 5 | End-to-end prove/verify, role proofs, wrong nonce rejection, key serialization |

**Benchmarks**:
- Poseidon2 leaf hash (34 elements): ~119us
- Poseidon2 pair hash: ~3.4us
- Merkle tree build (100 peers): ~0.5ms
- Merkle tree build (500 peers): ~2.2ms
- Proof generation: ~177ns
- Proof verification: ~24us
- Circuit compile: ~70ms
- PLONK prove (end-to-end): ~1.8s
- PLONK verify: ~2-3ms
- Proof size: 520 bytes

---

### Configuration

```yaml
security:
  zkp:
    enabled: false              # Master toggle (default: off)
    srs_cache_dir: ""           # KZG SRS cache (default: ~/.shurli/zkp/)
    max_tree_depth: 20          # Supports ~1M peers
```

Added to both `SecurityConfig` (home/client nodes) and `RelaySecurityConfig` (relay servers).

---

### Files Created (Phase 7-A)

| File | Lines | Purpose |
|------|-------|---------|
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/errors.go` | 14 | Sentinel errors |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/poseidon2.go` | 72 | Native + circuit Poseidon2 hash wrappers |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/poseidon2_test.go` | 277 | 13 tests + 2 benchmarks |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/merkle.go` | 217 | Tree builder, proof generation, verification |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/merkle_test.go` | 439 | 13 tests + 4 benchmarks |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/membership.go` | 90 | PLONK membership circuit |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/membership_test.go` | 261 | 6 tests + 1 benchmark |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/srs.go` | 60 | KZG SRS generation with caching |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/keys.go` | 91 | Proving/verifying key serialization |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/prover.go` | 169 | High-level prover |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/verifier.go` | 83 | High-level verifier |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/prover_test.go` | 155 | 5 end-to-end tests |
| `docs/images/binary-size-breakdown.svg` | 1 | Pie chart of binary composition |

**Total**: 13 new files, ~1,928 lines of code + tests. 1 modified file (`https://github.com/shurlinet/shurli/blob/main/internal/config/config.go`).

---

## Sub-Phase 7-B: Anonymous Relay Authorization

Wire protocol `/shurli/zkp-auth/1.0.0`, relay handler, client proof generation, challenge nonces, admin endpoints, Prometheus metrics.

---

### ADR-N07: Challenge-Response Wire Protocol

**Context**: The relay needs to verify a peer's membership without learning which peer they are. This requires a challenge-response protocol where the relay issues a nonce and the peer proves membership against that nonce.

**Decision**: Binary wire protocol on libp2p streams, same pattern as `/shurli/relay-unseal/1.0.0`. Three-phase handshake:

```
Phase 1 - Request:  [1 version] [1 auth_type] [1 role_required]
Phase 2 - Challenge: [1 status] [8 nonce BE] [32 merkle_root] [1 tree_depth]
Phase 3 - Proof:    [2 BE proof_len] [N proof_bytes]
Phase 4 - Result:   [1 status] [1 msg_len] [N message]
```

Auth types: `0x01` = membership (any authorized peer), `0x02` = role (specific role required). Role values: `0x00` = any, `0x01` = admin, `0x02` = member.

The relay sends the current Merkle root and tree depth with the challenge so the client can detect tree staleness before generating the proof. The nonce is a cryptographically random uint64, single-use, with a 30-second TTL.

**Consequences**: Total protocol overhead is ~50 bytes (excluding the ~520-byte proof). The 30-second TTL gives the client enough time for proof generation (~1.8s) with margin for network latency. Binary format keeps the protocol compact and consistent with existing Shurli wire protocols.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/zkp_auth.go`

---

### ADR-N08: Challenge Nonce Management

**Context**: Challenge nonces must be (1) cryptographically random to prevent prediction, (2) single-use to prevent replay, (3) time-bounded to prevent accumulation attacks.

**Decision**: `ChallengeStore` in `https://github.com/shurlinet/shurli/blob/main/internal/zkp/challenge.go`. Nonces are `uint64` from `crypto/rand`. The store tracks pending nonces in a mutex-protected map. `Consume()` atomically removes the nonce regardless of outcome (expired or valid). `CleanExpired()` runs periodically to remove stale entries.

Key properties:
- Single-use: `Consume()` deletes before returning, even on expiry
- 30-second default TTL (`DefaultChallengeTTL`)
- Merkle root snapshot bound at issuance (detects tree changes)
- `Pending()` gauge exposed via Prometheus for monitoring

**Consequences**: Memory cost is ~80 bytes per pending nonce. At 1000 concurrent auth attempts, that's ~80 KB. The cleanup goroutine keeps this bounded. The store is independent of the relay handler, testable in isolation.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/challenge.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/challenge_test.go`

---

### ADR-N09: Admin Endpoints for Tree Management

**Context**: The ZKP Merkle tree must be rebuilt when authorized_keys changes (peer added/removed). This should be triggerable via the existing admin socket API.

**Decision**: Two new endpoints on the existing admin Unix socket:

- `POST /v1/zkp/tree-rebuild` - Rebuilds tree from authorized_keys. Requires unsealed vault (same `requireUnsealedOr` middleware as invite creation). Returns leaf count, depth, root hash.
- `GET /v1/zkp/tree-info` - Returns current tree state (ready, root, leaves, depth). Always available, even when sealed.

Tree is also auto-built on relay startup when `security.zkp.enabled: true`.

**Consequences**: Tree rebuild is O(n log n) for n peers. At 500 peers, this takes ~2.2ms. The `requireUnsealedOr` guard ensures tree mutation only happens when the vault is unsealed, consistent with all other state-changing operations.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin.go` (handlers), `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_client.go` (client methods)

---

### ADR-N10: ZKP Prometheus Metrics

**Context**: ZKP operations are computationally expensive (prove ~1.8s, verify ~2-3ms). Observability is critical for detecting performance regressions, failed auth attempts, and tree staleness.

**Decision**: 9 new Prometheus metrics added to `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`:

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `shurli_zkp_prove_total` | CounterVec | result | Proof generations (success/error) |
| `shurli_zkp_prove_duration_seconds` | HistogramVec | result | Proof gen latency |
| `shurli_zkp_verify_total` | CounterVec | result | Proof verifications (success/invalid) |
| `shurli_zkp_verify_duration_seconds` | HistogramVec | result | Verify latency |
| `shurli_zkp_auth_total` | CounterVec | result | Auth attempts (success/denied/error) |
| `shurli_zkp_tree_rebuild_total` | CounterVec | result | Tree rebuilds |
| `shurli_zkp_tree_rebuild_duration_seconds` | HistogramVec | result | Rebuild latency |
| `shurli_zkp_tree_leaves` | Gauge | - | Current leaf count |
| `shurli_zkp_challenges_pending` | Gauge | - | Active challenge nonces |

All metrics are nil-safe: handlers work with or without metrics enabled.

**Consequences**: Total metric footprint is 9 collectors. Histogram buckets are tuned to expected latencies: prove buckets 100ms-12s, verify buckets 1ms-128ms, rebuild buckets 1ms-1s.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`

---

### Phase 7-B Test Summary

15 tests across 2 test files, all passing with race detector:

| File | Tests | What's Covered |
|------|-------|----------------|
| `challenge_test.go` | 7 | Issue/consume, replay rejection, unknown nonce, expiry, cleanup, multi-nonce, uniqueness |
| `zkp_auth_test.go` | 8 | Wire encoding (request, challenge, proof, response), error paths, empty messages |

```
go test -race -count=1 ./... -> 20 ok, 0 fail
internal/zkp: 44 tests in ~17.5s (37 from 7-A + 7 from 7-B)
internal/relay: +8 wire protocol tests
```

---

### Phase 7-B Files

| File | Lines | Purpose |
|------|-------|---------|
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/challenge.go` | 105 | Challenge nonce store (issue, consume, clean, pending) |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/challenge_test.go` | 130 | 7 tests: replay, expiry, uniqueness, cleanup |
| `https://github.com/shurlinet/shurli/blob/main/internal/relay/zkp_auth.go` | 270 | Relay ZKP handler + wire encoding/decoding helpers |
| `https://github.com/shurlinet/shurli/blob/main/internal/relay/zkp_auth_test.go` | 140 | 8 wire protocol tests |
| `https://github.com/shurlinet/shurli/blob/main/internal/relay/zkp_client.go` | 100 | Client-side ZKP auth (stream-based proof generation) |

**Modified files**:
- `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go` - 9 new ZKP metrics
- `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin.go` - 2 new endpoints, `SetZKPAuth`, `ZKPTreeInfoResponse`
- `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_client.go` - `ZKPTreeRebuild`, `ZKPTreeInfo` methods
- `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_serve.go` - ZKP handler init, stream registration, metrics wiring

**Total**: 5 new files (~745 lines), 4 modified files (~130 lines added).

---

## Phase 7-C: Private Reputation

### ADR-N11: Deterministic Reputation Scoring

**Context**: To prove "my reputation is above threshold X" without revealing the exact score, we first need a deterministic scoring function that maps `PeerRecord` interaction history to an integer score in [0, 100].

**Design**: Four equally-weighted components (0-25 each):
- **Availability** (0-25): `ConnectionCount / maxConnections`, linear scaling
- **Latency** (0-25): logarithmic decay from 10ms (25) to 5000ms (0). Formula: `25 * (1 - log10(latency/10) / log10(500))`
- **PathDiversity** (0-25): 0 types = 0, 1 = 8, 2 = 16, 3+ = 25
- **Tenure** (0-25): days since `FirstSeen / 365`, capped at 1.0

**Decision**: Equal weighting is the simplest defensible choice. All four components are locally observable (no gossip needed). The `maxConnections` parameter is passed explicitly, not hardcoded, so different network sizes can normalize appropriately. Logarithmic latency curve rewards the biggest gains (10ms to 100ms) more than marginal improvements at the tail.

**Consequences**: Score is fully deterministic: same `PeerRecord` + same `maxConnections` + same `now` always produces the same integer. This is essential for future score commitment in the Merkle tree.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/reputation/score.go`

---

### ADR-N12: Range Proof Circuit Design

**Context**: The membership circuit proves "I'm in the tree." The range proof circuit additionally proves "my reputation score >= threshold" without revealing the exact score.

**Design**: `RangeProofCircuit` extends `MembershipCircuit` with two additional public/private fields:
- **Public**: `Threshold` (minimum score required)
- **Private**: `Score` (the peer's actual score, 0-100)

Constraints added beyond membership:
1. `AssertIsLessOrEqual(Threshold, Score)` - score meets threshold
2. `AssertIsLessOrEqual(Score, 100)` - score is valid

**Constraint count**: 27,004 SCS constraints (vs 22,784 for membership-only). The +4,220 overhead comes from gnark's `AssertIsLessOrEqual` which decomposes operands into 254 bits for field-safe comparison.

**Performance**: 520-byte proofs (identical to membership). Prove ~1.8s, verify ~3ms. Separate PLONK keys required (different circuit = different CCS/SRS).

**Trust model note**: Score is committed in the Merkle tree leaf hash: `Poseidon2(pubkey[32], role, score)`. The range proof circuit verifies the same score value used in the leaf hash, preventing inflation. Updated from the initial self-reported design during Phase 7 audit.

**Decision**: Implement as a separate circuit rather than a mode flag on MembershipCircuit. Separate circuits keep constraint counts independent, allow independent key management, and avoid branching complexity inside the circuit.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/range_proof.go` (27,004 constraints, 520-byte proofs)

---

### ADR-N13: Anonymous NetIntel Announcements

**Context**: `NodeAnnouncement` currently requires `From` (peer ID) for cache deduplication and gossip forwarding. Phase 7-C adds the ability to send presence announcements anonymously, authenticated by a ZKP membership proof instead of peer identity.

**Design**: Two new fields on `NodeAnnouncement`:
- `AnonymousMode bool` (`json:"anon,omitempty"`) - when true, `From` is empty
- `ZKPProof []byte` (`json:"zkp_proof,omitempty"`) - serialized PLONK proof

When `AnonymousMode` is true, recipients verify the ZKP proof against the current Merkle root to confirm the sender is an authorized member. Cache keying switches from peer ID to a hash of the proof or announcement content.

**Decision**: Add fields now, wire verification logic in a future phase when anonymous announcements are activated. The fields are `omitempty`, so existing v1 announcements are unaffected.

**Consequences**: Zero impact on current functionality. Anonymous mode is opt-in per announcement. The gossip forwarding layer (Layer 2) works unchanged because forwarded messages already carry `From` for the originator.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/netintel.go` (NodeAnnouncement struct)

---

### ADR-N14: RLN Extension Point

**Context**: Rate-Limiting Nullifiers (RLN) enable anonymous rate limiting. Each member can perform one action per epoch; two actions reveal their secret for automatic slashing. This is the natural next step after membership and range proofs.

**Design**: Three types defined as an extension point:
- `RLNIdentity` - secret + Poseidon2 commitment
- `RLNProof` - epoch, nullifier, Shamir share, ZK proof
- `RLNVerifier` interface - `VerifyEpoch()` + `DetectSpam()`

**Decision**: Types only, no implementation. The Poseidon2 hash and PLONK system from 7-A/7-B are directly compatible. The RLN circuit will compose with the existing membership circuit (prove membership + valid share + epoch binding in a single proof).

**Consequences**: Zero binary impact (no circuit compilation). The interface defines the contract for future implementation. Spam detection via Shamir secret sharing is well-studied (used in Waku RLN, Semaphore).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/rln_seam.go`

---

### ADR-N15: Phase 7-C Prometheus Metrics

**Context**: Five new metrics for range proof operations and anonymous announcements. Follows the same nil-safe pattern as Phase 7-B metrics.

| Metric | Type | Labels | Purpose |
|--------|------|--------|---------|
| `shurli_zkp_range_prove_total` | CounterVec | result | Range proof generations |
| `shurli_zkp_range_prove_duration_seconds` | HistogramVec | result | Range prove latency |
| `shurli_zkp_range_verify_total` | CounterVec | result | Range proof verifications |
| `shurli_zkp_range_verify_duration_seconds` | HistogramVec | result | Range verify latency |
| `shurli_zkp_anon_announcements_total` | CounterVec | result | Anonymous presence announcements |

**Consequences**: Total Phase 7 metric count: 14 collectors (9 from 7-B + 5 from 7-C).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`

---

### Phase 7-C Test Summary

25 tests across 3 test files, all passing with race detector:

| File | Tests | What's Covered |
|------|-------|----------------|
| `score_test.go` | 14 | Nil/zero edge cases, max score, determinism, each component in isolation, mid-range composite |
| `range_proof_test.go` | 11 | Circuit satisfaction (pass/fail), threshold boundary, score > 100, role+range combo, wrong pubkey, constraint count, 2 end-to-end PLONK tests |

```
go test -race -count=1 ./... -> 20 ok, 0 fail
internal/reputation: 20 tests (6 history + 14 score)
internal/zkp: 48 tests in ~21.8s (37 from 7-A + 11 from 7-C)
```

---

### Phase 7-C Files

| File | Lines | Purpose |
|------|-------|---------|
| `https://github.com/shurlinet/shurli/blob/main/internal/reputation/score.go` | 112 | Deterministic ComputeScore (0-100, 4 components) |
| `https://github.com/shurlinet/shurli/blob/main/internal/reputation/score_test.go` | 182 | 14 tests: edge cases, component isolation, composite |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/range_proof.go` | 208 | RangeProofCircuit + RangeProver + RangeVerifier |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/range_proof_test.go` | 290 | 11 tests: circuit + 2 end-to-end PLONK |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/rln_seam.go` | 52 | RLN types + interface (extension point) |

**Modified files**:
- `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/netintel.go` - `AnonymousMode` + `ZKPProof` fields on `NodeAnnouncement`
- `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go` - 5 new range proof / anonymous metrics

**Total**: 5 new files (~844 lines), 2 modified files (~60 lines added).

---

## Phase 7-D: BIP39 Seed-Derived Deterministic Keys

### ADR-N16: BIP39 Mnemonic for Deterministic SRS

**Context**: During physical testing (Phase 7 relay deployment), a critical architectural finding emerged: every call to `unsafekzg.NewSRS()` generates a random toxic value, producing a unique SRS. A proving key from SRS-A cannot produce proofs verifiable by a verifying key from SRS-B. Relay and clients had incompatible keys. The workaround was manually copying key files via SCP - unacceptable for production.

**Options considered**:
1. **Relay serves keys via API** - client downloads proving/verifying key from relay. Simple but requires the relay to be reachable before any ZKP auth can happen.
2. **Shared ceremony SRS** - all nodes download the same ceremony SRS (e.g., Ethereum's). Correct but requires external dependency and download infrastructure.
3. **Deterministic SRS from shared secret** - derive SRS from a seed that both relay and client know. Same seed = same SRS = same keys.

**Decision**: Option 3, using BIP39 seed phrases. Satinder proposed this during physical testing, drawing from his cryptocurrency background (BIP39 is the standard for Bitcoin/Ethereum wallet recovery phrases).

Flow: `SHA256(mnemonic)` -> gnark's `WithToxicSeed(seed)` -> deterministic SRS -> same proving/verifying keys on any machine. The relay operator generates a seed phrase once with `shurli relay zkp-setup --seed "..."`, writes it down, and enters it on client nodes.

**Key properties**:
- One seed = one node's key set. No multi-derivation paths.
- Seeds NEVER stored on disk in production. Operator memorizes or backs up offline.
- 256-bit entropy -> 24-word mnemonic (standard BIP39 encoding with SHA256 checksum).
- Pure stdlib implementation: `crypto/rand`, `crypto/sha256`. No BIP39 library dependency.

**Security model**: In an authorized-pool model, the relay operator already controls the authorized_keys list and the vault passphrase. The seed phrase is equivalent authority. Keeping the seed = keeping the admin key. This is not a multi-party ceremony model; it's a single-operator key management pattern, same as running a Bitcoin full node with a wallet.

**Consequences**: Eliminates manual key copying entirely. The relay's `GET /v1/zkp/proving-key` and `GET /v1/zkp/verifying-key` endpoints provide an alternative path for key distribution when seed sharing is not desired. Two distribution models coexist: seed-based (deterministic) and API-based (download).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/bip39.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/srs.go` (`SetupKeysFromSeed`), `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_zkp.go`

---

### ADR-N17: ProvingKey/VerifyingKey Naming Convention

**Context**: Satinder flagged confusion during code review: "PK" looks like "Private Key" to anyone from a crypto/blockchain background. In PLONK, PK = Proving Key (PUBLIC, ~2 MB) and VK = Verifying Key (PUBLIC, ~34 KB). Both are safe to share freely. The abbreviation collision could lead to dangerous misunderstandings - someone might think the proving key is secret and refuse to distribute it, breaking the ZKP auth flow.

**Decision**: Rename throughout the entire codebase:
- File names: `pk.bin` -> `provingKey.bin`, `vk.bin` -> `verifyingKey.bin`
- Struct fields: `pk` -> `provingKey`, `vk` -> `verifyingKey`
- Local variables in PLONK setup calls
- Test variables: `pk` (meaning Ed25519 public key) renamed to `pubkey` to avoid namespace collision
- Comments and documentation updated

**Scope**: 13 files, ~60 individual renames across struct fields, function parameters, local variables, file constants, test helpers, and documentation.

**Consequences**: Zero functional change. Code is now unambiguous: `provingKey` and `verifyingKey` cannot be confused with identity keys. The rename also makes the code self-documenting for anyone reading it without ZKP background.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/zkp/keys.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/prover.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/verifier.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/range_proof.go`, `https://github.com/shurlinet/shurli/blob/main/internal/zkp/srs.go`

---

### ADR-N18: Key Distribution via Relay API

**Context**: Two models exist for getting circuit parameters (proving key + verifying key) to clients: (1) seed-based deterministic derivation, and (2) downloading from the relay. Both should be supported because they serve different use cases.

Seed-based: the operator enters the same seed on relay and client. Both derive identical keys independently. Best for: relay operators who manage all nodes.

API-based: the client downloads the proving key and verifying key from the relay's admin API. Best for: invited peers who should not know the relay's seed phrase.

**Decision**: Add two admin API endpoints on the existing Unix socket:
- `GET /v1/zkp/proving-key` - serves `provingKey.bin` (~2 MB) as `application/octet-stream`
- `GET /v1/zkp/verifying-key` - serves `verifyingKey.bin` (~34 KB) as `application/octet-stream`

Both endpoints use `http.ServeFile` for efficient binary streaming. Neither requires the vault to be unsealed - proving keys and verifying keys are public circuit parameters, not secrets.

**Not vault-gated**: Unlike `POST /v1/zkp/tree-rebuild` (which modifies state and requires unsealed vault), key download is read-only. The keys are mathematical artifacts derived from the SRS; possessing them grants no special access. A client needs the proving key to generate proofs and any verifier needs the verifying key to check them.

**Client methods**: `AdminClient.ZKPProvingKey()` and `AdminClient.ZKPVerifyingKey()` added for programmatic access.

**Consequences**: Two independent key distribution paths. Operators choose based on their trust model. Both produce identical results when starting from the same SRS.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin.go` (handlers), `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_client.go` (client methods), `https://github.com/shurlinet/shurli/blob/main/internal/zkp/keys.go` (`ProvingKeyPath`, `VerifyingKeyPath`)

---

### Phase 7-D Test Summary

14 tests across 2 test files, all passing with race detector:

| File | Tests | What's Covered |
|------|-------|----------------|
| `bip39_test.go` | 11 | Generation (24 words), validation (happy path, wrong checksum, wrong word count, unknown word, empty), checksum consistency, known test vectors, word count, seed determinism |
| `seed_test.go` | 3 | Deterministic key generation (same seed = same keys), different seeds produce different keys, full prove/verify round-trip with seed-derived keys |

```
go test -race -count=1 ./... -> 20 ok, 0 fail
internal/zkp: 62 tests in ~35s (48 from 7-A/7-B/7-C + 14 from 7-D)
```

---

### Phase 7-D Files

| File | Lines | Purpose |
|------|-------|---------|
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/bip39.go` | 165 | Pure-stdlib BIP39: generate, validate, seed derivation, file I/O |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/bip39_wordlist.go` | 2054 | Standard BIP39 English wordlist (2048 words) |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/bip39_test.go` | ~150 | 11 tests: generation, validation, checksum, known vectors |
| `https://github.com/shurlinet/shurli/blob/main/internal/zkp/seed_test.go` | ~160 | 3 tests: determinism, different seeds, prove/verify round-trip |

**Modified files**:
- `https://github.com/shurlinet/shurli/blob/main/internal/zkp/srs.go` - Added `SetupKeysFromSeed(keysDir, mnemonic)` using gnark's `WithToxicSeed`
- `https://github.com/shurlinet/shurli/blob/main/internal/zkp/keys.go` - Added `ProvingKeyPath()` and `VerifyingKeyPath()` accessor functions. Renamed file constants and all function parameters from `pk`/`vk` to `provingKey`/`verifyingKey`.
- `https://github.com/shurlinet/shurli/blob/main/internal/zkp/prover.go` - Struct field `pk` -> `provingKey`, all references updated
- `https://github.com/shurlinet/shurli/blob/main/internal/zkp/verifier.go` - Struct field `vk` -> `verifyingKey`, all references updated
- `https://github.com/shurlinet/shurli/blob/main/internal/zkp/range_proof.go` - `RangeProver.pk` -> `provingKey`, `RangeVerifier.vk` -> `verifyingKey`
- `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin.go` - Added `GET /v1/zkp/proving-key` and `GET /v1/zkp/verifying-key` endpoints
- `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_client.go` - Added `ZKPProvingKey()` and `ZKPVerifyingKey()` client methods
- `https://github.com/shurlinet/shurli/blob/main/internal/relay/zkp_auth.go` - Added `keysDir` field and `KeysDir()` accessor
- `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_zkp.go` - Added `runRelayZKPSetup` (zkp-setup subcommand), `--seed` flag on zkp-test
- `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay.go` - Added `zkp-setup` case to relay command router
- `docs/engineering-journal/phase7-zkp-privacy.md` - Updated `pk.bin`/`vk.bin` references to `provingKey.bin`/`verifyingKey.bin`

**Total**: 4 new files (~2,529 lines including wordlist), 11 modified files.

---

## Phase 7 Complete Summary

### All Sub-Phases

| Sub-Phase | Focus | New Files | Modified Files | Tests |
|-----------|-------|-----------|---------------|-------|
| 7-A | ZKP Foundation (Poseidon2, Merkle, PLONK circuit) | 13 | 1 | 37 |
| 7-B | Anonymous Relay Authorization (wire protocol, admin endpoints) | 5 | 4 | 15 |
| 7-C | Private Reputation (scoring, range proofs, RLN seam) | 5 | 2 | 25 |
| 7-D | BIP39 Seeds, Key Naming, Key Distribution | 4 | 11 | 14 |
| **Total** | | **27** | **18** | **91** |

### Key Numbers

- 22,784 SCS constraints (membership circuit)
- 27,004 SCS constraints (range proof circuit)
- 520-byte proofs (both circuits)
- ~1.8s proof generation + ~2-3ms verification (client -> relay over internet)
- ~1.8s prove, ~2-3ms verify, ~70ms circuit compile
- 14 new Prometheus metrics
- 6 new P2P/admin protocols and endpoints
- 2 key distribution models (seed-based + API-based)

### Full Test Suite
```
go test -race -count=1 ./... -> 20 ok, 0 fail
internal/zkp: 62 tests in ~35s
internal/reputation: 20 tests (6 history + 14 score)
internal/relay: includes 8 ZKP wire protocol tests
Total Phase 7 tests: ~91
```

---

**Post-publication corrections** (Phase 8, 2026-03-02):

- Constraint counts updated: membership 22,364 to 22,784 SCS, range proof 26,584 to 27,004 SCS (+420 each due to binding score commitment in Phase 8).
- Leaf elements updated: 33 to 34 (score field added to Merkle leaf hash).
- Proving time corrected: ~350ms to ~1.8s (end-to-end including circuit compilation).
- Trust model updated: scores are now committed in the Merkle tree leaf hash, not self-reported.
