---
title: "Phase 6 - ACL + Relay Security"
weight: 17
description: "Role-based access, macaroon HMAC-chain tokens, async invite deposits, passphrase-sealed vault, remote P2P unseal, TOTP, Yubikey."
---
<!-- Auto-synced from docs/engineering-journal/phase6-acl-relay-security.md by sync-docs - do not edit directly -->


Role-based access control, HMAC-chain macaroon tokens, async invite deposits with attenuation-only permissions, passphrase-sealed vault, remote P2P unseal, TOTP two-factor, and Yubikey HMAC-SHA1.

---

### ADR-M01: Role-Based Access Control on authorized_keys

**Context**: All authorized peers are equal. There is no distinction between the peer who created the network and a peer who just joined via invite. Without roles, any authorized peer could create invites, modify settings, or act as an administrator. This breaks the trust hierarchy that a real network needs.

**Alternatives considered**:
- **Separate ACL database** - Adds a persistence dependency and fragments authority into two sources of truth (authorized_keys + ACL DB). Harder to audit, harder to back up, more things to break.
- **JWT-based roles** - Requires issuer infrastructure (key management, token signing, rotation). Overkill for a peer-to-peer system with no central server.

**Decision**: Add `admin`/`member` role attribute directly on existing `authorized_keys` entries. `https://github.com/shurlinet/shurli/blob/main/internal/auth/roles.go` (65 lines) defines `RoleAdmin` and `RoleMember` constants, plus `GetPeerRole()`, `SetPeerRole()`, `IsAdmin()`, and `CountAdmins()`. First peer is auto-promoted to admin when `CountAdmins()` returns 0, which handles fresh network bootstrapping. Backward compatible: any entry missing a role attribute defaults to `member`. Implementation reuses the existing `parseLine()`/`formatLine()` and `SetPeerAttr()` functions from `manage.go`, so the authorized_keys file format gains a new attribute without any structural change.

**Consequences**: Single source of truth for both identity and permissions. No new files, no new persistence layer. Trade-off: roles are flat (admin/member), not hierarchical. If we need finer granularity later, macaroon caveats (ADR-M02) handle it at the capability layer.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/auth/roles.go`, `https://github.com/shurlinet/shurli/blob/main/internal/auth/roles_test.go`

---

### ADR-M02: Macaroon HMAC-Chain Capability Tokens

**Context**: The invite system needs delegatable, attenuable permissions. An admin creates an invite token, but that token should carry specific constraints: which group, how many peers can join, when it expires, what services they can access. A flat ACL cannot express these constraints, and bolting fields onto authorized_keys would turn it into a schema nightmare.

**Alternatives considered**:
- **JWTs** - Require issuer infrastructure (signing key management, validation endpoints). Cannot be attenuated offline after issuance. The holder cannot further restrict a JWT without re-signing it.
- **OAuth scopes** - Centralized model. Requires a token server. Fundamentally incompatible with peer-to-peer, offline-first operation.
- **Custom signed tokens** - Reinventing macaroons poorly. The HMAC-chain construction is well-studied; no reason to design a worse version.

**Decision**: HMAC-SHA256 chain construction. `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/macaroon.go` (139 lines) defines the `Macaroon` struct with Location, ID, Caveats, and Signature fields. Core functions: `New()`, `AddFirstPartyCaveat()`, `Verify()`, `Clone()`, `Encode()`/`Decode()` (JSON), and `EncodeBase64()`/`DecodeBase64()`. Verification recomputes the full chain from the root key and performs constant-time signature comparison via `hmac.Equal`. Zero external dependencies: stdlib `crypto/hmac` and `crypto/sha256` only.

`https://github.com/shurlinet/shurli/blob/main/internal/macaroon/caveat.go` (129 lines) defines 7 caveat types: `service`, `group`, `action`, `peers_max`, `delegate`, `expires`, and `network`. `DefaultVerifier()` evaluates caveats against a `VerifyContext` struct. Unknown caveat types are rejected (fail-closed), so future caveat additions cannot bypass old verifiers.

Reference implementation: LND macaroons use the same HMAC-SHA256 chaining pattern, proven at scale in Lightning Network infrastructure.

