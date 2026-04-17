package sdk

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// Stubs used only by VerifiedTransport tests. All IPs are RFC 5737 /
// RFC 3849 / obviously-fake RFC 1918 — never real session values.

type stubConn struct {
	network.Conn
	remoteMA ma.Multiaddr
	remotePID peer.ID
	limited  bool
}

func (c *stubConn) RemoteMultiaddr() ma.Multiaddr { return c.remoteMA }
func (c *stubConn) RemotePeer() peer.ID           { return c.remotePID }
func (c *stubConn) Stat() network.ConnStats {
	return network.ConnStats{Stats: network.Stats{Limited: c.limited}}
}

type stubStream struct {
	network.Stream
	conn *stubConn
}

func (s *stubStream) Conn() network.Conn { return s.conn }

func mustMA(t *testing.T, s string) ma.Multiaddr {
	t.Helper()
	m, err := ma.NewMultiaddr(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return m
}

func TestVerifiedTransport(t *testing.T) {
	// Generic test peer ID — RFC-style placeholder, not a real peer from any node.
	testPID := peer.ID("12D3KooWTestPeerPlaceholder0000000000000000000000")

	tests := []struct {
		name               string
		remoteMA           string
		limited            bool
		hasVerified        bool // what the callback returns for testPID
		nilCallback        bool
		want               TransportType
		wantDescription    string
	}{
		{
			name:            "relay limited conn is relay regardless of IP",
			remoteMA:        "/ip4/203.0.113.5/tcp/4001", // RFC 5737 TEST-NET-3
			limited:         true,
			want:            TransportRelay,
			wantDescription: "Limited (circuit) always relay",
		},
		{
			name:            "loopback IPv4 is LAN without mDNS (same machine)",
			remoteMA:        "/ip4/127.0.0.1/tcp/4001",
			want:            TransportLAN,
			wantDescription: "Loopback can't traverse routers",
		},
		{
			name:            "loopback IPv6 is LAN without mDNS",
			remoteMA:        "/ip6/::1/tcp/4001",
			want:            TransportLAN,
			wantDescription: "IPv6 loopback is same machine",
		},
		{
			name:            "link-local IPv4 is LAN without mDNS",
			remoteMA:        "/ip4/169.254.1.1/tcp/4001",
			want:            TransportLAN,
			wantDescription: "RFC 3927 link-local cannot cross router",
		},
		{
			name:            "link-local IPv6 is LAN without mDNS",
			remoteMA:        "/ip6/fe80::1/tcp/4001",
			want:            TransportLAN,
			wantDescription: "IPv6 link-local can't cross router",
		},
		{
			name:            "routed private IPv4 WITHOUT mDNS verification is Direct",
			remoteMA:        "/ip4/10.99.99.100/udp/4001/quic-v1", // obviously-fake RFC 1918
			hasVerified:     false,
			want:            TransportDirect,
			wantDescription: "The G3 blocker — routed-private without mDNS must be WAN",
		},
		{
			name:            "private IPv4 WITH mDNS verification is LAN",
			remoteMA:        "/ip4/10.99.99.100/udp/4001/quic-v1",
			hasVerified:     true,
			want:            TransportLAN,
			wantDescription: "mDNS-verified private IP is real LAN",
		},
		{
			name:            "public IPv4 without mDNS is Direct",
			remoteMA:        "/ip4/203.0.113.5/tcp/4001", // RFC 5737
			hasVerified:     false,
			want:            TransportDirect,
			wantDescription: "Public IP is never LAN without mDNS",
		},
		{
			name:            "public IPv6 WITH mDNS (two LAN machines on public IPv6)",
			remoteMA:        "/ip6/2001:db8::1/tcp/4001", // RFC 3849
			hasVerified:     true,
			want:            TransportLAN,
			wantDescription: "mDNS-discovered peer on LAN using public IPv6",
		},
		{
			name:            "public IPv6 without mDNS is Direct",
			remoteMA:        "/ip6/2001:db8::1/tcp/4001",
			hasVerified:     false,
			want:            TransportDirect,
			wantDescription: "Public IPv6 without mDNS verification is WAN",
		},
		{
			name:            "nil callback degrades to Direct for private IP",
			remoteMA:        "/ip4/192.168.99.50/udp/4001/quic-v1",
			nilCallback:     true,
			want:            TransportDirect,
			wantDescription: "Nil callback is conservative — treats private as WAN",
		},
		{
			name:            "nil callback still LAN for loopback",
			remoteMA:        "/ip4/127.0.0.1/tcp/4001",
			nilCallback:     true,
			want:            TransportLAN,
			wantDescription: "Nil callback still honors loopback as LAN",
		},
		{
			name:            "CGNAT-range 100.64.0.5 without mDNS is Direct",
			remoteMA:        "/ip4/100.99.99.42/udp/4001/quic-v1", // RFC 6598 CGNAT range
			hasVerified:     false,
			want:            TransportDirect,
			wantDescription: "RFC 6598 CGNAT without mDNS must be WAN (Starlink class)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := &stubStream{conn: &stubConn{
				remoteMA:  mustMA(t, tc.remoteMA),
				remotePID: testPID,
				limited:   tc.limited,
			}}

			var cb func(peer.ID) bool
			if !tc.nilCallback {
				verified := tc.hasVerified
				cb = func(pid peer.ID) bool {
					if pid != testPID {
						t.Errorf("callback called with unexpected peer %s", pid)
					}
					return verified
				}
			}

			got := VerifiedTransport(s, cb)
			if got != tc.want {
				t.Errorf("%s\n  got  = %v\n  want = %v\n  why  = %s",
					tc.name, got, tc.want, tc.wantDescription)
			}
		})
	}
}

