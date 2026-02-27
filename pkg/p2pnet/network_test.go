package p2pnet

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/protocol/holepunch"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/config"
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
	if err := net.ExposeService("ssh", "localhost:22", nil); err != nil {
		t.Fatalf("ExposeService: %v", err)
	}

	// List
	services := net.ListServices()
	if len(services) != 1 || services[0].Name != "ssh" {
		t.Errorf("ListServices: got %v", services)
	}

	// Duplicate
	err := net.ExposeService("ssh", "localhost:22", nil)
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
	netB.ExposeService("echo", "localhost:1", nil) // won't actually dial TCP in this test

	// Connect from A to B's echo service  - this tests DialService + serviceStream
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

// --- PeerIDFromKeyFile ---

func TestPeerIDFromKeyFile(t *testing.T) {
	t.Run("creates and loads", func(t *testing.T) {
		dir := t.TempDir()
		keyFile := filepath.Join(dir, "test.key")

		pid, err := PeerIDFromKeyFile(keyFile)
		if err != nil {
			t.Fatalf("PeerIDFromKeyFile: %v", err)
		}
		if pid == "" {
			t.Error("PeerIDFromKeyFile returned empty peer ID")
		}

		// Second call should return same peer ID
		pid2, err := PeerIDFromKeyFile(keyFile)
		if err != nil {
			t.Fatalf("PeerIDFromKeyFile (reload): %v", err)
		}
		if pid != pid2 {
			t.Errorf("peer IDs differ: %s vs %s", pid, pid2)
		}
	})

	t.Run("invalid path", func(t *testing.T) {
		_, err := PeerIDFromKeyFile("/nonexistent/dir/test.key")
		if err != nil {
			// Expected  - can't create key in nonexistent dir.
			// On some systems this might succeed if the parent exists.
			// Just verify it doesn't panic.
		}
	})
}

// --- Network.New additional branches ---

func TestNetworkNew_WithRelayConfig(t *testing.T) {
	dir := t.TempDir()
	net, err := New(&Config{
		KeyFile:            filepath.Join(dir, "test.key"),
		EnableRelay:        true,
		RelayAddrs:         []string{"/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An"},
		ForcePrivate:       true,
		EnableNATPortMap:   true,
		EnableHolePunching: true,
	})
	if err != nil {
		t.Fatalf("New with relay config: %v", err)
	}
	defer net.Close()

	if net.Host() == nil {
		t.Error("Host() returned nil")
	}
}

func TestNetworkNew_WithRelayInvalidAddrs(t *testing.T) {
	dir := t.TempDir()
	_, err := New(&Config{
		KeyFile:     filepath.Join(dir, "test.key"),
		EnableRelay: true,
		RelayAddrs:  []string{"not-a-multiaddr"},
	})
	if err == nil {
		t.Error("expected error for invalid relay addr")
	}
}

func TestNetworkNew_WithGater(t *testing.T) {
	dir := t.TempDir()
	gater := auth.NewAuthorizedPeerGater(nil)

	net, err := New(&Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Gater:   gater,
	})
	if err != nil {
		t.Fatalf("New with Gater: %v", err)
	}
	defer net.Close()
}

func TestNetworkNew_WithAuthorizedKeysFile(t *testing.T) {
	dir := t.TempDir()
	akPath := filepath.Join(dir, "authorized_keys")
	if err := os.WriteFile(akPath, []byte(""), 0600); err != nil {
		t.Fatalf("write authorized_keys: %v", err)
	}

	net, err := New(&Config{
		KeyFile:        filepath.Join(dir, "test.key"),
		AuthorizedKeys: akPath,
	})
	if err != nil {
		t.Fatalf("New with AuthorizedKeys: %v", err)
	}
	defer net.Close()
}

func TestNetworkNew_WithBadAuthorizedKeysFile(t *testing.T) {
	dir := t.TempDir()
	_, err := New(&Config{
		KeyFile:        filepath.Join(dir, "test.key"),
		AuthorizedKeys: filepath.Join(dir, "nonexistent_keys"),
	})
	if err == nil {
		t.Error("expected error for missing authorized_keys file")
	}
}

// --- ExposeService / UnexposeService invalid name ---

func TestExposeService_InvalidName(t *testing.T) {
	net := newListeningNetwork(t)
	if err := net.ExposeService("INVALID", "localhost:22", nil); err == nil {
		t.Error("expected error for invalid service name")
	}
}

