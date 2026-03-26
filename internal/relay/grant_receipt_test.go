package relay

import (
	"bytes"
	"crypto/rand"
	"testing"
	"time"
)

// testHMACKey generates a random 32-byte HMAC key for testing.
func testHMACKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestEncodeDecodeGrantReceipt_RoundTrip(t *testing.T) {
	key := testHMACKey(t)

	grantDuration := 2 * time.Hour
	sessionDataLimit := int64(2 * 1024 * 1024 * 1024) // 2GB
	sessionDuration := 10 * time.Minute
	issuedAt := time.Now().Truncate(time.Second) // Unix seconds precision

	data := EncodeGrantReceipt(grantDuration, sessionDataLimit, sessionDuration, false, issuedAt, key)

	if len(data) != GrantReceiptSize {
		t.Fatalf("encoded size = %d, want %d", len(data), GrantReceiptSize)
	}

	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if r.GrantDuration != grantDuration {
		t.Errorf("GrantDuration = %v, want %v", r.GrantDuration, grantDuration)
	}
	if r.SessionDataLimit != sessionDataLimit {
		t.Errorf("SessionDataLimit = %d, want %d", r.SessionDataLimit, sessionDataLimit)
	}
	if r.SessionDuration != sessionDuration {
		t.Errorf("SessionDuration = %v, want %v", r.SessionDuration, sessionDuration)
	}
	if r.Permanent {
		t.Error("Permanent = true, want false")
	}
	if !r.IssuedAt.Equal(issuedAt) {
		t.Errorf("IssuedAt = %v, want %v", r.IssuedAt, issuedAt)
	}
}

func TestEncodeDecodeGrantReceipt_Permanent(t *testing.T) {
	key := testHMACKey(t)

	// Permanent grant: duration field should be 0, permanent flag set.
	// Session limits still present (H14).
	sessionDataLimit := int64(64 * 1024 * 1024) // 64MB
	sessionDuration := 10 * time.Minute
	issuedAt := time.Now().Truncate(time.Second)

	data := EncodeGrantReceipt(0, sessionDataLimit, sessionDuration, true, issuedAt, key)

	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if r.GrantDuration != 0 {
		t.Errorf("GrantDuration = %v, want 0 for permanent", r.GrantDuration)
	}
	if !r.Permanent {
		t.Error("Permanent = false, want true")
	}
	if r.SessionDataLimit != sessionDataLimit {
		t.Errorf("SessionDataLimit = %d, want %d (H14: permanent grants carry session limits)", r.SessionDataLimit, sessionDataLimit)
	}
	if r.SessionDuration != sessionDuration {
		t.Errorf("SessionDuration = %v, want %v", r.SessionDuration, sessionDuration)
	}
}

func TestEncodeDecodeGrantReceipt_UnlimitedSession(t *testing.T) {
	key := testHMACKey(t)

	data := EncodeGrantReceipt(4*time.Hour, 0, 2*time.Hour, false, time.Now(), key)

	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if r.SessionDataLimit != 0 {
		t.Errorf("SessionDataLimit = %d, want 0 (unlimited)", r.SessionDataLimit)
	}
}

func TestEncodeDecodeGrantReceipt_NegativeDataLimitClamped(t *testing.T) {
	key := testHMACKey(t)

	// Negative sessionDataLimit should be clamped to 0 (F7: prevent uint64 wrap).
	data := EncodeGrantReceipt(time.Hour, -1, 10*time.Minute, false, time.Now(), key)

	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if r.SessionDataLimit != 0 {
		t.Errorf("SessionDataLimit = %d, want 0 (negative input clamped)", r.SessionDataLimit)
	}
}

func TestEncodeDecodeGrantReceipt_HMACRoundTrip(t *testing.T) {
	key := testHMACKey(t)
	issuedAt := time.Now().Truncate(time.Second)

	data := EncodeGrantReceipt(time.Hour, 1<<30, 10*time.Minute, false, issuedAt, key)

	// Decode and verify HMAC field is populated (non-zero).
	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	var zeroHMAC [32]byte
	if r.HMAC == zeroHMAC {
		t.Error("decoded HMAC is all zeros")
	}

	// Verify the HMAC matches.
	if !VerifyGrantReceipt(data, key) {
		t.Error("HMAC verification failed on freshly encoded receipt")
	}
}

