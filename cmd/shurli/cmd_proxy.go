package main

import (
	"context"
	"flag"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/daemon"
	tc "github.com/shurlinet/shurli/internal/termcolor"
	"github.com/shurlinet/shurli/pkg/sdk"
)

func runProxy(args []string) {
	// Subcommand dispatch: shurli proxy <subcommand> [args]
	// Backward compat: shurli proxy <target> <service> <port> (ephemeral foreground)
	if len(args) > 0 {
		switch args[0] {
		case "add":
			runProxyAdd(args[1:])
			return
		case "list", "ls":
			runProxyList(args[1:])
			return
		case "remove", "rm":
			runProxyRemove(args[1:])
			return
		case "enable":
			runProxyEnable(args[1:])
			return
		case "disable":
			runProxyDisable(args[1:])
			return
		}
	}

	// Backward compat: shurli proxy [flags] <target> <service> <port>
	runProxyEphemeral(args)
}

// --- Persistent proxy subcommands ---

func runProxyAdd(args []string) {
	fs := flag.NewFlagSet("proxy add", flag.ExitOnError)
	fs.Parse(args)

	remaining := fs.Args()
	if len(remaining) < 4 {
		fmt.Println("Usage: shurli proxy add <name> <peer> <service> <port>")
		fmt.Println()
		fmt.Println("Create a persistent proxy that survives daemon restarts.")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli proxy add home-ssh home-node ssh 2222")
		fmt.Println("  shurli proxy add work-rdp work xrdp 13389")
		osExit(1)
	}

	name := remaining[0]
	peer := remaining[1]
	service := remaining[2]
	port, err := strconv.Atoi(remaining[3])
	if err != nil || port < 1 || port > 65535 {
		fatal("Invalid port: %s (must be 1-65535)", remaining[3])
	}

	client := tryDaemonClient()
	if client == nil {
		fatal("Daemon not running. Start it with: shurli daemon")
	}

	resp, err := client.ProxyAdd(name, peer, service, port)
	if err != nil {
		fatal("Failed to add proxy: %v", err)
	}

	tc.Wgreen(os.Stdout, "Proxy %q added\n", resp.Name)
	fmt.Printf("  Listen: %s\n", resp.ListenAddress)
	fmt.Printf("  Status: %s\n", resp.Status)
	fmt.Println()
	fmt.Println("The proxy persists across daemon restarts.")
	fmt.Printf("Manage with: shurli proxy list, shurli proxy remove %s\n", name)
}

func runProxyList(args []string) {
	fs := flag.NewFlagSet("proxy list", flag.ExitOnError)
	jsonFlag := fs.Bool("json", false, "output as JSON")
	fs.Parse(args)

	client := tryDaemonClient()
	if client != nil {
		resp, err := client.ProxyList()
		if err != nil {
			fatal("Failed to list proxies: %v", err)
		}
		if *jsonFlag {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			enc.Encode(resp)
			return
		}
		if len(resp.Proxies) == 0 {
			fmt.Println("No proxies configured.")
			fmt.Println()
			fmt.Println("Add one with: shurli proxy add <name> <peer> <service> <port>")
			return
		}
		printProxyTable(resp.Proxies)
		return
	}

	// EDGE-7: Daemon not running — read proxies.json directly.
	configDir := daemonConfigDir()
	proxies, err := daemon.ProxyListOffline(configDir)
	if err != nil {
		fatal("Failed to read proxy config: %v", err)
	}
	if *jsonFlag {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(daemon.ProxyListResponse{Proxies: proxies})
		return
	}
	if len(proxies) == 0 {
		fmt.Println("No proxies configured.")
		return
	}
	tc.Wfaint(os.Stdout, "(daemon not running — showing saved config)\n\n")
	printProxyTable(proxies)
}

func printProxyTable(proxies []daemon.ProxyStatusInfo) {
	// Header
	fmt.Printf("%-16s %-14s %-10s %-22s %-8s %s\n",
		"NAME", "PEER", "SERVICE", "LISTEN", "ENABLED", "STATUS")
	fmt.Println(strings.Repeat("-", 90))

	for _, p := range proxies {
		listen := p.Listen
		if listen == "" && p.Port > 0 {
			listen = fmt.Sprintf("127.0.0.1:%d", p.Port)
		}
		enabled := "yes"
		if !p.Enabled {
			enabled = "no"
		}
		// Color-code status.
		status := p.Status
		fmt.Printf("%-16s %-14s %-10s %-22s %-8s ", p.Name, p.Peer, p.Service, listen, enabled)
		switch {
		case status == "active":
			tc.Wgreen(os.Stdout, "%s\n", status)
		case status == "waiting":
			tc.Wyellow(os.Stdout, "%s\n", status)
		case status == "disabled":
			tc.Wfaint(os.Stdout, "%s\n", status)
		case strings.HasPrefix(status, "error"):
			tc.Wred(os.Stdout, "%s\n", status)
		default:
			fmt.Printf("%s\n", status)
		}
	}
}

