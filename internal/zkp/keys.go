package zkp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/backend/plonk"
)

// Key file names within the cache directory.
const (
	provingKeyFile   = "provingKey.bin"
	verifyingKeyFile = "verifyingKey.bin"
)

// SaveProvingKey serializes a PLONK proving key to disk.
func SaveProvingKey(provingKey plonk.ProvingKey, dir string) error {
	if err := ensureDir(dir); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, provingKeyFile))
	if err != nil {
		return fmt.Errorf("creating proving key file: %w", err)
	}
	defer f.Close()
	_, err = provingKey.WriteTo(f)
	if err != nil {
		return fmt.Errorf("writing proving key: %w", err)
	}
	return nil
}

// SaveVerifyingKey serializes a PLONK verifying key to disk.
func SaveVerifyingKey(verifyingKey plonk.VerifyingKey, dir string) error {
	if err := ensureDir(dir); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, verifyingKeyFile))
	if err != nil {
		return fmt.Errorf("creating verifying key file: %w", err)
	}
	defer f.Close()
	_, err = verifyingKey.WriteTo(f)
	if err != nil {
		return fmt.Errorf("writing verifying key: %w", err)
	}
	return nil
}

// LoadProvingKey reads a PLONK proving key from disk.
func LoadProvingKey(dir string) (plonk.ProvingKey, error) {
	f, err := os.Open(filepath.Join(dir, provingKeyFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCircuitNotCompiled
		}
		return nil, fmt.Errorf("opening proving key: %w", err)
	}
	defer f.Close()
	provingKey := plonk.NewProvingKey(curveID())
	if _, err := provingKey.ReadFrom(f); err != nil {
		return nil, fmt.Errorf("reading proving key: %w", err)
	}
	return provingKey, nil
}

// LoadVerifyingKey reads a PLONK verifying key from disk.
func LoadVerifyingKey(dir string) (plonk.VerifyingKey, error) {
	f, err := os.Open(filepath.Join(dir, verifyingKeyFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCircuitNotCompiled
		}
		return nil, fmt.Errorf("opening verifying key: %w", err)
	}
	defer f.Close()
	verifyingKey := plonk.NewVerifyingKey(curveID())
	if _, err := verifyingKey.ReadFrom(f); err != nil {
		return nil, fmt.Errorf("reading verifying key: %w", err)
	}
	return verifyingKey, nil
}

// KeysExist checks if proving and verifying keys exist on disk.
func KeysExist(dir string) bool {
	_, err1 := os.Stat(filepath.Join(dir, provingKeyFile))
	_, err2 := os.Stat(filepath.Join(dir, verifyingKeyFile))
	return err1 == nil && err2 == nil
}

// ProvingKeyPath returns the full path to the proving key file in the given directory.
func ProvingKeyPath(dir string) string {
	return filepath.Join(dir, provingKeyFile)
}

// VerifyingKeyPath returns the full path to the verifying key file in the given directory.
func VerifyingKeyPath(dir string) string {
	return filepath.Join(dir, verifyingKeyFile)
}

// curveID returns the gnark curve identifier for BN254.
func curveID() ecc.ID {
	return ecc.BN254
}
