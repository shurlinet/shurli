# Batch I: Adaptive Multi-Interface Path Selection

Interface discovery, parallel dial racing, path quality tracking, network change monitoring, STUN hole-punching, and every-peer-is-a-relay.

---

### ADR-I01: Interface Discovery with IPv6/IPv4 Classification

**Context**: Shurli needs to know what network interfaces are available to make intelligent connection decisions. Without interface awareness, the system cannot distinguish between IPv4-only, IPv6-only, or dual-stack hosts.

**Alternatives considered**:
- **Rely on libp2p's address reporting** - libp2p reports listen addresses but doesn't classify them by interface or IP version. Insufficient for path ranking decisions.
- **Platform-specific APIs** (macOS SCDynamicStore, Linux netlink) - More detailed but requires platform-specific code for a cross-platform tool.

**Decision**: `DiscoverInterfaces()` in `pkg/p2pnet/interfaces.go` uses Go's `net.Interfaces()` to enumerate all interfaces and classify addresses as global IPv4, global IPv6, or loopback. Returns an `InterfaceSummary` with convenience flags (`HasGlobalIPv6`, `HasGlobalIPv4`). Called at startup and on every network change.

**Consequences**: Cross-platform (Go stdlib). Slightly less detailed than platform-native APIs but sufficient for path ranking. Prometheus `interface_count` gauge tracks interface availability.

**Reference**: `pkg/p2pnet/interfaces.go`, `pkg/p2pnet/interfaces_test.go`

---

### ADR-I02: Parallel Dial Racing (Replace Sequential Connect)

**Context**: The old `ConnectToPeer()` tried DHT discovery (15s timeout) then relay fallback (30s timeout) sequentially. Worst case: 45 seconds to connect. For a tool that needs to feel instant, this is unacceptable.

**Alternatives considered**:
- **Increase timeouts** - Makes the problem worse, not better.
- **Always use relay** - Fast but defeats the purpose of direct connections.
- **libp2p's built-in smart dialing only** - Handles address-level racing but doesn't race between discovery strategies (DHT vs relay).

**Decision**: `PathDialer.DialPeer()` races DHT discovery and relay connection in parallel goroutines. If the peer is already connected, returns immediately (fast path). First successful connection wins; the loser is cancelled. The winning path is classified as `DIRECT` or `RELAYED` based on multiaddr inspection. Old `ConnectToPeer()` preserved as fallback.

**Consequences**: Connection time drops from 45s worst-case to the faster of DHT or relay (typically 3-10s). Slightly more goroutines spawned per connection attempt, but context cancellation ensures clean cleanup.

**Reference**: `pkg/p2pnet/pathdialer.go`, `pkg/p2pnet/pathdialer_test.go`

---

### ADR-I03: Event-Driven Path Quality Tracking

**Context**: Once connected, Shurli needs to know the quality of each connection path (direct vs relayed, transport type, IP version) for monitoring and future path switching decisions.

**Alternatives considered**:
- **Periodic polling** - Poll connection state on a timer. Wasteful and misses transient changes.
- **Wrap every connection call** - Track state manually in every connect/disconnect code path. Error-prone and duplicative.

**Decision**: `PathTracker` subscribes to libp2p's event bus (`EvtPeerConnectednessChanged`) to receive connect/disconnect events passively. Maintains per-peer path info (type, transport, IP version, connected time, last RTT). Exposed via `GET /v1/paths` daemon API endpoint. Prometheus labels: `path_type`, `transport`, `ip_version`.

**Consequences**: Zero polling overhead. Event-driven means path info updates immediately on connection state changes. Adds a dependency on libp2p's event bus API stability.

**Reference**: `pkg/p2pnet/pathtracker.go`, `pkg/p2pnet/pathtracker_test.go`

---

### ADR-I04: Network Change Monitoring (Polling with Diff)

**Context**: When a user switches WiFi networks, gains/loses a cellular connection, or plugs in Ethernet, Shurli should detect the change and re-evaluate connection paths.

**Alternatives considered**:
- **macOS SCDynamicStore + Linux Netlink** - Platform-native, truly event-driven, zero polling. More code, platform-specific build tags, harder to test.
- **libp2p event bus only** - libp2p fires address change events but not for all interface changes (e.g., gaining an interface with no libp2p listener).

**Decision**: `NetworkMonitor` polls `DiscoverInterfaces()` at a configurable interval and diffs against the previous snapshot. On change, fires registered callbacks (interface re-scan, STUN re-probe, peer relay auto-detect update). Simple, cross-platform, testable.

**Consequences**: Polling introduces a detection delay (up to the poll interval). Acceptable because network changes are rare events and the poll interval is configurable. Platform-native event-driven detection can be added later as an optimization without changing the callback API.

**Reference**: `pkg/p2pnet/netmonitor.go`, `pkg/p2pnet/netmonitor_test.go`

---

### ADR-I05: Zero-Dependency STUN Client (RFC 5389)

**Context**: To classify NAT type and discover external addresses for hole-punching, Shurli needs STUN probing. Existing Go STUN libraries (pion/stun) would add a new dependency.

**Alternatives considered**:
- **pion/stun** - Mature, widely used. Rejected because it pulls in the entire pion dependency tree (already have pion/dtls as a transitive dep of libp2p, but adding pion/stun directly increases attack surface and binary size).
- **Skip STUN entirely** - Rely on AutoNAT v2 only. AutoNAT gives reachability but not NAT type classification (full-cone vs symmetric matters for hole-punch prediction).

**Decision**: Implement a minimal RFC 5389 STUN Binding Request client in `pkg/p2pnet/stunprober.go`. ~150 lines of code. Probes multiple STUN servers concurrently (Google, Cloudflare). Collects external addresses, classifies NAT type (none, full-cone, address-restricted, port-restricted, symmetric). `HolePunchable()` helper indicates DCUtR likelihood.

**Consequences**: Zero new dependencies. Binary size unchanged. The STUN client only implements Binding Request (the simplest STUN transaction). Does NOT implement TURN or ICE. Runs in background at startup (non-blocking) and re-probes on network change.

**Reference**: `pkg/p2pnet/stunprober.go`, `pkg/p2pnet/stunprober_test.go`

---

### ADR-I06: Every-Peer-Is-A-Relay (Auto-Enable with Public IP)

**Context**: The relay VPS is a single point of failure. If every peer with a public IP could relay for its authorized peers, the VPS becomes redundant.

**Alternatives considered**:
- **Manual relay enable** - User explicitly enables relay in config. Safe but friction prevents adoption.
- **Always enable relay** - Even on NATted nodes. Wasteful because NATted relays can't accept inbound connections.
- **DHT-based relay advertisement** - Peers discover relays via DHT. Deferred to Post-I because it requires the PeerManager/AddrMan infrastructure.

**Decision**: Any peer with a detected global IP (from `DiscoverInterfaces()`) auto-enables circuit relay v2 with conservative resource limits (4 reservations, 16 circuits, 128KB/direction, 10min sessions). Uses the existing `ConnectionGater` for authorization (no new ACL needed). Auto-detects on startup and network changes. Disables when public IP is lost.

**Consequences**: Peers behind NAT never become relays (correct). Peers with public IPs silently become relays for their authorized peers. The conservative limits prevent resource exhaustion on home machines. DHT-based relay discovery (so peers can find each other's relays) is deferred to Post-I.

**Reference**: `pkg/p2pnet/peerrelay.go`, `pkg/p2pnet/peerrelay_test.go`
