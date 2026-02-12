# libp2p Ping-Pong Demo (Relay + Hole-Punching)

Two nodes that find each other via the public libp2p DHT, connect through relays,
and attempt hole-punching for a direct connection.

## Prerequisites

- Go 1.22+
- Both machines need internet access (outbound)
- No inbound ports or public IPs required

## Setup

### 1. Home Node (your Linux or macOS machine)

```bash
cd home-node
go mod tidy
go run main.go
```

It will print something like:
```
🏠 Peer ID: 12D3KooW...
```

**Copy this Peer ID** — you'll need it for the client.

Give the home node 1-2 minutes to register with the DHT and find relay nodes.
Watch for relay addresses (containing `/p2p-circuit`) in the status output.

### 2. Client Node (another machine, or eventually your iPhone)

```bash
cd client-node
go mod tidy
go run main.go 12D3KooW...PASTE_HOME_PEER_ID_HERE
```

The client will:
1. Bootstrap into the DHT
2. Search for the home node by Peer ID
3. Connect (likely via relay first)
4. Send "ping" and receive "pong"
5. Wait to see if hole-punching upgrades to a direct connection

## What to Expect

- **First connection** will almost certainly be RELAYED
- **Hole-punching** may or may not succeed depending on your NAT type
- If hole-punching succeeds, subsequent messages go DIRECT (full speed)
- If it stays relayed, small messages work fine but large transfers will be slow

## For iPhone

The client-node code can be compiled for iOS using `gomobile bind`:

```bash
# Install gomobile
go install golang.org/x/mobile/cmd/gomobile@latest
gomobile init

# Create a package that exposes SendPing(peerID string) string
# Then: gomobile bind -target=ios ./mobile
```

This produces an .xcframework you import into your Swift project.

## Persistent Identity

The home node saves its key to `home_node.key` so the Peer ID stays the
same across restarts. The client generates a new identity each time (fine
for a transient client).

## Troubleshooting

- **"Could not find home node"**: Wait longer. DHT registration can take
  2-3 minutes. Make sure the home node shows connected bootstrap peers.
- **Stays relayed**: Normal for Starlink CGNAT. Relay works for small
  messages. For large transfers, consider Starlink bypass mode with your
  own router.
- **Buffer size warning**: Not an error. Fix with:
  ```bash
  sudo sysctl -w net.core.rmem_max=7500000
  sudo sysctl -w net.core.wmem_max=7500000
  ```
