package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
)

func loadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	if data, err := os.ReadFile(path); err == nil {
		return crypto.UnmarshalPrivateKey(data)
	}
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, err
	}
	data, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to save key: %w", err)
	}
	return priv, nil
}

// loadAuthKeysPath loads config and returns the authorized_keys file path.
func loadAuthKeysPath() string {
	cfg, err := config.LoadRelayServerConfig("relay-server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Security.AuthorizedKeysFile == "" {
		log.Fatal("No authorized_keys_file configured in relay-server.yaml")
	}
	return cfg.Security.AuthorizedKeysFile
}

func runAuthorize(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: relay-server authorize <peer-id> [comment]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  ./relay-server authorize 12D3KooW... home-node")
		fmt.Println("  ./relay-server authorize 12D3KooW... laptop")
		os.Exit(1)
	}

	peerID := args[0]
	comment := ""
	if len(args) > 1 {
		comment = strings.Join(args[1:], " ")
	}

	authKeysPath := loadAuthKeysPath()
	if err := auth.AddPeer(authKeysPath, peerID, comment); err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Authorized: %s\n", peerID[:16]+"...")
	if comment != "" {
		fmt.Printf("Comment:    %s\n", comment)
	}
	fmt.Printf("File:       %s\n", authKeysPath)
	fmt.Println()
	fmt.Println("Restart relay to apply: sudo systemctl restart relay-server")
}

func runDeauthorize(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: relay-server deauthorize <peer-id>")
		os.Exit(1)
	}

	peerID := args[0]
	authKeysPath := loadAuthKeysPath()
	if err := auth.RemovePeer(authKeysPath, peerID); err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Deauthorized: %s\n", peerID[:16]+"...")
	fmt.Println()
	fmt.Println("Restart relay to apply: sudo systemctl restart relay-server")
}

func runListPeers() {
	authKeysPath := loadAuthKeysPath()
	peers, err := auth.ListPeers(authKeysPath)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Authorized peers (%s):\n\n", authKeysPath)
	if len(peers) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, p := range peers {
			if p.Comment != "" {
				fmt.Printf("  %s  # %s\n", p.PeerID, p.Comment)
			} else {
				fmt.Printf("  %s\n", p.PeerID)
			}
		}
	}
	fmt.Printf("\nTotal: %d peer(s)\n", len(peers))
}

func main() {
	// Handle subcommands before starting the relay
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "authorize":
			runAuthorize(os.Args[2:])
			return
		case "deauthorize":
			runDeauthorize(os.Args[2:])
			return
		case "list-peers":
			runListPeers()
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx

	fmt.Println("=== Private libp2p Relay Server ===")
	fmt.Println()

	// Load configuration
	cfg, err := config.LoadRelayServerConfig("relay-server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v\n", err)
		fmt.Println("Please create relay-server.yaml from the sample:")
		fmt.Println("  cp configs/relay-server.sample.yaml relay-server.yaml")
		os.Exit(1)
	}

	// Validate configuration
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	fmt.Printf("Loaded configuration from relay-server.yaml\n")
	fmt.Printf("Authentication: %v\n", cfg.Security.EnableConnectionGating)
	fmt.Println()

	priv, err := loadOrCreateIdentity(cfg.Identity.KeyFile)
	if err != nil {
		log.Fatalf("Identity error: %v", err)
	}

	// Load authorized keys if connection gating is enabled
	var gater *auth.AuthorizedPeerGater
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			log.Fatalf("Connection gating enabled but no authorized_keys_file specified")
		}

		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			log.Fatalf("Failed to load authorized keys: %v", err)
		}

		if len(authorizedPeers) == 0 {
			fmt.Println("⚠️  WARNING: authorized_keys file is empty - no peers can make reservations!")
			fmt.Printf("   Add authorized peer IDs to %s\n", cfg.Security.AuthorizedKeysFile)
		} else {
			fmt.Printf("✅ Loaded %d authorized peer(s) from %s\n", len(authorizedPeers), cfg.Security.AuthorizedKeysFile)
		}

		gater = auth.NewAuthorizedPeerGater(authorizedPeers, log.Default())
	} else {
		fmt.Println("⚠️  WARNING: Connection gating is DISABLED - any peer can use this relay!")
	}
	fmt.Println()

	// Build host options
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(cfg.Network.ListenAddresses...),
	}

	// Add connection gater if enabled
	if gater != nil {
		hostOpts = append(hostOpts, libp2p.ConnectionGater(gater))
	}

	// Create a basic host first — no relay options
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Now manually start the relay service on this host
	_, err = relayv2.New(h, relayv2.WithInfiniteLimits())
	if err != nil {
		log.Fatalf("Failed to start relay service: %v", err)
	}

	fmt.Printf("🔄 Relay Peer ID: %s\n", h.ID())
	fmt.Println()

	// Verify the relay protocol is registered
	fmt.Println("Registered protocols:")
	for _, p := range h.Mux().Protocols() {
		fmt.Printf("  %s\n", p)
	}

	fmt.Println()
	fmt.Println("Multiaddrs:")
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, h.ID())
	}

	go func() {
		for {
			time.Sleep(15 * time.Second)
			peers := h.Network().Peers()
			fmt.Printf("\n--- %d connected peers ---\n", len(peers))
			for _, p := range peers {
				fmt.Printf("  %s\n", p.String()[:16])
			}
		}
	}()

	fmt.Println()
	fmt.Println("✅ Private relay running.")
	fmt.Println("Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Println("\nShutting down...")
}
