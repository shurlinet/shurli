package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
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

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx

	fmt.Println("=== Private libp2p Relay Server ===")
	fmt.Println()

	priv, err := loadOrCreateIdentity("relay_node.key")
	if err != nil {
		log.Fatalf("Identity error: %v", err)
	}

	// Create a basic host first — no relay options
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/7777",
			"/ip6/::/tcp/7777",
		),
	)
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
