package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/peer"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"

	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"

	ma "github.com/multiformats/go-multiaddr"
)

const PingPongProtocol = "/pingpong/1.0.0"
const Rendezvous = "khoji-pingpong-demo"

// *** CHANGE THIS — same values as in home-node ***
var relayAddrs = []string{
	"/ip4/LINODE_IPV4/tcp/4001/p2p/RELAY_PEER_ID",
	"/ip4/LINODE_IPV4/udp/4001/quic-v1/p2p/RELAY_PEER_ID",
	"/ip6/LINODE_IPV6/tcp/4001/p2p/RELAY_PEER_ID",
	"/ip6/LINODE_IPV6/udp/4001/quic-v1/p2p/RELAY_PEER_ID",
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run main.go <HOME_PEER_ID>")
		fmt.Println()
		fmt.Println("Example:")
		fmt.Println("  go run main.go 12D3KooWLCavCP1Pma9NGJQnGDQhgwSjgQgupWprZJH4w1P3HCVL")
		os.Exit(1)
	}

	targetPeerIDStr := os.Args[1]
	targetPeerID, err := peer.Decode(targetPeerIDStr)
	if err != nil {
		log.Fatalf("Invalid peer ID: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Println("=== Client Node (Ping Sender) ===")
	fmt.Println()

	// Create the libp2p host
	h, err := libp2p.New(
		libp2p.ListenAddrStrings(
			"/ip4/0.0.0.0/tcp/0",
			"/ip4/0.0.0.0/udp/0/quic-v1",
			"/ip6/::/tcp/0",
			"/ip6/::/udp/0/quic-v1",
		),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.NATPortMap(),
		libp2p.EnableHolePunching(),
		libp2p.EnableAutoRelayWithStaticRelays(parseRelayAddrs()),
	)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	fmt.Printf("📱 Client Peer ID: %s\n", h.ID())
	fmt.Printf("🎯 Target Home Peer: %s\n", targetPeerID)
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

	// Connect to our dedicated relay
	fmt.Println("Connecting to dedicated relay...")
	for _, ai := range parseRelayAddrs() {
		if err := h.Connect(ctx, ai); err != nil {
			fmt.Printf("⚠️  Could not connect to relay: %v\n", err)
		} else {
			fmt.Printf("✅ Connected to relay %s\n", ai.ID.String()[:16])
		}
	}
	fmt.Println()

	// Search for the home node via DHT
	routingDiscovery := drouting.NewRoutingDiscovery(kdht)

	fmt.Println("🔍 Searching for home node via rendezvous discovery...")
	fmt.Println("   (This can take 30-60 seconds)")

	// Method 1: Try rendezvous discovery
	var targetAddrInfo *peer.AddrInfo
	discoverCtx, discoverCancel := context.WithTimeout(ctx, 60*time.Second)

	peerCh, err := routingDiscovery.FindPeers(discoverCtx, Rendezvous)
	if err != nil {
		fmt.Printf("Rendezvous discovery error: %v\n", err)
	} else {
		for p := range peerCh {
			if p.ID == targetPeerID && len(p.Addrs) > 0 {
				fmt.Printf("✅ Found home node via rendezvous!\n")
				targetAddrInfo = &p
				break
			}
			if p.ID == targetPeerID && len(p.Addrs) == 0 {
				fmt.Printf("⚠️  Found peer via rendezvous but no addresses yet\n")
			}
			if p.ID != h.ID() && p.ID != targetPeerID {
				fmt.Printf("   Found peer %s (not our target)\n", p.ID.String()[:16])
			}
		}
	}
	discoverCancel()

	// Method 2: Direct DHT lookup (always try if we don't have addresses)
	if targetAddrInfo == nil || len(targetAddrInfo.Addrs) == 0 {
		fmt.Println()
		fmt.Println("🔍 Trying direct DHT peer routing lookup...")
		lookupCtx, lookupCancel := context.WithTimeout(ctx, 60*time.Second)
		pi, err := kdht.FindPeer(lookupCtx, targetPeerID)
		lookupCancel()
		if err != nil {
			fmt.Printf("❌ Could not find home node: %v\n", err)
			fmt.Println()
			fmt.Println("Make sure the home node is running and has had time to")
			fmt.Println("register with the DHT (give it at least 3-5 minutes).")
			os.Exit(1)
		}
		targetAddrInfo = &pi
		fmt.Printf("✅ Found home node via DHT lookup! (%d addresses)\n", len(pi.Addrs))
	}

	// Show discovered addresses
	fmt.Println()
	fmt.Println("Home node addresses:")
	for _, addr := range targetAddrInfo.Addrs {
		label := "direct"
		if strings.Contains(addr.String(), "p2p-circuit") {
			label = "relay"
		}
		fmt.Printf("  [%s] %s\n", label, addr)
	}

	// Connect to the home node
	fmt.Println()
	fmt.Println("📡 Connecting to home node...")

	// First try direct addresses from DHT
	connectCtx, connectCancel := context.WithTimeout(ctx, 15*time.Second)
	err = h.Connect(connectCtx, *targetAddrInfo)
	connectCancel()

	if err != nil {
		fmt.Printf("⚠️  Direct connection failed: %v\n", err)
		fmt.Println()
		fmt.Println("📡 Trying via relay circuit...")

		// Manually construct relay circuit addresses
		// Format: <relay_addr>/p2p/<relay_id>/p2p-circuit/p2p/<target_id>
		relayInfos := parseRelayAddrs()
		var circuitAddrs []ma.Multiaddr
		for _, ri := range relayInfos {
			for _, raddr := range ri.Addrs {
				circuitStr := fmt.Sprintf("%s/p2p/%s/p2p-circuit/p2p/%s",
					raddr.String(), ri.ID.String(), targetPeerID.String())
				caddr, err := ma.NewMultiaddr(circuitStr)
				if err != nil {
					fmt.Printf("   ⚠️  Bad circuit addr: %v\n", err)
					continue
				}
				circuitAddrs = append(circuitAddrs, caddr)
				fmt.Printf("   Trying: %s\n", circuitStr)
			}
		}

		// Create AddrInfo with circuit addresses
		circuitInfo := peer.AddrInfo{
			ID:    targetPeerID,
			Addrs: circuitAddrs,
		}

		connectCtx2, connectCancel2 := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx2, circuitInfo)
		connectCancel2()
		if err != nil {
			log.Fatalf("❌ Failed to connect via relay: %v", err)
		}
	}

	// Check connection type
	conns := h.Network().ConnsToPeer(targetPeerID)
	for _, conn := range conns {
		connType := "DIRECT"
		if conn.Stat().Limited {
			connType = "RELAYED"
		}
		fmt.Printf("✅ Connected! [%s] via %s\n", connType, conn.RemoteMultiaddr())
	}

	// Send ping
	fmt.Println()
	fmt.Println("🏓 Sending PING...")
	streamCtx, streamCancel := context.WithTimeout(ctx, 15*time.Second)
	s, err := h.NewStream(streamCtx, targetPeerID, PingPongProtocol)
	streamCancel()
	if err != nil {
		log.Fatalf("❌ Failed to open stream: %v", err)
	}

	// Check if this particular stream is relayed or direct
	streamConnType := "DIRECT"
	if s.Conn().Stat().Limited {
		streamConnType = "RELAYED"
	}
	fmt.Printf("   Stream connection type: %s\n", streamConnType)

	_, err = s.Write([]byte("ping\n"))
	if err != nil {
		log.Fatalf("❌ Failed to send ping: %v", err)
	}

	// Read response
	reader := bufio.NewReader(s)
	response, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("❌ Failed to read response: %v", err)
	}
	response = strings.TrimSpace(response)

	fmt.Printf("\n🎉 Response: %s\n", response)
	fmt.Printf("   Connection: %s\n", streamConnType)

	s.Close()

	// If connected via relay, wait a bit to see if hole-punching upgrades the connection
	if streamConnType == "RELAYED" {
		fmt.Println()
		fmt.Println("⏳ Connected via relay. Waiting 15s to see if hole-punching upgrades to direct...")
		time.Sleep(15 * time.Second)

		conns = h.Network().ConnsToPeer(targetPeerID)
		upgraded := false
		for _, conn := range conns {
			if !conn.Stat().Limited {
				fmt.Printf("🎉 Hole-punch SUCCESS! Direct connection via %s\n", conn.RemoteMultiaddr())
				upgraded = true
			}
		}
		if !upgraded {
			fmt.Println("ℹ️  Still relayed. Hole-punching didn't upgrade in time.")
			fmt.Println("   This is normal with Starlink CGNAT.")
			fmt.Println("   Relay still works for small messages.")
			fmt.Println("   For large transfers, consider using your own router (bypass mode).")
		}

		// Try sending another ping over the potentially upgraded connection
		fmt.Println()
		fmt.Println("🏓 Sending second PING (may use upgraded connection)...")
		s2Ctx, s2Cancel := context.WithTimeout(ctx, 15*time.Second)
		s2, err := h.NewStream(s2Ctx, targetPeerID, PingPongProtocol)
		s2Cancel()
		if err != nil {
			fmt.Printf("❌ Second stream failed: %v\n", err)
		} else {
			connType2 := "DIRECT"
			if s2.Conn().Stat().Limited {
				connType2 = "RELAYED"
			}
			s2.Write([]byte("ping\n"))
			reader2 := bufio.NewReader(s2)
			resp2, err := reader2.ReadString('\n')
			if err == nil {
				fmt.Printf("🎉 Response: %s [%s]\n", strings.TrimSpace(resp2), connType2)
			}
			s2.Close()
		}
	}

	fmt.Println()
	fmt.Println("Done!")
}

func parseRelayAddrs() []peer.AddrInfo {
	var infos []peer.AddrInfo
	seen := make(map[peer.ID]bool)
	for _, s := range relayAddrs {
		maddr, err := ma.NewMultiaddr(s)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		if !seen[ai.ID] {
			seen[ai.ID] = true
			infos = append(infos, *ai)
		} else {
			for i := range infos {
				if infos[i].ID == ai.ID {
					infos[i].Addrs = append(infos[i].Addrs, ai.Addrs...)
				}
			}
		}
	}
	return infos
}
