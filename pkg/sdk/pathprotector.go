package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/transport"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

const (
	// MaxProtectedPeers caps the number of peers with active path protection (S4).
	MaxProtectedPeers = 10

	// MaxManagedRelayConns caps managed relay circuits (R4-F12).
	// Protection without managed conn costs nothing (just prevents cleanup).
	// Managed conns cost a relay circuit slot each.
	MaxManagedRelayConns = 5

	// establishmentCooldown rate-limits relay establishment per peer (S1).
	establishmentCooldown = 5 * time.Minute

	// reaperInterval is how often the safety reaper checks for orphans (I5).
	reaperInterval = 5 * time.Minute

	// orphanTimeout is how long a tag can exist with zero activity before reaping (I5).
	orphanTimeout = 5 * time.Minute

	// healthCheckInterval is how often idle managed conns are pinged (R9-D3).
	healthCheckInterval = 60 * time.Second

	// DefaultConnGracePeriod is how long a new relay connection survives in
	// closeOnce before being eligible for cleanup (R8-C1). Configurable for tests.
	DefaultConnGracePeriod = 30 * time.Second
)

// shortPeerID returns a truncated peer ID for logging (safe for short IDs in tests).
func shortPeerID(pid peer.ID) string {
	s := pid.String()
	if len(s) > 16 {
		return s[:16]
	}
	return s
}

// ManagedPathInfo describes a managed relay connection for observability (R8-I1).
type ManagedPathInfo struct {
	PeerID      peer.ID
	RelayPeerID peer.ID
	RemoteAddr  string
	Created     time.Time
	Streams     int
	Dead        bool
}

// PathProtector prevents relay connection cleanup during active transfers
// and proactively establishes managed relay circuits as backup paths.
//
// Design: 9 rounds of thought experiments, 111 findings.
// See project-performance-baseline-2026-03-15.md TS-5 section.
type PathProtector struct {
	host        host.Host
	relaySource RelaySource
	lanRegistry *LANRegistry
	bwTracker   *BandwidthTracker // nil when bandwidth tracking disabled (R4-F4)
	metrics     *Metrics          // nil when metrics disabled (R8-I2)

	mu   sync.RWMutex
	tags map[peer.ID]map[string]time.Time // pid -> tag -> created

	// Managed relay connections established via Transport.Dial (D6).
	// NOT in the swarm. One per peer (R7-E2: concurrent transfers multiplex).
	managedConns map[peer.ID]*managedConn

	// Background establishment goroutines. Cancel func stops them on Unprotect (I4).
	establishMu  sync.Mutex
	establishing map[peer.ID]context.CancelFunc

	// Rate limit: last establishment attempt per peer (S1).
	lastEstablish map[peer.ID]time.Time

	// Relay server ConnManager protection refcount (R5-D2).
	relayProtectCount map[peer.ID]int // relay server peer ID -> circuit count

	// Dead managed conns (detected by health check or stream error).
	deadConns map[peer.ID]bool

	// Managed conn counter for unique IDs.
	connCounter uint64

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup // tracks reaper, health, and establishment goroutines
}

// NewPathProtector creates a PathProtector. Pass to both Network and PeerManager.
// Pattern matches LANRegistry: created externally, shared across components (D1).
func NewPathProtector(h host.Host, rs RelaySource, lr *LANRegistry, bw *BandwidthTracker) *PathProtector {
	ctx, cancel := context.WithCancel(context.Background())
	pp := &PathProtector{
		host:              h,
		relaySource:       rs,
		lanRegistry:       lr,
		bwTracker:         bw,
		tags:              make(map[peer.ID]map[string]time.Time),
		managedConns:      make(map[peer.ID]*managedConn),
		establishing:      make(map[peer.ID]context.CancelFunc),
		lastEstablish:     make(map[peer.ID]time.Time),
		relayProtectCount: make(map[peer.ID]int),
		deadConns:         make(map[peer.ID]bool),
		ctx:               ctx,
		cancel:            cancel,
	}

	// Start safety reaper and health monitor.
	pp.wg.Add(2)
	go func() { defer pp.wg.Done(); pp.reaperLoop() }()
	go func() { defer pp.wg.Done(); pp.healthLoop() }()

	return pp
}

