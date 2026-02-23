package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	drouting "github.com/libp2p/go-libp2p/p2p/discovery/routing"
	circuitv2client "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/client"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/satindergrewal/peer-up/internal/auth"
	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/internal/relay"
	"github.com/satindergrewal/peer-up/internal/reputation"
	"github.com/satindergrewal/peer-up/internal/watchdog"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// serveRuntime holds the shared P2P lifecycle state for the daemon command.
type serveRuntime struct {
	network    *p2pnet.Network
	config     *config.HomeNodeConfig
	configFile string
	gater      *auth.AuthorizedPeerGater // nil if connection gating disabled
	authKeys   string                    // path to authorized_keys file
	ctx        context.Context
	cancel     context.CancelFunc
	version    string
	startTime  time.Time
	kdht       *dht.IpfsDHT // stored for peer discovery from daemon API

	// Interface discovery (populated at startup)
	ifSummary *p2pnet.InterfaceSummary

	// Path dialer for parallel connection racing
	pathDialer *p2pnet.PathDialer

	// Path tracker for per-peer connection visibility
	pathTracker *p2pnet.PathTracker

	// STUN prober for NAT type detection and external address discovery
	stunProber *p2pnet.STUNProber

	// Peer relay (auto-enabled when public IP detected)
	peerRelay *p2pnet.PeerRelay

	// Observability (nil when telemetry disabled)
	metrics       *p2pnet.Metrics
	audit         *p2pnet.AuditLogger
	metricsServer *http.Server

	// Sovereign per-peer interaction history
	peerHistory *reputation.PeerHistory
}

// newServeRuntime creates a new serve runtime: loads config, creates P2P network,
// handles commit-confirmed. The caller owns the context and cancel function.
func newServeRuntime(ctx context.Context, cancel context.CancelFunc, configFlag, ver string) (*serveRuntime, error) {
	rt := &serveRuntime{
		ctx:       ctx,
		cancel:    cancel,
		version:   ver,
		startTime: time.Now(),
	}

	// Find and load configuration
	cfgFile, err := config.FindConfigFile(configFlag)
	if err != nil {
		return nil, fmt.Errorf("config error: %w", err)
	}
	cfg, err := config.LoadNodeConfig(cfgFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load config: %w", err)
	}
	config.ResolveConfigPaths(cfg, filepath.Dir(cfgFile))

	if err := config.ValidateNodeConfig(cfg); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	// Archive last-known-good config on successful validation
	if err := config.Archive(cfgFile); err != nil {
		log.Printf("Warning: failed to archive config: %v", err)
	}

	rt.config = cfg
	rt.configFile = cfgFile

	// Check for pending commit-confirmed
	if deadline, err := config.CheckPending(cfgFile); err == nil && !deadline.IsZero() {
		go config.EnforceCommitConfirmed(ctx, cfgFile, deadline, os.Exit)
		remaining := time.Until(deadline).Round(time.Second)
		fmt.Printf("Commit-confirmed active: %s remaining (run 'peerup config confirm' to keep this config)\n", remaining)
	}

	fmt.Printf("Loaded configuration from %s\n", cfgFile)
	fmt.Printf("Rendezvous: %s\n", cfg.Discovery.Rendezvous)
	fmt.Println()

	// Set up connection gater
	var authorizedKeysFile string
	if cfg.Security.EnableConnectionGating {
		if cfg.Security.AuthorizedKeysFile == "" {
			return nil, fmt.Errorf("connection gating enabled but no authorized_keys_file specified")
		}
		authorizedKeysFile = cfg.Security.AuthorizedKeysFile
		rt.authKeys = authorizedKeysFile
		fmt.Printf("Loading authorized peers from %s\n", authorizedKeysFile)

		// Create gater externally so we can retain reference for hot-reload
		authorizedPeers, err := auth.LoadAuthorizedKeys(authorizedKeysFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load authorized_keys: %w", err)
		}
		rt.gater = auth.NewAuthorizedPeerGater(authorizedPeers)
	} else {
		fmt.Println("WARNING: Connection gating is DISABLED - any peer can connect!")
	}
	fmt.Println()

	// Initialize observability (opt-in)
	if cfg.Telemetry.Metrics.Enabled {
		rt.metrics = p2pnet.NewMetrics(ver, runtime.Version())
		fmt.Printf("Telemetry: metrics enabled on %s\n", cfg.Telemetry.Metrics.ListenAddress)
	}
	if cfg.Telemetry.Audit.Enabled {
		rt.audit = p2pnet.NewAuditLogger(slog.NewJSONHandler(os.Stderr, nil))
		fmt.Println("Telemetry: audit logging enabled")
	}

	// Wire auth decision callback (metrics + audit)
	if rt.gater != nil && (rt.metrics != nil || rt.audit != nil) {
		rt.gater.SetDecisionCallback(func(peerID, result string) {
			if rt.metrics != nil {
				rt.metrics.AuthDecisionsTotal.WithLabelValues(result).Inc()
			}
			if rt.audit != nil {
				rt.audit.AuthDecision(peerID, "inbound", result)
			}
		})
	}

	// Create P2P network
	netCfg := &p2pnet.Config{
		KeyFile:            cfg.Identity.KeyFile,
		Gater:              rt.gater,
		Config:             &config.Config{Network: cfg.Network},
		UserAgent:          "peerup/" + ver,
		Metrics:            rt.metrics,
		EnableRelay:           true,
		RelayAddrs:            cfg.Relay.Addresses,
		ForcePrivate:          cfg.Network.ForcePrivateReachability,
		EnableNATPortMap:      true,
		EnableHolePunching:    true,
		ResourceLimitsEnabled: cfg.Network.ResourceLimitsEnabled,
	}

	net, err := p2pnet.New(netCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create P2P network: %w", err)
	}
	rt.network = net

	// Load name mappings from config
	if cfg.Names != nil {
		if err := net.LoadNames(cfg.Names); err != nil {
			log.Printf("Failed to load names: %v", err)
		}
	}

	fmt.Printf("Peer ID: %s\n", net.Host().ID())
	fmt.Println()

	// Initialize sovereign peer interaction history.
	historyPath := filepath.Join(filepath.Dir(cfgFile), "peer_history.json")
	rt.peerHistory = reputation.NewPeerHistory(historyPath)

	return rt, nil
}

