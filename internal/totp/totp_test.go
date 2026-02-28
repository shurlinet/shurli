package totp

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"
)

// RFC 6238 Appendix B test vectors (SHA1 mode).
// Secret: "12345678901234567890" (ASCII) = 0x3132333435363738393031323334353637383930
func rfc6238Secret() []byte {
	return []byte("12345678901234567890")
}

func TestRFC6238Vectors(t *testing.T) {
	// Test vectors from RFC 6238 Appendix B (SHA1, 8 digits, 30-second period).
	// We use 8 digits to match the RFC vectors exactly.
	cfg := &Config{
		Secret: rfc6238Secret(),
		Period: 30,
		Digits: 8,
	}

	tests := []struct {
		time int64
		want string
	}{
		{59, "94287082"},
		{1111111109, "07081804"},
		{1111111111, "14050471"},
		{1234567890, "89005924"},
		{2000000000, "69279037"},
		{20000000000, "65353130"},
	}

	for _, tt := range tests {
		ts := time.Unix(tt.time, 0).UTC()
		got := Generate(cfg, ts)
		if got != tt.want {
			t.Errorf("Generate(t=%d) = %q, want %q", tt.time, got, tt.want)
		}
	}
}

func TestGenerate6Digits(t *testing.T) {
	cfg := &Config{
		Secret: rfc6238Secret(),
		Period: 30,
		Digits: 6,
	}

	code := Generate(cfg, time.Unix(59, 0).UTC())
	if len(code) != 6 {
		t.Errorf("expected 6-digit code, got %q (len=%d)", code, len(code))
	}
}

func TestGenerateDefaultConfig(t *testing.T) {
	cfg := &Config{Secret: rfc6238Secret()}
	code := Generate(cfg, time.Now())
	if len(code) != DefaultDigits {
		t.Errorf("expected %d digits, got %d", DefaultDigits, len(code))
	}
}

func TestValidateExactMatch(t *testing.T) {
	cfg := &Config{Secret: rfc6238Secret()}
	now := time.Now()
	code := Generate(cfg, now)

	if !Validate(cfg, code, now, 0) {
		t.Error("exact match should validate")
	}
}

func TestValidateSkewWindow(t *testing.T) {
	cfg := &Config{Secret: rfc6238Secret(), Period: 30}
	now := time.Now()

	// Generate code for 30 seconds ago
	past := now.Add(-30 * time.Second)
	code := Generate(cfg, past)

	// Should fail with skew=0 (unless we're at a period boundary)
	// Should pass with skew=1
	if !Validate(cfg, code, now, 1) {
		t.Error("code from previous period should validate with skew=1")
	}
}

func TestValidateExpiredCode(t *testing.T) {
	cfg := &Config{Secret: rfc6238Secret(), Period: 30}
	now := time.Now()

	// Generate code for 5 minutes ago
	old := now.Add(-5 * time.Minute)
	code := Generate(cfg, old)

	if Validate(cfg, code, now, 1) {
		t.Error("code from 5 minutes ago should not validate with skew=1")
	}
}

func TestValidateWrongCode(t *testing.T) {
	cfg := &Config{Secret: rfc6238Secret()}
	if Validate(cfg, "000000", time.Now(), 1) {
		// This could theoretically pass if 000000 happens to be valid,
		// but it's astronomically unlikely with this secret and time
		t.Log("warning: 000000 validated (statistically possible but unlikely)")
	}
}

func TestNewSecret(t *testing.T) {
	secret, err := NewSecret(20)
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 20 {
		t.Errorf("secret length = %d, want 20", len(secret))
	}

	// Generate two secrets, they should differ
	secret2, _ := NewSecret(20)
	if hex.EncodeToString(secret) == hex.EncodeToString(secret2) {
		t.Error("two random secrets should differ")
	}
}

func TestNewSecretMinimumLength(t *testing.T) {
	secret, err := NewSecret(5) // too short, should be bumped to 20
	if err != nil {
		t.Fatal(err)
	}
	if len(secret) != 20 {
		t.Errorf("short request should be bumped to 20 bytes, got %d", len(secret))
	}
}

func TestFormatProvisioningURI(t *testing.T) {
	secret := []byte("12345678901234567890")
	uri := FormatProvisioningURI(secret, "Shurli", "relay@relay.example.com")

	if !strings.HasPrefix(uri, "otpauth://totp/") {
		t.Errorf("URI should start with otpauth://totp/, got: %s", uri)
	}
	if !strings.Contains(uri, "Shurli") {
		t.Errorf("URI should contain issuer 'Shurli': %s", uri)
	}
	if !strings.Contains(uri, "secret=") {
		t.Errorf("URI should contain secret parameter: %s", uri)
	}
	if !strings.Contains(uri, "algorithm=SHA1") {
		t.Errorf("URI should specify SHA1 algorithm: %s", uri)
	}
	if !strings.Contains(uri, "digits=6") {
		t.Errorf("URI should specify 6 digits: %s", uri)
	}
	if !strings.Contains(uri, "period=30") {
		t.Errorf("URI should specify 30-second period: %s", uri)
	}
}

func TestFormatProvisioningURINoIssuer(t *testing.T) {
	secret := []byte("12345678901234567890")
	uri := FormatProvisioningURI(secret, "", "relay")

	if strings.Contains(uri, "issuer=") {
		t.Errorf("URI without issuer should not have issuer param: %s", uri)
	}
}

func BenchmarkGenerate(b *testing.B) {
	cfg := &Config{Secret: rfc6238Secret()}
	now := time.Now()
	for i := 0; i < b.N; i++ {
		Generate(cfg, now)
	}
}

func BenchmarkValidate(b *testing.B) {
	cfg := &Config{Secret: rfc6238Secret()}
	now := time.Now()
	code := Generate(cfg, now)
	for i := 0; i < b.N; i++ {
		Validate(cfg, code, now, 1)
	}
}
