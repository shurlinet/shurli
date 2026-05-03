// Package pqnoise implements a post-quantum Noise security transport for libp2p.
//
// Protocol ID: /pq-noise/1
// Pattern: HybridDualLayer (outer NQ NN + inner Hybrid XX)
// Algorithms: X25519 + ML-KEM-768, ChaChaPoly, SHA256
// Wire format: 6 messages, ~8720 bytes total handshake
//
// This transport provides post-quantum key exchange alongside classical X25519,
// using go-clatter's HybridDualLayerHandshake. The outer NN layer provides an
// encrypted tunnel; the inner Hybrid XX layer provides PQ-resistant mutual
// authentication and key exchange.
//
// Semantics of /pq-noise/1 are immutable after release. Any algorithm or
// protocol change requires a new protocol ID (/pq-noise/2).
package pqnoise

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/sec"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/shurlinet/go-clatter"
)

// MaxPlaintextLength is the maximum payload per encrypted frame.
// Noise spec: MaxMessageLen (65535) minus TagLen (16).
const MaxPlaintextLength = clatter.MaxMessageLen - clatter.TagLen

// LengthPrefixLength is the 2-byte big-endian length prefix on every wire frame.
const LengthPrefixLength = 2

// pqSession implements sec.SecureConn over a post-quantum Noise channel.
// It wraps an insecure net.Conn with go-clatter's TransportState for
// authenticated encryption.
//
// Concurrency: Read and Write are independently safe for concurrent use
// (separate mutexes). Close is safe to call concurrently with Read/Write;
// it closes the underlying conn (unblocking IO) then destroys crypto state.
type pqSession struct {
	insecureConn   net.Conn
	insecureReader *bufio.Reader

	localID   peer.ID
	localKey  crypto.PrivKey
	remoteID  peer.ID
	remoteKey crypto.PubKey

	transportState *clatter.TransportState

	readLock  sync.Mutex
	writeLock sync.Mutex

	// Queued plaintext from a previous Read that returned more data than
	// the caller's buffer could hold.
	qseek int
	qbuf  []byte
	rlen  [2]byte // reusable buffer for reading the 2-byte length prefix

	closed atomic.Int32

	connectionState network.ConnectionState
}

// Compile-time interface checks.
var _ sec.SecureConn = (*pqSession)(nil)

// errSessionClosed is returned when Read/Write is called after Close.
var errSessionClosed = errors.New("pqnoise: session closed")

// errNoTransportState is returned when Read/Write is called before handshake completes.
var errNoTransportState = errors.New("pqnoise: transport state not initialized")

// Read decrypts data from the secure connection.
// Implements io.Reader contract: partial reads queue remainder for next call.
func (s *pqSession) Read(buf []byte) (int, error) {
	s.readLock.Lock()
	defer s.readLock.Unlock()

	if s.closed.Load() != 0 {
		return 0, errSessionClosed
	}
	if s.transportState == nil {
		return 0, errNoTransportState
	}

	// Drain queued bytes from a previous oversized decrypt.
	if s.qbuf != nil {
		copied := copy(buf, s.qbuf[s.qseek:])
		s.qseek += copied
		if s.qseek == len(s.qbuf) {
			zeroSlice(s.qbuf[:cap(s.qbuf)]) // zero full pool buffer, not just the slice
			pool.Put(s.qbuf)
			s.qseek, s.qbuf = 0, nil
		}
		return copied, nil
	}

	// Read the 2-byte length prefix for the next encrypted frame.
	nextMsgLen, err := s.readNextMsgLen()
	if err != nil {
		return 0, err
	}

	// Fast path: caller buffer is large enough for the full ciphertext.
	// Decrypt in-place into buf.
	if len(buf) >= nextMsgLen {
		if err := s.readExact(buf[:nextMsgLen]); err != nil {
			return 0, err
		}
		n, err := s.transportState.Receive(buf[:nextMsgLen], buf)
		if err != nil {
			return 0, err
		}
		return n, nil
	}

	// Slow path: get a pool buffer, decrypt into it, queue remainder.
	cbuf := pool.Get(nextMsgLen)
	if err := s.readExact(cbuf[:nextMsgLen]); err != nil {
		zeroSlice(cbuf)
		pool.Put(cbuf)
		return 0, err
	}

	// Receive decrypts message into cbuf (plaintext is shorter than ciphertext).
	n, err := s.transportState.Receive(cbuf[:nextMsgLen], cbuf)
	if err != nil {
		zeroSlice(cbuf)
		pool.Put(cbuf)
		return 0, err
	}

	// Retain decrypted buffer as qbuf; copy what fits into caller's buf.
	s.qbuf = cbuf[:n]
	s.qseek = copy(buf, s.qbuf)
	return s.qseek, nil
}

