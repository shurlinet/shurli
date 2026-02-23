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

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/daemon"
	"github.com/satindergrewal/peer-up/internal/watchdog"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// --- RuntimeInfo adapter (implements daemon.RuntimeInfo on serveRuntime) ---

func (rt *serveRuntime) Network() *p2pnet.Network            { return rt.network }
func (rt *serveRuntime) ConfigFile() string                   { return rt.configFile }
func (rt *serveRuntime) AuthKeysPath() string                 { return rt.authKeys }
func (rt *serveRuntime) Version() string                      { return rt.version }
func (rt *serveRuntime) StartTime() time.Time                 { return rt.startTime }
func (rt *serveRuntime) PingProtocolID() string               { return rt.config.Protocols.PingPong.ID }
func (rt *serveRuntime) Interfaces() *p2pnet.InterfaceSummary { return rt.ifSummary }
func (rt *serveRuntime) PathTracker() *p2pnet.PathTracker     { return rt.pathTracker }
func (rt *serveRuntime) STUNResult() *p2pnet.STUNResult {
	if rt.stunProber == nil {
		return nil
	}
	return rt.stunProber.Result()
}
func (rt *serveRuntime) IsRelaying() bool {
	if rt.peerRelay == nil {
		return false
	}
	return rt.peerRelay.Enabled()
}

func (rt *serveRuntime) GaterForHotReload() daemon.GaterReloader {
	if rt.gater == nil || rt.authKeys == "" {
		return nil
	}
	return &gaterReloader{gater: rt.gater, authKeysPath: rt.authKeys}
}

// gaterReloader implements daemon.GaterReloader by re-reading the
// authorized_keys file and updating the live connection gater.
type gaterReloader struct {
	gater        *auth.AuthorizedPeerGater
	authKeysPath string
}

func (g *gaterReloader) ReloadFromFile() error {
	peers, err := auth.LoadAuthorizedKeys(g.authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to reload authorized_keys: %w", err)
	}
	g.gater.UpdateAuthorizedPeers(peers)
	return nil
}

// --- Daemon paths ---

func daemonSocketPath() string {
	dir, err := config.DefaultConfigDir()
	if err != nil {
		fatal("Cannot determine config directory: %v", err)
	}
	return filepath.Join(dir, "peerup.sock")
}

func daemonCookiePath() string {
	dir, err := config.DefaultConfigDir()
	if err != nil {
		fatal("Cannot determine config directory: %v", err)
	}
	return filepath.Join(dir, ".daemon-cookie")
}

// --- Main daemon entry ---

func runDaemon(args []string) {
	// If no subcommand or "start", run the daemon foreground.
	if len(args) == 0 {
		runDaemonStart(args)
		return
	}

	switch args[0] {
	case "start":
		runDaemonStart(args[1:])
	case "status":
		runDaemonStatus(args[1:])
	case "stop":
		runDaemonStop()
	case "ping":
		runDaemonPing(args[1:])
	case "services":
		runDaemonServices(args[1:])
	case "peers":
		runDaemonPeers(args[1:])
	case "paths":
		runDaemonPaths(args[1:])
	case "connect":
		runDaemonConnect(args[1:])
	case "disconnect":
		runDaemonDisconnect(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown daemon subcommand: %s\n\n", args[0])
		printDaemonUsage()
		osExit(1)
	}
}

func printDaemonUsage() {
	fmt.Println("Usage: peerup daemon [subcommand]")
	fmt.Println()
	fmt.Println("  (no subcommand)  Start daemon in foreground")
	fmt.Println("  start            Start daemon in foreground")
	fmt.Println("  status [--json]  Show daemon status")
	fmt.Println("  stop             Graceful shutdown")
	fmt.Println("  ping <peer> [-c N] [--interval 1s] [--json]")
	fmt.Println("  services [--json]")
	fmt.Println("  peers [--all] [--json]")
	fmt.Println("  paths [--json]")
	fmt.Println("  connect --peer <name> --service <svc> --listen <addr>")
	fmt.Println("  disconnect <id>")
}

// --- Start daemon (foreground) ---

func runDaemonStart(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	fs.Parse(args)

	fmt.Printf("peerup daemon %s (%s)\n", version, commit)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())

	rt, err := newServeRuntime(ctx, cancel, *configFlag, version)
	if err != nil {
		cancel()
		fatal("Failed to start: %v", err)
	}

	if err := rt.Bootstrap(); err != nil {
		rt.Shutdown()
		fatal("Bootstrap failed: %v", err)
	}

	rt.ExposeConfiguredServices()
	rt.SetupPingPong()
	rt.SetupPeerNotify()
	rt.StartPeerHistorySaver()

	// Start daemon API server
	socketPath := daemonSocketPath()
	cookiePath := daemonCookiePath()

	srv := daemon.NewServer(rt, socketPath, cookiePath, version)
	srv.SetInstrumentation(rt.metrics, rt.audit)
	if err := srv.Start(); err != nil {
		rt.Shutdown()
		fatal("Daemon API failed to start: %v", err)
	}

	// Start metrics endpoint (no-op if telemetry disabled)
	rt.StartMetricsServer()

	fmt.Printf("Daemon API: %s\n", socketPath)
	fmt.Println()

	// Watchdog with socket health check
	rt.StartWatchdog(watchdog.HealthCheck{
		Name: "daemon-socket",
		Check: func() error {
			if srv.Listener() == nil {
				return fmt.Errorf("daemon socket not listening")
			}
			return nil
		},
	})

	rt.StartStatusPrinter()

	// Wait for signal or API-initiated shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		fmt.Printf("\nReceived %s, shutting down...\n", sig)
	case <-srv.ShutdownCh():
		fmt.Println("\nShutdown requested via API")
	case <-ctx.Done():
	}

	srv.Stop()
	rt.Shutdown()
	fmt.Println("Daemon stopped.")
}

