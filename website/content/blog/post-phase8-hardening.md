---
title: "Post-Phase 8: Public Seeds, Better Onboarding, Hardened Internals"
date: 2026-03-06T10:00:00+13:00
tags: [release, security, onboarding]
description: "Public seed relays live, async invite flow, relay naming, vault auto-init, 14 security fixes, remote unseal protocol, and DNSSEC. 38 commits, 122 files."
image: /images/blog/post-phase8-hardening.svg
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Post-Phase 8: Hardened Infrastructure](/images/blog/post-phase8-hardening.svg)

## What changed

This is not a new phase. It is the hardening pass that turns Phase 8's raw capability into a product people can actually use. Thirty-eight commits across 122 files. The highlights: public seed relays are live, onboarding is drastically simpler, and 14 security findings were identified and fixed.

## What this means for you

**You can connect without knowing anyone.** Public seed relays in two regions (AU and SG) are now hardcoded into the binary and published as DNS TXT records. Run `shurli init`, pick your password, and you are on the network. No relay address to copy-paste, no configuration file to edit.

**First run is one step.** `shurli init` now creates your identity, initializes the vault, generates your seed phrase, and connects to seeds. One password prompt, one seed to write down, done. Previously this was five separate steps.

**Inviting peers is simpler.** Start your daemon, run `shurli invite`. You get a short code. Your friend runs `shurli join <code>`. PAKE v2 handles the cryptography. The relay mediates the introduction. Previously, pairing required both peers to be online simultaneously. Now invites survive the inviter going offline: the relay stores the introduction and delivers it when both peers reconnect.

**You can see what your relay is doing.** `shurli status` shows relay connectivity, operator messages, and peer verification status. `shurli relay list-peers` shows connected peers with transport details. Relay names show up next to addresses (configured via the `name` field in relay config). Connection status is visible: `[connected]` or `[disconnected]`. If the relay operator set a message of the day, you see it on connect.

![Relay Visibility](/images/blog/post-phase8-relay-visibility.svg)

**Continuous ping works properly.** `shurli ping <peer>` now loops client-side instead of sending a single ping with count=1000000. Ctrl+C prints a summary with min/avg/max/loss stats. Matches how you expect ping to behave.

## Seed relay separation

The most important architectural change: public seed relays are discovery-only. They connect your node to the DHT and help peers find each other, but they do not relay your data by default.

![Seed Relay Separation](/images/blog/post-phase8-seed-separation.svg)

This is a deliberate separation:

| Relay type | Discovery | Data relay | Who runs it |
|-----------|-----------|------------|-------------|
| **Seed relay** | Yes | No (unless admin/relay_data) | Shurli project |
| **Private relay** | Yes | Yes | You or someone you trust |
| **Peer relay** | Via DHT | Yes (among authorized peers) | Any peer with a public IP |

Why this matters: seed relays scale to thousands of nodes without bandwidth concerns. Your SSH sessions, file transfers, and XRDP streams go direct (IPv6/IPv4) or through relays you explicitly trust. The seed relay never sees your data unless you are an admin or have `relay_data=true` in its authorized_keys.

The circuit ACL enforces this server-side. It cannot be bypassed by a client. And as of this release, the ACL caches its authorization data in memory instead of reading the authorized_keys file on every circuit decision.

## Async invite flow

The invite system was redesigned. Previously, both peers had to be connected to the relay simultaneously during pairing. Now the relay acts as a store-and-forward intermediary:

![Async Invite Flow](/images/blog/post-phase8-async-invite.svg)

1. **Inviter** creates an invite code (short, relay-mediated)
2. The relay stores a "contact card" (introduction) for the joiner
3. **Joiner** runs `shurli join <code>` at any time, even days later
4. PAKE v2 handshake completes, establishing mutual trust
5. Relay pushes the introduction to the inviter's daemon via `/shurli/peer-notify/1.0.0`

The contact card is a macaroon-backed deposit. It can be attenuated (restricted) but never widened. If the inviter wants to limit the invite to SSH-only access with a 24-hour window, they set those caveats at creation time.

