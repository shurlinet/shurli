package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runPing(args []string) {
	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: peerup ping [--config <path>] <target>")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  peerup ping home")
		fmt.Println("  peerup ping 12D3KooWLutPZ...")
		os.Exit(1)
	}

	target := remaining[0]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("=== Ping via P2P ===")
	fmt.Println()

	// Find and load configuration
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	fmt.Printf("Config: %s\n", cfgFile)
	fmt.Println()

	// Create P2P network
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		log.Fatalf("P2P network error: %v", err)
	}
	defer p2pNetwork.Close()

	// Load names from config
	if cfg.Names != nil {
		if err := p2pNetwork.LoadNames(cfg.Names); err != nil {
			log.Printf("Failed to load names: %v", err)
		}
	}

	// Resolve target
	targetPeerID, err := p2pNetwork.ResolveName(target)
	if err != nil {
		log.Fatalf("Cannot resolve target %q: %v", target, err)
	}

	h := p2pNetwork.Host()

	fmt.Printf("Client Peer ID: %s\n", h.ID())
	fmt.Printf("Target Peer: %s\n", targetPeerID)
	if target != targetPeerID.String() {
		fmt.Printf("   (resolved from name %q)\n", target)
	}
	fmt.Println()

	// Bootstrap DHT
	fmt.Println("Bootstrapping into the DHT...")
	kdht, err := dht.New(ctx, h, dht.Mode(dht.ModeClient))
	if err != nil {
		log.Fatalf("DHT error: %v", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		log.Fatalf("DHT bootstrap error: %v", err)
	}

	// Connect to bootstrap peers
	var bootstrapPeers []ma.Multiaddr
	if len(cfg.Discovery.BootstrapPeers) > 0 {
		for _, addr := range cfg.Discovery.BootstrapPeers {
			maddr, err := ma.NewMultiaddr(addr)
			if err != nil {
				fmt.Printf("Invalid bootstrap peer %s: %v\n", addr, err)
				continue
			}
			bootstrapPeers = append(bootstrapPeers, maddr)
		}
	} else {
		bootstrapPeers = dht.DefaultBootstrapPeers
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
	fmt.Printf("Connected to %d bootstrap peers\n", connected.Load())

	// Connect to relay
	relayInfos, err := p2pnet.ParseRelayAddrs(cfg.Relay.Addresses)
	if err != nil {
		log.Fatalf("Failed to parse relay addresses: %v", err)
	}
	fmt.Println("Connecting to dedicated relay...")
	for _, ai := range relayInfos {
		if err := h.Connect(ctx, ai); err != nil {
			fmt.Printf("Could not connect to relay: %v\n", err)
		} else {
			fmt.Printf("Connected to relay %s\n", ai.ID.String()[:16])
		}
	}
	fmt.Println()

	// Search for the target via DHT
	routingDiscovery := drouting.NewRoutingDiscovery(kdht)

	fmt.Println("Searching for target peer via rendezvous discovery...")
	fmt.Println("   (This can take 30-60 seconds)")

	// Method 1: Rendezvous discovery
	var targetAddrInfo *peer.AddrInfo
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 60*time.Second)

	peerCh, err := routingDiscovery.FindPeers(discoverCtx, cfg.Discovery.Rendezvous)
	if err != nil {
		fmt.Printf("Rendezvous discovery error: %v\n", err)
	} else {
		for p := range peerCh {
			if p.ID == targetPeerID && len(p.Addrs) > 0 {
				fmt.Printf("Found target peer via rendezvous!\n")
				targetAddrInfo = &p
				break
			}
			if p.ID == targetPeerID && len(p.Addrs) == 0 {
				fmt.Printf("Found peer via rendezvous but no addresses yet\n")
			}
			if p.ID != h.ID() && p.ID != targetPeerID {
				fmt.Printf("   Found peer %s (not our target)\n", p.ID.String()[:16])
			}
		}
	}
	discoverCancel()

	// Method 2: Direct DHT lookup
	if targetAddrInfo == nil || len(targetAddrInfo.Addrs) == 0 {
		fmt.Println()
		fmt.Println("Trying direct DHT peer routing lookup...")
		lookupCtx, lookupCancel := context.WithTimeout(ctx, 60*time.Second)
		pi, err := kdht.FindPeer(lookupCtx, targetPeerID)
		lookupCancel()
		if err != nil {
			fmt.Printf("Could not find target peer: %v\n", err)
			fmt.Println()
			fmt.Println("Make sure the server is running and has had time to")
			fmt.Println("register with the DHT (give it at least 3-5 minutes).")
			os.Exit(1)
		}
		targetAddrInfo = &pi
		fmt.Printf("Found target peer via DHT lookup! (%d addresses)\n", len(pi.Addrs))
	}

	// Show discovered addresses
	fmt.Println()
	fmt.Println("Target peer addresses:")
	for _, addr := range targetAddrInfo.Addrs {
		label := "direct"
		if strings.Contains(addr.String(), "p2p-circuit") {
			label = "relay"
		}
		fmt.Printf("  [%s] %s\n", label, addr)
	}

	// Connect to the target
	fmt.Println()
	fmt.Println("Connecting to target peer...")

	connectCtx, connectCancel := context.WithTimeout(ctx, 15*time.Second)
	err = h.Connect(connectCtx, *targetAddrInfo)
	connectCancel()

	if err != nil {
		fmt.Printf("Direct connection failed: %v\n", err)
		fmt.Println()
		fmt.Println("Trying via relay circuit...")

		// Add relay addresses for the target peer
		if err := p2pNetwork.AddRelayAddressesForPeer(cfg.Relay.Addresses, targetPeerID); err != nil {
			log.Fatalf("Failed to add relay addresses: %v", err)
		}

		// Retry connection
		connectCtx2, connectCancel2 := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx2, peer.AddrInfo{ID: targetPeerID})
		connectCancel2()
		if err != nil {
			log.Fatalf("Failed to connect via relay: %v", err)
		}
	}

	// Check connection type
	conns := h.Network().ConnsToPeer(targetPeerID)
	for _, conn := range conns {
		connType := "DIRECT"
		if conn.Stat().Limited {
			connType = "RELAYED"
		}
		fmt.Printf("Connected! [%s] via %s\n", connType, conn.RemoteMultiaddr())
	}

	// Send ping
	fmt.Println()
	fmt.Println("Sending PING...")
	streamCtx, streamCancel := context.WithTimeout(ctx, 15*time.Second)
	s, err := h.NewStream(streamCtx, targetPeerID, protocol.ID(cfg.Protocols.PingPong.ID))
	streamCancel()
	if err != nil {
		log.Fatalf("Failed to open stream: %v", err)
	}

	streamConnType := "DIRECT"
	if s.Conn().Stat().Limited {
		streamConnType = "RELAYED"
	}
	fmt.Printf("   Stream connection type: %s\n", streamConnType)

	_, err = s.Write([]byte("ping\n"))
	if err != nil {
		log.Fatalf("Failed to send ping: %v", err)
	}

	reader := bufio.NewReader(s)
	response, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("Failed to read response: %v", err)
	}
	response = strings.TrimSpace(response)

	fmt.Printf("\nResponse: %s\n", response)
	fmt.Printf("   Connection: %s\n", streamConnType)

	s.Close()

	// If connected via relay, wait to see if hole-punching upgrades
	if streamConnType == "RELAYED" {
		fmt.Println()
		fmt.Println("Connected via relay. Waiting 15s to see if hole-punching upgrades to direct...")
		select {
		case <-ctx.Done():
			return
		case <-time.After(15 * time.Second):
		}

		conns = h.Network().ConnsToPeer(targetPeerID)
		upgraded := false
		for _, conn := range conns {
			if !conn.Stat().Limited {
				fmt.Printf("Hole-punch SUCCESS! Direct connection via %s\n", conn.RemoteMultiaddr())
				upgraded = true
			}
		}
		if !upgraded {
			fmt.Println("Still relayed. Hole-punching didn't upgrade in time.")
			fmt.Println("   This is normal with Starlink CGNAT.")
		}

		// Second ping over potentially upgraded connection
		fmt.Println()
		fmt.Println("Sending second PING (may use upgraded connection)...")
		s2Ctx, s2Cancel := context.WithTimeout(ctx, 15*time.Second)
		s2, err := h.NewStream(s2Ctx, targetPeerID, protocol.ID(cfg.Protocols.PingPong.ID))
		s2Cancel()
		if err != nil {
			fmt.Printf("Second stream failed: %v\n", err)
		} else {
			connType2 := "DIRECT"
			if s2.Conn().Stat().Limited {
				connType2 = "RELAYED"
			}
			s2.Write([]byte("ping\n"))
			reader2 := bufio.NewReader(s2)
			resp2, err := reader2.ReadString('\n')
			if err == nil {
				fmt.Printf("Response: %s [%s]\n", strings.TrimSpace(resp2), connType2)
			}
			s2.Close()
		}
	}

	fmt.Println()
	fmt.Println("Done!")
}
