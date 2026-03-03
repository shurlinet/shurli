---
title: "Relay Setup"
weight: 5
description: "Complete guide to deploying your own Shurli relay server on a VPS. Ubuntu setup, systemd service, firewall rules, and health checks."
---
<!-- Auto-synced from RELAY-SETUP.md by sync-docs - do not edit directly -->


Complete guide to deploying the relay server on a fresh VPS (Ubuntu 22.04 / 24.04).

## 1. Initial VPS Setup

SSH into your fresh VPS and install git:

```bash
ssh root@YOUR_VPS_IP
apt update && apt upgrade -y
apt install -y git ufw

# Enable firewall with SSH access
ufw allow OpenSSH
ufw default deny incoming
ufw default allow outgoing
ufw enable
```

### Clone the repo

```bash
git clone https://github.com/shurlinet/shurli.git
cd shurli
```

### Run the setup script

The script detects that you're root and walks you through creating a secure service user:

```bash
bash tools/relay-setup.sh
```

It will:
1. Ask you to **select an existing user** or **create a new one**
2. **New user**: creates the account, sets a password, copies your SSH keys, locks down home directory (700), and offers to harden SSH (disable password auth + root login)
3. **Existing user**: audits sudo group, password, SSH keys, directory permissions, and SSH daemon config, and offers to fix any issues
4. Continues with the full setup: builds binary, installs to `/usr/local/bin/shurli`, creates data directory at `/etc/shurli/relay/`, installs systemd service, configures firewall, tunes QUIC buffers

**If creating a new user**, test SSH access in a separate terminal before closing the root session:

```bash
ssh newuser@YOUR_VPS_IP
sudo whoami    # should print: root
```

### Configure (as the service user)

```bash
ssh shurli@YOUR_VPS_IP

# Edit if needed (defaults are good - port 7777, gating enabled)
sudo nano /etc/shurli/relay/relay-server.yaml
```

Then restart the service to pick up config changes (config-level changes like ports, transport, and ZKP enablement require a restart):

```bash
sudo systemctl restart shurli-relay
```

> **Note**: Auth changes (`shurli relay authorize` / `shurli relay deauthorize`) apply immediately via live reload - no restart needed.

### Add peers to the relay

**Option A: Pairing codes (recommended)**

Generate pairing codes and share them with your peers:

```bash
# Generate 3 pairing codes (one per person)
cd /etc/shurli/relay
shurli relay pair --count 3

# Each person joins with one command on their machine:
shurli join <pairing-code> --name laptop
```

Pairing codes handle authorization automatically. Everyone who joins with a code from the same relay is mutually authorized and can verify each other with `shurli verify <name>`.

**Option B: Manual authorization**

If you already know the peer IDs, add them directly:

```bash
# Using the CLI (run from data directory)
cd /etc/shurli/relay
shurli relay authorize <peer-id> --comment "home-node"
shurli relay authorize <peer-id> --comment "client-node"

# Or edit the file directly (one peer ID per line)
sudo nano /etc/shurli/relay/relay_authorized_keys
```

```
12D3KooWARqzAAN9es44ACsL7W82tfbpiMVPfSi1M5czHHYPk5fY  # home-node
12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6  # client-node
```

After manual edits, trigger a live reload:

```bash
shurli relay authorize <any-peer-id>   # triggers reload of the full file
# Or restart if you prefer:
sudo systemctl restart shurli-relay
```

---

## 2. Verify

Run the health check anytime:

```bash
cd ~/shurli
bash tools/relay-setup.sh --check
```

You should see all `[OK]` items:

```
Binary:
  [OK]   shurli binary exists at /usr/local/bin/shurli
  [OK]   shurli is executable

Configuration:
  [OK]   relay-server.yaml exists
  [OK]   Connection gating is ENABLED
  ...

Service:
  [OK]   shurli-relay service is enabled (starts on boot)
  [OK]   shurli-relay service is running
  [OK]   Service runs as non-root user: shurli
  ...

=== Summary: 25 passed, 0 warnings, 0 failures ===
Everything looks great!
```

---

## 3. Uninstall

To remove the systemd service, firewall rules, and system tuning:

```bash
cd ~/shurli
bash tools/relay-setup.sh --uninstall
```

