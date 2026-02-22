# peer-up

Access your home server from anywhere. Share services with friends. No cloud, no account, no SaaS dependency.

**peer-up** connects your devices through firewalls and CGNAT using encrypted P2P tunnels with SSH-style authentication. One binary, zero configuration servers, works behind any NAT.

## News

| Date | What's New |
|------|-----------|
| 2026-02-18 | **Private DHT** - Peer discovery now runs on `/peerup/kad/1.0.0`, fully isolated from the public IPFS network |
| 2026-02-17 | **Daemon mode** - Background service with Unix socket API, cookie auth, and 14 REST endpoints |
| 2026-02-17 | **Network tools** - P2P ping, traceroute, and name resolution (standalone or via daemon) |
| 2026-02-16 | **Service management** - `peerup service add/remove/enable/disable` from the CLI |
| 2026-02-16 | **Config self-healing** - Archive, rollback, and commit-confirmed pattern for safe remote changes |
| 2026-02-16 | **AutoNAT v2** - Per-address reachability detection with nonce verification |
| 2026-02-16 | **Headless pairing** - `--non-interactive` flag for scripted invite/join workflows |
| 2026-02-15 | **Structured logging** - `log/slog` throughout, sentinel errors, build version embedding |

## What Can I Do With peer-up?

| Use Case | Command |
|----------|---------|
| SSH to your home machine behind CGNAT | `peerup proxy home ssh 2222` → `ssh -p 2222 localhost` |
| Remote desktop through NAT | `peerup proxy home xrdp 13389` → connect to `localhost:13389` |
| Share Jellyfin with a friend | `peerup invite` on your side, `peerup join <code>` on theirs |
| AI inference on a friend's GPU | `peerup proxy friend ollama 11434` → `curl localhost:11434` |
| Any TCP service, zero port forwarding | `peerup proxy <peer> <service> <local-port>` |
| Check connectivity | `peerup ping home` or `peerup traceroute home` |

peer-up works with **two machines and zero network effect** - useful from day one.

## Quick Start

### Path A: Joining someone's network

If someone shared an invite code with you:

```bash
# Install (or build from source: go build -o peerup ./cmd/peerup)
peerup join <invite-code> --name laptop
```

That's it. You're connected and mutually authorized.

### Path B: Setting up your own network

**1. Set up both machines:**
```bash
go build -o peerup ./cmd/peerup
peerup init
```

**2. Pair them (on the first machine):**
```bash
peerup invite --name home
# Shows invite code + QR code, waits for the other side...
```

**3. Join (on the second machine):**
```bash
peerup join <invite-code> --name laptop
```

**4. Use it:**
```bash
# On the server - start the daemon with services exposed
peerup daemon

# On the client - connect to a service
peerup proxy home ssh 2222
ssh -p 2222 user@localhost
```

> **Relay server**: Both machines connect through a relay for NAT traversal. See [relay-server/README.md](relay-server/README.md) for deploying your own. Run `peerup relay serve` to start a relay. A shared relay is used by default during development.

## Why peer-up exists

peer-up was created to solve one problem: reaching a service on a home server from outside the network without depending on anyone else's infrastructure.

Existing solutions require either a cloud account, a third-party VPN, or port forwarding - which CGNAT frequently makes impossible. They all share the same flaw: your connectivity depends on someone else's servers and their permission to keep it running.

peer-up uses a different model. Devices connect outbound to a lightweight relay for initial setup, then upgrade to direct peer-to-peer when possible. No accounts, no central identity server, no revocable subscriptions. Your keys stay on your machine, configuration lives in one YAML file, and you can run your own relay for zero external dependency.

## The Problem

Your devices are behind firewalls and NAT that block inbound connections. This affects:

- **Satellite ISPs** with Carrier-Grade NAT (CGNAT)
- **Mobile networks** (4G/5G), almost universally behind CGNAT
- **Many broadband providers** worldwide applying CGNAT to conserve IPv4 addresses
- **University and corporate networks** with strict firewalls
- **Double-NAT setups** - router behind router

Traditional solutions require either port forwarding (impossible with CGNAT), a VPN service (another dependency), or a cloud intermediary (defeats self-hosting). peer-up solves this with a lightweight relay that both sides connect to **outbound**, then upgrades to a direct connection when possible.

## Features

