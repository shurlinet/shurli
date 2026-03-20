# Shurli

Access your home server from anywhere. Share services with friends. No cloud, no account, no SaaS dependency.

**Shurli** connects your devices through firewalls and CGNAT using encrypted P2P tunnels with SSH-style authentication. One binary, zero configuration servers, works behind any NAT.

## Install

### Pre-built binary

Download the latest release for your platform from [GitHub Releases](https://github.com/shurlinet/shurli/releases/latest).

### Build from source

**Requirements:** [Go 1.26+](https://go.dev/dl/)

**Linux** - install the mDNS development library first:
```bash
# Debian / Ubuntu
sudo apt install libavahi-compat-libdnssd-dev

# Fedora / RHEL
sudo dnf install avahi-compat-libdns_sd-devel

# Arch
sudo pacman -S avahi
```

**macOS** - no additional dependencies (uses built-in mDNSResponder).

Then build:
```bash
git clone https://github.com/shurlinet/shurli.git
cd shurli
make build

# Or directly with Go:
# go build -o shurli ./cmd/shurli
```

Move the binary to your PATH:
```bash
sudo install -m 755 shurli /usr/local/bin/shurli
```

Or use `make install` to build, install, and set up a system service in one step.

## Quick Start

### 1. Initialize (first time only)

```bash
shurli init
```

Interactive wizard: creates your identity (seed phrase backup), sets a password, and writes your configuration.

### 2. Connect

**I have an invite code:**

```bash
shurli join <invite-code> --as laptop
```

Done. You're connected and mutually authorized.

**I want to set up my own network:**

On machine 1 (your server):
```bash
shurli invite --as home
# Shows invite code + QR code, waits for the other side...
```

On machine 2 (your client):
```bash
shurli join <invite-code> --as laptop
```

### 3. Use it

```bash
# On the server - start the daemon
shurli daemon

# On the client - connect to a service
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

## What Can I Do With Shurli?

| Use Case | Command |
|----------|---------|
| SSH to your home machine behind CGNAT | `shurli proxy home ssh 2222` then `ssh -p 2222 localhost` |
| Remote desktop through NAT | `shurli proxy home xrdp 13389` then connect to `localhost:13389` |
| Share Jellyfin with a friend | `shurli invite` on your side, `shurli join <code>` on theirs |
| AI inference on a friend's GPU | `shurli proxy friend ollama 11434` then `curl localhost:11434` |
| Any TCP service, zero port forwarding | `shurli proxy <peer> <service> <local-port>` |
| Send a file to a friend | `shurli send photo.jpg home` |
| Share a folder with a peer | `shurli share add ~/Public --to laptop` |
| Download from a peer's shares | `shurli download home:documents/report.pdf` |
| Check connectivity | `shurli ping home` or `shurli traceroute home` |

Shurli works with **two machines and zero network effect** - useful from day one.

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

1. **Server** runs `shurli daemon` behind CGNAT, connects outbound to a relay
2. **Client** runs `shurli proxy`, connects outbound to the same relay and reaches the server
3. **DCUtR** attempts hole-punching. If successful, traffic flows directly without the relay

For the full architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md)

## Why Shurli Exists

Shurli was created to solve one problem: reaching a service on a home server from outside the network without depending on anyone else's infrastructure.

Existing solutions require either a cloud account, a third-party VPN, or port forwarding - which CGNAT frequently makes impossible. They all share the same flaw: your connectivity depends on someone else's servers and their permission to keep it running.

Shurli uses a different model. Devices connect outbound to a lightweight relay for initial setup, then upgrade to direct peer-to-peer when possible. No accounts, no central identity server, no revocable subscriptions. Your keys stay on your machine, configuration lives in one YAML file, and you can run your own relay for zero external dependency.

## Features

| Feature | Description |
|---------|-------------|
| **NAT Traversal** | Circuit relay v2 + DCUtR hole-punching. Works behind CGNAT, symmetric NAT, double-NAT |
| **SSH-Style Auth** | `authorized_keys` peer allowlist - only explicitly trusted peers can connect |
| **60-Second Pairing** | `shurli invite` + `shurli join` - exchanges keys, adds auth, maps names |
| **TCP Service Proxy** | Forward any TCP port through P2P tunnels (SSH, XRDP, HTTP, databases, AI inference) |
| **Daemon Mode** | Background service with Unix socket API, cookie auth, hot-reload |
| **Private DHT** | Kademlia peer discovery on `/shurli/kad/1.0.0` - isolated from public networks |
| **Friendly Names** | `home`, `laptop`, `gpu-server` instead of raw peer IDs |
| **File Transfer** | Chunked P2P file transfer with FastCDC, BLAKE3 Merkle integrity, zstd compression, Reed-Solomon erasure coding, RaptorQ multi-source, parallel streams, resume support |
| **Selective Sharing** | Share files and directories with per-peer access control. AirDrop-style receive permissions (off/contacts/ask/open) |
| **Single Binary** | One `shurli` binary with 33 subcommands. No runtime dependencies |
| **Cross-Platform** | Linux, macOS, Windows, ARM |
| **systemd + launchd** | Service files included for both Linux and macOS |
| **Reusable Library** | `pkg/p2pnet` - import into your own Go projects |

## Security

**Two layers of defense:**

1. **ConnectionGater** (network level) - Blocks unauthorized peers during the connection handshake, before any data is exchanged
2. **Protocol handler** (application level) - Secondary authorization check before processing requests

**Fail-safe defaults:**
- Connection gating enabled + no authorized_keys file -> refuses to start
- Empty authorized_keys -> warns loudly (allows for initial setup)
- All unauthorized inbound connections blocked

## Running as a Service

**Linux (systemd):**
```bash
sudo cp deploy/shurli-daemon.service /etc/systemd/system/shurli.service
sudo systemctl daemon-reload
sudo systemctl enable --now shurli
```

**macOS (launchd):** Copy [deploy/com.shurli.daemon.plist](deploy/com.shurli.daemon.plist) to `~/Library/LaunchAgents/`.

**Relay server:** See [docs/RELAY-SETUP.md](docs/RELAY-SETUP.md) for the full VPS deployment guide. Quick install: `make install-relay`

## Documentation

| Document | Description |
|----------|-------------|
| [ARCHITECTURE.md](docs/ARCHITECTURE.md) | Full architecture: relay circuit, DHT, proxy, auth system |
| [COMMANDS.md](docs/COMMANDS.md) | Complete command reference (all 33 subcommands) |
| [DAEMON-API.md](docs/DAEMON-API.md) | Daemon REST API reference (38 endpoints) |
| [RELAY-SETUP.md](docs/RELAY-SETUP.md) | Relay server VPS deployment guide |
| [NETWORK-TOOLS.md](docs/NETWORK-TOOLS.md) | Ping, traceroute, resolve usage guide |
| [FAQ](docs/faq/) | Security FAQ, relay hardening, troubleshooting |
| [ROADMAP.md](docs/ROADMAP.md) | Multi-phase implementation plan |
| [TESTING.md](docs/TESTING.md) | Test strategy and coverage |
| [ENGINEERING-JOURNAL.md](docs/ENGINEERING-JOURNAL.md) | Architecture decision records |

## Troubleshooting

Run the built-in diagnostic:
```bash
shurli doctor        # Check installation health
shurli doctor --fix  # Auto-fix common issues
```

Common issues and solutions are in [docs/TROUBLESHOOTING.md](docs/TROUBLESHOOTING.md).

## Engineering Philosophy

This is not a weekend hobby project. Shurli is built as critical infrastructure, the kind where failure has real consequences for real people.

## Disclaimer

Shurli is experimental software under active development. It is provided "as is" with no warranty of any kind (see [LICENSE](LICENSE)). If you discover a bug, please [open an issue](https://github.com/shurlinet/shurli/issues).

## Development

Shurli is developed with significant AI assistance (Claude). All AI-generated code is reviewed, tested, and committed by a human maintainer.

**No Cryptocurrency / No Token.** Shurli is a networking tool. It has no token, no coin, no blockchain dependency.

**Contributing:** Issues and PRs are welcome. Run `go build ./...`, `go vet ./...`, and `go test -race -count=1 ./...` before submitting.

## Dependencies

**Networking:**
- [go-libp2p](https://github.com/libp2p/go-libp2p) v0.47.0 - P2P transport, relay, hole-punching
- [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht) v0.28.1 - Distributed peer discovery
- [go-multiaddr](https://github.com/multiformats/go-multiaddr) - Network address format

**File Transfer:**
- [zeebo/blake3](https://github.com/zeebo/blake3) v0.2.4 (CC0) - Per-chunk hash + Merkle tree integrity
- [klauspost/compress](https://github.com/klauspost/compress) v1.18.4 (BSD-3) - zstd streaming compression
- [klauspost/reedsolomon](https://github.com/klauspost/reedsolomon) v1.13.2 (MIT) - Erasure coding for loss recovery
- [xssnick/raptorq](https://github.com/xssnick/raptorq) v1.3.0 (MIT) - Fountain codes for multi-source download

**ZKP:**
- [gnark](https://github.com/Consensys/gnark) v0.14.0 - PLONK zero-knowledge proofs

**Other:**
- [gopkg.in/yaml.v3](https://gopkg.in/yaml.v3) v3.0.1 - Configuration

## License

MIT
