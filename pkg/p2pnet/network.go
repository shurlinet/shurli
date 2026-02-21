package p2pnet

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
)

// DHTProtocolPrefix is the default protocol prefix for the private peerup Kademlia DHT.
// This isolates peerup from the public IPFS Amino DHT (/ipfs/kad/1.0.0),
// giving us our own routing table at /peerup/kad/1.0.0.
// For namespace-specific prefixes, use DHTProtocolPrefixForNamespace.
const DHTProtocolPrefix = "/peerup"

// DHTProtocolPrefixForNamespace returns the DHT protocol prefix for a given
// network namespace. An empty namespace returns the default global prefix ("/peerup").
// A non-empty namespace produces "/peerup/<namespace>" which results in the
// full DHT protocol "/peerup/<namespace>/kad/1.0.0", completely isolated from
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
	ctx             context.Context
	cancel          context.CancelFunc
}

// Config for creating a new P2P network
type Config struct {
	KeyFile         string
	AuthorizedKeys  string                       // Path to authorized_keys file (auto-creates gater if Gater is nil)
	Gater           *auth.AuthorizedPeerGater     // Pre-created gater (for hot-reload support). Takes precedence over AuthorizedKeys.
	Config          *config.Config
	UserAgent       string                        // libp2p Identify user agent (e.g. "peerup/0.1.0")

	// Relay configuration (optional)
	EnableRelay         bool              // Enable relay support (AutoRelay + hole punching)
	RelayAddrs          []string          // Relay server multiaddrs (e.g., "/ip4/1.2.3.4/tcp/7777/p2p/12D3Koo...")
	ForcePrivate        bool              // Force private reachability (required for relay reservations)
	EnableNATPortMap    bool              // Enable NAT port mapping
	EnableHolePunching  bool              // Enable hole punching

	// Resource management
	ResourceLimitsEnabled bool            // Enable libp2p resource manager (connection/stream/memory limits)

	// Observability
	Metrics *Metrics // Custom peerup metrics (nil = disabled). When non-nil, libp2p metrics are registered on Metrics.Registry.
}

// New creates a new P2P network instance
func New(cfg *Config) (*Network, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Load identity
	priv, err := LoadOrCreateIdentity(cfg.KeyFile)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	// Create libp2p host options.
	// Transport order: QUIC first (3 RTTs, native multiplexing, better hole-punching),
	// TCP second (4 RTTs, universal fallback), WebSocket last (anti-censorship/DPI evasion).
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
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

	if cfg.UserAgent != "" {
		hostOpts = append(hostOpts, libp2p.UserAgent(cfg.UserAgent))
	}

	// Add listen addresses if configured
	if cfg.Config != nil && len(cfg.Config.Network.ListenAddresses) > 0 {
		hostOpts = append(hostOpts, libp2p.ListenAddrStrings(cfg.Config.Network.ListenAddresses...))
	}

	// Add relay support if enabled
	if cfg.EnableRelay {
		// Parse relay addresses
		relayInfos, err := ParseRelayAddrs(cfg.RelayAddrs)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to parse relay addresses: %w", err)
		}

		if len(relayInfos) > 0 {
			hostOpts = append(hostOpts, libp2p.EnableAutoRelayWithStaticRelays(relayInfos))
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

	// Create libp2p host
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to create libp2p host: %w", err)
	}

	net := &Network{
		host:            h,
		config:          cfg.Config,
		serviceRegistry: NewServiceRegistry(h, cfg.Metrics),
		nameResolver:    NewNameResolver(),
		ctx:             ctx,
		cancel:          cancel,
	}

	return net, nil
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
		Protocol:     fmt.Sprintf("/peerup/%s/1.0.0", name),
		LocalAddress: localAddress,
		Enabled:      true,
		AllowedPeers: allowedPeers,
	})
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
	protocol := fmt.Sprintf("/peerup/%s/1.0.0", serviceName)
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