// Bootstrap connects to relay servers, bootstraps the DHT, and starts
// background advertising. This is the "bring the network up" step.
func (rt *serveRuntime) Bootstrap() error {
	h := rt.network.Host()
	cfg := rt.config

	// Discover network interfaces and log IPv6/IPv4 availability
	ifSummary, err := p2pnet.DiscoverInterfaces()
	if err != nil {
		fmt.Printf("Warning: interface discovery failed: %v\n", err)
	} else {
		rt.ifSummary = ifSummary
		fmt.Printf("Network interfaces: %d with global addresses\n", len(ifSummary.Interfaces))
		if ifSummary.HasGlobalIPv6 {
			fmt.Printf("  Global IPv6: %d addresses (direct connections possible)\n", len(ifSummary.GlobalIPv6Addrs))
		}
		if ifSummary.HasGlobalIPv4 {
			fmt.Printf("  Global IPv4: %d addresses\n", len(ifSummary.GlobalIPv4Addrs))
		}
		if !ifSummary.HasGlobalIPv6 && !ifSummary.HasGlobalIPv4 {
			fmt.Println("  No global addresses detected - relay will be required")
		}
		fmt.Println()

		// Record metrics
		if rt.metrics != nil {
			ipv4Count := 0
			ipv6Count := 0
			for _, iface := range ifSummary.Interfaces {
				ipv4Count += len(iface.IPv4Addrs)
				ipv6Count += len(iface.IPv6Addrs)
			}
			rt.metrics.InterfaceCount.WithLabelValues("ipv4").Set(float64(ipv4Count))
			rt.metrics.InterfaceCount.WithLabelValues("ipv6").Set(float64(ipv6Count))
		}
	}

	// Parse relay addresses for manual connection
	relayInfos, err := p2pnet.ParseRelayAddrs(cfg.Relay.Addresses)
	if err != nil {
		return fmt.Errorf("failed to parse relay addresses: %w", err)
	}

	// Connect to the relay
	for _, ai := range relayInfos {
		if err := h.Connect(rt.ctx, ai); err != nil {
			fmt.Printf("Could not connect to relay %s: %v\n", ai.ID.String()[:16], err)
		} else {
			fmt.Printf("Connected to relay %s\n", ai.ID.String()[:16])
		}
	}

	// Give AutoRelay a moment to make reservations
	fmt.Println("Waiting for AutoRelay to establish reservations...")
	time.Sleep(5 * time.Second)

	// Check if we got relay addresses
	hasRelay := false
	for _, addr := range h.Addrs() {
		if strings.Contains(addr.String(), "p2p-circuit") {
			fmt.Printf("Relay address: %s\n", addr)
			hasRelay = true
		}
	}
	if !hasRelay {
		fmt.Println("No relay addresses yet - trying manual reservation...")
		for _, ai := range relayInfos {
			_, err := circuitv2client.Reserve(rt.ctx, h, ai)
			if err != nil {
				fmt.Printf("Manual reservation failed: %v\n", err)
			} else {
				fmt.Printf("Manual relay reservation active on %s\n", ai.ID.String()[:16])
			}
		}
	}

	// Keep reservation alive
	go func() {
		ticker := time.NewTicker(cfg.Relay.ReservationInterval)
		defer ticker.Stop()
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
				for _, ai := range relayInfos {
					h.Connect(rt.ctx, ai)
					circuitv2client.Reserve(rt.ctx, h, ai)
				}
			}
		}
	}()

	// Bootstrap the DHT
	dhtPrefix := p2pnet.DHTProtocolPrefixForNamespace(cfg.Discovery.Network)
	if cfg.Discovery.Network != "" {
		fmt.Printf("DHT network: %s (protocol: %s/kad/1.0.0)\n", cfg.Discovery.Network, dhtPrefix)
	} else {
		fmt.Println("DHT network: global (protocol: /peerup/kad/1.0.0)")
	}
	fmt.Println("Bootstrapping into the DHT...")
	kdht, err := dht.New(rt.ctx, h,
		dht.Mode(dht.ModeAutoServer),
		dht.ProtocolPrefix(protocol.ID(dhtPrefix)),
	)
	if err != nil {
		return fmt.Errorf("DHT error: %w", err)
	}
	if err := kdht.Bootstrap(rt.ctx); err != nil {
		return fmt.Errorf("DHT bootstrap error: %w", err)
	}
	rt.kdht = kdht

	// Connect to bootstrap peers
	var bootstrapPeers []ma.Multiaddr
	if len(cfg.Discovery.BootstrapPeers) > 0 {
		for _, addr := range cfg.Discovery.BootstrapPeers {
			maddr, err := ma.NewMultiaddr(addr)
			if err != nil {
				fmt.Printf("Invalid bootstrap peer %s: %v\n", addr, err)
				continue
			}
			bootstrapPeers = append(bootstrapPeers, maddr)
		}
	} else {
		// Use relay addresses as DHT bootstrap peers.
		// The relay server runs the peerup DHT  - IPFS Amino peers don't speak /peerup/kad/1.0.0.
		for _, addr := range cfg.Relay.Addresses {
			maddr, err := ma.NewMultiaddr(addr)
			if err != nil {
				fmt.Printf("Invalid relay bootstrap addr %s: %v\n", addr, err)
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
			if err := h.Connect(rt.ctx, pi); err == nil {
				connected.Add(1)
			}
		}(*pi)
	}
	wg.Wait()
	fmt.Printf("Connected to %d bootstrap peers\n", connected.Load())

	// Advertise ourselves on the DHT using a rendezvous string
	routingDiscovery := drouting.NewRoutingDiscovery(kdht)
	fmt.Printf("Advertising on rendezvous: %s\n", cfg.Discovery.Rendezvous)

	// Keep advertising in the background
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			routingDiscovery.Advertise(rt.ctx, cfg.Discovery.Rendezvous)
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()

	// Initialize path dialer for parallel connection racing
	rt.pathDialer = p2pnet.NewPathDialer(h, kdht, cfg.Relay.Addresses, rt.metrics)

	// Initialize path tracker for per-peer connection visibility
	rt.pathTracker = p2pnet.NewPathTracker(h, rt.metrics)
	go rt.pathTracker.Start(rt.ctx)

	// Start network change monitor (event-driven on macOS/Linux, polling fallback)
	netmon := p2pnet.NewNetworkMonitor(func(change *p2pnet.NetworkChange) {
		// Update interface summary
		newSummary, err := p2pnet.DiscoverInterfaces()
		if err != nil {
			fmt.Printf("Warning: interface re-discovery failed: %v\n", err)
			return
		}
		rt.ifSummary = newSummary

		// Update metrics
		if rt.metrics != nil {
			ipv4Count := 0
			ipv6Count := 0
			for _, iface := range newSummary.Interfaces {
				ipv4Count += len(iface.IPv4Addrs)
				ipv6Count += len(iface.IPv6Addrs)
			}
			rt.metrics.InterfaceCount.WithLabelValues("ipv4").Set(float64(ipv4Count))
			rt.metrics.InterfaceCount.WithLabelValues("ipv6").Set(float64(ipv6Count))
		}

		// Re-evaluate peer relay eligibility on network change
		if rt.peerRelay != nil {
			rt.peerRelay.AutoDetect(newSummary)
		}

		fmt.Printf("Network change: +%d -%d IPs (ipv6=%v ipv4=%v)\n",
			len(change.Added), len(change.Removed), change.IPv6Changed, change.IPv4Changed)

		// Re-probe STUN on network change (external address may have changed)
		if rt.stunProber != nil {
			go func() {
				probeCtx, probeCancel := context.WithTimeout(rt.ctx, 10*time.Second)
				defer probeCancel()
				if _, err := rt.stunProber.Probe(probeCtx); err != nil {
					fmt.Printf("Warning: STUN re-probe failed: %v\n", err)
				}
			}()
		}
	}, rt.metrics)
	go netmon.Run(rt.ctx)

	// STUN probe for NAT type detection and external address discovery.
	// Run in background so it doesn't block startup.
	rt.stunProber = p2pnet.NewSTUNProber(nil, rt.metrics) // default STUN servers
	go func() {
		probeCtx, probeCancel := context.WithTimeout(rt.ctx, 10*time.Second)
		defer probeCancel()
		result, err := rt.stunProber.Probe(probeCtx)
		if err != nil {
			fmt.Printf("Warning: STUN probe failed: %v\n", err)
			return
		}
		fmt.Printf("NAT type: %s", result.NATType)
		if len(result.ExternalAddrs) > 0 {
			fmt.Printf(" (external: %s)", result.ExternalAddrs[0])
		}
		if result.NATType.HolePunchable() {
			fmt.Print(" [hole-punchable]")
		}
		fmt.Println()
	}()

	// Initialize peer relay (auto-enables if this host has a public IP).
	// The existing ConnectionGater restricts who can use this relay.
	rt.peerRelay = p2pnet.NewPeerRelay(h, rt.metrics)
	if rt.ifSummary != nil {
		rt.peerRelay.AutoDetect(rt.ifSummary)
	}

	return nil
}

