---
title: "Security & Features"
weight: 4
description: "How pairing, verification, reachability grading, encrypted invites, and private DHT networks work."
---
<!-- Auto-synced from docs/faq/security-and-features.md by sync-docs - do not edit directly -->


## How does the encrypted invite handshake work?

The invite/join flow uses a PAKE-inspired encrypted handshake (shipped in Pre-Batch I-b). The invite code itself is a shared secret that never travels over the wire.

1. Both sides generate ephemeral X25519 key pairs
2. They exchange public keys over the relay-mediated stream
3. Each side derives a shared AEAD key: HKDF-SHA256(DH-shared-secret, invite-code-as-salt)
4. All subsequent messages (peer names, peer IDs) are encrypted with XChaCha20-Poly1305
5. Key confirmation MACs verify both sides derived the same key

The relay sees only ephemeral public keys and encrypted bytes. It cannot learn the invite code, peer names, or any protocol details. If the invite codes don't match, AEAD decryption fails silently with no information leaked.

The original v1 cleartext invite protocol has been deleted. There is zero downgrade surface.

---

## How does relay pairing work?

Relay pairing is the streamlined way to onboard multiple peers to a relay. Instead of SSH-ing into the relay and manually exchanging peer IDs, the relay admin generates pairing codes and shares them.

**Relay admin:**
```bash
peerup relay pair --count 3 --ttl 1h
# Generates 3 pairing codes, each valid for 1 hour
```

**Each person joining:**
```bash
peerup join <pairing-code> --name laptop
# Connects to relay, discovers other peers, mutually authorizes everyone
```

The flow has 8 steps: token validation, enrollment mode (probationary peer admission), peer discovery, mutual authorization, name conflict resolution (automatic -2, -3 suffixes), SAS fingerprint display, daemon auto-start, and expiry management.

Security properties:
- Pairing codes are hashed (SHA-256) on the relay. The relay stores the hash, not the code.
- Max 3 failed attempts per code group before all codes in the group burn.
- Probationary peers (max 10, 15s timeout) are evicted if pairing doesn't complete.
- All failure modes return a uniform "pairing failed" error (no oracle attacks).

**Comparison to invite/join**: Invite/join (`peerup invite` + `peerup join`) pairs exactly 2 peers directly with PAKE-encrypted key exchange. Relay pairing (`peerup relay pair`) onboards multiple peers to a relay-managed group. Use invite/join for peer-to-peer, relay pairing for groups.

---

## How does peer verification work?

After pairing, peers show an `[UNVERIFIED]` badge in ping, traceroute, and status output. This means the cryptographic identity hasn't been verified out-of-band.

**To verify a peer:**
```bash
peerup verify home
# Shows a 4-emoji + 6-digit numeric fingerprint
# Both sides must see the same fingerprint
# Confirm via a separate channel (phone call, in person, messaging app)
```

The fingerprint is computed from a sorted hash of both peer IDs (OMEMO-style). Once confirmed, the peer's `authorized_keys` entry gets a `verified=sha256:<prefix>` attribute and the badge changes to `[VERIFIED]`.

This is the same trust model as Signal's safety numbers or WhatsApp's security code. The emoji format makes it easy to compare verbally ("moon rocket house cat" is faster and less error-prone than reading hex digits).

**When verification matters**: If you paired via a relay code and want to confirm no MITM occurred during the relay-mediated exchange. If you paired in person or via a trusted channel, the pairing itself provides the verification.

---

## What are reachability grades?

The daemon status API (`peerup daemon status`) shows a reachability grade from A to F:

| Grade | Meaning | What it means for connections |
|-------|---------|------------------------------|
| **A** | Public IPv6 | Direct connections to anyone, no relay needed |
| **B** | Public IPv4 or hole-punchable NAT | Direct connections likely (full-cone or address-restricted NAT) |
| **C** | Port-restricted NAT | Hole-punching possible but less reliable |
| **D** | Symmetric NAT / CGNAT | Relay required for most connections |
| **F** | Offline or no connectivity | Cannot connect |

The grade is computed from interface discovery (which IPs are available) and STUN probe results (what type of NAT is in front of each interface). It updates automatically when the network changes (WiFi to cellular, cable plugged in, etc.).

The grade helps you understand *why* a connection goes through the relay instead of direct. A peer with grade D behind CGNAT connecting to a peer with grade A on public IPv6 will likely go direct via the grade-A peer's address. Two grade-D peers will relay.

---

## How do private DHT networks work?

By default, all peer-up nodes share one DHT with protocol prefix `/peerup/kad/1.0.0`. With private DHT networks, you set a namespace and your nodes form a completely separate DHT:

```yaml
# config.yaml
discovery:
  network: "my-crew"
```

This changes the DHT prefix to `/peerup/my-crew/kad/1.0.0`. Nodes with different namespaces literally speak different protocols and cannot discover each other. It's not a firewall or ACL - it's protocol-level isolation.

Use cases: gaming groups, family networks, organization-internal deployments. Each private network needs its own relay (or at least one bootstrap peer).

When using invite codes (v2 format), the inviter's namespace is encoded in the code. The joiner automatically inherits the same namespace.

---

**Last Updated**: 2026-02-24
