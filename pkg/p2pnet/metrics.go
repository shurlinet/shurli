package p2pnet

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all custom shurli Prometheus metrics.
// Uses an isolated prometheus.Registry so shurli metrics don't collide
// with the global default registry. Each test gets its own Metrics instance.
type Metrics struct {
	Registry *prometheus.Registry

	// Proxy metrics
	ProxyBytesTotal       *prometheus.CounterVec
	ProxyConnectionsTotal *prometheus.CounterVec
	ProxyActiveConns      *prometheus.GaugeVec
	ProxyDurationSeconds  *prometheus.HistogramVec

	// Auth metrics
	AuthDecisionsTotal *prometheus.CounterVec

	// Hole punch metrics (enhanced from existing holePunchTracer)
	HolePunchTotal           *prometheus.CounterVec
	HolePunchDurationSeconds *prometheus.HistogramVec

	// Daemon API metrics
	DaemonRequestsTotal          *prometheus.CounterVec
	DaemonRequestDurationSeconds *prometheus.HistogramVec

	// Path dial metrics
	PathDialTotal           *prometheus.CounterVec
	PathDialDurationSeconds *prometheus.HistogramVec

	// Connected peers (tracked by PathTracker)
	ConnectedPeers *prometheus.GaugeVec

	// Network change events (tracked by NetworkMonitor)
	NetworkChangeTotal *prometheus.CounterVec

	// STUN probe metrics
	STUNProbeTotal *prometheus.CounterVec

	// mDNS discovery metrics
	MDNSDiscoveredTotal *prometheus.CounterVec

	// PeerManager reconnection metrics
	PeerManagerReconnectTotal *prometheus.CounterVec

	// Network intelligence (presence) metrics
	NetIntelSentTotal     *prometheus.CounterVec
	NetIntelReceivedTotal *prometheus.CounterVec

	// Interface metrics
	InterfaceCount *prometheus.GaugeVec

	// Vault metrics (seal state, unseal attempts, lockout)
	VaultSealed            prometheus.Gauge
	VaultSealOpsTotal      *prometheus.CounterVec
	VaultUnsealTotal       *prometheus.CounterVec
	VaultUnsealLockedPeers prometheus.Gauge

	// Deposit metrics (invite lifecycle)
	DepositOpsTotal *prometheus.CounterVec
	DepositPending  prometheus.Gauge

	// Pairing metrics (relay-mediated pairing)
	PairingTotal *prometheus.CounterVec

	// Macaroon metrics (token verification)
	MacaroonVerifyTotal *prometheus.CounterVec

	// Admin socket metrics
	AdminRequestTotal          *prometheus.CounterVec
	AdminRequestDurationSeconds *prometheus.HistogramVec

	// Build info
	BuildInfo *prometheus.GaugeVec
}

