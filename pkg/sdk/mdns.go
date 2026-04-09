package sdk

import (
	"context"
	"log/slog"
	"math/rand"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	"github.com/libp2p/zeroconf/v2"
	ma "github.com/multiformats/go-multiaddr"
	manet "github.com/multiformats/go-multiaddr/net"
)

// MDNSServiceName is the DNS-SD service type used for LAN discovery.
// Fixed for all Shurli nodes. Network isolation is handled by the
// ConnectionGater (authorized_keys), not by mDNS service names.
const MDNSServiceName = "_shurli._udp"

const (
	// mdnsConnectTimeout is the per-peer connection timeout for mDNS
	// discovered peers. 5 seconds is generous for LAN connections.
	mdnsConnectTimeout = 5 * time.Second

	// mdnsDedupeInterval is how long to suppress repeated connection
	// attempts to the same peer. Prevents thundering herd when mDNS
	// fires multiple discovery events for the same peer.
	mdnsDedupeInterval = 30 * time.Second

	// mdnsMaxConcurrentConnects limits simultaneous mDNS connection
	// attempts. On a busy LAN with many Shurli nodes, prevents
	// spawning hundreds of goroutines at once.
	mdnsMaxConcurrentConnects = 5

	// discoveredAddrTTL is the peerstore TTL for addresses learned via
	// mDNS discovery, probes, or peerstore manipulation (strip/restore).
	// 10 minutes balances freshness with stability. Used across mdns.go
	// and peermanager.go. Intentionally shorter than DHT-discovered addrs
	// (1 hour in pathdialer.go) since LAN topology changes frequently.
	discoveredAddrTTL = 10 * time.Minute

	// mdnsBrowseInterval controls how often we re-query the network.
	// Each round creates a fresh multicast socket, working around
	// platform-specific issues where a single long-lived Browse
	// stalls silently (macOS mDNSResponder interference, Linux avahi
	// socket conflicts, etc.).
	mdnsBrowseInterval = 30 * time.Second

	// mdnsBrowseTimeout is how long each Browse round runs before
	// being canceled and restarted. Keeps the multicast socket fresh.
	mdnsBrowseTimeout = 10 * time.Second

	// dnsaddrPrefix matches libp2p's TXT record format for multiaddrs.
	dnsaddrPrefix = "dnsaddr="
)

// MDNSDiscovery handles LAN peer discovery using mDNS (DNS-SD).
// Registers the service via zeroconf.RegisterProxy, then runs a
// periodic browse loop using platform-native APIs when available
// (dns_sd.h on macOS/Linux) or zeroconf as fallback.
//
// Native browse cooperates with the system mDNS daemon via IPC
// (mDNSResponder on macOS, avahi on Linux) instead of competing
// for the multicast socket on port 5353.
//
// Discovered peers have their addresses added to the peerstore;
// connection attempts go through the normal ConnectionGater.
type MDNSDiscovery struct {
	host        host.Host
	server      *zeroconf.Server
	metrics     *Metrics
	lanRegistry *LANRegistry // mDNS-verified LAN peer/IP tracking

	// Managed context for clean shutdown of connection goroutines.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Dedup: tracks last connection attempt time per peer.
	mu      sync.Mutex
	lastTry map[peer.ID]time.Time

	// lanPeers tracks peers discovered via mDNS (proven on LAN).
	// mDNS multicast only works on the local network segment, so
	// discovery = proof of LAN presence. Used by transport classification
	// to detect LAN peers even when the QUIC connection uses public IPv6.
	lanPeers map[peer.ID]time.Time

	// Semaphore for concurrent connection attempts.
	sem chan struct{}

	// browseNowCh signals the browse loop to run immediately.
	// Used after network changes to discover LAN peers faster.
	browseNowCh chan struct{}
}

// NewMDNSDiscovery creates an mDNS discovery service.
// Metrics is optional (nil-safe).
func NewMDNSDiscovery(h host.Host, m *Metrics, lanReg *LANRegistry) *MDNSDiscovery {
	if lanReg == nil {
		lanReg = NewLANRegistry()
	}
	return &MDNSDiscovery{
		host:        h,
		metrics:     m,
		lanRegistry: lanReg,
		lastTry:     make(map[peer.ID]time.Time),
		lanPeers:    make(map[peer.ID]time.Time),
		sem:         make(chan struct{}, mdnsMaxConcurrentConnects),
		browseNowCh: make(chan struct{}, 1),
	}
}

