package p2pnet

import (
	"context"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	ProbeUntil      time.Time // probe cooldown: reconnect loop skips this peer until expired
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

	pm.wg.Add(3)
	go pm.eventLoop()
	go pm.reconnectLoop()
	go pm.probeLoop()

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
					if !mp.ProbeUntil.IsZero() && time.Now().Before(mp.ProbeUntil) {
						slog.Debug("peermanager: probe-upgraded peer disconnected",
							"peer", e.Peer,
							"probeUntil", mp.ProbeUntil.Format("15:04:05"))
					}
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

	result, err := pm.pathDialer.DialPeer(dialCtx, target)

	pm.mu.Lock()
	if err != nil {
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
func (pm *PeerManager) closeRelayConns(pid peer.ID) {
	short := pid.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	// Close immediately, then periodically for 90s.
	// The remote peer's reconnect loop runs every 30s, so we need
	// at least 3 sweeps to cover the window.
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()

	closeOnce := func() {
		for _, c := range pm.host.Network().ConnsToPeer(pid) {
			if c.Stat().Limited {
				c.Close()
				slog.Debug("peermanager: closed relay conn (direct exists)",
					"peer", short)
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
			pm.host.Peerstore().AddAddrs(pid, pi.Addrs, 10*time.Minute)

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

	// Collect unique global IPv6 TCP targets from the peer's addresses.
	type target struct{ ip6, port string }
	seen := map[string]bool{}
	var targets []target
	addrs := pm.host.Peerstore().Addrs(pid)
	for _, a := range addrs {
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

	slog.Info("peermanager: probing direct IPv6 paths",
		"peer", short,
		"targets", len(targets),
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

			slog.Info("peermanager: direct IPv6 path confirmed, upgrading from relay",
				"peer", short, "via", remote, "localIP", localIP)

			// Build IPv6-only address list for the direct dial.
			var directAddrs []ma.Multiaddr
			for _, pa := range pm.host.Peerstore().Addrs(pid) {
				pIP6, _ := extractIPv6TCPAddr(pa)
				if pIP6 == "" {
					continue
				}
				pip := net.ParseIP(pIP6)
				if pip != nil && isGlobalIPv6(pip) {
					directAddrs = append(directAddrs, pa)
				}
			}
			// Also include QUIC addresses for the same global IPv6.
			for _, pa := range pm.host.Peerstore().Addrs(pid) {
				str := pa.String()
				for _, tgt := range targets {
					if strings.Contains(str, "/ip6/"+tgt.ip6+"/") && strings.Contains(str, "/quic-v1") {
						directAddrs = append(directAddrs, pa)
					}
				}
			}

			// Add IPv6 addresses to the peerstore so the swarm can use them.
			pm.host.Peerstore().AddAddrs(pid, directAddrs, 10*time.Minute)

			// Force a direct dial even though a relay connection exists.
			// Without this, host.Connect() no-ops when the peer is already
			// connected (returns nil without dialing).
			ctx, cancel := context.WithTimeout(pm.ctx, probeConnectTimeout)
			ctx = network.WithForceDirectDial(ctx, "probe-upgrade")
			_, err = pm.host.Network().DialPeer(ctx, pid)
			cancel()
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
			go pm.closeRelayConns(pid)

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
