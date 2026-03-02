---
title: "What Shurli Actually Does (And Why Nothing Else Does All of It)"
date: 2026-03-01T14:00:00+13:00
tags: [overview]
pinned: true
description: "A factual inventory of what ships in the Shurli binary today: P2P networking, cryptographic authorization, zero-knowledge anonymous auth, private reputation, unified BIP39 identity, full observability. Single binary, zero cloud, zero accounts."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
image: /images/blog/the-shurli-difference.svg
---

![What Shurli Actually Does](/images/blog/the-shurli-difference.svg)

This is not a roadmap pitch. Everything described here is shipped, tested code running on physical hardware across multiple networks. This post exists because no other project combines all of these capabilities in one binary.

## The short version

Shurli is a single Go binary that gives you a private peer-to-peer network with:

1. Automatic NAT (Network Address Translation) traversal and path selection
2. Cryptographic identity and authorization
3. Zero-knowledge anonymous authentication
4. Private reputation proofs with binding score commitment
5. Unified BIP39 seed identity (one backup recovers everything)
6. Full Prometheus observability
7. Zero cloud accounts, zero third-party services

Each of these exists somewhere in isolation. The combination does not.

## What the binary does today

### 1. Peer-to-peer networking that works behind any NAT

![Automatic Path Selection](/images/blog/the-shurli-difference-path-selection.svg)

Shurli uses libp2p for transport and builds its own networking intelligence on top. When two peers need to connect, Shurli automatically finds the best path:

| Priority | Path type | When it's used |
|----------|-----------|---------------|
| 1 | LAN (mDNS) | Same local network, discovered automatically |
| 2 | Direct IPv6 | Both peers have IPv6, path probing confirms reachability |
| 3 | Direct IPv4 (STUN) | Both peers have public IPv4 or successful hole punch |
| 4 | Relay | Fallback through a Shurli relay server |

Path selection is continuous. If you start on cellular (relayed), walk into your home WiFi (direct IPv6), and then switch to a hotspot (back to relay), Shurli re-evaluates and transitions automatically. No reconnection. No configuration. Tested across satellite, cellular, terrestrial WiFi, and USB LAN networks.

This is built on:
- **Kademlia DHT (Distributed Hash Table)** for peer discovery (`/shurli/kad/1.0.0` protocol)
- **mDNS (multicast DNS)** for zero-config local discovery
- **Circuit relay v2** for NAT traversal
- **QUIC, TCP, and WebSocket** transports
- **Path probing** with ranked scoring to prefer direct connections

The daemon manages all connections and maintains a peer lifecycle (PeerManager) with promotion, demotion, and state tracking. Subcommands (ping, traceroute, proxy) route through the daemon by default, sharing its optimized connections.

### 2. Cryptographic authorization (not just authentication)

![Four Layers of Authorization](/images/blog/the-shurli-difference-auth-layers.svg)

Authentication answers "are you who you claim to be?" Authorization answers "what are you allowed to do?" Shurli does both, with four layers:

**Layer 1: Identity.** Every peer has an Ed25519 keypair, encrypted at rest with Argon2id + XChaCha20-Poly1305. The peer ID is derived from the public key. You cannot impersonate a peer without the private key and the encryption password.

**Layer 2: Allowlisting.** The `authorized_keys` file lists every peer ID that can connect. Unauthorized peers are rejected at the connection gater before any data flows. Attributes per peer: role (admin/member), verified status, expiration, group membership.

**Layer 3: Capability tokens.** Macaroon tokens (HMAC-SHA256 chains) define fine-grained permissions: which services a peer can use, which groups they belong to, when access expires, how many sub-invites they can create. Seven caveat types, composable. The critical property: tokens can only be made stricter (add restrictions), never looser. This isn't a software rule; it's a mathematical guarantee from the HMAC chain.

**Layer 4: Authority.** Two roles: admin and member. The first peer auto-promotes to admin. Admins create invites, manage the relay, rebuild the zero-knowledge proof tree. Members use the network. Invite policy is configurable (admin-only or open).

### 3. Sealed vault with remote P2P unseal

![Sealed Vault with Remote P2P Unseal](/images/blog/the-shurli-difference-vault.svg)

