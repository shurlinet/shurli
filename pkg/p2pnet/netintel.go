// NetIntel implements a lightweight presence announcement protocol for sharing
// network intelligence between peers.
//
// # Three-Layer Transport Architecture
//
// Announcements propagate through three independent, co-existing layers:
//
//   - Layer 1 (Direct Push): Each node pushes its own NodeAnnouncement to all
//     directly connected peers every announceInterval. This is optimal for
//     authorized pools where members are fully interconnected.
//
//   - Layer 2 (Gossip Forwarding): When a node receives a NEW announcement
//     (timestamp newer than cached), it forwards to gossipFanout random
//     connected peers with an incremented hop counter. Max maxHops prevents
//     infinite propagation. This extends reach beyond direct connections to
//     ~fanout^maxHops unique peers per originator.
//
//   - Layer 3 (GossipSub): Future addition when go-libp2p-pubsub supports
//     go-libp2p >= v0.47.0. Will feed into the same handleAnnouncement entry
//     point. Layer 2 can be disabled per-node when Layer 3 is active.
//
// All three layers share the same message format (NodeAnnouncement), the same
// in-memory cache, and the same PeerFilter callback. The transport layer is
// fully decoupled from the intelligence layer.
//
// # Scaling Characteristics
//
// Direct push overhead scales with connected peer count, not total network size:
//
//	20 peers  x 150 bytes = 3 KB per 5min tick
//	200 peers x 150 bytes = 30 KB per 5min tick
//	500 peers x 150 bytes = 75 KB per 5min tick
//
// A single node's connection count is bounded by the DHT routing table (~200)
// plus the authorized peer set. Gossip forwarding adds at most
// gossipFanout messages per received announcement, bounded by maxHops.
package p2pnet

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// ---------------------------------------------------------------------------
// Presence announcement tuning constants
//
// These control how frequently this node shares its network state with peers
// and how announcements propagate through the network via gossip forwarding.
//
// The direct-push transport (Layer 1) scales to ~500 connected peers with
// negligible overhead (75KB per 5min tick). The gossip forwarding layer
// (Layer 2) extends reach beyond direct connections. For both layers, the
// announcement frequency is the same; the transport is the scaling variable.
//
// To tune in code: adjust the constants below and rebuild.
// To make configurable: AnnounceInterval is already in DiscoveryConfig
// (discovery.announce_interval in YAML). Add TTL, fanout, and maxHops to
// config when needed. See internal/config/config.go DiscoveryConfig.
// ---------------------------------------------------------------------------

const (
	// presenceProtocol is the libp2p protocol ID for presence announcements.
	// Both direct push (Layer 1) and gossip forwarding (Layer 2) use this
	// same protocol. The Hops field distinguishes direct vs forwarded.
	presenceProtocol = "/shurli/presence/1.0.0"

	// announceInterval is how often this node pushes its own state to
	// connected peers. 5 minutes balances freshness with overhead.
	//
	// Scaling math (Layer 1 direct push):
	//   20 connected peers  x 150 bytes = 3 KB per tick
	//   200 connected peers x 150 bytes = 30 KB per tick
	//   500 connected peers x 150 bytes = 75 KB per tick
	//
	// At any practical connection count, this is negligible. The scaling
	// constraint is message propagation (reach), not bandwidth. Gossip
	// forwarding (Layer 2) and GossipSub (Layer 3) address reach.
	defaultAnnounceInterval = 5 * time.Minute

	// announceTTL is how long a cached peer announcement remains valid.
	// Set to 2x announceInterval so a single missed announcement doesn't
	// evict the peer's state. After 10 minutes with no update, the peer's
	// state is considered stale and evicted from cache.
	announceTTL = 10 * time.Minute

	// maxAnnouncementSize caps the JSON payload to prevent abuse.
	// Current announcements are ~150 bytes; 4KB is generous headroom
	// for future fields while preventing resource exhaustion.
	maxAnnouncementSize = 4096

	// gossipFanout is how many random peers receive a forwarded announcement.
	// Combined with maxHops, this controls propagation reach:
	//   fanout=3, maxHops=3: ~80 unique peers per originator (with overlap)
	//   fanout=5, maxHops=3: ~300+ unique peers per originator
	//
	// 3 is conservative and appropriate for networks up to ~1K nodes.
	// For larger networks, increase fanout or add GossipSub (Layer 3).
	//
	// To make configurable: add GossipFanout to DiscoveryConfig.
	gossipFanout = 3

	// maxHops caps how many times an announcement can be forwarded.
	// Prevents infinite propagation. 3 hops means an announcement can
	// reach peers up to 4 edges away from the originator (1 direct + 3
	// forwarded). Total network-wide copies per announcement: at most
	// fanout^maxHops (27 at current settings), spread across the mesh.
	//
	// To make configurable: add MaxHops to DiscoveryConfig.
	maxHops = 3

	// streamTimeout is the per-stream deadline for sending/receiving
	// a single announcement. Announcements are tiny (~150 bytes) so
	// this is generous. Prevents hung streams from blocking goroutines.
	streamTimeout = 10 * time.Second
)

