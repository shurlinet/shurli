package p2pnet

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNewMetrics(t *testing.T) {
	m := NewMetrics("0.1.0", "go1.26.0")
	if m == nil {
		t.Fatal("NewMetrics returned nil")
	}
	if m.Registry == nil {
		t.Fatal("Registry is nil")
	}
}

func TestMetricsIsolation(t *testing.T) {
	// Two Metrics instances should not share registries
	m1 := NewMetrics("0.1.0", "go1.26.0")
	m2 := NewMetrics("0.2.0", "go1.26.0")

	m1.AuthDecisionsTotal.WithLabelValues("allow").Inc()

	// Gather from m2 should not see m1's counter value
	families, err := m2.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}
	for _, f := range families {
		if f.GetName() == "peerup_auth_decisions_total" {
			for _, metric := range f.GetMetric() {
				if metric.GetCounter().GetValue() != 0 {
					t.Error("m2 registry saw m1 counter value; registries are not isolated")
				}
			}
		}
	}
}

func TestMetricsCounters(t *testing.T) {
	m := NewMetrics("test", "go1.26.0")

	m.ProxyBytesTotal.WithLabelValues("rx", "ssh").Add(1024)
	m.ProxyBytesTotal.WithLabelValues("tx", "ssh").Add(512)
	m.ProxyConnectionsTotal.WithLabelValues("ssh").Inc()
	m.ProxyActiveConns.WithLabelValues("ssh").Inc()
	m.ProxyDurationSeconds.WithLabelValues("ssh").Observe(5.0)
	m.AuthDecisionsTotal.WithLabelValues("allow").Inc()
	m.AuthDecisionsTotal.WithLabelValues("deny").Inc()
	m.HolePunchTotal.WithLabelValues("success").Inc()
	m.HolePunchDurationSeconds.WithLabelValues("success").Observe(0.5)
	m.DaemonRequestsTotal.WithLabelValues("GET", "/v1/status", "200").Inc()
	m.DaemonRequestDurationSeconds.WithLabelValues("GET", "/v1/status", "200").Observe(0.01)

	// Verify all metric families are present
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	expected := map[string]bool{
		"peerup_proxy_bytes_total":              false,
		"peerup_proxy_connections_total":        false,
		"peerup_proxy_active_connections":       false,
		"peerup_proxy_duration_seconds":         false,
		"peerup_auth_decisions_total":           false,
		"peerup_holepunch_total":                false,
		"peerup_holepunch_duration_seconds":     false,
		"peerup_daemon_requests_total":          false,
		"peerup_daemon_request_duration_seconds": false,
		"peerup_info":                           false,
	}

	for _, f := range families {
		if _, ok := expected[f.GetName()]; ok {
			expected[f.GetName()] = true
		}
	}

	for name, found := range expected {
		if !found {
			t.Errorf("metric family %q not found in gathered output", name)
		}
	}
}

func TestMetricsBuildInfo(t *testing.T) {
	m := NewMetrics("1.2.3", "go1.26.0")

	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather failed: %v", err)
	}

	for _, f := range families {
		if f.GetName() != "peerup_info" {
			continue
		}
		for _, metric := range f.GetMetric() {
			if metric.GetGauge().GetValue() != 1 {
				t.Errorf("build info gauge value = %f, want 1", metric.GetGauge().GetValue())
			}
			labels := make(map[string]string)
			for _, lp := range metric.GetLabel() {
				labels[lp.GetName()] = lp.GetValue()
			}
			if labels["version"] != "1.2.3" {
				t.Errorf("version label = %q, want %q", labels["version"], "1.2.3")
			}
			if labels["go_version"] != "go1.26.0" {
				t.Errorf("go_version label = %q, want %q", labels["go_version"], "go1.26.0")
			}
		}
	}
}

func TestMetricsHandler(t *testing.T) {
	m := NewMetrics("0.1.0", "go1.26.0")
	m.AuthDecisionsTotal.WithLabelValues("allow").Inc()

	handler := m.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("handler returned status %d, want 200", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	output := string(body)

	if !strings.Contains(output, "peerup_auth_decisions_total") {
		t.Error("handler output missing peerup_auth_decisions_total")
	}
	if !strings.Contains(output, "peerup_info") {
		t.Error("handler output missing peerup_info")
	}
	// Verify Go runtime metrics are present
	if !strings.Contains(output, "go_goroutines") {
		t.Error("handler output missing go_goroutines (Go runtime collector)")
	}
}

func TestMetricsNoLabelCollision(t *testing.T) {
	// Verify that creating metrics with valid label combinations doesn't panic
	m := NewMetrics("test", "go1.26.0")

	// Exercise all label combinations
	for _, dir := range []string{"rx", "tx"} {
		for _, svc := range []string{"ssh", "xrdp", "ollama"} {
			m.ProxyBytesTotal.WithLabelValues(dir, svc).Add(1)
		}
	}
	for _, decision := range []string{"allow", "deny"} {
		m.AuthDecisionsTotal.WithLabelValues(decision).Inc()
	}
	for _, result := range []string{"success", "failure"} {
		m.HolePunchTotal.WithLabelValues(result).Inc()
		m.HolePunchDurationSeconds.WithLabelValues(result).Observe(0.1)
	}

	// Gather should succeed without errors
	if _, err := m.Registry.Gather(); err != nil {
		t.Fatalf("Gather failed after exercising all labels: %v", err)
	}
}

func TestMetricsRegistryDoesNotUseGlobal(t *testing.T) {
	m := NewMetrics("test", "go1.26.0")

	// The peerup registry should be separate from the default global
	if m.Registry == prometheus.DefaultRegisterer {
		t.Error("Metrics registry is the global DefaultRegisterer; should be isolated")
	}
}
