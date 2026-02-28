package macaroon

import (
	"crypto/rand"
	"errors"
	"strconv"
	"testing"
)

func genRootKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return key
}

func TestNewAndVerify(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")

	if err := m.Verify(key, nil); err != nil {
		t.Fatalf("verify failed: %v", err)
	}
}

func TestVerifyWrongKey(t *testing.T) {
	key := genRootKey(t)
	wrongKey := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")

	err := m.Verify(wrongKey, nil)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature, got: %v", err)
	}
}

func TestAddCaveatAndVerify(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy")
	m.AddFirstPartyCaveat("action=connect")

	if err := m.Verify(key, nil); err != nil {
		t.Fatalf("verify with caveats failed: %v", err)
	}
}

func TestTamperedCaveat(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy")

	// Tamper: change the caveat after signing
	m.Caveats[0] = "service=admin"

	err := m.Verify(key, nil)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for tampered caveat, got: %v", err)
	}
}

func TestRemovedCaveat(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy")
	m.AddFirstPartyCaveat("action=connect")

	// Tamper: remove a caveat
	m.Caveats = m.Caveats[1:]

	err := m.Verify(key, nil)
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("expected ErrInvalidSignature for removed caveat, got: %v", err)
	}
}

func TestCaveatVerifierAccepts(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy,ping")

	verifier := func(caveat string) error {
		k, v, _ := ParseCaveat(caveat)
		if k == "service" && v == "proxy,ping" {
			return nil
		}
		return errors.New("rejected")
	}

	if err := m.Verify(key, verifier); err != nil {
		t.Fatalf("verify with passing verifier failed: %v", err)
	}
}

func TestCaveatVerifierRejects(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy")

	verifier := func(caveat string) error {
		return errors.New("always reject")
	}

	err := m.Verify(key, verifier)
	if !errors.Is(err, ErrCaveatFailed) {
		t.Fatalf("expected ErrCaveatFailed, got: %v", err)
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("service=proxy")
	m.AddFirstPartyCaveat("expires=2026-12-31T00:00:00Z")

	data, err := m.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	decoded, err := Decode(data)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if err := decoded.Verify(key, nil); err != nil {
		t.Fatalf("decoded macaroon failed verification: %v", err)
	}

	if decoded.Location != m.Location {
		t.Errorf("location = %q, want %q", decoded.Location, m.Location)
	}
	if decoded.ID != m.ID {
		t.Errorf("id = %q, want %q", decoded.ID, m.ID)
	}
	if len(decoded.Caveats) != 2 {
		t.Errorf("caveats count = %d, want 2", len(decoded.Caveats))
	}
}

func TestEncodeDecodeBase64RoundTrip(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "invite-001")
	m.AddFirstPartyCaveat("action=invite")

	encoded, err := m.EncodeBase64()
	if err != nil {
		t.Fatalf("encode base64: %v", err)
	}

	decoded, err := DecodeBase64(encoded)
	if err != nil {
		t.Fatalf("decode base64: %v", err)
	}

	if err := decoded.Verify(key, nil); err != nil {
		t.Fatalf("decoded base64 macaroon failed verification: %v", err)
	}
}

func TestAttenuationOnlyRestricts(t *testing.T) {
	key := genRootKey(t)

	// Admin creates a token with action=invite,connect,admin
	admin := New("relay.example.com", key, "token-001")
	admin.AddFirstPartyCaveat("action=invite,connect,admin")

	// Member attenuates to action=connect only
	member := admin.Clone()
	member.AddFirstPartyCaveat("action=connect")

	// Both should verify against the root key
	if err := admin.Verify(key, nil); err != nil {
		t.Fatalf("admin verify: %v", err)
	}
	if err := member.Verify(key, nil); err != nil {
		t.Fatalf("member verify: %v", err)
	}

	// Member token has more caveats (more restricted)
	if len(member.Caveats) <= len(admin.Caveats) {
		t.Error("attenuated token should have more caveats")
	}
}

func TestCloneIndependence(t *testing.T) {
	key := genRootKey(t)
	m := New("relay.example.com", key, "token-001")
	m.AddFirstPartyCaveat("service=proxy")

	cloned := m.Clone()
	cloned.AddFirstPartyCaveat("expires=2026-12-31T00:00:00Z")

	// Original should still have only 1 caveat
	if len(m.Caveats) != 1 {
		t.Errorf("original caveats = %d, want 1", len(m.Caveats))
	}
	if len(cloned.Caveats) != 2 {
		t.Errorf("cloned caveats = %d, want 2", len(cloned.Caveats))
	}

	// Both should verify
	if err := m.Verify(key, nil); err != nil {
		t.Fatalf("original verify: %v", err)
	}
	if err := cloned.Verify(key, nil); err != nil {
		t.Fatalf("cloned verify: %v", err)
	}
}

func TestDecodeInvalid(t *testing.T) {
	_, err := Decode([]byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON")
	}

	_, err = DecodeBase64("not-base64!!!")
	if err == nil {
		t.Error("expected error for invalid base64")
	}
}

func BenchmarkHMACChain(b *testing.B) {
	key := make([]byte, 32)
	rand.Read(key)

	for _, n := range []int{1, 5, 10, 20} {
		name := "caveats=" + strconv.Itoa(n)
		b.Run(name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				m := New("bench", key, "id")
				for j := 0; j < n; j++ {
					m.AddFirstPartyCaveat("caveat=" + strconv.Itoa(j))
				}
				m.Verify(key, nil)
			}
		})
	}
}
