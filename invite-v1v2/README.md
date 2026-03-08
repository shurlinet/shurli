# Discarded: Invite Code v1/v2 and Pairing Protocol v1

**Discarded**: 2026-03-08
**Last commit before deletion**: `75407c2`
**Branch at time of deletion**: `dev/next-iteration`

## What This Was

Three invite code formats and two pairing wire protocols that existed simultaneously:

### Invite Code Versions
- **v1** (base32, PAKE direct): 8-byte token + relay address + inviter peer ID. Long codes (~120 chars). Already dead (no handler in cmd_join.go).
- **v2** (base32, relay pairing): 16-byte token + relay address. Shorter than v1 but still ~80 chars. Used v1 wire protocol (raw token on the wire).
- **v3** (base36, short async): 10-byte token only. 16 chars (`KXMT-9FWR-PBLZ-4YAN`). Uses PAKE-secured v2 wire protocol.

### Pairing Wire Protocols
- **v1** (`/shurli/relay-pair/1.0.0`): Raw token + name on the wire. No encryption. Token visible to relay.
- **v2** (`/shurli/relay-pair/2.0.0`): PAKE-secured. SHA-256(token) + X25519 DH + XChaCha20-Poly1305. Token never exposed on wire.

## Why It Was Deleted

Satinder's directive: "Make v3 invite code v1. Erase ALL v1 and v2 invite code implementation AND any sort of backward compatibility code from the WHOLE codebase."

Reasoning:
1. **No userbase on the network yet** - zero backward compatibility burden.
2. **v3 short codes supersede everything** - shorter, simpler, more secure (PAKE-protected).
3. **Three formats + two wire protocols = unnecessary complexity** - violates the deletion principle.
4. **v1 wire protocol sends raw token** - security regression compared to PAKE. No reason to keep it.

## What Replaced It

After deletion, the codebase has:
- **One invite code format**: base36 short codes (what was v3, now just "invite code")
- **One wire protocol**: `/shurli/relay-pair/2.0.0` (PAKE-secured, what was v2)
- `invite.Encode()` / `invite.Decode()` (renamed from `EncodeShort` / `decodeV3`)
- `invite.GenerateToken()` returns `[]byte` (10 bytes, renamed from `GenerateShortToken`)
- `invite.TokenSize = 10` (renamed from `ShortTokenSize`)
- `relay.PairingProtocol` = `/shurli/relay-pair/2.0.0` (renamed from `PairingProtocolV2`)
- `relay.PairingHandler.HandleStream()` (renamed from `HandleStreamV2`)
- `relay.PairingResponse` (renamed from `PairingV2Response`)

## Files in This Archive

- `code.go` - Invite code encode/decode (v1, v2, v3 formats)
- `pake.go` - PAKE session with VersionV1/V2/V3 constants and 8-byte `deriveKey`
- `pairing.go` - Relay pairing handler with v1 `HandleStream`, v2 `HandleStreamV2`, v1 legacy rejection
- `cmd_join.go` - Join command with `runPairJoin` (v2 codes), `runPairJoinV3`, `loadOrCreateConfig`
- `code_test.go` - Tests for v1/v2 encode/decode round trips
- `code_bench_test.go` - Benchmarks for v1 encode/decode
