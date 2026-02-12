# libp2p Ping-Pong: Phone-to-Home-Computer via Relay

A working example of connecting an iPhone to a home computer behind Starlink CGNAT using [libp2p](https://libp2p.io/). The system uses a lightweight relay server on a VPS to bridge the connection, with peer discovery via the public libp2p DHT.

## The Problem

Starlink uses Carrier-Grade NAT (CGNAT) on IPv4, meaning your home computer has no directly reachable public IP. Inbound IPv6 connections are also blocked by Starlink's router firewall. This makes it impossible for a phone on cellular data to directly connect to a home machine.

## The Solution

Three components work together to establish connectivity:

```
┌──────────┐         ┌──────────────┐         ┌──────────────┐
│  Client   │───────▶│ Relay Server │◀────────│  Home Node   │
│  (Phone)  │ outbound    (VPS)   outbound   │  (Linux/Mac) │
└──────────┘         └──────────────┘         └──────────────┘
                           │
                     Both sides connect
                     OUTBOUND to relay.
                     No inbound ports needed.
```

1. **Relay Server** — Runs on a cheap VPS ($5/month Linode Nanode) with a public IP. Acts as a meeting point. Does not store data, only forwards streams between connected peers.
2. **Home Node** — Runs on your home Linux or macOS machine. Connects outbound to the relay, registers a reservation, then listens for incoming ping messages.
3. **Client Node** — Runs on a phone or laptop. Connects outbound to the relay, discovers the home node via the DHT, and sends ping messages through the relay circuit.

## Architecture

### Peer Discovery

The home node advertises itself on the [libp2p Kademlia DHT](https://docs.libp2p.io/concepts/fundamentals/protocols/#kad-dht) using a rendezvous string (`khoji-pingpong-demo`). The client node searches for this rendezvous string to find the home node's Peer ID and relay circuit addresses.

### Relay Circuit (Circuit Relay v2)

Since direct connections are blocked by CGNAT/firewall, the system uses [libp2p Circuit Relay v2](https://docs.libp2p.io/concepts/nat/circuit-relay/):

1. Home node connects outbound to the relay and makes a **reservation** (tells the relay "I'm reachable through you").
2. Client node connects outbound to the relay.
3. Client dials the home node using a **circuit address** like:
   ```
   /ip4/<RELAY_IP>/tcp/7777/p2p/<RELAY_PEER_ID>/p2p-circuit/p2p/<HOME_PEER_ID>
   ```
4. The relay bridges the two connections. Both sides only made outbound connections.

### Hole-Punching (DCUtR)

The home node enables [DCUtR](https://docs.libp2p.io/concepts/nat/hole-punching/) (Direct Connection Upgrade through Relay). After the initial relay connection is established, libp2p attempts to hole-punch a direct peer-to-peer connection. If successful, subsequent data flows directly without the relay — critical for large file transfers.

### Persistent Identity

The home node saves its Ed25519 keypair to `home_node.key` so its Peer ID remains stable across restarts. The relay server similarly saves to `relay_node.key`. The client generates a new ephemeral identity each run.

## Prerequisites

- **Go 1.22+** on all machines
- A **VPS with a public IP** for the relay server (Linode Nanode 1GB at $5/month works)
- Home machine running **Linux or macOS**

## Project Structure

```
├── relay-server/             # Runs on VPS
│   ├── main.go               # Minimal relay — no DHT, private
│   ├── go.mod
│   ├── setup-linode.sh       # VPS provisioning script
│   └── relay-server.service  # systemd unit file
├── home-node/                # Runs on your home computer
│   ├── main.go               # Pong responder with relay reservation
│   └── go.mod
└── client-node/              # Runs on phone/laptop
    ├── main.go               # Ping sender with DHT discovery
    └── go.mod
```

## Setup

### Step 1: Deploy the Relay Server

SSH into your VPS and run the setup script:

```bash
chmod +x relay-server/setup-linode.sh
./relay-server/setup-linode.sh
```

Or manually:

```bash
# Install Go
wget -q https://go.dev/dl/go1.22.5.linux-amd64.tar.gz
sudo tar -C /usr/local -xzf go1.22.5.linux-amd64.tar.gz
export PATH=$PATH:/usr/local/go/bin

# Open firewall
sudo ufw allow 7777/tcp

# Build and run
cd relay-server
go mod tidy
go build -o relay-server .
./relay-server
```

Output:

```
=== Private libp2p Relay Server ===

🔄 Relay Peer ID: 12D3KooW...

Multiaddrs:
  /ip4/<YOUR_VPS_IP>/tcp/7777/p2p/12D3KooW...
```

**Note the Peer ID and IP** — you'll need them for the next steps.

To run as a service:

```bash
sudo cp relay-server.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable relay-server
sudo systemctl start relay-server
```

### Step 2: Configure the Home Node and Client Node

Edit `relayAddrs` in **both** `home-node/main.go` and `client-node/main.go`:

```go
var relayAddrs = []string{
    "/ip4/<YOUR_VPS_IP>/tcp/7777/p2p/<RELAY_PEER_ID>",
}
```

Replace `<YOUR_VPS_IP>` and `<RELAY_PEER_ID>` with the actual values from the relay server output.

### Step 3: Start the Home Node

On your home Linux or macOS machine:

```bash
cd home-node
go mod tidy
go build -o home-node .
./home-node
```

Wait for:

```
✅ Connected to relay 12D3KooW...
✅ Relay address: /ip4/.../p2p-circuit
🏠 Peer ID: 12D3KooW...
```

**Copy the Home Node Peer ID** for the client.

### Step 4: Run the Client

On your phone (via gomobile) or another computer:

```bash
cd client-node
go mod tidy
go build -o client-node .
./client-node <HOME_PEER_ID>
```

Expected output:

```
✅ Connected to relay 12D3KooW...
✅ Found home node via rendezvous!
📡 Connecting to home node...
✅ Connected! via .../p2p-circuit
🏓 Sending PING...
🎉 Response: pong
```

## How the Relay Server Works

The relay server is intentionally minimal — a plain libp2p host with the circuit relay v2 service attached manually via `relayv2.New()`:

```go
h, err := libp2p.New(
    libp2p.ListenAddrStrings("/ip4/0.0.0.0/tcp/7777"),
)
relayv2.New(h, relayv2.WithInfiniteLimits())
```

Key design decisions:

- **No DHT participation** — avoids being swarmed by the public IPFS network. Port 4001 + DHT = hundreds of random IPFS peers connecting within minutes.
- **Non-standard port (7777)** — further avoids IPFS swarm traffic.
- **`WithInfiniteLimits()`** — removes bandwidth and time caps on relay connections. Safe because this is a private relay only your nodes use.
- **TCP only** — QUIC was removed to simplify the configuration and avoid transport negotiation issues.
- **Manual `relayv2.New()`** — `libp2p.EnableRelayService()` as a host option did not reliably register the hop protocol in testing. Creating the relay manually after host creation works consistently.

## Key Lessons Learned

1. **`ForceReachabilityPrivate()`** is essential on the home node. Without it, libp2p detects the public IPv6 address and assumes it's publicly reachable, so it never bothers with relay reservations.

2. **`libp2p.DisableRelay()`** disables ALL relay functionality, including the relay service. Don't use it on the relay server.

3. **`libp2p.EnableRelayService()`** as a host option didn't reliably register the hop protocol. Using `relayv2.New(host)` after host creation is more reliable.

4. **Port 4001** is the standard IPFS port. If your relay joins the DHT on this port, it will be swarmed by hundreds of IPFS peers within minutes. Use a non-standard port and skip DHT on the relay.

5. **Starlink assigns globally routable IPv6 addresses** but blocks inbound connections via its router firewall. The IPv6 address is reachable from the local network but not from the internet without router configuration changes.

6. **NAT port mapping (UPnP/PCP)** does not open inbound ports on Starlink's router, even though the libp2p host reports public addresses. The addresses are real but not reachable from outside.

7. **DHT peer discovery** can return a peer with zero addresses if the peer hasn't had time to propagate. Always fall through to `FindPeer()` as a backup, and allow 3–5 minutes after home node startup.

## Troubleshooting

| Issue | Solution |
|-------|----------|
| `failed to negotiate security protocol: EOF` | Old process still running on the port. `lsof -i :<PORT>` to find it, kill it, restart. |
| `protocols not supported: [/libp2p/circuit/relay/0.2.0/hop]` | Relay service not registered. Use `relayv2.New(h)` instead of `EnableRelayService()`. |
| `error opening hop stream to relay: connection failed` | Home node hasn't made a reservation. Check that `ForceReachabilityPrivate()` is set and relay addresses are correct. |
| `Found peer via rendezvous but no addresses` | Home node needs more time on the DHT. Wait 3–5 minutes after startup. |
| Home node shows no `/p2p-circuit` addresses | `ForceReachabilityPrivate()` missing, or relay address in config is wrong. |
| `failed to sufficiently increase receive buffer size` | Not an error. Fix with `sudo sysctl -w net.core.rmem_max=7500000` |
| Relay swarmed by hundreds of peers | Don't join the DHT on the relay. Use a non-standard port (not 4001). |

## Bandwidth Considerations

The relay only carries signaling and message traffic. For the ping-pong demo, this is negligible (bytes per message). A $5/month Linode with 1TB transfer is more than sufficient.

For large file transfers, the relay becomes a bottleneck. The strategy is:

1. Use the relay for initial connection establishment.
2. DCUtR (hole-punching) attempts to upgrade to a direct connection.
3. If hole-punching succeeds, bulk data flows directly — no relay bandwidth used.
4. If hole-punching fails (common with Starlink's symmetric NAT), consider placing the Starlink router in bypass mode and using your own router with configurable IPv6 firewall rules.

## Next Steps

- **iOS app** — Compile client-node Go code into an `.xcframework` using `gomobile bind` and call it from Swift.
- **File transfer** — Extend the `/pingpong/1.0.0` protocol to stream files between peers.
- **mDNS discovery** — Add local network discovery for when the phone is on home WiFi (full LAN speed, no relay needed).
- **Dynamic DNS** — Set up AAAA record updates for the home node's IPv6 address as a direct-connect fallback.

## Dependencies

- [go-libp2p](https://github.com/libp2p/go-libp2p) v0.38.2
- [go-libp2p-kad-dht](https://github.com/libp2p/go-libp2p-kad-dht) v0.28.1
- [go-multiaddr](https://github.com/multiformats/go-multiaddr) v0.14.0

## License

MIT
