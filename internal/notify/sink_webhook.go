package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"
)

// WebhookSink sends notification events as JSON HTTP POST requests.
// Supports configurable headers (for auth tokens), event type filtering,
// and exponential backoff retry (3 attempts: 1s, 2s, 4s).
type WebhookSink struct {
	url            string
	headers        map[string]string
	events         map[EventType]bool // nil = all events pass
	client         *http.Client
	logger         *slog.Logger
	initialBackoff time.Duration // for testing; production: 1s
}

// WebhookConfig holds configuration for creating a WebhookSink.
type WebhookConfig struct {
	URL     string
	Headers map[string]string
	Events  []string // event type filter; empty = all events
}

// NewWebhookSink creates a WebhookSink. Returns nil if URL is empty.
// Returns nil and logs a warning if the URL scheme is not http or https.
func NewWebhookSink(cfg WebhookConfig, logger *slog.Logger) *WebhookSink {
	if cfg.URL == "" {
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	parsed, err := url.Parse(cfg.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		logger.Warn("notify: webhook URL has invalid scheme, must be http or https",
			"url", cfg.URL)
		return nil
	}

	var filter map[EventType]bool
	if len(cfg.Events) > 0 {
		filter = make(map[EventType]bool, len(cfg.Events))
		for _, e := range cfg.Events {
			filter[EventType(e)] = true
		}
	}

	// Defensive copy of headers to prevent mutation after construction.
	hdrs := make(map[string]string, len(cfg.Headers))
	for k, v := range cfg.Headers {
		hdrs[k] = v
	}

	return &WebhookSink{
		url:     cfg.URL,
		headers: hdrs,
		events:  filter,
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
		logger:         logger,
		initialBackoff: 1 * time.Second,
	}
}

func (s *WebhookSink) Name() string { return "webhook" }

func (s *WebhookSink) Notify(event Event) error {
	// Event filter: skip events not in the allowed set.
	if s.events != nil && !s.events[event.Type] {
		return nil
	}

	body, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("webhook: marshal event: %w", err)
	}

	// Retry with exponential backoff: 1s, 2s, 4s.
	var lastErr error
	backoff := s.initialBackoff
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			time.Sleep(backoff)
			backoff *= 2
		}

		if err := s.doPost(body); err != nil {
			lastErr = err
			s.logger.Warn("webhook: attempt failed",
				"attempt", attempt+1,
				"url", s.url,
				"error", err)
			continue
		}
		return nil
	}
	return fmt.Errorf("webhook: all 3 attempts failed: %w", lastErr)
}

func (s *WebhookSink) doPost(body []byte) error {
	req, err := http.NewRequest("POST", s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range s.headers {
		req.Header.Set(k, v)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// Limit response read to 1 MB to prevent memory exhaustion from malicious endpoints.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
