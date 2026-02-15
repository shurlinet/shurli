package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/identity"
	"github.com/satindergrewal/peer-up/internal/watchdog"
)

// Set via -ldflags at build time.
var (
	version   = "dev"
	commit    = "unknown"
	buildDate = "unknown"
)

// buildRelayResources converts config resource settings into relayv2 types.
func buildRelayResources(rc *config.RelayResourcesConfig) (relayv2.Resources, *relayv2.RelayLimit) {
	// Parse durations (already validated by ValidateRelayServerConfig)
	reservationTTL, _ := time.ParseDuration(rc.ReservationTTL)
	sessionDuration, _ := time.ParseDuration(rc.SessionDuration)
	sessionDataLimit, _ := config.ParseDataSize(rc.SessionDataLimit)

	resources := relayv2.Resources{
		Limit: &relayv2.RelayLimit{
			Duration: sessionDuration,
			Data:     sessionDataLimit,
		},
		ReservationTTL:        reservationTTL,
		MaxReservations:       rc.MaxReservations,
		MaxCircuits:           rc.MaxCircuits,
		BufferSize:            rc.BufferSize,
		MaxReservationsPerPeer: 1,
		MaxReservationsPerIP:  rc.MaxReservationsPerIP,
		MaxReservationsPerASN: rc.MaxReservationsPerASN,
	}

	limit := &relayv2.RelayLimit{
		Duration: sessionDuration,
		Data:     sessionDataLimit,
	}

	return resources, limit
}

// loadAuthKeysPath loads config and returns the authorized_keys file path.
func loadAuthKeysPath() string {
	cfg, err := config.LoadRelayServerConfig("relay-server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	if cfg.Security.AuthorizedKeysFile == "" {
		log.Fatal("No authorized_keys_file configured in relay-server.yaml")
	}
	return cfg.Security.AuthorizedKeysFile
}

func runAuthorize(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: relay-server authorize <peer-id> [comment]")
		fmt.Println()
		fmt.Println("Examples:")
		fmt.Println("  ./relay-server authorize 12D3KooW... home-node")
		fmt.Println("  ./relay-server authorize 12D3KooW... laptop")
		os.Exit(1)
	}

	peerID := args[0]
	comment := ""
	if len(args) > 1 {
		comment = strings.Join(args[1:], " ")
	}

	authKeysPath := loadAuthKeysPath()
	if err := auth.AddPeer(authKeysPath, peerID, comment); err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Authorized: %s\n", peerID[:16]+"...")
	if comment != "" {
		fmt.Printf("Comment:    %s\n", comment)
	}
	fmt.Printf("File:       %s\n", authKeysPath)
	fmt.Println()
	fmt.Println("Restart relay to apply: sudo systemctl restart relay-server")
}

func runDeauthorize(args []string) {
	if len(args) < 1 {
		fmt.Println("Usage: relay-server deauthorize <peer-id>")
		os.Exit(1)
	}

	peerID := args[0]
	authKeysPath := loadAuthKeysPath()
	if err := auth.RemovePeer(authKeysPath, peerID); err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Deauthorized: %s\n", peerID[:16]+"...")
	fmt.Println()
	fmt.Println("Restart relay to apply: sudo systemctl restart relay-server")
}

func runListPeers() {
	authKeysPath := loadAuthKeysPath()
	peers, err := auth.ListPeers(authKeysPath)
	if err != nil {
		log.Fatalf("Error: %v", err)
	}

	fmt.Printf("Authorized peers (%s):\n\n", authKeysPath)
	if len(peers) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, p := range peers {
			if p.Comment != "" {
				fmt.Printf("  %s  # %s\n", p.PeerID, p.Comment)
			} else {
				fmt.Printf("  %s\n", p.PeerID)
			}
		}
	}
	fmt.Printf("\nTotal: %d peer(s)\n", len(peers))
}

