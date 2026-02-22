package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satindergrewal/peer-up/internal/config"
)

func TestDoRelaySetup_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer

	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader(""), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Config file created
	assertFileExistsWithPerm(t, filepath.Join(dir, "relay-server.yaml"), 0600)

	// Authorized keys created
	assertFileExistsWithPerm(t, filepath.Join(dir, "relay_authorized_keys"), 0600)

	// Key file NOT created (generated on first serve)
	if _, err := os.Stat(filepath.Join(dir, "relay_node.key")); !os.IsNotExist(err) {
		t.Error("relay_node.key should not exist yet")
	}

	// Config should contain expected content
	data, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if !strings.Contains(string(data), "version: 1") {
		t.Error("config missing version field")
	}
	if !strings.Contains(string(data), "relay_authorized_keys") {
		t.Error("config missing authorized_keys reference")
	}

	output := stdout.String()
	if !strings.Contains(output, "Created relay-server.yaml") {
		t.Error("output should mention config creation")
	}
}

func TestDoRelaySetup_FreshFlag(t *testing.T) {
	dir := t.TempDir()

	// Pre-create files with known content
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("old-config"), 0600)
	os.WriteFile(filepath.Join(dir, "relay_authorized_keys"), []byte("old-auth"), 0600)

	var stdout bytes.Buffer
	err := doRelaySetup([]string{"--dir", dir, "--fresh"}, strings.NewReader(""), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Backup should exist
	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))
	snaps, _ := sm.List()
	if len(snaps) == 0 {
		t.Fatal("expected backup snapshot")
	}

	// Backup should contain old content
	backupData, _ := os.ReadFile(filepath.Join(snaps[0].Path, "relay-server.yaml"))
	if string(backupData) != "old-config" {
		t.Errorf("backup content: got %q, want %q", backupData, "old-config")
	}

	// New config should be fresh template
	newData, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if !strings.Contains(string(newData), "peerup relay setup") {
		t.Error("new config should be from template")
	}
}

func TestDoRelaySetup_NonInteractiveWithExisting(t *testing.T) {
	dir := t.TempDir()

	// Pre-create a config
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("exists"), 0600)

	var stdout bytes.Buffer
	err := doRelaySetup([]string{"--dir", dir, "--non-interactive"}, strings.NewReader(""), &stdout)
	if err == nil {
		t.Fatal("expected error for non-interactive with existing files")
	}
	if !strings.Contains(err.Error(), "existing files found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestDoRelaySetup_NonInteractiveFreshOverride(t *testing.T) {
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("old"), 0600)

	var stdout bytes.Buffer
	// --fresh overrides --non-interactive
	err := doRelaySetup([]string{"--dir", dir, "--fresh", "--non-interactive"}, strings.NewReader(""), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Should have created a backup and fresh config
	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))
	snaps, _ := sm.List()
	if len(snaps) == 0 {
		t.Fatal("expected backup snapshot")
	}
}

func TestDoRelaySetup_InteractiveKeepAll(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("keep-me"), 0600)

	var stdout bytes.Buffer
	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader("1\n"), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Content unchanged
	data, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if string(data) != "keep-me" {
		t.Errorf("config was modified: %q", data)
	}

	// No backup created
	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))
	snaps, _ := sm.List()
	if len(snaps) != 0 {
		t.Error("no backup should be created for keep-all")
	}
}

func TestDoRelaySetup_InteractiveFresh(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("old"), 0600)
	os.WriteFile(filepath.Join(dir, "relay_authorized_keys"), []byte("old-auth"), 0600)

	var stdout bytes.Buffer
	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader("2\n"), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Backup exists
	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))
	snaps, _ := sm.List()
	if len(snaps) == 0 {
		t.Fatal("expected backup")
	}

	// Config is fresh
	data, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if !strings.Contains(string(data), "peerup relay setup") {
		t.Error("config should be fresh template")
	}
}

func TestDoRelaySetup_InteractiveRestore(t *testing.T) {
	dir := t.TempDir()
	backupDir := filepath.Join(dir, "backups")

	// Create a snapshot manually with known content
	sm := config.NewSnapshotManager(backupDir)
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("snapshot-config"), 0600)
	_, err := sm.Create(dir, relaySetupFiles)
	if err != nil {
		t.Fatalf("create snapshot: %v", err)
	}

	// Modify current files
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("current-config"), 0600)

	var stdout bytes.Buffer
	// Choose option 3 (restore), then pick snapshot 1
	err = doRelaySetup([]string{"--dir", dir}, strings.NewReader("3\n1\n"), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Config should be restored to snapshot content
	data, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if string(data) != "snapshot-config" {
		t.Errorf("expected restored content %q, got %q", "snapshot-config", data)
	}

	// Safety backup should exist (2 snapshots total now)
	snaps, _ := sm.List()
	if len(snaps) < 2 {
		t.Errorf("expected at least 2 snapshots (original + safety), got %d", len(snaps))
	}
}

func TestDoRelaySetup_PerFileReplaceConfig(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("old-config"), 0600)
	os.WriteFile(filepath.Join(dir, "relay_authorized_keys"), []byte("old-auth"), 0600)

	var stdout bytes.Buffer
	// Option 4, replace config (n), keep auth (Y)
	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader("4\nn\nY\n"), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Config should be fresh
	data, _ := os.ReadFile(filepath.Join(dir, "relay-server.yaml"))
	if !strings.Contains(string(data), "peerup relay setup") {
		t.Error("config should be fresh template")
	}

	// Auth should be unchanged
	authData, _ := os.ReadFile(filepath.Join(dir, "relay_authorized_keys"))
	if string(authData) != "old-auth" {
		t.Errorf("auth should be unchanged: %q", authData)
	}

	// Backup should exist (created on first replacement)
	sm := config.NewSnapshotManager(filepath.Join(dir, "backups"))
	snaps, _ := sm.List()
	if len(snaps) == 0 {
		t.Fatal("expected backup from per-file replacement")
	}
}

func TestDoRelaySetup_EnsuresMissingFiles(t *testing.T) {
	dir := t.TempDir()
	// Only create config, not auth
	os.WriteFile(filepath.Join(dir, "relay-server.yaml"), []byte("exists"), 0600)

	var stdout bytes.Buffer
	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader("1\n"), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	// Auth should be created even though we chose "keep all"
	assertFileExistsWithPerm(t, filepath.Join(dir, "relay_authorized_keys"), 0600)
}

func TestDoRelaySetup_Permissions(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer

	err := doRelaySetup([]string{"--dir", dir}, strings.NewReader(""), &stdout)
	if err != nil {
		t.Fatalf("doRelaySetup: %v", err)
	}

	assertFileExistsWithPerm(t, filepath.Join(dir, "relay-server.yaml"), 0600)
	assertFileExistsWithPerm(t, filepath.Join(dir, "relay_authorized_keys"), 0600)
}

// --- helpers ---

func assertFileExistsWithPerm(t *testing.T, path string, perm os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("file not found: %s: %v", filepath.Base(path), err)
	}
	if info.Mode().Perm() != perm {
		t.Errorf("%s permissions: got %o, want %o", filepath.Base(path), info.Mode().Perm(), perm)
	}
}
