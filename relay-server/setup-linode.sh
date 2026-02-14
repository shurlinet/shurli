#!/bin/bash
# Deploy, verify, and uninstall relay server on a VPS (Ubuntu 22.04 / 24.04)
#
# Usage:
#   cd ~/peer-up/relay-server
#   bash setup-linode.sh              # Full setup (install + start + verify)
#   bash setup-linode.sh --check      # Health check only (no changes)
#   bash setup-linode.sh --uninstall  # Remove service, firewall rules, tuning
#
# Peer authorization (via the relay-server binary):
#   ./relay-server authorize <peer-id> [comment]
#   ./relay-server deauthorize <peer-id>
#   ./relay-server list-peers
#
# If run as root:
#   - --check and --uninstall work directly as root
#   - Setup mode guides you through creating or selecting a secure non-root
#     service user, audits their security settings, then continues the
#     setup with the service running as that user
#
# What the full setup does:
#   1. Installs Go (if not present)
#   2. Installs qrencode (for QR codes in --check)
#   3. Tunes network buffers for QUIC
#   4. Configures journald log rotation
#   5. Opens firewall ports (7777 TCP/UDP)
#   6. Builds the relay-server binary
#   7. Sets correct file permissions
#   8. Installs and starts the systemd service
#   + Runs health check
#
# What --uninstall does:
#   1. Stops and removes the systemd service
#   2. Removes firewall rules for port 7777
#   3. Removes QUIC buffer tuning from sysctl
#   4. Reverts journald log rotation settings
#   (Does NOT delete binary, config, keys, or source code)

set -e

RELAY_DIR="$(cd "$(dirname "$0")" && pwd)"
CURRENT_USER="$(whoami)"
SERVICE_USER="$CURRENT_USER"

# Detect SSH service name (sshd on RHEL/Fedora, ssh on Debian/Ubuntu)
if systemctl list-unit-files sshd.service &>/dev/null && systemctl list-unit-files sshd.service 2>/dev/null | grep -q sshd; then
    SSH_SERVICE="sshd"
else
    SSH_SERVICE="ssh"
fi

# Run a command with sudo only when not already root
run_sudo() {
    if [ "$CURRENT_USER" = "root" ]; then
        "$@"
    else
        sudo "$@"
    fi
}

