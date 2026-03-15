package p2pnet

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/host/autorelay"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/identity"
)

// DHTProtocolPrefix is the default protocol prefix for the private shurli Kademlia DHT.
// This isolates shurli from the public IPFS Amino DHT (/ipfs/kad/1.0.0),
// giving us our own routing table at /shurli/kad/1.0.0.
// For namespace-specific prefixes, use DHTProtocolPrefixForNamespace.
const DHTProtocolPrefix = "/shurli"

// DHTProtocolPrefixForNamespace returns the DHT protocol prefix for a given
// network namespace. An empty namespace returns the default global prefix ("/shurli").
// A non-empty namespace produces "/shurli/<namespace>" which results in the
// full DHT protocol "/shurli/<namespace>/kad/1.0.0", completely isolated from
// other namespaces at the protocol level.
func DHTProtocolPrefixForNamespace(namespace string) string {
	if namespace == "" {
		return DHTProtocolPrefix
	}
	return DHTProtocolPrefix + "/" + namespace
}

// holePunchTracer logs DCUtR hole-punching events and records metrics when available.
type holePunchTracer struct {
	metrics *Metrics // nil when metrics disabled
}

// truncateError returns the first line of an error string, capped at 200 chars.
// libp2p dial errors can be enormous (20+ lines listing every address attempt).
func truncateError(s string) string {
	// Take first line only
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

func (t *holePunchTracer) Trace(evt *holepunch.Event) {
	short := evt.Remote.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	switch e := evt.Evt.(type) {
	case *holepunch.StartHolePunchEvt:
		slog.Info("hole punch started", "peer", short, "addrs", len(e.RemoteAddrs), "rtt", e.RTT)
	case *holepunch.EndHolePunchEvt:
		if e.Success {
			slog.Info("hole punch succeeded", "peer", short, "elapsed", e.EllapsedTime)
		} else {
			slog.Warn("hole punch failed", "peer", short, "elapsed", e.EllapsedTime, "error", truncateError(e.Error))
		}
		if t.metrics != nil {
			result := "failure"
			if e.Success {
				result = "success"
			}
			t.metrics.HolePunchTotal.WithLabelValues(result).Inc()
			t.metrics.HolePunchDurationSeconds.WithLabelValues(result).Observe(e.EllapsedTime.Seconds())
		}
	case *holepunch.DirectDialEvt:
		if e.Success {
			slog.Info("direct dial succeeded", "peer", short, "elapsed", e.EllapsedTime)
		} else {
			slog.Warn("direct dial failed", "peer", short, "error", truncateError(e.Error))
		}
	}
}

// Network represents a P2P network instance
type Network struct {
	host            host.Host
	config          *config.Config
	serviceRegistry *ServiceRegistry
	nameResolver    *NameResolver
	events          *EventBus
	ctx             context.Context
	cancel          context.CancelFunc

	// Black hole detector counters. Stored so NetworkMonitor can reset
	// them on network change (a new interface invalidates the previous
	// black hole state - the new network may have working IPv6/UDP).
	udpBlackHole  *swarm.BlackHoleSuccessCounter
	ipv6BlackHole *swarm.BlackHoleSuccessCounter
}

// Config for creating a new P2P network
type Config struct {
	KeyFile         string
	KeyPassword     string                       // Password for SHRL-encrypted identity.key
	AuthorizedKeys  string                       // Path to authorized_keys file (auto-creates gater if Gater is nil)
	Gater           *auth.AuthorizedPeerGater     // Pre-created gater (for hot-reload support). Takes precedence over AuthorizedKeys.
	Config          *config.Config
	UserAgent       string                        // libp2p Identify user agent (e.g. "shurli/0.1.0")

	// Relay configuration (optional)
	EnableRelay         bool              // Enable relay support (AutoRelay + hole punching)
	RelayAddrs          []string          // Relay server multiaddrs (e.g., "/ip4/1.2.3.4/tcp/7777/p2p/12D3Koo...")
	ForcePrivate        bool              // Force private reachability (required for relay reservations)
	EnableNATPortMap    bool              // Enable NAT port mapping
	EnableHolePunching  bool              // Enable hole punching

	// Per-network ephemeral identity: when set, derives a namespace-specific
	// Ed25519 key from the master identity via HKDF. The node uses a different
	// peer ID on each namespace, preventing cross-network correlation.
	// Empty = global network (uses master identity, backward compatible).
	Namespace string

	// Extension points (optional, nil = use defaults)
	Resolver Resolver // Custom name resolver (nil = built-in local resolver)

	// Resource management
	ResourceLimitsEnabled bool            // Enable libp2p resource manager (connection/stream/memory limits)

	// Observability
	Metrics          *Metrics          // Custom shurli metrics (nil = disabled). When non-nil, libp2p metrics are registered on Metrics.Registry.
	BandwidthTracker *BandwidthTracker // Per-peer bandwidth tracking (nil = disabled). Counter() wired into libp2p.BandwidthReporter().
}

// New creates a new P2P network instance
func New(cfg *Config) (*Network, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Load identity
	priv, err := LoadOrCreateIdentity(cfg.KeyFile, cfg.KeyPassword)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	// Per-network ephemeral identity: derive a namespace-specific key so the
	// node uses a different peer ID on each private network. This prevents
	// cross-network peer ID correlation. Global network (empty namespace) uses
	// the master identity unchanged for backward compatibility.
	if cfg.Namespace != "" {
		priv, err = identity.DeriveNamespaceKey(priv, cfg.Namespace)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to derive namespace identity: %w", err)
		}
	}

	// Create libp2p host options.
	// Transport order: QUIC first (3 RTTs, native multiplexing, better hole-punching),
	// TCP second (4 RTTs, universal fallback), WebSocket last (anti-censorship/DPI evasion).
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport, tcp.WithDialerForAddr(sourceBindDialerForAddr)),
		libp2p.Transport(ws.New),
		libp2p.EnableAutoNATv2(),
	}

	// Metrics: when enabled, register libp2p's built-in Prometheus collectors
	// on our isolated registry. When disabled, turn off libp2p's default metric
	// collection to avoid CPU overhead from counters nobody reads.
	if cfg.Metrics != nil {
		hostOpts = append(hostOpts, libp2p.PrometheusRegisterer(cfg.Metrics.Registry))
	} else {
		hostOpts = append(hostOpts, libp2p.DisableMetrics())
	}

	// Bandwidth tracking: wire libp2p's BandwidthCounter as a reporter so
	// every stream read/write is accounted for per-peer and per-protocol.
	if cfg.BandwidthTracker != nil {
		hostOpts = append(hostOpts, libp2p.BandwidthReporter(cfg.BandwidthTracker.Counter()))
	}

	if cfg.UserAgent != "" {
		hostOpts = append(hostOpts, libp2p.UserAgent(cfg.UserAgent))
	}

	// Add listen addresses if configured
	if cfg.Config != nil && len(cfg.Config.Network.ListenAddresses) > 0 {
		hostOpts = append(hostOpts, libp2p.ListenAddrStrings(cfg.Config.Network.ListenAddresses...))
	}

	// Ensure global IPv6 addresses from all interfaces are advertised.
	// libp2p's default address manager only includes addresses from the
	// OS default route interface. When a secondary interface (e.g., USB
	// LAN) has global IPv6 but the primary (e.g., 5G WiFi) does not,
	// those addresses are silently dropped. This factory adds them back
	// so identify/DHT advertise the full address set to peers.
	hostOpts = append(hostOpts, libp2p.AddrsFactory(globalIPv6AddrsFactory))

	// Add relay support if enabled
	if cfg.EnableRelay {
		// Parse relay addresses
		relayInfos, err := ParseRelayAddrs(cfg.RelayAddrs)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to parse relay addresses: %w", err)
		}

		if len(relayInfos) > 0 {
			// Static relay tuning (all defaults are designed for DHT-discovered relays):
			// - WithBackoff(30s): reduced from 1h default. Backoff is set BEFORE every
			//   reservation attempt. After network change kills the relay connection,
			//   30s means retry within one rsvpRefreshInterval.
			// - WithMinInterval(5s): reduced from 30s default. Rate-limits peer source
			//   calls. For static relays the peer source returns a hardcoded list (no
			//   network cost), so 5s is safe and reduces reconnection delay from ~30s to ~5s.
			// - WithBootDelay(0): default 3min waits for minCandidates (4) before connecting.
			//   We have known static relays, no discovery phase needed.
			// - WithMinCandidates(1): default 4. We have 2 static relays and want to
			//   connect to the first available immediately, not wait for more candidates.
			hostOpts = append(hostOpts, libp2p.EnableAutoRelayWithStaticRelays(relayInfos,
				autorelay.WithBackoff(30*time.Second),
				autorelay.WithMinInterval(5*time.Second),
				autorelay.WithBootDelay(0),
				autorelay.WithMinCandidates(1),
			))
		}

		if cfg.EnableNATPortMap {
			hostOpts = append(hostOpts, libp2p.NATPortMap())
		}

		if cfg.EnableHolePunching {
			hostOpts = append(hostOpts, libp2p.EnableHolePunching(holepunch.WithTracer(&holePunchTracer{metrics: cfg.Metrics})))
		}

		if cfg.ForcePrivate {
			hostOpts = append(hostOpts, libp2p.ForceReachabilityPrivate())
		}
	}

	// Add resource manager if enabled (bounds connections, streams, memory)
	if cfg.ResourceLimitsEnabled {
		limits := rcmgr.DefaultLimits
		libp2p.SetDefaultServiceLimits(&limits)
		scaled := limits.AutoScale()

		var rmOpts []rcmgr.Option
		if cfg.Metrics != nil {
			// Register rcmgr Prometheus collectors on our registry
			rcmgr.MustRegisterWith(cfg.Metrics.Registry)
			str, err := rcmgr.NewStatsTraceReporter()
			if err != nil {
				slog.Warn("failed to create rcmgr stats reporter", "error", err)
			} else {
				rmOpts = append(rmOpts, rcmgr.WithTraceReporter(str))
			}
		}

		rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(scaled), rmOpts...)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to create resource manager: %w", err)
		}
		hostOpts = append(hostOpts, libp2p.ResourceManager(rm))
		slog.Info("resource manager enabled", "limits", "auto-scaled")
	}

	// Add connection gater: use pre-created Gater if provided (enables hot-reload),
	// otherwise auto-create from AuthorizedKeys file path (simpler for commands that
	// don't need runtime auth management).
	if cfg.Gater != nil {
		hostOpts = append(hostOpts, libp2p.ConnectionGater(cfg.Gater))
	} else if cfg.AuthorizedKeys != "" {
		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.AuthorizedKeys)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to load authorized_keys: %w", err)
		}

		gater := auth.NewAuthorizedPeerGater(authorizedPeers)
		hostOpts = append(hostOpts, libp2p.ConnectionGater(gater))
	}

	// Create black hole detector counters with libp2p defaults. We store
	// references so NetworkMonitor can reset them on network change (a WiFi
	// switch invalidates black hole state - the new network may have working
	// IPv6/UDP even if the old one didn't).
	udpBH := &swarm.BlackHoleSuccessCounter{N: 100, MinSuccesses: 5, Name: "UDP"}
	ipv6BH := &swarm.BlackHoleSuccessCounter{N: 100, MinSuccesses: 5, Name: "IPv6"}
	hostOpts = append(hostOpts,
		libp2p.UDPBlackHoleSuccessCounter(udpBH),
		libp2p.IPv6BlackHoleSuccessCounter(ipv6BH),
	)

	// Create libp2p host
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	events := NewEventBus()

	// Use custom resolver if provided, otherwise default.
	var resolver *NameResolver
	if cfg.Resolver != nil {
		// Wrap custom resolver in a NameResolver that delegates to it.
		resolver = newNameResolverFrom(cfg.Resolver)
	} else {
		resolver = NewNameResolver()
	}

	net := &Network{
		host:            h,
		config:          cfg.Config,
		serviceRegistry: NewServiceRegistry(h, cfg.Metrics),
		nameResolver:    resolver,
		events:          events,
		ctx:             ctx,
		cancel:          cancel,
		udpBlackHole:    udpBH,
		ipv6BlackHole:   ipv6BH,
	}

	return net, nil
}

