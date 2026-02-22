---
title: "Core Architecture"
weight: 2
description: "Foundational technology choices: Go, libp2p, private DHT, circuit relay v2, connection gating, single binary."
---
<!-- Auto-synced from docs/engineering-journal/core-architecture.md by sync-docs.sh - do not edit directly -->


Foundational technology choices made before the batch system.

---

### ADR-001: Why Go

**Context**: peer-up needs to compile to a single static binary, run on Linux/macOS/Windows, and interface with libp2p (which has mature Go and Rust implementations).

**Alternatives considered**:
- **Rust** - Better memory safety guarantees, smaller binaries. Rejected because rust-libp2p has less mature circuit relay v2 support, and compile times would slow iteration during early development.
- **Python/Node.js** - Faster prototyping. Rejected because distribution requires runtime dependencies, violating the "single binary, zero dependencies" principle.

**Decision**: Go. Single binary compilation, excellent cross-platform support, mature libp2p ecosystem, and fast compilation for rapid iteration.

**Consequences**: Larger binary size (~28MB stripped) compared to Rust. Accepted because distribution simplicity outweighs binary size for a CLI tool. Binary size is actively monitored and optimized (see `binary-optimization` practices).

**Reference**: `go.mod`, `https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/main.go`

---

### ADR-002: Why libp2p (Not Raw QUIC, Not WireGuard)

**Context**: peer-up needs NAT traversal, encrypted transport, peer discovery, and circuit relay. Building these from scratch would take years.

**Alternatives considered**:
- **Raw QUIC + custom protocol** - Full control, smaller dependency tree. Rejected because we'd need to implement hole punching, relay, DHT, and peer routing from scratch.
- **WireGuard** - Excellent performance, kernel-level. Rejected because it requires root/admin privileges, doesn't solve discovery, and doesn't provide circuit relay for CGNAT.
- **Noise protocol + custom transport** - Lighter than libp2p. Rejected because discovery and relay still need to be built.

**Decision**: libp2p v0.47.0. Provides QUIC+TCP+WebSocket transports, circuit relay v2, hole punching (DCUtR), Kademlia DHT, peer identity (Ed25519), and connection gating - all battle-tested.

**Consequences**: Large dependency tree (100+ transitive deps). The binary includes WebRTC and other transports we don't directly use. Accepted because reliability > binary size, and we actively track CVEs in dependencies.

**Reference**: `go.mod`, `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go`

---

### ADR-003: Why Private DHT `/peerup/kad/1.0.0`

**Context**: Initially used the public IPFS Amino DHT (`/ipfs/kad/1.0.0`). This worked but mixed peerup peers into the global IPFS routing table, leaking peer discovery to the public network.

**Alternatives considered**:
- **Keep IPFS Amino DHT** - Zero config, large bootstrap. Rejected because (a) privacy: peerup peers are discoverable by anyone on IPFS, (b) pollution: peerup's rendezvous strings pollute the global DHT, (c) reliability: depends on IPFS bootstrap nodes staying healthy.
- **No DHT, relay-only** - Simpler. Rejected because DHT enables peer discovery without centralized infrastructure.
- **mDNS only** - Local network discovery. Rejected because it doesn't work across networks.

**Decision**: Private Kademlia DHT with protocol prefix `/peerup/kad/1.0.0` (constant `p2pnet.DHTProtocolPrefix`). Peerup peers only discover and route to other peerup peers.

**Consequences**: Smaller routing table (only peerup peers), no IPFS bootstrap dependency, but requires at least one known peer (relay) to bootstrap into the DHT.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:27` (`DHTProtocolPrefix` constant), commit `d1d4336`

---

### ADR-004: Why Circuit Relay v2

**Context**: Users behind CGNAT (5G, carrier-grade NAT, double NAT) cannot receive inbound connections. This is the core problem peer-up solves.

**Alternatives considered**:
- **UPnP/NAT-PMP only** - Works for simple NAT, fails on CGNAT. Rejected as sole strategy.
- **TURN server** - WebRTC-style relay. Rejected because it's a separate protocol ecosystem; libp2p's circuit relay v2 integrates naturally with the existing transport stack.
- **Circuit relay v1** - Deprecated by libp2p. Rejected.

**Decision**: Circuit relay v2 via `libp2p.EnableAutoRelayWithStaticRelays()`. The relay server makes reservations for peers, enabling them to be reached through the relay.

**Consequences**: All traffic flows through the relay when direct connection fails. Relay becomes a critical infrastructure component - must be hardened, monitored, and eventually made redundant. Batch I shipped every-peer-is-a-relay (I-f), beginning the path to relay VPS elimination.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/pkg/p2pnet/network.go:140`, `cmd/relay-server/`

