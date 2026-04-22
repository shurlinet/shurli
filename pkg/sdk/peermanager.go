package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// ---------------------------------------------------------------------------
// Reconnection tuning constants
//
// These control how aggressively PeerManager reconnects to disconnected peers.
// They are hard-coded for the current phase. The watchlist is bounded by
// authorized_keys entries, not by total network size. For open networks,
// these should be moved to config.
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

	// probeTimeout is the TCP handshake timeout for direct path probes.
	// 3 seconds is generous for cross-ISP IPv6 connections.
	probeTimeout = 3 * time.Second

	// probeConnectTimeout is the libp2p reconnection timeout after a
	// successful probe closes the relay connection.
	probeConnectTimeout = 10 * time.Second

	// probeInterval is how often the periodic probe checks for relayed
	// peers that could be upgraded to direct. This catches cases where
	// the local node's network didn't change but the remote peer gained
	// new addresses (e.g., via identify exchange).
	probeInterval = 2 * time.Minute

	// Connection churn detection (#28): prevents exhausting the remote
	// peer's rcmgr via rapid reconnection attempts. A connection that
	// lives shorter than churnThreshold counts as churn. When churnCount
	// reaches churnBackoffThreshold within churnWindow, the peer gets
	// an exponential backoff applied. Resets when a connection survives
	// longer than churnThreshold.
	churnThreshold        = 5 * time.Second  // connections shorter than this = churn
	churnWindow           = 60 * time.Second // sliding window for churn counting
	churnBackoffThreshold = 5                // churn events before backoff kicks in
)

// ConnectionRecorder is called on each successful reconnection with
// the peer ID, path type ("DIRECT"/"RELAYED"), and latency in ms.
// This callback bridges the pkg/sdk -> internal/reputation boundary:
// serve_common.go wires it to PeerHistory.RecordConnection().
type ConnectionRecorder func(peerID, pathType string, latencyMs float64)

// connLogger implements network.Notifee to log every connection close
// event for watched peers. This is the only way to capture the closing
// connection's RemoteMultiaddr and direction before libp2p removes it
// from ConnsToPeer. Used to diagnose why direct connections die unexpectedly
// (flag #1: 13s direct connection death after mDNS upgrade).
// Registered on Start(), deregistered on Close().
type connLogger struct {
	pm *PeerManager
}

func (cl *connLogger) Listen(n network.Network, addr ma.Multiaddr)      {}
func (cl *connLogger) ListenClose(n network.Network, addr ma.Multiaddr) {}
func (cl *connLogger) Connected(n network.Network, c network.Conn) {
	cl.pm.mu.RLock()
	_, watched := cl.pm.peers[c.RemotePeer()]
	cl.pm.mu.RUnlock()
	if !watched {
		return
	}
	dir := "outbound"
	if c.Stat().Direction == network.DirInbound {
		dir = "inbound"
	}
	connType := "direct"
	if c.Stat().Limited {
		connType = "relay"
	}
	short := c.RemotePeer().String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	// Diagnostic: snapshot all existing connections to this peer at open time.
	existingConns := n.ConnsToPeer(c.RemotePeer())
	var existingSummary []string
	for _, ec := range existingConns {
		if ec == c {
			continue
		}
		ecType := "direct"
		if ec.Stat().Limited {
			ecType = "relay"
		}
		ecDir := "out"
		if ec.Stat().Direction == network.DirInbound {
			ecDir = "in"
		}
		age := time.Since(ec.Stat().Opened).Round(time.Millisecond)
		existingSummary = append(existingSummary, fmt.Sprintf("%s/%s/%s/age=%s", ecType, ecDir, ec.RemoteMultiaddr(), age))
	}
	slog.Info("peermanager: connection opened",
		"peer", short,
		"type", connType,
		"direction", dir,
		"remote", c.RemoteMultiaddr(),
		"local", c.LocalMultiaddr(),
		"existing_conns", len(existingConns)-1,
		"existing", existingSummary)

	// When a direct connection arrives (especially inbound from home-node
	// after pathDialer already established relay), clean up the idle relay
	// immediately. Without this, relay lingers until the 2-minute probe
	// cycle catches it. The guard in startRelayCleanup prevents duplicates.
	if !c.Stat().Limited {
		pid := c.RemotePeer()
		for _, existing := range n.ConnsToPeer(pid) {
			if existing.Stat().Limited {
				cl.pm.startRelayCleanup(pid)
				break
			}
		}

		// BUG-MP-4+6+7: Close non-LAN connections when a verified LAN
		// connection exists. Uses mDNS-verified LANRegistry instead of
		// bare IsLANMultiaddr — RFC 1918 addresses alone are unreliable
		// (Starlink CGNAT 10.1.x.x, Docker 172.x.x.x both pass
		// IsLANMultiaddr but traverse the internet or are unreachable).
		reg := cl.pm.lanRegistry
		isNewConnVerifiedLAN := reg.IsVerifiedLAN(pid, c.RemoteMultiaddr())

		if !isNewConnVerifiedLAN && reg.HasVerifiedLANConn(cl.pm.host, pid) {
			// Guard: don't close if peer is path-protected (C4).
			if cl.pm.pathProtector != nil && cl.pm.pathProtector.IsProtected(pid) {
				slog.Debug("pathprotector: keeping non-LAN conn (protected)",
					"peer", short, "remote", c.RemoteMultiaddr())
			} else {
				// Case 1: New non-LAN arrives, verified LAN exists → close new.
				var lanAddr ma.Multiaddr
				for _, existing := range n.ConnsToPeer(pid) {
					if existing != c && !existing.Stat().Limited && reg.IsVerifiedLAN(pid, existing.RemoteMultiaddr()) {
						lanAddr = existing.RemoteMultiaddr()
						break
					}
				}
				slog.Info("peermanager: closing non-LAN conn (verified LAN exists)",
					"peer", short,
					"closing", c.RemoteMultiaddr(),
					"keeping", lanAddr,
					"direction", dir)
				go func() {
					c.Close()
					StripNonLANAddrs(cl.pm.host, pid, cl.pm.lanRegistry)
				}()
				return
			}
		}

		if isNewConnVerifiedLAN {
			// Case 2: New verified LAN arrives → close existing non-verified conns.
			var closedAny bool
			for _, existing := range n.ConnsToPeer(pid) {
				if existing == c || existing.Stat().Limited {
					continue
				}
				if reg.IsVerifiedLAN(pid, existing.RemoteMultiaddr()) {
					continue
				}
				// Guard: don't close if protected (C4) or has active streams.
				if cl.pm.pathProtector != nil && cl.pm.pathProtector.IsProtected(pid) {
					slog.Debug("pathprotector: keeping non-LAN conn (protected)",
						"peer", short, "remote", existing.RemoteMultiaddr())
					continue
				}
				if streams := existing.GetStreams(); len(streams) > 0 {
					slog.Info("peermanager: keeping non-LAN conn (active streams)",
						"peer", short, "remote", existing.RemoteMultiaddr(),
						"streams", len(streams))
					continue
				}
				slog.Info("peermanager: closing non-LAN conn (verified LAN arrived)",
					"peer", short,
					"closing", existing.RemoteMultiaddr(),
					"keeping", c.RemoteMultiaddr())
				go existing.Close()
				closedAny = true
			}
			if closedAny {
				go StripNonLANAddrs(cl.pm.host, pid, cl.pm.lanRegistry)
			}
		}
	}
}
func (cl *connLogger) Disconnected(n network.Network, c network.Conn) {
	cl.pm.mu.RLock()
	_, watched := cl.pm.peers[c.RemotePeer()]
	cl.pm.mu.RUnlock()
	if !watched {
		return
	}

	dir := "outbound"
	if c.Stat().Direction == network.DirInbound {
		dir = "inbound"
	}
	connType := "direct"
	if c.Stat().Limited {
		connType = "relay"
	}
	short := c.RemotePeer().String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	// Diagnostic: connection age and remaining connections at close time.
	age := time.Since(c.Stat().Opened).Round(time.Millisecond)
	remainingConns := n.ConnsToPeer(c.RemotePeer())
	var remainingSummary []string
	for _, rc := range remainingConns {
		if rc == c {
			continue
		}
		rcType := "direct"
		if rc.Stat().Limited {
			rcType = "relay"
		}
		rcDir := "out"
		if rc.Stat().Direction == network.DirInbound {
			rcDir = "in"
		}
		rcAge := time.Since(rc.Stat().Opened).Round(time.Millisecond)
		remainingSummary = append(remainingSummary, fmt.Sprintf("%s/%s/%s/age=%s", rcType, rcDir, rc.RemoteMultiaddr(), rcAge))
	}
	slog.Info("peermanager: connection closed",
		"peer", short,
		"type", connType,
		"direction", dir,
		"remote", c.RemoteMultiaddr(),
		"local", c.LocalMultiaddr(),
		"age", age,
		"remaining_conns", len(remainingSummary),
		"remaining", remainingSummary)

	// Connection churn detection (#28): if the connection lived shorter than
	// churnThreshold, count it. When too many short-lived connections accumulate,
	// apply backoff to prevent exhausting the remote peer's rcmgr.
	if age < churnThreshold {
		pid := c.RemotePeer()
		cl.pm.mu.Lock()
		mp, ok := cl.pm.peers[pid]
		if ok {
			now := time.Now()
			// Reset window if it expired.
			if now.Sub(mp.churnWindowStart) > churnWindow {
				mp.churnCount = 0
				mp.churnWindowStart = now
			}
			mp.churnCount++
			if mp.churnCount >= churnBackoffThreshold {
				// Apply exponential backoff based on churn count.
				backoff := backoffBase * time.Duration(1<<min(mp.churnCount/churnBackoffThreshold, 5))
				if backoff > backoffMax {
					backoff = backoffMax
				}
				mp.BackoffUntil = now.Add(backoff)
				slog.Warn("peermanager: connection churn detected, backing off",
					"peer", short,
					"churn_count", mp.churnCount,
					"backoff", backoff.Round(time.Second))
			}
		}
		cl.pm.mu.Unlock()
	} else {
		// Connection survived > churnThreshold: reset churn counter.
		pid := c.RemotePeer()
		cl.pm.mu.Lock()
		mp, ok := cl.pm.peers[pid]
		if ok && mp.churnCount > 0 {
			mp.churnCount = 0
			mp.churnWindowStart = time.Time{}
		}
		cl.pm.mu.Unlock()
	}
}