// sourceBindDialerForAddr returns a source-bound TCP dialer for global IPv6
// destinations. This fixes macOS routing on systems where disconnected VPN
// apps (Mullvad, ExpressVPN, ProtonVPN, etc.) create utun interfaces with
// default IPv6 routes that capture all unbound traffic, even when the VPN
// is not connected.
//
// For global IPv6 destinations: binds to the local global IPv6 address so
// the kernel routes through the real interface (e.g., USB LAN) instead of
// the dead utun interface.
//
// For all other destinations (IPv4, link-local, loopback): returns a plain
// net.Dialer with no source binding, preserving default behavior.
func sourceBindDialerForAddr(raddr ma.Multiaddr) (tcp.ContextDialer, error) {
	first, _ := ma.SplitFirst(raddr)
	if first == nil || first.Protocol().Code != ma.P_IP6 {
		return &net.Dialer{}, nil
	}

	ip := net.ParseIP(first.Value())
	if ip == nil || !isGlobalIPv6(ip) {
		return &net.Dialer{}, nil
	}

	// Destination is global IPv6. Source-bind to our global IPv6 so the
	// kernel routes through the real interface, bypassing utun defaults.
	summary, err := DiscoverInterfaces()
	if err != nil || !summary.HasGlobalIPv6 || len(summary.GlobalIPv6Addrs) == 0 {
		return &net.Dialer{}, nil
	}

	localIP := net.ParseIP(summary.GlobalIPv6Addrs[0])
	if localIP == nil {
		return &net.Dialer{}, nil
	}

	return &net.Dialer{
		LocalAddr: &net.TCPAddr{IP: localIP},
	}, nil
}

