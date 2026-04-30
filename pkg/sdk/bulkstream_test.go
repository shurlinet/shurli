package sdk

import (
	"testing"

	ma "github.com/multiformats/go-multiaddr"
)

func TestIsTCPConn_Multiaddr(t *testing.T) {
	tests := []struct {
		name string
		addr string
		want bool
	}{
		{"tcp ipv4", "/ip4/192.168.1.100/tcp/7778", true},
		{"tcp ipv6", "/ip6/fe80::1/tcp/7778", true},
		{"quic ipv4", "/ip4/192.168.1.100/udp/7778/quic-v1", false},
		{"quic ipv6", "/ip6/2001:db8::1/udp/7778/quic-v1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := ma.StringCast(tt.addr)
			hasTCP := false
			hasUDP := false
			ma.ForEach(addr, func(c ma.Component) bool {
				switch c.Protocol().Code {
				case ma.P_TCP:
					hasTCP = true
				case ma.P_UDP:
					hasUDP = true
				}
				return true
			})
			got := hasTCP && !hasUDP
			if got != tt.want {
				t.Errorf("isTCPConn(%s) = %v, want %v", tt.addr, got, tt.want)
			}
		})
	}
}

func TestFilterTCPAddrs(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/192.168.1.100/tcp/7778"),
		ma.StringCast("/ip4/192.168.1.100/udp/7778/quic-v1"),
		ma.StringCast("/ip6/fe80::1/tcp/4001"),
	}
	tcp := FilterTCPAddrs(addrs)
	if len(tcp) != 2 {
		t.Fatalf("expected 2 TCP addrs, got %d", len(tcp))
	}
}

func TestFilterLANAddrs(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/192.168.1.100/tcp/7778"),   // private
		ma.StringCast("/ip4/203.0.113.50/tcp/7778"),    // public (doc range)
		ma.StringCast("/ip6/fe80::1/tcp/4001"),          // link-local
		ma.StringCast("/ip6/2001:db8::1/tcp/4001"),     // global v6
		ma.StringCast("/ip4/100.64.0.1/tcp/7778"),       // CGNAT
	}
	lan := FilterLANAddrs(addrs)
	if len(lan) != 3 {
		t.Fatalf("expected 3 LAN addrs (private + link-local + CGNAT), got %d", len(lan))
	}
}

func TestFilterTCPAddrs_Empty(t *testing.T) {
	tcp := FilterTCPAddrs(nil)
	if len(tcp) != 0 {
		t.Fatalf("expected 0 TCP addrs from nil, got %d", len(tcp))
	}
}

func TestFilterTCPAddrs_ExcludesWSAndCircuit(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/192.168.1.100/tcp/7778"),              // raw TCP - keep
		ma.StringCast("/ip4/192.168.1.100/tcp/8080/ws"),            // WebSocket - exclude
		ma.StringCast("/ip4/192.168.1.100/tcp/8443/wss"),           // WSS - exclude
		ma.StringCast("/ip4/192.168.1.100/udp/7778/quic-v1"),       // QUIC - exclude
	}
	tcp := FilterTCPAddrs(addrs)
	if len(tcp) != 1 {
		t.Fatalf("expected 1 raw TCP addr (WS/WSS/QUIC excluded), got %d", len(tcp))
	}
}

func TestFilterLANAddrs_ExcludesLoopback(t *testing.T) {
	addrs := []ma.Multiaddr{
		ma.StringCast("/ip4/127.0.0.1/tcp/7778"),
	}
	lan := FilterLANAddrs(addrs)
	if len(lan) != 0 {
		t.Fatalf("expected 0 LAN addrs (loopback excluded), got %d", len(lan))
	}
}

