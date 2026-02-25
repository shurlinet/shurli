# Testing Guide: SSH Access via P2P Network

This guide walks through testing the complete Shurli system with SSH service exposure.

## Goal

Connect to your home computer's SSH server from a client device (laptop/phone) through the P2P network, traversing CGNAT/NAT using a relay server.

```
[Client]  ──shurli proxy──▶  [Relay Server]  ◀──shurli daemon──  [Home Server]  ──TCP──▶  [SSH :22]
 (Laptop)                       (VPS)                         (Behind CGNAT)
```

## Prerequisites

### 1. Three Machines/Terminals

- **Relay Server**: VPS with public IP (Linode, DigitalOcean, AWS, etc.)
- **Home Server**: Your home computer behind CGNAT/NAT (runs `shurli daemon`)
- **Client**: Laptop or another device (runs `shurli proxy`)

### 2. SSH Server Running

On your home computer:
```bash
# Check if SSH server is running
sudo systemctl status sshd  # or ssh on macOS

# Start if not running (Linux)
sudo systemctl start sshd

# macOS - enable in System Preferences > Sharing > Remote Login
```

### 3. Build shurli

```bash
# Build shurli (single binary - handles both client and relay server)
go build -ldflags="-s -w" -trimpath -o shurli ./cmd/shurli
```

---

## Step 1: Deploy Relay Server

See [relay-server/README.md](../relay-server/README.md) for the full VPS setup guide.

Quick version:

```bash
cd relay-server
cp ../configs/relay-server.sample.yaml relay-server.yaml
# Edit relay-server.yaml if needed (defaults are fine)

# Build from project root
cd ..
go build -ldflags="-s -w" -trimpath -o relay-server/shurli ./cmd/shurli
cd relay-server && ./shurli relay serve
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
./shurli init
```

The wizard will:
1. Create `~/.config/shurli/` directory
2. Ask for your relay server address (accepts flexible formats):
   - Full multiaddr: `/ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...`
   - IP and port: `1.2.3.4:7777` (then prompts for peer ID)
   - Bare IP: `1.2.3.4` (uses default port 7777, then prompts for peer ID)
   - IPv6: `[2600:3c00::1]:7777` or `[2600:3c00::1]`
3. Generate an Ed25519 identity key
4. Display your **Peer ID** as text + QR code (share with peers)
5. Write `config.yaml`, `identity.key`, and `authorized_keys`

**Tip**: Check your peer ID anytime with `./shurli whoami`

### Configure services

Add services via CLI (preferred) or by editing the config file:

```bash
# Add via CLI
./shurli service add ssh localhost:22
./shurli service add xrdp localhost:3389

# Or edit ~/.config/shurli/config.yaml directly
```

Ensure `force_private_reachability` is set for CGNAT:

```yaml
network:
  force_private_reachability: true  # CRITICAL for CGNAT (Starlink, etc.)
```

### Start the server

```bash
./shurli daemon
```

**Expected output:**
```
Loaded configuration from ~/.config/shurli/config.yaml
🏠 Peer ID: 12D3KooWHOME...ABC
✅ Connected to relay 12D3KooWABC...
✅ Relay address: /ip4/YOUR_VPS_IP/tcp/7777/p2p/12D3KooWABC.../p2p-circuit/p2p/12D3KooWHOME...ABC
✅ Registered service: ssh (protocol: /shurli/ssh/1.0.0, local: localhost:22)
```

**Save the Home Server Peer ID**: `12D3KooWHOME...ABC`

---

## Step 3: Set Up Client

### Run the setup wizard

```bash
./shurli init
```

### Authorize peers

**Option A: Invite/Join flow (recommended - handles both sides automatically)**

On the home server:
```bash
./shurli invite --name home
# Displays an invite code + QR code. Share the code with the client.
```

On the client:
```bash
./shurli join <invite-code> --name laptop
# Automatically: connects to inviter, exchanges peer IDs,
# adds each other to authorized_keys, adds name mapping.
```

**Option B: CLI commands**

On the client, add the home server's peer ID:
```bash
./shurli auth add 12D3KooWHOME...ABC --comment "home-server"
```

Do the same on the home server - add the client's peer ID:
```bash
./shurli auth add 12D3KooWCLIENT...XYZ --comment "laptop"
```

Verify with:
```bash
./shurli auth list
```