// ExposeConfiguredServices registers all enabled services from config on the P2P host.
func (rt *serveRuntime) ExposeConfiguredServices() {
	if rt.config.Services == nil {
		return
	}
	for name, svc := range rt.config.Services {
		if svc.Enabled {
			fmt.Printf("Exposing service: %s -> %s\n", name, svc.LocalAddress)

			// Convert AllowedPeers string slice to peer.ID set
			var allowedPeers map[peer.ID]struct{}
			if len(svc.AllowedPeers) > 0 {
				allowedPeers = make(map[peer.ID]struct{}, len(svc.AllowedPeers))
				for _, pidStr := range svc.AllowedPeers {
					pid, err := peer.Decode(pidStr)
					if err != nil {
						log.Printf("Invalid peer ID %q in allowed_peers for %s: %v", pidStr, name, err)
						continue
					}
					allowedPeers[pid] = struct{}{}
				}
				fmt.Printf("  ACL: %d allowed peers\n", len(allowedPeers))
			}

			if err := rt.network.ExposeService(name, svc.LocalAddress, allowedPeers); err != nil {
				log.Printf("Failed to expose service %s: %v", name, err)
			}
		}
	}
	fmt.Println()
}

// SetupPingPong registers the ping-pong stream handler if enabled in config.
func (rt *serveRuntime) SetupPingPong() {
	if !rt.config.Protocols.PingPong.Enabled {
		fmt.Println("Ping-pong protocol disabled in config")
		return
	}

	h := rt.network.Host()
	h.SetStreamHandler(protocol.ID(rt.config.Protocols.PingPong.ID), func(s network.Stream) {
		remotePeer := s.Conn().RemotePeer()

		connType := "DIRECT"
		if s.Conn().Stat().Limited {
			connType = "RELAYED"
		}
		fmt.Printf("\nIncoming stream from %s [%s]\n", remotePeer.String()[:16], connType)

		reader := bufio.NewReader(s)
		msg, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("   Read error: %v\n", err)
			s.Close()
			return
		}
		msg = strings.TrimSpace(msg)
		fmt.Printf("   Received: %s\n", msg)

		if msg == "ping" {
			fmt.Println("   PONG!")
			s.Write([]byte("pong\n"))
		} else {
			fmt.Printf("   Unknown message: %s\n", msg)
			s.Write([]byte("unknown\n"))
		}
		s.Close()
	})
}

