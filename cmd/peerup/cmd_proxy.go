package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runProxy(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 3 {
		fmt.Println("Usage: peerup proxy [--config <path>] <target> <service> <local-port>")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  peerup proxy home ssh 2222")
		fmt.Println("  peerup proxy home xrdp 13389")
		fmt.Println("  peerup proxy --config /path/to/config.yaml home ssh 2222")
		osExit(1)
	}

	target := remaining[0]
	serviceName := remaining[1]
	localPort := remaining[2]

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Find and load config
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fatal("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	fmt.Printf("=== TCP Proxy via P2P ===\n")
	fmt.Printf("Config: %s\n", cfgFile)
	fmt.Printf("Service: %s\n", serviceName)
	fmt.Println()

	// Create P2P network
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "peerup/" + version,
		EnableRelay:        true,
		RelayAddrs:         cfg.Relay.Addresses,
		ForcePrivate:       cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		fatal("P2P network error: %v", err)
	}
	defer p2pNetwork.Close()

	// Load names from config for resolution
	if cfg.Names != nil {
		if err := p2pNetwork.LoadNames(cfg.Names); err != nil {
			log.Printf("Failed to load names: %v", err)
		}
	}

	// Resolve target (name or peer ID)
	homePeerID, err := p2pNetwork.ResolveName(target)
	if err != nil {
		fatal("Cannot resolve target %q: %v", target, err)
	}

	h := p2pNetwork.Host()

	fmt.Printf("Client Peer ID: %s\n", p2pNetwork.PeerID())
	fmt.Printf("Target Peer: %s\n", homePeerID)
	if target != homePeerID.String() {
		fmt.Printf("   (resolved from name %q)\n", target)
	}
	fmt.Println()

	// Add relay circuit addresses for the home peer
	fmt.Println("Connecting to target peer via relay...")
	if err := p2pNetwork.AddRelayAddressesForPeer(cfg.Relay.Addresses, homePeerID); err != nil {
		fatal("Failed to add relay addresses: %v", err)
	}

	// Bootstrap DHT for direct connection discovery (DCUtR hole-punching).
	// This runs in the background  - if it finds the target peer's direct
	// addresses, libp2p will prefer them over the relay circuit.
	fmt.Println("Bootstrapping DHT for direct connection discovery...")
	dhtPrefix := p2pnet.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeClient),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
	)
	if err != nil {
		log.Printf("DHT init failed (relay-only mode): %v", err)
	} else {
		if err := kdht.Bootstrap(ctx); err != nil {
			log.Printf("DHT bootstrap failed (relay-only mode): %v", err)
		} else {
			// Connect to bootstrap peers in background
			var bootstrapPeers []ma.Multiaddr
			if len(cfg.Discovery.BootstrapPeers) > 0 {
				for _, addr := range cfg.Discovery.BootstrapPeers {
					maddr, err := ma.NewMultiaddr(addr)
					if err != nil {
						continue
					}
					bootstrapPeers = append(bootstrapPeers, maddr)
				}
			} else {
				// Use relay addresses as DHT bootstrap peers.
				for _, addr := range cfg.Relay.Addresses {
					maddr, err := ma.NewMultiaddr(addr)
					if err != nil {
						continue
					}
					bootstrapPeers = append(bootstrapPeers, maddr)
				}
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
					connectCtx, connectCancel := context.WithTimeout(ctx, 10*time.Second)
					defer connectCancel()
					if err := h.Connect(connectCtx, pi); err == nil {
						connected.Add(1)
					}
				}(*pi)
			}
			wg.Wait()
			fmt.Printf("Connected to %d bootstrap peers\n", connected.Load())

			// Try to find target peer's addresses via DHT (async  - doesn't block proxy startup)
			go func() {
				findCtx, findCancel := context.WithTimeout(ctx, 30*time.Second)
				defer findCancel()
				pi, err := kdht.FindPeer(findCtx, homePeerID)
				if err != nil {
					log.Printf("DHT peer discovery: target not found (using relay)")
					return
				}
				log.Printf("DHT found target peer with %d addresses  - direct connection possible", len(pi.Addrs))
				// Add discovered addresses to peerstore so libp2p can try direct connection
				h.Peerstore().AddAddrs(pi.ID, pi.Addrs, time.Hour)
			}()
		}
	}
	fmt.Println()

	// Create TCP listener with retry-enabled dial function.
	// Each incoming TCP connection triggers a P2P stream dial with
	// exponential backoff (3 retries: 1s, 2s, 4s) to handle transient
	// relay disconnections without failing the user's connection.
	localAddr := fmt.Sprintf("localhost:%s", localPort)
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		return p2pNetwork.ConnectToService(homePeerID, serviceName)
	}, 3)
	listener, err := p2pnet.NewTCPListener(localAddr, dialFunc)
	if err != nil {
		fatal("Failed to create listener: %v", err)
	}
	defer listener.Close()

	fmt.Printf("TCP proxy listening on %s\n", localAddr)
	fmt.Println()
	fmt.Println("Connect to the service:")
	fmt.Printf("   %s -> %s service on target\n", localAddr, serviceName)
	fmt.Println("\nPress Ctrl+C to stop.")
	fmt.Println()

	// Handle graceful shutdown
	shutdownCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		close(shutdownCh)
		cancel()         // stop DHT and background goroutines
		listener.Close() // causes Serve() to return
	}()

	// Serve connections (blocks until listener is closed)
	if err := listener.Serve(); err != nil {
		select {
		case <-shutdownCh:
			// Intentional shutdown  - don't log the accept error
		default:
			log.Printf("Listener stopped: %v", err)
		}
	}
}