**Option C: Manual file edit**
```bash
# Edit ~/.config/shurli/authorized_keys
# Add the peer ID (one per line):
12D3KooWHOME...ABC  # home-server
```

### Add friendly name

If you used the invite/join flow, names are added automatically. Otherwise, edit `~/.config/shurli/config.yaml` on the client:

```yaml
# Map friendly names to peer IDs:
names:
  home: "12D3KooWHOME...ABC"  # From Step 2
```

---

## Step 4: Test SSH Connection via P2P

### Test connectivity first

```bash
./shurli ping home
```

You should see a successful ping/pong response.

### Start the SSH proxy

```bash
./shurli proxy home ssh 2222
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

Restart `shurli daemon`, then on the client:

```bash
./shurli proxy home xrdp 13389
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
./shurli proxy home web 8080
# Then: curl http://localhost:8080
```

---

## Managing Relay Addresses

After initial setup, you can add or remove relay servers:

```bash
# Add a relay (flexible formats)
./shurli relay add 1.2.3.4 --peer-id 12D3KooW...
./shurli relay add 1.2.3.4:7777 --peer-id 12D3KooW...
./shurli relay add /ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...

# List configured relays
./shurli relay list

# Remove a relay
./shurli relay remove /ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...
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
- Ensure SSH protocol ID matches: `/shurli/ssh/1.0.0`

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
- Or use the full peer ID directly: `shurli proxy 12D3KooW... ssh 2222`

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
- [ ] `shurli ping home` succeeds from client
- [ ] `shurli proxy home ssh 2222` creates local listener
- [ ] `ssh -p 2222 user@localhost` connects to home computer
- [ ] XRDP / other TCP services also work

---

## Unit Tests

Shurli has automated unit tests for core packages. These run in CI (GitHub Actions) on every push.

### Running Tests

All packages are in a single Go module. Run everything from the project root:

```bash
# Run all tests with race detection (same as CI)
go test -race -count=1 ./...

# Run tests for a specific package
go test -race ./internal/config/
go test -race ./internal/auth/
go test -race ./internal/invite/
go test -race ./cmd/shurli/

# Verbose output (see individual test names)
go test -race -v ./internal/auth/
```

### Test Coverage

| Package | Tests | What's covered |
|---------|-------|---------------|
| `internal/config` | `loader_test.go` | Config loading, YAML parsing, validation (all config types), path resolution, config version handling, FindConfigFile discovery |
| `internal/config` | `archive_test.go` | Archive path derivation, archive/rollback round-trip, permissions (0600), overwrite semantics, no temp file leaks, ErrNoArchive sentinel |
| `internal/config` | `confirm_test.go` | Begin/confirm lifecycle, duplicate prevention (ErrCommitConfirmedPending), ErrNoPending, ApplyCommitConfirmed file swap, EnforceCommitConfirmed timeout revert, context cancellation, expired deadline handling |
| `internal/watchdog` | `watchdog_test.go` | Health check loop execution, unhealthy check logging, context cancellation, default interval, sd_notify no-op without NOTIFY_SOCKET, sd_notify error on bad socket |
| `internal/auth` | `gater_test.go` | ConnectionGater: inbound/outbound filtering, peer authorization, hot-reload |
| `internal/auth` | `authorized_keys_test.go` | File loading, comment handling, invalid peer IDs, missing files |
| `internal/auth` | `manage_test.go` | AddPeer (with duplicate/sanitize), RemovePeer (atomic write, preserves comments), ListPeers |
| `internal/identity` | `identity_test.go` | Key creation, persistence, file permissions, PeerIDFromKeyFile |
| `internal/validate` | `service_test.go` | Service name validation (valid/invalid cases, max length) |
| `internal/invite` | `code_test.go` | Encode/decode round-trip, invalid codes, trailing junk rejection |
| `cmd/shurli` | `relay_input_test.go` | Relay address parsing (IPv4, IPv6, multiaddr detection, port validation) |
| `pkg/p2pnet` | `integration_test.go` | In-process libp2p host-to-host streaming, half-close semantics, P2P-to-TCP proxy, DialWithRetry retry/backoff, UserAgent exchange via Identify protocol |
| `pkg/p2pnet` | `interfaces_test.go` | Interface discovery, IPv6/IPv4 classification, global unicast detection |
| `pkg/p2pnet` | `pathdialer_test.go` | Parallel dial racing, already-connected fast path, path type classification |
| `pkg/p2pnet` | `pathtracker_test.go` | Path quality tracking, event-bus subscription, per-peer path info |
| `pkg/p2pnet` | `netmonitor_test.go` | Network change monitoring, interface diff detection, callback firing |
| `pkg/p2pnet` | `stunprober_test.go` | STUN probing, NAT type classification, multi-server concurrent probing |
| `pkg/p2pnet` | `peerrelay_test.go` | Peer relay auto-enable/disable, global IP detection, resource limits |

