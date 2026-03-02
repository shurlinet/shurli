package identity

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const (
	sessionFile       = ".session"
	sessionHeaderLen  = 32 // install_time_random
	sessionNonceLen   = 24 // XChaCha20-Poly1305
	sessionVersion    = 1
	sessionMagic      = "SHRS" // Shurli Session
	sessionMagicLen   = 4
	sessionVersionLen = 1
	sessionDataOff    = sessionMagicLen + sessionVersionLen + sessionHeaderLen + sessionNonceLen // 61
)

// SessionPath returns the path to the .session file in the given config directory.
func SessionPath(configDir string) string {
	return filepath.Join(configDir, sessionFile)
}

// CreateSession creates a new session token that stores the password-derived
// decryption key, encrypted with a machine-bound key.
// The session enables auto-start without password on the same machine.
func CreateSession(configDir, password string) error {
	machineID, err := getMachineID()
	if err != nil {
		return fmt.Errorf("getting machine ID: %w", err)
	}

	// Generate install-time random.
	installRandom := make([]byte, sessionHeaderLen)
	if _, err := rand.Read(installRandom); err != nil {
		return fmt.Errorf("generating session random: %w", err)
	}

	// Derive machine-bound key.
	mbKey, err := deriveMachineKey(installRandom, machineID)
	if err != nil {
		return err
	}
	defer zeroBytes(mbKey)

	// Encrypt the password with the machine-bound key.
	aead, err := chacha20poly1305.NewX(mbKey)
	if err != nil {
		return fmt.Errorf("creating session AEAD: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return fmt.Errorf("generating session nonce: %w", err)
	}

	ciphertext := aead.Seal(nil, nonce, []byte(password), nil)

	// Build session file: [SHRS][version:1][installRandom:32][nonce:24][ciphertext...]
	out := make([]byte, 0, sessionDataOff+len(ciphertext))
	out = append(out, []byte(sessionMagic)...)
	out = append(out, sessionVersion)
	out = append(out, installRandom...)
	out = append(out, nonce...)
	out = append(out, ciphertext...)

	path := SessionPath(configDir)
	return os.WriteFile(path, out, 0600)
}

// LoadSession loads the session token and returns the stored password.
// Returns ("", nil) if no session file exists.
// Returns ("", error) if session exists but is invalid or machine binding fails.
func LoadSession(configDir string) (string, error) {
	path := SessionPath(configDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading session: %w", err)
	}

	if len(data) < sessionDataOff {
		return "", fmt.Errorf("session file too short")
	}

	if string(data[:sessionMagicLen]) != sessionMagic {
		return "", fmt.Errorf("invalid session magic")
	}

	if data[sessionMagicLen] != sessionVersion {
		return "", fmt.Errorf("unsupported session version %d", data[sessionMagicLen])
	}

	installRandom := data[sessionMagicLen+sessionVersionLen : sessionMagicLen+sessionVersionLen+sessionHeaderLen]
	nonce := data[sessionMagicLen+sessionVersionLen+sessionHeaderLen : sessionDataOff]
	ciphertext := data[sessionDataOff:]

	machineID, err := getMachineID()
	if err != nil {
		return "", fmt.Errorf("getting machine ID: %w", err)
	}

	mbKey, err := deriveMachineKey(installRandom, machineID)
	if err != nil {
		return "", err
	}
	defer zeroBytes(mbKey)

	aead, err := chacha20poly1305.NewX(mbKey)
	if err != nil {
		return "", fmt.Errorf("creating session AEAD: %w", err)
	}

	plaintext, err := aead.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("session token invalid (wrong machine or corrupted)")
	}

	return string(plaintext), nil
}

// DestroySession deletes the session file.
func DestroySession(configDir string) error {
	path := SessionPath(configDir)
	err := os.Remove(path)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// RefreshSession rotates the session token with fresh crypto material
// but the same password. Like changing your system password periodically.
func RefreshSession(configDir, password string) error {
	return CreateSession(configDir, password)
}

// SessionExists checks if a session file exists.
func SessionExists(configDir string) bool {
	_, err := os.Stat(SessionPath(configDir))
	return err == nil
}

// deriveMachineKey derives a machine-bound encryption key using HKDF.
func deriveMachineKey(installRandom, machineID []byte) ([]byte, error) {
	hkdfReader := hkdf.New(sha256.New, installRandom, machineID, []byte("shurli/session/v1"))
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, key); err != nil {
		return nil, fmt.Errorf("deriving machine key: %w", err)
	}
	return key, nil
}

// getMachineID returns a machine-specific identifier.
// On Linux: /etc/machine-id
// On macOS: IOPlatformUUID via ioreg
// Falls back to a hash of hostname if neither is available.
func getMachineID() ([]byte, error) {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/etc/machine-id")
		if err == nil && len(data) > 0 {
			h := sha256.Sum256(data)
			return h[:], nil
		}
		// Fallback: try /var/lib/dbus/machine-id.
		data, err = os.ReadFile("/var/lib/dbus/machine-id")
		if err == nil && len(data) > 0 {
			h := sha256.Sum256(data)
			return h[:], nil
		}
	case "darwin":
		// Get IOPlatformUUID via ioreg (the authoritative source on macOS).
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err == nil {
			// Extract IOPlatformUUID from output.
			for _, line := range bytes.Split(out, []byte("\n")) {
				if bytes.Contains(line, []byte("IOPlatformUUID")) {
					// Line format: "IOPlatformUUID" = "XXXXXXXX-XXXX-..."
					parts := bytes.SplitN(line, []byte("="), 2)
					if len(parts) == 2 {
						uuid := bytes.TrimSpace(parts[1])
						uuid = bytes.Trim(uuid, "\"")
						if len(uuid) > 0 {
							h := sha256.Sum256(uuid)
							return h[:], nil
						}
					}
				}
			}
		}
	}

	// Fallback: hostname hash. Less unique but works everywhere.
	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("cannot determine machine ID: %w", err)
	}
	h := sha256.Sum256([]byte("shurli-machine:" + hostname))
	return h[:], nil
}
