package pqnoise

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/go-clatter"
	clattercipher "github.com/shurlinet/go-clatter/crypto/cipher"
	clatterdh "github.com/shurlinet/go-clatter/crypto/dh"
	clatterhash "github.com/shurlinet/go-clatter/crypto/hash"
)

// makeTransportStatePair runs a minimal NQ NN handshake between two in-memory
// pipes and returns paired TransportState instances for data-path testing.
// This is the only way to get TransportState (no public constructor).
func makeTransportStatePair(t *testing.T) (initiatorTS, responderTS *clatter.TransportState, initiatorConn, responderConn net.Conn) {
	t.Helper()

	dh := clatterdh.NewX25519()
	suite := clatter.CipherSuite{
		DH:     dh,
		Cipher: clattercipher.NewChaChaPoly(),
		Hash:   clatterhash.NewSha256(),
	}

	initHS, err := clatter.NewNqHandshake(clatter.PatternNN, true, suite)
	if err != nil {
		t.Fatalf("init handshake: %v", err)
	}
	respHS, err := clatter.NewNqHandshake(clatter.PatternNN, false, suite)
	if err != nil {
		initHS.Destroy()
		t.Fatalf("resp handshake: %v", err)
	}

	// NN pattern: 2 messages (initiator writes, responder reads, responder writes, initiator reads).
	buf := make([]byte, 65535)

	// Message 1: initiator -> responder
	n, err := initHS.WriteMessage(nil, buf)
	if err != nil {
		t.Fatalf("init write msg1: %v", err)
	}
	if _, err := respHS.ReadMessage(buf[:n], buf); err != nil {
		t.Fatalf("resp read msg1: %v", err)
	}

	// Message 2: responder -> initiator
	n, err = respHS.WriteMessage(nil, buf)
	if err != nil {
		t.Fatalf("resp write msg2: %v", err)
	}
	if _, err := initHS.ReadMessage(buf[:n], buf); err != nil {
		t.Fatalf("init read msg2: %v", err)
	}

	if !initHS.IsFinished() || !respHS.IsFinished() {
		t.Fatal("handshake not finished after 2 messages")
	}

	its, err := initHS.Finalize()
	if err != nil {
		t.Fatalf("init finalize: %v", err)
	}
	rts, err := respHS.Finalize()
	if err != nil {
		its.Destroy()
		t.Fatalf("resp finalize: %v", err)
	}

	ic, rc := net.Pipe()
	return its, rts, ic, rc
}

// makeSessionPair creates a pair of pqSession instances backed by paired
// TransportStates and net.Pipe connections.
func makeSessionPair(t *testing.T) (initiator, responder *pqSession) {
	t.Helper()

	initTS, respTS, ic, rc := makeTransportStatePair(t)

	initPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("gen init key: %v", err)
	}
	respPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("gen resp key: %v", err)
	}
	initID, err := peer.IDFromPrivateKey(initPriv)
	if err != nil {
		t.Fatalf("init peer ID: %v", err)
	}
	respID, err := peer.IDFromPrivateKey(respPriv)
	if err != nil {
		t.Fatalf("resp peer ID: %v", err)
	}

	initSession := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
		localID:        initID,
		localKey:       initPriv,
		remoteID:       respID,
		remoteKey:      respPriv.GetPublic(),
		transportState: initTS,
	}
	respSession := &pqSession{
		insecureConn:   rc,
		insecureReader: bufio.NewReader(rc),
		localID:        respID,
		localKey:       respPriv,
		remoteID:       initID,
		remoteKey:      initPriv.GetPublic(),
		transportState: respTS,
	}

	t.Cleanup(func() {
		initSession.Close()
		respSession.Close()
	})

	return initSession, respSession
}

// writeAsync writes data in a goroutine and returns a channel for the error.
// net.Pipe is synchronous (no internal buffer), so Write blocks until the
// other side reads. All Write calls must be concurrent with Read.
func writeAsync(s *pqSession, data []byte) <-chan error {
	ch := make(chan error, 1)
	go func() {
		_, err := s.Write(data)
		ch <- err
	}()
	return ch
}

