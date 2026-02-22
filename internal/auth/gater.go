package auth

import (
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// AuthDecisionFunc is called on every inbound auth decision with the peer ID
// (truncated) and result ("allow" or "deny"). Used for metrics and audit logging
// without creating a circular dependency on pkg/p2pnet.
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
}

// NewAuthorizedPeerGater creates a new connection gater with the given authorized peers.
func NewAuthorizedPeerGater(authorizedPeers map[peer.ID]bool) *AuthorizedPeerGater {
	return &AuthorizedPeerGater{
		authorizedPeers: authorizedPeers,
		peerExpiry:      make(map[peer.ID]time.Time),
		probationPeers:  make(map[peer.ID]time.Time),
		probationLimit:  10,
		probationTimeout: 15 * time.Second,
	}
}

// InterceptPeerDial is called when dialing a peer
func (g *AuthorizedPeerGater) InterceptPeerDial(p peer.ID) bool {
	// Allow outbound connections to anyone
	// This is important for DHT, relay connections, etc.
	return true
}

// InterceptAddrDial is called when dialing an address
func (g *AuthorizedPeerGater) InterceptAddrDial(id peer.ID, ma multiaddr.Multiaddr) bool {
	// Allow outbound connections
	return true
}

// InterceptAccept is called when accepting a connection (before crypto handshake)
func (g *AuthorizedPeerGater) InterceptAccept(cm network.ConnMultiaddrs) bool {
	// Allow all at this stage - we'll check after crypto handshake in InterceptSecured
	return true
}

// InterceptSecured is called after the crypto handshake (peer ID is verified).
// This is the PRIMARY authorization check point.
func (g *AuthorizedPeerGater) InterceptSecured(dir network.Direction, p peer.ID, addr network.ConnMultiaddrs) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

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
	if g.enrollmentEnabled && len(g.probationPeers) < g.probationLimit {
		// Upgrade to write lock for probation admission.
		g.mu.RUnlock()
		g.mu.Lock()
		// Re-check under write lock (double-check pattern).
		if g.enrollmentEnabled && len(g.probationPeers) < g.probationLimit && !g.authorizedPeers[p] {
			g.probationPeers[p] = time.Now()
			slog.Info("inbound connection allowed (probation)", "peer", short)
			if g.onDecision != nil {
				g.onDecision(short, "allow")
			}
			g.mu.Unlock()
			g.mu.RLock()
			return true
		}
		g.mu.Unlock()
		g.mu.RLock()
	}

	slog.Warn("inbound connection denied", "peer", short)
	if g.onDecision != nil {
		g.onDecision(short, "deny")
	}
	return false
}

// InterceptUpgraded is called after connection upgrade (after muxer negotiation)
func (g *AuthorizedPeerGater) InterceptUpgraded(conn network.Conn) (bool, control.DisconnectReason) {
	// No additional checks needed at this stage
	return true, 0
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

// SetDecisionCallback sets a callback invoked on every inbound auth decision.
// This is used by the observability layer to record metrics and audit events
// without creating a circular import from internal/auth to pkg/p2pnet.
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
		// Clear probation peers when disabling.
		g.probationPeers = make(map[peer.ID]time.Time)
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
