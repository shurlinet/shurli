package zkp

// Rate-Limiting Nullifier (RLN) extension point.
//
// RLN enables anonymous rate limiting: each member can perform one action
// per epoch (e.g., one message per time slot). Performing two actions in
// the same epoch reveals the member's secret, allowing automatic detection
// and slashing of spammers without identifying honest participants.
//
// This file defines the types and interface for future RLN implementation.
// No circuit or logic is implemented yet. The types are designed to integrate
// with the existing Poseidon2 Merkle tree and PLONK proving system.
//
// Protocol sketch:
//  1. Each member registers commitment = Poseidon2(secret) in the Merkle tree
//  2. To signal once per epoch, member reveals one Shamir share of their secret
//  3. Two shares from the same epoch = full secret recovery = spammer identified
//  4. Honest members (one action per epoch) remain fully anonymous

// RLNIdentity holds a member's RLN secret and its Poseidon2 commitment.
// The commitment is registered in the membership Merkle tree.
// The secret is never shared directly; only shares are revealed per epoch.
type RLNIdentity struct {
	Secret     []byte // private random secret (32 bytes, BN254 field element)
	Commitment []byte // Poseidon2(secret), registered in tree
}

// RLNProof is a proof of rate-limited anonymous signaling.
// Each proof binds to a specific epoch and reveals one Shamir share.
// A second proof in the same epoch from the same identity reveals
// the secret (two shares = polynomial interpolation to recover secret).
type RLNProof struct {
	Epoch     uint64 // time slot identifier (e.g., Unix timestamp / epoch_length)
	Nullifier []byte // deterministic per (identity, epoch), prevents double-signal
	Share     []byte // one Shamir share of the secret for this epoch
	ZKProof   []byte // PLONK proof of valid share + membership + epoch binding
}

// RLNVerifier defines the interface for verifying RLN proofs and detecting spam.
// This will be implemented when RLN support is added in a future phase.
type RLNVerifier interface {
	// VerifyEpoch checks that an RLN proof is valid for the given tree root.
	// Returns true if the proof is valid and the nullifier has not been seen
	// in this epoch before.
	VerifyEpoch(proof *RLNProof, treeRoot []byte) (bool, error)

	// DetectSpam checks if two proofs from the same epoch reveal the same
	// identity. If so, returns the recovered commitment (which can be used
	// to identify and remove the spammer from the tree).
	// Returns nil commitment if the proofs are from different identities.
	DetectSpam(proof1, proof2 *RLNProof) (commitment []byte, err error)
}
