---
title: "Engineering Journal"
weight: 12
description: "Architecture Decision Records for Shurli. The why behind every significant design choice."
---
<!-- Auto-synced from docs/engineering-journal/README.md by sync-docs - do not edit directly -->


This document captures the **why** behind every significant architecture decision in Shurli. Each entry follows a lightweight ADR (Architecture Decision Record) format: what problem we faced, what options we considered, what we chose, and what trade-offs we accepted.

New developers, contributors, and future-us should be able to read this and understand not just what the code does, but why it's shaped the way it is.

## Reading Guide

- **ADR-0XX**: Core architecture decisions made before the batch system
- **ADR-X0Y**: Batch-specific decisions (A=reliability, B=code quality, etc.)
- Each ADR is self-contained - read any entry independently
- Entries link to source files and commits where relevant

## Sections

| Section | ADRs | Focus |
|---------|------|-------|
| [Core Architecture](core-architecture/) | ADR-001 to ADR-008 | Foundational technology choices |
| [Batch A: Reliability](batch-a-reliability/) | ADR-A01 to ADR-A04 | Timeouts, retries, DHT, integration tests |
| [Batch B: Code Quality](batch-b-code-quality/) | ADR-B01 to ADR-B04 | Dedup, logging, errors, versioning |
| [Batch C: Self-Healing](batch-c-self-healing/) | ADR-C01 to ADR-C03 | Config backup, commit-confirmed, watchdog |
| [Batch D: libp2p Features](batch-d-libp2p-features/) | ADR-D01 to ADR-D04 | AutoNAT, QUIC, Identify, smart dialing |
| [Batch E: New Capabilities](batch-e-new-capabilities/) | ADR-E01 to ADR-E02 | Relay health, headless invite/join |
| [Batch F: Daemon Mode](batch-f-daemon-mode/) | ADR-F01 to ADR-F04 | Unix socket, cookie auth, hot-reload |
| [Batch G: Test Coverage](batch-g-test-coverage/) | ADR-G01 to ADR-G04 | Docker tests, relay binary, injectable exit, audit protocol |
| [Batch H: Observability](batch-h-observability/) | ADR-H01 to ADR-H03 | Prometheus, nil-safe pattern, auth callback |
| [Pre-Batch I](pre-batch-i/) | ADR-Ia01 to ADR-Ib02 | Makefile, PAKE invite, DHT namespaces |
| [Batch I: Adaptive Path Selection](batch-i-adaptive-path/) | ADR-I01 to ADR-I06 | Interface discovery, dial racing, path tracking, network monitoring, STUN, peer relay |
| [Post-I-2: Trust & Delivery](post-i-2-trust-and-delivery/) | ADR-J01 to ADR-J06 | Peer notify, HMAC, relay admin, SAS verification, reachability, history |
| [Pre-Phase 5 Hardening](pre-phase5-hardening/) | ADR-K01 to ADR-K03 | Startup race, CGNAT detection, stale addresses, service deployment |
| [Phase 5: Network Resilience](phase5-network-resilience/) | ADR-L01 to ADR-L08 | Native mDNS, PeerManager, stale cleanup, IPv6 probing, relay-discard |
| [Phase 5: Relay Decentralization](phase5-relay-decentralization/) | ADR-RD01 to ADR-RD05 | Peer relay, DHT relay discovery, health-aware selection, bandwidth, layered bootstrap |
| [Dev Tooling](dev-tooling/) | ADR-DT01 to ADR-DT03 | Go doc sync pipeline, relay setup subcommand, directory consolidation |
| [Phase 6: ACL + Relay Security + Client Invites](phase6-acl-relay-security/) | ADR-M01 to ADR-M08 | Roles, macaroons, invite deposits, sealed vault, remote unseal, 2FA |
| [Phase 7: ZKP Privacy Layer](phase7-zkp-privacy/) | ADR-N01 to ADR-N18 | gnark PLONK, Poseidon2 Merkle tree, membership proofs, range proofs, BIP39 keys |
| [Phase 8: Identity & Remote Admin](phase8-identity-remote-admin/) | ADR-O01 to ADR-O17 | Unified BIP39 seed, encrypted identity, recovery, remote admin, MOTD, goodbye, session tokens |
| [Seed Relay Separation & Init Flow](seed-relay-separation/) | ADR-P01 to ADR-P05 | Discovery-only seed relays, server-side circuit ACL, init flow, config set |
| [Phase 9: SDK, Plugins & Protocol Consolidation](phase9-sdk-plugins/) | ADR-Q01 to ADR-Q05 | Invite v1/v2 deletion, protocol ID helpers, bootstrap extraction, file transfer plugin |
| [Phase 9: File Transfer Architecture](phase9-file-transfer/) | ADR-R01 to ADR-R09 | FastCDC, BLAKE3, zstd, Reed-Solomon, RaptorQ, parallel streams, receive permissions, rate limiting |
| [Post-Chaos Network Hardening](post-chaos-network-hardening/) | ADR-S01 to ADR-S07 | Black hole reset, ForceReachabilityPrivate, constrained dial, VPN detection, gateway tracking, dial worker workaround, autorelay tuning |
| [File Transfer Hardening](file-transfer-hardening/) | ADR-R10 to ADR-R16 | DDoS defense (7 layers), queue persistence (HMAC), path privacy, checkpoint resume, queue backpressure, name normalization, relay transport |
| [Phase 8B: Per-Peer Data Grants](per-peer-data-grants/) | ADR-T01 to ADR-T10 | Node-level enforcement, macaroon grants, dual-store (GrantStore + Pouch), binary stream header, P2P delivery protocol, delegation, notifications, audit log, rate limiting, backoff reset |
| [Phase 9: Plugin Security Threat Analysis](plugin-security-threat-analysis/) | ADR-U01 to ADR-U08 | Trusted computing base, WASM host function API, supply chain defense, decomposed permissions, lifecycle state machine, credential isolation, AI-era constraints, registry design |
| [Phase 9: Grant Receipt Protocol](grant-receipt-protocol/) | ADR-V01 to ADR-V05 | Relay-issued receipts, client-side grant cache, smart pre-transfer check, per-chunk circuit byte tracking, smart reconnection |
| [Phase 9: Relay Circuit Investigation](relay-circuit-investigation/) | ADR-W01 to ADR-W03 | Tier-aware session limits, receiver busy retry, seed relay churn (superseded by Grant Receipt Protocol) |
| [FT-Y: Tail Slayer Path Reliability](tail-slayer-path-reliability/) | ADR-X01 to ADR-X07 | Hedged relay racing, multi-peer manifest exchange, connection-pinned streams, managed relay backup paths, checkpoint/resume failover, zero-sync block coordination |
| [FT-Y: Streaming Protocol Rewrite](streaming-protocol-rewrite/) | ADR-Y01 to ADR-Y10 | Streaming SHFT protocol, global chunk space, directory transfer, hash probe split, compression detection, adaptive chunks, cross-session resume |
| [FT-Y: Multi-Peer Adaptive Transfer Scheduling](multi-peer-adaptive-transfer/) | ADR-MP01 to ADR-MP10 | Static partition failure, adaptive RaptorQ evolution, raw block work stealing, per-peer worker streams, checkpoint/resume |
| [FT-Y: Budget-Aware Relay Selection](budget-aware-relay-selection/) | ADR-BR01 to ADR-BR09 | Relay selection from health-only ranking to grant-aware routing, low-budget retry, receipt freshness, and budget exhaustion recovery |
| [FT-Y: Reed-Solomon Erasure Recovery](reed-solomon-erasure-recovery/) | ADR-RS01 to ADR-RS08 | Bounded per-stripe RS repair, streaming-compatible recovery metadata, receiver memory limits, missing-chunk reconstruction |
| [FT-Y: Verified-LAN Classification Migration](verified-lan-classification/) | ADR-VL01 to ADR-VL05 | Bare RFC 1918 mask replaced with mDNS-verified LAN detection for trust-making decisions, VerifiedTransport classifier, dead code deletion |
| [FT-Y: Persistent Proxy Service](persistent-proxy-service/) | ADR-PP01 to ADR-PP05 | Daemon-owned persistent proxy lifecycle with desired-state persistence, GATETIME stability detection, event-driven reconnect, poll fallback, and authorization cleanup |
