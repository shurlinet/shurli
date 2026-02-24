package invite

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

func TestPAKEHandshakeSuccess(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	// Create both sessions
	inviter, err := NewPAKESession()
	if err != nil {
		t.Fatalf("NewPAKESession (inviter): %v", err)
	}
	joiner, err := NewPAKESession()
	if err != nil {
		t.Fatalf("NewPAKESession (joiner): %v", err)
	}

	// Exchange public keys
	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	if len(inviterPub) != 32 {
		t.Fatalf("inviter public key length = %d, want 32", len(inviterPub))
	}
	if len(joinerPub) != 32 {
		t.Fatalf("joiner public key length = %d, want 32", len(joinerPub))
	}

	// Complete handshake (both sides use the same token)
	if err := inviter.Complete(joinerPub, token); err != nil {
		t.Fatalf("inviter.Complete: %v", err)
	}
	if err := joiner.Complete(inviterPub, token); err != nil {
		t.Fatalf("joiner.Complete: %v", err)
	}

	// Encrypt/decrypt round-trip: joiner -> inviter
	message := []byte("hello from joiner")
	encrypted, err := joiner.Encrypt(message)
	if err != nil {
		t.Fatalf("joiner.Encrypt: %v", err)
	}

	decrypted, err := inviter.Decrypt(bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("inviter.Decrypt: %v", err)
	}

	if !bytes.Equal(message, decrypted) {
		t.Errorf("decrypted message mismatch: got %q, want %q", decrypted, message)
	}

	// Encrypt/decrypt round-trip: inviter -> joiner
	reply := []byte("OK home-server")
	encrypted2, err := inviter.Encrypt(reply)
	if err != nil {
		t.Fatalf("inviter.Encrypt: %v", err)
	}

	decrypted2, err := joiner.Decrypt(bytes.NewReader(encrypted2))
	if err != nil {
		t.Fatalf("joiner.Decrypt: %v", err)
	}

	if !bytes.Equal(reply, decrypted2) {
		t.Errorf("decrypted reply mismatch: got %q, want %q", decrypted2, reply)
	}
}

func TestPAKETokenMismatch(t *testing.T) {
	tokenA := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}
	tokenB := [8]byte{9, 10, 11, 12, 13, 14, 15, 16}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	// Each side uses a different token
	inviter.Complete(joinerPub, tokenA)
	joiner.Complete(inviterPub, tokenB)

	// Joiner encrypts with tokenB's key
	encrypted, err := joiner.Encrypt([]byte("hello"))
	if err != nil {
		t.Fatalf("joiner.Encrypt: %v", err)
	}

	// Inviter tries to decrypt with tokenA's key - should fail
	_, err = inviter.Decrypt(bytes.NewReader(encrypted))
	if err == nil {
		t.Fatal("decryption should fail with mismatched tokens")
	}
	t.Logf("Correctly failed: %v", err)
}

func TestPAKETamperedCiphertext(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	encrypted, _ := joiner.Encrypt([]byte("hello"))

	// Tamper with the ciphertext (flip a byte after the 2-byte length prefix)
	if len(encrypted) > 3 {
		encrypted[3] ^= 0xFF
	}

	_, err := inviter.Decrypt(bytes.NewReader(encrypted))
	if err == nil {
		t.Fatal("decryption should fail with tampered ciphertext")
	}
}

func TestPAKEEmptyMessage(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	// Encrypt empty message
	encrypted, err := joiner.Encrypt([]byte{})
	if err != nil {
		t.Fatalf("Encrypt empty: %v", err)
	}

	decrypted, err := inviter.Decrypt(bytes.NewReader(encrypted))
	if err != nil {
		t.Fatalf("Decrypt empty: %v", err)
	}

	if len(decrypted) != 0 {
		t.Errorf("expected empty decrypted message, got %d bytes", len(decrypted))
	}
}

func TestPAKEOversizedMessage(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	// Craft a message with length prefix exceeding maxEncryptedMsgLen
	oversize := uint16(maxEncryptedMsgLen + 1)
	var buf bytes.Buffer
	buf.WriteByte(byte(oversize >> 8))
	buf.WriteByte(byte(oversize))
	buf.Write(make([]byte, oversize))

	_, err := inviter.Decrypt(&buf)
	if err == nil {
		t.Fatal("Decrypt should reject oversized messages")
	}
}

