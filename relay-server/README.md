# Shurli Relay Server Setup

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
