---
title: "FT-Y - Persistent Proxy Service"
weight: 36
description: "Daemon-owned persistent proxy lifecycle with desired-state persistence, GATETIME stability detection, event-driven reconnect, poll fallback, and authorization cleanup."
---
<!-- Auto-synced from docs/engineering-journal/persistent-proxy-service.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Complete |
| **Phase** | FT-Y (File Transfer Speed Optimization) |
| **ADRs** | ADR-PP01 to ADR-PP05 |
| **Primary Commits** | 42c8655 |

Persistent proxy work moved Shurli proxying from a foreground command that owned a local listener until the terminal exited to a daemon-owned service lifecycle. The important change is not the new command surface. It is that the daemon now owns desired proxy state, persists that intent, reconciles listeners against peer reachability, and cleans up proxy state when authorization changes.

That makes persistent proxying a local service lifecycle feature. It does not replace grants, service authorization, relay policy, path selection, or peer availability. A persistent proxy can keep a local listener and reconnect when conditions improve; it cannot make an unauthorized or unreachable peer valid.

---

## ADR-PP01: Foreground Proxies Were The Wrong Lifecycle

| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Accepted |
| **Commit** | 42c8655 |

### Context

The original proxy model was a foreground command. A local TCP listener existed because a CLI process was running. That worked for short sessions, but it tied service availability to a terminal, shell session, or supervising process outside Shurli.

That lifecycle was weaker than the rest of the daemon architecture. A daemon restart, peer reconnect, relay transition, or terminal close could leave the operator manually recreating a proxy that was still logically desired. The system had no durable difference between "this proxy should exist" and "a listener happens to be running right now."

### Decision

Move persistent proxy lifecycle into the daemon. The CLI declares intent through `proxy add`, `proxy remove`, `proxy enable`, and `proxy disable`; the daemon owns the actual listener lifecycle and service state.

The older foreground proxy remains as a backward-compatible ephemeral path. It is still useful for one-off sessions. Persistent proxies are different: they are daemon-managed local services.

This is not an access-control change. The daemon can preserve local service intent, bind a listener, and retry service connections, but the normal peer authorization, service policy, relay budget, and transport decisions still decide whether a remote service is reachable.

### Alternatives Considered

**Keep foreground-only proxies** preserved the existing mental model, but it made terminal lifetime part of service lifetime.

**Teach the CLI to supervise itself** would move daemon responsibilities into a client process and still fail when the session dies.

**Persist only shell snippets or service files** would push reconciliation onto the operating system instead of using the daemon's existing peer, path, and authorization state.

### Consequences

- Proxy intent is now durable daemon state, not terminal state.
- One-off foreground proxying remains available for compatibility.
- The daemon can reconcile proxy status after restart, reconnect, missed events, and authorization removal.
- Service authorization and path selection stay in their existing layers.

### Physical Verification

Code verification confirmed the lifecycle split. `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_proxy.go` still supports ephemeral foreground proxying, while persistent subcommands call daemon API methods in `https://github.com/shurlinet/shurli/blob/main/internal/daemon/client.go`. Commit 42c8655 changed 13 files with 1360 insertions and 22 deletions, centered on daemon API, store, server lifecycle, CLI, completions, man page, and SDK proxy behavior.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_proxy.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/client.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/handlers.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/server.go`

---

## ADR-PP02: Desired Proxy State Is Persisted And Reconciled

| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Accepted |
| **Commit** | 42c8655 |

### Context

Daemon ownership only helps if the daemon can distinguish desired state from active runtime state. A proxy may be configured but disabled, enabled but waiting for a peer, active with a bound listener, temporarily blocked by a local port conflict, or in an error state after authorization is removed.

That state also has to survive process restart without creating a new local security boundary. The persistence file must resist symlink attacks, partial writes, corrupt writes, and permissive file modes.

### Decision

Persist desired proxy state in `proxies.json` through `https://github.com/shurlinet/shurli/blob/main/internal/daemon/proxy_store.go`.

The desired state is a `ProxyEntry`: name, peer, service, port, and enabled flag. Runtime state lives separately in the daemon's `s.proxies` map as `activeProxy`. On startup, `RestoreProxies()` reads desired entries, removes orphaned ephemeral proxies, binds enabled listeners, and creates disabled placeholders for disabled entries.

The store uses several hardening rules:

- Atomic save with temp file, `fsync`, close, and rename.
- `O_NOFOLLOW` on read and temp-write paths where the platform supports it.
- Recovery from `proxies.json.tmp` when the main file is corrupt.
- Permission warning when `proxies.json` is more permissive than `0600`.
- Proxy name validation with `^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`.