| Feature | Description |
|---------|-------------|
| **NAT Traversal** | Circuit relay v2 + DCUtR hole-punching. Works behind CGNAT, symmetric NAT, double-NAT |
| **SSH-Style Auth** | `authorized_keys` peer allowlist - only explicitly trusted peers can connect |
| **60-Second Pairing** | `peerup invite` + `peerup join` - exchanges keys, adds auth, maps names automatically |
| **TCP Service Proxy** | Forward any TCP port through P2P tunnels (SSH, XRDP, HTTP, databases, AI inference) |
| **Daemon Mode** | Background service with Unix socket API, cookie auth, hot-reload of auth keys |
| **Config Self-Healing** | Last-known-good archive, rollback, and commit-confirmed pattern for safe remote changes |
| **Private DHT** | Kademlia peer discovery on `/peerup/kad/1.0.0` - isolated from public networks |
| **Friendly Names** | Map names to peer IDs in config - `home`, `laptop`, `gpu-server` instead of raw peer IDs |
| **Reusable Library** | `pkg/p2pnet` - import into your own Go projects for P2P networking |
| **Single Binary** | One `peerup` binary with 15 subcommands. No runtime dependencies |
| **Cross-Platform** | Go cross-compiles to Linux, macOS, Windows, ARM, and more |
| **systemd + launchd** | Service files included for both Linux and macOS |

## How It Works

```
┌──────────┐         ┌──────────────┐         ┌──────────────┐
│  Client   │───────▶│ Relay Server │◀────────│    Server    │
│  (Phone)  │ outbound    (VPS)   outbound   │  (Linux/Mac) │
└──────────┘         └──────────────┘         └──────────────┘
                           │
                     Both connect OUTBOUND
                     Relay bridges the connection
                     DCUtR upgrades to direct P2P
```

1. **Server** runs `peerup daemon` behind CGNAT, connects outbound to a relay and reserves a slot
2. **Client** runs `peerup proxy`, connects outbound to the same relay and reaches the server through a circuit address
3. **DCUtR** (Direct Connection Upgrade through Relay) attempts hole-punching. If successful, traffic flows directly without the relay

Peer discovery uses a **private Kademlia DHT** - the relay server acts as bootstrap peer. Authentication is enforced at both the connection level (ConnectionGater) and the protocol level.

For the full architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Commands

### Daemon

| Command | Description |
|---------|-------------|
| `peerup daemon` | Start the daemon (P2P host + Unix socket control API) |
| `peerup daemon status [--json]` | Query running daemon status |
| `peerup daemon stop` | Graceful shutdown |
| `peerup daemon ping <target> [-c N] [--json]` | Ping a peer via daemon |
| `peerup daemon services [--json]` | List exposed services via daemon |
| `peerup daemon peers [--all] [--json]` | List connected peers (peerup-only by default) |
| `peerup daemon connect --peer <p> --service <s> --listen <addr>` | Create a TCP proxy via daemon |
| `peerup daemon disconnect <id>` | Tear down a proxy |

### Network Tools (standalone, no daemon required)

| Command | Description |
|---------|-------------|
| `peerup ping <target> [-c N] [--interval 1s] [--json]` | P2P ping with stats |
| `peerup traceroute <target> [--json]` | P2P traceroute through relay hops |
| `peerup resolve <name> [--json]` | Resolve a name to peer ID and addresses |
| `peerup proxy <target> <service> <local-port>` | Forward a local TCP port to a remote service |

### Identity & Access

| Command | Description |
|---------|-------------|
| `peerup whoami` | Show your peer ID |
| `peerup auth add <peer-id> [--comment "..."]` | Authorize a peer |
| `peerup auth list` | List authorized peers |
| `peerup auth remove <peer-id>` | Revoke a peer |
| `peerup auth validate` | Validate authorized_keys format |

### Configuration & Setup

| Command | Description |
|---------|-------------|
| `peerup init` | Interactive setup wizard (config, keys, authorized_keys) |
| `peerup config validate` | Validate config file |
| `peerup config show` | Show resolved configuration |
| `peerup config rollback` | Restore last-known-good config |
| `peerup config apply <file> [--confirm-timeout 5m]` | Apply config with auto-revert safety net |
| `peerup config confirm` | Confirm applied config (cancels auto-revert) |
| `peerup relay add/list/remove` | Manage relay server addresses |
| `peerup service add/remove/enable/disable/list` | Manage exposed services |

