package relay

import (
	"log/slog"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
)

// CircuitACL implements the relayv2.ACLFilter interface to control which peers
// can establish data circuits through this relay.
//
// By default (EnableDataRelay=false), only admin peers and peers with the
// relay_data=true attribute can create circuits. All other authorized peers
// can still connect directly for signaling protocols (relay-pair, peer-notify,
// relay-admin, relay-unseal, relay-motd, zkp-auth, pingpong) since those are
// direct streams to the relay, not relay circuits.
//
// When EnableDataRelay=true, all authorized peers can create circuits.
// Connection gating (AuthorizedPeerGater) still handles unauthorized peers.
type CircuitACL struct {
	authKeysPath    string
	enableDataRelay bool
}

// NewCircuitACL creates a new circuit ACL filter.
// authKeysPath is the path to the authorized_keys file (read on each decision,
// supports hot-reload via relay auth-reload).
// enableDataRelay is the global toggle from relay-server.yaml security config.
func NewCircuitACL(authKeysPath string, enableDataRelay bool) *CircuitACL {
	return &CircuitACL{
		authKeysPath:    authKeysPath,
		enableDataRelay: enableDataRelay,
	}
}

// AllowReserve allows all authorized peers to make relay reservations.
// Connection gating handles unauthorized peers before this is called.
// Reservations are lightweight presence announcements; blocking them here
// would prevent peers from being discoverable via relay addresses.
func (a *CircuitACL) AllowReserve(p peer.ID, addr ma.Multiaddr) bool {
	return true
}

// AllowConnect controls whether src can establish a data circuit to dest
// through this relay. This is the enforcement point for seed relay data policy.
//
// When enableDataRelay is true, all circuits are allowed.
// When false (default), a circuit is allowed only if either peer is admin
// or has relay_data=true in authorized_keys.
func (a *CircuitACL) AllowConnect(src peer.ID, srcAddr ma.Multiaddr, dest peer.ID) bool {
	if a.enableDataRelay {
		return true
	}

	srcAllowed := a.peerHasDataAccess(src)
	destAllowed := a.peerHasDataAccess(dest)

	if srcAllowed || destAllowed {
		short := src.String()
		if len(short) > 16 {
			short = short[:16] + "..."
		}
		slog.Debug("circuit ACL: allowed data circuit",
			"src", short, "src_access", srcAllowed,
			"dest_access", destAllowed)
		return true
	}

	short := src.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}
	slog.Info("circuit ACL: denied data circuit (data relay disabled)",
		"src", short)
	return false
}

// peerHasDataAccess checks if a peer has admin role or relay_data=true.
func (a *CircuitACL) peerHasDataAccess(p peer.ID) bool {
	if auth.IsAdmin(a.authKeysPath, p) {
		return true
	}
	return auth.HasRelayData(a.authKeysPath, p)
}
