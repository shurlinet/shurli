package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
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

	// Always try daemon first (uses existing connections, supports direct paths).
	if !*standaloneFlag {
		if client := tryDaemonClient(); client != nil {
			runTracerouteViaDaemon(client, target, *jsonFlag)
			return
		}
	}

	// Daemon not available. Require explicit --standalone.
	if !*standaloneFlag {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		fmt.Println()
		fmt.Println("Or use --standalone flag for direct P2P (debug):")
		fmt.Printf("  shurli traceroute --standalone %s\n", target)
		osExit(1)
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

	// Resolve password for SHRL-encrypted identity key.
	pw, _ := resolvePassword(filepath.Dir(cfgFile))

	// Create P2P network
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		KeyPassword:        pw,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "shurli/" + version,
		Namespace:          cfg.Discovery.Network,
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
		tc.Wfaint(os.Stdout, "traceroute to %s (%s)\n", target, targetPeerID.String()[:16]+"...")
		fmt.Println("Connecting...")
	}

	// Bootstrap and connect to target
	bootstrapCfg := p2pnet.BootstrapConfig{
		Namespace:      cfg.Discovery.Network,
		BootstrapPeers: cfg.Discovery.BootstrapPeers,
		RelayAddrs:     cfg.Relay.Addresses,
	}
	if err := p2pnet.BootstrapAndConnect(ctx, h, p2pNetwork, targetPeerID, bootstrapCfg); err != nil {
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
			fmt.Printf(" %d  %s  %s  ", hop.Hop, peerShort, hop.Address)
			tc.Wred(os.Stdout, "*")
			fmt.Println()
		} else {
			name := ""
			if hop.Name != "" {
				name = " (" + hop.Name + ")"
			}
			fmt.Printf(" %d  %s%s  %s  ", hop.Hop, peerShort, name, hop.Address)
			tc.Wgreen(os.Stdout, "%.1fms", hop.RttMs)
			fmt.Println()
		}
	}
	tc.Wfaint(os.Stdout, "--- path: [%s] ---\n", result.Path)
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
