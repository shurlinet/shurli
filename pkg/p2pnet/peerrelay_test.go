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

	pr := NewPeerRelay(h, nil, PeerRelayConfig{})

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

	pr := NewPeerRelay(h, nil, PeerRelayConfig{})

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

	pr := NewPeerRelay(h, nil, PeerRelayConfig{})

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

	pr := NewPeerRelay(h, nil, PeerRelayConfig{})

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
	pr := NewPeerRelay(h, m, PeerRelayConfig{})

	if err := pr.Enable(); err != nil {
		t.Fatalf("Enable with metrics: %v", err)
	}
	defer pr.Disable()

	if !pr.Enabled() {
		t.Error("should be enabled")
	}
}

func TestPeerRelay_DefaultConfig(t *testing.T) {
	d := DefaultPeerRelayConfig()
	if d.MaxReservations > 10 {
		t.Errorf("MaxReservations too high: %d", d.MaxReservations)
	}
	if d.MaxCircuits > 32 {
		t.Errorf("MaxCircuits too high: %d", d.MaxCircuits)
	}
	if d.MaxReservationsPerPeer != 1 {
		t.Errorf("MaxReservationsPerPeer should be 1, got %d", d.MaxReservationsPerPeer)
	}
}

func TestPeerRelay_ForcedEnabled(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil, PeerRelayConfig{Enabled: "true"})

	// With no public IP, "true" should still enable
	summary := &InterfaceSummary{HasGlobalIPv4: false, HasGlobalIPv6: false}
	pr.AutoDetect(summary)
	if !pr.Enabled() {
		t.Error("forced 'true' should enable even without public IP")
	}

	pr.Disable()
}

func TestPeerRelay_ForcedDisabled(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil, PeerRelayConfig{Enabled: "false"})

	// With public IP, "false" should NOT enable
	summary := &InterfaceSummary{HasGlobalIPv4: true, GlobalIPv4Addrs: []string{"203.0.113.50"}}
	pr.AutoDetect(summary)
	if pr.Enabled() {
		t.Error("forced 'false' should not enable even with public IP")
	}
}

func TestPeerRelay_OnStateChange(t *testing.T) {
	h, err := libp2p.New(
		libp2p.NoSecurity,
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
	)
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	defer h.Close()

	pr := NewPeerRelay(h, nil, PeerRelayConfig{})

	var callbackState []bool
	pr.OnStateChange(func(enabled bool) {
		callbackState = append(callbackState, enabled)
	})

	pr.Enable()
	pr.Disable()

	if len(callbackState) != 2 {
		t.Fatalf("expected 2 callbacks, got %d", len(callbackState))
	}
	if !callbackState[0] {
		t.Error("first callback should be true (enabled)")
	}
	if callbackState[1] {
		t.Error("second callback should be false (disabled)")
	}
}
