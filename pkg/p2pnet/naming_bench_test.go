package p2pnet

import (
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func BenchmarkResolveByName(b *testing.B) {
	r := NewNameResolver()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		b.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		b.Fatal(err)
	}
	r.Register("home", pid)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Resolve("home")
	}
}

func BenchmarkResolveByPeerID(b *testing.B) {
	r := NewNameResolver()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		b.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		b.Fatal(err)
	}
	pidStr := pid.String()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.Resolve(pidStr)
	}
}