// ManagedPeer tracks the lifecycle state of a single watched peer.
type ManagedPeer struct {
	ID              peer.ID
	Connected       bool
	LastSeen        time.Time
	LastDialAttempt time.Time
	LastDialError   string
	ConsecFailures  int       // consecutive failures (resets on success or network change)
	BackoffUntil    time.Time // don't retry before this time
	ProbeUntil      time.Time // probe cooldown: reconnect loop skips this peer until expired

	// Connection churn detection (#28): tracks short-lived connections to prevent
	// exhausting the remote peer's rcmgr via rapid reconnection attempts.
	// A connection that lives <churnThreshold is counted as churn. When churnCount
	// reaches churnBackoffThreshold within churnWindow, the peer is backed off
	// exponentially. Resets when a connection survives >churnThreshold.
	churnCount    int
	churnWindowStart time.Time
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
	connLog     *connLogger        // logs connection close events for watched peers
	lanRegistry *LANRegistry       // mDNS-verified LAN peer/IP tracking

	// TS-5: PathProtector integration.
	pathProtector      *PathProtector           // nil-safe, set via SetPathProtector
	onWatchlistRemoved func(peer.ID)            // callback for deauth cleanup (R7-D1)
	connGracePeriod    time.Duration            // per-connection grace in closeOnce (R8-C1)

	mu    sync.RWMutex
	peers map[peer.ID]*ManagedPeer

	// relayCleanup tracks peers with an active closeRelayConns goroutine.
	// Prevents duplicate 90-second sweep goroutines from stacking up when
	// multiple triggers (connLogger, probeLoop) detect the same condition.
	relayCleanup map[peer.ID]struct{}

	// reconnectNow is a non-blocking trigger that causes the reconnect
	// loop to run a cycle immediately instead of waiting for the next
	// 30-second tick. Used after network changes to avoid stale delays.
	reconnectNow chan struct{}

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewPeerManager creates a PeerManager. The onReconnect callback is
// optional (nil-safe) and fires on each successful background reconnection.
func NewPeerManager(h host.Host, pd *PathDialer, m *Metrics, onReconnect ConnectionRecorder, lanReg *LANRegistry) *PeerManager {
	if lanReg == nil {
		lanReg = NewLANRegistry()
	}
	return &PeerManager{
		host:            h,
		pathDialer:      pd,
		metrics:         m,
		onReconnect:     onReconnect,
		lanRegistry:     lanReg,
		connGracePeriod: DefaultConnGracePeriod,
		peers:           make(map[peer.ID]*ManagedPeer),
		relayCleanup:    make(map[peer.ID]struct{}),
		reconnectNow:    make(chan struct{}, 1),
	}
}

// SetPathProtector sets the path protector for guard checks in cleanup code.
// Called during daemon wiring after both PeerManager and PathProtector are created.
func (pm *PeerManager) SetPathProtector(pp *PathProtector) {
	pm.pathProtector = pp
}

// SetOnWatchlistRemoved registers a callback fired for each peer removed from
// the watchlist. Used by PathProtector for deauth cleanup (R7-D1).
func (pm *PeerManager) SetOnWatchlistRemoved(fn func(peer.ID)) {
	pm.onWatchlistRemoved = fn
}

// OnWatchlistRemovedFunc returns the current watchlist-removed callback (for chaining).
func (pm *PeerManager) OnWatchlistRemovedFunc() func(peer.ID) {
	return pm.onWatchlistRemoved
}

// SetConnGracePeriod overrides the per-connection grace period for closeOnce.
// Used in tests (R9-D2). Production default: DefaultConnGracePeriod (30s).
func (pm *PeerManager) SetConnGracePeriod(d time.Duration) {
	pm.connGracePeriod = d
}

// LANRegistry returns the mDNS-verified LAN registry for use by mDNS
// discovery and the gater's LAN dial filter.
func (pm *PeerManager) GetLANRegistry() *LANRegistry {
	return pm.lanRegistry
}

// Start begins the event listener and reconnection loop.
// Call SetWatchlist before Start to populate the peer list.
func (pm *PeerManager) Start(ctx context.Context) {
	pm.ctx, pm.cancel = context.WithCancel(ctx)
	pm.snapshotExisting()

	pm.connLog = &connLogger{pm: pm}
	pm.host.Network().Notify(pm.connLog)

	pm.wg.Add(4)
	go pm.eventLoop()
	go pm.reconnectLoop()
	go pm.probeLoop()
	go pm.rcmgrMonitorLoop()

	slog.Info("peermanager: started", "watched", len(pm.peers))
}

// Close stops all background goroutines and waits for them to finish.
func (pm *PeerManager) Close() {
	if pm.connLog != nil {
		pm.host.Network().StopNotify(pm.connLog)
	}
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
	// Collect removed peers for deauth callback (R7-C1).
	var removed []peer.ID
	for pid := range pm.peers {
		if _, ok := newSet[pid]; !ok {
			removed = append(removed, pid)
			delete(pm.peers, pid)
		}
	}

	// Fire deauth callback synchronously BEFORE returning (R7-S1).
	// This ensures managed conns are closed before SetWatchlist completes.
	callback := pm.onWatchlistRemoved
	if callback != nil && len(removed) > 0 {
		// Release lock before callback to avoid deadlock.
		pm.mu.Unlock()
		for _, pid := range removed {
			callback(pid)
		}
		pm.mu.Lock()
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

// ResetPeerBackoff clears the backoff state for a single peer, allowing
// the PeerManager's reconnect loop to immediately attempt reconnection.
// Used by ConnectToPeer after a dial failure to give the peer a fresh chance
// (e.g. after relay budget was refilled for the remote peer).
func (pm *PeerManager) ResetPeerBackoff(pid peer.ID) {
	pm.mu.Lock()
	if mp, ok := pm.peers[pid]; ok {
		mp.BackoffUntil = time.Time{}
		mp.ConsecFailures = 0
	}
	pm.mu.Unlock()
}

// OnNetworkChange resets all backoff timers and triggers an immediate
// reconnect cycle. Used by the auth-reload path where deferral is not needed.
// The network change handler in serve_common.go calls ResetBackoffsForNetworkChange
// and TriggerReconnect separately with a grace period between them.
func (pm *PeerManager) OnNetworkChange() {
	pm.ResetBackoffsForNetworkChange()
	pm.TriggerReconnect()
}

// ResetBackoffsForNetworkChange clears backoff state for all watched peers
// without triggering a reconnect cycle. Called immediately on network change
// so that mDNS and other subsystems can dial without hitting stale backoffs.
// The reconnect trigger is deferred separately to give mDNS priority.
func (pm *PeerManager) ResetBackoffsForNetworkChange() {
	pm.mu.Lock()
	for _, mp := range pm.peers {
		mp.BackoffUntil = time.Time{}
		mp.ConsecFailures = 0
		mp.ProbeUntil = time.Time{} // network changed - probe cooldown is stale, don't block reconnect
	}
	pm.mu.Unlock()

	slog.Info("peermanager: backoffs reset (network change)")
}

// TriggerReconnect sends a non-blocking signal to the reconnect loop.
func (pm *PeerManager) TriggerReconnect() {
	select {
	case pm.reconnectNow <- struct{}{}:
	default:
		// Already pending, no need to queue another.
	}
}

// ReconnectPeer clears internal backoff state for a single peer and triggers
// an immediate reconnect cycle. This is the manual escape hatch for AI agents
// and operators to recover a peer from backoff state without waiting for it
// to expire naturally. Returns true if the peer was in the watchlist.
func (pm *PeerManager) ReconnectPeer(pid peer.ID) bool {
	pm.mu.Lock()
	mp, ok := pm.peers[pid]
	if ok {
		mp.BackoffUntil = time.Time{}
		mp.ConsecFailures = 0
		mp.ProbeUntil = time.Time{}
	}
	pm.mu.Unlock()

	if !ok {
		return false
	}

	short := pid.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	slog.Info("peermanager: manual reconnect requested", "peer", short)

	// Trigger immediate reconnect cycle (non-blocking send).
	select {
	case pm.reconnectNow <- struct{}{}:
	default:
	}
	return true
}

// StripPrivateAddrs removes private/LAN addresses from the peerstore for
// all watched peers. Called during network changes BEFORE triggering
// reconnect or mDNS browse. This prevents the swarm dial worker from
// caching stale LAN address failures that poison concurrent direct dials
// (see libp2p-overrides.md section 6: dial worker deduplication race).
// mDNS re-populates LAN addresses from fresh discovery after the strip.
func (pm *PeerManager) StripPrivateAddrs() {
	pm.mu.RLock()
	var peerIDs []peer.ID
	for pid := range pm.peers {
		peerIDs = append(peerIDs, pid)
	}
	pm.mu.RUnlock()

	ps := pm.host.Peerstore()
	stripped := 0
	for _, pid := range peerIDs {
		addrs := ps.Addrs(pid)
		// Count stale addrs first to avoid allocating keep slice when
		// all addrs are public (common case for relay-connected peers).
		staleCount := 0
		for _, a := range addrs {
			if isStaleOnNetworkChange(a) {
				staleCount++
			}
		}
		if staleCount == 0 {
			continue
		}
		keep := make([]ma.Multiaddr, 0, len(addrs)-staleCount)
		for _, a := range addrs {
			if !isStaleOnNetworkChange(a) {
				keep = append(keep, a)
			}
		}
		ps.ClearAddrs(pid)
		if len(keep) > 0 {
			ps.AddAddrs(pid, keep, discoveredAddrTTL)
		}
		stripped += staleCount
	}
	if stripped > 0 {
		slog.Debug("peermanager: stripped private addrs from peerstore",
			"count", stripped, "peers", len(peerIDs))
	}
}

// CloseAllPeerConnections closes ALL connections (direct AND relay circuits)
// to watched peers. Called during network changes to eliminate zombie
// connections of every type.
//
// After a WiFi switch, BOTH direct QUIC connections and relay circuits become
// zombies. Direct connections die because the local interface changed. Relay
// circuits die because the underlying transport to the relay server was on
// the old interface — the circuit stream errors but libp2p may not detect
// it immediately (IsClosed returns false until the next read/write).
//
// CloseStaleConnections' IP matching misses both classes:
//   - Direct zombies when returning to the same network (IP comes back)
//   - Relay circuit zombies (local IP is the relay server's, not the old interface)
//
// Closing everything is safe: the network change handler reconnects to relays
// immediately (step 9), mDNS re-establishes LAN connections in 1-2s, and
// PeerManager's deferred reconnect handles remaining peers via DHT/relay.
func (pm *PeerManager) CloseAllPeerConnections() {
	pm.mu.RLock()
	watchedPeers := make([]peer.ID, 0, len(pm.peers))
	for pid := range pm.peers {
		watchedPeers = append(watchedPeers, pid)
	}
	pm.mu.RUnlock()

	var closed int
	for _, pid := range watchedPeers {
		for _, c := range pm.host.Network().ConnsToPeer(pid) {
			c.Close()
			closed++
		}
	}
	if closed > 0 {
		slog.Info("peermanager: closed all peer connections (network change)", "count", closed)
	}
}

// CloseStaleConnections closes connections to watched peers whose local
// address is no longer present on any active interface. When a network
// interface disappears (WiFi switch, USB LAN unplug), connections bound
// to that interface are dead but libp2p may not detect it for minutes
// (TCP keepalive timeout). Closing them immediately lets the reconnect
// loop redial via the new active interface.
//
// IPv6 addresses go through DAD (Duplicate Address Detection) after a
// network change. During DAD (100-500ms), the address is "tentative"
// and invisible to net.Interfaces(). Connections using those addresses
// are still valid but would be killed by a naive "not in current IPs"
// check. To avoid this, IPv6 connections that are not explicitly in
// the removedIPs set are given the benefit of the doubt (TCP keepalive
// will catch truly dead ones). IPv4 has no DAD delay, so missing IPv4
// addresses mean the interface is truly gone.
func (pm *PeerManager) CloseStaleConnections(removedIPs []string) {
	// Build explicit removal set from NetworkMonitor signal.
	removed := make(map[string]struct{}, len(removedIPs))
	for _, ip := range removedIPs {
		removed[ip] = struct{}{}
	}

	// Build a set of ALL IPs currently on active interfaces (private
	// and global). We read directly from net.Interfaces rather than
	// InterfaceSummary because the summary only has global IPs.
	currentIPs := make(map[string]struct{})
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
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
				currentIPs[ipNet.IP.String()] = struct{}{}
			}
		}
	}
	// Always include loopback - those connections are never stale.
	currentIPs["127.0.0.1"] = struct{}{}
	currentIPs["::1"] = struct{}{}

	var closed, skippedDAD int
	pm.mu.RLock()
	watchedPeers := make(map[peer.ID]struct{}, len(pm.peers))
	for pid := range pm.peers {
		watchedPeers[pid] = struct{}{}
	}
	pm.mu.RUnlock()

	for pid := range watchedPeers {
		conns := pm.host.Network().ConnsToPeer(pid)
		for _, c := range conns {
			localIP := extractIPFromMultiaddrObj(c.LocalMultiaddr())
			if localIP == "" {
				continue
			}
			// Explicitly removed by NetworkMonitor - close immediately.
			// This check takes priority over currentIPs (NetworkMonitor is authoritative).
			if _, wasRemoved := removed[localIP]; wasRemoved {
				short := pid.String()
				if len(short) > 16 {
					short = short[:16] + "..."
				}
				slog.Info("peermanager: closing stale connection",
					"peer", short,
					"localIP", localIP,
					"remote", c.RemoteMultiaddr())
				c.Close()
				closed++
				continue
			}
			// Still on an active interface - keep.
			if _, current := currentIPs[localIP]; current {
				continue
			}
			// Not visible on any interface and not explicitly removed.
			// IPv6 may be in DAD (tentative) - skip to avoid killing
			// valid connections during network transitions.
			ip := net.ParseIP(localIP)
			if ip != nil && ip.To4() == nil {
				skippedDAD++
				continue
			}
			// IPv4 not on any active interface - interface is gone.
			short := pid.String()
			if len(short) > 16 {
				short = short[:16] + "..."
			}
			slog.Info("peermanager: closing stale connection",
				"peer", short,
				"localIP", localIP,
				"remote", c.RemoteMultiaddr())
			c.Close()
			closed++
		}
	}

	if skippedDAD > 0 {
		slog.Debug("peermanager: skipped IPv6 connections during possible DAD",
			"count", skippedDAD)
	}
	if closed > 0 {
		slog.Info("peermanager: closed stale connections", "count", closed)
		pm.incMetric("stale_close")
	}
}