The relay's secrets (root key for macaroon minting) live inside a vault encrypted with Argon2id + XChaCha20-Poly1305. After every restart, the vault is locked. The relay routes traffic and serves existing connections, but cannot create new invites or approve new peers.

To unlock, an admin provides a passphrase (plus optional TOTP or YubiKey challenge). This can happen locally or remotely over the P2P network from any admin device using the `/shurli/relay-admin/1.0.0` protocol. No SSH, no VPN, no new ports.

Escalating lockout: 4 free attempts, then 1 minute, 5 minutes, 15 minutes, 1 hour (x3), then permanently blocked. Auto-reseal after a configurable timeout. The key is zeroed from memory on seal.

### 4. Async invites (no simultaneous online requirement)

![Async Invites](/images/blog/the-shurli-difference-async-invites.svg)

Traditional P2P pairing needs both parties online at the same time. Shurli's invite deposits work like leaving a message: the admin creates a macaroon-backed invite, the invitee retrieves it hours or days later. The admin can tighten restrictions on the pending invite at any time (restriction-only, never loosen), or revoke it entirely.

### 5. Two-factor authentication

![Two-Factor Authentication](/images/blog/the-shurli-difference-2fa.svg)

TOTP (Time-based One-Time Password; works with any authenticator app) and YubiKey hardware key challenge. Both optional, both stored encrypted inside the vault. Pure Go standard library; no external auth services.

### 6. Zero-knowledge anonymous authentication

![Zero-Knowledge Anonymous Authentication](/images/blog/the-shurli-difference-zkp.svg)

This is where Shurli diverges from everything else in this category.

Standard authentication reveals your identity every time you connect. Shurli's zero-knowledge auth proves "I have a key whose public key is a leaf in the authorized set" without revealing which leaf. The relay learns one fact: an authorized peer connected. It never learns which one.

The proving system is PLONK (a zero-knowledge proof system) on BN254 (an elliptic curve), the same construction used by Ethereum Layer 2 rollups for transaction validity proofs. The hash function inside the circuit is Poseidon2, designed specifically for arithmetic circuits.

Performance:
- Proof size: 520 bytes
- Proof generation: ~1.8s (client-side, includes circuit compilation)
- Proof verification: 2-3ms (relay-side)
- Membership circuit: 22,784 SCS constraints
- Merkle tree build: ~2ms for 500 peers
- Tree capacity: 1,048,576 peers (depth 20)

Role-based anonymous auth works too: prove "I am an admin" without revealing which admin.

### 7. Private reputation (range proofs)

![Private Reputation Range Proofs](/images/blog/the-shurli-difference-reputation.svg)

Peers have a deterministic reputation score (0-100) computed from four locally observable metrics: availability, latency, path diversity, and tenure. Range proofs let a peer prove "my score is at least X" without revealing the exact number.

Same PLONK proving system, same Poseidon2 Merkle tree, extended with threshold comparison. 27,004 constraints, 520-byte proofs. The score is committed in the Merkle tree leaf hash alongside the public key and role, so the circuit enforces that the proven score matches what was committed. No self-reporting; the proof is binding.

### 8. Unified BIP39 seed identity

![BIP39 Seed Management](/images/blog/the-shurli-difference-bip39.svg)

One 24-word BIP39 seed phrase derives everything: your Ed25519 identity key, your vault encryption key, and your zero-knowledge proof circuit keys. Same phrase on any device = same identity + same keys. No manual file copying. The implementation is pure Go standard library.

`shurli init` generates the seed. Write it down. That's your backup for the entire identity: keys, vault, ZKP circuit parameters. Recover on any machine with `shurli recover`.

Session tokens (machine-bound, password-encrypted) allow password-free daemon restarts. `shurli lock` / `shurli unlock` gate sensitive operations without destroying the session.

### 9. Full relay management over P2P

Every relay admin operation - vault seal/unseal, invite creation, ZKP tree rebuild, peer management, MOTD announcements - works remotely over the `/shurli/relay-admin/1.0.0` protocol. Admin peers can manage the relay from any device on the network. No SSH, no VPN, no open ports beyond what the relay already listens on.