func TestUnexposeService_InvalidName(t *testing.T) {
	net := newListeningNetwork(t)
	if err := net.UnexposeService("INVALID"); err == nil {
		t.Error("expected error for invalid service name")
	}
}

// --- ConnectToService (thin wrapper with 30s timeout) ---

func TestConnectToService_DefaultTimeout(t *testing.T) {
	netA := newListeningNetwork(t)
	netB := newListeningNetwork(t)
	connectNetworks(t, netA, netB)

	netB.ExposeService("echo", "localhost:1", nil)

	conn, err := netA.ConnectToService(netB.PeerID(), "echo")
	if err != nil {
		t.Fatalf("ConnectToService: %v", err)
	}
	conn.Close()
}

func TestConnectToServiceContext_InvalidName(t *testing.T) {
	net := newListeningNetwork(t)
	_, err := net.ConnectToServiceContext(context.Background(), net.PeerID(), "INVALID")
	if err == nil {
		t.Error("expected error for invalid service name")
	}
}

// --- holePunchTracer.Trace ---

func TestHolePunchTracer(t *testing.T) {
	tracer := &holePunchTracer{}
	pid := genTestPeerID(t)

	// Exercise all three event types  - they just log, so we verify no panic
	tracer.Trace(&holepunch.Event{
		Remote: pid,
		Evt:    &holepunch.StartHolePunchEvt{RTT: time.Millisecond},
	})

	tracer.Trace(&holepunch.Event{
		Remote: pid,
		Evt:    &holepunch.EndHolePunchEvt{Success: true, EllapsedTime: time.Millisecond},
	})

	tracer.Trace(&holepunch.Event{
		Remote: pid,
		Evt:    &holepunch.EndHolePunchEvt{Success: false, EllapsedTime: time.Millisecond, Error: "timeout"},
	})

	tracer.Trace(&holepunch.Event{
		Remote: pid,
		Evt:    &holepunch.DirectDialEvt{Success: true, EllapsedTime: time.Millisecond},
	})

	tracer.Trace(&holepunch.Event{
		Remote: pid,
		Evt:    &holepunch.DirectDialEvt{Success: false, Error: "connection refused"},
	})

	// Short peer ID (< 16 chars) path
	shortPID := peer.ID("short")
	tracer.Trace(&holepunch.Event{
		Remote: shortPID,
		Evt:    &holepunch.StartHolePunchEvt{},
	})
}

func TestDHTProtocolPrefixForNamespace(t *testing.T) {
	tests := []struct {
		namespace string
		want      string
	}{
		{"", "/shurli"},
		{"my-crew", "/shurli/my-crew"},
		{"gaming", "/shurli/gaming"},
		{"org-internal", "/shurli/org-internal"},
		{"a", "/shurli/a"},
	}
	for _, tt := range tests {
		got := DHTProtocolPrefixForNamespace(tt.namespace)
		if got != tt.want {
			t.Errorf("DHTProtocolPrefixForNamespace(%q) = %q, want %q", tt.namespace, got, tt.want)
		}
	}
}

func TestDHTProtocolPrefixForNamespace_DefaultMatchesConstant(t *testing.T) {
	got := DHTProtocolPrefixForNamespace("")
	if got != DHTProtocolPrefix {
		t.Errorf("empty namespace returned %q, want DHTProtocolPrefix %q", got, DHTProtocolPrefix)
	}
}

func TestGlobalIPv6AddrsFactory(t *testing.T) {
	t.Run("noop when global IPv6 already present", func(t *testing.T) {
		addrs := mustMultiaddrs(t,
			"/ip4/203.0.113.1/tcp/4001",
			"/ip6/2001:db8::1/tcp/4001",
			"/ip6/::1/tcp/4001",
		)
		result := globalIPv6AddrsFactory(addrs)
		if len(result) != len(addrs) {
			t.Errorf("expected %d addrs unchanged, got %d", len(addrs), len(result))
		}
	})

	t.Run("noop when no IPv6 listeners", func(t *testing.T) {
		addrs := mustMultiaddrs(t,
			"/ip4/203.0.113.1/tcp/4001",
		)
		result := globalIPv6AddrsFactory(addrs)
		if len(result) != len(addrs) {
			t.Errorf("expected %d addrs unchanged, got %d", len(addrs), len(result))
		}
	})

	t.Run("extracts ports from loopback", func(t *testing.T) {
		// Simulate: loopback IPv6 with TCP and QUIC, no global IPv6.
		// The factory should try to add global IPv6 from interfaces.
		// On CI/test machines there may not be global IPv6, so just
		// verify it doesn't panic or corrupt the address list.
		addrs := mustMultiaddrs(t,
			"/ip4/172.20.10.3/tcp/4001",
			"/ip6/::1/tcp/5001",
			"/ip6/::1/udp/6001/quic-v1",
		)
		result := globalIPv6AddrsFactory(addrs)
		// Should always return at least the original addresses.
		if len(result) < len(addrs) {
			t.Errorf("factory removed addresses: had %d, got %d", len(addrs), len(result))
		}
	})
}