// extractIPFromMultiaddrObj extracts the IP address string from a
// multiaddr. Returns "" if no IP component is found.
func extractIPFromMultiaddrObj(addr ma.Multiaddr) string {
	if addr == nil {
		return ""
	}
	var ip string
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP4, ma.P_IP6:
			ip = c.Value()
			return false // stop after first IP
		}
		return true
	})
	return ip
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
					if !mp.ProbeUntil.IsZero() && time.Now().Before(mp.ProbeUntil) {
						slog.Info("peermanager: probe-upgraded peer disconnected, clearing cooldown",
							"peer", e.Peer,
							"probeUntil", mp.ProbeUntil.Format("15:04:05"))
					}
					mp.Connected = false
					mp.ProbeUntil = time.Time{} // peer gone - cooldown invalid, reconnect immediately
				}
			}
			pm.mu.Unlock()
		}
	}
}

// reconnectLoop periodically dials disconnected watched peers with
// exponential backoff. See package-level constants for tuning parameters.
// Also responds to immediate triggers via reconnectNow channel (used
// after network changes to avoid waiting for the next 30s tick).
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
		case <-pm.reconnectNow:
			pm.runReconnectCycle(sem)
		}
	}
}

// probeLoop periodically runs ProbeAndUpgradeRelayed to detect direct
// paths that become available without a local network change. For example,
// the remote peer may have gained new addresses via identify exchange,
// or the remote side may be able to dial us directly even when we can't
// dial them (e.g., macOS utun routing blocks our outgoing IPv6 but the
// remote has a clean route to our advertised IPv6).
func (pm *PeerManager) probeLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(probeInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Error("peermanager: probeLoop recovered from panic",
							"panic", r)
					}
				}()
				pm.ProbeAndUpgradeRelayed()
			}()
		}
	}
}

