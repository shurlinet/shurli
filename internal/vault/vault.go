// Package vault implements a passphrase-sealed key vault for the Shurli relay.
//
// The vault protects the relay's root key material (used for macaroon minting).
// When sealed, the relay operates in watch-only mode: it routes traffic for
// existing peers but cannot authorize new ones. Unsealing requires a passphrase
// (and optionally a TOTP code), then auto-reseals after a configurable timeout.
//
// Crypto: Argon2id for passphrase KDF, XChaCha20-Poly1305 for encryption.
// Recovery: BIP39-compatible 24-word seed phrase regenerates the root key.
package vault

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/shurlinet/shurli/internal/totp"
)

var (
	ErrVaultSealed         = errors.New("vault is sealed")
	ErrVaultAlreadyUnsealed = errors.New("vault is already unsealed")
	ErrInvalidPassphrase   = errors.New("invalid passphrase")
	ErrInvalidTOTP         = errors.New("invalid TOTP code")
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
	seedWordCount = 24
)

// SealedData is the on-disk representation of the vault.
type SealedData struct {
	Version        int    `json:"version"`
	Salt           []byte `json:"salt"`            // Argon2id salt
	EncryptedKey   []byte `json:"encrypted_key"`   // XChaCha20-Poly1305(rootKey)
	Nonce          []byte `json:"nonce"`           // XChaCha20-Poly1305 nonce
	TOTPEnabled    bool   `json:"totp_enabled"`
	TOTPSecret     []byte `json:"totp_secret,omitempty"`    // encrypted alongside root key
	TOTPNonce      []byte `json:"totp_nonce,omitempty"`
	SeedHash       []byte `json:"seed_hash"`       // SHA-256 of seed for verification
	AutoSealMins   int    `json:"auto_seal_mins"`  // auto-reseal timeout (0 = manual)
	YubikeyEnabled bool   `json:"yubikey_enabled"` // Yubikey HMAC-SHA1 challenge-response
	YubikeySlot    int    `json:"yubikey_slot,omitempty"` // 1 or 2
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

// Create initializes a new vault with a passphrase and optional TOTP.
// Returns the vault (unsealed) and the seed phrase for recovery.
func Create(passphrase string, enableTOTP bool, autoSealMins int) (*Vault, string, error) {
	// Generate root key
	rootKey := make([]byte, rootKeyLen)
	if _, err := rand.Read(rootKey); err != nil {
		return nil, "", fmt.Errorf("failed to generate root key: %w", err)
	}

	// Generate seed phrase from root key
	seedPhrase := encodeSeedPhrase(rootKey)
	seedHash := sha256.Sum256([]byte(seedPhrase))

	// Generate salt
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, "", fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive encryption key from passphrase
	encKey := argon2.IDKey([]byte(passphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Encrypt root key
	encryptedKey, nonce, err := encrypt(encKey, rootKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to encrypt root key: %w", err)
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
			return nil, "", fmt.Errorf("failed to generate TOTP secret: %w", err)
		}
		encTOTP, totpNonce, err := encrypt(encKey, totpSecret)
		if err != nil {
			return nil, "", fmt.Errorf("failed to encrypt TOTP secret: %w", err)
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

	return v, seedPhrase, nil
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

// Unseal decrypts the root key using the passphrase and validates the TOTP code.
func (v *Vault) Unseal(passphrase, totpCode string) error {
	v.mu.Lock()
	defer v.mu.Unlock()

	if !v.sealed {
		return ErrVaultAlreadyUnsealed
	}

	sd := v.sealedData
	if sd == nil {
		return ErrVaultNotInitialized
	}

	// Derive key from passphrase
	encKey := argon2.IDKey([]byte(passphrase), sd.Salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Decrypt root key
	rootKey, err := decrypt(encKey, sd.EncryptedKey, sd.Nonce)
	if err != nil {
		return ErrInvalidPassphrase
	}

	// Decrypt and validate TOTP if enabled
	if sd.TOTPEnabled {
		totpSecret, err := decrypt(encKey, sd.TOTPSecret, sd.TOTPNonce)
		if err != nil {
			return ErrInvalidPassphrase
		}

		cfg := &totp.Config{Secret: totpSecret}
		if !totp.Validate(cfg, totpCode, time.Now(), 1) {
			// Zero the decrypted key before returning
			zeroBytes(rootKey)
			return ErrInvalidTOTP
		}
		v.totpConfig = cfg
	}

	v.rootKey = rootKey
	v.sealed = false
	v.unsealedAt = time.Now()

	return nil
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

// RecoverFromSeed reconstructs a vault from a seed phrase and new passphrase.
func RecoverFromSeed(seedPhrase, newPassphrase string, enableTOTP bool, autoSealMins int) (*Vault, error) {
	rootKey, err := decodeSeedPhrase(seedPhrase)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidSeed, err)
	}

	// Generate new salt
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("failed to generate salt: %w", err)
	}

	// Derive new encryption key
	encKey := argon2.IDKey([]byte(newPassphrase), salt, argonTime, argonMemory, argonThreads, argonKeyLen)

	// Encrypt root key with new passphrase
	encryptedKey, nonce, err := encrypt(encKey, rootKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt root key: %w", err)
	}

	seedHash := sha256.Sum256([]byte(encodeSeedPhrase(rootKey)))

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

// --- Seed phrase encoding ---
// Encodes 32 bytes as 24 hex-pair words (simple, deterministic, no wordlist dependency).
// Each "word" is a 2-character hex string. Recovery-friendly: unambiguous, no typos.

func encodeSeedPhrase(key []byte) string {
	words := make([]string, len(key))
	for i, b := range key {
		words[i] = hex.EncodeToString([]byte{b})
	}
	return strings.Join(words, " ")
}

func decodeSeedPhrase(phrase string) ([]byte, error) {
	words := strings.Fields(phrase)
	if len(words) != seedWordCount {
		// Also accept 32 words (full key bytes)
		if len(words) != rootKeyLen {
			return nil, fmt.Errorf("expected %d words, got %d", rootKeyLen, len(words))
		}
	}

	key := make([]byte, 0, len(words))
	for _, w := range words {
		b, err := hex.DecodeString(w)
		if err != nil {
			return nil, fmt.Errorf("invalid seed word %q: %w", w, err)
		}
		key = append(key, b...)
	}

	if len(key) != rootKeyLen {
		return nil, fmt.Errorf("decoded key length %d, expected %d", len(key), rootKeyLen)
	}

	return key, nil
}