func runProxyRemove(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: shurli proxy remove <name>")
		osExit(1)
	}
	name := args[0]

	client := tryDaemonClient()
	if client == nil {
		fatal("Daemon not running. Start it with: shurli daemon")
	}

	if err := client.ProxyRemove(name); err != nil {
		fatal("Failed to remove proxy: %v", err)
	}
	fmt.Printf("Proxy %q removed.\n", name)
}

func runProxyEnable(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: shurli proxy enable <name>")
		osExit(1)
	}
	name := args[0]

	client := tryDaemonClient()
	if client == nil {
		fatal("Daemon not running. Start it with: shurli daemon")
	}

	if err := client.ProxyEnable(name); err != nil {
		fatal("Failed to enable proxy: %v", err)
	}
	fmt.Printf("Proxy %q enabled.\n", name)
}

func runProxyDisable(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: shurli proxy disable <name>")
		osExit(1)
	}
	name := args[0]

	client := tryDaemonClient()
	if client == nil {
		fatal("Daemon not running. Start it with: shurli daemon")
	}

	if err := client.ProxyDisable(name); err != nil {
		fatal("Failed to disable proxy: %v", err)
	}
	fmt.Printf("Proxy %q disabled.\n", name)
}

// --- Ephemeral foreground proxy (backward compat) ---

func runProxyEphemeral(args []string) {
	fs := flag.NewFlagSet("proxy", flag.ExitOnError)
	configFlag := fs.String("config", "", "path to config file")
	standaloneFlag := fs.Bool("standalone", false, "use direct P2P without daemon (debug)")
	fs.Parse(reorderFlags(fs, args))

	remaining := fs.Args()
	if len(remaining) < 3 {
		fmt.Println("Usage: shurli proxy <command> [args]")
		fmt.Println()
		fmt.Println("Persistent proxy management:")
		fmt.Println("  add <name> <peer> <service> <port>   Create a persistent proxy")
		fmt.Println("  list [--json]                         List all proxies")
		fmt.Println("  remove <name>                         Remove a proxy")
		fmt.Println("  enable <name>                         Enable a disabled proxy")
		fmt.Println("  disable <name>                        Disable without removing")
		fmt.Println()
		fmt.Println("Ephemeral (foreground, stops on Ctrl+C):")
		fmt.Println("  shurli proxy <target> <service> <local-port>")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  shurli proxy add home-ssh home-node ssh 2222")
		fmt.Println("  shurli proxy list")
		fmt.Println("  shurli proxy home ssh 2222")
		osExit(1)
	}

	target := remaining[0]
	serviceName := remaining[1]
	localPort := remaining[2]

	// Standalone allowed via CLI flag or config setting.
	allowStandalone := *standaloneFlag || configAllowsStandalone(*configFlag)

	// Try daemon first (faster, uses daemon's managed connection with
	// PeerManager path upgrades, mDNS, IPv6 probing).
	if !allowStandalone {
		if client := tryDaemonClient(); client != nil {
			runProxyViaDaemon(client, target, serviceName, localPort)
			return
		}
	}

	// Standalone P2P host (no daemon running, or --standalone forced)
	runProxyStandalone(target, serviceName, localPort, *configFlag, allowStandalone)
}

// runProxyViaDaemon creates a TCP proxy through the running daemon.
// The daemon's host handles the P2P connection, so the proxy benefits from
// PeerManager's automatic path upgrades (relay to direct).
func runProxyViaDaemon(client *daemon.Client, target, service, port string) {
	listenAddr := fmt.Sprintf("localhost:%s", port)

	tc.Wblue(os.Stdout, "=== TCP Proxy via P2P (daemon) ===\n")
	tc.Wblue(os.Stdout, "Service: ")
	fmt.Printf("%s\n", service)
	fmt.Println()

	// Show verification badge
	showVerificationBadge(client, target)

	fmt.Println("Connecting to target peer...")
	resp, err := client.Connect(target, service, listenAddr)
	if err != nil {
		fatal("Failed to create proxy: %v", err)
	}

	if resp.PathType != "" {
		tc.Wgreen(os.Stdout, "Connected")
		tc.Wfaint(os.Stdout, " [%s] via %s", resp.PathType, resp.Address)
		fmt.Println()
	} else {
		tc.Wgreen(os.Stdout, "Connected\n")
	}
	fmt.Println()
	tc.Wblue(os.Stdout, "TCP proxy listening on ")
	fmt.Printf("%s\n", resp.ListenAddress)
	fmt.Println()
	fmt.Println("Connect to the service:")
	fmt.Printf("   %s -> %s service on target\n", resp.ListenAddress, service)
	tc.Wfaint(os.Stdout, "\nPress Ctrl+C to stop.")
	fmt.Println()

	// Block until Ctrl+C
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	if err := client.Disconnect(resp.ID); err != nil {
		log.Printf("Disconnect error: %v", err)
	}
}

