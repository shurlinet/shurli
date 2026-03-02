package vault

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/totp"
)

// testSeed generates a BIP39 seed for testing.
func testSeed(t *testing.T) ([]byte, string) {
	t.Helper()
	mnemonic, entropy, err := identity.GenerateSeed()
	if err != nil {
		t.Fatalf("GenerateSeed: %v", err)
	}
	return entropy, mnemonic
}

// testCreate is a helper that creates a vault with a test seed.
func testCreate(t *testing.T, password string, enableTOTP bool, autoSealMins int) (*Vault, string) {
	t.Helper()
	seedBytes, mnemonic := testSeed(t)
	v, err := Create(seedBytes, mnemonic, password, enableTOTP, autoSealMins)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return v, mnemonic
}

func TestCreateAndUnseal(t *testing.T) {
	v, _ := testCreate(t, "test-password", false, 0)
	if v.IsSealed() {
		t.Fatal("newly created vault should be unsealed")
	}

	key, err := v.RootKey()
	if err != nil {
		t.Fatalf("RootKey: %v", err)
	}
	if len(key) != rootKeyLen {
		t.Errorf("root key length = %d, want %d", len(key), rootKeyLen)
	}
}

func TestSealAndUnseal(t *testing.T) {
	v, _ := testCreate(t, "my-password", false, 0)

	// Remember the root key
	originalKey, _ := v.RootKey()
	keyCopy := make([]byte, len(originalKey))
	copy(keyCopy, originalKey)

	// Seal
	v.Seal()
	if !v.IsSealed() {
		t.Fatal("vault should be sealed")
	}

	_, err := v.RootKey()
	if !errors.Is(err, ErrVaultSealed) {
		t.Fatalf("expected ErrVaultSealed, got: %v", err)
	}

	// Unseal
	if err := v.Unseal("my-password", ""); err != nil {
		t.Fatalf("Unseal: %v", err)
	}

	key, err := v.RootKey()
	if err != nil {
		t.Fatalf("RootKey after unseal: %v", err)
	}

	// Key should match the original
	if len(key) != len(keyCopy) {
		t.Fatalf("key length mismatch: %d vs %d", len(key), len(keyCopy))
	}
	for i := range key {
		if key[i] != keyCopy[i] {
			t.Fatalf("key mismatch at byte %d", i)
		}
	}
}

func TestUnsealWrongPassword(t *testing.T) {
	v, _ := testCreate(t, "correct-password", false, 0)

	v.Seal()

	err := v.Unseal("wrong-password", "")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("expected ErrInvalidPassword, got: %v", err)
	}

	if !v.IsSealed() {
		t.Fatal("vault should remain sealed after wrong password")
	}
}

func TestUnsealWithTOTP(t *testing.T) {
	v, _ := testCreate(t, "password123", true, 0)

	// Get TOTP config while unsealed
	totpCfg := v.totpConfig
	if totpCfg == nil {
		t.Fatal("TOTP config should be set")
	}

	// Generate valid code
	code := totp.Generate(totpCfg, time.Now())

	v.Seal()

	// Unseal with correct TOTP
	if err := v.Unseal("password123", code); err != nil {
		t.Fatalf("Unseal with TOTP: %v", err)
	}
}

