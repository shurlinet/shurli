# Testing Guide: SSH Access via P2P Network

This guide walks through testing the complete peer-up system with SSH service exposure.

## Goal

Connect to your home computer's SSH server from a client device (laptop/phone) through the P2P network, traversing CGNAT/NAT using a relay server.

```
[Client]  ──peerup proxy──▶  [Relay Server]  ◀──peerup serve──  [Home Server]  ──TCP──▶  [SSH :22]
 (Laptop)                       (VPS)                         (Behind CGNAT)
```

## Prerequisites

### 1. Three Machines/Terminals

- **Relay Server**: VPS with public IP (Linode, DigitalOcean, AWS, etc.)
- **Home Server**: Your home computer behind CGNAT/NAT (runs `peerup serve`)
- **Client**: Laptop or another device (runs `peerup proxy`)

### 2. SSH Server Running

On your home computer:
```bash
# Check if SSH server is running
sudo systemctl status sshd  # or ssh on macOS

# Start if not running (Linux)
sudo systemctl start sshd

# macOS - enable in System Preferences > Sharing > Remote Login
```

### 3. Build peerup

```bash
# Build peerup (single binary for everything)
go build -o peerup ./cmd/peerup
```

---

## Step 1: Deploy Relay Server

See [relay-server/README.md](../relay-server/README.md) for the full VPS setup guide.

Quick version:

```bash
cd relay-server
cp ../configs/relay-server.sample.yaml relay-server.yaml
# Edit relay-server.yaml if needed (defaults are fine)
go build -o relay-server
./relay-server
```

**Expected output:**
```
=== Relay Server (Circuit Relay v2) ===
🆔 Relay Peer ID: 12D3KooWABC...XYZ
📍 Listening on:
  /ip4/YOUR_VPS_IP/tcp/7777
  /ip4/YOUR_VPS_IP/udp/7777/quic-v1
✅ Relay server is running!
```

**Save these values:**
- Relay Peer ID: `12D3KooWABC...XYZ`
- VPS IP: `YOUR_VPS_IP`

---

## Step 2: Set Up Home Server

### Run the setup wizard

```bash
./peerup init
```

The wizard will:
1. Create `~/.config/peerup/` directory
2. Ask for your relay server address (accepts flexible formats):
   - Full multiaddr: `/ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...`
   - IP and port: `1.2.3.4:7777` (then prompts for peer ID)
   - Bare IP: `1.2.3.4` (uses default port 7777, then prompts for peer ID)
   - IPv6: `[2600:3c00::1]:7777` or `[2600:3c00::1]`
3. Generate an Ed25519 identity key
4. Display your **Peer ID** as text + QR code (share with peers)
5. Write `config.yaml`, `identity.key`, and `authorized_keys`

**Tip**: Check your peer ID anytime with `./peerup whoami`

### Configure services

Edit `~/.config/peerup/config.yaml` on the home server:

```yaml
network:
  force_private_reachability: true  # CRITICAL for CGNAT (Starlink, etc.)

# Enable SSH service
services:
  ssh:
    enabled: true
    local_address: "localhost:22"
```

### Start the server

```bash
./peerup serve
```

**Expected output:**
```
Loaded configuration from ~/.config/peerup/config.yaml
🏠 Peer ID: 12D3KooWHOME...ABC
✅ Connected to relay 12D3KooWABC...
✅ Relay address: /ip4/YOUR_VPS_IP/tcp/7777/p2p/12D3KooWABC.../p2p-circuit/p2p/12D3KooWHOME...ABC
✅ Registered service: ssh (protocol: /peerup/ssh/1.0.0, local: localhost:22)
```

**Save the Home Server Peer ID**: `12D3KooWHOME...ABC`

---

## Step 3: Set Up Client

### Run the setup wizard

```bash
./peerup init
```

### Authorize peers

**Option A: Invite/Join flow (recommended — handles both sides automatically)**

On the home server:
```bash
./peerup invite --name home
# Displays an invite code + QR code. Share the code with the client.
```

On the client:
```bash
./peerup join <invite-code> --name laptop
# Automatically: connects to inviter, exchanges peer IDs,
# adds each other to authorized_keys, adds name mapping.
```

**Option B: CLI commands**

On the client, add the home server's peer ID:
```bash
./peerup auth add 12D3KooWHOME...ABC --comment "home-server"
```