// runProxyStandalone creates a TCP proxy with its own P2P host.
// Used when no daemon is running (debug/development mode).
func runProxyStandalone(target, serviceName, localPort, configPath string, allowStandalone bool) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Require explicit --standalone or config setting when daemon is not available.
	if !allowStandalone {
		fmt.Println("Daemon not running. Start it with:")
		fmt.Println("  shurli daemon")
		fmt.Println()
		fmt.Println("Or use --standalone flag for direct P2P (debug):")
		fmt.Printf("  shurli proxy --standalone %s %s %s\n", target, serviceName, localPort)
		fmt.Println()
		fmt.Println("Or set cli.allow_standalone: true in config for persistent standalone access.")
		osExit(1)
	}

	// Create standalone P2P host from config.
	pw, _ := resolvePasswordFromConfig(configPath)
	standalone, err := sdk.NewStandaloneHost(sdk.StandaloneConfig{
		ConfigPath: configPath,
		Password:   pw,
		UserAgent:  "shurli/" + version,
	})
	if err != nil {
		fatal("%v", err)
	}
	p2pNetwork := standalone.Network
	cfg := standalone.NodeConfig
	defer p2pNetwork.Close()

	tc.Wblue(os.Stdout, "=== TCP Proxy via P2P (standalone) ===\n")
	tc.Wblue(os.Stdout, "Service: ")
	fmt.Printf("%s\n", serviceName)
	fmt.Println()

	// Resolve target (name or peer ID)
	homePeerID, err := p2pNetwork.ResolveName(target)
	if err != nil {
		fatal("Cannot resolve target %q: %v", target, err)
	}

	h := p2pNetwork.Host()

	fmt.Printf("Client Peer ID: %s\n", p2pNetwork.PeerID())
	fmt.Printf("Target Peer: %s\n", homePeerID)
	if target != homePeerID.String() {
		fmt.Printf("   (resolved from name %q)\n", target)
	}
	fmt.Println()

	// Bootstrap DHT for peer discovery
	fmt.Println("Bootstrapping DHT...")
	dhtPrefix := sdk.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	var kdht *dht.IpfsDHT
	kdht, err = dht.New(ctx, h,
		dht.Mode(dht.ModeClient),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
		dht.RoutingTablePeerDiversityFilter(dht.NewRTPeerDiversityFilter(h, 3, 50)),
	)
	if err != nil {
		log.Printf("DHT init failed (relay-only mode): %v", err)
		kdht = nil
	} else {
		if err := kdht.Bootstrap(ctx); err != nil {
			log.Printf("DHT bootstrap failed (relay-only mode): %v", err)
			kdht = nil
		}
	}

	// Connect to target using parallel path racing (DHT + relay simultaneously)
	fmt.Println("Connecting to target peer...")
	pd := sdk.NewPathDialer(h, kdht, &sdk.StaticRelaySource{Addrs: cfg.Relay.Addresses}, nil)
	connectCtx, connectCancel := context.WithTimeout(ctx, 45*time.Second)
	result, err := pd.DialPeer(connectCtx, homePeerID)
	connectCancel()
	if err != nil {
		fatal("Failed to connect to target: %v", err)
	}
	tc.Wgreen(os.Stdout, "Connected")
	tc.Wfaint(os.Stdout, " [%s] via %s (%s)", result.PathType, result.Address, result.Duration.Round(time.Millisecond))
	fmt.Println()
	fmt.Println()

	// Create TCP listener with retry-enabled dial function.
	// Each incoming TCP connection triggers a P2P stream dial with
	// exponential backoff (3 retries: 1s, 2s, 4s) to handle transient
	// relay disconnections without failing the user's connection.
	localAddr := fmt.Sprintf("localhost:%s", localPort)
	dialFunc := sdk.DialWithRetry(func() (sdk.ServiceConn, error) {
		return p2pNetwork.ConnectToService(homePeerID, serviceName)
	}, 3)
	listener, err := sdk.NewTCPListener(localAddr, dialFunc)
	if err != nil {
		fatal("Failed to create listener: %v", err)
	}
	defer listener.Close()

	tc.Wblue(os.Stdout, "TCP proxy listening on ")
	fmt.Printf("%s\n", localAddr)
	fmt.Println()
	fmt.Println("Connect to the service:")
	fmt.Printf("   %s -> %s service on target\n", localAddr, serviceName)
	tc.Wfaint(os.Stdout, "\nPress Ctrl+C to stop.")
	fmt.Println()

	// Handle graceful shutdown
	shutdownCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("\nShutting down...")
		close(shutdownCh)
		cancel()         // stop DHT and background goroutines
		listener.Close() // causes Serve() to return
	}()

	// Serve connections (blocks until listener is closed)
	if err := listener.Serve(); err != nil {
		select {
		case <-shutdownCh:
			// Intentional shutdown  - don't log the accept error
		default:
			log.Printf("Listener stopped: %v", err)
		}
	}
}
