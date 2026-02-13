#!/bin/bash
# Deploy relay server on a fresh VPS (Ubuntu 22.04 / 24.04)
#
# Usage: Run this script from inside the relay-server/ directory:
#   cd ~/peer-up/relay-server
#   bash setup-linode.sh
#
# What it does:
#   1. Installs Go (if not present)
#   2. Tunes network buffers for QUIC
#   3. Configures journald log rotation
#   4. Opens firewall ports (7777 TCP/UDP)
#   5. Builds the relay-server binary
#   6. Sets correct file permissions
#   7. Installs and starts the systemd service

set -e

RELAY_DIR="$(cd "$(dirname "$0")" && pwd)"
CURRENT_USER="$(whoami)"

echo "=== peer-up Relay Server Setup ==="
echo
echo "Relay directory: $RELAY_DIR"
echo "Running as user: $CURRENT_USER"
echo

# --- 1. Install Go if not present ---
if ! command -v go &> /dev/null; then
    echo "[1/7] Installing Go..."
    GO_VERSION="1.23.6"
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
    rm "go${GO_VERSION}.linux-amd64.tar.gz"
    export PATH=$PATH:/usr/local/go/bin
    if ! grep -q '/usr/local/go/bin' ~/.bashrc; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    fi
    echo "  Go $(go version | awk '{print $3}') installed"
else
    echo "[1/7] Go already installed: $(go version | awk '{print $3}')"
fi
echo

# --- 2. Tune network buffers for QUIC ---
echo "[2/7] Tuning network buffers for QUIC..."
if ! grep -q 'net.core.rmem_max=7500000' /etc/sysctl.conf 2>/dev/null; then
    echo "net.core.rmem_max=7500000" | sudo tee -a /etc/sysctl.conf > /dev/null
    echo "net.core.wmem_max=7500000" | sudo tee -a /etc/sysctl.conf > /dev/null
fi
sudo sysctl -w net.core.rmem_max=7500000 > /dev/null
sudo sysctl -w net.core.wmem_max=7500000 > /dev/null
echo "  Buffer sizes set to 7.5MB"
echo

# --- 3. Configure journald log rotation ---
echo "[3/7] Configuring journald log rotation..."
JOURNALD_CONF="/etc/systemd/journald.conf"
NEEDS_RESTART=false

if ! grep -q '^SystemMaxUse=500M' "$JOURNALD_CONF" 2>/dev/null; then
    sudo sed -i 's/^#\?SystemMaxUse=.*/SystemMaxUse=500M/' "$JOURNALD_CONF"
    if ! grep -q '^SystemMaxUse=' "$JOURNALD_CONF"; then
        echo "SystemMaxUse=500M" | sudo tee -a "$JOURNALD_CONF" > /dev/null
    fi
    NEEDS_RESTART=true
fi

if ! grep -q '^MaxRetentionSec=30day' "$JOURNALD_CONF" 2>/dev/null; then
    sudo sed -i 's/^#\?MaxRetentionSec=.*/MaxRetentionSec=30day/' "$JOURNALD_CONF"
    if ! grep -q '^MaxRetentionSec=' "$JOURNALD_CONF"; then
        echo "MaxRetentionSec=30day" | sudo tee -a "$JOURNALD_CONF" > /dev/null
    fi
    NEEDS_RESTART=true
fi

if [ "$NEEDS_RESTART" = true ]; then
    sudo systemctl restart systemd-journald
    echo "  Journald: max 500MB, 30-day retention (restarted)"
else
    echo "  Journald already configured"
fi
echo

# --- 4. Firewall ---
echo "[4/7] Configuring firewall..."
if command -v ufw &> /dev/null; then
    sudo ufw allow 7777/tcp comment 'peer-up relay TCP' > /dev/null 2>&1 || true
    sudo ufw allow 7777/udp comment 'peer-up relay QUIC' > /dev/null 2>&1 || true
    echo "  UFW: ports 7777 TCP+UDP open"
else
    echo "  UFW not found — manually open port 7777 TCP+UDP in your firewall"
fi
echo

# --- 5. Build ---
echo "[5/7] Building relay-server..."
cd "$RELAY_DIR"
go build -o relay-server .
echo "  Built: $RELAY_DIR/relay-server"
echo

# --- 6. File permissions ---
echo "[6/7] Setting file permissions..."
chmod 700 "$RELAY_DIR/relay-server"
if [ -f "$RELAY_DIR/relay_node.key" ]; then
    chmod 600 "$RELAY_DIR/relay_node.key"
fi
if [ -f "$RELAY_DIR/relay_authorized_keys" ]; then
    chmod 600 "$RELAY_DIR/relay_authorized_keys"
fi
if [ -f "$RELAY_DIR/relay-server.yaml" ]; then
    chmod 644 "$RELAY_DIR/relay-server.yaml"
fi
echo "  Binary: 700, keys: 600, config: 644"
echo

# --- 7. Install systemd service ---
echo "[7/7] Installing systemd service..."

# Generate service file with correct paths for this machine
cat > /tmp/relay-server.service <<SERVICEEOF
[Unit]
Description=peer-up Relay Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${CURRENT_USER}
Group=${CURRENT_USER}
WorkingDirectory=${RELAY_DIR}
ExecStart=${RELAY_DIR}/relay-server
Restart=always
RestartSec=5

# File descriptor limit for many concurrent connections
LimitNOFILE=65536

# Security hardening
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=read-only
PrivateTmp=true
ProtectKernelTunables=true
ProtectControlGroups=true
RestrictSUIDSGID=true
ReadWritePaths=${RELAY_DIR}

# Logging
StandardOutput=journal
StandardError=journal
SyslogIdentifier=relay-server

[Install]
WantedBy=multi-user.target
SERVICEEOF

sudo cp /tmp/relay-server.service /etc/systemd/system/relay-server.service
rm /tmp/relay-server.service
sudo systemctl daemon-reload
sudo systemctl enable relay-server
echo "  Service installed and enabled"
echo

# --- Start or restart ---
if systemctl is-active --quiet relay-server; then
    sudo systemctl restart relay-server
    echo "Service restarted."
else
    sudo systemctl start relay-server
    echo "Service started."
fi
echo

# --- Status ---
echo "=== Setup Complete ==="
echo
echo "Service status:"
sudo systemctl status relay-server --no-pager -l 2>&1 | head -15
echo
echo "Useful commands:"
echo "  sudo systemctl status relay-server     # Check status"
echo "  sudo journalctl -u relay-server -f     # Follow logs"
echo "  sudo journalctl --disk-usage           # Check log disk usage"
echo "  sudo systemctl restart relay-server    # Restart after config change"
echo
echo "Security checklist:"
echo "  [x] Running as non-root user ($CURRENT_USER)"
echo "  [x] Systemd hardening (NoNewPrivileges, ProtectSystem, etc.)"
echo "  [x] Journald log rotation (500MB max, 30-day retention)"
echo "  [x] File permissions (keys: 600, binary: 700)"
echo "  [ ] Verify enable_connection_gating: true in relay-server.yaml"
echo "  [ ] Verify relay_authorized_keys contains only trusted peer IDs"
echo "  [ ] Consider: sudo ufw default deny incoming"
