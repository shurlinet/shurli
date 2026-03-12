// Package vault implements a password-sealed key vault for the Shurli relay.
//
// The vault protects the relay's root key material (used for macaroon minting).
// When sealed, the relay operates in watch-only mode: it routes traffic for
// existing peers but cannot authorize new ones. Unsealing requires a password
// (and optionally a TOTP code), then auto-reseals after a configurable timeout.
//
// The vault root key is derived from the unified BIP39 seed via HKDF domain
// separation ("shurli/vault/v1"), ensuring one seed backup covers identity,
// vault, and ZKP keys.
//
// Crypto: Argon2id for password KDF, XChaCha20-Poly1305 for encryption.
// Recovery: BIP39 24-word seed phrase re-derives the root key via HKDF.
package vault

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/shurlinet/shurli/internal/identity"
	"github.com/shurlinet/shurli/internal/totp"
)

var (
	ErrVaultSealed         = errors.New("vault is sealed")
	ErrVaultAlreadyUnsealed = errors.New("vault is already unsealed")
	ErrInvalidPassword   = errors.New("invalid password")
	ErrInvalidTOTP         = errors.New("invalid TOTP code")
	ErrInvalidYubikey      = errors.New("invalid Yubikey response")
	ErrInvalidSeed         = errors.New("invalid seed phrase")
	ErrVaultNotInitialized = errors.New("vault not initialized")
)

// Argon2id parameters tuned for a solo operator VPS.
// time=3, memory=64MB, threads=4 gives ~1-2s derivation on modest hardware.
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB in KiB
	argonThreads = 4
	argonKeyLen  = 32
	saltLen      = 16
	rootKeyLen   = 32
)

// SealedData is the on-disk representation of the vault.
type SealedData struct {
	Version         int    `json:"version"`
	Salt            []byte `json:"salt"`            // Argon2id salt
	EncryptedKey    []byte `json:"encrypted_key"`   // XChaCha20-Poly1305(rootKey)
	Nonce           []byte `json:"nonce"`           // XChaCha20-Poly1305 nonce
	TOTPEnabled     bool   `json:"totp_enabled"`
	TOTPSecret      []byte `json:"totp_secret,omitempty"`    // encrypted alongside root key
	TOTPNonce       []byte `json:"totp_nonce,omitempty"`
	LastTOTPCounter uint64 `json:"last_totp_counter,omitempty"` // replay prevention (RFC 6238)
	SeedHash        []byte `json:"seed_hash"`       // SHA-256 of seed for verification
	AutoSealMins    int    `json:"auto_seal_mins"`  // auto-reseal timeout (0 = manual)
	YubikeyEnabled      bool   `json:"yubikey_enabled"`                // Yubikey HMAC-SHA1 challenge-response
	YubikeySlot         int    `json:"yubikey_slot,omitempty"`         // 1 or 2
	YubikeyResponseHash []byte `json:"yubikey_response_hash,omitempty"` // SHA-256 of expected response
}

// Vault manages the relay's root key material.
type Vault struct {
	mu           sync.RWMutex
	sealed       bool
	rootKey      []byte // nil when sealed
	totpConfig   *totp.Config
	sealedData   *SealedData
	filePath     string
	unsealedAt   time.Time
	autoSealMins int
}

// Create initializes a new vault from seed entropy and a password.
// The root key is derived from seedBytes via HKDF("shurli/vault/v1"),
// ensuring the same BIP39 seed that derives the identity key also
// derives the vault key (different HKDF domain = cryptographic independence).
//
// The caller is responsible for seed generation and display (done at shurli init).
// The mnemonic parameter is the BIP39 phrase used to compute SeedHash for
// recovery verification.
func Create(seedBytes []byte, mnemonic, password string, enableTOTP bool, autoSealMins int) (*Vault, error) {
	// Derive vault root key from seed via HKDF.
	rootKey, err := identity.DeriveVaultKey(seedBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to derive vault key: %w", err)
	}

	// Hash the mnemonic for recovery verification.
	seedHash := sha256.Sum256([]byte(mnemonic))

	// Generate salt for Argon2id.
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive encryption key from password.
	encKey := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(encKey)

	// Encrypt root key.
	encryptedKey, nonce, err := encrypt(encKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt root key: %w", err)
	}

	sd := &SealedData{
		Version:      1,
		Salt:         salt,
		EncryptedKey: encryptedKey,
		Nonce:        nonce,
		SeedHash:     seedHash[:],
		AutoSealMins: autoSealMins,
	}

	var totpCfg *totp.Config
	if enableTOTP {
		totpSecret, err := totp.NewSecret(20)
		if err != nil {
			return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
		}
		encTOTP, totpNonce, err := encrypt(encKey, totpSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt TOTP secret: %w", err)
		}
		sd.TOTPEnabled = true
		sd.TOTPSecret = encTOTP
		sd.TOTPNonce = totpNonce
		totpCfg = &totp.Config{Secret: totpSecret}
	}

	v := &Vault{
		sealed:       false,
		rootKey:      rootKey,
		totpConfig:   totpCfg,
		sealedData:   sd,
		unsealedAt:   time.Now(),
		autoSealMins: autoSealMins,
	}

	return v, nil
}

