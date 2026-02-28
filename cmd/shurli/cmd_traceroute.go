package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runTraceroute(args []string) {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("traceroute", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	standaloneFlag := fs.Bool("standalone", false, "use direct P2P without daemon (debug)")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli traceroute [--config <path>] [--json] [--standalone] <target>")
		osExit(1)
	}

	target := remaining[0]

	// Try daemon first (faster, no bootstrap needed).
	if !*standaloneFlag {
		if client := tryDaemonClient(); client != nil {
			runTracerouteViaDaemon(client, target, *jsonFlag)
			return
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Load configuration
	cfgFile, err := config.FindConfigFile(*configFlag)
	if err != nil {
		fatal("Config error: %v", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		fatal("Config error: %v", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	// Check if standalone mode is allowed
	if !*standaloneFlag && !cfg.CLI.AllowStandalone {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		fmt.Println()
		fmt.Println("Or use --standalone flag for direct P2P (debug):")
		fmt.Printf("  shurli traceroute --standalone %s\n", target)
		osExit(1)
	}

	// Create P2P network
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "shurli/" + version,
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

	// Load names
	if cfg.Names != nil {
		p2pNetwork.LoadNames(cfg.Names)
	}

	// Resolve target
	targetPeerID, err := p2pNetwork.ResolveName(target)
	if err != nil {
		fatal("Cannot resolve target %q: %v", target, err)
	}

	h := p2pNetwork.Host()

	if !*jsonFlag {
		fmt.Printf("traceroute to %s (%s)\n", target, targetPeerID.String()[:16]+"...")
		fmt.Println("Connecting...")
	}

	// Bootstrap and connect to target
	if err := bootstrapAndConnect(ctx, h, cfg, targetPeerID, p2pNetwork); err != nil {
		fatal("Failed to connect: %v", err)
	}

	// Run traceroute
	result, err := p2pnet.TracePeer(ctx, h, targetPeerID)
	if err != nil {
		fatal("Traceroute failed: %v", err)
	}
	result.Target = target

	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(result)
		return
	}

	// Print results
	for _, hop := range result.Hops {
		peerShort := hop.PeerID
		if len(peerShort) > 16 {
			peerShort = peerShort[:16] + "..."
		}
		if hop.Error != "" {
			fmt.Printf(" %d  %s  %s  *\n", hop.Hop, peerShort, hop.Address)
		} else {
			name := ""
			if hop.Name != "" {
				name = " (" + hop.Name + ")"
			}
			fmt.Printf(" %d  %s%s  %s  %.1fms\n", hop.Hop, peerShort, name, hop.Address, hop.RttMs)
		}
	}
	fmt.Printf("--- path: [%s] ---\n", result.Path)
}

// bootstrapAndConnect bootstraps the DHT and connects to the target peer.
// Shared by traceroute and enhanced ping.
func bootstrapAndConnect(ctx context.Context, h host.Host, cfg *config.HomeNodeConfig, targetPeerID peer.ID, p2pNetwork *p2pnet.Network) error {
	// Bootstrap DHT
	dhtPrefix := p2pnet.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeClient),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
		dht.RoutingTablePeerDiversityFilter(dht.NewRTPeerDiversityFilter(h, 3, 50)),
	)
	if err != nil {
		return fmt.Errorf("DHT error: %w", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		return fmt.Errorf("DHT bootstrap error: %w", err)
	}

	// Connect to bootstrap peers
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
			if err := h.Connect(ctx, pi); err == nil {
				connected.Add(1)
			}
		}(*pi)
	}
	wg.Wait()

	// Connect to relay
	relayInfos, err := p2pnet.ParseRelayAddrs(cfg.Relay.Addresses)
	if err != nil {
		return fmt.Errorf("relay address parse error: %w", err)
	}
	for _, ai := range relayInfos {
		h.Connect(ctx, ai)
	}

	// Find target via DHT
	findCtx, findCancel := context.WithTimeout(ctx, 60*time.Second)
	pi, err := kdht.FindPeer(findCtx, targetPeerID)
	findCancel()
	if err != nil {
		// Peer not in DHT  - try connecting via relay
		if err := p2pNetwork.AddRelayAddressesForPeer(cfg.Relay.Addresses, targetPeerID); err != nil {
			return fmt.Errorf("failed to add relay addresses: %w", err)
		}
		connectCtx, connectCancel := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx, peer.AddrInfo{ID: targetPeerID})
		connectCancel()
		if err != nil {
			return fmt.Errorf("cannot connect to peer: %w", err)
		}
		return nil
	}

	// Connect using DHT-discovered addresses
	connectCtx, connectCancel := context.WithTimeout(ctx, 15*time.Second)
	err = h.Connect(connectCtx, pi)
	connectCancel()
	if err != nil {
		// Fallback to relay
		if err := p2pNetwork.AddRelayAddressesForPeer(cfg.Relay.Addresses, targetPeerID); err != nil {
			return fmt.Errorf("failed to add relay addresses: %w", err)
		}
		connectCtx2, connectCancel2 := context.WithTimeout(ctx, 30*time.Second)
		err = h.Connect(connectCtx2, peer.AddrInfo{ID: targetPeerID})
		connectCancel2()
		if err != nil {
			return fmt.Errorf("cannot connect to peer: %w", err)
		}
	}

	return nil
}

// runTracerouteViaDaemon traces a peer through the running daemon.
func runTracerouteViaDaemon(client *daemon.Client, target string, jsonOutput bool) {
	// Show verification badge.
	if !jsonOutput {
		showVerificationBadge(client, target)
	}

	if jsonOutput {
		resp, err := client.Traceroute(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := client.TracerouteText(target)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}
