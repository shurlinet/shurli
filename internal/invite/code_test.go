package invite

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	pid, _ := peer.IDFromPrivateKey(priv)

	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	data := &InviteData{
		Token:     token,
		RelayAddr: "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
		PeerID:    pid,
	}

	code, err := Encode(data)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	t.Logf("Invite code (%d chars): %s", len(code), code)

	decoded, err := Decode(code)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}

	if token != decoded.Token {
		t.Errorf("Token mismatch")
	}
	if data.RelayAddr != decoded.RelayAddr {
		t.Errorf("RelayAddr mismatch: got %s, want %s", decoded.RelayAddr, data.RelayAddr)
	}
	if data.PeerID != decoded.PeerID {
		t.Errorf("PeerID mismatch: got %s, want %s", decoded.PeerID, data.PeerID)
	}
}

func TestDecodeInvalid(t *testing.T) {
	_, err := Decode("not-a-valid-code")
	if err == nil {
		t.Error("expected error for invalid code")
	}

	_, err = Decode("")
	if err == nil {
		t.Error("expected error for empty code")
	}
}
