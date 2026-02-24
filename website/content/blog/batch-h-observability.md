---
title: "Prometheus Metrics and Audit Logging"
date: 2026-02-21
tags: [release, batch-h]
image: /images/blog/batch-h-observability.svg
description: "Opt-in Prometheus metrics endpoint, custom peerup metrics, structured audit logging, and free libp2p built-in metrics. Zero overhead when disabled."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Batch H: Observability](/images/blog/batch-h-observability.svg)

## What's new

peer-up now exposes a Prometheus `/metrics` endpoint and structured audit logging. Both are opt-in, disabled by default, with zero overhead when off. Enable them in your config:

```yaml
telemetry:
  metrics:
    enabled: true
    listen_address: "127.0.0.1:9091"
  audit:
    enabled: true
```

## Why it matters

You can't fix what you can't measure. Before Batch H, the only way to understand what peer-up was doing was reading log output. Now you can track proxy throughput, auth decisions, hole punch success rates, and API latency with any Prometheus-compatible tool.

This is also the foundation for Batch I (Adaptive Path Selection). Smart path decisions need connection quality data, and the metrics pipeline provides it.

## Technical highlights

![Observability data flow - from metric sources through Prometheus registry to /metrics endpoint](/images/docs/observability-flow.svg)

### 10 custom peerup metrics

- **Proxy**: bytes transferred, connections established, active sessions, session duration (per service)
- **Auth**: allow/deny decision counts
- **Hole punch**: success/failure counts, timing histograms
- **Daemon API**: request counts, latency histograms (with path sanitization to prevent label explosion)
- **Build info**: version and Go version as labels

### Free libp2p metrics (no extra code)

When metrics are enabled, all libp2p built-in metrics appear automatically: swarm connections by transport, autonat reachability, resource manager limits, relay service stats, and identify events. These come from `libp2p.PrometheusRegisterer(reg)` with zero additional instrumentation.

### Structured audit events

Auth decisions, service ACL denials, API access, and auth changes are logged as structured JSON events via `log/slog`:

```json
{"time":"...","level":"WARN","msg":"auth_decision","audit":{"peer":"12D3KooW...","direction":"inbound","result":"deny"}}
```

### Why Prometheus, not OpenTelemetry

The original roadmap said "OpenTelemetry integration." Research showed this was the wrong choice for peer-up:

1. libp2p v0.47.0 emits metrics natively via `prometheus/client_golang`, not OTel
2. Adding the OTel SDK would add ~4MB to the binary
3. Distributed tracing has 35% CPU overhead from span management on every stream
4. `prometheus/client_golang` was already in our dependency tree (indirect dep of libp2p)

The Prometheus bridge (`go.opentelemetry.io/contrib/bridges/prometheus`) can forward all metrics to any OTel backend later, without changing a single line of instrumentation code.

### Design decisions

- **Isolated registry**: Each `Metrics` instance uses `prometheus.NewRegistry()`, not the global default. Tests get their own registry. No collision with other Prometheus users.
- **Nil-safe everywhere**: Every call site checks for nil metrics/audit. `InstrumentHandler` returns the handler unchanged when both are nil.
- **Callback pattern for auth**: `internal/auth` can't import `pkg/p2pnet` (circular import). An `AuthDecisionFunc` callback is wired in the startup code to feed both metrics counters and audit events.
- **Path sanitization**: `/v1/auth/12D3KooW...` becomes `/v1/auth/:id` in metrics labels to prevent high cardinality.

### Grafana dashboard included

A pre-built Grafana dashboard ships in `grafana/peerup-dashboard.json`. Import it into any Grafana instance to get 29 panels across 6 sections:

- **Overview** - version, uptime, active connections, total bytes, auth summary
- **Proxy Throughput** - bytes/sec per service, active connections, connection rate, session duration percentiles
- **Security** - auth allow/deny rate, cumulative decision counts
- **Hole Punch** - attempt counts, success rate gauge, duration percentiles
- **Daemon API** - request rate by path, latency percentiles, status code breakdown
- **System** - memory, goroutines, GC rate, file descriptors, CPU

### Impact

| Metric | Value |
|--------|-------|
| Binary size | 27MB to 28MB (+1MB) |
| New dependencies | 0 (prometheus already indirect) |
| CPU overhead (disabled) | Zero (`DisableMetrics()`) |
| New files | 5 (metrics.go, audit.go, middleware.go, tests, Grafana dashboard) |
| Modified files | 10 |
| New tests | 20+ |
---

*Batch H is the eighth development batch. See the [monitoring guide](/docs/monitoring/) for Prometheus + Grafana setup, and the [engineering journal](/docs/engineering-journal/) for the full decision trail.*
