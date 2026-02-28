---
title: "Managing Your Network"
weight: 8
description: "Day-to-day operations: admin and member roles, viewing and managing peers, relay configuration, and troubleshooting common issues."
---

Your relay is secured and peers are connected. This guide covers daily operations: who can do what, how to manage peers, and what to do when things go wrong.

## Roles: admin vs member

Every authorized peer has a role. The first peer to join a fresh relay is automatically promoted to admin.

| | Admin | Member |
|--|-------|--------|
| **Connect and use the relay** | Yes | Yes |
| **Use proxied services** | Yes | Yes |
| **Create pairing codes** | Yes | Only if invite policy is "open" |
| **Create invite deposits** | Yes | Only if invite policy is "open" |
| **Seal/unseal the vault** | Yes | No |
| **Remote unseal** | Yes | No |
| **Authorize/deauthorize peers** | Yes | No |
| **Change invite policy** | Yes (config file) | No |

> **Auto-promotion**: When the relay starts with zero admins in `authorized_keys`, the first peer to pair is automatically promoted to admin. After that, new peers join as members.

## Viewing your peers

### From the relay server

```bash
# Full peer list with roles, groups, and verification status
shurli relay list-peers
```

Example output:

```
1. [admin]  12D3KooWLqK4ab...  home-node
2. [member] 12D3KooWRtP7cd...  laptop    group=family  [UNVERIFIED]
3. [member] 12D3KooWXyZ9ef...  phone     group=family  [verified=2026-03-01]

3 authorized peers (1 admin, 2 members)
```

### From any device

```bash
# Uses the local config to find the authorized_keys file
shurli auth list
```

The output shows:

- Short peer ID (first 16 characters)
- Role badge: `[admin]` or `[member]`
- Comment (the name you gave when pairing)
- Group affiliation (if set)
- Verification status: `[UNVERIFIED]` or `[verified=DATE]`

## Changing roles

### Promote a member to admin

```bash
shurli auth add 12D3KooWRtP7cd... --role admin
```

If the peer is already authorized, this updates their role without removing their other attributes.

### Demote an admin to member

```bash
shurli auth add 12D3KooWRtP7cd... --role member
```

> **Caution**: Don't demote yourself unless another admin exists. A relay with zero admins can't create invites, unseal the vault remotely, or manage peers.

## Removing peers

```bash
# From a client device
shurli auth remove 12D3KooWXyZ9ef...

# From the relay server
shurli relay deauthorize 12D3KooWXyZ9ef...
```

What happens when you remove a peer:

1. The peer ID is deleted from `authorized_keys`
2. Existing connections from this peer continue until they disconnect (TCP keepalive timeout)
3. When the peer tries to reconnect, the connection gater rejects them
4. The peer needs a new invite to rejoin

> **Relay server note**: After running `shurli relay authorize` or `shurli relay deauthorize`, restart the relay to apply changes: `sudo systemctl restart shurli-relay`

## Adding peers manually

Sometimes you have a peer ID but don't want to go through the invite flow (e.g., adding your own new device):

```bash
# On the relay server
shurli relay authorize 12D3KooWNewDevice... my-new-laptop

# On a client device
shurli auth add 12D3KooWNewDevice... --comment "my-new-laptop" --role member
```

## Relay info

See your relay's identity, addresses, and connection details:

```bash
shurli relay info
```

Example output:

```
Peer ID: 12D3KooWRelay...
Connection gating: enabled
Authorized peers: 3

Public addresses:
  /ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRelay...
  /ip4/203.0.113.50/udp/7777/quic-v1/p2p/12D3KooWRelay...
  /ip6/2001:db8::1/tcp/7777/p2p/12D3KooWRelay...

Quick setup:
  shurli relay add 203.0.113.50:7777 --peer-id 12D3KooWRelay...
```

If `qrencode` is installed, it also displays a QR code for easy mobile setup.

## Validating your auth file

Check for syntax errors or malformed peer IDs:

```bash
shurli auth validate
```

Or validate a specific file:

```bash
shurli auth validate /etc/shurli/relay_authorized_keys
```

Output shows the count of valid peer IDs and any errors with line numbers.

## Configuration reference

Key configuration fields for network management:

### relay-server.yaml (relay side)

| Key | Type | Default | What it does |
|-----|------|---------|-------------|
| `security.invite_policy` | string | `"admin-only"` | Who can create invites: `admin-only` or `open` |
| `security.vault_file` | string | `""` | Path to sealed vault JSON (empty = no vault) |
| `security.auto_seal_minutes` | int | `0` | Auto-reseal timeout (0 = manual only) |
| `security.require_totp` | bool | `false` | Force TOTP for all unseal operations |
| `security.enable_connection_gating` | bool | `true` | Reject unauthorized peers |
| `security.authorized_keys_file` | string | `"relay_authorized_keys"` | Path to the peer allowlist |

### shurli.yaml (client side)

| Key | Type | Default | What it does |
|-----|------|---------|-------------|
| `security.authorized_keys_file` | string | `"authorized_keys"` | Path to the peer allowlist |
| `relay.addresses` | list | `[]` | Relay server multiaddrs |
| `names` | map | `{}` | Peer name to ID mappings |

## Common operations quick reference

| Task | Command |
|------|---------|
| List peers | `shurli auth list` or `shurli relay list-peers` |
| Add peer | `shurli auth add <peer-id> --comment "name"` |
| Remove peer | `shurli auth remove <peer-id>` |
| Promote to admin | `shurli auth add <peer-id> --role admin` |
| View relay info | `shurli relay info` |
| Add relay to config | `shurli relay add <addr> --peer-id <id>` |
| Remove relay | `shurli relay remove <addr>` |
| List relays | `shurli relay list` |
| Validate auth file | `shurli auth validate` |
| Create pairing code | `shurli relay pair --count N --ttl 2h` |
| Create invite deposit | `shurli relay invite create --ttl 86400` |
| List invites | `shurli relay invite list` |
| Seal vault | `shurli relay seal` |
| Unseal vault | `shurli relay unseal` |
| Remote unseal | `shurli relay unseal --remote <addr>` |
| Vault status | `shurli relay seal-status` |

## Troubleshooting

| Issue | Cause | Solution |
|-------|-------|----------|
| Peer can't connect | Not in authorized_keys | `shurli relay authorize <peer-id>` and restart relay |
| "vault is sealed" on invite | Vault locked | Unseal: `shurli relay unseal` |
| TOTP code rejected | Clock skew | Sync your device clock (NTP). TOTP allows +/- 30 seconds |
| Remote unseal fails | Not an admin, or wrong address | Check role with `shurli auth list`, verify address with `shurli relay info` |
| "permanently blocked" on unseal | 11+ failed attempts | SSH to relay and unseal locally: `shurli relay unseal` |
| Peer shows [UNVERIFIED] | SAS not confirmed | Compare emojis and run: `shurli verify <peer>` |
| Invite deposit "not found" | Typo in ID, or expired | Check with `shurli relay invite list` |
| Can't create invites (member) | Invite policy is admin-only | Ask an admin, or change policy to "open" in config |
| Relay address changed | Server IP changed | Update clients: `shurli relay remove <old>` then `shurli relay add <new>` |
| Auth file has errors | Malformed peer IDs | Run `shurli auth validate` to find the problem |

---

**Next step**: [Monitoring](../monitoring/) - set up Prometheus and Grafana to see everything your relay is doing in real time.
