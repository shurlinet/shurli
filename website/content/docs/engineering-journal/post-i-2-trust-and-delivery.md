---
title: "Post-I-2 - Trust & Delivery"
weight: 13
description: "Peer introduction delivery, HMAC group commitment, relay admin socket, SAS verification, reachability grades, sovereign interaction history."
---
<!-- Auto-synced from docs/engineering-journal/post-i-2-trust-and-delivery.md by sync-docs - do not edit directly -->


Peer introduction delivery, HMAC group commitment, relay admin socket, SAS verification, reachability grades, and sovereign peer interaction history.

---

### ADR-J01: Peer Introduction Delivery Protocol

**Context**: After relay pairing completes, the first peer to join a group gets authorized by the relay. But when a second peer joins later, the first peer's daemon doesn't know about the newcomer. In live testing, the home-node denied the client-node 20+ times because it had no record of the client's peer ID. The relay knows about both peers but had no mechanism to tell existing peers about newcomers.

**Alternatives considered**:
- **Polling-based discovery** - Daemons periodically ask the relay "who's in my group?" Wasteful and adds latency.
- **GossipSub** - Premature for current scale and adds protocol complexity.
- **Direct peer-to-peer sync** - Requires both peers to be online simultaneously, which is exactly the problem we're solving.

**Decision**: `/shurli/peer-notify/1.0.0` stream protocol. The relay acts as a post office: it knows who joined together and delivers introductions when peers connect. Wire format: version byte + group ID (32 bytes) + group size (byte) + per-peer entries (peer ID length + peer ID + name length + name + 32-byte HMAC proof). Two triggers: post-pairing notification (immediate, after new peer joins) and reconnect notification (on `EvtPeerIdentificationCompleted`, for peers that were offline during pairing). The receiving daemon validates group membership, enforces group size limits, verifies HMAC proofs, and adds authorized peers via hot-reload.

**Consequences**: Solves the first-joiner gap completely. Relay is the introduction medium, not the trust authority (HMAC proofs provide cryptographic verification). Wire format is generic enough for future introduction sources (peer-to-peer, multi-relay mesh). Adds a stream protocol dependency between relay and daemon.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/notify.go`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/notify_test.go`

---

### ADR-J02: HMAC Group Commitment Proof

**Context**: Peer-notify delivers introductions from the relay. A compromised relay could inject arbitrary peer IDs into introduction messages. Need cryptographic proof that each introduced peer actually held a valid pairing token.

**Alternatives considered**:
- **Trust the relay** - Works for private relays but violates the sovereignty principle. A relay should be a transport, not a trust root.
- **Public-key signatures per peer** - Each peer signs their own introduction. More complex and requires key exchange before the introduction itself.
- **Token-based challenge** - Relay challenges peers to prove token possession during pairing. Simpler but requires live interaction.

**Decision**: `HMAC-SHA256(token, groupID)` computed during pairing while the raw token is still in memory. The HMAC proof is stored in the TokenStore alongside the peer's entry. When peer-notify delivers introductions, each peer entry includes its 32-byte HMAC proof. The receiving daemon stores the proof in authorized_keys as `hmac_proof=<hex>`. Raw tokens are never stored - only SHA-256 hashes. HMAC proofs are the only derivative that persists.

**Consequences**: A compromised relay cannot forge valid HMAC proofs without access to the original pairing tokens (which are hashed immediately). Proofs can be verified by any peer that holds the same group's token. Adds 32 bytes per peer in the wire format, which is negligible.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/pairing.go:103-106`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/tokens.go:202-213`

---

### ADR-J03: Relay Admin Socket (Unix Socket + Cookie Auth)

**Context**: The first live test of relay pairing failed. `relay pair` created a fresh ephemeral TokenStore, generated tokens, and exited. The running `relay serve` process had a separate TokenStore with no knowledge of the generated tokens. Pairing was dead on arrival.

**Alternatives considered**:
- **Shared file-based token store** - Both processes read/write a shared token file. Race conditions, no atomic operations, no TTL enforcement.
- **Embed token generation in relay serve** - Makes relay serve interactive, breaking the systemd service model.
- **gRPC or custom protocol** - Over-engineered for what amounts to 3 HTTP endpoints.

**Decision**: Unix domain socket with cookie auth, following the exact pattern proven by the daemon API. Three endpoints: `POST /v1/pair` (create pairing group), `GET /v1/pair` (list groups), `DELETE /v1/pair/{id}` (revoke group). Rewrite `relay pair` as a fire-and-forget HTTP client that talks to the running relay's admin socket. Cookie is 32-byte random hex, `0600` permissions, rotated per restart. Socket path: `<config-dir>/relay-admin.sock`.

**Consequences**: Token generation happens inside the relay serve process where the TokenStore lives. No shared state, no file races. The admin socket pattern is now battle-tested in two places (daemon + relay). Adds ~300 lines but reuses the same auth pattern. Security: constant-time cookie comparison (`subtle.ConstantTimeCompare`), `MaxBytesReader` on request body, upper bound on pairing count.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin.go`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_client.go`, `https://github.com/shurlinet/shurli/blob/main/internal/relay/admin_test.go`

