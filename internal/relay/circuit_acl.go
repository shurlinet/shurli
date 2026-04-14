package relay

import (
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/grants"
)

// CircuitACL implements the relayv2.ACLFilter interface to control which peers
// can establish data circuits through this relay.
//
// By default (EnableDataRelay=false), only admin peers and peers with an
// active time-limited grant can create circuits. All other authorized peers
// can still connect directly for signaling protocols (invite, peer-notify,
// relay-admin, relay-unseal, relay-motd, zkp-auth, pingpong) since those are
// direct streams to the relay, not relay circuits.
//
// When EnableDataRelay=true, all authorized peers can create circuits.
// Connection gating (AuthorizedPeerGater) still handles unauthorized peers.
type CircuitACL struct {
	authKeysPath         string
	enableDataRelay      bool
	enableConnectionGating bool
	grantStore           *grants.Store   // time-limited data access grants
	budgetTracker        *BudgetTracker  // per-peer relay data budgets (nil if not configured)

	mu      sync.RWMutex
	peers   map[peer.ID]bool         // cached authorized peer set
	entries map[peer.ID]auth.PeerEntry // cached entries for role checks (admin detection)

	// Rate-limited denial logging: under attack, deny logs could flood.
	// Only log every Nth denial after the threshold.
	denyCount   atomic.Int64
	lastDenyLog time.Time
	denyLogMu   sync.Mutex
}

// NewCircuitACL creates a new circuit ACL filter.
// authKeysPath is the path to the authorized_keys file.
// enableDataRelay is the global toggle from relay-server.yaml security config.
// enableConnectionGating controls whether reservations require authorization.
// When gating is disabled, all peers can reserve (open relay mode).
// grantStore provides time-limited per-peer data access grants (may be nil if
// grants are not configured).
// The authorized_keys data is cached in memory and refreshed via Reload().
func NewCircuitACL(authKeysPath string, enableDataRelay, enableConnectionGating bool, grantStore *grants.Store) *CircuitACL {
	acl := &CircuitACL{
		authKeysPath:         authKeysPath,
		enableDataRelay:      enableDataRelay,
		enableConnectionGating: enableConnectionGating,
		grantStore:           grantStore,
		peers:                make(map[peer.ID]bool),
		entries:              make(map[peer.ID]auth.PeerEntry),
	}
	// Initial load.
	acl.loadFromDisk()
	return acl
}

// SetBudgetTracker wires the budget tracker for AllowConnect budget checks (TE3-C1).
func (a *CircuitACL) SetBudgetTracker(bt *BudgetTracker) {
	a.budgetTracker = bt
}

// IsAdmin returns true if the peer has the admin role in authorized_keys (SEC4).
// Used by LimitingHost to bypass budget enforcement for admin peers.
func (a *CircuitACL) IsAdmin(p peer.ID) bool {
	a.mu.RLock()
	e, ok := a.entries[p]
	a.mu.RUnlock()
	return ok && e.Role == auth.RoleAdmin
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
// through this relay. This is the enforcement point for relay data policy.
//
// When enableDataRelay is true, all circuits are allowed.
// When false (default), a circuit is allowed only if either peer is admin
// or has an active time-limited grant in the relay's grant store.
func (a *CircuitACL) AllowConnect(src peer.ID, srcAddr ma.Multiaddr, dest peer.ID) bool {
	if a.enableDataRelay {
		return true
	}

	srcAllowed := a.hasDataAccess(src)
	destAllowed := a.hasDataAccess(dest)

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

	a.logDenial(src)
	return false
}

// logDenial rate-limits circuit denial logging to prevent log flooding under attack.
// First 100 denials are logged individually. After that, logs every 100th denial
// or at most once per 10 seconds.
func (a *CircuitACL) logDenial(src peer.ID) {
	count := a.denyCount.Add(1)
	short := src.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	// Always log the first 100 denials.
	if count <= 100 {
		slog.Info("circuit ACL: denied data circuit (data relay disabled)", "src", short)
		return
	}

	// After threshold: log every 100th denial or at most once per 10 seconds.
	if count%100 == 0 {
		slog.Warn("circuit ACL: denied data circuits (rate-limited log)", "src", short, "total_denials", count)
		return
	}
	a.denyLogMu.Lock()
	if time.Since(a.lastDenyLog) >= 10*time.Second {
		a.lastDenyLog = time.Now()
		a.denyLogMu.Unlock()
		slog.Warn("circuit ACL: denied data circuits (rate-limited log)", "src", short, "total_denials", count)
		return
	}
	a.denyLogMu.Unlock()
}

// hasDataAccess checks if a peer has admin role or an active grant.
// Admin check uses the cached entry map (requires a.mu). Grant check
// uses the grant store (its own locking).
//
// The grant check uses empty service ("") because the relay ACL gates on
// "does this peer have ANY valid grant", not on which specific service.
// Service-scoped grants (e.g. --services file-transfer) restrict which
// plugin streams a node allows, not whether the relay forwards data.
func (a *CircuitACL) hasDataAccess(p peer.ID) bool {
	// Check admin role and get grant store reference under a single lock.
	a.mu.RLock()
	e, ok := a.entries[p]
	isAdmin := ok && e.Role == auth.RoleAdmin
	gs := a.grantStore
	a.mu.RUnlock()

	if isAdmin {
		return true
	}

	// Check time-limited grant (empty service = any grant qualifies).
	// Transport 0 = skip transport caveat check: circuit ACL is before the
	// circuit is usable for plugin streams, so transport-level restrictions
	// in the grant caveat chain aren't applicable here.
	if gs != nil {
		if !gs.Check(p, "", 0) {
			return false
		}
		// TE3-C1: deny if grant exists but budget is exhausted.
		// Prevents wasted relay resources (goroutines, memory, streams) on
		// circuits that would immediately terminate.
		if a.budgetTracker != nil && a.budgetTracker.HasBudget(p) {
			if a.budgetTracker.RemainingBudget(p) <= 0 {
				short := p.String()
				if len(short) > 16 {
					short = short[:16] + "..."
				}
				slog.Info("circuit ACL: denied (budget exhausted)", "peer", short)
				return false
			}
		}
		return true
	}
	return false
}
