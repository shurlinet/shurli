package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	if len(os.Args) < 2 {
		log.Fatalf("Usage: %s <server-peer-id>", os.Args[0])
	}

	serverPeerIDStr := os.Args[1]

	// Parse server peer ID
	serverPeerID, err := peer.Decode(serverPeerIDStr)
	if err != nil {
		log.Fatalf("Invalid peer ID: %v", err)
	}

	// Create P2P network
	net, err := p2pnet.New(&p2pnet.Config{
		KeyFile: "client.key",
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{
					"/ip4/0.0.0.0/tcp/0", // Ephemeral port
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create P2P network: %v", err)
	}
	defer net.Close()

	log.Printf("🆔 Client Peer ID: %s", net.PeerID())
	log.Printf("📍 Listening on: %v", net.Host().Addrs())

	// Add server's address manually (for local testing)
	// In production, use DHT or relay discovery
	serverAddr := fmt.Sprintf("/ip4/127.0.0.1/tcp/9100/p2p/%s", serverPeerID)
	serverMultiaddr, err := peer.AddrInfoFromString(serverAddr)
	if err != nil {
		log.Fatalf("Failed to parse server address: %v", err)
	}

	log.Printf("🔗 Connecting to server at %s...", serverAddr)

	// Connect to server peer
	ctx := context.Background()
	if err := net.Host().Connect(ctx, *serverMultiaddr); err != nil {
		log.Fatalf("Failed to connect to server: %v", err)
	}

	log.Println("✅ Connected to server!")

	// Connect to HTTP service
	log.Println("🌐 Opening HTTP service stream...")
	conn, err := net.ConnectToService(serverPeerID, "http")
	if err != nil {
		log.Fatalf("Failed to connect to HTTP service: %v", err)
	}
	defer conn.Close()

	log.Println("✅ HTTP service stream opened!")

	// Send HTTP request
	request := "GET / HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n"
	log.Printf("📤 Sending HTTP request...")

	if _, err := conn.Write([]byte(request)); err != nil {
		log.Fatalf("Failed to send request: %v", err)
	}

	// Read response
	log.Println("📥 Reading response...")
	reader := bufio.NewReader(conn)

	// Read headers
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err != io.EOF {
				log.Fatalf("Failed to read response: %v", err)
			}
			break
		}
		fmt.Print(line)
		if line == "\r\n" {
			break
		}
	}

	// Read body
	body, err := io.ReadAll(reader)
	if err != nil && err != io.EOF {
		log.Fatalf("Failed to read body: %v", err)
	}

	fmt.Println(string(body))

	log.Println("✅ Test completed successfully!")
}
