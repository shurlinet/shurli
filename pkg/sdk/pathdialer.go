package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/net/swarm"
	ma "github.com/multiformats/go-multiaddr"
)

// PathType describes how a peer connection was established.
type PathType string

const (
	PathDirect  PathType = "DIRECT"
	PathRelayed PathType = "RELAYED"
)

// DialResult is the outcome of a successful PathDialer.DialPeer call.
type DialResult struct {
	PathType PathType      `json:"path_type"`
	Duration time.Duration `json:"duration_ms"`
	Address  string        `json:"address"` // winning multiaddr
}

// PathDialer connects to peers using parallel path racing. It launches
// DHT discovery and relay circuit attempts concurrently and returns as
// soon as the first path succeeds, cancelling the other.
type PathDialer struct {
	host        host.Host
	kdht        *dht.IpfsDHT // may be nil (no DHT)
	relaySource RelaySource  // provides relay addresses (static or dynamic)
	metrics     *Metrics     // nil-safe
}

// NewPathDialer creates a PathDialer. The DHT and metrics are optional (nil-safe).
// relaySource provides relay addresses; use &StaticRelaySource{Addrs: addrs} for
// a fixed list, or a RelayDiscovery for dynamic DHT-discovered relays.
func NewPathDialer(h host.Host, kdht *dht.IpfsDHT, relaySource RelaySource, m *Metrics) *PathDialer {
	return &PathDialer{
		host:        h,
		kdht:        kdht,
		relaySource: relaySource,
		metrics:     m,
	}
}