// globalIPv6AddrsFactory ensures global IPv6 addresses from all network
// interfaces are included in the host's advertised address set.
//
// libp2p's default address manager (interfaceAddrs.Filtered) only returns
// addresses from the OS default route interface. When the default route is
// 5G WiFi (no global IPv6) but a secondary interface like USB LAN has
// global IPv6, those addresses are silently dropped. Peers never learn
// about the IPv6 path through identify or DHT.
//
// This factory detects missing global IPv6 and adds it back by extracting
// the listen port from the loopback IPv6 entry (which shares the same
// wildcard socket) and constructing addresses for each global IPv6.
func globalIPv6AddrsFactory(addrs []ma.Multiaddr) []ma.Multiaddr {
	// Check if global IPv6 is already present in the address set.
	for _, a := range addrs {
		first, _ := ma.SplitFirst(a)
		if first == nil || first.Protocol().Code != ma.P_IP6 {
			continue
		}
		maddr, err := manet.ToNetAddr(a)
		if err != nil {
			continue
		}
		tcpAddr, ok := maddr.(*net.TCPAddr)
		if !ok {
			continue
		}
		if tcpAddr.IP != nil && isGlobalIPv6(tcpAddr.IP) {
			return addrs // Already has global IPv6, nothing to add.
		}
	}

	// Extract IPv6 TCP and QUIC listen ports from loopback entries.
	// The wildcard listen /ip6/::/tcp/0 binds [::]:PORT, so loopback
	// and all other IPv6 interfaces share the same port.
	var tcpPort, quicPort string
	for _, a := range addrs {
		first, _ := ma.SplitFirst(a)
		if first == nil || first.Protocol().Code != ma.P_IP6 {
			continue
		}
		if first.Value() != "::1" {
			continue
		}
		ma.ForEach(a, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case ma.P_TCP:
				if tcpPort == "" {
					tcpPort = c.Value()
				}
			case ma.P_UDP:
				if quicPort == "" {
					quicPort = c.Value()
				}
			}
			return true
		})
	}

	if tcpPort == "" && quicPort == "" {
		return addrs // No IPv6 listeners at all.
	}

	// Discover global IPv6 addresses from all interfaces.
	summary, err := DiscoverInterfaces()
	if err != nil || !summary.HasGlobalIPv6 {
		return addrs
	}

	// Add each global IPv6 with the wildcard socket ports.
	for _, ip6 := range summary.GlobalIPv6Addrs {
		if tcpPort != "" {
			addr, err := ma.NewMultiaddr("/ip6/" + ip6 + "/tcp/" + tcpPort)
			if err == nil {
				addrs = append(addrs, addr)
			}
		}
		if quicPort != "" {
			addr, err := ma.NewMultiaddr("/ip6/" + ip6 + "/udp/" + quicPort + "/quic-v1")
			if err == nil {
				addrs = append(addrs, addr)
			}
		}
	}

	slog.Debug("addrs-factory: added global IPv6",
		"ipv6_count", len(summary.GlobalIPv6Addrs),
		"tcpPort", tcpPort, "quicPort", quicPort)

	return addrs
}