// IsLANPeer returns true if the peer was discovered via mDNS within the last
// 5 minutes. mDNS multicast only works on the local network segment, so
// discovery is proof of LAN presence regardless of which IP the QUIC
// connection uses (private IPv4 or public IPv6 on the same LAN).
func (md *MDNSDiscovery) IsLANPeer(id peer.ID) bool {
	md.mu.Lock()
	t, ok := md.lanPeers[id]
	md.mu.Unlock()
	return ok && time.Since(t) < 5*time.Minute
}

// Start begins mDNS advertising and periodic browsing on the local network.
func (md *MDNSDiscovery) Start(ctx context.Context) error {
	md.ctx, md.cancel = context.WithCancel(ctx)

	// Register our service with zeroconf directly (no libp2p wrapper).
	if err := md.startServer(); err != nil {
		return err
	}

	// Start periodic browse loop.
	md.wg.Add(1)
	go md.browseLoop()
	return nil
}

// Close stops the mDNS service and waits for in-flight connection
// attempts to finish.
func (md *MDNSDiscovery) Close() error {
	md.cancel()
	if md.server != nil {
		md.server.Shutdown()
	}
	md.wg.Wait()
	return nil
}

// startServer registers our service with zeroconf for mDNS advertising.
// Builds TXT records from the host's listen addresses, following libp2p's
// dnsaddr= format so other nodes (including those using libp2p's mDNS
// wrapper) can parse them.
func (md *MDNSDiscovery) startServer() error {
	interfaceAddrs, err := md.host.Network().InterfaceListenAddresses()
	if err != nil {
		return err
	}

	p2pAddrs, err := peer.AddrInfoToP2pAddrs(&peer.AddrInfo{
		ID:    md.host.ID(),
		Addrs: interfaceAddrs,
	})
	if err != nil {
		return err
	}

	// Build TXT records for addresses suitable for mDNS (IP-based, no relay).
	var txts []string
	for _, addr := range p2pAddrs {
		if isSuitableForMDNS(addr) {
			txts = append(txts, dnsaddrPrefix+addr.String())
		}
	}

	// Extract IPs for A/AAAA records (required by DNS-SD spec but we
	// only use TXT records for actual address resolution).
	ips := getIPs(p2pAddrs)

	peerName := randomString(32 + rand.Intn(32))
	server, err := zeroconf.RegisterProxy(
		peerName,
		MDNSServiceName,
		"local",
		4001, // port required by spec; we use TXT records for actual addresses
		peerName,
		ips,
		txts,
		nil,
	)
	if err != nil {
		return err
	}
	md.server = server
	return nil
}

// BrowseNow triggers an immediate mDNS re-browse. Called after network
// changes to discover LAN peers without waiting for the next 30s cycle.
// Clears dedup timers since the network context has changed.
func (md *MDNSDiscovery) BrowseNow() {
	md.mu.Lock()
	clear(md.lastTry)
	md.mu.Unlock()
	select {
	case md.browseNowCh <- struct{}{}:
	default: // browse already pending
	}
}

// browseLoop periodically runs nativeBrowse to discover peers.
// On macOS/Linux with CGo, uses the platform's DNS-SD API.
// Falls back to zeroconf on other platforms.
func (md *MDNSDiscovery) browseLoop() {
	defer md.wg.Done()

	// Small initial delay to let the host finish setting up
	// (interface binding, relay connection, etc.).
	select {
	case <-time.After(2 * time.Second):
	case <-md.ctx.Done():
		return
	}

	// Run first browse immediately after initial delay.
	md.runBrowse()

	ticker := time.NewTicker(mdnsBrowseInterval)
	defer ticker.Stop()

	for {
		select {
		case <-md.ctx.Done():
			return
		case <-ticker.C:
			md.runBrowse()
		case <-md.browseNowCh:
			md.runBrowse()
		}
	}
}

// runBrowse executes a single bounded browse round, processing any
// discovered entries through HandlePeerFound. Uses nativeBrowse which
// is platform-specific (dns_sd.h on macOS/Linux, zeroconf fallback).
func (md *MDNSDiscovery) runBrowse() {
	browseCtx, browseCancel := context.WithTimeout(md.ctx, mdnsBrowseTimeout)
	defer browseCancel()

	entries := make(chan []string, 100)

	// Consumer: process TXT record sets as they arrive.
	var browseWG sync.WaitGroup
	browseWG.Add(1)
	go func() {
		defer browseWG.Done()
		for txts := range entries {
			md.processTextRecords(txts)
		}
	}()

	// nativeBrowse blocks until context is canceled or times out.
	if err := nativeBrowse(browseCtx, MDNSServiceName, "local.", entries); err != nil {
		// Context cancellation/timeout is normal.
		if md.ctx.Err() == nil {
			slog.Debug("mdns: browse round error", "error", err)
		}
	}
	close(entries)

	browseWG.Wait()
}

