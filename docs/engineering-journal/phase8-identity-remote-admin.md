# Phase 8: Identity Security & Remote Admin

| | |
|---|---|
| **Date** | 2026-03-02 |
| **Status** | Complete |
| **ADRs** | ADR-O01 to ADR-O17 |

Phase 8 addresses seven converging problems: two separate seed systems, no backup confirmation, wrong password UX, unprotected identity.key, missing recovery CLI, unseal-only remote management, and engineering journal gaps for doctor/completion/man.

---

## ADR-O01: Shell Completion (bash/zsh/fish)

**Problem**: No tab completion for any commands.

**Decision**: Generate completion scripts as Go string constants in `cmd_completion.go`. One file, three shells. Same patterns: top-level commands, subcommands, flags. `--install`/`--uninstall`/`--path` flags for system-wide management.

**Why not external files**: Keeping completion scripts as Go constants means they're always in sync with the binary. No separate files to distribute or version.

**Reference**: `cmd/shurli/cmd_completion.go`

---

## ADR-O02: Man Page (troff)

**Problem**: No man page. `man shurli` does nothing.

**Decision**: Generate a full troff man page as a Go string constant in `cmd_man.go`. Covers all commands, examples, concepts, security considerations, and file locations. `--install` writes to `/usr/local/share/man/man1/shurli.1`.

**Why troff**: It's the standard. Every Unix system renders it natively. No dependencies.

**Reference**: `cmd/shurli/cmd_man.go`

---

## ADR-O03: Doctor Command (health checks + auto-fix)

**Problem**: After upgrading, completions and man page may be stale. Config issues may go unnoticed.

**Decision**: `shurli doctor` checks config, identity key, completions, and man page. `--fix` auto-repairs everything. Reports pass/warn/fail for each check.

**Reference**: `cmd/shurli/cmd_doctor.go`

---

## ADR-O04: Unified BIP39 Seed Architecture

**Problem**: Vault used 24 hex pairs, ZKP used 24 BIP39 words. Two seeds, two backups, two recovery processes. Should be ONE seed like Bitcoin wallets.

**Decision**: ONE BIP39 seed phrase (24 words) derives everything via HKDF domain separation:
- `HKDF(seed, "shurli/identity/v1")` -> Ed25519 private key
- `HKDF(seed, "shurli/vault/v1")` -> vault root key
- `SRS(SHA256(mnemonic))` -> ZKP circuit keys

**Why HKDF domain separation**: Same construction Bitcoin HD wallets use. Cryptographically independent keys from one seed. Proven, audited, secures trillions.

**Trade-off**: Seed compromise = total compromise. But that's true of any single-seed system, and one backup is far more likely to be done correctly than two.

**Reference**: `internal/identity/seed.go`

---

## ADR-O05: Password-Protected Identity (mandatory encrypted identity.key)

**Problem**: Raw Ed25519 key on disk for ALL nodes. File access = identity theft.

**Decision**: SHRL encrypted format: Argon2id (time=1, memory=64MB, threads=4) + XChaCha20-Poly1305. All nodes, not just relays. Magic header for format detection.

**Why Argon2id**: Memory-hard KDF resists GPU/ASIC attacks. Same algorithm as vault (consistency).

**Why mandatory**: Optional encryption means nobody enables it. Mandatory means the default is secure.

**Backward compatibility**: Raw key files detected by magic header check. User prompted to encrypt on first use.

**Reference**: `internal/identity/encrypted.go`

---

## ADR-O06: Seed Backup Confirmation (wallet-style quiz)

**Problem**: Seed phrase shown and trusted. No verification the user actually wrote it down.

**Decision**: After displaying the seed, quiz the user: "What is word 5?" and "What is word 18?" (random positions). Must answer both correctly. Same pattern as hardware wallet setup.

**Why only 2 words**: Balance between verification confidence and user annoyance. Two random positions from 24 words is sufficient to prove the user has the full phrase accessible.

**Reference**: `cmd/shurli/cmd_seed_helpers.go`

---

