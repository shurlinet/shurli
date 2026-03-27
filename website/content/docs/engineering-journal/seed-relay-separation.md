---
title: "Seed Relay Separation & Init Flow"
weight: 21
description: "Discovery-only seed relays, server-side circuit ACL, init flow, config set."
---
<!-- Auto-synced from docs/engineering-journal/seed-relay-separation.md by sync-docs - do not edit directly -->


| | |
|---|---|
| **Date** | 2026-03-03 |
| **Status** | Complete |
| **ADRs** | ADR-P01 to ADR-P05 |

Public seed relays are reclassified as discovery-only nodes. Data forwarding (SSH, XRDP, etc.) through seed relays is blocked at the relay server. The init flow defaults to the public Shurli network. A generic `config set` subcommand is added.

---

## ADR-P01: Seed Relays Are Discovery Nodes, Not Data Relays

**Context**: Shurli ships hardcoded seed relay addresses and resolves DNS seeds at startup. These public relays serve two purposes: (1) DHT bootstrap and peer discovery, and (2) circuit relay for data forwarding (SSH, XRDP). Problem: Shurli's seed relays were forwarding arbitrary data traffic from every user on the network. SSH sessions, XRDP streams, file transfers - all flowing through infrastructure he pays for. At scale, this is unsustainable and creates a central point of failure. If seed relays go down under load, the entire network loses both discovery AND data transport simultaneously.

**Alternatives considered**:
- **Rate-limit data circuits on seed relays** - Solves bandwidth but not the architectural problem. Users would still depend on public infrastructure for private data transfer. Partial failure mode: rate-limited SSH is worse than no SSH (timeouts, stalls, corrupted sessions).
- **Separate "discovery relay" and "data relay" binaries** - Doubles operational complexity. Two binaries, two configs, two sets of deployment scripts. The relay code is the same; only the policy differs.
- **Client-side enforcement** - Rejected. A client that voluntarily refuses to use seed relays for data can be recompiled to ignore that restriction. Client-side enforcement is security theater. The attacker changes one boolean and recompiles. Server-side is the only enforcement point that matters.

**Decision**: Seed relays are reclassified as **discovery and signaling nodes**. They handle:
- DHT bootstrap and peer discovery (`/shurli/kad/`)
- Relay pairing ceremonies (`/shurli/relay-pair/`)
- Peer introduction delivery (`/shurli/peer-notify/`)
- Remote admin (`/shurli/relay-admin/`)
- Remote unseal (`/shurli/relay-unseal/`)
- MOTD delivery (`/shurli/relay-motd/`)
- ZKP auth (`/shurli/zkp-auth/`)
- Ping/pong (`/pingpong/`)

All of these are **direct streams** between the peer and the relay itself - they do NOT use circuit relay forwarding. The relay is one endpoint of the conversation.

Data forwarding (circuit relay, where the relay blindly forwards bytes between two peers) is **disabled by default** via `enable_data_relay: false` in the relay server config. This is enforced server-side using libp2p's `relayv2.WithACL()` filter.

**Key insight**: Signaling protocols are direct streams. Circuit relay is a separate mechanism. Blocking circuit relay does NOT affect any signaling protocol. The distinction is architectural, not a policy hack.

**Consequence for users**: Peers that can only reach each other through a seed relay (both behind NAT, hole punching fails) will NOT be able to transfer data. They get a clear error explaining that seed relays enable discovery and direct connections only. The path forward: deploy your own relay server, which is a single command (`shurli relay setup`).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/circuit_acl.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_serve.go`

---

## ADR-P02: Server-Side Circuit ACL (relayv2.WithACL)

**Context**: libp2p's circuit relay v2 supports an `ACLFilter` interface with two methods: `AllowReserve(peer.ID, ma.Multiaddr) bool` and `AllowConnect(src peer.ID, srcAddr ma.Multiaddr, dest peer.ID) bool`. `AllowReserve` controls whether a peer can make a relay reservation (claim a slot). `AllowConnect` controls whether a peer-to-peer data circuit can be established through the relay.

**Alternatives considered**:
- **Firewall-level blocking** - Too coarse. Cannot distinguish between authorized admin traffic and unauthorized user data. Would block everything or nothing.
- **Protocol-level filtering** - The relay cannot inspect circuit traffic. It forwards bytes blindly. There is no protocol ID visible at the circuit layer. The relay sees "peer A wants to relay to peer B", not "peer A wants SSH to peer B."
- **Client-side enforcement** - Rejected on principle (ADR-P01). If enforcement is not at the server, it does not exist.

**Decision**: Implement `CircuitACL` struct satisfying `relayv2.ACLFilter`. Behavior:

- `AllowReserve`: Always returns true. Connection gating (`authorized_keys`) already controls who can connect at all. Double-gating reservations would break legitimate signaling peers.
- `AllowConnect`: If `enable_data_relay` is true globally, allow all circuits. Otherwise, allow only if either the source or destination peer has data relay privileges (admin role OR an active time-limited grant issued via `shurli relay grant <peer-id> --duration 1h`).

Wired in at relay startup:
```go
circuitACL := relay.NewCircuitACL(cfg.Security.AuthorizedKeysFile, cfg.Security.EnableDataRelay)
relayv2.New(h, relayv2.WithACL(circuitACL), ...)
```

**Per-peer override**: A time-limited grant issued via `shurli relay grant <peer-id> --duration 1h` grants that specific peer circuit relay access without enabling it globally. Grants expire automatically; the relay operator does not need to manually revoke them. Grant state is checked on every `AllowConnect` call, so newly issued or expired grants take effect immediately without relay restart.

**Consequences**: Zero protocol changes. Zero wire format changes. The relay simply refuses to forward data circuits for unauthorized peers. Authorized peers and admin peers are unaffected. The ACL reads authorized_keys on every `AllowConnect` call (no caching), so attribute changes take effect immediately without relay restart.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/circuit_acl.go`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/circuit_acl_test.go`, `https://github.com/shurlinet/shurli/blob/main/internal/auth/manage.go` (`HasRelayData`, `RelayData` field on `PeerEntry`)