func TestReadWriteRoundTrip(t *testing.T) {
	initS, respS := makeSessionPair(t)

	msg := []byte("hello post-quantum world")
	wErr := writeAsync(initS, msg)

	buf := make([]byte, 256)
	n, err := respS.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(msg, buf[:n]) {
		t.Fatalf("mismatch: got %q, want %q", buf[:n], msg)
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReadWriteBidirectional(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// initiator -> responder
	msg1 := []byte("ping")
	wErr1 := writeAsync(initS, msg1)
	buf := make([]byte, 256)
	n, err := respS.Read(buf)
	if err != nil {
		t.Fatalf("read ping: %v", err)
	}
	if !bytes.Equal(msg1, buf[:n]) {
		t.Fatalf("ping mismatch: got %q", buf[:n])
	}
	if err := <-wErr1; err != nil {
		t.Fatalf("write ping: %v", err)
	}

	// responder -> initiator
	msg2 := []byte("pong")
	wErr2 := writeAsync(respS, msg2)
	n, err = initS.Read(buf)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if !bytes.Equal(msg2, buf[:n]) {
		t.Fatalf("pong mismatch: got %q", buf[:n])
	}
	if err := <-wErr2; err != nil {
		t.Fatalf("write pong: %v", err)
	}
}

func TestLargeMessageChunking(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// 200KB message - will be chunked at MaxPlaintextLength boundaries.
	const size = 200 * 1024
	msg := make([]byte, size)
	if _, err := rand.Read(msg); err != nil {
		t.Fatalf("rand: %v", err)
	}

	wErr := writeAsync(initS, msg)

	got := make([]byte, size)
	if _, err := io.ReadFull(respS, got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(msg, got) {
		t.Fatal("large message mismatch")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestSmallBufferQueuing(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Send a message larger than the reader's buffer.
	msg := make([]byte, 1024)
	for i := range msg {
		msg[i] = byte(i % 256)
	}
	wErr := writeAsync(initS, msg)

	// Read in small chunks - exercises the qbuf/qseek queuing path.
	var collected []byte
	buf := make([]byte, 100)
	for len(collected) < len(msg) {
		n, err := respS.Read(buf)
		if err != nil {
			t.Fatalf("read chunk: %v (collected %d/%d)", err, len(collected), len(msg))
		}
		collected = append(collected, buf[:n]...)
	}
	if !bytes.Equal(msg, collected) {
		t.Fatal("queued read mismatch")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestEmptyWrite(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Write empty data - should succeed with 0 bytes written and no wire traffic.
	n, err := initS.Write([]byte{})
	if err != nil {
		t.Fatalf("empty write: %v", err)
	}
	if n != 0 {
		t.Fatalf("expected 0 bytes written, got %d", n)
	}

	// Verify non-empty write still works after empty write.
	msg := []byte("after empty")
	wErr := writeAsync(initS, msg)
	buf := make([]byte, 256)
	rn, err := respS.Read(buf)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(msg, buf[:rn]) {
		t.Fatalf("follow-up mismatch")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestExactMaxPlaintextLength(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Write exactly MaxPlaintextLength bytes - should produce exactly one frame.
	msg := make([]byte, MaxPlaintextLength)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}
	wErr := writeAsync(initS, msg)

	got := make([]byte, MaxPlaintextLength)
	if _, err := io.ReadFull(respS, got); err != nil {
		t.Fatalf("read max: %v", err)
	}
	if !bytes.Equal(msg, got) {
		t.Fatal("max plaintext mismatch")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write max: %v", err)
	}
}

func TestMaxPlaintextLengthPlusOne(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// One byte over MaxPlaintextLength - forces 2 frames.
	msg := make([]byte, MaxPlaintextLength+1)
	if _, err := rand.Read(msg); err != nil {
		t.Fatal(err)
	}
	wErr := writeAsync(initS, msg)

	got := make([]byte, MaxPlaintextLength+1)
	if _, err := io.ReadFull(respS, got); err != nil {
		t.Fatalf("read max+1: %v", err)
	}
	if !bytes.Equal(msg, got) {
		t.Fatal("max+1 plaintext mismatch")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write max+1: %v", err)
	}
}

func TestConcurrentReadWrite(t *testing.T) {
	initS, respS := makeSessionPair(t)

	const messages = 50
	const msgSize = 512
	var wg sync.WaitGroup

	// Writer goroutine: initiator -> responder
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < messages; i++ {
			msg := make([]byte, msgSize)
			msg[0] = byte(i)
			if _, err := initS.Write(msg); err != nil {
				t.Errorf("write %d: %v", i, err)
				return
			}
		}
	}()

	// Reader goroutine: responder reads all
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, msgSize)
		for i := 0; i < messages; i++ {
			if _, err := io.ReadFull(respS, buf); err != nil {
				t.Errorf("read %d: %v", i, err)
				return
			}
			if buf[0] != byte(i) {
				t.Errorf("message %d: got tag %d", i, buf[0])
			}
		}
	}()

	wg.Wait()
}

func TestConcurrentBidirectional(t *testing.T) {
	initS, respS := makeSessionPair(t)

	const messages = 30
	var wg sync.WaitGroup

	// Bidirectional: init -> resp AND resp -> init simultaneously.
	// This exercises read/write lock independence.
	for _, dir := range []struct {
		writer, reader *pqSession
		tag            byte
	}{
		{initS, respS, 0xAA},
		{respS, initS, 0xBB},
	} {
		wg.Add(2)
		go func(w *pqSession, tag byte) {
			defer wg.Done()
			for i := 0; i < messages; i++ {
				msg := []byte{tag, byte(i)}
				if _, err := w.Write(msg); err != nil {
					t.Errorf("write tag=%x i=%d: %v", tag, i, err)
					return
				}
			}
		}(dir.writer, dir.tag)

		go func(r *pqSession, tag byte) {
			defer wg.Done()
			buf := make([]byte, 2)
			for i := 0; i < messages; i++ {
				if _, err := io.ReadFull(r, buf); err != nil {
					t.Errorf("read tag=%x i=%d: %v", tag, i, err)
					return
				}
				if buf[0] != tag {
					t.Errorf("wrong tag: got %x, want %x", buf[0], tag)
				}
			}
		}(dir.reader, dir.tag)
	}

	wg.Wait()
}

func TestWriteAfterClose(t *testing.T) {
	initS, _ := makeSessionPair(t)

	if err := initS.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := initS.Write([]byte("should fail"))
	if err == nil {
		t.Fatal("expected error writing to closed session")
	}
	if !errors.Is(err, errSessionClosed) {
		t.Fatalf("expected errSessionClosed, got: %v", err)
	}
}

func TestReadAfterClose(t *testing.T) {
	initS, _ := makeSessionPair(t)

	if err := initS.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	buf := make([]byte, 256)
	_, err := initS.Read(buf)
	if err == nil {
		t.Fatal("expected error reading from closed session")
	}
	if !errors.Is(err, errSessionClosed) {
		t.Fatalf("expected errSessionClosed, got: %v", err)
	}
}

func TestDestroyAfterClose(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Verify TransportState is live before close.
	if initS.transportState.IsDestroyed() {
		t.Fatal("init transportState destroyed before close")
	}
	if respS.transportState.IsDestroyed() {
		t.Fatal("resp transportState destroyed before close")
	}

	initS.Close()
	respS.Close()

	// After close, TransportState.Destroy() must have been called.
	if !initS.transportState.IsDestroyed() {
		t.Error("init transportState not destroyed after close")
	}
	if !respS.transportState.IsDestroyed() {
		t.Error("resp transportState not destroyed after close")
	}
}

func TestDoubleCloseIdempotent(t *testing.T) {
	initS, _ := makeSessionPair(t)

	err1 := initS.Close()
	err2 := initS.Close()

	// First close may or may not error depending on conn state.
	// Second close must return nil (already closed).
	_ = err1
	if err2 != nil {
		t.Fatalf("second Close should return nil, got: %v", err2)
	}
}

func TestCloseWithQueuedData(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Write 1024 bytes, then read only 50 to leave data in qbuf.
	msg := make([]byte, 1024)
	for i := range msg {
		msg[i] = byte(i % 256)
	}
	wErr := writeAsync(initS, msg)

	// Partial read into small buffer - leaves remainder in qbuf.
	buf := make([]byte, 50)
	n, err := respS.Read(buf)
	if err != nil {
		t.Fatalf("partial read: %v", err)
	}
	if n == 0 {
		t.Fatal("partial read returned 0 bytes")
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}

	// Verify qbuf is populated.
	if respS.qbuf == nil {
		t.Fatal("qbuf should be non-nil after partial read")
	}

	// Close should zero and release qbuf without panic.
	if err := respS.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// qbuf should be released.
	if respS.qbuf != nil {
		t.Error("qbuf not released after close")
	}
}

func TestCloseDuringBlockedRead(t *testing.T) {
	_, respS := makeSessionPair(t)

	// Start a Read that blocks (no data available).
	readErr := make(chan error, 1)
	go func() {
		buf := make([]byte, 256)
		_, err := respS.Read(buf)
		readErr <- err
	}()

	// Give the reader goroutine time to block on IO.
	time.Sleep(20 * time.Millisecond)

	// Close should unblock the reader via conn.Close().
	if err := respS.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	select {
	case err := <-readErr:
		if err == nil {
			t.Fatal("expected error from read after close")
		}
		// The error should be an IO error from the closed pipe, not a panic.
	case <-time.After(5 * time.Second):
		t.Fatal("read did not unblock after close")
	}
}

func TestCloseDuringWrite(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// net.Pipe is synchronous - a large Write blocks until the reader drains.
	writeErr := make(chan error, 1)
	go func() {
		big := make([]byte, 1024*1024) // 1MB should block on pipe
		_, err := initS.Write(big)
		writeErr <- err
	}()

	time.Sleep(20 * time.Millisecond)

	// Close both sides to unblock the writer.
	respS.Close()
	initS.Close()

	select {
	case err := <-writeErr:
		if err == nil {
			t.Fatal("expected error from write after close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("write did not unblock after close")
	}
}

func TestReadNoTransportState(t *testing.T) {
	// Session without TransportState (handshake not complete).
	ic, rc := net.Pipe()
	defer ic.Close()
	defer rc.Close()
	s := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
	}

	buf := make([]byte, 256)
	_, err := s.Read(buf)
	if !errors.Is(err, errNoTransportState) {
		t.Fatalf("expected errNoTransportState, got: %v", err)
	}
}

func TestWriteNoTransportState(t *testing.T) {
	ic, rc := net.Pipe()
	defer ic.Close()
	defer rc.Close()
	s := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
	}

	_, err := s.Write([]byte("test"))
	if !errors.Is(err, errNoTransportState) {
		t.Fatalf("expected errNoTransportState, got: %v", err)
	}
}

func TestIdentityAccessors(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// LocalPeer / RemotePeer
	if initS.LocalPeer() != respS.RemotePeer() {
		t.Error("init.LocalPeer != resp.RemotePeer")
	}
	if respS.LocalPeer() != initS.RemotePeer() {
		t.Error("resp.LocalPeer != init.RemotePeer")
	}

	// LocalPublicKey / RemotePublicKey
	if !initS.LocalPublicKey().Equals(respS.RemotePublicKey()) {
		t.Error("init public key mismatch")
	}
	if !respS.LocalPublicKey().Equals(initS.RemotePublicKey()) {
		t.Error("resp public key mismatch")
	}
}

func TestDelegatedMethods(t *testing.T) {
	initS, _ := makeSessionPair(t)

	// LocalAddr and RemoteAddr should not panic.
	if initS.LocalAddr() == nil {
		t.Error("LocalAddr is nil")
	}
	if initS.RemoteAddr() == nil {
		t.Error("RemoteAddr is nil")
	}

	// Deadlines should not error on a pipe.
	now := time.Now().Add(time.Hour)
	if err := initS.SetDeadline(now); err != nil {
		t.Errorf("SetDeadline: %v", err)
	}
	if err := initS.SetReadDeadline(now); err != nil {
		t.Errorf("SetReadDeadline: %v", err)
	}
	if err := initS.SetWriteDeadline(now); err != nil {
		t.Errorf("SetWriteDeadline: %v", err)
	}
	// Clear deadlines.
	if err := initS.SetDeadline(time.Time{}); err != nil {
		t.Errorf("clear deadline: %v", err)
	}
}

func TestMultipleMessages(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Send many small messages in sequence - writer and reader run concurrently.
	const count = 100

	wErr := make(chan error, 1)
	go func() {
		for i := 0; i < count; i++ {
			msg := []byte{byte(i), byte(i + 1), byte(i + 2)}
			if _, err := initS.Write(msg); err != nil {
				wErr <- err
				return
			}
		}
		wErr <- nil
	}()

	buf := make([]byte, 3)
	for i := 0; i < count; i++ {
		if _, err := io.ReadFull(respS, buf); err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		expected := []byte{byte(i), byte(i + 1), byte(i + 2)}
		if !bytes.Equal(buf, expected) {
			t.Fatalf("message %d: got %v, want %v", i, buf, expected)
		}
	}
	if err := <-wErr; err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestReadDecryptionError(t *testing.T) {
	initTS, respTS, ic, rc := makeTransportStatePair(t)
	defer respTS.Destroy()

	initPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	initID, err := peer.IDFromPrivateKey(initPriv)
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}

	s := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
		localID:        initID,
		localKey:       initPriv,
		transportState: initTS,
	}

	// Write garbage bytes directly to the other end of the pipe.
	// This bypasses encryption - the 2-byte length prefix says "32 bytes"
	// but the body is random, not valid ciphertext.
	done := make(chan struct{})
	go func() {
		defer close(done)
		var lenBuf [2]byte
		lenBuf[0] = 0
		lenBuf[1] = 32 // claim 32 bytes of "ciphertext"
		rc.Write(lenBuf[:])
		garbage := make([]byte, 32)
		rand.Read(garbage)
		rc.Write(garbage)
	}()

	// Fast path: caller buffer (256) >= ciphertext (32). Tests line 121-123.
	buf := make([]byte, 256)
	_, err = s.Read(buf)
	if err == nil {
		t.Fatal("expected decryption error from garbage ciphertext (fast path)")
	}
	<-done
	s.Close()
	rc.Close()
}

func TestReadDecryptionErrorSlowPath(t *testing.T) {
	initTS, respTS, ic, rc := makeTransportStatePair(t)
	defer respTS.Destroy()

	initPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	initID, err := peer.IDFromPrivateKey(initPriv)
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}

	s := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
		localID:        initID,
		localKey:       initPriv,
		transportState: initTS,
	}

	// Write garbage with a large length prefix so the ciphertext exceeds
	// the caller's read buffer, forcing the slow path (pool buffer).
	const ciphertextLen = 200
	done := make(chan struct{})
	go func() {
		defer close(done)
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], ciphertextLen)
		rc.Write(lenBuf[:])
		garbage := make([]byte, ciphertextLen)
		rand.Read(garbage)
		rc.Write(garbage)
	}()

	// Slow path: caller buffer (50) < ciphertext (200). Tests line 137-141.
	buf := make([]byte, 50)
	_, err = s.Read(buf)
	if err == nil {
		t.Fatal("expected decryption error from garbage ciphertext (slow path)")
	}
	<-done
	s.Close()
	rc.Close()
}

