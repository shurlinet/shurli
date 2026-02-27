# Daemon Service Deployment

This directory contains service files for running `shurli daemon` as a managed system service. Two platforms are supported:

| File | Platform | Init system |
|------|----------|-------------|
| `shurli-daemon.service` | Linux | systemd |
| `com.shurli.daemon.plist` | macOS | launchd |

Both ensure the daemon starts on boot, restarts on crash, and integrates with the platform's logging system.

---

## Prerequisites

1. **Binary installed** at `/usr/local/bin/shurli`:

   ```bash
   # Option A: build from source
   go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli
   sudo cp shurli /usr/local/bin/shurli

   # Option B: symlink (dev workflow, see "Dev Workflow" below)
   sudo ln -sf "$(pwd)/shurli" /usr/local/bin/shurli
   ```

2. **Config initialized** (creates `~/.config/shurli/` with identity key and config):

   ```bash
   shurli init
   ```

3. **Verify it runs** before installing the service:

   ```bash
   shurli daemon
   # Ctrl+C to stop once you see "daemon started"
   ```

---

## Linux (systemd)

### 1. Create a service user

Running as a dedicated non-root user is recommended:

```bash
sudo useradd -r -m -s /bin/bash shurli
```

Initialize config as that user:

```bash
sudo -u shurli shurli init
```

This creates `/home/shurli/.config/shurli/` with identity key, config, and authorized_keys.

### 2. Install the service file

```bash
sudo cp deploy/shurli-daemon.service /etc/systemd/system/shurli-daemon.service
```

If your config directory is not `/home/shurli/.config/shurli`, edit the `ReadWritePaths` line:

```bash
sudo nano /etc/systemd/system/shurli-daemon.service
# Change: ReadWritePaths=/home/shurli/.config/shurli
# To:     ReadWritePaths=/path/to/your/.config/shurli
```

### 3. Enable and start

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now shurli-daemon
```

### 4. Verify

```bash
# Check status (should show "active (running)")
sudo systemctl status shurli-daemon

# Check recent logs
sudo journalctl -u shurli-daemon -n 20

# Confirm the daemon API is responding
shurli status
```

### What the service file does

| Directive | Purpose |
|-----------|---------|
| `Type=notify` | Daemon signals readiness via sd_notify (`READY=1`) |
| `WatchdogSec=90` | systemd kills the process if no heartbeat for 90 seconds |
| `Restart=on-failure` | Auto-restart on crash (not on clean exit) |
| `RestartSec=5` | Wait 5 seconds before restarting |
| `User=shurli` | Runs as non-root user |
| `NoNewPrivileges=true` | Cannot gain additional privileges |
| `ProtectSystem=strict` | Filesystem is read-only except explicit paths |
| `ProtectHome=read-only` | Home directories are read-only except explicit paths |
| `ReadWritePaths=...` | Only the config directory is writable |
| `ProtectKernelTunables=true` | Cannot modify kernel parameters |
| `ProtectControlGroups=true` | Read-only access to cgroups |
| `RestrictSUIDSGID=true` | Cannot set SUID/SGID bits |
| `PrivateTmp=true` | Isolated /tmp |
| `LimitNOFILE=65536` | Enough file descriptors for many peer connections |
| `MemoryMax=512M` | Hard memory cap |

---

## macOS (launchd)

### 1. Copy the plist

```bash
cp deploy/com.shurli.daemon.plist ~/Library/LaunchAgents/
```

This installs as a **user agent** (runs as your user, no sudo needed). The daemon has access to your user's config at `~/.config/shurli/`.

### 2. Load the service

```bash
launchctl load ~/Library/LaunchAgents/com.shurli.daemon.plist
```

The daemon starts immediately (`RunAtLoad=true`) and will restart automatically if it crashes (`KeepAlive=true`).

### 3. Verify

```bash
# Check it's running (look for com.shurli.daemon, exit code 0 means running)
launchctl list | grep shurli

# Check logs
tail -50 /tmp/shurli-daemon.log

# Confirm the daemon API is responding
shurli status
```

### What the plist does

| Key | Purpose |
|-----|---------|
| `RunAtLoad` | Starts when the plist is loaded (and on login) |
| `KeepAlive` | Restarts automatically if the process exits |
| `StandardOutPath` / `StandardErrorPath` | Logs to `/tmp/shurli-daemon.log` |
| `PATH` | Ensures `/usr/local/bin` is available |

---

## How the Watchdog Works

The daemon includes a built-in watchdog (`internal/watchdog/watchdog.go`) that is pure Go with no CGo dependency.

**On Linux (systemd):**
- Every 30 seconds, the watchdog runs health checks and sends `WATCHDOG=1` to systemd via the notify socket
- On startup, it sends `READY=1` (tells systemd the daemon is fully initialized)
- On graceful shutdown, it sends `STOPPING=1`
- `WatchdogSec=90` in the service file means systemd allows 3 missed heartbeats (3 x 30s) before killing and restarting the process
- Detection: reads `NOTIFY_SOCKET` environment variable (set by systemd automatically)

**On macOS (launchd):**
- The watchdog is a no-op. `NOTIFY_SOCKET` is not set, so all sd_notify calls return immediately
- Crash recovery is handled by launchd's `KeepAlive=true` instead
- Health checks still run and log via slog, but no external signaling occurs

---

## Managing the Service

### Start / Stop / Restart

| Action | Linux (systemd) | macOS (launchd) |
|--------|-----------------|-----------------|
| Start | `sudo systemctl start shurli-daemon` | `launchctl load ~/Library/LaunchAgents/com.shurli.daemon.plist` |
| Stop | `sudo systemctl stop shurli-daemon` | `launchctl unload ~/Library/LaunchAgents/com.shurli.daemon.plist` |
| Restart | `sudo systemctl restart shurli-daemon` | Unload then load (or `launchctl kickstart -k gui/$(id -u)/com.shurli.daemon`) |
| Status | `sudo systemctl status shurli-daemon` | `launchctl list \| grep shurli` |

### Viewing Logs

| Platform | Command |
|----------|---------|
| Linux | `sudo journalctl -u shurli-daemon -f` (follow) |
| Linux | `sudo journalctl -u shurli-daemon --since "10 min ago"` |
| macOS | `tail -f /tmp/shurli-daemon.log` |

### Checking Daemon Health

Works on both platforms once the daemon is running:

```bash
# Quick status (peer count, uptime, connection state)
shurli status

