package zkp

import (
	"bytes"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/constraint"
	"github.com/consensys/gnark/frontend"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Prover generates ZKP membership proofs. It holds the compiled circuit,
// proving key, and can produce proofs for any peer in the Merkle tree.
type Prover struct {
	ccs        constraint.ConstraintSystem
	provingKey plonk.ProvingKey
}

// NewProver creates a Prover by compiling the circuit and loading the proving
// key from the given directory. Circuit compilation is deterministic and fast
// (~70ms), so the CCS is not persisted to disk.
func NewProver(keysDir string) (*Prover, error) {
	ccs, err := CompileCircuit()
	if err != nil {
		return nil, fmt.Errorf("compiling circuit: %w", err)
	}
	provingKey, err := LoadProvingKey(keysDir)
	if err != nil {
		return nil, fmt.Errorf("loading proving key: %w", err)
	}
	return &Prover{ccs: ccs, provingKey: provingKey}, nil
}

// NewProverFromKeys creates a Prover from pre-loaded keys (for testing or
// when keys are already in memory).
func NewProverFromKeys(ccs constraint.ConstraintSystem, provingKey plonk.ProvingKey) *Prover {
	return &Prover{ccs: ccs, provingKey: provingKey}
}

// Prove generates a PLONK membership proof for the given peer.
//
// Parameters:
//   - tree: the current Merkle tree of authorized peers
//   - peerID: the peer proving their membership
//   - nonce: fresh challenge nonce (prevents replay)
//   - roleRequired: 0 = any role, 1 = admin, 2 = member
//
// Returns the serialized proof bytes.
func (p *Prover) Prove(tree *MerkleTree, peerID peer.ID, nonce uint64, roleRequired uint64) ([]byte, error) {
	// Find the peer in the tree and get their leaf data.
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

	// Generate Merkle proof.
	merkleProof, err := tree.GenerateProof(peerID)
	if err != nil {
		return nil, fmt.Errorf("generating merkle proof: %w", err)
	}

	// Build the circuit assignment.
	assignment, err := buildAssignment(tree, merkleProof, targetLeaf, nonce, roleRequired)
	if err != nil {
		return nil, fmt.Errorf("building assignment: %w", err)
	}

	// Create the full witness from the assignment.
	w, err := frontend.NewWitness(assignment, ecc.BN254.ScalarField())
	if err != nil {
		return nil, fmt.Errorf("creating witness: %w", err)
	}

	// Generate the PLONK proof.
	proof, err := plonk.Prove(p.ccs, p.provingKey, w)
	if err != nil {
		return nil, fmt.Errorf("generating proof: %w", err)
	}

	// Serialize the proof.
	proofBytes, err := serializeProof(proof)
	if err != nil {
		return nil, fmt.Errorf("serializing proof: %w", err)
	}

	return proofBytes, nil
}

// buildAssignment creates a MembershipCircuit assignment from tree data.
func buildAssignment(tree *MerkleTree, proof *MerkleProof, leaf MerkleLeaf, nonce, roleRequired uint64) (*MembershipCircuit, error) {
	assignment := &MembershipCircuit{}

	// Public inputs.
	var rootFe fr.Element
	rootFe.SetBytes(tree.Root)
	assignment.MerkleRoot = rootFe
	assignment.Nonce = nonce
	assignment.RoleRequired = roleRequired

	// Private witness: pubkey bytes, role, and committed score.
	for i := 0; i < 32; i++ {
		assignment.PubKeyBytes[i] = uint64(leaf.PubKeyBytes[i])
	}
	assignment.RoleEncoding = roleEncoding(leaf.Role)
	assignment.Score = leaf.Score

	// Merkle path with padding for unused levels.
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

	// For trees with depth < MaxTreeDepth, compute the extended root
	// that results from padding unused levels with zero siblings.
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

// ExtendRoot computes the "extended root" by hashing through padding levels.
// For trees with depth < MaxTreeDepth, the circuit continues hashing with
// zero siblings for unused levels. This function computes what the circuit
// will produce so we can set the correct public MerkleRoot.
func ExtendRoot(root []byte, treeDepth int) ([]byte, error) {
	current := root
	var zeroFe fr.Element
	zeroBytes := zeroFe.Marshal()
	for i := treeDepth; i < MaxTreeDepth; i++ {
		h, err := NativePoseidon2Pair(current, zeroBytes)
		if err != nil {
			return nil, fmt.Errorf("extending root at level %d: %w", i, err)
		}
		current = h
	}
	return current, nil
}

// serializeProof converts a gnark proof to bytes.
func serializeProof(proof plonk.Proof) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := proof.WriteTo(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