// SetupPeerNotify registers the peer-notify stream handler so the daemon
// can receive peer introductions from the relay. This is the "mailbox
// receiver" - when the relay delivers an introduction, the daemon
// processes it and updates authorized_keys.
func (rt *serveRuntime) SetupPeerNotify() {
	if rt.authKeys == "" {
		return // no authorized_keys = no gating = nothing to update
	}

	h := rt.network.Host()
	h.SetStreamHandler(protocol.ID(relay.PeerNotifyProtocol), func(s network.Stream) {
		defer s.Close()
		remotePeer := s.Conn().RemotePeer()

		// SECURITY Layer 2: Only accept from configured relay peer IDs.
		if !rt.isConfiguredRelay(remotePeer) {
			slog.Warn("peer-notify: rejected from non-relay",
				"peer", remotePeer.String()[:16]+"...")
			return
		}

		// Parse notification.
		peers, groupID, groupSize, err := relay.ReadPeerNotify(s)
		if err != nil {
			slog.Warn("peer-notify: parse error", "err", err)
			return
		}

		// SECURITY Layer 3: Group ID validation - only accept for groups we participated in.
		if !rt.hasGroupMembership(groupID) {
			slog.Warn("peer-notify: unknown group", "group", groupID)
			return
		}

		// SECURITY Layer 1: Group size enforcement.
		existing := rt.countGroupPeers(groupID)
		if existing+len(peers) >= groupSize {
			slog.Warn("peer-notify: group size exceeded",
				"group", groupID,
				"existing", existing,
				"incoming", len(peers),
				"max", groupSize)
			return
		}

		// Add each introduced peer to authorized_keys.
		added := 0
		for _, p := range peers {
			comment := p.Name
			if comment == "" {
				comment = "introduced-" + time.Now().Format("2006-01-02")
			}
			if err := auth.AddPeer(rt.authKeys, p.PeerID, comment); err != nil {
				if !strings.Contains(err.Error(), "already authorized") {
					slog.Error("peer-notify: add failed",
						"peer", p.PeerID[:16]+"...", "err", err)
				}
				continue
			}
			auth.SetPeerAttr(rt.authKeys, p.PeerID, "group", groupID)

			// Store HMAC commitment proof if present (Layer 4 data).
			if len(p.HMACProof) == relay.HMACProofSize {
				auth.SetPeerAttr(rt.authKeys, p.PeerID, "hmac_proof", hex.EncodeToString(p.HMACProof))
			}
			added++

			// Record introduction in sovereign history.
			if rt.peerHistory != nil {
				rt.peerHistory.RecordIntroduction(p.PeerID, remotePeer.String(), "relay-pairing")
			}

			// SECURITY Layer 6: Audit logging.
			slog.Info("peer-notify: peer introduced",
				"name", p.Name,
				"peer", p.PeerID[:16]+"...",
				"group", groupID,
				"relay", remotePeer.String()[:16]+"...")
		}

		// Hot-reload gater so new peers are immediately allowed.
		if added > 0 && rt.gater != nil {
			newPeers, err := auth.LoadAuthorizedKeys(rt.authKeys)
			if err != nil {
				slog.Error("peer-notify: gater reload failed", "err", err)
			} else {
				rt.gater.UpdateAuthorizedPeers(newPeers)
				slog.Info("peer-notify: gater reloaded", "peers", len(newPeers))
			}
		}
	})
}

