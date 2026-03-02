package zkp

import (
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/scs"
	"github.com/consensys/gnark/test"
	"github.com/consensys/gnark/test/unsafekzg"
)

// makeRangeAssignment builds a range proof circuit assignment for testing.
// Score comes from the leaf (committed in tree), not passed separately.
func makeRangeAssignment(tree *MerkleTree, proof *MerkleProof, leaf MerkleLeaf, nonce, roleRequired uint64, threshold int) *RangeProofCircuit {
	assignment := &RangeProofCircuit{}

	var rootFe fr.Element
	rootFe.SetBytes(tree.Root)
	assignment.MerkleRoot = rootFe
	assignment.Nonce = nonce
	assignment.RoleRequired = roleRequired
	assignment.Threshold = threshold
	assignment.Score = leaf.Score // committed score from tree

	for i := 0; i < 32; i++ {
		assignment.PubKeyBytes[i] = uint64(leaf.PubKeyBytes[i])
	}
	assignment.RoleEncoding = roleEncoding(leaf.Role)

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

	return assignment
}

// precomputeRangePaddingPath computes the extended root for range proof tests.
func precomputeRangePaddingPath(assignment *RangeProofCircuit, tree *MerkleTree) {
	if tree.Depth >= MaxTreeDepth {
		return
	}
	current := tree.Root
	for i := tree.Depth; i < MaxTreeDepth; i++ {
		var zeroFe fr.Element
		zeroBytes := zeroFe.Marshal()
		h, err := NativePoseidon2Pair(current, zeroBytes)
		if err != nil {
			panic(err)
		}
		current = h
	}
	var extendedRoot fr.Element
	extendedRoot.SetBytes(current)
	assignment.MerkleRoot = extendedRoot
}

func TestRangeProofCircuitSolves(t *testing.T) {
	// Target peer has committed score=75, threshold=50. Should pass.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 8, "member", 75)
	assignment := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("range proof circuit not satisfied: %v", err)
	}
}

func TestRangeProofScoreBelowThresholdFails(t *testing.T) {
	// Target peer has committed score=40, threshold=50. Should fail.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 8, "member", 40)
	assignment := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("score below threshold should not satisfy the circuit")
	}
}

func TestRangeProofScoreEqualsThreshold(t *testing.T) {
	// Target peer has committed score=50, threshold=50. Should pass.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 4, "admin", 50)
	assignment := makeRangeAssignment(tree, proof, leaf, 99, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("score == threshold should pass: %v", err)
	}
}

func TestRangeProofScoreAbove100Fails(t *testing.T) {
	// Target peer has committed score=101. Circuit should reject (score > 100).
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 4, "member", 101)
	assignment := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("score > 100 should not satisfy the circuit")
	}
}

func TestRangeProofWithRoleCheck(t *testing.T) {
	// Admin with committed score=80, threshold=30. Should pass.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 8, "admin", 80)
	assignment := makeRangeAssignment(tree, proof, leaf, 55, 1, 30)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("admin role + range proof should pass: %v", err)
	}
}

func TestRangeProofMemberCannotProveAdmin(t *testing.T) {
	// Member with committed score=100, tries admin role. Should fail on role.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 8, "member", 100)
	assignment := makeRangeAssignment(tree, proof, leaf, 55, 1, 0)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("member should not prove admin role even with max score")
	}
}

func TestRangeProofZeroThreshold(t *testing.T) {
	// Any committed score >= 0 should pass with threshold=0.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 4, "member", 0)
	assignment := makeRangeAssignment(tree, proof, leaf, 1, 0, 0)
	precomputeRangePaddingPath(assignment, tree)

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("zero threshold should always pass: %v", err)
	}
}

func TestRangeProofScoreBindingEnforced(t *testing.T) {
	// Build tree with committed score=75.
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 8, "member", 75)
	assignment := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	// Should pass with correct committed score.
	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("committed score proof should pass: %v", err)
	}

	// Tamper: change score in witness to 90 (different from committed 75).
	// The leaf hash includes 75, so hashing with 90 produces a wrong leaf.
	// Merkle proof fails because the tampered leaf isn't in the tree.
	assignment2 := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment2, tree)
	assignment2.Score = 90 // tampered: committed is 75

	err = test.IsSolved(circuit, assignment2, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("tampered score should not satisfy the circuit (binding violated)")
	}
}