---

## ADR-P03: Client-Side UX Error Mapping (Not Enforcement)

**Context**: When a seed relay blocks a data circuit, the client gets a generic libp2p error ("failed to open stream" or "relay connection failed"). This tells the user nothing about why it failed or what to do about it.

**Alternatives considered**:
- **Do nothing** - Generic error is confusing. Users would assume the network is broken, not that data relay is intentionally disabled.
- **Custom error protocol** - Add a protocol where the relay explains why it rejected the circuit. Over-engineered for a case that has a simple heuristic solution.

**Decision**: Client-side heuristic in `DialService()`. When a stream open fails for a data protocol AND the peer's only connections are relay circuits, append a hint to the error:

```
This relay is a discovery node, NOT a full data relay.
It enables peer discovery and direct connections only.
No SSH, XRDP, or other data is forwarded through it.

To transfer data between your devices, deploy your own relay server:
  shurli relay setup
  https://shurli.io/docs/relay-setup/

To override (for testing only):
  shurli config set relay.allow_seed_data true
```

This is **UX only**. The client does not make any enforcement decisions. Even if this code is removed or bypassed, the server-side ACL still blocks the circuit. The message exists purely to save the user from confusion.

**Why "discovery node" language**: Design directive: seed relays must never be described as "full relays" in any context. They are discovery nodes and direct connection enablers. This language must be consistent across CLI output, error messages, and documentation.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/sdk/service.go` (`isRelayOnlyPeer`, `relayDataHint`)

---

## ADR-P04: Init Flow Defaults to Public Network

**Context**: `shurli init` previously required the user to manually enter a relay address. But the binary already ships with hardcoded seed addresses (`seeds.go`) and resolves DNS seeds at startup. The default experience should be "join the Shurli network" with zero manual input, not "paste a multiaddr you probably don't have."

**Alternatives considered**:
- **Auto-detect from config** - There is no config yet; `init` creates it. Chicken-and-egg.
- **Remove manual option entirely** - Some users will run private networks with their own relays. The option must exist but should not be the default.

**Decision**: `shurli init` now presents a choice:

```
Network setup:
  1. Join the Shurli public network (default)
  2. Use my own relay server

Choice [1]:
```

Option 1 (default, just press Enter): writes all `HardcodedSeeds` addresses into the config's `relay.addresses` list. Clear messaging: "Uses public seed nodes for peer discovery and direct connections. NOTE: Seed nodes enable discovery only, NOT data relay."

Option 2: existing flow (prompt for relay multiaddr, validate, write single address).

The `nodeConfigTemplate` function signature changed from `relayAddr string` to `relayAddrs []string` to support multiple relay addresses in the config.

**Consequences**: First-run friction drops to near zero for public network users. Private network users still have a clear path. The config file now contains all seed addresses, making the relay list auditable.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_init.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/config_template.go`

---

## ADR-P05: Generic Config Set Subcommand

**Context**: The seed relay data override (`relay.allow_seed_data`) needs a way to be set from the CLI without hand-editing YAML. Rather than adding a one-off flag, build the generic mechanism.

**Alternatives considered**:
- **Dedicated flag per setting** - Does not scale. Every new config knob requires a new CLI flag, new tests, new man page entry.
- **Interactive config editor** - Over-engineered. Users know what key they want to set.

**Decision**: `shurli config set <key> <value>` with dotted key path navigation. Implementation uses `yaml.Node` tree traversal to preserve YAML structure and comments when modifying values.

```
$ shurli config set relay.allow_seed_data true
Set relay.allow_seed_data = true in /path/to/config.yaml
```

If intermediate keys don't exist, they are created as mapping nodes. Boolean values (`true`/`false`) are stored as YAML booleans, not strings. The config file's existing formatting and comments are preserved.

**Consequences**: Any YAML config value can be set from the CLI. New config knobs require zero CLI code. The man page and completion scripts include `config set`. Trade-off: no validation against a schema. `shurli config set typo.key value` will happily create a nonsense key. Acceptable: `shurli config show` makes the full config visible for review, and the parser ignores unknown keys.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_config.go` (`doConfigSet`, `yamlNodeSet`), `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_config_test.go`
