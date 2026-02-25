package main

import (
	"testing"
)

func TestParseRelayHostPort(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantIP  string
		wantP   int
		wantErr bool
	}{
		// IPv4
		{"ipv4 with port", "203.0.113.50:7777", "203.0.113.50", 7777, false},
		{"ipv4 bare", "203.0.113.50", "203.0.113.50", 7777, false},
		{"ipv4 custom port", "10.0.0.1:9999", "10.0.0.1", 9999, false},

		// IPv6
		{"ipv6 bracketed with port", "[2600:3c00::1]:7777", "2600:3c00::1", 7777, false},
		{"ipv6 bracketed no port", "[2600:3c00::1]", "2600:3c00::1", 7777, false},
		{"ipv6 bare", "2600:3c00::1", "2600:3c00::1", 7777, false},
		{"ipv6 loopback bracketed", "[::1]:8080", "::1", 8080, false},
		{"ipv6 loopback bare", "::1", "::1", 7777, false},

		// Invalid
		{"hostname rejected", "relay.example.com:7777", "", 0, true},
		{"bare hostname rejected", "relay.example.com", "", 0, true},
		{"empty", "", "", 0, true},
		{"port zero", "1.2.3.4:0", "", 0, true},
		{"port too high", "1.2.3.4:70000", "", 0, true},
		{"port negative", "1.2.3.4:-1", "", 0, true},
		{"gibberish", "not-an-ip", "", 0, true},
		{"multiaddr should use isFullMultiaddr", "/ip4/1.2.3.4/tcp/7777", "", 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ip, port, err := parseRelayHostPort(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("parseRelayHostPort(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if ip != tt.wantIP {
				t.Errorf("parseRelayHostPort(%q) ip = %q, want %q", tt.input, ip, tt.wantIP)
			}
			if port != tt.wantP {
				t.Errorf("parseRelayHostPort(%q) port = %d, want %d", tt.input, port, tt.wantP)
			}
		})
	}
}

func TestBuildRelayMultiaddr(t *testing.T) {
	tests := []struct {
		ip     string
		port   int
		peerID string
		want   string
	}{
		{"203.0.113.50", 7777, "12D3KooWTest", "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWTest"},
		{"2600:3c00::1", 7777, "12D3KooWTest", "/ip6/2600:3c00::1/tcp/7777/p2p/12D3KooWTest"},
		{"10.0.0.1", 9999, "12D3KooWTest", "/ip4/10.0.0.1/tcp/9999/p2p/12D3KooWTest"},
	}

	for _, tt := range tests {
		got := buildRelayMultiaddr(tt.ip, tt.port, tt.peerID)
		if got != tt.want {
			t.Errorf("buildRelayMultiaddr(%q, %d, %q) = %q, want %q", tt.ip, tt.port, tt.peerID, got, tt.want)
		}
	}
}

func TestIsFullMultiaddr(t *testing.T) {
	if !isFullMultiaddr("/ip4/1.2.3.4/tcp/7777/p2p/12D3KooW...") {
		t.Error("should detect multiaddr")
	}
	if isFullMultiaddr("203.0.113.50:7777") {
		t.Error("should not detect IP:PORT as multiaddr")
	}
	if isFullMultiaddr("[::1]:7777") {
		t.Error("should not detect IPv6 as multiaddr")
	}
}
