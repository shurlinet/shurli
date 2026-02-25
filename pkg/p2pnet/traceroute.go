package p2pnet

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// TraceHop represents a single hop in a P2P traceroute.
type TraceHop struct {
	Hop      int     `json:"hop"`
	PeerID   string  `json:"peer_id"`
	Name     string  `json:"name,omitempty"`     // friendly name if known
	Address  string  `json:"address,omitempty"`   // multiaddr of this hop
	RttMs    float64 `json:"rtt_ms"`
	Error    string  `json:"error,omitempty"`
}

// TraceResult holds the full traceroute output.
type TraceResult struct {
	Target   string     `json:"target"`
	TargetID string     `json:"target_id"`
	Path     string     `json:"path"`   // "DIRECT" or "RELAYED"
	Hops     []TraceHop `json:"hops"`
}

// TracePeer traces the network path to a peer.
//
// libp2p doesn't support TTL-based tracing, so this determines the path
// by inspecting connection metadata: is the connection direct or relayed?
// If relayed, it measures RTT to the relay and to the target separately
// to show per-hop latency  - the information that actually matters for debugging.
func TracePeer(ctx context.Context, h host.Host, targetPeerID peer.ID) (*TraceResult, error) {
	result := &TraceResult{
		TargetID: targetPeerID.String(),
	}

	// Check existing connections to the target
	conns := h.Network().ConnsToPeer(targetPeerID)
	if len(conns) == 0 {
		return nil, fmt.Errorf("not connected to peer %s", targetPeerID.String()[:16]+"...")
	}

	// Find the best connection (prefer direct over relayed)
	var relayPeerID peer.ID
	isRelayed := false
	var connAddr string

	for _, conn := range conns {
		addr := conn.RemoteMultiaddr().String()
		if !conn.Stat().Limited && !strings.Contains(addr, "/p2p-circuit") {
			// Direct connection found
			connAddr = addr
			isRelayed = false
			break
		}
		// Relayed connection
		isRelayed = true
		connAddr = addr

		// Extract relay peer ID from the circuit address
		// Format: /ip4/.../tcp/.../p2p/<relay-id>/p2p-circuit/p2p/<target-id>
		parts := strings.Split(addr, "/p2p-circuit")
		if len(parts) >= 1 {
			relayAddr := parts[0]
			// Find the /p2p/<id> component in the relay part
			p2pIdx := strings.LastIndex(relayAddr, "/p2p/")
			if p2pIdx >= 0 {
				relayIDStr := relayAddr[p2pIdx+5:]
				if pid, err := peer.Decode(relayIDStr); err == nil {
					relayPeerID = pid
				}
			}
		}
	}

	if isRelayed {
		result.Path = "RELAYED"
		hopNum := 1

		// Hop 1: Relay server
		if relayPeerID != "" {
			relayHop := TraceHop{
				Hop:    hopNum,
				PeerID: relayPeerID.String(),
			}

			// Measure RTT to relay
			relayRTT, err := measurePeerRTT(ctx, h, relayPeerID)
			if err != nil {
				relayHop.Error = err.Error()
			} else {
				relayHop.RttMs = relayRTT
			}

			// Get relay address (non-circuit part)
			relayConns := h.Network().ConnsToPeer(relayPeerID)
			for _, rc := range relayConns {
				addr := rc.RemoteMultiaddr().String()
				if !strings.Contains(addr, "/p2p-circuit") {
					relayHop.Address = addr
					break
				}
			}

			// Check peerstore for agent version (relay name)
			if av, err := h.Peerstore().Get(relayPeerID, "AgentVersion"); err == nil {
				if agent, ok := av.(string); ok {
					relayHop.Name = agent
				}
			}

			result.Hops = append(result.Hops, relayHop)
			hopNum++
		}

		// Hop 2: Target via relay
		targetHop := TraceHop{
			Hop:     hopNum,
			PeerID:  targetPeerID.String(),
			Address: connAddr,
		}

		targetRTT, err := measurePeerRTT(ctx, h, targetPeerID)
		if err != nil {
			targetHop.Error = err.Error()
		} else {
			targetHop.RttMs = targetRTT
		}

		result.Hops = append(result.Hops, targetHop)
	} else {
		result.Path = "DIRECT"

		// Single hop: direct to target
		targetHop := TraceHop{
			Hop:     1,
			PeerID:  targetPeerID.String(),
			Address: connAddr,
		}

		rtt, err := measurePeerRTT(ctx, h, targetPeerID)
		if err != nil {
			targetHop.Error = err.Error()
		} else {
			targetHop.RttMs = rtt
		}

		result.Hops = append(result.Hops, targetHop)
	}

	return result, nil
}

// measurePeerRTT measures round-trip time to a peer using libp2p's built-in
// ping protocol (not our custom ping-pong). Falls back to a stream open/close
// timing if the ping service isn't available.
func measurePeerRTT(ctx context.Context, h host.Host, peerID peer.ID) (float64, error) {
	// Use a quick connect+identify roundtrip as RTT measurement.
	// Open a stream with a short protocol that the peer should reject quickly.
	// The time for the rejection is approximately 1 RTT.
	measureCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	start := time.Now()

	// Try to open a stream with a probe protocol  - the peer will either
	// accept (unlikely) or reject it quickly. Either way, the round-trip
	// gives us RTT.
	probeCtx := network.WithAllowLimitedConn(measureCtx, "/shurli/rtt-probe/1.0.0")
	s, err := h.NewStream(probeCtx, peerID, "/shurli/rtt-probe/1.0.0")
	rtt := time.Since(start)

	if err != nil {
		// "protocol not supported" errors still give us RTT
		// Only actual connection failures are real errors
		errStr := err.Error()
		if strings.Contains(errStr, "protocol not supported") ||
			strings.Contains(errStr, "protocols not supported") {
			return float64(rtt.Microseconds()) / 1000.0, nil
		}
		return 0, fmt.Errorf("cannot reach peer: %s", truncateError(errStr))
	}

	// Stream opened somehow  - close it and use the RTT
	s.Close()
	return float64(rtt.Microseconds()) / 1000.0, nil
}