func TestPAKEStreamSimulation(t *testing.T) {
	// Simulate the full wire protocol using io.Pipe
	token := [8]byte{0xAA, 0xBB, 0xCC, 0xDD, 0xEE, 0xFF, 0x11, 0x22}

	inviterToJoiner := &bytes.Buffer{}
	joinerToInviter := &bytes.Buffer{}

	// Step 1: Both create sessions
	inviterSession, _ := NewPAKESession()
	joinerSession, _ := NewPAKESession()

	// Step 2: Joiner sends [0x02][32-byte pubkey] to inviter
	joinerToInviter.WriteByte(VersionV1)
	joinerToInviter.Write(joinerSession.PublicKey())

	// Step 3: Inviter reads version + pubkey from joiner
	versionByte, _ := joinerToInviter.ReadByte()
	if versionByte != VersionV1 {
		t.Fatalf("expected version 0x02, got 0x%02x", versionByte)
	}
	joinerPub, err := ReadPublicKey(joinerToInviter)
	if err != nil {
		t.Fatalf("ReadPublicKey: %v", err)
	}

	// Step 4: Inviter sends pubkey to joiner
	inviterToJoiner.Write(inviterSession.PublicKey())

	// Step 5: Both complete the handshake
	if err := inviterSession.Complete(joinerPub, token); err != nil {
		t.Fatalf("inviter.Complete: %v", err)
	}
	inviterPub, _ := ReadPublicKey(inviterToJoiner)
	if err := joinerSession.Complete(inviterPub, token); err != nil {
		t.Fatalf("joiner.Complete: %v", err)
	}

	// Step 6: Encrypted message exchange
	joinerMsg := []byte("laptop")
	joinerToInviter.Reset()
	if err := joinerSession.WriteEncrypted(joinerToInviter, joinerMsg); err != nil {
		t.Fatalf("joiner WriteEncrypted: %v", err)
	}

	decryptedName, err := inviterSession.Decrypt(joinerToInviter)
	if err != nil {
		t.Fatalf("inviter Decrypt: %v", err)
	}
	if string(decryptedName) != "laptop" {
		t.Errorf("expected 'laptop', got %q", string(decryptedName))
	}

	// Inviter replies
	inviterToJoiner.Reset()
	if err := inviterSession.WriteEncrypted(inviterToJoiner, []byte("OK home")); err != nil {
		t.Fatalf("inviter WriteEncrypted: %v", err)
	}

	decryptedReply, err := joinerSession.Decrypt(inviterToJoiner)
	if err != nil {
		t.Fatalf("joiner Decrypt: %v", err)
	}
	if string(decryptedReply) != "OK home" {
		t.Errorf("expected 'OK home', got %q", string(decryptedReply))
	}
}

func TestPAKEConfirmationMAC(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	// Same role should produce same MAC
	mac1 := inviter.ConfirmationMAC("inviter")
	mac2 := joiner.ConfirmationMAC("inviter")
	if !bytes.Equal(mac1, mac2) {
		t.Error("MACs for same role should match with same session key")
	}

	// Different roles should produce different MACs
	mac3 := inviter.ConfirmationMAC("joiner")
	if bytes.Equal(mac1, mac3) {
		t.Error("MACs for different roles should differ")
	}
}

func TestPAKENotCompleted(t *testing.T) {
	session, _ := NewPAKESession()

	_, err := session.Encrypt([]byte("test"))
	if err == nil {
		t.Error("Encrypt should fail before Complete()")
	}

	_, err = session.Decrypt(bytes.NewReader([]byte{0, 5, 1, 2, 3, 4, 5}))
	if err == nil {
		t.Error("Decrypt should fail before Complete()")
	}
}

func TestPAKEDecryptTruncated(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	encrypted, _ := joiner.Encrypt([]byte("hello"))

	// Truncate the encrypted message
	truncated := encrypted[:len(encrypted)/2]

	_, err := inviter.Decrypt(bytes.NewReader(truncated))
	if err == nil {
		t.Fatal("Decrypt should fail with truncated ciphertext")
	}
}

func TestPAKEDecryptZeroLength(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	// Zero-length message (length prefix = 0)
	_, err := inviter.Decrypt(bytes.NewReader([]byte{0, 0}))
	if err == nil {
		t.Fatal("Decrypt should reject zero-length messages")
	}
}