**Consequences**: Tokens are self-contained, offline-verifiable, and attenuable by any holder. Trade-off: first-party caveats only (no third-party discharge). Third-party caveats would require a discharge protocol, which is unnecessary at current scale.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/macaroon.go`, `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/caveat.go`, `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/macaroon_test.go`, `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/caveat_test.go`

---

### ADR-M03: Async Invite Deposits ("Contact Card" Model)

**Context**: The existing PAKE invite (Phase 4) requires both peers to be online simultaneously. The admin runs `shurli invite create`, gets a code, sends it to the joining peer, who runs `shurli join` within a time window. If the joining peer is asleep, at work, or in a different timezone, the invite expires and the admin has to repeat the ceremony. This blocks adoption.

**Alternatives considered**:
- **Keep synchronous PAKE** - Works but is fragile. Requires real-time coordination between two humans. Every failed attempt wastes both parties' time.
- **Email-based tokens** - Requires email infrastructure (SMTP, delivery verification). Breaks sovereignty: now you depend on an email provider.
- **DHT-stored invites** - Unauthenticated peers cannot write to the private DHT. Storing secrets on the DHT exposes them to any node that discovers the key.

**Decision**: Store-and-forward "deposit" model. `https://github.com/shurlinet/shurli/blob/main/internal/deposit/store.go` (235 lines) defines the `InviteDeposit` struct with ID, Macaroon, CreatedBy, ExpiresAt, Status, ConsumedBy, and ConsumedAt fields. The `DepositStore` provides Create, Get, Consume, Revoke, AddCaveat, List, CleanExpired, and Count operations. Four-state lifecycle: `pending` -> `consumed` | `revoked` | `expired`. Thread-safe with `sync.RWMutex`. IDs are 16-byte random hex. Storage is in-memory now; the interface supports persistence later without changing the API.

`https://github.com/shurlinet/shurli/blob/main/internal/deposit/errors.go` defines `ErrDepositNotFound`, `ErrDepositConsumed`, `ErrDepositRevoked`, and `ErrDepositExpired` for unambiguous error handling.

Relay admin endpoints: POST `/v1/invite` (create), GET `/v1/invite` (list), DELETE `/v1/invite` (revoke), PATCH `/v1/invite` (modify). CLI: `shurli relay invite create/list/revoke/modify`.

**Consequences**: Invites persist until consumed, revoked, or expired. No real-time coordination needed. The joining peer can redeem hours or days later. Trade-off: in-memory storage means invites are lost on relay restart. Acceptable for now; persistence is an interface swap, not an architecture change.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/deposit/store.go`, `https://github.com/shurlinet/shurli/blob/main/internal/deposit/store_test.go`, `https://github.com/shurlinet/shurli/blob/main/internal/deposit/errors.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_relay_invite.go`

---

### ADR-M04: Attenuation-Only Permission Model

**Context**: An admin creates an invite with broad permissions (full service access, unlimited peer count). Then realizes the joining peer should only have access to one service, or should not be able to delegate further. The admin needs to narrow the invite's permissions without revoking and re-creating it from scratch.

**Alternatives considered**:
- **Mutable permission fields** - No cryptographic guarantee that permissions were not widened. An attacker (or a bug) could escalate permissions by editing the token.
- **Immutable invites only** - Forces a revoke-and-recreate workflow for every correction. Poor UX, especially when the invite code has already been shared with the joining peer.

**Decision**: The macaroon HMAC chain enforces attenuation-only by construction. `AddFirstPartyCaveat()` computes a new HMAC over the previous signature and the new caveat. The previous signature is consumed (overwritten) by the chain. Removing a caveat would require recovering the previous signature, which is computationally infeasible. This means permissions can only be narrowed, never widened.

The deposit's `AddCaveat()` method checks `Status == StatusPending` before modifying, so consumed or revoked invites cannot be altered. CLI: `shurli relay invite modify <id> --add-caveat <k=v>`.

Permissions are attenuation-only: mistakes can always be corrected (restrict further or revoke entirely), but never made worse. The cryptography enforces the policy, not application logic.

**Consequences**: Admins can iteratively tighten an invite after sharing the ID. The joining peer always gets the most restricted version. Trade-off: if the admin over-restricts, the only fix is revoke and create a new invite. This is intentional: loosening permissions should require a deliberate act, not an edit.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/macaroon/macaroon.go:AddFirstPartyCaveat`, `https://github.com/shurlinet/shurli/blob/main/internal/deposit/store.go:AddCaveat`

---

