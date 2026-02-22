---
title: "Engineering Journal"
weight: 12
description: "Architecture Decision Records for peer-up. The why behind every significant design choice."
---
<!-- Auto-synced from docs/engineering-journal/README.md by sync-docs.sh - do not edit directly -->


This document captures the **why** behind every significant architecture decision in peer-up. Each entry follows a lightweight ADR (Architecture Decision Record) format: what problem we faced, what options we considered, what we chose, and what trade-offs we accepted.

New developers, contributors, and future-us should be able to read this and understand not just what the code does, but why it's shaped the way it is.

## Reading Guide

- **ADR-0XX**: Core architecture decisions made before the batch system
- **ADR-X0Y**: Batch-specific decisions (A=reliability, B=code quality, etc.)
- Each ADR is self-contained - read any entry independently
- Entries link to source files and commits where relevant

## Sections

| Section | ADRs | Focus |
|---------|------|-------|
| [Core Architecture](core-architecture.md) | ADR-001 to ADR-008 | Foundational technology choices |
| [Batch A: Reliability](batch-a-reliability.md) | ADR-A01 to ADR-A04 | Timeouts, retries, DHT, integration tests |
| [Batch B: Code Quality](batch-b-code-quality.md) | ADR-B01 to ADR-B04 | Dedup, logging, errors, versioning |
| [Batch C: Self-Healing](batch-c-self-healing.md) | ADR-C01 to ADR-C03 | Config backup, commit-confirmed, watchdog |
| [Batch D: libp2p Features](batch-d-libp2p-features.md) | ADR-D01 to ADR-D04 | AutoNAT, QUIC, Identify, smart dialing |
| [Batch E: New Capabilities](batch-e-new-capabilities.md) | ADR-E01 to ADR-E02 | Relay health, headless invite/join |
| [Batch F: Daemon Mode](batch-f-daemon-mode.md) | ADR-F01 to ADR-F04 | Unix socket, cookie auth, hot-reload |
| [Batch G: Test Coverage](batch-g-test-coverage.md) | ADR-G01 to ADR-G04 | Docker tests, relay binary, injectable exit, audit protocol |
| [Batch H: Observability](batch-h-observability.md) | ADR-H01 to ADR-H03 | Prometheus, nil-safe pattern, auth callback |
| [Pre-Batch I](pre-batch-i.md) | ADR-Ia01 to ADR-Ib02 | Makefile, PAKE invite, DHT namespaces |
| [Batch I: Adaptive Path Selection](batch-i-adaptive-path.md) | ADR-I01 to ADR-I06 | Interface discovery, dial racing, path tracking, network monitoring, STUN, peer relay |