// isConfiguredRelay checks if a peer ID matches one of the configured relay addresses.
func (rt *serveRuntime) isConfiguredRelay(p peer.ID) bool {
	for _, addr := range rt.config.Relay.Addresses {
		maddr, err := ma.NewMultiaddr(addr)
		if err != nil {
			continue
		}
		ai, err := peer.AddrInfoFromP2pAddr(maddr)
		if err != nil {
			continue
		}
		if ai.ID == p {
			return true
		}
	}
	return false
}

// hasGroupMembership checks if this peer participated in the given pairing group.
// Returns true if any peer in authorized_keys has a matching group attribute.
func (rt *serveRuntime) hasGroupMembership(groupID string) bool {
	entries, err := auth.ListPeers(rt.authKeys)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Group == groupID {
			return true
		}
	}
	return false
}

// countGroupPeers counts how many peers in authorized_keys belong to the given group.
func (rt *serveRuntime) countGroupPeers(groupID string) int {
	entries, err := auth.ListPeers(rt.authKeys)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if e.Group == groupID {
			count++
		}
	}
	return count
}

// StartWatchdog signals systemd readiness and starts health check loop.
// Additional health checks can be passed for daemon-specific checks (e.g., socket alive).
func (rt *serveRuntime) StartWatchdog(extraChecks ...watchdog.HealthCheck) {
	h := rt.network.Host()

	watchdog.Ready()

	checks := []watchdog.HealthCheck{
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
			Name: "relay-reservation",
			Check: func() error {
				for _, addr := range h.Addrs() {
					if strings.Contains(addr.String(), "p2p-circuit") {
						return nil
					}
				}
				return fmt.Errorf("no relay addresses")
			},
		},
	}

	checks = append(checks, extraChecks...)

	go watchdog.Run(rt.ctx, watchdog.Config{Interval: 30 * time.Second}, checks)
}