Port conflicts are runtime state, not a reason to delete desired state. A configured proxy enters `port_conflict`, then `RetryPortConflicts()` attempts to rebind it on the same 30-second ticker used by proxy status polling.

### Alternatives Considered

**Treat active listeners as source of truth** would lose intent on restart or failed bind.

**Persist full runtime status** would make stale state authoritative. Peer connectedness and bound listeners are facts to reconcile, not facts to trust from disk.

**Use best-effort JSON writes** was rejected because a crash during save could lose the proxy set.

**Allow arbitrary names** was rejected because names appear in paths, logs, CLI output, and API routes.

### Consequences

- Operators can declare proxy intent once and let the daemon reconcile it.
- Disabled entries remain visible without binding local ports.
- Port conflicts become recoverable when the local port becomes free.
- The persistence layer is hardened without becoming the authorization layer.
- The runtime map and persisted store have a clear boundary: desired state versus active listener state.

### Physical Verification

Code verification confirmed the store and reconciliation path. `proxyStore.save()` writes `proxies.json.tmp`, fsyncs it, closes it, and renames it into place. `readFileNoFollow()` opens the store with `O_NOFOLLOW` and warns on permissive modes. `recoverFromTmp()` restores from a valid temp file when the main file is corrupt. `RestoreProxies()` restores enabled entries after daemon startup and leaves disabled entries as placeholders.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/daemon/proxy_store.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/server.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_daemon.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_proxy.go`

---

## ADR-PP03: Event-Driven Reconnect With GATETIME Stability And Poll Fallback

| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Accepted |
| **Commit** | 42c8655 |

### Context

A persistent proxy has to track peer reachability without pretending that the listener itself proves the remote path is healthy. A local listener can be bound while the peer is offline. A peer can reconnect before the event subscription starts. A relay path can appear through reconnect or path dialing without delivering the expected connectedness event.

The unstable case also needed a guardrail. If the peer connects and disconnects repeatedly within a short window, endlessly flipping the proxy back to active is noisy and misleading.

### Decision

Use event-driven status as the primary mechanism and polling as the safety net.

`startProxyEventLoop()` subscribes to libp2p `EvtPeerConnectednessChanged`. `OnPeerConnected()` moves matching persistent proxies from `waiting` or `port_conflict` to `active` once a listener exists. `OnPeerDisconnected()` moves active proxies back to `waiting`.

The GATETIME rule is about peer connection stability, not listener startup. If a peer connection dies within 30 seconds of becoming active, `quickDeathCount` increments. After 3 rapid peer-connection deaths, the proxy status becomes `error: peer connection unstable (3 rapid failures)`.

Bootstrap and missed-event cases have explicit coverage:

- `DetectAlreadyConnected()` runs once after restore and event subscription setup to catch peers that connected during daemon bootstrap.
- `PollProxyStatus()` runs every 30 seconds to correct missed connectedness events, including relay connections established through path dialing or reconnect.
- `RetryPortConflicts()` runs on the same 30-second ticker, so port conflict recovery and status reconciliation share one periodic loop.

### Alternatives Considered

**Only poll peer status** would work eventually, but it would add unnecessary delay to normal reconnects.

**Only trust event delivery** would miss bootstrap races and relay-path transitions that do not surface through the expected event path.

**Treat listener lifetime as stability** was rejected. The failure being detected is peer connection churn; the listener can be perfectly stable while the remote path is not.

**Retry forever on rapid reconnect loops** would hide real instability and keep presenting an unhealthy proxy as normal.

### Consequences

- Normal reconnects are event-driven and fast.
- Bootstrap races are handled once without waiting for the next poll.
- Missed events are corrected by a 30-second fallback.
- Rapid peer connection churn turns into a visible error instead of silent flapping.
- Port-conflict retry and peer-status polling share one timer, reducing background work.

### Physical Verification

On 2026-04-21, physical testing of an interactive TCP session through a relay path measured about 90ms latency. After a peer reconnect, the daemon-owned proxy recovered in about 3 seconds. Code verification confirmed the 30-second `proxyGateTime`, 3 rapid-failure threshold, `DetectAlreadyConnected()`, and shared 30-second ticker for `RetryPortConflicts()` plus `PollProxyStatus()`.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/server.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/proxy.go`

---

## ADR-PP04: Authorization Cleanup Is Chained Into Persistent Services

| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Accepted |
| **Commit** | 42c8655 |