// ResetBlackHoles resets libp2p's UDP and IPv6 black hole detectors.
// Call after a network change: the previous black hole state is invalid
// because the new network may have different connectivity (e.g., switching
// from cellular CGNAT with no IPv6 to a WiFi network with full IPv6).
// Without this, the swarm refuses to dial IPv6/UDP addresses even when
// our raw probes confirm reachability.
func (n *Network) ResetBlackHoles() {
	if n.udpBlackHole != nil {
		n.udpBlackHole.RecordResult(true)
	}
	if n.ipv6BlackHole != nil {
		n.ipv6BlackHole.RecordResult(true)
	}
	slog.Info("libp2p: black hole detectors reset (network change)")
}

// ClearDialBackoffs clears the swarm's per-peer dial backoff cache.
// Call after a network change: the previous "no route to host" failures
// are invalid because the new network may have different routing (e.g.,
// VPN with local network sharing now allows LAN paths that previously failed).
// Without this, mDNS upgrade attempts are rejected by stale swarm backoffs.
func (n *Network) ClearDialBackoffs(peers []peer.ID) {
	sw, ok := n.host.Network().(*swarm.Swarm)
	if !ok {
		return
	}
	for _, pid := range peers {
		sw.Backoff().Clear(pid)
	}
	if len(peers) > 0 {
		slog.Info("libp2p: swarm dial backoffs cleared (network change)", "peers", len(peers))
	}
}

