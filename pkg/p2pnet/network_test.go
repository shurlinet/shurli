package p2pnet

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/internal/config"
)

func TestTruncateError(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short", "connection refused", "connection refused"},
		{"multiline", "first line\nsecond line\nthird", "first line"},
		{"long", strings.Repeat("a", 250), strings.Repeat("a", 200) + "..."},
		{"multiline long first", strings.Repeat("x", 250) + "\nsecond", strings.Repeat("x", 200) + "..."},
		{"empty", "", ""},
		{"exactly 200", strings.Repeat("b", 200), strings.Repeat("b", 200)},
		{"newline at start", "\nsecond line", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateError(tt.input)
			if got != tt.want {
				t.Errorf("truncateError(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseRelayAddrs(t *testing.T) {
	t.Run("valid single", func(t *testing.T) {
		addrs := []string{
			"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
		}
		infos, err := ParseRelayAddrs(addrs)
		if err != nil {
			t.Fatalf("ParseRelayAddrs: %v", err)
		}
		if len(infos) != 1 {
			t.Fatalf("got %d infos, want 1", len(infos))
		}
		if infos[0].ID.String() != "12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An" {
			t.Errorf("peer ID = %s", infos[0].ID)
		}
	})

	t.Run("dedup same peer", func(t *testing.T) {
		addrs := []string{
			"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
			"/ip4/203.0.113.50/udp/7778/quic-v1/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
		}
		infos, err := ParseRelayAddrs(addrs)
		if err != nil {
			t.Fatalf("ParseRelayAddrs: %v", err)
		}
		if len(infos) != 1 {
			t.Fatalf("got %d infos, want 1 (dedup)", len(infos))
		}
		// Merged addresses
		if len(infos[0].Addrs) != 2 {
			t.Errorf("got %d addrs, want 2 (merged)", len(infos[0].Addrs))
		}
	})

	t.Run("empty list", func(t *testing.T) {
		infos, err := ParseRelayAddrs(nil)
		if err != nil {
			t.Fatalf("ParseRelayAddrs nil: %v", err)
		}
		if len(infos) != 0 {
			t.Errorf("got %d infos, want 0", len(infos))
		}
	})

	t.Run("invalid multiaddr", func(t *testing.T) {
		_, err := ParseRelayAddrs([]string{"not-a-multiaddr"})
		if err == nil {
			t.Error("expected error for invalid multiaddr")
		}
	})

	t.Run("missing peer ID", func(t *testing.T) {
		_, err := ParseRelayAddrs([]string{"/ip4/1.2.3.4/tcp/7777"})
		if err == nil {
			t.Error("expected error for addr without peer ID")
		}
	})
}

// newListeningNetwork creates a p2pnet.Network that listens on localhost TCP.
func newListeningNetwork(t *testing.T) *Network {
	t.Helper()
	dir := t.TempDir()
	net, err := New(&Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create listening network: %v", err)
	}
	t.Cleanup(func() { net.Close() })
	return net
}

// connectNetworks connects Network A to Network B via localhost.
func connectNetworks(t *testing.T, a, b *Network) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := a.Host().Connect(ctx, peer.AddrInfo{
		ID:    b.Host().ID(),
		Addrs: b.Host().Addrs(),
	})
	if err != nil {
		t.Fatalf("connect networks: %v", err)
	}
}

// --- Network constructor and basic methods ---

func TestNetworkNew(t *testing.T) {
	t.Run("nil config", func(t *testing.T) {
		_, err := New(nil)
		if err == nil {
			t.Fatal("expected error for nil config")
		}
	})

	t.Run("basic", func(t *testing.T) {
		dir := t.TempDir()
		net, err := New(&Config{
			KeyFile: filepath.Join(dir, "test.key"),
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		defer net.Close()

		if net.Host() == nil {
			t.Error("Host() returned nil")
		}
		if net.PeerID() == "" {
			t.Error("PeerID() empty")
		}
	})

	t.Run("with listen addresses", func(t *testing.T) {
		net := newListeningNetwork(t)
		addrs := net.Host().Addrs()
		if len(addrs) == 0 {
			t.Error("expected listen addresses")
		}
	})

	t.Run("with user agent", func(t *testing.T) {
		dir := t.TempDir()
		net, err := New(&Config{
			KeyFile:   filepath.Join(dir, "test.key"),
			UserAgent: "test-agent/1.0",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		net.Close()
	})
}

// --- Network facade methods ---

func TestNetworkServiceFacade(t *testing.T) {
	net := newListeningNetwork(t)

	// Expose
	if err := net.ExposeService("ssh", "localhost:22"); err != nil {
		t.Fatalf("ExposeService: %v", err)
	}

	// List
	services := net.ListServices()
	if len(services) != 1 || services[0].Name != "ssh" {
		t.Errorf("ListServices: got %v", services)
	}

	// Duplicate
	err := net.ExposeService("ssh", "localhost:22")
	if err == nil {
		t.Error("expected error for duplicate service")
	}

	// Unexpose
	if err := net.UnexposeService("ssh"); err != nil {
		t.Fatalf("UnexposeService: %v", err)
	}

	// Unexpose nonexistent
	err = net.UnexposeService("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent service")
	}

	// List empty
	if len(net.ListServices()) != 0 {
		t.Error("expected empty services after unexpose")
	}
}

func TestNetworkNameFacade(t *testing.T) {
	net := newListeningNetwork(t)
	pid := net.PeerID() // use own peer ID for convenience

	// Register
	if err := net.RegisterName("home", pid); err != nil {
		t.Fatalf("RegisterName: %v", err)
	}

	// Resolve by name
	resolved, err := net.ResolveName("home")
	if err != nil {
		t.Fatalf("ResolveName: %v", err)
	}
	if resolved != pid {
		t.Errorf("resolved %s, want %s", resolved, pid)
	}

	// Resolve by peer ID string
	resolved, err = net.ResolveName(pid.String())
	if err != nil {
		t.Fatalf("ResolveName(peerID): %v", err)
	}
	if resolved != pid {
		t.Errorf("resolved %s, want %s", resolved, pid)
	}

	// Not found
	_, err = net.ResolveName("nonexistent")
	if err == nil {
		t.Error("expected error for nonexistent name")
	}

	// LoadNames
	dir := t.TempDir()
	net2, err := New(&Config{KeyFile: filepath.Join(dir, "test2.key")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer net2.Close()

	err = net2.LoadNames(map[string]string{
		"server": pid.String(),
	})
	if err != nil {
		t.Fatalf("LoadNames: %v", err)
	}
	resolved, err = net2.ResolveName("server")
	if err != nil {
		t.Fatalf("ResolveName after LoadNames: %v", err)
	}
	if resolved != pid {
		t.Errorf("resolved %s, want %s", resolved, pid)
	}
}

// --- TracePeer with connected hosts ---

func TestTracePeerDirect(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	result, err := TracePeer(context.Background(), netA.Host(), netB.Host().ID())
	if err != nil {
		t.Fatalf("TracePeer: %v", err)
	}

	if result.Path != "DIRECT" {
		t.Errorf("Path = %q, want DIRECT", result.Path)
	}
	if len(result.Hops) != 1 {
		t.Fatalf("expected 1 hop, got %d", len(result.Hops))
	}
	if result.Hops[0].PeerID != netB.Host().ID().String() {
		t.Errorf("hop PeerID = %q", result.Hops[0].PeerID)
	}
	if result.Hops[0].RttMs <= 0 && result.Hops[0].Error == "" {
		t.Errorf("expected positive RTT or error, got rtt=%f", result.Hops[0].RttMs)
	}
}

func TestTracePeerNotConnected(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	// NOT connected

	_, err := TracePeer(context.Background(), netA.Host(), netB.Host().ID())
	if err == nil {
		t.Fatal("expected error when not connected")
	}
	if !strings.Contains(err.Error(), "not connected") {
		t.Errorf("expected 'not connected' error, got: %v", err)
	}
}

func TestMeasurePeerRTT(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	rtt, err := measurePeerRTT(context.Background(), netA.Host(), netB.Host().ID())
	if err != nil {
		t.Fatalf("measurePeerRTT: %v", err)
	}
	if rtt <= 0 {
		t.Errorf("expected positive RTT, got %f", rtt)
	}
}

func TestConnectionTag(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	conns := netA.Host().Network().ConnsToPeer(netB.Host().ID())
	if len(conns) == 0 {
		t.Fatal("no connections")
	}

	// Open a test stream to get a stream object
	netB.Host().SetStreamHandler("/test/tag/1.0.0", func(s network.Stream) {
		s.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := netA.Host().NewStream(ctx, netB.Host().ID(), "/test/tag/1.0.0")
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()

	tag := connectionTag(s)
	if tag != "[DIRECT]" {
		t.Errorf("connectionTag = %q, want [DIRECT]", tag)
	}
}

// --- ConnectToService / DialService ---

func TestConnectToService(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	// Register an echo service on B (don't need real TCP, just the stream handler)
	netB.ExposeService("echo", "localhost:1") // won't actually dial TCP in this test

	// Connect from A to B's echo service — this tests DialService + serviceStream
	conn, err := netA.ConnectToServiceContext(context.Background(), netB.Host().ID(), "echo")
	if err != nil {
		t.Fatalf("ConnectToService: %v", err)
	}
	// The stream is open. Close it (tests serviceStream.Close + CloseWrite)
	conn.CloseWrite()
	conn.Close()
}

// --- AddRelayAddressesForPeer ---

func TestAddRelayAddressesForPeer(t *testing.T) {
	net := newListeningNetwork(t)
	// Use a fake relay addr and target peer
	dir := t.TempDir()
	net2, err := New(&Config{KeyFile: filepath.Join(dir, "target.key")})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer net2.Close()

	targetPID := net2.PeerID()
	relayAddrs := []string{
		"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
	}

	if err := net.AddRelayAddressesForPeer(relayAddrs, targetPID); err != nil {
		t.Fatalf("AddRelayAddressesForPeer: %v", err)
	}

	// Verify addresses were added to peerstore
	addrs := net.Host().Peerstore().Addrs(targetPID)
	if len(addrs) == 0 {
		t.Error("expected relay circuit addresses in peerstore")
	}
	found := false
	for _, a := range addrs {
		if strings.Contains(a.String(), "p2p-circuit") {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected p2p-circuit address in peerstore")
	}
}