---

## Benchmarks

Performance benchmarks establish baselines for hot-path and cold-path functions.

### Running Benchmarks

```bash
# Run all benchmarks with memory stats
go test -bench=. -benchmem ./internal/auth/
go test -bench=. -benchmem ./internal/invite/
go test -bench=. -benchmem ./internal/config/
go test -bench=. -benchmem ./pkg/p2pnet/

# For statistical comparison (3+ runs recommended)
go test -bench=. -benchmem -count=3 ./internal/auth/

# Compare before/after with benchstat
go install golang.org/x/perf/cmd/benchstat@latest
go test -bench=. -benchmem -count=5 ./internal/auth/ > old.txt
# (make changes)
go test -bench=. -benchmem -count=5 ./internal/auth/ > new.txt
benchstat old.txt new.txt
```

### Benchmark Coverage

| File | Benchmarks | Path Type | What's Measured |
|------|-----------|-----------|-----------------|
| `internal/auth/gater_bench_test.go` | `InterceptSecuredAllowed`, `InterceptSecuredDenied`, `IsAuthorized` | Hot (per-connection) | RWMutex + map lookup latency |
| `internal/auth/authorized_keys_bench_test.go` | `LoadAuthorizedKeys5`, `LoadAuthorizedKeys50` | Cold (startup/reload) | File parse + peer ID decode |
| `internal/invite/code_bench_test.go` | `Encode`, `Decode` | Mixed (per-invite) | Base32 + multihash + multiaddr ops |
| `internal/config/loader_bench_test.go` | `LoadNodeConfig`, `ValidateNodeConfig` | Cold (startup) | YAML parse, validation |
| `pkg/p2pnet/naming_bench_test.go` | `ResolveByName`, `ResolveByPeerID` | Hot (per-proxy) | Map lookup vs peer.Decode fallback |

---

### Coverage-Instrumented Docker Tests

Docker integration tests exercise the actual compiled binary end-to-end (relay server, invite/join flow, ping through circuit relay). The binary is built with `go build -cover`, so coverage data is captured when processes exit.

```bash
# Run Docker tests with coverage collection
mkdir -p coverage/integration
SHURLI_COVDIR="$PWD/coverage/integration" \
  go test -tags integration -v -timeout 5m ./test/docker/

# Run unit tests with binary-format coverage (for merging)
mkdir -p coverage/unit
go test -cover ./... -args -test.gocoverdir="$PWD/coverage/unit"

# Merge unit + Docker coverage
mkdir -p coverage/merged
go tool covdata merge -i=coverage/unit,coverage/integration -o=coverage/merged

# Generate combined report
go tool covdata textfmt -i=coverage/merged -o=coverage/combined.out
go tool cover -func=coverage/combined.out | tail -1

# HTML visualization
go tool cover -html=coverage/combined.out -o=coverage/report.html
```

This captures code paths that unit tests cannot reach: `runRelayServe`, `runDaemon`, `runInvite`, `runJoin`, `runPing` through real P2P circuits.

### CI Pipeline

GitHub Actions runs on every push to `main` and `dev/next-iteration`. All commands run from the project root against the single Go module:

1. **Build** - all packages compile (`go build ./...`)
2. **Vet** - static analysis (`go vet ./...`)
3. **Test** - all tests with race detection (`go test -race -count=1 ./...`)
4. **Coverage** - unit + Docker integration coverage merged and reported

Config: [`.github/workflows/ci.yml`](../.github/workflows/ci.yml)

---

## Logging in Tests

Library code uses `log/slog` for structured logging. In tests, slog output goes to stderr by default, which `go test` captures and only shows on failure. No special test configuration is needed.

For benchmarks that previously used `log.New(io.Discard, ...)` to suppress logging, slog's default handler is used instead - the small overhead is part of the realistic benchmark measurement.

---

**Last Updated**: 2026-02-22
