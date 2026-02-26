package p2pnet

import (
	"context"
	"log/slog"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
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

	// Add addresses with a 10-minute TTL. LAN addresses are ephemeral;
	// they refresh on the next mDNS cycle.
	md.host.Peerstore().AddAddrs(pi.ID, pi.Addrs, 10*time.Minute)

	// Path upgrade: if peer is connected via relay only, close the relay
	// connections so host.Connect() below will establish a direct path
	// using the LAN addresses we just added to the peerstore.
	conns := md.host.Network().ConnsToPeer(pi.ID)
	allRelayed := len(conns) > 0
	for _, conn := range conns {
		if !conn.Stat().Limited {
			allRelayed = false
			break
		}
	}
	if allRelayed {
		slog.Info("mdns: peer on LAN but connected via relay, upgrading to direct", "peer", short)
		for _, conn := range conns {
			conn.Close()
		}
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

		if err := md.host.Connect(ctx, pi); err != nil {
			slog.Debug("mdns: connect failed", "peer", short, "error", err)
			return
		}

		slog.Info("mdns: connected to LAN peer", "peer", short)
		if md.metrics != nil && md.metrics.MDNSDiscoveredTotal != nil {
			md.metrics.MDNSDiscoveredTotal.WithLabelValues("connected").Inc()
		}
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
