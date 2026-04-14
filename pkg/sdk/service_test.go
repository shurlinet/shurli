package sdk

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// newRawTestHost builds a libp2p host with no registry — for tests that
// want direct access to the host.
func newRawTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

func newTestHost(t *testing.T) *ServiceRegistry {
	t.Helper()
	h, err := libp2p.New(libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"))
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return NewServiceRegistry(h, nil)
}

func TestNewServiceRegistry(t *testing.T) {
	reg := newTestHost(t)
	if reg == nil {
		t.Fatal("NewServiceRegistry returned nil")
	}
	if len(reg.ListServices()) != 0 {
		t.Error("new registry should have no services")
	}
}

func TestRegisterService(t *testing.T) {
	reg := newTestHost(t)

	t.Run("valid", func(t *testing.T) {
		svc := &Service{
			Name:         "ssh",
			Protocol:     "/shurli/ssh/1.0.0",
			LocalAddress: "localhost:22",
			Enabled:      true,
		}
		if err := reg.RegisterService(svc); err != nil {
			t.Fatalf("RegisterService: %v", err)
		}
		services := reg.ListServices()
		if len(services) != 1 {
			t.Fatalf("got %d services, want 1", len(services))
		}
		if services[0].Name != "ssh" {
			t.Errorf("Name = %q, want %q", services[0].Name, "ssh")
		}
	})

	t.Run("nil service", func(t *testing.T) {
		if err := reg.RegisterService(nil); err == nil {
			t.Error("expected error for nil service")
		}
	})

	t.Run("empty name", func(t *testing.T) {
		svc := &Service{
			Name:         "",
			Protocol:     "/shurli/test/1.0.0",
			LocalAddress: "localhost:8080",
		}
		if err := reg.RegisterService(svc); err == nil {
			t.Error("expected error for empty name")
		}
	})

	t.Run("empty address", func(t *testing.T) {
		svc := &Service{
			Name:         "test",
			Protocol:     "/shurli/test/1.0.0",
			LocalAddress: "",
		}
		if err := reg.RegisterService(svc); err == nil {
			t.Error("expected error for empty address")
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		svc := &Service{
			Name:         "ssh",
			Protocol:     "/shurli/ssh2/1.0.0",
			LocalAddress: "localhost:2222",
		}
		err := reg.RegisterService(svc)
		if err == nil {
			t.Error("expected error for duplicate name")
		}
		if !errors.Is(err, ErrServiceAlreadyRegistered) {
			t.Errorf("expected ErrServiceAlreadyRegistered, got: %v", err)
		}
	})
}

func TestUnregisterService(t *testing.T) {
	reg := newTestHost(t)

	svc := &Service{
		Name:         "ssh",
		Protocol:     "/shurli/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	}
	reg.RegisterService(svc)

	t.Run("exists", func(t *testing.T) {
		if err := reg.UnregisterService("ssh"); err != nil {
			t.Fatalf("UnregisterService: %v", err)
		}
		if len(reg.ListServices()) != 0 {
			t.Error("service should be removed")
		}
	})

	t.Run("not found", func(t *testing.T) {
		err := reg.UnregisterService("nonexistent")
		if err == nil {
			t.Error("expected error for nonexistent service")
		}
		if !errors.Is(err, ErrServiceNotFound) {
			t.Errorf("expected ErrServiceNotFound, got: %v", err)
		}
	})
}

func TestGetService(t *testing.T) {
	reg := newTestHost(t)

	svc := &Service{
		Name:         "ssh",
		Protocol:     "/shurli/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	}
	reg.RegisterService(svc)

	t.Run("found", func(t *testing.T) {
		got, ok := reg.GetService("ssh")
		if !ok {
			t.Fatal("expected service to be found")
		}
		if got.Name != "ssh" {
			t.Errorf("Name = %q", got.Name)
		}
		if got.LocalAddress != "localhost:22" {
			t.Errorf("LocalAddress = %q", got.LocalAddress)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, ok := reg.GetService("nonexistent")
		if ok {
			t.Error("expected service not to be found")
		}
	})
}

func TestListServices(t *testing.T) {
	reg := newTestHost(t)

	// Empty
	if len(reg.ListServices()) != 0 {
		t.Error("new registry should return empty list")
	}

	// Add two services
	reg.RegisterService(&Service{
		Name:         "ssh",
		Protocol:     "/shurli/ssh/1.0.0",
		LocalAddress: "localhost:22",
		Enabled:      true,
	})
	reg.RegisterService(&Service{
		Name:         "xrdp",
		Protocol:     "/shurli/xrdp/1.0.0",
		LocalAddress: "localhost:3389",
		Enabled:      true,
	})

	services := reg.ListServices()
	if len(services) != 2 {
		t.Fatalf("got %d services, want 2", len(services))
	}

	// Verify both are present (order not guaranteed from map)
	names := make(map[string]bool)
	for _, s := range services {
		names[s.Name] = true
	}
	if !names["ssh"] || !names["xrdp"] {
		t.Errorf("missing services: %v", names)
	}
}

func TestRelayPeerFromAddr(t *testing.T) {
	// Use real peer IDs for valid multiaddr parsing.
	// These are deterministic test peer IDs (base58btc-encoded Ed25519 public keys).
	relayID := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	targetID := "12D3KooWGC5CAhVgwBCupPqrNd83VKHHhR4ghtNvxqaXG4NVbKR1"

	tests := []struct {
		name    string
		addr    string
		wantID  string
		wantErr bool // if true, addr is invalid multiaddr (skip)
	}{
		{
			"full circuit relay addr",
			"/ip4/1.2.3.4/udp/4001/quic-v1/p2p/" + relayID + "/p2p-circuit/p2p/" + targetID,
			relayID, false,
		},
		{
			"circuit relay without trailing peer",
			"/ip4/1.2.3.4/udp/4001/quic-v1/p2p/" + relayID + "/p2p-circuit",
			relayID, false,
		},
		{
			"direct addr no circuit",
			"/ip4/1.2.3.4/udp/4001/quic-v1/p2p/" + relayID,
			"", false,
		},
		{
			"circuit but no p2p before it",
			"/ip4/1.2.3.4/udp/4001/quic-v1/p2p-circuit",
			"", false,
		},
		{
			"ipv6 circuit relay",
			"/ip6/2001:db8::1/udp/4001/quic-v1/p2p/" + relayID + "/p2p-circuit/p2p/" + targetID,
			relayID, false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			maddr, err := ma.NewMultiaddr(tt.addr)
			if err != nil {
				if tt.wantErr {
					return
				}
				t.Fatalf("invalid test multiaddr %q: %v", tt.addr, err)
			}
			got := RelayPeerFromAddr(maddr)
			if tt.wantID == "" {
				if got != "" {
					t.Errorf("RelayPeerFromAddr(%q) = %s, want empty", tt.addr, got)
				}
			} else {
				wantPeer, _ := peer.Decode(tt.wantID)
				if got != wantPeer {
					t.Errorf("RelayPeerFromAddr(%q) = %s, want %s", tt.addr, got, wantPeer)
				}
			}
		})
	}
}

func TestValidateServiceName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{"valid lowercase", "ssh", false},
		{"valid with dash", "my-service", false},
		{"valid with numbers", "svc123", false},
		{"invalid slash", "foo/bar", true},
		{"invalid newline", "foo\nbar", true},
		{"invalid uppercase", "SSH", true},
		{"empty", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateServiceName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateServiceName(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

// TestPluginPolicyLANAllowsDefault validates that a plugin service with the
// default policy (LAN|Direct) accepts a LAN stream without consulting the
// grant checker — the grant path is relay-only by design.
//
// This is the TS-5b Session 2 plumbing smoke test: stream arrives,
// ClassifyTransport returns TransportLAN for loopback, policy allows LAN,
// handler runs, no grant checker call needed.
func TestPluginPolicyLANAllowsDefault(t *testing.T) {
	serverHost := newRawTestHost(t)
	clientHost := newRawTestHost(t)

	const protoID = "/shurli/test-plugin/1.0.0"
	const svcName = "test-plugin"

	reg := NewServiceRegistry(serverHost, nil)

	handlerCh := make(chan struct{}, 1)
	svc := &Service{
		Name:     svcName,
		Protocol: protoID,
		Handler: func(name string, s network.Stream) {
			defer s.Close()
			_, _ = s.Write([]byte("ok"))
			select {
			case handlerCh <- struct{}{}:
			default:
			}
		},
		Policy: DefaultPluginPolicy(), // LAN|Direct, no relay
	}
	if err := reg.RegisterService(svc); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}

	// Wire a grantChecker that records any call — on a LAN path, we expect
	// this to never be called.
	var checkerInvoked int32 = 0
	reg.SetGrantChecker(func(pid peer.ID, svcName string, tr TransportType) bool {
		checkerInvoked = 1
		return false
	})
	reg.Seal()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := clientHost.NewStream(ctx, serverHost.ID(), protoID)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()

	if err := WriteGrantHeader(s, ""); err != nil {
		t.Fatalf("WriteGrantHeader: %v", err)
	}

	buf := make([]byte, 2)
	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := s.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "ok" {
		t.Fatalf("expected \"ok\", got n=%d err=%v buf=%q", n, err, buf[:n])
	}

	select {
	case <-handlerCh:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran")
	}

	if checkerInvoked == 1 {
		t.Error("grantChecker should not be called for default-LAN transport")
	}
}

// TestPluginPolicyBlocksWithoutGrant verifies a policy with
// AllowedTransports=0 rejects all streams when no grant is presented and no
// grantChecker is configured. No fallback path — pure deny.
func TestPluginPolicyBlocksWithoutGrant(t *testing.T) {
	serverHost := newRawTestHost(t)
	clientHost := newRawTestHost(t)

	const protoID = "/shurli/test-plugin-blocked/1.0.0"
	const svcName = "test-plugin-blocked"

	reg := NewServiceRegistry(serverHost, nil)
	svc := &Service{
		Name:     svcName,
		Protocol: protoID,
		Handler: func(name string, s network.Stream) {
			defer s.Close()
			_, _ = s.Write([]byte("ok"))
		},
		Policy: &PluginPolicy{AllowedTransports: 0},
	}
	if err := reg.RegisterService(svc); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}
	reg.Seal()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := clientHost.NewStream(ctx, serverHost.ID(), protoID)
	if err != nil {
		return
	}
	defer s.Close()
	_ = WriteGrantHeader(s, "")

	buf := make([]byte, 2)
	_ = s.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, _ := s.Read(buf)
	if n == 2 && string(buf[:n]) == "ok" {
		t.Error("blocked policy should not accept stream")
	}
}

// TestPluginPolicyGrantUnlocksDeniedTransport verifies the TS-5b Session 2
// generalization: a plugin policy that denies the current stream transport
// can be unlocked by a grantChecker that returns true for (peer, svc,
// transport). This is the whole point of the transport caveat — a narrow
// grant is honored on whatever transport the grant names.
//
// Setup: plugin policy blocks LAN; grantChecker returns true for the client
// peer when transport is LAN. Client opens loopback stream (LAN), hands over
// an empty grant header (no token), handler runs via grantChecker fallback.
func TestPluginPolicyGrantUnlocksDeniedTransport(t *testing.T) {
	serverHost := newRawTestHost(t)
	clientHost := newRawTestHost(t)

	const protoID = "/shurli/test-plugin-unlocked/1.0.0"
	const svcName = "test-plugin-unlocked"

	reg := NewServiceRegistry(serverHost, nil)

	handlerCh := make(chan struct{}, 1)
	svc := &Service{
		Name:     svcName,
		Protocol: protoID,
		Handler: func(name string, s network.Stream) {
			defer s.Close()
			_, _ = s.Write([]byte("ok"))
			select {
			case handlerCh <- struct{}{}:
			default:
			}
		},
		Policy: &PluginPolicy{AllowedTransports: 0}, // deny everything by default
	}
	if err := reg.RegisterService(svc); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}

	var gotTransport TransportType
	var gotService string
	var gotPeer peer.ID
	reg.SetGrantChecker(func(pid peer.ID, svcName string, tr TransportType) bool {
		gotPeer = pid
		gotService = svcName
		gotTransport = tr
		// Honor the grant only for LAN transport (transport caveat semantics).
		return tr == TransportLAN
	})
	reg.Seal()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := clientHost.NewStream(ctx, serverHost.ID(), protoID)
	if err != nil {
		t.Fatalf("NewStream: %v", err)
	}
	defer s.Close()
	if err := WriteGrantHeader(s, ""); err != nil {
		t.Fatalf("WriteGrantHeader: %v", err)
	}

	buf := make([]byte, 2)
	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := s.Read(buf)
	if err != nil || n != 2 || string(buf[:n]) != "ok" {
		t.Fatalf("expected \"ok\" via grant unlock, got n=%d err=%v buf=%q", n, err, buf[:n])
	}

	select {
	case <-handlerCh:
	case <-time.After(2 * time.Second):
		t.Fatal("handler never ran via grant unlock")
	}

	if gotTransport != TransportLAN {
		t.Errorf("grantChecker called with transport=%d, want TransportLAN (%d)", gotTransport, TransportLAN)
	}
	if gotService != svcName {
		t.Errorf("grantChecker called with service=%q, want %q", gotService, svcName)
	}
	if gotPeer != clientHost.ID() {
		t.Errorf("grantChecker called with peer=%s, want %s", gotPeer, clientHost.ID())
	}
}

// TestPluginPolicyGrantDeniesWrongTransport verifies that a grantChecker
// returning false for the current transport leads to stream rejection —
// i.e. a narrow transport caveat is honored bit-exact by the handler.
func TestPluginPolicyGrantDeniesWrongTransport(t *testing.T) {
	serverHost := newRawTestHost(t)
	clientHost := newRawTestHost(t)

	const protoID = "/shurli/test-plugin-denied/1.0.0"
	const svcName = "test-plugin-denied"

	reg := NewServiceRegistry(serverHost, nil)
	svc := &Service{
		Name:     svcName,
		Protocol: protoID,
		Handler: func(name string, s network.Stream) {
			defer s.Close()
			_, _ = s.Write([]byte("ok"))
		},
		Policy: &PluginPolicy{AllowedTransports: 0},
	}
	if err := reg.RegisterService(svc); err != nil {
		t.Fatalf("RegisterService: %v", err)
	}

	// Grant exists but only for relay — LAN stream must be rejected.
	reg.SetGrantChecker(func(pid peer.ID, svcName string, tr TransportType) bool {
		return tr == TransportRelay
	})
	reg.Seal()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := clientHost.Connect(ctx, peer.AddrInfo{ID: serverHost.ID(), Addrs: serverHost.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}
	s, err := clientHost.NewStream(ctx, serverHost.ID(), protoID)
	if err != nil {
		return
	}
	defer s.Close()
	_ = WriteGrantHeader(s, "")

	buf := make([]byte, 2)
	_ = s.SetReadDeadline(time.Now().Add(1 * time.Second))
	n, _ := s.Read(buf)
	if n == 2 && string(buf[:n]) == "ok" {
		t.Error("relay-only grant must not unlock LAN stream")
	}
}
