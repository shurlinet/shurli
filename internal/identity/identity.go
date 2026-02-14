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

// LoadOrCreateIdentity loads an existing identity from a file or creates a new one.
func LoadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	// Try to load existing key
	if data, err := os.ReadFile(path); err == nil {
		// Check permissions before using the key
		if err := CheckKeyFilePermissions(path); err != nil {
			return nil, err
		}
		priv, err := crypto.UnmarshalPrivateKey(data)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal key from %s: %w", path, err)
		}
		return priv, nil
	}

	// Generate new key
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		return nil, fmt.Errorf("failed to generate keypair: %w", err)
	}

	// Marshal and save
	data, err := crypto.MarshalPrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal private key: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return nil, fmt.Errorf("failed to save key to %s: %w", path, err)
	}

	return priv, nil
}

// PeerIDFromKeyFile loads (or creates) a key file and returns the derived peer ID.
func PeerIDFromKeyFile(path string) (peer.ID, error) {
	priv, err := LoadOrCreateIdentity(path)
	if err != nil {
		return "", err
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		return "", fmt.Errorf("failed to derive peer ID: %w", err)
	}
	return id, nil
}
