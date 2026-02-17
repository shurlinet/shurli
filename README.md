# peer-up: Decentralized P2P Network Infrastructure

A libp2p-based peer-to-peer network platform that enables secure connections across CGNAT networks with SSH-style authentication, service exposure, and local-first naming.

## Vision

**peer-up** is evolving from a simple NAT traversal tool into a comprehensive **decentralized P2P network infrastructure** that:

- Connects your devices across CGNAT/firewall barriers (Starlink, mobile networks)
- Exposes local services (SSH, XRDP, HTTP, SMB, custom protocols) through P2P connections
- Federates networks - Connect your network to friends' networks
- Works on mobile - iOS/Android apps with VPN-like functionality
- Flexible naming - Local names, network-scoped domains, optional blockchain anchoring
- Reusable library - Import `pkg/p2pnet` in your own Go projects

## Engineering Philosophy

This is not a weekend hobby project. peer-up is built as critical infrastructure — the kind where failure has real consequences for real people: financial, psychological, and potentially physical. Every line of code, every deployment decision, and every security choice is made with that weight in mind.

Think of it like a bubble in outer space. If it breaks, the people inside don't get a second chance. That standard guides everything here.

## Current Status

- **Single Binary** - One `peerup` binary with subcommands: `init`, `serve`, `proxy`, `ping`, `invite`, `join`, `whoami`, `auth`, `relay`, `config`, `version`
- **60-Second Onboarding** - `peerup invite` + `peerup join` pairs two machines with zero manual config
- **Easy Setup** - `peerup init` interactive wizard generates config, keys, and authorized_keys
- **Standard Config** - Auto-discovers config from `./peerup.yaml` or `~/.config/peerup/config.yaml`
- **Configuration-Based** - YAML config files, no hardcoded values
- **SSH-Style Authentication** - `authorized_keys` file for peer access control
- **NAT Traversal** - Works through Starlink CGNAT using relay + hole-punching
- **Persistent Identity** - Ed25519 keypairs saved to files
- **DHT Discovery** - Private Kademlia DHT (`/peerup/kad/1.0.0`) for peer discovery — isolated from public IPFS network
- **Direct Connection Upgrade** - DCUtR attempts hole-punching for direct P2P
- **CLI Auth & Relay Management** - `peerup auth` and `peerup relay` for managing peers and relays without editing files
- **Service Exposure** - Expose any TCP service (SSH, XRDP, HTTP, etc.) via P2P
- **Reusable Library** - `pkg/p2pnet` package for building P2P applications
- **Name Resolution** - Map friendly names to peer IDs in config

## The Problem

Starlink uses Carrier-Grade NAT (CGNAT) on IPv4, and blocks inbound IPv6 connections via router firewall. This makes direct peer-to-peer connections impossible without a relay.

## The Solution

```
┌──────────┐         ┌──────────────┐         ┌──────────────┐
│  Client   │───────▶│ Relay Server │◀────────│    Server    │
│  (Phone)  │ outbound    (VPS)   outbound   │  (Linux/Mac) │
└──────────┘         └──────────────┘         └──────────────┘
                           │
                     Both connect OUTBOUND
                     Authentication enforced
```

1. **Relay Server** (VPS) - Circuit relay with optional authentication via `authorized_keys`
2. **Server** (`peerup daemon`) - Exposes local services, accepts only authorized peers
3. **Client** (`peerup proxy`) - Connects to server's services through the relay

## Quick Start

### 1. Deploy Relay Server (VPS)

```bash
cd relay-server

# Create config from sample
cp ../configs/relay-server.sample.yaml relay-server.yaml
# Edit relay-server.yaml if needed (defaults are fine)

# Build and run (from project root)
cd ..
go build -o relay-server/relay-server ./cmd/relay-server
./relay-server/relay-server
```

Copy the **Relay Peer ID** from the output - you'll need it for the next steps.

### 2. Set Up a Node

Run the setup wizard on each machine (server and client):

```bash
# Build
go build -o peerup ./cmd/peerup

# Run the setup wizard
./peerup init
```

The wizard will:
1. Create `~/.config/peerup/` directory
2. Ask for your relay server address
3. Generate an Ed25519 identity key
4. Display your **Peer ID** (share this with peers who need to authorize you)
5. Write `config.yaml`, `identity.key`, and `authorized_keys`

### 3. Pair Machines (invite/join)

The fastest way to connect two machines — handles authorization and naming automatically:

**On the server (home machine):**
```bash
./peerup invite --name home
# Displays an invite code + QR code
# Waits for the other machine to join...
```

**On the client (laptop):**
```bash
./peerup join <invite-code> --name laptop
# Automatically: connects, exchanges keys, adds authorized_keys, adds name mapping
```

Both machines are now mutually authorized and can use friendly names.

**Alternative: Manual authorization**

If you prefer manual control, use the `auth` CLI commands:
```bash
# On each machine, add the other's peer ID:
peerup auth add 12D3KooW... --comment "home-server"
peerup auth list
peerup auth remove 12D3KooW...
```

### 4. Configure the Server

Edit `~/.config/peerup/config.yaml` on the server machine:

```yaml
network:
  force_private_reachability: true  # Required for CGNAT (Starlink, etc.)

# Uncomment and enable services you want to expose:
services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  xrdp:
    enabled: true
    local_address: "localhost:3389"
```

### 5. Run

**On the server:**
```bash
peerup daemon
```

**On the client:**
```bash
# SSH
peerup proxy home ssh 2222
# Then: ssh -p 2222 user@localhost

# Remote desktop (XRDP)
peerup proxy home xrdp 13389
# Then: xfreerdp /v:localhost:13389 /u:user

# Any TCP service
peerup proxy home web 8080
# Then: http://localhost:8080

# Test connectivity
peerup ping home
```

## Project Structure

```
├── cmd/
│   ├── peerup/                 # Single binary with subcommands
│   │   ├── main.go             # Command dispatch
│   │   ├── cmd_init.go         # Interactive setup wizard
│   │   ├── cmd_serve.go        # Server mode (expose services)
│   │   ├── cmd_proxy.go        # TCP proxy client
│   │   ├── cmd_ping.go         # Connectivity test
│   │   ├── cmd_whoami.go       # Show own peer ID
│   │   ├── cmd_auth.go         # Auth add/list/remove/validate subcommands
│   │   ├── cmd_relay.go        # Relay add/list/remove subcommands
│   │   ├── cmd_config.go       # Config validate/show/rollback/apply/confirm
│   │   ├── cmd_invite.go       # Generate invite code + QR + P2P handshake
│   │   ├── cmd_join.go         # Decode invite, connect, auto-configure
│   │   └── relay_input.go      # Flexible relay address parsing
│   └── relay-server/           # Circuit relay v2 source
│       └── main.go
├── pkg/p2pnet/                 # Reusable P2P networking library
│   ├── network.go              # Core network setup, relay helpers, name resolution
│   ├── service.go              # Service registry (delegates to internal/validate)
│   ├── proxy.go                # Bidirectional TCP↔Stream proxy with half-close
│   ├── naming.go               # Local name resolution (name → peer ID)
│   └── identity.go             # Identity helpers (delegates to internal/identity)
├── internal/
│   ├── config/                 # YAML configuration loading + self-healing
│   │   ├── config.go
│   │   ├── loader.go
│   │   ├── archive.go          # Last-known-good config archive/rollback
│   │   └── confirm.go          # Commit-confirmed pattern (safe remote changes)
│   ├── auth/                   # Authentication system
│   │   ├── authorized_keys.go
│   │   ├── gater.go
│   │   └── manage.go           # AddPeer/RemovePeer/ListPeers
│   ├── identity/               # Ed25519 identity management (shared)
│   │   └── identity.go         # CheckKeyFilePermissions, LoadOrCreateIdentity, PeerIDFromKeyFile
│   ├── invite/                 # Invite code encoding/decoding
│   │   └── code.go             # Binary → base32 with dash grouping
│   ├── validate/               # Input validation helpers
│   │   └── validate.go         # ServiceName() — DNS-label format
│   └── watchdog/               # Health monitoring + systemd sd_notify
│       └── watchdog.go         # Health check loop, Ready/Watchdog/Stopping
├── relay-server/               # Deployment artifacts (setup, configs, systemd)
│   ├── setup.sh                # Deploy/verify/uninstall (builds from cmd/relay-server)
│   ├── relay-server.service
│   └── README.md               # Full VPS deployment guide
├── configs/                    # Sample configuration files
│   ├── peerup.sample.yaml
│   ├── relay-server.sample.yaml
│   └── authorized_keys.sample
├── examples/                   # Example implementations
│   └── basic-service/
├── docs/                       # Project documentation
│   ├── ARCHITECTURE.md
│   ├── FAQ.md
│   ├── ROADMAP.md
│   └── TESTING.md
├── go.mod                      # Single module (all packages under one module)
```