# ============================================================
# Security audit for an existing user
# ============================================================
audit_user() {
    local TARGET="$1"
    local TARGET_HOME
    TARGET_HOME=$(eval echo "~$TARGET")

    echo "--- Security audit: $TARGET ---"
    echo

    # 1. Sudo group
    if groups "$TARGET" 2>/dev/null | grep -qw sudo; then
        echo "  [OK]   Member of sudo group"
    else
        echo "  [FIX]  Not in sudo group (required for setup)"
        read -p "         Add to sudo group? [Y/n] " RESP
        if [ "$RESP" != "n" ] && [ "$RESP" != "N" ]; then
            usermod -aG sudo "$TARGET"
            echo "         Added"
        fi
    fi

    # 2. Password status (needed for sudo)
    local PASS_STATUS
    PASS_STATUS=$(passwd -S "$TARGET" 2>/dev/null | awk '{print $2}')
    case "$PASS_STATUS" in
        P)  echo "  [OK]   Password is set (can use sudo)" ;;
        L)
            echo "  [FIX]  Password is locked — cannot use sudo"
            echo "         Set a password now:"
            passwd "$TARGET"
            ;;
        NP)
            echo "  [FIX]  No password set — cannot use sudo"
            echo "         Set a password now:"
            passwd "$TARGET"
            ;;
        *)  echo "  [WARN] Could not determine password status" ;;
    esac

    # 3. SSH keys
    if [ -f "$TARGET_HOME/.ssh/authorized_keys" ] && [ -s "$TARGET_HOME/.ssh/authorized_keys" ]; then
        local KEY_COUNT
        KEY_COUNT=$(grep -c '' "$TARGET_HOME/.ssh/authorized_keys")
        echo "  [OK]   SSH authorized_keys: $KEY_COUNT key(s)"
    else
        echo "  [FIX]  No SSH keys found"
        if [ -f /root/.ssh/authorized_keys ]; then
            read -p "         Copy root's SSH keys to $TARGET? [Y/n] " RESP
            if [ "$RESP" != "n" ] && [ "$RESP" != "N" ]; then
                mkdir -p "$TARGET_HOME/.ssh"
                cp /root/.ssh/authorized_keys "$TARGET_HOME/.ssh/"
                chown -R "$TARGET:$TARGET" "$TARGET_HOME/.ssh"
                chmod 700 "$TARGET_HOME/.ssh"
                chmod 600 "$TARGET_HOME/.ssh/authorized_keys"
                echo "         Copied"
            fi
        else
            echo "         Add keys manually: $TARGET_HOME/.ssh/authorized_keys"
        fi
    fi

    # 4. SSH directory permissions
    if [ -d "$TARGET_HOME/.ssh" ]; then
        local SSH_PERMS
        SSH_PERMS=$(stat -c '%a' "$TARGET_HOME/.ssh" 2>/dev/null)
        if [ "$SSH_PERMS" = "700" ]; then
            echo "  [OK]   .ssh permissions: 700"
        else
            chmod 700 "$TARGET_HOME/.ssh"
            [ -f "$TARGET_HOME/.ssh/authorized_keys" ] && chmod 600 "$TARGET_HOME/.ssh/authorized_keys"
            chown -R "$TARGET:$TARGET" "$TARGET_HOME/.ssh"
            echo "  [FIX]  .ssh permissions corrected to 700"
        fi
    fi

    # 5. Home directory permissions
    local HOME_PERMS
    HOME_PERMS=$(stat -c '%a' "$TARGET_HOME" 2>/dev/null)
    if [ "$HOME_PERMS" = "700" ] || [ "$HOME_PERMS" = "750" ]; then
        echo "  [OK]   Home directory permissions: $HOME_PERMS"
    else
        echo "  [FIX]  Home directory permissions: $HOME_PERMS (should be 700)"
        read -p "         Set to 700? [Y/n] " RESP
        if [ "$RESP" != "n" ] && [ "$RESP" != "N" ]; then
            chmod 700 "$TARGET_HOME"
            echo "         Set to 700"
        fi
    fi

    # 6. SSH daemon config
    echo
    echo "  SSH daemon:"
    local SSH_ISSUES=0
    if grep -qE '^\s*PasswordAuthentication\s+no' /etc/ssh/sshd_config 2>/dev/null; then
        echo "  [OK]   Password authentication disabled"
    else
        echo "  [WARN] Password authentication may be enabled"
        SSH_ISSUES=$((SSH_ISSUES + 1))
    fi
    if grep -qE '^\s*PermitRootLogin\s+no' /etc/ssh/sshd_config 2>/dev/null; then
        echo "  [OK]   Root login disabled"
    else
        echo "  [WARN] Root login may be enabled"
        SSH_ISSUES=$((SSH_ISSUES + 1))
    fi
    if [ "$SSH_ISSUES" -gt 0 ]; then
        echo
        echo "  Recommended: harden /etc/ssh/sshd_config:"
        echo "    PasswordAuthentication no"
        echo "    PermitRootLogin no"
        read -p "  Apply SSH hardening now? [y/N] " RESP
        if [ "$RESP" = "y" ] || [ "$RESP" = "Y" ]; then
            sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
            grep -q '^PasswordAuthentication' /etc/ssh/sshd_config || echo "PasswordAuthentication no" >> /etc/ssh/sshd_config
            sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
            grep -q '^PermitRootLogin' /etc/ssh/sshd_config || echo "PermitRootLogin no" >> /etc/ssh/sshd_config
            systemctl restart "$SSH_SERVICE"
            echo "  [OK] SSH hardened and restarted"
            echo
            echo "  *** TEST: Verify SSH in a NEW terminal before closing this session! ***"
        fi
    fi
    echo
}

