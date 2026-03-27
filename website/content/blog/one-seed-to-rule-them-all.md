---
title: "Phase 8: One Seed to Rule Them All"
date: 2026-03-02T14:00:00+13:00
tags: [release, phase-8]
description: "Unified BIP39 seed, encrypted identity, full remote relay admin over P2P, signed operator announcements, session tokens. 24 new files, 56 tests. One backup covers everything."
image: /images/blog/phase8-identity-remote-admin.svg
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Phase 8: One Seed to Rule Them All](/images/blog/phase8-identity-remote-admin.svg)

## The problem we solved

Phase 7 gave peers privacy: zero-knowledge proofs, anonymous auth, private reputation. But the operational story was still fragmented. Three separate key management paths: identity key, vault passphrase, ZKP circuit keys. Each with its own backup, its own recovery, its own failure mode.

And relay administration meant SSH. Every vault unseal, every MOTD change, every invite creation: open a terminal, SSH to the VPS, run the command locally. Secure, but friction-heavy. Running a relay shouldn't feel like babysitting a server.

Phase 8 fixes both: one seed phrase for everything, and full relay management over the same encrypted P2P connections your peers already use.

## What this means for you

**One backup covers everything.** Write down 24 words once. That seed derives your identity key, your vault key, and your ZKP circuit keys. Lose your machine? Those 24 words reconstruct every key. Same construction Bitcoin HD wallets use, applied to P2P networking.

**Your identity key is encrypted at rest.** Every node, not just relays. Argon2id KDF + XChaCha20-Poly1305. If someone copies your `identity.key`, they get ciphertext. Without your password, it's useless.

**Manage your relay from anywhere.** Full admin over P2P: unseal the vault, create invites, set announcements, rebuild ZKP trees. All 19 admin endpoints, all from your laptop. No SSH session required.

**Operators can talk to their network.** Set a message of the day. Announce planned maintenance. Push a goodbye notice before decommissioning. Every message signed by the relay's Ed25519 key, verified by every client. Defense against prompt injection built in.

**Session tokens for convenience without compromise.** Enter your password once, work until the token expires. Machine-bound: copying the token to another device doesn't work. Lock/unlock for sensitive operations without destroying the session.

## Unified seed architecture

```
BIP39 Seed (24 words)               <-- ONE backup
    |
    |-- HKDF(seed, "shurli/identity/v1")  --> Ed25519 private key
    |                                          (encrypted with password)
    |
    |-- HKDF(seed, "shurli/vault/v1")     --> Vault root key
    |                                          (encrypted with vault password)
    |
    `-- SRS derivation from seed           --> ZKP proving/verifying keys
                                               (cached as .bin files)
