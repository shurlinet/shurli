package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

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
		os.Exit(1)
	}

	target := remaining[0]
	serviceName := remaining[1]
	localPort := remaining[2]

	// Find and load config
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		log.Fatalf("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		log.Fatalf("Config error: %v", err)
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

	// Load names from config for resolution
	if cfg.Names != nil {
		if err := p2pNetwork.LoadNames(cfg.Names); err != nil {
			log.Printf("Failed to load names: %v", err)
		}
	}

	// Resolve target (name or peer ID)
	homePeerID, err := p2pNetwork.ResolveName(target)
	if err != nil {
		log.Fatalf("Cannot resolve target %q: %v", target, err)
	}

	fmt.Printf("Client Peer ID: %s\n", p2pNetwork.PeerID())
	fmt.Printf("Target Peer: %s\n", homePeerID)
	if target != homePeerID.String() {
		fmt.Printf("   (resolved from name %q)\n", target)
	}
	fmt.Println()

	// Add relay circuit addresses for the home peer
	fmt.Println("Connecting to target peer...")
	if err := p2pNetwork.AddRelayAddressesForPeer(cfg.Relay.Addresses, homePeerID); err != nil {
		log.Fatalf("Failed to add relay addresses: %v", err)
	}
	fmt.Println()

	// Create TCP listener using library helper
	localAddr := fmt.Sprintf("localhost:%s", localPort)
	listener, err := p2pnet.NewTCPListener(localAddr, func() (p2pnet.ServiceConn, error) {
		return p2pNetwork.ConnectToService(homePeerID, serviceName)
	})
	if err != nil {
		log.Fatalf("Failed to create listener: %v", err)
	}
	defer listener.Close()

	fmt.Printf("TCP proxy listening on %s\n", localAddr)
	fmt.Println()
	fmt.Println("Connect to the service:")
	fmt.Printf("   %s -> %s service on target\n", localAddr, serviceName)
	fmt.Println("\nPress Ctrl+C to stop.")
	fmt.Println()

	// Handle graceful shutdown
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
		<-ch
		fmt.Println("\nShutting down...")
		listener.Close()
		p2pNetwork.Close()
		os.Exit(0)
	}()

	// Serve connections
	if err := listener.Serve(); err != nil {
		log.Printf("Listener stopped: %v", err)
	}
}
