---
title: "Quick Start"
weight: 2
description: "Get connected with Shurli. Deploy a relay, join with one command, proxy any TCP service through an encrypted P2P tunnel."
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
