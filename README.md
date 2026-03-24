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

### 0. Install Shurli

```bash
# Short URL
curl -sSL get.shurli.io | sh

# Or use the full GitHub URL directly
curl -sSL https://raw.githubusercontent.com/shurlinet/shurli/dev/tools/install.sh | sh
```

The install script detects your OS and architecture, downloads a pre-built binary, verifies checksums, and walks you through setup. It handles peer nodes, relay servers, upgrades, and uninstall.

For pre-release builds, set the environment variable before `sh`:
```bash
curl -sSL <URL> | SHURLI_DEV=1 sh
```

<details>
<summary>Build from source instead</summary>

Requires [Go 1.26+](https://go.dev/dl/) and mDNS dev library on Linux:
```bash
# Linux (Debian/Ubuntu)
sudo apt install libavahi-compat-libdnssd-dev

git clone https://github.com/shurlinet/shurli.git
cd shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli
sudo install -m 755 shurli /usr/local/bin/shurli
```
</details>

<details>
<summary>Install script options reference</summary>

### Options

| Flag | Description |
|------|-------------|
| `--dev` | Install latest dev/pre-release build (default: stable only) |
| `--version VERSION` | Install a specific version (e.g., `v0.2.2-dev`) |
| `--method METHOD` | Install method: `download` or `build` (default: interactive prompt) |
| `--role ROLE` | Setup role: `peer`, `relay`, or `binary` (default: interactive prompt) |
| `--dir DIR` | Install directory (default: `/usr/local/bin`) |
| `--no-verify` | Skip SHA256 checksum verification |
| `--upgrade MODE` | Existing install behavior: `upgrade` or `reinstall` (default: interactive) |
| `--yes`, `-y` | Accept all defaults non-interactively |
| `--backup` | Back up config without changing anything |
| `--uninstall` | Uninstall Shurli |
| `--help`, `-h` | Show help |

### Environment variables

When piping (`curl | sh`), use environment variables instead of flags:

| Variable | Equivalent |
|----------|------------|
| `SHURLI_DEV=1` | `--dev` |
| `SHURLI_VERSION=v0.2.2-dev` | `--version v0.2.2-dev` |
| `SHURLI_METHOD=download` | `--method download` |
| `SHURLI_ROLE=relay` | `--role relay` |
| `SHURLI_UPGRADE=upgrade` | `--upgrade upgrade` |
| `SHURLI_YES=1` | `--yes` |
| `SHURLI_UNINSTALL=1` | `--uninstall` |
| `SHURLI_BACKUP=1` | `--backup` |

### Examples

```bash
# Interactive install (prompts for method and role)
curl -sSL get.shurli.io | sh

# Latest dev build
curl -sSL get.shurli.io | SHURLI_DEV=1 sh

# Specific version
curl -sSL get.shurli.io | SHURLI_VERSION=v0.2.2-dev sh

# Non-interactive relay server deploy
curl -sSL get.shurli.io | SHURLI_DEV=1 SHURLI_METHOD=download SHURLI_ROLE=relay sh

# Non-interactive peer node install
curl -sSL get.shurli.io | SHURLI_DEV=1 SHURLI_METHOD=download SHURLI_ROLE=peer sh

# Non-interactive upgrade (replace binary, keep config, restart service)
curl -sSL get.shurli.io | SHURLI_YES=1 SHURLI_UPGRADE=upgrade SHURLI_METHOD=download sh

# Fully unattended binary install (AI agents and automation)
curl -sSL get.shurli.io | SHURLI_YES=1 SHURLI_DEV=1 SHURLI_METHOD=download SHURLI_ROLE=binary sh

# Non-interactive with flags (when running script directly)
sh install.sh --method download --role relay --yes

# Back up config only (no install)
curl -sSL get.shurli.io | SHURLI_BACKUP=1 sh

# Uninstall (interactive: choose keep config, backup, or full removal)
curl -sSL get.shurli.io | SHURLI_UNINSTALL=1 sh
```

### What the script does

1. **Detects platform** (OS and architecture: linux/darwin, amd64/arm64)
2. **Checks for existing install** (offers upgrade, reinstall, or cancel)
3. **Downloads or builds** (pre-built archive with checksum verification, or isolated build-from-source)
4. **Installs binary** to `/usr/local/bin` (or `~/.local/bin` without sudo)
5. **Runs role setup**:
   - **Peer node**: installs runtime deps, systemd/launchd service, runs `shurli init`
   - **Relay server**: runs `relay-setup.sh` in prebuilt mode (user creation, firewall, systemd, identity)
   - **Binary only**: installs binary, prints next steps
6. **Supports backup/restore**: detects previous backups in `~/.shurli/backups/`, offers to restore
7. **macOS**: codesigns binary for stable Local Network Privacy identity

### Uninstall options

1. **Keep config and keys** - removes binary and services, preserves identity (can reinstall later)
2. **Back up then remove** - backs up to `~/.shurli/backups/`, then removes everything
3. **Complete removal** - permanently deletes config, keys, and identity (requires typing "yes")

</details>

### 1. Deploy your relay

Follow the [docs/RELAY-SETUP.md](docs/RELAY-SETUP.md) to deploy your own relay on any VPS.
One script, takes a few minutes. Your relay, your rules. No third party controls your network.

### 2. Join your relay

On the relay, generate an invite:
```bash
shurli relay invite create --ttl 24h
# Output includes: invite code, relay IP:PORT, and Peer ID
```

On your device, join with one command (it will prompt for the Peer ID):
```bash
shurli join <invite-code> --relay <IP:PORT>
```

That's it. Identity created, config written, relay connected, daemon started.
On Linux it offers to install as a systemd service (starts on boot).
Repeat on each device you want to connect.

### 3. Use it

```bash
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

---

### Alternative: Init + Join (two-step)

Set up config first, connect later:

```bash
# Step 1: Create identity and config (choose your relay or public seeds)
shurli init

# Step 2: On your relay, create an invite
shurli relay invite create --ttl 24h

# Step 3: On one device, generate the invite
shurli invite --as home

# Step 4: On the other device, join
shurli join <invite-code> --as laptop

# Step 5: Start the daemon
shurli daemon
```

### Advanced: Manual setup (no wizards)

For users who prefer full control without interactive prompts:

```bash
# 1. Create identity from seed phrase
shurli recover --dir /etc/shurli

# 2. Write config manually
cat > /etc/shurli/config.yaml << 'EOF'
version: 1
identity:
  key_file: "identity.key"
network:
  listen_addresses:
    - "/ip4/0.0.0.0/tcp/0"
    - "/ip4/0.0.0.0/udp/0/quic-v1"
relay:
  addresses:
    - "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooW..."
  reservation_interval: "2m"
discovery:
  rendezvous: "shurli-default-network"
  bootstrap_peers: []
security:
  authorized_keys_file: "authorized_keys"
  enable_connection_gating: true
names: {}
EOF

# 3. Add authorized peers manually
echo "12D3KooW...  # relay" >> /etc/shurli/authorized_keys
echo "12D3KooW...  # home-node" >> /etc/shurli/authorized_keys

# 4. Start
shurli daemon
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
