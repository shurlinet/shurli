package daemon

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// statusRecorder wraps http.ResponseWriter to capture the status code.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (sr *statusRecorder) WriteHeader(code int) {
	sr.status = code
	sr.ResponseWriter.WriteHeader(code)
}

// InstrumentHandler wraps an HTTP handler with Prometheus metrics and audit logging.
// If both metrics and audit are nil, the handler is returned unchanged (zero overhead).
func InstrumentHandler(next http.Handler, metrics *p2pnet.Metrics, audit *p2pnet.AuditLogger) http.Handler {
	if metrics == nil && audit == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		duration := time.Since(start).Seconds()
		path := sanitizePath(r.URL.Path)
		status := strconv.Itoa(rec.status)

		if metrics != nil {
			metrics.DaemonRequestsTotal.WithLabelValues(r.Method, path, status).Inc()
			metrics.DaemonRequestDurationSeconds.WithLabelValues(r.Method, path, status).Observe(duration)
		}
		if audit != nil {
			audit.DaemonAPIAccess(r.Method, path, rec.status)
		}
	})
}

// sanitizePath replaces dynamic path segments with fixed labels to prevent
// high cardinality in Prometheus metrics. For example:
//
//	/v1/auth/12D3KooW... -> /v1/auth/:id
//	/v1/connect/proxy-1  -> /v1/connect/:id
//	/v1/expose/ssh       -> /v1/expose/:id
func sanitizePath(path string) string {
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	// Parameterized routes have 4 parts: ["", "v1", resource, param]
	if len(parts) == 4 && parts[1] == "v1" {
		switch parts[2] {
		case "auth", "connect", "expose":
			return "/v1/" + parts[2] + "/:id"
		}
	}
	return path
}
