package zkp

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

// generateTestPeer creates a random Ed25519 libp2p peer for testing.
func generateTestPeer(t *testing.T) (peer.ID, [32]byte) {
	t.Helper()
	privKey, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatalf("generating Ed25519 key: %v", err)
	}
	peerID, err := peer.IDFromPrivateKey(privKey)
	if err != nil {
		t.Fatalf("creating peer ID: %v", err)
	}
	pubKey, err := peerID.ExtractPublicKey()
	if err != nil {
		t.Fatalf("extracting public key: %v", err)
	}
	raw, err := pubKey.Raw()
	if err != nil {
		t.Fatalf("getting raw pubkey: %v", err)
	}
	var pubkeyBytes [32]byte
	copy(pubkeyBytes[:], raw)
	return peerID, pubkeyBytes
}

// makeLeaf creates a MerkleLeaf for a test peer with a committed score.
func makeLeaf(t *testing.T, peerID peer.ID, pubkey [32]byte, role string, score int) MerkleLeaf {
	t.Helper()
	return MerkleLeaf{
		PeerID:      peerID,
		Role:        role,
		Score:       score,
		PubKeyBytes: pubkey,
		LeafHash:    ComputeLeafHash(pubkey, role, score),
	}
}

func TestBuildMerkleTreeDeterministic(t *testing.T) {
	// Create 5 peers.
	leaves := make([]MerkleLeaf, 5)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		if i == 0 {
			role = "admin"
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, 50+i*10)
	}

	tree1, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree 1: %v", err)
	}

	// Build again with reversed input order.
	reversed := make([]MerkleLeaf, len(leaves))
	for i, l := range leaves {
		reversed[len(leaves)-1-i] = l
	}
	tree2, err := BuildMerkleTreeFromLeaves(reversed)
	if err != nil {
		t.Fatalf("building tree 2: %v", err)
	}

	if !bytes.Equal(tree1.Root, tree2.Root) {
		t.Fatal("same peers in different order should produce the same root")
	}
}

func TestBuildMerkleTreeSinglePeer(t *testing.T) {
	pid, pubkey := generateTestPeer(t)
	leaf := makeLeaf(t, pid, pubkey, "admin", 75)

	tree, err := BuildMerkleTreeFromLeaves([]MerkleLeaf{leaf})
	if err != nil {
		t.Fatalf("building single-peer tree: %v", err)
	}

	if tree.Depth != 0 {
		t.Fatalf("single peer tree depth should be 0, got %d", tree.Depth)
	}
	if !bytes.Equal(tree.Root, leaf.LeafHash) {
		t.Fatal("single peer tree root should equal the leaf hash")
	}
	if tree.LeafCount() != 1 {
		t.Fatalf("leaf count should be 1, got %d", tree.LeafCount())
	}
}

func TestBuildMerkleTreePowerOf2(t *testing.T) {
	// Exactly 4 peers (power of 2, no padding needed).
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	if tree.Depth != 2 {
		t.Fatalf("4-leaf tree should have depth 2, got %d", tree.Depth)
	}
	if tree.LeafCount() != 4 {
		t.Fatalf("leaf count should be 4, got %d", tree.LeafCount())
	}
}

func TestBuildMerkleTreeNonPowerOf2(t *testing.T) {
	// 3 peers (padded to 4).
	leaves := make([]MerkleLeaf, 3)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	if tree.Depth != 2 {
		t.Fatalf("3-leaf tree should be padded to depth 2, got %d", tree.Depth)
	}
	if tree.LeafCount() != 3 {
		t.Fatalf("leaf count should be 3, got %d", tree.LeafCount())
	}
}

func TestBuildMerkleTreeEmpty(t *testing.T) {
	_, err := BuildMerkleTreeFromLeaves(nil)
	if err != ErrTreeEmpty {
		t.Fatalf("expected ErrTreeEmpty, got %v", err)
	}
	_, err = BuildMerkleTreeFromLeaves([]MerkleLeaf{})
	if err != ErrTreeEmpty {
		t.Fatalf("expected ErrTreeEmpty, got %v", err)
	}
}

func TestGenerateAndVerifyProof(t *testing.T) {
	// Build tree with 10 peers.
	leaves := make([]MerkleLeaf, 10)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		if i < 2 {
			role = "admin"
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, 50+i*5)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	// Verify proof for every real leaf.
	for _, leaf := range tree.Leaves {
		proof, err := tree.GenerateProof(leaf.PeerID)
		if err != nil {
			t.Fatalf("generating proof for %s: %v", leaf.PeerID, err)
		}

		ok, err := VerifyProof(proof, tree.Root)
		if err != nil {
			t.Fatalf("verifying proof for %s: %v", leaf.PeerID, err)
		}
		if !ok {
			t.Fatalf("proof verification failed for %s", leaf.PeerID)
		}
	}
}

func TestProofFailsForNonMember(t *testing.T) {
	leaves := make([]MerkleLeaf, 5)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	// Generate a peer not in the tree.
	outsider, _ := generateTestPeer(t)
	_, err = tree.GenerateProof(outsider)
	if err != ErrPeerNotInTree {
		t.Fatalf("expected ErrPeerNotInTree, got %v", err)
	}
}

func TestProofFailsWithWrongRoot(t *testing.T) {
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building tree: %v", err)
	}

	proof, err := tree.GenerateProof(tree.Leaves[0].PeerID)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with the root.
	fakeRoot := make([]byte, 32)
	copy(fakeRoot, tree.Root)
	fakeRoot[0] ^= 0xFF

	ok, err := VerifyProof(proof, fakeRoot)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("proof should fail with wrong root")
	}
}

