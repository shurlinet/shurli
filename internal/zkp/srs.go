package zkp

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/constraint"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/scs"
	"github.com/consensys/gnark/test/unsafekzg"
)

// defaultSRSDir returns the default directory for SRS and key caching.
func defaultSRSDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".shurli/zkp"
	}
	return filepath.Join(home, ".shurli", "zkp")
}

// ensureDir creates the directory if it doesn't exist.
func ensureDir(dir string) error {
	return os.MkdirAll(dir, 0700)
}

// CompileCircuit compiles the MembershipCircuit into a constraint system.
func CompileCircuit() (constraint.ConstraintSystem, error) {
	circuit := &MembershipCircuit{}
	return frontend.Compile(ecc.BN254.ScalarField(), scs.NewBuilder, circuit)
}

// GenerateSRS generates the KZG SRS for the compiled constraint system.
// Uses gnark's unsafekzg with filesystem caching. The SRS is auto-sized
// to the constraint system and cached at cacheDir (defaults to ~/.shurli/zkp/).
//
// For a private authorized-pool ZKP system, unsafekzg provides equivalent
// security to a ceremony SRS: the toxic value is generated randomly once,
// used for setup, and the security relies on it not being recoverable
// from the proving/verifying keys.
func GenerateSRS(ccs constraint.ConstraintSystem, cacheDir string) (canonical, lagrange interface{}, err error) {
	if cacheDir == "" {
		cacheDir = defaultSRSDir()
	}
	if err := ensureDir(cacheDir); err != nil {
		return nil, nil, fmt.Errorf("creating SRS cache dir: %w", err)
	}

	srs, srsLagrange, err := unsafekzg.NewSRS(ccs,
		unsafekzg.WithFSCache(),
		unsafekzg.WithCacheDir(cacheDir),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("generating SRS: %w", err)
	}

	return srs, srsLagrange, nil
}

// SetupKeys compiles the membership circuit, generates SRS, runs PLONK setup,
// and saves the proving + verifying keys to keysDir. This is a one-time
// operation: subsequent starts load cached keys. Takes ~2-5s depending on
// hardware. Safe to call if keys already exist (no-op check via KeysExist).
//
// Uses random SRS generation. For deterministic key generation from a
// BIP39 seed phrase, use SetupKeysFromSeed instead.
func SetupKeys(keysDir string) error {
	if keysDir == "" {
		keysDir = defaultSRSDir()
	}
	if KeysExist(keysDir) {
		return nil // already done
	}
	if err := ensureDir(keysDir); err != nil {
		return fmt.Errorf("creating keys dir: %w", err)
	}

	ccs, err := CompileCircuit()
	if err != nil {
		return fmt.Errorf("compiling circuit: %w", err)
	}

	srs, srsLagrange, err := unsafekzg.NewSRS(ccs,
		unsafekzg.WithFSCache(),
		unsafekzg.WithCacheDir(keysDir),
	)
	if err != nil {
		return fmt.Errorf("generating SRS: %w", err)
	}

	provingKey, verifyingKey, err := plonk.Setup(ccs, srs, srsLagrange)
	if err != nil {
		return fmt.Errorf("PLONK setup: %w", err)
	}

	if err := SaveProvingKey(provingKey, keysDir); err != nil {
		return fmt.Errorf("saving proving key: %w", err)
	}
	if err := SaveVerifyingKey(verifyingKey, keysDir); err != nil {
		return fmt.Errorf("saving verifying key: %w", err)
	}

	return nil
}

// SetupKeysFromSeed compiles the circuit, generates a deterministic SRS from
// the given BIP39 mnemonic, runs PLONK setup, and saves the proving key and
// verifying key to keysDir.
//
// Same seed phrase = same SRS = same keys on any machine. This enables relay
// and client to independently derive compatible proving/verifying keys without
// copying key files.
//
// If keys already exist in keysDir, this is a no-op (same as SetupKeys).
// To regenerate from a different seed, wipe the keys directory first.
func SetupKeysFromSeed(keysDir string, mnemonic string) error {
	if keysDir == "" {
		keysDir = defaultSRSDir()
	}
	if KeysExist(keysDir) {
		return nil // already done
	}
	if err := ensureDir(keysDir); err != nil {
		return fmt.Errorf("creating keys dir: %w", err)
	}

	ccs, err := CompileCircuit()
	if err != nil {
		return fmt.Errorf("compiling circuit: %w", err)
	}

	// Derive deterministic SRS from the mnemonic.
	// MnemonicToSeedBytes returns SHA256(mnemonic).
	// gnark's WithToxicSeed does SHA256(seed) -> tau (toxic value).
	// Same mnemonic -> same tau -> same SRS -> same proving/verifying keys.
	seedBytes := MnemonicToSeedBytes(mnemonic)
	srs, srsLagrange, err := unsafekzg.NewSRS(ccs,
		unsafekzg.WithToxicSeed(seedBytes),
	)
	if err != nil {
		return fmt.Errorf("generating deterministic SRS: %w", err)
	}

	provingKey, verifyingKey, err := plonk.Setup(ccs, srs, srsLagrange)
	if err != nil {
		return fmt.Errorf("PLONK setup: %w", err)
	}

	if err := SaveProvingKey(provingKey, keysDir); err != nil {
		return fmt.Errorf("saving proving key: %w", err)
	}
	if err := SaveVerifyingKey(verifyingKey, keysDir); err != nil {
		return fmt.Errorf("saving verifying key: %w", err)
	}

	return nil
}
