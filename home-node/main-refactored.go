package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("=== Home Node (Refactored with pkg/p2pnet) ===")
	fmt.Println()

	// Load configuration
	cfg, err := config.LoadHomeNodeConfig("home-node.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v\n", err)
		fmt.Println("Please create home-node.yaml from the sample:")
		fmt.Println("  cp configs/home-node.sample.yaml home-node.yaml")
		fmt.Println("  # Edit the file with your relay server details")
		os.Exit(1)
	}

	// Validate configuration
	if err := config.ValidateHomeNodeConfig(cfg); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	fmt.Printf("Loaded configuration from home-node.yaml\n")
	fmt.Printf("Rendezvous: %s\n", cfg.Discovery.Rendezvous)
	fmt.Println()

	// Create P2P network using pkg/p2pnet with relay support
	var authorizedKeysFile string
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			log.Fatalf("Connection gating enabled but no authorized_keys_file specified")
		}
		authorizedKeysFile = cfg.Security.AuthorizedKeysFile
		fmt.Printf("✅ Loading authorized peers from %s\n", authorizedKeysFile)
	} else {
		fmt.Println("⚠️  WARNING: Connection gating is DISABLED - any peer can connect!")
	}
	fmt.Println()

	net, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		AuthorizedKeys:     authorizedKeysFile,
		Config:             &config.Config{Network: cfg.Network},
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		log.Fatalf("Failed to create P2P network: %v", err)
	}
	defer net.Close()

	// Get underlying libp2p host for advanced features
	h := net.Host()

	fmt.Printf("🏠 Peer ID: %s\n", h.ID())
	fmt.Println()

	// Parse relay addresses for manual connection
	relayInfos := parseRelayAddrs(cfg.Relay.Addresses)

	// Connect to the relay
	for _, ai := range relayInfos {
		if err := h.Connect(ctx, ai); err != nil {
			fmt.Printf("⚠️  Could not connect to relay %s: %v\n", ai.ID.String()[:16], err)
		} else {
			fmt.Printf("✅ Connected to relay %s\n", ai.ID.String()[:16])
		}
	}

	// Give AutoRelay a moment to make reservations
	fmt.Println("Waiting for AutoRelay to establish reservations...")
	time.Sleep(5 * time.Second)

	// Check if we got relay addresses
	hasRelay := false
	for _, addr := range h.Addrs() {
		if strings.Contains(addr.String(), "p2p-circuit") {
			fmt.Printf("✅ Relay address: %s\n", addr)
			hasRelay = true
		}
	}
	if !hasRelay {
		fmt.Println("⚠️  No relay addresses yet — trying manual reservation...")
		for _, ai := range relayInfos {
			_, err := circuitv2client.Reserve(ctx, h, ai)
			if err != nil {
				fmt.Printf("⚠️  Manual reservation failed: %v\n", err)
			} else {
				fmt.Printf("✅ Manual relay reservation active on %s\n", ai.ID.String()[:16])
			}
		}
	}

	// Keep reservation alive
	go func() {
		for {
			time.Sleep(cfg.Relay.ReservationInterval)
			for _, ai := range relayInfos {
				h.Connect(ctx, ai)
				circuitv2client.Reserve(ctx, h, ai)
			}
		}
	}()

	// Expose services from config
	if cfg.Services != nil {
		for name, svc := range cfg.Services {
			if svc.Enabled {
				fmt.Printf("📡 Exposing service: %s -> %s\n", name, svc.LocalAddress)
				if err := net.ExposeService(name, svc.LocalAddress); err != nil {
					log.Printf("⚠️  Failed to expose service %s: %v", name, err)
				}
			}
		}
	}
	fmt.Println()

	// Set up the ping-pong handler
	h.SetStreamHandler(protocol.ID(cfg.Protocols.PingPong.ID), func(s network.Stream) {
		remotePeer := s.Conn().RemotePeer()

		connType := "unknown"
		if s.Conn().Stat().Limited {
			connType = "RELAYED"
		} else {
			connType = "DIRECT"
		}
		fmt.Printf("\n📨 Incoming stream from %s [%s]\n", remotePeer.String()[:16], connType)

		reader := bufio.NewReader(s)
		msg, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("   Read error: %v\n", err)
			s.Close()
			return
		}
		msg = strings.TrimSpace(msg)
		fmt.Printf("   Received: %s\n", msg)

		if msg == "ping" {
			fmt.Println("   🏓 PONG!")
			s.Write([]byte("pong\n"))
		} else {
			fmt.Printf("   Unknown message: %s\n", msg)
			s.Write([]byte("unknown\n"))
		}
		s.Close()
	})

	// Bootstrap the DHT
	fmt.Println("Bootstrapping into the DHT...")
	kdht, err := dht.New(ctx, h, dht.Mode(dht.ModeAutoServer))
	if err != nil {
		log.Fatalf("DHT error: %v", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		log.Fatalf("DHT bootstrap error: %v", err)
	}

	// Connect to bootstrap peers
	var bootstrapPeers []ma.Multiaddr
	if len(cfg.Discovery.BootstrapPeers) > 0 {
		// Use custom bootstrap peers from config
		for _, addr := range cfg.Discovery.BootstrapPeers {
			maddr, err := ma.NewMultiaddr(addr)
			if err != nil {
				fmt.Printf("⚠️  Invalid bootstrap peer %s: %v\n", addr, err)
				continue
			}
			bootstrapPeers = append(bootstrapPeers, maddr)
		}
	} else {
		// Use default libp2p bootstrap peers
		bootstrapPeers = dht.DefaultBootstrapPeers
	}

	var wg sync.WaitGroup
	connected := 0
	for _, pAddr := range bootstrapPeers {
		pi, err := peer.AddrInfoFromP2pAddr(pAddr)
		if err != nil {
			continue
		}
		wg.Add(1)
		go func(pi peer.AddrInfo) {
			defer wg.Done()
			if err := h.Connect(ctx, pi); err == nil {
				connected++
			}
		}(*pi)
	}
	wg.Wait()
	fmt.Printf("Connected to %d bootstrap peers\n", connected)

	// Advertise ourselves on the DHT using a rendezvous string
	routingDiscovery := drouting.NewRoutingDiscovery(kdht)
	fmt.Printf("Advertising on rendezvous: %s\n", cfg.Discovery.Rendezvous)

	// Keep advertising in the background
	go func() {
		for {
			_, err := routingDiscovery.Advertise(ctx, cfg.Discovery.Rendezvous)
			if err != nil {
				fmt.Printf("Advertise error: %v\n", err)
			}
			time.Sleep(time.Minute)
		}
	}()

	// Periodically print status
	go func() {
		time.Sleep(10 * time.Second) // initial wait
		for {
			fmt.Println()
			fmt.Println("--- Status ---")
			fmt.Printf("Peer ID: %s\n", h.ID())
			fmt.Printf("Connected peers: %d\n", len(h.Network().Peers()))
			fmt.Println("Addresses:")
			for _, addr := range h.Addrs() {
				label := "local"
				addrStr := addr.String()
				if strings.Contains(addrStr, "/p2p-circuit") {
					label = "RELAY ✅"
				} else if !strings.Contains(addrStr, "/ip4/10.") &&
					!strings.Contains(addrStr, "/ip4/192.168.") &&
					!strings.Contains(addrStr, "/ip4/127.") &&
					!strings.Contains(addrStr, "/ip6/::1") &&
					!strings.Contains(addrStr, "/ip6/fe80") &&
					!strings.Contains(addrStr, "/ip6/fd") {
					label = "public"
				}
				fmt.Printf("  [%s] %s\n", label, addrStr)
			}
			fmt.Println("--------------")
			time.Sleep(30 * time.Second)
		}
	}()

	fmt.Println()
	fmt.Println("✅ Home node is running and waiting for pings!")
	fmt.Println("   Share your Peer ID with the client/phone app.")
	fmt.Println("   Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Println("\nShutting down...")
}

func parseRelayAddrs(relayAddrs []string) []peer.AddrInfo {
	var infos []peer.AddrInfo
	seen := make(map[peer.ID]bool)
	for _, s := range relayAddrs {
		maddr, err := ma.NewMultiaddr(s)
		if err != nil {
			fmt.Printf("⚠️  Invalid relay addr %s: %v\n", s, err)
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			fmt.Printf("⚠️  Cannot parse relay addr %s: %v\n", s, err)
			continue
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
	return infos
}