// rcmgrMonitorLoop periodically logs per-peer connection counts and churn state.
// This is diagnostic instrumentation for #28: validates that per-peer rcmgr
// resources stay bounded over long runs (8-15h). Remove after stability is
// confirmed across multiple overnight runs.
func (pm *PeerManager) rcmgrMonitorLoop() {
	defer pm.wg.Done()

	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-ticker.C:
			pm.mu.RLock()
			for pid, mp := range pm.peers {
				short := pid.String()
				if len(short) > 16 {
					short = short[:16] + "..."
				}
				conns := pm.host.Network().ConnsToPeer(pid)
				var directCount, relayCount int
				for _, c := range conns {
					if c.Stat().Limited {
						relayCount++
					} else {
						directCount++
					}
				}
				if len(conns) > 0 || mp.churnCount > 0 {
					slog.Info("rcmgr-monitor: peer state",
						"peer", short,
						"conns_total", len(conns),
						"conns_direct", directCount,
						"conns_relay", relayCount,
						"churn_count", mp.churnCount,
						"connected", mp.Connected,
						"consec_failures", mp.ConsecFailures)
				}
			}
			pm.mu.RUnlock()
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
		// After a successful probe upgrade, skip this peer so the
		// reconnect loop doesn't race back to relay. The direct
		// connection needs time to stabilize via identify exchange.
		if now.Before(mp.ProbeUntil) {
			slog.Debug("peermanager: reconnect skip (probe cooldown)", "peer", pid)
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

	// Clear stale swarm dial backoffs for target (F33-R2-6). Without this,
	// failed relay circuits create per-address backoffs that compound across
	// PM cycles (30s, 60s, 120s... up to 8 min unreachable).
	if sw, ok := pm.host.Network().(*swarm.Swarm); ok {
		sw.Backoff().Clear(target)
	}

	result, err := pm.pathDialer.DialPeer(dialCtx, target)

	pm.mu.Lock()
	if err != nil {
		// If the peer was connected by another mechanism (mDNS) while
		// we were dialing, don't count this as a failure.
		if mp.Connected {
			pm.mu.Unlock()
			slog.Debug("peermanager: reconnect failed but peer already connected",
				"peer", short)
			return
		}

		mp.ConsecFailures++
		mp.LastDialError = err.Error()

		// Exponential backoff: backoffBase * 2^failures, capped at backoffMax.
		// The min(..., 5) guard caps the bit shift to prevent overflow:
		// 1 << 5 = 32, so max = 30s * 32 = 960s, clamped to backoffMax.
		backoff := backoffBase * (1 << min(mp.ConsecFailures, 5))
		if backoff > backoffMax {
			backoff = backoffMax
		}
		mp.BackoffUntil = time.Now().Add(backoff)

		pm.incMetric("failure")
		pm.mu.Unlock()
		slog.Debug("peermanager: reconnect failed",
			"peer", short,
			"failures", mp.ConsecFailures,
			"backoff", backoff.Round(time.Second))
		return
	}

	// If we got a relay connection but a direct connection already
	// exists (established by mDNS while we were dialing), discard
	// the relay. Direct is always preferred over relay.
	if result.PathType == "RELAYED" && hasLiveDirectConnection(pm.host, target) {
		pm.mu.Unlock()
		// Close the relay connection we just established.
		for _, c := range pm.host.Network().ConnsToPeer(target) {
			if c.Stat().Limited {
				c.Close()
			}
		}
		slog.Info("peermanager: discarded relay (direct already active)",
			"peer", short)
		return
	}

	mp.Connected = true
	mp.LastSeen = time.Now()
	mp.ConsecFailures = 0
	mp.BackoffUntil = time.Time{}
	mp.LastDialError = ""

	pm.incMetric("success")
	pm.mu.Unlock()

	slog.Info("peermanager: reconnected", "peer", short, "path", result.PathType)

	// Invoke callback OUTSIDE the lock to prevent potential deadlock
	// if the callback (e.g., PeerHistory.RecordConnection) ever calls
	// back into PeerManager.
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

// closeRelayConns closes relay (Limited) connections to a peer, leaving
// direct connections intact. Runs periodically for the probe cooldown
// duration to catch relay connections re-established by the remote
// peer's reconnect loop.
//
// Guarded by relayCleanup map: only one goroutine runs per peer at a time.
// Call via startRelayCleanup() which handles the guard check.
func (pm *PeerManager) closeRelayConns(pid peer.ID) {
	short := pid.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	defer func() {
		pm.mu.Lock()
		delete(pm.relayCleanup, pid)
		pm.mu.Unlock()
	}()

	// Close immediately, then periodically for 90s.
	// The remote peer's reconnect loop runs every 30s, so we need
	// at least 3 sweeps to cover the window.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()

	closeOnce := func() {
		// Guard: skip cleanup if peer is path-protected (C1, I2).
		// Re-evaluated on every tick — protection can be set after goroutine launch.
		if pm.pathProtector != nil && pm.pathProtector.IsProtected(pid) {
			slog.Debug("pathprotector: relay cleanup blocked (protected)",
				"peer", short)
			return
		}
		for _, c := range pm.host.Network().ConnsToPeer(pid) {
			if c.Stat().Limited {
				// Per-connection grace: skip recently-arrived relays (R8-C1).
				// Covers managed circuits arriving mid-sweep before Protect() is called.
				if pm.connGracePeriod > 0 && time.Since(c.Stat().Opened) < pm.connGracePeriod {
					slog.Info("pathprotector: closeOnce skipped recent relay",
						"peer", short, "age", time.Since(c.Stat().Opened).Round(time.Millisecond))
					continue
				}
				c.Close()
				slog.Info("peermanager: closed relay conn (direct exists)",
					"peer", short,
					"remote", c.RemoteMultiaddr())
			}
		}
	}

	closeOnce() // immediate

	for {
		select {
		case <-pm.ctx.Done():
			return
		case <-timer.C:
			closeOnce() // final sweep
			return
		case <-ticker.C:
			// Only keep closing if we still have a direct connection.
			hasDirectConn := false
			for _, c := range pm.host.Network().ConnsToPeer(pid) {
				if !c.Stat().Limited {
					hasDirectConn = true
					break
				}
			}
			if !hasDirectConn {
				slog.Debug("peermanager: direct conn lost, stopping relay sweep",
					"peer", short)
				return
			}
			closeOnce()
		}
	}
}

// startRelayCleanup launches closeRelayConns for a peer if one isn't
// already running. Returns true if a new goroutine was launched.
func (pm *PeerManager) startRelayCleanup(pid peer.ID) bool {
	// Guard: skip cleanup if peer is path-protected (C2).
	if pm.pathProtector != nil && pm.pathProtector.IsProtected(pid) {
		short := pid.String()
		if len(short) > 16 {
			short = short[:16] + "..."
		}
		slog.Debug("pathprotector: relay cleanup skipped (protected)",
			"peer", short)
		return false
	}

	pm.mu.Lock()
	if _, running := pm.relayCleanup[pid]; running {
		pm.mu.Unlock()
		return false
	}
	pm.relayCleanup[pid] = struct{}{}
	pm.mu.Unlock()

	// I6/R5-I2: Grace period before starting relay cleanup sweep.
	// Gives transfers time to call Protect() before relay is torn down.
	// Tracked by wg to ensure clean shutdown.
	pm.wg.Add(1)
	go func() {
		defer pm.wg.Done()

		// Wait for grace period (cancellable via ctx for clean shutdown).
		if pm.connGracePeriod > 0 {
			timer := time.NewTimer(pm.connGracePeriod)
			select {
			case <-pm.ctx.Done():
				if !timer.Stop() {
					<-timer.C
				}
				pm.mu.Lock()
				delete(pm.relayCleanup, pid)
				pm.mu.Unlock()
				return
			case <-timer.C:
			}

			// Re-check protection after grace period.
			if pm.pathProtector != nil && pm.pathProtector.IsProtected(pid) {
				pm.mu.Lock()
				delete(pm.relayCleanup, pid)
				pm.mu.Unlock()
				return
			}
		}

		pm.closeRelayConns(pid)
	}()
	return true
}

// ProbeAndUpgradeRelayed checks if any watched peers currently connected
// via relay have a direct IPv6 path available through any local interface.
// For each candidate, a raw TCP probe confirms reachability before closing
// the relay connection. Called after network changes to exploit secondary
// interfaces (e.g., USB LAN with public IPv6) that the OS default route
// doesn't prefer but that can reach the peer directly.
//
// RFC 6724 source address selection ensures the probe uses the correct
// interface automatically: if only one interface has global IPv6, the
// kernel routes through it regardless of the default gateway priority.
func (pm *PeerManager) ProbeAndUpgradeRelayed() {
	summary, err := DiscoverInterfaces()
	if err != nil || !summary.HasGlobalIPv6 {
		slog.Debug("peermanager: probe skipped", "err", err,
			"hasIPv6", summary != nil && summary.HasGlobalIPv6)
		return
	}

	// Collect relayed peers. Separate into those with/without IPv6 in peerstore.
	pm.mu.RLock()
	var candidates []peer.ID
	var needDHT []peer.ID
	for pid, mp := range pm.peers {
		if !mp.Connected {
			continue
		}
		conns := pm.host.Network().ConnsToPeer(pid)
		relayed := allConnsRelayed(conns)

		short := pid.String()
		if len(short) > 16 {
			short = short[:16] + "..."
		}

		if !relayed {
			// Peer has a direct connection. Clean up any relay connections
			// that accumulated alongside it — e.g., pathDialer established
			// relay just before mDNS or home-node's inbound direct arrived.
			// probeAndUpgrade normally skips non-relayed peers, so without
			// this, idle relay connections linger indefinitely wasting relay
			// server resources.
			for _, c := range conns {
				if c.Stat().Limited {
					pm.startRelayCleanup(pid)
					break
				}
			}
			slog.Debug("peermanager: probe skip (direct)", "peer", short)
			continue
		}

		addrs := pm.host.Peerstore().Addrs(pid)
		hasV6 := peerHasIPv6(addrs)

		slog.Debug("peermanager: probe candidate",
			"peer", short,
			"allRelayed", true,
			"hasIPv6", hasV6)

		if hasV6 {
			candidates = append(candidates, pid)
		} else {
			// Peerstore lacks IPv6. DHT FindPeer can refresh it.
			needDHT = append(needDHT, pid)
		}
	}
	pm.mu.RUnlock()

	// For relayed peers missing IPv6: ask the DHT for fresh addresses.
	if len(needDHT) > 0 && pm.pathDialer != nil && pm.pathDialer.kdht != nil {
		for _, pid := range needDHT {
			short := pid.String()
			if len(short) > 16 {
				short = short[:16] + "..."
			}
			findCtx, findCancel := context.WithTimeout(pm.ctx, 10*time.Second)
			pi, err := pm.pathDialer.kdht.FindPeer(findCtx, pid)
			findCancel()
			if err != nil {
				slog.Debug("peermanager: DHT FindPeer failed", "peer", short, "error", err)
				continue
			}
			// AddAddrs to peerstore so probeAndUpgrade can use them.
			pm.host.Peerstore().AddAddrs(pid, pi.Addrs, discoveredAddrTTL)

			slog.Debug("peermanager: DHT refreshed addrs", "peer", short,
				"count", len(pi.Addrs))

			if peerHasIPv6(pi.Addrs) {
				candidates = append(candidates, pid)
			}
		}
	}

	for _, pid := range candidates {
		pm.probeAndUpgrade(pid)
	}
}

// probeAndUpgrade attempts a raw TCP handshake to a peer's global IPv6
// addresses. If any succeeds, closes relay connections and reconnects.
//
// Probes bind to each local global IPv6 address explicitly. This is
// required because macOS utun interfaces (iCloud Private Relay, VPN)
// often claim the default IPv6 route but don't forward public traffic.
// Binding to a specific USB LAN / Ethernet IPv6 forces the kernel to
// route through that interface regardless of default route priority.
func (pm *PeerManager) probeAndUpgrade(pid peer.ID) {
	short := pid.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	// Collect our local global IPv6 addresses for source-binding.
	summary, _ := DiscoverInterfaces()
	var localIPs []net.IP
	if summary != nil {
		for _, s := range summary.GlobalIPv6Addrs {
			if ip := net.ParseIP(s); ip != nil {
				localIPs = append(localIPs, ip)
			}
		}
	}
	if len(localIPs) == 0 {
		slog.Debug("peermanager: probe skip, no local global IPv6")
		return
	}

	// Collect unique global IPv6 TCP targets from the peer's DIRECT
	// addresses only. Circuit/relay addresses are excluded: their outer
	// IP:port belongs to the relay server, not the peer. Probing those
	// would confirm relay reachability, not direct-path viability.
	type target struct{ ip6, port string }
	seen := map[string]bool{}
	var targets []target
	addrs := pm.host.Peerstore().Addrs(pid)
	for _, a := range addrs {
		if isCircuitAddr(a) {
			continue
		}
		ip6, port := extractIPv6TCPAddr(a)
		if ip6 == "" {
			continue
		}
		ip := net.ParseIP(ip6)
		if ip == nil || !isGlobalIPv6(ip) {
			continue
		}
		key := ip6 + ":" + port
		if seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, target{ip6, port})
	}

	if len(targets) == 0 {
		slog.Debug("peermanager: probe skip, no global IPv6 targets", "peer", short)
		return
	}

	// Log target details for diagnostic visibility.
	var targetStrs []string
	for _, t := range targets {
		targetStrs = append(targetStrs, net.JoinHostPort(t.ip6, t.port))
	}
	slog.Debug("peermanager: probing direct IPv6 paths",
		"peer", short,
		"targets", targetStrs,
		"localIPs", len(localIPs))

	// Try each target with each local source address.
	for _, t := range targets {
		remote := net.JoinHostPort(t.ip6, t.port)
		for _, localIP := range localIPs {
			d := net.Dialer{
				Timeout:   probeTimeout,
				LocalAddr: &net.TCPAddr{IP: localIP, Port: 0},
			}
			conn, err := d.Dial("tcp6", remote)
			if err != nil {
				slog.Debug("peermanager: probe failed",
					"peer", short,
					"remote", remote,
					"localIP", localIP,
					"error", err)
				continue
			}
			conn.Close()

			slog.Debug("peermanager: direct IPv6 path confirmed, upgrading from relay",
				"peer", short, "via", remote, "localIP", localIP)

			// Build the confirmed TCP multiaddr from the probe result.
			// Only use the address we KNOW works. The swarm's DialPeer
			// tries ALL peerstore addresses; when many are unreachable
			// (QUIC through utun, ULA, stale), the cascade of rapid
			// failures causes macOS to throttle even valid addresses.
			confirmedTCP, mErr := ma.NewMultiaddr("/ip6/" + t.ip6 + "/tcp/" + t.port)
			if mErr != nil {
				slog.Warn("peermanager: failed to build confirmed addr",
					"error", mErr)
				return
			}

			// Save all current addresses, then temporarily restrict the
			// peerstore to ONLY the confirmed address. Existing relay
			// connections survive (they're independent of the peerstore).
			allPeerAddrs := pm.host.Peerstore().Addrs(pid)
			pm.host.Peerstore().ClearAddrs(pid)
			pm.host.Peerstore().AddAddrs(pid, []ma.Multiaddr{confirmedTCP}, discoveredAddrTTL)

			// Force a direct dial using only the confirmed address.
			ctx, cancel := context.WithTimeout(pm.ctx, probeConnectTimeout)
			ctx = network.WithForceDirectDial(ctx, "probe-upgrade")
			_, err = pm.host.Network().DialPeer(ctx, pid)
			cancel()

			// Restore the full address set regardless of outcome.
			pm.host.Peerstore().AddAddrs(pid, allPeerAddrs, discoveredAddrTTL)

			if err != nil {
				slog.Warn("peermanager: direct dial failed, relay stays",
					"peer", short, "error", err)
				pm.incMetric("probe_upgrade")
				return
			}

			slog.Info("peermanager: upgraded to DIRECT via IPv6",
				"peer", short)

			// Mark peer as connected and set cooldown so the reconnect
			// loop doesn't race back to relay while the direct connection
			// stabilizes.
			pm.mu.Lock()
			if mp := pm.peers[pid]; mp != nil {
				mp.Connected = true
				mp.LastSeen = time.Now()
				mp.ConsecFailures = 0
				mp.BackoffUntil = time.Time{}
				mp.ProbeUntil = time.Now().Add(90 * time.Second)
			}
			pm.mu.Unlock()

			// Close relay connections now that direct is established.
			// Runs as goroutine to keep sweeping for 90s in case the
			// remote peer's reconnect loop re-establishes relay.
			pm.startRelayCleanup(pid)

			pm.incMetric("probe_upgrade")
			return
		}
	}

	slog.Info("peermanager: all IPv6 probes failed", "peer", short)
}

// allConnsRelayed returns true if conns is non-empty and every connection
// is relay-limited (conn.Stat().Limited == true).
func allConnsRelayed(conns []network.Conn) bool {
	if len(conns) == 0 {
		return false
	}
	for _, c := range conns {
		if !c.Stat().Limited {
			return false
		}
	}
	return true
}

// IsLANMultiaddr returns true if the multiaddr starts with a private IPv4
// address (RFC 1918 / RFC 6598). Used to distinguish LAN addresses from
// public internet paths. Relay circuit addresses return false.
func IsLANMultiaddr(addr ma.Multiaddr) bool {
	if addr == nil {
		return false
	}
	first, _ := ma.SplitFirst(addr)
	if first == nil || first.Protocol().Code != ma.P_IP4 {
		return false
	}
	ip := net.ParseIP(first.Value())
	return ip != nil && isPrivateIPv4(ip)
}

// StripNonLANAddrs removes all non-LAN, non-relay addresses from the
// peerstore for a given peer. Keeps only relay circuit addresses and
// addresses verified as LAN by the LANRegistry (mDNS-proven). When
// lanReg is nil, falls back to bare IsLANMultiaddr (private IPv4 check).
//
// Using LANRegistry instead of IsLANMultiaddr aligns this function with
// connLogger's trust model (F17-U1): connLogger uses IsVerifiedLAN to
// decide which connections to close, so the peerstore filter must use the
// same source of truth. Without this, a private IPv4 address added by
// identify (not mDNS-verified) survives the strip but gets its connection
// closed by connLogger, causing unnecessary close-and-redial churn.
//
// Collects keepers first, then does a single ClearAddrs + AddAddrs to
// minimize the window where the peer has no addresses in the peerstore.
func StripNonLANAddrs(h host.Host, pid peer.ID, lanReg *LANRegistry) {
	allAddrs := h.Peerstore().Addrs(pid)
	keepers := make([]ma.Multiaddr, 0, len(allAddrs))
	for _, addr := range allAddrs {
		if isCircuitAddr(addr) {
			keepers = append(keepers, addr)
			continue
		}
		if lanReg != nil {
			if lanReg.IsVerifiedLAN(pid, addr) {
				keepers = append(keepers, addr)
			}
		} else if IsLANMultiaddr(addr) {
			keepers = append(keepers, addr)
		}
	}
	h.Peerstore().ClearAddrs(pid)
	if len(keepers) > 0 {
		h.Peerstore().AddAddrs(pid, keepers, discoveredAddrTTL)
	}
}

// hasLiveDirectConnection returns true if the peer has at least one
// non-relay connection whose local IP is still present on an active
// interface. Connections whose local IP has been removed (e.g. USB LAN
// unplugged) are considered dead even if they linger in the swarm:
// when the source interface disappears, the OS cannot send a TCP FIN,
// so the connection sits in the swarm for tens of seconds until the
// kernel finally tears it down. Treating such zombie connections as
// "direct already active" would block relay fallback indefinitely.
func hasLiveDirectConnection(h host.Host, pid peer.ID) bool {
	conns := h.Network().ConnsToPeer(pid)
	if len(conns) == 0 {
		return false
	}

	// Build current active IPs from all up interfaces.
	activeIPs := make(map[string]struct{})
	if ifaces, err := net.Interfaces(); err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 {
				continue
			}
			addrs, _ := iface.Addrs()
			for _, a := range addrs {
				ipNet, ok := a.(*net.IPNet)
				if !ok {
					continue
				}
				activeIPs[ipNet.IP.String()] = struct{}{}
			}
		}
	}
	activeIPs["127.0.0.1"] = struct{}{}
	activeIPs["::1"] = struct{}{}

	for _, c := range conns {
		if c.Stat().Limited {
			continue // relay connection - not direct
		}
		localIP := extractIPFromMultiaddrObj(c.LocalMultiaddr())
		if localIP == "" {
			continue
		}
		if _, ok := activeIPs[localIP]; ok {
			return true // live direct connection on an active interface
		}
		// Local IP not on any active interface. This connection is a
		// zombie: the interface was removed but the TCP socket hasn't
		// been torn down yet. Do not count it as a live direct path.
	}
	return false
}