// processTextRecords converts mDNS TXT records to peer.AddrInfo
// and feeds each through HandlePeerFound.
func (md *MDNSDiscovery) processTextRecords(txts []string) {
	addrs := make([]ma.Multiaddr, 0, len(txts))
	for _, txt := range txts {
		if !strings.HasPrefix(txt, dnsaddrPrefix) {
			continue
		}
		addr, err := ma.NewMultiaddr(txt[len(dnsaddrPrefix):])
		if err != nil {
			slog.Debug("mdns: bad multiaddr in TXT", "error", err)
			continue
		}
		addrs = append(addrs, addr)
	}
	if len(addrs) == 0 {
		return
	}

	infos, err := peer.AddrInfosFromP2pAddrs(addrs...)
	if err != nil {
		slog.Debug("mdns: failed to parse peer addrs", "error", err)
		return
	}
	for _, info := range infos {
		if info.ID == md.host.ID() {
			continue
		}
		md.HandlePeerFound(info)
	}
}

// HandlePeerFound is called when a peer is discovered via mDNS on the
// local network.
func (md *MDNSDiscovery) HandlePeerFound(pi peer.AddrInfo) {
	if pi.ID == md.host.ID() {
		return
	}

	short := pi.ID.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	// Check if peer needs upgrading BEFORE dedup. Upgrade is needed when:
	// 1. All connections are relayed (relay->direct upgrade), OR
	// 2. Direct connections exist but none use LAN IPv4 (internet->LAN upgrade).
	//    Case 2 happens on satellite networks: DHT bootstrap connects via IPv6 QUIC, but
	//    satellite router client isolation blocks inter-client IPv6 data. The connection
	//    appears "direct" but is silently dead. mDNS proving LAN reachability
	//    means we should establish a real LAN connection.
	// Upgrade attempts bypass dedup because mDNS multicast reception is proof
	// the peer is on LAN right now.
	connsForDedup := md.host.Network().ConnsToPeer(pi.ID)
	peerNeedsUpgrade := allConnsRelayed(connsForDedup) ||
		(len(connsForDedup) > 0 && !md.lanRegistry.HasVerifiedLANConn(md.host, pi.ID))

	// Dedup: skip if we attempted this peer recently.
	// Exception: relay-only peers always get an upgrade attempt.
	md.mu.Lock()
	if last, ok := md.lastTry[pi.ID]; ok && time.Since(last) < mdnsDedupeInterval && !peerNeedsUpgrade {
		md.mu.Unlock()
		return
	}
	md.lastTry[pi.ID] = time.Now()
	md.mu.Unlock()

	// Mark peer as LAN-proven. mDNS multicast = same network segment.
	md.mu.Lock()
	md.lanPeers[pi.ID] = time.Now()
	md.mu.Unlock()

	slog.Info("mdns: peer discovered on LAN", "peer", short, "addrs", len(pi.Addrs))

	if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
		md.metrics.MDNSDiscoveredTotal.WithLabelValues("discovered").Inc()
	}

	// Filter to LAN-reachable addresses for the mDNS connect. mDNS means
	// "same LAN" so only private IPv4 on our subnets is reliable. Public
	// IPv6, ULA, and cross-network addresses waste the connect timeout
	// (e.g., satellite routers with client isolation block inter-client IPv6).
	//
	// We save all addresses but only add LAN addrs to the peerstore
	// BEFORE connecting. This prevents the swarm from trying unreachable
	// addresses (it uses ALL peerstore addrs, not just pi.Addrs).
	// The full address set is added AFTER the connect succeeds.
	allAddrs := pi.Addrs
	lanAddrs := filterLANAddrs(allAddrs)

	// Use the upgrade check from above (computed before dedup).
	// Re-check in case state changed between dedup and here.
	currentConns := md.host.Network().ConnsToPeer(pi.ID)
	needsUpgrade := allConnsRelayed(currentConns) ||
		(len(currentConns) > 0 && !md.lanRegistry.HasVerifiedLANConn(md.host, pi.ID))

	if len(lanAddrs) > 0 {
		pi.Addrs = lanAddrs
		md.host.Peerstore().AddAddrs(pi.ID, lanAddrs, discoveredAddrTTL)
		slog.Info("mdns: filtered to LAN addresses", "peer", short, "lan_addrs", len(lanAddrs))

		// Register mDNS-verified LAN IPs. Only these IPs are trusted as
		// LAN by the gater and connLogger (BUG-MP-6+7).
		var verifiedIPs []string
		for _, addr := range lanAddrs {
			if ip := extractIPFromMultiaddrObj(addr); ip != "" {
				verifiedIPs = append(verifiedIPs, ip)
			}
		}
		if len(verifiedIPs) > 0 {
			md.lanRegistry.Add(pi.ID, verifiedIPs)
		}
	} else {
		// No LAN match: add all and let libp2p try everything.
		md.host.Peerstore().AddAddrs(pi.ID, allAddrs, discoveredAddrTTL)
	}
	if needsUpgrade {
		reason := "relay-only"
		if !allConnsRelayed(currentConns) {
			reason = "direct-but-no-LAN"
		}
		slog.Info("mdns: peer on LAN but needs upgrade", "peer", short, "reason", reason)
		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("upgraded").Inc()
		}
	}

	// Acquire semaphore slot. Non-blocking: if all slots are busy, skip.
	select {
	case md.sem <- struct{}{}:
	default:
		slog.Debug("mdns: concurrent connect limit reached, skipping", "peer", short)
		return
	}

	// Attempt connection in a tracked goroutine using the managed context.
	md.wg.Add(1)
	go func() {
		defer md.wg.Done()
		defer func() { <-md.sem }()

		if needsUpgrade {
			// Use only LAN IPv4 TCP for the first attempt. Trying multiple
			// address families simultaneously (IPv4 + IPv6) causes cascade
			// failures in the swarm: rapid IPv6 "no route" kills the IPv4
			// through rate limiting. Retry broadens to all TCP if needed.
			tcpAddrs := filterTCPAddrs(filterLANAddrs(allAddrs))
			if len(tcpAddrs) == 0 {
				slog.Warn("mdns: no usable TCP addresses for upgrade", "peer", short)
				md.host.Peerstore().AddAddrs(pi.ID, allAddrs, discoveredAddrTTL)
				return
			}

			// Probe TCP reachability before involving libp2p's dial machinery.
			// The swarm dial worker caches failures per address in trackedDials;
			// if WiFi is still settling, a failed DialPeer poisons the cache and
			// blocks retries that join the same worker. The retrying probe (500ms
			// intervals, 3s budget) waits for the path to work before committing
			// to a DialPeer that would burn the address in the worker cache.
			// The probe uses its own timeout so it does NOT consume DialPeer's 5s.
			if !md.probeAddr(tcpAddrs[0], short) {
				md.scheduleRetry(pi.ID, tcpAddrs, allAddrs, short)
				return
			}

			// Fresh context for DialPeer - probe already ran with its own timeout,
			// DialPeer gets a full mdnsConnectTimeout budget.
			ctx, cancel := context.WithTimeout(md.ctx, mdnsConnectTimeout)
			defer cancel()
			ctx = network.WithForceDirectDial(ctx, "mdns-upgrade")

			dialErr := md.dialWithBackoffClear(ctx, pi.ID, tcpAddrs, allAddrs)

			if dialErr != nil {
				slog.Info("mdns: upgrade to direct failed", "peer", short, "error", dialErr)
				md.scheduleRetry(pi.ID, tcpAddrs, allAddrs, short)
				return
			}
			slog.Info("mdns: upgraded to DIRECT via LAN", "peer", short)

			// Close non-LAN direct connections AND strip their addresses
			// from the peerstore. When upgrading from internet-direct (IPv6)
			// to LAN-direct (private IPv4), the old connection is likely dead
			// (e.g. satellite router client isolation blocks inter-client IPv6).
			//
			// Two-part cleanup is essential:
			// 1. Close the connection (stop streams from using it)
			// 2. Strip non-LAN addrs from peerstore (prevent PeerManager
			//    from immediately re-dialing the dead IPv6 path)
			//
			// LAN addresses are preserved via mDNS re-discovery (every 30s).
			// Non-LAN addresses will be restored naturally by DHT refresh
			// or identify protocol, at which point the connection may work
			// again (e.g., after switching away from satellite router).
			for _, c := range md.host.Network().ConnsToPeer(pi.ID) {
				if c.Stat().Limited {
					continue // leave relay connections alone
				}
				remoteMA := c.RemoteMultiaddr()
				first, _ := ma.SplitFirst(remoteMA)
				if first == nil {
					continue
				}
				if first.Protocol().Code == ma.P_IP4 {
					ip := net.ParseIP(first.Value())
					if ip != nil && isPrivateIPv4(ip) {
						continue // keep LAN connections
					}
				}
				if streams := c.GetStreams(); len(streams) > 0 {
					slog.Info("mdns: keeping non-LAN conn (has active streams)",
						"peer", short, "remote", remoteMA, "streams", len(streams))
					continue
				}
				slog.Info("mdns: closing non-LAN direct conn (LAN established)",
					"peer", short, "remote", remoteMA)
				c.Close()
			}

			// Strip non-LAN addresses from peerstore to prevent re-dial.
			// Uses shared function (also called by connLogger for BUG-MP-4).
			StripNonLANAddrs(md.host, pi.ID)
		} else {
			// Strip non-LAN addresses from peerstore BEFORE connecting.
			// host.Connect dials ALL peerstore addresses, not just pi.Addrs.
			// Without this, old public IPv6 addresses from identify/DHT win
			// the dial race against LAN IPv4 (BUG-MP-4 root cause: gater
			// can't protect a LAN path that was never established).
			savedAddrs := md.host.Peerstore().Addrs(pi.ID)
			StripNonLANAddrs(md.host, pi.ID)

			ctx, cancel := context.WithTimeout(md.ctx, mdnsConnectTimeout)
			defer cancel()

			if err := md.host.Connect(ctx, pi); err != nil {
				slog.Info("mdns: connect failed", "peer", short, "error", err)
				// Restore all addresses so peer is reachable via other paths.
				md.host.Peerstore().AddAddrs(pi.ID, savedAddrs, discoveredAddrTTL)
				md.host.Peerstore().AddAddrs(pi.ID, allAddrs, discoveredAddrTTL)
				return
			}
			// Verify we actually established a direct connection. If pathDialer
			// won the race while this goroutine was starting, Connect() returns
			// nil immediately (peer already connected via relay) with no direct
			// established. The false "connected to LAN peer" log masked this
			// TOCTOU race — the real direct arrives later via home-node inbound.
			if allConnsRelayed(md.host.Network().ConnsToPeer(pi.ID)) {
				slog.Info("mdns: connect nil but relay-only (pathDialer won race, awaiting inbound direct)",
					"peer", short)
			} else {
				slog.Info("mdns: connected to LAN peer", "peer", short)
			}
		}
		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("connected").Inc()
		}

		// Add only LAN + relay addresses when a LAN connection exists.
		// Adding public addresses causes the remote peer's PeerManager to
		// reconnect via public IPv6 after we close the non-LAN connection,
		// undoing the LAN upgrade in a 30s cycle.
		if md.lanRegistry.HasVerifiedLANConn(md.host, pi.ID) {
			var safeAddrs []ma.Multiaddr
			for _, addr := range allAddrs {
				if isCircuitAddr(addr) || IsLANMultiaddr(addr) {
					safeAddrs = append(safeAddrs, addr)
				}
			}
			if len(safeAddrs) > 0 {
				md.host.Peerstore().AddAddrs(pi.ID, safeAddrs, discoveredAddrTTL)
			}
		} else {
			md.host.Peerstore().AddAddrs(pi.ID, allAddrs, discoveredAddrTTL)
		}

		// Don't close relay connections here. Let direct and relay
		// coexist. libp2p prefers non-limited (direct) connections for
		// new streams, so relay traffic stops naturally. Aggressively
		// closing relay triggers a fight with the remote peer's
		// PeerManager reconnect loop: we close relay, they re-establish
		// it, the churn eventually kills the direct connection too.
		// Relay cleanup is handled by probeAndUpgrade's closeRelayConns
		// after confirming the direct path is stable.
	}()
}

