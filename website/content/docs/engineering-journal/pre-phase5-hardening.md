---
title: "Pre-Phase 5 Hardening"
weight: 14
description: "Startup race fix, stale address detection, systemd/launchd service deployment."
---
<!-- Auto-synced from docs/engineering-journal/pre-phase5-hardening.md by sync-docs - do not edit directly -->


Bug fixes from cross-network testing, CGNAT detection improvements, and systemd/launchd service deployment.

---

### ADR-K01: Startup Race Condition Fix and CGNAT Detection

**Context**: Cross-network testing on 4 different networks (satellite WiFi, terrestrial WiFi, wired Ethernet, 5G cellular) exposed two bugs in the same area: network awareness.

Bug 1: The relay fired `peer-notify` introductions before the daemon finished registering its `/shurli/peer-notify/1.0.0` handler. First 1-2 delivery attempts failed with "protocols not supported." Root cause: `Bootstrap()` was called before `SetupPeerNotify()`, so the relay saw the peer connect before its stream handlers were ready.

Bug 2: RFC 6598 CGNAT addresses (100.64.0.0/10) on local interfaces were not being detected. The STUN prober reported "hole-punchable" for mobile hotspot connections even when a carrier NAT sat above the inner NAT.

**Decision**: Move `SetupPeerNotify()` before `Bootstrap()` in the startup sequence. All stream handlers must be registered before any peer discovery begins. For CGNAT: detect RFC 6598 addresses on local interfaces, set `BehindCGNAT` flag in STUN results, and cap the reachability grade at D. Two new unit tests cover the CGNAT detection path.

**Consequences**: Clean cold starts on all tested networks. Startup race eliminated. CGNAT detection works for RFC 6598 addresses. Limitation: mobile carriers that use RFC 1918 addresses (172.20.x.x, 10.x.x.x) for CGNAT cannot be distinguished from home networks. This is a fundamental limitation, not a bug - the address ranges overlap.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/stunprober.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/reachability.go`

---

### ADR-K02: Stale Address Detection and Diagnostics

**Context**: After switching from WiFi to 5G cellular, the daemon's address list still contained the old WiFi address. The network monitor detected the interface removal correctly, but the stale address persisted in libp2p's address list. When switching back to WiFi, the daemon stayed on relay because it didn't re-evaluate direct paths.

**Alternatives considered**:
- **Full address lifecycle management** - Remove stale addresses from libp2p, trigger re-discovery. This is the correct long-term fix but requires the PeerManager that Phase 5-L will build.
- **Force restart on network change** - Crude but effective. Violates the "no disruption" principle.

**Decision**: Display-only fix for now. Cross-check `h.Addrs()` against `net.InterfaceAddrs()` and label stale addresses as `[local,stale?]` in status output. Delayed diagnostic log fires 10 seconds after a network change event, giving interfaces time to stabilize. Full address lifecycle management is deferred to Phase 5-L PeerManager.

**Consequences**: Users can see stale addresses in status output and understand why connections stay on relay. The diagnostic log helps debugging. Does not fix the underlying problem (PeerManager will). Avoids premature complexity that would need to be rewritten in Phase 5-L anyway.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/netmonitor.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/serve_common.go`

---

### ADR-K03: systemd and launchd Service Deployment

**Context**: Running Shurli via `nohup` or bare terminal means no automatic restart on crash, no watchdog monitoring, no boot-time startup, and log management via terminal scrollback. For infrastructure that's meant to be always-on, this is unacceptable.

**Decision**: Ship service files for both major platforms so any node (relay or daemon) gets proper process management:

- **Linux (systemd)**: Two unit files provided. `shurli-daemon.service` for daemon nodes, `relay-server.service` for relay nodes. Both use `Type=notify`, `WatchdogSec=90`, `Restart=on-failure`, `RestartSec=5`, security hardening (`NoNewPrivileges`, `ProtectSystem=strict`, `PrivateTmp`), and `LimitNOFILE=65536`.
- **macOS (launchd)**: `com.shurli.daemon.plist` with `RunAtLoad=true`, `KeepAlive=true`. Logs to `/tmp/shurli-daemon.log`.

Both platforms use the pure-Go `sd_notify` implementation in `https://github.com/shurlinet/shurli/blob/main/internal/watchdog/` (no CGo, no-op on non-systemd). The watchdog checks health every 30s, sends `WATCHDOG=1` to systemd on success. `WatchdogSec=90` (3x interval) triggers restart if health checks stop. On macOS, launchd's `KeepAlive` provides equivalent crash recovery.

**Consequences**: Nodes survive crashes, reboots, and power failures without manual intervention. Watchdog detects hung processes. Logs go to journald (Linux) or system log (macOS). Dev workflow: rebuild binary, restart service. Symlink from `/usr/local/bin/shurli` to repo means no file copy needed.

**Reference**: `deploy/shurli-daemon.service`, `deploy/com.shurli.daemon.plist`, `relay-server/relay-server.service`, `https://github.com/shurlinet/shurli/blob/main/internal/watchdog/watchdog.go`