### Pairing

| Command | Description |
|---------|-------------|
| `peerup invite [--name "home"] [--non-interactive]` | Generate invite code + QR, wait for join |
| `peerup join <code> [--name "laptop"] [--non-interactive]` | Accept invite, auto-configure both sides |
| `peerup status` | Show local config, identity, authorized peers, services |
| `peerup version` | Show version, commit, build date, Go version |

The `<target>` in network commands accepts either a peer ID or a name from the `names:` section of your config. All commands support `--config <path>`.

## Daemon Mode

The daemon runs `peerup daemon` as a long-lived background process. It starts the full P2P host, exposes configured services, and opens a Unix socket API for management.

**Key features:**
- Unix socket at `~/.config/peerup/.daemon.sock` (no TCP exposure)
- Cookie-based auth (`~/.config/peerup/.daemon-cookie`) - 32-byte random token, rotated per restart
- Hot-reload of authorized_keys via `daemon` auth endpoints
- 14 REST endpoints for status, peers, services, auth, proxies, ping, traceroute, resolve

**Example:**
```bash
# Start the daemon
peerup daemon

# In another terminal - query status
peerup daemon status

# Create a proxy through the daemon
peerup daemon connect --peer home --service ssh --listen localhost:2222
```

For the full API reference: [docs/DAEMON-API.md](docs/DAEMON-API.md)

## Configuration

### Config Search Order

1. `--config <path>` flag (explicit)
2. `./peerup.yaml` (current directory)
3. `~/.config/peerup/config.yaml` (standard location, created by `peerup init`)
4. `/etc/peerup/config.yaml` (system-wide)

### Essential Config

```yaml
identity:
  key_file: "identity.key"

network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
    - "/ip4/0.0.0.0/udp/0/quic-v1"
  force_private_reachability: false  # true for servers behind CGNAT

relay:
  addresses:
    - "/ip4/YOUR_VPS_IP/tcp/7777/p2p/YOUR_RELAY_PEER_ID"

security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true

# services:       # Uncomment to expose services (server only)
#   ssh:
#     enabled: true
#     local_address: "localhost:22"

names: {}         # Map friendly names to peer IDs
#  home: "12D3KooW..."
```

Full sample configs: [configs/](configs/)

## Running as a Service

### Linux (systemd)

A service file is provided at [deploy/peerup-daemon.service](deploy/peerup-daemon.service):

```bash
sudo cp deploy/peerup-daemon.service /etc/systemd/system/peerup.service
# Edit ExecStart path and --config as needed
sudo systemctl daemon-reload
sudo systemctl enable --now peerup
```

Both `peerup daemon` and `peerup relay serve` send `sd_notify` signals (`READY=1`, `WATCHDOG=1`, `STOPPING=1`).

### macOS (launchd)

A plist is provided at [deploy/com.peerup.daemon.plist](deploy/com.peerup.daemon.plist).

### Relay Server

See [relay-server/README.md](relay-server/README.md) for the full VPS deployment guide (user creation, SSH hardening, firewall, systemd, health checks).

## Building

A Makefile is provided for common operations:

```bash
make build            # Build with version embedding and optimizations
make test             # Run all tests with race detection
make clean            # Remove build artifacts
make install          # Build, install to /usr/local/bin, and set up system service
make install-service  # Install and enable systemd (Linux) or launchd (macOS) service
make restart-service  # Restart the service after a rebuild
make uninstall        # Remove service and binary
make website          # Start Hugo development server for peerup.dev
make help             # Show all available targets
```

**Local checks**: `make check` runs commands from a `.checks` file (gitignored, one command per line). `make push` runs checks before pushing. Create your own `.checks` with any validation commands you need:

```bash
# Example .checks file
echo "Running lint..."
go vet ./...
```

You can also build directly with Go:

```bash
# Build peerup
go build -o peerup ./cmd/peerup

# Build with version info
go build -ldflags "-X main.version=0.1.0 \
  -X main.commit=$(git rev-parse --short HEAD) \
  -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  -o peerup ./cmd/peerup

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o peerup ./cmd/peerup

# Run tests
go test -race -count=1 ./...
```

## Library (`pkg/p2pnet`)

The `pkg/p2pnet` package is an importable Go library for building P2P applications:

```go
import "github.com/satindergrewal/peer-up/pkg/p2pnet"

// Create a P2P network
net, _ := p2pnet.New(&p2pnet.Config{
    KeyFile:     "myapp.key",
    EnableRelay: true,
    RelayAddrs:  []string{"/ip4/.../tcp/7777/p2p/..."},
})

// Expose a local service
net.ExposeService("api", "localhost:8080", nil)

// Connect to a peer's service
conn, _ := net.ConnectToService(peerID, "api")

// Name resolution
net.LoadNames(map[string]string{"home": "12D3KooW..."})
peerID, _ := net.ResolveName("home")
```

## Project Structure

```
cmd/
├── peerup/                    # Single binary with subcommands
│   ├── main.go                # Command dispatch (15 subcommands)
│   ├── cmd_daemon.go          # Daemon mode (start, stop, status, ping, peers, ...)
│   ├── cmd_proxy.go           # TCP proxy client
│   ├── cmd_ping.go            # Standalone P2P ping
│   ├── cmd_traceroute.go      # P2P traceroute
│   ├── cmd_resolve.go         # Name resolution
│   ├── cmd_init.go            # Interactive setup wizard
│   ├── cmd_invite.go          # Generate invite code + QR + P2P handshake
│   ├── cmd_join.go            # Accept invite, auto-configure
│   ├── cmd_auth.go            # Auth add/list/remove/validate
│   ├── cmd_relay.go           # Relay add/list/remove (client config)
│   ├── cmd_relay_serve.go     # Relay server: serve/authorize/info/config
│   ├── cmd_config.go          # Config validate/show/rollback/apply/confirm
│   ├── cmd_service.go         # Service add/remove/enable/disable/list
│   ├── cmd_status.go          # Local status display
│   ├── cmd_whoami.go          # Show peer ID
│   ├── serve_common.go        # Shared P2P runtime (used by daemon + standalone tools)
│   ├── config_template.go     # Config YAML template
│   ├── flag_helpers.go        # CLI flag reordering for natural usage
│   └── relay_input.go         # Flexible relay address parsing
pkg/p2pnet/                    # Importable P2P networking library
├── network.go                 # Core: host setup, relay, DHT, name resolution
├── service.go                 # Service registry
├── proxy.go                   # Bidirectional TCP↔Stream proxy with half-close
├── naming.go                  # Local name resolution (name → peer ID)
├── identity.go                # Identity helpers
├── ping.go                    # PingPeer() with streaming results
├── traceroute.go              # P2P traceroute
└── errors.go                  # Sentinel errors
internal/
├── config/                    # YAML configuration + self-healing
│   ├── config.go              # Config structs
│   ├── loader.go              # Auto-discovery, path resolution, validation
│   ├── archive.go             # Last-known-good archive/rollback
│   ├── confirm.go             # Commit-confirmed pattern
│   └── errors.go
├── auth/                      # Connection gating + authorized_keys
│   ├── gater.go               # ConnectionGater (blocks unauthorized at network level)
│   ├── authorized_keys.go     # File parser
│   ├── manage.go              # AddPeer/RemovePeer/ListPeers
│   └── errors.go
├── daemon/                    # Daemon API server + client library
│   ├── server.go              # Unix socket HTTP server with cookie auth
│   ├── handlers.go            # 14 REST endpoint handlers
│   ├── client.go              # Go client (auto-reads cookie, Unix transport)
│   ├── types.go               # Request/response types
│   └── errors.go
├── identity/                  # Ed25519 identity management
├── invite/                    # Invite code encoding (binary → base32 + dash groups)
├── validate/                  # Input validation (service names, DNS-label format)
├── watchdog/                  # Health monitoring + systemd sd_notify (pure Go)
├── qr/                        # QR code generation (zero dependencies)
└── termcolor/                 # Terminal color output
relay-server/                  # Deployment artifacts
├── setup.sh                   # Full VPS setup (build, permissions, systemd, health)
├── relay-server.service       # systemd unit file
└── README.md                  # VPS deployment guide
deploy/                        # Client service files
├── peerup-daemon.service      # systemd unit for peerup daemon
└── com.peerup.daemon.plist    # launchd plist for macOS
configs/                       # Sample configuration files
├── peerup.sample.yaml
├── relay-server.sample.yaml
└── authorized_keys.sample
docs/                          # Documentation
├── ARCHITECTURE.md            # Full architecture deep dive
├── DAEMON-API.md              # Daemon REST API reference
├── FAQ.md                     # Frequently asked questions
├── NETWORK-TOOLS.md           # Ping, traceroute, resolve guide
├── ROADMAP.md                 # Multi-phase implementation plan
├── TESTING.md                 # Test strategy and coverage
└── ENGINEERING-JOURNAL.md     # Architecture decision records (ADRs)
```