// dialWithBackoffClear attempts a ForceDirectDial to a peer using only the
// specified TCP addresses. Before dialing, it clears any stale swarm backoff
// for this peer (mDNS discovery proves LAN reachability, so prior "no route
// to host" failures from a different network state are invalid).
func (md *MDNSDiscovery) dialWithBackoffClear(ctx context.Context, pid peer.ID, tcpAddrs, allAddrs []ma.Multiaddr) error {
	// Clear stale swarm dial backoff. mDNS multicast reception is proof
	// the peer is on our LAN - any cached dial failure is from old state.
	if sw, ok := md.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(pid)
	}

	// Temporarily restrict peerstore to TCP LAN addresses only,
	// same technique as probeAndUpgrade fix #3.
	savedAddrs := md.host.Peerstore().Addrs(pid)
	md.host.Peerstore().ClearAddrs(pid)
	md.host.Peerstore().AddAddrs(pid, tcpAddrs, discoveredAddrTTL)

	_, dialErr := md.host.Network().DialPeer(ctx, pid)

	if dialErr != nil {
		// Restore full address set on failure so peer is reachable via other paths.
		md.host.Peerstore().AddAddrs(pid, savedAddrs, discoveredAddrTTL)
		md.host.Peerstore().AddAddrs(pid, allAddrs, discoveredAddrTTL)
	}
	// On success: keep only LAN+relay in peerstore. Non-LAN addresses will
	// be restored naturally by identify protocol. Restoring them here would
	// re-contaminate the peerstore and let PeerManager re-dial via IPv6
	// during the window before StripNonLANAddrs runs in the caller.

	return dialErr
}