func TestProofFailsWithTamperedPath(t *testing.T) {
	leaves := make([]MerkleLeaf, 8)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	proof, err := tree.GenerateProof(tree.Leaves[0].PeerID)
	if err != nil {
		t.Fatal(err)
	}

	// Tamper with a sibling hash.
	if len(proof.Path) > 0 {
		proof.Path[0] = make([]byte, 32) // zero out first sibling
	}

	ok, err := VerifyProof(proof, tree.Root)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("proof should fail with tampered sibling hash")
	}
}

func TestTreeAddPeerChangesRoot(t *testing.T) {
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree1, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	// Add one more peer.
	pid, pubkey := generateTestPeer(t)
	leaves = append(leaves, makeLeaf(t, pid, pubkey, "member", 50))

	tree2, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(tree1.Root, tree2.Root) {
		t.Fatal("adding a peer should change the root")
	}
}

func TestTreeRoleChangeChangesRoot(t *testing.T) {
	pid, pubkey := generateTestPeer(t)

	memberLeaf := makeLeaf(t, pid, pubkey, "member", 50)
	adminLeaf := makeLeaf(t, pid, pubkey, "admin", 50)

	tree1, err := BuildMerkleTreeFromLeaves([]MerkleLeaf{memberLeaf})
	if err != nil {
		t.Fatal(err)
	}
	tree2, err := BuildMerkleTreeFromLeaves([]MerkleLeaf{adminLeaf})
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(tree1.Root, tree2.Root) {
		t.Fatal("changing role should change the root")
	}
}

func TestTreeScoreChangeChangesRoot(t *testing.T) {
	pid, pubkey := generateTestPeer(t)

	leaf50 := makeLeaf(t, pid, pubkey, "member", 50)
	leaf75 := makeLeaf(t, pid, pubkey, "member", 75)

	tree1, err := BuildMerkleTreeFromLeaves([]MerkleLeaf{leaf50})
	if err != nil {
		t.Fatal(err)
	}
	tree2, err := BuildMerkleTreeFromLeaves([]MerkleLeaf{leaf75})
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(tree1.Root, tree2.Root) {
		t.Fatal("changing score should change the root")
	}
}

func TestNextPowerOf2(t *testing.T) {
	tests := []struct {
		input, expected int
	}{
		{0, 1},
		{1, 1},
		{2, 2},
		{3, 4},
		{4, 4},
		{5, 8},
		{7, 8},
		{8, 8},
		{9, 16},
		{15, 16},
		{16, 16},
		{17, 32},
		{1000, 1024},
		{1024, 1024},
		{1025, 2048},
	}

	for _, tc := range tests {
		got := nextPowerOf2(tc.input)
		if got != tc.expected {
			t.Errorf("nextPowerOf2(%d) = %d, want %d", tc.input, got, tc.expected)
		}
	}
}

func TestLargeTree(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large tree test in short mode")
	}

	// Build a 500-peer tree.
	const peerCount = 500
	leaves := make([]MerkleLeaf, peerCount)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50+i%51)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatalf("building large tree: %v", err)
	}

	if tree.Depth != 9 { // 500 -> padded to 512 = 2^9
		t.Fatalf("expected depth 9, got %d", tree.Depth)
	}

	// Verify a sample of proofs (first, middle, last real leaf).
	for _, idx := range []int{0, peerCount / 2, peerCount - 1} {
		proof, err := tree.GenerateProof(tree.Leaves[idx].PeerID)
		if err != nil {
			t.Fatalf("generating proof for leaf %d: %v", idx, err)
		}
		ok, err := VerifyProof(proof, tree.Root)
		if err != nil {
			t.Fatalf("verifying proof for leaf %d: %v", idx, err)
		}
		if !ok {
			t.Fatalf("proof failed for leaf %d", idx)
		}
	}
}

func BenchmarkBuildTree100(b *testing.B) {
	leaves := makeNLeaves(b, 100)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildMerkleTreeFromLeaves(leaves)
	}
}

func BenchmarkBuildTree500(b *testing.B) {
	leaves := makeNLeaves(b, 500)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		BuildMerkleTreeFromLeaves(leaves)
	}
}

func BenchmarkGenerateProof(b *testing.B) {
	leaves := makeNLeaves(b, 100)
	tree, _ := BuildMerkleTreeFromLeaves(leaves)
	targetPeer := tree.Leaves[50].PeerID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree.GenerateProof(targetPeer)
	}
}

func BenchmarkVerifyProof(b *testing.B) {
	leaves := makeNLeaves(b, 100)
	tree, _ := BuildMerkleTreeFromLeaves(leaves)
	proof, _ := tree.GenerateProof(tree.Leaves[50].PeerID)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		VerifyProof(proof, tree.Root)
	}
}

// makeNLeaves creates n random MerkleLeaf entries for benchmarking.
func makeNLeaves(tb testing.TB, n int) []MerkleLeaf {
	tb.Helper()
	leaves := make([]MerkleLeaf, n)
	for i := range leaves {
		_, privBytes, _ := ed25519.GenerateKey(rand.Reader)
		privKey, _ := crypto.UnmarshalEd25519PrivateKey(privBytes)
		peerID, _ := peer.IDFromPrivateKey(privKey)
		pubKey, _ := peerID.ExtractPublicKey()
		raw, _ := pubKey.Raw()
		var pubkey [32]byte
		copy(pubkey[:], raw)
		score := 50 + i%51 // varied scores 50-100
		leaves[i] = MerkleLeaf{
			PeerID:      peerID,
			Role:        "member",
			Score:       score,
			PubKeyBytes: pubkey,
			LeafHash:    ComputeLeafHash(pubkey, "member", score),
		}
	}
	return leaves
}
