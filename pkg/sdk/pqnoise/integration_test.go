package pqnoise_test

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/p2p/security/noise"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/pkg/sdk/pqnoise"
)

// TestIntegration_PQNoiseHostToHost verifies two libp2p hosts connect
// using PQ Noise over TCP loopback and the security protocol is /pq-noise/1.
func TestIntegration_PQNoiseHostToHost(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Create two hosts with PQ Noise only (mandatory mode).
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(pqnoise.ID, pqnoise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(pqnoise.ID, pqnoise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	defer h2.Close()

	// Connect h1 -> h2.
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	if err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	// Verify PQ Noise negotiated.
	conns := h1.Network().ConnsToPeer(h2.ID())
	if len(conns) == 0 {
		t.Fatal("no connections to h2")
	}
	for _, c := range conns {
		if c.ConnState().Security != pqnoise.ID {
			t.Errorf("expected security %q, got %q", pqnoise.ID, c.ConnState().Security)
		}
	}
}

// TestIntegration_OpportunisticFallback verifies that in opportunistic mode,
// a PQ host can connect to a classical-only host via /noise fallback.
func TestIntegration_OpportunisticFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// h1: opportunistic (PQ Noise preferred, classical Noise fallback).
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(pqnoise.ID, pqnoise.New),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h1: %v", err)
	}
	defer h1.Close()

	// h2: classical only (no PQ Noise support).
	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	defer h2.Close()

	// Connect h1 -> h2. Should negotiate /noise (classical fallback).
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	if err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	conns := h1.Network().ConnsToPeer(h2.ID())
	if len(conns) == 0 {
		t.Fatal("no connections to h2")
	}
	for _, c := range conns {
		if c.ConnState().Security != noise.ID {
			t.Errorf("expected fallback to %q, got %q", noise.ID, c.ConnState().Security)
		}
	}
}

// TestIntegration_MandatoryRejectsClassical verifies that mandatory mode
// does not connect to a classical-only host (no fallback offered).
func TestIntegration_MandatoryRejectsClassical(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// h1: mandatory (PQ Noise only).
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(pqnoise.ID, pqnoise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h1: %v", err)
	}
	defer h1.Close()

	// h2: classical only.
	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.Security(noise.ID, noise.New),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	defer h2.Close()

	// Connect should fail - no common security protocol.
	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	err = h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()})
	if err == nil {
		t.Fatal("expected connection to fail (no common security), but it succeeded")
	}
}

// TestIntegration_DisabledModeClassicalOnly verifies that disabled mode
// does not register PQ Noise (uses default classical security).
func TestIntegration_DisabledModeClassicalOnly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Both hosts use defaults (TLS + classical Noise) = "disabled" PQC mode.
	h1, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h1: %v", err)
	}
	defer h1.Close()

	h2, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoTransports,
		libp2p.Transport(tcpTransport),
	)
	if err != nil {
		t.Fatalf("h2: %v", err)
	}
	defer h2.Close()

	h1.Peerstore().AddAddrs(h2.ID(), h2.Addrs(), time.Hour)
	if err := h1.Connect(ctx, peer.AddrInfo{ID: h2.ID(), Addrs: h2.Addrs()}); err != nil {
		t.Fatalf("connect: %v", err)
	}

	conns := h1.Network().ConnsToPeer(h2.ID())
	if len(conns) == 0 {
		t.Fatal("no connections")
	}
	for _, c := range conns {
		// Should be classical (noise or TLS, NOT /pq-noise/1).
		if c.ConnState().Security == pqnoise.ID {
			t.Errorf("disabled mode should not use PQ Noise, got %q", c.ConnState().Security)
		}
	}
}

