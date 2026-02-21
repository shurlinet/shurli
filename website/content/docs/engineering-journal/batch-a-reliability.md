---
title: "Batch A - Reliability"
weight: 3
description: "Timeouts, retries, DHT in the proxy path, and in-process integration tests."
---
<!-- Auto-synced from docs/engineering-journal/batch-a-reliability.md by sync-docs.sh - do not edit directly -->


Timeouts, retries, DHT in the proxy path, and in-process integration tests.

---

### ADR-A01: TCP Timeout Strategy

**Context**: TCP proxy connections through circuit relay need appropriate timeouts. Too short = drops active SSH sessions. Too long = leaked connections consume relay resources.

**Alternatives considered**:
- **No explicit timeouts** (rely on libp2p defaults) - Rejected because libp2p's default stream timeouts are too short for interactive SSH sessions.
- **Configurable per-service timeouts** - Considered for future, but adds complexity for a problem that has reasonable defaults.

**Decision**: 10-second dial timeout for initial TCP connection (`net.DialTimeout("tcp", addr, 10*time.Second)`), 30-second context timeout for service connections. No idle timeout - SSH sessions can be long-lived; the half-close proxy (`BidirectionalProxy`) cleanly handles EOF propagation.

**Consequences**: Long-lived connections are supported, but a peer that disappears without closing the stream will hold resources until the relay's session duration limit (default 10 minutes) kicks in.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/proxy.go:66`

---

### ADR-A02: Retry with Exponential Backoff

**Context**: P2P connections through relays are inherently unreliable. A single dial failure shouldn't kill a proxy session.

**Alternatives considered**:
- **No retry** - Fail immediately. Rejected because relay connections often fail transiently.
- **Fixed delay retry** - Simpler but can cause thundering herd and doesn't adapt to load.

**Decision**: `DialWithRetry()` wraps any dial function with exponential backoff: 1s, 2s, 4s, ..., capped at 60s. Default 3 retries for daemon-created proxies.

**Consequences**: A failing connection takes up to ~7 seconds before giving up (1+2+4), which is acceptable for interactive use. The cap at 60s prevents runaway delays.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/proxy.go:130-155`

---

### ADR-A03: DHT in Proxy Path

**Context**: When the daemon receives a proxy connect request, the target peer might not be directly connected. Need to find and reach them first.

**Alternatives considered**:
- **Require pre-existing connection** - Simpler but fragile. Rejected because peers reconnect through DHT discovery, and the user shouldn't need to manually reconnect before proxying.
- **DNS-based discovery** - Rejected because it requires external infrastructure.

**Decision**: `ConnectToPeer()` in the daemon runtime performs DHT lookup + relay address injection before establishing the service stream. Every proxy and ping operation calls this first.

**Consequences**: First connection to a peer may be slow (DHT walk + relay reservation). Subsequent connections reuse the existing link. This is the correct behavior - find the peer, then talk to them.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/serve_common.go` (`ConnectToPeer` method), `https://github.com/satindergrewal/peer-up/blob/main/internal/daemon/handlers.go:338`

---

### ADR-A04: In-Process Integration Tests

**Context**: Need integration tests that verify multi-peer P2P scenarios without requiring Docker, LAN access, or actual network infrastructure.

**Alternatives considered**:
- **Docker-only tests** - Realistic but slow and requires Docker installed. Added later as a complement (Batch G), not a replacement.
- **Mock libp2p hosts** - Too much mocking makes tests unreliable.

**Decision**: Create real libp2p hosts in the same process, connecting through an in-process relay. Tests in `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/` create 2-3 hosts that communicate through circuit relay within a single test binary.

**Consequences**: Tests are fast (~2s) and run anywhere (`go test ./...`). They don't test actual network conditions (latency, packet loss), which is why Docker integration tests were added later as a complement.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network_test.go`, `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/service_test.go`
