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

	// Interface metrics
	InterfaceCount *prometheus.GaugeVec

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

		InterfaceCount: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "shurli_interface_count",
				Help: "Number of network interfaces with global unicast addresses.",
			},
			[]string{"ip_version"},
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
		m.InterfaceCount,
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
