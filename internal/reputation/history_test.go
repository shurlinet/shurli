package reputation

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestPeerHistory_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "peer_history.json")

	h := NewPeerHistory(path)
	h.RecordConnection("peer-A", "direct", 10.0)
	h.RecordConnection("peer-A", "relay", 50.0)
	h.RecordIntroduction("peer-A", "relay-001", "relay-pairing")
	h.RecordConnection("peer-B", "direct", 5.0)

	if err := h.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	// Reload into a new instance.
	h2 := NewPeerHistory(path)
	if h2.Count() != 2 {
		t.Fatalf("Count = %d, want 2", h2.Count())
	}

	r := h2.Get("peer-A")
	if r == nil {
		t.Fatal("peer-A not found")
	}
	if r.ConnectionCount != 2 {
		t.Errorf("connection_count = %d, want 2", r.ConnectionCount)
	}
	if r.IntroducedBy != "relay-001" {
		t.Errorf("introduced_by = %q, want %q", r.IntroducedBy, "relay-001")
	}
	if r.IntroMethod != "relay-pairing" {
		t.Errorf("intro_method = %q, want %q", r.IntroMethod, "relay-pairing")
	}
	if r.PathTypes["direct"] != 1 {
		t.Errorf("path_types[direct] = %d, want 1", r.PathTypes["direct"])
	}
	if r.PathTypes["relay"] != 1 {
		t.Errorf("path_types[relay] = %d, want 1", r.PathTypes["relay"])
	}
}

func TestPeerHistory_RunningAverage(t *testing.T) {
	dir := t.TempDir()
	h := NewPeerHistory(filepath.Join(dir, "history.json"))

	// 10, 20, 30 -> avg = 20
	h.RecordConnection("peer-X", "direct", 10.0)
	h.RecordConnection("peer-X", "direct", 20.0)
	h.RecordConnection("peer-X", "direct", 30.0)

	r := h.Get("peer-X")
	if r == nil {
		t.Fatal("peer-X not found")
	}
	// Running average: (10 + 20 + 30) / 3 = 20
	if r.AvgLatencyMs < 19.9 || r.AvgLatencyMs > 20.1 {
		t.Errorf("avg_latency_ms = %f, want ~20.0", r.AvgLatencyMs)
	}
}

func TestPeerHistory_ConcurrentAccess(t *testing.T) {
	dir := t.TempDir()
	h := NewPeerHistory(filepath.Join(dir, "history.json"))

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h.RecordConnection("peer-concurrent", "direct", 5.0)
		}()
	}
	wg.Wait()

	r := h.Get("peer-concurrent")
	if r == nil {
		t.Fatal("peer-concurrent not found")
	}
	if r.ConnectionCount != 100 {
		t.Errorf("connection_count = %d, want 100", r.ConnectionCount)
	}
}

func TestPeerHistory_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	h := NewPeerHistory(path)
	if h.Count() != 0 {
		t.Errorf("Count = %d, want 0", h.Count())
	}

	// Get on empty history returns nil.
	if r := h.Get("nobody"); r != nil {
		t.Error("expected nil for unknown peer")
	}
}

func TestPeerHistory_GetReturnsCopy(t *testing.T) {
	dir := t.TempDir()
	h := NewPeerHistory(filepath.Join(dir, "history.json"))

	h.RecordConnection("peer-copy", "direct", 10.0)

	r := h.Get("peer-copy")
	r.ConnectionCount = 999
	r.PathTypes["hacked"] = 1

	// Original should be unaffected.
	r2 := h.Get("peer-copy")
	if r2.ConnectionCount != 1 {
		t.Errorf("mutation leaked: connection_count = %d, want 1", r2.ConnectionCount)
	}
	if _, ok := r2.PathTypes["hacked"]; ok {
		t.Error("mutation leaked: path_types contains 'hacked'")
	}
}

func TestPeerHistory_SaveCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subdir", "history.json")

	// Create parent dir.
	os.MkdirAll(filepath.Dir(path), 0700)

	h := NewPeerHistory(path)
	h.RecordConnection("peer-save", "direct", 1.0)

	if err := h.Save(); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat error: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions = %v, want 0600", info.Mode().Perm())
	}
}