func TestPAKEDecryptEOF(t *testing.T) {
	token := [8]byte{1, 2, 3, 4, 5, 6, 7, 8}

	inviter, _ := NewPAKESession()
	joiner, _ := NewPAKESession()

	inviterPub := inviter.PublicKey()
	joinerPub := joiner.PublicKey()

	inviter.Complete(joinerPub, token)
	joiner.Complete(inviterPub, token)

	// Empty reader (EOF before length prefix)
	_, err := inviter.Decrypt(bytes.NewReader(nil))
	if err == nil {
		t.Fatal("Decrypt should fail on empty reader")
	}
}

func TestReadPublicKeyEOF(t *testing.T) {
	_, err := ReadPublicKey(bytes.NewReader([]byte{1, 2, 3})) // only 3 bytes, need 32
	if err == nil {
		t.Fatal("ReadPublicKey should fail with insufficient data")
	}
}

func TestPAKEUniqueKeysPerSession(t *testing.T) {
	s1, _ := NewPAKESession()
	s2, _ := NewPAKESession()

	if bytes.Equal(s1.PublicKey(), s2.PublicKey()) {
		t.Error("two sessions should have different public keys")
	}
}

// TestPAKEWithIOPipe simulates a real bidirectional stream using io.Pipe.
func TestPAKEWithIOPipe(t *testing.T) {
	token := [8]byte{0xDE, 0xAD, 0xBE, 0xEF, 0xCA, 0xFE, 0xBA, 0xBE}

	// Create two pipes simulating a bidirectional stream
	joinerToInviterR, joinerToInviterW := io.Pipe()
	inviterToJoinerR, inviterToJoinerW := io.Pipe()

	errCh := make(chan error, 2)

	// Inviter goroutine
	go func() {
		defer joinerToInviterR.Close()
		defer inviterToJoinerW.Close()

		session, err := NewPAKESession()
		if err != nil {
			errCh <- err
			return
		}

		// Read version byte + joiner's pubkey
		var vBuf [1]byte
		if _, err := io.ReadFull(joinerToInviterR, vBuf[:]); err != nil {
			errCh <- err
			return
		}
		if vBuf[0] != VersionV1 {
			errCh <- fmt.Errorf("expected v2, got %d", vBuf[0])
			return
		}

		joinerPub, err := ReadPublicKey(joinerToInviterR)
		if err != nil {
			errCh <- err
			return
		}

		// Send our pubkey
		if err := session.WritePublicKey(inviterToJoinerW); err != nil {
			errCh <- err
			return
		}

		// Complete handshake
		if err := session.Complete(joinerPub, token); err != nil {
			errCh <- err
			return
		}

		// Read encrypted name from joiner
		name, err := session.Decrypt(joinerToInviterR)
		if err != nil {
			errCh <- err
			return
		}

		// Send encrypted response
		if err := session.WriteEncrypted(inviterToJoinerW, []byte("OK home "+string(name))); err != nil {
			errCh <- err
			return
		}

		errCh <- nil
	}()

	// Joiner goroutine
	go func() {
		defer inviterToJoinerR.Close()
		defer joinerToInviterW.Close()

		session, err := NewPAKESession()
		if err != nil {
			errCh <- err
			return
		}

		// Send version byte + our pubkey
		if _, err := joinerToInviterW.Write([]byte{VersionV1}); err != nil {
			errCh <- err
			return
		}
		if err := session.WritePublicKey(joinerToInviterW); err != nil {
			errCh <- err
			return
		}

		// Read inviter's pubkey
		inviterPub, err := ReadPublicKey(inviterToJoinerR)
		if err != nil {
			errCh <- err
			return
		}

		// Complete handshake
		if err := session.Complete(inviterPub, token); err != nil {
			errCh <- err
			return
		}

		// Send encrypted name
		if err := session.WriteEncrypted(joinerToInviterW, []byte("laptop")); err != nil {
			errCh <- err
			return
		}

		// Read encrypted response
		reply, err := session.Decrypt(inviterToJoinerR)
		if err != nil {
			errCh <- err
			return
		}

		if string(reply) != "OK home laptop" {
			errCh <- fmt.Errorf("expected 'OK home laptop', got %q", string(reply))
			return
		}

		errCh <- nil
	}()

	// Wait for both sides to complete
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("goroutine error: %v", err)
		}
	}
}