# ============================================================
# Create a new hardened user for the relay service
# ============================================================
create_secure_user() {
    local NEW_USER="$1"
    echo "--- Creating hardened user: $NEW_USER ---"
    echo

    # Create with home directory and bash shell
    useradd -m -s /bin/bash "$NEW_USER"
    echo "  [OK] User created: /home/$NEW_USER"

    # Set password (required for sudo)
    echo
    echo "  Set a strong password for $NEW_USER (required for sudo):"
    passwd "$NEW_USER"
    echo

    # Sudo group
    usermod -aG sudo "$NEW_USER"
    echo "  [OK] Added to sudo group"

    # Lock down home directory
    chmod 700 "/home/$NEW_USER"
    echo "  [OK] Home directory: 700"

    # SSH keys from root
    local HAS_SSH_KEYS=false
    if [ -f /root/.ssh/authorized_keys ]; then
        mkdir -p "/home/$NEW_USER/.ssh"
        cp /root/.ssh/authorized_keys "/home/$NEW_USER/.ssh/"
        chown -R "$NEW_USER:$NEW_USER" "/home/$NEW_USER/.ssh"
        chmod 700 "/home/$NEW_USER/.ssh"
        chmod 600 "/home/$NEW_USER/.ssh/authorized_keys"
        echo "  [OK] SSH keys copied from root"
        HAS_SSH_KEYS=true
    else
        echo "  [WARN] No root SSH keys to copy"
        echo "         Add keys manually: /home/$NEW_USER/.ssh/authorized_keys"
    fi

    # SSH daemon hardening
    echo
    echo "  SSH daemon hardening:"
    echo "    PasswordAuthentication no  — key-only SSH login"
    echo "    PermitRootLogin no         — block root SSH access"
    echo
    if [ "$HAS_SSH_KEYS" = true ]; then
        echo "  SSH keys are in place — safe to harden."
        read -p "  Apply SSH hardening? [Y/n] " RESP
        RESP=${RESP:-Y}
    else
        echo "  WARNING: No SSH keys confirmed. Hardening could lock you out!"
        read -p "  Apply SSH hardening? [y/N] " RESP
        RESP=${RESP:-N}
    fi
    if [ "$RESP" = "y" ] || [ "$RESP" = "Y" ]; then
        sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication no/' /etc/ssh/sshd_config
        grep -q '^PasswordAuthentication' /etc/ssh/sshd_config || echo "PasswordAuthentication no" >> /etc/ssh/sshd_config
        sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin no/' /etc/ssh/sshd_config
        grep -q '^PermitRootLogin' /etc/ssh/sshd_config || echo "PermitRootLogin no" >> /etc/ssh/sshd_config
        systemctl restart sshd
        echo "  [OK] SSH hardened and restarted"
        echo
        local VPS_IP
        VPS_IP=$(hostname -I 2>/dev/null | awk '{print $1}')
        echo "  *** CRITICAL: Test SSH in a NEW terminal before closing this session! ***"
        echo "  ssh $NEW_USER@${VPS_IP:-YOUR_VPS_IP}"
    else
        echo "  Skipped — apply manually later in /etc/ssh/sshd_config"
    fi

    echo
    echo "User $NEW_USER created and secured."
    echo
}

# ============================================================
# Root handler — create/select service user or allow --check/--uninstall
# ============================================================
if [ "$CURRENT_USER" = "root" ]; then
    # --check and --uninstall are safe to run as root
    if [ "$1" = "--check" ] || [ "$1" = "--uninstall" ]; then
        : # fall through to handlers below
    else
        echo "=== Running as root — service user setup ==="
        echo
        echo "The relay server must NOT run as root."
        echo "Let's set up a proper service user first."
        echo
        echo "  1) Select an existing user"
        echo "  2) Create a new dedicated user"
        echo
        read -p "Choice [1/2]: " USER_CHOICE
        echo

        TARGET_USER=""

        case "$USER_CHOICE" in
            1)
                echo "Non-root users with login shells:"
                USERS=()
                while IFS=: read -r name _ uid _ _ home shell; do
                    if [ "$uid" -ge 1000 ] && [ "$uid" -lt 65534 ] && \
                       [[ "$shell" == */bash || "$shell" == */zsh || "$shell" == */sh ]]; then
                        USERS+=("$name")
                        echo "  ${#USERS[@]}) $name  (home: $home)"
                    fi
                done < /etc/passwd

                if [ ${#USERS[@]} -eq 0 ]; then
                    echo "  No suitable users found. Use option 2 to create one."
                    exit 1
                fi

                echo
                read -p "Select [1-${#USERS[@]}]: " IDX
                if [[ "$IDX" =~ ^[0-9]+$ ]] && [ "$IDX" -ge 1 ] && [ "$IDX" -le "${#USERS[@]}" ]; then
                    TARGET_USER="${USERS[$((IDX - 1))]}"
                else
                    echo "Invalid selection."
                    exit 1
                fi
                echo
                audit_user "$TARGET_USER"
                ;;
            2)
                read -p "Username for the new user: " NEW_NAME
                if [ -z "$NEW_NAME" ]; then
                    echo "Username cannot be empty."
                    exit 1
                fi
                if id "$NEW_NAME" &>/dev/null; then
                    echo "ERROR: User '$NEW_NAME' already exists. Use option 1."
                    exit 1
                fi
                echo
                create_secure_user "$NEW_NAME"
                TARGET_USER="$NEW_NAME"
                ;;
            *)
                echo "Invalid choice."
                exit 1
                ;;
        esac

        # Set SERVICE_USER and fall through to the setup section.
        # We stay as root (which doesn't need sudo) and use SERVICE_USER
        # for the systemd service file and file ownership.
        SERVICE_USER="$TARGET_USER"
        echo "Service will run as: $SERVICE_USER"
        echo "Continuing with setup..."
        echo
    fi