// --- Client helper ---

func daemonClient() *daemon.Client {
	c, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	return c
}

// tryDaemonClient attempts to connect to a running daemon.
// Returns nil if the daemon is not running or unreachable.
func tryDaemonClient() *daemon.Client {
	c, err := daemon.NewClient(daemonSocketPath(), daemonCookiePath())
	if err != nil {
		return nil
	}
	return c
}

// --- Client subcommands ---

func runDaemonStatus(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Status()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.StatusText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonStop() {
	c := daemonClient()
	if err := c.Shutdown(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Println("Shutdown requested.")
}

func runDaemonPing(args []string) {
	args = reorderArgs(args, map[string]bool{"json": true})

	fs := flag.NewFlagSet("daemon ping", flag.ExitOnError)
	count := fs.Int("c", 4, "number of pings")
	intervalMs := fs.Int("interval", 1000, "interval between pings (ms)")
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: peerup daemon ping <peer> [-c N] [--json]")
		osExit(1)
	}

	peer := remaining[0]
	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Ping(peer, *count, *intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PingText(peer, *count, *intervalMs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonServices(args []string) {
	fs := flag.NewFlagSet("daemon services", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Services()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.ServicesText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonPeers(args []string) {
	fs := flag.NewFlagSet("daemon peers", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	allFlag := fs.Bool("all", false, "show all connected peers (including DHT/IPFS neighbors)")
	fs.Parse(args)

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Peers(*allFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PeersText(*allFlag)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonPaths(args []string) {
	fs := flag.NewFlagSet("daemon paths", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	c := daemonClient()

	if *jsonFlag {
		resp, err := c.Paths()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(resp)
	} else {
		text, err := c.PathsText()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			osExit(1)
		}
		fmt.Print(text)
	}
}

func runDaemonConnect(args []string) {
	fs := flag.NewFlagSet("daemon connect", flag.ExitOnError)
	peerFlag := fs.String("peer", "", "peer name or ID")
	serviceFlag := fs.String("service", "", "service name")
	listenFlag := fs.String("listen", "", "local listen address (e.g. 127.0.0.1:2222)")
	fs.Parse(args)

	if *peerFlag == "" || *serviceFlag == "" || *listenFlag == "" {
		fmt.Fprintln(os.Stderr, "Usage: peerup daemon connect --peer <name> --service <svc> --listen <addr>")
		osExit(1)
	}

	c := daemonClient()
	resp, err := c.Connect(*peerFlag, *serviceFlag, *listenFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Printf("Proxy created: %s -> %s:%s (listen: %s)\n", resp.ID, *peerFlag, *serviceFlag, resp.ListenAddress)
}

func runDaemonDisconnect(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: peerup daemon disconnect <proxy-id>")
		osExit(1)
	}

	c := daemonClient()
	if err := c.Disconnect(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Println("Proxy disconnected.")
}