## ADR-O07: Password UX (identity vs vault passwords)

**Problem**: "Passphrase" confused with seed phrase. Daily unseal should feel like typing a password.

**Decision**: Three-tier terminology:
- **Seed phrase**: 24 BIP39 words. Disaster recovery only. Paper. Offline. Used once at setup.
- **Identity password**: Protects identity.key. 8+ chars. Entered at daemon start (or skip via session token). ALL nodes.
- **Vault password**: Protects vault root key. Relay only. Separate from identity password.

**Why separate passwords**: Identity password is entered frequently (daemon start). Vault password is entered rarely (unseal after reboot). Different risk profiles warrant different credentials.

**Reference**: `cmd/shurli/cmd_change_password.go`

---

## ADR-O08: Top-Level Recover CLI

**Problem**: `RecoverFromSeed()` existed in vault.go but no CLI command. Recovery required code knowledge.

**Decision**: `shurli recover` as a top-level command. Interactive by default (prompts for seed, password). Non-interactive seed via `--seed` flag. `--relay` flag also recovers vault + ZKP keys. Password is always interactive (no `--password-file` - attack vector removed during Phase 8 testing).

**Why top-level**: Recovery is critical infrastructure. Burying it under `relay vault recover` makes it invisible to node operators who need it most.

**Reference**: `cmd/shurli/cmd_recover.go`

---

## ADR-O09: Top-Level Change Password CLI

**Problem**: No way to change identity password without code modification.

**Decision**: `shurli change-password` prompts for current password, new password, confirmation. Updates session token if one exists.

**Reference**: `cmd/shurli/cmd_change_password.go`

---

## ADR-O10: Remote Admin Protocol (`/shurli/relay-admin/1.0.0`)

**Problem**: All 16+ admin API endpoints were local-only (Unix socket). Admin peers could only unseal remotely, nothing else. Future mobile app needs full remote management.

**Decision**: Generic P2P admin channel using `/shurli/relay-admin/1.0.0`. JSON-over-stream with request/response framing. The handler adapts P2P stream requests into HTTP requests against the local admin socket.

**Why adapter pattern**: Reuse all existing HTTP handlers and validation. Zero duplication. Any new admin endpoint automatically works remotely.

**Security**: Admin role check at stream open (rejected before data). Rate limiting (5 req/s per peer). Same auth model as Unix socket.

**Trade-off**: JSON-over-stream adds ~2KB overhead vs binary protocol. Acceptable for admin operations (not data plane).

**Reference**: `internal/relay/remote_admin.go`, `internal/relay/remote_admin_client.go`

---

## ADR-O11: setup.sh Assessment (bash/Go split rationale)

**Problem**: Relay deployment uses bash (setup.sh) for OS-level tasks (users, systemd, firewall) and Go for Shurli-specific logic. Is this the right split?

**Decision**: Keep the split. Bash handles OS integration (users, systemd units, firewall rules, sysctl tuning). Go handles application logic (config generation, key management, admin operations).

**Why**: OS-level tasks vary wildly by distribution. Bash with apt/ufw is the right tool for Debian/Ubuntu. The setup.sh already handles root vs non-root, user creation, SSH hardening, and log rotation. Rewriting this in Go would add complexity without value.

---

## ADR-O12: Relay MOTD (operator announcements, client-side sanitization)

**Problem**: Relay operators have no way to communicate with connected peers. Maintenance windows, migration notices, and policy changes require out-of-band communication.

**Decision**: `/shurli/relay-motd/1.0.0` protocol with three message types:
- MOTD (0x01): shown on connect, deduped per-relay (24h)
- Goodbye (0x02): persistent farewell, cached by clients
- Retract (0x03): clears cached goodbye

Wire format: `[1 version][1 type][2 BE msg-len][N msg][8 BE timestamp][Ed25519 sig]`

**Why signed**: Prevents man-in-the-middle from injecting fake announcements. Clients verify signature against relay's known public key before display.

**Why 280-char limit**: Operator messages should be brief. Long messages are usually spam or injection attempts.