# Ping a known peer
shurli ping <peer-name>
```

---

## Dev Workflow

For active development, use a symlink so you can rebuild without copying binaries:

```bash
# One-time setup: symlink repo binary to system path
cd /path/to/Shurli
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli
sudo ln -sf "$(pwd)/shurli" /usr/local/bin/shurli
```

After code changes, the cycle is:

```bash
# Rebuild
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli

# Restart the service (picks up new binary via symlink)
sudo systemctl restart shurli-daemon          # Linux
launchctl kickstart -k gui/$(id -u)/com.shurli.daemon  # macOS

# Check it came up clean
shurli status
```

No `sudo cp` needed. The symlink points to the binary in your repo, so rebuilding in place is enough.

---

## Uninstall

### Linux

```bash
sudo systemctl stop shurli-daemon
sudo systemctl disable shurli-daemon
sudo rm /etc/systemd/system/shurli-daemon.service
sudo systemctl daemon-reload
```

Optionally remove the service user and config:

```bash
sudo userdel -r shurli    # removes user + home directory (including config)
```

Or keep the config and just remove the user:

```bash
sudo userdel shurli
# Config remains at /home/shurli/.config/shurli/
```

### macOS

```bash
launchctl unload ~/Library/LaunchAgents/com.shurli.daemon.plist
rm ~/Library/LaunchAgents/com.shurli.daemon.plist
```

Optionally remove config and logs:

```bash
rm -rf ~/.config/shurli
rm /tmp/shurli-daemon.log
```

---

## Troubleshooting

| Issue | Cause | Solution |
|-------|-------|----------|
| Service fails to start | Binary not found | Verify `/usr/local/bin/shurli` exists and is executable |
| "Permission denied" on config | Wrong ownership | `sudo chown -R shurli:shurli /home/shurli/.config/shurli` |
| Watchdog keeps restarting | Daemon hangs or crashes | `sudo journalctl -u shurli-daemon -n 50` for error logs |
| Socket permission denied | Different user trying to access | `shurli status` must run as the same user that owns the socket |
| launchctl: "service already loaded" | Plist already active | `launchctl unload` first, then `launchctl load` |
| macOS logs empty | Daemon not starting | Check `launchctl list \| grep shurli` for exit code; non-zero means crash |
| High memory usage | Many peer connections | `MemoryMax=512M` in systemd caps it; check peer count with `shurli status` |
| "Config not found" after install | `shurli init` not run as service user | `sudo -u shurli shurli init` |
| Logs filling disk (Linux) | No journal rotation | See below |

### Log rotation (Linux)

Limit journal size for the daemon:

```bash
sudo mkdir -p /etc/systemd/journald.conf.d
echo -e "[Journal]\nSystemMaxUse=500M\nMaxFileSec=7day" | sudo tee /etc/systemd/journald.conf.d/shurli-daemon.conf
sudo systemctl restart systemd-journald
```

### Log rotation (macOS)

The plist logs to `/tmp/shurli-daemon.log`. macOS clears `/tmp` on reboot. For long-running systems, set up periodic cleanup:

```bash
# Add to crontab (keeps last 10000 lines)
echo "0 4 * * * tail -10000 /tmp/shurli-daemon.log > /tmp/shurli-daemon.log.tmp && mv /tmp/shurli-daemon.log.tmp /tmp/shurli-daemon.log" | crontab -
```

---

## File Layout After Setup

### Linux

```
/etc/systemd/system/shurli-daemon.service   # service unit
/usr/local/bin/shurli                        # binary (or symlink)
/home/shurli/.config/shurli/
    config.yaml                              # daemon config
    node.key                                 # Ed25519 identity key
    authorized_keys                          # peer allowlist
    shurli.sock                              # Unix domain socket (runtime)
    .daemon-cookie                           # cookie auth token (runtime)
```

### macOS

```
~/Library/LaunchAgents/com.shurli.daemon.plist   # launchd plist
/usr/local/bin/shurli                            # binary (or symlink)
~/.config/shurli/
    config.yaml
    node.key
    authorized_keys
    shurli.sock
    .daemon-cookie
/tmp/shurli-daemon.log                           # daemon log output
```
