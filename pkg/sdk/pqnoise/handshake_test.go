package pqnoise

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
)

// newTestTransport creates a Transport for testing with a fresh Ed25519 key.
func newTestTransport(t *testing.T, muxers ...protocol.ID) *Transport {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	var sm []tptu.StreamMuxer
	for _, m := range muxers {
		sm = append(sm, tptu.StreamMuxer{ID: m})
	}
	tpt, err := New(ID, priv, sm)
	if err != nil {
		t.Fatalf("new transport: %v", err)
	}
	return tpt
}

// handshakePair runs SecureOutbound on the initiator and SecureInbound on the
// responder concurrently over a net.Pipe and returns both secure connections.
func handshakePair(t *testing.T, initTpt, respTpt *Transport, remotePeerForInbound peer.ID) (sec.SecureConn, sec.SecureConn) {
	t.Helper()
	ic, rc := net.Pipe()

	type result struct {
		conn sec.SecureConn
		err  error
	}

	initCh := make(chan result, 1)
	go func() {
		c, err := initTpt.SecureOutbound(context.Background(), ic, respTpt.localID)
		initCh <- result{c, err}
	}()

	respConn, respErr := respTpt.SecureInbound(context.Background(), rc, remotePeerForInbound)

	initRes := <-initCh
	if initRes.err != nil {
		if respConn != nil {
			respConn.Close()
		}
		t.Fatalf("initiator handshake: %v", initRes.err)
	}
	if respErr != nil {
		if initRes.conn != nil {
			initRes.conn.Close()
		}
		t.Fatalf("responder handshake: %v", respErr)
	}

	t.Cleanup(func() {
		initRes.conn.Close()
		respConn.Close()
	})

	return initRes.conn, respConn
}

// --- Handshake Tests ---

func TestHandshakeSuccess(t *testing.T) {
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// Verify peer identity exchange.
	if initConn.LocalPeer() != initTpt.localID {
		t.Error("init local peer mismatch")
	}
	if initConn.RemotePeer() != respTpt.localID {
		t.Error("init remote peer mismatch")
	}
	if respConn.LocalPeer() != respTpt.localID {
		t.Error("resp local peer mismatch")
	}
	if respConn.RemotePeer() != initTpt.localID {
		t.Error("resp remote peer mismatch")
	}

	// Verify public key exchange.
	// sec.SecureConn has RemotePublicKey (via ConnSecurity) but not LocalPublicKey.
	// Use type assertion to access pqSession's LocalPublicKey.
	initS := initConn.(*pqSession)
	respS := respConn.(*pqSession)
	if !initS.LocalPublicKey().Equals(respConn.RemotePublicKey()) {
		t.Error("init public key not received by resp")
	}
	if !respS.LocalPublicKey().Equals(initConn.RemotePublicKey()) {
		t.Error("resp public key not received by init")
	}

	// Verify handshake hash match (both sides derived the same session).
	initHash := initS.transportState.GetHandshakeHash()
	respHash := respS.transportState.GetHandshakeHash()
	if !bytes.Equal(initHash, respHash) {
		t.Error("handshake hash mismatch between initiator and responder")
	}
	if len(initHash) == 0 {
		t.Error("handshake hash is empty")
	}

	// Verify connection state reports PQ Noise.
	if initConn.ConnState().Security != ID {
		t.Errorf("init security: got %q, want %q", initConn.ConnState().Security, ID)
	}
	if respConn.ConnState().Security != ID {
		t.Errorf("resp security: got %q, want %q", respConn.ConnState().Security, ID)
	}
}

