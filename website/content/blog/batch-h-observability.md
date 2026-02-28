---
title: "Prometheus Metrics and Audit Logging"
date: 2026-02-21
tags: [release, batch-h]
image: /images/blog/batch-h-observability.svg
description: "Opt-in Prometheus metrics endpoint, 30 custom shurli metrics, structured audit logging, and free libp2p built-in metrics. Zero overhead when disabled."
authors:
  - name: Satinder Grewal
    link: https://github.com/satindergrewal
---

![Batch H: Observability](/images/blog/batch-h-observability.svg)

## The problem we solved

You can't fix what you can't see. Before Batch H, the only way to understand what Shurli was doing was reading raw log output. How much data is flowing through your proxy? How often are connections being denied? Is that hole-punch actually working? You'd have to grep through log files and hope you asked the right question.

This is like driving a car without a dashboard. The engine runs, but you have no speedometer, no fuel gauge, no warning lights. You find out something is wrong when the car stops.

Batch H adds the dashboard. Every important event inside Shurli is now measured and exposed through an industry-standard interface that any monitoring tool can read.

## What this means for you

**If you run a relay or home-node:** You get a real-time view of everything your node is doing. How many devices are connected, how much bandwidth is flowing, whether anyone tried to connect and was denied. All visible in a browser through Grafana, without SSH-ing into your server.

**If you're a developer:** Every subsystem emits Prometheus counters, gauges, and histograms. Isolated registry (no global collisions), nil-safe helpers (zero overhead when disabled), callback patterns to avoid circular imports. The same metrics pipeline that feeds your Grafana dashboard feeds your integration tests.

## How it works

Think of it like plumbing for data. Each part of Shurli (proxy, auth, hole-punch, API) has a gauge attached. Those gauges feed into a central meter (Prometheus registry), which exposes readings through a single tap (`/metrics` endpoint). Any monitoring tool that speaks Prometheus can drink from that tap.

![Observability data flow - from metric sources through Prometheus registry to /metrics endpoint](/images/docs/observability-flow.svg)

Enable it in your config:

```yaml
telemetry:
  metrics:
    enabled: true
    listen_address: "127.0.0.1:9091"
  audit:
    enabled: true
```

Both are opt-in, disabled by default, with zero overhead when off. The binary is the same whether you use metrics or not.

## 30 custom metrics, built incrementally

Metrics weren't bolted on as an afterthought. Each phase adds measurements for its own components. The dashboard grows with the project.

![Metrics Growth: Built Incrementally Across Phases](/images/blog/batch-h-metrics-evolution.svg)

**Batch H (foundation, 10 metrics):** Proxy bytes transferred, connections, active sessions, session duration. Auth allow/deny decisions. Hole-punch success/failure counts and timing. Daemon API request counts and latency. Build info.

**Phase 5 (+10 network metrics):** Path dial attempts and timing. Connected peers by path type and transport. Interface changes, STUN probes, mDNS discovery events. PeerManager reconnection attempts. NetIntel presence announcements.

**Phase 6 (+10 security metrics):** Vault seal state gauge (LOCKED/UNLOCKED). Seal/unseal operations by trigger. Remote unseal attempts with lockout tracking. Deposit operations (create/revoke/modify). Pending deposit count. Pairing attempts by result. Macaroon verification. Admin socket request counts and latency.

Every metric helper is nil-safe: if Prometheus is disabled, the handlers work identically with zero overhead. No `if metricsEnabled` sprinkled through the codebase. The metric call either records or no-ops.

### Free libp2p metrics (no extra code)

When metrics are enabled, all libp2p built-in metrics appear automatically: swarm connections by transport, autonat reachability, resource manager limits, relay service stats, and identify events. These come from `libp2p.PrometheusRegisterer(reg)` with zero additional instrumentation.

## What the dashboard shows

