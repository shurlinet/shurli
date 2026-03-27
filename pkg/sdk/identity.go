package sdk

import (
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/identity"
)

// LoadOrCreateIdentity loads an existing SHRL-encrypted identity or creates a new one.
func LoadOrCreateIdentity(path, password string) (crypto.PrivKey, error) {
	return identity.LoadOrCreateIdentity(path, password)
}

// PeerIDFromKeyFile loads an encrypted key file and returns the derived peer ID.
func PeerIDFromKeyFile(path, password string) (peer.ID, error) {
	return identity.PeerIDFromKeyFile(path, password)
}
