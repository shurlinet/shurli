package invite

import (
	"strings"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	code, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	t.Logf("invite code (%d chars): %s", len(code), code)

	decoded, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if len(decoded.Token) != TokenSize {
		t.Fatalf("Token length = %d, want %d", len(decoded.Token), TokenSize)
	}
	for i := 0; i < TokenSize; i++ {
		if decoded.Token[i] != token[i] {
			t.Errorf("Token[%d] = %d, want %d", i, decoded.Token[i], token[i])
		}
	}
}

func TestGenerateTokenUniqueness(t *testing.T) {
	t1, _ := GenerateToken()
	t2, _ := GenerateToken()
	if string(t1) == string(t2) {
		t.Error("two tokens should be different")
	}
}

func TestDecodeInvalid(t *testing.T) {
	_, err := Decode("not-a-valid-code")
	if err == nil {
		t.Error("expected error for invalid code")
	}

	_, err = Decode("")
	if err == nil {
		t.Error("expected error for empty code")
	}
}

func TestDecodeRejectsLowercase(t *testing.T) {
	// Lowercase should be normalized (Decode uppercases)
	token, _ := GenerateToken()
	code, _ := Encode(token)
	lower := strings.ToLower(code)

	decoded, err := Decode(lower)
	if err != nil {
		t.Fatalf("Decode lowercase: %v", err)
	}
	for i := 0; i < TokenSize; i++ {
		if decoded.Token[i] != token[i] {
			t.Errorf("Token[%d] mismatch after lowercase decode", i)
		}
	}
}

func TestDecodeWithSpaces(t *testing.T) {
	token, _ := GenerateToken()
	code, _ := Encode(token)

	// Add spaces instead of dashes
	spaced := strings.ReplaceAll(code, "-", " ")
	decoded, err := Decode(spaced)
	if err != nil {
		t.Fatalf("Decode with spaces: %v", err)
	}
	for i := 0; i < TokenSize; i++ {
		if decoded.Token[i] != token[i] {
			t.Errorf("Token[%d] mismatch after spaced decode", i)
		}
	}
}

func TestDecodeRejectsJunk(t *testing.T) {
	// Too long
	_, err := Decode("KXMT-9FWR-PBLZ-4YAN-EXTRA")
	if err == nil {
		t.Error("should reject code with extra chars")
	}

	// Too short
	_, err = Decode("KXMT-9FWR")
	if err == nil {
		t.Error("should reject short code")
	}

	// Non-base36 characters
	_, err = Decode("KXMT-9FWR-PBLZ-4Y!N")
	if err == nil {
		t.Error("should reject non-base36 chars")
	}
}

func TestEncodeRejectsWrongSize(t *testing.T) {
	_, err := Encode([]byte("short"))
	if err == nil {
		t.Error("should reject non-10-byte token")
	}

	_, err = Encode(make([]byte, 16))
	if err == nil {
		t.Error("should reject 16-byte token")
	}
}

func TestEncodeZeroToken(t *testing.T) {
	token := make([]byte, TokenSize)
	code, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode zero token: %v", err)
	}

	decoded, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode zero token: %v", err)
	}

	for i, b := range decoded.Token {
		if b != 0 {
			t.Errorf("Token[%d] = %d, want 0", i, b)
		}
	}
}

func TestEncodeMaxToken(t *testing.T) {
	token := make([]byte, TokenSize)
	for i := range token {
		token[i] = 0xFF
	}

	code, err := Encode(token)
	if err != nil {
		t.Fatalf("Encode max token: %v", err)
	}

	decoded, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode max token: %v", err)
	}

	for i, b := range decoded.Token {
		if b != 0xFF {
			t.Errorf("Token[%d] = %d, want 0xFF", i, b)
		}
	}
}
