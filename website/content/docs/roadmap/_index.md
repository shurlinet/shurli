---
title: "Roadmap"
weight: 15
description: "Multi-phase development roadmap for Shurli. From NAT traversal tool to decentralized P2P network infrastructure."
---

This document outlines the multi-phase evolution of Shurli from a simple NAT traversal tool to a comprehensive decentralized P2P network infrastructure.

## Philosophy

> **Build for 1-5 years. Make it adaptable. Don't predict 2074.**

- **Modular architecture** - Easy to add/swap components
- **Library-first** - Core logic reusable in other projects
- **Progressive enhancement** - Each phase adds value independently
- **No hard dependencies** - Works without optional features (naming, blockchain, etc.)
- **Local-first** - Offline-capable, no central services required
- **Self-sovereign** - No accounts, no telemetry, no vendor dependency
- **Automation-friendly** - Daemon API, headless onboarding, multi-language SDKs

---

## Batch Overview

| Batch | Focus | What It Does | Status |
|-------|-------|--------------|--------|
| Phase&nbsp;1 | **Configuration** | YAML config, sample files | Done |
| Phase&nbsp;2 | **Authentication** | ConnectionGater, authorized_keys | Done |
| Phase&nbsp;3 | **keytool&nbsp;CLI** | Key management (now shurli subcommands) | Done |
| Phase&nbsp;4A | **Core&nbsp;Library** | `pkg/sdk/`, single binary, init wizard | Done |
| Phase&nbsp;4B | **Onboarding** | invite/join, QR codes, auth + relay CLI | Done |
| A | **Reliability** | Reconnection with backoff, dial timeout, DHT in proxy | Done |
| B | **Code&nbsp;Quality** | Proxy dedup, `log/slog`, sentinel errors, version embedding | Done |
| C | **Self-Healing** | Config archive/rollback, commit-confirmed, watchdog | Done |
| D | **libp2p** | AutoNAT v2, QUIC preferred, Identify UserAgent | Done |
| E | **Capabilities** | `shurli status`, `/healthz`, headless invite/join | Done |
| F | **Daemon** | Unix socket API, cookie auth, ping/traceroute/resolve | Done |
| G | **Testing** | 80.3% coverage, Docker tests, relay merge, website | Done |
| H | **Observability** | Prometheus metrics, audit logging, Grafana dashboard | Done |
| Pre-I-a | **Build&nbsp;Tooling** | Makefile, service install (systemd/launchd) | Done |
| Pre-I-b | **PAKE&nbsp;Invite** | Encrypted handshake, token-bound AEAD | Done |
| Pre-I-c | **Private&nbsp;DHT** | Namespace isolation for peer groups | Done |
| I | **Adaptive&nbsp;Path** | Interface discovery, dial racing, STUN, every-peer-relay | Done |
| Post-I-1 | **Relay&nbsp;Pairing** | Pairing codes, SAS verification, reachability grades | Done |
| Post-I-2 | **Peer&nbsp;Intro** | HMAC group commitment, relay-pushed introductions | Done |
| Pre-5 | **Hardening** | 8 cross-network fixes, 5 NoDaemon test fixes | Done |
| **Phase&nbsp;5** | **Network&nbsp;Intelligence** | mDNS, PeerManager, NetIntel presence | **Done** |
| 5-K | mDNS | Native DNS-SD LAN discovery (dns_sd.h CGo) | Done |
| 5-L | PeerManager | Background reconnection, authorized peer lifecycle | Done |
| 5-M | NetIntel | Presence announcements, gossip forwarding | Done |
| **Phase&nbsp;6** | **ACL&nbsp;+&nbsp;Relay&nbsp;Security** | Macaroon tokens, sealed vault, async invites, roles | **Done** |
| **Phase&nbsp;7** | **ZKP&nbsp;Privacy** | Anonymous auth, Poseidon2 Merkle tree, range proofs | **Done** |
| **Phase&nbsp;8** | **Identity&nbsp;Security** | BIP39 seed, encrypted keys, session tokens, remote admin | **Done** |
| **Phase&nbsp;8B** | **Per-Peer&nbsp;Data&nbsp;Grants** | Macaroon grants, token delivery, delegation, notifications, audit log | **Done** |
| **Phase&nbsp;8C** | **ACL-to-Macaroon** | M1 done (Phase 8B). M2-M5 moved to Phase 16 | Partial |
| **Phase&nbsp;8D** | **Module&nbsp;Slots** | Swappable system algorithms. Moved to Phase 21 | Planned |
| 9A | **Interfaces&nbsp;&&nbsp;Library** | Core interfaces, extension points, library consolidation | **Done** |
| 9B | **File&nbsp;Transfer** | Chunked P2P transfer, erasure coding, multi-source download | **Done** |
| Post-9B | **Plugin&nbsp;Architecture** | Plugin framework, file transfer extraction, supervisor, security hardening, physical retest | **Done** |
| **v0.3.0** | **Release** (2026-03-26) | 148 commits. Plugins, grants, receipts, relay-first onboarding, bandwidth budgets | **Done** |
| **v0.4.0** | **Release** (2026-05-01) | Streaming protocol, multi-peer, Tail Slayer hedging, LAN 111 MB/s send | **Done** |
| **go-clatter** | **v0.1.0** | PQ Noise framework: 5 handshake modes, ML-KEM-768, 233+ tests, 408 interop vectors | **Done** |
| **go-clatter** | **v0.2.0** | ML-DSA-65 signing module (FIPS 204), 29 tests, secret zeroing | **Done** |
| **Phase&nbsp;11** | **PQC&nbsp;Integration** | `/pq-noise/1` transport, ML-DSA-65 signing | 11A+11B Done |
| **Phase&nbsp;12** | **Seed&nbsp;&&nbsp;Recovery** | go-bip85, SLIP39 fork+harden, SeedSource interface, SHRL redesign | Next |
| **Phase&nbsp;13** | **PQ&nbsp;Identity&nbsp;Attestation** | ML-DSA-65 handshake, gater enforcement, offline master key, signing agent | Planned |
| **Phase&nbsp;14** | **Topic-Based&nbsp;Pub/Sub** | GossipSub integration via NetIntel Layer 3 slot | Planned |
| **Phase&nbsp;15** | **Naming&nbsp;Standards** | 5 identity layers, DID, petnames, resolution pipeline, plugin resolvers | Planned |
| **Phase&nbsp;16** | **ACL-to-Macaroon** | M2-M5 migration (promoted from 8C) | Planned |
| **Phase&nbsp;17** | **Agent&nbsp;Foundation** | MCP service templates, identity mgmt APIs, per-identity permissions | Planned |
| **Phase&nbsp;18** | **Agent&nbsp;Task&nbsp;Protocol** | A2A plugin, Agent Cards, task FSM, MCP bridge, agent auth | Planned |
| **Phase&nbsp;19** | **Discovery&nbsp;+&nbsp;Federation** | Capability discovery, relay federation protocol | Planned |
| **Phase&nbsp;20** | **Payments** | Machine + agent payment protocols (HTTP 402) | Planned |
| **Phase&nbsp;21** | **Reputation** | Module slots, connected identity trust | Deferred |
| **Phase&nbsp;22** | **Apple&nbsp;App** | macOS/iOS/iPadOS/visionOS (separate repo) | In Progress |
| **Phase&nbsp;23** | **Gateway&nbsp;+&nbsp;DNS** | Desktop gateway, private DNS on relay | Deferred |
| 9C | **Discovery&nbsp;&&nbsp;Plugins** | Service discovery, service templates, Wake-on-LAN | Planned |
| 9D | **Python&nbsp;SDK&nbsp;&&nbsp;Docs** | Python SDK (separate repo), SDK documentation | Planned |
| 9E | **Swift&nbsp;SDK** | Swift SDK for Apple platforms (separate repo, SPM) | Planned |
| 9F | **Layer&nbsp;2&nbsp;WASM** | Third-party plugins in any language via wazero sandbox | Planned |
| 9G | **Layer&nbsp;3&nbsp;AI** | AI-driven plugin generation from Skills.md specs | Future |

