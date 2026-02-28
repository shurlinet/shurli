// Package macaroon implements HMAC-chain capability tokens for Shurli's
// access control system. Macaroons are bearer tokens that support
// offline attenuation: any holder can add caveats (restrictions) but
// nobody can remove them without the root key. This is enforced by the
// HMAC chain, not by trust.
//
// Reference: "Macaroons: Cookies with Contextual Caveats" (Birgisson et al.)
// Go reference: LND's macaroon implementation (same HMAC-SHA256 pattern).
package macaroon

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
)

var (
	ErrInvalidSignature = errors.New("macaroon signature verification failed")
	ErrCaveatFailed     = errors.New("caveat verification failed")
)

// Macaroon is an HMAC-chain capability token.
type Macaroon struct {
	Location  string   `json:"location"`            // identifies the target service
	ID        string   `json:"id"`                  // unique identifier for this token
	Caveats   []string `json:"caveats,omitempty"`   // first-party caveats (restrictions)
	Signature []byte   `json:"signature"`           // current HMAC signature
}

// CaveatVerifier validates a single caveat string.
// Returns nil if the caveat is satisfied, error otherwise.
type CaveatVerifier func(caveat string) error

// New creates a macaroon with the given root key, location, and identifier.
// The root key is used to compute the initial HMAC signature and must be
// kept secret by the issuer.
func New(location string, rootKey []byte, id string) *Macaroon {
	sig := computeHMAC(rootKey, []byte(id))
	return &Macaroon{
		Location:  location,
		ID:        id,
		Signature: sig,
	}
}

// AddFirstPartyCaveat adds a restriction to this macaroon.
// Each caveat chains a new HMAC on top of the previous signature,
// making removal cryptographically impossible without the prior signature.
// Returns the macaroon for chaining.
func (m *Macaroon) AddFirstPartyCaveat(predicate string) *Macaroon {
	m.Caveats = append(m.Caveats, predicate)
	m.Signature = computeHMAC(m.Signature, []byte(predicate))
	return m
}

// Verify checks that the macaroon's HMAC chain is valid and all caveats
// are satisfied by the verifier. The rootKey must be the same key used
// to create the macaroon.
func (m *Macaroon) Verify(rootKey []byte, verifier CaveatVerifier) error {
	// Recompute the HMAC chain from scratch.
	sig := computeHMAC(rootKey, []byte(m.ID))
	for _, caveat := range m.Caveats {
		sig = computeHMAC(sig, []byte(caveat))
	}

	// Constant-time signature comparison.
	if !hmac.Equal(sig, m.Signature) {
		return ErrInvalidSignature
	}

	// Verify each caveat.
	if verifier != nil {
		for _, caveat := range m.Caveats {
			if err := verifier(caveat); err != nil {
				return fmt.Errorf("%w: %s: %v", ErrCaveatFailed, caveat, err)
			}
		}
	}

	return nil
}

// Clone returns a deep copy of the macaroon. Use this before adding
// caveats to preserve the original for future delegations.
func (m *Macaroon) Clone() *Macaroon {
	caveats := make([]string, len(m.Caveats))
	copy(caveats, m.Caveats)
	sig := make([]byte, len(m.Signature))
	copy(sig, m.Signature)
	return &Macaroon{
		Location:  m.Location,
		ID:        m.ID,
		Caveats:   caveats,
		Signature: sig,
	}
}

// Encode serializes the macaroon to JSON bytes.
func (m *Macaroon) Encode() ([]byte, error) {
	return json.Marshal(m)
}

// Decode deserializes a macaroon from JSON bytes.
func Decode(data []byte) (*Macaroon, error) {
	var m Macaroon
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("failed to decode macaroon: %w", err)
	}
	return &m, nil
}

// EncodeBase64 serializes the macaroon to a URL-safe base64 string.
func (m *Macaroon) EncodeBase64() (string, error) {
	data, err := m.Encode()
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(data), nil
}

// DecodeBase64 deserializes a macaroon from a URL-safe base64 string.
func DecodeBase64(s string) (*Macaroon, error) {
	data, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("failed to decode base64: %w", err)
	}
	return Decode(data)
}

// computeHMAC returns HMAC-SHA256(key, data).
func computeHMAC(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
