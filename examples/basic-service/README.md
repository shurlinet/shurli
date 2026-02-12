# Basic Service Example

This example demonstrates the core functionality of the `pkg/p2pnet` library:
- Exposing a local HTTP service through the P2P network
- Connecting to a remote peer's service
- Bidirectional data transfer

## What This Example Does

1. **Server** (`server.go`):
   - Starts a local HTTP server on `localhost:8080`
   - Creates a P2P network node
   - Exposes the HTTP service via P2P using protocol `/peerup/http/1.0.0`
   - Waits for incoming connections

2. **Client** (`client.go`):
   - Creates a P2P network node
   - Connects to the server peer
   - Opens a stream to the HTTP service
   - Sends an HTTP GET request
   - Receives and prints the response

## Prerequisites

- Go 1.23.0 or later
- Two terminal windows

## Running the Example

### Terminal 1: Start the Server

```bash
cd examples/basic-service
go run server.go
```

You'll see output like:
```
🌐 Starting local HTTP server on localhost:8080
🆔 Server Peer ID: 12D3KooWLCavCP1Pma9NGJQnGDQhgwSjgQgupWprZJH4w1P3HCVL
📍 Listening on: [/ip4/127.0.0.1/tcp/9100 /ip4/192.168.1.100/tcp/9100]
✅ Registered service: http (protocol: /peerup/http/1.0.0, local: localhost:8080)
✅ Server ready! Share this peer ID with clients:
   12D3KooWLCavCP1Pma9NGJQnGDQhgwSjgQgupWprZJH4w1P3HCVL

💡 To connect from client:
   go run client.go 12D3KooWLCavCP1Pma9NGJQnGDQhgwSjgQgupWprZJH4w1P3HCVL
```

**Copy the peer ID** from the output.

### Terminal 2: Run the Client

```bash
cd examples/basic-service
go run client.go <SERVER_PEER_ID>
```

Replace `<SERVER_PEER_ID>` with the peer ID from the server output.

You'll see:
```
🆔 Client Peer ID: 12D3KooWPjceQrSwdWXPyLLeABRXmuqt69Rg3sBYUXCUVwbj7QbA
📍 Listening on: [/ip4/127.0.0.1/tcp/54321]
🔗 Connecting to server at /ip4/127.0.0.1/tcp/9100/p2p/12D3Koo...
✅ Connected to server!
🌐 Opening HTTP service stream...
✅ HTTP service stream opened!
📤 Sending HTTP request...
📥 Reading response...
HTTP/1.1 200 OK
Content-Length: 45
Content-Type: text/plain; charset=utf-8

Hello from P2P! Time: 2026-02-13T10:30:45Z
✅ Test completed successfully!
```

### On the Server Side

You'll see logs indicating the incoming connection:
```
📥 Incoming http connection from 12D3KooWPjceQr...
📨 Served HTTP request from 127.0.0.1:54321
✅ Closed http connection from 12D3KooWPjceQr...
```

## What's Happening Under the Hood

1. **Service Registration**: Server registers the HTTP service with `ExposeService("http", "localhost:8080")`
2. **Stream Handler**: Library sets up a handler for protocol `/peerup/http/1.0.0`
3. **Peer Connection**: Client connects to server using direct TCP (in production, use relay/DHT)
4. **Service Stream**: Client opens a stream using `ConnectToService()`
5. **Bidirectional Proxy**: Library creates TCP↔Stream proxy automatically
6. **Data Transfer**: HTTP request/response flows through the P2P stream

## Key Concepts Demonstrated

### Service Exposure Pattern
```go
net.ExposeService("http", "localhost:8080")
```
- Maps local TCP service to P2P protocol
- Automatic stream handling
- Bidirectional proxy setup

### Service Connection Pattern
```go
conn, err := net.ConnectToService(peerID, "http")
// conn implements io.ReadWriteCloser
conn.Write(request)
conn.Read(response)
```

### Protocol Naming Convention
```
/peerup/<service-name>/<version>
```
Examples: `/peerup/http/1.0.0`, `/peerup/ssh/1.0.0`

## Limitations of This Example

- **Direct Connection**: Uses hardcoded IP/port (`/ip4/127.0.0.1/tcp/9100`)
- **No NAT Traversal**: Won't work across different networks
- **No Authentication**: Anyone can connect

See the full `home-node` and `client-node` implementations for:
- Relay-based NAT traversal
- DHT-based peer discovery
- Connection gating with authorized_keys

## Next Steps

1. **Add More Services**: Try exposing SSH, SMB, or custom protocols
2. **Add Naming**: Use `RegisterName()` to map friendly names to peer IDs
3. **Integrate with home-node**: Refactor home-node to use this library
4. **Add Relay**: Use circuit relay for NAT traversal

## Troubleshooting

**Client can't connect:**
- Ensure server is running first
- Copy the exact peer ID from server output
- Check firewall allows TCP port 9100

**"Address already in use":**
- Another process is using port 8080 or 9100
- Change ports in server.go

**Import errors:**
- Run `go mod tidy` in this directory
- Ensure you're in the examples/basic-service directory

## Files Generated

- `server.key` - Server's Ed25519 private key (auto-generated)
- `client.key` - Client's Ed25519 private key (auto-generated)

These files persist identity across restarts. Delete them to get new peer IDs.
