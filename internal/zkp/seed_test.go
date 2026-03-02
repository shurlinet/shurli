package zkp

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSetupKeysFromSeed_Deterministic(t *testing.T) {
	mnemonic, err := GenerateMnemonic()
	if err != nil {
		t.Fatal(err)
	}

	// Generate keys in two separate directories from the same seed.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	if err := SetupKeysFromSeed(dir1, mnemonic); err != nil {
		t.Fatalf("setup 1: %v", err)
	}
	if err := SetupKeysFromSeed(dir2, mnemonic); err != nil {
		t.Fatalf("setup 2: %v", err)
	}

	// Proving key and verifying key files must be byte-for-byte identical.
	prov1, err := os.ReadFile(filepath.Join(dir1, "provingKey.bin"))
	if err != nil {
		t.Fatal(err)
	}
	prov2, err := os.ReadFile(filepath.Join(dir2, "provingKey.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(prov1) != len(prov2) {
		t.Fatalf("Proving key size mismatch: %d vs %d", len(prov1), len(prov2))
	}
	for i := range prov1 {
		if prov1[i] != prov2[i] {
			t.Fatalf("Proving key mismatch at byte %d", i)
		}
	}

	ver1, err := os.ReadFile(filepath.Join(dir1, "verifyingKey.bin"))
	if err != nil {
		t.Fatal(err)
	}
	ver2, err := os.ReadFile(filepath.Join(dir2, "verifyingKey.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ver1) != len(ver2) {
		t.Fatalf("Verifying key size mismatch: %d vs %d", len(ver1), len(ver2))
	}
	for i := range ver1 {
		if ver1[i] != ver2[i] {
			t.Fatalf("Verifying key mismatch at byte %d", i)
		}
	}

	t.Logf("ProvingKey: %d bytes, VerifyingKey: %d bytes - both identical from same seed", len(prov1), len(ver1))
}

func TestSetupKeysFromSeed_DifferentSeeds(t *testing.T) {
	m1, _ := GenerateMnemonic()
	m2, _ := GenerateMnemonic()

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	if err := SetupKeysFromSeed(dir1, m1); err != nil {
		t.Fatalf("setup 1: %v", err)
	}
	if err := SetupKeysFromSeed(dir2, m2); err != nil {
		t.Fatalf("setup 2: %v", err)
	}

	// Verifying key files must be different for different seeds.
	ver1, _ := os.ReadFile(filepath.Join(dir1, "verifyingKey.bin"))
	ver2, _ := os.ReadFile(filepath.Join(dir2, "verifyingKey.bin"))

	same := len(ver1) == len(ver2)
	if same {
		for i := range ver1 {
			if ver1[i] != ver2[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Fatal("different seeds produced identical verifying key files")
	}
}

func TestSetupKeysFromSeed_ProveVerifyRoundTrip(t *testing.T) {
	mnemonic, _ := GenerateMnemonic()

	// Setup keys from seed in two separate directories (simulates relay + client).
	relayDir := t.TempDir()
	clientDir := t.TempDir()

	if err := SetupKeysFromSeed(relayDir, mnemonic); err != nil {
		t.Fatalf("relay setup: %v", err)
	}
	if err := SetupKeysFromSeed(clientDir, mnemonic); err != nil {
		t.Fatalf("client setup: %v", err)
	}

	// Client creates prover from its keys.
	prover, err := NewProver(clientDir)
	if err != nil {
		t.Fatalf("creating prover: %v", err)
	}

	// Relay creates verifier from its keys.
	verifier, err := NewVerifier(relayDir)
	if err != nil {
		t.Fatalf("creating verifier: %v", err)
	}

	// Build a test tree.
	tree := buildTestTree(t)
	peerID := tree.Leaves[0].PeerID
	nonce := uint64(12345)

	// Client proves membership.
	proofBytes, err := prover.Prove(tree, peerID, nonce, 0)
	if err != nil {
		t.Fatalf("proving: %v", err)
	}

	// Relay verifies proof.
	if err := verifier.Verify(proofBytes, tree.Root, nonce, 0, tree.Depth); err != nil {
		t.Fatalf("verify failed: %v (proof from client keys, verified with relay keys)", err)
	}

	t.Log("Seed-derived keys: client proved, relay verified - PASS")
}

// buildTestTree creates a small Merkle tree for testing.
func buildTestTree(t *testing.T) *MerkleTree {
	t.Helper()
	dir := t.TempDir()
	authFile := filepath.Join(dir, "authorized_keys")
	// Use a known peer ID format.
	content := "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN admin\n"
	if err := os.WriteFile(authFile, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	tree, err := BuildMerkleTree(authFile)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
