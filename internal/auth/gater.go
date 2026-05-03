package auth

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// PQC policy constants.
const (
	PQCPolicyDisabled      = "disabled"
	PQCPolicyOpportunistic = "opportunistic"
	PQCPolicyMandatory     = "mandatory"
)

// pqNoiseProtocolID is the security protocol reported by PQ Noise connections.
const pqNoiseProtocolID = "/pq-noise/1"

// AuthDecisionFunc is called on every inbound auth decision with the peer ID
// (truncated) and result ("allow" or "deny"). Used for metrics and audit logging
// without creating a circular dependency on pkg/sdk.
type AuthDecisionFunc func(peerID, result string)

// AuthorizedPeerGater implements the ConnectionGater interface.
// It blocks connections from peers that are not in the authorized list.
// Supports enrollment mode for relay pairing and expiring peer authorization.
type AuthorizedPeerGater struct {
	authorizedPeers map[peer.ID]bool
	peerExpiry      map[peer.ID]time.Time // zero = never expires
	onDecision      AuthDecisionFunc      // nil-safe
	mu              sync.RWMutex

	// Enrollment mode: temporarily allows unknown peers during pairing.
	enrollmentEnabled bool
	probationPeers    map[peer.ID]time.Time // peer -> admitted time
	probationLimit    int                   // max concurrent probation peers
	probationTimeout  time.Duration         // evict after this duration

	// Per-IP rate limiting: prevents rapid probation cycling from a single IP.
	probationIPCooldown  map[string]time.Time // IP -> last probation admission
	probationCooldownDur time.Duration        // minimum gap between admissions from same IP

	// lanDialFilter is called by InterceptAddrDial to decide whether an
	// outbound dial to a specific address should be allowed. When set,
	// returning false blocks the dial. Used to prevent non-LAN dials when
	// a LAN connection already exists to the peer (BUG-MP-4).
	lanDialFilter func(peer.ID, ma.Multiaddr) bool

	// PQC enforcement: belt-and-suspenders check in InterceptUpgraded.
	// Primary enforcement is at transport registration level (network.go).
	// This catches edge cases where a classical connection slips through.
	pqcPolicy        string              // "mandatory", "opportunistic", "disabled"
	peerPQCOverrides map[peer.ID]string  // per-peer PQC policy override from authorized_keys
}

// NewAuthorizedPeerGater creates a new connection gater with the given authorized peers.
func NewAuthorizedPeerGater(authorizedPeers map[peer.ID]bool) *AuthorizedPeerGater {
	return &AuthorizedPeerGater{
		authorizedPeers:      authorizedPeers,
		peerExpiry:           make(map[peer.ID]time.Time),
		probationPeers:       make(map[peer.ID]time.Time),
		probationLimit:       10,
		probationTimeout:     10 * time.Second,
		probationIPCooldown:  make(map[string]time.Time),
		probationCooldownDur: 2 * time.Second,
		peerPQCOverrides:     make(map[peer.ID]string),
	}
}

// InterceptPeerDial is called when dialing a peer
func (g *AuthorizedPeerGater) InterceptPeerDial(p peer.ID) bool {
	// Allow outbound connections to anyone
	// This is important for DHT, relay connections, etc.
	return true
}

// InterceptAddrDial is called when dialing an address.
// When a LAN dial filter is set, blocks non-LAN dials to peers that already
// have a LAN connection (prevents identify/DHT address leaks from displacing
// established LAN paths).
func (g *AuthorizedPeerGater) InterceptAddrDial(id peer.ID, addr ma.Multiaddr) bool {
	g.mu.RLock()
	filter := g.lanDialFilter
	g.mu.RUnlock()
	if filter != nil {
		if !filter(id, addr) {
			return false
		}
	}
	return true
}

// SetLANDialFilter sets a callback that controls outbound address dials.
// The callback receives the target peer ID and address. Return true to
// allow the dial, false to block it.
func (g *AuthorizedPeerGater) SetLANDialFilter(fn func(peer.ID, ma.Multiaddr) bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.lanDialFilter = fn
}

// InterceptAccept is called when accepting a connection (before crypto handshake)
func (g *AuthorizedPeerGater) InterceptAccept(cm network.ConnMultiaddrs) bool {
	// Allow all at this stage - we'll check after crypto handshake in InterceptSecured
	return true
}

