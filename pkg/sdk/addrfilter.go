package sdk

import (
	"net"

	ma "github.com/multiformats/go-multiaddr"
)

// FilterTCPAddrs returns only multiaddrs using raw TCP transport.
// Excludes QUIC/UDP, WebSocket (/ws, /wss), WebRTC, and circuit relay addrs.
// Independent of mdns.go's unexported filterTCPAddrs (R14-I3: keeps mdns.go frozen).
func FilterTCPAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var tcp []ma.Multiaddr
	for _, addr := range addrs {
		hasTCP := false
		excluded := false
		ma.ForEach(addr, func(c ma.Component) bool {
			switch c.Protocol().Code {
			case ma.P_TCP:
				hasTCP = true
			case ma.P_UDP, ma.P_WS, ma.P_WSS, ma.P_CIRCUIT:
				excluded = true
				return false
			}
			return true
		})
		if hasTCP && !excluded {
			tcp = append(tcp, addr)
		}
	}
	return tcp
}

// FilterLANAddrs returns only LAN multiaddrs: private IPv4 (RFC 1918 + CGNAT)
// and IPv6 link-local (fe80::/10). Independent of mdns.go's unexported filterLANAddrs.
func FilterLANAddrs(addrs []ma.Multiaddr) []ma.Multiaddr {
	var lan []ma.Multiaddr
	for _, addr := range addrs {
		first, _ := ma.SplitFirst(addr)
		if first == nil {
			continue
		}
		switch first.Protocol().Code {
		case ma.P_IP4:
			ip := net.ParseIP(first.Value())
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip.IsPrivate() || isCGNAT(ip) {
				lan = append(lan, addr)
			}
		case ma.P_IP6:
			ip := net.ParseIP(first.Value())
			if ip != nil && ip.IsLinkLocalUnicast() {
				lan = append(lan, addr)
			}
		}
	}
	return lan
}

// isCGNAT returns true for 100.64.0.0/10 (RFC 6598 Carrier-Grade NAT).
func isCGNAT(ip net.IP) bool {
	ip4 := ip.To4()
	return len(ip4) == 4 && ip4[0] == 100 && ip4[1]&0xC0 == 64
}