// NodeAnnouncement is the presence message exchanged between peers.
// ~150 bytes JSON. Contains ONLY aggregate capabilities, no private data
// (no IP addresses, no peer IDs of connected peers, no hostnames).
//
// Wire format versioning:
//
//	v1 (current): JSON encoding via direct-push + gossip forwarding
//	v2 (future):  Protobuf encoding when gossip volume warrants it
//
// Transport independence: this struct is encoded/decoded identically
// regardless of whether it arrives via direct stream, gossip forwarding,
// or GossipSub message.
type NodeAnnouncement struct {
	Version    int    `json:"v"`          // Protocol version (1)
	From       string `json:"from"`       // Originator peer ID (set by sender, preserved on forward)
	Grade      string `json:"grade"`      // Reachability grade: A/B/C/D/F
	NATType    string `json:"nat_type"`   // full-cone, address-restricted, port-restricted, symmetric, unknown
	HasIPv4    bool   `json:"ipv4"`       // Has global unicast IPv4
	HasIPv6    bool   `json:"ipv6"`       // Has global unicast IPv6
	BehindCGNAT bool  `json:"cgnat"`     // Behind carrier-grade NAT (RFC 6598)
	UptimeSec  int64  `json:"uptime_sec"` // Seconds since daemon start
	PeerCount  int    `json:"peer_count"` // Number of connected peers
	Timestamp  int64  `json:"ts"`         // Unix timestamp of announcement
	Hops       int    `json:"hops"`       // 0 = direct from originator, incremented on each forward
}

// PeerFilter decides whether a peer should receive announcements and
// whether incoming announcements from a peer should be cached.
//
// Current wiring (serve_common.go): gater.IsAuthorized() for the
// authorized-peers phase. When public network mode lands, this callback
// gets a second branch for open-network peers. No change to NetIntel.
type PeerFilter func(peer.ID) bool

// NodeStateProvider builds the current NodeAnnouncement from runtime state.
// Called on every publish tick and on-demand via AnnounceNow().
//
// Wired in serve_common.go to a closure that reads rt.ifSummary,
// rt.stunProber.Result(), and rt.startTime. This breaks the import
// boundary between pkg/p2pnet and the runtime structs.
type NodeStateProvider func() *NodeAnnouncement

// PeerAnnouncement is a cached announcement from a remote peer.
type PeerAnnouncement struct {
	PeerID       peer.ID
	Announcement NodeAnnouncement
	ReceivedAt   time.Time
}

