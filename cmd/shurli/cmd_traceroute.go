package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

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
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli traceroute [--config <path>] [--json] [--standalone] <target>")
		osExit(1)
	}

	target := remaining[0]

	// Standalone allowed via CLI flag or config setting.
	allowStandalone := *standaloneFlag || configAllowsStandalone(*configFlag)

	// Always try daemon first (uses existing connections, supports direct paths).
	if !allowStandalone {
		if client := tryDaemonClient(); client != nil {
			runTracerouteViaDaemon(client, target, *jsonFlag)
			return
		}
	}

	// Daemon not available. Require explicit --standalone or config setting.
	if !allowStandalone {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		fmt.Println()
		fmt.Println("Or use --standalone flag for direct P2P (debug):")
		fmt.Printf("  shurli traceroute --standalone %s\n", target)
		fmt.Println()
		fmt.Println("Or set cli.allow_standalone: true in config for persistent standalone access.")
		osExit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create standalone P2P host, resolve target, bootstrap, and connect.
	pw, _ := resolvePasswordFromConfig(*configFlag)
	standalone, err := p2pnet.NewStandaloneHost(p2pnet.StandaloneConfig{
		ConfigPath: *configFlag,
		Password:   pw,
		UserAgent:  "shurli/" + version,
	})
	if err != nil {
		fatal("%v", err)
	}
	defer standalone.Network.Close()

	if !*jsonFlag {
		tc.Wfaint(os.Stdout, "traceroute to %s\n", target)
		fmt.Println("Connecting...")
	}

	targetPeerID, err := standalone.ResolveAndConnect(ctx, target)
	if err != nil {
		fatal("%v", err)
	}

	// Run traceroute
	result, err := p2pnet.TracePeer(ctx, standalone.Network.Host(), targetPeerID)
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