// peerHasIPv6 returns true if any multiaddr contains an IPv6 component.
func peerHasIPv6(addrs []ma.Multiaddr) bool {
	for _, a := range addrs {
		if ip6, _ := extractIPv6TCPAddr(a); ip6 != "" {
			return true
		}
	}
	return false
}

// extractIPv6TCPAddr extracts the IPv6 address and TCP port from a
// multiaddr like /ip6/2001:db8::1/tcp/4001/p2p/12D3KooW...
func extractIPv6TCPAddr(addr ma.Multiaddr) (ip6, port string) {
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP6:
			ip6 = c.Value()
		case ma.P_TCP:
			port = c.Value()
		}
		return true
	})
	if ip6 == "" || port == "" {
		return "", ""
	}
	return ip6, port
}

// isCircuitAddr returns true if the multiaddr contains a /p2p-circuit
// component, indicating it is a relay circuit address rather than a
// direct address.
func isCircuitAddr(addr ma.Multiaddr) bool {
	found := false
	ma.ForEach(addr, func(c ma.Component) bool {
		if c.Protocol().Code == ma.P_CIRCUIT {
			found = true
			return false // stop iteration
		}
		return true
	})
	return found
}

// ---------------------------------------------------------------------------
// LANRegistry — mDNS-verified LAN peer/address tracking (BUG-MP-6/7)
//
// mDNS multicast reception is the ONLY proof that a peer is on the local
// network. RFC 1918 addresses alone are NOT proof of LAN proximity:
//   - Starlink CGNAT presents remote peers with 10.1.x.x source addresses
//   - Docker bridge networks create 172.17-21.x.x addresses
//   - VPN tunnels use 10.x.x.x or 172.x.x.x ranges
//
// All of these pass IsLANMultiaddr but traverse the internet or are
// unreachable from the LAN. The registry solves this by tracking which
// (peer, IP) pairs were confirmed via mDNS discovery. Only connections
// matching a registry entry are trusted as LAN by the gater and connLogger.
// ---------------------------------------------------------------------------

