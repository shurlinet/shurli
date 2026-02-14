package p2pnet

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
)

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
	AuthorizedKeys  string
	Config          *config.Config

	// Relay configuration (optional)
	EnableRelay         bool              // Enable relay support (AutoRelay + hole punching)
	RelayAddrs          []string          // Relay server multiaddrs (e.g., "/ip4/1.2.3.4/tcp/7777/p2p/12D3Koo...")
	ForcePrivate        bool              // Force private reachability (required for relay reservations)
	EnableNATPortMap    bool              // Enable NAT port mapping
	EnableHolePunching  bool              // Enable hole punching
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

	// Create libp2p host options
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(ws.New),
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
			hostOpts = append(hostOpts, libp2p.EnableHolePunching())
		}

		if cfg.ForcePrivate {
			hostOpts = append(hostOpts, libp2p.ForceReachabilityPrivate())
		}
	}

	// Add connection gater if authorized_keys provided
	if cfg.AuthorizedKeys != "" {
		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.AuthorizedKeys)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("failed to load authorized_keys: %w", err)
		}

		logger := log.New(log.Writer(), "[p2pnet] ", log.LstdFlags)
		gater := auth.NewAuthorizedPeerGater(authorizedPeers, logger)
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
		serviceRegistry: NewServiceRegistry(h),
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

// ExposeService exposes a local TCP service through the P2P network
func (n *Network) ExposeService(name, localAddress string) error {
	if err := ValidateServiceName(name); err != nil {
		return err
	}
	return n.serviceRegistry.RegisterService(&Service{
		Name:         name,
		Protocol:     fmt.Sprintf("/peerup/%s/1.0.0", name),
		LocalAddress: localAddress,
		Enabled:      true,
	})
}

// ConnectToService connects to a remote peer's service
func (n *Network) ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error) {
	if err := ValidateServiceName(serviceName); err != nil {
		return nil, err
	}
	protocol := fmt.Sprintf("/peerup/%s/1.0.0", serviceName)
	return n.serviceRegistry.DialService(n.ctx, peerID, protocol)
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
