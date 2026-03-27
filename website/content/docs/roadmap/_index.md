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
| **Phase&nbsp;8C** | **ACL-to-Macaroon** | Replace all 5 ACL layers with capability tokens (M1 done, M2-M5 planned) | Partial |
| **Phase&nbsp;8D** | **Module&nbsp;Slots** | Swappable system algorithms (reputation, auth, storage) | Planned |
| 9A | **Interfaces&nbsp;&&nbsp;Library** | Core interfaces, extension points, library consolidation | **Done** |
| 9B | **File&nbsp;Transfer** | Chunked P2P transfer, erasure coding, multi-source download | **Done** |
| Post-9B | **Plugin&nbsp;Architecture** | Plugin framework, file transfer extraction, supervisor, security hardening, physical retest | **Done** |
| 9C | **Discovery&nbsp;&&nbsp;Plugins** | Service discovery, service templates, Wake-on-LAN | Planned |
| 9D | **Python&nbsp;SDK&nbsp;&&nbsp;Docs** | Python SDK (separate repo), SDK documentation | Planned |
| 9E | **Swift&nbsp;SDK** | Swift SDK for Apple platforms (separate repo, SPM) | Planned |
| 9F | **Layer&nbsp;2&nbsp;WASM** | Third-party plugins in any language via wazero sandbox | Planned |
| 9G | **Layer&nbsp;3&nbsp;AI** | AI-driven plugin generation from Skills.md specs | Future |

---

## Timeline Summary

<img src="/images/docs/roadmap-timeline.svg" alt="Development timeline showing completed phases (1-4C) and planned phases (5-12+)" loading="lazy" />

| Phase | Duration | Status |
|-------|----------|--------|
| Phase 1: Configuration | 1 week | Complete |
| Phase 2: Authentication | 2 weeks | Complete |
| Phase 3: keytool CLI | 1 week | Complete |
| Phase 4A: Core Library + UX | 2-3 weeks | Complete |
| Phase 4B: Frictionless Onboarding | 1-2 weeks | Complete |
| **Phase 4C: Core Hardening & Security** | 6-8 weeks | Complete (Batches A-I, Post-I-1) |
| **Phase 5: Network Intelligence** | 4-6 weeks | **Complete** |
| **Phase 6: ACL + Relay Security + Client Invites** | 1 day | **Complete** |
| **Phase 7: ZKP Privacy Layer** | 1 day | **Complete** |
| **Phase 8: Identity Security + Remote Admin** | 1 day | **Complete** |
| **Phase 8B: Per-Peer Data Grants** | 3 days | **Complete** |
| **Phase 8C: ACL-to-Macaroon Migration** | - | M1 complete, M2-M5 planned |
| **Phase 8D: Module Slots** | - | Planned |
| **Phase 9A: Core Interfaces & Library** | 1 week | **Complete** |
| **Phase 9B: File Transfer Plugin** | 3 weeks | **Complete** |
| **Post-9B: Chaos Testing + Network Hardening** | 4 days | **Complete** |
| **Post-9B: Plugin Architecture Shift** | 5 days | **Complete** |
| Phase 9C: Service Discovery & Plugins | 1-2 weeks | Planned |
| Phase 9D: Python SDK & Documentation | 1-2 weeks | Planned |
| Phase 9E: Swift SDK | 1-2 weeks | Planned |
| Phase 9F: Layer 2 WASM Runtime | - | Planned |
| Phase 9G: Layer 3 AI Plugin Generation | - | Future |
| Phase 10: Distribution & Launch | 1-2 weeks | Planned |
| Phase 11: Desktop Gateway + Private DNS | 2-3 weeks | Planned |
| Phase 12: Apple Multiplatform App | 3-4 weeks | Planned (separate repo: shurli-ios) |
| Phase 13: Federation | 2-3 weeks | Planned |
| Phase 14: Advanced Naming + Peer ID Prefix | 2-3 weeks | Planned (Optional) |
| Phase 15+: Ecosystem | Ongoing | Conceptual |

**Priority logic**: Harden the core (done) -> network intelligence (done) -> ACL and relay security (done) -> ZKP privacy (done) -> identity security (done) -> interfaces, file transfer, and plugin architecture (9A-9B + plugin shift done) -> remaining plugins and SDKs (9C-9E) -> distribute -> transparent access (gateway, DNS) -> expand (Apple multiplatform app -> federation -> naming).

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

*Last updated: 2026-03-23. Current: Phase 8B (per-peer data grants) complete, plugin architecture complete. Next: Phase 8C-8D (ACL migration, module slots), Phase 9C-9G (discovery, SDKs, WASM, AI).*