This removes:
- The systemd service (stopped, disabled, file deleted)
- Firewall rules for port 7777
- QUIC buffer tuning from `/etc/sysctl.conf`
- Journald log rotation settings

It does **not** delete config, keys, or source code. To fully clean up:

```bash
sudo rm -rf /etc/shurli/relay  # Remove relay data
sudo rm /usr/local/bin/shurli  # Remove binary
rm -rf ~/shurli                # Remove source (only if you want to)
```

---

## 4. Useful Commands

```bash
# Service management
sudo systemctl status shurli-relay
sudo systemctl restart shurli-relay
sudo systemctl stop shurli-relay

# Follow logs
sudo journalctl -u shurli-relay -f

# Recent logs (last 50 lines)
sudo journalctl -u shurli-relay -n 50

# Check log disk usage
sudo journalctl --disk-usage

# Update relay server (after code changes)
cd ~/shurli
git pull
make install-relay
sudo systemctl restart shurli-relay
```

---

## 5. Security Checklist

| Item | How to verify |
|------|--------------|
| Non-root user | `whoami` (should NOT be root) |
| SSH key-only login | `grep PasswordAuthentication /etc/ssh/sshd_config` - `no` |
| Root login disabled | `grep PermitRootLogin /etc/ssh/sshd_config` - `no` |
| Firewall active | `sudo ufw status` - active, default deny incoming |
| Only needed ports open | `sudo ufw status` - 22/tcp (SSH) + 7777/tcp+udp |
| Connection gating on | `grep enable_connection_gating /etc/shurli/relay/relay-server.yaml` - `true` |
| Key file permissions | `ls -la /etc/shurli/relay/relay_node.key` - `-rw-------` (600) |
| Log rotation | `grep SystemMaxUse /etc/systemd/journald.conf` - `500M` |
| System updates | `sudo apt update && sudo apt upgrade` |

Or just run: `bash tools/relay-setup.sh --check`

---

## File Layout

After setup, the relay data and binary are installed to system paths:

```
/usr/local/bin/shurli              # Binary (single binary for all modes)

/etc/shurli/relay/
  relay-server.yaml                # Config (created by shurli relay setup)
  relay_node.key                   # Identity key (auto-generated on first run)
  relay_authorized_keys            # Allowed peer IDs
  backups/                         # Config snapshots (auto-created)
```

Source code (where you cloned the repo):

```
~/shurli/
  tools/relay-setup.sh             # Setup + health check script
  deploy/shurli-relay.service      # systemd service file
  configs/relay-server.sample.yaml # Sample configuration
```

---

## Manual Installation (Any Linux/Unix)

The `tools/relay-setup.sh` script targets Debian/Ubuntu with `apt` and `ufw`. If you run a different distribution, or prefer to set things up yourself, here's everything the script does broken into individual steps.

### Prerequisites

**Go** (version from `go.mod`, currently 1.26.0+):
```bash
# Download from https://go.dev/dl/
wget https://go.dev/dl/go1.26.0.linux-amd64.tar.gz
sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf go1.26.0.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin
```

**Native mDNS library** (optional, enables LAN peer discovery):

| Distribution | Package | Install |
|-------------|---------|---------|
| Debian/Ubuntu | `libavahi-compat-libdnssd-dev` | `sudo apt install libavahi-compat-libdnssd-dev` |
| Fedora/RHEL/CentOS | `avahi-compat-libdns_sd-devel` | `sudo dnf install avahi-compat-libdns_sd-devel` |
| Arch Linux | `avahi` | `sudo pacman -S avahi` |
| Alpine Linux | `avahi-compat-libdns_sd` | `sudo apk add avahi-compat-libdns_sd avahi-dev` |
| openSUSE | `avahi-compat-mDNSResponder-devel` | `sudo zypper install avahi-compat-mDNSResponder-devel` |
| FreeBSD | `avahi-libdns` | `sudo pkg install avahi-libdns` |
| macOS | Built-in | No install needed (dns_sd is in libSystem) |

Without this library, Shurli falls back to pure-Go mDNS (works but less reliable on systems with a running mDNS daemon). Build with `CGO_ENABLED=0` to explicitly use the fallback.

**QR code tool** (optional, for `relay-setup.sh --check` display):