func TestUnsealWithWrongTOTP(t *testing.T) {
	v, _ := testCreate(t, "password123", true, 0)

	v.Seal()

	err := v.Unseal("password123", "000000")
	if !errors.Is(err, ErrInvalidTOTP) {
		t.Fatalf("expected ErrInvalidTOTP, got: %v", err)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")

	v, _ := testCreate(t, "password123", false, 30)

	originalKey, _ := v.RootKey()
	keyCopy := make([]byte, len(originalKey))
	copy(keyCopy, originalKey)

	if err := v.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Verify file exists and has restrictive permissions
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Load from disk (starts sealed)
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !loaded.IsSealed() {
		t.Fatal("loaded vault should be sealed")
	}

	// Unseal and verify key matches
	if err := loaded.Unseal("password123", ""); err != nil {
		t.Fatalf("Unseal loaded: %v", err)
	}
	loadedKey, _ := loaded.RootKey()
	for i := range keyCopy {
		if loadedKey[i] != keyCopy[i] {
			t.Fatalf("loaded key mismatch at byte %d", i)
		}
	}
}

func TestSeedRecovery(t *testing.T) {
	seedBytes, mnemonic := testSeed(t)

	// Create vault from seed.
	v, err := Create(seedBytes, mnemonic, "old-password", false, 0)
	if err != nil {
		t.Fatal(err)
	}

	originalKey, _ := v.RootKey()
	keyCopy := make([]byte, len(originalKey))
	copy(keyCopy, originalKey)

	// Recover from the same mnemonic with a new password.
	recovered, err := RecoverFromSeed(mnemonic, "new-password", false, 0)
	if err != nil {
		t.Fatalf("RecoverFromSeed: %v", err)
	}

	recoveredKey, _ := recovered.RootKey()
	for i := range keyCopy {
		if recoveredKey[i] != keyCopy[i] {
			t.Fatalf("recovered key mismatch at byte %d", i)
		}
	}

	// Verify the recovered vault can be sealed and unsealed with the new password.
	recovered.Seal()
	if err := recovered.Unseal("new-password", ""); err != nil {
		t.Fatalf("Unseal recovered: %v", err)
	}
}

func TestSeedRecoveryInvalid(t *testing.T) {
	_, err := RecoverFromSeed("not a valid seed", "password123", false, 0)
	if !errors.Is(err, ErrInvalidSeed) {
		t.Fatalf("expected ErrInvalidSeed, got: %v", err)
	}
}

func TestAutoSeal(t *testing.T) {
	v, _ := testCreate(t, "password123", false, 1) // 1 minute auto-seal

	// Should not auto-seal immediately
	if v.ShouldAutoSeal() {
		t.Error("should not auto-seal immediately")
	}

	// Fake the unseal time to 2 minutes ago
	v.mu.Lock()
	v.unsealedAt = time.Now().Add(-2 * time.Minute)
	v.mu.Unlock()

	if !v.ShouldAutoSeal() {
		t.Error("should auto-seal after timeout")
	}
}

func TestAutoSealDisabled(t *testing.T) {
	v, _ := testCreate(t, "password123", false, 0) // 0 = no auto-seal

	v.mu.Lock()
	v.unsealedAt = time.Now().Add(-24 * time.Hour)
	v.mu.Unlock()

	if v.ShouldAutoSeal() {
		t.Error("should not auto-seal when disabled (0 minutes)")
	}
}

func TestDoubleUnseal(t *testing.T) {
	v, _ := testCreate(t, "password123", false, 0)

	err := v.Unseal("password123", "")
	if !errors.Is(err, ErrVaultAlreadyUnsealed) {
		t.Fatalf("expected ErrVaultAlreadyUnsealed, got: %v", err)
	}
}

func TestMemoryZeroing(t *testing.T) {
	v, _ := testCreate(t, "password123", false, 0)

	key, _ := v.RootKey()
	// Take a reference to the underlying array
	keyRef := key

	v.Seal()

	// After sealing, the referenced bytes should be zeroed
	allZero := true
	for _, b := range keyRef {
		if b != 0 {
			allZero = false
			break
		}
	}
	if !allZero {
		t.Error("root key memory should be zeroed after seal (best-effort in Go)")
	}
}

func TestTOTPProvisioningURI(t *testing.T) {
	v, _ := testCreate(t, "password123", true, 0)

	uri, err := v.TOTPProvisioningURI("relay.example.com")
	if err != nil {
		t.Fatalf("TOTPProvisioningURI: %v", err)
	}

	if uri == "" {
		t.Error("URI should not be empty")
	}
}

func TestTOTPProvisioningURISealed(t *testing.T) {
	v, _ := testCreate(t, "password123", true, 0)

	v.Seal()

	_, err := v.TOTPProvisioningURI("relay.example.com")
	if err == nil {
		t.Error("should error when sealed")
	}
}

func TestChangePassword(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vault.json")

	v, _ := testCreate(t, "old-password", false, 0)
	if err := v.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}

	originalKey, _ := v.RootKey()
	keyCopy := make([]byte, len(originalKey))
	copy(keyCopy, originalKey)

	// Change password.
	if err := v.ChangePassword("old-password", "new-password"); err != nil {
		t.Fatalf("ChangePassword: %v", err)
	}

	// Vault should still be unsealed.
	if v.IsSealed() {
		t.Fatal("vault should remain unsealed after password change")
	}

	// Seal and unseal with new password.
	v.Seal()
	if err := v.Unseal("new-password", ""); err != nil {
		t.Fatalf("Unseal with new password: %v", err)
	}

	// Old password should fail.
	v.Seal()
	if err := v.Unseal("old-password", ""); !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("expected ErrInvalidPassword with old password, got: %v", err)
	}

	// Root key should be unchanged.
	v.Unseal("new-password", "")
	key, _ := v.RootKey()
	for i := range keyCopy {
		if key[i] != keyCopy[i] {
			t.Fatalf("root key changed after password change at byte %d", i)
		}
	}
}

func TestChangePasswordWrongOld(t *testing.T) {
	v, _ := testCreate(t, "correct-password", false, 0)

	err := v.ChangePassword("wrong-password", "new-password")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Fatalf("expected ErrInvalidPassword, got: %v", err)
	}
}

func TestChangePasswordSealed(t *testing.T) {
	v, _ := testCreate(t, "password123", false, 0)
	v.Seal()

	err := v.ChangePassword("password123", "new-password")
	if !errors.Is(err, ErrVaultSealed) {
		t.Fatalf("expected ErrVaultSealed, got: %v", err)
	}
}
