package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	rcmgr "github.com/libp2p/go-libp2p/p2p/host/resource-manager"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	libp2pquic "github.com/libp2p/go-libp2p/p2p/transport/quic"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ws "github.com/libp2p/go-libp2p/p2p/transport/websocket"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/identity"
	"github.com/satindergrewal/peer-up/internal/watchdog"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

const relayConfigFile = "relay-server.yaml"

// runRelayServe starts the circuit relay server. This is the equivalent of the
// former standalone relay-server binary's main() function.
func runRelayServe(args []string) {
	// Handle --config flag
	configFile := relayConfigFile
	for i, arg := range args {
		if (arg == "--config" || arg == "-config") && i+1 < len(args) {
			configFile = args[i+1]
			break
		}
		if strings.HasPrefix(arg, "--config=") {
			configFile = strings.TrimPrefix(arg, "--config=")
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Printf("=== Private libp2p Relay Server (%s) ===\n", version)
	fmt.Println()

	// Load configuration
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		fatal("Failed to load config: %v\n", err)
	}

	// Validate configuration
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		fatal("Invalid configuration: %v", err)
	}

	// Archive last-known-good config on successful validation
	if err := config.Archive(configFile); err != nil {
		log.Printf("Warning: failed to archive config: %v", err)
	}

	fmt.Printf("Loaded configuration from %s\n", configFile)
	fmt.Printf("Authentication: %v\n", cfg.Security.EnableConnectionGating)
	fmt.Println()

	priv, err := identity.LoadOrCreateIdentity(cfg.Identity.KeyFile)
	if err != nil {
		fatal("Identity error: %v", err)
	}

	// Load authorized keys if connection gating is enabled
	var gater *auth.AuthorizedPeerGater
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			fatal("Connection gating enabled but no authorized_keys_file specified")
		}

		authorizedPeers, err := auth.LoadAuthorizedKeys(cfg.Security.AuthorizedKeysFile)
		if err != nil {
			fatal("Failed to load authorized keys: %v", err)
		}

		if len(authorizedPeers) == 0 {
			fmt.Printf("WARNING: authorized_keys file is empty - no peers can make reservations!\n")
			fmt.Printf("   Add authorized peer IDs to %s\n", cfg.Security.AuthorizedKeysFile)
		} else {
			fmt.Printf("Loaded %d authorized peer(s) from %s\n", len(authorizedPeers), cfg.Security.AuthorizedKeysFile)
		}

		gater = auth.NewAuthorizedPeerGater(authorizedPeers)
	} else {
		fmt.Println("WARNING: Connection gating is DISABLED - any peer can use this relay!")
	}
	fmt.Println()

	// Build host options.
	// Transport order: QUIC first (3 RTTs, native multiplexing), TCP second (universal fallback),
	// WebSocket last (anti-censorship/DPI evasion). AutoNAT v2 for per-address reachability testing.
	hostOpts := []libp2p.Option{
		libp2p.Identity(priv),
		libp2p.ListenAddrStrings(cfg.Network.ListenAddresses...),
		libp2p.Transport(libp2pquic.NewTransport),
		libp2p.Transport(tcp.NewTCPTransport),
		libp2p.Transport(ws.New),
		libp2p.EnableAutoNATv2(),
		libp2p.UserAgent(fmt.Sprintf("relay-server/%s", version)),
	}

	// Resource manager (always enabled on relay - public-facing service)
	{
		limits := rcmgr.DefaultLimits
		libp2p.SetDefaultServiceLimits(&limits)
		scaled := limits.AutoScale()
		rm, err := rcmgr.NewResourceManager(rcmgr.NewFixedLimiter(scaled))
		if err != nil {
			fatal("Failed to create resource manager: %v", err)
		}
		hostOpts = append(hostOpts, libp2p.ResourceManager(rm))
		slog.Info("resource manager enabled", "limits", "auto-scaled")
	}

	// Add connection gater if enabled
	if gater != nil {
		hostOpts = append(hostOpts, libp2p.ConnectionGater(gater))
	}

	// Create host - relay service is added separately below
	h, err := libp2p.New(hostOpts...)
	if err != nil {
		fatal("Failed to create host: %v", err)
	}
	defer h.Close()

	// Start the relay service with configured resource limits
	relayResources, relayLimit := buildRelayResources(&cfg.Resources)
	_, err = relayv2.New(h, relayv2.WithResources(relayResources), relayv2.WithLimit(relayLimit))
	if err != nil {
		fatal("Failed to start relay service: %v", err)
	}
	fmt.Printf("Relay limits: max_reservations=%d, max_circuits=%d, session=%s, data=%s/direction\n",
		cfg.Resources.MaxReservations, cfg.Resources.MaxCircuits,
		cfg.Resources.SessionDuration, cfg.Resources.SessionDataLimit)

	// Bootstrap into the private peerup DHT as a server.
	// The relay is the primary bootstrap peer - all peerup nodes connect here first
	// and use this DHT for peer discovery.
	dhtPrefix := p2pnet.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	kdht, err := dht.New(ctx, h,
		dht.Mode(dht.ModeServer),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
	)
	if err != nil {
		fatal("DHT error: %v", err)
	}
	if err := kdht.Bootstrap(ctx); err != nil {
		fatal("DHT bootstrap error: %v", err)
	}
	defer kdht.Close()
	if cfg.Discovery.Network != "" {
		fmt.Printf("Private DHT active: network %q (protocol: %s/kad/1.0.0)\n", cfg.Discovery.Network, dhtPrefix)
	} else {
		fmt.Printf("Private DHT active (protocol: %s/kad/1.0.0)\n", dhtPrefix)
	}

	fmt.Printf("Relay Peer ID: %s\n", h.ID())
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

	// Initialize relay observability (opt-in)
	var relayMetrics *p2pnet.Metrics
	if cfg.Telemetry.Metrics.Enabled {
		relayMetrics = p2pnet.NewMetrics(version, runtime.Version())
		slog.Info("telemetry: metrics enabled", "addr", cfg.Telemetry.Metrics.ListenAddress)
	}
	var relayAudit *p2pnet.AuditLogger
	if cfg.Telemetry.Audit.Enabled {
		relayAudit = p2pnet.NewAuditLogger(slog.NewJSONHandler(os.Stderr, nil))
		slog.Info("telemetry: audit logging enabled")
	}

	// Wire auth decision callback on relay gater
	if gater != nil && (relayMetrics != nil || relayAudit != nil) {
		gater.SetDecisionCallback(func(peerID, result string) {
			if relayMetrics != nil {
				relayMetrics.AuthDecisionsTotal.WithLabelValues(result).Inc()
			}
			if relayAudit != nil {
				relayAudit.AuthDecision(peerID, "inbound", result)
			}
		})
	}

	// Start /healthz HTTP endpoint if enabled.
	// Security: only exposes operational status (no peer IDs, versions, or protocol lists).
	// Default listen address is 127.0.0.1:9090 (localhost-only), but if configured to
	// bind externally, we validate that the source IP is loopback to prevent information leakage.
	var healthServer *http.Server
	if cfg.Health.Enabled || relayMetrics != nil {
		startTime := time.Now()
		mux := http.NewServeMux()

		if cfg.Health.Enabled {
			mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
				// Reject non-loopback sources when bound to a non-loopback address
				host, _, _ := net.SplitHostPort(r.RemoteAddr)
				if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
					http.Error(w, "forbidden", http.StatusForbidden)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]any{
					"status":          "ok",
					"uptime_seconds":  int(time.Since(startTime).Seconds()),
					"connected_peers": len(h.Network().Peers()),
				})
			})
		}

		if relayMetrics != nil {
			mux.Handle("/metrics", relayMetrics.Handler())
		}

		// Use metrics listen address when health is not enabled
		listenAddr := cfg.Health.ListenAddress
		if !cfg.Health.Enabled && relayMetrics != nil {
			listenAddr = cfg.Telemetry.Metrics.ListenAddress
		}

		healthServer = &http.Server{
			Addr:         listenAddr,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 5 * time.Second,
		}
		go func() {
			slog.Info("HTTP endpoint started", "addr", listenAddr)
			if err := healthServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				slog.Error("HTTP endpoint error", "err", err)
			}
		}()
	}

	fmt.Println()
	fmt.Println("Private relay running.")
	fmt.Println("Press Ctrl+C to stop.")

	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)
	<-ch
	watchdog.Stopping()
	fmt.Println("\nShutting down...")
	if healthServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		healthServer.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	cancel() // Stop background goroutines
}

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
		ReservationTTL:         reservationTTL,
		MaxReservations:        rc.MaxReservations,
		MaxCircuits:            rc.MaxCircuits,
		BufferSize:             rc.BufferSize,
		MaxReservationsPerPeer: 1,
		MaxReservationsPerIP:   rc.MaxReservationsPerIP,
		MaxReservationsPerASN:  rc.MaxReservationsPerASN,
	}

	limit := &relayv2.RelayLimit{
		Duration: sessionDuration,
		Data:     sessionDataLimit,
	}

	return resources, limit
}