// TestNetworkHasVerifiedLANConn_NilSafety verifies the nil-safe paths so a
// daemon that never wired a LANRegistry (or that lost it during teardown)
// degrades to "not LAN" instead of panicking. This matters because the
// daemon wires the callback at startup as a method value
// (`pluginProvider.HasVerifiedLANConn = rt.network.HasVerifiedLANConn`)
// and any panic on first inbound stream would take the daemon down.
func TestNetworkHasVerifiedLANConn_NilSafety(t *testing.T) {
	// Generic test peer ID — not a real peer.
	testPID := peer.ID("12D3KooWNilSafetyTestPeerPlaceholder000000000000")

	t.Run("nil network returns false", func(t *testing.T) {
		var n *Network
		if n.HasVerifiedLANConn(testPID) {
			t.Error("nil network must return false")
		}
	})

	t.Run("nil lanRegistry returns false", func(t *testing.T) {
		n := &Network{} // lanRegistry is nil
		if n.HasVerifiedLANConn(testPID) {
			t.Error("network with nil lanRegistry must return false")
		}
	})
}

// TestVerifiedTransport_RoutedPrivateRegressionG3 pins the exact bug class
// that motivated the verified-LAN migration. A peer reached via a routed
// private IPv4 (here represented generically) MUST classify as Direct
// when mDNS has not verified the peer — not LAN. Misclassification would
// disable RS erasure on a genuinely unreliable WAN link, causing silent
// corruption under network stress.
func TestVerifiedTransport_RoutedPrivateRegressionG3(t *testing.T) {
	// Obviously-fake RFC 1918 address. NEVER use real session IPs in tests.
	const routedPrivate = "/ip4/10.99.99.100/udp/4001/quic-v1"
	testPID := peer.ID("12D3KooWG3RegressionTestPeerPlaceholder0000000000")

	s := &stubStream{conn: &stubConn{
		remoteMA:  mustMA(t, routedPrivate),
		remotePID: testPID,
	}}
	cb := func(pid peer.ID) bool { return false } // mDNS has NOT verified

	got := VerifiedTransport(s, cb)
	if got != TransportDirect {
		t.Fatalf("routed-private-IPv4 without mDNS verification must classify as Direct (got %v)", got)
	}
}