fi

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
        if run_sudo ufw status 2>/dev/null | grep -q '7777'; then
            check_pass "UFW: port 7777 is allowed"
        else
            check_fail "UFW: port 7777 not in firewall rules"
            echo "         Fix: sudo ufw allow 7777/tcp && sudo ufw allow 7777/udp"
        fi

        if run_sudo ufw status 2>/dev/null | grep -q 'Status: active'; then
            check_pass "UFW firewall is active"

            # Check default policy
            if run_sudo ufw status verbose 2>/dev/null | grep -q 'Default: deny (incoming)'; then
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

    # --- Connection Info ---
    echo "Connection info:"
    # Try to get peer ID from journal logs
    PEER_ID=$(journalctl -u relay-server --no-pager -n 100 2>/dev/null | grep 'Relay Peer ID:' | tail -1 | awk '{print $NF}')
    if [ -z "$PEER_ID" ]; then
        check_warn "Cannot determine Peer ID (start the service first)"
    else
        check_pass "Relay Peer ID: $PEER_ID"

        # Detect public IPs
        PUBLIC_IPV4=$(ip -4 addr show scope global 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1 | head -1)
        PUBLIC_IPV6=$(ip -6 addr show scope global 2>/dev/null | grep 'inet6 ' | awk '{print $2}' | cut -d/ -f1 | head -1)

        if [ -n "$PUBLIC_IPV4" ]; then
            MADDR_V4="/ip4/$PUBLIC_IPV4/tcp/7777/p2p/$PEER_ID"
            echo "  [INFO] Multiaddr (IPv4): $MADDR_V4"
        fi
        if [ -n "$PUBLIC_IPV6" ]; then
            MADDR_V6="/ip6/$PUBLIC_IPV6/tcp/7777/p2p/$PEER_ID"
            echo "  [INFO] Multiaddr (IPv6): $MADDR_V6"
        fi

        # Show QR code if qrencode is available
        if command -v qrencode &>/dev/null; then
            if [ -n "$PUBLIC_IPV4" ]; then
                echo
                echo "  Scan this QR code during 'peerup init':"
                qrencode -t ANSIUTF8 "$MADDR_V4" 2>/dev/null || qrencode -t UTF8 "$MADDR_V4" 2>/dev/null
            fi
        fi

        echo
        echo "  Quick setup on your nodes:"
        if [ -n "$PUBLIC_IPV4" ]; then
            echo "    peerup init   →  enter: $PUBLIC_IPV4:7777"
        fi
        echo "    Peer ID:  $PEER_ID"
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
# If --uninstall flag, reverse the full setup
# ============================================================
if [ "$1" = "--uninstall" ]; then
    echo "=== peer-up Relay Server Uninstall ==="
    echo
    echo "This will remove the systemd service, firewall rules,"
    echo "and system tuning applied by setup-linode.sh."
    echo
    echo "It will NOT delete your config, keys, binary, or source code."
    echo
    read -p "Continue? [y/N] " CONFIRM
    if [ "$CONFIRM" != "y" ] && [ "$CONFIRM" != "Y" ]; then
        echo "Aborted."
        exit 0
    fi
    echo

    # --- 1. Stop and remove systemd service ---
    echo "[1/4] Removing systemd service..."
    if systemctl is-active --quiet relay-server 2>/dev/null; then
        run_sudo systemctl stop relay-server
        echo "  Service stopped"
    fi
    if systemctl is-enabled --quiet relay-server 2>/dev/null; then
        run_sudo systemctl disable relay-server
        echo "  Service disabled"
    fi
    if [ -f /etc/systemd/system/relay-server.service ]; then
        run_sudo rm /etc/systemd/system/relay-server.service
        run_sudo systemctl daemon-reload
        echo "  Service file removed, daemon reloaded"
    else
        echo "  Service file not found (already removed)"
    fi
    echo

    # --- 2. Remove firewall rules ---
    echo "[2/4] Removing firewall rules..."
    if command -v ufw &> /dev/null; then
        run_sudo ufw delete allow 7777/tcp > /dev/null 2>&1 && echo "  Removed 7777/tcp rule" || echo "  No 7777/tcp rule found"
        run_sudo ufw delete allow 7777/udp > /dev/null 2>&1 && echo "  Removed 7777/udp rule" || echo "  No 7777/udp rule found"
    else
        echo "  UFW not found — remove port 7777 rules from your firewall manually"
    fi
    echo

    # --- 3. Remove sysctl QUIC buffer tuning ---
    echo "[3/4] Removing QUIC buffer tuning..."
    if grep -q 'net.core.rmem_max=7500000' /etc/sysctl.conf 2>/dev/null; then
        run_sudo sed -i '/^net\.core\.rmem_max=7500000$/d' /etc/sysctl.conf
        run_sudo sed -i '/^net\.core\.wmem_max=7500000$/d' /etc/sysctl.conf
        run_sudo sysctl -p > /dev/null 2>&1
        echo "  Removed buffer tuning from /etc/sysctl.conf"
    else
        echo "  No QUIC buffer tuning found in /etc/sysctl.conf"
    fi
    echo

    # --- 4. Revert journald log rotation ---
    echo "[4/4] Reverting journald log rotation..."
    JOURNALD_CONF="/etc/systemd/journald.conf"
    JOURNALD_CHANGED=false
    if grep -q '^SystemMaxUse=500M' "$JOURNALD_CONF" 2>/dev/null; then
        run_sudo sed -i 's/^SystemMaxUse=500M/#SystemMaxUse=/' "$JOURNALD_CONF"
        JOURNALD_CHANGED=true
    fi
    if grep -q '^MaxRetentionSec=30day' "$JOURNALD_CONF" 2>/dev/null; then
        run_sudo sed -i 's/^MaxRetentionSec=30day/#MaxRetentionSec=/' "$JOURNALD_CONF"
        JOURNALD_CHANGED=true
    fi
    if [ "$JOURNALD_CHANGED" = true ]; then
        run_sudo systemctl restart systemd-journald
        echo "  Reverted journald settings (restarted)"
    else
        echo "  No journald changes to revert"
    fi
    echo

    echo "=== Uninstall complete ==="
    echo
    echo "The following were left untouched (delete manually if desired):"
    echo "  $RELAY_DIR/relay-server          (binary)"
    echo "  $RELAY_DIR/relay-server.yaml     (config)"
    echo "  $RELAY_DIR/relay_node.key        (identity key)"
    echo "  $RELAY_DIR/relay_authorized_keys (peer allowlist)"
    echo
    echo "To reinstall later:  bash setup-linode.sh"
    exit 0
