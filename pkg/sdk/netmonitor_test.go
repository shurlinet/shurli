package sdk

import (
	"context"
	"testing"
	"time"
)

// Test fixtures populate both Global* and All* fields. Global* feeds
// reachability classification (HasGlobalIPv*, Global*Addrs), while All*
// feeds the authoritative Added/Removed diff via makeIPSet. Every global
// IP must also appear in AllIPv*Addrs — they are a superset.

func TestDiffSummaries_NoChange(t *testing.T) {
	a := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
		AllIPv6Addrs:    []string{"2001:db8::1"},
	}
	b := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
		AllIPv6Addrs:    []string{"2001:db8::1"},
	}

	change := diffSummaries(a, b)
	if change != nil {
		t.Errorf("expected nil change, got %+v", change)
	}
}

func TestDiffSummaries_IPAdded(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv4:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
		AllIPv6Addrs:    []string{"2001:db8::1"},
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change, got nil")
	}
	if len(change.Added) != 1 || change.Added[0] != "2001:db8::1" {
		t.Errorf("Added = %v, want [2001:db8::1]", change.Added)
	}
	if len(change.Removed) != 0 {
		t.Errorf("Removed = %v, want []", change.Removed)
	}
	if !change.IPv6Changed {
		t.Error("expected IPv6Changed=true")
	}
}

func TestDiffSummaries_IPRemoved(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
		AllIPv6Addrs:    []string{"2001:db8::1"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv4:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change, got nil")
	}
	if len(change.Removed) != 1 || change.Removed[0] != "2001:db8::1" {
		t.Errorf("Removed = %v, want [2001:db8::1]", change.Removed)
	}
	if !change.IPv6Changed {
		t.Error("expected IPv6Changed=true")
	}
}

func TestDiffSummaries_NilOld(t *testing.T) {
	current := &InterfaceSummary{
		HasGlobalIPv4:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		AllIPv4Addrs:    []string{"203.0.113.50"},
	}

	change := diffSummaries(nil, current)
	if change == nil {
		t.Fatal("expected change from nil old")
	}
	if len(change.Added) != 1 {
		t.Errorf("Added = %v, want 1 item", change.Added)
	}
}

