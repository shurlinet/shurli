package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestArchivePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/home/user/.config/peerup/config.yaml", "/home/user/.config/peerup/.config.last-good.yaml"},
		{"/etc/peerup/config.yaml", "/etc/peerup/.config.last-good.yaml"},
		{"relay-server.yaml", ".relay-server.last-good.yaml"},
		{"/path/to/peerup.yaml", "/path/to/.peerup.last-good.yaml"},
	}
	for _, tt := range tests {
		got := ArchivePath(tt.input)
		if got != tt.want {
			t.Errorf("ArchivePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestArchiveAndRollback(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	original := []byte("version: 1\nidentity:\n  key_file: identity.key\n")

	if err := os.WriteFile(cfgPath, original, 0600); err != nil {
		t.Fatal(err)
	}

	// Archive the config
	if err := Archive(cfgPath); err != nil {
		t.Fatalf("Archive() error: %v", err)
	}

	// Verify archive exists
	if !HasArchive(cfgPath) {
		t.Fatal("HasArchive() = false after Archive()")
	}

	archivePath := ArchivePath(cfgPath)
	archived, err := os.ReadFile(archivePath)
	if err != nil {
		t.Fatalf("read archive: %v", err)
	}
	if string(archived) != string(original) {
		t.Errorf("archive content = %q, want %q", archived, original)
	}

	// Verify archive has restricted permissions
	info, err := os.Stat(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0600 {
		t.Errorf("archive permissions = %o, want 0600", perm)
	}

	// Modify the config
	modified := []byte("version: 1\nidentity:\n  key_file: broken.key\n")
	if err := os.WriteFile(cfgPath, modified, 0600); err != nil {
		t.Fatal(err)
	}

	// Rollback
	if err := Rollback(cfgPath); err != nil {
		t.Fatalf("Rollback() error: %v", err)
	}

	restored, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(restored) != string(original) {
		t.Errorf("rollback content = %q, want %q", restored, original)
	}
}

func TestRollbackNoArchive(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	err := Rollback(cfgPath)
	if err == nil {
		t.Fatal("Rollback() expected error, got nil")
	}
	if !errors.Is(err, ErrNoArchive) {
		t.Errorf("Rollback() error = %v, want ErrNoArchive", err)
	}
}

func TestHasArchiveNoFile(t *testing.T) {
	if HasArchive("/nonexistent/config.yaml") {
		t.Error("HasArchive() = true for nonexistent path")
	}
}

func TestArchiveNonexistentConfig(t *testing.T) {
	err := Archive("/nonexistent/config.yaml")
	if err == nil {
		t.Fatal("Archive() expected error for nonexistent config")
	}
}

func TestArchiveOverwrite(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	// First version
	v1 := []byte("version: 1\n")
	if err := os.WriteFile(cfgPath, v1, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Archive(cfgPath); err != nil {
		t.Fatal(err)
	}

	// Second version overwrites
	v2 := []byte("version: 1\nupdated: true\n")
	if err := os.WriteFile(cfgPath, v2, 0600); err != nil {
		t.Fatal(err)
	}
	if err := Archive(cfgPath); err != nil {
		t.Fatal(err)
	}

	// Archive should contain v2
	archived, err := os.ReadFile(ArchivePath(cfgPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(archived) != string(v2) {
		t.Errorf("archive = %q, want %q", archived, v2)
	}
}

func TestArchiveNoTempLeftBehind(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte("test\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := Archive(cfgPath); err != nil {
		t.Fatal(err)
	}

	// No .tmp files should remain
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}
