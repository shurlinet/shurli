package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/relay"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// remoteAdminConnection holds a RemoteAdminClient and its cleanup function.
// Call cleanup() when done (shuts down the P2P host).
type remoteAdminConnection struct {
	client  *relay.RemoteAdminClient
	network *p2pnet.Network
}

func (c *remoteAdminConnection) Close() {
	if c.network != nil {
		c.network.Close()
	}
}

// connectRemoteRelay creates a minimal P2P host, connects to the specified
// relay, and returns a RemoteAdminClient for issuing admin commands over P2P.
//
// The remoteAddr can be:
//   - Full multiaddr: /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...
//   - Short name: my-relay (resolved via config names)
//   - Raw peer ID: 12D3KooW... (matched to relay address in config)
func connectRemoteRelay(remoteAddr string) (*remoteAdminConnection, error) {
	_, cfg := resolveConfigFile("")

	// Resolve the relay address.
	resolvedAddr, err := resolveRelayAddr(remoteAddr, cfg)
	if err != nil {
		return nil, err
	}

	peerInfo, err := peer.AddrInfoFromString(resolvedAddr)
	if err != nil {
		return nil, fmt.Errorf("invalid relay address: %w", err)
	}

	fmt.Fprintf(os.Stderr, "Connecting to relay: %s\n", truncateAddr(resolvedAddr))

	// Resolve password for SHRL-encrypted identity key.
	pw, err := resolvePassword(filepath.Dir(cfg.Identity.KeyFile))
	if err != nil {
		// For remote admin, password is needed to unlock identity. If no session,
		// allow empty password (will fail at key load if key is encrypted).
		pw = ""
	}

	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		KeyPassword:        pw,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "shurli/" + version,
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create P2P host: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := p2pNetwork.Host().Connect(ctx, *peerInfo); err != nil {
		p2pNetwork.Close()
		return nil, fmt.Errorf("failed to connect to relay: %w", err)
	}

	client := relay.NewRemoteAdminClient(p2pNetwork.Host(), peerInfo.ID)
	return &remoteAdminConnection{
		client:  client,
		network: p2pNetwork,
	}, nil
}

// relayAdminClientOrRemote returns either a local AdminClient or a remote
// RemoteAdminClient based on whether remoteAddr is set.
// Returns the client (satisfying RelayAdminAPI), and a cleanup function.
func relayAdminClientOrRemote(remoteAddr, configFile string) (relay.RelayAdminAPI, func(), error) {
	if remoteAddr != "" {
		conn, err := connectRemoteRelay(remoteAddr)
		if err != nil {
			return nil, nil, err
		}
		return conn.client, conn.Close, nil
	}

	client, err := relayAdminClient(configFile)
	if err != nil {
		return nil, nil, err
	}
	return client, func() {}, nil
}
