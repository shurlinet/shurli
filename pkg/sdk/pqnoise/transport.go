package pqnoise

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"runtime/debug"
	"slices"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/sec"
	tptu "github.com/libp2p/go-libp2p/p2p/net/upgrader"
	"github.com/libp2p/go-libp2p/p2p/security/noise/pb"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/shurlinet/go-clatter"
	clattercipher "github.com/shurlinet/go-clatter/crypto/cipher"
	clatterdh "github.com/shurlinet/go-clatter/crypto/dh"
	clatterhash "github.com/shurlinet/go-clatter/crypto/hash"
	clatterkem "github.com/shurlinet/go-clatter/crypto/kem"
	"google.golang.org/protobuf/proto"
)

// ID is the protocol ID for PQ Noise.
const ID = "/pq-noise/1"

// payloadSigPrefix is prepended to the handshake hash before signing with
// the Ed25519 identity key. Different from classical noise's prefix to prevent
// cross-protocol signature replay.
const payloadSigPrefix = "noise-libp2p-pq-handshake:"

// innerPrologueStr is the fixed domain separator for the inner Hybrid XX handshake.
// Stored as string (immutable in Go) and converted to []byte at each handshake
// to prevent accidental mutation of protocol-critical data.
const innerPrologueStr = "noise-libp2p-pq-v1"

// maxProtoNum caps the number of muxer entries in the handshake payload.
const maxProtoNum = 100

// maxHandshakeMessageLen caps the size of any single handshake wire message.
// Largest message (msg4) is ~3716 bytes. 8192 provides generous margin.
const maxHandshakeMessageLen = 8192

// bufSize is the buffer size for the DualLayer's internal outerRecvBuf and
// our handshake buffer. Matches go-clatter test convention.
const bufSize = 8192

// maxPayloadLen caps identity payload size after decryption (defense in depth).
const maxPayloadLen = 4096

// Transport implements sec.SecureTransport for post-quantum Noise.
type Transport struct {
	protocolID protocol.ID
	localID    peer.ID
	privateKey crypto.PrivKey
	muxers     []protocol.ID
}

// Compile-time interface assertion.
var _ sec.SecureTransport = (*Transport)(nil)

// New creates a new PQ Noise transport. This constructor signature matches
// the libp2p Fx injection pattern exactly.
func New(id protocol.ID, privkey crypto.PrivKey, muxers []tptu.StreamMuxer) (*Transport, error) {
	localID, err := peer.IDFromPrivateKey(privkey)
	if err != nil {
		return nil, err
	}

	muxerIDs := make([]protocol.ID, 0, len(muxers))
	for _, m := range muxers {
		muxerIDs = append(muxerIDs, m.ID)
	}

	return &Transport{
		protocolID: id,
		localID:    localID,
		privateKey: privkey,
		muxers:     muxerIDs,
	}, nil
}

// ID returns the protocol ID.
func (t *Transport) ID() protocol.ID { return t.protocolID }

// SecureInbound runs the PQ Noise handshake as the responder.
// If p is empty, connections from any peer are accepted.
func (t *Transport) SecureInbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	c, muxer, err := t.newSecureSession(ctx, insecure, p, false, p != "")
	if err != nil {
		return c, err
	}
	return sessionWithConnState(c, muxer), nil
}

// SecureOutbound runs the PQ Noise handshake as the initiator.
func (t *Transport) SecureOutbound(ctx context.Context, insecure net.Conn, p peer.ID) (sec.SecureConn, error) {
	c, muxer, err := t.newSecureSession(ctx, insecure, p, true, true)
	if err != nil {
		return c, err
	}
	return sessionWithConnState(c, muxer), nil
}

