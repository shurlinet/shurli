package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestWebhookSink_Name(t *testing.T) {
	s := NewWebhookSink(WebhookConfig{URL: "http://example.com"}, nil)
	if s.Name() != "webhook" {
		t.Errorf("Name() = %q, want %q", s.Name(), "webhook")
	}
}

func TestWebhookSink_NilOnEmptyURL(t *testing.T) {
	s := NewWebhookSink(WebhookConfig{}, nil)
	if s != nil {
		t.Error("expected nil for empty URL")
	}
}

func TestWebhookSink_NilOnInvalidScheme(t *testing.T) {
	for _, u := range []string{"ftp://example.com", "file:///etc/passwd", "://bad", "not-a-url"} {
		s := NewWebhookSink(WebhookConfig{URL: u}, nil)
		if s != nil {
			t.Errorf("expected nil for invalid URL %q, got non-nil", u)
		}
	}
}

func TestWebhookSink_PostsJSON(t *testing.T) {
	var mu sync.Mutex
	var received Event
	var gotHeaders http.Header

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotHeaders = r.Header.Clone()
		body, _ := io.ReadAll(r.Body)
		json.Unmarshal(body, &received)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := NewWebhookSink(WebhookConfig{
		URL: ts.URL,
		Headers: map[string]string{
			"Authorization": "Bearer test-token",
			"X-Custom":      "custom-value",
		},
	}, slog.Default())

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmTestPeer", "alice", "grant created")
	event = event.WithMetadata("expires_at", "2026-04-01T00:00:00Z")

	if err := s.Notify(event); err != nil {
		t.Fatalf("Notify() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	if received.Type != EventGrantCreated {
		t.Errorf("received type = %s, want %s", received.Type, EventGrantCreated)
	}
	if received.PeerID != "QmTestPeer" {
		t.Errorf("received peer_id = %q, want %q", received.PeerID, "QmTestPeer")
	}
	if received.PeerName != "alice" {
		t.Errorf("received peer_name = %q, want %q", received.PeerName, "alice")
	}
	if received.Metadata["expires_at"] != "2026-04-01T00:00:00Z" {
		t.Errorf("received metadata expires_at = %q", received.Metadata["expires_at"])
	}
	if gotHeaders.Get("Content-Type") != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotHeaders.Get("Content-Type"))
	}
	if gotHeaders.Get("Authorization") != "Bearer test-token" {
		t.Errorf("Authorization header = %q", gotHeaders.Get("Authorization"))
	}
	if gotHeaders.Get("X-Custom") != "custom-value" {
		t.Errorf("X-Custom header = %q", gotHeaders.Get("X-Custom"))
	}
}

func TestWebhookSink_EventFilter(t *testing.T) {
	var count atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := NewWebhookSink(WebhookConfig{
		URL:    ts.URL,
		Events: []string{"grant_expiring", "grant_expired"},
	}, slog.Default())

	// This event should be filtered out.
	e1 := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "created")
	if err := s.Notify(e1); err != nil {
		t.Fatalf("Notify() error: %v", err)
	}

	// This event should pass the filter.
	e2 := NewEvent(EventGrantExpiring, SeverityWarn, "QmPeer", "", "expiring")
	if err := s.Notify(e2); err != nil {
		t.Fatalf("Notify() error: %v", err)
	}

	if count.Load() != 1 {
		t.Errorf("webhook received %d requests, want 1 (filter should block grant_created)", count.Load())
	}
}

func TestWebhookSink_RetryOnFailure(t *testing.T) {
	var attempts atomic.Int64

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	s := NewWebhookSink(WebhookConfig{URL: ts.URL}, slog.Default())
	s.initialBackoff = time.Millisecond // fast retry for tests

	event := NewEvent(EventGrantRevoked, SeverityWarn, "QmPeer", "", "revoked")
	if err := s.Notify(event); err != nil {
		t.Fatalf("Notify() error after retries: %v", err)
	}

	if attempts.Load() != 3 {
		t.Errorf("attempts = %d, want 3", attempts.Load())
	}
}

func TestWebhookSink_AllRetriesFail(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	s := NewWebhookSink(WebhookConfig{URL: ts.URL}, slog.Default())
	s.initialBackoff = time.Millisecond

	event := NewEvent(EventGrantRevoked, SeverityWarn, "QmPeer", "", "revoked")
	err := s.Notify(event)
	if err == nil {
		t.Error("expected error when all retries fail, got nil")
	}
}
