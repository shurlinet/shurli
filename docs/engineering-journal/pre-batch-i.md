# Pre-Batch I Decisions

Makefile and build tooling, PAKE-secured invite/join, and private DHT namespace isolation.

---

### ADR-Ia01: Makefile over Task Runner

**Context**: Shurli's build command has non-trivial flags (`-ldflags="-s -w" -trimpath` with version/commit/date injection). Service installation differs by OS (systemd vs launchd). There was no single command to build, install, and manage the daemon.

**Alternatives considered**:
- **Shell script** (`build.sh`) - Portable, but no dependency tracking, no `.PHONY`, no `make -j` parallelism. Would duplicate what Make already provides.
- **Mage / Task** - Go-based task runners. Adds a dependency and learning curve for contributors. Overkill for ~120 lines of targets.
- **Just** - Modern command runner. Not installed by default anywhere. Make is ubiquitous.

**Decision**: GNU Make. Present on every Unix system. Targets map directly to the operations developers need: `build`, `test`, `install`, `check`, `push`. OS detection via `uname -s` routes `install-service` to the correct init system.

**Consequences**: Windows users need `make` installed (via WSL, MSYS2, or Chocolatey). Accepted because Shurli's primary targets are Linux servers and macOS desktops.

**Reference**: `Makefile`

---

### ADR-Ia02: Generic Checks Runner (Not Privacy-Specific)

**Context**: The project needs a pre-push verification step. The specific checks include scanning for private data leaks (IPs, hostnames, peer IDs). But encoding these checks in the Makefile would expose the exact values being checked for in a public repository.

**Alternatives considered**:
- **Hardcoded privacy checks** - Embed `git grep` commands in Makefile. Would publicly document the private values being searched for. Rejected.
- **Pre-push git hook** - Works but is per-clone, easily bypassed with `--no-verify`, and not part of the normal workflow.

**Decision**: `make check` reads commands from a `.checks` file (gitignored, user-created, one command per line). The Makefile target is entirely generic: it runs each line and fails if any return non-zero. No words about what is being checked or why. `make push` gates on `make check`, making it impossible to push without passing.

**Consequences**: Each developer creates their own `.checks` file. The mechanism is reusable for any project-specific validation. The Makefile reveals nothing about the nature of the checks.

**Reference**: `Makefile:check`, `.gitignore`

---

### ADR-Ic01: Protocol-Level DHT Namespace Isolation

**Context**: All Shurli nodes share a single DHT with protocol prefix `/shurli/kad/1.0.0`. While `authorized_keys` controls who can communicate, discovery is shared. A gaming group, family, or organization has no way to form a completely isolated peer network.

**Alternatives considered**:
- **Application-layer filtering** - Keep a shared DHT but filter results by namespace tag. Rejected because nodes still participate in routing for all namespaces, and filtering is a soft boundary (peers are discoverable, just ignored).
- **Rendezvous string per network** - Different rendezvous points but same DHT. Rejected for the same reason: the DHT is shared, so cross-namespace discovery leaks metadata about who is online.
- **Separate relay per namespace** - Run independent relay servers. Works but is operationally heavy. Not mutually exclusive with namespace isolation.

**Decision**: Derive the DHT protocol prefix from an optional namespace: `/shurli/<namespace>/kad/1.0.0`. Empty namespace preserves the existing `/shurli/kad/1.0.0` prefix. Nodes on different namespaces speak entirely different DHT protocols and cannot discover each other. This is protocol-level isolation, not a filter.

**Implementation**: `DHTProtocolPrefixForNamespace()` function in `pkg/p2pnet/network.go` replaces direct use of the `DHTProtocolPrefix` constant at all 4 DHT bootstrap call sites. Config field: `discovery.network` (optional, validated as DNS-label format). `shurli init --network` and `shurli status` expose the namespace in the CLI.

**Consequences**: Each private network needs its own relay (or a relay configured with the matching namespace). This is intentional: isolation means isolation. Multi-namespace relay support is deferred. Zero backward compatibility impact (empty namespace = global DHT).