Do the same on the home server — add the client's peer ID:
```bash
./peerup auth add 12D3KooWCLIENT...XYZ --comment "laptop"
```

Verify with:
```bash
./peerup auth list
```

**Option C: Manual file edit**
```bash
# Edit ~/.config/peerup/authorized_keys
# Add the peer ID (one per line):
12D3KooWHOME...ABC  # home-server
```

### Add friendly name

If you used the invite/join flow, names are added automatically. Otherwise, edit `~/.config/peerup/config.yaml` on the client:

```yaml
# Map friendly names to peer IDs:
names:
  home: "12D3KooWHOME...ABC"  # From Step 2
```

---

## Step 4: Test SSH Connection via P2P

### Test connectivity first

```bash
./peerup ping home
```

You should see a successful ping/pong response.

### Start the SSH proxy

```bash
./peerup proxy home ssh 2222
```

This creates a local TCP listener on port 2222 that tunnels through the P2P network to the home server's SSH service.

### Connect via SSH

In another terminal:

```bash
ssh -p 2222 your_username@localhost
```

You should see your home computer's SSH prompt!

---

## Step 5: Test Other Services

### XRDP (Remote Desktop)

On the home server, enable XRDP in config:

```yaml
services:
  ssh:
    enabled: true
    local_address: "localhost:22"
  xrdp:
    enabled: true
    local_address: "localhost:3389"
```

Restart `peerup serve`, then on the client:

```bash
./peerup proxy home xrdp 13389
# Then connect:
xfreerdp /v:localhost:13389 /u:your_username
```

### Any TCP Service

```yaml
services:
  web:
    enabled: true
    local_address: "localhost:8080"
```

```bash
./peerup proxy home web 8080
# Then: curl http://localhost:8080
```

---

## Managing Relay Addresses

After initial setup, you can add or remove relay servers:

```bash
# Add a relay (flexible formats)
./peerup relay add 1.2.3.4 --peer-id 12D3KooW...
./peerup relay add 1.2.3.4:7777 --peer-id 12D3KooW...
./peerup relay add /ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...

# List configured relays
./peerup relay list

# Remove a relay
./peerup relay remove /ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...
```

### Relay health check

On the VPS, verify the relay is healthy:
```bash
sudo ./setup.sh --check
```
This shows systemd status, peer ID, public IPs, full multiaddrs, and a QR code for easy sharing.

---

## Troubleshooting

### Relay Connection Failed

```
⚠️  Could not connect to relay
```

**Fix:**
- Verify VPS firewall allows TCP 7777 and UDP 7777
- Check relay server is actually running
- Verify relay peer ID is correct in config

### No Relay Address

```
⚠️  No relay addresses yet
```

**Fix:**
- Ensure `force_private_reachability: true` in home server config
- Wait 10-15 seconds for AutoRelay
- Check relay server logs for reservation requests

### SSH Service Not Found

```
Failed to connect to SSH service: protocol not supported
```

**Fix:**
- Verify `services.ssh.enabled: true` in home server config
- Check server logs for "Registered service: ssh"
- Ensure SSH protocol ID matches: `/peerup/ssh/1.0.0`

### Connection Refused on localhost:22

```
Failed to connect to local service localhost:22
```

**Fix:**
- Start SSH server on home computer
- Check: `sudo systemctl status sshd`
- Verify SSH is listening: `netstat -tlnp | grep :22`

### Cannot Resolve Target

```
Cannot resolve target "home"
```

**Fix:**
- Add name mapping to `names:` section in client config
- Or use the full peer ID directly: `peerup proxy 12D3KooW... ssh 2222`

### Discovery Not Working

```
📡 Searching for peers... (no results)
```

**Fix:**
- Verify both nodes use the same `rendezvous` string in config
- Check DHT is bootstrapped
- Wait 30-60 seconds for DHT propagation

---

## Success Criteria

- [ ] Relay server running and accessible
- [ ] Home server gets relay address with `/p2p-circuit`
- [ ] `peerup ping home` succeeds from client
- [ ] `peerup proxy home ssh 2222` creates local listener
- [ ] `ssh -p 2222 user@localhost` connects to home computer
- [ ] XRDP / other TCP services also work

---

**Last Updated**: 2026-02-14
