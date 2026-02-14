package p2pnet

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestLoadOrCreateIdentity_Creates(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	priv, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity() error = %v", err)
	}
	if priv == nil {
		t.Fatal("LoadOrCreateIdentity() returned nil key")
	}

	// Verify file was created
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}

	// Verify permissions (skip on Windows)
	if runtime.GOOS != "windows" {
		mode := info.Mode().Perm()
		if mode != 0600 {
			t.Errorf("key file permissions = %04o, want 0600", mode)
		}
	}
}

func TestLoadOrCreateIdentity_Loads(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Create key
	priv1, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("first LoadOrCreateIdentity() error = %v", err)
	}
	pid1, err := peer.IDFromPrivateKey(priv1)
	if err != nil {
		t.Fatalf("IDFromPrivateKey() error = %v", err)
	}

	// Load same key
	priv2, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("second LoadOrCreateIdentity() error = %v", err)
	}
	pid2, err := peer.IDFromPrivateKey(priv2)
	if err != nil {
		t.Fatalf("IDFromPrivateKey() error = %v", err)
	}

	// Verify same peer ID
	if pid1 != pid2 {
		t.Errorf("peer IDs differ: %s != %s", pid1, pid2)
	}
}

func TestLoadOrCreateIdentity_BadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions not applicable on Windows")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Create key (will be 0600)
	_, err := LoadOrCreateIdentity(keyPath)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity() error = %v", err)
	}

	// Weaken permissions
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	// Loading should fail
	_, err = LoadOrCreateIdentity(keyPath)
	if err == nil {
		t.Fatal("LoadOrCreateIdentity() should fail with insecure permissions")
	}
	if got := err.Error(); !contains(got, "insecure permissions") {
		t.Errorf("error = %q, want it to contain 'insecure permissions'", got)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