| Distribution | Package |
|-------------|---------|
| Debian/Ubuntu | `qrencode` |
| Fedora/RHEL | `qrencode` |
| Arch | `qrencode` |
| Alpine | `libqrencode` |
| macOS | `brew install qrencode` |

### Build and install

```bash
git clone https://github.com/shurlinet/shurli.git
cd shurli

# Build and install to system paths
make install-relay

# This does:
#   1. Builds the binary with version embedding
#   2. Installs to /usr/local/bin/shurli
#   3. Creates /etc/shurli/relay/ with config files
#   4. Installs and enables the systemd service
```

### Configure

```bash
# Edit if needed (defaults: port 7777, gating enabled)
sudo nano /etc/shurli/relay/relay-server.yaml
```

### Create service user (if not using the setup script)

```bash
sudo useradd -r -m -s /bin/bash shurli
sudo chown -R shurli:shurli /etc/shurli/relay
```

### Network tuning (QUIC performance)

```bash
# Increase UDP buffer sizes for QUIC
echo 'net.core.rmem_max=7500000' | sudo tee -a /etc/sysctl.conf
echo 'net.core.wmem_max=7500000' | sudo tee -a /etc/sysctl.conf
sudo sysctl -p
```

### Firewall

Open port 7777 for TCP and UDP:

| Firewall | Commands |
|----------|----------|
| ufw (Ubuntu) | `sudo ufw allow 7777/tcp && sudo ufw allow 7777/udp` |
| firewalld (Fedora/RHEL) | `sudo firewall-cmd --permanent --add-port=7777/tcp && sudo firewall-cmd --permanent --add-port=7777/udp && sudo firewall-cmd --reload` |
| iptables | `sudo iptables -A INPUT -p tcp --dport 7777 -j ACCEPT && sudo iptables -A INPUT -p udp --dport 7777 -j ACCEPT` |
| nftables | Add `tcp dport 7777 accept` and `udp dport 7777 accept` to your input chain |
| pf (FreeBSD/macOS) | Add `pass in proto { tcp, udp } to port 7777` to `/etc/pf.conf` |

Also check your VPS provider's security groups/firewall rules in their web console.

### Install systemd service (manual)

```bash
# The service file uses fixed paths - no editing needed
sudo cp deploy/shurli-relay.service /etc/systemd/system/shurli-relay.service
sudo systemctl daemon-reload
sudo systemctl enable --now shurli-relay
```

For **non-systemd** systems (Alpine with OpenRC, FreeBSD with rc.d, etc.), create a service script that runs:

```bash
/usr/local/bin/shurli relay serve --config /etc/shurli/relay/relay-server.yaml
```

The binary is self-contained. It reads config from the working directory or the path specified by `--config`.

### Log rotation

For systemd systems, limit journal size:

```bash
sudo mkdir -p /etc/systemd/journald.conf.d
echo -e "[Journal]\nSystemMaxUse=500M\nMaxFileSec=7day" | sudo tee /etc/systemd/journald.conf.d/shurli-relay.conf
sudo systemctl restart systemd-journald
```

### Verify

```bash
# Check service is running
sudo systemctl status shurli-relay

# Check it's listening
ss -tlnp | grep 7777

# Check logs
sudo journalctl -u shurli-relay -n 20

# Or run the health check script (even on non-Ubuntu, it reports useful info)
bash tools/relay-setup.sh --check
```

---

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Service fails to start | `sudo journalctl -u shurli-relay -n 30` for error logs |
| "Permission denied" on key file | `chmod 600 /etc/shurli/relay/relay_node.key` |
| Peers can't connect | Use `shurli relay pair` to generate codes, or check `relay_authorized_keys` has their peer IDs |
| Random peers connecting | Verify `enable_connection_gating: true` in config |
| High log disk usage | `sudo journalctl --vacuum-size=200M` to trim now |
| Port not reachable | `sudo ufw status` and check VPS provider firewall/security group |
| Service runs as root | `bash tools/relay-setup.sh --uninstall` then re-run `bash tools/relay-setup.sh` (as root it will guide you through user setup) |
| `dns_sd.h: No such file or directory` | Install the avahi compat library for your distro (see Prerequisites above) |
| CGo build fails / not wanted | Build with `CGO_ENABLED=0 go build ...` to use pure-Go fallback (no native mDNS) |
