# peer-up Relay Server Setup

Complete guide to deploying the relay server on a fresh VPS (Ubuntu 22.04 / 24.04).

## 1. Initial VPS Setup (as root)

SSH into your VPS as root:

```bash
ssh root@YOUR_VPS_IP
```

### Create a non-root user

```bash
# Create user (replace 'peerup' with your preferred username)
adduser peerup

# Give sudo access
usermod -aG sudo peerup

# Set up SSH key access (copy your public key)
mkdir -p /home/peerup/.ssh
cp ~/.ssh/authorized_keys /home/peerup/.ssh/
chown -R peerup:peerup /home/peerup/.ssh
chmod 700 /home/peerup/.ssh
chmod 600 /home/peerup/.ssh/authorized_keys
```

### Harden SSH

```bash
# Edit SSH config
nano /etc/ssh/sshd_config
```

Set these values:

```
PermitRootLogin no
PasswordAuthentication no
```

```bash
# Apply changes
systemctl restart sshd
```

**Test in a new terminal before closing root session:**

```bash
ssh peerup@YOUR_VPS_IP
sudo whoami    # should print: root
```

### Enable firewall

```bash
# Allow SSH first (don't lock yourself out!)
ufw allow OpenSSH

# Allow relay port
ufw allow 7777/tcp comment 'peer-up relay TCP'
ufw allow 7777/udp comment 'peer-up relay QUIC'

# Block everything else inbound
ufw default deny incoming
ufw default allow outgoing

# Enable
ufw enable
ufw status verbose
```

### System updates

```bash
apt update && apt upgrade -y
apt install -y git
```

Now **log out of root** and continue as the new user.

---

## 2. Deploy Relay Server (as your user)

SSH in as your user:

```bash
ssh peerup@YOUR_VPS_IP
```

### Clone and build

```bash
# Install Go
wget -q https://go.dev/dl/go1.23.6.linux-amd64.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf go1.23.6.linux-amd64.tar.gz
rm go1.23.6.linux-amd64.tar.gz
echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
source ~/.bashrc
go version

# Clone the repo
git clone https://github.com/satindergrewal/peer-up.git
cd peer-up/relay-server

# Build
go build -o relay-server .
```

### Configure

```bash
# Create config from sample
cp ../configs/relay-server.sample.yaml relay-server.yaml

# Edit if needed (defaults are good — port 7777, gating enabled)
nano relay-server.yaml

# Create authorized_keys with your home-node and client-node peer IDs
nano relay_authorized_keys
```

Add one peer ID per line to `relay_authorized_keys`:

```
12D3KooWARqzAAN9es44ACsL7W82tfbpiMVPfSi1M5czHHYPk5fY  # home-node
12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6  # client-node
```

### Test manually first

```bash
./relay-server
```

You should see your **Relay Peer ID** in the output. Copy it — you'll need it for your home-node and client-node configs. Press Ctrl+C to stop.

### Run the setup script

**Important:** Run as your regular user, not root. The script will refuse to run as root and uses `sudo` internally where needed.

This installs the systemd service, sets permissions, configures log rotation, and runs a health check:

```bash
bash setup-linode.sh
```

Done. The relay server is now running as a systemd service.

---

## 3. Verify

Run the health check anytime:

```bash
cd ~/peer-up/relay-server
bash setup-linode.sh --check
```

You should see all `[OK]` items:

```
Binary:
  [OK]   relay-server binary exists
  [OK]   relay-server is executable

Configuration:
  [OK]   relay-server.yaml exists
  [OK]   Connection gating is ENABLED
  ...

Service:
  [OK]   relay-server service is enabled (starts on boot)
  [OK]   relay-server service is running
  [OK]   Service runs as non-root user: peerup
  ...

=== Summary: 15 passed, 0 warnings, 0 failures ===
Everything looks great!
```

---

## 4. Uninstall

To remove the systemd service, firewall rules, and system tuning:

```bash
cd ~/peer-up/relay-server
bash setup-linode.sh --uninstall
```

This removes:
- The systemd service (stopped, disabled, file deleted)
- Firewall rules for port 7777
- QUIC buffer tuning from `/etc/sysctl.conf`
- Journald log rotation settings

It does **not** delete your binary, config, keys, or source code. To fully clean up:

```bash
rm -rf ~/peer-up  # Only if you want to remove everything
```

---

## 5. Useful Commands

```bash
# Service management
sudo systemctl status relay-server
sudo systemctl restart relay-server
sudo systemctl stop relay-server

# Follow logs
sudo journalctl -u relay-server -f

# Recent logs (last 50 lines)
sudo journalctl -u relay-server -n 50

# Check log disk usage
sudo journalctl --disk-usage

# Update relay server (after code changes)
cd ~/peer-up
git pull
cd relay-server
go build -o relay-server .
sudo systemctl restart relay-server
```

---

## 6. Security Checklist

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

Or just run: `bash setup-linode.sh --check`

---

## File Layout

After setup, your relay-server directory looks like:

```
~/peer-up/relay-server/
├── relay-server              # Binary (built, gitignored)
├── relay-server.yaml         # Config (gitignored)
├── relay-server.service      # Template service file (in git)
├── relay_node.key            # Identity key (auto-generated, gitignored)
├── relay_authorized_keys     # Allowed peer IDs (gitignored)
├── setup-linode.sh           # Setup + health check script
├── main.go                   # Source code
├── go.mod
└── go.sum
```

---

## Troubleshooting

| Issue | Solution |
|-------|----------|
| Service fails to start | `sudo journalctl -u relay-server -n 30` for error logs |
| "Permission denied" on key file | `chmod 600 relay_node.key` |
| Peers can't connect | Check `relay_authorized_keys` has their peer IDs |
| Random peers connecting | Verify `enable_connection_gating: true` in config |
| High log disk usage | `sudo journalctl --vacuum-size=200M` to trim now |
| Port not reachable | `sudo ufw status` and check VPS provider firewall/security group |
| Service runs as root | `bash setup-linode.sh --uninstall` then re-run `bash setup-linode.sh` as a non-root user |