// loadRelayAuthKeysPathErr loads relay config and returns the authorized_keys file path.
func loadRelayAuthKeysPathErr(configFile string) (string, error) {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return "", fmt.Errorf("failed to load config: %w", err)
	}
	if cfg.Security.AuthorizedKeysFile == "" {
		return "", fmt.Errorf("no authorized_keys_file configured")
	}
	return cfg.Security.AuthorizedKeysFile, nil
}

func loadRelayAuthKeysPath(configFile string) string {
	path, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		fatal("%v", err)
	}
	return path
}

func doRelayAuthorize(args []string, configFile string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: peerup relay authorize <peer-id> [comment]")
	}

	peerID := args[0]
	comment := ""
	if len(args) > 1 {
		comment = strings.Join(args[1:], " ")
	}

	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	if err := auth.AddPeer(authKeysPath, peerID, comment); err != nil {
		return fmt.Errorf("failed to authorize peer: %w", err)
	}

	fmt.Fprintf(stdout, "Authorized: %s\n", peerID[:min(16, len(peerID))]+"...")
	if comment != "" {
		fmt.Fprintf(stdout, "Comment:    %s\n", comment)
	}
	fmt.Fprintf(stdout, "File:       %s\n", authKeysPath)
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart relay to apply: sudo systemctl restart peerup-relay")
	return nil
}