// InterceptSecured is called after the crypto handshake (peer ID is verified).
// This is the PRIMARY authorization check point.
//
// Uses a write lock (not RWMutex read lock) because the enrollment/probation
// path mutates state. A single lock type eliminates the RLock->Lock upgrade
// gap that could allow brief probation limit overruns under contention.
// This is called per-connection (not per-packet), so the cost is negligible.
func (g *AuthorizedPeerGater) InterceptSecured(dir network.Direction, p peer.ID, addr network.ConnMultiaddrs) bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if dir != network.DirInbound {
		return true // always allow outbound
	}

	short := p.String()[:16] + "..."

	// Check if peer is in the authorized list.
	if g.authorizedPeers[p] {
		// Check expiry if set.
		if exp, ok := g.peerExpiry[p]; ok && !exp.IsZero() && time.Now().After(exp) {
			slog.Warn("inbound connection denied (expired)", "peer", short)
			if g.onDecision != nil {
				g.onDecision(short, "deny")
			}
			return false
		}
		slog.Info("inbound connection allowed", "peer", short)
		if g.onDecision != nil {
			g.onDecision(short, "allow")
		}
		return true
	}

	// Check enrollment mode: allow probationary peers during pairing.
	// If at capacity, evict the oldest probation peer (newest is more likely a legitimate pairing).
	if g.enrollmentEnabled {
		// At capacity: evict the oldest probation peer to make room.
		if len(g.probationPeers) >= g.probationLimit {
			var oldestPeer peer.ID
			var oldestTime time.Time
			for pid, admitted := range g.probationPeers {
				if oldestTime.IsZero() || admitted.Before(oldestTime) {
					oldestPeer = pid
					oldestTime = admitted
				}
			}
			if oldestPeer != "" {
				delete(g.probationPeers, oldestPeer)
				slog.Info("probation peer preempted (oldest evicted)", "evicted", oldestPeer.String()[:16]+"...", "new", short)
			}
		}
		// Per-IP rate limiting: prevent rapid probation cycling from a single IP.
		// IPv6 addresses are normalized to /64 prefix to prevent bypass via rotation.
		remoteIP := normalizeIPForRateLimit(extractIPFromMultiaddr(addr.RemoteMultiaddr()))
		if remoteIP != "" {
			if lastAdmit, ok := g.probationIPCooldown[remoteIP]; ok {
				if time.Since(lastAdmit) < g.probationCooldownDur {
					slog.Warn("inbound connection denied (IP cooldown)", "peer", short)
					if g.onDecision != nil {
						g.onDecision(short, "deny")
					}
					return false
				}
			}
			// Evict stale entries to prevent unbounded growth.
			if len(g.probationIPCooldown) >= 1000 {
				now := time.Now()
				for ip, t := range g.probationIPCooldown {
					if now.Sub(t) > g.probationCooldownDur*10 {
						delete(g.probationIPCooldown, ip)
					}
				}
			}
			g.probationIPCooldown[remoteIP] = time.Now()
		}
		g.probationPeers[p] = time.Now()
		slog.Info("inbound connection allowed (probation)", "peer", short)
		if g.onDecision != nil {
			g.onDecision(short, "allow")
		}
		return true
	}

	slog.Warn("inbound connection denied", "peer", short)
	if g.onDecision != nil {
		g.onDecision(short, "deny")
	}
	return false
}

