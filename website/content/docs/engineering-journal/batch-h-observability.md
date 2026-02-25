---
title: "Batch H - Observability"
weight: 10
description: "Prometheus metrics, nil-safe observability pattern, auth decision callback."
---
<!-- Auto-synced from docs/engineering-journal/batch-h-observability.md by sync-docs - do not edit directly -->


Prometheus metrics, nil-safe observability pattern, and auth decision callback.

---

### ADR-H01: Prometheus over OpenTelemetry

**Context**: Batch H adds metrics and audit logging. The original roadmap said "OpenTelemetry integration." Research revealed that libp2p v0.47.0 emits metrics natively via `prometheus/client_golang`, not OpenTelemetry.

**Alternatives considered**:
- **OpenTelemetry SDK** - Industry standard, supports traces + metrics + logs. Rejected because: +4MB binary size, 35% CPU overhead from span management on every stream, and libp2p already speaks Prometheus natively. Adding OTel would require a translation layer (Prometheus -> OTel) for zero benefit.
- **OpenTelemetry bridge only** (`go.opentelemetry.io/contrib/bridges/prometheus`) - Forward Prometheus metrics to OTel backends. Deferred to a future release when users request it. The bridge can be added later without changing any instrumentation code.
- **StatsD/Graphite** - Simpler push model. Rejected because Prometheus is already in our dependency tree as an indirect dep of libp2p.

**Decision**: Use `prometheus/client_golang` directly with an isolated `prometheus.Registry`. When metrics enabled, pass the registry to libp2p via `libp2p.PrometheusRegisterer(reg)` to get all built-in libp2p metrics for free. When disabled, call `libp2p.DisableMetrics()` for zero overhead.

**Consequences**: No distributed tracing (deferred). No OTLP export (can be added via bridge later). Binary size increase: ~1MB (28MB total). Any Prometheus-compatible tool (Grafana, Datadog, etc.) works out of the box.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/network.go`

---

### ADR-H02: Nil-Safe Observability Pattern

**Context**: Metrics and audit logging are opt-in. Every call site that records a metric or audit event needs to work correctly when observability is disabled.

**Alternatives considered**:
- **Feature flags with conditional compilation** - Build tags to exclude metrics code entirely. Rejected because it creates two binaries with different behavior, complicating testing.
- **No-op implementations** (interface-based) - Create `NullMetrics` / `NullAuditLogger` implementations. More idiomatic but adds interface overhead and boilerplate.
- **Global singleton with init check** - Single global metrics instance. Rejected to maintain testability (isolated registries per test).

**Decision**: Nil pointer checks at every call site. `*Metrics` and `*AuditLogger` are nil when disabled. All methods on `AuditLogger` check `if a == nil { return }`. Metrics call sites check `if metrics != nil` before recording. The `InstrumentHandler` middleware returns the handler unchanged when both are nil.

**Consequences**: Slightly verbose call sites (`if m != nil { m.Counter.Inc() }`). But: zero allocations when disabled, zero interface overhead, testable with isolated registries, and trivially verifiable (grep for nil checks).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/audit.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/middleware.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`

---

### ADR-H03: Auth Decision Callback (Avoiding Circular Imports)

**Context**: Auth decisions happen in `https://github.com/shurlinet/shurli/blob/main/internal/auth/gater.go`. Metrics live in `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/metrics.go`. Go forbids circular imports: `https://github.com/shurlinet/shurli/blob/main/internal/auth` cannot import `pkg/p2pnet`.

**Alternatives considered**:
- **Move gater to pkg/p2pnet** - Would work but breaks the `https://github.com/shurlinet/shurli/blob/main/internal/` boundary. The gater is an internal implementation detail.
- **Shared interface package** - Create a `pkg/observe` package with metric recording interfaces. Adds complexity for a single callback.

**Decision**: Define `AuthDecisionFunc func(peerID, result string)` as a callback type in `https://github.com/shurlinet/shurli/blob/main/internal/auth`. The gater calls this callback on every inbound decision. The wiring layer (`https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`) creates a closure that feeds both the Prometheus counter and the audit logger.

**Consequences**: Clean dependency graph. The auth package has zero knowledge of Prometheus or audit logging. The callback is nil-safe (checked before calling). Easy to add more observers later by extending the closure.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/auth/gater.go:SetDecisionCallback`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`
