---
title: Trust & Security
weight: 9
---

## Security Program

peer-up is infrastructure software that people depend on for remote access to their machines. A security compromise doesn't just leak data — it could give an attacker SSH access to your home server. We take this seriously.

This page documents our security posture, threat model, vulnerability reporting process, and response commitments. We believe transparency is the strongest security signal we can provide.

## Current Security Controls

peer-up ships with these security defaults:

| Control | Description |
|---------|-------------|
| **ConnectionGater** | Peer ID allowlist (`authorized_keys`). Unauthorized peers are rejected at the network layer before any protocol runs. |
| **Ed25519 identities** | Cryptographic peer identity. Keys generated locally, never transmitted. |
| **Key file permissions** | Identity keys require `0600` — loader refuses keys with wider permissions. |
| **Config file permissions** | Config files written with `0600`. |
| **Input validation** | Service names: DNS-label format enforced. Relay addresses: parsed as multiaddr before writing. Comments: newline injection sanitized. |
| **Stream read limits** | Invite/join streams capped at 512 bytes to prevent OOM. |
| **Relay resource limits** | Circuit relay v2 with configurable `WithResources()` — session duration, data caps, per-peer limits. |
| **Private DHT** | Kademlia DHT uses `/peerup/kad/1.0.0` protocol prefix — isolated from public IPFS routing. |
| **Cookie auth** | Daemon API uses 32-byte random hex cookie, `0600` permissions, rotated every restart. |
| **Config rollback** | Commit-confirmed pattern auto-reverts bad configs on remote nodes. |

## Threat Model

peer-up's threat surface includes:

### Relay Server (Public-Facing VPS)

The relay is the most exposed component — it's a public-facing server that accepts connections from the internet.

| Threat | Mitigation | Status |
|--------|-----------|--------|
| **Resource exhaustion** | Circuit relay v2 resource limits (session duration, data caps) | Implemented |
| **Log injection** | Structured logging via `log/slog` — no string interpolation in log messages | Implemented |
| **YAML injection** | Peer names sanitized before writing to config | Implemented |
| **Path traversal** | Config paths resolved and validated, no user-controlled path components | Implemented |
| **Peer ID spoofing** | ConnectionGater validates against `authorized_keys` at network layer | Implemented |
| **DDoS amplification** | QUIC source address verification (planned) | Planned |
| **OS-level flood protection** | iptables/nftables rate limiting in `setup.sh` (planned) | Planned |
| **Per-service access control** | Per-service `authorized_keys` override (planned) | Planned |

### Invite/Join Flow

| Threat | Mitigation | Status |
|--------|-----------|--------|
| **Invite code interception** | Codes are short-lived (default 10 minutes) and single-use | Implemented |
| **Malformed invite codes** | Strict multihash length validation, base32 re-encode comparison | Implemented |
| **Stream flooding** | Read limit of 512 bytes on invite/join streams | Implemented |
| **Man-in-the-middle** | Relay mediates handshake but never sees private keys; mutual authorization | Implemented |

### Daemon API

| Threat | Mitigation | Status |
|--------|-----------|--------|
| **Unauthorized API access** | Cookie-based auth, Unix socket (local-only), `0600` permissions | Implemented |
| **Stale socket hijacking** | Dial-test detection, no PID files | Implemented |
| **Auth bypass** | Every endpoint validates cookie before processing | Implemented |

### Supply Chain

| Threat | Mitigation | Status |
|--------|-----------|--------|
| **Dependency vulnerabilities** | CVE monitoring, `go mod tidy`, CI pinned to commit SHAs | Active |
| **CI compromise** | GitHub Actions pinned to SHAs (not tags) | Implemented |
| **Binary tampering** | Ed25519-signed checksums in release manifest (planned) | Planned |
| **Cosign / Sigstore signing** | SLSA provenance for Go binaries (planned) | Planned |

## Vulnerability Reporting

If you find a security vulnerability in peer-up, please report it responsibly:

**Email**: security@peerup.dev *(not yet active — use GitHub Security Advisories until domain is configured)*

**GitHub Security Advisories**: [Report a vulnerability](https://github.com/satindergrewal/peer-up/security/advisories/new)

### What to Include

- **Title**: Brief description of the vulnerability
- **Severity**: Your assessment (Critical / High / Medium / Low)
- **Affected component**: Which part of peer-up is affected
- **Reproduction steps**: How to trigger the vulnerability
- **Impact**: What an attacker could achieve
- **Environment**: OS, Go version, peer-up version

### Response Commitments

| Severity | First Response | Triage | Fix Target |
|----------|---------------|--------|------------|
| **Critical** (RCE, auth bypass, relay takeover) | 48 hours | 5 days | 14 days |
| **High** (significant single-user impact) | 5 days | 14 days | 30 days |
| **Medium** (limited impact, defense-in-depth) | 14 days | 30 days | 90 days |
| **Low** (hardening, best practice) | 30 days | 60 days | Best effort |

These are targets, not guarantees — peer-up is maintained by a small team. But we take every report seriously and will communicate transparently about our progress.

## Security Audit History

| Date | Scope | Findings | Resolution |
|------|-------|----------|------------|
| 2026-02-19 | Full post-phase audit (Phase 4C) | CVE-2026-26014 (pion/dtls), CI action tags, 10 hardening items | All resolved (commit `83d02d3`) |

## Contributing to Security

We welcome security contributions. The threat model above is a living document — if you see a gap, please:

1. **Open a GitHub issue** for non-sensitive improvements
2. **Use Security Advisories** for actual vulnerabilities
3. **Submit PRs** for hardening improvements

The [Engineering Journal](/docs/engineering-journal/) documents the reasoning behind every security design decision (28 ADRs and counting).

## Trust Model Philosophy

peer-up's security model is deliberately simple:

> **An `authorized_keys` file decides who connects. You control the file. That's it.**

No accounts. No tokens. No OAuth. No SAML. No OIDC. No JWTs. No API keys. No central authority.

This is the same model that has secured SSH for 30 years. It's not perfect — but it's well-understood, auditable, and entirely under your control. When something goes wrong, there's exactly one place to look: the `authorized_keys` file on your machine.

Future phases will add optional layers (per-service ACLs, pluggable auth backends), but the base model will always be this simple.
