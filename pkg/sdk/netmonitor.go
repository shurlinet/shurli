package sdk

import (
	"context"
	"log/slog"
	"time"
)

// NetworkChange describes what changed between two interface snapshots.
type NetworkChange struct {
	Added          []string // new global IPs
	Removed        []string // lost global IPs
	IPv6Changed    bool
	IPv4Changed    bool
	TunnelChanged  bool // VPN/tunnel interface appeared or disappeared
	GatewayChanged bool // default gateway IP changed (private IPv4 network switch)
}

// NetworkMonitor watches for network interface changes and calls onChange
// when global IP addresses are added or removed. The platform-specific
// implementation uses event-driven detection (macOS route socket, Linux
// Netlink) with polling as a fallback on other platforms.
type NetworkMonitor struct {
	onChange func(*NetworkChange)
	metrics  *Metrics // nil-safe
	previous *InterfaceSummary
}

// NewNetworkMonitor creates a NetworkMonitor. Metrics is optional (nil-safe).
func NewNetworkMonitor(onChange func(*NetworkChange), m *Metrics) *NetworkMonitor {
	return &NetworkMonitor{
		onChange: onChange,
		metrics: m,
	}
}

// Run blocks until the context is cancelled. It watches for network changes
// using platform-specific event sources and calls onChange when global IPs
// change. The initial interface snapshot is taken on start.
func (nm *NetworkMonitor) Run(ctx context.Context) {
	// Take initial snapshot
	summary, err := DiscoverInterfaces()
	if err != nil {
		slog.Warn("netmonitor: initial discovery failed", "error", err)
		summary = &InterfaceSummary{}
	}
	nm.previous = summary

	// Platform-specific event channel
	eventCh := make(chan struct{}, 1)
	go watchNetworkChanges(ctx, eventCh)

	// Debounce: network changes often come in bursts (multiple interfaces
	// updated within milliseconds). Wait 500ms after the last event before
	// running discovery.
	var debounceTimer *time.Timer

	for {
		select {
		case <-ctx.Done():
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			return

		case <-eventCh:
			if debounceTimer != nil {
				debounceTimer.Stop()
			}
			debounceTimer = time.AfterFunc(500*time.Millisecond, func() {
				nm.checkForChanges()
			})
		}
	}
}

// checkForChanges re-discovers interfaces and diffs against the previous snapshot.
func (nm *NetworkMonitor) checkForChanges() {
	current, err := DiscoverInterfaces()
	if err != nil {
		slog.Warn("netmonitor: discovery failed", "error", err)
		return
	}

	var prevGW string
	if nm.previous != nil {
		prevGW = nm.previous.DefaultGateway
	}
	slog.Debug("netmonitor: checking for changes",
		"prev_gateway", prevGW,
		"curr_gateway", current.DefaultGateway,
	)

	change := diffSummaries(nm.previous, current)
	if change == nil {
		return // no meaningful change
	}

	nm.previous = current

	slog.Info("netmonitor: network change detected",
		"added", len(change.Added),
		"removed", len(change.Removed),
		"ipv6_changed", change.IPv6Changed,
		"ipv4_changed", change.IPv4Changed,
		"tunnel_changed", change.TunnelChanged,
		"gateway_changed", change.GatewayChanged,
	)

	if nm.metrics != nil && nm.metrics.NetworkChangeTotal != nil {
		if change.IPv6Changed {
			nm.metrics.NetworkChangeTotal.WithLabelValues("ipv6").Inc()
		}
		if change.IPv4Changed {
			nm.metrics.NetworkChangeTotal.WithLabelValues("ipv4").Inc()
		}
		if change.TunnelChanged {
			nm.metrics.NetworkChangeTotal.WithLabelValues("tunnel").Inc()
		}
	}

	nm.onChange(change)
}