// SetMetrics sets the Prometheus metrics for managed conn lifecycle (R8-I2).
func (pp *PathProtector) SetMetrics(m *Metrics) {
	pp.metrics = m
}

// Protect marks a peer as path-protected with the given tag.
// Multiple tags per peer are supported (I1). Background relay establishment
// triggers on first tag (0->1 transition, I3).
//
// Security: compiled-in plugins are trusted (current phase). When Layer 2 WASM
// ships, Protect must become a host function requiring a capability token (S3).
func (pp *PathProtector) Protect(pid peer.ID, tag string) {
	pp.mu.Lock()

	// Cap protected peers (S4).
	if _, exists := pp.tags[pid]; !exists {
		if len(pp.tags) >= MaxProtectedPeers {
			pp.mu.Unlock()
			slog.Warn("pathprotector: max protected peers reached, skipping",
				"peer", shortPeerID(pid), "tag", tag, "max", MaxProtectedPeers)
			return
		}
	}

	peerTags, exists := pp.tags[pid]
	if !exists {
		peerTags = make(map[string]time.Time)
		pp.tags[pid] = peerTags
	}
	peerTags[tag] = time.Now()
	firstTag := len(peerTags) == 1 && !exists
	pp.mu.Unlock()

	short := shortPeerID(pid)

	// Belt-and-suspenders: also protect in ConnManager (I10).
	pp.host.ConnManager().Protect(pid, "shurli-path-"+tag)

	slog.Info("pathprotector: protect",
		"peer", short, "tag", tag, "first", firstTag)

	// On first tag, start background relay establishment (I3).
	if firstTag {
		pp.maybeEstablishRelay(pid)
	}
}

// Unprotect removes a protection tag from a peer. When the last tag is removed,
// the managed relay connection is closed (I4).
func (pp *PathProtector) Unprotect(pid peer.ID, tag string) {
	pp.mu.Lock()
	peerTags, exists := pp.tags[pid]
	if !exists {
		pp.mu.Unlock()
		return
	}
	delete(peerTags, tag)
	lastRemoved := len(peerTags) == 0
	if lastRemoved {
		delete(pp.tags, pid)
	}
	pp.mu.Unlock()

	// Remove ConnManager protection.
	pp.host.ConnManager().Unprotect(pid, "shurli-path-"+tag)

	short := shortPeerID(pid)
	slog.Info("pathprotector: unprotect",
		"peer", short, "tag", tag, "last", lastRemoved)

	if lastRemoved {
		pp.cancelEstablishment(pid)
		pp.closeManagedConn(pid, "unprotect")

		// Reset establishment cooldown on clean unprotect so the next
		// transfer to this peer can immediately establish a new managed circuit.
		// The cooldown exists to prevent rapid retries on failure, not to block
		// the next legitimate transfer after a clean completion.
		pp.mu.Lock()
		delete(pp.lastEstablish, pid)
		pp.mu.Unlock()
	}
}

// IsProtected returns whether a peer has any active protection tags.
// Called by PeerManager's closeOnce and startRelayCleanup guards (C1, C2, C4).
func (pp *PathProtector) IsProtected(pid peer.ID) bool {
	pp.mu.RLock()
	defer pp.mu.RUnlock()
	tags, ok := pp.tags[pid]
	return ok && len(tags) > 0
}

// ForceUnprotectAll removes all tags and closes all managed connections to a peer.
// Called on deauthorization (R7-C1).
func (pp *PathProtector) ForceUnprotectAll(pid peer.ID) {
	pp.mu.Lock()
	peerTags, exists := pp.tags[pid]
	if !exists {
		pp.mu.Unlock()
		return
	}
	// Remove all ConnManager protections.
	for tag := range peerTags {
		pp.host.ConnManager().Unprotect(pid, "shurli-path-"+tag)
	}
	delete(pp.tags, pid)
	pp.mu.Unlock()

	short := shortPeerID(pid)
	slog.Info("pathprotector: force-unprotect (deauthorized)", "peer", short)

	pp.cancelEstablishment(pid)
	pp.closeManagedConn(pid, "deauth")
}