// NetIntel manages the presence announcement protocol.
//
// It is transport-agnostic: the cache and query methods work identically
// regardless of how announcements arrive. Currently Layer 1 (direct push)
// and Layer 2 (gossip forwarding) are built-in. Layer 3 (GossipSub) can
// be added as an additional transport by calling handleAnnouncement()
// from a GossipSub subscription handler.
type NetIntel struct {
	host          host.Host
	metrics       *Metrics         // nil-safe
	peerFilter    PeerFilter       // nil = accept all
	stateProvider NodeStateProvider
	interval      time.Duration // announce interval (from config or default)

	mu    sync.RWMutex
	cache map[peer.ID]*PeerAnnouncement

	announceCh chan struct{} // triggers immediate re-announce

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewNetIntel creates a NetIntel instance. The peerFilter and stateProvider
// callbacks are required. Metrics is optional (nil-safe). The interval
// parameter overrides the default announce interval (0 = use default).
func NewNetIntel(h host.Host, m *Metrics, pf PeerFilter, sp NodeStateProvider, interval time.Duration) *NetIntel {
	if interval <= 0 {
		interval = defaultAnnounceInterval
	}
	return &NetIntel{
		host:          h,
		metrics:       m,
		peerFilter:    pf,
		stateProvider: sp,
		interval:      interval,
		cache:         make(map[peer.ID]*PeerAnnouncement),
		announceCh:    make(chan struct{}, 1),
	}
}

// Start registers the stream handler and spawns background goroutines
// for publishing, cleanup, and gossip forwarding.
func (ni *NetIntel) Start(ctx context.Context) {
	ni.ctx, ni.cancel = context.WithCancel(ctx)

	ni.host.SetStreamHandler(protocol.ID(presenceProtocol), ni.streamHandler)

	ni.wg.Add(2)
	go ni.publishLoop()
	go ni.cleanupLoop()

	slog.Info("netintel: started", "interval", ni.interval, "fanout", gossipFanout, "max_hops", maxHops)
}

// Close stops all background goroutines and removes the stream handler.
func (ni *NetIntel) Close() {
	ni.host.RemoveStreamHandler(protocol.ID(presenceProtocol))
	ni.cancel()
	ni.wg.Wait()
}

// AnnounceNow triggers an immediate re-announcement to all connected peers.
// Non-blocking: if a publish is already pending, this is a no-op.
// Called by serve_common.go on network change events.
func (ni *NetIntel) AnnounceNow() {
	select {
	case ni.announceCh <- struct{}{}:
	default:
	}
}

// GetPeerState returns a copy of the cached announcement for a single peer,
// or nil if not found or expired.
func (ni *NetIntel) GetPeerState(pid peer.ID) *PeerAnnouncement {
	ni.mu.RLock()
	defer ni.mu.RUnlock()
	pa, ok := ni.cache[pid]
	if !ok {
		return nil
	}
	copy := *pa
	return &copy
}

// GetAllPeerState returns a snapshot of all cached peer announcements.
func (ni *NetIntel) GetAllPeerState() []PeerAnnouncement {
	ni.mu.RLock()
	defer ni.mu.RUnlock()
	result := make([]PeerAnnouncement, 0, len(ni.cache))
	for _, pa := range ni.cache {
		result = append(result, *pa)
	}
	return result
}

// streamHandler processes an incoming presence announcement.
func (ni *NetIntel) streamHandler(s network.Stream) {
	defer s.Close()

	sender := s.Conn().RemotePeer()

	// Read announcement with size limit.
	data, err := io.ReadAll(io.LimitReader(s, maxAnnouncementSize))
	if err != nil {
		ni.incRecvMetric("invalid")
		return
	}

	var ann NodeAnnouncement
	if err := json.Unmarshal(data, &ann); err != nil {
		ni.incRecvMetric("invalid")
		slog.Debug("netintel: invalid announcement", "peer", shortID(sender), "error", err)
		return
	}

	// Validate version.
	if ann.Version != 1 {
		ni.incRecvMetric("invalid")
		slog.Debug("netintel: unsupported version", "peer", shortID(sender), "version", ann.Version)
		return
	}

	// Determine the originator. For direct announcements (Hops==0), the
	// sender IS the originator. For forwarded, use the From field.
	var originator peer.ID
	if ann.Hops == 0 {
		originator = sender
		ann.From = sender.String()
	} else {
		originator, err = peer.Decode(ann.From)
		if err != nil {
			ni.incRecvMetric("invalid")
			slog.Debug("netintel: invalid originator", "from", ann.From, "error", err)
			return
		}
	}

	// Don't cache our own announcements (can happen via gossip forwarding).
	if originator == ni.host.ID() {
		return
	}

	// Apply peer filter to the originator.
	if ni.peerFilter != nil && !ni.peerFilter(originator) {
		ni.incRecvMetric("rejected")
		return
	}

	// Check if this is newer than what we have cached.
	ni.mu.Lock()
	existing, exists := ni.cache[originator]
	isNew := !exists || ann.Timestamp > existing.Announcement.Timestamp
	if isNew {
		ni.cache[originator] = &PeerAnnouncement{
			PeerID:       originator,
			Announcement: ann,
			ReceivedAt:   time.Now(),
		}
	}
	ni.mu.Unlock()

	if !isNew {
		return // duplicate or stale, already have newer data
	}

	ni.incRecvMetric("accepted")

	// Layer 2: Gossip forwarding. If announcement has room to travel
	// further, forward to random peers.
	if ann.Hops < maxHops {
		ni.forwardToRandomPeers(ann, sender, originator)
	}
}

// publishLoop periodically pushes this node's announcement to all connected
// peers that pass the PeerFilter. Also responds to AnnounceNow() triggers.
func (ni *NetIntel) publishLoop() {
	defer ni.wg.Done()

	ticker := time.NewTicker(ni.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ni.ctx.Done():
			return
		case <-ticker.C:
			ni.publishToAll()
		case <-ni.announceCh:
			ni.publishToAll()
		}
	}
}

