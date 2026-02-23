---
title: "Roadmap"
weight: 9
description: "Multi-phase development roadmap for peer-up. From NAT traversal tool to decentralized P2P network infrastructure."
---

This document outlines the multi-phase evolution of peer-up from a simple NAT traversal tool to a comprehensive decentralized P2P network infrastructure.

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
| Phase&nbsp;3 | **keytool&nbsp;CLI** | Key management (now peerup subcommands) | Done |
| Phase&nbsp;4A | **Core&nbsp;Library** | `pkg/p2pnet/`, single binary, init wizard | Done |
| Phase&nbsp;4B | **Onboarding** | invite/join, QR codes, auth + relay CLI | Done |
| A | **Reliability** | Reconnection with backoff, dial timeout, DHT in proxy | Done |
| B | **Code&nbsp;Quality** | Proxy dedup, `log/slog`, sentinel errors, version embedding | Done |
| C | **Self-Healing** | Config archive/rollback, commit-confirmed, watchdog | Done |
| D | **libp2p** | AutoNAT v2, QUIC preferred, Identify UserAgent | Done |
| E | **Capabilities** | `peerup status`, `/healthz`, headless invite/join | Done |
| F | **Daemon** | Unix socket API, cookie auth, ping/traceroute/resolve | Done |
| G | **Testing** | 80.3% coverage, Docker tests, relay merge, website | Done |
| H | **Observability** | Prometheus metrics, audit logging, Grafana dashboard | Done |
| Pre-I-a | **Build&nbsp;Tooling** | Makefile, service install (systemd/launchd) | Done |
| Pre-I-b | **PAKE&nbsp;Invite** | Encrypted handshake, token-bound AEAD | Done |
| Pre-I-c | **Private&nbsp;DHT** | Namespace isolation for peer groups | Done |
| I | **Adaptive&nbsp;Path** | Interface discovery, dial racing, STUN, every-peer-relay | Done |
| Post-I-1 | **Relay&nbsp;Pairing** | Pairing codes, SAS verification, reachability grades | Done |
| K | **mDNS** | Zero-config LAN peer discovery | Planned |
| L | **PeerManager** | Bitcoin-inspired scoring, persistent peer table, PEX | Planned |
| M | **GossipSub** | PubSub broadcast, address change announcements | Planned |
| N | **ZKP&nbsp;Privacy** | Anonymous auth, anonymous relay, private reputation | Planned |
| J | **Visual&nbsp;Channel** | "Constellation Code" - animated visual pairing | Future |

---

## Timeline Summary

<img src="/images/docs/roadmap-timeline.svg" alt="Development timeline showing completed phases (1-4C) and planned phases (K-5+)" loading="lazy" />

| Phase | Duration | Status |
|-------|----------|--------|
| Phase 1: Configuration | 1 week | Complete |
| Phase 2: Authentication | 2 weeks | Complete |
| Phase 3: keytool CLI | 1 week | Complete |
| Phase 4A: Core Library + UX | 2-3 weeks | Complete |
| Phase 4B: Frictionless Onboarding | 1-2 weeks | Complete |
| **Phase 4C: Core Hardening & Security** | 6-8 weeks | Complete (Batches A-I, Post-I-1) |
| Batch K: mDNS Local Discovery | <1 week | Planned |
| Batch L: PeerManager / AddrMan | 2-3 weeks | Planned |
| Batch M: GossipSub Network Intelligence | 1-2 weeks | Planned |
| Phase 4D: Plugins, SDK & First Plugins | 3-4 weeks | Planned |
| Phase 4E: Distribution & Launch | 1-2 weeks | Planned |
| Phase 4F: Desktop Gateway + Private DNS | 2-3 weeks | Planned |
| Phase 4G: Mobile Apps | 3-4 weeks | Planned |
| Phase 4H: Federation | 2-3 weeks | Planned |
| Phase 4I: Advanced Naming | 2-3 weeks | Planned (Optional) |
| Phase 5+: Ecosystem | Ongoing | Conceptual |

**Total estimated time for Phase 4**: 18-26 weeks (5-6 months)

**Priority logic**: Onboarding first (remove friction) -> harden the core (security, self-healing, reliability, tests) -> make it extensible with real plugins (file sharing, service templates, WoL prove the architecture) -> distribute with use-case content (GPU, IoT, gaming) -> transparent access (gateway, DNS) -> expand (mobile -> federation -> naming).

---

## Contributing

This roadmap is a living document. Phases may be reordered, combined, or adjusted based on:
- User feedback and demand
- Technical challenges discovered during implementation
- Emerging technologies (AI, quantum, blockchain alternatives)
- Community contributions

**Adaptability over perfection.** We build for the next 1-5 years, not 50.

---

**Last Updated**: 2026-02-23
**Current Phase**: Post-I-1 Complete (Frictionless Relay Pairing + Daemon-Centric + Reachability Grade).
**Phase count**: 4C-4I (7 phases, down from 9 - file sharing and service templates merged into plugin architecture)
**Next Milestone**: Batch L (PeerManager / AddrMan)
**Future milestones**: L (PeerManager) -> N (ZKP Privacy) -> J (Visual Channel)
**Relay elimination**: Every-peer-is-a-relay shipped (Batch I-f). `require_auth` peer relays -> DHT discovery -> VPS becomes obsolete
