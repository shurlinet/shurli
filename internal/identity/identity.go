package identity

import (
	"fmt"
	"os"
	"runtime"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// CheckKeyFilePermissions verifies that a key file is not readable by group or others.
func CheckKeyFilePermissions(path string) error {
	if runtime.GOOS == "windows" {
		return nil // Windows file permissions work differently
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat key file %s: %w", path, err)
	}
	mode := info.Mode().Perm()
	if mode&0077 != 0 {
		return fmt.Errorf("key file %s has insecure permissions %04o (expected 0600); fix with: chmod 600 %s", path, mode, path)
	}
	return nil
}

// LoadIdentity loads an encrypted identity key from disk.
// The file MUST be in SHRL format. Raw (unencrypted) keys are rejected.
func LoadIdentity(path, password string) (crypto.PrivKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading identity key: %w", err)
	}

	if err := CheckKeyFilePermissions(path); err != nil {
		return nil, err
	}

	if !IsEncrypted(data) {
		return nil, ErrNotEncrypted
	}

	return DecryptKey(data, password)
}

// SaveIdentity encrypts and saves a private key to disk in SHRL format.
func SaveIdentity(path string, privKey crypto.PrivKey, password string) error {
	data, err := EncryptKey(privKey, password)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

// LoadOrCreateIdentity loads an existing SHRL-encrypted identity or creates a new one.
// When creating, generates a random Ed25519 key (no seed derivation).
// For seed-derived keys, use DeriveIdentityKey + SaveIdentity directly.
func LoadOrCreateIdentity(path, password string) (crypto.PrivKey, error) {
	if _, err := os.Stat(path); err == nil {
		return LoadIdentity(path, password)
	}

	// Generate new key.
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("generating keypair: %w", err)
	}

	if err := SaveIdentity(path, priv, password); err != nil {
		return nil, err
	}

	return priv, nil
}

// PeerIDFromKeyFile loads an encrypted key file and returns the derived peer ID.
func PeerIDFromKeyFile(path, password string) (peer.ID, error) {
	priv, err := LoadIdentity(path, password)
	if err != nil {
		return "", err
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("deriving peer ID: %w", err)
	}
	return id, nil
}