### ADR-M05: Passphrase-Sealed Relay Vault

**Context**: The relay stores a root key for macaroon minting. In plaintext memory, this key is exposed if the process is compromised (memory dump, core file, debugger attach). Worse, when the relay restarts, it comes up with full capabilities immediately. There is no "locked" state where the relay can route traffic but cannot authorize new peers.

**Alternatives considered**:
- **HSM/TPM** - Not available on typical VPS instances. Even when available, requires platform-specific drivers and setup.
- **Environment variables** - Visible in `/proc/<pid>/environ` on Linux. No sealed state concept. Still plaintext in memory after read.
- **OS keychain** - Platform-specific (Keychain on macOS, Secret Service on Linux). No watch-only mode. Cannot enforce "sealed until operator acts."

**Decision**: `https://github.com/shurlinet/shurli/blob/main/internal/vault/vault.go` (454 lines) implements a passphrase-sealed vault with optional TOTP. Key derivation: Argon2id with time=3, memory=64MB, threads=4, keyLen=32. Encryption: XChaCha20-Poly1305 for root key at rest. The `SealedData` struct is persisted as JSON.

Two operational modes:
- **Sealed** (default on start): watch-only. The relay routes traffic, serves existing connections, but cannot mint new macaroons or authorize new peers.
- **Unsealed**: full operations. Time-bounded with auto-reseal after a configurable duration.

`Seal()` zeros the key material via `subtle.XORBytes`. `RecoverFromSeed()` provides disaster recovery when the passphrase is lost.

Inspired by operators who chose to shut down rather than compromise their users: the relay must never betray its users. Sealed by default. Watch-only when locked. Seed recovery when all else fails.

All crypto dependencies already exist in go.mod: `golang.org/x/crypto` (Argon2id) and `chacha20poly1305` (already used in PAKE invite). Zero new dependencies.

Known limitation: Go's garbage collector cannot guarantee deterministic memory zeroing. The `subtle.XORBytes` overwrite is best-effort. If the GC copies the key before zeroing, a memory dump could still recover it. Seed recovery is the escape hatch for the operator; the zeroing is defense-in-depth, not a guarantee.

**Consequences**: Relay restarts in watch-only mode. Operator must actively unseal to authorize. Compromise of a sealed relay yields encrypted root key material, not plaintext. Trade-off: operator must be available to unseal after every restart (mitigated by ADR-M07 remote unseal).

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/vault/vault.go`, `https://github.com/shurlinet/shurli/blob/main/internal/vault/vault_test.go`, `https://github.com/shurlinet/shurli/blob/main/internal/vault/errors.go`

---

### ADR-M06: TOTP Two-Factor (RFC 6238, Zero Dependencies)

**Context**: Passphrase alone is single-factor. If an attacker compromises VPS SSH keys, they can read the passphrase from shell history or shoulder-surf it from a script. A second factor eliminates this class of attack without adding external service dependencies.

**Alternatives considered**:
- **SMS codes** - Requires an SMS provider (Twilio, etc.). Breaks sovereignty: now the relay's security depends on a third-party API and a phone number.
- **Push notifications** - Requires push infrastructure (APNs, FCM). Same sovereignty problem as SMS, plus adds mobile app dependency.
- **Hardware token only** - Not everyone has a YubiKey. Making it the only 2FA option excludes most operators.

**Decision**: `https://github.com/shurlinet/shurli/blob/main/internal/totp/totp.go` (126 lines) implements RFC 6238 with HMAC-SHA1. Core functions: `Generate()` computes the current TOTP, `Validate()` checks with a configurable skew window (skew=1 means +/- 30 seconds, handling VPS clock drift). `NewSecret()` generates 20 bytes minimum per RFC 4226. `FormatProvisioningURI()` outputs an `otpauth://` URI compatible with Google Authenticator, Authy, and any standard TOTP app.

Zero external dependencies: stdlib `crypto/hmac` and `crypto/sha1` only.

The TOTP secret is encrypted alongside the root key in the vault (ADR-M05). Decrypted only during unseal. Zeroed on seal.

**Consequences**: Two-factor unseal with no external services. Any standard TOTP app works. Trade-off: TOTP is time-based, so VPS clock accuracy matters. The skew window handles reasonable drift (up to 30 seconds). Operators with badly drifting clocks can increase skew or use YubiKey (ADR-M08) instead.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/totp/totp.go`, `https://github.com/shurlinet/shurli/blob/main/internal/totp/totp_test.go`