// TestDiffSummaries_PrivateIPv4Removed covers the regression that kept
// zombie conns alive on phone-hotspot / carrier-NAT / home-ISP transitions:
// a private-IPv4 interface vanishing must show up in change.Removed so the
// serve_common handler's authoritative gate opens and CloseStaleConnections
// runs against it. Before the fix, makeIPSet read GlobalIPv4Addrs only and
// ignored private IPs entirely.
func TestDiffSummaries_PrivateIPv4Removed(t *testing.T) {
	// Was on network A (carrier NAT private IPv4 only), now on network B
	// (different private IPv4 + global IPv6 from the new uplink).
	old := &InterfaceSummary{
		AllIPv4Addrs: []string{"10.0.1.3"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8:1::1"},
		AllIPv4Addrs:    []string{"10.0.2.144"},
		AllIPv6Addrs:    []string{"2001:db8:1::1"},
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change, got nil")
	}
	// The old private IPv4 must appear in Removed so the network-change
	// handler's len(change.Removed) > 0 gate opens.
	foundRemoved := false
	for _, ip := range change.Removed {
		if ip == "10.0.1.3" {
			foundRemoved = true
		}
	}
	if !foundRemoved {
		t.Errorf("expected 10.0.1.3 in Removed, got %v", change.Removed)
	}
	// The new private IPv4 + global IPv6 must appear in Added.
	foundPrivate := false
	foundGlobal := false
	for _, ip := range change.Added {
		if ip == "10.0.2.144" {
			foundPrivate = true
		}
		if ip == "2001:db8:1::1" {
			foundGlobal = true
		}
	}
	if !foundPrivate || !foundGlobal {
		t.Errorf("expected 10.0.2.144 and 2001:db8:1::1 in Added, got %v", change.Added)
	}
}

// TestDiffSummaries_PrivateIPv4OnlySwap covers the hardest case from physical
// testing: one private-IPv4 subnet → a different private-IPv4 subnet, with NO
// global IPs on either side (neither network gives the client global IPv6 in
// this scenario). Before the fix, diffSummaries only saw gateway_changed and
// returned Added=[] Removed=[] — the serve_common gate stayed closed and the
// zombie conn was never killed.
func TestDiffSummaries_PrivateIPv4OnlySwap(t *testing.T) {
	old := &InterfaceSummary{
		AllIPv4Addrs:   []string{"10.0.2.144"},
		DefaultGateway: "10.0.2.1",
	}
	current := &InterfaceSummary{
		AllIPv4Addrs:   []string{"10.0.3.58"},
		DefaultGateway: "10.0.3.1",
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change, got nil")
	}
	if len(change.Removed) != 1 || change.Removed[0] != "10.0.2.144" {
		t.Errorf("Removed = %v, want [10.0.2.144]", change.Removed)
	}
	if len(change.Added) != 1 || change.Added[0] != "10.0.3.58" {
		t.Errorf("Added = %v, want [10.0.3.58]", change.Added)
	}
	if !change.GatewayChanged {
		t.Error("expected GatewayChanged=true")
	}
}

// TestDiffSummaries_PrivateIPv4Stable verifies that a stable private IPv4
// (same DHCP lease, same subnet, no reassignment) produces no diff. This
// guards against false-positive Added/Removed events on DHCP lease renewal.
func TestDiffSummaries_PrivateIPv4Stable(t *testing.T) {
	old := &InterfaceSummary{
		AllIPv4Addrs:   []string{"192.168.1.117"},
		DefaultGateway: "192.168.1.1",
	}
	current := &InterfaceSummary{
		AllIPv4Addrs:   []string{"192.168.1.117"},
		DefaultGateway: "192.168.1.1",
	}

	change := diffSummaries(old, current)
	if change != nil {
		t.Errorf("expected nil change for stable private IPv4, got %+v", change)
	}
}

func TestDiffSummaries_BothNil(t *testing.T) {
	change := diffSummaries(nil, &InterfaceSummary{})
	if change != nil {
		t.Errorf("expected nil change for nil/empty, got %+v", change)
	}
}

func TestNetworkMonitor_Run(t *testing.T) {
	// Verify the monitor starts and stops cleanly
	called := make(chan struct{}, 10)
	mon := NewNetworkMonitor(func(c *NetworkChange) {
		select {
		case called <- struct{}{}:
		default:
		}
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	done := make(chan struct{})
	go func() {
		mon.Run(ctx)
		close(done)
	}()

	// Wait for context cancellation
	<-ctx.Done()

	select {
	case <-done:
		// Good - Run returned after context cancellation
	case <-time.After(3 * time.Second):
		t.Fatal("NetworkMonitor.Run did not return after context cancellation")
	}
}

func TestMakeIPSet(t *testing.T) {
	// makeIPSet now reads AllIPv*Addrs (superset of globals). The set
	// must cover every unicast IP — private, global, CGNAT, ULA. Reading
	// Global* only was the root cause of the private-IPv4 transition
	// blindness (see diffSummaries comment).
	s := &InterfaceSummary{
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		AllIPv4Addrs:    []string{"203.0.113.50", "192.168.1.117", "10.0.3.58"},
		AllIPv6Addrs:    []string{"2001:db8::1", "fd00::1"},
	}
	set := makeIPSet(s)
	if len(set) != 5 {
		t.Errorf("expected 5 IPs in set, got %d: %v", len(set), set)
	}
	for _, want := range []string{"203.0.113.50", "192.168.1.117", "10.0.3.58", "2001:db8::1", "fd00::1"} {
		if !set[want] {
			t.Errorf("missing %s in set", want)
		}
	}
}

func TestMakeIPSet_Nil(t *testing.T) {
	set := makeIPSet(nil)
	if len(set) != 0 {
		t.Errorf("expected empty set for nil, got %d", len(set))
	}
}

func TestDiffSummaries_TunnelAdded(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8::1"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv6:    true,
		GlobalIPv6Addrs:  []string{"2001:db8::1"},
		TunnelInterfaces: []string{"utun5"},
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change when tunnel added, got nil")
	}
	if !change.TunnelChanged {
		t.Error("expected TunnelChanged=true")
	}
	// Global IPs didn't change
	if len(change.Added) != 0 || len(change.Removed) != 0 {
		t.Errorf("expected no IP changes, got added=%v removed=%v", change.Added, change.Removed)
	}
}

func TestDiffSummaries_TunnelRemoved(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv6:    true,
		GlobalIPv6Addrs:  []string{"2001:db8::1"},
		TunnelInterfaces: []string{"utun5"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8::1"},
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change when tunnel removed, got nil")
	}
	if !change.TunnelChanged {
		t.Error("expected TunnelChanged=true")
	}
}

func TestDiffSummaries_TunnelUnchanged(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv6:    true,
		GlobalIPv6Addrs:  []string{"2001:db8::1"},
		TunnelInterfaces: []string{"utun3"},
	}
	current := &InterfaceSummary{
		HasGlobalIPv6:    true,
		GlobalIPv6Addrs:  []string{"2001:db8::1"},
		TunnelInterfaces: []string{"utun3"},
	}

	change := diffSummaries(old, current)
	if change != nil {
		t.Errorf("expected nil change when tunnel unchanged, got %+v", change)
	}
}

func TestTunnelSetChanged(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []string
		changed bool
	}{
		{"both empty", nil, nil, false},
		{"same", []string{"utun3"}, []string{"utun3"}, false},
		{"added", nil, []string{"utun5"}, true},
		{"removed", []string{"utun5"}, nil, true},
		{"different", []string{"utun3"}, []string{"utun5"}, true},
		{"added second", []string{"utun3"}, []string{"utun3", "utun5"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tunnelSetChanged(tt.a, tt.b)
			if got != tt.changed {
				t.Errorf("tunnelSetChanged = %v, want %v", got, tt.changed)
			}
		})
	}
}

