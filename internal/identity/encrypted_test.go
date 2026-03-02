package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatal(err)
	}

	password := "test-password-123"
	encrypted, err := EncryptKey(priv, password)
	if err != nil {
		t.Fatalf("EncryptKey: %v", err)
	}

	if !IsEncrypted(encrypted) {
		t.Fatal("encrypted data should have SHRL header")
	}

	decrypted, err := DecryptKey(encrypted, password)
	if err != nil {
		t.Fatalf("DecryptKey: %v", err)
	}

	id1, _ := peer.IDFromPrivateKey(priv)
	id2, _ := peer.IDFromPrivateKey(decrypted)
	if id1 != id2 {
		t.Fatalf("peer IDs differ after round-trip: %s vs %s", id1, id2)
	}
}

func TestDecryptKey_WrongPassword(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	encrypted, _ := EncryptKey(priv, "correct-password")

	_, err := DecryptKey(encrypted, "wrong-password")
	if err == nil {
		t.Fatal("expected error for wrong password")
	}
	if err != ErrWrongPassword {
		t.Fatalf("expected ErrWrongPassword, got: %v", err)
	}
}

func TestIsEncrypted_RawKey(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	raw, _ := crypto.MarshalPrivateKey(priv)

	if IsEncrypted(raw) {
		t.Fatal("raw key should not be detected as encrypted")
	}
}

func TestDecryptKey_RawKey(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	raw, _ := crypto.MarshalPrivateKey(priv)

	_, err := DecryptKey(raw, "password")
	if err != ErrNotEncrypted {
		t.Fatalf("expected ErrNotEncrypted, got: %v", err)
	}
}

func TestSHRLFormat(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	encrypted, _ := EncryptKey(priv, "test")

	// Check magic.
	if string(encrypted[:4]) != "SHRL" {
		t.Fatalf("magic bytes: %q, expected SHRL", string(encrypted[:4]))
	}

	// Check version.
	if encrypted[4] != 1 {
		t.Fatalf("version: %d, expected 1", encrypted[4])
	}

	// Minimum size: header(5) + salt(16) + nonce(24) + at least some ciphertext.
	if len(encrypted) < shrlDataOff+16 {
		t.Fatalf("encrypted data too short: %d bytes", len(encrypted))
	}
}

func TestChangeKeyPassword(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "identity.key")

	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	origID, _ := peer.IDFromPrivateKey(priv)

	// Save with old password.
	encrypted, _ := EncryptKey(priv, "old-password")
	os.WriteFile(keyPath, encrypted, 0600)

	// Change password.
	if err := ChangeKeyPassword(keyPath, "old-password", "new-password"); err != nil {
		t.Fatalf("ChangeKeyPassword: %v", err)
	}

	// Old password should fail.
	data, _ := os.ReadFile(keyPath)
	_, err := DecryptKey(data, "old-password")
	if err != ErrWrongPassword {
		t.Fatalf("old password should fail, got: %v", err)
	}

	// New password should work.
	recovered, err := DecryptKey(data, "new-password")
	if err != nil {
		t.Fatalf("new password failed: %v", err)
	}

	newID, _ := peer.IDFromPrivateKey(recovered)
	if origID != newID {
		t.Fatal("peer ID changed after password change")
	}
}

func TestLoadOrCreateIdentity_Create(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "identity.key")

	priv, err := LoadOrCreateIdentity(keyPath, "test-pw")
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity create: %v", err)
	}
	if priv == nil {
		t.Fatal("returned nil key")
	}

	// File should exist and be SHRL-encrypted.
	data, _ := os.ReadFile(keyPath)
	if !IsEncrypted(data) {
		t.Fatal("created file should be SHRL-encrypted")
	}
}

func TestLoadOrCreateIdentity_Load(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "identity.key")

	priv1, _ := LoadOrCreateIdentity(keyPath, "test-pw")
	id1, _ := peer.IDFromPrivateKey(priv1)

	priv2, err := LoadOrCreateIdentity(keyPath, "test-pw")
	if err != nil {
		t.Fatalf("LoadOrCreateIdentity load: %v", err)
	}
	id2, _ := peer.IDFromPrivateKey(priv2)

	if id1 != id2 {
		t.Fatalf("peer IDs differ after reload: %s vs %s", id1, id2)
	}
}

func TestSeedDerivedKey_EncryptDecrypt(t *testing.T) {
	_, entropy, _ := GenerateSeed()

	// Derive identity key from seed.
	priv, _ := DeriveIdentityKey(entropy)
	origID, _ := peer.IDFromPrivateKey(priv)

	// Encrypt, save, reload.
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "identity.key")
	SaveIdentity(keyPath, priv, "my-password")

	loaded, err := LoadIdentity(keyPath, "my-password")
	if err != nil {
		t.Fatalf("LoadIdentity: %v", err)
	}

	loadedID, _ := peer.IDFromPrivateKey(loaded)
	if origID != loadedID {
		t.Fatal("seed-derived key changed after encrypt/decrypt")
	}
}
