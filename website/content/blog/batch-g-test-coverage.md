---
title: "80% Coverage and a Security Audit"
date: 2026-02-19
tags: [release, batch-g]
image: /images/blog/batch-g-coverage.svg
description: "80.3% combined test coverage with Docker integration tests and a post-phase security audit that caught 10 issues."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![80% Coverage and a Security Audit](/images/blog/batch-g-coverage.svg)

## What's new

peer-up now has 80.3% combined test coverage across unit tests and Docker integration tests. Every CLI command, every daemon API handler, every config edge case - tested. And a post-phase security audit caught 10 issues, all fixed.

## Why it matters

For infrastructure software that handles network connectivity, untested code is a liability. The jump from ~30% to 80% coverage means confidence in refactoring, confidence in releases, and confidence that edge cases are handled. The security audit proved its value immediately: it found a nonce-reuse CVE in a transitive dependency.

## Technical highlights

![Docker integration test topology - relay server, node A, and node B in a real container network](/images/blog/batch-g-docker-topology.svg)

- **80.3% combined coverage**: Unit tests (Go's testing framework) + Docker integration tests (real binaries in containers communicating through relay). Coverage profiles merged with `go tool covdata`
- **Docker integration tests**: Compose environment with relay server, two nodes. Tests: relay startup, invite/join pairing, ping through circuit relay. Build tag `integration` keeps them separate from `go test ./...`
- **Injectable `osExit`**: Package-level variable replaces `os.Exit` in tests, enabling testing of all exit paths without killing the test process
- **Post-phase security audit** found and fixed:
  - CVE in pion/dtls/v3 (nonce reuse vulnerability), bumped to v3.1.2
  - TOCTOU race on Unix socket permissions, fixed with `umask(0077)` during `Listen()`
  - Cookie write ordering - cookie now written after socket is secured
  - Request body size limits - 1MB cap on all JSON API handlers
  - CI action SHA pinning - supply chain hardening
  - Config file permission checks - rejects files with mode wider than 0600
  - Relay `/healthz` hardened - loopback-only, minimal information exposure

## What's next

Observability: OpenTelemetry integration, metrics, and audit logging. Then adaptive path selection for direct connections.
