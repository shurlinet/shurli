package daemon

import (
	"net/http"
	"strings"

	"github.com/shurlinet/shurli/internal/notify"
)

// handleNotifySinks returns all configured notification sinks.
// GET /v1/notify/sinks
func (s *Server) handleNotifySinks(w http.ResponseWriter, r *http.Request) {
	router := s.runtime.NotifyRouter()
	if router == nil {
		RespondError(w, http.StatusServiceUnavailable, "notification system not available")
		return
	}

	names := router.Sinks()
	result := make([]NotifySinkInfo, len(names))
	for i, name := range names {
		result[i] = NotifySinkInfo{Name: name, Status: "active"}
	}

	RespondJSON(w, http.StatusOK, result)
}

// handleNotifyTest sends a test notification to all configured sinks.
// POST /v1/notify/test
func (s *Server) handleNotifyTest(w http.ResponseWriter, r *http.Request) {
	router := s.runtime.NotifyRouter()
	if router == nil {
		RespondError(w, http.StatusServiceUnavailable, "notification system not available")
		return
	}

	event := notify.NewEvent(notify.EventTest, notify.SeverityInfo, "", "", "test notification from shurli")
	router.Emit(event)

	sinkNames := router.Sinks()
	sinksStr := "(none)"
	if len(sinkNames) > 0 {
		sinksStr = strings.Join(sinkNames, ", ")
	}

	RespondJSON(w, http.StatusOK, map[string]string{
		"status": "sent",
		"sinks":  sinksStr,
	})
}