// Host returns the underlying libp2p host
func (n *Network) Host() host.Host {
	return n.host
}

// PeerID returns the peer ID of this network node
func (n *Network) PeerID() peer.ID {
	return n.host.ID()
}

// ExposeService exposes a local TCP service through the P2P network.
// If allowedPeers is nil, all authorized peers can access the service.
func (n *Network) ExposeService(name, localAddress string, allowedPeers map[peer.ID]struct{}) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}
	return n.serviceRegistry.RegisterService(&Service{
		Name:         name,
		Protocol:     fmt.Sprintf("/shurli/%s/1.0.0", name),
		LocalAddress: localAddress,
		Enabled:      true,
		AllowedPeers: allowedPeers,
	})
}

// RegisterHandler registers a custom stream handler as a named service.
// This is the plugin registration path - unlike ExposeService (which proxies
// to a local TCP port), the handler processes streams directly.
//
// All plugins get a default PluginPolicy (LAN + Direct only, relay excluded).
// To override, set a custom policy on the returned service via GetService + modify,
// or pass a pre-built Service to ServiceRegistry.RegisterService directly.
func (n *Network) RegisterHandler(name string, handler StreamHandler, allowedPeers map[peer.ID]struct{}) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}

	policy := DefaultPluginPolicy()

	// Merge legacy allowedPeers into the policy if provided.
	if allowedPeers != nil {
		policy.AllowPeers = allowedPeers
	}

	return n.serviceRegistry.RegisterService(&Service{
		Name:     name,
		Protocol: fmt.Sprintf("/shurli/%s/1.0.0", name),
		Handler:  handler,
		Enabled:  true,
		Policy:   policy,
	})
}

