package identity

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const testPassword = "test-password-123"

func TestLoadOrCreateIdentity_Creates(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	priv, err := LoadOrCreateIdentity(keyPath, testPassword)
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

	// Verify file is SHRL-encrypted.
	data, _ := os.ReadFile(keyPath)
	if !IsEncrypted(data) {
		t.Fatal("created key file should be SHRL-encrypted")
	}
}

func TestLoadOrCreateIdentity_Loads(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Create key
	priv1, err := LoadOrCreateIdentity(keyPath, testPassword)
	if err != nil {
		t.Fatalf("first LoadOrCreateIdentity() error = %v", err)
	}
	pid1, err := peer.IDFromPrivateKey(priv1)
	if err != nil {
		t.Fatalf("IDFromPrivateKey() error = %v", err)
	}

	// Load same key
	priv2, err := LoadOrCreateIdentity(keyPath, testPassword)
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
	_, err := LoadOrCreateIdentity(keyPath, testPassword)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity() error = %v", err)
	}

	// Weaken permissions
	if err := os.Chmod(keyPath, 0644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	// Loading should fail
	_, err = LoadOrCreateIdentity(keyPath, testPassword)
	if err == nil {
		t.Fatal("LoadOrCreateIdentity() should fail with insecure permissions")
	}
	if !strings.Contains(err.Error(), "insecure permissions") {
		t.Errorf("error = %q, want it to contain 'insecure permissions'", err.Error())
	}
}

func TestCheckKeyFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permissions not applicable on Windows")
	}

	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Create key with correct permissions.
	LoadOrCreateIdentity(keyPath, testPassword)

	// Good permissions (0600)
	if err := CheckKeyFilePermissions(keyPath); err != nil {
		t.Errorf("0600 should pass: %v", err)
	}

	// Bad permissions (0644)
	os.Chmod(keyPath, 0644)
	if err := CheckKeyFilePermissions(keyPath); err == nil {
		t.Error("0644 should fail")
	}

	// Bad permissions (0666)
	os.Chmod(keyPath, 0666)
	if err := CheckKeyFilePermissions(keyPath); err == nil {
		t.Error("0666 should fail")
	}

	// Nonexistent file
	if err := CheckKeyFilePermissions("/nonexistent/key"); err == nil {
		t.Error("nonexistent file should fail")
	}
}

func TestPeerIDFromKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "test.key")

	// Create key first (PeerIDFromKeyFile requires existing file).
	_, err := LoadOrCreateIdentity(keyPath, testPassword)
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity: %v", err)
	}

	pid, err := PeerIDFromKeyFile(keyPath, testPassword)
	if err != nil {
		t.Fatalf("PeerIDFromKeyFile() error = %v", err)
	}
	if pid == "" {
		t.Fatal("PeerIDFromKeyFile() returned empty peer ID")
	}

	// Second call should return same ID.
	pid2, err := PeerIDFromKeyFile(keyPath, testPassword)
	if err != nil {
		t.Fatalf("second PeerIDFromKeyFile() error = %v", err)
	}
	if pid != pid2 {
		t.Errorf("peer IDs differ: %s != %s", pid, pid2)
	}
}

func TestLoadIdentity_RejectsRawKey(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "raw.key")

	// Write a raw (unencrypted) key file.
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	raw, _ := crypto.MarshalPrivateKey(priv)
	os.WriteFile(keyPath, raw, 0600)

	_, err := LoadIdentity(keyPath, "any-password")
	if err != ErrNotEncrypted {
		t.Fatalf("expected ErrNotEncrypted, got: %v", err)
	}
}