func runInfo() {
	cfg, err := config.LoadRelayServerConfig("relay-server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Read identity key (don't auto-create — info is read-only)
	data, err := os.ReadFile(cfg.Identity.KeyFile)
	if err != nil {
		log.Fatalf("Cannot read identity key %s: %v\n  Run the relay server once to generate a key.", cfg.Identity.KeyFile, err)
	}
	priv, err := crypto.UnmarshalPrivateKey(data)
	if err != nil {
		log.Fatalf("Invalid identity key: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		log.Fatalf("Failed to derive peer ID: %v", err)
	}

	fmt.Printf("Peer ID: %s\n", peerID)

	// Connection gating status
	if cfg.Security.EnableConnectionGating {
		fmt.Println("Connection gating: enabled")
	} else {
		fmt.Println("Connection gating: DISABLED")
	}

	// Authorized peers count
	if cfg.Security.AuthorizedKeysFile != "" {
		peers, err := auth.ListPeers(cfg.Security.AuthorizedKeysFile)
		if err == nil {
			fmt.Printf("Authorized peers: %d\n", len(peers))
		}
	}
	fmt.Println()

	// Detect public IPs and construct multiaddrs for all configured transports
	publicIPs := detectPublicIPs()
	multiaddrs := buildPublicMultiaddrs(cfg.Network.ListenAddresses, publicIPs, peerID)

	if len(multiaddrs) > 0 {
		fmt.Println("Multiaddrs:")
		for _, maddr := range multiaddrs {
			fmt.Printf("  %s\n", maddr)
		}

		// Find primary TCP multiaddr (IPv4 preferred) for QR code and quick setup
		primaryAddr := ""
		primaryPort := ""
		for _, maddr := range multiaddrs {
			if strings.Contains(maddr, "/ip4/") && strings.Contains(maddr, "/tcp/") && !strings.Contains(maddr, "/ws") {
				primaryAddr = maddr
				primaryPort = extractTCPPort([]string{maddr})
				break
			}
		}
		if primaryAddr == "" && len(multiaddrs) > 0 {
			primaryAddr = multiaddrs[0]
			primaryPort = extractTCPPort([]string{multiaddrs[0]})
		}

		// QR code (use qrencode if available)
		if primaryAddr != "" {
			if qrPath, err := exec.LookPath("qrencode"); err == nil && qrPath != "" {
				fmt.Println()
				fmt.Println("Scan this QR code during 'peerup init':")
				cmd := exec.Command("qrencode", "-t", "ANSIUTF8", primaryAddr)
				cmd.Stdout = os.Stdout
				_ = cmd.Run()
			}
		}

		fmt.Println()
		fmt.Println("Quick setup:")
		for _, ip := range publicIPs {
			if !strings.Contains(ip, ":") && primaryPort != "" {
				fmt.Printf("  peerup init  →  enter: %s:%s\n", ip, primaryPort)
			}
		}
		fmt.Printf("  Peer ID: %s\n", peerID)
	} else {
		fmt.Println("Multiaddrs: could not detect public IPs")
	}
}

// buildPublicMultiaddrs constructs public multiaddrs from listen addresses by
// replacing bind addresses (0.0.0.0, ::) with detected public IPs.
// Handles all transport types: TCP, QUIC, WebSocket, WebTransport.
func buildPublicMultiaddrs(listenAddrs []string, publicIPs []string, peerID peer.ID) []string {
	var result []string
	for _, listen := range listenAddrs {
		for _, ip := range publicIPs {
			isIPv6 := strings.Contains(ip, ":")
			proto := "ip4"
			if isIPv6 {
				proto = "ip6"
			}
			// Only match listen addresses with the same IP version
			if strings.HasPrefix(listen, "/ip4/") && isIPv6 {
				continue
			}
			if strings.HasPrefix(listen, "/ip6/") && !isIPv6 {
				continue
			}
			// Replace the bind address with the public IP
			maddr := listen
			maddr = strings.Replace(maddr, "/ip4/0.0.0.0", "/"+proto+"/"+ip, 1)
			maddr = strings.Replace(maddr, "/ip6/::", "/"+proto+"/"+ip, 1)
			maddr += "/p2p/" + peerID.String()
			result = append(result, maddr)
		}
	}
	return result
}

// detectPublicIPs returns non-private, non-loopback IP addresses from network interfaces.
func detectPublicIPs() []string {
	var ips []string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ips
	}
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if !ip.IsGlobalUnicast() {
			continue
		}
		// Skip private IPv4 (10/8, 172.16/12, 192.168/16)
		if ip4 := ip.To4(); ip4 != nil {
			if ip4[0] == 10 ||
				(ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31) ||
				(ip4[0] == 192 && ip4[1] == 168) {
				continue
			}
		}
		// Skip ULA IPv6 (fc00::/7)
		if ip.To4() == nil && len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc {
			continue
		}
		ips = append(ips, ip.String())
	}
	return ips
}

// extractTCPPort finds the first TCP port from multiaddr listen addresses.
func extractTCPPort(listenAddresses []string) string {
	for _, addr := range listenAddresses {
		parts := strings.Split(addr, "/")
		for i, part := range parts {
			if part == "tcp" && i+1 < len(parts) {
				return parts[i+1]
			}
		}
	}
	return "7777"
}

func printUsage() {
	fmt.Println("Usage: relay-server [command]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  (no command)                        Start the relay server")
	fmt.Println("  info                                Show peer ID, multiaddrs, QR code")
	fmt.Println("  authorize <peer-id> [comment]       Allow a peer to use this relay")
	fmt.Println("  deauthorize <peer-id>               Remove a peer's access")
	fmt.Println("  list-peers                          List authorized peers")
	fmt.Println("  help                                Show this help message")
	fmt.Println()
	fmt.Println("Setup (via bash script):")
	fmt.Println("  bash setup.sh                       Full setup (build, systemd, firewall)")
	fmt.Println("  bash setup.sh --check               Health check only")
	fmt.Println("  bash setup.sh --uninstall           Remove service and config")
}

