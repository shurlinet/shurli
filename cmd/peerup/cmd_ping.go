package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/daemon"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

func runPing(args []string) {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	count := fs.Int("c", 0, "number of pings (0 = continuous until Ctrl+C)")
	intervalStr := fs.String("interval", "1s", "interval between pings")
	jsonFlag := fs.Bool("json", false, "output as JSON (one line per ping)")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: peerup ping [--config <path>] [-c N] [--interval 1s] [--json] <target>")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -c N           Number of pings (0 = continuous, default)")
		fmt.Println("  --interval 1s  Time between pings (default: 1s)")
		fmt.Println("  --json         Output each ping as a JSON line")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  peerup ping home-server")
		fmt.Println("  peerup ping home-server -c 5")
		fmt.Println("  peerup ping 12D3KooWPrmh... -c 3 --json")
		osExit(1)
	}

	target := remaining[0]

	interval, err := time.ParseDuration(*intervalStr)
	if err != nil {
		fatal("Invalid interval %q: %v", *intervalStr, err)
	}

	// Try daemon first (faster, no bootstrap needed).
	if client := tryDaemonClient(); client != nil {
		runPingViaDaemon(client, target, *count, int(interval.Milliseconds()), *jsonFlag)
		return
	}

	// Set up context with Ctrl+C cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

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

	// Create P2P network
	p2pNetwork, err := p2pnet.New(&p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "peerup/" + version,
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
		fmt.Printf("PING %s (%s)\n", target, targetPeerID.String()[:16]+"...")
		fmt.Println("Connecting...")
	}

	// Bootstrap and connect
	if err := bootstrapAndConnect(ctx, h, cfg, targetPeerID, p2pNetwork); err != nil {
		fatal("Failed to connect: %v", err)
	}

	if !*jsonFlag {
		fmt.Println()
	}

	// Ping loop using shared logic
	protocolID := cfg.Protocols.PingPong.ID
	ch := p2pnet.PingPeer(ctx, h, targetPeerID, protocolID, *count, interval)

	var results []p2pnet.PingResult
	for result := range ch {
		results = append(results, result)

		if *jsonFlag {
			line, _ := json.Marshal(result)
			fmt.Println(string(line))
		} else {
			if result.Error != "" {
				fmt.Printf("seq=%d error=%s\n", result.Seq, result.Error)
			} else {
				fmt.Printf("seq=%d rtt=%.1fms path=[%s]\n", result.Seq, result.RttMs, result.Path)
			}
		}
	}

	// Print summary
	stats := p2pnet.ComputePingStats(results)

	if *jsonFlag {
		summary, _ := json.Marshal(stats)
		fmt.Println(string(summary))
	} else {
		fmt.Printf("\n--- %s ping statistics ---\n", target)
		fmt.Printf("%d sent, %d received, %.0f%% loss, rtt min/avg/max = %.1f/%.1f/%.1f ms\n",
			stats.Sent, stats.Received, stats.LossPct, stats.MinMs, stats.AvgMs, stats.MaxMs)
	}
}

// runPingViaDaemon pings a peer through the running daemon.
func runPingViaDaemon(client *daemon.Client, target string, count, intervalMs int, jsonOutput bool) {
	// Show verification badge (OMEMO-style).
	if !jsonOutput {
		showVerificationBadge(client, target)
	}

	if jsonOutput {
		resp, err := client.Ping(target, count, intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := client.PingText(target, count, intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

// showVerificationBadge queries the daemon for a peer's verification status
// and prints a badge. Unverified peers get a persistent warning.
func showVerificationBadge(client *daemon.Client, target string) {
	entries, err := client.AuthList()
	if err != nil {
		return // non-fatal, skip badge
	}

	for _, e := range entries {
		if e.Comment == target || e.PeerID == target {
			if e.Verified != "" {
				fmt.Printf("[VERIFIED] Peer %q verified (%s)\n", target, e.Verified)
			} else {
				fmt.Printf("[UNVERIFIED] Peer %q not verified. Run: peerup verify %s\n", target, target)
			}
			return
		}
	}
}
