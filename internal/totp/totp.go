// Package totp implements RFC 6238 Time-Based One-Time Passwords.
// Zero external dependencies beyond Go stdlib.
package totp

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

// DefaultPeriod is the time step in seconds (RFC 6238 default).
const DefaultPeriod = 30

// DefaultDigits is the number of digits in the OTP (RFC 6238 default).
const DefaultDigits = 6

// Config holds TOTP generation parameters.
type Config struct {
	Secret []byte // shared secret (minimum 20 bytes recommended)
	Period int    // time step in seconds (default: 30)
	Digits int    // number of digits (default: 6)
}

// defaults returns a config with zero values replaced by defaults.
func (c *Config) defaults() (period, digits int) {
	period = c.Period
	if period <= 0 {
		period = DefaultPeriod
	}
	digits = c.Digits
	if digits <= 0 {
		digits = DefaultDigits
	}
	return period, digits
}

// Generate produces a TOTP code for the given time.
// Implements RFC 6238 with HMAC-SHA1.
func Generate(cfg *Config, t time.Time) string {
	period, digits := cfg.defaults()

	// Calculate time counter (T = floor(Unix / period))
	counter := uint64(t.Unix()) / uint64(period)

	// HOTP(K, C) per RFC 4226
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, counter)

	mac := hmac.New(sha1.New, cfg.Secret)
	mac.Write(buf)
	hash := mac.Sum(nil)

	// Dynamic truncation
	offset := hash[len(hash)-1] & 0x0F
	code := binary.BigEndian.Uint32(hash[offset:offset+4]) & 0x7FFFFFFF

	// Modulo to get the right number of digits
	mod := uint32(math.Pow10(digits))
	otp := code % mod

	return fmt.Sprintf("%0*d", digits, otp)
}

// Validate checks a TOTP code against the current time with a +/- skew window.
// skew=0 means exact match only. skew=1 means accept t-period, t, t+period.
func Validate(cfg *Config, code string, t time.Time, skew int) bool {
	period, _ := cfg.defaults()

	for i := -skew; i <= skew; i++ {
		check := t.Add(time.Duration(i*period) * time.Second)
		if Generate(cfg, check) == code {
			return true
		}
	}
	return false
}

// NewSecret generates a cryptographically random secret of the given byte length.
// RFC 4226 recommends at least 20 bytes (160 bits) for HMAC-SHA1.
func NewSecret(length int) ([]byte, error) {
	if length < 16 {
		length = 20
	}
	secret := make([]byte, length)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("failed to generate TOTP secret: %w", err)
	}
	return secret, nil
}

// FormatProvisioningURI generates an otpauth:// URI for QR code provisioning.
// Compatible with Google Authenticator, Authy, and other TOTP apps.
func FormatProvisioningURI(secret []byte, issuer, account string) string {
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	encoded = strings.ToUpper(encoded)

	label := account
	if issuer != "" {
		label = issuer + ":" + account
	}

	u := url.URL{
		Scheme: "otpauth",
		Host:   "totp",
		Path:   "/" + label,
	}
	q := u.Query()
	q.Set("secret", encoded)
	q.Set("algorithm", "SHA1")
	q.Set("digits", "6")
	q.Set("period", "30")
	if issuer != "" {
		q.Set("issuer", issuer)
	}
	u.RawQuery = q.Encode()

	return u.String()
}
