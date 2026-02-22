package p2pnet

import (
	"testing"

	"github.com/libp2p/go-libp2p"
)

func TestPeerRelay_EnableDisable(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil)

	// Not enabled initially
	if pr.Enabled() {
		t.Error("should not be enabled initially")
	}

	// Enable
	if err := pr.Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if !pr.Enabled() {
		t.Error("should be enabled after Enable()")
	}

	// Enable again (idempotent)
	if err := pr.Enable(); err != nil {
		t.Fatalf("second Enable: %v", err)
	}
	if !pr.Enabled() {
		t.Error("should still be enabled")
	}

	// Disable
	pr.Disable()
	if pr.Enabled() {
		t.Error("should not be enabled after Disable()")
	}

	// Disable again (idempotent)
	pr.Disable()
	if pr.Enabled() {
		t.Error("should still not be enabled")
	}
}

func TestPeerRelay_AutoDetect_PublicIP(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil)

	// With global IPv4 - should enable
	summary := &InterfaceSummary{
		HasGlobalIPv4:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
	}
	pr.AutoDetect(summary)
	if !pr.Enabled() {
		t.Error("should be enabled with global IPv4")
	}

	// Still has public IP - no change
	pr.AutoDetect(summary)
	if !pr.Enabled() {
		t.Error("should still be enabled")
	}

	// Lost public IP - should disable
	summaryNoPublic := &InterfaceSummary{
		HasGlobalIPv4: false,
		HasGlobalIPv6: false,
	}
	pr.AutoDetect(summaryNoPublic)
	if pr.Enabled() {
		t.Error("should be disabled without public IP")
	}

	pr.Disable() // clean up
}

func TestPeerRelay_AutoDetect_PublicIPv6(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil)

	// With global IPv6 only - should enable
	summary := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8::1"},
	}
	pr.AutoDetect(summary)
	if !pr.Enabled() {
		t.Error("should be enabled with global IPv6")
	}

	pr.Disable()
}

func TestPeerRelay_AutoDetect_NilSummary(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil)

	// Nil summary should be safe
	pr.AutoDetect(nil)
	if pr.Enabled() {
		t.Error("should not be enabled with nil summary")
	}
}

func TestPeerRelay_WithMetrics(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	m := NewMetrics("test", "go1.26")
	pr := NewPeerRelay(h, m)

	if err := pr.Enable(); err != nil {
		t.Fatalf("Enable with metrics: %v", err)
	}
	defer pr.Disable()

	if !pr.Enabled() {
		t.Error("should be enabled")
	}
}

func TestPeerRelayResources_Values(t *testing.T) {
	// Verify the resource limits are conservative
	if PeerRelayResources.MaxReservations > 10 {
		t.Errorf("MaxReservations too high: %d", PeerRelayResources.MaxReservations)
	}
	if PeerRelayResources.MaxCircuits > 32 {
		t.Errorf("MaxCircuits too high: %d", PeerRelayResources.MaxCircuits)
	}
	if PeerRelayResources.MaxReservationsPerPeer != 1 {
		t.Errorf("MaxReservationsPerPeer should be 1, got %d", PeerRelayResources.MaxReservationsPerPeer)
	}
}