---

### ADR-M07: Remote Unseal Over P2P (Binary Wire Protocol)

**Context**: After a restart, the relay is sealed (ADR-M05). The operator needs to unseal it. But operators are not always at a terminal with SSH access. They might be on a phone behind CGNAT, on a tablet at a cafe, or simply away from their SSH setup. The relay needs an unseal path that works from any admin peer over the existing P2P network.

**Alternatives considered**:
- **HTTP API over public internet** - Exposes the unseal endpoint to the entire internet. Even with rate limiting, this is an unnecessary attack surface.
- **SSH tunnel** - Defeats the purpose. If the operator has SSH access, they can unseal directly. The whole point is to handle the case where SSH is unavailable.
- **JSON over libp2p stream** - Parsing overhead, harder to validate field lengths. JSON parsers accept arbitrarily large inputs by default, making resource exhaustion attacks easier.

**Decision**: Custom binary wire protocol on `/shurli/relay-unseal/1.0.0`. `https://github.com/shurlinet/shurli/blob/main/internal/relay/unseal.go` (235 lines) defines the format:

Request: `[1 version][2 BE passphrase-len][N passphrase][1 TOTP-len][M TOTP]`
Response: `[1 status][1 msg-len][N message]`

The handler checks `auth.IsAdmin()` before processing any request. iOS-style escalating lockout: 4 free attempts (typo grace), then 1 minute, 5 minutes, 15 minutes, 1 hour (x3), then permanent block. Successful unseal resets the counter. 30-second read deadline prevents slow-loris attacks. Prometheus metrics (`shurli_vault_unseal_total`, `shurli_vault_unseal_locked_peers`) feed the Grafana dashboard. The handler is registered even when the relay is sealed, because receiving unseal requests while sealed is the entire purpose.

Client-side helpers: `EncodeUnsealRequest()` and `ReadUnsealResponse()` for CLI integration.

**Consequences**: Admin peers can unseal the relay from any network, including behind CGNAT, over the existing P2P transport. No new ports, no new attack surface beyond the authenticated libp2p stream. Trade-off: the passphrase travels over the P2P connection. This is acceptable because libp2p streams are already encrypted (TLS 1.3 or Noise), and only admin peers can open the stream.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/unseal.go`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/unseal_test.go`

---

### ADR-M08: YubiKey via CLI (Not C HID Bindings)

**Context**: TOTP (ADR-M06) provides software-based 2FA. For operators who want hardware-backed 2FA, YubiKey HMAC-SHA1 challenge-response is the strongest option. But the standard approach (C HID bindings via `ykpers` or `libfido2`) requires CGo, adds build complexity, and creates a platform testing matrix.

**Alternatives considered**:
- **C HID bindings** - CGo dependency. Build matrix explodes: macOS, Linux, Windows, ARM, AMD64, each needing `libykpers` or `libhidapi`. Cross-compilation becomes painful.
- **WebAuthn/FIDO2** - Requires a browser context. Not suitable for CLI or headless server operation.
- **PIV/smart card** - Overkill for challenge-response. Requires PKCS#11 middleware, more complex than the problem warrants.

**Decision**: Shell out to `ykman` CLI via `exec.Command`. `https://github.com/shurlinet/shurli/blob/main/internal/yubikey/challenge.go` (170 lines) implements `IsAvailable()` (checks `ykman` on PATH + YubiKey connected), `ChallengeResponse()` (sends HMAC-SHA1 challenge, returns response), and `ListSlots()` (checks which slots are configured). 15-second timeout for touch-required keys.

Same zero-dep pattern as QR code generation (which shells out to `qrencode`). The binary stays pure Go; external tools are optional enhancements.

Purely optional. If `ykman` is not installed or no YubiKey is connected, `IsAvailable()` returns false with a clear error message. Graceful fallback to TOTP or passphrase-only.

**Consequences**: Hardware 2FA without CGo. Build stays simple. Trade-off: requires `ykman` installed on the operator's machine. This is acceptable because YubiKey users already have `ykman` (it is the standard management tool for their hardware). Operators without `ykman` are not affected; the feature simply does not appear.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/yubikey/challenge.go`, `https://github.com/shurlinet/shurli/blob/main/internal/yubikey/challenge_test.go`
