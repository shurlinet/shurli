---
title: "Go SDK"
weight: 10
description: "Shurli Go SDK reference: PeerNetwork, ServiceManager, Resolver, Authorizer interfaces, events, middleware, file transfer, and network tools."
---

# Shurli Go SDK

Shurli ships a public Go SDK at `pkg/sdk`. This package is the primary interface for building Go applications on top of Shurli's P2P network. Import it as:

```go
import "github.com/shurlinet/shurli/pkg/sdk"
```

Everything in `pkg/sdk` is stable, documented, and safe for concurrent use.

## Core Interfaces

The SDK is built around four interfaces. Depend on these instead of concrete types.

### PeerNetwork

The top-level interface for interacting with a Shurli network.

```go
type PeerNetwork interface {
    PeerID() peer.ID
    ExposeService(name, localAddress string, allowedPeers map[peer.ID]struct{}) error
    UnexposeService(name string) error
    ListServices() []*Service
    ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error)
    ConnectToServiceContext(ctx context.Context, peerID peer.ID, serviceName string) (ServiceConn, error)
    ResolveName(name string) (peer.ID, error)
    RegisterName(name string, peerID peer.ID) error
    OnEvent(handler EventHandler) func()
    Close() error
}
```

Create a concrete `*Network` with `sdk.New()`:

```go
net, err := sdk.New(&sdk.Config{
    KeyFile: "identity.key",
    Config:  &config.Config{
        Network: config.NetworkConfig{
            ListenAddresses: []string{"/ip4/0.0.0.0/tcp/9100"},
        },
    },
})
defer net.Close()
```

### Resolver

Maps human-readable names to peer IDs. Supports chaining: local config, DNS, DHT, custom backends.

```go
type Resolver interface {
    Resolve(name string) (peer.ID, error)
}
```

### ServiceManager

Manages service registration, discovery, and dialing. Supports stream middleware for cross-cutting concerns.

```go
type ServiceManager interface {
    RegisterService(svc *Service) error
    UnregisterService(name string) error
    GetService(name string) (*Service, bool)
    ListServices() []*Service
    DialService(ctx context.Context, peerID peer.ID, protocolID string) (ServiceConn, error)
    Use(middleware ...StreamMiddleware)
}
```

### Authorizer

Makes authorization decisions about peers. Default implementation uses Shurli's `authorized_keys` file.

```go
type Authorizer interface {
    IsAuthorized(p peer.ID) bool
}
```

## Key Components

| Component | Type | Purpose |
|-----------|------|---------|
| `Network` | struct | Core P2P host with relay, NAT traversal, DHT |
| `ServiceRegistry` | struct | Service registration and stream routing |
| `EventBus` | struct | Connection, service, and auth event dispatching |
| `NameResolver` | struct | Human-readable name resolution with fallback chains |
| `TransferService` | struct | Chunked file transfer (SHFT v2 wire format) |
| `PeerManager` | struct | Background reconnection with exponential backoff |
| `PathDialer` | struct | Parallel dial racing across transport paths |
| `PathTracker` | struct | Per-peer path quality tracking |
| `NetIntel` | struct | Presence protocol for network intelligence |
| `MDNSDiscovery` | struct | LAN peer discovery |
| `STUNProber` | struct | NAT type detection |
| `NetworkMonitor` | struct | OS-level network change detection |
| `RelayDiscovery` | struct | Static + DHT relay discovery |
| `PeerRelay` | struct | Auto-enabled relay when node has public IP |
| `BandwidthTracker` | struct | Per-peer bandwidth stats and budgets |
| `RelayHealth` | struct | EWMA-based relay health scoring |
| `Metrics` | struct | Prometheus metrics (50+ custom metrics) |
| `AuditLogger` | struct | Structured security audit trail |
| `PluginPolicy` | struct | Transport-aware access control for plugins |

## Quick Start: Expose a Service

The simplest use case: expose a local TCP service over Shurli's P2P network.

**Server** (expose local HTTP on port 8080):

```go
package main

import (
    "log"
    "github.com/shurlinet/shurli/internal/config"
    "github.com/shurlinet/shurli/pkg/sdk"
)

func main() {
    net, err := sdk.New(&sdk.Config{
        KeyFile: "server.key",
        Config: &config.Config{
            Network: config.NetworkConfig{
                ListenAddresses: []string{"/ip4/0.0.0.0/tcp/9100"},
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer net.Close()

    // Expose local HTTP server to all authorized peers
    net.ExposeService("http", "localhost:8080", nil)

    log.Printf("Peer ID: %s", net.PeerID())
    select {} // block forever
}
```

**Client** (connect to the exposed service):

