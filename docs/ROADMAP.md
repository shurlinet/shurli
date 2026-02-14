# peer-up Development Roadmap

This document outlines the multi-phase evolution of peer-up from a simple NAT traversal tool to a comprehensive decentralized P2P network infrastructure.

## Philosophy

> **Build for 1-5 years. Make it adaptable. Don't predict 2074.**

- ✅ **Modular architecture** - Easy to add/swap components
- ✅ **Library-first** - Core logic reusable in other projects
- ✅ **Progressive enhancement** - Each phase adds value independently
- ✅ **No hard dependencies** - Works without optional features (naming, blockchain, etc.)
- ✅ **Local-first** - Offline-capable, no central services required
- ✅ **Self-sovereign** - No accounts, no telemetry, no vendor dependency

---

## Phase 1: Configuration Infrastructure ✅ COMPLETE

**Goal**: Externalize all hardcoded values to YAML configuration files.

**Status**: ✅ Completed

**Deliverables**:
- [x] `internal/config` package for loading YAML configs
- [x] Sample configuration files in `configs/`
- [x] Updated `.gitignore` for config files
- [x] Refactored home-node/client-node/relay-server to use configs

**Key Files**:
- `internal/config/config.go` - Configuration structs
- `internal/config/loader.go` - YAML parsing
- `configs/*.sample.yaml` - Sample configurations

---

## Phase 2: Key-Based Authentication ✅ COMPLETE

**Goal**: Implement SSH-style authentication using ConnectionGater and authorized_keys files.

**Status**: ✅ Completed

**Deliverables**:
- [x] `internal/auth/gater.go` - ConnectionGater implementation (primary defense)
- [x] `internal/auth/authorized_keys.go` - Parser for authorized_keys
- [x] Integration into home-node and client-node
- [x] Protocol-level validation (defense-in-depth)
- [x] Relay server authentication (optional)

**Security Model**:
- **Layer 1**: ConnectionGater (network level - earliest rejection)
- **Layer 2**: Protocol handler validation (application level - secondary check)

---

## Phase 3: Enhanced Usability - keytool CLI ✅ COMPLETE

**Goal**: Create production-ready CLI tool for managing Ed25519 keypairs and authorized_keys.

**Status**: ✅ Completed

**Deliverables**:
- [x] `cmd/keytool` with 5 commands: generate, peerid, validate, authorize, revoke
- [x] Comment-preserving parser for authorize/revoke
- [x] Color-coded terminal output
- [x] Integration with existing auth system
- [x] Comprehensive documentation in README

**Commands**:
- `keytool generate` - Create new Ed25519 keypair
- `keytool peerid` - Extract peer ID from key file
- `keytool validate` - Check authorized_keys format
- `keytool authorize` - Add peer to authorized_keys
- `keytool revoke` - Remove peer from authorized_keys

---

## Phase 4: Service Exposure & Core Library

**Goal**: Transform peer-up into a reusable library and enable exposing local services through P2P connections.

### Phase 4A: Core Library & Service Registry ✅ COMPLETE

**Timeline**: 2-3 weeks
**Status**: ✅ Completed

**Deliverables**:
- [x] Create `pkg/p2pnet/` as importable package
  - [x] `network.go` - Core P2P network setup, relay helpers, name resolution
  - [x] `service.go` - Service registry and management
  - [x] `proxy.go` - Bidirectional TCP↔Stream proxy with half-close
  - [x] `naming.go` - Local name resolution (name → peer ID)
  - [x] `identity.go` - Ed25519 identity management
- [x] Extend config structs for service definitions
- [x] Update sample YAML configs with service examples
- [x] Refactor to `cmd/` layout with single Go module
- [x] Tested: SSH, XRDP, generic TCP proxy all working across LAN and 5G
- [x] **UX Streamlining**:
  - [x] Single binary — merged home-node into `peerup serve`
  - [x] Standard config path — auto-discovery (`./peerup.yaml` → `~/.config/peerup/config.yaml` → `/etc/peerup/config.yaml`)
  - [x] `peerup init` — interactive setup wizard (generates config, keys, authorized_keys)
  - [x] All commands support `--config <path>` flag
  - [x] Unified config type (one config format for all modes)