## Building

```bash
# Build peerup (single binary for everything)
go build -o peerup ./cmd/peerup

# Build with version info (recommended for deployments)
go build -ldflags "-X main.version=0.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildDate=$(date -u +%Y-%m-%dT%H:%M:%SZ)" -o peerup ./cmd/peerup

# Build relay server (into relay-server/ deployment directory)
go build -o relay-server/relay-server ./cmd/relay-server

# Cross-compile for Linux (e.g., deploy to a Linux server)
GOOS=linux GOARCH=amd64 go build -o peerup ./cmd/peerup

# Check version
./peerup version

# Run all tests
go test -race -count=1 ./...
```

## Commands

### `peerup init` - Interactive setup wizard

```
Usage: peerup init [--dir <path>]

Creates config directory with config.yaml, identity.key, and authorized_keys.
Default directory: ~/.config/peerup/
```

### `peerup daemon` - Run as server

```
Usage: peerup daemon [--config <path>]

Starts the server node, exposing configured services.
Connects to relay, bootstraps DHT, and accepts authorized connections.
```

### `peerup proxy` - Forward TCP to remote service

```
Usage: peerup proxy [--config <path>] <target> <service> <local-port>

Arguments:
  target       Peer ID or name from config (e.g., "home")
  service      Service name as defined in server config (e.g., "ssh", "xrdp")
  local-port   Local TCP port to listen on

Examples:
  peerup proxy home ssh 2222
  peerup proxy home xrdp 13389
  peerup proxy 12D3KooW... ssh 2222
```

### `peerup ping` - Test connectivity

```
Usage: peerup ping [--config <path>] <target>

Arguments:
  target    Peer ID or name from config

Examples:
  peerup ping home
  peerup ping 12D3KooW...
```

### `peerup invite` - Generate invite code for pairing

```
Usage: peerup invite [--config <path>] [--name "home"] [--ttl 10m]

Generates a one-time invite code (+ QR code) and waits for a peer to join.
Both sides are mutually authorized on successful join.

Options:
  --name    Friendly name for this peer (shared with joiner)
  --ttl     Invite code expiry duration (default: 10m)
```

### `peerup join` - Accept invite and auto-configure

```
Usage: peerup join [--config <path>] <invite-code> [--name "laptop"]

Decodes the invite code, connects to the inviter through the relay,
exchanges peer IDs, and auto-configures both sides (authorized_keys + names).

If no config exists, creates one automatically at ~/.config/peerup/.

Options:
  --name    Friendly name for this peer (shared with inviter)
```

### `peerup whoami` - Show your peer ID

```
Usage: peerup whoami [--config <path>]

Displays your peer ID derived from the identity key file.
```

### `peerup auth` - Manage authorized peers

```
Usage:
  peerup auth add <peer-id> [--comment "label"]   Add a peer to authorized_keys
  peerup auth list                                 List authorized peers
  peerup auth remove <peer-id>                     Remove a peer
  peerup auth validate                             Validate authorized_keys file format
```

### `peerup relay` - Manage relay addresses

```
Usage:
  peerup relay add <address> [--peer-id <ID>]      Add a relay server
  peerup relay list                                 List configured relays
  peerup relay remove <multiaddr>                   Remove a relay

The <address> accepts flexible formats:
  /ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...   Full multiaddr
  1.2.3.4:7777                              IP:port (prompts for peer ID)
  1.2.3.4                                   Bare IP (default port 7777)
```

### `peerup config` - Config management & self-healing

```
Usage:
  peerup config validate [--config path]                                   Validate config
  peerup config show     [--config path]                                   Show resolved config
  peerup config rollback [--config path]                                   Restore last-known-good
  peerup config apply    <new-config> [--config path] [--confirm-timeout]  Apply with safety net
  peerup config confirm  [--config path]                                   Confirm applied config
```

**Config archive**: On each successful `peerup daemon` startup, the validated config is archived as last-known-good. If a bad edit prevents startup, `peerup config rollback` restores it.

**Commit-confirmed** (for safe remote config changes):
```bash
# On the remote machine:
peerup config apply new-config.yaml --confirm-timeout 5m
sudo systemctl restart peerup

# Test connectivity, then confirm:
peerup config confirm

# If you don't confirm within 5 minutes, the config auto-reverts
# and systemd restarts with the previous known-good config.
```

## Configuration