// Load reads a vault from disk in sealed state.
func Load(path string) (*Vault, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read vault file: %w", err)
	}

	var sd SealedData
	if err := json.Unmarshal(data, &sd); err != nil {
		return nil, fmt.Errorf("failed to parse vault file: %w", err)
	}

	return &Vault{
		sealed:       true,
		sealedData:   &sd,
		filePath:     path,
		autoSealMins: sd.AutoSealMins,
	}, nil
}

// Save persists the sealed vault data to disk.
func (v *Vault) Save(path string) error {
	v.mu.RLock()
	defer v.mu.RUnlock()

	if v.sealedData == nil {
		return ErrVaultNotInitialized
	}

	data, err := json.MarshalIndent(v.sealedData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal vault: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("failed to write vault file: %w", err)
	}

	v.filePath = path
	return nil
}

// Unseal decrypts the root key using the password, validates the TOTP code,
// and checks the Yubikey HMAC-SHA1 challenge-response if enabled.
// The yubikeyResponse parameter is the raw HMAC-SHA1 response from the YubiKey;
// it is only checked when YubikeyEnabled is true in the vault config.
func (v *Vault) Unseal(password, totpCode string, yubikeyResponse []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.sealed {
		return ErrVaultAlreadyUnsealed
	}

	sd := v.sealedData
	if sd == nil {
		return ErrVaultNotInitialized
	}

	// Derive key from password
	encKey := argon2.IDKey([]byte(password), sd.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(encKey)

	// Decrypt root key
	rootKey, err := decrypt(encKey, sd.EncryptedKey, sd.Nonce)
	if err != nil {
		return ErrInvalidPassword
	}

	// Decrypt and validate TOTP if enabled
	if sd.TOTPEnabled {
		totpSecret, err := decrypt(encKey, sd.TOTPSecret, sd.TOTPNonce)
		if err != nil {
			return ErrInvalidPassword
		}

		cfg := &totp.Config{Secret: totpSecret}
		counter, ok := totp.ValidateWithCounter(cfg, totpCode, time.Now(), 1, sd.LastTOTPCounter)
		if !ok {
			// Zero the decrypted key before returning
			zeroBytes(rootKey)
			return ErrInvalidTOTP
		}
		sd.LastTOTPCounter = counter
		v.totpConfig = cfg
		// Persist updated counter to prevent replay across restarts.
		v.persistCounterLocked()
	}

	// Validate Yubikey HMAC-SHA1 challenge-response if enabled.
	if sd.YubikeyEnabled {
		if len(yubikeyResponse) == 0 {
			zeroBytes(rootKey)
			return ErrInvalidYubikey
		}
		respHash := sha256.Sum256(yubikeyResponse)
		if subtle.ConstantTimeCompare(respHash[:], sd.YubikeyResponseHash) != 1 {
			zeroBytes(rootKey)
			return ErrInvalidYubikey
		}
	}

	v.rootKey = rootKey
	v.sealed = false
	v.unsealedAt = time.Now()

	return nil
}

// YubikeyChallenge returns the challenge bytes to send to the YubiKey.
// Derived from SHA-256(salt)[:20] (20 bytes = standard HMAC-SHA1 challenge length).
func (v *Vault) YubikeyChallenge() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealedData == nil {
		return nil, ErrVaultNotInitialized
	}
	h := sha256.Sum256(v.sealedData.Salt)
	return h[:20], nil
}

