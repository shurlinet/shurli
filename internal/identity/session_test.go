package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSession_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	password := "test-session-password"

	if err := CreateSession(dir, password); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	if !SessionExists(dir) {
		t.Fatal("session should exist after creation")
	}

	got, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got != password {
		t.Fatalf("password mismatch: got %q, want %q", got, password)
	}
}

func TestSession_NoFile(t *testing.T) {
	dir := t.TempDir()

	got, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession (no file): %v", err)
	}
	if got != "" {
		t.Fatalf("expected empty string for no session, got %q", got)
	}
}

func TestSession_Destroy(t *testing.T) {
	dir := t.TempDir()

	CreateSession(dir, "password")
	if !SessionExists(dir) {
		t.Fatal("session should exist")
	}

	if err := DestroySession(dir); err != nil {
		t.Fatalf("DestroySession: %v", err)
	}

	if SessionExists(dir) {
		t.Fatal("session should not exist after destroy")
	}
}

func TestSession_Refresh(t *testing.T) {
	dir := t.TempDir()
	password := "same-password"

	CreateSession(dir, password)

	// Read original session bytes.
	orig, _ := os.ReadFile(filepath.Join(dir, ".session"))

	// Refresh (new crypto material, same password).
	if err := RefreshSession(dir, password); err != nil {
		t.Fatalf("RefreshSession: %v", err)
	}

	// File content should be different (new random, new nonce).
	refreshed, _ := os.ReadFile(filepath.Join(dir, ".session"))
	if string(orig) == string(refreshed) {
		t.Fatal("refreshed session should have different crypto material")
	}

	// Password should still be recoverable.
	got, err := LoadSession(dir)
	if err != nil {
		t.Fatalf("LoadSession after refresh: %v", err)
	}
	if got != password {
		t.Fatalf("password mismatch after refresh: got %q", got)
	}
}

func TestSession_Corrupted(t *testing.T) {
	dir := t.TempDir()
	CreateSession(dir, "password")

	// Corrupt the session file.
	path := filepath.Join(dir, ".session")
	data, _ := os.ReadFile(path)
	data[len(data)-1] ^= 0xFF // Flip last byte.
	os.WriteFile(path, data, 0600)

	_, err := LoadSession(dir)
	if err == nil {
		t.Fatal("expected error for corrupted session")
	}
}