```

Three HKDF domain separators. Cryptographically independent outputs from the same seed. Compromise one key and the others remain safe, because HKDF with different `info` parameters produces unrelated key material.

The seed itself is never stored on disk. It exists in memory during `shurli init` (where you confirm it via a 3-word quiz) and during `shurli recover`. Then it's zeroed.

## Encrypted identity

![SHRL Encrypted Identity Format](/images/blog/phase8-encrypted-identity.svg)

Every `identity.key` on every node:

| Parameter | Value |
|-----------|-------|
| KDF | Argon2id (time=3, memory=64MB, threads=4) |
| Cipher | XChaCha20-Poly1305 |
| Nonce | 24 bytes (random per encryption) |
| Salt | 16 bytes (random per encryption) |
| File format | `SHRL` magic + version + salt + nonce + ciphertext |

Legacy unencrypted key files from older installations are detected automatically. The node prompts you to encrypt on first use.

`shurli change-password` re-encrypts with a new password. The key itself doesn't change; only the encryption wrapper rotates.

## Remote admin over P2P

![Remote Admin Architecture](/images/blog/phase8-remote-admin.svg)

Protocol: `/shurli/relay-admin/1.0.0`

19 admin endpoints accessible over encrypted P2P streams. JSON-over-stream with length-prefixed framing. The remote admin handler translates P2P requests into local admin socket calls and streams responses back.

**Security model:**
- Admin role check at stream open (non-admins rejected before any data exchange)
- Rate limited: 5 requests per second per peer
- Same auth model as the local Unix socket
- Vault init is LOCAL ONLY: seed material never travels over the network. Recovery requires SSH or physical access. This is a deliberate security decision, not a missing feature.

All relay commands support `--remote <addr>`:

```bash
# From your laptop, manage a relay across the internet
shurli relay vault unseal --remote /ip4/203.0.113.50/tcp/4001/p2p/QmRelay...
shurli relay invite create --caveat "service:ssh" --remote /ip4/203.0.113.50/...
shurli relay motd set "Maintenance window: Sunday 2am-4am UTC" --remote ...
```

No SSH. No VPN. Just the same P2P connection your peers use, with admin role enforcement.

## Signed operator announcements

![MOTD Wire Protocol](/images/blog/phase8-motd-protocol.svg)

Protocol: `/shurli/relay-motd/1.0.0`

Three message types, all Ed25519-signed by the relay's identity key:

| Type | Wire value | Purpose |
|------|-----------|---------|
| MOTD | `0x01` | Short announcement shown on connect (280-char max, deduped per relay for 24h) |
| Goodbye | `0x02` | Persistent farewell pushed to all connected peers immediately. Cached by clients, survives restarts. |
| Retract | `0x03` | Cancels a goodbye (relay is back) |

Wire format: `[1 version][1 type][2 BE msg-len][N msg][8 BE timestamp][Ed25519 sig]`

**Defense in depth on message content:**
- URL stripping (no phishing links)
- Email stripping (no contact harvesting)
- Non-ASCII removal (no homograph attacks)
- Control character stripping
- 280-character truncation
- Timestamp validation: reject messages >5 minutes in the future or >7 days old

The goodbye lifecycle enables graceful relay decommission: `relay goodbye set` pushes to all peers, `relay goodbye retract` clears cached goodbyes if you change your mind, `relay goodbye shutdown` sends the farewell then initiates graceful shutdown.

## Session tokens

Machine-bound auto-decrypt so you don't have to type your password on every daemon restart.

```
Token format: [SHRS][version:1][installRandom:32][nonce:24][ciphertext...]

Key derivation:
  HKDF-SHA256(
    IKM  = installRandom (32 random bytes, per-session),
    salt = machineID (OS-specific, SHA256'd),
    info = "shurli/session/v1"
  ) --> 32-byte machine key
```

The token encrypts your identity password with a key derived from both random material and your machine's hardware identity. Copy the token to another machine: decryption fails. This is the same UX as ssh-agent: unlock once, work until you lock or the session expires.

`shurli lock` gates sensitive operations without destroying the session. `shurli unlock` re-enables them with your password.

## CLI completeness

Phase 8 also closes the gap on CLI quality of life:

- `shurli doctor [--fix]`: validates installation health (config, permissions, completions, man page) and auto-fixes issues
- `shurli completion <bash|zsh|fish>`: shell completion scripts
- `shurli man`: troff man page (display, install to system, uninstall)

These are small additions individually, but they make the difference between "developer tool" and "production software."

## Impact

| | Before Phase 8 | After Phase 8 |
|--|----------------|---------------|
| **Key management** | 3 separate backup paths (identity, vault, ZKP) | 1 seed phrase covers everything |
| **Identity at rest** | Unencrypted (relays only had vault encryption) | Argon2id + XChaCha20-Poly1305 on every node |
| **Relay admin** | SSH to VPS, run commands locally | Full admin over P2P (19 endpoints) |
| **Operator comms** | None | Signed MOTD/goodbye/retract (Ed25519 verified) |
| **Session management** | Password on every restart | Machine-bound session tokens (auto-unlock) |
| **CLI polish** | No completions, no man page | bash/zsh/fish completions, troff man page, doctor |
| **New code** | | 24 files, 56 tests |
| **New protocols** | | `/shurli/relay-admin/1.0.0`, `/shurli/relay-motd/1.0.0` |

24 new files. 56 tests. 2 new P2P protocols. 19 remote admin endpoints. One seed phrase for everything. Your relay is now manageable from anywhere, your identity is encrypted at rest, and your operators can talk to their network.