// scheduleRetry schedules a single delayed retry of the mDNS upgrade dial.
// Called when the first attempt fails but mDNS proved the peer is on LAN
// (multicast packet received = LAN reachable). The retry runs after 10s,
// giving ARP time to resolve (satellite WiFi: 60-80s from cold, but often
// only needs a few seconds after initial failure). Capped at one retry -
// if it fails, the next mDNS browse cycle (30s) takes over with fresh state.
func (md *MDNSDiscovery) scheduleRetry(pid peer.ID, tcpLANAddrs, allAddrs []ma.Multiaddr, short string) {
	md.wg.Add(1)
	time.AfterFunc(10*time.Second, func() {
		defer md.wg.Done()

		// Check if context is still alive (daemon not shutting down).
		if md.ctx.Err() != nil {
			return
		}

		// Acquire semaphore slot (same as HandlePeerFound). Non-blocking:
		// if all slots are busy, skip - the next browse cycle will retry.
		select {
		case md.sem <- struct{}{}:
		default:
			slog.Debug("mdns: retry skipped, concurrent connect limit reached", "peer", short)
			return
		}
		defer func() { <-md.sem }()

		// Check if peer is still relay-only. If already direct (home-node
		// dialed in, or probeAndUpgrade succeeded), no retry needed.
		conns := md.host.Network().ConnsToPeer(pid)
		if !allConnsRelayed(conns) {
			slog.Debug("mdns: retry skipped, already direct", "peer", short)
			return
		}

		// Retry with same filtered set (private IPv4 + global IPv6 TCP).
		// The first attempt may have failed due to ARP not resolved yet.
		// 10s later, ARP should be ready.
		retryAddrs := filterMDNSUpgradeAddrs(allAddrs)
		if len(retryAddrs) == 0 {
			retryAddrs = tcpLANAddrs // fallback
		}

		// Probe before DialPeer (same as first attempt). By 10s after the
		// network switch, WiFi should be settled. If probe still fails,
		// the path is genuinely broken - let the next browse cycle handle it.
		if !md.probeAddr(retryAddrs[0], short) {
			return
		}

		slog.Info("mdns: retry upgrade (mDNS proved LAN reachable)",
			"peer", short, "tcp_addrs", len(retryAddrs))

		ctx, cancel := context.WithTimeout(md.ctx, mdnsConnectTimeout)
		defer cancel()
		ctx = network.WithForceDirectDial(ctx, "mdns-retry")

		if err := md.dialWithBackoffClear(ctx, pid, retryAddrs, allAddrs); err != nil {
			slog.Info("mdns: retry upgrade failed", "peer", short, "error", err)
			return
		}

		slog.Info("mdns: retry upgraded to DIRECT via LAN", "peer", short)

		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("connected").Inc()
		}
	})
}