// RegisterHandlerRelayAllowed registers a custom stream handler that permits
// relay transport in addition to LAN and Direct. Use this for plugins that
// need to work for relay-only peers (e.g., file transfer for NAT-to-NAT).
func (n *Network) RegisterHandlerRelayAllowed(name string, handler StreamHandler, allowedPeers map[peer.ID]struct{}) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}

	policy := &PluginPolicy{
		AllowedTransports: TransportLAN | TransportDirect | TransportRelay,
	}

	if allowedPeers != nil {
		policy.AllowPeers = allowedPeers
	}

	return n.serviceRegistry.RegisterService(&Service{
		Name:     name,
		Protocol: fmt.Sprintf("/shurli/%s/1.0.0", name),
		Handler:  handler,
		Enabled:  true,
		Policy:   policy,
	})
}

// RegisterServiceQuery registers the service-query protocol handler, which
// allows remote peers to discover this node's enabled services. Relay transport
// is allowed since this only returns metadata (service names and protocols).
func (n *Network) RegisterServiceQuery() error {
	policy := &PluginPolicy{
		AllowedTransports: TransportLAN | TransportDirect | TransportRelay,
	}

	return n.serviceRegistry.RegisterService(&Service{
		Name:     "service-query",
		Protocol: ServiceQueryProtocol,
		Handler:  HandleServiceQuery(n.serviceRegistry),
		Enabled:  true,
		Policy:   policy,
	})
}

// OpenPluginStream opens a stream to a remote peer for a registered plugin,
// enforcing the plugin's transport and peer policy.
//
// If the plugin's policy forbids relay, the stream will not be opened over
// relay connections. If the peer is denied by the policy, the call fails
// immediately without a network round-trip.
//
// This is the correct way to initiate outbound plugin streams. Do NOT use
// Host().NewStream() directly for plugin protocols.
func (n *Network) OpenPluginStream(ctx context.Context, peerID peer.ID, serviceName string) (network.Stream, error) {
	svc, ok := n.serviceRegistry.GetService(serviceName)
	if !ok {
		return nil, fmt.Errorf("plugin %q not registered", serviceName)
	}

	// Enforce peer restrictions before touching the network.
	if svc.Policy != nil && !svc.Policy.PeerAllowed(peerID) {
		return nil, fmt.Errorf("peer denied by plugin %q policy", serviceName)
	}

	// Respect transport policy: only use WithAllowLimitedConn if relay is permitted.
	dialCtx := ctx
	if svc.Policy == nil || svc.Policy.RelayAllowed() {
		dialCtx = network.WithAllowLimitedConn(ctx, svc.Protocol)
	}

	s, err := n.host.NewStream(dialCtx, peerID, protocol.ID(svc.Protocol))
	if err != nil {
		// Helpful error when relay-only peer + relay not allowed.
		if svc.Policy != nil && !svc.Policy.RelayAllowed() && isRelayOnlyPeer(n.host, peerID) {
			return nil, fmt.Errorf("plugin %q does not allow relay, and peer is only reachable via relay: %w", serviceName, err)
		}
		return nil, fmt.Errorf("open stream: %w", err)
	}

	// Post-dial transport verification: the dialer may have chosen a path
	// that the policy forbids (e.g., relay fallback despite direct attempt).
	if svc.Policy != nil {
		transport := ClassifyTransport(s)
		if !svc.Policy.TransportAllowed(transport) {
			s.Reset()
			return nil, fmt.Errorf("plugin %q: connection transport not allowed by policy", serviceName)
		}
	}

	return s, nil
}