---

### ADR-J04: SAS Verification (OMEMO-style Emoji Fingerprint)

**Context**: After relay pairing, peers are authorized but unverified. The relay mediated the exchange - if the relay were compromised, it could have substituted peer IDs (MITM). Need out-of-band identity confirmation that users can perform over a phone call or in person.

**Alternatives considered**:
- **Full public key display** - Too long for humans to compare reliably.
- **Numeric fingerprint only** - Works but low memorability. Users won't do it unless it's engaging.
- **QR code scan** - Requires physical proximity and camera access. Too restrictive.

**Decision**: OMEMO-style SAS (Short Authentication String). Sorted SHA-256 hash of both peer IDs (sorted so both sides compute the same fingerprint). First 8 bytes encode to 4 emoji (2 bytes per index, mod 256 into a table of 256 universally recognizable emoji). Also generates a 6-digit numeric code as fallback. `[UNVERIFIED]` badge persists in `auth list` output until verification, then `verified=sha256:<prefix>` is written to authorized_keys. The 256-emoji table covers animals, nature, weather, food, objects, musical instruments, transport, and sports - categories that work across cultures.

**Consequences**: Verification is optional but visible. The persistent `[UNVERIFIED]` badge serves as a constant reminder without blocking functionality. 4-emoji comparison is quick and engaging. Entropy: 4 emoji from 256 = 2^32 possibilities, sufficient for active MITM detection in the private-network threat model.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/verify.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/verify_test.go`, `https://github.com/shurlinet/shurli/blob/main/cmd/shurli/cmd_verify.go`

---

### ADR-J05: Reachability Grade Computation

**Context**: Users need to understand why connections go through relay instead of direct. STUN reporting "hole-punchable" is meaningless when CGNAT sits above the inner NAT and drops unsolicited inbound packets. Need a simple, honest grade that captures real-world reachability.

**Alternatives considered**:
- **Binary "reachable or not"** - Too coarse. A peer behind port-restricted NAT is meaningfully different from one behind CGNAT.
- **Raw STUN output** - Too technical for most users. "Address-restricted cone NAT" means nothing to someone who just wants SSH to work.
- **Auto-detect and hide** - Don't show the grade at all, just silently pick paths. But hiding information violates the principle of honest communication.

**Decision**: A-F grade computed from interface discovery + STUN results, displayed in daemon status output and exposed via API. Grade A: public IPv6 detected. Grade B: public IPv4 or hole-punchable NAT (full-cone, address-restricted). Grade C: port-restricted NAT. Grade D: symmetric NAT or CGNAT. Grade F: no connectivity detected. Critical design choice: CGNAT detection (`stun.BehindCGNAT`) caps the grade at D regardless of inner NAT type. This overrides STUN's false optimism. Grade updates on network change events.

**Consequences**: Users get honest, actionable information. "Grade D - CGNAT detected, hole-punch unlikely" is more useful than "hole-punchable: yes" when CGNAT will actually block it. The grade informs path selection decisions in future phases (Phase 5-L PeerManager). CGNAT detection is limited to RFC 6598 (100.64.0.0/10) on local interfaces - mobile CGNAT using RFC 1918 addresses (172.20.x.x) cannot be distinguished from home networks.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/reachability.go`, `https://github.com/shurlinet/shurli/blob/main/pkg/p2pnet/reachability_test.go`

---

### ADR-J06: Sovereign Peer Interaction History

**Context**: Future trust algorithms (EigenTrust, Community Notes bridging, reputation scoring) need interaction data as input. If we wait until those algorithms ship to start collecting data, we'll have zero history to work with. Building the data layer now means months of interaction data will be ready when the algorithms arrive.

**Alternatives considered**:
- **Centralized reputation service** - Defeats sovereignty. A central server that knows who interacts with whom is a privacy nightmare.
- **Gossip-based reputation** - Peers share reputation scores with each other. Complex, game-able, and premature.
- **No collection, implement later** - Loses months of valuable interaction data that can never be recovered.

**Decision**: Per-peer JSON file (`peer_history.json`), stored locally and never shared. Tracks: `first_seen`, `last_seen`, `connection_count`, `avg_latency_ms` (Welford's online algorithm for running average), `path_types` (map of "direct":N, "relay":M), `introduced_by`, `intro_method` ("relay-pairing", "invite", "manual"). Thread-safe with `sync.RWMutex`. Atomic file writes (temp + rename) for crash safety. Best-effort load on startup (missing file is not an error).

**Consequences**: Zero external dependencies. Data stays sovereign - each peer controls its own history file. The schema is intentionally minimal but extensible. Future trust algorithms can consume this data without schema migrations. Storage growth is bounded by peer count (not connection count), since records are per-peer aggregates.

**Reference**: `https://github.com/shurlinet/shurli/blob/main/internal/reputation/history.go`, `https://github.com/shurlinet/shurli/blob/main/internal/reputation/history_test.go`