### Config Search Order

All commands support `--config <path>` to specify a config file explicitly. Without it, peerup searches:

1. `./peerup.yaml` (current directory)
2. `~/.config/peerup/config.yaml` (standard location, created by `peerup init`)
3. `/etc/peerup/config.yaml` (system-wide)

### Unified Config (`peerup.yaml`)

```yaml
identity:
  key_file: "identity.key"

network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
    - "/ip4/0.0.0.0/udp/0/quic-v1"
  force_private_reachability: false  # Set true for server behind CGNAT

relay:
  addresses:
    - "/ip4/YOUR_VPS_IP/tcp/7777/p2p/YOUR_RELAY_PEER_ID"
  reservation_interval: "2m"

discovery:
  rendezvous: "peerup-default-network"
  bootstrap_peers: []

security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true

protocols:
  ping_pong:
    enabled: true
    id: "/pingpong/1.0.0"

# Services to expose (server only, uncomment as needed):
# services:
#   ssh:
#     enabled: true
#     local_address: "localhost:22"
#   xrdp:
#     enabled: true
#     local_address: "localhost:3389"

# Map friendly names to peer IDs:
names: {}
#  home: "12D3KooW..."
```

### Authorized Keys Format

```bash
# File: authorized_keys
# Format: <peer_id> # optional comment
12D3KooWLCavCP1Pma9NGJQnGDQhgwSjgQgupWprZJH4w1P3HCVL  # my-laptop
12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYUXCUVwbj7QbA  # my-phone
```

## Library (`pkg/p2pnet`)

The `pkg/p2pnet` package can be imported into your own Go projects:

```go
import "github.com/satindergrewal/peer-up/pkg/p2pnet"

// Create a P2P network
net, _ := p2pnet.New(&p2pnet.Config{
    KeyFile:      "myapp.key",
    EnableRelay:  true,
    RelayAddrs:   []string{"/ip4/.../tcp/7777/p2p/..."},
})

// Expose a local service
net.ExposeService("api", "localhost:8080")

// Connect to a peer's service
conn, _ := net.ConnectToService(peerID, "api")

// Name resolution
net.LoadNames(map[string]string{"home": "12D3KooW..."})
peerID, _ := net.ResolveName("home")

// Add relay addresses for a remote peer
net.AddRelayAddressesForPeer(relayAddrs, peerID)

// Create a TCP listener that proxies to a remote service
listener, _ := p2pnet.NewTCPListener("localhost:8080", func() (p2pnet.ServiceConn, error) {
    return net.ConnectToService(peerID, "api")
})
listener.Serve()
```

## Authentication System

Two layers of defense:

1. **ConnectionGater** (network level) - Blocks unauthorized peers during connection handshake
2. **Protocol handler validation** (application level) - Double-checks authorization before processing requests

### How It Works

1. Server loads `authorized_keys` at startup
2. When a peer attempts to connect, `InterceptSecured()` checks the peer ID
3. If not authorized, connection is **DENIED** at network level
4. If authorized, connection is allowed and protocol handler performs secondary check

### Fail-Safe Defaults

- If `enable_connection_gating: true` but no `authorized_keys` file: **refuses to start**
- If `authorized_keys` is empty: **warns loudly** but allows (for initial setup)
- Server: **allows all outbound** connections (for DHT, relay, etc.)
- Server: **blocks all unauthorized inbound** connections

## Security Notes

### File Permissions

```bash
chmod 600 *.key              # Private keys: owner read/write only
chmod 600 authorized_keys    # SSH-style: owner read/write only
chmod 644 *.yaml             # Configs: readable by all
```

### Relay Server Security

The relay server supports `authorized_keys` to restrict who can make reservations. Enable authentication in production to protect your VPS bandwidth.

## Architecture

### Relay Circuit (Circuit Relay v2)

1. Server connects outbound to relay and makes a **reservation**
2. Client connects outbound to relay
3. Client dials server via circuit address:
   ```
   /ip4/<RELAY_IP>/tcp/7777/p2p/<RELAY_PEER_ID>/p2p-circuit/p2p/<SERVER_PEER_ID>
   ```
4. Relay bridges the connection - both sides only made outbound connections

### Hole-Punching (DCUtR)

After relay connection is established, libp2p attempts **Direct Connection Upgrade through Relay**:
- If successful: subsequent data flows directly (no relay bandwidth)
- If failed (symmetric NAT): continues using relay

### Peer Discovery (Private Kademlia DHT)

