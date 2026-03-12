package p2pnet

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// BootstrapConfig configures standalone bootstrap for one-shot CLI commands
// (ping, traceroute) that create a temporary P2P host without a full daemon.
type BootstrapConfig struct {
	// Namespace is the DHT namespace (empty = global "/shurli/kad/1.0.0").
	Namespace string

	// BootstrapPeers are explicit bootstrap peer multiaddrs from config.
	// When empty, RelayAddrs are used as DHT bootstrap peers.
	BootstrapPeers []string

	// RelayAddrs are relay server multiaddrs used for circuit relay
	// fallback and (when BootstrapPeers is empty) DHT bootstrapping.
	RelayAddrs []string
}

// BootstrapAndConnect bootstraps the DHT in client mode and connects to
// the target peer. It tries DHT discovery first, then falls back to relay
// circuit addresses. This is the library-level bootstrap for standalone
// commands and SDK consumers that operate without a daemon.
func BootstrapAndConnect(ctx context.Context, h host.Host, net *Network, target peer.ID, cfg BootstrapConfig) error {
	// Bootstrap DHT in client mode.
	dhtPrefix := DHTProtocolPrefixForNamespace(cfg.Namespace)
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeClient),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
		dht.RoutingTablePeerDiversityFilter(dht.NewRTPeerDiversityFilter(h, 3, 50)),
	)
	if err != nil {
		return fmt.Errorf("DHT error: %w", err)
	}
	defer kdht.Close()
	if err := kdht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("DHT bootstrap error: %w", err)
	}

	// Connect to bootstrap peers (config peers or relay addrs as fallback).
	var bootstrapPeers []ma.Multiaddr
	sources := cfg.BootstrapPeers
	if len(sources) == 0 {
		sources = cfg.RelayAddrs
	}
	for _, addr := range sources {
		maddr, err := ma.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		bootstrapPeers = append(bootstrapPeers, maddr)
	}

	var wg sync.WaitGroup
	var connected atomic.Int32
	for _, pAddr := range bootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(pAddr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := h.Connect(ctx, pi); err == nil {
				connected.Add(1)
			}
		}(*pi)
	}
	wg.Wait()

	// Connect to relay servers.
	relayInfos, err := ParseRelayAddrs(cfg.RelayAddrs)
	if err != nil {
		return fmt.Errorf("relay address parse error: %w", err)
	}
	for _, ai := range relayInfos {
		h.Connect(ctx, ai)
	}

	// Find target via DHT.
	findCtx, findCancel := context.WithTimeout(ctx, 60*time.Second)
	pi, err := kdht.FindPeer(findCtx, target)
	findCancel()
	if err != nil {
		// Peer not in DHT - try connecting via relay circuit.
		if err := net.AddRelayAddressesForPeer(cfg.RelayAddrs, target); err != nil {
			return fmt.Errorf("failed to add relay addresses: %w", err)
		}
		connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx, peer.AddrInfo{ID: target})
		connectCancel()
		if err != nil {
			return fmt.Errorf("cannot connect to peer: %w", err)
		}
		return nil
	}

	// Connect using DHT-discovered addresses.
	connectCtx, connectCancel := context.WithTimeout(ctx, 15*time.Second)
	err = h.Connect(connectCtx, pi)
	connectCancel()
	if err != nil {
		// Fallback to relay circuit.
		if err := net.AddRelayAddressesForPeer(cfg.RelayAddrs, target); err != nil {
			return fmt.Errorf("failed to add relay addresses: %w", err)
		}
		connectCtx2, connectCancel2 := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx2, peer.AddrInfo{ID: target})
		connectCancel2()
		if err != nil {
			return fmt.Errorf("cannot connect to peer: %w", err)
		}
	}

	return nil
}
