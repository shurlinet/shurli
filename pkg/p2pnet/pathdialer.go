package p2pnet

import (
	"context"
	"fmt"
	"strings"
	"time"

	dht "github.com/libp2p/go-libp2p-kad-dht"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
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
	host       host.Host
	kdht       *dht.IpfsDHT // may be nil (no DHT)
	relayAddrs []string
	metrics    *Metrics // nil-safe
}

// NewPathDialer creates a PathDialer. The DHT and metrics are optional (nil-safe).
func NewPathDialer(h host.Host, kdht *dht.IpfsDHT, relayAddrs []string, m *Metrics) *PathDialer {
	return &PathDialer{
		host:       h,
		kdht:       kdht,
		relayAddrs: relayAddrs,
		metrics:    m,
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

	// Leg 2: Relay circuit
	if len(pd.relayAddrs) > 0 {
		go func() {
			if err := AddRelayAddressesForPeerFunc(pd.host, pd.relayAddrs, peerID); err != nil {
				resultCh <- raceResult{err: fmt.Errorf("relay addrs: %w", err)}
				return
			}

			connectCtx, connectCancel := context.WithTimeout(raceCtx, 30*time.Second)
			defer connectCancel()

			if err := pd.host.Connect(connectCtx, peer.AddrInfo{ID: peerID}); err != nil {
				resultCh <- raceResult{err: fmt.Errorf("relay connect: %w", err)}
				return
			}

			resultCh <- raceResult{
				pathType: classifyConnection(pd.host, peerID),
				addr:     firstConnAddr(pd.host, peerID),
			}
		}()
	} else {
		go func() {
			resultCh <- raceResult{err: fmt.Errorf("relay: no relay addresses configured")}
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
