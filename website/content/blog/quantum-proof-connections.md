---
title: "Quantum-Proof Connections"
date: 2026-05-10
description: "Shurli now protects every connection against future quantum computers. Two independent layers of post-quantum cryptography, zero configuration required."
image: /images/blog/quantum-proof-hero.png
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Quantum-proof connections: a shield splitting into two layers, protecting data flowing between nodes](/images/blog/quantum-proof-hero.svg)

## The Quantum Threat Is Accelerating

The waters are calm right now. Quantum computers cannot break today's encryption yet. But the timeline is shrinking fast, and every major institution is sounding the alarm.

In December 2024, Google unveiled [Willow](https://blog.google/innovation-and-ai/technology/research/google-willow-quantum-chip/), a 105-qubit quantum chip that achieved exponential error correction for the first time. In October 2025, Google demonstrated [Quantum Echoes](https://www.programming-helper.com/tech/google-quantum-advantage-willow-chip-breakthrough-2026) - the first verifiable quantum advantage on hardware, solving problems 13,000 times faster than the world's fastest supercomputers.

Then the real wake-up call. Between May 2025 and March 2026, [three research papers](https://thequantuminsider.com/2026/03/31/q-day-just-got-closer-three-papers-in-three-months-are-rewriting-the-quantum-threat-timeline/) rewrote the threat timeline. The number of quantum bits needed to break RSA-2048 encryption (the standard protecting most internet banking, email, and digital certificates) dropped from 20 million to fewer than one million - and potentially as low as 100,000 using newer architectures. Google researcher Craig Gidney showed a quantum computer with fewer than one million noisy physical qubits could break RSA-2048 [in less than a week](https://cyberscoop.com/google-moves-post-quantum-encryption-timeline-to-2029/).

In February 2026, [Google publicly called on governments and industry](https://blog.google/innovation-and-ai/technology/safety-security/cryptography-migration-timeline/) to "prepare now" and [moved their own migration deadline to 2029](https://thequantuminsider.com/2026/03/25/google-shortens-timeline-for-quantum-safe-encryption-transition/). The NSA's [CNSA 2.0 directive](https://www.nsa.gov/Press-Room/News-Highlights/Article/Article/3148990/nsa-releases-future-quantum-resistant-qr-algorithm-requirements-for-national-se/) requires all U.S. national security systems to be quantum-resistant by 2035, with new equipment compliant by 2027 and legacy systems phased out by 2030. The [World Economic Forum](https://www.weforum.org/stories/2026/02/quantum-security-question-leaders-cannot-ignore/) declared quantum security a question "leaders cannot ignore." 2026 has been called the ["Year of Quantum Security"](https://thequantuminsider.com/2026/04/28/why-2026-matters-quantum-security/) by an industry coalition featuring senior officials from the FBI, [NIST](https://www.nist.gov/news-events/news/2024/08/nist-releases-first-3-finalized-post-quantum-encryption-standards), and CISA.

The protection Shurli uses is not experimental. In August 2024, NIST finalized three post-quantum cryptography standards ([FIPS 203, 204, 205](https://csrc.nist.gov/news/2024/postquantum-cryptography-fips-approved)) after an 8-year standardization process. These algorithms are designed to be hard for both classical AND quantum computers to break. Shurli implements [ML-KEM-768](https://grokipedia.com/page/Kyber) (FIPS 203) for key exchange and [ML-DSA-65](https://csrc.nist.gov/pubs/fips/204/final) (FIPS 204) for signing - the same algorithms Google, Cloudflare, and the NSA are adopting.

### The Hidden Danger: Harvest Now, Decrypt Later

Right now, someone could be recording your encrypted traffic. Not reading it - just saving it. Waiting. This is called ["harvest now, decrypt later"](https://www.paloaltonetworks.com/cyberpedia/harvest-now-decrypt-later-hndl) (HNDL). When quantum computers arrive that can break today's encryption, every saved conversation, every transferred file, every authentication exchange becomes readable. Retroactively.

This is not theoretical. [Intelligence agencies and sophisticated attackers are already collecting encrypted traffic](https://www.isc2.org/Insights/2026/05/harvest-now-decrypt-later) for future decryption. The migration window is [5 to 10 years](https://thequantuminsider.com/2026/05/01/harvest-now-decrypt-later-why-should-you-care/), and the threat could arrive within 10 to 15 years. That means the time to protect your infrastructure is now - while the waters are still calm.

## The Story

A new device joins your network. Maybe you added it. Maybe an AI agent provisioned it automatically. Either way, the two devices find each other and start talking.

Before any data flows, they need to agree on a secret - a shared key that scrambles everything between them so nobody else can read it. Think of it like two strangers meeting and agreeing on a private language only they understand, right there on the spot. In networking, this initial agreement is called a "handshake."

Today's handshakes use math problems that are extremely hard for regular computers to crack. But quantum computers solve those specific problems easily. So Shurli's handshake uses two completely different kinds of math at the same time: the proven kind that works against today's threats, and a new kind built specifically to resist quantum attacks. Both must succeed. An attacker would need to break two unrelated mathematical problems to read a single connection.

Behind the scenes, every connection between your devices is now quantum-proof. Devices that talk directly to each other, devices that connect through an intermediary - all of them. The protection works regardless of how the devices found each other or what route the data takes.

Nobody had to turn it on. Nobody had to choose a setting. The infrastructure just does it.

## Two Layers, Zero Gaps

![Dual-layer PQ architecture: QUIC connections protected at TLS layer, TCP/WebSocket connections protected by PQ Noise transport](/images/blog/quantum-proof-dual-layer.svg)

Shurli provides post-quantum protection at two independent levels:

**Transport layer (QUIC)**: Go's TLS 1.3 automatically negotiates X25519MLKEM768 - a hybrid scheme combining classical [Diffie-Hellman](https://grokipedia.com/page/Diffie%E2%80%93Hellman_key_exchange) with [ML-KEM](https://grokipedia.com/page/Kyber) (NIST FIPS 203). Direct connections between nodes get this for free.

**Application layer (PQ Noise)**: A custom security protocol for TCP and WebSocket connections - the paths used by relay circuits and fallback transports. This uses a 5-message hybrid handshake: outer X25519 (proven classical) wrapping inner ML-KEM-768 (quantum-resistant). Both layers must succeed independently.

Why two layers? Because connections take different paths. A direct connection uses QUIC and gets PQ at the transport layer. A connection through a relay uses TCP and needs PQ at the application layer. Without both layers, some connections would be unprotected.

## How the Handshake Works

![PQ Noise handshake sequence: 5 messages exchanged, outer X25519 and inner ML-KEM-768 running in parallel](/images/blog/quantum-proof-handshake.svg)

The PQ Noise handshake follows the [Noise protocol framework](https://noiseprotocol.org/) pattern with a post-quantum extension:

1. **Outer layer**: Classical X25519 [Diffie-Hellman](https://grokipedia.com/page/Diffie%E2%80%93Hellman_key_exchange) key exchange (the XX pattern). Fast, proven, decades of cryptanalysis.
2. **Inner layer**: ML-KEM-768 key encapsulation. Lattice-based, resistant to Shor's algorithm.
3. **Identity binding**: The initiator signs the handshake hash with their [Ed25519](https://grokipedia.com/page/EdDSA) identity key, proving who they are to the responder.

An attacker needs to break both X25519 AND ML-KEM-768 to compromise a single connection. These rely on fundamentally different mathematical hardness assumptions - discrete logarithms and lattice problems.

## Policy Control

![Three PQC policy modes: mandatory (shield locked), opportunistic (shield with fallback arrow), disabled (shield greyed out)](/images/blog/quantum-proof-policy.svg)

Not every deployment needs the same level of enforcement:

- **Opportunistic** (default): Prefer post-quantum, fall back gracefully if the remote peer does not support it. Zero breakage for mixed networks.
- **Mandatory**: Reject any TCP/WebSocket connection that fails to negotiate PQ Noise. For networks where every node is upgraded.
- **Disabled**: Classical only. For constrained environments or testing.

Per-peer overrides let you require PQ from specific high-value peers while allowing classical connections from others.

## Visibility

The daemon reports PQC state in real time:

```
$ shurli status
...
PQC Status:
  Policy:   opportunistic
  QUIC PQ:  verified (X25519MLKEM768)
  Noise PQ: verified (/pq-noise/1)
  Connections:
    12D3KooW...      quic-v1 X25519MLKEM768 [PQ]
    12D3KooW...      tcp     /pq-noise/1    [PQ]
```

Every layer reported independently. Every connection shows its security state. If post-quantum key exchange is working, you know immediately. No guessing. No hidden downgrades.

When a relay circuit uses classical Noise in opportunistic mode, the daemon logs a warning. You always know what is protected and what is not.

## What Comes Next

Phase 11 delivers post-quantum key exchange. The next step (Phase 13) adds post-quantum identity - [ML-DSA-65](https://csrc.nist.gov/pubs/fips/204/final) signatures proving peer identity with quantum-resistant math. No other P2P project has shipped PQ peer identity authentication. Shurli will be the first.

The building blocks are ready. [go-clatter](https://github.com/shurlinet/go-clatter) v0.2.0 already includes ML-DSA-65 signing with FIPS 204 compliance. The handshake payload has a reserved field for the PQ attestation. When Phase 13 lands, every connection will have quantum-resistant encryption AND quantum-resistant identity proof.

---

*Built with [Claude Code](https://claude.com/claude-code) by Anthropic using intent-based development. See [How We Build Shurli](/blog/how-we-build-shurli/) for the philosophy behind this approach.*