// TestIntegration_GaterInterceptUpgraded_Mandatory verifies the gater
// rejects classical connections when PQC policy is mandatory.
func TestIntegration_GaterInterceptUpgraded_Mandatory(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	gater.SetPQCPolicyStartup(auth.PQCPolicyMandatory)

	pid, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("decode peer: %v", err)
	}

	// Simulate a classical Noise connection.
	mockConn := &mockConnForGater{
		security: "/noise",
		peerID:   pid,
	}
	allowed, _ := gater.InterceptUpgraded(mockConn)
	if allowed {
		t.Error("mandatory policy should reject classical /noise connection")
	}

	// Simulate a PQ Noise connection.
	mockConn.security = string(pqnoise.ID)
	allowed, _ = gater.InterceptUpgraded(mockConn)
	if !allowed {
		t.Error("mandatory policy should allow /pq-noise/1 connection")
	}

	// Simulate a QUIC connection (Transport field identifies it, security empty).
	mockConn.security = ""
	mockConn.transport = "quic-v1"
	allowed, _ = gater.InterceptUpgraded(mockConn)
	if !allowed {
		t.Error("mandatory policy should allow QUIC connection (PQ at TLS layer)")
	}
}

// TestIntegration_GaterInterceptUpgraded_Opportunistic verifies the gater
// allows all connections when PQC policy is opportunistic.
func TestIntegration_GaterInterceptUpgraded_Opportunistic(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	gater.SetPQCPolicyStartup(auth.PQCPolicyOpportunistic)

	pid, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("decode peer: %v", err)
	}

	mockConn := &mockConnForGater{
		security: "/noise",
		peerID:   pid,
	}
	allowed, _ := gater.InterceptUpgraded(mockConn)
	if !allowed {
		t.Error("opportunistic policy should allow classical /noise connection")
	}
}

// TestIntegration_GaterPerPeerOverride verifies per-peer PQC override.
func TestIntegration_GaterPerPeerOverride(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	gater.SetPQCPolicyStartup(auth.PQCPolicyOpportunistic) // global = opportunistic

	pid, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatalf("decode peer: %v", err)
	}

	// Set per-peer override to mandatory.
	gater.SetPeerPQCOverride(pid, auth.PQCPolicyMandatory)

	// Classical connection from that peer should be rejected.
	mockConn := &mockConnForGater{
		security: "/noise",
		peerID:   pid,
	}
	allowed, _ := gater.InterceptUpgraded(mockConn)
	if allowed {
		t.Error("per-peer mandatory override should reject classical connection")
	}

	// PQ Noise from that peer should be allowed.
	mockConn.security = string(pqnoise.ID)
	allowed, _ = gater.InterceptUpgraded(mockConn)
	if !allowed {
		t.Error("per-peer mandatory override should allow PQ Noise connection")
	}
}

// TestIntegration_SetPQCPolicy_DisabledAtRuntime verifies that "disabled"
// cannot be set at runtime (startup-only per F151).
func TestIntegration_SetPQCPolicy_DisabledAtRuntime(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})

	err := gater.SetPQCPolicy(auth.PQCPolicyDisabled)
	if err == nil {
		t.Error("expected error when setting 'disabled' at runtime")
	}

	// Valid runtime changes.
	if err := gater.SetPQCPolicy(auth.PQCPolicyMandatory); err != nil {
		t.Errorf("unexpected error setting mandatory: %v", err)
	}
	if gater.PQCPolicy() != auth.PQCPolicyMandatory {
		t.Errorf("expected mandatory, got %q", gater.PQCPolicy())
	}

	if err := gater.SetPQCPolicy(auth.PQCPolicyOpportunistic); err != nil {
		t.Errorf("unexpected error setting opportunistic: %v", err)
	}
	if gater.PQCPolicy() != auth.PQCPolicyOpportunistic {
		t.Errorf("expected opportunistic, got %q", gater.PQCPolicy())
	}
}

// TestIntegration_DefaultPolicyOpportunistic verifies the default PQC policy
// is "opportunistic" when not configured (F167).
func TestIntegration_DefaultPolicyOpportunistic(t *testing.T) {
	gater := auth.NewAuthorizedPeerGater(map[peer.ID]bool{})
	if gater.PQCPolicy() != auth.PQCPolicyOpportunistic {
		t.Errorf("default policy should be opportunistic, got %q", gater.PQCPolicy())
	}
}
