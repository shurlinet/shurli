package p2pnet

import (
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/internal/identity"
)

// LoadOrCreateIdentity loads an existing identity from a file or creates a new one.
func LoadOrCreateIdentity(path string) (crypto.PrivKey, error) {
	return identity.LoadOrCreateIdentity(path)
}

// PeerIDFromKeyFile loads (or creates) a key file and returns the derived peer ID.
func PeerIDFromKeyFile(path string) (peer.ID, error) {
	return identity.PeerIDFromKeyFile(path)
}