---

### ADR-005: Why Connection Gating via `authorized_keys`

**Context**: peer-up networks are private. Only explicitly authorized peers should connect. Needed an SSH-like trust model.

**Alternatives considered**:
- **Certificate authority** - More scalable, supports expiration. Rejected because it requires PKI infrastructure (CA key management, certificate issuance), which contradicts "no central authority."
- **Pre-shared keys** - Simpler than CA. Rejected because it doesn't provide per-peer identity.
- **No gating, encryption only** - Let any peer connect but encrypt traffic. Rejected because authorization is a core security requirement, not optional.

**Decision**: `authorized_keys` file containing one peer ID per line, checked by `auth.AuthorizedPeerGater` in `InterceptSecured()`. Only inbound connections are gated; outbound (to relay, DHT) are always allowed.

**Consequences**: Simple file-based auth that users already understand from SSH. Hot-reloadable at runtime. Scales to hundreds of peers. Does not support per-peer permissions (all-or-nothing access) - acceptable for current scope.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/internal/auth/gater.go`, `https://github.com/satindergrewal/peer-up/blob/main/internal/auth/keys.go`

---

### ADR-006: Why Single Binary with Subcommands

**Context**: peer-up has many functions: daemon, ping, proxy, config management, relay server (separate binary). Needed a clean CLI structure.

**Alternatives considered**:
- **Separate binaries per function** - `peerup-daemon`, `peerup-ping`, etc. Rejected because it complicates distribution and PATH management.
- **cobra/urfave CLI framework** - Feature-rich. Rejected because they add dependency weight and complexity for what's essentially a dispatch table. Standard library `flag` + manual dispatch is lighter and fully sufficient.

**Decision**: Single `peerup` binary using `os.Args[1]` dispatch (`https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/main.go`) with standard library `flag` for each subcommand. Relay server is a separate binary (`cmd/relay-server/`) because it has different deployment concerns (VPS vs local machine).

**Consequences**: The binary includes all functionality, so it's slightly larger than specialized binaries would be. Accepted because single-binary deployment is a core principle - `curl install | sh` drops one file.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/main.go`

---

### ADR-007: Why YAML Config

**Context**: peer-up needs configuration for identity, network, relay, discovery, security, services, and names. Needed a human-readable, editable format.

**Alternatives considered**:
- **TOML** - Good for flat config. Rejected because nested structures (services map, relay addresses) are more natural in YAML.
- **JSON** - Universal. Rejected because no comments, poor human editability for config files users need to hand-edit.
- **HCL** - HashiCorp's format. Rejected because it adds a dependency and is unfamiliar to most users.
- **Flags/env vars only** - Simpler. Rejected because the configuration is too complex for command-line flags alone.

**Decision**: YAML via `gopkg.in/yaml.v3`. Single config file with versioning (`version: 1`), duration strings (`10m`, `1h`), and relative path resolution.

**Consequences**: YAML is sensitive to indentation, which can confuse users. Mitigated by: (a) `peerup init` generates valid config automatically, (b) `peerup config validate` catches syntax errors, (c) config templates in `config_template.go` ensure consistency.

**Reference**: `https://github.com/satindergrewal/peer-up/blob/main/internal/config/types.go`, `https://github.com/satindergrewal/peer-up/blob/main/internal/config/loader.go`, `https://github.com/satindergrewal/peer-up/blob/main/cmd/peerup/config_template.go`

---

### ADR-008: Why No External Dependencies Beyond libp2p

**Context**: Every dependency is an attack surface, a binary size cost, and a maintenance burden. peer-up is infrastructure software.

**Alternatives considered**: N/A - this is a constraint, not a choice between options.

**Decision**: The only direct dependencies are `go-libp2p`, `go-libp2p-kad-dht`, `go-multiaddr`, and `gopkg.in/yaml.v3`. Everything else (logging, config, auth, watchdog, QR codes) is implemented with Go standard library.

**Consequences**: More code to maintain (e.g., pure-Go sd_notify instead of using a systemd library), but complete control over behavior, smaller binary, and zero supply chain risk beyond the libp2p ecosystem.

**Reference**: `go.mod` (4 direct dependencies)
