package sdk

import (
	"context"
	"crypto/sha256"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	mh "github.com/multiformats/go-multihash"
)

// RelaySource provides relay addresses to PathDialer.
// Implementations return the current set of relay multiaddrs.
type RelaySource interface {
	RelayAddrs() []string
}

// StaticRelaySource wraps a fixed relay address list for backward compatibility.
type StaticRelaySource struct {
	Addrs []string
}

// RelayAddrs returns the static relay address list.
func (s *StaticRelaySource) RelayAddrs() []string {
	return s.Addrs
}

// RelayServiceCID returns a deterministic CID for relay discovery on DHT.
// Namespace-aware: different private networks get different CIDs.
func RelayServiceCID(namespace string) cid.Cid {
	key := "/shurli/relay/v1"
	if namespace != "" {
		key = "/shurli/" + namespace + "/relay/v1"
	}
	hash := sha256.Sum256([]byte(key))
	encoded, _ := mh.Encode(hash[:], mh.SHA2_256)
	return cid.NewCidV1(cid.Raw, encoded)
}

// RelayDiscovery discovers relay peers via DHT and combines them with
// static relay addresses from config.
type RelayDiscovery struct {
	staticRelays []peer.AddrInfo
	namespace    string
	metrics      *Metrics // nil-safe

	mu             sync.RWMutex
	host           host.Host
	kdht           *dht.IpfsDHT
	discovered     []peer.AddrInfo
	health         *RelayHealth         // nil-safe; when set, RelayAddrs returns health-ranked order
	budgetChecker  RelayGrantChecker    // nil-safe; when set, RelayAddrs factors budget into ranking
}

// NewRelayDiscovery creates a RelayDiscovery with static relays.
// DHT discovery is enabled later via SetDHT after host construction.
func NewRelayDiscovery(staticRelays []peer.AddrInfo, namespace string, m *Metrics) *RelayDiscovery {
	return &RelayDiscovery{
		staticRelays: staticRelays,
		namespace:    namespace,
		metrics:      m,
	}
}

// SetHost provides the host for DHT operations.
func (rd *RelayDiscovery) SetHost(h host.Host) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.host = h
}

// SetDHT provides the DHT for relay discovery. Called after DHT creation.
func (rd *RelayDiscovery) SetDHT(kdht *dht.IpfsDHT) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.kdht = kdht
}

// SetHealth provides a health tracker for relay scoring. When set,
// RelayAddrs returns addresses ranked by health score (best first).
func (rd *RelayDiscovery) SetHealth(rh *RelayHealth) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.health = rh
}

// SetBudgetChecker provides a grant checker for budget-aware relay ranking.
// When set, RelayAddrs factors remaining session budget into relay scoring
// so high-budget relays are preferred over low-budget seed relays.
func (rd *RelayDiscovery) SetBudgetChecker(gc RelayGrantChecker) {
	rd.mu.Lock()
	defer rd.mu.Unlock()
	rd.budgetChecker = gc
}