// newSecureSession creates and runs the PQ Noise handshake using the
// goroutine + channel + select pattern (matching classical noise).
func (t *Transport) newSecureSession(ctx context.Context, insecure net.Conn, remote peer.ID, initiator, checkPeerID bool) (*pqSession, protocol.ID, error) {
	s := &pqSession{
		insecureConn:   insecure,
		insecureReader: bufio.NewReader(insecure),
		localID:        t.localID,
		localKey:       t.privateKey,
		remoteID:       remote,
	}

	type handshakeResult struct {
		muxer protocol.ID
		err   error
	}

	respCh := make(chan handshakeResult, 1)
	go func() {
		defer func() {
			if rerr := recover(); rerr != nil {
				// Send error FIRST to unblock the caller, then log.
				respCh <- handshakeResult{err: fmt.Errorf("panic in PQ Noise handshake: %s", rerr)}
				fmt.Fprintf(os.Stderr, "caught panic in PQ Noise handshake: %s\n%s\n", rerr, debug.Stack())
			}
		}()
		muxer, err := t.runHandshake(ctx, s, initiator, checkPeerID)
		respCh <- handshakeResult{muxer: muxer, err: err}
	}()

	select {
	case res := <-respCh:
		if res.err != nil {
			_ = s.insecureConn.Close()
			return s, "", res.err
		}
		return s, res.muxer, nil

	case <-ctx.Done():
		_ = s.insecureConn.Close()
		<-respCh // drain goroutine
		return nil, "", ctx.Err()
	}
}

