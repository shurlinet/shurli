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
	"github.com/libp2p/go-libp2p/core/network"
)

const TestProtocol = "/nat-test/1.0.0"

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = ctx

	fmt.Println("=== libp2p NAT Port Mapping Test ===")
	fmt.Println()

	// Create a libp2p host with NAT port mapping enabled
	// NATPortMap() tries UPnP, NAT-PMP, and PCP automatically
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip6/::/tcp/9100",
			"/ip6/::/udp/9100/quic-v1",
			"/ip4/0.0.0.0/tcp/9100",
			"/ip4/0.0.0.0/udp/9100/quic-v1",
		),
		libp2p.NATPortMap(),
	)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Simple echo handler
	h.SetStreamHandler(TestProtocol, func(s network.Stream) {
		buf := make([]byte, 64)
		n, _ := s.Read(buf)
		fmt.Printf(">>> Received: %s\n", string(buf[:n]))
		s.Write([]byte("pong"))
		s.Close()
	})

	fmt.Printf("Peer ID: %s\n", h.ID())
	fmt.Println()
	fmt.Println("Waiting 15 seconds for NAT port mapping discovery...")
	time.Sleep(15 * time.Second)

	// Categorize addresses
	fmt.Println()
	fmt.Println("=== Addresses ===")
	publicCount := 0
	for _, addr := range h.Addrs() {
		addrStr := addr.String()
		label := classifyAddr(addrStr)
		symbol := "  "
		if label == "PUBLIC" {
			symbol = "✅"
			publicCount++
		} else if label == "PRIVATE" {
			symbol = "🏠"
		} else {
			symbol = "⚪"
		}
		fmt.Printf("  %s [%s] %s\n", symbol, label, addrStr)
	}

	fmt.Println()
	if publicCount > 0 {
		fmt.Println("✅ NAT port mapping appears to be WORKING!")
		fmt.Println()
		fmt.Println("Test from the internet:")
		fmt.Println("  1. Keep this running")
		fmt.Println("  2. Go to https://port.tools/port-checker-ipv6/")
		fmt.Println("  3. Check port 9100 on your public IPv6 address shown above")
		fmt.Println()
		fmt.Println("Full multiaddrs for your iPhone app:")
		for _, addr := range h.Addrs() {
			if classifyAddr(addr.String()) == "PUBLIC" {
				fmt.Printf("  %s/p2p/%s\n", addr, h.ID())
			}
		}
	} else {
		fmt.Println("❌ No public addresses detected — NAT port mapping likely FAILED.")
		fmt.Println()
		fmt.Println("This means your router doesn't support UPnP/PCP/NAT-PMP,")
		fmt.Println("or the Starlink router's IPv6 firewall is blocking inbound.")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  1. Check Starlink app for firewall/security settings")
		fmt.Println("  2. Use your own router behind Starlink (bypass mode)")
		fmt.Println("  3. Fall back to libp2p relay + DCUtR hole-punching")
	}

	fmt.Println()
	fmt.Println("Node running. Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	fmt.Println("\nShutting down...")
}

func classifyAddr(addr string) string {
	// Loopback
	if strings.Contains(addr, "/ip4/127.") || addr == "/ip6/::1" || strings.Contains(addr, "/ip6/::1/") {
		return "LOOPBACK"
	}
	// Private IPv4
	if strings.Contains(addr, "/ip4/10.") ||
		strings.Contains(addr, "/ip4/192.168.") ||
		strings.Contains(addr, "/ip4/172.16.") ||
		strings.Contains(addr, "/ip4/172.17.") ||
		strings.Contains(addr, "/ip4/172.18.") ||
		strings.Contains(addr, "/ip4/172.19.") ||
		strings.Contains(addr, "/ip4/172.2") ||
		strings.Contains(addr, "/ip4/172.3") {
		return "PRIVATE"
	}
	// Link-local / ULA IPv6
	if strings.Contains(addr, "/ip6/fe80") ||
		strings.Contains(addr, "/ip6/fd") ||
		strings.Contains(addr, "/ip6/fc") {
		return "LINK-LOCAL"
	}
	// Public
	if strings.Contains(addr, "/ip4/") || strings.Contains(addr, "/ip6/") {
		return "PUBLIC"
	}
	return "UNKNOWN"
}
