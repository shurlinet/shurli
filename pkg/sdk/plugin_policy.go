package sdk

import (
	"net"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// TransportType classifies how a peer connection is established.
// Used as a bitmask to express which transports a plugin permits.
type TransportType int

const (
	// TransportLAN is a connection over the local network (private/link-local IP).
	TransportLAN TransportType = 1 << iota

	// TransportDirect is a connection over the public internet (non-relay, non-LAN).
	TransportDirect

	// TransportRelay is a connection mediated through a relay (p2p-circuit).
	TransportRelay
)

// DefaultTransport permits LAN and Direct connections. Relay is excluded.
// This is the default for ALL plugins: no data flows through relays unless
// explicitly allowed per-plugin.
const DefaultTransport = TransportLAN | TransportDirect

// PluginPolicy defines transport restrictions and peer access control
// for a plugin registered through the ServiceRegistry.
//
// By design, plugins are peer-to-peer only. Relay server protocols live
// in internal/relay and register directly on the host, completely outside
// the plugin system. This separation is architectural and non-negotiable.
type PluginPolicy struct {
	// AllowedTransports is a bitmask of permitted connection types.
	// Default: TransportLAN | TransportDirect (relay excluded).
	AllowedTransports TransportType

	// AllowPeers restricts the plugin to only these peers.
	// nil = all authorized peers allowed (subject to DenyPeers).
	AllowPeers map[peer.ID]struct{}

	// DenyPeers blocks these peers from using this plugin.
	// Checked before AllowPeers (deny takes precedence).
	DenyPeers map[peer.ID]struct{}
}

// DefaultPluginPolicy returns a policy that allows LAN + Direct only,
// with no peer restrictions. This is applied to every plugin registered
// via RegisterHandler unless overridden.
func DefaultPluginPolicy() *PluginPolicy {
	return &PluginPolicy{
		AllowedTransports: DefaultTransport,
	}
}

// RelayAllowed returns true if the policy permits relay connections.
func (p *PluginPolicy) RelayAllowed() bool {
	return p.AllowedTransports&TransportRelay != 0
}

// PeerAllowed returns true if the given peer is permitted by this policy.
// Deny list is checked first (deny wins over allow).
func (p *PluginPolicy) PeerAllowed(id peer.ID) bool {
	if p.DenyPeers != nil {
		if _, denied := p.DenyPeers[id]; denied {
			return false
		}
	}
	if p.AllowPeers != nil {
		_, allowed := p.AllowPeers[id]
		return allowed
	}
	return true
}

// TransportAllowed returns true if the given transport type is permitted.
func (p *PluginPolicy) TransportAllowed(t TransportType) bool {
	return p.AllowedTransports&t != 0
}

// ClassifyTransport determines the transport type of a libp2p stream.
//
//   - Limited connections (Stat().Limited) are classified as TransportRelay.
//   - Private, loopback, or link-local IPs are classified as TransportLAN.
//   - Everything else is TransportDirect.
//
// Note: two LAN machines connected via public IPv6 will be classified as
// TransportDirect. Use ClassifyPeerTransport when you have access to the
// host network and need LAN detection across all connections to a peer.
func ClassifyTransport(s network.Stream) TransportType {
	if s.Conn().Stat().Limited {
		return TransportRelay
	}

	// Extract IP from the first multiaddr component to distinguish LAN vs Direct.
	addr := s.Conn().RemoteMultiaddr()
	first, _ := ma.SplitFirst(addr)
	if first != nil {
		ip := net.ParseIP(first.Value())
		if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
			return TransportLAN
		}
	}

	return TransportDirect
}

// ClassifyPeerTransport is like ClassifyTransport but also checks all other
// connections to the same peer. If any connection uses a private IPv4 address
// (i.e. the peer is on the same LAN), the result is TransportLAN even if the
// stream itself uses a public IPv6 address. This prevents erasure coding and
// other WAN-only behaviors between two LAN machines connected via public IPv6.
func ClassifyPeerTransport(s network.Stream, net network.Network) TransportType {
	t := ClassifyTransport(s)
	if t != TransportDirect {
		return t
	}

	// Stream is on a public IP. Check if the peer has any LAN connection.
	if AnyConnIsLAN(net.ConnsToPeer(s.Conn().RemotePeer())) {
		return TransportLAN
	}
	return TransportDirect
}
