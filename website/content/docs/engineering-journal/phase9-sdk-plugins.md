---
title: "Phase 9 - SDK, Plugins & Protocol Consolidation"
weight: 22
description: "Invite v1/v2 deletion, protocol ID helpers, bootstrap extraction, file transfer plugin."
---
<!-- Auto-synced from docs/engineering-journal/phase9-sdk-plugins.md by sync-docs - do not edit directly -->


**Date**: 2026-03-08
**Status**: In Progress
**ADRs**: ADR-Q01 to ADR-Q05

Phase 9 builds the plugin system, SDK interfaces, and file transfer - the first concrete plugin. It also consolidates legacy protocol versions that accumulated during rapid iteration.

---

## ADR-Q01: Delete Invite v1/v2 Code

**Date**: 2026-03-08
**Status**: Accepted

### Context

Three invite code formats accumulated over development:
- **v1**: Base32 encoding, direct PAKE between peers (no relay)
- **v2**: Base32 encoding, relay-mediated pairing with raw token
- **v3**: Base36 short codes (16 chars like `KXMT-9FWR-PBLZ-4YAN`), async relay pairing with PAKE

v3 was strictly superior: shorter codes, better UX, PAKE-secured wire protocol, async deposit model. v1 and v2 existed only for backward compatibility that nobody needed (no deployed users on those versions).

### Decision

Delete all v1 and v2 code. Make v3 the only format. No version constants, no dispatch switches, no backward compatibility shims.

Specifically:
- `invite/code.go`: Delete v1 base32 and v2 base32+relay encoding. Keep only base36.
- `invite/pake.go`: Unify `Complete()` + `CompleteWithSalt()` into single `Complete(remotePub, salt []byte, channelBinding ...[]byte)`.
- `relay/pairing.go`: Delete v1 raw-token `HandleStream`. Rename v2 PAKE handler to `HandleStream`.
- `cmd_join.go`: Delete old `runPairJoin` (~200 lines). Rename v3 flow to `runPairJoin`.
- `cmd_relay_serve.go`: Remove v1 protocol handler registration.

### Result

-1,322 lines deleted across 10 files. Single code path, single wire protocol. Discarded code archived on `discarded` branch (`invite-v1v2/` folder) per project convention.

### Why Satinder Instructed This

> "Make v3 invite code v1. Erase ALL v1 and v2 invite code implementation AND any sort of backward compatibility code from the WHOLE codebase."

The reasoning: backward compatibility for formats with zero users is pure complexity. Every version switch, every dispatch branch, every `V2` suffix in a function name is cognitive overhead that slows down the next person reading the code. Delete it. If you need functionality from the old code to make the new code work, move it to the new code and delete the old.

---

## ADR-Q02: Pairing Protocol Wire Version Retained

**Date**: 2026-03-08
**Status**: Accepted

### Context

After deleting v1, the remaining pairing protocol is `/shurli/relay-pair/2.0.0`. Should it be renumbered to `1.0.0`?

### Decision

Keep the `2.0.0` wire version. The protocol ID is a wire identifier, not a marketing version. Relay servers already running `2.0.0` would break if we renumbered. The code constant is now just `PairingProtocol` (no version suffix).

---

## ADR-Q03: Protocol ID Helpers

**Date**: 2026-03-08
**Status**: Accepted

### Context

Protocol IDs (`/shurli/<name>/<version>`) were string literals scattered across packages. Typos or formatting mistakes would silently create incompatible protocols.

### Decision

Add `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/protocolid.go`:
- `ProtocolID(name, version)` - constructor that panics on empty, slash, or whitespace
- `ValidateProtocolID(id)` - runtime validation
- `MustValidateProtocolIDs(ids...)` - batch validation for `init()` blocks

All relay protocol constants validated at startup via `init()`.

---

## ADR-Q04: Bootstrap Extraction

**Date**: 2026-03-08
**Status**: Accepted

### Context

Standalone CLI commands (ping, traceroute) duplicated ~50 lines of DHT bootstrap + relay connect + peer discovery logic each.

### Decision

Extract to `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/bootstrap.go`:
- `BootstrapConfig` struct (namespace, bootstrap peers, relay addrs)
- `BootstrapAndConnect()` function: DHT client mode, connect to bootstrap peers, connect to relays, find peer via DHT, fallback to relay circuit

CLI commands now call one function instead of duplicating the pattern.

---

## ADR-Q05: File Transfer Plugin (Phase 9B)

**Date**: 2026-03-08
**Status**: Accepted

### Context

File transfer is the first concrete plugin built on the Phase 9A service infrastructure. It validates the `ServiceManager`, `StreamHandler`, and event bus interfaces.

### Decision

`https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/transfer.go` implements:
- Wire protocol: `version(1) + type(1) + nameLen(2) + name(var) + size(8) + sha256(32)`
- `TransferService` with `HandleInbound()` returning a `StreamHandler`
- `SendFile()` for outbound transfers with background progress tracking
- Path traversal protection (`filepath.Base`), checksum verification, unique filename collision avoidance
- Max 1 TB per file, 64 KB copy buffer

Daemon integration:
- `POST /send` endpoint, `GET /transfers` listing, `GET /transfer/{id}` status
- `shurli send <file> <peer>` CLI with progress polling

### Trade-offs

- No directory transfer yet (single files only)
- No encryption beyond libp2p transport security (adequate for authorized peers)
- Progress polling (500ms ticker) rather than streaming updates