// runHandshake executes the 6-message HybridDualLayer handshake.
// Pattern: outer NQ NN (encrypted tunnel) + inner Hybrid XX (PQ key exchange + identity).
func (t *Transport) runHandshake(ctx context.Context, s *pqSession, initiator, checkPeerID bool) (protocol.ID, error) {
	// Set deadline from context if available. Clear after handshake.
	if deadline, ok := ctx.Deadline(); ok {
		if err := s.SetDeadline(deadline); err == nil {
			defer s.SetDeadline(time.Time{})
		}
	}

	// Generate fresh keypairs for the inner Hybrid XX handshake.
	// Outer NN needs no static key (ephemeral generated internally by go-clatter).
	x25519DH := clatterdh.NewX25519()
	mlkem := clatterkem.NewMlKem768()

	innerX25519, err := x25519DH.GenerateKeypair(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("pqnoise: inner X25519 keygen: %w", err)
	}
	defer innerX25519.Destroy()

	innerMLKEM, err := mlkem.GenerateKeypair(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("pqnoise: inner ML-KEM-768 keygen: %w", err)
	}
	defer innerMLKEM.Destroy()

	// Build cipher suites for outer and inner layers.
	outerSuite := clatter.CipherSuite{
		DH:     x25519DH,
		Cipher: clattercipher.NewChaChaPoly(),
		Hash:   clatterhash.NewSha256(),
	}
	innerSuite := clatter.CipherSuite{
		DH:     x25519DH,
		Cipher: clattercipher.NewChaChaPoly(),
		Hash:   clatterhash.NewSha256(),
		EKEM:   mlkem,
		SKEM:   mlkem,
	}

	// Construct outer NQ NN handshaker (no static key, no prologue).
	outerHS, err := clatter.NewNqHandshake(clatter.PatternNN, initiator, outerSuite)
	if err != nil {
		return "", fmt.Errorf("pqnoise: outer handshake init: %w", err)
	}
	// Do NOT defer outerHS.Destroy() - DualLayer takes ownership (F194).

	// Construct inner Hybrid XX handshaker with static keys and prologue.
	innerHS, err := clatter.NewHybridHandshake(clatter.PatternHybridXX, initiator, innerSuite,
		clatter.WithStaticKey(innerX25519),
		clatter.WithStaticKEMKey(innerMLKEM),
		clatter.WithPrologue([]byte(innerPrologueStr)),
	)
	if err != nil {
		outerHS.Destroy() // manual cleanup
		return "", fmt.Errorf("pqnoise: inner handshake init: %w", err)
	}
	// Do NOT defer innerHS.Destroy() - DualLayer takes ownership (F194).

	// Compose into HybridDualLayer.
	hs, err := clatter.NewHybridDualLayerHandshake(outerHS, innerHS, bufSize)
	if err != nil {
		outerHS.Destroy()
		innerHS.Destroy()
		return "", fmt.Errorf("pqnoise: dual-layer composition: %w", err)
	}
	defer hs.Destroy()

	// Handshake buffer for read/write (reused across all messages).
	// Size accounts for LengthPrefixLength + largest possible message body.
	hbuf := pool.Get(LengthPrefixLength + maxHandshakeMessageLen)
	defer func() { zeroSlice(hbuf); pool.Put(hbuf) }()

	// Muxer negotiation state.
	var receivedMuxers []protocol.ID
	var negotiatedMuxer protocol.ID

	// Run the 6-message handshake loop.
	// For identity messages, we capture the handshake hash BEFORE the
	// WriteMessage/ReadMessage call, because both operations update the hash.
	// The signer signs hash-before-write; the verifier must verify against
	// the same hash-before-read (F139).
	msgIndex := 0
	for !hs.IsFinished() {
		if hs.IsWriteTurn() {
			// Determine payload: identity for the identity message, nil otherwise.
			var payload []byte
			if isIdentityMessage(msgIndex, true, initiator) {
				// Capture hash BEFORE WriteMessage (F139: signer uses pre-write hash).
				payload, err = t.generatePayload(s, hs)
				if err != nil {
					return "", err
				}
			}

			// WriteMessage encrypts payload into hbuf[2:].
			n, err := hs.WriteMessage(payload, hbuf[LengthPrefixLength:])
			if err != nil {
				return "", fmt.Errorf("pqnoise: write msg %d: %w", msgIndex, err)
			}

			// Prepend 2-byte length prefix.
			binary.BigEndian.PutUint16(hbuf, uint16(n))

			// Send on wire.
			if _, err := s.insecureConn.Write(hbuf[:LengthPrefixLength+n]); err != nil {
				return "", fmt.Errorf("pqnoise: wire write msg %d: %w", msgIndex, err)
			}
		} else {
			// Capture hash BEFORE ReadMessage for identity verification (F139).
			var preReadHash []byte
			if isIdentityMessage(msgIndex, false, initiator) {
				preReadHash = hs.GetHandshakeHash()
			}

			// Read length prefix.
			if _, err := io.ReadFull(s.insecureReader, hbuf[:LengthPrefixLength]); err != nil {
				return "", fmt.Errorf("pqnoise: read len msg %d: %w", msgIndex, err)
			}
			msgLen := int(binary.BigEndian.Uint16(hbuf[:LengthPrefixLength]))

			// Validate message size (F153).
			if msgLen > maxHandshakeMessageLen {
				return "", fmt.Errorf("pqnoise: msg %d too large: %d > %d", msgIndex, msgLen, maxHandshakeMessageLen)
			}

			// Read message body.
			if _, err := io.ReadFull(s.insecureReader, hbuf[LengthPrefixLength:LengthPrefixLength+msgLen]); err != nil {
				return "", fmt.Errorf("pqnoise: read body msg %d: %w", msgIndex, err)
			}

			// ReadMessage decrypts and returns payload length.
			payloadN, err := hs.ReadMessage(hbuf[LengthPrefixLength:LengthPrefixLength+msgLen], hbuf[LengthPrefixLength:])
			if err != nil {
				return "", fmt.Errorf("pqnoise: read msg %d: %w", msgIndex, err)
			}

			// Process identity payload if this is an identity message.
			if preReadHash != nil {
				// Defense in depth: cap payload size before processing (F208).
				if payloadN > maxPayloadLen {
					return "", fmt.Errorf("pqnoise: identity payload too large: %d > %d", payloadN, maxPayloadLen)
				}

				rcvdMuxers, err := t.verifyPayloadWithHash(s, hbuf[LengthPrefixLength:LengthPrefixLength+payloadN], preReadHash, checkPeerID)
				if err != nil {
					return "", err
				}
				receivedMuxers = rcvdMuxers
			}
		}
		msgIndex++
	}

	// Finalize: extract inner TransportState (outer is destroyed internally).
	ts, err := hs.Finalize()
	if err != nil {
		return "", fmt.Errorf("pqnoise: finalize: %w", err)
	}
	s.transportState = ts

	// Negotiate muxer from exchanged preferences.
	if initiator {
		negotiatedMuxer = matchMuxers(t.muxers, receivedMuxers)
	} else {
		negotiatedMuxer = matchMuxers(receivedMuxers, t.muxers)
	}

	return negotiatedMuxer, nil
}

