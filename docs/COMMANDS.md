# Commands

Shurli ships as a single binary with 33 subcommands. All commands support `--config <path>` to specify a config file.

## Daemon

| Command | Description |
|---------|-------------|
| `shurli daemon` | Start the daemon (P2P host + Unix socket control API) |
| `shurli daemon status [--json]` | Query running daemon status |
| `shurli daemon stop` | Graceful shutdown |
| `shurli daemon ping <target> [-c N] [--json]` | Ping a peer via daemon |
| `shurli daemon services [--json]` | List exposed services via daemon |
| `shurli daemon peers [--all] [--json]` | List connected peers (shurli-only by default) |
| `shurli daemon connect --peer <p> --service <s> --listen <addr>` | Create a TCP proxy via daemon |
| `shurli daemon paths [--json]` | Show connection paths for each peer |
| `shurli daemon disconnect <id>` | Tear down a proxy |

## Network Tools (standalone, no daemon required)

| Command | Description |
|---------|-------------|
| `shurli ping <target> [-c N] [--interval 1s] [--json]` | P2P ping with stats |
| `shurli traceroute <target> [--json]` | P2P traceroute through relay hops |
| `shurli resolve <name> [--json]` | Resolve a name to peer ID and addresses |
| `shurli proxy <target> <service> <local-port>` | Forward a local TCP port to a remote service |

## Identity & Access

| Command | Description |
|---------|-------------|
| `shurli whoami` | Show your peer ID |
| `shurli auth add <peer-id> [--comment "..."]` | Authorize a peer |
| `shurli auth list` | List authorized peers |
| `shurli auth remove <peer-id>` | Revoke a peer |
| `shurli auth validate` | Validate authorized_keys format |

## Configuration & Setup

| Command | Description |
|---------|-------------|
| `shurli init` | Interactive setup wizard (config, keys, authorized_keys) |
| `shurli config validate` | Validate config file |
| `shurli config show` | Show resolved configuration |
| `shurli config set <key> <value> [--duration 10m]` | Set a config value (dotted path, e.g. `network.force_private_reachability true`) |
| `shurli config rollback` | Restore last-known-good config |
| `shurli config apply <file> [--confirm-timeout 5m]` | Apply config with auto-revert safety net |
| `shurli config confirm` | Confirm applied config (cancels auto-revert) |

## Pairing

| Command | Description |
|---------|-------------|
| `shurli invite [--name "home"] [--non-interactive]` | Generate invite code + QR, wait for join |
| `shurli join <code> [--name "laptop"] [--non-interactive]` | Accept invite or relay pairing code, auto-configure |
| `shurli verify <peer>` | Verify peer identity via SAS fingerprint (4-emoji + numeric) |
| `shurli status` | Show local config, identity, authorized peers, services, names |
| `shurli version` | Show version, commit, build date, Go version |

## File Transfer

| Command | Description |
|---------|-------------|
| `shurli send <file> <peer> [--follow] [--no-compress] [--streams N] [--priority P] [--quiet] [--silent] [--json]` | Send a file to a peer. Fire-and-forget by default (exits immediately). `--follow` stays attached for inline progress. `--streams` sets parallel stream count (0 = auto). `--priority` sets queue priority (low/normal/high). |
| `shurli transfers [--watch] [--history] [--max N] [--json]` | List pending, active, and completed transfers. `--watch` for live feed (refreshes every 2s). `--history` shows the structured event log. |
| `shurli accept <id\|--all> [--dest /path/] [--json]` | Accept a pending incoming transfer. `--all` accepts all pending at once. `--dest` overrides the default receive directory. |
| `shurli reject <id\|--all> [--reason space\|busy\|size] [--json]` | Reject a pending incoming transfer. `--reason` announces the rejection reason to the sender. |
| `shurli cancel <id> [--json]` | Cancel a queued or active transfer. |

## Selective Sharing

| Command | Description |
|---------|-------------|
| `shurli share add <path> [--to peer] [--peers id1,id2] [--persist] [--json]` | Share a file or directory. `--to` for a single peer, `--peers` for multiple. Without either, all authorized peers have access. `--persist` survives daemon restarts. |
| `shurli share remove <path> [--json]` | Stop sharing a path. |
| `shurli share list [--json]` | List all shared paths with type and peer restrictions. |
| `shurli browse <peer> [<path>] [--path /sub/dir] [--json]` | Browse files shared by a remote peer. Path can be positional or via `--path` flag. |
| `shurli download <peer>:<path> [--dest dir] [--follow] [--multi-peer] [--peers list] [--quiet] [--silent] [--json]` | Download from a peer's shares. `--multi-peer` with `--peers` enables RaptorQ multi-source swarming. |

## Identity Security

| Command | Description |
|---------|-------------|
| `shurli recover [--relay] [--dir path]` | Recover identity from BIP39 seed phrase |
| `shurli change-password [--dir path]` | Change identity password |
| `shurli lock` | Lock daemon (disable sensitive operations until unlocked) |
| `shurli unlock` | Unlock a locked daemon with password verification |
| `shurli session refresh` | Rotate session token |
| `shurli session destroy` | Delete session token |

## Services

| Command | Description |
|---------|-------------|
| `shurli service add <name> <address>` | Expose a local TCP service to authorized peers |
| `shurli service remove <name>` | Remove a service |
| `shurli service enable <name>` | Re-enable a disabled service |
| `shurli service disable <name>` | Disable a service without removing its config |
| `shurli service list` | List configured services |

## Diagnostics

| Command | Description |
|---------|-------------|
| `shurli doctor` | Check installation health |
| `shurli doctor --fix` | Auto-fix common issues |
| `shurli completion [bash\|zsh\|fish]` | Generate shell completions |
| `shurli man` | Display the man page |
| `shurli help` | Show help |

## Target Resolution

The `<target>` in network commands accepts either a peer ID or a friendly name from the `names:` section of your config (e.g., `home`, `laptop`, `gpu-server`).

## Daemon Mode

The daemon runs as a long-lived background process. It starts the full P2P host, exposes configured services, and opens a Unix socket API for management.

**Key features:**
- Unix socket at `~/.config/shurli/shurli.sock` (no TCP exposure)
- Cookie-based auth (`~/.config/shurli/.daemon-cookie`) - 32-byte random token, rotated per restart
- Hot-reload of authorized_keys via `daemon` auth endpoints
- 38 REST endpoints for status, peers, services, auth, proxies, ping, traceroute, resolve, paths, file transfers, shares, config reload

**Example:**
```bash
# Start the daemon
shurli daemon

# In another terminal - query status
shurli daemon status

# Create a proxy through the daemon
shurli daemon connect --peer home --service ssh --listen localhost:2222
```

For the full API reference: [DAEMON-API.md](DAEMON-API.md)

## Configuration

### Config Search Order

1. `--config <path>` flag (explicit)
2. `./shurli.yaml` (current directory)
3. `~/.config/shurli/config.yaml` (standard location, created by `shurli init`)
4. `/etc/shurli/config.yaml` (system-wide)

### Essential Config

```yaml
identity:
  key_file: "identity.key"

network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
    - "/ip4/0.0.0.0/udp/0/quic-v1"
  force_private_reachability: false  # true for servers behind CGNAT

relay:
  addresses:
    - "/ip4/YOUR_VPS_IP/tcp/7777/p2p/YOUR_RELAY_PEER_ID"

security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true

# services:       # Uncomment to expose services (server only)
#   ssh:
#     enabled: true
#     local_address: "localhost:22"

names: {}         # Map friendly names to peer IDs
#  home: "12D3KooW..."
```

Full sample configs: [configs/](../configs/)
