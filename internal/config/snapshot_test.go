package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSnapshotCreate(t *testing.T) {
	sourceDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	// Create source files
	writeTestFile(t, filepath.Join(sourceDir, "config.yaml"), "version: 1\n")
	writeTestFile(t, filepath.Join(sourceDir, "node.key"), "secret-key-data")
	writeTestFile(t, filepath.Join(sourceDir, "authorized_keys"), "peer1\npeer2\n")

	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(sourceDir, []string{"config.yaml", "node.key", "authorized_keys"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(snap.Files) != 3 {
		t.Errorf("expected 3 files, got %d: %v", len(snap.Files), snap.Files)
	}

	// Verify snapshot directory exists
	if _, err := os.Stat(snap.Path); err != nil {
		t.Errorf("snapshot dir not found: %v", err)
	}

	// Verify file contents match originals
	data, err := os.ReadFile(filepath.Join(snap.Path, "config.yaml"))
	if err != nil {
		t.Fatalf("read snapshot config: %v", err)
	}
	if string(data) != "version: 1\n" {
		t.Errorf("config content mismatch: %q", data)
	}

	// Verify permissions
	info, err := os.Stat(filepath.Join(snap.Path, "node.key"))
	if err != nil {
		t.Fatalf("stat snapshot key: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("key permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestSnapshotCreatePartial(t *testing.T) {
	sourceDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	// Only create one of the three files
	writeTestFile(t, filepath.Join(sourceDir, "config.yaml"), "version: 1\n")

	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(sourceDir, []string{"config.yaml", "node.key", "authorized_keys"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if len(snap.Files) != 1 {
		t.Errorf("expected 1 file, got %d: %v", len(snap.Files), snap.Files)
	}
	if snap.Files[0] != "config.yaml" {
		t.Errorf("expected config.yaml, got %s", snap.Files[0])
	}
}

func TestSnapshotCreateNoTempLeftBehind(t *testing.T) {
	sourceDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	writeTestFile(t, filepath.Join(sourceDir, "config.yaml"), "data")

	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(sourceDir, []string{"config.yaml"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	entries, err := os.ReadDir(snap.Path)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", entry.Name())
		}
	}
}

func TestSnapshotList(t *testing.T) {
	backupDir := filepath.Join(t.TempDir(), "backups")

	// Create snapshots with different timestamps
	ts1 := "2026-01-01_100000"
	ts2 := "2026-01-02_100000"
	ts3 := "2026-01-03_100000"

	for _, ts := range []string{ts1, ts2, ts3} {
		dir := filepath.Join(backupDir, ts)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatalf("mkdir %s: %v", ts, err)
		}
		writeTestFile(t, filepath.Join(dir, "config.yaml"), "data-"+ts)
	}

	// Add a non-directory entry (should be skipped)
	writeTestFile(t, filepath.Join(backupDir, "README"), "ignore me")

	// Add a directory with bad name (should be skipped)
	if err := os.MkdirAll(filepath.Join(backupDir, "not-a-timestamp"), 0700); err != nil {
		t.Fatal(err)
	}

	sm := NewSnapshotManager(backupDir)
	snaps, err := sm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(snaps) != 3 {
		t.Fatalf("expected 3 snapshots, got %d", len(snaps))
	}

	// Verify newest-first ordering
	if snaps[0].Name != ts3 {
		t.Errorf("expected newest first (%s), got %s", ts3, snaps[0].Name)
	}
	if snaps[2].Name != ts1 {
		t.Errorf("expected oldest last (%s), got %s", ts1, snaps[2].Name)
	}
}

func TestSnapshotListEmpty(t *testing.T) {
	sm := NewSnapshotManager(filepath.Join(t.TempDir(), "nonexistent"))
	snaps, err := sm.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(snaps) != 0 {
		t.Errorf("expected empty list, got %d", len(snaps))
	}
}

func TestSnapshotRestore(t *testing.T) {
	sourceDir := t.TempDir()
	targetDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	// Create original files
	writeTestFile(t, filepath.Join(sourceDir, "config.yaml"), "original-config")
	writeTestFile(t, filepath.Join(sourceDir, "node.key"), "original-key")

	// Create snapshot
	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(sourceDir, []string{"config.yaml", "node.key"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Write different content to target
	writeTestFile(t, filepath.Join(targetDir, "config.yaml"), "modified-config")
	writeTestFile(t, filepath.Join(targetDir, "node.key"), "modified-key")

	// Restore
	if err := sm.Restore(snap, targetDir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify content matches original
	assertFileContent(t, filepath.Join(targetDir, "config.yaml"), "original-config")
	assertFileContent(t, filepath.Join(targetDir, "node.key"), "original-key")

	// Verify permissions
	info, err := os.Stat(filepath.Join(targetDir, "node.key"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions: got %o, want 0600", info.Mode().Perm())
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	files := []string{"config.yaml", "node.key", "authorized_keys"}

	// Create original files
	writeTestFile(t, filepath.Join(dir, "config.yaml"), "original-config")
	writeTestFile(t, filepath.Join(dir, "node.key"), "original-key")
	writeTestFile(t, filepath.Join(dir, "authorized_keys"), "peer1\n")

	// Snapshot
	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(dir, files)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Modify originals
	writeTestFile(t, filepath.Join(dir, "config.yaml"), "modified-config")
	writeTestFile(t, filepath.Join(dir, "node.key"), "modified-key")
	writeTestFile(t, filepath.Join(dir, "authorized_keys"), "peer1\npeer2\n")

	// Restore
	if err := sm.Restore(snap, dir); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Verify originals are back
	assertFileContent(t, filepath.Join(dir, "config.yaml"), "original-config")
	assertFileContent(t, filepath.Join(dir, "node.key"), "original-key")
	assertFileContent(t, filepath.Join(dir, "authorized_keys"), "peer1\n")
}

func TestSnapshotTimestamp(t *testing.T) {
	sourceDir := t.TempDir()
	backupDir := filepath.Join(t.TempDir(), "backups")

	writeTestFile(t, filepath.Join(sourceDir, "config.yaml"), "data")

	sm := NewSnapshotManager(backupDir)
	snap, err := sm.Create(sourceDir, []string{"config.yaml"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Timestamp should be very recent (within last 5 seconds)
	if time.Since(snap.Timestamp) > 5*time.Second {
		t.Errorf("timestamp too old: %v", snap.Timestamp)
	}

	// Name should parse back to the timestamp
	parsed, err := time.Parse(snapshotTimeFormat, snap.Name)
	if err != nil {
		t.Errorf("name doesn't parse: %v", err)
	}
	if !parsed.Equal(snap.Timestamp.Truncate(time.Second)) {
		t.Errorf("timestamp mismatch: name=%v, ts=%v", parsed, snap.Timestamp)
	}
}

// --- helpers ---

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func assertFileContent(t *testing.T, path, expected string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if string(data) != expected {
		t.Errorf("%s: got %q, want %q", filepath.Base(path), string(data), expected)
	}
}