// InterceptUpgraded is called after connection upgrade (after muxer negotiation).
// Enforces PQC policy as belt-and-suspenders: rejects classical Noise connections
// when the effective policy for the peer is "mandatory".
//
// QUIC connections report empty Security (PQC handled at TLS layer via
// X25519MLKEM768) and are always allowed through this check.
func (g *AuthorizedPeerGater) InterceptUpgraded(conn network.Conn) (bool, control.DisconnectReason) {
	if conn == nil {
		return true, 0
	}

	g.mu.RLock()
	policy := g.effectivePQCPolicy(conn.RemotePeer())
	g.mu.RUnlock()

	if policy != PQCPolicyMandatory {
		return true, 0
	}

	// QUIC: PQC is handled at the TLS 1.3 layer (X25519MLKEM768).
	// Check Transport field directly rather than relying on empty Security.
	cs := conn.ConnState()
	if cs.Transport == "quic-v1" || cs.Transport == "quic" {
		return true, 0
	}

	// Non-QUIC with empty security (shouldn't happen, but defensive).
	if cs.Security == "" {
		return true, 0
	}

	security := cs.Security

	// TCP/WS: check that PQ Noise was negotiated, not classical /noise.
	if security == pqNoiseProtocolID {
		return true, 0
	}

	short := conn.RemotePeer().String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	dir := "inbound"
	if conn.Stat().Direction == network.DirOutbound {
		dir = "outbound"
	}
	slog.Warn("pqc: rejected connection (mandatory policy, classical security)",
		"peer", short, "security", security, "direction", dir)
	return false, 0
}

// effectivePQCPolicy returns the PQC policy for a specific peer, considering
// per-peer overrides. Must be called with at least a read lock held.
func (g *AuthorizedPeerGater) effectivePQCPolicy(p peer.ID) string {
	if override, ok := g.peerPQCOverrides[p]; ok {
		return override
	}
	if g.pqcPolicy == "" {
		return PQCPolicyOpportunistic
	}
	return g.pqcPolicy
}

// SetPQCPolicy sets the global PQC policy. Only "mandatory" and "opportunistic"
// are valid runtime values. "disabled" can only be set at startup (F151).
// Returns an error if attempting to set "disabled" at runtime.
func (g *AuthorizedPeerGater) SetPQCPolicy(policy string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if policy == PQCPolicyDisabled {
		return fmt.Errorf("pqc policy cannot be set to 'disabled' at runtime (startup-only)")
	}
	if policy != PQCPolicyMandatory && policy != PQCPolicyOpportunistic {
		return fmt.Errorf("invalid pqc policy: %q (must be 'mandatory' or 'opportunistic')", policy)
	}
	g.pqcPolicy = policy
	slog.Info("pqc policy updated (gater enforcement active, restart for full transport change)", "policy", policy)
	return nil
}

// SetPQCPolicyStartup sets the PQC policy at startup (allows all values including "disabled").
func (g *AuthorizedPeerGater) SetPQCPolicyStartup(policy string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.pqcPolicy = policy
}

// SetPeerPQCOverride sets a per-peer PQC policy override.
// Pass empty string to remove the override.
func (g *AuthorizedPeerGater) SetPeerPQCOverride(p peer.ID, policy string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if policy == "" {
		delete(g.peerPQCOverrides, p)
		return
	}
	if g.peerPQCOverrides == nil {
		g.peerPQCOverrides = make(map[peer.ID]string)
	}
	g.peerPQCOverrides[p] = policy
}

// UpdatePeerPQCOverrides replaces all per-peer PQC overrides with the given map.
// Used during hot-reload to sync with authorized_keys file.
func (g *AuthorizedPeerGater) UpdatePeerPQCOverrides(overrides map[peer.ID]string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.peerPQCOverrides = overrides
	if g.peerPQCOverrides == nil {
		g.peerPQCOverrides = make(map[peer.ID]string)
	}
}

// PQCPolicy returns the current global PQC policy.
func (g *AuthorizedPeerGater) PQCPolicy() string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	if g.pqcPolicy == "" {
		return PQCPolicyOpportunistic
	}
	return g.pqcPolicy
}

// UpdateAuthorizedPeers updates the authorized peers list (for hot-reload support)
func (g *AuthorizedPeerGater) UpdateAuthorizedPeers(authorizedPeers map[peer.ID]bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.authorizedPeers = authorizedPeers
	slog.Info("updated authorized peers list", "count", len(authorizedPeers))
}

// GetAuthorizedPeersCount returns the number of authorized peers
func (g *AuthorizedPeerGater) GetAuthorizedPeersCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.authorizedPeers)
}

// IsAuthorized checks if a peer is authorized
func (g *AuthorizedPeerGater) IsAuthorized(p peer.ID) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.authorizedPeers[p]
}