// filterMDNSUpgradeAddrs returns TCP multiaddrs suitable for mDNS upgrade
// dials: private IPv4 (LAN) + global IPv6, TCP only. Excludes QUIC/UDP
// (black hole risk), loopback (never reachable cross-host), ULA (fd00::/8,
// typically not routable across satellite router segments), and circuit
// (relay, not direct). This gives the swarm a clean set: either the LAN
// IPv4 path works, or the global IPv6 TCP path works, or both.
func filterMDNSUpgradeAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var result []ma.Multiaddr
	for _, addr := range addrs {
		// Must have TCP transport.
		hasTCP := false
		ma.ForEach(addr, func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_TCP {
				hasTCP = true
				return false
			}
			return true
		})
		if !hasTCP {
			continue
		}
		// Exclude circuit/relay addresses.
		if isCircuitAddr(addr) {
			continue
		}
		// Check IP: allow private IPv4 (LAN) and global IPv6.
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		switch first.Protocol().Code {
		case ma.P_IP4:
			ip := net.ParseIP(first.Value())
			if ip == nil || ip.IsLoopback() {
				continue
			}
			// Allow private IPv4 (LAN addresses from mDNS).
			if isPrivateIPv4(ip) {
				result = append(result, addr)
			}
		case ma.P_IP6:
			ip := net.ParseIP(first.Value())
			if ip == nil || ip.IsLoopback() {
				continue
			}
			// Allow global IPv6 only. Exclude ULA (fd/fc) - often not
			// routable across router segments on satellite networks.
			if isGlobalIPv6(ip) {
				result = append(result, addr)
			}
		}
	}
	return result
}

