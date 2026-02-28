---
title: "Securing Your Relay"
weight: 6
description: "Lock down your relay with a passphrase-sealed vault, two-factor authentication, auto-seal timeout, and remote unseal from any device. Step-by-step setup with disaster recovery."
---

After deploying your relay (see [Relay Setup](../relay-setup/)), the next step is securing it. This guide walks you through vault initialization, two-factor authentication, and remote unseal.

## Prerequisites

- Relay deployed and running (`shurli relay serve`)
- SSH access to your relay server (or local terminal)
- All `shurli relay vault` and `shurli relay invite` commands run on the relay server itself (they talk to the relay through a local Unix socket)

## Why secure your relay

Without the vault, your relay starts with full privileges after every reboot. The root key (used to mint invite tokens and authorize peers) sits unprotected in memory. If an attacker compromises the server, they own the relay.

The vault changes this:

| | Without vault | With vault |
|--|---------------|------------|
| **After reboot** | Full privileges, wide open | Locked, watch-only mode |
| **Root key** | In memory, unprotected | Encrypted on disk (Argon2id + XChaCha20-Poly1305) |
| **New invites** | Anyone with server access | Only after passphrase + optional 2FA |
| **Compromise risk** | Attacker gets everything | Attacker gets encrypted data |

## Step 1: Initialize the vault

On your relay server:

```bash
shurli relay vault init --auto-seal 30
```

You'll be prompted for a passphrase (minimum 8 characters) and confirmation:

```
Enter passphrase: ********
Confirm passphrase: ********
```

The command outputs two critical pieces of information:

1. **Seed phrase** - 24 hex pairs (e.g., `a1 b2 c3 d4 ...`). This is your last-resort recovery key. Write it down on paper. Store it offline. If you lose both the passphrase and seed, the vault is gone.

2. **Seal status** - the vault starts unsealed after init so you can immediately create invites and authorize peers.

> **The `--auto-seal 30` flag** tells the vault to automatically relock after 30 minutes of being unsealed. Adjust this to your workflow: 15 minutes for high-security, 60 for convenience, 0 for manual-only sealing.

### Verify

```bash
shurli relay vault status
```

You should see:

```
Status:    UNSEALED
TOTP:      disabled
Auto-seal: 30 minutes
```

## Step 2: Add two-factor authentication (optional)

Two options, both optional. Choose one or both.

### Option A: Authenticator app (TOTP)

Initialize with TOTP enabled:

```bash
shurli relay vault init --totp --auto-seal 30
```

After entering your passphrase, you'll see an `otpauth://` URI:

```
otpauth://totp/Shurli:my-relay?secret=JBSWY3DPEHPK3PXP&algorithm=SHA1&digits=6&period=30&issuer=Shurli
```

Scan this URI with any standard authenticator app (Google Authenticator, Authy, or any TOTP app). The app generates 6-digit codes that refresh every 30 seconds.

> **Already initialized without TOTP?** You'll need to recover from seed and re-initialize with `--totp`. See the disaster recovery section below.

### Option B: YubiKey (hardware key)

If you have a YubiKey with HMAC-SHA1 configured on Slot 1 or 2:

1. Install `ykman`: `pip install yubikey-manager` (or your system's package manager)
2. Verify: `ykman info` should show your key
3. Configure HMAC-SHA1 on a slot: `ykman otp chalresp --generate 2` (Slot 2)

YubiKey support activates automatically when `ykman` is installed and a key is connected. The 15-second timeout accommodates touch-required keys.

## Step 3: Test the seal/unseal cycle

Before relying on the vault in production, test the full cycle:

```bash
# 1. Seal the vault (switches to watch-only mode)
shurli relay vault seal

# 2. Check status
shurli relay vault status
# Status: SEALED

# 3. Unseal with your passphrase
shurli relay vault unseal
# Enter passphrase: ********
# (Enter TOTP code if enabled)

# 4. Verify unsealed
shurli relay vault status
# Status: UNSEALED
```

While sealed, the relay continues routing traffic for existing peers. But it cannot create new invites, authorize new peers, or mint new tokens. This is the safe default after every restart.

> **Shorthand aliases**: `shurli relay seal`, `shurli relay unseal`, and `shurli relay seal-status` work identically to the `vault` subcommands.

## Step 4: Remote unseal

After a restart, the vault is sealed. You need to unseal it. But you might be on your phone behind CGNAT, on a tablet at a cafe, or away from SSH.

Any admin peer can unseal the vault over the P2P network:

```bash
# From any device where you're an admin, on any network
shurli relay vault unseal --remote /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
```

You can also use the relay's peer ID or a configured name:

```bash
# By peer ID
shurli relay vault unseal --remote 12D3KooW...

# With TOTP
shurli relay vault unseal --remote my-relay --totp
```

The command prompts for your passphrase (and TOTP code if enabled), then sends them over the encrypted P2P connection using the `/shurli/relay-unseal/1.0.0` protocol. The relay checks your admin status before reading the passphrase. Non-admins are rejected immediately.

### Escalating lockout

Remote unseal has brute-force protection with an iOS-style escalating lockout:

| Attempt | What happens |
|---------|-------------|
| 1-4 | Immediate retry (for typos) |
| 5 | 1-minute lockout |
| 6 | 5-minute lockout |
| 7 | 15-minute lockout |
| 8-10 | 1-hour lockout each |
| 11+ | Permanently blocked from remote unseal |

A successful unseal resets the counter. If permanently blocked, you must SSH to the server to unseal locally or fix the lockout.

## Disaster recovery

### Lost passphrase

If you have the seed phrase from initialization:

```bash
shurli relay vault recover --seed "a1 b2 c3 d4 e5 ..."
```

This reconstructs the root key and lets you set a new passphrase. If you also want TOTP:

```bash
shurli relay vault recover --seed "a1 b2 c3 ..." --totp --auto-seal 30
```

> **No seed phrase?** The vault is cryptographically locked. There is no backdoor. You'll need to re-initialize the relay identity and re-pair all peers. This is by design.

### Locked out of remote unseal

If your peer is permanently blocked (11+ failed attempts), SSH to the relay server and unseal locally:

```bash
# On the relay server directly
shurli relay vault unseal
```

Local unseal through the Unix socket is not subject to the escalating lockout.

### Auto-seal fired while you're working

The auto-seal timer runs from the moment you unseal. If you need more time:

- Unseal again (resets the timer)
- Set a longer timeout: re-initialize with `--auto-seal 60` (or 0 for manual-only)

## Security checklist

| Item | Done? |
|------|-------|
| Passphrase is strong (8+ characters, not reused) | |
| Seed phrase written on paper and stored offline | |
| Seed phrase is NOT stored digitally on the relay | |
| TOTP registered in authenticator app (if enabled) | |
| Auto-seal timeout configured (recommended: 30-60 min) | |
| Tested seal/unseal cycle before relying on it | |
| Tested remote unseal from a second device | |
| Relay restarts into sealed (watch-only) mode | |

---

**Next step**: [Inviting Peers](../inviting-peers/) - create pairing codes and async invite deposits to bring people onto your network.