// LANRegistry tracks peers and IPs confirmed as LAN-local via mDNS.
type LANRegistry struct {
	mu      sync.RWMutex
	entries map[peer.ID]*lanEntry
}

type lanEntry struct {
	ips      map[string]struct{} // IPs discovered via mDNS for this peer
	lastSeen time.Time           // refreshed on each mDNS discovery
}

// lanRegistryTTL is how long an mDNS-verified entry remains valid without
// a refresh. 2 minutes = 4 missed mDNS cycles (30s each). After expiry,
// the peer is no longer trusted as LAN until the next mDNS discovery.
const lanRegistryTTL = 2 * time.Minute

// NewLANRegistry creates an empty registry.
func NewLANRegistry() *LANRegistry {
	return &LANRegistry{
		entries: make(map[peer.ID]*lanEntry),
	}
}

// Add registers IPs discovered via mDNS for a peer. Called by mDNS
// discovery after filtering to LAN addresses. Each call refreshes the
// TTL and merges new IPs (previous IPs from the same peer are kept).
func (r *LANRegistry) Add(pid peer.ID, ips []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[pid]
	if !ok {
		e = &lanEntry{ips: make(map[string]struct{})}
		r.entries[pid] = e
	}
	e.lastSeen = time.Now()
	for _, ip := range ips {
		e.ips[ip] = struct{}{}
	}
}

