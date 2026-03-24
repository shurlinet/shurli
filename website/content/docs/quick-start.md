---
title: "Quick Start"
weight: 2
description: "Get two devices connected with Shurli in 60 seconds. Build from source, init, invite, join, and proxy any TCP service through an encrypted P2P tunnel."
---
<!-- Auto-synced from README.md by sync-docs - do not edit directly -->


## 0. Install Shurli

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

## Options

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

## Environment variables

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

## Examples

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

## What the script does

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

## Uninstall options

1. **Keep config and keys** - removes binary and services, preserves identity (can reinstall later)
2. **Back up then remove** - backs up to `~/.shurli/backups/`, then removes everything
3. **Complete removal** - permanently deletes config, keys, and identity (requires typing "yes")

</details>

## 1. Deploy your relay

Follow the [Relay Setup guide](../relay-setup/) to deploy your own relay on any VPS.
One script, takes a few minutes. Your relay, your rules. No third party controls your network.

## 2. Join your relay

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

## 3. Use it

```bash
shurli proxy home ssh 2222
ssh -p 2222 user@localhost
```

---

## Alternative: Init + Join (two-step)

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

## Advanced: Manual setup (no wizards)

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

## Disclaimer

Shurli is experimental software under active development. It is provided "as is" with no warranty of any kind (see [LICENSE](LICENSE)). If you discover a bug, please [open an issue](https://github.com/shurlinet/shurli/issues).
