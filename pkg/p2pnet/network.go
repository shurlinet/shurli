package p2pnet

import (
	"context"
	"fmt"
	"log"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
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
}

// New creates a new P2P network instance
func New(cfg *Config) (*Network, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config cannot be nil")
	}

	ctx, cancel := context.WithCancel(context.Background())

	// Load identity
	priv, err := loadOrCreateIdentity(cfg.KeyFile)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("failed to load identity: %w", err)
	}

	// Create libp2p host options
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
	}

	// Add listen addresses if configured
	if cfg.Config != nil && len(cfg.Config.Network.ListenAddresses) > 0 {
		hostOpts = append(hostOpts, libp2p.ListenAddrStrings(cfg.Config.Network.ListenAddresses...))
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
	return n.serviceRegistry.RegisterService(&Service{
		Name:         name,
		Protocol:     fmt.Sprintf("/peerup/%s/1.0.0", name),
		LocalAddress: localAddress,
		Enabled:      true,
	})
}

// ConnectToService connects to a remote peer's service
func (n *Network) ConnectToService(peerID peer.ID, serviceName string) (ServiceConn, error) {
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

// Close shuts down the network
func (n *Network) Close() error {
	n.cancel()
	return n.host.Close()
}