**Key Files**:
- `cmd/peerup/` - Single binary with subcommands: init, serve, proxy, ping
- `pkg/p2pnet/` - Reusable P2P networking library
- `internal/config/loader.go` - Config discovery, loading, path resolution

---

### Phase 4B: Frictionless Onboarding ✅ COMPLETE

**Timeline**: 1-2 weeks
**Status**: ✅ Completed

**Goal**: Eliminate manual key exchange and config editing. Get two machines connected in under 60 seconds.

**Rationale**: The current flow (generate key → share peer ID → edit authorized_keys → write config) has 4 friction points before anything works. This is the single biggest adoption barrier.

**Deliverables**:
- [x] `peerup invite` — generate short-lived invite code (encodes relay address + peer ID)
- [x] `peerup join <code>` — accept invite, exchange keys, auto-configure, connect
- [x] QR code output for `peerup invite` (scannable by mobile app later)
- [x] `peerup whoami` — show own peer ID and friendly name for sharing
- [x] `peerup auth add <peer-id> --comment "friend"` — append to authorized_keys
- [x] `peerup auth list` — show authorized peers
- [x] `peerup auth remove <peer-id>` — revoke access
- [x] `peerup relay add/list/remove` — manage relay addresses without editing YAML
- [x] Flexible relay address input — accept `IP:PORT` or bare `IP` (default port 7777) in addition to full multiaddr
- [x] QR code display in `peerup init` (peer ID) and `peerup invite` (invite code)
- [x] Relay connection info + QR code in `setup.sh --check`

**Security hardening** (done as part of 4B):
- [x] Sanitize authorized_keys comments (prevent newline injection)
- [x] Sanitize YAML names from remote peers (prevent config injection)
- [x] Limit invite/join stream reads to 512 bytes (prevent OOM DoS)
- [x] Validate multiaddr before writing to config YAML
- [x] Use `os.CreateTemp` for atomic writes (prevent symlink attacks)
- [x] Reject hostnames in relay input — only IP addresses accepted (no DNS resolution / SSRF)
- [x] Config files written with 0600 permissions

**Key Files**:
- `cmd/peerup/cmd_auth.go` — auth add/list/remove subcommands
- `cmd/peerup/cmd_whoami.go` — show peer ID
- `cmd/peerup/cmd_invite.go` — generate invite code + QR + P2P handshake
- `cmd/peerup/cmd_join.go` — decode invite, connect, auto-configure
- `cmd/peerup/cmd_relay.go` — relay add/list/remove subcommands
- `cmd/peerup/relay_input.go` — flexible relay address parsing (IP, IP:PORT, multiaddr)
- `internal/auth/manage.go` — shared AddPeer/RemovePeer/ListPeers with input sanitization
- `internal/invite/code.go` — binary invite code encoding/decoding (base32)

**User Experience**:
```bash
# Machine A (home server)
$ peerup invite --name home
=== Invite Code (expires in 10m0s) ===
AEQB-XJKZ-M4NP-...
[QR code displayed]
Waiting for peer to join...

# Machine B (laptop)
$ peerup join AEQB-XJKZ-M4NP-... --name laptop
=== Joined successfully! ===
Peer "home" authorized and added to names.
Try: peerup ping home

# Or use CLI auth commands directly:
$ peerup auth add 12D3KooW... --comment "friend"
$ peerup auth list
$ peerup auth remove 12D3KooW...

# Manage relay servers:
$ peerup relay add 203.0.113.50:7777 --peer-id 12D3KooW...
$ peerup relay list
$ peerup relay remove /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
```

**Security**:
- Invite codes are short-lived (configurable TTL, default 10 minutes)
- One-time use — code is invalidated after successful join
- Relay mediates the handshake but never sees private keys
- Both sides must be online simultaneously during join
- Stream reads capped at 512 bytes to prevent OOM attacks
- All user-facing inputs sanitized before writing to files

---

