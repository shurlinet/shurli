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

## Current Status

- **Single Binary** - One `peerup` binary with subcommands: `init`, `serve`, `proxy`, `ping`
- **Easy Setup** - `peerup init` interactive wizard generates config, keys, and authorized_keys
- **Standard Config** - Auto-discovers config from `./peerup.yaml` or `~/.config/peerup/config.yaml`
- **Configuration-Based** - YAML config files, no hardcoded values
- **SSH-Style Authentication** - `authorized_keys` file for peer access control
- **NAT Traversal** - Works through Starlink CGNAT using relay + hole-punching
- **Persistent Identity** - Ed25519 keypairs saved to files
- **DHT Discovery** - Find peers using rendezvous on Kademlia DHT
- **Direct Connection Upgrade** - DCUtR attempts hole-punching for direct P2P
- **Key Management Tool** - `keytool` CLI for managing keypairs and authorized_keys
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
2. **Server** (`peerup serve`) - Exposes local services, accepts only authorized peers
3. **Client** (`peerup proxy`) - Connects to server's services through the relay

## Quick Start

### 1. Deploy Relay Server (VPS)

```bash
cd relay-server

# Create config from sample
cp ../configs/relay-server.sample.yaml relay-server.yaml
# Edit relay-server.yaml if needed (defaults are fine)

# Build and run
go build -o relay-server
./relay-server
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

### 3. Authorize Peers

On each machine, add the other machine's Peer ID to the authorized_keys file:

```bash
# Edit ~/.config/peerup/authorized_keys
# Add one peer ID per line:
12D3KooWARqzAAN9es44ACsL7W82tfbpiMVPfSi1M5czHHYPk5fY  # my-server
12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6  # my-laptop
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

# Map friendly names to peer IDs (optional):
names:
  laptop: "12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6"
```

### 5. Configure the Client

Edit `~/.config/peerup/config.yaml` on the client machine:

```yaml
# Map friendly names to peer IDs:
names:
  home: "12D3KooWARqzAAN9es44ACsL7W82tfbpiMVPfSi1M5czHHYPk5fY"
```

### 6. Run

**On the server:**
```bash
peerup serve
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
│   ├── peerup/                 # Main binary (init, serve, proxy, ping)
│   │   ├── main.go
│   │   ├── cmd_init.go
│   │   ├── cmd_serve.go
│   │   ├── cmd_proxy.go
│   │   └── cmd_ping.go
│   └── keytool/                # Key management CLI
│       ├── main.go
│       └── commands/
├── pkg/p2pnet/                 # Reusable P2P networking library
│   ├── network.go              # Core network setup, relay helpers, name resolution
│   ├── service.go              # Service registry and management
│   ├── proxy.go                # Bidirectional TCP↔Stream proxy with half-close
│   ├── naming.go               # Local name resolution (name → peer ID)
│   └── identity.go             # Ed25519 identity management
├── internal/
│   ├── config/                 # YAML configuration loading
│   │   ├── config.go
│   │   └── loader.go
│   └── auth/                   # Authentication system
│       ├── authorized_keys.go
│       └── gater.go
├── relay-server/               # VPS relay node (separate module)
│   ├── main.go
│   └── relay-server.service
├── configs/                    # Sample configuration files
│   ├── peerup.sample.yaml
│   ├── relay-server.sample.yaml
│   └── authorized_keys.sample
├── go.mod                      # Single root module
└── ROADMAP.md
```

## Building

```bash
# Build peerup (single binary for everything)
go build -o peerup ./cmd/peerup

# Build keytool (key management utility)
go build -o keytool ./cmd/keytool

# Build relay server (separate module, runs on VPS)
cd relay-server && go build -o relay-server

# Cross-compile for Linux (e.g., deploy to a Linux server)
GOOS=linux GOARCH=amd64 go build -o peerup ./cmd/peerup
```

## Commands

### `peerup init` - Interactive setup wizard

```
Usage: peerup init [--dir <path>]

Creates config directory with config.yaml, identity.key, and authorized_keys.
Default directory: ~/.config/peerup/
```

### `peerup serve` - Run as server

```
Usage: peerup serve [--config <path>]

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

### Peer Discovery (Kademlia DHT)

Server **advertises** on DHT using rendezvous string.
Client **searches** DHT for the rendezvous string to find the server's peer ID and addresses.

### Bidirectional Proxy

The TCP proxy uses the half-close pattern (inspired by Go stdlib's `httputil.ReverseProxy`):
- When one direction finishes sending, it signals `CloseWrite` instead of closing the connection
- The other direction can continue sending until it also finishes
- This prevents premature connection closure and works correctly with protocols like SSH and XRDP

## keytool - Key Management Utility

```bash
# Build
go build -o keytool ./cmd/keytool

# Generate new Ed25519 keypair
keytool generate my-node.key

# Extract peer ID from key file
keytool peerid identity.key

# Validate authorized_keys file
keytool validate authorized_keys

# Add peer to authorized_keys
keytool authorize 12D3KooW... --comment "laptop" --file authorized_keys

# Remove peer from authorized_keys
keytool revoke 12D3KooW... --file authorized_keys
```

## Running as a Service (systemd)

### Relay Server

```bash
sudo cp relay-server/relay-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable relay-server
sudo systemctl start relay-server
```

### peerup serve

Create `/etc/systemd/system/peerup.service`:
```ini
[Unit]
Description=peer-up Server
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/peerup serve --config /etc/peerup/config.yaml
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
```

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `Config error: no config file found` | Run `peerup init` or use `--config <path>` |
| `Cannot resolve target` | Add name mapping to `names:` section in config |
| `DENIED inbound connection` | Add peer ID to `authorized_keys` and restart server |
| Server shows no `/p2p-circuit` addresses | Check `force_private_reachability: true` and relay address |
| `protocols not supported` | Relay service not running |
| XRDP window manager crashes | Ensure no conflicting physical desktop session for the same user |
| `failed to sufficiently increase receive buffer size` | Warning only: `sudo sysctl -w net.core.rmem_max=7500000` |

## Bandwidth Considerations

- **Relay-based connection**: Limited by relay VPS bandwidth (~1TB/month on $5 Linode)
- **After DCUtR upgrade**: Direct P2P connection, no relay bandwidth used
- **Starlink symmetric NAT**: DCUtR often fails, relay remains in use

## Roadmap

See [ROADMAP.md](ROADMAP.md) for detailed multi-phase implementation plan.

## Dependencies

- [go-libp2p](https://github.com/libp2p/go-libp2p) v0.47.0
- [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht) v0.28.1
- [go-multiaddr](https://github.com/multiformats/go-multiaddr)
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) v3.0.1
- [urfave/cli](https://github.com/urfave/cli) v1.22.17 (keytool)
- [fatih/color](https://github.com/fatih/color) v1.18.0 (keytool)

## License

MIT

## Contributing

This is a personal project, but issues and PRs are welcome!

**Testing checklist for PRs:**
- [ ] `go build ./cmd/peerup` succeeds
- [ ] `go build ./cmd/keytool` succeeds
- [ ] Config files load without errors
- [ ] Unauthorized peer is denied
- [ ] Authorized peer connects successfully
- [ ] Service proxy works (SSH, XRDP, or other TCP)

---

**Built with libp2p** - Peer-to-peer networking that just works.