// diffSummaries compares two InterfaceSummary values and returns a NetworkChange
// if global IP addresses changed, or nil if they're the same.
func diffSummaries(old, current *InterfaceSummary) *NetworkChange {
	oldIPs := makeIPSet(old)
	newIPs := makeIPSet(current)

	var added, removed []string
	for ip := range newIPs {
		if !oldIPs[ip] {
			added = append(added, ip)
		}
	}
	for ip := range oldIPs {
		if !newIPs[ip] {
			removed = append(removed, ip)
		}
	}

	// Check if tunnel interfaces changed (VPN connect/disconnect).
	var oldTunnels []string
	if old != nil {
		oldTunnels = old.TunnelInterfaces
	}
	tunnelChanged := tunnelSetChanged(oldTunnels, current.TunnelInterfaces)

	// Check if default gateway changed (private IPv4 network switch).
	// Require current to be non-empty (ignore intermittent lookup failures)
	// but allow old to be empty (covers daemon boot without WiFi, then
	// WiFi connects - a genuine network change that should fire).
	var oldGateway string
	if old != nil {
		oldGateway = old.DefaultGateway
	}
	gatewayChanged := current.DefaultGateway != "" &&
		oldGateway != current.DefaultGateway

	if len(added) == 0 && len(removed) == 0 && !tunnelChanged && !gatewayChanged {
		return nil
	}

	// Safe access for nil old
	var oldIPv6, oldIPv4 bool
	var oldIPv6Addrs, oldIPv4Addrs []string
	if old != nil {
		oldIPv6 = old.HasGlobalIPv6
		oldIPv4 = old.HasGlobalIPv4
		oldIPv6Addrs = old.GlobalIPv6Addrs
		oldIPv4Addrs = old.GlobalIPv4Addrs
	}

	ipv4Changed := oldIPv4 != current.HasGlobalIPv4 ||
		ipVersionChanged(oldIPv4Addrs, current.GlobalIPv4Addrs) ||
		gatewayChanged

	return &NetworkChange{
		Added:          added,
		Removed:        removed,
		IPv6Changed:    oldIPv6 != current.HasGlobalIPv6 || ipVersionChanged(oldIPv6Addrs, current.GlobalIPv6Addrs),
		IPv4Changed:    ipv4Changed,
		TunnelChanged:  tunnelChanged,
		GatewayChanged: gatewayChanged,
	}
}

// makeIPSet creates a set of every unicast IP (private + global) from a
// summary, used by diffSummaries to compute Added/Removed.
//
// Originally this read the Global* fields only, which silently hid every
// private-IPv4 transition from the delta (e.g. one carrier-NAT private IPv4
// → a different carrier-NAT private IPv4, or a wired-LAN unplug). The
// network-change handler in serve_common gates CloseStaleConnections on
// len(change.Removed) > 0 — a deliberate "only kill conns when NetworkMonitor
// has an authoritative removal signal" policy. Reading only globals broke
// that policy: private-IP transitions produced an empty Removed list, the
// gate stayed closed, and conns bound to the dead interface lived on as
// zombies until TCP/QUIC keepalive killed them ~30s later (or indefinitely,
// if the connection gater's LAN-first guard kept the zombie alive).
//
// Reading AllIPv*Addrs makes NetworkMonitor genuinely authoritative for
// every unicast IP change. The gate in serve_common stays intact; its
// original intent (trust the NetworkMonitor signal) is preserved.
func makeIPSet(s *InterfaceSummary) map[string]bool {
	set := make(map[string]bool)
	if s == nil {
		return set
	}
	for _, ip := range s.AllIPv4Addrs {
		set[ip] = true
	}
	for _, ip := range s.AllIPv6Addrs {
		set[ip] = true
	}
	return set
}

// tunnelSetChanged returns true if the set of tunnel interface names differs.
func tunnelSetChanged(a, b []string) bool {
	if len(a) != len(b) {
		return true
	}
	set := make(map[string]bool, len(a))
	for _, name := range a {
		set[name] = true
	}
	for _, name := range b {
		if !set[name] {
			return true
		}
	}
	return false
}

// ipVersionChanged returns true if the IP address lists differ.
func ipVersionChanged(a, b []string) bool {
	if len(a) != len(b) {
		return true
	}
	setA := make(map[string]bool, len(a))
	for _, ip := range a {
		setA[ip] = true
	}
	for _, ip := range b {
		if !setA[ip] {
			return true
		}
	}
	return false
}
