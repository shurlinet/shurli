package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func main() {
	log.SetFlags(log.Ltime | log.Lshortfile)

	// Start local HTTP server
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		response := fmt.Sprintf("Hello from P2P! Time: %s\n", time.Now().Format(time.RFC3339))
		w.Write([]byte(response))
		log.Printf("📨 Served HTTP request from %s", r.RemoteAddr)
	})

	httpServer := &http.Server{
		Addr:    "localhost:8080",
		Handler: mux,
	}

	// Start HTTP server in background
	go func() {
		log.Printf("🌐 Starting local HTTP server on %s", httpServer.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}()

	// Wait a moment for HTTP server to start
	time.Sleep(100 * time.Millisecond)

	// Create P2P network
	net, err := p2pnet.New(&p2pnet.Config{
		KeyFile: "server.key",
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{
					"/ip4/0.0.0.0/tcp/9100",
				},
			},
		},
	})
	if err != nil {
		log.Fatalf("Failed to create P2P network: %v", err)
	}
	defer net.Close()

	log.Printf("🆔 Server Peer ID: %s", net.PeerID())
	log.Printf("📍 Listening on: %v", net.Host().Addrs())

	// Expose HTTP service via P2P
	if err := net.ExposeService("http", "localhost:8080", nil); err != nil {
		log.Fatalf("Failed to expose HTTP service: %v", err)
	}

	log.Println("✅ Server ready! Share this peer ID with clients:")
	log.Printf("   %s", net.PeerID())
	log.Println("\n💡 To connect from client:")
	log.Printf("   go run client.go %s\n", net.PeerID())

	// Wait for interrupt
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Println("\n👋 Shutting down...")

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(ctx)
}
