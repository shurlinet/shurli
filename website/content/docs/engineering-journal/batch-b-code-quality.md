---
title: "Batch B - Code Quality"
weight: 4
description: "Relay address deduplication, structured logging, sentinel errors, build version embedding."
---
<!-- Auto-synced from docs/engineering-journal/batch-b-code-quality.md by sync-docs - do not edit directly -->


Relay address deduplication, structured logging, sentinel errors, and build version embedding.

---

### ADR-B01: Proxy Deduplication

**Context**: `ParseRelayAddrs()` could receive duplicate relay addresses (same peer, different multiaddrs). Without dedup, libp2p would make redundant connections.

**Alternatives considered**:
- **Let libp2p handle it** - libp2p does some dedup, but passing duplicates to `EnableAutoRelayWithStaticRelays` wastes resources.

**Decision**: `ParseRelayAddrs()` deduplicates by peer ID and merges addresses for the same relay peer. If the same relay appears twice with different addresses, all addresses are collected under one `peer.AddrInfo`.

**Consequences**: Clean relay configuration. Users can list multiple addresses for the same relay (e.g., IPv4 and IPv6) without issues.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/network.go:280-309`

---

### ADR-B02: `log/slog` over zerolog/zap

**Context**: Needed structured logging throughout the project. Many Go projects use zerolog or zap for performance.

**Alternatives considered**:
- **zerolog** - Zero-allocation, fast. Rejected because it's another dependency, and Shurli doesn't produce enough log volume to need zero-allocation logging.
- **zap** - Uber's logger, excellent performance. Rejected for the same reason - adds dependency weight for no measurable benefit.
- **log/slog** - Go 1.21+ standard library structured logging. Built-in, no dependency, sufficient performance.

**Decision**: `log/slog` everywhere. `slog.Info`, `slog.Warn`, `slog.Error` with structured key-value pairs. Default handler writes to stderr.

**Consequences**: No external logging dependency. Standard library compatibility means any future handler (JSON, OpenTelemetry) can be swapped in without changing call sites. Slightly more verbose than zerolog's fluent API, but consistency with stdlib is worth it.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/main.go:20-22` (handler setup), used throughout all packages

---

### ADR-B03: Sentinel Errors

**Context**: Error handling was using `fmt.Errorf("service not found")` strings that callers couldn't programmatically check.

**Alternatives considered**:
- **String matching** - `strings.Contains(err.Error(), "not found")`. Rejected because it's fragile and breaks on message changes.
- **Custom error types** - `type NotFoundError struct { Name string }`. Considered for complex errors, but sentinel variables are simpler for the common case.

**Decision**: Package-level sentinel errors using `errors.New()`: `ErrServiceNotFound`, `ErrNameNotFound`, `ErrConfigNotFound`, `ErrNoArchive`, `ErrCommitConfirmedPending`, `ErrNoPending`, `ErrDaemonAlreadyRunning`, `ErrProxyNotFound`. Callers use `errors.Is()` to check.

**Consequences**: Clean error checking, wrappable with `fmt.Errorf("%w: ...", ErrFoo)`. Error messages in two packages: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/errors.go` and `https://github.com/shurlinet/shurli/blob/main/internal/config/errors.go`.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/errors.go`, `https://github.com/shurlinet/shurli/blob/main/internal/config/errors.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/errors.go`

---

### ADR-B04: Build Version Embedding

**Context**: Need to know exactly which version and commit is running, especially when debugging relay issues remotely.

**Alternatives considered**:
- **Version file** - Read from embedded file. Rejected because it's another artifact to maintain.
- **Git describe at runtime** - Call `git describe` at startup. Rejected because the binary might not be in a git repo.

**Decision**: `ldflags` injection at build time: `-X main.version=... -X main.commit=... -X main.buildDate=...`. Defaults to `dev` and `unknown` for development builds. Also sent as libp2p Identify UserAgent (`shurli/0.1.0`).

**Consequences**: Every binary is self-identifying. `shurli version` shows exact build info. The UserAgent appears in `shurli daemon peers --all`, making it easy to verify what version each peer runs.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/main.go:10-17`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/network.go:121-123`