// ManagedGroups returns managed connections as ConnGroups for hedging (R8-D2).
// Used by AllConnGroups to merge with swarm groups.
func (pp *PathProtector) ManagedGroups(peerID peer.ID) []ConnGroup {
	pp.mu.RLock()
	mc, ok := pp.managedConns[peerID]
	dead := pp.deadConns[peerID]
	pp.mu.RUnlock()

	if !ok || dead || mc.IsClosed() {
		return nil
	}

	return []ConnGroup{{
		Type:  "managed-relay-" + mc.relayPeerID.String(),
		Conns: []network.Conn{mc},
	}}
}

// ManagedConnsForCancel returns managed connections as network.Conn for cancel
// fan-out (R5-C2). Returns nil if no managed conn exists.
func (pp *PathProtector) ManagedConnsForCancel(peerID peer.ID) []network.Conn {
	pp.mu.RLock()
	mc, ok := pp.managedConns[peerID]
	dead := pp.deadConns[peerID]
	pp.mu.RUnlock()

	if !ok || dead || mc.IsClosed() {
		return nil
	}
	return []network.Conn{mc}
}

// ManagedPaths returns info about all managed connections for observability (R8-I1).
func (pp *PathProtector) ManagedPaths() []ManagedPathInfo {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	if len(pp.managedConns) == 0 {
		return nil
	}

	result := make([]ManagedPathInfo, 0, len(pp.managedConns))
	for pid, mc := range pp.managedConns {
		result = append(result, ManagedPathInfo{
			PeerID:      pid,
			RelayPeerID: mc.relayPeerID,
			RemoteAddr:  mc.RemoteMultiaddr().String(),
			Created:     mc.created,
			Streams:     mc.streamCount(),
			Dead:        pp.deadConns[pid],
		})
	}
	return result
}

// Close stops the PathProtector and cleans up all managed connections (R7-D2).
// Called from daemon shutdown chain after PeerManager.Close().
func (pp *PathProtector) Close() {
	pp.cancel() // stops reaper, health, and establishment goroutines
	pp.wg.Wait() // wait for all background goroutines to exit

	pp.mu.Lock()
	// Cancel all establishment goroutines.
	for pid, cancelFn := range pp.establishing {
		cancelFn()
		delete(pp.establishing, pid)
	}
	// Close all managed conns and unprotect relay servers.
	for pid, mc := range pp.managedConns {
		mc.Close()
		pp.unprotectRelayServerLocked(mc.relayPeerID)
		if pp.metrics != nil {
			pp.metrics.ManagedConnsActive.Dec()
			pp.metrics.ManagedConnsClosedTotal.WithLabelValues("shutdown").Inc()
		}
		delete(pp.managedConns, pid)
		delete(pp.deadConns, pid)
	}
	// Clear all tags + ConnManager protections.
	for pid, peerTags := range pp.tags {
		for tag := range peerTags {
			pp.host.ConnManager().Unprotect(pid, "shurli-path-"+tag)
		}
	}
	pp.tags = make(map[peer.ID]map[string]time.Time)
	pp.mu.Unlock()

	slog.Info("pathprotector: closed")
}

// --- Internal: managed relay establishment ---