### Phase 4C: Core Hardening & Security

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Harden the core for production reliability. Fix critical security gaps, add test coverage, and improve connection resilience before expanding features.

**Rationale**: The relay has no resource limits, there are zero tests, and connections don't survive relay restarts. These must be solid before wider adoption — especially before distribution (4F) puts binaries in more hands.

**Deliverables**:

**Security (Critical)**:
- [ ] Relay resource limits — replace `WithInfiniteLimits()` with explicit caps (max reservations, circuits, bandwidth per peer)
- [ ] Auth hot-reload — file watcher or SIGHUP to reload `authorized_keys` without restart (revoke access immediately)
- [ ] Per-service access control — allow granting specific peers access to specific services only
- [ ] Rate limiting on incoming connections and streams (per-peer throttling)
- [x] Config file permissions — write with 0600 (not 0644) *(done in Phase 4B)*
- [ ] Key file permission check on load — warn/refuse if not 0600
- [ ] Service name validation — reject special characters that could create ambiguous protocol IDs
- [x] Relay address validation in `peerup init` — parse multiaddr before writing config *(done in Phase 4B)*

**Reliability**:
- [ ] Reconnection with exponential backoff — recover from relay drops automatically
- [ ] Connection warmup — pre-establish connection to target peer at `peerup proxy` startup
- [ ] Stream pooling — reuse streams instead of creating fresh ones per TCP connection
- [ ] DHT bootstrap in proxy command — enable DCUtR hole-punching (currently proxy always relays)
- [ ] Graceful shutdown — replace `os.Exit(0)` with proper cleanup, drain active connections
- [ ] Goroutine lifecycle — use `select` + `context.Done()` instead of bare `time.Sleep` loops
- [ ] TCP dial timeout — `net.DialTimeout(5s)` for local service connections
- [ ] Fix data race in bootstrap peer counter (`atomic.AddInt32`)

**Code Quality**:
- [ ] Unit test suite — auth (gater, authorized_keys), config (loader, validation), naming, proxy
- [ ] Structured logging — migrate to `log/slog` with levels and structured fields
- [ ] Sentinel errors — define `ErrServiceNotFound`, `ErrPeerNotAuthorized`, etc.
- [ ] Deduplicate proxy pattern — extract single `bidirectionalProxy()` function (currently copy-pasted 4x)
- [ ] Consolidate config loaders — unify `LoadHomeNodeConfig`/`LoadClientNodeConfig`
- [ ] Upgrade relay-server libp2p to v0.47.0 (currently 9 minor versions behind main module)
- [ ] Health/status endpoint — expose connection state, relay status, active streams
- [ ] `peerup status` command — show peer online status, connection type (relay/direct), latency

---

### Phase 4D: Plugin Architecture & SDK

**Timeline**: 1-2 weeks
**Status**: 📋 Planned

**Goal**: Make peer-up extensible by third parties. Define clean interfaces, add extension points, and document the SDK.

**Rationale**: A solo developer can't build everything. Interfaces and hooks let the community add auth backends, name resolvers, service middleware, and monitoring — without forking. This also makes the codebase easier to test and maintain.

**Deliverables**:

**Core Interfaces** (new file: `pkg/p2pnet/interfaces.go`):
- [ ] `PeerNetwork` — interface for core network operations (expose, connect, resolve, close)
- [ ] `Resolver` — interface for name resolution (resolve, register). Enables chaining: local → DNS → DHT → blockchain
- [ ] `ServiceManager` — interface for service registration and dialing. Enables middleware.
- [ ] `Authorizer` — interface for authorization decisions. Enables pluggable auth (certs, tokens, database)
- [ ] `Logger` — interface for structured logging injection

**Extension Points**:
- [ ] Constructor injection — `Network.Config` accepts optional `Resolver`, `ConnectionGater`, `Logger`
- [ ] Event hook system — `OnEvent(handler)` for peer connected/disconnected, auth allow/deny, service registered, stream opened
- [ ] Stream middleware — `ServiceRegistry.Use(middleware)` for compression, bandwidth limiting, audit trails
- [ ] Protocol ID formatter — configurable protocol namespace and versioning

