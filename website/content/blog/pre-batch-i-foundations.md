---
title: "Build Tooling, Encrypted Pairing, and Private Networks"
date: 2026-02-21
tags: [release, pre-batch-i]
description: "Makefile automation, PAKE-secured invite/join handshake, and protocol-level DHT namespace isolation. Three foundational items shipped before Batch I."
image: /images/blog/pre-batch-i-foundations.svg
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Pre-Batch I: Foundations](/images/blog/pre-batch-i-foundations.svg)

## What's new

Three independent foundational items shipped as Pre-Batch I, clearing the path for Batch I (Adaptive Path Selection):

1. **Build & Deployment Tooling** - Makefile with 12 targets
2. **Encrypted Invite/Join Handshake** - relay can't observe pairing tokens
3. **Private DHT Networks** - protocol-level isolation between peer groups

## Pre-I-a: Makefile

![Makefile Target Flow](/images/blog/pre-batch-i-makefile-flow.svg)

Every developer action now has a single command:

```bash
make build     # optimized binary with version/commit/date injection
make test      # go test -race -count=1 ./...
make install   # build + install binary + install systemd/launchd service
make check     # run commands from .checks file, fail on any non-zero
make push      # make check && git push (impossible to push without checks passing)
```

**OS detection** routes `make install-service` to the right init system: Linux gets systemd, macOS gets launchd. Clear messaging before any `sudo` operation.

**Generic checks runner**: `make check` reads a `.checks` file (gitignored, user-created) and runs each line. The Makefile target is entirely generic. What you check is up to you.

### Why this matters

- **Consistency**: Every contributor runs the exact same build flags, test flags, and lint checks. No "works on my machine" drift.
- **Safety gate**: `make push` makes it impossible to push code that fails checks. The `.checks` file is yours to customize, so you define what "safe to push" means for your setup.
- **Cross-platform install**: One command installs the binary and sets up the right service manager (systemd on Linux, launchd on macOS). No manual service file copying or editing.
- **Version injection**: Every binary knows its exact version, commit hash, and build date. `peerup version` always tells you precisely what you're running.

## Pre-I-b: Encrypted invite/join handshake

![Encrypted Invite/Join Handshake](/images/blog/pre-batch-i-pake-handshake.svg)

The invite/join pairing now uses an encrypted handshake. Before, the invite token was sent as cleartext hex over the stream. A malicious relay operator could observe it. Now the relay sees only opaque encrypted bytes.

### How it works

```
1. Joiner -> Inviter:  [version 0x02] [32-byte X25519 public key]
2. Inviter -> Joiner:  [32-byte X25519 public key]
   -- Both derive: key = HKDF-SHA256(DH_shared || token, "peerup-invite-v2")
3. Joiner -> Inviter:  [AEAD encrypted: joiner name]
4. Inviter -> Joiner:  [AEAD encrypted: "OK" + inviter name]
```

Both sides compute an ephemeral X25519 Diffie-Hellman shared secret, mix it with the invite token via HKDF, and derive an XChaCha20-Poly1305 AEAD key. If the tokens don't match, HKDF produces different keys and AEAD decryption fails silently. The inviter logs "invalid invite code" with no protocol details leaked.

### What the relay sees

| Before | After |
|--------|-------|
| Token hex in cleartext | Ephemeral public keys + encrypted bytes |
| Peer names in cleartext | Encrypted bytes |
| Could replay token | Can't reconstruct AEAD key |

### Zero new dependencies

`crypto/ecdh` (Go stdlib), `golang.org/x/crypto/hkdf`, and `golang.org/x/crypto/chacha20poly1305` were all already in the dependency tree via libp2p. Binary size: unchanged.

### Backward compatibility

Invite code version byte determines the protocol: 0x01 = legacy cleartext (still supported), 0x02 = encrypted handshake. The inviter's stream handler auto-detects based on the first byte. Future versions (0x03+) are rejected with a "please upgrade peerup" message.

### v2 invite codes carry the namespace

v2 invite codes include a namespace field. When you join a private network, the joiner auto-inherits the inviter's DHT namespace in their config. No extra flags needed.

### Why this matters

- **Relay resistance**: Even if you use a relay you don't fully trust, your invite token and peer names are never visible to it. The relay forwards encrypted bytes it cannot decrypt.
- **Silent failure on wrong token**: A brute-force attacker gets no feedback. Wrong token = wrong HKDF key = AEAD decryption fails = connection closes. No error messages, no timing leaks.
- **Forward secrecy**: Ephemeral X25519 keys are generated per handshake. Compromising one session reveals nothing about past or future pairings.
- **Zero cost**: All cryptographic primitives were already in the dependency tree via libp2p. No new imports, no binary size increase, no new attack surface from third-party crypto libraries.

## Pre-I-c: Private DHT networks

![Private DHT Network Isolation](/images/blog/pre-batch-i-private-dht.svg)

Nodes can now form completely isolated peer groups by setting a network namespace:

```bash
peerup init --network "my-crew"
```

This produces a config with:

```yaml
discovery:
  rendezvous: "peerup-default-network"
  network: "my-crew"
```

### Protocol-level isolation

The DHT protocol prefix becomes `/peerup/my-crew/kad/1.0.0`. Nodes on different namespaces speak entirely different protocols. They don't just filter each other out. They literally cannot discover each other. This is a protocol-level guarantee, not an application-layer filter.

| Config | DHT Protocol Prefix |
|--------|-------------------|
| `network: ""` (default) | `/peerup/kad/1.0.0` |
| `network: "my-crew"` | `/peerup/my-crew/kad/1.0.0` |
| `network: "family"` | `/peerup/family/kad/1.0.0` |

### Status display

```bash
$ peerup status
Version:  v0.x.x
Peer ID:  12D3KooW...
Network:  my-crew
Config:   ~/.config/peerup/config.yaml
...
```

### Backward compatibility

Empty or missing `network` field = global DHT (`/peerup/kad/1.0.0`). Zero breaking changes for existing deployments.

### Why this matters

- **True isolation**: This isn't an application-layer filter that hides peers from listings. Nodes on different namespaces speak literally different DHT protocols. They can't discover each other even if they try.
- **Simple setup**: One flag at init time (`--network "my-crew"`) and you're in your own private network. No additional infrastructure, no separate relay, no configuration files to sync.
- **Automatic propagation**: v2 invite codes carry the namespace. When someone joins your private network, their config is set automatically. No manual coordination needed.
- **Scalable group management**: Families, teams, organizations can each have their own namespace. Share a relay for transport, but keep peer discovery completely separate.

## Impact summary

| Metric | Value |
|--------|-------|
| New files | 5 (Makefile, pake.go, pake_test.go, network.go, network_test.go) |
| Modified files | 19 |
| New tests | 30+ (19 PAKE + 11 invite code + namespace validation + DHT prefix) |
| New ADRs | 4 (Ia01, Ia02, Ib01, Ib02, Ic01) |
| Binary size | Unchanged (28MB) |
| New dependencies | 0 (golang.org/x/crypto promoted from indirect to direct) |

---

*These three items are the Pre-Batch I foundation. See the [engineering journal](/docs/engineering-journal/) for the full decision trail on each item.*