// generatePayload creates the identity protobuf payload signed with the
// handshake hash. The signature binds both layers (inner hash includes
// outer hash via domain separator).
func (t *Transport) generatePayload(s *pqSession, hs clatter.Handshaker) ([]byte, error) {
	localKeyRaw, err := crypto.MarshalPublicKey(s.LocalPublicKey())
	if err != nil {
		return nil, fmt.Errorf("pqnoise: marshal identity key: %w", err)
	}

	// Sign: prefix || handshakeHash
	// The hash at this point includes: prologue + domain separator + outer hash + prior inner tokens.
	hsHash := hs.GetHandshakeHash()
	toSign := append([]byte(payloadSigPrefix), hsHash...)
	sig, err := s.localKey.Sign(toSign)
	if err != nil {
		return nil, fmt.Errorf("pqnoise: sign handshake: %w", err)
	}

	// Build protobuf (reuses libp2p noise/pb for interop with upgrader).
	ext := &pb.NoiseExtensions{
		StreamMuxers: protocol.ConvertToStrings(t.muxers),
	}
	payloadEnc, err := proto.Marshal(&pb.NoiseHandshakePayload{
		IdentityKey: localKeyRaw,
		IdentitySig: sig,
		Extensions:  ext,
	})
	if err != nil {
		return nil, fmt.Errorf("pqnoise: marshal payload: %w", err)
	}
	return payloadEnc, nil
}

// verifyPayloadWithHash verifies the remote peer's identity payload against
// the provided handshake hash (captured BEFORE ReadMessage) and returns their
// muxer preferences.
func (t *Transport) verifyPayloadWithHash(s *pqSession, payload []byte, hsHash []byte, checkPeerID bool) ([]protocol.ID, error) {
	nhp := new(pb.NoiseHandshakePayload)
	if err := proto.Unmarshal(payload, nhp); err != nil {
		return nil, fmt.Errorf("pqnoise: unmarshal remote payload: %w", err)
	}

	// Unpack remote Ed25519 public key.
	remotePubKey, err := crypto.UnmarshalPublicKey(nhp.GetIdentityKey())
	if err != nil {
		return nil, fmt.Errorf("pqnoise: unmarshal remote key: %w", err)
	}
	id, err := peer.IDFromPublicKey(remotePubKey)
	if err != nil {
		return nil, fmt.Errorf("pqnoise: derive remote peer ID: %w", err)
	}

	// Check peer ID if required.
	if checkPeerID && s.remoteID != id {
		return nil, sec.ErrPeerIDMismatch{Expected: s.remoteID, Actual: id}
	}

	// Verify signature over handshake hash.
	// hsHash was captured BEFORE ReadMessage - both sides have identical
	// hash at that point (F139: signer uses pre-write, verifier uses pre-read).
	msg := append([]byte(payloadSigPrefix), hsHash...)
	ok, err := remotePubKey.Verify(msg, nhp.GetIdentitySig())
	if err != nil {
		return nil, fmt.Errorf("pqnoise: verify signature: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("pqnoise: handshake signature invalid")
	}

	// Set remote identity on session.
	s.remoteID = id
	s.remoteKey = remotePubKey

	// Extract muxer preferences (cap at maxProtoNum for DoS defense).
	var muxers []protocol.ID
	if nhp.Extensions != nil && len(nhp.Extensions.StreamMuxers) <= maxProtoNum {
		muxers = protocol.ConvertFromStrings(nhp.Extensions.GetStreamMuxers())
	}
	return muxers, nil
}

// isIdentityMessage returns true if the message at msgIndex carries an identity
// payload (for writing or reading).
//
// For NN(2 msgs) + HybridXX(4 msgs) = 6 total messages:
//   - Responder SENDS identity at msg index 3 (inner msg2: Ekem+E+EE+S+ES)
//   - Initiator SENDS identity at msg index 4 (inner msg3: Skem+S+SE)
//   - Initiator READS identity at msg index 3
//   - Responder READS identity at msg index 4
func isIdentityMessage(msgIndex int, isWrite bool, initiator bool) bool {
	if initiator {
		return (isWrite && msgIndex == 4) || (!isWrite && msgIndex == 3)
	}
	return (isWrite && msgIndex == 3) || (!isWrite && msgIndex == 4)
}

// matchMuxers selects the first muxer from preferredOrder that exists in supported.
func matchMuxers(preferredOrder, supported []protocol.ID) protocol.ID {
	for _, m := range preferredOrder {
		if slices.Contains(supported, m) {
			return m
		}
	}
	return ""
}

// sessionWithConnState sets the connection state on a session after handshake.
func sessionWithConnState(s *pqSession, muxer protocol.ID) *pqSession {
	if s != nil {
		s.connectionState = network.ConnectionState{
			Security:                   ID,
			StreamMultiplexer:          muxer,
			UsedEarlyMuxerNegotiation: muxer != "",
		}
	}
	return s
}