func TestIsTunnelInterface(t *testing.T) {
	tests := []struct {
		name   string
		iface  string
		tunnel bool
	}{
		{"utun macOS", "utun5", true},
		{"tun Linux", "tun0", true},
		{"wg WireGuard", "wg0", true},
		{"ppp L2TP", "ppp0", true},
		{"en0 WiFi", "en0", false},
		{"lo0 loopback", "lo0", false},
		{"bridge", "bridge0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isTunnelInterface(tt.iface)
			if got != tt.tunnel {
				t.Errorf("isTunnelInterface(%q) = %v, want %v", tt.iface, got, tt.tunnel)
			}
		})
	}
}

func TestDiffSummaries_GatewayChanged(t *testing.T) {
	old := &InterfaceSummary{
		DefaultGateway: "10.0.2.1",
	}
	current := &InterfaceSummary{
		DefaultGateway: "192.168.100.1",
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change when gateway changed, got nil")
	}
	if !change.GatewayChanged {
		t.Error("expected GatewayChanged=true")
	}
	if !change.IPv4Changed {
		t.Error("expected IPv4Changed=true (gateway change implies IPv4 change)")
	}
	// No global IPs changed
	if len(change.Added) != 0 || len(change.Removed) != 0 {
		t.Errorf("expected no IP changes, got added=%v removed=%v", change.Added, change.Removed)
	}
}

func TestDiffSummaries_GatewayUnchanged(t *testing.T) {
	old := &InterfaceSummary{
		DefaultGateway: "10.0.2.1",
	}
	current := &InterfaceSummary{
		DefaultGateway: "10.0.2.1",
	}

	change := diffSummaries(old, current)
	if change != nil {
		t.Errorf("expected nil change when gateway unchanged, got %+v", change)
	}
}

func TestDiffSummaries_GatewayEmptyCurrentIgnored(t *testing.T) {
	// Empty current gateway (intermittent lookup failure) should NOT fire
	tests := []struct {
		name      string
		oldGW     string
		currentGW string
	}{
		{"current empty", "10.0.2.1", ""},
		{"both empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			old := &InterfaceSummary{DefaultGateway: tt.oldGW}
			current := &InterfaceSummary{DefaultGateway: tt.currentGW}

			change := diffSummaries(old, current)
			if change != nil {
				t.Errorf("expected nil change, got %+v", change)
			}
		})
	}
}

func TestDiffSummaries_GatewayEmptyOldFires(t *testing.T) {
	// Empty old + non-empty current = genuine network appearance (e.g.,
	// daemon booted without WiFi, then WiFi connects). Must fire to
	// prevent permanent gateway blindness.
	old := &InterfaceSummary{DefaultGateway: ""}
	current := &InterfaceSummary{DefaultGateway: "10.0.2.1"}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change when gateway appears (empty old), got nil")
	}
	if !change.GatewayChanged {
		t.Error("expected GatewayChanged=true")
	}
	if !change.IPv4Changed {
		t.Error("expected IPv4Changed=true")
	}
}

func TestDiffSummaries_GatewayWithGlobalIPChange(t *testing.T) {
	old := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8::1"},
		DefaultGateway:  "10.0.2.1",
	}
	current := &InterfaceSummary{
		HasGlobalIPv6:   true,
		GlobalIPv6Addrs: []string{"2001:db8::2"},
		DefaultGateway:  "192.168.100.1",
	}

	change := diffSummaries(old, current)
	if change == nil {
		t.Fatal("expected change, got nil")
	}
	if !change.GatewayChanged {
		t.Error("expected GatewayChanged=true")
	}
	if !change.IPv6Changed {
		t.Error("expected IPv6Changed=true")
	}
	if !change.IPv4Changed {
		t.Error("expected IPv4Changed=true (gateway change)")
	}
}

func TestIPVersionChanged(t *testing.T) {
	tests := []struct {
		name    string
		a, b    []string
		changed bool
	}{
		{"same", []string{"1.2.3.4"}, []string{"1.2.3.4"}, false},
		{"added", []string{"1.2.3.4"}, []string{"1.2.3.4", "5.6.7.8"}, true},
		{"removed", []string{"1.2.3.4", "5.6.7.8"}, []string{"1.2.3.4"}, true},
		{"replaced", []string{"1.2.3.4"}, []string{"5.6.7.8"}, true},
		{"empty both", nil, nil, false},
		{"empty to one", nil, []string{"1.2.3.4"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ipVersionChanged(tt.a, tt.b)
			if got != tt.changed {
				t.Errorf("ipVersionChanged = %v, want %v", got, tt.changed)
			}
		})
	}
}