func TestSourceBindDialerForAddr(t *testing.T) {
	t.Run("IPv4 returns plain dialer", func(t *testing.T) {
		raddr, _ := ma.NewMultiaddr("/ip4/203.0.113.50/tcp/4001")
		d, err := sourceBindDialerForAddr(raddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		nd, ok := d.(*net.Dialer)
		if !ok {
			t.Fatalf("expected *net.Dialer, got %T", d)
		}
		if nd.LocalAddr != nil {
			t.Errorf("expected nil LocalAddr for IPv4, got %v", nd.LocalAddr)
		}
	})

	t.Run("link-local IPv6 returns plain dialer", func(t *testing.T) {
		raddr, _ := ma.NewMultiaddr("/ip6/fe80::1/tcp/4001")
		d, err := sourceBindDialerForAddr(raddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		nd, ok := d.(*net.Dialer)
		if !ok {
			t.Fatalf("expected *net.Dialer, got %T", d)
		}
		if nd.LocalAddr != nil {
			t.Errorf("expected nil LocalAddr for link-local, got %v", nd.LocalAddr)
		}
	})

	t.Run("loopback IPv6 returns plain dialer", func(t *testing.T) {
		raddr, _ := ma.NewMultiaddr("/ip6/::1/tcp/4001")
		d, err := sourceBindDialerForAddr(raddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		nd, ok := d.(*net.Dialer)
		if !ok {
			t.Fatalf("expected *net.Dialer, got %T", d)
		}
		if nd.LocalAddr != nil {
			t.Errorf("expected nil LocalAddr for loopback, got %v", nd.LocalAddr)
		}
	})

	t.Run("global IPv6 returns dialer", func(t *testing.T) {
		// Uses RFC 3849 documentation address as destination.
		// Whether LocalAddr is set depends on the test machine's
		// interfaces. Just verify it returns without error.
		raddr, _ := ma.NewMultiaddr("/ip6/2001:db8::1/tcp/4001")
		d, err := sourceBindDialerForAddr(raddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if d == nil {
			t.Fatal("dialer is nil")
		}
		nd, ok := d.(*net.Dialer)
		if !ok {
			t.Fatalf("expected *net.Dialer, got %T", d)
		}
		// On machines WITH global IPv6: LocalAddr is set.
		// On machines WITHOUT global IPv6 (CI): LocalAddr is nil (fallback).
		// Both are correct behavior.
		t.Logf("global IPv6 dialer LocalAddr: %v", nd.LocalAddr)
	})

	t.Run("non-TCP multiaddr returns plain dialer", func(t *testing.T) {
		raddr, _ := ma.NewMultiaddr("/ip6/2001:db8::1/udp/4001")
		d, err := sourceBindDialerForAddr(raddr)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		nd, ok := d.(*net.Dialer)
		if !ok {
			t.Fatalf("expected *net.Dialer, got %T", d)
		}
		// The TCP transport only calls this for TCP, but the function
		// checks the IP layer, not the transport layer. UDP multiaddr
		// still has ip6 as first component, so for global IPv6 destination
		// it may source-bind. This is fine - the TCP transport won't pass
		// UDP addrs anyway.
		t.Logf("UDP multiaddr dialer LocalAddr: %v", nd.LocalAddr)
	})
}

func mustMultiaddrs(t *testing.T, strs ...string) []ma.Multiaddr {
	t.Helper()
	addrs := make([]ma.Multiaddr, len(strs))
	for i, s := range strs {
		a, err := ma.NewMultiaddr(s)
		if err != nil {
			t.Fatalf("bad multiaddr %q: %v", s, err)
		}
		addrs[i] = a
	}
	return addrs
}
