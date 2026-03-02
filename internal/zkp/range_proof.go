package zkp

import (
	"bytes"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/constraint"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/scs"
	"github.com/libp2p/go-libp2p/core/peer"
)

// RangeProofCircuit extends the membership circuit with a range proof on
// a reputation score. It proves: "I am a member of this tree AND my
// committed score is >= threshold" without revealing the exact score or
// which member I am.
//
// The score is committed in the leaf hash (binding): the same Score value
// is used both in the leaf hash computation (Merkle membership) and in the
// range check. A peer cannot claim a different score than what the relay
// committed in the tree.
//
// Public inputs:
//   - MerkleRoot: Poseidon2 root of the authorized_keys Merkle tree
//   - Nonce: fresh per-challenge value (replay protection)
//   - RoleRequired: 0 = any, 1 = admin, 2 = member
//   - Threshold: minimum score required (0-100)
//
// Private inputs (witness):
//   - PubKeyBytes: 32 field elements (Ed25519 pubkey, byte-by-byte)
//   - RoleEncoding: 1 = admin, 2 = member
//   - Path: sibling hashes at each tree level
//   - PathBits: left/right direction bits
//   - Score: the peer's reputation score (0-100), committed in leaf hash
type RangeProofCircuit struct {
	// Public inputs.
	MerkleRoot   frontend.Variable `gnark:",public"`
	Nonce        frontend.Variable `gnark:",public"`
	RoleRequired frontend.Variable `gnark:",public"`
	Threshold    frontend.Variable `gnark:",public"`

	// Private witness.
	PubKeyBytes  [32]frontend.Variable
	RoleEncoding frontend.Variable
	Path         [MaxTreeDepth]frontend.Variable
	PathBits     [MaxTreeDepth]frontend.Variable
	Score        frontend.Variable
}

// Define implements frontend.Circuit. It constrains:
//  1. Compute leaf = Poseidon2(PubKeyBytes[0..31], RoleEncoding, Score)
//  2. Merkle membership proof (walk path to root)
//  3. Role check (if RoleRequired != 0)
//  4. Score >= Threshold (binding: same Score that was hashed in the leaf)
//  5. Score <= 100
//  6. Nonce bound as public input
func (c *RangeProofCircuit) Define(api frontend.API) error {
	// --- Membership proof (identical to MembershipCircuit) ---

	// Step 1: Compute leaf hash = Poseidon2(pubkey_bytes[0..31], role_encoding, score).
	// The score is committed here: if the prover lies about their score,
	// the leaf hash won't match any leaf in the tree, and the Merkle
	// proof fails at step 3.
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
		api.AssertIsBoolean(c.PathBits[i])
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
	isRoleCheck := api.Sub(1, api.IsZero(c.RoleRequired))
	roleDiff := api.Sub(c.RoleEncoding, c.RoleRequired)
	api.AssertIsEqual(api.Mul(isRoleCheck, roleDiff), 0)

	// --- Range proof (binding: Score is the SAME value committed in the leaf) ---

	// Step 5: Assert Score >= Threshold.
	api.AssertIsLessOrEqual(c.Threshold, c.Score)

	// Step 6: Assert Score <= 100.
	api.AssertIsLessOrEqual(c.Score, 100)

	// Step 7: Nonce bound as public input.
	_ = c.Nonce

	return nil
}

// CompileRangeProofCircuit compiles the RangeProofCircuit into a constraint system.
func CompileRangeProofCircuit() (constraint.ConstraintSystem, error) {
	circuit := &RangeProofCircuit{}
	return frontend.Compile(ecc.BN254.ScalarField(), scs.NewBuilder, circuit)
}

// RangeProver generates ZKP range proofs (membership + score >= threshold).
type RangeProver struct {
	ccs        constraint.ConstraintSystem
	provingKey plonk.ProvingKey
}

// NewRangeProverFromKeys creates a RangeProver from pre-loaded keys.
func NewRangeProverFromKeys(ccs constraint.ConstraintSystem, provingKey plonk.ProvingKey) *RangeProver {
	return &RangeProver{ccs: ccs, provingKey: provingKey}
}

