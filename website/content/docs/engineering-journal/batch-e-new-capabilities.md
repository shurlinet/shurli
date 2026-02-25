---
title: "Batch E - New Capabilities"
weight: 7
description: "Relay health endpoint and headless invite/join for scripting."
---
<!-- Auto-synced from docs/engineering-journal/batch-e-new-capabilities.md by sync-docs - do not edit directly -->


Relay health endpoint and headless invite/join for scripting.

---

### ADR-E01: `/healthz` on Relay

**Context**: The relay server is a critical public-facing service. Monitoring systems need a health endpoint.

**Alternatives considered**:
- **TCP port check only** - Just verify the port is open. Rejected because it doesn't verify the relay is actually functional.
- **Full metrics endpoint** - Prometheus-style. Planned for Batch H, but `/healthz` needed now for basic monitoring.

**Decision**: HTTP `/healthz` endpoint on configurable address (default `127.0.0.1:9090`). Returns JSON with `status`, `uptime_seconds`, and `connected_peers`. Restricted to loopback by default - reverse proxy or SSH tunnel for remote access.

**Consequences**: Minimal information exposure (no peer IDs, no version, no protocol list in the health response - hardened in the post-phase audit). Loopback-only prevents information disclosure to the public internet.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_serve.go`

---

### ADR-E02: Headless Invite/Join

**Context**: Docker containers, CI/CD pipelines, and scripts need to create/accept invites without interactive prompts or QR codes.

**Alternatives considered**:
- **Separate CLI for scripting** - A `shurli-cli` tool. Rejected because it fragments the tool.
- **Environment variables only** - `SHURLI_INVITE_CODE=xxx shurli join`. Supported alongside the flag.

**Decision**: `--non-interactive` flag on both `invite` and `join`. In non-interactive mode: invite prints bare code to stdout (progress to stderr), join reads code from positional arg or `SHURLI_INVITE_CODE` env var. No QR code, no prompts, no color.

**Consequences**: Docker integration tests can create and exchange invite codes programmatically. The flag reuses the same code paths as interactive mode - just different I/O routing.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_invite.go:34`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_join.go`
