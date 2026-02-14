package invite

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func benchInviteData(b *testing.B) *InviteData {
	b.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		b.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		b.Fatal(err)
	}
	token, err := GenerateToken()
	if err != nil {
		b.Fatal(err)
	}
	return &InviteData{
		Token:     token,
		RelayAddr: "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWRzaGMTqQbRHNMZkAYj8ALUXoK99qSjhiFLanDoVWK9An",
		PeerID:    pid,
	}
}

func BenchmarkEncode(b *testing.B) {
	data := benchInviteData(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Encode(data)
	}
}

func BenchmarkDecode(b *testing.B) {
	data := benchInviteData(b)
	code, err := Encode(data)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		Decode(code)
	}
}