// EnableYubikey configures Yubikey HMAC-SHA1 challenge-response on this vault.
// The responseHash is SHA-256 of the expected Yubikey response for the salt-derived challenge.
// slot is 1 or 2 (matching the YubiKey HMAC-SHA1 slot).
func (v *Vault) EnableYubikey(slot int, responseHash []byte) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.sealedData == nil {
		return ErrVaultNotInitialized
	}
	v.sealedData.YubikeyEnabled = true
	v.sealedData.YubikeySlot = slot
	v.sealedData.YubikeyResponseHash = make([]byte, len(responseHash))
	copy(v.sealedData.YubikeyResponseHash, responseHash)
	if v.filePath == "" {
		return nil
	}
	data, err := json.MarshalIndent(v.sealedData, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal vault: %w", err)
	}
	return os.WriteFile(v.filePath, data, 0600)
}

// persistCounterLocked saves the sealed data to disk to persist the TOTP counter.
// Must be called with v.mu held. Errors are logged but not fatal (counter is
// also held in memory for the current session).
func (v *Vault) persistCounterLocked() {
	if v.filePath == "" || v.sealedData == nil {
		return
	}
	data, err := json.MarshalIndent(v.sealedData, "", "  ")
	if err != nil {
		return
	}
	// Atomic write: temp + rename.
	tmp := v.filePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		os.Remove(tmp)
		return
	}
	if err := os.Rename(tmp, v.filePath); err != nil {
		os.Remove(tmp)
	}
}

// Seal zeroes the root key from memory and marks the vault as sealed.
func (v *Vault) Seal() {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.rootKey != nil {
		zeroBytes(v.rootKey)
		v.rootKey = nil
	}
	if v.totpConfig != nil {
		zeroBytes(v.totpConfig.Secret)
		v.totpConfig = nil
	}
	v.sealed = true
}

// IsSealed returns whether the vault is currently sealed.
func (v *Vault) IsSealed() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.sealed
}

// RootKey returns the root key or an error if the vault is sealed.
func (v *Vault) RootKey() ([]byte, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealed || v.rootKey == nil {
		return nil, ErrVaultSealed
	}
	return v.rootKey, nil
}

// TOTPProvisioningURI returns the otpauth:// URI for TOTP setup.
// Only valid when the vault is unsealed and TOTP is enabled.
func (v *Vault) TOTPProvisioningURI(relayName string) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.totpConfig == nil {
		return "", fmt.Errorf("TOTP not enabled or vault sealed")
	}
	return totp.FormatProvisioningURI(v.totpConfig.Secret, "Shurli", relayName), nil
}

// AutoSealMinutes returns the configured auto-seal timeout.
func (v *Vault) AutoSealMinutes() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.autoSealMins
}

// TOTPEnabled returns whether TOTP is configured on this vault.
func (v *Vault) TOTPEnabled() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealedData == nil {
		return false
	}
	return v.sealedData.TOTPEnabled
}

// YubikeyEnabled returns whether Yubikey challenge-response is configured.
func (v *Vault) YubikeyEnabled() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealedData == nil {
		return false
	}
	return v.sealedData.YubikeyEnabled
}

// YubikeySlot returns the configured Yubikey HMAC-SHA1 slot (1 or 2).
func (v *Vault) YubikeySlot() int {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealedData == nil {
		return 0
	}
	return v.sealedData.YubikeySlot
}

// ShouldAutoSeal returns true if the vault has been unsealed longer than
// the configured auto-seal timeout.
func (v *Vault) ShouldAutoSeal() bool {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if v.sealed || v.autoSealMins <= 0 {
		return false
	}
	return time.Since(v.unsealedAt) > time.Duration(v.autoSealMins)*time.Minute
}

