package zkp

import (
	"crypto/rand"
	"math/big"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/frontend/cs/scs"
	"github.com/consensys/gnark/test"
)

// buildTestTreeAndProof creates a Merkle tree with n peers and returns
// the tree, a valid proof for the first peer, and its leaf.
// The target peer gets the specified role and a default score of 50.
func buildTestTreeAndProof(t *testing.T, n int, targetRole string) (*MerkleTree, *MerkleProof, MerkleLeaf) {
	return buildTestTreeAndProofWithScore(t, n, targetRole, 50)
}

// buildTestTreeAndProofWithScore creates a Merkle tree with n peers where the
// target peer (index 0) gets the specified role and committed score.
func buildTestTreeAndProofWithScore(t *testing.T, n int, targetRole string, targetScore int) (*MerkleTree, *MerkleProof, MerkleLeaf) {
	t.Helper()
	leaves := make([]MerkleLeaf, n)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		score := 50
		if i == 0 {
			role = targetRole
			score = targetScore
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, score)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	// The target is leaves[0], but after sorting it may be at a different index.
	// Find it by peer ID.
	proof, err := tree.GenerateProof(leaves[0].PeerID)
	if err != nil {
		t.Fatalf("generating proof: %v", err)
	}

	// Find the actual leaf in sorted order.
	var targetLeaf MerkleLeaf
	for _, l := range tree.Leaves {
		if l.PeerID == leaves[0].PeerID {
			targetLeaf = l
			break
		}
	}

	return tree, proof, targetLeaf
}

// makeAssignment converts a native MerkleProof into a circuit assignment.
func makeAssignment(tree *MerkleTree, proof *MerkleProof, leaf MerkleLeaf, nonce uint64, roleRequired uint64) *MembershipCircuit {
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

	// Merkle path: pad proof to MaxTreeDepth.
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

// precomputePaddingPath fills the unused Merkle levels so the circuit
// "walks" correctly from the real root through padding levels back to itself.
func precomputePaddingPath(assignment *MembershipCircuit, tree *MerkleTree) {
	if tree.Depth >= MaxTreeDepth {
		return // no padding needed
	}

	current := tree.Root
	for i := tree.Depth; i < MaxTreeDepth; i++ {
		var zeroFe fr.Element // zero
		zeroBytes := zeroFe.Marshal()
		h, err := NativePoseidon2Pair(current, zeroBytes)
		if err != nil {
			panic(err)
		}
		current = h
	}

	// Set the "extended root" as the public MerkleRoot.
	var extendedRoot fr.Element
	extendedRoot.SetBytes(current)
	assignment.MerkleRoot = extendedRoot
}

func TestMembershipCircuitSolves(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "admin")
	assignment := makeAssignment(tree, proof, leaf, 12345, 0)
	precomputePaddingPath(assignment, tree)

	circuit := &MembershipCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("membership circuit not satisfied: %v", err)
	}
}

func TestMembershipCircuitAdminRoleCheck(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "admin")

	// Prove admin role (roleRequired=1).
	assignment := makeAssignment(tree, proof, leaf, 99, 1)
	precomputePaddingPath(assignment, tree)

	circuit := &MembershipCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("admin role proof should pass: %v", err)
	}
}

func TestMembershipCircuitMemberCannotProveAdmin(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "member")

	// Member tries to prove admin (roleRequired=1) - should fail.
	assignment := makeAssignment(tree, proof, leaf, 99, 1)
	precomputePaddingPath(assignment, tree)

	circuit := &MembershipCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("member should not be able to prove admin role")
	}
}

func TestMembershipCircuitAnyRoleAcceptsBoth(t *testing.T) {
	// Admin with roleRequired=0 (any).
	tree1, proof1, leaf1 := buildTestTreeAndProof(t, 4, "admin")
	assignment1 := makeAssignment(tree1, proof1, leaf1, 1, 0)
	precomputePaddingPath(assignment1, tree1)

	circuit := &MembershipCircuit{}
	if err := test.IsSolved(circuit, assignment1, ecc.BN254.ScalarField()); err != nil {
		t.Fatalf("admin with any-role should pass: %v", err)
	}

	// Member with roleRequired=0 (any).
	tree2, proof2, leaf2 := buildTestTreeAndProof(t, 4, "member")
	assignment2 := makeAssignment(tree2, proof2, leaf2, 2, 0)
	precomputePaddingPath(assignment2, tree2)

	if err := test.IsSolved(circuit, assignment2, ecc.BN254.ScalarField()); err != nil {
		t.Fatalf("member with any-role should pass: %v", err)
	}
}

func TestMembershipCircuitWrongPubkeyFails(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "admin")
	assignment := makeAssignment(tree, proof, leaf, 1, 0)
	precomputePaddingPath(assignment, tree)

	// Tamper with the public key (change first byte).
	assignment.PubKeyBytes[0] = 255

	circuit := &MembershipCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("wrong pubkey should not satisfy the circuit")
	}
}

func TestMembershipCircuitWrongRootFails(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "member")
	assignment := makeAssignment(tree, proof, leaf, 1, 0)
	precomputePaddingPath(assignment, tree)

	// Tamper with the root.
	assignment.MerkleRoot = 999999

	circuit := &MembershipCircuit{}
	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("wrong root should not satisfy the circuit")
	}
}

func TestMembershipScoreTamperFails(t *testing.T) {
	tree, proof, leaf := buildTestTreeAndProof(t, 8, "member")
	assignment := makeAssignment(tree, proof, leaf, 42, 0)
	precomputePaddingPath(assignment, tree)

	// Should pass with correct committed score.
	circuit := &MembershipCircuit{}
	if err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField()); err != nil {
		t.Fatalf("correct score should pass: %v", err)
	}

	// Tamper with score: change from committed value (50) to 999.
	// Leaf hash now won't match any leaf in the tree.
	assignment2 := makeAssignment(tree, proof, leaf, 42, 0)
	precomputePaddingPath(assignment2, tree)
	assignment2.Score = 999

	if err := test.IsSolved(circuit, assignment2, ecc.BN254.ScalarField()); err == nil {
		t.Fatal("tampered score should fail membership proof (binding violated)")
	}
}

func TestMembershipCircuitConstraintCount(t *testing.T) {
	circuit := &MembershipCircuit{}
	cs, err := frontend.Compile(ecc.BN254.ScalarField(), scs.NewBuilder, circuit)
	if err != nil {
		t.Fatalf("compiling circuit: %v", err)
	}

	nbConstraints := cs.GetNbConstraints()
	t.Logf("MembershipCircuit constraints: %d", nbConstraints)

	// With 34-element leaf (pubkey[32] + role + score), expect ~22,800 SCS
	// constraints. The extra element adds ~420 constraints.
	if nbConstraints > 30000 {
		t.Fatalf("constraint count %d exceeds limit of 30,000", nbConstraints)
	}
	if nbConstraints < 100 {
		t.Fatalf("constraint count %d suspiciously low", nbConstraints)
	}
}

func BenchmarkCircuitCompile(b *testing.B) {
	circuit := &MembershipCircuit{}
	for i := 0; i < b.N; i++ {
		frontend.Compile(ecc.BN254.ScalarField(), scs.NewBuilder, circuit)
	}
}

// Ensure the circuit satisfies the gnark interface.
var _ frontend.Circuit = (*MembershipCircuit)(nil)

// Suppress unused import warnings.
var _ = big.NewInt
var _ = rand.Reader
