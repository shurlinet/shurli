package p2pnet

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
	"github.com/libp2p/zeroconf/v2"
	ma "github.com/multiformats/go-multiaddr"
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
	host    host.Host
	server  *zeroconf.Server
	metrics *Metrics

	// Managed context for clean shutdown of connection goroutines.
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Dedup: tracks last connection attempt time per peer.
	mu      sync.Mutex
	lastTry map[peer.ID]time.Time

	// Semaphore for concurrent connection attempts.
	sem chan struct{}

	// browseNowCh signals the browse loop to run immediately.
	// Used after network changes to discover LAN peers faster.
	browseNowCh chan struct{}
}

// NewMDNSDiscovery creates an mDNS discovery service.
// Metrics is optional (nil-safe).
func NewMDNSDiscovery(h host.Host, m *Metrics) *MDNSDiscovery {
	return &MDNSDiscovery{
		host:        h,
		metrics:     m,
		lastTry:     make(map[peer.ID]time.Time),
		sem:         make(chan struct{}, mdnsMaxConcurrentConnects),
		browseNowCh: make(chan struct{}, 1),
	}
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

	// Dedup: skip if we attempted this peer recently.
	md.mu.Lock()
	if last, ok := md.lastTry[pi.ID]; ok && time.Since(last) < mdnsDedupeInterval {
		md.mu.Unlock()
		return
	}
	md.lastTry[pi.ID] = time.Now()
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
	if len(lanAddrs) > 0 {
		pi.Addrs = lanAddrs
		md.host.Peerstore().AddAddrs(pi.ID, lanAddrs, 10*time.Minute)
		slog.Info("mdns: filtered to LAN addresses", "peer", short, "lan_addrs", len(lanAddrs))
	} else {
		// No LAN match: add all and let libp2p try everything.
		md.host.Peerstore().AddAddrs(pi.ID, allAddrs, 10*time.Minute)
	}

	// Check if peer is connected via relay only. If so, we'll upgrade
	// to direct using ForceDirectDial (establishes direct alongside relay,
	// then closes relay after).
	conns := md.host.Network().ConnsToPeer(pi.ID)
	needsUpgrade := allConnsRelayed(conns)
	if needsUpgrade {
		slog.Info("mdns: peer on LAN but connected via relay, upgrading to direct", "peer", short)
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

		ctx, cancel := context.WithTimeout(md.ctx, mdnsConnectTimeout)
		defer cancel()

		if needsUpgrade {
			// Force a direct dial even though relay connection exists.
			// This establishes direct without dropping relay first.
			ctx = network.WithForceDirectDial(ctx, "mdns-upgrade")
		}

		if err := md.host.Connect(ctx, pi); err != nil {
			slog.Debug("mdns: connect failed", "peer", short, "error", err)
			// Still add all addrs so other subsystems can use them.
			md.host.Peerstore().AddAddrs(pi.ID, allAddrs, 10*time.Minute)
			return
		}

		slog.Info("mdns: connected to LAN peer", "peer", short)
		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("connected").Inc()
		}

		// Add the full address set now that connect succeeded.
		md.host.Peerstore().AddAddrs(pi.ID, allAddrs, 10*time.Minute)

		// Close any relay connections. After an mDNS LAN connect, relay
		// is always redundant. Sweep multiple times to catch relay
		// connections that PeerManager may establish concurrently
		// (its reconnect loop runs every 30s, may already be mid-dial).
		go func() {
			closeRelay := func() int {
				closed := 0
				for _, conn := range md.host.Network().ConnsToPeer(pi.ID) {
					if conn.Stat().Limited {
						conn.Close()
						closed++
					}
				}
				return closed
			}

			// Immediate close + 3 sweeps over 30s to cover PeerManager's
			// reconnect loop window.
			if n := closeRelay(); n > 0 {
				slog.Info("mdns: closed relay conns (LAN direct active)",
					"peer", short, "closed", n)
			}
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			for {
				select {
				case <-md.ctx.Done():
					return
				case <-timer.C:
					closeRelay()
					return
				case <-ticker.C:
					if n := closeRelay(); n > 0 {
						slog.Info("mdns: closed relay conns (sweep)",
							"peer", short, "closed", n)
					}
				}
			}
		}()
	}()
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

// filterLANAddrs returns only the multiaddrs with a private IPv4 address
// on the same subnet as one of our local interfaces. mDNS means "same
// LAN", so only private IPv4 addresses are reliable for direct connection.
//
// Why not IPv6/ULA: many consumer routers give all clients the same IPv6
// prefix but block inter-client IPv6 traffic (client isolation). ULA
// prefixes (fd00::/8) match our subnet but may also be blocked. Private
// IPv4 is the universal LAN signal: if both devices share a 10.x/16 or
// 192.168.x/24 subnet, they can talk.
func filterLANAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	localNets := localIPv4Subnets()
	if len(localNets) == 0 {
		return nil // can't determine local subnets, don't filter
	}

	var lan []ma.Multiaddr
	for _, addr := range addrs {
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		if first.Protocol().Code != ma.P_IP4 {
			continue
		}
		ip := net.ParseIP(first.Value())
		if ip == nil || ip.IsLoopback() {
			continue
		}
		// Keep if the IPv4 is on any of our local subnets
		for _, ln := range localNets {
			if ln.Contains(ip) {
				lan = append(lan, addr)
				break
			}
		}
	}
	return lan
}

// localIPv4Subnets returns the CIDR networks of all private IPv4
// addresses on active, non-loopback interfaces.
func localIPv4Subnets() []*net.IPNet {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var nets []*net.IPNet
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipNet.IP.To4()
			if ip4 == nil {
				continue // IPv6, skip
			}
			if ip4.IsLinkLocalUnicast() || ip4.IsLoopback() {
				continue
			}
			nets = append(nets, ipNet)
		}
	}
	return nets
}