---

## Timeline Summary

<img src="/images/docs/roadmap-timeline.svg" alt="Development timeline showing completed phases (1-4C) and planned phases (5-12+)" loading="lazy" />

| Phase | Status |
|-------|--------|
| Phase 1: Configuration | Complete |
| Phase 2: Authentication | Complete |
| Phase 3: keytool CLI | Complete (superseded) |
| Phase 4A-4B: Core Library + Onboarding | Complete |
| Phase 4C: Core Hardening & Security | Complete (Batches A-I, Post-I-1/2, Pre-Phase 5) |
| Phase 5: Network Intelligence | **Complete** (mDNS, PeerManager, Presence) |
| Phase 6: ACL + Relay Security | **Complete** (Macaroons, vault, 2FA) |
| Phase 7: ZKP Privacy Layer | **Complete** (gnark PLONK + KZG) |
| Phase 8: Identity Security + Remote Admin | **Complete** (BIP39, encrypted identity, P2P admin) |
| Phase 8B: Per-Peer Data Grants | **Complete** (macaroon grants, delegation, audit) |
| Grant Receipt Protocol | **Complete** |
| Phase 9A-9B: Plugins + File Transfer | **Complete** |
| Plugin Architecture Shift | **Complete** (framework, extraction, supervisor, 43-vector threat analysis) |
| E14: Relay-First Onboarding | **Complete** (3 ISP physical test) |
| FT-Y: Transfer Speed Optimization | **Complete** (streaming protocol, multi-peer, Tail Slayer, 22 bug fixes) |
| **v0.3.0 Release** (2026-03-26) | **148 commits merged** |
| **v0.4.0 Release** (2026-05-01) | **Streaming protocol, hedged racing, LAN 111 MB/s send** |
| **go-clatter v0.1.0** (PQ Noise) | **5 handshake modes, 233+ tests, 408 interop vectors** |
| **go-clatter v0.2.0** (ML-DSA-65) | **FIPS 204 signing, 29 tests, secret zeroing** |
| Phase 10: Distribution | Partial (install script, archives done. Homebrew/APT planned) |
| **Phase 11: PQC Integration** | 11A+11B DONE, 11C pending |
| **Phase 12: Seed & Recovery** | Next |
| **Phase 13: PQ Identity Attestation** | Planned |
| **Phase 14: Topic-Based Pub/Sub** | Planned |
| **Phase 15: Naming Standards (SNR)** | Planned |
| **Phase 16: ACL-to-Macaroon (M2-M5)** | Planned |
| **Phase 17: Agent Foundation (MCP)** | Planned |
| **Phase 18: Agent Task Protocol (A2A)** | Planned |
| **Phase 19: Agent Discovery + Federation** | Planned |
| **Phase 20: Payments** | Planned |
| **Phase 21: Reputation / Module Slots** | Deferred |
| **Phase 22: Apple Multiplatform App** | In Progress (separate repo) |
| **Phase 23: Desktop Gateway + Private DNS** | Deferred |
| Phase 9C-9G: SDKs, WASM, AI Plugins | Planned / Future |
| Phase 24+: Ecosystem | Conceptual |

**Priority logic**: Harden core (done) -> network intelligence (done) -> ACL + relay security (done) -> ZKP (done) -> identity + remote admin (done) -> plugins + file transfer (done) -> speed optimization (done) -> PQC (11A+11B done) -> **seed infrastructure** -> **PQ identity attestation** -> pub/sub -> naming standards -> macaroon migration -> agent foundation -> agent protocol -> discovery + federation -> payments -> reputation -> mobile -> gateway.

**Repository strategy**: Non-Go SDKs and consumer apps live in separate GitHub repos. The Go SDK (`pkg/sdk`) stays in this repo.

---

## Contributing

This roadmap is a living document. Phases may be reordered, combined, or adjusted based on:
- User feedback and demand
- Technical challenges discovered during implementation
- Emerging technologies (AI, quantum, blockchain alternatives)
- Community contributions

**Adaptability over perfection.** We build for the next 1-5 years, not 50.

---

*Last updated: 2026-05-05. v0.4.0 released. go-clatter v0.2.0 released. Phase 11A+11B done (PQ Noise transport + ML-DSA-65 signing). Next: Phase 12 Seed & Recovery Infrastructure.*