// maybeEstablishRelay starts a background relay circuit to the peer (I7, I11).
// Skipped for LAN peers (R4-F7). Rate limited per peer (S1).
func (pp *PathProtector) maybeEstablishRelay(pid peer.ID) {
	// Skip if shutting down.
	if pp.ctx.Err() != nil {
		return
	}

	// Skip LAN peers (R4-F7): same failure domain, relay backup provides no benefit.
	if pp.lanRegistry != nil && pp.lanRegistry.HasVerifiedLANConn(pp.host, pid) {
		slog.Info("pathprotector: skipping managed circuit (LAN peer)",
			"peer", shortPeerID(pid))
		return
	}

	// Rate limit (S1).
	pp.mu.RLock()
	lastAttempt := pp.lastEstablish[pid]
	pp.mu.RUnlock()
	if time.Since(lastAttempt) < establishmentCooldown {
		slog.Debug("pathprotector: skipping managed circuit (cooldown)",
			"peer", shortPeerID(pid))
		return
	}

	// Check managed conn cap (R4-F12).
	pp.mu.RLock()
	connCount := len(pp.managedConns)
	_, alreadyHas := pp.managedConns[pid]
	pp.mu.RUnlock()
	if alreadyHas {
		return // already have a managed conn
	}
	if connCount >= MaxManagedRelayConns {
		slog.Debug("pathprotector: max managed conns reached",
			"peer", shortPeerID(pid), "max", MaxManagedRelayConns)
		return
	}

	// Avoid duplicate establishment goroutines (I3).
	pp.establishMu.Lock()
	if _, running := pp.establishing[pid]; running {
		pp.establishMu.Unlock()
		return
	}
	estCtx, estCancel := context.WithCancel(pp.ctx)
	pp.establishing[pid] = estCancel
	pp.establishMu.Unlock()

	pp.wg.Add(1)
	go func() {
		defer pp.wg.Done()
		pp.establishRelay(estCtx, pid)
	}()
}

// establishRelay dials a relay circuit to the peer via Transport.Dial (breakthrough design).
// Uses public go-libp2p APIs only. No fork needed.
func (pp *PathProtector) establishRelay(ctx context.Context, pid peer.ID) {
	defer func() {
		pp.establishMu.Lock()
		delete(pp.establishing, pid)
		pp.establishMu.Unlock()
	}()

	pp.mu.Lock()
	pp.lastEstablish[pid] = time.Now()
	pp.mu.Unlock()

	short := shortPeerID(pid)

	// Get relay addresses.
	if pp.relaySource == nil {
		slog.Debug("pathprotector: no relay source", "peer", short)
		return
	}
	relayAddrs := pp.relaySource.RelayAddrs()
	if len(relayAddrs) == 0 {
		slog.Debug("pathprotector: no relay addresses", "peer", short)
		return
	}

	// Get swarm for Transport.Dial access (V5).
	sw, ok := pp.host.Network().(*swarm.Swarm)
	if !ok {
		slog.Warn("pathprotector: host network is not a swarm")
		return
	}

	// Build circuit addresses and try each relay (R4-F13: distribute across relays).
	// Pick relay that doesn't already have a managed circuit for this PathProtector.
	pp.mu.RLock()
	existingRelays := make(map[peer.ID]bool)
	for _, mc := range pp.managedConns {
		existingRelays[mc.relayPeerID] = true
	}
	pp.mu.RUnlock()

	for _, relayAddr := range relayAddrs {
		if ctx.Err() != nil {
			return
		}

		// Parse relay address to get relay peer ID.
		relayMaddr, err := ma.NewMultiaddr(relayAddr)
		if err != nil {
			continue
		}
		relayAI, err := peer.AddrInfoFromP2pAddr(relayMaddr)
		if err != nil {
			continue
		}

		// Prefer relay without existing managed circuit (R4-F13).
		if existingRelays[relayAI.ID] {
			continue
		}

		// Build circuit multiaddr.
		circuitStr := relayAddr + "/p2p-circuit/p2p/" + pid.String()
		circuitAddr, err := ma.NewMultiaddr(circuitStr)
		if err != nil {
			slog.Debug("pathprotector: invalid circuit addr",
				"relay", relayAddr, "error", err)
			continue
		}

		// Get transport for circuit address (V5).
		tpt := sw.TransportForDialing(circuitAddr)
		if tpt == nil {
			// Relay transport not available (R4-F11).
			slog.Debug("pathprotector: no transport for circuit addr",
				"peer", short)
			continue
		}

		// Dial with timeout.
		dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
		capConn, err := tpt.Dial(dialCtx, circuitAddr, pid)
		dialCancel()
		if err != nil {
			slog.Debug("pathprotector: managed circuit dial failed",
				"peer", short, "relay", shortPeerID(relayAI.ID), "error", err)
			continue
		}

		// Success. Store managed connection.
		pp.storeManagedConn(pid, relayAI.ID, capConn)

		slog.Info("pathprotector: managed circuit established",
			"peer", short, "relay", shortPeerID(relayAI.ID),
			"remote", capConn.RemoteMultiaddr())
		return
	}

	// If all preferred relays failed, try existing relays as fallback.
	for _, relayAddr := range relayAddrs {
		if ctx.Err() != nil {
			return
		}
		relayMaddr, err := ma.NewMultiaddr(relayAddr)
		if err != nil {
			continue
		}
		relayAI, err := peer.AddrInfoFromP2pAddr(relayMaddr)
		if err != nil {
			continue
		}
		if !existingRelays[relayAI.ID] {
			continue // already tried
		}

		circuitStr := relayAddr + "/p2p-circuit/p2p/" + pid.String()
		circuitAddr, err := ma.NewMultiaddr(circuitStr)
		if err != nil {
			continue
		}
		tpt := sw.TransportForDialing(circuitAddr)
		if tpt == nil {
			continue
		}
		dialCtx, dialCancel := context.WithTimeout(ctx, 15*time.Second)
		capConn, err := tpt.Dial(dialCtx, circuitAddr, pid)
		dialCancel()
		if err != nil {
			continue
		}
		pp.storeManagedConn(pid, relayAI.ID, capConn)
		slog.Info("pathprotector: managed circuit established (fallback relay)",
			"peer", short, "relay", shortPeerID(relayAI.ID))
		return
	}

	if pp.metrics != nil {
		pp.metrics.ManagedConnsFailedTotal.WithLabelValues().Inc()
	}
	slog.Debug("pathprotector: all relay establishment attempts failed",
		"peer", short)
}

