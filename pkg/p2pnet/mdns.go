package p2pnet

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	libp2pmdns "github.com/libp2p/go-libp2p/p2p/discovery/mdns"
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
)

// MDNSDiscovery wraps libp2p mDNS for local network peer discovery.
// Discovered peers have their addresses added to the peerstore;
// connection attempts go through the normal ConnectionGater.
type MDNSDiscovery struct {
	host    host.Host
	service libp2pmdns.Service
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
}

// NewMDNSDiscovery creates an mDNS discovery service.
// Metrics is optional (nil-safe).
func NewMDNSDiscovery(h host.Host, m *Metrics) *MDNSDiscovery {
	md := &MDNSDiscovery{
		host:    h,
		metrics: m,
		lastTry: make(map[peer.ID]time.Time),
		sem:     make(chan struct{}, mdnsMaxConcurrentConnects),
	}
	md.service = libp2pmdns.NewMdnsService(h, MDNSServiceName, md)
	return md
}

// Start begins mDNS advertising and browsing on the local network.
func (md *MDNSDiscovery) Start(ctx context.Context) error {
	md.ctx, md.cancel = context.WithCancel(ctx)
	return md.service.Start()
}

// Close stops the mDNS service and waits for in-flight connection
// attempts to finish. The managed context cancellation ensures
// goroutines don't leak on shutdown.
func (md *MDNSDiscovery) Close() error {
	md.cancel()
	md.wg.Wait()
	return md.service.Close()
}

// HandlePeerFound implements the libp2p mdns.Notifee interface.
// Called when a peer is discovered via mDNS on the local network.
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