Operators can set a message of the day (MOTD) shown to peers on connect, or a "goodbye" message for planned relay decommission that persists on client devices across restarts. All messages are Ed25519-signed and verified by clients.

### 10. Full observability

![Full Observability](/images/blog/the-shurli-difference-observability.svg)

40 custom Prometheus metrics covering:

| Category | What's tracked |
|----------|---------------|
| Network | Connections, paths, hole punches, peer counts |
| Proxy | Throughput, sessions, bytes transferred |
| Auth | Pairing, invites, macaroon verifications |
| Vault | Seal state, seal/unseal events, lockout counts |
| Zero-knowledge | Proof gen/verify latency, auth outcomes, tree state, challenges |
| System | API latency, audit events, daemon health |

Every metric helper is nil-safe: if you don't set `--metrics-addr`, zero overhead. The pre-built Grafana dashboard has 56 panels across 11 row groups. Import the JSON, point at Prometheus, done.

### 11. Single binary, zero cloud

![Single Binary, Zero Cloud](/images/blog/the-shurli-difference-single-binary.svg)

Everything above compiles to one Go binary. ~37 MB stripped. No containers required (though it works in them). No cloud accounts. No third-party auth services. No subscription. No API keys.

systemd and launchd service files included. Cross-compiles to Linux (amd64, arm64) and macOS (arm64). The relay runs on a $5/month VPS (Virtual Private Server).

## Why this combination matters

Each capability above exists somewhere:

- P2P networking with NAT traversal? Several projects.
- Cryptographic identity with Ed25519? Standard in libp2p-based projects.
- Macaroon capability tokens? Lightning Network (LND) uses them for API auth.
- Sealed vault? Hardware security modules, various password managers.
- Zero-knowledge proofs? Dozens of blockchain projects.
- BIP39 seed phrases? Every cryptocurrency wallet.
- Prometheus metrics? Most production infrastructure.

What doesn't exist: all of them in one binary, working together, for the purpose of building a private P2P network that you own completely.

Zero-knowledge anonymous auth is Layer 2 blockchain technology. It exists in production on Ethereum rollups that process millions of transactions. It does not exist in any networking tool. Shurli applies PLONK membership proofs to network authorization. The score is committed in the Merkle tree, so range proofs are binding, not self-reported.

The result is a tool where:
- Your identity is cryptographic (Ed25519 keys, not usernames)
- Your authorization is mathematical (HMAC chains, not database roles)
- Your anonymity is provable (PLONK proofs, not trust)
- Your reputation is private (range proofs with binding commitment, not public scores)
- Your keys are deterministic (one BIP39 seed recovers everything)
- Your infrastructure is sovereign (single binary, not cloud APIs)
- Your operations are observable (40 Prometheus metrics, not blind hope)

## The numbers

| Metric | Value |
|--------|-------|
| Binary size | ~37 MB stripped |
| Languages | Go (single module) |
| External crypto dependencies | 0 (stdlib + golang.org/x/crypto, already in project) |
| Zero-knowledge proof dependency | 1 (gnark v0.14.0, ConsenSys, audited) |
| Custom Prometheus metrics | 40 |
| Grafana panels | 56 |
| Test count | 884 across 20 packages |
| P2P protocols | 11 custom |
| Admin API endpoints | 42 (24 relay + 18 daemon) |
| Subcommands | 24 |
| Supported transports | QUIC, TCP, WebSocket |
| Auth factors | Identity + passphrase + TOTP + YubiKey |
| Membership proof size | 520 bytes |
| Membership proof constraints | 22,784 SCS |
| Range proof constraints | 27,004 SCS |
| Proof verification time | 2-3ms |
| Tree capacity | 1M+ peers |
| Cloud accounts required | 0 |
| Subscriptions required | 0 |
| Third-party services required | 0 |

## Who this is for

Shurli is for people who want a private network they fully control. Not a network that someone else controls on their behalf. Not a network where "private" means "we promise not to look." A network where privacy is enforced by mathematics, authorization is enforced by cryptography, and the only infrastructure you depend on is a binary you compiled yourself.

Inspired by operators who chose to shut down rather than compromise their users.