// isSuitableForMDNS returns true for multiaddrs that should be advertised
// via mDNS. Must start with /ip4, /ip6, or .local DNS; must not use relay
// or browser-only transports. Matches libp2p's filtering logic.
func isSuitableForMDNS(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}
	first, _ := ma.SplitFirst(addr)
	if first == nil {
		return false
	}
	switch first.Protocol().Code {
	case ma.P_IP4, ma.P_IP6:
		// Direct IP addresses are always suitable for LAN discovery.
	case ma.P_DNS, ma.P_DNS4, ma.P_DNS6, ma.P_DNSADDR:
		if !strings.HasSuffix(strings.ToLower(first.Value()), ".local") {
			return false
		}
	default:
		return false
	}
	// Exclude relay and browser transports.
	excluded := false
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_CIRCUIT, ma.P_WEBTRANSPORT, ma.P_WEBRTC,
			ma.P_WEBRTC_DIRECT, ma.P_P2P_WEBRTC_DIRECT, ma.P_WS, ma.P_WSS:
			excluded = true
			return false
		}
		return true
	})
	return !excluded
}

// getIPs extracts one IPv4 and one IPv6 address from multiaddrs for
// A/AAAA records required by DNS-SD spec. Falls back to 127.0.0.1.
func getIPs(addrs []ma.Multiaddr) []string {
	var ip4, ip6 string
	for _, addr := range addrs {
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		if ip4 == "" && first.Protocol().Code == ma.P_IP4 {
			ip4 = first.Value()
		} else if ip6 == "" && first.Protocol().Code == ma.P_IP6 {
			ip6 = first.Value()
		}
	}
	var ips []string
	if ip4 != "" {
		ips = append(ips, ip4)
	}
	if ip6 != "" {
		ips = append(ips, ip6)
	}
	if len(ips) == 0 {
		ips = append(ips, "127.0.0.1")
	}
	return ips
}

func randomString(l int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	s := make([]byte, 0, l)
	for i := 0; i < l; i++ {
		s = append(s, alphabet[rand.Intn(len(alphabet))])
	}
	return string(s)
}

// rfc1918Nets are the globally-defined private IPv4 ranges (RFC 1918) plus
// RFC 6598 shared address space (100.64.0.0/10, used by CGNAT). These are
// the authoritative "local" ranges — any IP in them is non-routable on the
// public internet, and if mDNS delivered a packet from a peer advertising
// such an address, the peer is reachable on the local network by definition.
// Subnet mask size is irrelevant: a /16 LAN and a /24 LAN are equally local.
var rfc1918Nets = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",
		"172.16.0.0/12",
		"192.168.0.0/16",
		"100.64.0.0/10", // RFC 6598 shared address space (CGNAT)
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, _ := net.ParseCIDR(cidr)
		nets = append(nets, n)
	}
	return nets
}()

// isPrivateIPv4 returns true if ip falls within any RFC 1918 or RFC 6598 range.
func isPrivateIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	for _, n := range rfc1918Nets {
		if n.Contains(ip4) {
			return true
		}
	}
	return false
}