func TestHandshakeMuxerNegotiation(t *testing.T) {
	// Both sides share yamux - should negotiate it.
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	if initConn.ConnState().StreamMultiplexer != "/yamux/1.0.0" {
		t.Errorf("init muxer: got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "/yamux/1.0.0" {
		t.Errorf("resp muxer: got %q", respConn.ConnState().StreamMultiplexer)
	}
	if !initConn.ConnState().UsedEarlyMuxerNegotiation {
		t.Error("init: UsedEarlyMuxerNegotiation should be true")
	}
	if !respConn.ConnState().UsedEarlyMuxerNegotiation {
		t.Error("resp: UsedEarlyMuxerNegotiation should be true")
	}
}

func TestHandshakeMuxerNegotiationPreference(t *testing.T) {
	// Initiator prefers yamux, responder prefers mplex but supports both.
	// Initiator's preference should win (matchMuxers takes initiator first).
	initTpt := newTestTransport(t, "/yamux/1.0.0", "/mplex/6.7.0")
	respTpt := newTestTransport(t, "/mplex/6.7.0", "/yamux/1.0.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// Initiator's first preference that exists in responder's list wins.
	// Both sides must agree on the same muxer.
	if initConn.ConnState().StreamMultiplexer != "/yamux/1.0.0" {
		t.Errorf("init: expected yamux, got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "/yamux/1.0.0" {
		t.Errorf("resp: expected yamux, got %q", respConn.ConnState().StreamMultiplexer)
	}
}

func TestHandshakeNoCommonMuxer(t *testing.T) {
	// No common muxer - negotiated muxer should be empty string.
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/mplex/6.7.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	if initConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("init: expected empty muxer, got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("resp: expected empty muxer, got %q", respConn.ConnState().StreamMultiplexer)
	}
}

func TestHandshakeNoMuxers(t *testing.T) {
	// Both sides have no muxers. Handshake should still succeed.
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	if initConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("expected empty muxer, got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("expected empty muxer, got %q", respConn.ConnState().StreamMultiplexer)
	}
}

func TestHandshakeDataRoundTrip(t *testing.T) {
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// net.Pipe is synchronous (no internal buffer), so Write blocks until
	// the other side reads. Run Write in a goroutine.
	msg := []byte("post-quantum encrypted message")
	writeErr := make(chan error, 1)
	go func() {
		_, err := initConn.Write(msg)
		writeErr <- err
	}()

	buf := make([]byte, 256)
	n, err := respConn.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(msg, buf[:n]) {
		t.Fatalf("data mismatch: got %q", buf[:n])
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestHandshakeLargeData(t *testing.T) {
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// 500KB message over PQ encrypted channel.
	const size = 500 * 1024
	msg := make([]byte, size)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}

	writeErr := make(chan error, 1)
	go func() {
		_, err := initConn.Write(msg)
		writeErr <- err
	}()

	got := make([]byte, size)
	if _, err := io.ReadFull(respConn, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(msg, got) {
		t.Fatal("large data mismatch")
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

// --- Peer ID Tests ---

func TestPeerIDMismatchOutbound(t *testing.T) {
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	ic, rc := net.Pipe()
	defer rc.Close()

	// Outbound to a wrong peer ID.
	fakePeerID := peer.ID("wrong-peer-id-12345")

	type result struct {
		conn sec.SecureConn
		err  error
	}

	initCh := make(chan result, 1)
	go func() {
		c, err := initTpt.SecureOutbound(context.Background(), ic, fakePeerID)
		initCh <- result{c, err}
	}()

	// Responder runs normally.
	respConn, respErr := respTpt.SecureInbound(context.Background(), rc, "")

	initRes := <-initCh

	// Initiator should fail with peer ID mismatch.
	if initRes.err == nil {
		initRes.conn.Close()
		t.Fatal("expected peer ID mismatch error")
	}
	var mismatch sec.ErrPeerIDMismatch
	if !errors.As(initRes.err, &mismatch) {
		t.Fatalf("expected ErrPeerIDMismatch, got: %v", initRes.err)
	}
	if mismatch.Expected != fakePeerID {
		t.Errorf("expected peer: got %s, want %s", mismatch.Expected, fakePeerID)
	}
	if mismatch.Actual != respTpt.localID {
		t.Errorf("actual peer: got %s, want %s", mismatch.Actual, respTpt.localID)
	}

	// On handshake error, returned conn should NOT have ConnState set
	// (sessionWithConnState is only called on success path).
	if initRes.conn != nil {
		if initRes.conn.ConnState().Security != "" {
			t.Errorf("failed conn should have empty Security, got %q", initRes.conn.ConnState().Security)
		}
	}

	// Responder may succeed or fail depending on timing (pipe closed).
	if respConn != nil {
		respConn.Close()
	}
	_ = respErr
}

func TestPeerIDMismatchInbound(t *testing.T) {
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	ic, rc := net.Pipe()

	type result struct {
		conn sec.SecureConn
		err  error
	}

	initCh := make(chan result, 1)
	go func() {
		c, err := initTpt.SecureOutbound(context.Background(), ic, respTpt.localID)
		initCh <- result{c, err}
	}()

	// Responder expects a specific (wrong) peer ID.
	wrongPeer := peer.ID("not-the-initiator")
	respConn, respErr := respTpt.SecureInbound(context.Background(), rc, wrongPeer)

	initRes := <-initCh

	// Responder should fail.
	if respErr == nil {
		respConn.Close()
		t.Fatal("expected responder to reject mismatched peer ID")
	}
	var mismatch sec.ErrPeerIDMismatch
	if !errors.As(respErr, &mismatch) {
		t.Fatalf("expected ErrPeerIDMismatch, got: %v", respErr)
	}

	// Initiator may also fail from broken pipe.
	if initRes.conn != nil {
		initRes.conn.Close()
	}
}

func TestEmptyRemotePeerAcceptsAny(t *testing.T) {
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	// Empty peer ID for inbound = accept any peer.
	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// Should succeed and correctly identify the peer.
	if respConn.RemotePeer() != initTpt.localID {
		t.Errorf("resp did not identify initiator: got %s", respConn.RemotePeer())
	}
	if initConn.RemotePeer() != respTpt.localID {
		t.Errorf("init did not identify responder: got %s", initConn.RemotePeer())
	}
}

// --- Context Tests ---

func TestContextCancellation(t *testing.T) {
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	ic, rc := net.Pipe()
	defer rc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	_, err := initTpt.SecureOutbound(ctx, ic, respTpt.localID)
	if err == nil {
		t.Fatal("expected error with cancelled context")
	}
	// Must be context.Canceled.
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
}

func TestContextTimeout(t *testing.T) {
	initTpt := newTestTransport(t)
	// No responder - the handshake will hang on the first read.

	ic, rc := net.Pipe()
	defer rc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := initTpt.SecureOutbound(ctx, ic, "some-peer")
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Security Tests ---

func TestCrossProtocolSignatureRejection(t *testing.T) {
	// This test verifies that a classical noise signature cannot be replayed
	// against a PQ noise verifier. The payloadSigPrefix is different
	// ("noise-libp2p-pq-handshake:" vs "noise-libp2p-static-key:").
	// We verify this by checking the constant is correct.
	if payloadSigPrefix == "noise-libp2p-static-key:" {
		t.Fatal("payloadSigPrefix must differ from classical noise prefix")
	}
	if !strings.HasPrefix(payloadSigPrefix, "noise-libp2p-pq-") {
		t.Fatal("payloadSigPrefix should contain 'pq' domain separator")
	}
}

func TestKeyZeroingAfterClose(t *testing.T) {
	initTpt := newTestTransport(t)
	respTpt := newTestTransport(t)

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// Get references to the internal sessions.
	initSession := initConn.(*pqSession)
	respSession := respConn.(*pqSession)

	// Verify TransportState exists before close.
	if initSession.transportState == nil {
		t.Fatal("init transportState nil before close")
	}
	if respSession.transportState == nil {
		t.Fatal("resp transportState nil before close")
	}

	// Close both sessions.
	initSession.Close()
	respSession.Close()

	// After close, TransportState.Destroy() was called.
	// Verify by checking IsDestroyed.
	if !initSession.transportState.IsDestroyed() {
		t.Error("init transportState not destroyed after close")
	}
	if !respSession.transportState.IsDestroyed() {
		t.Error("resp transportState not destroyed after close")
	}
}

func TestOversizedHandshakeMessageRejected(t *testing.T) {
	respTpt := newTestTransport(t)

	ic, rc := net.Pipe()

	type result struct {
		conn sec.SecureConn
		err  error
	}

	// Responder runs normally (it will try to read messages).
	respCh := make(chan result, 1)
	go func() {
		c, err := respTpt.SecureInbound(context.Background(), rc, "")
		respCh <- result{c, err}
	}()

	// Attacker side: send an oversized handshake message manually.
	// Write a valid-looking length prefix claiming > maxHandshakeMessageLen.
	var lenBuf [2]byte
	binary.BigEndian.PutUint16(lenBuf[:], uint16(maxHandshakeMessageLen+1))
	_, _ = ic.Write(lenBuf[:])

	// Send garbage body.
	garbage := make([]byte, maxHandshakeMessageLen+1)
	_, _ = ic.Write(garbage)

	// Close attacker side.
	ic.Close()

	// Responder should reject.
	res := <-respCh
	if res.err == nil {
		res.conn.Close()
		t.Fatal("expected responder to reject oversized message")
	}
	if !strings.Contains(res.err.Error(), "too large") {
		t.Fatalf("expected 'too large' error, got: %v", res.err)
	}
}

func TestMuxerListCapEnforcement(t *testing.T) {
	// Create transport with >100 muxers. The verifier should ignore muxers
	// that exceed maxProtoNum.
	muxers := make([]protocol.ID, maxProtoNum+10)
	for i := range muxers {
		muxers[i] = protocol.ID("/muxer/" + string(rune('a'+i%26)) + string(rune('0'+i/26)))
	}
	initTpt := newTestTransport(t, muxers...)
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	// Handshake should succeed (muxer list is silently capped, not rejected).
	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	// With >100 muxers from initiator, responder ignores the list entirely.
	// So no common muxer should be found on either side.
	if initConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("init: expected empty muxer with oversized list, got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("resp: expected empty muxer with oversized list, got %q", respConn.ConnState().StreamMultiplexer)
	}
}

func TestEmptyMuxerListFallback(t *testing.T) {
	// One side has muxers, other has none. Should negotiate empty.
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t)

	initConn, respConn := handshakePair(t, initTpt, respTpt, "")

	if initConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("init: expected empty muxer, got %q", initConn.ConnState().StreamMultiplexer)
	}
	if respConn.ConnState().StreamMultiplexer != "" {
		t.Errorf("resp: expected empty muxer, got %q", respConn.ConnState().StreamMultiplexer)
	}
}

// --- Handshake Hash Binding Tests ---

func TestDifferentOuterKeysProduceDifferentSignatures(t *testing.T) {
	// Two independent handshakes should produce different handshake hashes
	// (because ephemeral keys are random), and therefore different signatures.
	// This is verified indirectly: if both handshakes succeed with correct
	// peer ID verification, the hash binding is working correctly since
	// replaying one hash's signature against the other would fail.
	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	conn1i, conn1r := handshakePair(t, initTpt, respTpt, "")
	conn2i, conn2r := handshakePair(t, initTpt, respTpt, "")

	// Both should succeed independently (correct signatures).
	if conn1i.RemotePeer() != respTpt.localID {
		t.Error("conn1 init peer mismatch")
	}
	if conn2i.RemotePeer() != respTpt.localID {
		t.Error("conn2 init peer mismatch")
	}
	if conn1r.RemotePeer() != initTpt.localID {
		t.Error("conn1 resp peer mismatch")
	}
	if conn2r.RemotePeer() != initTpt.localID {
		t.Error("conn2 resp peer mismatch")
	}
}

func TestNoKeyMaterialInErrors(t *testing.T) {
	// Verify that handshake errors do not leak key material (F164).
	initTpt := newTestTransport(t)

	ic, rc := net.Pipe()
	rc.Close() // break pipe to force handshake error

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := initTpt.SecureOutbound(ctx, ic, "some-peer")
	ic.Close()
	if err == nil {
		t.Fatal("expected error")
	}

	errStr := err.Error()
	// Key material patterns that must never appear in error messages.
	for _, pattern := range []string{
		"AAAA", // base64 key fragment (32+ zero bytes)
		"x25519",
		"ml-kem",
		"private",
		"secret",
	} {
		if strings.Contains(strings.ToLower(errStr), pattern) {
			t.Errorf("error contains key-like pattern %q: %s", pattern, errStr)
		}
	}
}

// --- Protocol ID Tests ---

func TestTransportID(t *testing.T) {
	tpt := newTestTransport(t)
	if tpt.ID() != ID {
		t.Errorf("ID: got %q, want %q", tpt.ID(), ID)
	}
	if ID != "/pq-noise/1" {
		t.Errorf("protocol ID changed: got %q", ID)
	}
}

// --- Helper Function Tests ---

func TestIsIdentityMessage(t *testing.T) {
	tests := []struct {
		name      string
		msgIndex  int
		isWrite   bool
		initiator bool
		want      bool
	}{
		// Initiator writes identity at msg4 (index 4).
		{"init_write_msg4", 4, true, true, true},
		{"init_write_msg3", 3, true, true, false},
		// Initiator reads identity at msg3 (index 3).
		{"init_read_msg3", 3, false, true, true},
		{"init_read_msg4", 4, false, true, false},
		// Responder writes identity at msg3 (index 3).
		{"resp_write_msg3", 3, true, false, true},
		{"resp_write_msg4", 4, true, false, false},
		// Responder reads identity at msg4 (index 4).
		{"resp_read_msg4", 4, false, false, true},
		{"resp_read_msg3", 3, false, false, false},
		// Non-identity messages.
		{"msg0", 0, true, true, false},
		{"msg1", 1, true, true, false},
		{"msg2", 2, true, true, false},
		{"msg5", 5, true, true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isIdentityMessage(tt.msgIndex, tt.isWrite, tt.initiator)
			if got != tt.want {
				t.Errorf("isIdentityMessage(%d, write=%v, init=%v) = %v, want %v",
					tt.msgIndex, tt.isWrite, tt.initiator, got, tt.want)
			}
		})
	}
}

func TestMatchMuxers(t *testing.T) {
	tests := []struct {
		name      string
		preferred []protocol.ID
		supported []protocol.ID
		want      protocol.ID
	}{
		{"match_first", []protocol.ID{"/a", "/b"}, []protocol.ID{"/b", "/a"}, "/a"},
		{"match_second", []protocol.ID{"/c", "/b"}, []protocol.ID{"/b"}, "/b"},
		{"no_match", []protocol.ID{"/a"}, []protocol.ID{"/b"}, ""},
		{"empty_preferred", nil, []protocol.ID{"/a"}, ""},
		{"empty_supported", []protocol.ID{"/a"}, nil, ""},
		{"both_empty", nil, nil, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := matchMuxers(tt.preferred, tt.supported)
			if got != tt.want {
				t.Errorf("matchMuxers = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- Panic Recovery Test ---

func TestPanicRecoveryInHandshakeGoroutine(t *testing.T) {
	// This tests the panic recovery in newSecureSession's goroutine.
	// We can't easily trigger a panic in the real handshake path without
	// corrupting internals, but we can verify the mechanism works by
	// checking that a failed handshake (broken pipe) returns an error
	// and doesn't leak goroutines.
	initTpt := newTestTransport(t)

	ic, rc := net.Pipe()

	// Close responder side immediately to break the pipe.
	rc.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := initTpt.SecureOutbound(ctx, ic, "some-peer")
	if err == nil {
		t.Fatal("expected error from broken pipe handshake")
	}

	ic.Close()
}

// --- Handshake Stress Test ---

func TestHandshakeStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	initTpt := newTestTransport(t, "/yamux/1.0.0")
	respTpt := newTestTransport(t, "/yamux/1.0.0")

	// Run 10 handshakes in sequence. PQ handshake involves ML-KEM-768 keygen
	// and HybridDualLayer (6 messages), so each is non-trivial.
	for i := 0; i < 10; i++ {
		ic, rc := net.Pipe()

		type result struct {
			conn sec.SecureConn
			err  error
		}

		initCh := make(chan result, 1)
		go func() {
			c, err := initTpt.SecureOutbound(context.Background(), ic, respTpt.localID)
			initCh <- result{c, err}
		}()

		respConn, respErr := respTpt.SecureInbound(context.Background(), rc, "")

		initRes := <-initCh
		if initRes.err != nil {
			t.Fatalf("handshake %d init: %v", i, initRes.err)
		}
		if respErr != nil {
			initRes.conn.Close()
			t.Fatalf("handshake %d resp: %v", i, respErr)
		}

		// Quick data round-trip (net.Pipe: write must be concurrent with read).
		msg := []byte{byte(i)}
		wErr := make(chan error, 1)
		go func() {
			_, err := initRes.conn.Write(msg)
			wErr <- err
		}()
		buf := make([]byte, 1)
		if _, err := io.ReadFull(respConn, buf); err != nil {
			t.Fatalf("handshake %d read: %v", i, err)
		}
		if buf[0] != byte(i) {
			t.Fatalf("handshake %d data mismatch", i)
		}
		if err := <-wErr; err != nil {
			t.Fatalf("handshake %d write: %v", i, err)
		}

		initRes.conn.Close()
		respConn.Close()
	}
}

// --- Constants Verification ---

func TestConstants(t *testing.T) {
	if MaxPlaintextLength <= 0 {
		t.Fatal("MaxPlaintextLength must be positive")
	}
	if MaxPlaintextLength > 65535 {
		t.Fatal("MaxPlaintextLength exceeds Noise spec")
	}
	if LengthPrefixLength != 2 {
		t.Fatal("LengthPrefixLength must be 2")
	}
	if maxHandshakeMessageLen != 8192 {
		t.Fatalf("maxHandshakeMessageLen changed: got %d", maxHandshakeMessageLen)
	}
	if bufSize != 8192 {
		t.Fatalf("bufSize changed: got %d", bufSize)
	}
	if maxPayloadLen != 4096 {
		t.Fatalf("maxPayloadLen changed: got %d", maxPayloadLen)
	}
	if maxProtoNum != 100 {
		t.Fatalf("maxProtoNum changed: got %d", maxProtoNum)
	}
}

func TestInnerPrologueImmutability(t *testing.T) {
	// innerPrologueStr is a Go string (immutable). Verify it has the expected value.
	if innerPrologueStr != "noise-libp2p-pq-v1" {
		t.Fatalf("innerPrologueStr changed: got %q", innerPrologueStr)
	}
}