// DialPeer connects to the target peer using parallel path racing.
// If already connected, it returns immediately with the current path type.
// Otherwise it races DHT discovery against relay circuit, returning the
// first successful connection.
func (pd *PathDialer) DialPeer(ctx context.Context, peerID peer.ID) (*DialResult, error) {
	start := time.Now()

	// Already connected - classify the existing connection and return
	if pd.host.Network().Connectedness(peerID) == network.Connected {
		result := &DialResult{
			PathType: classifyConnection(pd.host, peerID),
			Duration: time.Since(start),
			Address:  firstConnAddr(pd.host, peerID),
		}
		pd.recordMetric(result)
		return result, nil
	}

	// Race: DHT discovery vs relay circuit
	type raceResult struct {
		pathType PathType
		addr     string
		err      error
	}

	resultCh := make(chan raceResult, 2)
	raceCtx, raceCancel := context.WithCancel(ctx)
	defer raceCancel()

	// Leg 1: DHT FindPeer + Connect
	if pd.kdht != nil {
		go func() {
			findCtx, findCancel := context.WithTimeout(raceCtx, 15*time.Second)
			defer findCancel()

			pi, err := pd.kdht.FindPeer(findCtx, peerID)
			if err != nil {
				resultCh <- raceResult{err: fmt.Errorf("DHT: %w", err)}
				return
			}

			connectCtx, connectCancel := context.WithTimeout(raceCtx, 15*time.Second)
			defer connectCancel()

			if err := pd.host.Connect(connectCtx, pi); err != nil {
				resultCh <- raceResult{err: fmt.Errorf("DHT connect: %w", err)}
				return
			}

			resultCh <- raceResult{
				pathType: classifyConnection(pd.host, peerID),
				addr:     firstConnAddr(pd.host, peerID),
			}
		}()
	} else {
		// No DHT - send immediate failure so relay leg can win
		go func() {
			resultCh <- raceResult{err: fmt.Errorf("DHT: not available")}
		}()
	}

	// Leg 2: Relay circuit - race relay candidates with staggered starts (TS-1).
	// Each relay server is an independent "channel" (Tail Slayer pattern).
	// Group addresses by relay peer ID, launch one goroutine per relay with
	// 100ms stagger. First circuit wins, context cancels losers.
	var relayAddrs []string
	if pd.relaySource != nil {
		relayAddrs = pd.relaySource.RelayAddrs()
	}
	if len(relayAddrs) > 0 {
		go func() {
			// Group relay addresses by relay peer ID so each relay server
			// gets its own independent connection attempt.
			relayGroups, err := groupRelayAddrsByPeer(pd.host, relayAddrs, peerID)
			if err != nil || len(relayGroups) == 0 {
				if err != nil {
					resultCh <- raceResult{err: fmt.Errorf("relay addrs: %w", err)}
				} else {
					resultCh <- raceResult{err: fmt.Errorf("relay: no valid relay addresses")}
				}
				return
			}

			// Clear stale backoffs for target (F33-R2-7). groupRelayAddrsByPeer
			// just added fresh circuit addresses to the peerstore, but the swarm
			// checks backoffs BEFORE peerstore — stale entries block new addrs.
			if sw, ok := pd.host.Network().(*swarm.Swarm); ok {
				sw.Backoff().Clear(peerID)
			}

			// Single relay - no need to hedge, just connect directly.
			if len(relayGroups) == 1 {
				connectCtx, connectCancel := context.WithTimeout(raceCtx, 30*time.Second)
				defer connectCancel()
				if err := pd.host.Connect(connectCtx, relayGroups[0]); err != nil {
					resultCh <- raceResult{err: fmt.Errorf("relay connect: %w", err)}
					return
				}
				resultCh <- raceResult{
					pathType: classifyConnection(pd.host, peerID),
					addr:     firstConnAddr(pd.host, peerID),
				}
				return
			}

			// Multiple relays - race with staggered starts (100ms apart).
			// Zero shared state between goroutines (Tail Slayer principle).
			// Cap at 5 relay candidates to bound resource consumption.
			// Budget-filtered relay list is already sorted by health+budget score,
			// so we race the top 5 candidates.
			const maxRelayRace = 5
			if len(relayGroups) > maxRelayRace {
				relayGroups = relayGroups[:maxRelayRace]
			}
			relayWinner := make(chan raceResult, len(relayGroups))
			relayCtx, relayCancel := context.WithTimeout(raceCtx, 30*time.Second)
			defer relayCancel()

			for i, ai := range relayGroups {
				go func(idx int, addrInfo peer.AddrInfo) {
					// Every goroutine MUST send exactly one result to relayWinner.
					// Failing to send causes the collector to deadlock.

					// Staggered start: first relay fires immediately,
					// subsequent relays wait idx*100ms. Prevents thundering herd.
					if idx > 0 {
						stagger := time.NewTimer(time.Duration(idx) * 100 * time.Millisecond)
						select {
						case <-relayCtx.Done():
							stagger.Stop()
							relayWinner <- raceResult{err: fmt.Errorf("relay[%d]: cancelled during stagger", idx)}
							return
						case <-stagger.C:
						}
					}
					if err := pd.host.Connect(relayCtx, addrInfo); err != nil {
						relayWinner <- raceResult{err: fmt.Errorf("relay[%d] connect: %w", idx, err)}
						return
					}
					relayWinner <- raceResult{
						pathType: classifyConnection(pd.host, peerID),
						addr:     firstConnAddr(pd.host, peerID),
					}
				}(i, ai)
			}

			// Collect results: first success wins, cancel the rest.
			// Loop expects exactly len(relayGroups) results (guaranteed by goroutine contract above).
			var relayErr error
			for range relayGroups {
				r := <-relayWinner
				if r.err == nil {
					relayCancel() // cancel losing relays
					resultCh <- r
					// No drain needed: relayWinner is buffered with
					// len(relayGroups) capacity, so remaining goroutines
					// can always send without blocking.
					return
				}
				if relayErr == nil {
					relayErr = r.err
				}
			}
			// All relays failed.
			resultCh <- raceResult{err: fmt.Errorf("all relays failed: %v", relayErr)}
		}()
	} else {
		go func() {
			resultCh <- raceResult{err: fmt.Errorf("relay: no relay addresses configured; add one with 'shurli relay add <address>'")}
		}()
	}

	// Wait for the first success or both failures
	var firstErr, secondErr error
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case r := <-resultCh:
			if r.err == nil {
				// Winner - cancel the other leg
				raceCancel()
				result := &DialResult{
					PathType: r.pathType,
					Duration: time.Since(start),
					Address:  r.addr,
				}
				pd.recordMetric(result)
				return result, nil
			}
			if firstErr == nil {
				firstErr = r.err
			} else {
				secondErr = r.err
			}
		}
	}

	// Both failed
	pd.recordFailure()
	return nil, fmt.Errorf("all paths failed: %v; %v", firstErr, secondErr)
}

// recordMetric records a successful dial in Prometheus.
func (pd *PathDialer) recordMetric(r *DialResult) {
	if pd.metrics == nil {
		return
	}
	pd.metrics.PathDialTotal.WithLabelValues(string(r.PathType), "success").Inc()
	pd.metrics.PathDialDurationSeconds.WithLabelValues(string(r.PathType)).Observe(r.Duration.Seconds())
}

// recordFailure records a failed dial in Prometheus.
func (pd *PathDialer) recordFailure() {
	if pd.metrics == nil {
		return
	}
	pd.metrics.PathDialTotal.WithLabelValues("none", "failure").Inc()
}

// classifyConnection determines the PathType for an existing connection to a peer.
func classifyConnection(h host.Host, peerID peer.ID) PathType {
	conns := h.Network().ConnsToPeer(peerID)
	for _, conn := range conns {
		if !conn.Stat().Limited {
			return PathDirect
		}
	}
	return PathRelayed
}

