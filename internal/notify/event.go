// Package notify provides a notification subsystem for grant lifecycle events.
// The Router dispatches events to pluggable NotificationSink implementations
// (log, desktop, webhook). Sinks are non-blocking and failure-isolated.
package notify

import (
	"time"

	"github.com/google/uuid"
)

// EventType identifies the kind of notification event.
type EventType string

const (
	EventGrantCreated   EventType = "grant_created"
	EventGrantExpiring  EventType = "grant_expiring"
	EventGrantExpired   EventType = "grant_expired"
	EventGrantRevoked   EventType = "grant_revoked"
	EventGrantExtended  EventType = "grant_extended"
	EventGrantRefreshed   EventType = "grant_refreshed"
	EventGrantRateLimited EventType = "grant_rate_limited"
	EventTest             EventType = "test"
)

// Severity indicates the urgency of an event.
type Severity string

const (
	SeverityInfo Severity = "info"
	SeverityWarn Severity = "warn"
)

// Event is a notification event dispatched to all configured sinks.
type Event struct {
	ID        string            `json:"id"`
	Type      EventType         `json:"type"`
	Severity  Severity          `json:"severity"`
	PeerID    string            `json:"peer_id"`
	PeerName  string            `json:"peer_name,omitempty"`
	Message   string            `json:"message"`
	Metadata  map[string]string `json:"metadata,omitempty"`
	Timestamp time.Time         `json:"timestamp"`
}

// NewEvent creates an Event with a unique ID and current timestamp.
func NewEvent(typ EventType, severity Severity, peerID, peerName, message string) Event {
	return Event{
		ID:        uuid.NewString(),
		Type:      typ,
		Severity:  severity,
		PeerID:    peerID,
		PeerName:  peerName,
		Message:   message,
		Timestamp: time.Now(),
	}
}

// WithMetadata returns a copy of the event with the given key-value pair added.
// The original event's metadata is not mutated.
func (e Event) WithMetadata(key, value string) Event {
	cp := make(map[string]string, len(e.Metadata)+1)
	for k, v := range e.Metadata {
		cp[k] = v
	}
	cp[key] = value
	e.Metadata = cp
	return e
}
