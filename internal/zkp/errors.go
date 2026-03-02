// Package zkp provides zero-knowledge proof primitives for anonymous
// authentication and private reputation in Shurli networks.
//
// The core primitive is a Poseidon2 Merkle tree of authorized_keys:
// a PLONK circuit proves "I know a key whose hash is a leaf in this tree"
// without revealing which leaf. Role-aware encoding enables proving
// "I'm an admin" without revealing which admin.
//
// Built on gnark (ConsenSys) with PLONK proving on BN254.
package zkp

import "errors"

var (
	// ErrPeerNotInTree is returned when a peer ID is not found in the Merkle tree.
	ErrPeerNotInTree = errors.New("peer not in merkle tree")

	// ErrTreeEmpty is returned when attempting to build a tree with no peers.
	ErrTreeEmpty = errors.New("cannot build merkle tree with zero peers")

	// ErrInvalidProof is returned when a ZKP proof fails verification.
	ErrInvalidProof = errors.New("invalid zkp proof")

	// ErrProofExpired is returned when a proof's timestamp is too old.
	ErrProofExpired = errors.New("zkp proof expired")

	// ErrNonceReused is returned when a challenge nonce has already been consumed.
	ErrNonceReused = errors.New("challenge nonce already used")

	// ErrSRSNotFound is returned when the KZG SRS file is not cached locally.
	ErrSRSNotFound = errors.New("kzg srs not found; run 'shurli zkp setup'")

	// ErrVaultSealed is returned when a ZKP operation requires an unsealed vault.
	ErrVaultSealed = errors.New("vault must be unsealed for this zkp operation")

	// ErrCircuitNotCompiled is returned when proving keys have not been generated.
	ErrCircuitNotCompiled = errors.New("zkp circuit not compiled; run 'shurli zkp setup'")

	// ErrRootMismatch is returned when the Merkle root at verification time
	// does not match the root at challenge issuance.
	ErrRootMismatch = errors.New("merkle root changed since challenge was issued")

	// ErrTooManyChallenges is returned when the challenge store has reached
	// its maximum capacity of pending challenges.
	ErrTooManyChallenges = errors.New("too many pending challenges")
)
