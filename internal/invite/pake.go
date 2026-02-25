package invite

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

// PAKE handshake for invite/join protocol.
//
// Wire protocol:
//   1. Joiner -> Inviter: [0x01] [32-byte X25519 public key]     (33 bytes)
//   2. Inviter -> Joiner: [32-byte X25519 public key]             (32 bytes)
//      Both compute: shared = X25519(myPrivate, theirPublic)
//      Both derive:  key = HKDF-SHA256(shared || token, "shurli-invite-v1")
//   3. Joiner -> Inviter: [length-prefixed AEAD encrypted message]
//   4. Inviter -> Joiner: [length-prefixed AEAD encrypted message]
//
// If the tokens don't match, HKDF produces different keys and AEAD
// decryption fails. The inviter reports "invalid invite code" with
// no protocol details leaked.

const (
	// pakeInfo is the HKDF info string for deriving the session key.
	pakeInfo = "shurli-invite-v1"

	// maxEncryptedMsgLen caps the length of a single encrypted message
	// to prevent memory exhaustion from a malicious peer.
	maxEncryptedMsgLen = 4096

	// VersionV1 identifies the PAKE-secured invite protocol (peer-to-peer).
	VersionV1 byte = 0x01

	// VersionV2 identifies the relay pairing protocol (relay-mediated).
	VersionV2 byte = 0x02
)

// PAKESession holds the state for one side of a PAKE handshake.
type PAKESession struct {
	privKey *ecdh.PrivateKey
	key     []byte // derived AEAD key (32 bytes)
}

// deriveKey computes the shared AEAD key from the DH shared secret and token.
func deriveKey(sharedSecret []byte, token [8]byte) ([]byte, error) {
	// Combine shared secret with token as HKDF salt material.
	// This binds the session key to the invite token.
	salt := make([]byte, len(sharedSecret)+len(token))
	copy(salt, sharedSecret)
	copy(salt[len(sharedSecret):], token[:])

	r := hkdf.New(sha256.New, salt, nil, []byte(pakeInfo))
	key := make([]byte, chacha20poly1305.KeySize)
	if _, err := io.ReadFull(r, key); err != nil {
		return nil, fmt.Errorf("HKDF key derivation failed: %w", err)
	}
	return key, nil
}

// NewPAKESession creates a new PAKE session with a fresh ephemeral X25519 key pair.
func NewPAKESession() (*PAKESession, error) {
	privKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("X25519 key generation failed: %w", err)
	}
	return &PAKESession{privKey: privKey}, nil
}

// PublicKey returns the session's ephemeral X25519 public key (32 bytes).
func (s *PAKESession) PublicKey() []byte {
	return s.privKey.PublicKey().Bytes()
}

// Complete performs the DH exchange and derives the AEAD key.
// remotePub is the other side's 32-byte X25519 public key.
// token is the invite token (shared secret).
func (s *PAKESession) Complete(remotePub []byte, token [8]byte) error {
	peerKey, err := ecdh.X25519().NewPublicKey(remotePub)
	if err != nil {
		return fmt.Errorf("invalid remote public key: %w", err)
	}

	shared, err := s.privKey.ECDH(peerKey)
	if err != nil {
		return fmt.Errorf("X25519 key exchange failed: %w", err)
	}

	key, err := deriveKey(shared, token)
	if err != nil {
		return err
	}
	s.key = key

	// Zero the private key to limit exposure.
	// (Go's ecdh doesn't expose raw bytes for zeroing, but setting to nil
	// makes the object unusable and eligible for GC.)
	s.privKey = nil

	return nil
}

// Encrypt encrypts plaintext with the derived session key using XChaCha20-Poly1305.
// Returns length-prefixed ciphertext suitable for writing to a stream.
func (s *PAKESession) Encrypt(plaintext []byte) ([]byte, error) {
	if s.key == nil {
		return nil, fmt.Errorf("session not completed: call Complete() first")
	}

	aead, err := chacha20poly1305.NewX(s.key)
	if err != nil {
		return nil, fmt.Errorf("AEAD creation failed: %w", err)
	}

	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce generation failed: %w", err)
	}

	ciphertext := aead.Seal(nonce, nonce, plaintext, nil)

	// Length-prefix: 2-byte big-endian length + ciphertext
	buf := make([]byte, 2+len(ciphertext))
	binary.BigEndian.PutUint16(buf, uint16(len(ciphertext)))
	copy(buf[2:], ciphertext)

	return buf, nil
}

// Decrypt reads a length-prefixed encrypted message from r and decrypts it.
func (s *PAKESession) Decrypt(r io.Reader) ([]byte, error) {
	if s.key == nil {
		return nil, fmt.Errorf("session not completed: call Complete() first")
	}

	// Read 2-byte length prefix
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("failed to read message length: %w", err)
	}
	msgLen := int(binary.BigEndian.Uint16(lenBuf[:]))

	if msgLen == 0 {
		return nil, fmt.Errorf("empty encrypted message")
	}
	if msgLen > maxEncryptedMsgLen {
		return nil, fmt.Errorf("encrypted message too large: %d bytes (max %d)", msgLen, maxEncryptedMsgLen)
	}

	aead, err := chacha20poly1305.NewX(s.key)
	if err != nil {
		return nil, fmt.Errorf("AEAD creation failed: %w", err)
	}

	// Read ciphertext
	ciphertext := make([]byte, msgLen)
	if _, err := io.ReadFull(r, ciphertext); err != nil {
		return nil, fmt.Errorf("failed to read encrypted message: %w", err)
	}

	if len(ciphertext) < aead.NonceSize() {
		return nil, fmt.Errorf("ciphertext too short for nonce")
	}

	nonce := ciphertext[:aead.NonceSize()]
	encrypted := ciphertext[aead.NonceSize():]

	plaintext, err := aead.Open(nil, nonce, encrypted, nil)
	if err != nil {
		return nil, fmt.Errorf("decryption failed (wrong invite code?): %w", err)
	}

	return plaintext, nil
}

// ConfirmationMAC computes a MAC over the role label using the session key.
// Both sides compute MACs with different labels ("inviter" vs "joiner") and
// exchange them for explicit key confirmation.
func (s *PAKESession) ConfirmationMAC(role string) []byte {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(role))
	return mac.Sum(nil)
}

// WritePublicKey writes the session's public key to w (32 bytes).
func (s *PAKESession) WritePublicKey(w io.Writer) error {
	_, err := w.Write(s.PublicKey())
	return err
}

// ReadPublicKey reads a 32-byte X25519 public key from r.
func ReadPublicKey(r io.Reader) ([]byte, error) {
	pub := make([]byte, 32)
	if _, err := io.ReadFull(r, pub); err != nil {
		return nil, fmt.Errorf("failed to read public key: %w", err)
	}
	return pub, nil
}

// WriteEncrypted encrypts plaintext and writes it to w.
func (s *PAKESession) WriteEncrypted(w io.Writer, plaintext []byte) error {
	msg, err := s.Encrypt(plaintext)
	if err != nil {
		return err
	}
	_, err = w.Write(msg)
	return err
}
