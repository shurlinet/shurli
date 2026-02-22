package p2pnet

import (
	"context"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// PeerPathInfo describes the current path to a connected peer.
type PeerPathInfo struct {
	PeerID      string   `json:"peer_id"`
	PathType    PathType `json:"path_type"`    // DIRECT or RELAYED
	Address     string   `json:"address"`      // current multiaddr
	ConnectedAt string   `json:"connected_at"` // RFC 3339
	Transport   string   `json:"transport"`    // quic, tcp, websocket
	IPVersion   string   `json:"ip_version"`   // ipv4, ipv6
	LastRTTMs   float64  `json:"last_rtt_ms,omitempty"`
}

// PathTracker monitors peer connections via the libp2p event bus and
// maintains per-peer path information (type, transport, IP version).
type PathTracker struct {
	host    host.Host
	metrics *Metrics // nil-safe

	mu    sync.RWMutex
	peers map[peer.ID]*peerPathEntry
}

// peerPathEntry is the internal state for a tracked peer.
type peerPathEntry struct {
	pathType    PathType
	address     string
	connectedAt time.Time
	transport   string
	ipVersion   string
	lastRTTMs   float64
}

// NewPathTracker creates a PathTracker. Metrics is optional (nil-safe).
func NewPathTracker(h host.Host, m *Metrics) *PathTracker {
	return &PathTracker{
		host:    h,
		metrics: m,
		peers:   make(map[peer.ID]*peerPathEntry),
	}
}

// Start subscribes to peer connectedness events and processes them
// until the context is cancelled. Call this in a goroutine.
func (pt *PathTracker) Start(ctx context.Context) {
	sub, err := pt.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		return
	}
	defer sub.Close()

	// Snapshot currently connected peers
	pt.snapshotExisting()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub.Out():
			if !ok {
				return
			}
			e := evt.(event.EvtPeerConnectednessChanged)
			switch e.Connectedness {
			case network.Connected:
				pt.onConnect(e.Peer)
			case network.NotConnected:
				pt.onDisconnect(e.Peer)
			}
		}
	}
}

// snapshotExisting captures peers that are already connected when the
// tracker starts (e.g. relay, bootstrap peers connected during Bootstrap).
func (pt *PathTracker) snapshotExisting() {
	peerIDs := pt.host.Network().Peers()
	for _, pid := range peerIDs {
		pt.onConnect(pid)
	}
}

// onConnect classifies and records a new peer connection.
func (pt *PathTracker) onConnect(pid peer.ID) {
	conns := pt.host.Network().ConnsToPeer(pid)
	if len(conns) == 0 {
		return
	}

	addr := conns[0].RemoteMultiaddr().String()
	pathType, transport, ipVersion := ClassifyMultiaddr(addr)

	// Prefer non-relay connections for classification
	for _, conn := range conns {
		if !conn.Stat().Limited {
			addr = conn.RemoteMultiaddr().String()
			pathType, transport, ipVersion = ClassifyMultiaddr(addr)
			break
		}
	}

	pt.mu.Lock()
	pt.peers[pid] = &peerPathEntry{
		pathType:    pathType,
		address:     addr,
		connectedAt: time.Now(),
		transport:   transport,
		ipVersion:   ipVersion,
	}
	pt.mu.Unlock()

	pt.updateMetrics()
}

// onDisconnect removes a peer from tracking.
func (pt *PathTracker) onDisconnect(pid peer.ID) {
	pt.mu.Lock()
	delete(pt.peers, pid)
	pt.mu.Unlock()

	pt.updateMetrics()
}

// UpdateRTT records the latest round-trip time for a peer.
func (pt *PathTracker) UpdateRTT(pid peer.ID, rttMs float64) {
	pt.mu.Lock()
	if entry, ok := pt.peers[pid]; ok {
		entry.lastRTTMs = rttMs
	}
	pt.mu.Unlock()
}

// GetPeerPath returns path info for a specific peer.
func (pt *PathTracker) GetPeerPath(pid peer.ID) (*PeerPathInfo, bool) {
	pt.mu.RLock()
	entry, ok := pt.peers[pid]
	if !ok {
		pt.mu.RUnlock()
		return nil, false
	}
	info := entryToInfo(pid, entry)
	pt.mu.RUnlock()
	return info, true
}

// ListPeerPaths returns path info for all tracked peers.
func (pt *PathTracker) ListPeerPaths() []*PeerPathInfo {
	pt.mu.RLock()
	defer pt.mu.RUnlock()

	result := make([]*PeerPathInfo, 0, len(pt.peers))
	for pid, entry := range pt.peers {
		result = append(result, entryToInfo(pid, entry))
	}
	return result
}

func entryToInfo(pid peer.ID, e *peerPathEntry) *PeerPathInfo {
	return &PeerPathInfo{
		PeerID:      pid.String(),
		PathType:    e.pathType,
		Address:     e.address,
		ConnectedAt: e.connectedAt.Format(time.RFC3339),
		Transport:   e.transport,
		IPVersion:   e.ipVersion,
		LastRTTMs:   e.lastRTTMs,
	}
}

// updateMetrics recalculates the connected peers gauge from current state.
func (pt *PathTracker) updateMetrics() {
	if pt.metrics == nil || pt.metrics.ConnectedPeers == nil {
		return
	}

	// Reset all labels and recount
	pt.metrics.ConnectedPeers.Reset()

	pt.mu.RLock()
	counts := make(map[[3]string]int) // [path_type, transport, ip_version] -> count
	for _, entry := range pt.peers {
		key := [3]string{string(entry.pathType), entry.transport, entry.ipVersion}
		counts[key]++
	}
	pt.mu.RUnlock()

	for key, count := range counts {
		pt.metrics.ConnectedPeers.WithLabelValues(key[0], key[1], key[2]).Set(float64(count))
	}
}
