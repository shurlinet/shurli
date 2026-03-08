package invite

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"math/big"
	"strings"
)

// TokenSize is the byte length of an invite token.
const TokenSize = 10

// InviteData holds the payload encoded in an invite code.
type InviteData struct {
	Token []byte // invite token (10 bytes)
}

// GenerateToken creates a cryptographically random 10-byte token.
func GenerateToken() ([]byte, error) {
	token := make([]byte, TokenSize)
	_, err := rand.Read(token)
	return token, err
}

// --- short invite code (base36, token-only) ---

const (
	// shortCodeLen is the number of base36 characters in a short code.
	shortCodeLen = 16

	// base36 alphabet: 0-9, A-Z (uppercase).
	base36Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
)

var base36Base = big.NewInt(36)

// Encode encodes a raw token (10 bytes, 80 bits) into an invite code.
// Format: KXMT-9FWR-PBLZ-4YAN (16 base36 chars, ~82.7 bits capacity).
func Encode(token []byte) (string, error) {
	if len(token) != TokenSize {
		return "", fmt.Errorf("token must be %d bytes, got %d", TokenSize, len(token))
	}

	// Interpret token bytes as a big-endian unsigned integer.
	n := new(big.Int).SetBytes(token)

	// Encode to base36, left-padded to shortCodeLen characters.
	chars := make([]byte, shortCodeLen)
	for i := shortCodeLen - 1; i >= 0; i-- {
		var rem big.Int
		n.DivMod(n, base36Base, &rem)
		chars[i] = base36Alphabet[rem.Int64()]
	}

	// Group with dashes every 4 characters.
	var groups []string
	for i := 0; i < shortCodeLen; i += 4 {
		end := i + 4
		if end > shortCodeLen {
			end = shortCodeLen
		}
		groups = append(groups, string(chars[i:end]))
	}
	return strings.Join(groups, "-"), nil
}

// Decode parses a dash-separated invite code back into InviteData.
func Decode(code string) (*InviteData, error) {
	clean := strings.ReplaceAll(code, "-", "")
	clean = strings.ReplaceAll(clean, " ", "")
	clean = strings.ToUpper(clean)

	// Expect exactly 16 base36 chars (A-Z, 0-9).
	if len(clean) != shortCodeLen || !isBase36(clean) {
		return nil, fmt.Errorf("invalid invite code: expected %d base36 characters", shortCodeLen)
	}

	// Decode base36 -> big.Int -> bytes.
	n := new(big.Int)
	for _, c := range clean {
		idx := strings.IndexRune(base36Alphabet, c)
		if idx < 0 {
			return nil, fmt.Errorf("invalid character in invite code: %c", c)
		}
		n.Mul(n, base36Base)
		n.Add(n, big.NewInt(int64(idx)))
	}

	// Convert to fixed-size byte slice (10 bytes).
	raw := n.Bytes()
	if len(raw) > TokenSize {
		return nil, fmt.Errorf("invite code value too large")
	}

	token := make([]byte, TokenSize)
	copy(token[TokenSize-len(raw):], raw) // right-align (big-endian)

	return &InviteData{
		Token: token,
	}, nil
}

// isBase36 returns true if s contains only base36 characters (0-9, A-Z).
func isBase36(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z')) {
			return false
		}
	}
	return true
}

// strictMultihashLen verifies that buf is exactly one multihash with no
// trailing bytes. A multihash is: <varint-code><varint-length><digest>.
// We read both varints, then check that code+length+digest consume the
// entire buffer with nothing left over.
//
// This replaces the github.com/multiformats/go-multihash dependency.
// The multihash wire format is defined at https://multiformats.io/multihash/
// Original library: MIT License, Copyright (c) 2014 Juan Batiz-Benet.
// See THIRD_PARTY_NOTICES in the repo root.
func strictMultihashLen(buf []byte) error {
	if len(buf) < 2 {
		return fmt.Errorf("multihash too short: %d bytes", len(buf))
	}

	// Read the hash function code varint.
	_, n1 := binary.Uvarint(buf)
	if n1 <= 0 {
		return fmt.Errorf("multihash: invalid code varint")
	}

	// Read the digest length varint.
	digestLen, n2 := binary.Uvarint(buf[n1:])
	if n2 <= 0 {
		return fmt.Errorf("multihash: invalid length varint")
	}

	// The total consumed bytes must equal the buffer length exactly.
	expected := n1 + n2 + int(digestLen)
	if len(buf) != expected {
		return fmt.Errorf("multihash has %d trailing bytes", len(buf)-expected)
	}
	return nil
}
