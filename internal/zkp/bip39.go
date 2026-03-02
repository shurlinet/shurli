package zkp

import (
	"crypto/sha256"
	"os"
	"strings"

	"github.com/shurlinet/shurli/internal/identity"
)

// GenerateMnemonic generates a new 24-word BIP39 mnemonic.
// Delegates to the identity package which owns seed generation.
func GenerateMnemonic() (string, error) {
	mnemonic, _, err := identity.GenerateSeed()
	return mnemonic, err
}

// ValidateMnemonic checks that a mnemonic is a valid 24-word BIP39 phrase.
// Delegates to the identity package.
func ValidateMnemonic(mnemonic string) error {
	return identity.ValidateMnemonic(mnemonic)
}

// MnemonicToSeedBytes converts a BIP39 mnemonic to deterministic seed bytes.
// Uses SHA256(mnemonic) to produce a 32-byte seed suitable for gnark's
// WithToxicSeed option. This is intentionally NOT BIP39's PBKDF2 derivation
// (which targets HD wallet key trees); we just need a deterministic mapping
// from mnemonic to a scalar for SRS generation.
func MnemonicToSeedBytes(mnemonic string) []byte {
	h := sha256.Sum256([]byte(mnemonic))
	return h[:]
}

// ReadSeedFile reads a BIP39 mnemonic from a file and validates it.
func ReadSeedFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	perm := info.Mode().Perm()
	if perm&0077 != 0 {
		return "", os.ErrPermission
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	mnemonic := strings.TrimSpace(string(data))
	if err := ValidateMnemonic(mnemonic); err != nil {
		return "", err
	}
	return mnemonic, nil
}

// WriteSeedFile writes a BIP39 mnemonic to a file with strict permissions (0600).
func WriteSeedFile(path, mnemonic string) error {
	return os.WriteFile(path, []byte(mnemonic+"\n"), 0600)
}