// GetAuthorizedPeerIDs returns a slice of all currently authorized peer IDs.
// Used by PeerManager to build the reconnection watchlist.
func (g *AuthorizedPeerGater) GetAuthorizedPeerIDs() []peer.ID {
	g.mu.RLock()
	defer g.mu.RUnlock()
	ids := make([]peer.ID, 0, len(g.authorizedPeers))
	for pid := range g.authorizedPeers {
		ids = append(ids, pid)
	}
	return ids
}

// SetDecisionCallback sets a callback invoked on every inbound auth decision.
// This is used by the observability layer to record metrics and audit events
// without creating a circular import from internal/auth to pkg/sdk.
func (g *AuthorizedPeerGater) SetDecisionCallback(fn AuthDecisionFunc) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.onDecision = fn
}

// PrintAuthorizedPeers prints the list of authorized peers (for debugging)
func (g *AuthorizedPeerGater) PrintAuthorizedPeers() {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fmt.Println("Authorized peers:")
	for p := range g.authorizedPeers {
		fmt.Printf("  - %s\n", p.String())
	}
}

// SetEnrollmentMode enables or disables enrollment mode for relay pairing.
// When enabled, unknown peers are admitted on probation up to the limit.
func (g *AuthorizedPeerGater) SetEnrollmentMode(enabled bool, limit int, timeout time.Duration) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.enrollmentEnabled = enabled
	if limit > 0 {
		g.probationLimit = limit
	}
	if timeout > 0 {
		g.probationTimeout = timeout
	}
	if !enabled {
		// Clear probation peers and IP cooldowns when disabling.
		g.probationPeers = make(map[peer.ID]time.Time)
		g.probationIPCooldown = make(map[string]time.Time)
	}
	slog.Info("enrollment mode changed", "enabled", enabled, "limit", g.probationLimit, "timeout", g.probationTimeout)
}

// IsEnrollmentEnabled returns whether enrollment mode is active.
func (g *AuthorizedPeerGater) IsEnrollmentEnabled() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.enrollmentEnabled
}

// PromotePeer moves a peer from probation to the authorized list.
func (g *AuthorizedPeerGater) PromotePeer(p peer.ID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.probationPeers, p)
	g.authorizedPeers[p] = true
	slog.Info("peer promoted from probation", "peer", p.String()[:16]+"...")
}

// SetPeerExpiry sets an expiration time for an authorized peer.
// Zero time means never expires.
func (g *AuthorizedPeerGater) SetPeerExpiry(p peer.ID, expiresAt time.Time) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if expiresAt.IsZero() {
		delete(g.peerExpiry, p)
	} else {
		g.peerExpiry[p] = expiresAt
	}
}

// CleanupProbation evicts probation peers that have exceeded the timeout.
// The disconnect callback is called for each evicted peer (outside the lock).
func (g *AuthorizedPeerGater) CleanupProbation(disconnect func(peer.ID)) {
	g.mu.Lock()
	now := time.Now()
	var evicted []peer.ID
	for p, admitted := range g.probationPeers {
		if now.Sub(admitted) > g.probationTimeout {
			evicted = append(evicted, p)
			delete(g.probationPeers, p)
		}
	}
	g.mu.Unlock()

	for _, p := range evicted {
		slog.Info("probation peer evicted", "peer", p.String()[:16]+"...")
		if disconnect != nil {
			disconnect(p)
		}
	}
}

// ProbationCount returns the current number of probation peers.
func (g *AuthorizedPeerGater) ProbationCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.probationPeers)
}

// extractIPFromMultiaddr extracts the IP address string from a multiaddr.
// Returns "" if no IP component is found.
func extractIPFromMultiaddr(addr ma.Multiaddr) string {
	if addr == nil {
		return ""
	}
	var ip string
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_IP4, ma.P_IP6:
			ip = c.Value()
			return false
		}
		return true
	})
	return ip
}

// normalizeIPForRateLimit returns a rate-limiting key for the given IP.
// IPv4 addresses are used as-is. IPv6 addresses are masked to /64 prefix
// to prevent trivial bypass via address rotation within a single allocation.
func normalizeIPForRateLimit(ip string) string {
	if ip == "" {
		return ""
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	// IPv4: use the full address.
	if parsed.To4() != nil {
		return ip
	}
	// IPv6: mask to /64 prefix.
	masked := parsed.Mask(net.CIDRMask(64, 128))
	return masked.String() + "/64"
}
