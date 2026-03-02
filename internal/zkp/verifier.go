package zkp

import (
	"bytes"
	"fmt"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/frontend"
)

// Verifier validates ZKP membership proofs. It holds the verifying key
// and can check proofs against any Merkle root.
type Verifier struct {
	verifyingKey plonk.VerifyingKey
}

// NewVerifier creates a Verifier by loading the verifying key from disk.
func NewVerifier(keysDir string) (*Verifier, error) {
	verifyingKey, err := LoadVerifyingKey(keysDir)
	if err != nil {
		return nil, fmt.Errorf("loading verifying key: %w", err)
	}
	return &Verifier{verifyingKey: verifyingKey}, nil
}

// NewVerifierFromKey creates a Verifier from a pre-loaded key.
func NewVerifierFromKey(verifyingKey plonk.VerifyingKey) *Verifier {
	return &Verifier{verifyingKey: verifyingKey}
}

// Verify checks a serialized PLONK proof against the given public inputs.
//
// Parameters:
//   - proofBytes: serialized PLONK proof from Prover.Prove()
//   - merkleRoot: the expected Poseidon2 Merkle root (32 bytes)
//   - nonce: the challenge nonce that was used during proving
//   - roleRequired: 0 = any role, 1 = admin, 2 = member
//   - treeDepth: actual depth of the Merkle tree (for root extension)
//
// Returns nil on success, ErrInvalidProof on failure.
func (v *Verifier) Verify(proofBytes []byte, merkleRoot []byte, nonce uint64, roleRequired uint64, treeDepth int) error {
	// Deserialize the proof.
	proof := plonk.NewProof(curveID())
	if _, err := proof.ReadFrom(bytes.NewReader(proofBytes)); err != nil {
		return fmt.Errorf("deserializing proof: %w", err)
	}

	// Compute the extended root if the tree is shallower than MaxTreeDepth.
	root := merkleRoot
	if treeDepth < MaxTreeDepth {
		var extErr error
		root, extErr = ExtendRoot(merkleRoot, treeDepth)
		if extErr != nil {
			return fmt.Errorf("extending root: %w", extErr)
		}
	}

	// Build the public-only witness: MerkleRoot, Nonce, RoleRequired.
	pubAssignment := &MembershipCircuit{}
	var rootFe fr.Element
	rootFe.SetBytes(root)
	pubAssignment.MerkleRoot = rootFe
	pubAssignment.Nonce = nonce
	pubAssignment.RoleRequired = roleRequired

	pubWitness, err := frontend.NewWitness(pubAssignment, ecc.BN254.ScalarField(), frontend.PublicOnly())
	if err != nil {
		return fmt.Errorf("creating public witness: %w", err)
	}

	// Verify the proof.
	if err := plonk.Verify(proof, v.verifyingKey, pubWitness); err != nil {
		return ErrInvalidProof
	}

	return nil
}