// storeManagedConn stores a newly-established managed connection.
func (pp *PathProtector) storeManagedConn(pid peer.ID, relayPeerID peer.ID, capConn transport.CapableConn) {
	rm := pp.host.Network().ResourceManager()

	pp.mu.Lock()
	pp.connCounter++
	id := fmt.Sprintf("managed-%d", pp.connCounter)

	// Close any existing managed conn for this peer (shouldn't happen, but safe).
	if old, exists := pp.managedConns[pid]; exists {
		old.Close()
		pp.unprotectRelayServerLocked(old.relayPeerID)
	}

	mc := newManagedConn(capConn, relayPeerID, rm, pp.bwTracker, id)
	// R4-F10: reactive dead detection on stream open error.
	mc.onStreamError = func() {
		pp.mu.Lock()
		pp.deadConns[pid] = true
		pp.mu.Unlock()
		slog.Warn("pathprotector: managed circuit stream error (marking dead)",
			"peer", shortPeerID(pid))
	}
	pp.managedConns[pid] = mc
	delete(pp.deadConns, pid)

	// Protect relay SERVER peer in ConnManager (R5-C1).
	pp.protectRelayServerLocked(relayPeerID)
	pp.mu.Unlock()

	// Metrics (R8-I2).
	if pp.metrics != nil {
		pp.metrics.ManagedConnsActive.Inc()
		pp.metrics.ManagedConnsEstablishedTotal.WithLabelValues().Inc()
	}
}

// closeManagedConn closes and removes the managed conn for a peer.
func (pp *PathProtector) closeManagedConn(pid peer.ID, reason string) {
	pp.mu.Lock()
	mc, exists := pp.managedConns[pid]
	if !exists {
		pp.mu.Unlock()
		return
	}
	delete(pp.managedConns, pid)
	delete(pp.deadConns, pid)
	pp.unprotectRelayServerLocked(mc.relayPeerID)
	pp.mu.Unlock()

	mc.Close()

	// Metrics (R8-I2).
	if pp.metrics != nil {
		pp.metrics.ManagedConnsActive.Dec()
		pp.metrics.ManagedConnsClosedTotal.WithLabelValues(reason).Inc()
	}

	slog.Info("pathprotector: managed circuit closed",
		"peer", shortPeerID(pid), "reason", reason)
}

// cancelEstablishment cancels any background relay establishment for a peer (I4).
func (pp *PathProtector) cancelEstablishment(pid peer.ID) {
	pp.establishMu.Lock()
	if cancelFn, ok := pp.establishing[pid]; ok {
		cancelFn()
		delete(pp.establishing, pid)
	}
	pp.establishMu.Unlock()
}