// Prove generates a range proof for the given peer. The score is read from
// the peer's committed leaf in the tree (binding guarantee). The prover cannot
// claim a different score than what was committed during tree building.
//
// Parameters:
//   - tree: the current Merkle tree of authorized peers (with committed scores)
//   - peerID: the peer proving their membership and score
//   - nonce: fresh challenge nonce
//   - roleRequired: 0 = any, 1 = admin, 2 = member
//   - threshold: the minimum score to prove (score >= threshold)
func (p *RangeProver) Prove(tree *MerkleTree, peerID peer.ID, nonce, roleRequired uint64, threshold int) ([]byte, error) {
	var targetLeaf MerkleLeaf
	found := false
	for _, leaf := range tree.Leaves {
		if leaf.PeerID == peerID {
			targetLeaf = leaf
			found = true
			break
		}
	}
	if !found {
		return nil, ErrPeerNotInTree
	}

	merkleProof, err := tree.GenerateProof(peerID)
	if err != nil {
		return nil, fmt.Errorf("generating merkle proof: %w", err)
	}

	assignment, err := buildRangeAssignment(tree, merkleProof, targetLeaf, nonce, roleRequired, threshold)
	if err != nil {
		return nil, fmt.Errorf("building assignment: %w", err)
	}

	w, err := frontend.NewWitness(assignment, ecc.BN254.ScalarField())
	if err != nil {
		return nil, fmt.Errorf("creating witness: %w", err)
	}

	proof, err := plonk.Prove(p.ccs, p.provingKey, w)
	if err != nil {
		return nil, fmt.Errorf("generating proof: %w", err)
	}

	var buf bytes.Buffer
	if _, err := proof.WriteTo(&buf); err != nil {
		return nil, fmt.Errorf("serializing proof: %w", err)
	}
	return buf.Bytes(), nil
}

// RangeVerifier validates ZKP range proofs.
type RangeVerifier struct {
	verifyingKey plonk.VerifyingKey
}

// NewRangeVerifierFromKey creates a RangeVerifier from a pre-loaded key.
func NewRangeVerifierFromKey(verifyingKey plonk.VerifyingKey) *RangeVerifier {
	return &RangeVerifier{verifyingKey: verifyingKey}
}

// Verify checks a range proof against the given public inputs.
func (v *RangeVerifier) Verify(proofBytes []byte, merkleRoot []byte, nonce, roleRequired uint64, threshold, treeDepth int) error {
	proof := plonk.NewProof(curveID())
	if _, err := proof.ReadFrom(bytes.NewReader(proofBytes)); err != nil {
		return fmt.Errorf("deserializing proof: %w", err)
	}

	root := merkleRoot
	if treeDepth < MaxTreeDepth {
		var extErr error
		root, extErr = ExtendRoot(merkleRoot, treeDepth)
		if extErr != nil {
			return fmt.Errorf("extending root: %w", extErr)
		}
	}

	pubAssignment := &RangeProofCircuit{}
	var rootFe fr.Element
	rootFe.SetBytes(root)
	pubAssignment.MerkleRoot = rootFe
	pubAssignment.Nonce = nonce
	pubAssignment.RoleRequired = roleRequired
	pubAssignment.Threshold = threshold

	pubWitness, err := frontend.NewWitness(pubAssignment, ecc.BN254.ScalarField(), frontend.PublicOnly())
	if err != nil {
		return fmt.Errorf("creating public witness: %w", err)
	}

	if err := plonk.Verify(proof, v.verifyingKey, pubWitness); err != nil {
		return ErrInvalidProof
	}
	return nil
}

// buildRangeAssignment creates a RangeProofCircuit assignment.
// Score comes from the leaf (committed in tree), not from caller.
func buildRangeAssignment(tree *MerkleTree, proof *MerkleProof, leaf MerkleLeaf, nonce, roleRequired uint64, threshold int) (*RangeProofCircuit, error) {
	assignment := &RangeProofCircuit{}

	// Public inputs.
	var rootFe fr.Element
	rootFe.SetBytes(tree.Root)
	assignment.MerkleRoot = rootFe
	assignment.Nonce = nonce
	assignment.RoleRequired = roleRequired
	assignment.Threshold = threshold

	// Private witness. Score is the committed value from the leaf.
	for i := 0; i < 32; i++ {
		assignment.PubKeyBytes[i] = uint64(leaf.PubKeyBytes[i])
	}
	assignment.RoleEncoding = roleEncoding(leaf.Role)
	assignment.Score = leaf.Score

	// Merkle path with padding.
	for i := 0; i < MaxTreeDepth; i++ {
		if i < len(proof.Path) {
			var pathFe fr.Element
			pathFe.SetBytes(proof.Path[i])
			assignment.Path[i] = pathFe
			if proof.PathBits[i] {
				assignment.PathBits[i] = 1
			} else {
				assignment.PathBits[i] = 0
			}
		} else {
			assignment.Path[i] = 0
			assignment.PathBits[i] = 0
		}
	}

	// Extend root for shallow trees.
	if tree.Depth < MaxTreeDepth {
		extRoot, err := ExtendRoot(tree.Root, tree.Depth)
		if err != nil {
			return nil, fmt.Errorf("extending root: %w", err)
		}
		var extRootFe fr.Element
		extRootFe.SetBytes(extRoot)
		assignment.MerkleRoot = extRootFe
	}

	return assignment, nil
}
