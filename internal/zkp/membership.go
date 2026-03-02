package zkp

import (
	"github.com/consensys/gnark/frontend"
)

// MembershipCircuit is a PLONK circuit that proves knowledge of a key
// whose Poseidon2 hash is a leaf in a Merkle tree, without revealing which leaf.
//
// Public inputs:
//   - MerkleRoot: the Poseidon2 root of the authorized_keys Merkle tree
//   - Nonce: fresh per-challenge value (binds proof to session, prevents replay)
//   - RoleRequired: 0 = any role accepted, 1 = must be admin, 2 = must be member
//
// Private inputs (witness):
//   - PubKeyBytes: 32 field elements, one per byte of Ed25519 public key
//   - RoleEncoding: 1 = admin, 2 = member
//   - Score: reputation score committed in the leaf (not range-checked here,
//     but included in leaf hash for binding with range proofs)
//   - Path: sibling hashes at each tree level
//   - PathBits: left/right direction at each level
type MembershipCircuit struct {
	// Public inputs.
	MerkleRoot   frontend.Variable `gnark:",public"`
	Nonce        frontend.Variable `gnark:",public"`
	RoleRequired frontend.Variable `gnark:",public"`

	// Private witness.
	PubKeyBytes  [32]frontend.Variable
	RoleEncoding frontend.Variable
	Score        frontend.Variable
	Path         [MaxTreeDepth]frontend.Variable
	PathBits     [MaxTreeDepth]frontend.Variable
}

// Define implements frontend.Circuit. It constrains the membership proof:
//  1. Compute leaf = Poseidon2(PubKeyBytes[0..31], RoleEncoding, Score)
//  2. Walk the Merkle path to compute the root
//  3. Assert computed root == MerkleRoot (public)
//  4. If RoleRequired != 0, assert RoleEncoding == RoleRequired
//  5. Nonce is bound as a public input (no arithmetic, prevents replay)
//
// Note: Score is committed in the leaf hash but not range-checked here.
// The membership circuit only proves "I am in this tree." For score
// verification, use RangeProofCircuit which extends this with score >= threshold.
func (c *MembershipCircuit) Define(api frontend.API) error {
	// Step 1: Compute leaf hash = Poseidon2(pubkey_bytes[0..31], role_encoding, score).
	leafInputs := make([]frontend.Variable, 34)
	for i := 0; i < 32; i++ {
		leafInputs[i] = c.PubKeyBytes[i]
	}
	leafInputs[32] = c.RoleEncoding
	leafInputs[33] = c.Score

	leaf, err := CircuitPoseidon2(api, leafInputs...)
	if err != nil {
		return err
	}

	// Step 2: Walk the Merkle path.
	current := leaf
	for i := 0; i < MaxTreeDepth; i++ {
		// PathBits[i] == 0 means we're the left child, == 1 means right child.
		// Enforce PathBits[i] is boolean.
		api.AssertIsBoolean(c.PathBits[i])

		// Select left and right based on path direction.
		// If PathBits[i] == 0: left = current, right = Path[i]
		// If PathBits[i] == 1: left = Path[i], right = current
		left := api.Select(c.PathBits[i], c.Path[i], current)
		right := api.Select(c.PathBits[i], current, c.Path[i])

		current, err = CircuitPoseidon2Pair(api, left, right)
		if err != nil {
			return err
		}
	}

	// Step 3: Assert computed root == public MerkleRoot.
	api.AssertIsEqual(current, c.MerkleRoot)

	// Step 4: Role check. If RoleRequired != 0, enforce RoleEncoding == RoleRequired.
	// Compute: isRoleCheck = (RoleRequired != 0) ? 1 : 0
	isRoleCheck := api.Sub(1, api.IsZero(c.RoleRequired))
	// Compute: roleDiff = RoleEncoding - RoleRequired
	roleDiff := api.Sub(c.RoleEncoding, c.RoleRequired)
	// If isRoleCheck == 1, roleDiff must be 0.
	// Enforce: isRoleCheck * roleDiff == 0
	api.AssertIsEqual(api.Mul(isRoleCheck, roleDiff), 0)

	// Step 5: Nonce is bound as public input. No arithmetic needed;
	// its presence in the public inputs ties this proof to a specific challenge.
	// The verifier checks the nonce externally (freshness, single-use).
	_ = c.Nonce

	return nil
}
