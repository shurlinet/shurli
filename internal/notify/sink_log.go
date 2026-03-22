package notify

import "log/slog"

// LogSink writes every event to structured logging via slog.
// This sink is always active and cannot be disabled - it forms
// the baseline audit trail for all grant lifecycle events.
type LogSink struct {
	logger *slog.Logger
}

// NewLogSink creates a LogSink using the given logger.
func NewLogSink(logger *slog.Logger) *LogSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &LogSink{logger: logger}
}

func (s *LogSink) Name() string { return "log" }

func (s *LogSink) Notify(event Event) error {
	attrs := []any{
		"event_id", event.ID,
		"event_type", string(event.Type),
		"peer_id", event.PeerID,
	}
	if event.PeerName != "" {
		attrs = append(attrs, "peer_name", event.PeerName)
	}
	for k, v := range event.Metadata {
		attrs = append(attrs, k, v)
	}

	switch event.Severity {
	case SeverityWarn:
		s.logger.Warn("notify: "+event.Message, attrs...)
	default:
		s.logger.Info("notify: "+event.Message, attrs...)
	}
	return nil
}
