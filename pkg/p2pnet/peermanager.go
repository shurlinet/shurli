package p2pnet

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// ---------------------------------------------------------------------------
// Reconnection tuning constants
//
// These control how aggressively PeerManager reconnects to disconnected peers.
// They are hard-coded for the current authorized-peers phase (small networks,
// 5-20 peers). For larger or open networks, these should be moved to config.
//
// To tune in code: adjust the constants below and rebuild.
// To make configurable: add fields to DiscoveryConfig in internal/config,
// pass them to NewPeerManager, and use them in place of these constants.
// ---------------------------------------------------------------------------

const (
	// reconnectInterval is how often the reconnect loop checks for
	// disconnected peers. 30 seconds balances responsiveness with
	// network overhead. Lower values reconnect faster but generate
	// more dial attempts; higher values save bandwidth but leave
	// peers disconnected longer.
	reconnectInterval = 30 * time.Second

	// reconnectDialTimeout is the per-peer dial timeout for each
	// reconnection attempt. PathDialer races DHT vs relay within
	// this window.
	reconnectDialTimeout = 30 * time.Second

	// backoffBase is the starting backoff duration after the first
	// failure. Each subsequent failure doubles it (exponential backoff).
	// 30 seconds means: first retry at next tick, second at ~60s, etc.
	backoffBase = 30 * time.Second

	// backoffMax caps the exponential backoff. After 5 consecutive
	// failures, backoff stops growing and stays at this ceiling.
	// 15 minutes prevents abandoned peers from consuming resources
	// while still retrying periodically.
	backoffMax = 15 * time.Minute

	// maxConcurrentDials limits simultaneous reconnection attempts.
	// Prevents flooding the network when many peers disconnect at once
	// (e.g., after a network outage). 3 is conservative; increase if
	// the node has many authorized peers and good bandwidth.
	maxConcurrentDials = 3
)

// ConnectionRecorder is called on each successful reconnection with
// the peer ID, path type ("DIRECT"/"RELAYED"), and latency in ms.
// This callback bridges the pkg/p2pnet -> internal/reputation boundary:
// serve_common.go wires it to PeerHistory.RecordConnection().
type ConnectionRecorder func(peerID, pathType string, latencyMs float64)

// ManagedPeer tracks the lifecycle state of a single watched peer.
type ManagedPeer struct {
	ID              peer.ID
	Connected       bool
	LastSeen        time.Time
	LastDialAttempt time.Time
	LastDialError   string
	ConsecFailures  int       // consecutive failures (resets on success or network change)
	BackoffUntil    time.Time // don't retry before this time
}

// ManagedPeerInfo is a read-only snapshot for the daemon API and status display.
type ManagedPeerInfo struct {
	PeerID         string `json:"peer_id"`
	Connected      bool   `json:"connected"`
	LastSeen       string `json:"last_seen,omitempty"`
	LastDialError  string `json:"last_dial_error,omitempty"`
	ConsecFailures int    `json:"consec_failures"`
	BackoffUntil   string `json:"backoff_until,omitempty"`
}

