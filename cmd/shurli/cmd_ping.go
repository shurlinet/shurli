package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func runPing(args []string) {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("ping", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	count := fs.Int("c", 0, "number of pings (0 = continuous until Ctrl+C)")
	fs.IntVar(count, "n", 0, "alias for -c")
	intervalStr := fs.String("interval", "1s", "interval between pings")
	jsonFlag := fs.Bool("json", false, "output as JSON (one line per ping)")
	standaloneFlag := fs.Bool("standalone", false, "use direct P2P without daemon (debug)")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Println("Usage: shurli ping [--config <path>] [-c N] [--interval 1s] [--json] [--standalone] <target>")
		fmt.Println()
		fmt.Println("Options:")
		fmt.Println("  -c, -n N       Number of pings (0 = continuous, default)")
		fmt.Println("  --interval 1s  Time between pings (default: 1s)")
		fmt.Println("  --json         Output each ping as a JSON line")
		fmt.Println("  --standalone   Use direct P2P without daemon (debug)")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli ping home-server")
		fmt.Println("  shurli ping home-server -c 5")
		fmt.Println("  shurli ping 12D3KooWPrmh... -c 3 --json")
		osExit(1)
	}

	target := remaining[0]

	interval, err := time.ParseDuration(*intervalStr)
	if err != nil {
		fatal("Invalid interval %q: %v", *intervalStr, err)
	}

	// Standalone allowed via CLI flag or config setting.
	allowStandalone := *standaloneFlag || configAllowsStandalone(*configFlag)

	// Always try daemon first (uses existing connections, supports direct paths).
	if !allowStandalone {
		if client := tryDaemonClient(); client != nil {
			if *count == 0 {
				// Continuous: loop single pings client-side, Ctrl+C stops.
				runPingViaDaemonContinuous(client, target, int(interval.Milliseconds()), *jsonFlag)
			} else {
				runPingViaDaemon(client, target, *count, int(interval.Milliseconds()), *jsonFlag)
			}
			return
		}
	}

	// Daemon not available. Require explicit --standalone or config setting.
	if !allowStandalone {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		fmt.Println()
		fmt.Println("Or use --standalone flag for direct P2P (debug):")
		fmt.Printf("  shurli ping --standalone %s -c 5\n", target)
		fmt.Println()
		fmt.Println("Or set cli.allow_standalone: true in config for persistent standalone access.")
		osExit(1)
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
		tc.Wfaint(os.Stdout, "PING %s\n", target)
		fmt.Println("Connecting...")
	}

	targetPeerID, err := standalone.ResolveAndConnect(ctx, target)
	if err != nil {
		fatal("%v", err)
	}

	if !*jsonFlag {
		fmt.Println()
	}

	// Ping loop using shared logic
	protocolID := standalone.NodeConfig.Protocols.PingPong.ID
	ch := p2pnet.PingPeer(ctx, standalone.Network.Host(), targetPeerID, protocolID, *count, interval)

	var results []p2pnet.PingResult
	for result := range ch {
		results = append(results, result)

		if *jsonFlag {
			line, _ := json.Marshal(result)
			fmt.Println(string(line))
		} else {
			if result.Error != "" {
				fmt.Printf("seq=%d ", result.Seq)
				tc.Wred(os.Stdout, "error=%s", result.Error)
				fmt.Println()
			} else {
				fmt.Printf("seq=%d ", result.Seq)
				tc.Wgreen(os.Stdout, "rtt=%.1fms", result.RttMs)
				tc.Wfaint(os.Stdout, " path=[%s]", result.Path)
				fmt.Println()
			}
		}
	}

	// Print summary
	stats := p2pnet.ComputePingStats(results)

	if *jsonFlag {
		summary, _ := json.Marshal(stats)
		fmt.Println(string(summary))
	} else {
		tc.Wfaint(os.Stdout, "\n--- %s ping statistics ---\n", target)
		fmt.Printf("%d sent, %d received, ", stats.Sent, stats.Received)
		if stats.LossPct > 0 {
			tc.Wred(os.Stdout, "%.0f%% loss", stats.LossPct)
		} else {
			tc.Wgreen(os.Stdout, "%.0f%% loss", stats.LossPct)
		}
		fmt.Printf(", rtt min/avg/max = %.1f/%.1f/%.1f ms\n",
			stats.MinMs, stats.AvgMs, stats.MaxMs)
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

// runPingViaDaemonContinuous sends one ping at a time via the daemon until Ctrl+C.
func runPingViaDaemonContinuous(client *daemon.Client, target string, intervalMs int, jsonOutput bool) {
	if !jsonOutput {
		showVerificationBadge(client, target)
	}

	// Ctrl+C handler
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var results []p2pnet.PingResult
	seq := 0

	if !jsonOutput {
		tc.Wfaint(os.Stdout, "PING %s (via daemon, continuous):\n", target)
	}

	for {
		select {
		case <-sigCh:
			goto done
		default:
		}

		resp, err := client.Ping(target, 1, intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}

		for _, r := range resp.Results {
			r.Seq = seq
			seq++
			results = append(results, r)
			if jsonOutput {
				line, _ := json.Marshal(r)
				fmt.Println(string(line))
			} else {
				if r.Error != "" {
					fmt.Printf("seq=%d ", r.Seq)
					tc.Wred(os.Stdout, "error=%s", r.Error)
					fmt.Println()
				} else {
					fmt.Printf("seq=%d ", r.Seq)
					tc.Wgreen(os.Stdout, "rtt=%.1fms", r.RttMs)
					tc.Wfaint(os.Stdout, " path=[%s]", r.Path)
					fmt.Println()
				}
			}
		}

		// Wait for interval or signal
		select {
		case <-sigCh:
			goto done
		case <-time.After(time.Duration(intervalMs) * time.Millisecond):
		}
	}

done:
	stats := p2pnet.ComputePingStats(results)
	if jsonOutput {
		summary, _ := json.Marshal(stats)
		fmt.Println(string(summary))
	} else {
		tc.Wfaint(os.Stdout, "\n--- %s ping statistics ---\n", target)
		fmt.Printf("%d sent, %d received, ", stats.Sent, stats.Received)
		if stats.LossPct > 0 {
			tc.Wred(os.Stdout, "%.0f%% loss", stats.LossPct)
		} else {
			tc.Wgreen(os.Stdout, "%.0f%% loss", stats.LossPct)
		}
		fmt.Printf(", rtt min/avg/max = %.1f/%.1f/%.1f ms\n",
			stats.MinMs, stats.AvgMs, stats.MaxMs)
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
				tc.Wgreen(os.Stdout, "[VERIFIED]")
				fmt.Printf(" Peer %q verified (%s)\n", target, e.Verified)
			} else {
				tc.Wyellow(os.Stdout, "[UNVERIFIED]")
				fmt.Printf(" Peer %q not verified. Run: shurli verify %s\n", target, target)
			}
			return
		}
	}
}
