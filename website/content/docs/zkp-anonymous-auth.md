---
title: "Anonymous Authentication"
weight: 10
description: "Connect to your relay anonymously using zero-knowledge proofs. Prove you are an authorized peer without revealing which peer you are."
---

After [setting up ZKP keys and the Merkle tree](../zkp-setup/), you can authenticate to your relay anonymously. This guide shows you how.

## How it works

Standard authentication sends your peer ID to the relay. The relay looks it up in `authorized_keys` and either accepts or rejects. Simple, but the relay knows exactly who connected.

Anonymous authentication uses a four-step challenge-response protocol:

1. **Client requests auth** - sends the auth type (membership or role-based) to the relay
2. **Relay issues a challenge** - sends a fresh random nonce and the current Merkle root
3. **Client generates a proof** - proves "I know a private key whose public key is a leaf in this tree" without revealing which leaf
4. **Relay verifies the proof** - confirms the math checks out and grants access

The relay learns one fact: an authorized peer connected. It never learns which one.

{{< callout type="info" >}}
The proof is generated using PLONK, the same proving system used by Ethereum L2 rollups for transaction validity. Shurli applies it to network authentication.
{{< /callout >}}

## Prerequisites

- ZKP keys generated on the relay (see [ZKP Privacy Setup](../zkp-setup/))
- Merkle tree built from `authorized_keys`
- Client has compatible proving key (same seed, or downloaded from relay)
- Client's peer ID exists in the relay's `authorized_keys`

## Testing anonymous auth

The `zkp-test` command runs a full anonymous authentication round-trip against your relay:

```bash
shurli relay zkp-test \
  --auth-keys /path/to/relay_authorized_keys \
  --relay /ip4/203.0.113.50/tcp/9000/p2p/<relay-peer-id> \
  --seed "word1 word2 word3 ... word24"
```

Output on success:

```
Bootstrapping PLONK circuit keys...
  Using deterministic SRS from seed phrase
  Keys ready (2.1s)
Building Merkle tree...
  5 leaves, depth 3, root a1b2c3d4...
Loading prover...
  Prover ready (180ms)
Local peer: 12D3KooW...
Connected to relay 12D3KooW...
Authenticating (role=any)...
  AUTHORIZED (356ms)

ZKP anonymous auth successful.
```

### What the timings mean

| Phase | Typical time | What happens |
|-------|-------------|--------------|
| Key bootstrap | 2-3s (first run) | Compiles PLONK circuit, generates SRS, creates proving/verifying keys. Cached after first run. |
| Tree build | < 2ms | Reads `authorized_keys`, computes Poseidon2 leaf per peer, builds Merkle tree. |
| Prover load | 150-200ms | Loads proving key from disk, recompiles circuit. |
| Proof generation | ~1.8s | Generates the PLONK proof (the expensive part). |
| Verification | 2-3ms | Relay verifies the proof against the Merkle root. |

Total round-trip (after first run): ~1.8s proving + 2-3ms verification. The relay sees nothing about your identity.

## Auth types

### Membership proof (default)

Proves "I am in the authorized set." No role information revealed.

```bash
shurli relay zkp-test --auth-keys /path/to/keys --relay <addr>
```

### Role-based proof

Proves "I am an admin in the authorized set" without revealing which admin.

```bash
shurli relay zkp-test --auth-keys /path/to/keys --relay <addr> --role 1
```

Role values: `0` = any (membership only), `1` = admin, `2` = member.

If you specify `--role 1` but your peer is a member (not admin), the proof will fail. The relay only learns that the proof was invalid, not why.

## Getting the proving key to client nodes

Clients need the proving key to generate proofs. Two options:

### Option A: Same seed phrase

If all nodes share a seed phrase, each generates identical keys locally:

```bash
shurli relay zkp-setup --seed "word1 word2 word3 ... word24"
```

### Option B: Download from relay

For invited peers who should not have the seed phrase:

```bash
curl --unix-socket /path/to/shurli.sock \
  http://localhost/v1/zkp/proving-key -o provingKey.bin

curl --unix-socket /path/to/shurli.sock \
  http://localhost/v1/zkp/verifying-key -o verifyingKey.bin
```

Both keys are public circuit parameters, not secrets. Safe to distribute.

## Wire protocol details

For developers building integrations, the protocol ID is `/shurli/zkp-auth/1.0.0`. All messages are binary, length-prefixed:

```
Client -> Relay:  [1 version] [1 auth_type] [1 role_required]
Relay  -> Client: [1 status] [8 nonce] [32 merkle_root] [1 tree_depth]
Client -> Relay:  [2 proof_length] [N proof_bytes]
Relay  -> Client: [1 status] [1 msg_length] [N message]
```

Proofs are ~520 bytes. The nonce is single-use with a 30-second TTL, preventing replay attacks.

## Monitoring ZKP auth

The relay exposes Prometheus metrics for ZKP operations:

| Metric | Type | What it tracks |
|--------|------|----------------|
| `shurli_zkp_auth_total` | Counter | Auth attempts by result (success, proof_invalid, tree_not_ready, ...) |
| `shurli_zkp_verify_total` | Counter | Proof verifications by result |
| `shurli_zkp_verify_duration_seconds` | Histogram | Verification latency |
| `shurli_zkp_prove_total` | Counter | Client-side proof generations |
| `shurli_zkp_prove_duration_seconds` | Histogram | Proof generation latency |
| `shurli_zkp_challenges_pending` | Gauge | Outstanding challenge nonces |

See [Monitoring](../monitoring/) for Grafana dashboard setup.

## Troubleshooting

### "tree not initialized"

The Merkle tree has not been built. On the relay:

```bash
shurli relay unseal
shurli relay admin zkp-tree-rebuild
```

### "proof verification failed"

- Client and relay keys may be incompatible. Ensure both used the same seed phrase, or the client downloaded keys from this relay.
- Client's peer ID might not be in `authorized_keys`. Check with `shurli relay list-peers`.
- The tree may be stale. Rebuild after any changes to `authorized_keys`.

### "challenge rejected"

- ZKP is not enabled on the relay. Add `security.zkp.enabled: true` to config.
- The tree was rebuilt between challenge issuance and proof submission (rare; retry).

### "unsupported protocol version"

Client and relay are running different Shurli versions. Update both to the same release.

### Proof generation is slow

- First run includes circuit compilation (~2-3s one-time cost). Subsequent runs: ~1.8s proving.
- If consistently slow, check system resources. Proof generation is CPU-bound.

---

**Next step**: [Monitoring](../monitoring/) - set up Prometheus and Grafana to see everything your relay is doing in real time, including ZKP authentication metrics.