**Reference**: `pkg/p2pnet/network.go:DHTProtocolPrefixForNamespace`, `internal/config/config.go:DiscoveryConfig.Network`, `internal/validate/network.go`

---

### ADR-Ib01: Ephemeral DH + Token-Bound AEAD over Formal PAKE

**Context**: The invite/join handshake transmits the invite token as cleartext hex over the stream. A malicious relay operator can observe the token and potentially replay it. The goal is to upgrade the handshake so the relay sees only opaque encrypted bytes.

**Alternatives considered**:
- **Formal PAKE (CPace/SPAKE2)** - `filippo.io/cpace` is a "weekend project", `github.com/bytemare/pake` hasn't been updated since 2020. No mature, maintained Go PAKE library exists. Implementing CPace from scratch would add complexity for marginal benefit given our high-entropy token (64-bit random).
- **SRP** - Well-specified but complex, designed for password authentication with stored verifiers. Overkill for single-use tokens with 10-minute TTL.
- **Pre-shared key TLS** - Would require a TLS layer on top of libp2p's existing Noise transport. Layering violation.

**Decision**: Ephemeral X25519 Diffie-Hellman key exchange with token-bound HKDF key derivation and XChaCha20-Poly1305 AEAD encryption. Zero new dependencies: `crypto/ecdh` (Go stdlib), `golang.org/x/crypto/hkdf` and `golang.org/x/crypto/chacha20poly1305` (already in dependency tree via libp2p).

Both sides generate ephemeral X25519 keypairs, exchange public keys, compute the shared secret, then derive the AEAD key via HKDF-SHA256 with the invite token mixed in. If tokens differ, HKDF produces different keys, AEAD decryption fails, and the inviter reports "invalid invite code" with no protocol details leaked.

**Security properties**:
- Passive relay: sees only ephemeral public keys + encrypted bytes. Cannot learn token or peer names.
- Active MITM: prevented by libp2p Noise handshake (invite code contains inviter's peer ID, verified by transport layer).
- Token brute force: 64-bit entropy = 2^64 attempts. Single-use + 10min TTL.
- Offline dictionary: attacker needs an ephemeral private key (destroyed after exchange) to compute the DH shared secret.

**Consequences**: Not formally a PAKE (a true PAKE protects even against private key compromise with low-entropy passwords). Since our token is 64-bit random and ephemeral keys are destroyed immediately, this distinction is academic. If formal PAKE is needed later, the wire format stays identical - just swap the DH for CPace.

**Reference**: `internal/invite/pake.go`, `internal/invite/pake_test.go`

---

### ADR-Ib02: Invite Code Versioning (v1/v2 Coexistence)

**Context**: The invite code format needs to evolve to include the DHT namespace and support the PAKE handshake. Existing v1 invite codes (generated by older shurli versions) should still work.

**Alternatives considered**:
- **Breaking change** - Only support v2 codes. Rejected because users may have old codes in scripts or documentation.
- **Content negotiation** - Single format with feature flags. Adds complexity without clear benefit.

**Decision**: Version byte in invite code (first byte of binary payload) determines format. Originally: v1 (0x01) = legacy cleartext, v2 (0x02) = PAKE-encrypted with namespace. Post-I-1 deleted the cleartext protocol and renumbered: v1 (0x01) = PAKE-encrypted invite, v2 (0x02) = relay pairing code. Future versions (0x03+) are rejected with a "please upgrade shurli" message.

On the wire, the stream handler reads the version byte: 0x01 triggers PAKE handshake, 0x02 triggers relay pairing protocol.

**Consequences**: v2 invite codes are slightly longer (1 extra byte for namespace length when global, more with a namespace). The inviter must handle both protocols in the stream handler, but the code paths are cleanly separated.

**Reference**: `internal/invite/code.go`, `cmd/shurli/cmd_invite.go`, `cmd/shurli/cmd_join.go`