// firstConnAddr returns the remote multiaddr of the first connection to the peer.
func firstConnAddr(h host.Host, peerID peer.ID) string {
	conns := h.Network().ConnsToPeer(peerID)
	if len(conns) > 0 {
		return conns[0].RemoteMultiaddr().String()
	}
	return ""
}

// PeerConnInfo returns the path type ("DIRECT" or "RELAYED") and best remote
// address for an existing connection to a peer. Prefers direct connections.
func PeerConnInfo(h host.Host, peerID peer.ID) (pathType string, addr string) {
	conns := h.Network().ConnsToPeer(peerID)
	for _, conn := range conns {
		if !conn.Stat().Limited {
			return string(PathDirect), conn.RemoteMultiaddr().String()
		}
	}
	if len(conns) > 0 {
		return string(PathRelayed), conns[0].RemoteMultiaddr().String()
	}
	return "", ""
}

// ClassifyMultiaddr determines path type and extracts transport and IP version
// from a multiaddr string. Used by PathTracker and status display.
func ClassifyMultiaddr(addr string) (pathType PathType, transport string, ipVersion string) {
	if strings.Contains(addr, "/p2p-circuit") {
		pathType = PathRelayed
	} else {
		pathType = PathDirect
	}

	switch {
	case strings.Contains(addr, "/quic-v1"):
		transport = "quic"
	case strings.Contains(addr, "/quic"):
		transport = "quic"
	case strings.Contains(addr, "/ws"):
		transport = "websocket"
	case strings.Contains(addr, "/tcp"):
		transport = "tcp"
	default:
		transport = "unknown"
	}

	switch {
	case strings.Contains(addr, "/ip6/"):
		ipVersion = "ipv6"
	case strings.Contains(addr, "/ip4/"):
		ipVersion = "ipv4"
	default:
		ipVersion = "unknown"
	}

	return
}

// AddRelayAddressesForPeerFunc adds relay circuit addresses to the peerstore
// for a target peer. This is the standalone version that works with any host,
// matching the pattern from Network.AddRelayAddressesForPeer().
func AddRelayAddressesForPeerFunc(h host.Host, relayAddrs []string, target peer.ID) error {
	for _, relayAddr := range relayAddrs {
		circuitAddr := relayAddr + "/p2p-circuit/p2p/" + target.String()
		addrInfo, err := peer.AddrInfoFromString(circuitAddr)
		if err != nil {
			return fmt.Errorf("failed to parse relay circuit address %s: %w", circuitAddr, err)
		}
		h.Peerstore().AddAddrs(addrInfo.ID, addrInfo.Addrs, time.Hour)
	}
	return nil
}

// groupRelayAddrsByPeer groups relay addresses by relay server peer ID, returning
// one peer.AddrInfo per relay server with circuit addresses targeting the given peer.
// Each group becomes an independent hedged connection attempt (TS-1).
// Also adds all circuit addresses to the peerstore.
func groupRelayAddrsByPeer(h host.Host, relayAddrs []string, target peer.ID) ([]peer.AddrInfo, error) {
	// Indexed by relay peer ID position in result slice.
	var groupAddrs [][]ma.Multiaddr
	seen := make(map[peer.ID]int) // relay peer ID -> index in groupAddrs

	for _, relayAddr := range relayAddrs {
		circuitAddr := relayAddr + "/p2p-circuit/p2p/" + target.String()
		addrInfo, err := peer.AddrInfoFromString(circuitAddr)
		if err != nil {
			slog.Debug("pathdialer: skipping unparseable circuit address",
				"relay_addr", relayAddr, "error", err)
			continue
		}
		// Extract relay peer ID from the relay address to group by relay server.
		relayMaddr, err := ma.NewMultiaddr(relayAddr)
		if err != nil {
			slog.Debug("pathdialer: skipping invalid relay multiaddr",
				"relay_addr", relayAddr, "error", err)
			continue
		}
		relayAI, err := peer.AddrInfoFromP2pAddr(relayMaddr)
		if err != nil {
			slog.Debug("pathdialer: skipping relay addr without peer ID",
				"relay_addr", relayAddr, "error", err)
			continue
		}
		h.Peerstore().AddAddrs(addrInfo.ID, addrInfo.Addrs, time.Hour)

		if idx, ok := seen[relayAI.ID]; ok {
			groupAddrs[idx] = append(groupAddrs[idx], addrInfo.Addrs...)
		} else {
			seen[relayAI.ID] = len(groupAddrs)
			groupAddrs = append(groupAddrs, addrInfo.Addrs)
		}
	}

	// Convert to peer.AddrInfo slice (target peer ID with per-relay circuit addrs).
	result := make([]peer.AddrInfo, len(groupAddrs))
	for i, addrs := range groupAddrs {
		result[i] = peer.AddrInfo{ID: target, Addrs: addrs}
	}
	return result, nil
}