func TestVerifyGrantReceipt_Valid(t *testing.T) {
	key := testHMACKey(t)

	data := EncodeGrantReceipt(time.Hour, 1024*1024*1024, 10*time.Minute, false, time.Now(), key)

	if !VerifyGrantReceipt(data, key) {
		t.Error("valid receipt failed verification")
	}
}

func TestVerifyGrantReceipt_TamperedPayload(t *testing.T) {
	key := testHMACKey(t)

	data := EncodeGrantReceipt(time.Hour, 1024*1024*1024, 10*time.Minute, false, time.Now(), key)

	// Tamper with session_data_limit field (byte 10).
	data[10] ^= 0xFF

	if VerifyGrantReceipt(data, key) {
		t.Error("tampered receipt passed verification")
	}
}

func TestVerifyGrantReceipt_TamperedHMAC(t *testing.T) {
	key := testHMACKey(t)

	data := EncodeGrantReceipt(time.Hour, 1024*1024*1024, 10*time.Minute, false, time.Now(), key)

	// Tamper with HMAC field directly.
	data[GrantReceiptPayloadSize] ^= 0xFF

	if VerifyGrantReceipt(data, key) {
		t.Error("receipt with tampered HMAC passed verification")
	}
}

func TestVerifyGrantReceipt_WrongKey(t *testing.T) {
	key1 := testHMACKey(t)
	key2 := testHMACKey(t)

	data := EncodeGrantReceipt(time.Hour, 1024*1024*1024, 10*time.Minute, false, time.Now(), key1)

	if VerifyGrantReceipt(data, key2) {
		t.Error("receipt signed with key1 verified with key2")
	}
}

func TestDecodeGrantReceipt_InvalidSize(t *testing.T) {
	if _, err := DecodeGrantReceipt([]byte{0x01, 0x02, 0x03}); err == nil {
		t.Error("expected error for short data")
	}
	if _, err := DecodeGrantReceipt(make([]byte, GrantReceiptSize+1)); err == nil {
		t.Error("expected error for oversized data")
	}
}

func TestDecodeGrantReceipt_InvalidVersion(t *testing.T) {
	data := make([]byte, GrantReceiptSize)
	data[0] = 0xFF // bad version

	if _, err := DecodeGrantReceipt(data); err == nil {
		t.Error("expected error for bad version")
	}
}

func TestVerifyGrantReceipt_WrongSize(t *testing.T) {
	if VerifyGrantReceipt([]byte{0x01}, nil) {
		t.Error("expected false for wrong size")
	}
}

func TestEncodeGrantReceipt_Deterministic(t *testing.T) {
	key := testHMACKey(t)

	issuedAt := time.Unix(1700000000, 0)
	d1 := EncodeGrantReceipt(time.Hour, 1024, 10*time.Minute, false, issuedAt, key)
	d2 := EncodeGrantReceipt(time.Hour, 1024, 10*time.Minute, false, issuedAt, key)

	if !bytes.Equal(d1, d2) {
		t.Error("same inputs produced different outputs")
	}
}

func TestDecodeGrantReceipt_DurationOverflowCapped(t *testing.T) {
	// Craft a receipt with max uint64 duration to verify overflow protection.
	data := make([]byte, GrantReceiptSize)
	data[0] = grantReceiptVersion
	// Set grant_duration_secs to MaxUint64.
	for i := 1; i <= 8; i++ {
		data[i] = 0xFF
	}

	r, err := DecodeGrantReceipt(data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	// Duration must be positive (capped, not wrapped negative).
	if r.GrantDuration < 0 {
		t.Errorf("GrantDuration = %v, expected positive (overflow should be capped)", r.GrantDuration)
	}
}

func TestEncodeGrantReceipt_DifferentInputsDifferentOutput(t *testing.T) {
	key := testHMACKey(t)
	issuedAt := time.Unix(1700000000, 0)

	d1 := EncodeGrantReceipt(time.Hour, 1024, 10*time.Minute, false, issuedAt, key)
	d2 := EncodeGrantReceipt(2*time.Hour, 1024, 10*time.Minute, false, issuedAt, key)

	if bytes.Equal(d1, d2) {
		t.Error("different grant durations produced identical output")
	}
}