// PeerManager maintains connections to watched peers using background
// reconnection with exponential backoff. It subscribes to the libp2p
// event bus for connect/disconnect events and delegates actual dialing
// to PathDialer (which races DHT vs relay paths).
//
// The watchlist is populated from authorized_keys via the gater.
// Only watched peers are reconnected; other connected peers (relay,
// bootstrap, DHT) are ignored.
type PeerManager struct {
	host        host.Host
	pathDialer  *PathDialer
	metrics     *Metrics
	onReconnect ConnectionRecorder // nil-safe

	mu    sync.RWMutex
	peers map[peer.ID]*ManagedPeer

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPeerManager creates a PeerManager. The onReconnect callback is
// optional (nil-safe) and fires on each successful background reconnection.
func NewPeerManager(h host.Host, pd *PathDialer, m *Metrics, onReconnect ConnectionRecorder) *PeerManager {
	return &PeerManager{
		host:        h,
		pathDialer:  pd,
		metrics:     m,
		onReconnect: onReconnect,
		peers:       make(map[peer.ID]*ManagedPeer),
	}
}

// Start begins the event listener and reconnection loop.
// Call SetWatchlist before Start to populate the peer list.
func (pm *PeerManager) Start(ctx context.Context) {
	pm.ctx, pm.cancel = context.WithCancel(ctx)
	pm.snapshotExisting()

	pm.wg.Add(2)
	go pm.eventLoop()
	go pm.reconnectLoop()

	slog.Info("peermanager: started", "watched", len(pm.peers))
}

// Close stops all background goroutines and waits for them to finish.
func (pm *PeerManager) Close() {
	pm.cancel()
	pm.wg.Wait()
}

// SetWatchlist updates which peers PeerManager should maintain connections to.
// Typically called with gater.GetAuthorizedPeerIDs(). Peers removed from the
// watchlist are no longer tracked; new peers are checked for current connectedness.
func (pm *PeerManager) SetWatchlist(peerIDs []peer.ID) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	newSet := make(map[peer.ID]struct{}, len(peerIDs))
	for _, pid := range peerIDs {
		newSet[pid] = struct{}{}
	}

	// Remove peers no longer in the watchlist.
	for pid := range pm.peers {
		if _, ok := newSet[pid]; !ok {
			delete(pm.peers, pid)
		}
	}

	// Add new peers, checking current connection state.
	for _, pid := range peerIDs {
		if pid == pm.host.ID() {
			continue // never watch self
		}
		if _, exists := pm.peers[pid]; !exists {
			connected := pm.host.Network().Connectedness(pid) == network.Connected
			mp := &ManagedPeer{
				ID:        pid,
				Connected: connected,
			}
			if connected {
				mp.LastSeen = time.Now()
			}
			pm.peers[pid] = mp
		}
	}

	slog.Info("peermanager: watchlist updated", "watched", len(pm.peers))
}

// OnNetworkChange resets all backoff timers, triggering immediate
// reconnection attempts on the next loop tick. Called when NetworkMonitor
// detects interface changes (new IP, lost IP, WiFi switch, etc.).
func (pm *PeerManager) OnNetworkChange() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, mp := range pm.peers {
		mp.BackoffUntil = time.Time{}
		mp.ConsecFailures = 0
	}
	slog.Info("peermanager: backoffs reset (network change)")
}

// GetManagedPeers returns a snapshot of all watched peers and their state.
func (pm *PeerManager) GetManagedPeers() []ManagedPeerInfo {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	result := make([]ManagedPeerInfo, 0, len(pm.peers))
	for _, mp := range pm.peers {
		info := ManagedPeerInfo{
			PeerID:         mp.ID.String(),
			Connected:      mp.Connected,
			ConsecFailures: mp.ConsecFailures,
		}
		if !mp.LastSeen.IsZero() {
			info.LastSeen = mp.LastSeen.Format(time.RFC3339)
		}
		if mp.LastDialError != "" {
			info.LastDialError = mp.LastDialError
		}
		if !mp.BackoffUntil.IsZero() && mp.BackoffUntil.After(time.Now()) {
			info.BackoffUntil = mp.BackoffUntil.Format(time.RFC3339)
		}
		result = append(result, info)
	}
	return result
}

// snapshotExisting detects peers that are already connected when PeerManager
// starts (e.g., relay or mDNS connections established during bootstrap).
func (pm *PeerManager) snapshotExisting() {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for pid, mp := range pm.peers {
		if pm.host.Network().Connectedness(pid) == network.Connected {
			mp.Connected = true
			mp.LastSeen = time.Now()
		}
	}
}

