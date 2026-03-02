---
title: "ZKP Privacy Setup"
weight: 9
description: "Set up zero-knowledge proof authentication on your relay. Generate deterministic keys from a seed phrase, build the Merkle tree, and enable anonymous auth for your network."
---

After securing your relay (see [Securing Your Relay](../relay-security/)) and connecting peers (see [Inviting Peers](../inviting-peers/)), you can enable zero-knowledge proof authentication. This lets peers prove they're authorized without revealing which peer they are.

## What ZKP auth does

Standard Shurli authentication works like a guest list: the relay checks your peer ID against `authorized_keys`. It works, but the relay knows exactly who you are every time you connect.

ZKP auth works differently. Instead of showing your ID, you prove you know a secret (your private key) whose public key is somewhere in the authorized set. The relay confirms you're authorized without learning which authorized peer you are.

| | Standard auth | ZKP auth |
|--|---------------|----------|
| **Relay learns your identity** | Yes, every connection | No |
| **Proof of authorization** | Peer ID in authorized_keys | PLONK zero-knowledge proof |
| **What the relay sees** | Which peer connected | That an authorized peer connected |
| **Proof size** | Peer ID (~52 bytes) | 520 bytes |
| **Auth time** | Instant (lookup) | ~1.8s proving + 2-3ms verification |

Both modes coexist. Standard auth continues to work for all existing connections. ZKP auth is an additional protocol that peers can use when they want anonymity.

## Prerequisites

- Relay deployed and running with Phase 7+ binary
- At least one peer authorized via `authorized_keys`
- `security.zkp.enabled: true` in relay config
- All `shurli relay zkp-*` commands run on the relay server (Unix socket)

## Step 1: Enable ZKP in config

On your relay server, add the ZKP section to your config:

```yaml
security:
  zkp:
    enabled: true
    srs_cache_dir: "zkp-keys"    # relative to working directory
    max_tree_depth: 20           # supports up to 1M peers
```

> **systemd users**: If your relay runs under systemd with `ProtectHome=read-only`, use a relative path for `srs_cache_dir` (relative to your `WorkingDirectory`) instead of the default `~/.shurli/zkp/`.

Restart the relay to pick up this config change (ZKP enablement requires a restart since it changes the relay's circuit compilation).

## Step 2: Generate keys from a seed phrase

The ZKP system uses PLONK proving keys and verifying keys derived from a BIP39 seed phrase. Same seed = same keys on any machine.

> **Unified seed (Phase 8)**: If you initialized your relay with `shurli init` (Phase 8+), you already have a BIP39 seed phrase that derives your identity key. The ZKP keys use the same seed via a different HKDF domain, so you only need one seed backup for everything. Use `--seed` with the same phrase you wrote down during init.

```bash
# Generate keys from your existing seed phrase (from 'shurli init')
shurli relay zkp-setup --seed "word1 word2 word3 ... word24"
```

Output:

```
Deriving PLONK keys from seed phrase...
(Same seed as identity and vault - one backup covers everything)
  Keys saved to ~/.shurli/zkp/ (3.2s)
Done. Relay and clients using this seed will produce compatible proofs.
```

> **Interactive mode**: If you don't provide `--seed`, the command will prompt you to enter the seed phrase interactively.

## Step 3: Build the Merkle tree

The Merkle tree is built from your `authorized_keys` file. Every authorized peer becomes a leaf in the tree.

```bash
# Unseal the vault first (tree rebuild requires unsealed vault)
shurli relay unseal

# Rebuild the tree
shurli relay admin zkp-tree-rebuild
```

The relay logs the result:

```
zkp: merkle tree rebuilt  leaves=5  depth=3  duration=1.2ms
```

The tree is also automatically rebuilt on relay startup when ZKP is enabled.

## Step 4: Verify the setup

Check the tree state:

```bash
shurli relay admin zkp-tree-info
```

Output:

```json
{
  "ready": true,
  "root": "a1b2c3d4...",
  "leaves": 5,
  "depth": 3
}
```

## Getting keys to clients

Clients need the proving key and verifying key to generate proofs. Two options:

### Option A: Seed phrase (recommended for operators)

If you manage all nodes, enter the same seed phrase on each client:

```bash
shurli relay zkp-setup --seed "word1 word2 word3 ... word24"
```

Same seed = same keys. No file copying needed.

### Option B: Download from relay (recommended for invited peers)

Clients can download keys from the relay's admin API:

```bash
# These endpoints don't require vault unseal (keys are public circuit parameters)
curl --unix-socket /path/to/shurli.sock http://localhost/v1/zkp/proving-key -o provingKey.bin
curl --unix-socket /path/to/shurli.sock http://localhost/v1/zkp/verifying-key -o verifyingKey.bin
```

> **Important**: Proving keys and verifying keys are public circuit parameters, not secrets. They're safe to distribute to any authorized peer. They're derived from the SRS (Structured Reference String) and contain no information about the seed phrase.

## Understanding the key files

| File | Size | Purpose | Secret? |
|------|------|---------|---------|
| `provingKey.bin` | ~2 MB | Used by clients to generate proofs | No (public) |
| `verifyingKey.bin` | ~34 KB | Used by relay to verify proofs | No (public) |
| Seed phrase | 24 words | Derives both keys deterministically | Yes (never store on disk) |

The proving key is what makes proof generation possible. The verifying key is what makes verification fast (~3ms). Both are mathematical artifacts, not secrets.

## When to rebuild the tree

Rebuild the tree whenever `authorized_keys` changes:

- After a new peer joins (pairing or invite deposit)
- After removing a peer
- After changing a peer's role

```bash
# Always unseal first, then rebuild
shurli relay unseal
shurli relay admin zkp-tree-rebuild
```

The tree rebuild is fast: ~2ms for 500 peers.

## Troubleshooting

### "zkp auth not configured"

ZKP is not enabled in your config. Add `security.zkp.enabled: true` and restart the relay (ZKP enablement requires a restart).

### "tree not initialized"

The Merkle tree hasn't been built yet. Run `shurli relay admin zkp-tree-rebuild` (requires unsealed vault).

### "zkp circuit not compiled"

Keys haven't been generated yet. Run `shurli relay zkp-setup --seed "..."` to generate keys from your seed phrase.

### Keys directory blocked by systemd

If using `ProtectHome=read-only`, set `srs_cache_dir` to a relative path under your `WorkingDirectory` instead of the default `~/.shurli/zkp/`.

### Client proof rejected

- Ensure client and relay have compatible keys (same seed, or client downloaded from relay)
- Ensure the client's peer ID is in the relay's `authorized_keys`
- Rebuild the tree if authorized_keys changed since last rebuild

## Next step

With ZKP enabled, learn how to [use anonymous authentication](../zkp-anonymous-auth/) to connect without revealing your identity.
