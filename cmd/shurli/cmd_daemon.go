package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/internal/daemon"
	"github.com/shurlinet/shurli/internal/watchdog"
	"github.com/shurlinet/shurli/pkg/p2pnet"
	"github.com/shurlinet/shurli/pkg/plugin"
	"github.com/shurlinet/shurli/plugins"
)

// --- RuntimeInfo adapter (implements daemon.RuntimeInfo on serveRuntime) ---

func (rt *serveRuntime) Network() *p2pnet.Network            { return rt.network }
func (rt *serveRuntime) ConfigFile() string                   { return rt.configFile }
func (rt *serveRuntime) AuthKeysPath() string                 { return rt.authKeys }
func (rt *serveRuntime) Version() string                      { return rt.version }
func (rt *serveRuntime) StartTime() time.Time                 { return rt.startTime }
func (rt *serveRuntime) PingProtocolID() string               { return rt.config.Protocols.PingPong.ID }
func (rt *serveRuntime) Interfaces() *p2pnet.InterfaceSummary { return rt.ifSummary }
func (rt *serveRuntime) PathTracker() *p2pnet.PathTracker         { return rt.pathTracker }
func (rt *serveRuntime) BandwidthTracker() *p2pnet.BandwidthTracker { return rt.bwTracker }
func (rt *serveRuntime) RelayHealth() *p2pnet.RelayHealth           { return rt.relayHealth }
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

func (rt *serveRuntime) RelayAddresses() []string  { return rt.config.Relay.Addresses }
func (rt *serveRuntime) DiscoveryNetwork() string   { return rt.config.Discovery.Network }

func (rt *serveRuntime) RelayMOTDs() []daemon.MOTDInfo {
	if rt.motdClient == nil {
		return nil
	}

	h := rt.network.Host()
	var result []daemon.MOTDInfo

	for _, addrStr := range rt.config.Relay.Addresses {
		maddr, err := ma.NewMultiaddr(addrStr)
		if err != nil {
			continue
		}
		info, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		relayPeer := info.ID

		// Parse relay name from AgentVersion.
		relayName := ""
		if av, avErr := h.Peerstore().Get(relayPeer, "AgentVersion"); avErr == nil {
			if s, ok := av.(string); ok {
				relayName = parseRelayName(s)
			}
		}

		// Check for goodbye (persisted to disk).
		if msg, ts, ok := rt.motdClient.GetStoredGoodbye(relayPeer); ok {
			result = append(result, daemon.MOTDInfo{
				RelayPeerID: relayPeer.String(),
				RelayName:   relayName,
				Message:     msg,
				Type:        "goodbye",
				Timestamp:   time.Unix(ts, 0).UTC().Format(time.RFC3339),
			})
			continue // goodbye supersedes MOTD
		}

		// Check for MOTD (in-memory, 24h window).
		if msg, ts, ok := rt.motdClient.GetLastMOTD(relayPeer); ok {
			result = append(result, daemon.MOTDInfo{
				RelayPeerID: relayPeer.String(),
				RelayName:   relayName,
				Message:     msg,
				Type:        "motd",
				Timestamp:   time.Unix(ts, 0).UTC().Format(time.RFC3339),
			})
		}
	}
	return result
}

// parseRelayName extracts the name from a relay UserAgent like "relay-server/0.1.0 (AU-Sydney)".
func parseRelayName(agentVersion string) string {
	start := strings.Index(agentVersion, "(")
	end := strings.LastIndex(agentVersion, ")")
	if start >= 0 && end > start+1 {
		return agentVersion[start+1 : end]
	}
	return ""
}

func (rt *serveRuntime) ConfigReloader() daemon.ConfigReloader {
	return &configReloader{rt: rt}
}

func (rt *serveRuntime) GaterForHotReload() daemon.GaterReloader {
	if rt.gater == nil || rt.authKeys == "" {
		return nil
	}
	return &gaterReloader{
		gater:        rt.gater,
		authKeysPath: rt.authKeys,
		peerManager:  rt.peerManager,
	}
}

// gaterReloader implements daemon.GaterReloader by re-reading the
// authorized_keys file and updating the live connection gater.
// Also syncs PeerManager's watchlist so newly authorized peers
// are immediately reconnected.
type gaterReloader struct {
	gater        *auth.AuthorizedPeerGater
	authKeysPath string
	peerManager  *p2pnet.PeerManager // nil-safe
}

func (g *gaterReloader) ReloadFromFile() error {
	peers, err := auth.LoadAuthorizedKeys(g.authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to reload authorized_keys: %w", err)
	}
	g.gater.UpdateAuthorizedPeers(peers)
	if g.peerManager != nil {
		g.peerManager.SetWatchlist(g.gater.GetAuthorizedPeerIDs())
	}
	return nil
}

// configReloader implements daemon.ConfigReloader by re-reading the
// config file from disk and cascading changes to live subsystems.
type configReloader struct {
	rt *serveRuntime
}