fi

# ============================================================
# Full setup
# ============================================================
echo "=== peer-up Relay Server Setup ==="
echo
echo "Relay directory: $RELAY_DIR"
echo "Running as:      $CURRENT_USER"
echo "Service user:    $SERVICE_USER"
echo

# --- 1. Install Go if not present ---
if ! command -v go &> /dev/null; then
    echo "[1/8] Installing Go..."
    GO_VERSION="1.23.6"
    wget -q "https://go.dev/dl/go${GO_VERSION}.linux-amd64.tar.gz"
    run_sudo rm -rf /usr/local/go
    run_sudo tar -C /usr/local -xzf "go${GO_VERSION}.linux-amd64.tar.gz"
    rm "go${GO_VERSION}.linux-amd64.tar.gz"
    export PATH=$PATH:/usr/local/go/bin
    if ! grep -q '/usr/local/go/bin' ~/.bashrc; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> ~/.bashrc
    fi
    echo "  Go $(go version | awk '{print $3}') installed"
else
    echo "[1/8] Go already installed: $(go version | awk '{print $3}')"
fi
echo

# --- 2. Install qrencode for QR code display in --check ---
if ! command -v qrencode &>/dev/null; then
    echo "[2/8] Installing qrencode..."
    run_sudo apt-get install -y -qq qrencode > /dev/null 2>&1
    echo "  qrencode installed (used by --check for QR codes)"
