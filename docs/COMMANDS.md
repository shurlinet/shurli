# Commands

Shurli ships as a single binary with subcommands. All commands support `--config <path>` to specify a config file.

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
| `shurli auth set-attr <peer-id> <key> <value>` | Set peer attribute (role, group, verified, bandwidth_budget) |

## Configuration & Setup

| Command | Description |
|---------|-------------|
| `shurli init` | Interactive setup wizard (config, keys, authorized_keys) |
| `shurli config validate` | Validate config file |
| `shurli config show` | Show resolved configuration |
| `shurli config set <key> <value> [--duration 10m]` | Set a config value (dotted path, e.g. `network.force_private_reachability true`) |
| `shurli config reload` | Trigger daemon to reload config from disk |
| `shurli config rollback` | Restore last-known-good config |
| `shurli config apply <file> [--confirm-timeout 5m]` | Apply config with auto-revert safety net |
| `shurli config confirm` | Confirm applied config (cancels auto-revert) |

## Pairing

| Command | Description |
|---------|-------------|
| `shurli invite [--as "home"] [--non-interactive]` | Generate invite code + QR, wait for join |
| `shurli join <code> [--as "laptop"] [--non-interactive]` | Accept invite or relay pairing code, auto-configure |
| `shurli verify <peer>` | Verify peer identity via SAS fingerprint (4-emoji + numeric) |
| `shurli status` | Show local config, identity, authorized peers, relay grants, services, names |
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
| `shurli share add <path> [--to peer] [--peers id1,id2] [--persist] [--json]` | Share a file or directory. `--to` for a single peer, `--peers` for multiple. Adding to an existing share appends peers. Without `--to`/`--peers`, all authorized peers have access. `--persist` survives daemon restarts. |
| `shurli share deny <path> <peer> [--json]` | Remove a peer from a share's peer list. |
| `shurli share remove <path> [--json]` | Stop sharing a path entirely. |
| `shurli share list [--json]` | List all shared paths with type and peer names. |
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

## Relay Server (operator commands)

### Client-side relay config

| Command | Description |
|---------|-------------|
| `shurli relay add <multiaddr>` | Add a relay address to config |
| `shurli relay list` | List configured relay addresses |
| `shurli relay remove <multiaddr>` | Remove a relay address from config |
| `shurli relay seeds` | Show bootstrap seed addresses |

### Server-side relay management

| Command | Description |
|---------|-------------|
| `shurli relay serve [--config path]` | Start the relay server |
| `shurli relay setup` | Interactive relay setup wizard |
| `shurli relay show` | Show relay server config |
| `shurli relay authorize <peer-id>` | Authorize a peer on relay |
| `shurli relay deauthorize <peer-id>` | Deauthorize a peer on relay |
| `shurli relay set-attr <peer-id> <key> <value>` | Set peer attribute (role, bandwidth_budget, etc.) |
| `shurli relay list-peers` | List authorized peers on relay |
| `shurli relay info` | Show relay identity and status |
| `shurli relay version` | Show relay version |
| `shurli relay config <subcommand>` | Relay config management |
| `shurli relay recover` | Recover relay identity from seed phrase |
| `shurli relay verify <peer>` | Verify relay peer identity |

### Relay grants

| Command | Description |
|---------|-------------|
| `shurli relay grant <peer-id> <plugin> [--duration 24h]` | Grant plugin access to a peer on relay |
| `shurli relay grants [--json]` | List all grants on relay |
| `shurli relay revoke <peer-id> <plugin>` | Revoke a grant on relay |
| `shurli relay extend <peer-id> <plugin> [--duration 24h]` | Extend a grant on relay |

### Relay grant receipts in `shurli status`

When the daemon is running, `shurli status` displays a `Relay Grants:` section showing cached grant receipts received from relays via the `/shurli/grant-receipt/1.0.0` protocol.

**Output format per relay**:

```
Relay Grants:
  my-relay: active, expires in 23h45m, 2.0 GB/session (1.2 GB used), 2h0m0s/circuit
  backup-relay: permanent, unlimited/session, 2h0m0s/circuit
  signal-relay: no grant (signaling only)
```

| Field | Meaning |
|-------|---------|
| `active` / `permanent` | Whether the grant has a time limit or is permanent |
| `expires in <duration>` | Time remaining before the grant expires (omitted for permanent grants) |
| `<size>/session` | Per-direction data budget for the current circuit session (`unlimited` if no limit) |
| `(<size> used)` | Cumulative bytes sent and received on the current circuit (omitted if zero) |
| `<duration>/circuit` | Maximum duration of a single relay circuit session (omitted if unlimited) |
| `no grant (signaling only)` | Relay is configured but no grant receipt has been received. The relay can be used for peer discovery and signaling but not for data transfer. |

Grant receipts are cached locally in `~/.shurli/grant_cache.json` and survive daemon restarts. Per-circuit usage counters reset when a new circuit is established.

### Relay invites

| Command | Description |
|---------|-------------|
| `shurli relay invite create [--count N] [--ttl 10m]` | Generate pairing codes |
| `shurli relay invite list` | List active invite codes |
| `shurli relay invite revoke <code>` | Revoke an invite code |
| `shurli relay invite modify <code> [--add-caveat ...]` | Add caveats to an invite |

### Relay vault

| Command | Description |
|---------|-------------|
| `shurli relay vault init` | Initialize vault with passphrase |
| `shurli relay vault seal` | Seal the vault (lock root key) |
| `shurli relay vault unseal` | Unseal the vault (unlock root key) |
| `shurli relay vault status` | Show vault seal status |
| `shurli relay seal` | Shorthand for vault seal |
| `shurli relay unseal [--remote <peer>]` | Shorthand for vault unseal (supports remote) |
| `shurli relay seal-status` | Shorthand for vault status |

### Relay MOTD and goodbye

| Command | Description |
|---------|-------------|
| `shurli relay motd set <message>` | Set relay message of the day |
| `shurli relay motd clear` | Clear relay MOTD |
| `shurli relay motd status` | Show current MOTD |
| `shurli relay goodbye set <message>` | Set goodbye message (maintenance warning) |
| `shurli relay goodbye retract` | Retract goodbye message |
| `shurli relay goodbye shutdown [--grace 5m]` | Send goodbye and shut down relay |

### Relay ZKP

| Command | Description |
|---------|-------------|
| `shurli relay zkp-setup [--seed]` | Generate ZKP proving/verifying keys |
| `shurli relay zkp-test` | Test ZKP proof generation and verification |

## Plugins

| Command | Description |
|---------|-------------|
| `shurli plugin list [--json]` | List installed plugins with status |
| `shurli plugin enable <name>` | Enable a plugin |
| `shurli plugin disable <name>` | Disable a plugin |
| `shurli plugin info <name>` | Show plugin details |
| `shurli plugin disable-all` | Emergency kill switch - disable all plugins |

## Reconnect

| Command | Description |
|---------|-------------|
| `shurli reconnect <peer> [--json]` | Force reconnect to a peer via daemon (resets backoff) |

## Notifications

| Command | Description |
|---------|-------------|
| `shurli notify test [--json]` | Send a test notification to all configured sinks |
| `shurli notify list [--json]` | List configured notification sinks |

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
- Unix socket at `~/.shurli/shurli.sock` (no TCP exposure)
- Cookie-based auth (`~/.shurli/.daemon-cookie`) - 32-byte random token, rotated per restart
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
3. `~/.shurli/config.yaml` (standard location, created by `shurli init`)
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
