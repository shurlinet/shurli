package main

import (
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/libp2p/go-libp2p/core/peer"
)

const defaultRelayPort = 7777

// parseRelayHostPort parses a relay address given as "IP:PORT" or bare "IP".
// Returns the IP, port, and any error. If no port is provided, defaultRelayPort is used.
//
// Accepted formats:
//   - "1.2.3.4:7777"         → IPv4 with port
//   - "1.2.3.4"              → IPv4 with default port
//   - "[2600:3c00::1]:7777"  → IPv6 with port
//   - "[2600:3c00::1]"       → IPv6 with default port (brackets stripped)
//   - "2600:3c00::1"         → IPv6 with default port (bare)
//
// Hostnames are rejected  - only IP addresses are accepted (no DNS resolution).
func parseRelayHostPort(input string) (ip string, port int, err error) {
	// Try host:port (handles both IPv4 "1.2.3.4:7777" and IPv6 "[::1]:7777")
	host, portStr, splitErr := net.SplitHostPort(input)
	if splitErr == nil {
		p, e := strconv.Atoi(portStr)
		if e != nil || p <= 0 || p > 65535 {
			return "", 0, fmt.Errorf("invalid port: %s", portStr)
		}
		if net.ParseIP(host) == nil {
			return "", 0, fmt.Errorf("invalid IP address: %s (hostnames not accepted)", host)
		}
		return host, p, nil
	}

	// Strip brackets from "[IPv6]" without port
	bare := input
	if strings.HasPrefix(bare, "[") && strings.HasSuffix(bare, "]") {
		bare = bare[1 : len(bare)-1]
	}

	// Try as bare IP (IPv4 or IPv6)
	if net.ParseIP(bare) != nil {
		return bare, defaultRelayPort, nil
	}

	return "", 0, fmt.Errorf("expected IP:PORT, IP address, or full multiaddr starting with /")
}

// buildRelayMultiaddr constructs a multiaddr string from IP, port, and peer ID.
func buildRelayMultiaddr(ip string, port int, peerID string) string {
	ipVer := "ip4"
	if strings.Contains(ip, ":") {
		ipVer = "ip6"
	}
	return fmt.Sprintf("/%s/%s/tcp/%d/p2p/%s", ipVer, ip, port, peerID)
}

// isFullMultiaddr returns true if the input looks like a complete multiaddr.
func isFullMultiaddr(input string) bool {
	return strings.HasPrefix(input, "/")
}

// validatePeerID checks if a string is a valid libp2p peer ID.
func validatePeerID(s string) error {
	_, err := peer.Decode(s)
	return err
}