// Write encrypts plaintext and sends it on the secure connection.
// Large writes are chunked at MaxPlaintextLength boundaries.
func (s *pqSession) Write(data []byte) (int, error) {
	s.writeLock.Lock()
	defer s.writeLock.Unlock()

	if s.closed.Load() != 0 {
		return 0, errSessionClosed
	}
	if s.transportState == nil {
		return 0, errNoTransportState
	}

	// Empty write: no work needed, no pool allocation.
	if len(data) == 0 {
		return 0, nil
	}

	var (
		written int
		total   = len(data)
	)

	// Allocate a single buffer for all chunks (reused across loop iterations).
	writeBufSize := total + clatter.TagLen + LengthPrefixLength
	if total > MaxPlaintextLength {
		writeBufSize = MaxPlaintextLength + clatter.TagLen + LengthPrefixLength
	}
	cbuf := pool.Get(writeBufSize)
	defer func() {
		zeroSlice(cbuf)
		pool.Put(cbuf)
	}()

	for written < total {
		end := written + MaxPlaintextLength
		if end > total {
			end = total
		}
		chunkLen := end - written

		// Encrypt into cbuf after the length prefix.
		n, err := s.transportState.Send(data[written:end], cbuf[LengthPrefixLength:LengthPrefixLength+chunkLen+clatter.TagLen])
		if err != nil {
			return written, err
		}

		// Write 2-byte big-endian length prefix.
		binary.BigEndian.PutUint16(cbuf, uint16(n))

		// Send length prefix + ciphertext on the wire.
		if _, err := s.insecureConn.Write(cbuf[:LengthPrefixLength+n]); err != nil {
			return written, err
		}
		written = end
	}
	return written, nil
}

// Close closes the secure session. It first closes the underlying connection
// (which unblocks any in-flight Read/Write via IO errors), then acquires both
// locks to safely destroy crypto state and release buffers.
func (s *pqSession) Close() error {
	if !s.closed.CompareAndSwap(0, 1) {
		return nil // already closed
	}
	// Close the underlying conn first. This unblocks any goroutine blocked
	// on IO (io.ReadFull returns error), causing it to release its lock.
	err := s.insecureConn.Close()

	// Acquire both locks to ensure no concurrent Read/Write is mid-crypto.
	// After conn.Close(), any blocked IO returns error. Any Read/Write that
	// already has data from the bufio buffer will complete its crypto op and
	// release the lock. We wait for that to finish, then destroy safely.
	s.writeLock.Lock()
	s.readLock.Lock()

	if s.transportState != nil {
		s.transportState.Destroy()
	}
	if s.qbuf != nil {
		zeroSlice(s.qbuf[:cap(s.qbuf)]) // zero full pool buffer, not just the slice
		pool.Put(s.qbuf)
		s.qseek, s.qbuf = 0, nil
	}

	s.readLock.Unlock()
	s.writeLock.Unlock()

	return err
}

// LocalAddr delegates to the underlying connection.
func (s *pqSession) LocalAddr() net.Addr { return s.insecureConn.LocalAddr() }

// RemoteAddr delegates to the underlying connection.
func (s *pqSession) RemoteAddr() net.Addr { return s.insecureConn.RemoteAddr() }

// SetDeadline delegates to the underlying connection.
func (s *pqSession) SetDeadline(t time.Time) error { return s.insecureConn.SetDeadline(t) }

// SetReadDeadline delegates to the underlying connection.
func (s *pqSession) SetReadDeadline(t time.Time) error { return s.insecureConn.SetReadDeadline(t) }

// SetWriteDeadline delegates to the underlying connection.
func (s *pqSession) SetWriteDeadline(t time.Time) error { return s.insecureConn.SetWriteDeadline(t) }

// LocalPeer returns the local peer ID.
func (s *pqSession) LocalPeer() peer.ID { return s.localID }

// LocalPublicKey returns the local Ed25519 identity public key.
func (s *pqSession) LocalPublicKey() crypto.PubKey { return s.localKey.GetPublic() }

// RemotePeer returns the authenticated remote peer ID.
func (s *pqSession) RemotePeer() peer.ID { return s.remoteID }

// RemotePublicKey returns the remote peer's Ed25519 identity public key.
func (s *pqSession) RemotePublicKey() crypto.PubKey { return s.remoteKey }

// ConnState returns the connection state including negotiated muxer.
func (s *pqSession) ConnState() network.ConnectionState { return s.connectionState }

// readNextMsgLen reads the 2-byte big-endian length prefix from the wire.
func (s *pqSession) readNextMsgLen() (int, error) {
	_, err := io.ReadFull(s.insecureReader, s.rlen[:])
	if err != nil {
		return 0, err
	}
	return int(binary.BigEndian.Uint16(s.rlen[:])), nil
}

// readExact reads exactly len(buf) bytes from the insecure reader.
func (s *pqSession) readExact(buf []byte) error {
	_, err := io.ReadFull(s.insecureReader, buf)
	return err
}

// zeroSlice zeroes all bytes in a slice (defense in depth for pool buffers).
func zeroSlice(b []byte) {
	clear(b)
}
