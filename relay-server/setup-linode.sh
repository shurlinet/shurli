#!/bin/bash
# Deploy and verify relay server on a VPS (Ubuntu 22.04 / 24.04)
#
# Usage:
#   cd ~/peer-up/relay-server
#   bash setup-linode.sh            # Full setup (install + start + verify)
#   bash setup-linode.sh --check    # Health check only (no changes)
#
# What the full setup does:
#   1. Installs Go (if not present)
#   2. Tunes network buffers for QUIC
#   3. Configures journald log rotation
#   4. Opens firewall ports (7777 TCP/UDP)
#   5. Builds the relay-server binary
#   6. Sets correct file permissions
#   7. Installs and starts the systemd service
#   8. Runs health check

set -e

RELAY_DIR="$(cd "$(dirname "$0")" && pwd)"
CURRENT_USER="$(whoami)"

# ============================================================
# Health check function — verifies everything is correct
# ============================================================
run_check() {
    echo "=== peer-up Relay Server Health Check ==="
    echo
    echo "Directory: $RELAY_DIR"
    echo

    PASS=0
    WARN=0
    FAIL=0

    check_pass() { echo "  [OK]   $1"; PASS=$((PASS + 1)); }
    check_warn() { echo "  [WARN] $1"; WARN=$((WARN + 1)); }
    check_fail() { echo "  [FAIL] $1"; FAIL=$((FAIL + 1)); }

    # --- Binary ---
    echo "Binary:"
    if [ -f "$RELAY_DIR/relay-server" ]; then
        check_pass "relay-server binary exists"
    else
        check_fail "relay-server binary not found — run: go build -o relay-server ."
    fi

    if [ -x "$RELAY_DIR/relay-server" ]; then
        check_pass "relay-server is executable"
    elif [ -f "$RELAY_DIR/relay-server" ]; then
        check_fail "relay-server is not executable — run: chmod 700 relay-server"
    fi
    echo

    # --- Config file ---
    echo "Configuration:"
    if [ -f "$RELAY_DIR/relay-server.yaml" ]; then
        check_pass "relay-server.yaml exists"

        # Check connection gating
        if grep -q 'enable_connection_gating:.*true' "$RELAY_DIR/relay-server.yaml" 2>/dev/null; then
            check_pass "Connection gating is ENABLED"
        elif grep -q 'enable_connection_gating:.*false' "$RELAY_DIR/relay-server.yaml" 2>/dev/null; then
            check_fail "Connection gating is DISABLED — any peer can use your relay!"
            echo "         Fix: set enable_connection_gating: true in relay-server.yaml"
        else
            check_warn "Cannot determine connection gating status"
        fi

        # Check config permissions
        PERMS=$(stat -c '%a' "$RELAY_DIR/relay-server.yaml" 2>/dev/null || stat -f '%Lp' "$RELAY_DIR/relay-server.yaml" 2>/dev/null)
        if [ "$PERMS" = "644" ] || [ "$PERMS" = "600" ]; then
            check_pass "relay-server.yaml permissions: $PERMS"
        else
            check_warn "relay-server.yaml permissions: $PERMS (expected 644)"
        fi
    else
        check_fail "relay-server.yaml not found — copy from configs/relay-server.sample.yaml"
    fi
    echo

    # --- Key file ---
    echo "Identity:"
    if [ -f "$RELAY_DIR/relay_node.key" ]; then
        check_pass "relay_node.key exists"
        PERMS=$(stat -c '%a' "$RELAY_DIR/relay_node.key" 2>/dev/null || stat -f '%Lp' "$RELAY_DIR/relay_node.key" 2>/dev/null)
        if [ "$PERMS" = "600" ]; then
            check_pass "relay_node.key permissions: 600"
        else
            check_fail "relay_node.key permissions: $PERMS (should be 600)"
            echo "         Fix: chmod 600 relay_node.key"
        fi
    else
        check_warn "relay_node.key not found (will be created on first run)"
    fi
    echo

    # --- Authorized keys ---
    echo "Authorization:"
    if [ -f "$RELAY_DIR/relay_authorized_keys" ]; then
        check_pass "relay_authorized_keys exists"
        PEER_COUNT=$(grep -cE '^[^#[:space:]]' "$RELAY_DIR/relay_authorized_keys" 2>/dev/null || echo 0)
        if [ "$PEER_COUNT" -gt 0 ]; then
            check_pass "$PEER_COUNT authorized peer(s) configured"
        else
            check_warn "relay_authorized_keys is empty — no peers can connect"
        fi

        PERMS=$(stat -c '%a' "$RELAY_DIR/relay_authorized_keys" 2>/dev/null || stat -f '%Lp' "$RELAY_DIR/relay_authorized_keys" 2>/dev/null)
        if [ "$PERMS" = "600" ]; then
            check_pass "relay_authorized_keys permissions: 600"
        else
            check_warn "relay_authorized_keys permissions: $PERMS (should be 600)"
            echo "         Fix: chmod 600 relay_authorized_keys"
        fi
    else
        check_fail "relay_authorized_keys not found"
        echo "         Fix: create it with one peer ID per line"
    fi
    echo

    # --- Systemd service ---
    echo "Service:"
    if systemctl is-enabled --quiet relay-server 2>/dev/null; then
        check_pass "relay-server service is enabled (starts on boot)"
    else
        check_warn "relay-server service is not enabled"
        echo "         Fix: sudo systemctl enable relay-server"
    fi

    if systemctl is-active --quiet relay-server 2>/dev/null; then
        check_pass "relay-server service is running"
        # Check how long it's been running
        UPTIME=$(systemctl show relay-server --property=ActiveEnterTimestamp --value 2>/dev/null)
        if [ -n "$UPTIME" ]; then
            echo "         Started: $UPTIME"
        fi
    else
        check_fail "relay-server service is NOT running"
        echo "         Fix: sudo systemctl start relay-server"
        echo "         Logs: sudo journalctl -u relay-server -n 20"
    fi

    # Check service user
    SVC_USER=$(systemctl show relay-server --property=User --value 2>/dev/null)
    if [ -n "$SVC_USER" ] && [ "$SVC_USER" != "root" ]; then
        check_pass "Service runs as non-root user: $SVC_USER"
    elif [ "$SVC_USER" = "root" ]; then
        check_fail "Service runs as root — update the service file"
    fi
    echo

    # --- Network ---
    echo "Network:"
    # Check if port 7777 is listening
    if ss -tlnp 2>/dev/null | grep -q ':7777 ' || netstat -tlnp 2>/dev/null | grep -q ':7777 '; then
        check_pass "Port 7777 TCP is listening"
    elif systemctl is-active --quiet relay-server 2>/dev/null; then
        check_warn "Port 7777 TCP not detected (may need a moment to start)"
    else
        check_warn "Port 7777 TCP not listening (service not running)"
    fi

    # Check firewall
    if command -v ufw &> /dev/null; then
        if sudo ufw status 2>/dev/null | grep -q '7777'; then
            check_pass "UFW: port 7777 is allowed"
        else
            check_fail "UFW: port 7777 not in firewall rules"
            echo "         Fix: sudo ufw allow 7777/tcp && sudo ufw allow 7777/udp"
        fi

        if sudo ufw status 2>/dev/null | grep -q 'Status: active'; then
            check_pass "UFW firewall is active"

            # Check default policy
            if sudo ufw status verbose 2>/dev/null | grep -q 'Default: deny (incoming)'; then
                check_pass "UFW default incoming policy: deny"
            else
                check_warn "UFW default incoming policy is not 'deny'"
                echo "         Consider: sudo ufw default deny incoming"
            fi
        else
            check_warn "UFW firewall is not active"
            echo "         Consider: sudo ufw enable"
        fi
    else
        check_warn "UFW not installed — verify firewall manually"
    fi
    echo

    # --- Journald ---
    echo "Log management:"
    JOURNALD_CONF="/etc/systemd/journald.conf"
    if grep -q '^SystemMaxUse=' "$JOURNALD_CONF" 2>/dev/null; then
        MAX_USE=$(grep '^SystemMaxUse=' "$JOURNALD_CONF" | cut -d= -f2)
        check_pass "Journald max disk usage: $MAX_USE"
    else
        check_warn "Journald SystemMaxUse not configured (unbounded logs)"
        echo "         Fix: add SystemMaxUse=500M to $JOURNALD_CONF"
    fi

    if grep -q '^MaxRetentionSec=' "$JOURNALD_CONF" 2>/dev/null; then
        RETENTION=$(grep '^MaxRetentionSec=' "$JOURNALD_CONF" | cut -d= -f2)
        check_pass "Journald retention: $RETENTION"
    else
        check_warn "Journald MaxRetentionSec not configured"
        echo "         Fix: add MaxRetentionSec=30day to $JOURNALD_CONF"
    fi

    DISK_USAGE=$(journalctl --disk-usage 2>/dev/null | grep -oP '[\d.]+[KMGT]' || echo "unknown")
    echo "  [INFO] Current journal disk usage: $DISK_USAGE"
    echo

    # --- QUIC buffers ---
    echo "QUIC buffers:"
    RMEM=$(sysctl -n net.core.rmem_max 2>/dev/null || echo 0)
    if [ "$RMEM" -ge 7500000 ] 2>/dev/null; then
        check_pass "net.core.rmem_max = $RMEM"
    else
        check_warn "net.core.rmem_max = $RMEM (recommended: 7500000)"
    fi

    WMEM=$(sysctl -n net.core.wmem_max 2>/dev/null || echo 0)
    if [ "$WMEM" -ge 7500000 ] 2>/dev/null; then
        check_pass "net.core.wmem_max = $WMEM"
    else
        check_warn "net.core.wmem_max = $WMEM (recommended: 7500000)"
    fi
    echo

    # --- Summary ---
    echo "=== Summary: $PASS passed, $WARN warnings, $FAIL failures ==="
    if [ "$FAIL" -gt 0 ]; then
        echo "Fix the [FAIL] items above before running in production."
        return 1
    elif [ "$WARN" -gt 0 ]; then
        echo "All good, but review [WARN] items for best security."
        return 0
    else
        echo "Everything looks great!"
        return 0
    fi
}

# ============================================================
# If --check flag, run health check only and exit
# ============================================================
if [ "$1" = "--check" ]; then
    run_check
    exit $?
fi

# ============================================================
# Full setup
# ============================================================
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

# Give the service a moment to start
sleep 2
echo

# --- Run health check ---
echo
run_check