func runRelayAuthorize(args []string, configFile string) {
	if err := doRelayAuthorize(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayDeauthorize(args []string, configFile string, stdout io.Writer) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: peerup relay deauthorize <peer-id>")
	}

	peerID := args[0]
	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	if err := auth.RemovePeer(authKeysPath, peerID); err != nil {
		return fmt.Errorf("failed to deauthorize peer: %w", err)
	}

	fmt.Fprintf(stdout, "Deauthorized: %s\n", peerID[:min(16, len(peerID))]+"...")
	fmt.Fprintln(stdout)
	fmt.Fprintln(stdout, "Restart relay to apply: sudo systemctl restart peerup-relay")
	return nil
}

func runRelayDeauthorize(args []string, configFile string) {
	if err := doRelayDeauthorize(args, configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func doRelayListPeers(configFile string, stdout io.Writer) error {
	authKeysPath, err := loadRelayAuthKeysPathErr(configFile)
	if err != nil {
		return err
	}
	peers, err := auth.ListPeers(authKeysPath)
	if err != nil {
		return fmt.Errorf("failed to list peers: %w", err)
	}

	fmt.Fprintf(stdout, "Authorized peers (%s):\n\n", authKeysPath)
	if len(peers) == 0 {
		fmt.Fprintln(stdout, "  (none)")
	} else {
		for _, p := range peers {
			if p.Comment != "" {
				fmt.Fprintf(stdout, "  %s  # %s\n", p.PeerID, p.Comment)
			} else {
				fmt.Fprintf(stdout, "  %s\n", p.PeerID)
			}
		}
	}
	fmt.Fprintf(stdout, "\nTotal: %d peer(s)\n", len(peers))
	return nil
}

func runRelayListPeers(configFile string) {
	if err := doRelayListPeers(configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
	}
}

func runRelayInfo(configFile string) {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		fatal("Failed to load config: %v", err)
	}

	// Read identity key (don't auto-create  - info is read-only)
	data, err := os.ReadFile(cfg.Identity.KeyFile)
	if err != nil {
		fatal("Cannot read identity key %s: %v\n  Run the relay server once to generate a key.", cfg.Identity.KeyFile, err)
	}
	priv, err := crypto.UnmarshalPrivateKey(data)
	if err != nil {
		fatal("Invalid identity key: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		fatal("Failed to derive peer ID: %v", err)
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

func runRelayServerVersion() {
	fmt.Printf("peerup relay %s (%s) built %s\n", version, commit, buildDate)
	fmt.Printf("Go %s %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
}

func runRelayServerConfig(args []string, configFile string) {
	if len(args) < 1 {
		fmt.Println("Usage: peerup relay config <command>")
		fmt.Println()
		fmt.Println("Commands:")
		fmt.Println("  validate    Validate relay-server.yaml without starting")
		fmt.Println("  rollback    Restore last-known-good config")
		osExit(1)
	}
	switch args[0] {
	case "validate":
		runRelayServerConfigValidate(configFile)
	case "rollback":
		runRelayServerConfigRollback(configFile)
	default:
		fmt.Fprintf(os.Stderr, "Unknown config command: %s\n", args[0])
		osExit(1)
	}
}

func doRelayServerConfigValidate(configFile string, stdout io.Writer) error {
	cfg, err := config.LoadRelayServerConfig(configFile)
	if err != nil {
		return fmt.Errorf("FAIL: %v", err)
	}
	if err := config.ValidateRelayServerConfig(cfg); err != nil {
		return fmt.Errorf("FAIL: %v", err)
	}
	fmt.Fprintf(stdout, "OK: %s is valid\n", configFile)
	return nil
}

func runRelayServerConfigValidate(configFile string) {
	if err := doRelayServerConfigValidate(configFile, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		osExit(1)
	}
}

func doRelayServerConfigRollback(configFile string, stdout io.Writer) error {
	if !config.HasArchive(configFile) {
		return fmt.Errorf("no last-known-good archive for %s\nArchives are created automatically on each successful relay startup", configFile)
	}
	if err := config.Rollback(configFile); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}
	fmt.Fprintf(stdout, "Restored %s from last-known-good archive\n", configFile)
	fmt.Fprintln(stdout, "You can now restart the relay.")
	return nil
}

func runRelayServerConfigRollback(configFile string) {
	if err := doRelayServerConfigRollback(configFile, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		osExit(1)
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

func printRelayServeUsage() {
	fmt.Println("Usage: peerup relay <command> [options]")
	fmt.Println()
	fmt.Println("Client configuration:")
	fmt.Println("  add    <address> [--peer-id <ID>]   Add a relay server address")
	fmt.Println("  list                                List configured relay addresses")
	fmt.Println("  remove <multiaddr>                  Remove a relay server address")
	fmt.Println()
	fmt.Println("Relay server management:")
	fmt.Println("  serve                               Start the relay server")
	fmt.Println("  info                                Show peer ID, multiaddrs, QR code")
	fmt.Println("  authorize <peer-id> [comment]       Allow a peer to use this relay")
	fmt.Println("  deauthorize <peer-id>               Remove a peer's access")
	fmt.Println("  list-peers                          List authorized peers")
	fmt.Println("  config validate                     Validate relay config without starting")
	fmt.Println("  config rollback                     Restore last-known-good config")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  peerup relay add /ip4/203.0.113.50/tcp/7777/p2p/12D3KooW...")
	fmt.Println("  peerup relay add 203.0.113.50:7777 --peer-id 12D3KooW...")
	fmt.Println("  peerup relay serve --config /etc/peerup/relay-server.yaml")
	fmt.Println("  peerup relay authorize 12D3KooW... home-node")
	fmt.Println("  peerup relay info")
	fmt.Println()
	fmt.Println("Server commands use relay-server.yaml in the working directory by default.")
	fmt.Println("All commands support --config <path>.")
}
