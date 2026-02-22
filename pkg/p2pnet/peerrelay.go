package p2pnet

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// PeerRelay enables a peer with a public IP to act as a circuit relay
// for other authorized peers. This reduces dependence on the central VPS
// relay by letting any peer with a routable address serve as an alternative.
//
// Security: The existing ConnectionGater (authorized_keys peer ID allowlist)
// applies to relay connections. Only peers in the authorized_keys file can
// make reservations or create circuits through this relay.
type PeerRelay struct {
	host    host.Host
	metrics *Metrics // nil-safe
	enabled atomic.Bool
	relay   *relayv2.Relay
}

// PeerRelayResources defines conservative resource limits for peer relays.
// These are much tighter than the VPS relay to avoid overloading home connections.
var PeerRelayResources = relayv2.Resources{
	Limit: &relayv2.RelayLimit{
		Duration: 10 * time.Minute,
		Data:     1 << 17, // 128KB per direction
	},
	ReservationTTL:         30 * time.Minute,
	MaxReservations:        4,
	MaxCircuits:            16,
	BufferSize:             4096,
	MaxReservationsPerPeer: 1,
	MaxReservationsPerIP:   2,
	MaxReservationsPerASN:  4,
}

// PeerRelayLimit is the per-circuit limit for peer relays.
var PeerRelayLimit = &relayv2.RelayLimit{
	Duration: 10 * time.Minute,
	Data:     1 << 17, // 128KB per direction
}

// NewPeerRelay creates a PeerRelay. The relay is not enabled until Enable() is called.
// Metrics is optional (nil-safe).
func NewPeerRelay(h host.Host, m *Metrics) *PeerRelay {
	return &PeerRelay{
		host:    h,
		metrics: m,
	}
}

// Enable starts the circuit relay v2 service on this host.
// Returns nil if already enabled.
func (pr *PeerRelay) Enable() error {
	if pr.enabled.Load() {
		return nil // already running
	}

	r, err := relayv2.New(pr.host,
		relayv2.WithResources(PeerRelayResources),
		relayv2.WithLimit(PeerRelayLimit),
	)
	if err != nil {
		return err
	}

	pr.relay = r
	pr.enabled.Store(true)
	slog.Info("peer relay enabled",
		"max_reservations", PeerRelayResources.MaxReservations,
		"max_circuits", PeerRelayResources.MaxCircuits,
	)
	return nil
}

// Disable stops the circuit relay service.
func (pr *PeerRelay) Disable() {
	if !pr.enabled.Load() {
		return
	}
	if pr.relay != nil {
		pr.relay.Close()
		pr.relay = nil
	}
	pr.enabled.Store(false)
	slog.Info("peer relay disabled")
}

// Enabled returns true if the relay is currently active.
func (pr *PeerRelay) Enabled() bool {
	return pr.enabled.Load()
}

// AutoDetect enables the relay if the host has a global (routable) IP address,
// or disables it if global addresses are lost. This should be called after
// interface discovery (I-a) and on network change events (I-d).
func (pr *PeerRelay) AutoDetect(summary *InterfaceSummary) {
	if summary == nil {
		return
	}

	hasPublic := summary.HasGlobalIPv4 || summary.HasGlobalIPv6
	if hasPublic && !pr.enabled.Load() {
		slog.Info("peer relay: public IP detected, enabling relay")
		if err := pr.Enable(); err != nil {
			slog.Warn("peer relay: failed to enable", "error", err)
		}
	} else if !hasPublic && pr.enabled.Load() {
		slog.Info("peer relay: no public IP, disabling relay")
		pr.Disable()
	}
}
