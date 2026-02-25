package daemon

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	dto "github.com/prometheus/client_model/go"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

func TestSanitizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/v1/status", "/v1/status"},
		{"/v1/services", "/v1/services"},
		{"/v1/peers", "/v1/peers"},
		{"/v1/auth", "/v1/auth"},
		{"/v1/ping", "/v1/ping"},
		{"/v1/auth/12D3KooWTest1234", "/v1/auth/:id"},
		{"/v1/connect/proxy-1", "/v1/connect/:id"},
		{"/v1/expose/ssh", "/v1/expose/:id"},
		// Trailing slashes are stripped before matching
		{"/v1/auth/someid/", "/v1/auth/:id"},
		// Unknown 3-segment paths pass through
		{"/v1/unknown/thing", "/v1/unknown/thing"},
		// Root path
		{"/", "/"},
		// Non-API paths
		{"/metrics", "/metrics"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := sanitizePath(tt.input)
			if got != tt.want {
				t.Errorf("sanitizePath(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInstrumentHandler_NilPassthrough(t *testing.T) {
	called := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	})

	// With nil metrics and nil audit, the handler should be returned unchanged
	wrapped := InstrumentHandler(handler, nil, nil)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if !called {
		t.Error("handler was not called")
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestInstrumentHandler_RecordsMetrics(t *testing.T) {
	m := p2pnet.NewMetrics("test-0.1.0", runtime.Version())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := InstrumentHandler(handler, m, nil)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	val := gatherCounter(t, m, "shurli_daemon_requests_total", map[string]string{
		"method": "GET", "path": "/v1/status", "status": "200",
	})
	if val != 1 {
		t.Errorf("DaemonRequestsTotal = %v, want 1", val)
	}
}

func TestInstrumentHandler_CapturesErrorStatus(t *testing.T) {
	m := p2pnet.NewMetrics("test-0.1.0", runtime.Version())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	wrapped := InstrumentHandler(handler, m, nil)

	req := httptest.NewRequest("GET", "/v1/unknown", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}

	val := gatherCounter(t, m, "shurli_daemon_requests_total", map[string]string{
		"method": "GET", "path": "/v1/unknown", "status": "404",
	})
	if val != 1 {
		t.Errorf("DaemonRequestsTotal = %v, want 1", val)
	}
}

func TestInstrumentHandler_SanitizesPath(t *testing.T) {
	m := p2pnet.NewMetrics("test-0.1.0", runtime.Version())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := InstrumentHandler(handler, m, nil)

	// Request with a path parameter (peer ID in the URL)
	req := httptest.NewRequest("DELETE", "/v1/auth/12D3KooWTest1234567890", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// The metric should use the sanitized path
	val := gatherCounter(t, m, "shurli_daemon_requests_total", map[string]string{
		"method": "DELETE", "path": "/v1/auth/:id", "status": "200",
	})
	if val != 1 {
		t.Errorf("DaemonRequestsTotal with sanitized path = %v, want 1", val)
	}
}

func TestInstrumentHandler_RecordsDuration(t *testing.T) {
	m := p2pnet.NewMetrics("test-0.1.0", runtime.Version())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := InstrumentHandler(handler, m, nil)

	req := httptest.NewRequest("POST", "/v1/ping", nil)
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// Verify histogram sample count is 1
	count := gatherHistogramCount(t, m, "shurli_daemon_request_duration_seconds", map[string]string{
		"method": "POST", "path": "/v1/ping", "status": "200",
	})
	if count != 1 {
		t.Errorf("DaemonRequestDurationSeconds sample count = %d, want 1", count)
	}
}

func TestInstrumentHandler_MultipleRequests(t *testing.T) {
	m := p2pnet.NewMetrics("test-0.1.0", runtime.Version())

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := InstrumentHandler(handler, m, nil)

	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/v1/status", nil)
		rec := httptest.NewRecorder()
		wrapped.ServeHTTP(rec, req)
	}

	val := gatherCounter(t, m, "shurli_daemon_requests_total", map[string]string{
		"method": "GET", "path": "/v1/status", "status": "200",
	})
	if val != 5 {
		t.Errorf("DaemonRequestsTotal = %v, want 5", val)
	}
}

func TestStatusRecorder_DefaultStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	// If handler writes body without explicit WriteHeader, status should be 200
	sr.Write([]byte("hello"))

	if sr.status != http.StatusOK {
		t.Errorf("default status = %d, want 200", sr.status)
	}
}

func TestStatusRecorder_ExplicitStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	sr := &statusRecorder{ResponseWriter: rec, status: http.StatusOK}

	sr.WriteHeader(http.StatusCreated)

	if sr.status != http.StatusCreated {
		t.Errorf("status = %d, want 201", sr.status)
	}
}

// --- Test helpers using Registry.Gather() ---

// gatherCounter extracts a counter value from the metrics registry.
func gatherCounter(t *testing.T, m *p2pnet.Metrics, name string, labels map[string]string) float64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			if labelsMatch(metric.GetLabel(), labels) {
				return metric.GetCounter().GetValue()
			}
		}
	}
	return 0
}

// gatherHistogramCount extracts the sample count from a histogram.
func gatherHistogramCount(t *testing.T, m *p2pnet.Metrics, name string, labels map[string]string) uint64 {
	t.Helper()
	families, err := m.Registry.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, metric := range f.GetMetric() {
			if labelsMatch(metric.GetLabel(), labels) {
				return metric.GetHistogram().GetSampleCount()
			}
		}
	}
	return 0
}

// labelsMatch returns true if all expected labels are present with matching values.
func labelsMatch(pairs []*dto.LabelPair, expected map[string]string) bool {
	if len(pairs) != len(expected) {
		return false
	}
	for _, lp := range pairs {
		if expected[lp.GetName()] != lp.GetValue() {
			return false
		}
	}
	return true
}