func (cr *configReloader) ReloadConfig() (*daemon.ConfigReloadResult, error) {
	result := &daemon.ConfigReloadResult{}

	// Re-read config from disk.
	newCfg, err := config.LoadNodeConfig(cr.rt.configFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	config.ResolveConfigPaths(newCfg, filepath.Dir(cr.rt.configFile))
	if err := config.ValidateNodeConfig(newCfg); err != nil {
		return nil, fmt.Errorf("config validation failed: %w", err)
	}

	// Transfer config reload is now handled by the filetransfer plugin's
	// OnConfigReload callback, triggered via registry.NotifyConfigReload()
	// in handleConfigReload after this function returns.

	// Authorized keys (connection gating) - always refresh on reload.
	if reloader := cr.rt.GaterForHotReload(); reloader != nil {
		if err := reloader.ReloadFromFile(); err != nil {
			return nil, fmt.Errorf("authorized_keys reload failed: %w", err)
		}
		result.Changed = append(result.Changed, "security.authorized_keys")
	}

	// Update the stored config pointer for future comparisons.
	cr.rt.config = newCfg

	if len(result.Changed) == 0 {
		result.Changed = []string{} // empty slice, not nil (cleaner JSON)
	}
	return result, nil
}

// --- Daemon paths ---

func daemonSocketPath() string {
	dir, err := config.DefaultConfigDir()
	if err != nil {
		fatal("Cannot determine config directory: %v", err)
	}
	return filepath.Join(dir, "shurli.sock")
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
	fmt.Println("Usage: shurli daemon [subcommand]")
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
	fs.Parse(reorderFlags(fs, args))

	fmt.Printf("shurli daemon %s (%s)\n", version, commit)
	fmt.Println()

	ctx, cancel := context.WithCancel(context.Background())

	rt, err := newServeRuntime(ctx, cancel, *configFlag, version)
	if err != nil {
		cancel()
		fatal("Failed to start: %v", err)
	}

	// Register protocol handlers BEFORE Bootstrap so they're ready when
	// the relay fires reconnect-notifier on our connection. Without this,
	// the relay tries to deliver peer introductions before the handler exists.
	rt.SetupPingPong()
	rt.SetupPeerNotify()
	rt.SetupMOTDClient()

	// Check plugin directory permissions (for future WASM plugins).
	// Layer 2 will make this a hard error; for now it's a warning.
	pluginDir := filepath.Join(filepath.Dir(rt.configFile), "plugins")
	if info, err := os.Stat(pluginDir); err == nil {
		if info.Mode().Perm() != 0700 {
			slog.Warn("plugin.dir-perms",
				"path", pluginDir,
				"perms", fmt.Sprintf("%04o", info.Mode().Perm()),
				"expected", "0700")
		}
	}

	// Create plugin registry and register compiled-in plugins.
	pluginProvider := &plugin.ContextProvider{
		Network:         rt.network,
		ServiceRegistry: rt.network.ServiceRegistry(),
		ConfigDir:       filepath.Dir(rt.configFile),
		NameResolver:    rt.network.ResolveName,
		PeerConnector:   rt.ConnectToPeer,
	}
	pluginRegistry := plugin.NewRegistry(pluginProvider)
	plugins.RegisterAll(pluginRegistry)
	if states := rt.config.Plugins.PluginStates(); states != nil {
		pluginRegistry.ApplyConfig(states)
	}
	pluginRegistry.StartAll()

	if err := rt.Bootstrap(); err != nil {
		rt.Shutdown()
		fatal("Bootstrap failed: %v", err)
	}

	// Notify plugins that network is ready (post-bootstrap).
	pluginRegistry.NotifyNetworkReady()

	rt.ExposeConfiguredServices()
	rt.StartPeerHistorySaver()

	// Start daemon API server
	socketPath := daemonSocketPath()
	cookiePath := daemonCookiePath()

	srv := daemon.NewServer(rt, socketPath, cookiePath, version)
	srv.SetInstrumentation(rt.metrics, rt.audit)
	srv.SetRegistry(pluginRegistry)
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
	rt.StartDHTHealthCheck()

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
	pluginRegistry.StopAll()
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

// configAllowsStandalone loads the node config and returns true if
// cli.allow_standalone is set. Returns false on any config load error
// (missing config is not an error condition for this check).
func configAllowsStandalone(configPath string) bool {
	cfgFile, err := config.FindConfigFile(configPath)
	if err != nil {
		return false
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return false
	}
	return cfg.CLI.AllowStandalone
}

// --- Client subcommands ---

func runDaemonStatus(args []string) {
	fs := flag.NewFlagSet("daemon status", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(reorderFlags(fs, args))

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
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 1 {
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon ping <peer> [-c N] [--json]")
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
	fs.Parse(reorderFlags(fs, args))

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
	fs.Parse(reorderFlags(fs, args))

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
	fs.Parse(reorderFlags(fs, args))

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
	fs.Parse(reorderFlags(fs, args))

	if *peerFlag == "" || *serviceFlag == "" || *listenFlag == "" {
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon connect --peer <name> --service <svc> --listen <addr>")
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
		fmt.Fprintln(os.Stderr, "Usage: shurli daemon disconnect <proxy-id>")
		osExit(1)
	}

	c := daemonClient()
	if err := c.Disconnect(args[0]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
	fmt.Println("Proxy disconnected.")
}