// eventLoop subscribes to libp2p connect/disconnect events and updates
// ManagedPeer state accordingly. Only watched peers are tracked.
func (pm *PeerManager) eventLoop() {
	defer pm.wg.Done()

	sub, err := pm.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		slog.Error("peermanager: event bus subscribe failed", "error", err)
		return
	}
	defer sub.Close()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case evt, ok := <-sub.Out():
			if !ok {
				return
			}
			e := evt.(event.EvtPeerConnectednessChanged)
			pm.mu.Lock()
			mp, watched := pm.peers[e.Peer]
			if watched {
				switch e.Connectedness {
				case network.Connected:
					mp.Connected = true
					mp.LastSeen = time.Now()
					mp.ConsecFailures = 0
					mp.BackoffUntil = time.Time{}
					mp.LastDialError = ""
				case network.NotConnected:
					mp.Connected = false
				}
			}
			pm.mu.Unlock()
		}
	}
}

// reconnectLoop periodically dials disconnected watched peers with
// exponential backoff. See package-level constants for tuning parameters.
func (pm *PeerManager) reconnectLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(reconnectInterval)
	defer ticker.Stop()

	sem := make(chan struct{}, maxConcurrentDials)

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.runReconnectCycle(sem)
		}
	}
}

// runReconnectCycle checks all watched peers and dials any that are
// disconnected and past their backoff window.
func (pm *PeerManager) runReconnectCycle(sem chan struct{}) {
	pm.mu.RLock()
	now := time.Now()
	var targets []peer.ID
	for pid, mp := range pm.peers {
		if pid == pm.host.ID() {
			continue
		}
		if mp.Connected {
			continue
		}
		if now.Before(mp.BackoffUntil) {
			pm.incMetric("backoff_skip")
			continue
		}
		targets = append(targets, pid)
	}
	pm.mu.RUnlock()

	for _, pid := range targets {
		select {
		case sem <- struct{}{}:
		default:
			continue // all dial slots busy, skip remaining
		}
		pm.wg.Add(1)
		go func(target peer.ID) {
			defer pm.wg.Done()
			defer func() { <-sem }()
			pm.attemptReconnect(target)
		}(pid)
	}
}

// attemptReconnect dials a single peer via PathDialer and updates state.
func (pm *PeerManager) attemptReconnect(target peer.ID) {
	short := target.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	pm.mu.Lock()
	mp := pm.peers[target]
	if mp == nil {
		pm.mu.Unlock()
		return
	}
	mp.LastDialAttempt = time.Now()
	pm.mu.Unlock()

	dialCtx, dialCancel := context.WithTimeout(pm.ctx, reconnectDialTimeout)
	defer dialCancel()

	result, err := pm.pathDialer.DialPeer(dialCtx, target)

	pm.mu.Lock()
	defer pm.mu.Unlock()

	if err != nil {
		mp.ConsecFailures++
		mp.LastDialError = err.Error()

		// Exponential backoff: backoffBase * 2^failures, capped at backoffMax.
		backoff := backoffBase * (1 << min(mp.ConsecFailures, 5))
		if backoff > backoffMax {
			backoff = backoffMax
		}
		mp.BackoffUntil = time.Now().Add(backoff)

		pm.incMetric("failure")
		slog.Debug("peermanager: reconnect failed",
			"peer", short,
			"failures", mp.ConsecFailures,
			"backoff", backoff.Round(time.Second))
		return
	}

	mp.Connected = true
	mp.LastSeen = time.Now()
	mp.ConsecFailures = 0
	mp.BackoffUntil = time.Time{}
	mp.LastDialError = ""

	pm.incMetric("success")
	slog.Info("peermanager: reconnected", "peer", short, "path", result.PathType)

	if pm.onReconnect != nil {
		pm.onReconnect(target.String(), string(result.PathType), result.Duration.Seconds()*1000)
	}
}

// incMetric increments PeerManagerReconnectTotal if metrics are available.
func (pm *PeerManager) incMetric(result string) {
	if pm.metrics != nil && pm.metrics.PeerManagerReconnectTotal != nil {
		pm.metrics.PeerManagerReconnectTotal.WithLabelValues(result).Inc()
	}
}
