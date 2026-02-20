package auth

import (
	"fmt"
	"log/slog"
	"sync"

	"github.com/libp2p/go-libp2p/core/control"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// AuthDecisionFunc is called on every inbound auth decision with the peer ID
// (truncated) and result ("allow" or "deny"). Used for metrics and audit logging
// without creating a circular dependency on pkg/p2pnet.
type AuthDecisionFunc func(peerID, result string)

// AuthorizedPeerGater implements the ConnectionGater interface
// It blocks connections from peers that are not in the authorized list
type AuthorizedPeerGater struct {
	authorizedPeers map[peer.ID]bool
	onDecision      AuthDecisionFunc // nil-safe
	mu              sync.RWMutex
}

// NewAuthorizedPeerGater creates a new connection gater with the given authorized peers
func NewAuthorizedPeerGater(authorizedPeers map[peer.ID]bool) *AuthorizedPeerGater {
	return &AuthorizedPeerGater{
		authorizedPeers: authorizedPeers,
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

// InterceptSecured is called after the crypto handshake (peer ID is verified)
// This is the PRIMARY authorization check point
func (g *AuthorizedPeerGater) InterceptSecured(dir network.Direction, p peer.ID, addr network.ConnMultiaddrs) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Only check authorization for inbound connections
	if dir == network.DirInbound {
		short := p.String()[:16] + "..."
		authorized := g.authorizedPeers[p]
		if !authorized {
			slog.Warn("inbound connection denied", "peer", short)
			if g.onDecision != nil {
				g.onDecision(short, "deny")
			}
			return false
		}
		slog.Info("inbound connection allowed", "peer", short)
		if g.onDecision != nil {
			g.onDecision(short, "allow")
		}
	}

	// Always allow outbound connections
	return true
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