// Advertise announces this node as a relay provider on the DHT.
// Should be called when PeerRelay enables (via OnStateChange callback).
func (rd *RelayDiscovery) Advertise(ctx context.Context, interval time.Duration) {
	rd.mu.RLock()
	kdht := rd.kdht
	rd.mu.RUnlock()

	if kdht == nil {
		return
	}

	c := RelayServiceCID(rd.namespace)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if err := kdht.Provide(ctx, c, true); err != nil {
			slog.Debug("relay discovery: provide failed", "error", err)
		} else {
			slog.Info("relay discovery: advertised as relay on DHT")
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Discover queries the DHT for relay providers. Returns up to count peers.
func (rd *RelayDiscovery) Discover(ctx context.Context, count int) []peer.AddrInfo {
	rd.mu.RLock()
	kdht := rd.kdht
	rd.mu.RUnlock()

	if kdht == nil {
		return nil
	}

	c := RelayServiceCID(rd.namespace)
	findCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	ch := kdht.FindProvidersAsync(findCtx, c, count)

	var result []peer.AddrInfo
	for ai := range ch {
		if len(ai.Addrs) > 0 {
			result = append(result, ai)
		}
	}

	if len(result) > 0 {
		slog.Info("relay discovery: found DHT relays", "count", len(result))
	}

	return result
}

// StartDiscoveryLoop runs periodic relay discovery in the background.
func (rd *RelayDiscovery) StartDiscoveryLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		peers := rd.Discover(ctx, 10)
		if len(peers) > 0 {
			rd.mu.Lock()
			rd.discovered = peers
			health := rd.health
			rd.mu.Unlock()

			// Register newly discovered relays with health tracker
			if health != nil {
				for _, ai := range peers {
					health.RegisterRelay(ai.ID, false)
				}
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// AllRelays returns static relays followed by DHT-discovered relays.
func (rd *RelayDiscovery) AllRelays() []peer.AddrInfo {
	rd.mu.RLock()
	defer rd.mu.RUnlock()

	// Static first, then discovered (dedup by peer ID)
	seen := make(map[peer.ID]bool)
	var result []peer.AddrInfo

	for _, ai := range rd.staticRelays {
		seen[ai.ID] = true
		result = append(result, ai)
	}
	for _, ai := range rd.discovered {
		if !seen[ai.ID] {
			result = append(result, ai)
		}
	}

	return result
}

// RelayAddrs implements RelaySource. Returns multiaddr strings for all
// known relays (static + DHT discovered). Relays are ranked by a composite
// score combining health (latency + success rate) and budget availability
// (remaining session bytes). High-budget, healthy relays appear first.
func (rd *RelayDiscovery) RelayAddrs() []string {
	rd.mu.RLock()
	health := rd.health
	bc := rd.budgetChecker
	rd.mu.RUnlock()

	relays := rd.AllRelays()

	if len(relays) > 1 && (health != nil || bc != nil) {
		sort.Slice(relays, func(i, j int) bool {
			return rd.relayScore(relays[i].ID, health, bc) > rd.relayScore(relays[j].ID, health, bc)
		})
	}

	var addrs []string
	for _, ai := range relays {
		for _, addr := range ai.Addrs {
			full := addr.String() + "/p2p/" + ai.ID.String()
			addrs = append(addrs, full)
		}
	}
	return addrs
}

// relayScore computes a composite score for general relay ranking.
// Relays with active grants get a budget bonus on top of health.
// Relays without grants compete on pure health (not penalized).
// This prevents a failing relay with a grant from ranking above
// a healthy relay without a grant.
func (rd *RelayDiscovery) relayScore(pid peer.ID, health *RelayHealth, bc RelayGrantChecker) float64 {
	hs := defaultScore
	if health != nil {
		hs = health.Score(pid)
	}

	if bc == nil {
		return hs
	}

	bs := relayBudgetScore(pid, bc)
	if bs == 0.0 {
		// No grant or expired: rank by health only. Don't penalize
		// healthy relays just because they don't have a grant yet.
		return hs
	}
	// Has active grant: health is the baseline, budget adds a bonus.
	// Max possible: 1.0 (health) + 0.5 (budget bonus) = 1.5.
	// Relays with grants always rank above relays without grants
	// (because hs alone is capped at 1.0, and 1.0 + any bonus > 1.0).
	return hs + bs*0.5
}

// relayBudgetScore returns a 0.0-1.0 score based on remaining session budget.
// 0.0 = no grant or expired. 1.0 = unlimited or >= 2GB remaining.
func relayBudgetScore(pid peer.ID, bc RelayGrantChecker) float64 {
	remaining, budget, _, ok := bc.GrantStatus(pid)
	if !ok || remaining <= 0 {
		return 0.0 // no grant or expired
	}

	// math.MaxInt64 budget = unlimited.
	if budget >= math.MaxInt64/2 {
		return 1.0
	}

	// Scale: 0.0 at 0 bytes, 1.0 at >= 2GB.
	const twoGB = 2 << 30
	if budget >= twoGB {
		return 1.0
	}
	return float64(budget) / float64(twoGB)
}

// PeerSource returns a function compatible with libp2p's autorelay.PeerSource.
// The returned channel yields relay peers for AutoRelay to use.
// Safe to call before DHT is set (returns static relays only until DHT is available).
func (rd *RelayDiscovery) PeerSource(ctx context.Context, numPeers int) <-chan peer.AddrInfo {
	ch := make(chan peer.AddrInfo, numPeers)
	go func() {
		defer close(ch)
		relays := rd.AllRelays()
		for _, ai := range relays {
			select {
			case ch <- ai:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
