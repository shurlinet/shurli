package p2pnet

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestAuditLoggerNilSafe(t *testing.T) {
	var a *AuditLogger

	// All methods must not panic when called on nil
	a.AuthDecision("12D3KooWTest...", "inbound", "denied")
	a.ServiceACLDenied("12D3KooWTest...", "ssh")
	a.DaemonAPIAccess("GET", "/v1/status", 200)
	a.AuthChange("add", "12D3KooWTest...")
}

func TestAuditLoggerAuthDecision(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	a := NewAuditLogger(handler)

	a.AuthDecision("12D3KooWTest...", "inbound", "allowed")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON log: %v", err)
	}

	if entry["msg"] != "auth_decision" {
		t.Errorf("msg = %q, want %q", entry["msg"], "auth_decision")
	}

	audit, ok := entry["audit"].(map[string]any)
	if !ok {
		t.Fatal("missing audit group in log entry")
	}

	if audit["peer"] != "12D3KooWTest..." {
		t.Errorf("peer = %q, want %q", audit["peer"], "12D3KooWTest...")
	}
	if audit["direction"] != "inbound" {
		t.Errorf("direction = %q, want %q", audit["direction"], "inbound")
	}
	if audit["result"] != "allowed" {
		t.Errorf("result = %q, want %q", audit["result"], "allowed")
	}
}

func TestAuditLoggerServiceACLDenied(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	a := NewAuditLogger(handler)

	a.ServiceACLDenied("12D3KooWTest...", "ssh")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON log: %v", err)
	}

	if entry["msg"] != "service_acl_denied" {
		t.Errorf("msg = %q, want %q", entry["msg"], "service_acl_denied")
	}

	audit, ok := entry["audit"].(map[string]any)
	if !ok {
		t.Fatal("missing audit group in log entry")
	}

	if audit["service"] != "ssh" {
		t.Errorf("service = %q, want %q", audit["service"], "ssh")
	}
}

func TestAuditLoggerDaemonAPIAccess(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	a := NewAuditLogger(handler)

	a.DaemonAPIAccess("POST", "/v1/ping", 200)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON log: %v", err)
	}

	audit, ok := entry["audit"].(map[string]any)
	if !ok {
		t.Fatal("missing audit group in log entry")
	}

	if audit["method"] != "POST" {
		t.Errorf("method = %q, want %q", audit["method"], "POST")
	}
	if audit["path"] != "/v1/ping" {
		t.Errorf("path = %q, want %q", audit["path"], "/v1/ping")
	}
	// JSON numbers decode as float64
	if audit["status"] != float64(200) {
		t.Errorf("status = %v, want %v", audit["status"], 200)
	}
}

func TestAuditLoggerAuthChange(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	a := NewAuditLogger(handler)

	a.AuthChange("remove", "12D3KooWTest...")

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("failed to parse JSON log: %v", err)
	}

	audit, ok := entry["audit"].(map[string]any)
	if !ok {
		t.Fatal("missing audit group in log entry")
	}

	if audit["action"] != "remove" {
		t.Errorf("action = %q, want %q", audit["action"], "remove")
	}
	if audit["peer"] != "12D3KooWTest..." {
		t.Errorf("peer = %q, want %q", audit["peer"], "12D3KooWTest...")
	}
}