// NewMetrics creates a new Metrics instance with all collectors registered
// on an isolated registry. The version and goVersion are recorded as labels
// on the shurli_info gauge.
func NewMetrics(version, goVersion string) *Metrics {
	reg := prometheus.NewRegistry()

	// Standard Go runtime + process metrics
	reg.MustRegister(prometheus.NewGoCollector())
	reg.MustRegister(prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))

	m := &Metrics{
		Registry: reg,

		ProxyBytesTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_proxy_bytes_total",
				Help: "Total bytes transferred through proxy connections.",
			},
			[]string{"direction", "service"},
		),
		ProxyConnectionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_proxy_connections_total",
				Help: "Total number of proxy connections established.",
			},
			[]string{"service"},
		),
		ProxyActiveConns: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "shurli_proxy_active_connections",
				Help: "Number of currently active proxy connections.",
			},
			[]string{"service"},
		),
		ProxyDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "shurli_proxy_duration_seconds",
				Help:    "Duration of proxy connections in seconds.",
				Buckets: prometheus.ExponentialBuckets(1, 2, 12), // 1s to ~1h
			},
			[]string{"service"},
		),

		AuthDecisionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_auth_decisions_total",
				Help: "Total number of authentication decisions.",
			},
			[]string{"decision"},
		),

		HolePunchTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_holepunch_total",
				Help: "Total number of hole punch attempts.",
			},
			[]string{"result"},
		),
		HolePunchDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "shurli_holepunch_duration_seconds",
				Help:    "Duration of hole punch attempts in seconds.",
				Buckets: prometheus.ExponentialBuckets(0.01, 2, 10), // 10ms to ~10s
			},
			[]string{"result"},
		),

		DaemonRequestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_daemon_requests_total",
				Help: "Total number of daemon API requests.",
			},
			[]string{"method", "path", "status"},
		),
		DaemonRequestDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "shurli_daemon_request_duration_seconds",
				Help:    "Duration of daemon API requests in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"method", "path", "status"},
		),

		PathDialTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_path_dial_total",
				Help: "Total number of path dial attempts.",
			},
			[]string{"path_type", "result"},
		),
		PathDialDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "shurli_path_dial_duration_seconds",
				Help:    "Duration of path dial attempts in seconds.",
				Buckets: prometheus.ExponentialBuckets(0.1, 2, 10), // 100ms to ~50s
			},
			[]string{"path_type"},
		),

		ConnectedPeers: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "shurli_connected_peers",
				Help: "Number of connected peers by path type, transport, and IP version.",
			},
			[]string{"path_type", "transport", "ip_version"},
		),

		NetworkChangeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_network_change_total",
				Help: "Total number of network interface changes detected.",
			},
			[]string{"change_type"},
		),

		STUNProbeTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_stun_probe_total",
				Help: "Total number of STUN probe attempts.",
			},
			[]string{"result"},
		),

		MDNSDiscoveredTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_mdns_discovered_total",
				Help: "Total mDNS discovery events by result.",
			},
			[]string{"result"},
		),

		PeerManagerReconnectTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_peermanager_reconnect_total",
				Help: "Total PeerManager reconnection attempts by result (success, failure, backoff_skip).",
			},
			[]string{"result"},
		),

		NetIntelSentTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_netintel_sent_total",
				Help: "Total presence announcements sent by result (success, error).",
			},
			[]string{"result"},
		),
		NetIntelReceivedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_netintel_received_total",
				Help: "Total presence announcements received by result (accepted, forwarded, rejected, invalid).",
			},
			[]string{"result"},
		),

		InterfaceCount: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "shurli_interface_count",
				Help: "Number of network interfaces with global unicast addresses.",
			},
			[]string{"ip_version"},
		),

		VaultSealed: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "shurli_vault_sealed",
				Help: "Current vault seal state (1 = sealed, 0 = unsealed).",
			},
		),
		VaultSealOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_vault_seal_operations_total",
				Help: "Total vault seal/unseal transitions by trigger.",
			},
			[]string{"trigger"},
		),
		VaultUnsealTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_vault_unseal_total",
				Help: "Total remote vault unseal attempts by result.",
			},
			[]string{"result"},
		),
		VaultUnsealLockedPeers: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "shurli_vault_unseal_locked_peers",
				Help: "Number of peers currently locked out or permanently blocked from remote unseal.",
			},
		),

		DepositOpsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_deposit_operations_total",
				Help: "Total invite deposit operations by type.",
			},
			[]string{"operation"},
		),
		DepositPending: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "shurli_deposit_pending",
				Help: "Number of pending (unconsumed) invite deposits.",
			},
		),

		PairingTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_pairing_total",
				Help: "Total relay-mediated pairing attempts by result.",
			},
			[]string{"result"},
		),

		MacaroonVerifyTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_macaroon_verify_total",
				Help: "Total macaroon verification attempts by result.",
			},
			[]string{"result"},
		),

		AdminRequestTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "shurli_admin_request_total",
				Help: "Total admin socket requests by endpoint and status.",
			},
			[]string{"endpoint", "status"},
		),
		AdminRequestDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "shurli_admin_request_duration_seconds",
				Help:    "Duration of admin socket requests in seconds.",
				Buckets: prometheus.DefBuckets,
			},
			[]string{"endpoint"},
		),

		BuildInfo: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "shurli_info",
				Help: "Build information for the running shurli instance.",
			},
			[]string{"version", "go_version"},
		),
	}

	// Register all collectors
	reg.MustRegister(
		m.ProxyBytesTotal,
		m.ProxyConnectionsTotal,
		m.ProxyActiveConns,
		m.ProxyDurationSeconds,
		m.AuthDecisionsTotal,
		m.HolePunchTotal,
		m.HolePunchDurationSeconds,
		m.DaemonRequestsTotal,
		m.DaemonRequestDurationSeconds,
		m.ConnectedPeers,
		m.NetworkChangeTotal,
		m.STUNProbeTotal,
		m.MDNSDiscoveredTotal,
		m.PeerManagerReconnectTotal,
		m.NetIntelSentTotal,
		m.NetIntelReceivedTotal,
		m.InterfaceCount,
		m.VaultSealed,
		m.VaultSealOpsTotal,
		m.VaultUnsealTotal,
		m.VaultUnsealLockedPeers,
		m.DepositOpsTotal,
		m.DepositPending,
		m.PairingTotal,
		m.MacaroonVerifyTotal,
		m.AdminRequestTotal,
		m.AdminRequestDurationSeconds,
		m.BuildInfo,
	)

	// Set build info gauge (always 1, labels carry the data)
	m.BuildInfo.WithLabelValues(version, goVersion).Set(1)

	return m
}

// Handler returns an http.Handler that serves the Prometheus metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
