package sdk

import (
	"context"
	"fmt"
	"sync"
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
	for _, pAddr := range bootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(pAddr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			h.Connect(ctx, pi) //nolint:errcheck // best-effort bootstrap
		}(*pi)
	}
	wg.Wait()

	// Connect to relay servers - race all in parallel with staggered starts (TS-2).
	// Each relay is an independent path. First connection unblocks DHT routing,
	// no need to wait for slow relays sequentially.
	relayInfos, err := ParseRelayAddrs(cfg.RelayAddrs)
	if err != nil {
		return fmt.Errorf("relay address parse error: %w", err)
	}
	if len(relayInfos) > 0 {
		var relayWg sync.WaitGroup
		relayCtx, relayCancel := context.WithTimeout(ctx, 30*time.Second)
		relayConnected := make(chan struct{}, 1) // signal first success
		relayAllDone := make(chan struct{})      // signal all goroutines finished
		for i, ai := range relayInfos {
			relayWg.Add(1)
			go func(idx int, info peer.AddrInfo) {
				defer relayWg.Done()
				// Staggered start: 100ms apart to prevent thundering herd.
				if idx > 0 {
					stagger := time.NewTimer(time.Duration(idx) * 100 * time.Millisecond)
					select {
					case <-relayCtx.Done():
						stagger.Stop()
						return
					case <-stagger.C:
					}
				}
				if err := h.Connect(relayCtx, info); err == nil {
					select {
					case relayConnected <- struct{}{}:
					default:
					}
				}
			}(i, ai)
		}
		// Background: signal when all goroutines finish, then cancel context.
		go func() {
			relayWg.Wait()
			close(relayAllDone)
			relayCancel()
		}()
		// Proceed once first relay connects, all finish, or timeout.
		// Do NOT cancel on first success - additional relay connections
		// give the DHT more routing options. Goroutines continue in
		// background, bounded by the 30s relayCtx timeout.
		select {
		case <-relayConnected:
		case <-relayAllDone:
		case <-relayCtx.Done():
		}
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