A pre-built Grafana dashboard ships in `grafana/shurli-dashboard.json`. Import it into any Grafana instance to get 37 panels across 6 sections. No configuration beyond pointing it at your Prometheus.

![Grafana Dashboard: 37 Panels Across 6 Sections](/images/blog/batch-h-grafana-sections.svg)

**Overview:** Version, uptime, active connections, total bytes transferred, auth decision summary. The "is my node healthy?" glance.

**Proxy Throughput:** Bytes per second per service, active connection gauge, connection rate, session duration percentiles (p50/p95/p99). See exactly how much traffic each service handles.

**Security:** Auth allow/deny rates, cumulative decision counts, vault seal state (LOCKED/UNLOCKED with color coding), seal/unseal operations by trigger, pairing attempts (success/failure), deposit operations, pending deposit count, admin socket request tracking. Everything Phase 6 does is visible here.

**Hole Punch:** Attempt counts, success rate gauge, duration percentiles. See whether your network's NAT is consistently punchable or if you should expect relay-only.

**Daemon API:** Request rate by path, latency percentiles, status code breakdown. Catch slow endpoints or unexpected 4xx/5xx rates.

**System:** Memory usage, goroutines, GC rate, file descriptors, CPU. Standard Go runtime metrics for capacity planning.

## Structured audit events

Security-sensitive operations emit structured JSON events via `log/slog`:

```json
{"time":"...","level":"WARN","msg":"auth_decision","audit":{"peer":"12D3KooW...","direction":"inbound","result":"deny"}}
```

Auth decisions, service ACL denials, API access, and auth changes are logged. Feed these to journalctl, a log aggregator, or any SIEM. The format is stable and machine-parseable.

## Technical decisions

### Why Prometheus, not OpenTelemetry

The original roadmap said "OpenTelemetry integration." Research showed this was the wrong choice for Shurli:

1. libp2p v0.47.0 emits metrics natively via `prometheus/client_golang`, not OTel
2. Adding the OTel SDK would add ~4MB to the binary
3. Distributed tracing has 35% CPU overhead from span management on every stream
4. `prometheus/client_golang` was already in our dependency tree (indirect dep of libp2p)

The Prometheus bridge (`go.opentelemetry.io/contrib/bridges/prometheus`) can forward all metrics to any OTel backend later, without changing a single line of instrumentation code.

### Design decisions

- **Isolated registry**: Each `Metrics` instance uses `prometheus.NewRegistry()`, not the global default. Tests get their own registry. No collision with other Prometheus users in the same process.
- **Nil-safe everywhere**: Every call site checks for nil metrics/audit. `InstrumentHandler` returns the handler unchanged when both are nil. Zero overhead when disabled.
- **Callback pattern for auth**: `internal/auth` can't import `pkg/p2pnet` (circular import). An `AuthDecisionFunc` callback is wired in the startup code to feed both metrics counters and audit events.
- **Path sanitization**: `/v1/auth/12D3KooW...` becomes `/v1/auth/:id` in metrics labels to prevent high cardinality (label explosion).

## Impact

| | Before Batch H | After Batch H |
|--|----------------|---------------|
| **Visibility** | grep through logs | 37-panel Grafana dashboard |
| **Proxy monitoring** | None | Bytes, connections, duration per service |
| **Auth tracking** | Log messages only | Counters + structured audit events |
| **Hole-punch insight** | "Did it work?" | Success rate, timing histograms |
| **Security ops visibility** | None | Vault state, pairing, deposits, admin socket |
| **Setup effort** | N/A | Import JSON + connect Prometheus |
| **Overhead when disabled** | N/A | Zero (`DisableMetrics()`) |
| **Binary size impact** | 27MB | 28MB (+1MB) |
| **New dependencies** | | 0 (prometheus already indirect) |

---

*Batch H is the eighth development batch. See the [monitoring guide](/docs/monitoring/) for Prometheus + Grafana setup, and the [engineering journal](/docs/engineering-journal/) for the full decision trail.*
