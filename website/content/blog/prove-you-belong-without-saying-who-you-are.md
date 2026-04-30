---
title: "Phase 7: Prove You Belong Without Saying Who You Are"
date: 2026-03-01T12:00:00+13:00
tags: [release, phase-7]
description: "Zero-knowledge membership proofs, private reputation scores, BIP39 key management. 27 new files, 91 tests, 14 Prometheus metrics. L2 blockchain cryptography applied to P2P networking."
image: /images/blog/phase7-zkp-privacy.png
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Phase 7: ZKP Privacy Layer](/images/blog/phase7-zkp-privacy.svg)

## The problem we solved

Phase 6 built a fortress: roles, sealed vault, macaroon tokens, two-factor auth. Strong security. But every time a peer connects, the relay sees exactly which peer it is.

Think of it like a private members' club with perfect security cameras. The door check is robust: valid ID, correct PIN, face matches photo. But the cameras record every visit. Who came, when, how often. You trust the club owner today, but that log exists forever. An attacker who compromises the relay gets a complete social graph of your network.

The question Phase 7 answers: **can you prove you're a member of the club without the door ever seeing your face?**

Yes. And the math has been battle-tested by the same L2 blockchain infrastructure that processes millions of Ethereum transactions.

## What this means for you

If you're running a Shurli network:

- **Your relay no longer knows who connected.** It knows an authorized peer connected. Not which one. Your peers' connection patterns are invisible, even to you.
- **Admin operations stay private too.** Need to prove you're an admin? The relay confirms you are one without learning which admin you are.
- **Reputation can be proven without exposing it.** A peer can prove "my reputation is above 70" without revealing whether it's 71 or 99.
- **Key management uses the same seed phrases as cryptocurrency wallets.** If you've ever backed up a Bitcoin or Ethereum wallet, you already know the drill: 24 words, write them down, never type them into a website.

If you're evaluating Shurli:

This is L2 blockchain proving technology (the same PLONK system that powers Linea, ConsenSys's Ethereum rollup) applied to P2P network authentication. No other networking tool does this. The gap between "authorized peers only" and "authorized peers with mathematical anonymity" is the gap between a VPN and Tor, but without Tor's performance cost. Proof verification takes 2-3 milliseconds.

## What shipped

Four sub-phases, 27 new files, ~91 tests. One new external dependency (gnark, ConsenSys's audited zero-knowledge library).

| Component | What it does | Size | Tests |
|-----------|-------------|------|-------|
| Poseidon2 Merkle tree | Cryptographic tree of all authorized peers | 289 lines | 26 |
| PLONK membership circuit | Proves "I'm in the tree" without revealing which leaf | 90 lines | 6 |
| Range proof circuit | Proves "score >= threshold" without revealing score | 208 lines | 11 |
| Challenge-response protocol | Binary wire format for anonymous auth handshake | 375 lines | 8 |
| Challenge nonce store | Single-use, time-bounded replay protection | 105 lines | 7 |
| Reputation scoring | Deterministic 0-100 score from peer history | 112 lines | 14 |
| BIP39 seed management | 24-word phrases for deterministic key derivation | 165 lines | 11 |
| Prover + Verifier | High-level proof generation and verification | 252 lines | 5 |
| RLN extension point | Types for future anonymous rate-limiting | 52 lines | 0 |
| Key serialization + SRS | Proving/verifying key management and caching | 151 lines | 3 |

## How zero-knowledge proofs work (no math degree required)

Imagine you have a book of solved crossword puzzles. I want to prove I solved today's puzzle without showing you my solution. Here's how:

1. I put my completed puzzle in a locked box with a tiny window.
2. You pick a random word from the puzzle clues: "7 across."
3. I rotate the box so "7 across" shows through the window.
4. You see the correct answer. But you can't see any other answers.

Repeat this enough times with different random words, and you become mathematically certain I solved the puzzle without ever seeing my full solution.

Shurli's version: the "puzzle" is "my public key is in the authorized list." The relay picks a random challenge (the nonce). The client proves their key is in the Merkle tree without revealing which position it occupies. The proof is 520 bytes and takes 2-3 milliseconds to verify.

## The Merkle tree: a fingerprint of your entire network

Every authorized peer becomes a leaf in a Poseidon2 Merkle tree. The tree produces a single 32-byte root hash: a fingerprint of your entire authorized set.

![Poseidon2 Merkle Tree](/images/docs/arch-zkp-merkle-tree.svg)

### Why Poseidon2?

Inside a zero-knowledge circuit, every computation costs "constraints" (think of them as CPU cycles for proofs). SHA-256 costs ~25,000 constraints per hash. Poseidon2 costs ~420. For a Merkle tree with 20 levels of hashing, that's the difference between a proof that takes minutes and one that takes under two seconds.

Poseidon2 was designed specifically for this purpose: efficient hashing inside arithmetic circuits, with security proofs on the BN254 elliptic curve that PLONK uses.

### How leaves are encoded

Each leaf contains 34 field elements: 32 bytes of the peer's Ed25519 public key (one byte per field element, no overflow risk), one role encoding (admin=1, member=2), and one reputation score (0-100). The leaf hash is `Poseidon2(pubkey[0], pubkey[1], ..., pubkey[31], role, score)`. The score is committed directly in the leaf, making range proofs binding: a peer cannot claim a different score than what the relay committed during tree building.

Leaves are sorted by hash before tree construction. This makes the root deterministic: same peers in any order always produce the same root. No coordination protocol needed. Every node with the same `authorized_keys` computes the same tree independently.

### Scale

Max tree depth is 20, supporting 2^20 = 1,048,576 peers. Building a tree of 500 peers takes ~2.2 milliseconds.

## The circuit: what gets proved

The PLONK membership circuit has four public inputs (visible to the relay) and many private inputs (known only to the prover):

**Public** (relay sees these):
- Merkle root: the tree fingerprint
- Nonce: relay's random challenge (replay protection)
- Role required: 0=any, 1=admin, 2=member

**Private** (relay never sees these):
- The peer's public key bytes
- Their role encoding
- Their reputation score (committed in leaf hash [^3])
- The Merkle path (sibling hashes at each level)
- Direction bits (left/right at each level)

The circuit enforces five constraints:
1. Recompute the leaf hash from the private key, role, and score
2. Walk the Merkle path from leaf to root
3. The computed root matches the public Merkle root
4. If a role is required, the peer's role matches
5. The nonce is bound into the proof (replay protection)

If all five pass, the relay knows an authorized peer generated this proof. It never learns which one.

**Constraint count**: 22,784 SCS constraints [^1]. Proving time: ~1.8 seconds [^2] (client-side PLONK proving). Verification: 2-3ms. Proof size: 520 bytes.

## The protocol: four steps on the wire

![ZKP Authentication Protocol](/images/docs/arch-zkp-auth-protocol.svg)

The protocol runs over a libp2p stream (`/shurli/zkp-auth/1.0.0`), the same transport layer as all Shurli protocols. All communication is already encrypted by libp2p's Noise handshake.

```
Step 1: Client sends auth request     [3 bytes]
Step 2: Relay sends challenge nonce    [42 bytes]
Step 3: Client sends PLONK proof       [~522 bytes]
Step 4: Relay sends result             [~12 bytes]
```

Total protocol overhead: ~579 bytes. The expensive part is the proof generation (~1.8s [^2] on the client), not the network transfer.

### Replay protection

Each nonce is:
- Generated from `crypto/rand` (cryptographically random)
- Single-use: consumed and deleted on first verification attempt, pass or fail
- Time-bounded: 30-second TTL, then automatically expired
- Root-bound: snapshot of the Merkle root at issuance, detects tree changes

An attacker who captures a valid proof cannot replay it: the nonce has already been consumed. An attacker who tries to generate their own proof cannot: they don't have a private key that's a leaf in the tree.

## Private reputation: prove quality without exposing the number

Beyond membership, Phase 7 introduces range proofs. A peer can prove "my reputation score is at least 70" without revealing whether it's 71 or 99.

![Range Proof Circuit](/images/docs/arch-zkp-range-proof.svg)

### The scoring formula

Four components, equally weighted (0-25 each), totaling 0-100:

| Component | Input | Scoring |
|-----------|-------|---------|
| Availability | Connection count | Linear: connections / max, scaled to 0-25 |
| Latency | Average RTT | Logarithmic: 10ms=25, 100ms=17, 1000ms=8, 5000ms=0 |
| Path diversity | Unique path types | 0 types=0, 1=8, 2=16, 3+=25 |
| Tenure | Days since first seen | Linear: days/365, capped at 25 |

The score is fully deterministic: same peer record, same time, same result. Scores are committed directly into the Merkle tree leaves: the leaf hash is `Poseidon2(pubkey[0..31], role, score)`. This makes range proofs binding: a peer cannot claim a higher score than what the relay committed in the tree. If the prover lies about their score, the leaf hash won't match any leaf, and the Merkle proof fails.

### Trust model

The relay computes reputation scores from its own observations (connection history, latency, path diversity, tenure) and commits them into the tree during rebuilds. A peer cannot inflate their own score because the committed value in the leaf hash must match. The relay operator controls score computation, which is correct for the authorized-pool model: the relay operator is trusted to build the tree honestly. For future open networks, decentralized score attestation may add additional trust anchors.

## BIP39: cryptocurrency key management for network auth

During physical testing, a critical finding emerged: every node that generates PLONK keys gets a different random Structured Reference String (SRS). Keys from one SRS are incompatible with keys from another. Manually copying key files between nodes works but doesn't scale.

The solution draws from cryptocurrency wallet design. Bitcoin, Ethereum, and every major blockchain use BIP39 seed phrases: 24 words that deterministically derive all keys. Same phrase on any device = same keys.

Shurli applies the same principle: `SHA256(mnemonic)` -> gnark's deterministic SRS -> same proving and verifying keys on any machine that knows the phrase.

```bash
# Generate identity + seed phrase (one-time, during node init)
shurli init
# Output includes:
#   Generated BIP39 seed phrase (24 words):
#   abandon ability able about above absent ... (24 words)
#   WRITE THIS DOWN. It is NOT stored anywhere.

# Use the same seed for ZKP key setup on the relay
shurli relay zkp-setup --seed "abandon ability able about above absent ..."
# -> Derives deterministic PLONK keys from seed phrase

# Enter the same seed on client nodes
shurli relay zkp-setup --seed "abandon ability able about above absent ..."
# -> Same keys. No file copying needed.
```

The implementation is pure stdlib Go: `crypto/rand` for entropy, `crypto/sha256` for checksum. No external BIP39 library. The 2,048-word English wordlist is embedded directly.

### Two distribution models

| Model | Use case | How it works |
|-------|----------|-------------|
| Seed-based | Operator manages all nodes | Same seed phrase entered on each device. Deterministic. |
| API-based | Invited peers | Client downloads keys from relay's `GET /v1/zkp/proving-key` endpoint. No seed needed. |

Both models produce compatible keys when starting from the same SRS. Proving keys and verifying keys are public circuit parameters (mathematical artifacts, not secrets), safe to distribute to any authorized peer.

## What could go wrong (and what we did about it)

| What could happen | How it's handled |
|-------------------|-----------------|
| Attacker replays a captured proof | Nonces are single-use and time-bounded (30s TTL). Consumed on first attempt. |
| Attacker generates a fake proof | Requires a private key whose public key is a Merkle leaf. PLONK soundness prevents forgery. |
| Relay correlates proofs to identify peers | Proofs contain no identity information. Different challenges produce different proofs for the same peer. |
| Merkle tree becomes stale after peer changes | Tree auto-rebuilds on startup. Manual rebuild: `shurli relay admin zkp-tree-rebuild`. |
| Proving key leak compromises security | Proving keys are public. They're mathematical circuit parameters, not secrets. Safe to share. |
| Seed phrase compromised | Equivalent to losing the admin key. Generate new seed, redistribute. Same as cryptocurrency wallet recovery. |
| BIP39 checksum collision | SHA-256 checksum over 256 bits of entropy. Collision probability: 2^-8 per word (caught immediately). |
| Score inflation in range proofs | Scores are committed in tree leaves (binding). A peer cannot claim a different score than what the relay committed during tree building. |

## Observability: 14 new metrics

Every ZKP operation is instrumented. Zero overhead when Prometheus is disabled (nil-safe metric helpers).

| Metric | Type | What it tracks |
|--------|------|----------------|
| `shurli_zkp_prove_total` | Counter | Proof generation attempts by result |
| `shurli_zkp_prove_duration_seconds` | Histogram | Client-side proving latency |
| `shurli_zkp_verify_total` | Counter | Proof verifications by result |
| `shurli_zkp_verify_duration_seconds` | Histogram | Relay-side verification latency |
| `shurli_zkp_auth_total` | Counter | Auth protocol attempts by outcome |
| `shurli_zkp_tree_rebuild_total` | Counter | Tree rebuilds by result |
| `shurli_zkp_tree_rebuild_duration_seconds` | Histogram | Tree rebuild latency |
| `shurli_zkp_tree_leaves` | Gauge | Current authorized peer count in tree |
| `shurli_zkp_challenges_pending` | Gauge | Outstanding challenge nonces |
| `shurli_zkp_range_prove_total` | Counter | Range proof generations |
| `shurli_zkp_range_prove_duration_seconds` | Histogram | Range proof proving latency |
| `shurli_zkp_range_verify_total` | Counter | Range proof verifications |
| `shurli_zkp_range_verify_duration_seconds` | Histogram | Range proof verification latency |
| `shurli_zkp_anon_announcements_total` | Counter | Anonymous presence announcements |

Total custom Prometheus metrics across Shurli: 44. The pre-built [Grafana dashboard](/docs/monitoring/) now includes a dedicated ZKP section with panels for proof latency distribution, auth success/failure rates, tree state, and challenge nonce tracking.

## Where this leads

Phase 7 builds the mathematical foundation. The next steps use it:

```
Phase 7 (now):   Prove membership, prove role, prove reputation.
                 Scores committed in tree leaves (binding proofs).

Future:          RLN rate-limiting (one action per epoch, spam auto-detected).
                 Anonymous NetIntel announcements (presence without identity).
```

The RLN (Rate-Limiting Nullifier) extension point is already in the code: types and interface defined, ready for circuit implementation. RLN uses Shamir secret sharing inside the proof: one message per epoch is free, two messages in the same epoch reveal the sender's secret for automatic slashing. This is the same construction used by Waku for anonymous messaging rate limits.

## Impact

| | Before Phase 7 | After Phase 7 |
|--|----------------|---------------|
| **Relay identity knowledge** | Knows exactly who connected | Knows an authorized peer connected |
| **Auth proof** | Peer ID in authorized_keys (52 bytes) | PLONK zero-knowledge proof (520 bytes) |
| **Auth time** | Instant (allowlist lookup) | ~1.8s proving [^2] + 2-3ms verification |
| **Role privacy** | Relay knows your role | Relay confirms role without knowing which peer |
| **Reputation privacy** | Score visible or not shared | Prove "score >= X" without revealing score |
| **Key management** | Manual key file copying | 24-word seed phrase or API download |
| **Anonymity set** | None (identified by peer ID) | All authorized peers (indistinguishable) |
| **Proof system** | None | PLONK on BN254 (same as Ethereum L2 rollups) |
| **Observability** | 30 metrics | 44 metrics (14 new ZKP-specific) |
| **New code** | | 27 files, ~91 tests |
| **New dependencies** | | 1 (gnark v0.14.0, ConsenSys, audited) |

Four sub-phases. 27 files. 91 tests. One dependency. The relay no longer knows who you are. It only knows you belong.

---

[^1]: **Updated (Phase 8)**: Constraint counts increased from the original 22,364 to 22,784 SCS (membership) and 26,584 to 27,004 SCS (range proof). Cause: binding score commitment added to Merkle tree leaves (34 elements per leaf instead of 33).
[^2]: **Updated (Phase 8)**: Proving time corrected from ~350ms to ~1.8s to reflect accurate end-to-end measurement including circuit compilation overhead.
[^3]: **Updated (Phase 8)**: Originally scores were self-reported. Phase 8's score commitment means the score proven in a range proof must match what's committed in the Merkle tree leaf hash. The proof is now binding.
