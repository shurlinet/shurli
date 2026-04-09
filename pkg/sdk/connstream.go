package sdk

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	basichost "github.com/libp2p/go-libp2p/p2p/host/basic"
	multistream "github.com/multiformats/go-multistream"
)

const (
	// streamNegotiationTimeout is the deadline for multistream-select protocol
	// negotiation on a raw stream. Prevents slowloris on dead/slow connections.
	// Healthy connections negotiate in <100ms. 10 seconds is generous.
	streamNegotiationTimeout = 10 * time.Second
)

// ConnGroup represents a group of connections that share the same failure domain.
// Connections in the same group are NOT independent — hedging across them wastes
// resources for zero benefit.
//
// Groups are classified by path type:
//   - "direct": all non-relay connections (same physical network path)
//   - "relay-<peerID>": connections through a specific relay server
//
// TS-4 design: hedge picks ONE connection from each group.
type ConnGroup struct {
	Type  string         // "direct" or "relay-<relayPeerID>"
	Conns []network.Conn // connections in this group
}

// HostNetwork is the interface satisfied by host.Host — used to avoid importing
// the full host package for ConnGroups.
type HostNetwork interface {
	Network() network.Network
}

// Compile-time check: basichost.BasicHost satisfies HostNetwork.
var _ HostNetwork = (*basichost.BasicHost)(nil)

// ConnGroups classifies all connections to a peer into independent groups.
// Returns one group per independent failure domain. Direct connections are
// always a single group (same NIC, same cable, same switch — not independent).
// Relay connections are grouped by relay server peer ID.
//
// Returns nil if no connections exist to the peer.
func ConnGroups(h HostNetwork, peerID peer.ID) []ConnGroup {
	conns := h.Network().ConnsToPeer(peerID)
	if len(conns) == 0 {
		return nil
	}

	groups := make(map[string]*ConnGroup)

	for _, conn := range conns {
		key := classifyConnGroup(conn)
		if g, ok := groups[key]; ok {
			g.Conns = append(g.Conns, conn)
		} else {
			groups[key] = &ConnGroup{Type: key, Conns: []network.Conn{conn}}
		}
	}

	result := make([]ConnGroup, 0, len(groups))
	for _, g := range groups {
		result = append(result, *g)
	}
	return result
}

// classifyConnGroup determines which failure domain a connection belongs to.
// Relay connections are identified by the relay server's peer ID extracted
// from the circuit address. Direct connections all share one group.
func classifyConnGroup(conn network.Conn) string {
	addr := conn.RemoteMultiaddr().String()
	if !strings.Contains(addr, "/p2p-circuit") {
		return "direct"
	}
	// Extract relay peer ID from circuit address.
	// Circuit addrs look like: /ip4/1.2.3.4/tcp/7777/p2p/<relayPeerID>/p2p-circuit/p2p/<targetPeerID>
	// The relay peer ID is the segment before /p2p-circuit.
	parts := strings.Split(addr, "/p2p-circuit")
	if len(parts) < 2 {
		return "relay-unknown"
	}
	relayPart := parts[0]
	// Find the last /p2p/<peerID> in the relay part.
	segments := strings.Split(relayPart, "/p2p/")
	if len(segments) < 2 {
		return "relay-unknown"
	}
	relayPeerID := segments[len(segments)-1]
	// Strip any trailing segments (shouldn't exist, but be safe).
	if idx := strings.IndexByte(relayPeerID, '/'); idx >= 0 {
		relayPeerID = relayPeerID[:idx]
	}
	return "relay-" + relayPeerID
}

// OpenStreamOnConn opens a protocol-negotiated stream on a specific connection.
// This is the primitive needed for hedging across independent paths — it bypasses
// host.NewStream's automatic connection selection.
//
// Uses go-multistream's eager SelectProtoOrFail for protocol negotiation.
// Sets a 10-second deadline for negotiation to prevent slowloris on stalled connections.
// The caller should extend/clear the deadline after this returns.
//
// On negotiation failure, the raw stream is properly Reset() to prevent leaks (R2-C2).
func OpenStreamOnConn(ctx context.Context, conn network.Conn, proto protocol.ID) (network.Stream, error) {
	s, err := conn.NewStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("new stream: %w", err)
	}

	// Prevent negotiation from blocking forever on a dead/slow connection (R3-I3).
	s.SetDeadline(time.Now().Add(streamNegotiationTimeout))

	// SelectProtoOrFail does eager multistream-select negotiation:
	// sends /multistream/1.0.0 + protocol ID, waits for server confirmation.
	// Compatible with libp2p's server-side multistream muxer.
	if err := multistream.SelectProtoOrFail(string(proto), s); err != nil {
		s.Reset() // Clean up raw stream on negotiation failure.
		return nil, fmt.Errorf("negotiate %s: %w", proto, err)
	}

	return s, nil
}
