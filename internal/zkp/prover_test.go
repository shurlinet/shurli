package zkp

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/consensys/gnark/backend/plonk"
	"github.com/consensys/gnark/test/unsafekzg"
)

// setupPlonk compiles the circuit, generates SRS, and runs PLONK setup.
// Returns proving key, verifying key. Cached on disk for test performance.
func setupPlonk(t *testing.T) (plonk.ProvingKey, plonk.VerifyingKey) {
	t.Helper()

	// Use a temp dir for test key caching.
	cacheDir := filepath.Join(os.TempDir(), "shurli-zkp-test")

	// Compile circuit.
	ccs, err := CompileCircuit()
	if err != nil {
		t.Fatalf("compiling circuit: %v", err)
	}
	t.Logf("Circuit compiled: %d constraints", ccs.GetNbConstraints())

	// Generate SRS (cached).
	srs, srsLagrange, err := unsafekzg.NewSRS(ccs,
		unsafekzg.WithFSCache(),
		unsafekzg.WithCacheDir(cacheDir),
	)
	if err != nil {
		t.Fatalf("generating SRS: %v", err)
	}

	// PLONK setup.
	provingKey, verifyingKey, err := plonk.Setup(ccs, srs, srsLagrange)
	if err != nil {
		t.Fatalf("PLONK setup: %v", err)
	}

	return provingKey, verifyingKey
}

func TestEndToEndProveVerify(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end PLONK test in short mode")
	}

	provingKey, verifyingKey := setupPlonk(t)

	// Compile circuit for the prover.
	ccs, err := CompileCircuit()
	if err != nil {
		t.Fatal(err)
	}

	prover := NewProverFromKeys(ccs, provingKey)
	verifier := NewVerifierFromKey(verifyingKey)

	// Build a tree with 8 peers. Scores committed in leaves.
	leaves := make([]MerkleLeaf, 8)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		if i == 0 {
			role = "admin"
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, 50+i*5)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	// Prove membership for the admin (first peer).
	nonce := uint64(42)
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, nonce, 0)
	if err != nil {
		t.Fatalf("proving: %v", err)
	}

	t.Logf("Proof size: %d bytes", len(proofBytes))

	// Verify.
	err = verifier.Verify(proofBytes, tree.Root, nonce, 0, tree.Depth)
	if err != nil {
		t.Fatalf("verification failed: %v", err)
	}
}

func TestEndToEndRoleProof(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping end-to-end role proof test in short mode")
	}

	provingKey, verifyingKey := setupPlonk(t)
	ccs, _ := CompileCircuit()

	prover := NewProverFromKeys(ccs, provingKey)
	verifier := NewVerifierFromKey(verifyingKey)

	// Build tree: 1 admin + 3 members.
	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		role := "member"
		if i == 0 {
			role = "admin"
		}
		leaves[i] = makeLeaf(t, pid, pubkey, role, 50)
	}

	tree, err := BuildMerkleTreeFromLeaves(leaves)
	if err != nil {
		t.Fatal(err)
	}

	// Admin proves admin role.
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, 1, 1)
	if err != nil {
		t.Fatalf("admin role proof: %v", err)
	}
	if err := verifier.Verify(proofBytes, tree.Root, 1, 1, tree.Depth); err != nil {
		t.Fatalf("admin role verification failed: %v", err)
	}

	// Member proves any-role (should pass).
	proofBytes, err = prover.Prove(tree, leaves[1].PeerID, 2, 0)
	if err != nil {
		t.Fatalf("member any-role proof: %v", err)
	}
	if err := verifier.Verify(proofBytes, tree.Root, 2, 0, tree.Depth); err != nil {
		t.Fatalf("member any-role verification failed: %v", err)
	}
}

func TestEndToEndWrongNonceFails(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping wrong nonce test in short mode")
	}

	provingKey, verifyingKey := setupPlonk(t)
	ccs, _ := CompileCircuit()

	prover := NewProverFromKeys(ccs, provingKey)
	verifier := NewVerifierFromKey(verifyingKey)

	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, _ := BuildMerkleTreeFromLeaves(leaves)

	// Prove with nonce=100.
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}

	// Verify with different nonce=999 (should fail).
	err = verifier.Verify(proofBytes, tree.Root, 999, 0, tree.Depth)
	if err == nil {
		t.Fatal("verification should fail with wrong nonce")
	}
}

func TestKeySerialization(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping key serialization test in short mode")
	}

	provingKey, verifyingKey := setupPlonk(t)

	dir := t.TempDir()

	// Save proving and verifying keys (CCS is always recompiled, not serialized).
	if err := SaveProvingKey(provingKey, dir); err != nil {
		t.Fatalf("saving proving key: %v", err)
	}
	if err := SaveVerifyingKey(verifyingKey, dir); err != nil {
		t.Fatalf("saving verifying key: %v", err)
	}

	if !KeysExist(dir) {
		t.Fatal("keys should exist after saving")
	}

	// Load keys and recompile circuit.
	loadedProvingKey, err := LoadProvingKey(dir)
	if err != nil {
		t.Fatalf("loading proving key: %v", err)
	}
	loadedVerifyingKey, err := LoadVerifyingKey(dir)
	if err != nil {
		t.Fatalf("loading verifying key: %v", err)
	}
	ccs2, err := CompileCircuit()
	if err != nil {
		t.Fatalf("recompiling circuit: %v", err)
	}

	// Prove with loaded keys + recompiled circuit.
	prover := NewProverFromKeys(ccs2, loadedProvingKey)
	verifier := NewVerifierFromKey(loadedVerifyingKey)

	leaves := make([]MerkleLeaf, 4)
	for i := range leaves {
		pid, pubkey := generateTestPeer(t)
		leaves[i] = makeLeaf(t, pid, pubkey, "member", 50)
	}

	tree, _ := BuildMerkleTreeFromLeaves(leaves)
	proofBytes, err := prover.Prove(tree, leaves[0].PeerID, 55, 0)
	if err != nil {
		t.Fatalf("prove with loaded keys: %v", err)
	}

	if err := verifier.Verify(proofBytes, tree.Root, 55, 0, tree.Depth); err != nil {
		t.Fatalf("verify with loaded keys: %v", err)
	}

	// Log file sizes for documentation.
	for _, name := range []string{provingKeyFile, verifyingKeyFile} {
		info, _ := os.Stat(filepath.Join(dir, name))
		t.Logf("%s: %d bytes (%.1f KB)", name, info.Size(), float64(info.Size())/1024)
	}
}

func TestKeysNotExist(t *testing.T) {
	dir := t.TempDir()
	if KeysExist(dir) {
		t.Fatal("keys should not exist in empty dir")
	}

	_, err := LoadProvingKey(dir)
	if err != ErrCircuitNotCompiled {
		t.Fatalf("expected ErrCircuitNotCompiled, got %v", err)
	}
}