## Security

**Two layers of defense:**

1. **ConnectionGater** (network level) - Blocks unauthorized peers during the connection handshake, before any data is exchanged
2. **Protocol handler** (application level) - Secondary authorization check before processing requests

**Fail-safe defaults:**
- Connection gating enabled + no authorized_keys file → **refuses to start**
- Empty authorized_keys → **warns loudly** (allows for initial setup)
- All outbound connections allowed (required for DHT and relay)
- All unauthorized inbound connections blocked

**File permissions:**
```
chmod 600 *.key              # Private keys: owner read/write only
chmod 600 authorized_keys    # Peer allowlist: owner read/write only
chmod 644 *.yaml             # Configs: readable
```

For security details, relay hardening, and threat model: [docs/FAQ.md](docs/FAQ.md)

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `no config file found` | Run `peerup init` or use `--config <path>` |
| `Cannot resolve target` | Add name mapping to `names:` in config |
| `DENIED inbound connection` | Add peer ID to `authorized_keys`, restart daemon |
| `Invalid invite code` | Paste the full code as one argument (quote if spaces) |
| `Failed to connect to inviter` | Ensure `peerup invite` is still running |
| No `/p2p-circuit` addresses | Check `force_private_reachability: true` and relay address |
| `protocols not supported` | Relay server not running or unreachable |
| Bad config edit broke startup | `peerup config rollback` restores last-known-good |
| Remote config change went wrong | `peerup config apply new.yaml --confirm-timeout 5m`, then `config confirm` |
| `failed to sufficiently increase receive buffer size` | QUIC works but suboptimal - see UDP buffer tuning below |
| Daemon won't start (socket exists) | Stale socket from crash - daemon auto-detects and cleans up |

### UDP Buffer Tuning (QUIC)

QUIC works with default buffers but performs better with increased limits:

```bash
# Linux (persistent)
echo "net.core.rmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
echo "net.core.wmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
sudo sysctl --system
```

## Engineering Philosophy

This is not a weekend hobby project. peer-up is built as critical infrastructure, the kind where failure has real consequences for real people: financial, psychological, and potentially physical.

Think of it like a bubble in outer space. If it breaks, the people inside don't get a second chance. That standard guides everything here - from code quality to deployment to security decisions.

## Development

### AI-Assisted Development

peer-up is developed with significant AI assistance (Claude). All AI-generated code is reviewed, tested, and committed by a human maintainer. The architecture, vision, and engineering decisions are human-directed.

### No Cryptocurrency / No Token

peer-up is a networking tool. It has no token, no coin, no blockchain dependency, and no plans to add one. If someone tells you otherwise, they're not affiliated with this project.

### Contributing

Issues and PRs are welcome.

**Testing checklist:**
- [ ] `go build ./...` succeeds
- [ ] `go vet ./...` passes
- [ ] `go test -race -count=1 ./...` passes
- [ ] Unauthorized peer is denied, authorized peer connects
- [ ] Service proxy works end-to-end

## Documentation

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | Full architecture: relay circuit, DHT, proxy, auth system |
| [DAEMON-API.md](docs/DAEMON-API.md) | Daemon REST API reference (14 endpoints) |
| [FAQ.md](docs/FAQ.md) | Security FAQ, relay hardening, troubleshooting |
| [NETWORK-TOOLS.md](docs/NETWORK-TOOLS.md) | Ping, traceroute, resolve usage guide |
| [ROADMAP.md](docs/ROADMAP.md) | Multi-phase implementation plan |
| [TESTING.md](docs/TESTING.md) | Test strategy, coverage, integration tests |
| [ENGINEERING-JOURNAL.md](docs/ENGINEERING-JOURNAL.md) | Architecture decision records: why every design choice was made |

## Dependencies

- [go-libp2p](https://github.com/libp2p/go-libp2p) v0.47.0
- [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht) v0.28.1
- [go-multiaddr](https://github.com/multiformats/go-multiaddr)
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) v3.0.1

## License

MIT