### Context

Persistent local services must not outlive peer authorization. Removing a peer from the watchlist or authorization set should also make any persistent proxy targeting that peer stop presenting itself as usable.

At the same time, proxy cleanup is not the authority layer. The existing peer-manager and path-protection cleanup still own their own responsibilities. Proxy cleanup has to chain after that work without replacing it.

### Decision

Add `OnPeerDeauthorized()` to the daemon proxy manager. When a target peer is deauthorized, matching persistent proxies move to `error: peer not authorized`.

Wire it by chaining the existing `PeerManager` watchlist-removal callback. `startProxyEventLoop()` retrieves the existing callback, installs a new callback, calls the existing callback first, then calls `srv.OnPeerDeauthorized(pid)`.

### Alternatives Considered

**Leave proxies in waiting state** would make an authorization removal look like ordinary peer absence.

**Replace the existing watchlist-removal callback** would risk dropping PathProtector cleanup.

**Delete proxy entries automatically** was rejected because configuration intent and authorization failure are separate. The operator may reauthorize the peer later or inspect the error first.

### Consequences

- Authorization removal becomes visible in proxy status.
- Existing peer cleanup still runs first.
- Persistent proxy cleanup follows the authority decision; it does not make the decision.
- The daemon preserves configured entries while preventing them from looking active.

### Physical Verification

Code verification confirmed the callback chain in `startProxyEventLoop()`: existing watchlist-removal behavior is called before daemon proxy cleanup. `OnPeerDeauthorized()` resolves configured proxy targets and marks matching persistent proxies as `error: peer not authorized`.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/server.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/peermanager.go`

---

## ADR-PP05: Boundaries And Non-Goals

| | |
|---|---|
| **Date** | 2026-04-21 |
| **Status** | Accepted |
| **Commit** | 42c8655 |

### Context

Persistent proxying can be mistaken for a networking guarantee because it keeps local listeners alive and reconnects when peers become reachable. That would be the wrong contract. It is a daemon-owned lifecycle layer for local proxy services, not a replacement for Shurli's authority, routing, or transport layers.

### Decision

Keep persistent proxy scope narrow:

- It does not replace peer authorization, service access control lists, grants, relay budget checks, transport policy, path selection, or peer availability.
- It does not make an unauthorized peer reachable.
- It does not guarantee a relay or direct path exists.
- It retries and reconciles local listeners and service dial attempts when policy and reachability allow them.

The SDK proxy layer still enforces local resource bounds and connection hygiene:

- `DefaultMaxProxyConns = 64` limits accepted local connections per proxy through `netutil.LimitListener`.
- Accepted TCP connections enable keepalive with a 30-second period.
- `DialWithRetry()` uses exponential backoff starting at 1 second, capped at 60 seconds.
- Persistent proxies use 5 retries around service dialing.
- `GracefulClose()` stops accepting, sets deadlines on active connections, waits for handlers, then force-closes stragglers if needed.
- `InstrumentedBidirectionalProxy()` preserves Prometheus accounting for proxy connections, bytes, active connections, and duration when metrics are configured.

### Alternatives Considered

**Treat persistent proxy as reachability guarantee** would create false expectations. A local listener cannot create authorization or network paths.

**Move access checks into proxy persistence** would duplicate policy and risk drift from the service and peer authorization layers.

**Remove resource bounds because the listener is local** was rejected. Local listeners can still be abused by local users or automation loops.

### Consequences

- Persistent proxy status is operational state, not an authority decision.
- Failures remain diagnosable as authorization, reachability, port conflict, or unstable peer connection.
- Local resource use is bounded even when a proxy is persistent.
- Future proxy improvements must preserve the separation between desired local service lifecycle and remote access policy.

### Physical Verification

Code verification confirmed the operational limits and retry values: connection cap 64, TCP keepalive 30 seconds, exponential dial backoff from 1 second to a 60-second cap, 5 retries for persistent daemon proxies, and graceful listener close with deadlines. These are implementation invariants, not throughput benchmarks.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/proxy.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/server.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/handlers.go`, `https://github.com/shurlinet/shurli/blob/main/internal/daemon/types.go`

---

## Public Notes

This journal omits private topology, node names, peer IDs, addresses, hostnames, provider details, device names, usernames, local paths, and command outputs. Physical numbers are included only where they explain architecture: an interactive relay-path session measured about 90ms latency, and daemon-owned proxy reconnect after peer reconnect was about 3 seconds. These numbers validate the persistent lifecycle behavior; they are not formal benchmarks.
