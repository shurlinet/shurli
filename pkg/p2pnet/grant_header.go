// Grant header: binary token presentation on plugin stream open.
//
// Every plugin stream (opened via OpenPluginStream / handled by plugin services)
// begins with a grant header. The header carries an optional macaroon capability
// token that the receiving node verifies cryptographically.
//
// Wire format:
//
//	Byte 0:    version (0x01)
//	Byte 1:    flags   (0x01 = has token, 0x00 = no token)
//	Bytes 2-3: token length (uint16 big-endian, max 8192). Zero when no token.
//	Bytes 4-N: base64-encoded macaroon token (only if flags & 0x01)
//
// 4 bytes overhead when no token. Binary because this is every stream open.
package p2pnet

import (
	"encoding/binary"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

const (
	grantHeaderVersion  = 0x01
	grantFlagHasToken   = 0x01
	grantFlagNoToken    = 0x00
	grantHeaderSize     = 4 // version(1) + flags(1) + length(2)
	grantMaxTokenLen    = 8192
	grantHeaderTimeout  = 2 * time.Second
)

// WriteGrantHeader writes a grant token header to the stream.
// If tokenBase64 is empty, writes a 4-byte "no token" header.
// Returns an error if the token exceeds the max length.
// Sets a 2-second write deadline to prevent blocking on a stuck remote peer,
// then clears the deadline so the rest of the stream is unaffected.
func WriteGrantHeader(s network.Stream, tokenBase64 string) error {
	if len(tokenBase64) > grantMaxTokenLen {
		return fmt.Errorf("grant token too large: %d > %d", len(tokenBase64), grantMaxTokenLen)
	}

	s.SetWriteDeadline(time.Now().Add(grantHeaderTimeout))
	defer s.SetWriteDeadline(time.Time{})

	if tokenBase64 == "" {
		// Fast path: no token. Stack-allocated 4-byte header, no heap allocation.
		header := [grantHeaderSize]byte{grantHeaderVersion, grantFlagNoToken, 0, 0}
		_, err := s.Write(header[:])
		if err != nil {
			return fmt.Errorf("write grant header: %w", err)
		}
		return nil
	}

	// Token path: single buffer with header + token for atomic write.
	buf := make([]byte, grantHeaderSize+len(tokenBase64))
	buf[0] = grantHeaderVersion
	buf[1] = grantFlagHasToken
	binary.BigEndian.PutUint16(buf[2:], uint16(len(tokenBase64)))
	copy(buf[grantHeaderSize:], tokenBase64)

	if _, err := s.Write(buf); err != nil {
		return fmt.Errorf("write grant header: %w", err)
	}
	return nil
}

// ReadGrantHeader reads a grant token header from the stream.
// Returns the base64-encoded token, or empty string if no token was presented.
// Sets a 2-second deadline for the header read to prevent slowloris attacks,
// then clears the deadline so the rest of the stream is unaffected.
func ReadGrantHeader(s network.Stream) (string, error) {
	// Set deadline for header read only.
	s.SetReadDeadline(time.Now().Add(grantHeaderTimeout))
	defer s.SetReadDeadline(time.Time{}) // clear after header

	var header [grantHeaderSize]byte
	if _, err := io.ReadFull(s, header[:]); err != nil {
		return "", fmt.Errorf("read grant header: %w", err)
	}

	if header[0] != grantHeaderVersion {
		return "", fmt.Errorf("unsupported grant header version: 0x%02x", header[0])
	}

	tokenLen := binary.BigEndian.Uint16(header[2:])

	if header[1] == grantFlagNoToken {
		if tokenLen != 0 {
			return "", fmt.Errorf("grant header: no-token flag but length=%d", tokenLen)
		}
		return "", nil
	}

	if header[1] != grantFlagHasToken {
		return "", fmt.Errorf("grant header: unknown flags: 0x%02x", header[1])
	}

	if tokenLen == 0 || tokenLen > grantMaxTokenLen {
		return "", fmt.Errorf("grant header: invalid token length: %d", tokenLen)
	}

	tokenBuf := make([]byte, tokenLen)
	if _, err := io.ReadFull(s, tokenBuf); err != nil {
		return "", fmt.Errorf("read grant token: %w", err)
	}

	return string(tokenBuf), nil
}