func TestRangeProofConstraintCount(t *testing.T) {
	circuit := &RangeProofCircuit{}
	cs, err := frontend.Compile(ecc.BN254.ScalarField(), scs.NewBuilder, circuit)
	if err != nil {
		t.Fatalf("compiling range proof circuit: %v", err)
	}

	nbConstraints := cs.GetNbConstraints()
	t.Logf("RangeProofCircuit constraints: %d", nbConstraints)

	// With 34-element leaf, expect ~27,000 SCS constraints (membership ~22,800
	// + range comparison ~4,200).
	if nbConstraints > 35000 {
		t.Fatalf("constraint count %d exceeds limit of 35,000", nbConstraints)
	}
	if nbConstraints < 22000 {
		t.Fatalf("constraint count %d suspiciously low (expected > 22,000)", nbConstraints)
	}
}

func TestRangeProofWrongPubkeyFails(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProofWithScore(t, 4, "member", 75)
	assignment := makeRangeAssignment(tree, proof, leaf, 42, 0, 50)
	precomputeRangePaddingPath(assignment, tree)

	// Tamper with pubkey.
	assignment.PubKeyBytes[0] = 255

	circuit := &RangeProofCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("wrong pubkey should not satisfy the circuit")
	}
}

// Ensure the circuit satisfies the gnark interface.
var _ frontend.Circuit = (*RangeProofCircuit)(nil)

// --- End-to-end PLONK test (slow) ---

// setupRangePlonk compiles the range proof circuit and sets up PLONK keys.
func setupRangePlonk(t *testing.T) (plonk.ProvingKey, plonk.VerifyingKey, *RangeProofCircuit) {
	t.Helper()
	ccs, err := CompileRangeProofCircuit()
	if err != nil {
		t.Fatalf("compiling range proof circuit: %v", err)
	}
	t.Logf("Range proof circuit compiled: %d constraints", ccs.GetNbConstraints())

	srs, srsLagrange, err := unsafekzg.NewSRS(ccs, unsafekzg.WithFSCache())
	if err != nil {
		t.Fatalf("generating SRS: %v", err)
	}

	provingKey, verifyingKey, err := plonk.Setup(ccs, srs, srsLagrange)
	if err != nil {
		t.Fatalf("PLONK setup: %v", err)
	}
	return provingKey, verifyingKey, &RangeProofCircuit{}
}

func TestEndToEndRangeProof(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end range proof test in short mode")
	}

	provingKey, verifyingKey, _ := setupRangePlonk(t)
	ccs, _ := CompileRangeProofCircuit()

	prover := NewRangeProverFromKeys(ccs, provingKey)
	verifier := NewRangeVerifierFromKey(verifyingKey)

	// Build tree: 1 admin (score=75) + 3 members (score=60).
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		score := 60
		if i == 0 {
			role = "admin"
			score = 75
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, score)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	// Prove: committed score=75, threshold=50.
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, 42, 0, 50)
	if err != nil {
		t.Fatalf("proving: %v", err)
	}
	t.Logf("Range proof size: %d bytes", len(proofBytes))

	// Verify.
	err = verifier.Verify(proofBytes, tree.Root, 42, 0, 50, tree.Depth)
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
}

func TestEndToEndRangeProofBelowThreshold(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end range proof test in short mode")
	}

	provingKey, verifyingKey, _ := setupRangePlonk(t)
	ccs, _ := CompileRangeProofCircuit()

	prover := NewRangeProverFromKeys(ccs, provingKey)
	verifier := NewRangeVerifierFromKey(verifyingKey)

	// Build tree with committed score=60 for all peers.
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 60)
	}

	tree, _ := BuildMerkleTreeFromLeaves(leaves)

	// Prove with threshold=70: committed score=60 < threshold=70.
	// Prove should fail because circuit won't be satisfied.
	_, err := prover.Prove(tree, leaves[0].PeerID, 1, 0, 70)
	if err == nil {
		t.Fatal("proving with committed score < threshold should fail")
	}
	t.Logf("Expected prove failure: %v", err)

	// Prove with threshold=50: committed score=60 >= threshold=50. Should pass.
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, 1, 0, 50)
	if err != nil {
		t.Fatalf("proving with score=60, threshold=50: %v", err)
	}

	// Verify with original threshold (50) should pass.
	err = verifier.Verify(proofBytes, tree.Root, 1, 0, 50, tree.Depth)
	if err != nil {
		t.Fatalf("verify with matching threshold: %v", err)
	}

	// Verify with higher threshold (70) should fail.
	err = verifier.Verify(proofBytes, tree.Root, 1, 0, 70, tree.Depth)
	if err == nil {
		t.Fatal("verify with higher threshold should fail")
	}
}