// IsVerifiedLAN returns true if the remote address of a connection
// matches an mDNS-verified LAN IP for the given peer. The entry must
// not be expired (within lanRegistryTTL of last mDNS discovery).
func (r *LANRegistry) IsVerifiedLAN(pid peer.ID, remoteAddr ma.Multiaddr) bool {
	ip := extractIPFromMultiaddrObj(remoteAddr)
	if ip == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.entries[pid]
	if !ok || time.Since(e.lastSeen) > lanRegistryTTL {
		return false
	}
	_, verified := e.ips[ip]
	return verified
}

// HasVerifiedLANConn returns true if the peer has at least one live
// non-relay connection whose remote IP is mDNS-verified. This is the
// authoritative "does this peer have a real LAN connection?" check for
// all trust-making code (RS gates, bandwidth budgets, transport policy).
// Bare RFC 1918 matches misclassify CGNAT, Docker, VPN, and multi-WAN
// routed-private subnets as LAN — only mDNS multicast reception proves
// link-local proximity.
func (r *LANRegistry) HasVerifiedLANConn(h host.Host, pid peer.ID) bool {
	conns := h.Network().ConnsToPeer(pid)
	if len(conns) == 0 {
		return false
	}
	r.mu.RLock()
	e, ok := r.entries[pid]
	expired := !ok || time.Since(e.lastSeen) > lanRegistryTTL
	r.mu.RUnlock()
	if expired {
		return false
	}
	for _, c := range conns {
		if c.Stat().Limited {
			continue
		}
		ip := extractIPFromMultiaddrObj(c.RemoteMultiaddr())
		if ip == "" {
			continue
		}
		r.mu.RLock()
		_, verified := e.ips[ip]
		r.mu.RUnlock()
		if verified {
			return true
		}
	}
	return false
}

// Remove deletes a peer from the registry (e.g., when deauthorized).
func (r *LANRegistry) Remove(pid peer.ID) {
	r.mu.Lock()
	delete(r.entries, pid)
	r.mu.Unlock()
}