## Vault auto-init

New nodes no longer need to manually initialize the vault. `shurli init` detects a fresh install and runs the full init sequence: password, seed phrase display, vault creation. One prompt, one flow.

Relay setup follows the same pattern. `relay-setup.sh` handles everything: install Go, build the binary, create the service user, generate config, initialize the vault. FHS paths (`/usr/local/bin`, `/etc/shurli`, `/var/lib/shurli`) instead of scattered locations.

## Security hardening (14 findings fixed)

Two rounds of security review. The first batch (8 findings) was committed earlier in the cycle. The pre-merge audit found 6 more. All fixed before merge.

![Security Hardening Summary](/images/blog/post-phase8-security-fixes.svg)

### First batch (8 fixes)
- **TOCTOU in token claim**: pairing tokens now use an `InProgress` flag under mutex to prevent double-claim races
- **Stream deadlines**: all P2P protocol handlers now set 30-second deadlines
- **File race in authorized_keys**: atomic write pattern (write temp, rename) prevents partial reads
- **Admin origin check**: fail-closed origin tagging on remote admin requests
- **Invite ACL**: group ownership enforcement, per-peer invite quota, scoped listing
- **Token constant-time comparison**: `crypto/subtle.ConstantTimeCompare` for all token lookups
- **Token burn-after-three**: failed pairing attempts burn the token after 3 tries
- **Enrollment probation**: per-IP rate limiting with IPv6 /64 normalization

### Pre-merge audit (6 fixes)
- **Daemon auth timing**: switched from `==` to `subtle.ConstantTimeCompare` for bearer token
- **Admin body limits**: added `http.MaxBytesReader` to authorize/deauthorize endpoints
- **Client response limits**: added `io.LimitReader` (10 MB cap) on daemon client
- **Circuit ACL caching**: authorized_keys data cached in memory, refreshed on auth-reload (was reading file per circuit decision)
- **Probation map bounded**: IP cooldown map capped at 1,000 entries with stale eviction
- **Peer relay security documented**: connection gater is the ACL for peer relays (explicit design boundary)

### Remote unseal protocol fix

`relay unseal --remote` was broken: the client sent `POST /v1/unseal` through the generic admin proxy, which correctly blocks it (passphrase should not travel through a generic JSON adapter). The dedicated `/shurli/relay-unseal/1.0.0` protocol existed but was never registered as a stream handler.

Fixed: the relay now registers the `UnsealHandler` on the dedicated protocol, and the CLI opens a stream directly on `/shurli/relay-unseal/1.0.0`. This gives remote unseal its own security properties:

- **Binary wire format** (not JSON through a generic adapter)
- **iOS-style escalating lockout**: 4 free tries, then 1 minute, 5 minutes, 15 minutes, 1 hour, permanent block
- **Per-peer failure tracking**: each admin peer's attempts tracked independently
- **Dedicated metrics**: `vault_unseal_total`, `vault_unseal_locked_peers`

### DNSSEC for DNS seeds

The seed domain's `_dnsaddr` TXT records are now DNSSEC-signed. This prevents DNS spoofing of bootstrap records. Defense-in-depth: even without DNSSEC, the ConnectionGater rejects unauthorized peers post-bootstrap, and hardcoded fallback seeds in the binary provide an independent bootstrap path.

### Known deferred
Two upstream vulnerabilities remain without fixes:
- **GO-2026-4479** (pion/dtls v2 nonce reuse): fix merged on go-libp2p master, waiting for v0.48.0 release
- **GO-2024-3218** (kad-dht): no upstream fix available

Go 1.26.1 security release is available (March 5). Upgrade recommended.

## What is next

- Phase 9: Plugin SDK, exposing all internal capabilities to third-party developers
- Phase 10: Distribution (apt, brew, snap) and public launch

The onboarding story is now: install, init, connect. Three steps to a private P2P network. The infrastructure is hardened. The relays are public. The next push is making it beautiful.