func TestWriteReturnsByteCount(t *testing.T) {
	initS, respS := makeSessionPair(t)

	// Verify Write returns the exact plaintext byte count, not ciphertext.
	msg := make([]byte, 1000)
	wCh := make(chan int, 1)
	go func() {
		n, err := initS.Write(msg)
		if err != nil {
			wCh <- -1
			return
		}
		wCh <- n
	}()

	got := make([]byte, 1000)
	if _, err := io.ReadFull(respS, got); err != nil {
		t.Fatalf("read: %v", err)
	}

	n := <-wCh
	if n != len(msg) {
		t.Fatalf("Write returned %d bytes, want %d", n, len(msg))
	}
}

func TestReadZeroLengthFrame(t *testing.T) {
	initTS, respTS, ic, rc := makeTransportStatePair(t)
	defer respTS.Destroy()

	initPriv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("keygen: %v", err)
	}
	initID, err := peer.IDFromPrivateKey(initPriv)
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}

	s := &pqSession{
		insecureConn:   ic,
		insecureReader: bufio.NewReader(ic),
		localID:        initID,
		localKey:       initPriv,
		transportState: initTS,
	}

	// Attacker sends a zero-length frame: length prefix = 0x0000.
	// A valid ciphertext is always >= TagLen (16) bytes.
	// Receive on 0 bytes must return an error (no AEAD tag).
	done := make(chan struct{})
	go func() {
		defer close(done)
		var lenBuf [2]byte // 0x0000
		rc.Write(lenBuf[:])
	}()

	buf := make([]byte, 256)
	_, err = s.Read(buf)
	if err == nil {
		t.Fatal("expected error from zero-length ciphertext frame")
	}
	<-done
	s.Close()
	rc.Close()
}

func TestZeroSlice(t *testing.T) {
	b := []byte{1, 2, 3, 4, 5}
	zeroSlice(b)
	for i, v := range b {
		if v != 0 {
			t.Fatalf("byte %d not zeroed: %d", i, v)
		}
	}
}
