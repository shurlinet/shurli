---
title: "Inviting Peers"
weight: 7
description: "Two ways to bring people onto your Shurli network: real-time pairing codes for instant setup, or async invite deposits that wait until your friend is ready."
---

Your relay is deployed and secured. Now you need to get people onto your network. Shurli offers two invitation methods, each suited to different situations.

## Prerequisites

- Relay deployed and running (see [Relay Setup](../relay-setup/))
- Vault initialized and unsealed (see [Securing Your Relay](../relay-security/))
- You are an admin on the relay (the first peer is auto-promoted)

## Two ways to invite

| | Pairing codes | Invite deposits |
|--|---------------|-----------------|
| **When to use** | You and your friend are both available now | Your friend will join later (different timezone, busy) |
| **How it works** | Share a code, friend joins immediately | Leave a "message" on the relay, friend picks it up anytime |
| **Timing** | Both online at the same time | Async, no coordination needed |
| **Expiry** | TTL (default 1 hour) | TTL (configurable, or never) |
| **Restrictions** | Peer expiry only | Full macaroon caveats (service, group, action, etc.) |
| **Command** | `shurli relay pair` | `shurli relay invite create` |

## Method 1: Pairing codes (real-time)

Best for: "Hey, run this command right now."

### Create a code

On the relay server:

```bash
# Single code, valid for 1 hour (default)
shurli relay pair

# Multiple codes for a group of 3
shurli relay pair --count 3 --ttl 2h

# With peer authorization expiry (peers auto-expire after 24 hours)
shurli relay pair --count 5 --ttl 1h --expires 86400
```

The command outputs a pairing code and a group ID. Share the code with your friend through any channel (message, email, phone call).

### Friend joins

On your friend's machine:

```bash
shurli join <pairing-code> --name laptop
```

What happens behind the scenes:

1. Friend's device connects to the relay over P2P
2. The relay validates the pairing code (16-byte token, one-time use)
3. Both sides compute an HMAC proof (proves the friend held a valid code)
4. The relay adds the friend to `authorized_keys` with `member` role
5. The friend receives a list of other peers already in the group
6. SAS verification fingerprints are shown for identity confirmation

### Manage pairing groups

```bash
# List active pairing groups
shurli relay pair --list

# Revoke a group (invalidates all remaining codes)
shurli relay pair --revoke <group-id>
```

## Method 2: Invite deposits (async)

Best for: "Here's an invite, use it whenever you're ready."

### Create a deposit

On the relay server:

```bash
# Basic invite, never expires
shurli relay invite create

# With restrictions and 72-hour TTL
shurli relay invite create --ttl 259200 --caveat "service=proxy;peers_max=1"
```

The command outputs a deposit ID and the encoded macaroon token. Share the deposit ID with your friend.

### Friend joins

Your friend can use the deposit whenever they're ready:

```bash
shurli join --deposit <deposit-id> --relay /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
```

### Manage deposits

While the invite sits waiting, you can:

```bash
# List all deposits with their status
shurli relay invite list

# Add more restrictions (tighten the rules)
shurli relay invite modify <id> --add-caveat "service=proxy"

# Revoke entirely (friend can no longer use it)
shurli relay invite revoke <id>
```

### Deposit lifecycle

Every deposit moves through these states:

```
pending  ──→  consumed   (friend used it)
   │
   ├──→  revoked    (you cancelled it)
   │
   └──→  expired    (TTL ran out)
```

Once consumed, the deposit cannot be reused. This is enforced by the macaroon's single-use design, not by a database flag that could be tampered with.

## Smart keys: restrictions that stick

Both invitation methods create macaroon tokens under the hood. What makes macaroons special is attenuation: you can add restrictions but never remove them. This is enforced by cryptographic HMAC chains, not by software rules.

### Example workflow

```bash
# Create a generous invite
shurli relay invite create --ttl 604800

# Realize you only want proxy access
shurli relay invite modify <id> --add-caveat "service=proxy"

# Limit to 1 person
shurli relay invite modify <id> --add-caveat "peers_max=1"

# The invite now carries both restrictions. Your friend
# gets proxy access only, for 1 person, expiring in 7 days.
```

You can keep adding restrictions. You cannot remove the ones you already added.

### Seven restriction types

| Restriction | Example | What it controls |
|-------------|---------|-----------------|
| `service` | `service=proxy,ssh` | Which services the key grants access to |
| `group` | `group=family` | Which group the key belongs to |
| `action` | `action=invite,connect` | What actions are allowed |
| `peers_max` | `peers_max=5` | Maximum number of people this key can onboard |
| `delegate` | `delegate=false` | Whether the key holder can create sub-keys |
| `expires` | `expires=2026-04-01T00:00:00Z` | When the key stops working |
| `network` | `network=home` | Which network namespace the key works in |

Unknown restriction types are rejected (fail-closed). A token cannot be bypassed by inventing a restriction type that doesn't exist yet.

## Invite policy

By default, only admins can create invites (`admin-only` policy). You can change this:

```yaml
# In relay-server.yaml
security:
  invite_policy: "open"   # any connected peer can invite
```

Or keep the default:

```yaml
security:
  invite_policy: "admin-only"  # only admins can invite (default)
```

> **Recommendation**: Start with `admin-only`. Switch to `open` only if you want a community-managed network where any member can bring in new people.

## Verify your peer

After a peer joins, Shurli shows SAS (Short Authentication String) verification:

```
Peer: 12D3KooWLqK...
SAS:  🌊 🔥 🌲 🎯
```

Compare the 4-emoji fingerprint with your friend over a trusted channel (phone call, in person, video chat). If they match, the connection is authentic. If they don't, someone is intercepting the connection.

Peers show an `[UNVERIFIED]` badge until you manually verify them:

```bash
shurli verify <peer-name-or-id>
```

After verification, the badge changes to `[verified=2026-03-01]`.

## Troubleshooting

| Issue | Cause | Solution |
|-------|-------|----------|
| "pairing failed" | Code expired or already used | Generate a new code: `shurli relay pair` |
| "vault is sealed" | Vault locked, can't create invites | Unseal first: `shurli relay vault unseal` |
| "invite deposit not found" | Wrong deposit ID or already consumed | Check with `shurli relay invite list` |
| "unauthorized" | You're not an admin | Ask an admin to create the invite, or change invite policy |
| Friend can't reach relay | Wrong address or firewall | Check relay address: `shurli relay info` |
| "invite deposit expired" | TTL ran out before friend joined | Create a new deposit with longer TTL |
| TOTP code rejected | Clock skew or wrong code | Sync your device clock, try current code |

---

**Next step**: [Managing Your Network](../managing-network/) - understand roles, manage peers, and handle day-to-day operations.