**Library Consolidation**:
- [ ] Extract DHT/relay bootstrap from CLI into `pkg/p2pnet/bootstrap.go`
- [ ] Centralize orchestration — new commands become ~20 lines instead of ~200
- [ ] Package-level documentation for `pkg/p2pnet/`

**SDK Documentation**:
- [ ] `docs/SDK.md` — guide for building on `pkg/p2pnet`
- [ ] Example: custom name resolver plugin
- [ ] Example: auth middleware (rate limiting, logging)
- [ ] Example: service middleware (bandwidth metering)

**Plugin Interface Preview**:
```go
// Third-party resolver
type DNSResolver struct { ... }
func (r *DNSResolver) Resolve(name string) (peer.ID, error) { ... }

// Third-party auth
type DatabaseAuthorizer struct { ... }
func (a *DatabaseAuthorizer) IsAuthorized(p peer.ID) bool { ... }

// Wire it up
net, _ := p2pnet.New(&p2pnet.Config{
    Resolver:        &DNSResolver{},
    ConnectionGater: &DatabaseAuthorizer{},
    Logger:          slog.Default(),
})

// React to events
net.OnEvent(func(e p2pnet.Event) {
    if e.Type == p2pnet.EventPeerConnected {
        metrics.PeerConnections.Inc()
    }
})
```

---

### Phase 4E: File Sharing

**Timeline**: 1 week
**Status**: 📋 Planned

**Goal**: Simple peer-to-peer file transfer between authorized devices.

**Rationale**: Before people need SSH tunnels or GPU inference, they need to send files. This is a universal use case that gives people a reason to install peer-up *today*. Low effort — builds directly on existing bidirectional streams.

**Deliverables**:
- [ ] `peerup send <file> --to <peer>` — send a file to an authorized peer
- [ ] `peerup receive` — listen for incoming file transfers
- [ ] Auto-accept from authorized peers (configurable)
- [ ] Progress bar and transfer speed display
- [ ] Resume interrupted transfers
- [ ] Directory transfer support (`peerup send ./folder --to laptop`)

**Usage**:
```bash
# Send a file
$ peerup send photo.jpg --to laptop
Sending photo.jpg (4.2 MB) to laptop...
████████████████████████████ 100% — 4.2 MB/s
✓ Transfer complete

# Send to multiple peers
$ peerup send presentation.pdf --to home --to phone

# Receive mode (optional — auto-accept if peer is authorized)
$ peerup receive --save-to ~/Downloads/
Waiting for transfers...
```

---

### Phase 4F: Distribution & Install

**Timeline**: 1 week
**Status**: 📋 Planned

**Goal**: Make peer-up installable without a Go toolchain. Nobody will try later features if they can't `curl | sh` the binary.

**Rationale**: High impact, low effort. Prerequisite for wider adoption.

