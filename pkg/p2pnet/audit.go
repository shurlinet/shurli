package p2pnet

import (
	"log/slog"
)

// AuditLogger writes structured audit events for security-relevant actions.
// All methods are nil-safe: calling any method on a nil *AuditLogger is a no-op.
// This allows callers to skip nil checks at every call site.
type AuditLogger struct {
	logger *slog.Logger
}

// NewAuditLogger creates an AuditLogger that writes to the given handler.
// All audit events are written under the "audit" group for easy filtering.
func NewAuditLogger(handler slog.Handler) *AuditLogger {
	return &AuditLogger{
		logger: slog.New(handler).WithGroup("audit"),
	}
}

// AuthDecision logs an authentication allow/deny decision.
func (a *AuditLogger) AuthDecision(peerID, direction, result string) {
	if a == nil {
		return
	}
	a.logger.Info("auth_decision",
		"peer", peerID,
		"direction", direction,
		"result", result,
	)
}

// ServiceACLDenied logs a per-service access control denial.
func (a *AuditLogger) ServiceACLDenied(peerID, service string) {
	if a == nil {
		return
	}
	a.logger.Warn("service_acl_denied",
		"peer", peerID,
		"service", service,
	)
}

// DaemonAPIAccess logs an API request to the daemon.
func (a *AuditLogger) DaemonAPIAccess(method, path string, status int) {
	if a == nil {
		return
	}
	a.logger.Info("daemon_api_access",
		"method", method,
		"path", path,
		"status", status,
	)
}

// AuthChange logs a peer authorization change (add or remove).
func (a *AuditLogger) AuthChange(action, peerID string) {
	if a == nil {
		return
	}
	a.logger.Info("auth_change",
		"action", action,
		"peer", peerID,
	)
}