else
    echo "[2/8] qrencode already installed"
fi
echo

# --- 3. Tune network buffers for QUIC ---
echo "[3/8] Tuning network buffers for QUIC..."
if ! grep -q 'net.core.rmem_max=7500000' /etc/sysctl.conf 2>/dev/null; then
    echo "net.core.rmem_max=7500000" | run_sudo tee -a /etc/sysctl.conf > /dev/null
    echo "net.core.wmem_max=7500000" | run_sudo tee -a /etc/sysctl.conf > /dev/null
fi
run_sudo sysctl -w net.core.rmem_max=7500000 > /dev/null
run_sudo sysctl -w net.core.wmem_max=7500000 > /dev/null
echo "  Buffer sizes set to 7.5MB"
echo

# --- 3. Configure journald log rotation ---
echo "[4/8] Configuring journald log rotation..."
JOURNALD_CONF="/etc/systemd/journald.conf"
NEEDS_RESTART=false

if ! grep -q '^SystemMaxUse=500M' "$JOURNALD_CONF" 2>/dev/null; then
    run_sudo sed -i 's/^#\?SystemMaxUse=.*/SystemMaxUse=500M/' "$JOURNALD_CONF"
    if ! grep -q '^SystemMaxUse=' "$JOURNALD_CONF"; then
        echo "SystemMaxUse=500M" | run_sudo tee -a "$JOURNALD_CONF" > /dev/null
    fi
    NEEDS_RESTART=true
fi

if ! grep -q '^MaxRetentionSec=30day' "$JOURNALD_CONF" 2>/dev/null; then
    run_sudo sed -i 's/^#\?MaxRetentionSec=.*/MaxRetentionSec=30day/' "$JOURNALD_CONF"
    if ! grep -q '^MaxRetentionSec=' "$JOURNALD_CONF"; then
        echo "MaxRetentionSec=30day" | run_sudo tee -a "$JOURNALD_CONF" > /dev/null
    fi
    NEEDS_RESTART=true
fi

if [ "$NEEDS_RESTART" = true ]; then
    run_sudo systemctl restart systemd-journald
    echo "  Journald: max 500MB, 30-day retention (restarted)"
else
    echo "  Journald already configured"
fi
echo

# --- 4. Firewall ---
echo "[5/8] Configuring firewall..."
if command -v ufw &> /dev/null; then
    run_sudo ufw allow 7777/tcp comment 'peer-up relay TCP' > /dev/null 2>&1 || true
    run_sudo ufw allow 7777/udp comment 'peer-up relay QUIC' > /dev/null 2>&1 || true
    echo "  UFW: ports 7777 TCP+UDP open"
else
    echo "  UFW not found — manually open port 7777 TCP+UDP in your firewall"
fi
echo

# --- 5. Build ---
echo "[6/8] Building relay-server..."
cd "$RELAY_DIR"
go build -o relay-server .
echo "  Built: $RELAY_DIR/relay-server"
echo

# --- 6. File permissions ---
echo "[7/8] Setting file permissions..."
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
# When running as root for a different service user, transfer ownership
if [ "$SERVICE_USER" != "$CURRENT_USER" ]; then
    chown -R "$SERVICE_USER:$SERVICE_USER" "$RELAY_DIR"
    echo "  Ownership: $SERVICE_USER"
fi
echo "  Binary: 700, keys: 600, config: 644"
echo

# --- 7. Install systemd service ---
echo "[8/8] Installing systemd service..."

# Generate service file with correct paths for this machine
cat > /tmp/relay-server.service <<SERVICEEOF
[Unit]
Description=peer-up Relay Server
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${SERVICE_USER}
Group=${SERVICE_USER}
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

run_sudo cp /tmp/relay-server.service /etc/systemd/system/relay-server.service
rm /tmp/relay-server.service
run_sudo systemctl daemon-reload
run_sudo systemctl enable relay-server
echo "  Service installed and enabled"
echo

# --- Start or restart ---
if systemctl is-active --quiet relay-server; then
    run_sudo systemctl restart relay-server
    echo "Service restarted."
else
    run_sudo systemctl start relay-server
    echo "Service started."
fi

# Give the service a moment to start
sleep 2
echo

# --- Run health check ---
echo
run_check
