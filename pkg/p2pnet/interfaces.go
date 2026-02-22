package p2pnet

import (
	"fmt"
	"net"
	"sort"
)

// InterfaceInfo describes a single network interface with its global unicast addresses.
type InterfaceInfo struct {
	Name       string   `json:"name"`
	IPv4Addrs  []string `json:"ipv4_addrs,omitempty"`
	IPv6Addrs  []string `json:"ipv6_addrs,omitempty"`
	IsLoopback bool     `json:"is_loopback"`
}

// InterfaceSummary is the result of DiscoverInterfaces. It provides a
// snapshot of all network interfaces with global unicast addresses and
// convenience flags for IPv4/IPv6 availability.
type InterfaceSummary struct {
	Interfaces      []InterfaceInfo `json:"interfaces"`
	HasGlobalIPv6   bool           `json:"has_global_ipv6"`
	HasGlobalIPv4   bool           `json:"has_global_ipv4"`
	GlobalIPv6Addrs []string       `json:"global_ipv6_addrs,omitempty"`
	GlobalIPv4Addrs []string       `json:"global_ipv4_addrs,omitempty"`
}

// DiscoverInterfaces enumerates all network interfaces, filters for global
// unicast addresses, and returns a summary. Link-local, ULA, and private
// IPv4 addresses are excluded from the global lists but the interface itself
// is still reported (for debugging).
func DiscoverInterfaces() (*InterfaceSummary, error) {
	return discoverInterfacesFrom(net.Interfaces)
}

// discoverInterfacesFrom is the testable core. It accepts a function matching
// net.Interfaces so tests can inject synthetic interface lists.
func discoverInterfacesFrom(listFn func() ([]net.Interface, error)) (*InterfaceSummary, error) {
	ifaces, err := listFn()
	if err != nil {
		return nil, fmt.Errorf("enumerate interfaces: %w", err)
	}

	summary := &InterfaceSummary{}

	for _, iface := range ifaces {
		// Skip interfaces that are down
		if iface.Flags&net.FlagUp == 0 {
			continue
		}

		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}

		info := InterfaceInfo{
			Name:       iface.Name,
			IsLoopback: iface.Flags&net.FlagLoopback != 0,
		}

		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP

			if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
				continue
			}

			if ip.To4() != nil {
				// IPv4
				if isGlobalIPv4(ip) {
					info.IPv4Addrs = append(info.IPv4Addrs, ip.String())
					summary.GlobalIPv4Addrs = append(summary.GlobalIPv4Addrs, ip.String())
					summary.HasGlobalIPv4 = true
				}
			} else if len(ip) == net.IPv6len {
				// IPv6 - exclude ULA (fc00::/7)
				if isGlobalIPv6(ip) {
					info.IPv6Addrs = append(info.IPv6Addrs, ip.String())
					summary.GlobalIPv6Addrs = append(summary.GlobalIPv6Addrs, ip.String())
					summary.HasGlobalIPv6 = true
				}
			}
		}

		// Include interface if it has any global addresses or is loopback
		if len(info.IPv4Addrs) > 0 || len(info.IPv6Addrs) > 0 || info.IsLoopback {
			summary.Interfaces = append(summary.Interfaces, info)
		}
	}

	// Sort for stable output
	sort.Slice(summary.Interfaces, func(i, j int) bool {
		return summary.Interfaces[i].Name < summary.Interfaces[j].Name
	})

	return summary, nil
}

// isGlobalIPv4 returns true if the IPv4 address is globally routable
// (not private, not loopback, not link-local, not CGNAT).
func isGlobalIPv4(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// RFC 1918 private
	if ip4[0] == 10 {
		return false
	}
	if ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31 {
		return false
	}
	if ip4[0] == 192 && ip4[1] == 168 {
		return false
	}
	// CGNAT (100.64.0.0/10)
	if ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127 {
		return false
	}
	// Link-local (169.254.0.0/16)
	if ip4[0] == 169 && ip4[1] == 254 {
		return false
	}
	return ip4.IsGlobalUnicast()
}

// isGlobalIPv6 returns true if the IPv6 address is globally routable
// (not ULA, not link-local).
func isGlobalIPv6(ip net.IP) bool {
	if len(ip) != net.IPv6len {
		return false
	}
	// ULA: fc00::/7
	if (ip[0] & 0xfe) == 0xfc {
		return false
	}
	return ip.IsGlobalUnicast()
}
