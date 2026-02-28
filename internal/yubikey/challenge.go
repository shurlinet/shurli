// Package yubikey provides HMAC-SHA1 challenge-response authentication
// using YubiKeys via the ykman CLI tool.
//
// This is an optional 2FA method for relay vault unseal. It requires
// ykman to be installed and a YubiKey with an HMAC-SHA1 slot configured.
// Uses exec.Command (no C HID bindings), following the same zero-dep
// pattern as the QR generation package.
package yubikey

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

var (
	ErrYkmanNotFound    = errors.New("ykman not found: install with 'pip install yubikey-manager' or system package")
	ErrNoYubikey        = errors.New("no YubiKey detected")
	ErrChallengeTimeout = errors.New("challenge-response timed out (touch required?)")
	ErrSlotNotConfigured = errors.New("HMAC-SHA1 slot not configured")
)

// Slot represents a YubiKey HMAC-SHA1 slot (1 or 2).
type Slot int

const (
	Slot1 Slot = 1
	Slot2 Slot = 2
)

// DefaultTimeout for ykman operations. Touch-required keys need time
// for the user to physically touch the key.
const DefaultTimeout = 15 * time.Second

// IsAvailable checks if ykman is installed and a YubiKey is connected.
func IsAvailable() bool {
	return YkmanInstalled() && YubikeyConnected()
}

// YkmanInstalled checks if the ykman CLI tool is on PATH.
func YkmanInstalled() bool {
	_, err := exec.LookPath("ykman")
	return err == nil
}

// YubikeyConnected checks if a YubiKey is physically connected.
func YubikeyConnected() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ykman", "info")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false
	}
	// ykman info outputs device info when a key is connected
	return len(out) > 0 && !strings.Contains(string(out), "No YubiKey detected")
}

// ChallengeResponse sends a challenge to the YubiKey and returns the
// HMAC-SHA1 response. The challenge is a hex-encoded string.
// This may require the user to touch the YubiKey (depending on slot config).
func ChallengeResponse(slot Slot, challenge []byte) ([]byte, error) {
	if !YkmanInstalled() {
		return nil, ErrYkmanNotFound
	}

	challengeHex := fmt.Sprintf("%x", challenge)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ykman", "otp", "calculate",
		fmt.Sprintf("%d", slot), challengeHex)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, ErrChallengeTimeout
	}
	if err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if strings.Contains(errMsg, "not configured") || strings.Contains(errMsg, "not programmed") {
			return nil, ErrSlotNotConfigured
		}
		if strings.Contains(errMsg, "No YubiKey") {
			return nil, ErrNoYubikey
		}
		return nil, fmt.Errorf("ykman challenge-response failed: %s", errMsg)
	}

	// Parse hex response
	respHex := strings.TrimSpace(stdout.String())
	resp, err := parseHexResponse(respHex)
	if err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return resp, nil
}

// ListSlots returns which HMAC-SHA1 slots are configured on the YubiKey.
func ListSlots() (slot1Configured, slot2Configured bool, err error) {
	if !YkmanInstalled() {
		return false, false, ErrYkmanNotFound
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "ykman", "otp", "info")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return false, false, fmt.Errorf("ykman otp info failed: %w", err)
	}

	output := string(out)
	// Parse ykman otp info output for HMAC-SHA1 slots
	lines := strings.Split(output, "\n")
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "slot 1") && strings.Contains(lower, "hmac-sha1") {
			slot1Configured = true
		}
		if strings.Contains(lower, "slot 2") && strings.Contains(lower, "hmac-sha1") {
			slot2Configured = true
		}
	}

	return slot1Configured, slot2Configured, nil
}

func parseHexResponse(s string) ([]byte, error) {
	// Remove any non-hex characters (spaces, colons)
	clean := strings.NewReplacer(" ", "", ":", "", "\n", "", "\r", "").Replace(s)
	if len(clean) == 0 {
		return nil, fmt.Errorf("empty response")
	}

	b := make([]byte, len(clean)/2)
	for i := 0; i < len(clean); i += 2 {
		if i+2 > len(clean) {
			return nil, fmt.Errorf("odd-length hex string")
		}
		var v byte
		for j := 0; j < 2; j++ {
			c := clean[i+j]
			switch {
			case c >= '0' && c <= '9':
				v = v*16 + (c - '0')
			case c >= 'a' && c <= 'f':
				v = v*16 + (c - 'a' + 10)
			case c >= 'A' && c <= 'F':
				v = v*16 + (c - 'A' + 10)
			default:
				return nil, fmt.Errorf("invalid hex character: %c", c)
			}
		}
		b[i/2] = v
	}
	return b, nil
}