// RecoverFromSeed reconstructs a vault from a BIP39 mnemonic and new password.
// The mnemonic is converted to seed bytes, then the vault root key is derived
// via HKDF("shurli/vault/v1"). This produces the same root key as the original
// Create() call with the same seed.
func RecoverFromSeed(mnemonic, newPassword string, enableTOTP bool, autoSealMins int) (*Vault, error) {
	seedBytes, err := identity.SeedFromMnemonic(mnemonic)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSeed, err)
	}
	defer zeroBytes(seedBytes)

	rootKey, err := identity.DeriveVaultKey(seedBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: failed to derive vault key", ErrInvalidSeed)
	}
	// rootKey stored in Vault.rootKey; zeroed when Vault.Seal() is called.

	// Generate new salt.
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive new encryption key from password.
	encKey := argon2.IDKey([]byte(newPassword), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(encKey)

	// Encrypt root key with new password.
	encryptedKey, nonce, err := encrypt(encKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt root key: %w", err)
	}

	seedHash := sha256.Sum256([]byte(mnemonic))

	sd := &SealedData{
		Version:      1,
		Salt:         salt,
		EncryptedKey: encryptedKey,
		Nonce:        nonce,
		SeedHash:     seedHash[:],
		AutoSealMins: autoSealMins,
	}

	var totpCfg *totp.Config
	if enableTOTP {
		totpSecret, err := totp.NewSecret(20)
		if err != nil {
			return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
		}
		encTOTP, totpNonce, err := encrypt(encKey, totpSecret)
		if err != nil {
			return nil, fmt.Errorf("failed to encrypt TOTP secret: %w", err)
		}
		sd.TOTPEnabled = true
		sd.TOTPSecret = encTOTP
		sd.TOTPNonce = totpNonce
		totpCfg = &totp.Config{Secret: totpSecret}
	}

	return &Vault{
		sealed:       false,
		rootKey:      rootKey,
		totpConfig:   totpCfg,
		sealedData:   sd,
		unsealedAt:   time.Now(),
		autoSealMins: autoSealMins,
	}, nil
}

// ChangePassword re-encrypts the vault root key with a new password.
// The vault must be unsealed (root key in memory) for this to work.
// After changing, the vault remains unsealed.
func (v *Vault) ChangePassword(oldPassword, newPassword string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if v.sealed || v.rootKey == nil {
		return ErrVaultSealed
	}
	if v.sealedData == nil {
		return ErrVaultNotInitialized
	}

	// Reject same password.
	if oldPassword == newPassword {
		return fmt.Errorf("new password must be different from current password")
	}

	// Verify old password by attempting decryption.
	oldEncKey := argon2.IDKey([]byte(oldPassword), v.sealedData.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(oldEncKey)
	if _, err := decrypt(oldEncKey, v.sealedData.EncryptedKey, v.sealedData.Nonce); err != nil {
		return ErrInvalidPassword
	}

	// Generate new salt.
	newSalt := make([]byte, saltLen)
	if _, err := rand.Read(newSalt); err != nil {
		return fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive new encryption key.
	newEncKey := argon2.IDKey([]byte(newPassword), newSalt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(newEncKey)

	// Re-encrypt root key.
	encryptedKey, nonce, err := encrypt(newEncKey, v.rootKey)
	if err != nil {
		return fmt.Errorf("failed to encrypt root key: %w", err)
	}

	// Re-encrypt TOTP secret if enabled.
	if v.sealedData.TOTPEnabled && v.totpConfig != nil {
		encTOTP, totpNonce, err := encrypt(newEncKey, v.totpConfig.Secret)
		if err != nil {
			return fmt.Errorf("failed to encrypt TOTP secret: %w", err)
		}
		v.sealedData.TOTPSecret = encTOTP
		v.sealedData.TOTPNonce = totpNonce
	}

	v.sealedData.Salt = newSalt
	v.sealedData.EncryptedKey = encryptedKey
	v.sealedData.Nonce = nonce

	// Persist if we have a file path.
	if v.filePath != "" {
		data, err := json.MarshalIndent(v.sealedData, "", "  ")
		if err != nil {
			return fmt.Errorf("failed to marshal vault: %w", err)
		}
		if err := os.WriteFile(v.filePath, data, 0600); err != nil {
			return fmt.Errorf("failed to write vault file: %w", err)
		}
	}

	return nil
}

// --- Crypto helpers ---

func encrypt(key, plaintext []byte) (ciphertext, nonce []byte, err error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, nil, err
	}

	nonce = make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, err
	}

	ciphertext = aead.Seal(nil, nonce, plaintext, nil)
	return ciphertext, nonce, nil
}

func decrypt(key, ciphertext, nonce []byte) ([]byte, error) {
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

func zeroBytes(b []byte) {
	subtle.XORBytes(b, b, b)
}

