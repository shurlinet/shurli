# Troubleshooting

## Diagnostics

Run the built-in diagnostic tool:
```bash
shurli doctor        # Check installation health
shurli doctor --fix  # Auto-fix common issues
```

## Common Issues

| Issue | Solution |
|-------|----------|
| `no config file found` | Run `shurli init` or use `--config <path>` |
| `Cannot resolve target` | Add name mapping to `names:` in config |
| `DENIED inbound connection` | Add peer ID to `authorized_keys`, restart daemon |
| `Invalid invite code` | Paste the full code as one argument (quote if spaces) |
| `Failed to connect to inviter` | Ensure `shurli invite` is still running |
| No `/p2p-circuit` addresses | Check `force_private_reachability: true` and relay address |
| `protocols not supported` | Relay server not running or unreachable |
| Bad config edit broke startup | `shurli config rollback` restores last-known-good |
| Remote config change went wrong | `shurli config apply new.yaml --confirm-timeout 5m`, then `config confirm` |
| `failed to sufficiently increase receive buffer size` | QUIC works but suboptimal - see UDP buffer tuning below |
| Daemon won't start (socket exists) | Stale socket from crash - daemon auto-detects and cleans up |

## UDP Buffer Tuning (QUIC)

QUIC works with default buffers but performs better with increased limits:

```bash
# Linux (persistent)
echo "net.core.rmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
echo "net.core.wmem_max=7500000" | sudo tee -a /etc/sysctl.d/99-quic.conf
sudo sysctl --system
```

## Build Issues

### Linux: `dns_sd.h: No such file or directory`

The native mDNS module requires the Avahi compatibility library:

```bash
# Debian / Ubuntu
sudo apt install libavahi-compat-libdnssd-dev

# Fedora / RHEL
sudo dnf install avahi-compat-libdns_sd-devel

# Arch
sudo pacman -S avahi
```
