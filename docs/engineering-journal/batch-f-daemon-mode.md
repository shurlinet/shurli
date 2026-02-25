# Batch F: Daemon Mode

Unix socket IPC, cookie authentication, RuntimeInfo interface, and hot-reload authorized_keys.

---

### ADR-F01: Unix Socket (Not TCP)

**Context**: The daemon needs a control API for CLI subcommands (`shurli daemon status`, `shurli daemon ping`, etc.). Need an IPC mechanism.

**Alternatives considered**:
- **TCP on localhost** - Universal, works on all platforms. Rejected because (a) any local process can connect (no filesystem permissions), (b) port conflicts with other services, (c) potentially exposed if firewall misconfigured.
- **Named pipes** - Windows-friendly. Rejected because they don't support HTTP natively and complicate the implementation.
- **gRPC** - Type-safe, bi-directional streaming. Rejected because it adds protobuf dependency, code generation, and binary size. HTTP+JSON is simpler and sufficient.

**Decision**: Unix domain socket at `~/.config/shurli/shurli.sock` with HTTP/1.1 over it. Socket created with `umask(0077)` to ensure `0700` permissions atomically (no TOCTOU race between `Listen()` and `Chmod()`). Stale socket detection: try connecting first, only remove if connection fails.

**Consequences**: Unix-only (no Windows support for now). Accepted because Shurli's target users are Linux/macOS. Socket permissions enforce that only the owning user can connect. The HTTP layer means standard tools (`curl --unix-socket`) work for debugging.

**Reference**: `internal/daemon/server.go:86-138`

---

### ADR-F02: Cookie Auth (Not mTLS)

**Context**: Even with socket permissions, the API needs authentication to prevent attacks via symlink races or debugger attachment.

**Alternatives considered**:
- **mTLS** - Strong mutual authentication. Rejected because it requires certificate management, key generation, and trust store configuration - too complex for a local IPC mechanism.
- **Token in socket filename** - Embed the token in the path. Rejected because path-based auth is fragile and leaks the token in `ps` output and logs.
- **No auth** (rely on socket permissions) - Rejected because defense-in-depth requires authentication even when filesystem permissions are correct.

**Decision**: 32-byte random hex cookie written to `~/.config/shurli/.daemon-cookie` with `0600` permissions. CLI reads the cookie and sends it as `Authorization: Bearer <token>`. Cookie is rotated every daemon restart. Written AFTER socket is secured (ordering prevents clients from reading cookie before socket is ready).

**Consequences**: Simple, fast, no crypto libraries needed. The cookie file is the single secret - protect it like an SSH private key. If compromised, restart the daemon to rotate.

**Reference**: `internal/daemon/server.go:88-116`, `internal/daemon/client.go`

---

### ADR-F03: RuntimeInfo Interface

**Context**: The daemon server needs access to the P2P network, config paths, version info, and connection methods. But the daemon package shouldn't import `cmd/shurli`.

**Alternatives considered**:
- **Pass individual fields** - `NewServer(network, configPath, authKeys, version, ...)`. Rejected because the parameter list would grow with every new feature.
- **Share a struct directly** - Import the runtime struct from cmd. Rejected because it creates a circular dependency between `internal/daemon` and `cmd/shurli`.

**Decision**: `daemon.RuntimeInfo` interface with methods: `Network()`, `ConfigFile()`, `AuthKeysPath()`, `GaterForHotReload()`, `Version()`, `StartTime()`, `PingProtocolID()`, `ConnectToPeer()`. The `serveRuntime` struct in `cmd/shurli/cmd_daemon.go` implements it.

**Consequences**: Clean dependency direction (daemon depends on interface, not concrete type). Easy to mock in tests (`mockRuntime`). Adding new runtime capabilities means adding methods to the interface - intentionally explicit.

**Reference**: `internal/daemon/server.go:23-32`, `cmd/shurli/cmd_daemon.go:23-28`

---

### ADR-F04: Hot-Reload `authorized_keys`

**Context**: Adding or removing peers via `shurli daemon auth add/remove` should take effect immediately without restarting the daemon.

**Alternatives considered**:
- **File watcher (fsnotify)** - Watch the file for changes. Rejected because it adds a dependency and doesn't help with API-triggered changes (where we already know when to reload).
- **Restart required** - Simpler but terrible UX. Rejected.

**Decision**: `GaterReloader` interface with `ReloadFromFile()` method. When the daemon API adds/removes a peer from the `authorized_keys` file, it immediately calls `ReloadFromFile()`, which re-reads the file and calls `gater.UpdateAuthorizedPeers()` with the new map. The gater uses `sync.RWMutex` for concurrent safety.

**Consequences**: Changes are atomic (read file, swap map under lock). No file watching needed. The gater's `authorizedPeers` map is replaced entirely - no incremental updates. This is fine because the authorized_keys file is small (typically <100 entries).

**Reference**: `cmd/shurli/cmd_daemon.go:37-51`, `internal/auth/gater.go:74-79`