// filterLANAddrs returns only the multiaddrs with a private IPv4 address
// (RFC 1918 / RFC 6598). mDNS multicast is link-local — receiving a peer's
// advertisement proves the peer is on the same physical network. The peer's
// private IPv4 addresses are therefore directly reachable regardless of the
// subnet mask size (/16, /24, etc.). Public IPv6, ULA, and relay addresses
// are excluded: many routers block inter-client IPv6 (client isolation), and
// relay addresses should never be used for direct LAN connects.
func filterLANAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var lan []ma.Multiaddr
	for _, addr := range addrs {
		first, _ := ma.SplitFirst(addr)
		if first == nil || first.Protocol().Code != ma.P_IP4 {
			continue
		}
		ip := net.ParseIP(first.Value())
		if ip == nil || ip.IsLoopback() {
			continue
		}
		if isPrivateIPv4(ip) {
			lan = append(lan, addr)
		}
	}
	return lan
}

// filterTCPAddrs returns only multiaddrs using TCP transport.
// QUIC/UDP addresses are excluded because the UDP black hole detector
// may be in Blocked state from a previous CGNAT network.
func filterTCPAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var tcp []ma.Multiaddr
	for _, addr := range addrs {
		hasTCP := false
		ma.ForEach(addr, func(c ma.Component) bool {
			if c.Protocol().Code == ma.P_TCP {
				hasTCP = true
				return false
			}
			return true
		})
		if hasTCP {
			tcp = append(tcp, addr)
		}
	}
	return tcp
}

// isStaleOnNetworkChange returns true if the address becomes stale after a
// network switch and should be stripped from the peerstore. Covers:
//   - RFC 1918 private IPv4 (10/8, 172.16/12, 192.168/16)
//   - RFC 6598 CGNAT (100.64/10) - common with satellite ISP CGNAT, missed by manet.IsPrivateAddr
//   - ULA IPv6 (fc00::/7), link-local IPv6 (fe80::/10), loopback
//
// Public IPs and relay circuit multiaddrs (which start with the relay's
// public IP) are NOT stale - they survive network changes.
func isStaleOnNetworkChange(a ma.Multiaddr) bool {
	if manet.IsPrivateAddr(a) {
		return true
	}
	// manet.IsPrivateAddr misses CGNAT (100.64.0.0/10, RFC 6598).
	// filterLANAddrs includes CGNAT via isPrivateIPv4 - must be consistent.
	first, _ := ma.SplitFirst(a)
	if first != nil && first.Protocol().Code == ma.P_IP4 {
		if ip := net.ParseIP(first.Value()); ip != nil {
			return isPrivateIPv4(ip)
		}
	}
	return false
}

// probeAddr probes a single multiaddr for TCP reachability. Returns true if
// the path is ready (or if the multiaddr can't be parsed - let DialPeer handle
// malformed addrs). Logs the result with the peer short ID for diagnostics.
func (md *MDNSDiscovery) probeAddr(addr ma.Multiaddr, short string) bool {
	network, host, err := manet.DialArgs(addr)
	if err != nil {
		// Can't parse addr for probing - skip probe, let DialPeer handle it.
		return true
	}
	if probeTCPReachable(md.ctx, network, host, 3*time.Second) {
		return true
	}
	slog.Info("mdns: TCP probe failed (WiFi settling?)", "peer", short, "addr", host)
	return false
}

// probeTCPReachable does a retrying TCP connect probe to verify network
// reachability before involving libp2p's dial machinery. Returns true if
// a TCP connection succeeds within the budget. Retries every 500ms because
// macOS returns instant EHOSTUNREACH when WiFi is settling (the 3s budget
// would be wasted on a single non-retrying DialTimeout). The probe prevents
// poisoning the swarm dial worker's trackedDials cache with failures from
// transient network states. Context-aware: returns false immediately on
// cancellation (important for clean daemon shutdown - caller holds WaitGroup).
func probeTCPReachable(ctx context.Context, network, addr string, budget time.Duration) bool {
	deadline := time.Now().Add(budget)
	dialer := net.Dialer{Timeout: 1 * time.Second}
	for time.Now().Before(deadline) {
		dialCtx, cancel := context.WithTimeout(ctx, 1*time.Second)
		conn, err := dialer.DialContext(dialCtx, network, addr)
		cancel()
		if err == nil {
			conn.Close()
			return true
		}
		slog.Debug("mdns: probe attempt failed", "addr", addr, "error", err)
		if ctx.Err() != nil {
			return false
		}
		remaining := time.Until(deadline)
		if remaining < 500*time.Millisecond {
			return false
		}
		select {
		case <-time.After(500 * time.Millisecond):
		case <-ctx.Done():
			return false
		}
	}
	return false
}

