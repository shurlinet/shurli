package relay

import (
	"log/slog"
	"sync"

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
	authKeysPath         string
	enableDataRelay      bool
	enableConnectionGating bool

	mu      sync.RWMutex
	peers   map[peer.ID]bool         // cached authorized peer set
	entries map[peer.ID]auth.PeerEntry // cached entries for role/attribute checks
}

// NewCircuitACL creates a new circuit ACL filter.
// authKeysPath is the path to the authorized_keys file.
// enableDataRelay is the global toggle from relay-server.yaml security config.
// enableConnectionGating controls whether reservations require authorization.
// When gating is disabled, all peers can reserve (open relay mode).
// The authorized_keys data is cached in memory and refreshed via Reload().
func NewCircuitACL(authKeysPath string, enableDataRelay, enableConnectionGating bool) *CircuitACL {
	acl := &CircuitACL{
		authKeysPath:         authKeysPath,
		enableDataRelay:      enableDataRelay,
		enableConnectionGating: enableConnectionGating,
		peers:                make(map[peer.ID]bool),
		entries:              make(map[peer.ID]auth.PeerEntry),
	}
	// Initial load.
	acl.loadFromDisk()
	return acl
}

// Reload refreshes the cached authorized_keys data from disk.
// Called by AdminServer.reloadAuth after peer mutations or auth-reload.
func (a *CircuitACL) Reload() {
	a.loadFromDisk()
}

func (a *CircuitACL) loadFromDisk() {
	if a.authKeysPath == "" {
		return
	}
	peers, err := auth.LoadAuthorizedKeys(a.authKeysPath)
	if err != nil {
		slog.Warn("circuit ACL: failed to load authorized_keys", "err", err)
		return
	}
	entries, err := auth.ListPeers(a.authKeysPath)
	if err != nil {
		slog.Warn("circuit ACL: failed to list peers", "err", err)
		return
	}
	entryMap := make(map[peer.ID]auth.PeerEntry, len(entries))
	for _, e := range entries {
		entryMap[e.PeerID] = e
	}
	a.mu.Lock()
	a.peers = peers
	a.entries = entryMap
	a.mu.Unlock()
}

// AllowReserve allows authorized peers to make relay reservations.
// Probation peers (not in authorized_keys) are denied to prevent relay
// circuit abuse during enrollment mode.
// If connection gating is disabled or no authKeysPath is configured,
// all peers are allowed (open relay).
func (a *CircuitACL) AllowReserve(p peer.ID, addr ma.Multiaddr) bool {
	if !a.enableConnectionGating || a.authKeysPath == "" {
		return true
	}
	a.mu.RLock()
	allowed := a.peers[p]
	a.mu.RUnlock()
	return allowed
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

	a.mu.RLock()
	srcAllowed := a.cachedHasDataAccess(src)
	destAllowed := a.cachedHasDataAccess(dest)
	a.mu.RUnlock()

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

// cachedHasDataAccess checks if a peer has admin role or relay_data=true
// using the cached entry map. Must be called with a.mu held (read or write).
func (a *CircuitACL) cachedHasDataAccess(p peer.ID) bool {
	e, ok := a.entries[p]
	if !ok {
		return false
	}
	return e.Role == auth.RoleAdmin || e.RelayData
}