func main() {
	// Handle subcommands before starting the relay
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "help", "--help", "-h":
			printUsage()
			return
		case "version", "--version":
			fmt.Printf("relay-server %s (%s) built %s\n", version, commit, buildDate)
			fmt.Printf("Go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
			return
		case "authorize":
			runAuthorize(os.Args[2:])
			return
		case "deauthorize":
			runDeauthorize(os.Args[2:])
			return
		case "list-peers":
			runListPeers()
			return
		case "info":
			runInfo()
			return
		default:
			fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
			printUsage()
			os.Exit(1)
		}
	}

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Printf("=== Private libp2p Relay Server (%s) ===\n", version)
	fmt.Println()

	// Load configuration
	cfg, err := config.LoadRelayServerConfig("relay-server.yaml")
	if err != nil {
		log.Fatalf("Failed to load config: %v\n", err)
		fmt.Println("Please create relay-server.yaml from the sample:")
		fmt.Println("  cp configs/relay-server.sample.yaml relay-server.yaml")
		os.Exit(1)
	}

	// Validate configuration
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	// Archive last-known-good config on successful validation
	if err := config.Archive("relay-server.yaml"); err != nil {
		log.Printf("Warning: failed to archive config: %v", err)
	}

	fmt.Printf("Loaded configuration from relay-server.yaml\n")
	fmt.Printf("Authentication: %v\n", cfg.Security.EnableConnectionGating)
	fmt.Println()

	priv, err := identity.LoadOrCreateIdentity(cfg.Identity.KeyFile)
	if err != nil {
		log.Fatalf("Identity error: %v", err)
	}

	// Load authorized keys if connection gating is enabled
	var gater *auth.AuthorizedPeerGater
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			log.Fatalf("Connection gating enabled but no authorized_keys_file specified")
		}

		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			log.Fatalf("Failed to load authorized keys: %v", err)
		}

		if len(authorizedPeers) == 0 {
			fmt.Println("⚠️  WARNING: authorized_keys file is empty - no peers can make reservations!")
			fmt.Printf("   Add authorized peer IDs to %s\n", cfg.Security.AuthorizedKeysFile)
		} else {
			fmt.Printf("✅ Loaded %d authorized peer(s) from %s\n", len(authorizedPeers), cfg.Security.AuthorizedKeysFile)
		}

		gater = auth.NewAuthorizedPeerGater(authorizedPeers)
	} else {
		fmt.Println("⚠️  WARNING: Connection gating is DISABLED - any peer can use this relay!")
	}
	fmt.Println()

	// Build host options
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(cfg.Network.ListenAddresses...),
	}

	// Add connection gater if enabled
	if gater != nil {
		hostOpts = append(hostOpts, libp2p.ConnectionGater(gater))
	}

	// Create a basic host first — no relay options
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		log.Fatalf("Failed to create host: %v", err)
	}
	defer h.Close()

	// Start the relay service with configured resource limits
	relayResources, relayLimit := buildRelayResources(&cfg.Resources)
	_, err = relayv2.New(h, relayv2.WithResources(relayResources), relayv2.WithLimit(relayLimit))
	if err != nil {
		log.Fatalf("Failed to start relay service: %v", err)
	}
	fmt.Printf("Relay limits: max_reservations=%d, max_circuits=%d, session=%s, data=%s/direction\n",
		cfg.Resources.MaxReservations, cfg.Resources.MaxCircuits,
		cfg.Resources.SessionDuration, cfg.Resources.SessionDataLimit)

	fmt.Printf("🔄 Relay Peer ID: %s\n", h.ID())
	fmt.Println()

	// Verify the relay protocol is registered
	fmt.Println("Registered protocols:")
	for _, p := range h.Mux().Protocols() {
		fmt.Printf("  %s\n", p)
	}

	fmt.Println()
	fmt.Println("Multiaddrs:")
	for _, addr := range h.Addrs() {
		fmt.Printf("  %s/p2p/%s\n", addr, h.ID())
	}

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				peers := h.Network().Peers()
				fmt.Printf("\n--- %d connected peers ---\n", len(peers))
				for _, p := range peers {
					fmt.Printf("  %s\n", p.String()[:16])
				}
			}
		}
	}()

	// Start watchdog health checks and notify systemd
	watchdog.Ready()
	go watchdog.Run(ctx, watchdog.Config{Interval: 30 * time.Second}, []watchdog.HealthCheck{
		{
			Name: "host-listening",
			Check: func() error {
				if len(h.Addrs()) == 0 {
					return fmt.Errorf("no listen addresses")
				}
				return nil
			},
		},
		{
			Name: "protocols-registered",
			Check: func() error {
				if len(h.Mux().Protocols()) == 0 {
					return fmt.Errorf("no protocols registered")
				}
				return nil
			},
		},
	})

	fmt.Println()
	fmt.Println("✅ Private relay running.")
	fmt.Println("Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	watchdog.Stopping()
	fmt.Println("\nShutting down...")
	cancel() // Stop background goroutines
}