// StartStatusPrinter runs a background goroutine that periodically prints status.
func (rt *serveRuntime) StartStatusPrinter() {
	h := rt.network.Host()

	go func() {
		select {
		case <-rt.ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			fmt.Println()
			fmt.Println("--- Status ---")
			fmt.Printf("Peer ID: %s\n", h.ID())
			fmt.Printf("Connected peers: %d\n", len(h.Network().Peers()))
			fmt.Println("Addresses:")
			for _, addr := range h.Addrs() {
				label := "local"
				addrStr := addr.String()
				if strings.Contains(addrStr, "/p2p-circuit") {
					label = "RELAY"
				} else if !strings.Contains(addrStr, "/ip4/10.") &&
					!strings.Contains(addrStr, "/ip4/192.168.") &&
					!strings.Contains(addrStr, "/ip4/127.") &&
					!strings.Contains(addrStr, "/ip6/::1") &&
					!strings.Contains(addrStr, "/ip6/fe80") &&
					!strings.Contains(addrStr, "/ip6/fd") {
					label = "public"
				}
				fmt.Printf("  [%s] %s\n", label, addrStr)
			}
			fmt.Println("--------------")
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// ConnectToPeer ensures the host can reach the target peer using parallel
// path racing. It launches DHT discovery and relay circuit attempts
// concurrently, returning as soon as the first path succeeds.
// This is used by daemon API handlers (ping, traceroute, connect).
func (rt *serveRuntime) ConnectToPeer(ctx context.Context, peerID peer.ID) error {
	result, err := rt.pathDialer.DialPeer(ctx, peerID)
	if err != nil {
		return err
	}
	fmt.Printf("Connected to %s [%s] via %s (%s)\n",
		peerID.String()[:16], result.PathType, result.Address, result.Duration.Round(time.Millisecond))
	return nil
}

// StartMetricsServer starts the /metrics HTTP endpoint if metrics are enabled.
// Returns immediately; the server runs in a background goroutine.
func (rt *serveRuntime) StartMetricsServer() {
	if rt.metrics == nil {
		return
	}

	addr := rt.config.Telemetry.Metrics.ListenAddress
	mux := http.NewServeMux()
	mux.Handle("/metrics", rt.metrics.Handler())

	rt.metricsServer = &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	go func() {
		slog.Info("metrics endpoint started", "addr", addr)
		if err := rt.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			slog.Error("metrics endpoint error", "err", err)
		}
	}()
}

// StartPeerHistorySaver runs a background goroutine that periodically saves
// the peer interaction history to disk.
func (rt *serveRuntime) StartPeerHistorySaver() {
	if rt.peerHistory == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-rt.ctx.Done():
				return
			case <-ticker.C:
				if err := rt.peerHistory.Save(); err != nil {
					slog.Warn("peer-history: save failed", "err", err)
				}
			}
		}
	}()
}

// Shutdown cancels the context, stops the metrics server, disables the peer relay,
// and closes the P2P network.
func (rt *serveRuntime) Shutdown() {
	// Save peer history before exit.
	if rt.peerHistory != nil {
		if err := rt.peerHistory.Save(); err != nil {
			slog.Warn("peer-history: final save failed", "err", err)
		}
	}
	if rt.peerRelay != nil {
		rt.peerRelay.Disable()
	}
	if rt.metricsServer != nil {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 3*time.Second)
		rt.metricsServer.Shutdown(shutdownCtx)
		shutdownCancel()
	}
	rt.cancel()
	rt.network.Close()
}