// --- Relay server ConnManager protection (R5-C1, R5-D2) ---

func (pp *PathProtector) protectRelayServerLocked(relayPeerID peer.ID) {
	pp.relayProtectCount[relayPeerID]++
	if pp.relayProtectCount[relayPeerID] == 1 {
		pp.host.ConnManager().Protect(relayPeerID, "shurli-managed-hop")
	}
}

func (pp *PathProtector) unprotectRelayServerLocked(relayPeerID peer.ID) {
	pp.relayProtectCount[relayPeerID]--
	if pp.relayProtectCount[relayPeerID] <= 0 {
		pp.host.ConnManager().Unprotect(relayPeerID, "shurli-managed-hop")
		delete(pp.relayProtectCount, relayPeerID)
	}
}

// --- Safety reaper (I5) ---

func (pp *PathProtector) reaperLoop() {
	ticker := time.NewTicker(reaperInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pp.ctx.Done():
			return
		case <-ticker.C:
			pp.reap()
		}
	}
}

func (pp *PathProtector) reap() {
	pp.mu.RLock()
	var orphans []peer.ID
	now := time.Now()
	for pid, tags := range pp.tags {
		allOld := true
		for _, created := range tags {
			if now.Sub(created) < orphanTimeout {
				allOld = false
				break
			}
		}
		if allOld {
			// Check if there are active streams on managed conn.
			if mc, ok := pp.managedConns[pid]; ok {
				if mc.streamCount() > 0 {
					continue // active streams, not orphaned (R4-F9)
				}
			}
			orphans = append(orphans, pid)
		}
	}
	pp.mu.RUnlock()

	for _, pid := range orphans {
		// Re-check under write lock to avoid TOCTOU with concurrent Protect().
		// A new tag could have been added between our RLock scan and now.
		pp.mu.Lock()
		tags, exists := pp.tags[pid]
		if !exists {
			pp.mu.Unlock()
			continue // already removed by something else
		}
		stillOrphan := true
		for _, created := range tags {
			if time.Since(created) < orphanTimeout {
				stillOrphan = false
				break
			}
		}
		pp.mu.Unlock()

		if stillOrphan {
			slog.Warn("pathprotector: reaper cleaned orphaned protection",
				"peer", shortPeerID(pid))
			pp.ForceUnprotectAll(pid)
		}
	}
}

// --- Health monitoring (R9-D3) ---

func (pp *PathProtector) healthLoop() {
	ticker := time.NewTicker(healthCheckInterval)
	defer ticker.Stop()

	for {
		select {
		case <-pp.ctx.Done():
			return
		case <-ticker.C:
			pp.checkHealth()
		}
	}
}

func (pp *PathProtector) checkHealth() {
	pp.mu.RLock()
	var toCheck []peer.ID
	for pid, mc := range pp.managedConns {
		if pp.deadConns[pid] {
			continue
		}
		// Only check idle conns (no active streams).
		if mc.streamCount() == 0 {
			toCheck = append(toCheck, pid)
		}
	}
	pp.mu.RUnlock()

	for _, pid := range toCheck {
		pp.mu.RLock()
		mc, ok := pp.managedConns[pid]
		pp.mu.RUnlock()
		if !ok {
			continue
		}

		// Try to open and immediately close a stream as a health check.
		// This validates the yamux session and relay circuit are alive.
		ctx, cancel := context.WithTimeout(pp.ctx, 5*time.Second)
		s, err := mc.CapableConn.OpenStream(ctx)
		cancel()
		if err != nil {
			slog.Warn("pathprotector: managed circuit dead",
				"peer", shortPeerID(pid), "error", err)
			pp.mu.Lock()
			pp.deadConns[pid] = true
			pp.mu.Unlock()

			// Attempt re-establishment if still protected.
			if pp.IsProtected(pid) {
				pp.mu.Lock()
				pp.lastEstablish[pid] = time.Time{} // allow immediate re-establishment
				pp.mu.Unlock()
				pp.closeManagedConn(pid, "dead")
				pp.maybeEstablishRelay(pid)
			}
		} else {
			s.Reset() // close the test stream immediately
		}
	}
}
