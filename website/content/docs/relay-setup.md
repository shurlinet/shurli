---
title: "Relay Setup"
weight: 5
description: "Complete guide to deploying your own Shurli relay server on a VPS. Ubuntu setup, systemd service, firewall rules, and health checks."
---
<!-- Auto-synced from relay-server/README.md by sync-docs - do not edit directly -->


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
cd Shurli/relay-server
```

### Run the setup script

The script detects that you're root and walks you through creating a secure service user:

```bash
bash setup.sh
```

It will:
1. Ask you to **select an existing user** or **create a new one**
2. **New user**: creates the account, sets a password, copies your SSH keys, locks down home directory (700), and offers to harden SSH (disable password auth + root login)
3. **Existing user**: audits sudo group, password, SSH keys, directory permissions, and SSH daemon config, and offers to fix any issues
4. Continues with the full setup: builds binary, installs systemd service (running as the selected user), configures firewall, tunes QUIC buffers, and transfers file ownership

**If creating a new user**, test SSH access in a separate terminal before closing the root session:

```bash
ssh newuser@YOUR_VPS_IP
sudo whoami    # should print: root
```

### Configure (as the service user)

```bash
ssh shurli@YOUR_VPS_IP
cd Shurli/relay-server

# Create config from sample
cp ../configs/relay-server.sample.yaml relay-server.yaml

# Edit if needed (defaults are good - port 7777, gating enabled)
nano relay-server.yaml
```

Then restart the service to pick up config changes:

```bash
sudo systemctl restart shurli-relay
```

### Add peers to the relay

**Option A: Pairing codes (recommended)**

Generate pairing codes and share them with your peers:

```bash
# Generate 3 pairing codes (one per person)
./shurli relay pair --count 3

# Each person joins with one command on their machine:
shurli join <pairing-code> --name laptop
```

Pairing codes handle authorization automatically. Everyone who joins with a code from the same relay is mutually authorized and can verify each other with `shurli verify <name>`.

**Option B: Manual authorization**

If you already know the peer IDs, add them directly:

```bash
# Using the CLI
./shurli relay authorize <peer-id> --comment "home-node"
./shurli relay authorize <peer-id> --comment "client-node"

# Or edit the file directly (one peer ID per line)
nano relay_authorized_keys
```

```
12D3KooWARqzAAN9es44ACsL7W82tfbpiMVPfSi1M5czHHYPk5fY  # home-node
12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6  # client-node
```

Restart the service after manual changes:

```bash
sudo systemctl restart shurli-relay
```

---

## 2. Verify

Run the health check anytime:

```bash
cd ~/Shurli/relay-server
bash setup.sh --check
```

You should see all `[OK]` items:

```
Binary:
  [OK]   shurli binary exists
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
cd ~/Shurli/relay-server
bash setup.sh --uninstall
```

This removes:
- The systemd service (stopped, disabled, file deleted)
- Firewall rules for port 7777
- QUIC buffer tuning from `/etc/sysctl.conf`
- Journald log rotation settings

It does **not** delete your binary, config, keys, or source code. To fully clean up:

```bash
rm -rf ~/Shurli  # Only if you want to remove everything
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
cd ~/Shurli
git pull
go build -ldflags="-s -w" -trimpath -o relay-server/shurli ./cmd/shurli
sudo systemctl restart shurli-relay
```

---

## 5. Security Checklist

| Item | How to verify |
|------|--------------|
| Non-root user | `whoami` (should NOT be root) |
| SSH key-only login | `grep PasswordAuthentication /etc/ssh/sshd_config` → `no` |
| Root login disabled | `grep PermitRootLogin /etc/ssh/sshd_config` → `no` |
| Firewall active | `sudo ufw status` → active, default deny incoming |
| Only needed ports open | `sudo ufw status` → 22/tcp (SSH) + 7777/tcp+udp |
| Connection gating on | `grep enable_connection_gating relay-server.yaml` → `true` |
| Key file permissions | `ls -la relay_node.key` → `-rw-------` (600) |
| Log rotation | `grep SystemMaxUse /etc/systemd/journald.conf` → `500M` |
| System updates | `sudo apt update && sudo apt upgrade` |

Or just run: `bash setup.sh --check`

---

## File Layout

After setup, your relay-server directory looks like:

```
~/Shurli/relay-server/
├── shurli                    # Binary (built from cmd/shurli, gitignored)
├── relay-server.yaml         # Config (gitignored)
├── relay-server.service      # Template service file (in git)
├── relay_node.key            # Identity key (auto-generated, gitignored)
├── relay_authorized_keys     # Allowed peer IDs (gitignored)
└── setup.sh                  # Setup + health check script
```

---

## Manual Installation (Any Linux/Unix)

The `setup.sh` script targets Debian/Ubuntu with `apt` and `ufw`. If you run a different distribution, or prefer to set things up yourself, here's everything the script does broken into individual steps.

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

**QR code tool** (optional, for `setup.sh --check` display):

| Distribution | Package |
|-------------|---------|
| Debian/Ubuntu | `qrencode` |
| Fedora/RHEL | `qrencode` |
| Arch | `qrencode` |
| Alpine | `libqrencode` |
| macOS | `brew install qrencode` |

### Build

```bash
git clone https://github.com/shurlinet/shurli.git
cd Shurli

# Full build with optimizations
go build -ldflags="-s -w" -trimpath -o relay-server/shurli ./cmd/shurli

# Verify
./relay-server/shurli version
```

### Configure

```bash
cd relay-server
cp ../configs/relay-server.sample.yaml relay-server.yaml
# Edit relay-server.yaml if needed (defaults: port 7777, gating enabled)
```

### Create service user (if running as a service)

```bash
sudo useradd -r -m -s /bin/bash shurli
sudo chown -R shurli:shurli /path/to/Shurli/relay-server
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

### Install systemd service

```bash
# Copy and customize the service template
sudo cp relay-server.service /etc/systemd/system/shurli-relay.service

# Edit to match your paths and user
sudo nano /etc/systemd/system/shurli-relay.service
# Key fields: User=shurli, WorkingDirectory=/path/to/relay-server, ExecStart=/path/to/shurli relay serve

sudo systemctl daemon-reload
sudo systemctl enable --now shurli-relay
```

For **non-systemd** systems (Alpine with OpenRC, FreeBSD with rc.d, etc.), create a service script that runs:

```bash
/path/to/shurli relay serve --config /path/to/relay-server.yaml
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
bash setup.sh --check
```

---

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Service fails to start | `sudo journalctl -u shurli-relay -n 30` for error logs |
| "Permission denied" on key file | `chmod 600 relay_node.key` |
| Peers can't connect | Use `shurli relay pair` to generate codes, or check `relay_authorized_keys` has their peer IDs |
| Random peers connecting | Verify `enable_connection_gating: true` in config |
| High log disk usage | `sudo journalctl --vacuum-size=200M` to trim now |
| Port not reachable | `sudo ufw status` and check VPS provider firewall/security group |
| Service runs as root | `bash setup.sh --uninstall` then re-run `bash setup.sh` (as root it will guide you through user setup) |
| `dns_sd.h: No such file or directory` | Install the avahi compat library for your distro (see Prerequisites above) |
| CGo build fails / not wanted | Build with `CGO_ENABLED=0 go build ...` to use pure-Go fallback (no native mDNS) |

---

**Next step**: [Securing Your Relay](../relay-security/) - initialize the vault, set up 2FA, and configure auto-seal so your relay starts locked after every restart.