**Reference**: `internal/relay/motd.go`, `internal/relay/motd_client.go`

---

## ADR-O13: Graceful Goodbye (relay shutdown notification)

**Problem**: When a relay shuts down, peers get connection errors with no explanation. They don't know if it's temporary (maintenance) or permanent (decommission).

**Decision**: Goodbye messages are pushed to all connected peers immediately, persisted to disk (`.relay-goodbye.json`), and cached by clients. The goodbye survives relay restarts and client reconnect attempts.

**Lifecycle**: `set` -> pushed to peers. `retract` -> peers clear cache. `shutdown` -> push goodbye, then trigger relay shutdown with 2s delay for message delivery.

**Why 2s delay**: Ensures goodbye reaches connected peers before the TCP connections drop.

**Reference**: `internal/relay/motd.go` (`SetGoodbye`, `RetractGoodbye`, `HandleGoodbyeShutdown`)

---

## ADR-O14: Relay Message Security (untrusted input)

**Problem**: MOTD/goodbye messages originate from the relay operator but are displayed on client devices. A compromised relay could inject phishing URLs, email addresses, or AI prompt injection payloads.

**Decision**: `SanitizeRelayMessage()` applies defense-in-depth:
1. Strip URLs (http://, https://, ftp://, www.)
2. Strip email addresses
3. Remove non-ASCII characters (unicode homoglyphs, RTL overrides)
4. Truncate to 280 characters
5. Collapse whitespace

**Why strip URLs**: The primary phishing vector. A compromised relay sending "Visit https://evil.com to update" is the exact attack we're defending against.

**Why strip non-ASCII**: Unicode contains RTL override characters, zero-width joiners, and homoglyphs that enable visual spoofing. ASCII-only eliminates the entire class.

**Reference**: `internal/validate/relay_message.go`

---

## ADR-O15: MOTD Cryptographic Signing

**Problem**: Without signatures, any network intermediary could forge relay announcements.

**Decision**: All messages signed with the relay's Ed25519 identity key. Signature covers: type byte + message + timestamp. Clients verify against the relay's known public key (from the libp2p connection).

**Why Ed25519**: Already used for libp2p identity. No new key types. Signing is fast (microseconds).

**Stored goodbye verification**: Clients persist goodbye signatures. On reconnect, the stored goodbye can be re-verified against the relay's public key.

**Reference**: `internal/relay/motd.go` (`buildSignableData`), `internal/relay/motd_client.go` (`VerifyStoredGoodbye`)

---

## ADR-O16: Goodbye Lifecycle

**Problem**: Goodbye needs clear states: active (relay is leaving), retracted (relay is back), shutdown (relay is gone).

**Decision**: Three-operation lifecycle:
- `goodbye set "message"`: push to all peers, persist to disk
- `goodbye retract`: push retract to all peers, delete from disk
- `goodbye shutdown "message"`: set goodbye, wait 2s, shut down relay

The `shutdown` operation requires explicit confirmation for safety (sends to all peers, then kills the process).

**Admin API**: `POST /v1/goodbye/shutdown` triggers async shutdown via `shutdownFunc` channel. The relay's signal handler selects on both OS signals and the admin shutdown channel.

**Reference**: `internal/relay/admin.go` (`handleGoodbyeShutdown`), `cmd/shurli/cmd_relay_serve.go`

---

## ADR-O17: Session Token & Lock/Unlock

**Problem**: Entering password at every daemon start is tedious. But removing password protection defeats the purpose of encrypted identity.

**Decision**: Session tokens (ssh-agent model):
- `session refresh`: encrypt identity key with machine-derived key, save token
- `session destroy`: delete token, require password on next start
- `lock`/`unlock`: gate sensitive operations without destroying session

Machine binding: token encrypted with key derived from hostname + machine ID. Copying token to another device does not work.

**Why separate lock/unlock from session**: Session is about startup convenience. Lock/unlock is about runtime security. Different concerns, different commands.

**Reference**: `internal/identity/session.go`, `cmd/shurli/cmd_lock.go`
