# Daemon API Reference

The peer-up daemon (`peerup daemon`) runs a long-lived P2P host with a Unix domain socket HTTP API for programmatic control.

## Table of Contents

- [Architecture](#architecture)
- [Authentication](#authentication)
- [Response Format](#response-format)
- [Endpoints](#endpoints)
  - [GET /v1/status](#get-v1status)
  - [GET /v1/services](#get-v1services)
  - [GET /v1/peers](#get-v1peers)
  - [GET /v1/auth](#get-v1auth)
  - [POST /v1/auth](#post-v1auth)
  - [DELETE /v1/auth/{peer_id}](#delete-v1authpeer_id)
  - [POST /v1/ping](#post-v1ping)
  - [POST /v1/traceroute](#post-v1traceroute)
  - [POST /v1/resolve](#post-v1resolve)
  - [POST /v1/connect](#post-v1connect)
  - [DELETE /v1/connect/{id}](#delete-v1connectid)
  - [POST /v1/expose](#post-v1expose)
  - [DELETE /v1/expose/{name}](#delete-v1exposename)
  - [POST /v1/shutdown](#post-v1shutdown)
- [Error Codes](#error-codes)
- [CLI Usage](#cli-usage)
- [Integration Examples](#integration-examples)
- [Socket Lifecycle](#socket-lifecycle)

---

## Architecture

The daemon runs the full P2P lifecycle (relay connection, DHT bootstrap, service exposure, watchdog) plus an HTTP server on a Unix socket.

![Daemon architecture: P2P Runtime (relay, DHT, services, watchdog) connected bidirectionally to Unix Socket API (HTTP/1.1, cookie auth, 14 endpoints), with P2P Network below left and CLI/Scripts below right](images/daemon-api-architecture.svg)

**Default paths**:
- Socket: `~/.config/peerup/peerup.sock` (permissions `0600`)
- Cookie: `~/.config/peerup/.daemon-cookie` (permissions `0600`)

---

## Authentication

The daemon uses cookie-based authentication (same pattern as Bitcoin Core, Docker, containerd).

### How It Works

1. On startup, the daemon generates a 32-byte random hex token
2. Token is written to `~/.config/peerup/.daemon-cookie` with `0600` permissions
3. Every API request must include `Authorization: Bearer <token>` header
4. Token is validated on every request — `401 Unauthorized` if missing or wrong
5. Cookie file is deleted on clean shutdown
6. Token rotates on every daemon restart (limits exposure window)

### Why Cookie Over Config-Based Password

- No plaintext passwords in config files
- Token rotates every daemon restart
- Same-user access only (cookie file is `0600`)
- Proven pattern used by Bitcoin Core, Docker, containerd

### Example

```bash
curl -H "Authorization: Bearer $(cat ~/.config/peerup/.daemon-cookie)" \
     --unix-socket ~/.config/peerup/peerup.sock \
     http://localhost/v1/status
```

The CLI client (`peerup daemon status`, etc.) reads the cookie file automatically — no manual auth needed.

> **Tip**: All curl examples in this document use inline `$(cat ~/.config/peerup/.daemon-cookie)` so they work as-is when copy-pasted. For scripts that make multiple API calls, read the token once into a variable — see [Integration Examples](#integration-examples).

### Unauthorized Response

```json
{
  "error": "unauthorized: invalid or missing auth token"
}
```

HTTP status: `401 Unauthorized`

---

## Response Format

Every endpoint supports two output formats:

### JSON (Default)

Success responses are wrapped in a `data` envelope:

```json
{"data": { ... }}
```

Error responses use an `error` envelope:

```json
{"error": "description of what went wrong"}
```

### Plain Text

Request plain text via:
- Query parameter: `?format=text`
- Accept header: `Accept: text/plain`

Plain text responses are single-line or tabular, designed for `grep`/`awk`/`cut`.

### CLI Format Selection

```bash
peerup daemon status          # human-readable text
peerup daemon status --json   # raw JSON
```

---

## Endpoints

### GET /v1/status

Returns daemon status: peer ID, version, uptime, connected peers, addresses, services count.

**Response (JSON)**:

```json
{
  "data": {
    "peer_id": "12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt",
    "version": "0.1.0",
    "uptime_seconds": 3600,
    "connected_peers": 2,
    "listen_addresses": [
      "/ip4/10.0.1.50/tcp/9000",
      "/ip4/10.0.1.50/udp/9000/quic-v1"
    ],
    "relay_addresses": [
      "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWK.../p2p-circuit"
    ],
    "services_count": 2
  }
}
```

**Response (Text)**:

```
peer_id: 12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt
version: 0.1.0
uptime: 3600s
connected_peers: 2
services: 2
listen_addresses: 2
  /ip4/10.0.1.50/tcp/9000
  /ip4/10.0.1.50/udp/9000/quic-v1
relay_addresses: 1
  /ip4/203.0.113.50/tcp/7777/p2p/12D3KooWK.../p2p-circuit
```

**curl**:

```bash
curl -H "Authorization: Bearer $(cat ~/.config/peerup/.daemon-cookie)" \
     --unix-socket ~/.config/peerup/peerup.sock \
     http://localhost/v1/status
```

---

### GET /v1/services

Lists all registered services.

**Response (JSON)**:

```json
{
  "data": [
    {
      "name": "ssh",
      "protocol": "/peerup/ssh/1.0.0",
      "local_address": "localhost:22",
      "enabled": true
    },
    {
      "name": "ollama",
      "protocol": "/peerup/ollama/1.0.0",
      "local_address": "localhost:11434",
      "enabled": true
    }
  ]
}
```

**Response (Text)** (tab-separated):

```
ssh	localhost:22	/peerup/ssh/1.0.0	enabled
ollama	localhost:11434	/peerup/ollama/1.0.0	enabled
```

---

### GET /v1/peers

Lists connected peers with their addresses and software version.

**By default, only peerup and relay-server peers are shown.** Peer-up uses a private Kademlia DHT (`/peerup/kad/1.0.0`), isolated from the public IPFS Amino network. Your node only communicates with other peer-up nodes for DHT peer discovery.

To see all connected peers (including DHT neighbors), add `?all=true`:

```
GET /v1/peers           → only peerup/relay-server peers
GET /v1/peers?all=true  → all connected peers (including DHT neighbors)
```

**CLI**:

```bash
peerup daemon peers          # only peerup peers
peerup daemon peers --all    # all peers including DHT neighbors
```

**Response (JSON)**:

```json
{
  "data": [
    {
      "id": "12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6",
      "addresses": [
        "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWK.../p2p-circuit/p2p/12D3KooWH..."
      ],
      "agent_version": "peerup/0.1.0"
    }
  ]
}
```

**Response (Text)**:

```
12D3KooWHoy98z8...	peerup/0.1.0	3 addrs
```

---

### GET /v1/auth

Lists authorized peers from the `authorized_keys` file.

**Response (JSON)**:

```json
{
  "data": [
    {
      "peer_id": "12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6",
      "comment": "laptop"
    }
  ]
}
```

**Response (Text)**:

```
12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6	# laptop
```

---

### POST /v1/auth

Adds a peer to `authorized_keys` and hot-reloads the connection gater. Takes effect immediately — no restart needed.

**Request Body**:

```json
{
  "peer_id": "12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6",
  "comment": "laptop"
}
```

**Response (JSON)**:

```json
{
  "data": {
    "status": "added"
  }
}
```

---

### DELETE /v1/auth/{peer_id}

Removes a peer from `authorized_keys` and hot-reloads the connection gater. Access revoked immediately.

**Response (JSON)**:

```json
{
  "data": {
    "status": "removed"
  }
}
```

**curl**:

```bash
curl -X DELETE \
     -H "Authorization: Bearer $(cat ~/.config/peerup/.daemon-cookie)" \
     --unix-socket ~/.config/peerup/peerup.sock \
     http://localhost/v1/auth/12D3KooWNq8c1fNjXwhRoWxSXT419bumWQFoTbowCwHEa96RJRg6
```

---

### POST /v1/ping

Pings a peer using the P2P ping-pong protocol. Returns per-ping results and summary statistics.

**Request Body**:

```json
{
  "peer": "home-server",
  "count": 4,
  "interval_ms": 1000
}
```

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `peer` | string | required | Peer name or ID |
| `count` | int | 4 | Number of pings (API defaults to 4) |
| `interval_ms` | int | 1000 | Milliseconds between pings |

**Response (JSON)**:

```json
{
  "data": {
    "results": [
      {"seq": 1, "peer_id": "12D3KooWDRDM...", "rtt_ms": 45.2, "path": "RELAYED"},
      {"seq": 2, "peer_id": "12D3KooWDRDM...", "rtt_ms": 42.1, "path": "DIRECT"},
      {"seq": 3, "peer_id": "12D3KooWDRDM...", "rtt_ms": 43.0, "path": "DIRECT"},
      {"seq": 4, "peer_id": "12D3KooWDRDM...", "rtt_ms": 41.8, "path": "DIRECT"}
    ],
    "stats": {
      "sent": 4,
      "received": 4,
      "lost": 0,
      "loss_pct": 0.0,
      "min_ms": 41.8,
      "avg_ms": 43.0,
      "max_ms": 45.2
    }
  }
}
```

**Response (Text)**:

```
PING home-server (12D3KooWDRDMuwQV...):
seq=1 rtt=45.2ms path=[RELAYED]
seq=2 rtt=42.1ms path=[DIRECT]
seq=3 rtt=43.0ms path=[DIRECT]
seq=4 rtt=41.8ms path=[DIRECT]
--- home-server ping statistics ---
4 sent, 4 received, 0% loss, rtt min/avg/max = 41.8/43.0/45.2 ms
```

---

### POST /v1/traceroute

Traces the network path to a peer. Shows whether the connection is direct or relayed, with per-hop latency.

**Request Body**:

```json
{
  "peer": "home-server"
}
```

**Response (JSON)**:

```json
{
  "data": {
    "target": "home-server",
    "target_peer_id": "12D3KooWDRDM...",
    "path": "RELAYED via relay-server/0.1.0",
    "hops": [
      {
        "hop": 1,
        "peer_id": "12D3KooWK...",
        "name": "relay",
        "address": "203.0.113.50:7777",
        "rtt_ms": 23.0
      },
      {
        "hop": 2,
        "peer_id": "12D3KooWDRDM...",
        "name": "home-server",
        "address": "via relay",
        "rtt_ms": 45.0
      }
    ]
  }
}
```

**Response (Text)**:

```
traceroute to home-server (12D3KooWDRDMuwQV...):
 1  12D3KooWK...  (relay)  203.0.113.50:7777  23.0ms
 2  12D3KooWDRDM...  (home-server)  via relay  45.0ms
--- path: [RELAYED via relay-server/0.1.0] ---
```

---

### POST /v1/resolve

Resolves a peer name to its peer ID. Shows the resolution source.

**Request Body**:

```json
{
  "name": "home-server"
}
```

**Response (JSON)**:

```json
{
  "data": {
    "name": "home-server",
    "peer_id": "12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt",
    "source": "local_config"
  }
}
```

| Source | Meaning |
|--------|---------|
| `local_config` | Resolved from `names:` section in config |
| `peer_id` | Input was already a valid peer ID |

**Response (Text)**:

```
home-server → 12D3KooWPrmh163sTHW3mYQm7YsLsSR2wr71fPp4g6yjuGv3sGQt (source: local_config)
```

---

### POST /v1/connect

Creates a dynamic TCP proxy to a peer's service. Returns a proxy ID and the local listen address.

**Request Body**:

```json
{
  "peer": "home-server",
  "service": "ssh",
  "listen": "127.0.0.1:2222"
}
```

| Field | Type | Description |
|-------|------|-------------|
| `peer` | string | Peer name or ID |
| `service` | string | Service name to connect to |
| `listen` | string | Local address:port to listen on |

**Response (JSON)**:

```json
{
  "data": {
    "id": "proxy-1",
    "listen_address": "127.0.0.1:2222"
  }
}
```

After this call, `ssh user@127.0.0.1 -p 2222` connects to the remote peer's SSH service through the P2P tunnel.

---

### DELETE /v1/connect/{id}

Tears down an active proxy by ID.

**Response (JSON)**:

```json
{
  "data": {
    "status": "disconnected"
  }
}
```

**curl**:

```bash
curl -X DELETE \
     -H "Authorization: Bearer $(cat ~/.config/peerup/.daemon-cookie)" \
     --unix-socket ~/.config/peerup/peerup.sock \
     http://localhost/v1/connect/proxy-1
```

---

### POST /v1/expose

Dynamically registers a service on the P2P host. Other peers can connect to it immediately.

**Request Body**:

```json
{
  "name": "jupyter",
  "local_address": "localhost:8888"
}
```

**Response (JSON)**:

```json
{
  "data": {
    "status": "exposed"
  }
}
```

---

### DELETE /v1/expose/{name}

Unregisters a service from the P2P host.

**Response (JSON)**:

```json
{
  "data": {
    "status": "unexposed"
  }
}
```

---

### POST /v1/shutdown

Requests a graceful shutdown of the daemon. The daemon closes all active proxies, shuts down the HTTP server, removes the socket and cookie files, then exits.

**Response (JSON)**:

```json
{
  "data": {
    "status": "shutting down"
  }
}
```

---

## Error Codes

| HTTP Status | Meaning |
|-------------|---------|
| `200` | Success |
| `400` | Bad request (missing/invalid fields) |
| `401` | Unauthorized (missing/wrong auth token) |
| `404` | Not found (unknown proxy ID, unresolvable name) |
| `500` | Internal error (file I/O failure, network error) |

All error responses use the envelope:

```json
{
  "error": "description of what went wrong"
}
```

### Sentinel Errors

| Error | Trigger |
|-------|---------|
| `daemon already running` | Socket is in use by another daemon instance |
| `daemon not running` | Socket file doesn't exist (client can't connect) |
| `proxy not found` | Disconnect called with unknown proxy ID |
| `unauthorized` | Missing or invalid auth token |

---

## CLI Usage

The CLI communicates with the daemon over the Unix socket. It reads the cookie file automatically.

### Starting the Daemon

```bash
peerup daemon              # Start daemon (foreground)
peerup daemon start        # Same as above
```

### Querying the Daemon

```bash
peerup daemon status               # Human-readable status
peerup daemon status --json        # JSON output
peerup daemon services             # List services
peerup daemon services --json
peerup daemon peers                # List connected peers
peerup daemon peers --json
```

### Network Diagnostics (via daemon)

```bash
peerup daemon ping home-server                 # 4 pings via daemon
peerup daemon ping home-server -c 10           # 10 pings
peerup daemon ping home-server --json          # JSON output
```

### Dynamic Proxy Management

```bash
# Create a proxy
peerup daemon connect --peer home-server --service ssh --listen 127.0.0.1:2222

# Use it
ssh user@127.0.0.1 -p 2222

# Tear it down
peerup daemon disconnect proxy-1
```

### Stopping the Daemon

```bash
peerup daemon stop          # Graceful shutdown via API
```

---

## Integration Examples

### Bash Script

```bash
#!/bin/bash
SOCKET=~/.config/peerup/peerup.sock
TOKEN=$(cat ~/.config/peerup/.daemon-cookie)

# Check if daemon is running
if [ ! -S "$SOCKET" ]; then
    echo "Daemon not running"
    exit 1
fi

# Get peer count
PEERS=$(curl -s -H "Authorization: Bearer $TOKEN" \
    --unix-socket "$SOCKET" \
    http://localhost/v1/status | jq '.data.connected_peers')

echo "Connected peers: $PEERS"

# Create SSH proxy to home server
PROXY=$(curl -s -X POST -H "Authorization: Bearer $TOKEN" \
    -d '{"peer":"home-server","service":"ssh","listen":"127.0.0.1:2222"}' \
    --unix-socket "$SOCKET" \
    http://localhost/v1/connect)

echo "Proxy: $(echo $PROXY | jq -r '.data.id')"
echo "Listen: $(echo $PROXY | jq -r '.data.listen_address')"
```

### Python (direct socket)

```python
import http.client
import json
import socket

SOCKET_PATH = os.path.expanduser("~/.config/peerup/peerup.sock")
COOKIE_PATH = os.path.expanduser("~/.config/peerup/.daemon-cookie")

# Read auth token
with open(COOKIE_PATH) as f:
    token = f.read().strip()

# Connect over Unix socket
conn = http.client.HTTPConnection("localhost")
conn.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
conn.sock.connect(SOCKET_PATH)

# Query status
conn.request("GET", "/v1/status", headers={
    "Authorization": f"Bearer {token}"
})
resp = conn.getresponse()
data = json.loads(resp.read())
print(f"Peer ID: {data['data']['peer_id']}")
print(f"Peers: {data['data']['connected_peers']}")
```

---

## Socket Lifecycle

### Startup

1. Generate 32-byte random hex token
2. Write token to `~/.config/peerup/.daemon-cookie` (`0600`)
3. Check for stale socket — dial the existing socket:
   - Connection succeeds → another daemon is alive → return `ErrDaemonAlreadyRunning`
   - Connection fails → stale socket → remove it and proceed
4. Create Unix socket at `~/.config/peerup/peerup.sock`
5. Set socket permissions to `0600`
6. Start HTTP server on the socket

### Stale Socket Detection

No PID files. The daemon dials the existing socket to determine if a daemon is alive:

- If the dial succeeds, another daemon is running — refuse to start.
- If the dial fails, the socket is stale (leftover from a crash) — remove it and start fresh.

This is more reliable than PID files, which can be stale themselves.

### Shutdown

1. HTTP server shutdown with 3s grace period
2. All active proxies cancelled and awaited
3. Socket file removed
4. Cookie file removed

---

**Last Updated**: 2026-02-20
