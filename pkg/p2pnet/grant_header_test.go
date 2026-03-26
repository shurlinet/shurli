package p2pnet

import (
	"bytes"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// mockStream implements network.Stream with a bytes.Buffer for testing.
// Only Read, Write, and SetReadDeadline are functional.
type mockStream struct {
	buf           bytes.Buffer
	readDeadline  time.Time
	writeDeadline time.Time
}

func (m *mockStream) Read(p []byte) (int, error)  { return m.buf.Read(p) }
func (m *mockStream) Write(p []byte) (int, error)  { return m.buf.Write(p) }
func (m *mockStream) Close() error                  { return nil }
func (m *mockStream) CloseWrite() error              { return nil }
func (m *mockStream) CloseRead() error               { return nil }
func (m *mockStream) Reset() error                              { return nil }
func (m *mockStream) ResetWithError(network.StreamErrorCode) error { return nil }
func (m *mockStream) SetDeadline(t time.Time) error  { return nil }
func (m *mockStream) SetReadDeadline(t time.Time) error {
	m.readDeadline = t
	return nil
}
func (m *mockStream) SetWriteDeadline(t time.Time) error {
	m.writeDeadline = t
	return nil
}
func (m *mockStream) ID() string                      { return "test" }
func (m *mockStream) Protocol() protocol.ID            { return "/test/1.0.0" }
func (m *mockStream) SetProtocol(protocol.ID) error    { return nil }
func (m *mockStream) Stat() network.Stats              { return network.Stats{} }
func (m *mockStream) Conn() network.Conn               { return nil }
func (m *mockStream) Scope() network.StreamScope        { return nil }

func TestGrantHeaderRoundTripWithToken(t *testing.T) {
	token := "dGVzdC10b2tlbi1iYXNlNjQtZW5jb2RlZC1tYWNhcm9vbg=="
	s := &mockStream{}

	if err := WriteGrantHeader(s, token); err != nil {
		t.Fatalf("WriteGrantHeader: %v", err)
	}

	got, err := ReadGrantHeader(s)
	if err != nil {
		t.Fatalf("ReadGrantHeader: %v", err)
	}
	if got != token {
		t.Errorf("token mismatch: got %q, want %q", got, token)
	}
}

func TestGrantHeaderRoundTripNoToken(t *testing.T) {
	s := &mockStream{}

	if err := WriteGrantHeader(s, ""); err != nil {
		t.Fatalf("WriteGrantHeader: %v", err)
	}

	// Verify exactly 4 bytes written.
	if s.buf.Len() != grantHeaderSize {
		t.Errorf("expected %d bytes for no-token header, got %d", grantHeaderSize, s.buf.Len())
	}

	got, err := ReadGrantHeader(s)
	if err != nil {
		t.Fatalf("ReadGrantHeader: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty token, got %q", got)
	}
}

func TestGrantHeaderOversizedTokenRejected(t *testing.T) {
	s := &mockStream{}
	bigToken := strings.Repeat("A", grantMaxTokenLen+1)

	err := WriteGrantHeader(s, bigToken)
	if err == nil {
		t.Fatal("expected error for oversized token")
	}
	if !strings.Contains(err.Error(), "too large") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGrantHeaderMaxTokenAccepted(t *testing.T) {
	s := &mockStream{}
	maxToken := strings.Repeat("A", grantMaxTokenLen)

	if err := WriteGrantHeader(s, maxToken); err != nil {
		t.Fatalf("WriteGrantHeader with max token: %v", err)
	}

	got, err := ReadGrantHeader(s)
	if err != nil {
		t.Fatalf("ReadGrantHeader with max token: %v", err)
	}
	if got != maxToken {
		t.Errorf("max token round-trip failed: lengths %d vs %d", len(got), len(maxToken))
	}
}

func TestGrantHeaderBadVersion(t *testing.T) {
	s := &mockStream{}
	// Write a header with bad version byte.
	var header [grantHeaderSize]byte
	header[0] = 0xFF // bad version
	header[1] = grantFlagNoToken
	s.buf.Write(header[:])

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for bad version")
	}
	if !strings.Contains(err.Error(), "unsupported grant header version") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGrantHeaderTruncated(t *testing.T) {
	s := &mockStream{}
	// Write only 2 of the 4 header bytes.
	s.buf.Write([]byte{grantHeaderVersion, grantFlagNoToken})

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for truncated header")
	}
}

func TestGrantHeaderTruncatedToken(t *testing.T) {
	s := &mockStream{}
	// Write header claiming 100 bytes of token, but only provide 10.
	var header [grantHeaderSize]byte
	header[0] = grantHeaderVersion
	header[1] = grantFlagHasToken
	binary.BigEndian.PutUint16(header[2:], 100)
	s.buf.Write(header[:])
	s.buf.Write([]byte("0123456789")) // only 10 bytes

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for truncated token payload")
	}
}

func TestGrantHeaderNoTokenFlagWithNonZeroLength(t *testing.T) {
	s := &mockStream{}
	var header [grantHeaderSize]byte
	header[0] = grantHeaderVersion
	header[1] = grantFlagNoToken
	binary.BigEndian.PutUint16(header[2:], 42) // inconsistent: no-token but length=42
	s.buf.Write(header[:])

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for no-token flag with non-zero length")
	}
}

func TestGrantHeaderHasTokenFlagWithZeroLength(t *testing.T) {
	s := &mockStream{}
	var header [grantHeaderSize]byte
	header[0] = grantHeaderVersion
	header[1] = grantFlagHasToken
	binary.BigEndian.PutUint16(header[2:], 0) // inconsistent: has-token but length=0
	s.buf.Write(header[:])

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for has-token flag with zero length")
	}
}

func TestGrantHeaderUnknownFlags(t *testing.T) {
	s := &mockStream{}
	var header [grantHeaderSize]byte
	header[0] = grantHeaderVersion
	header[1] = 0xAB // unknown flags
	s.buf.Write(header[:])

	_, err := ReadGrantHeader(s)
	if err == nil {
		t.Fatal("expected error for unknown flags")
	}
	if !strings.Contains(err.Error(), "unknown flags") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestGrantHeaderReadDeadlineCleared(t *testing.T) {
	s := &mockStream{}
	WriteGrantHeader(s, "")

	// After ReadGrantHeader, the read deadline should be cleared (zero time).
	ReadGrantHeader(s)

	if !s.readDeadline.IsZero() {
		t.Errorf("expected read deadline to be cleared after ReadGrantHeader, got %v", s.readDeadline)
	}
}

func TestGrantHeaderWriteDeadlineCleared(t *testing.T) {
	s := &mockStream{}
	WriteGrantHeader(s, "test-token")

	// After WriteGrantHeader, the write deadline should be cleared (zero time).
	if !s.writeDeadline.IsZero() {
		t.Errorf("expected write deadline to be cleared after WriteGrantHeader, got %v", s.writeDeadline)
	}
}
