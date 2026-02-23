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
| **Phase&nbsp;5** | **Network&nbsp;Intelligence** | | |
| 5-K | mDNS | Zero-config LAN peer discovery | Planned |
| 5-L | PeerManager | Bitcoin-inspired scoring, persistent peer table, PEX | Planned |
| 5-M | GossipSub | PubSub broadcast, address change announcements | Planned |
| N | **ZKP&nbsp;Privacy** | Anonymous auth, anonymous relay, private reputation | Watching |
| J | **Visual&nbsp;Channel** | "Constellation Code" - animated visual pairing | Future |

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
| **Phase 5: Network Intelligence** | 4-6 weeks | Planned |
| Phase 6: Plugins, SDK & First Plugins | 3-4 weeks | Planned |
| Phase 7: Distribution & Launch | 1-2 weeks | Planned |
| Phase 8: Desktop Gateway + Private DNS | 2-3 weeks | Planned |
| Phase 9: Mobile Apps | 3-4 weeks | Planned |
| Phase 10: Federation | 2-3 weeks | Planned |
| Phase 11: Advanced Naming | 2-3 weeks | Planned (Optional) |
| Phase 12+: Ecosystem | Ongoing | Conceptual |

**Priority logic**: Harden the core (done) -> network intelligence (mDNS, PeerManager, GossipSub) -> make it extensible with real plugins -> distribute with use-case content (GPU, IoT, gaming) -> transparent access (gateway, DNS) -> expand (mobile -> federation -> naming).

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
**Current Phase**: Phase 4C Complete. Phase 5 (Network Intelligence) next.
**Phases**: 1-4C (complete), 5 (Network Intelligence), 6-11 (planned), 12+ (ecosystem)
**Next Milestone**: Phase 5 - mDNS (5-K), PeerManager (5-L), GossipSub (5-M)
**Future milestones**: Phase 5 (Network Intelligence) -> Phase 6 (Plugins) -> Phase 7 (Distribution)
**Research**: N (ZKP Privacy - watching for trustless ZKP in Go) -> J (Visual Channel - future)
**Relay elimination**: Every-peer-is-a-relay shipped (Batch I-f). `require_auth` peer relays -> DHT discovery -> VPS becomes obsolete
