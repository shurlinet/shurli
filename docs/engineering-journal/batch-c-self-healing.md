# Batch C: Self-Healing

Config archive/rollback, commit-confirmed pattern, and watchdog with pure-Go sd_notify.

---

### ADR-C01: Config Archive/Rollback (Juniper-Inspired)

**Context**: A bad config change on a remote node (e.g., wrong relay address) could make it permanently unreachable. Need a recovery mechanism.

**Alternatives considered**:
- **Git-based config history** - Track config in a git repo. Rejected because it requires git installed and adds complexity.
- **Numbered backups** (config.1, config.2, ...) - More history but harder to manage cleanup.

**Decision**: Juniper-style last-known-good: `Archive()` copies current config to `.config.last-good.yaml` with atomic write (temp file + rename). `Rollback()` restores it. Single backup slot - simple, sufficient.

**Consequences**: Only one rollback level (no multi-step undo). Accepted because the common case is "my last change broke it, undo that one change." The archive is created before daemon start and before config apply.

**Reference**: `internal/config/archive.go`

---

### ADR-C02: Commit-Confirmed Pattern

**Context**: Changing config on a remote node is dangerous - if the new config prevents connectivity, you're locked out. Network engineers solve this with "commit confirmed" - apply the change, and if you don't confirm within N minutes, it auto-reverts.

**Alternatives considered**:
- **Manual rollback only** - User must SSH in (if they can) and run `shurli config rollback`. Rejected because if the config broke SSH access, there's no way in.
- **Two-phase commit** - More complex, requires coordination. Rejected as over-engineering for a single-node config change.

**Decision**: `shurli config apply <new> --confirm-timeout 5m` backs up current config, applies new config, starts a timer. If `shurli config confirm` isn't run within the timeout, the daemon reverts to the backup and restarts via `exitFunc(1)` (systemd restarts it with the restored config).

**Consequences**: Requires systemd (or equivalent) to restart on exit. The `exitFunc` is injectable for testing (`EnforceCommitConfirmed` takes `func(int)` instead of calling `os.Exit` directly).

**Reference**: `internal/config/confirm.go`, `cmd/shurli/cmd_config.go`

---

### ADR-C03: Watchdog + sd_notify (Pure Go)

**Context**: The daemon needs to report health to systemd and restart on failure. Most Go projects use `coreos/go-systemd` for sd_notify.

**Alternatives considered**:
- **`coreos/go-systemd`** - Mature library. Rejected because it's another dependency for 30 lines of socket code. Also pulls in dbus bindings we don't need.
- **No watchdog** - Let systemd's simple restart handle failures. Rejected because watchdog provides proactive health checking, not just crash recovery.

**Decision**: Pure Go sd_notify implementation in `internal/watchdog/watchdog.go`. Three functions: `Ready()` (READY=1), `Watchdog()` (WATCHDOG=1), `Stopping()` (STOPPING=1). All send datagrams to `$NOTIFY_SOCKET`. No-op when not running under systemd (macOS, manual launch).

The watchdog loop runs configurable health checks (default 30s interval) and only sends WATCHDOG=1 when all checks pass. The daemon adds a socket health check to verify the API is still accepting connections.

**Consequences**: Zero dependency for systemd integration. Works on both Linux (systemd) and macOS (launchd, where sd_notify is a no-op). The health check framework is extensible - Batch H will add libp2p connection health.

**Reference**: `internal/watchdog/watchdog.go`, `cmd/shurli/cmd_daemon.go:158-166`
