package grants

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// computeFileHMAC computes HMAC-SHA256 over a version counter and data payload.
// Used by Store, Pouch, and DeliveryQueue for file integrity verification.
func computeFileHMAC(key []byte, version uint64, data []byte) string {
	versionBytes := []byte(fmt.Sprintf("v%d", version))
	mac := hmac.New(sha256.New, key)
	mac.Write(versionBytes)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyFileHMAC checks a file's HMAC against the expected value.
// Returns nil if valid, error if tampered or missing.
// When key is set and the file has entries, a valid HMAC is required (rejects unsigned files).
func verifyFileHMAC(key []byte, fileHMAC string, version uint64, data []byte, hasEntries bool) error {
	if len(key) == 0 {
		return nil // no key, skip verification
	}
	if fileHMAC == "" && hasEntries {
		return fmt.Errorf("file has no HMAC signature (possible tampering)")
	}
	if fileHMAC == "" {
		return nil // empty file, no HMAC needed
	}

	expected := computeFileHMAC(key, version, data)
	if !hmac.Equal([]byte(fileHMAC), []byte(expected)) {
		return fmt.Errorf("HMAC verification failed (possible tampering)")
	}
	return nil
}
