package p2pnet

import (
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
)

// PeerRelayConfig controls peer relay behavior.
type PeerRelayConfig struct {
	// Enabled: "auto" (default/empty), "true", or "false".
	Enabled string

	// Resource limits (zero values use defaults).
	MaxReservations        int
	MaxCircuits            int
	MaxReservationsPerPeer int
	MaxReservationsPerIP   int
	MaxReservationsPerASN  int
	BufferSize             int
	CircuitDuration        time.Duration
	CircuitDataLimit       int64 // bytes per direction
}

// DefaultPeerRelayConfig returns the default peer relay configuration.
func DefaultPeerRelayConfig() PeerRelayConfig {
	return PeerRelayConfig{
		Enabled:                "auto",
		MaxReservations:        4,
		MaxCircuits:            16,
		MaxReservationsPerPeer: 1,
		MaxReservationsPerIP:   2,
		MaxReservationsPerASN:  4,
		BufferSize:             4096,
		CircuitDuration:        10 * time.Minute,
		CircuitDataLimit:       1 << 17, // 128KB
	}
}

// applyDefaults fills zero values with defaults.
func (c *PeerRelayConfig) applyDefaults() {
	d := DefaultPeerRelayConfig()
	if c.Enabled == "" {
		c.Enabled = d.Enabled
	}
	if c.MaxReservations == 0 {
		c.MaxReservations = d.MaxReservations
	}
	if c.MaxCircuits == 0 {
		c.MaxCircuits = d.MaxCircuits
	}
	if c.MaxReservationsPerPeer == 0 {
		c.MaxReservationsPerPeer = d.MaxReservationsPerPeer
	}
	if c.MaxReservationsPerIP == 0 {
		c.MaxReservationsPerIP = d.MaxReservationsPerIP
	}
	if c.MaxReservationsPerASN == 0 {
		c.MaxReservationsPerASN = d.MaxReservationsPerASN
	}
	if c.BufferSize == 0 {
		c.BufferSize = d.BufferSize
	}
	if c.CircuitDuration == 0 {
		c.CircuitDuration = d.CircuitDuration
	}
	if c.CircuitDataLimit == 0 {
		c.CircuitDataLimit = d.CircuitDataLimit
	}
}

// PeerRelay enables a peer with a public IP to act as a circuit relay
// for other authorized peers. This reduces dependence on the central VPS
// relay by letting any peer with a routable address serve as an alternative.
//
// Security: The host-level ConnectionGater (authorized_keys peer ID allowlist)
// is the access control for peer relays. Only peers that pass the gater can
// connect, and therefore only they can make reservations or create circuits.
// No separate circuit ACL is applied because all connected peers are already
// vetted by the gater. If connection gating is disabled (no authorized_keys),
// this relay is open to any DHT peer - this is by design for open networks.
type PeerRelay struct {
	host    host.Host
	metrics *Metrics // nil-safe
	config  PeerRelayConfig
	enabled atomic.Bool
	relay   *relayv2.Relay

	onStateChange func(enabled bool) // optional callback
}

// NewPeerRelay creates a PeerRelay. The relay is not enabled until Enable() is called.
// Pass a zero-value config to use defaults. Metrics is optional (nil-safe).
func NewPeerRelay(h host.Host, m *Metrics, cfg PeerRelayConfig) *PeerRelay {
	cfg.applyDefaults()
	return &PeerRelay{
		host:    h,
		metrics: m,
		config:  cfg,
	}
}

// OnStateChange registers a callback invoked when the relay is enabled or disabled.
// Used by relay discovery to start/stop DHT advertisement.
func (pr *PeerRelay) OnStateChange(fn func(enabled bool)) {
	pr.onStateChange = fn
}

// Enable starts the circuit relay v2 service on this host.
// Returns nil if already enabled.
func (pr *PeerRelay) Enable() error {
	if pr.enabled.Load() {
		return nil // already running
	}

	resources := relayv2.Resources{
		Limit: &relayv2.RelayLimit{
			Duration: pr.config.CircuitDuration,
			Data:     pr.config.CircuitDataLimit,
		},
		ReservationTTL:         30 * time.Minute,
		MaxReservations:        pr.config.MaxReservations,
		MaxCircuits:            pr.config.MaxCircuits,
		BufferSize:             pr.config.BufferSize,
		MaxReservationsPerPeer: pr.config.MaxReservationsPerPeer,
		MaxReservationsPerIP:   pr.config.MaxReservationsPerIP,
		MaxReservationsPerASN:  pr.config.MaxReservationsPerASN,
	}

	limit := &relayv2.RelayLimit{
		Duration: pr.config.CircuitDuration,
		Data:     pr.config.CircuitDataLimit,
	}

	r, err := relayv2.New(pr.host,
		relayv2.WithResources(resources),
		relayv2.WithLimit(limit),
	)
	if err != nil {
		return err
	}

	pr.relay = r
	pr.enabled.Store(true)
	slog.Info("peer relay enabled",
		"max_reservations", pr.config.MaxReservations,
		"max_circuits", pr.config.MaxCircuits,
	)

	if pr.onStateChange != nil {
		pr.onStateChange(true)
	}

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

	if pr.onStateChange != nil {
		pr.onStateChange(false)
	}
}

// Enabled returns true if the relay is currently active.
func (pr *PeerRelay) Enabled() bool {
	return pr.enabled.Load()
}

// AutoDetect enables or disables the relay based on config and network state.
// "true" = always on, "false" = always off, "auto" = enable if public IP detected.
func (pr *PeerRelay) AutoDetect(summary *InterfaceSummary) {
	if summary == nil {
		return
	}

	switch pr.config.Enabled {
	case "true":
		if !pr.enabled.Load() {
			slog.Info("peer relay: forced enabled by config")
			if err := pr.Enable(); err != nil {
				slog.Warn("peer relay: failed to enable", "error", err)
			}
		}
	case "false":
		if pr.enabled.Load() {
			slog.Info("peer relay: forced disabled by config")
			pr.Disable()
		}
	default: // "auto"
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
}