// publishToAll sends this node's announcement to every connected peer
// that passes the PeerFilter.
func (ni *NetIntel) publishToAll() {
	ann := ni.stateProvider()
	if ann == nil {
		return
	}
	ann.From = ni.host.ID().String()
	ann.Hops = 0

	data, err := json.Marshal(ann)
	if err != nil {
		slog.Warn("netintel: marshal failed", "error", err)
		return
	}

	peers := ni.host.Network().Peers()
	for _, pid := range peers {
		if pid == ni.host.ID() {
			continue
		}
		if ni.peerFilter != nil && !ni.peerFilter(pid) {
			continue
		}
		go ni.sendToPeer(pid, data)
	}
}

// sendToPeer opens a stream to the target peer and writes the announcement.
func (ni *NetIntel) sendToPeer(pid peer.ID, data []byte) {
	ctx, cancel := context.WithTimeout(ni.ctx, streamTimeout)
	defer cancel()

	s, err := ni.host.NewStream(ctx, pid, protocol.ID(presenceProtocol))
	if err != nil {
		ni.incSendMetric("error")
		return
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(streamTimeout))
	if _, err := s.Write(data); err != nil {
		ni.incSendMetric("error")
		return
	}

	ni.incSendMetric("success")
}

// forwardToRandomPeers picks up to gossipFanout random connected peers
// (excluding the sender and originator) and forwards the announcement
// with an incremented hop counter.
//
// This is Layer 2 gossip forwarding. It extends announcement reach beyond
// direct connections. Combined with maxHops, total copies per announcement
// is bounded by fanout^maxHops (27 at default settings).
func (ni *NetIntel) forwardToRandomPeers(ann NodeAnnouncement, sender, originator peer.ID) {
	ann.Hops++

	data, err := json.Marshal(&ann)
	if err != nil {
		return
	}

	// Build candidate list: connected peers minus sender and originator.
	allPeers := ni.host.Network().Peers()
	candidates := make([]peer.ID, 0, len(allPeers))
	for _, pid := range allPeers {
		if pid == sender || pid == originator || pid == ni.host.ID() {
			continue
		}
		if ni.peerFilter != nil && !ni.peerFilter(pid) {
			continue
		}
		candidates = append(candidates, pid)
	}

	if len(candidates) == 0 {
		return
	}

	// Shuffle and pick up to gossipFanout peers.
	rand.Shuffle(len(candidates), func(i, j int) {
		candidates[i], candidates[j] = candidates[j], candidates[i]
	})
	n := gossipFanout
	if n > len(candidates) {
		n = len(candidates)
	}

	for _, pid := range candidates[:n] {
		go ni.sendToPeer(pid, data)
	}

	ni.incRecvMetric("forwarded")
}

// cleanupLoop periodically evicts stale cache entries.
func (ni *NetIntel) cleanupLoop() {
	defer ni.wg.Done()

	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ni.ctx.Done():
			return
		case <-ticker.C:
			ni.evictStale()
		}
	}
}

// evictStale removes cache entries older than announceTTL.
func (ni *NetIntel) evictStale() {
	ni.mu.Lock()
	defer ni.mu.Unlock()

	cutoff := time.Now().Add(-announceTTL)
	for pid, pa := range ni.cache {
		if pa.ReceivedAt.Before(cutoff) {
			delete(ni.cache, pid)
		}
	}
}

// incSendMetric increments the sent counter if metrics are available.
func (ni *NetIntel) incSendMetric(result string) {
	if ni.metrics != nil && ni.metrics.NetIntelSentTotal != nil {
		ni.metrics.NetIntelSentTotal.WithLabelValues(result).Inc()
	}
}

// incRecvMetric increments the received counter if metrics are available.
func (ni *NetIntel) incRecvMetric(result string) {
	if ni.metrics != nil && ni.metrics.NetIntelReceivedTotal != nil {
		ni.metrics.NetIntelReceivedTotal.WithLabelValues(result).Inc()
	}
}

// shortID returns a truncated peer ID for logging.
func shortID(pid peer.ID) string {
	s := pid.String()
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}