**Deliverables**:
- [ ] Set up [GoReleaser](https://goreleaser.com/) config (`.goreleaser.yaml`)
- [ ] GitHub Actions workflow: on tag push, build binaries for Linux/macOS/Windows (amd64 + arm64)
- [ ] Publish to GitHub Releases with checksums
- [ ] Homebrew tap: `brew install satindergrewal/tap/peerup`
- [ ] One-line install script: `curl -sSL https://get.peerup.dev | sh`
- [ ] APT repository for Debian/Ubuntu
- [ ] AUR package for Arch Linux
- [ ] Docker image + `docker-compose.yml` for containerized deployment
- [ ] Use case guides: IoT/smart home remote access, media server sharing, game server hosting

**Result**: Zero-dependency install on any platform.

---

### Phase 4G: Desktop Gateway Daemon + Private DNS

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Create multi-mode gateway daemon for transparent service access, backed by a private DNS zone on the relay that is never exposed to the public internet.

**Deliverables**:

**Client-side Gateway**:
- [ ] `cmd/gateway/` - Gateway daemon with multiple modes
- [ ] **Mode 1**: SOCKS5 proxy (localhost:1080)
- [ ] **Mode 2**: Local DNS server (`.p2p` TLD)
- [ ] **Mode 3**: TUN/TAP virtual network interface (requires root)
- [ ] `/etc/hosts` integration for local name overrides
- [ ] Virtual IP assignment (10.64.0.0/16 range)
- [ ] Subnet routing — route entire LAN segments through tunnel (access printers, cameras, IoT without per-device install)
- [ ] Trusted network detection — auto-disable tunneling when already on home LAN

**Relay-side Private DNS**:
- [ ] Lightweight DNS zone on the relay server (e.g., CoreDNS or custom)
- [ ] Exposed **only** via P2P protocol — never bound to public UDP/53
- [ ] Relay operator configures a real domain (e.g., `example.com`) pointing to the VPS IP
- [ ] Subdomains (`bob.example.com`, `home.example.com`) assigned on the relay, resolvable only within the P2P network
- [ ] Public DNS returns NXDOMAIN for all subdomains — they don't exist outside the network
- [ ] Gateway daemon queries relay's private DNS as upstream resolver

**Private DNS Architecture**:
```
Public Internet:
  example.com → 123.123.123.123 (relay VPS)    ← public, A record
  bob.example.com → NXDOMAIN                    ← not in public DNS
  home.example.com → NXDOMAIN                   ← not in public DNS

Inside P2P network (via relay's private DNS):
  bob.example.com → Bob's peer ID → Bob's services
  home.example.com → Home's peer ID → SSH, XRDP, Ollama
```

**How it works**:
1. Relay operator owns `example.com`, points it to the relay VPS
2. Relay runs a private DNS zone mapping `<name>.example.com` → peer ID
3. Peers register their friendly name with the relay on connect
4. Client gateway daemon queries the relay's DNS over a P2P stream (not raw UDP)
5. Gateway translates the response into a local DNS answer for the OS
6. Subdomains stay private — no DNS records ever created on public registrars

**Usage Examples**:
```bash
# Mode 1: SOCKS proxy (no root needed)
peerup-gateway --mode socks --port 1080
# Configure apps to use SOCKS proxy

# Mode 2: DNS server (queries relay's private DNS)
peerup-gateway --mode dns --port 53
# Resolves: home.example.com → virtual IP (via relay's private zone)

# Mode 3: Virtual network (requires root)
sudo peerup-gateway --mode tun --network 10.64.0.0/16
# Creates virtual interface, transparent routing
```

**Connection Examples**:
```bash
# After gateway is running:
ssh user@home.example.com        # resolved privately via relay
curl http://bob.example.com:8080 # never touches public DNS
mount -t cifs //home.example.com/media /mnt/media
```

---

### Phase 4H: GPU Inference & Streaming Polish

**Timeline**: 1-2 weeks
**Status**: 📋 Planned

**Goal**: Polish GPU inference and low-latency streaming into first-class, demo-ready features.

**Rationale**: The Starlink + home GPU crowd is a real, underserved audience actively searching for solutions. No competing tool markets this use case — peer-up can own the narrative.

**Deliverables**:
- [ ] `peerup serve --ollama` shortcut (auto-detects Ollama on localhost:11434)
- [ ] `peerup serve --vllm` shortcut (auto-detects vLLM on localhost:8000)
- [ ] Health check endpoint — verify GPU service is reachable before exposing
- [ ] Streaming response support verification (chunked transfer for LLM output)
- [ ] Latency/throughput benchmarks (relay vs direct via DCUtR)
- [ ] Example: phone → relay → home 5090 → streaming LLM response
- [ ] Game/media streaming optimization — test Moonlight/Sunshine tunneling, document latency characteristics
- [ ] `peerup wake <peer>` — Wake-on-LAN integration (send magic packet before connecting)
- [ ] Blog post / demo: *"Access your home GPU from anywhere through Starlink CGNAT"*

**How it works**:
- Home machine runs Ollama, vLLM, or TGI on the GPU
- `peerup serve` exposes it as a service (e.g., `ollama` on `localhost:11434`)
- Remote VPS or laptop runs `peerup proxy home ollama 11434`
- VPS sends prompts, gets completions back — only text over the wire
- Home IP/ports never exposed to the internet

**Config (home machine)**:
```yaml
services:
  ollama:
    enabled: true
    local_address: "localhost:11434"
```

**Multi-GPU / distributed inference**:
- Multiple LAN machines with GPUs run exo or llama.cpp RPC
- One machine runs `peerup serve` as the entry point
- Remote peers connect through the single peer-up tunnel
- Cluster stays on LAN, only the API endpoint is exposed via P2P

---

### Phase 4I: Mobile Applications

**Timeline**: 3-4 weeks
**Status**: 📋 Planned

**Goal**: Native iOS and Android apps with VPN-like functionality.

**Rationale**: Phone → relay → home GPU is the dream demo. Mobile closes the loop on "access your stuff from anywhere."

**iOS Strategy**:
- **Primary**: NEPacketTunnelProvider (VPN mode)
  - Full TUN interface
  - Virtual network support
  - Frame as "self-hosted personal network" (like WireGuard)
- **Fallback**: SOCKS proxy app (if VPN rejected by Apple)
- **Apple Review Approach**: "Connect to your own devices via relay server"

**Android Strategy**:
- VPNService API (full feature parity)
- TUN interface
- No approval process limitations

**Deliverables**:
- [ ] iOS app with NEPacketTunnelProvider
- [ ] Android app with VPNService
- [ ] Mobile-optimized config UI
- [ ] QR code scanning for `peerup invite` codes
- [ ] Background connection maintenance
- [ ] Battery optimization
- [ ] Per-app SDK for third-party integration

**User Experience**:
```
iOS/Android App Config:
├─ Scan QR Code (from peerup invite)
├─ Or enter invite code: ABCX-7KMN-P2P3
└─ Connect Button

Once connected:
- SSH clients work: ssh user@home
- Browsers work: http://laptop:8080
- Native apps work: Plex connects to home.grewal:32400
- Chat with home LLM via Ollama API
```

---

### Phase 4J: Federation - Network Peering

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Enable relay-to-relay federation for cross-network communication.

**Rationale**: Only matters once you have multiple users with their own networks. Deferred until adoption features ship first.

**Deliverables**:
- [ ] Relay federation configuration
- [ ] Network-scoped naming (`host.network`)
- [ ] Cross-network routing protocol
- [ ] Trust/authorization between networks
- [ ] Route advertisement and discovery
- [ ] Multi-network client support — single client connected to multiple independent networks simultaneously

**Federation Config Example**:
```yaml
# relay-server.yaml
network:
  name: "grewal"

federation:
  enabled: true
  peers:
    - network_name: "alice"
      relay: "/ip4/45.67.89.12/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"

    - network_name: "bob"
      relay: "/dns4/bob-relay.com/tcp/7777/p2p/12D3KooW..."
      trust_level: "full"

  routing:
    allow_transit: true  # Let alice → bob via your relay
```

**Usage**:
```bash
# From your network, access friend's services:
ssh user@laptop.alice
curl http://desktop.bob:8080
```

**Architecture**:
```
┌─────────────────┐      ┌─────────────────┐      ┌─────────────────┐
│  Your Network   │      │  Alice Network  │      │   Bob Network   │
│    "grewal"     │◄────►│     "alice"     │◄────►│      "bob"      │
│                 │      │                 │      │                 │
│  ├─ laptop      │      │  ├─ desktop     │      │  ├─ server      │
│  └─ relay.      │      │  └─ relay.      │      │  └─ relay.      │
│     grewal      │      │     alice       │      │     bob         │
└─────────────────┘      └─────────────────┘      └─────────────────┘
```

---

### Phase 4K: Advanced Naming Systems (Optional)

**Timeline**: 2-3 weeks
**Status**: 📋 Planned

**Goal**: Pluggable naming architecture supporting multiple backends.

**Deliverables**:
- [ ] Plugin architecture for name resolvers
- [ ] Built-in resolvers:
  - [ ] Local file (YAML/JSON)
  - [ ] DHT-based (federated)
  - [ ] mDNS (.local)
- [ ] Optional blockchain resolvers:
  - [ ] Ethereum smart contract
  - [ ] Bitcoin OP_RETURN
  - [ ] ENS (.eth domains)
- [ ] IPFS/Arweave archiving for redundancy

**Name Resolution Tiers**:

**Tier 1: Local Override** (Free, Instant)
```yaml
# ~/.peerup/names.yaml
names:
  home: "12D3KooWHome..."
  laptop: "12D3KooWLaptop..."
```

**Tier 2: Network-Scoped** (Free, Federated)
```
Format: <hostname>.<network>
Examples: laptop.grewal, desktop.alice
Resolution: Ask relay for peer ID
```

**Tier 3: Blockchain-Anchored** (Paid, Guaranteed)
```
Register on Ethereum: peerup register grewal --chain ethereum
Cost: ~$10-50 one-time
Format: <hostname>.grewal (globally unique)
```

**Tier 4: Existing Blockchain DNS** (Premium)
```
Use ENS: grewal.eth ($5-640/year)
Format: laptop.grewal.eth
```

**Plugin Interface**:
```go
type NameResolver interface {
    Resolve(name string) (peer.ID, error)
}

// Users can add custom resolvers
resolvers := []NameResolver{
    LocalFileResolver,
    DHTResolver,
    BlockchainResolver,  // Optional
    ENSResolver,         // Optional
    CustomResolver,      // Your plugin
}
```

---

## Positioning & Community

### Privacy Narrative — peer-up's Moat

peer-up is not a cheaper Tailscale. It's the **self-sovereign alternative** for people who care about owning their network.

| | **peer-up** | **Tailscale** |
|---|---|---|
| **Accounts** | None — no email, no OAuth | Required (Google, GitHub, etc.) |
| **Telemetry** | Zero — no data leaves your network | Coordination server sees device graph |
| **Control plane** | None — relay only forwards bytes | Centralized coordination server |
| **Key custody** | You generate, you store, you control | Keys managed via their control plane |
| **Source** | Fully open, self-hosted | Open source client, proprietary control plane |

> *"Tailscale for people who don't want to trust a company with their network topology."*

### Target Audiences (in order of receptiveness)

1. **r/selfhosted** — Already run services at home, hate port forwarding, value self-sovereignty
2. **Starlink/CGNAT users** — Actively searching for solutions to reach home machines
3. **AI/ML hobbyists** — Home GPU + remote access is exactly their problem
4. **Privacy-conscious developers** — Won't use Tailscale because of the coordination server

### Launch Strategy

1. **Hacker News post**: *"Show HN: peer-up — self-hosted P2P tunnels through Starlink CGNAT (no accounts, no vendor)"*
2. **r/selfhosted post**: Focus on SSH + XRDP + GPU inference through CGNAT
3. **Blog post**: *"Access your home GPU from anywhere through Starlink CGNAT"*
4. **Demo video**: Phone → relay → home 5090 → streaming LLM response
5. **Comparisons**: Honest peer-up vs Tailscale / Zerotier / Netbird posts

---

## Phase 5+: Ecosystem & Polish

**Timeline**: Ongoing
**Status**: 📋 Conceptual

**Potential Features**:
- [ ] Web-based dashboard for network management
- [ ] Protocol marketplace (community-contributed service templates)
- [ ] Performance monitoring and analytics (Prometheus metrics)
- [ ] Automatic relay failover/redundancy
- [ ] Bandwidth optimization and QoS per peer/service
- [ ] Multi-relay routing for redundancy
- [ ] Integration with existing VPN clients (OpenVPN, WireGuard)
- [ ] Desktop apps (macOS, Windows, Linux)
- [ ] Browser extension for `.p2p` domain resolution
- [ ] Community relay network
- [ ] IPv6 transport testing and documentation
- [ ] Split tunneling (route only specific traffic through tunnel)

---

## Timeline Summary

| Phase | Duration | Status |
|-------|----------|--------|
| Phase 1: Configuration | ✅ 1 week | Complete |
| Phase 2: Authentication | ✅ 2 weeks | Complete |
| Phase 3: keytool CLI | ✅ 1 week | Complete |
| Phase 4A: Core Library + UX | ✅ 2-3 weeks | Complete |
| Phase 4B: Frictionless Onboarding | ✅ 1-2 weeks | Complete |
| **Phase 4C: Core Hardening & Security** | 📋 2-3 weeks | **Next** |
| Phase 4D: Plugin Architecture & SDK | 📋 1-2 weeks | Planned |
| Phase 4E: File Sharing | 📋 1 week | Planned |
| Phase 4F: Distribution & Install | 📋 1 week | Planned |
| Phase 4G: Desktop Gateway + Private DNS | 📋 2-3 weeks | Planned |
| Phase 4H: GPU Inference & Streaming | 📋 1-2 weeks | Planned |
| Phase 4I: Mobile Apps | 📋 3-4 weeks | Planned |
| Phase 4J: Federation | 📋 2-3 weeks | Planned |
| Phase 4K: Advanced Naming | 📋 2-3 weeks | Planned (Optional) |
| Phase 5+: Ecosystem | 📋 Ongoing | Conceptual |

**Total estimated time for Phase 4**: 18-26 weeks (5-6 months)

**Priority logic**: Onboarding first (remove friction) → harden the core (security, reliability, tests) → make it extensible (plugin architecture) → quick wins (file sharing, distribution) → transparent access (gateway, GPU streaming) → expand (mobile → federation → naming).

---

## Contributing

This roadmap is a living document. Phases may be reordered, combined, or adjusted based on:
- User feedback and demand
- Technical challenges discovered during implementation
- Emerging technologies (AI, quantum, blockchain alternatives)
- Community contributions

**Adaptability over perfection.** We build for the next 1-5 years, not 50.

---

## Success Metrics

**Phase 4A Success**:
- Library can be imported and used in external projects
- Services can be exposed and consumed via P2P
- At least 3 example services documented (SSH, HTTP, custom)

**Phase 4B Success**:
- Two machines connected via invite code in under 60 seconds
- Zero manual file editing required
- Invite codes expire and are single-use

**Phase 4C Success**:
- `go test -race ./...` passes with >60% coverage on auth, config, naming, proxy
- Relay has explicit resource limits (not infinite)
- `authorized_keys` changes take effect without restart
- Proxy command attempts DCUtR direct connection before falling back to relay
- Relay reconnection recovers automatically within 30 seconds

**Phase 4D Success**:
- Third-party code can implement custom `Resolver`, `Authorizer`, and stream middleware
- Event hooks fire for peer connect/disconnect and auth decisions
- New CLI commands require <30 lines of orchestration (bootstrap consolidated)
- SDK documentation published with working examples

**Phase 4E Success**:
- File transfer works between authorized peers
- Transfer speed saturates relay bandwidth
- Resume works after interrupted transfer

**Phase 4F Success**:
- GoReleaser builds binaries for 6 targets (linux/mac/windows × amd64/arm64)
- Homebrew tap works: `brew install satindergrewal/tap/peerup`
- Docker image available
- Install-to-running in under 30 seconds

**Phase 4G Success**:
- Gateway daemon works in all 3 modes (SOCKS, DNS, TUN)
- Private DNS on relay resolves subdomains only within P2P network
- Public DNS queries for subdomains return NXDOMAIN (zero leakage)
- Native apps connect using real domain names (e.g., `home.example.com`)

**Phase 4H Success**:
- `peerup serve --ollama` auto-detects and exposes Ollama
- Streaming LLM responses work end-to-end through relay
- Game/media streaming latency documented
- Blog post / demo published

**Phase 4I Success**:
- iOS app approved by Apple
- Android app published on Play Store
- QR code invite flow works mobile → desktop

**Phase 4J Success**:
- Two independent networks successfully federate
- Cross-network routing works transparently
- Trust model prevents unauthorized access

**Phase 4K Success**:
- At least 3 naming backends working (local, DHT, one optional)
- Plugin API documented and usable
- Migration path demonstrated when one backend fails

---

**Last Updated**: 2026-02-14
**Current Phase**: 4B Complete, 4C Next
**Next Milestone**: Core Hardening & Security (relay limits, tests, reconnection)
