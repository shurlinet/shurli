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

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"

	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
)

const PingPongProtocol = "/pingpong/1.0.0"
const Rendezvous = "khoji-pingpong-demo"

// *** CHANGE THIS to your Linode relay's multiaddr after deploying ***
// Format: /ip4/<LINODE_IP>/tcp/4001/p2p/<RELAY_PEER_ID>
// You'll also want the QUIC and IPv6 variants
var relayAddrs = []string{
	"/ip4/LINODE_IPV4/tcp/4001/p2p/RELAY_PEER_ID",
	"/ip4/LINODE_IPV4/udp/4001/quic-v1/p2p/RELAY_PEER_ID",
	"/ip6/LINODE_IPV6/tcp/4001/p2p/RELAY_PEER_ID",
	"/ip6/LINODE_IPV6/udp/4001/quic-v1/p2p/RELAY_PEER_ID",
}

// Persistent identity — saves/loads a key so your Peer ID stays the same across restarts
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

	fmt.Println("=== Home Node (Pong Responder) ===")
	fmt.Println()

	// Load or create persistent identity
	priv, err := loadOrCreateIdentity("home_node.key")
	if err != nil {
		log.Fatalf("Identity error: %v", err)
	}

	// Create the libp2p host
	h, err := libp2p.New(
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/9100",
			"/ip4/0.0.0.0/udp/9100/quic-v1",
			"/ip6/::/tcp/9100",
			"/ip6/::/udp/9100/quic-v1",
		),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithStaticRelays(parseRelayAddrs()),
		libp2p.ForceReachabilityPrivate(), // Tell libp2p we're behind NAT — triggers relay usage
	)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Connect to the relay
	for _, ai := range parseRelayAddrs() {
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
		for _, ai := range parseRelayAddrs() {
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
			time.Sleep(2 * time.Minute)
			for _, ai := range parseRelayAddrs() {
				h.Connect(ctx, ai)
				circuitv2client.Reserve(ctx, h, ai)
			}
		}
	}()

	fmt.Printf("🏠 Peer ID: %s\n", h.ID())
	fmt.Println()

	// Set up the ping-pong handler
	h.SetStreamHandler(PingPongProtocol, func(s network.Stream) {
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
	bootstrapPeers := dht.DefaultBootstrapPeers
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
	fmt.Printf("Advertising on rendezvous: %s\n", Rendezvous)

	// Keep advertising in the background
	go func() {
		for {
			_, err := routingDiscovery.Advertise(ctx, Rendezvous)
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

func parseRelayAddrs() []peer.AddrInfo {
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