// ServiceRegistry returns the underlying ServiceRegistry for direct access.
// Plugins that need Use() or other ServiceManager methods use this.
func (n *Network) ServiceRegistry() *ServiceRegistry {
	return n.serviceRegistry
}

// UnexposeService removes a previously exposed service from the P2P network.
func (n *Network) UnexposeService(name string) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}
	return n.serviceRegistry.UnregisterService(name)
}

// ListServices returns all registered services.
func (n *Network) ListServices() []*Service {
	return n.serviceRegistry.ListServices()
}

// ConnectToService connects to a remote peer's service with a default 30s timeout.
func (n *Network) ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error) {
	ctx, cancel := context.WithTimeout(n.ctx, 30*time.Second)
	defer cancel()
	return n.ConnectToServiceContext(ctx, peerID, serviceName)
}

// ConnectToServiceContext connects to a remote peer's service using the provided context.
func (n *Network) ConnectToServiceContext(ctx context.Context, peerID peer.ID, serviceName string) (ServiceConn, error) {
	if err := ValidateServiceName(serviceName); err != nil {
		return nil, err
	}
	protocol := fmt.Sprintf("/shurli/%s/1.0.0", serviceName)
	return n.serviceRegistry.DialService(ctx, peerID, protocol)
}

// ResolveName resolves a name to a peer ID
func (n *Network) ResolveName(name string) (peer.ID, error) {
	return n.nameResolver.Resolve(name)
}

// RegisterName registers a local name mapping
func (n *Network) RegisterName(name string, peerID peer.ID) error {
	return n.nameResolver.Register(name, peerID)
}

// LoadNames loads name-to-peer-ID mappings from a string map (e.g., from YAML config)
func (n *Network) LoadNames(names map[string]string) error {
	return n.nameResolver.LoadFromMap(names)
}

// AddRelayAddressesForPeer adds relay circuit addresses for a target peer to the peerstore.
// This allows the client to reach the target peer through the configured relay servers.
func (n *Network) AddRelayAddressesForPeer(relayAddrs []string, targetPeerID peer.ID) error {
	for _, relayAddr := range relayAddrs {
		circuitAddr := relayAddr + "/p2p-circuit/p2p/" + targetPeerID.String()
		addrInfo, err := peer.AddrInfoFromString(circuitAddr)
		if err != nil {
			return fmt.Errorf("failed to parse relay circuit address %s: %w", circuitAddr, err)
		}
		n.host.Peerstore().AddAddrs(addrInfo.ID, addrInfo.Addrs, peerstore.PermanentAddrTTL)
	}
	return nil
}

// OnEvent registers an event handler. Returns a function to unsubscribe.
func (n *Network) OnEvent(handler EventHandler) func() {
	return n.events.Subscribe(handler)
}

// Events returns the event bus for emitting events from external code
// (e.g., daemon, relay server). Not part of PeerNetwork interface.
func (n *Network) Events() *EventBus {
	return n.events
}

// Close shuts down the network
func (n *Network) Close() error {
	n.cancel()
	return n.host.Close()
}

// ParseRelayAddrs parses relay multiaddrs into peer.AddrInfo slices.
// It deduplicates by peer ID and merges addresses for the same relay peer.
func ParseRelayAddrs(relayAddrs []string) ([]peer.AddrInfo, error) {
	var infos []peer.AddrInfo
	seen := make(map[peer.ID]bool)

	for _, s := range relayAddrs {
		maddr, err := ma.NewMultiaddr(s)
		if err != nil {
			return nil, fmt.Errorf("invalid relay addr %s: %w", s, err)
		}

		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			return nil, fmt.Errorf("cannot parse relay addr %s: %w", s, err)
		}

		if !seen[ai.ID] {
			seen[ai.ID] = true
			infos = append(infos, *ai)
		} else {
			// Merge addrs for same peer
			for i := range infos {
				if infos[i].ID == ai.ID {
					infos[i].Addrs = append(infos[i].Addrs, ai.Addrs...)
				}
			}
		}
	}

	return infos, nil
}