```go
package main

import (
    "bufio"
    "io"
    "log"
    "net/http"
    "github.com/libp2p/go-libp2p/core/peer"
    "github.com/shurlinet/shurli/internal/config"
    "github.com/shurlinet/shurli/pkg/sdk"
)

func main() {
    serverID, _ := peer.Decode("12D3KooW...") // server's peer ID

    net, err := sdk.New(&sdk.Config{
        KeyFile: "client.key",
        Config: &config.Config{
            Network: config.NetworkConfig{
                ListenAddresses: []string{"/ip4/0.0.0.0/tcp/9200"},
            },
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    defer net.Close()

    // Connect to the remote service
    conn, err := net.ConnectToService(serverID, "http")
    if err != nil {
        log.Fatal(err)
    }

    // Use like any net.Conn - send HTTP request over P2P
    req, _ := http.NewRequest("GET", "http://localhost/", nil)
    req.Write(conn)

    resp, _ := http.ReadResponse(bufio.NewReader(conn), req)
    body, _ := io.ReadAll(resp.Body)
    log.Printf("Response: %s", body)
}
```

See the [complete example on GitHub](https://github.com/shurlinet/shurli/tree/main/examples/basic-service).

## Events

Subscribe to network events for monitoring, logging, or reactive behavior:

```go
unsubscribe := net.OnEvent(func(evt sdk.Event) {
    switch evt.Type {
    case sdk.EventPeerConnected:
        log.Printf("Peer connected: %s", evt.PeerID)
    case sdk.EventPeerDisconnected:
        log.Printf("Peer disconnected: %s", evt.PeerID)
    case sdk.EventServiceRegistered:
        log.Printf("Service registered: %s", evt.ServiceName)
    }
})
defer unsubscribe()
```

## Stream Middleware

Add cross-cutting behavior to all stream handlers:

```go
type StreamMiddleware func(next network.StreamHandler) network.StreamHandler
```

Middleware wraps every stream handler in the order added (first added = outermost wrapper). Use cases: compression, bandwidth limiting, progress tracking, audit trails.

## Plugin Transport Policy

By default, plugins only operate over LAN and direct connections. Relay transport is excluded unless explicitly allowed:

```go
policy := sdk.DefaultPluginPolicy()
policy.AllowRelay("filetransfer") // allow file transfer over relay
```

This prevents accidental relay bandwidth consumption by plugins that should only work on direct paths.

## Network Tools

The SDK includes network diagnostic functions:

```go
// Streaming ping with configurable count and interval
ch := sdk.PingPeer(ctx, host, peerID, protocolID, count, interval)
for result := range ch {
    fmt.Printf("RTT: %v\n", result.RTT)
}

// Compute statistics from ping results
stats := sdk.ComputePingStats(results)
fmt.Printf("avg=%v min=%v max=%v loss=%.1f%%\n",
    stats.Avg, stats.Min, stats.Max, stats.Loss*100)

// Trace connection path
sdk.TracePeer(ctx, host, peerID, protocolID)
```

## File Transfer

The `TransferService` implements production-grade file transfer:

- **FastCDC** content-defined chunking
- **BLAKE3** Merkle tree integrity verification
- **zstd** compression (on by default)
- **Reed-Solomon** erasure coding (auto-enabled on direct WAN)
- **Resumable** transfers with bitfield checkpoint persistence
- Receive permissions: off, contacts, ask, open
- Disk space checks, atomic writes, compression bomb protection

File transfer is exposed as a plugin (`plugins/filetransfer/`) that builds on the `TransferService` engine in `pkg/sdk`.

## Thread Safety

All exported types are safe for concurrent use. `Network`, `ServiceRegistry`, `EventBus`, `NameResolver`, and `TransferService` use internal locking.

## Package Layout

```
pkg/sdk/
    contracts.go       Interfaces: PeerNetwork, Resolver, ServiceManager, Authorizer
    network.go         Network constructor and lifecycle
    service.go         ServiceRegistry and ServiceConn
    events.go          EventBus
    naming.go          NameResolver
    transfer.go        TransferService (SHFT v2)
    bootstrap.go       BootstrapAndConnect
    standalone.go      NewStandaloneHost for CLI commands
    protocolid.go      Protocol ID construction and validation
    plugin_policy.go   Transport-aware plugin access control
    proxy.go           TCP proxy (ExposeService/ConnectToService)
    ping.go            PingPeer, ComputePingStats
    traceroute.go      TracePeer
    peermanager.go     Background reconnection
    pathdialer.go      Parallel dial racing
    pathtracker.go     Per-peer path quality
    mdns.go            LAN discovery
    netintel.go        Presence protocol
    netmonitor.go      OS network change detection
    interfaces.go      DiscoverInterfaces
    stunprober.go      NAT type detection
    metrics.go         Prometheus metrics
    audit.go           Security audit logging
    bandwidth.go       Per-peer bandwidth tracking
    errors.go          Sentinel errors
    doc.go             Package documentation
```

## Go Documentation

Full API reference is available via `go doc`:

```bash
go doc github.com/shurlinet/shurli/pkg/sdk
go doc github.com/shurlinet/shurli/pkg/sdk.PeerNetwork
go doc github.com/shurlinet/shurli/pkg/sdk.New
```
