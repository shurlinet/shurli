package identity

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// SHRL encrypted identity format constants.
var shrlMagic = []byte("SHRL")

const (
	shrlVersion  = 1
	shrlSaltLen  = 16
	shrlNonceLen = 24 // XChaCha20-Poly1305

	// Argon2id parameters (same as vault for consistency).
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MB in KiB
	argonThreads = 4
	argonKeyLen  = 32
)

// SHRL format offsets.
const (
	shrlMagicOff = 0
	shrlMagicLen = 4
	shrlVerOff   = 4
	shrlVerLen   = 1
	shrlSaltOff  = 5
	shrlNonceOff = 5 + shrlSaltLen          // 21
	shrlDataOff  = 5 + shrlSaltLen + shrlNonceLen // 45
)

var (
	ErrNotEncrypted    = errors.New("identity key is not SHRL-encrypted; run 'shurli recover --seed \"...\"' to create a password-protected key")
	ErrWrongPassword   = errors.New("wrong password")
	ErrUnsupportedVer  = errors.New("unsupported SHRL version")
)

// IsEncrypted checks if the data has the SHRL magic header.
func IsEncrypted(data []byte) bool {
	return len(data) >= shrlDataOff && bytes.Equal(data[:shrlMagicLen], shrlMagic)
}

// EncryptKey encrypts a libp2p private key with a password using the SHRL format.
// Format: [SHRL][version:1][salt:16][nonce:24][ciphertext...]
func EncryptKey(privKey libp2pcrypto.PrivKey, password string) ([]byte, error) {
	// Marshal the private key.
	raw, err := libp2pcrypto.MarshalPrivateKey(privKey)
	if err != nil {
		return nil, fmt.Errorf("marshalling private key: %w", err)
	}
	defer zeroBytes(raw)

	// Generate salt.
	salt := make([]byte, shrlSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("generating salt: %w", err)
	}

	// Derive encryption key.
	encKey := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(encKey)

	// Encrypt with XChaCha20-Poly1305.
	aead, err := chacha20poly1305.NewX(encKey)
	if err != nil {
		return nil, fmt.Errorf("creating AEAD: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, raw, nil)

	// Build SHRL frame.
	out := make([]byte, 0, shrlDataOff+len(ciphertext))
	out = append(out, shrlMagic...)
	out = append(out, shrlVersion)
	out = append(out, salt...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	return out, nil
}

// DecryptKey decrypts a SHRL-format encrypted identity key.
func DecryptKey(data []byte, password string) (libp2pcrypto.PrivKey, error) {
	if !IsEncrypted(data) {
		return nil, ErrNotEncrypted
	}

	ver := data[shrlVerOff]
	if ver != shrlVersion {
		return nil, fmt.Errorf("%w: got %d, expected %d", ErrUnsupportedVer, ver, shrlVersion)
	}

	salt := data[shrlSaltOff : shrlSaltOff+shrlSaltLen]
	nonce := data[shrlNonceOff : shrlNonceOff+shrlNonceLen]
	ciphertext := data[shrlDataOff:]

	// Derive key from password.
	encKey := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	defer zeroBytes(encKey)

	// Decrypt.
	aead, err := chacha20poly1305.NewX(encKey)
	if err != nil {
		return nil, fmt.Errorf("creating AEAD: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, ErrWrongPassword
	}
	defer zeroBytes(plaintext)

	// Unmarshal the private key.
	privKey, err := libp2pcrypto.UnmarshalPrivateKey(plaintext)
	if err != nil {
		return nil, fmt.Errorf("unmarshalling decrypted key: %w", err)
	}

	return privKey, nil
}

// ChangeKeyPassword re-encrypts an identity.key file with a new password.
// Uses atomic write (temp file + rename) so a crash mid-write cannot
// leave the key file in a corrupted state.
func ChangeKeyPassword(path, oldPassword, newPassword string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading key file: %w", err)
	}

	privKey, err := DecryptKey(data, oldPassword)
	if err != nil {
		return err
	}

	newData, err := EncryptKey(privKey, newPassword)
	if err != nil {
		return fmt.Errorf("re-encrypting: %w", err)
	}

	// Atomic write: write to temp file in same directory, then rename.
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".identity-key-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.Write(newData); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("writing temp key file: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return fmt.Errorf("setting temp file permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("renaming temp to key file: %w", err)
	}

	return nil
}

// zeroBytes securely zeroes a byte slice.
func zeroBytes(b []byte) {
	subtle.XORBytes(b, b, b)
}
