package p2pnet

import (
	"context"
	"testing"
	"time"
)

func TestDiffSummaries_NoChange(t *testing.T) {
	a := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
	}
	b := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
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
	}
	current := &InterfaceSummary{
		HasGlobalIPv4:   true,
		HasGlobalIPv6:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
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
	}
	current := &InterfaceSummary{
		HasGlobalIPv4:   true,
		GlobalIPv4Addrs: []string{"203.0.113.50"},
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
	}

	change := diffSummaries(nil, current)
	if change == nil {
		t.Fatal("expected change from nil old")
	}
	if len(change.Added) != 1 {
		t.Errorf("Added = %v, want 1 item", change.Added)
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
	s := &InterfaceSummary{
		GlobalIPv4Addrs: []string{"203.0.113.50", "198.51.100.1"},
		GlobalIPv6Addrs: []string{"2001:db8::1"},
	}
	set := makeIPSet(s)
	if len(set) != 3 {
		t.Errorf("expected 3 IPs in set, got %d", len(set))
	}
	if !set["203.0.113.50"] {
		t.Error("missing 203.0.113.50")
	}
	if !set["2001:db8::1"] {
		t.Error("missing 2001:db8::1")
	}
}

func TestMakeIPSet_Nil(t *testing.T) {
	set := makeIPSet(nil)
	if len(set) != 0 {
		t.Errorf("expected empty set for nil, got %d", len(set))
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
