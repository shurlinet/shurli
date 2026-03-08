# Discarded: relay pair CLI command

## What it did

The `shurli relay pair` command generated one-time pairing codes for relay
servers. Each code allowed a new peer to join the relay's authorized set.

Usage:
```
shurli relay pair                              # Generate 1 code (default)
shurli relay pair --count 3 --ttl 2h           # Generate 3 codes, valid 2 hours
shurli relay pair --count 5 --expires 86400    # Codes with 24h auth expiry
shurli relay pair --list                       # List active pairing groups
shurli relay pair --revoke <group-id>          # Revoke a pairing group
shurli relay pair --remote <relay>             # Remote admin
```

The joining peer used: `shurli join <code>`

## Why it was removed

The `relay pair` command was redundant with `relay invite create`. Both
commands created pairing codes using the same underlying system (TokenStore,
PairingHandler, `/shurli/relay-pair/1.0.0` protocol).

`relay invite create` provides a simpler UX for the common case (invite one
peer) while the underlying pairing infrastructure remains intact.

The `relay pair` command exposed implementation details (--count, --namespace,
group IDs) that are unnecessary for most users. `relay invite` hides this
complexity.

## What was kept

The internal pairing system was NOT removed:
- `internal/relay/pairing.go` (PairingHandler, PairingProtocol)
- `internal/relay/tokens.go` (TokenStore, CreateGroup, ValidateAndUse)
- Admin API endpoints (`/v1/pair`, `/v1/pair/{id}`)
- Admin client methods (CreateGroup, ListGroups, RevokeGroup)

These power `relay invite create` under the hood.

## Replacement

```
# Old                                    # New
shurli relay pair                         shurli relay invite create
shurli relay pair --count 3 --ttl 2h      shurli relay invite create --ttl 2h  (one code per invocation)
shurli relay pair --list                  shurli relay invite list
shurli relay pair --revoke <id>           shurli relay invite revoke <id>
```

## Files

- `cmd_relay_pair.go` - The complete CLI command file (was at `cmd/shurli/cmd_relay_pair.go`)

## Date removed

2026-03-05, branch `dev/next-iteration`