Peer-up runs its own Kademlia DHT on protocol `/peerup/kad/1.0.0`, completely isolated from the public IPFS Amino network. The relay server acts as the bootstrap peer.

Server **advertises** on DHT using rendezvous string.
Client **searches** DHT for the rendezvous string to find the server's peer ID and addresses.

### Bidirectional Proxy

The TCP proxy uses the half-close pattern (inspired by Go stdlib's `httputil.ReverseProxy`):
- When one direction finishes sending, it signals `CloseWrite` instead of closing the connection
- The other direction can continue sending until it also finishes
- This prevents premature connection closure and works correctly with protocols like SSH and XRDP

## Running as a Service (systemd)

### Relay Server

See [relay-server/README.md](relay-server/README.md) for the full VPS setup guide (user creation, SSH hardening, firewall, systemd).

Quick version if already configured:
```bash
cd relay-server
bash setup.sh        # Full setup (build, permissions, systemd, health check)
bash setup.sh --check  # Health check only
```

### peerup daemon

Create `/etc/systemd/system/peerup.service`:
```ini
[Unit]
Description=peer-up Server
After=network-online.target
Wants=network-online.target

[Service]
Type=notify
WatchdogSec=90
ExecStart=/usr/local/bin/peerup daemon --config /etc/peerup/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

Both `peerup daemon` and `relay-server` send `sd_notify` signals to systemd: `READY=1` after startup, `WATCHDOG=1` every 30s while healthy, `STOPPING=1` on shutdown. On non-systemd systems (macOS), these are no-ops.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `Config error: no config file found` | Run `peerup init` or use `--config <path>` |
| `Cannot resolve target` | Add name mapping to `names:` section in config |
| `DENIED inbound connection` | Add peer ID to `authorized_keys` and restart server |
| `Invalid invite code` | Ensure the full invite code is pasted as one argument (quote it if it contains spaces) |
| `Failed to connect to inviter` | Ensure `peerup invite` is still running on the other machine |
| Server shows no `/p2p-circuit` addresses | Check `force_private_reachability: true` and relay address |
| `protocols not supported` | Relay service not running |
| XRDP window manager crashes | Ensure no conflicting physical desktop session for the same user |
| `failed to sufficiently increase receive buffer size` | QUIC works but with smaller buffers. Fix: see UDP buffer tuning below |
| Bad config edit broke startup | `peerup config rollback` restores last-known-good config |
| Need to test config changes on remote node | Use `peerup config apply new.yaml --confirm-timeout 5m`, then `peerup config confirm` |
| `commit-confirmed timeout` in logs | Config auto-reverted because `peerup config confirm` wasn't run in time |

### UDP Buffer Tuning (QUIC)

The `failed to sufficiently increase receive buffer size` message means the kernel's UDP buffer cap is too low for optimal QUIC performance. QUIC still works, just with smaller buffers. The relay server's `setup.sh` handles this automatically, but home nodes need it applied manually.

**Temporary** (until reboot):
```bash
sudo sysctl -w net.core.rmem_max=7500000
sudo sysctl -w net.core.wmem_max=7500000
```

**Persistent** (survives reboot):
```bash
echo "net.core.rmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
echo "net.core.wmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
sudo sysctl --system
```

Restart `peerup daemon` after applying — the message will be gone.

## Bandwidth Considerations

- **Relay-based connection**: Limited by relay VPS bandwidth (~1TB/month on $5 Linode)
- **After DCUtR upgrade**: Direct P2P connection, no relay bandwidth used
- **Starlink symmetric NAT**: DCUtR often fails, relay remains in use

## Roadmap

See [ROADMAP.md](docs/ROADMAP.md) for detailed multi-phase implementation plan.

## Dependencies

- [go-libp2p](https://github.com/libp2p/go-libp2p) v0.47.0
- [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht) v0.28.1
- [go-multiaddr](https://github.com/multiformats/go-multiaddr)
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) v3.0.1

## License

MIT

## Contributing

This is a personal project, but issues and PRs are welcome!

**Testing checklist for PRs:**
- [ ] `go build ./...` succeeds
- [ ] `go vet ./...` passes
- [ ] `go test -race -count=1 ./...` passes
- [ ] Config files load without errors
- [ ] Unauthorized peer is denied
- [ ] Authorized peer connects successfully
- [ ] Service proxy works (SSH, XRDP, or other TCP)

---

**Built with libp2p** - Peer-to-peer networking that just works.
