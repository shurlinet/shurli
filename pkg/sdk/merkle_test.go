package sdk

import (
	"testing"
)

func TestMerkleRootEmpty(t *testing.T) {
	root := MerkleRoot(nil)
	if root != [32]byte{} {
		t.Error("expected zero hash for empty input")
	}
}

func TestMerkleRootSingle(t *testing.T) {
	hash := blake3Sum([]byte("single chunk"))
	root := MerkleRoot([][32]byte{hash})
	if root != hash {
		t.Error("single hash should be returned as-is")
	}
}

func TestMerkleRootPair(t *testing.T) {
	h1 := blake3Sum([]byte("chunk1"))
	h2 := blake3Sum([]byte("chunk2"))
	root := MerkleRoot([][32]byte{h1, h2})

	// Root should be BLAKE3(h1 || h2).
	var combined [64]byte
	copy(combined[:32], h1[:])
	copy(combined[32:], h2[:])
	expected := blake3Sum(combined[:])
	if root != expected {
		t.Error("pair root mismatch")
	}
}

func TestMerkleRootOdd(t *testing.T) {
	h1 := blake3Sum([]byte("a"))
	h2 := blake3Sum([]byte("b"))
	h3 := blake3Sum([]byte("c"))

	root := MerkleRoot([][32]byte{h1, h2, h3})

	// Level 1: pair(h1,h2), promote h3
	var combined [64]byte
	copy(combined[:32], h1[:])
	copy(combined[32:], h2[:])
	pair12 := blake3Sum(combined[:])

	// Level 2: pair(pair12, h3)
	copy(combined[:32], pair12[:])
	copy(combined[32:], h3[:])
	expected := blake3Sum(combined[:])

	if root != expected {
		t.Error("odd root mismatch")
	}
}

func TestMerkleRootDeterministic(t *testing.T) {
	hashes := make([][32]byte, 7)
	for i := range hashes {
		hashes[i] = blake3Sum([]byte{byte(i)})
	}

	r1 := MerkleRoot(hashes)
	r2 := MerkleRoot(hashes)
	if r1 != r2 {
		t.Error("Merkle root should be deterministic")
	}
}

func TestMerkleRootDoesNotMutateInput(t *testing.T) {
	hashes := make([][32]byte, 4)
	for i := range hashes {
		hashes[i] = blake3Sum([]byte{byte(i)})
	}

	// Save original.
	original := make([][32]byte, len(hashes))
	copy(original, hashes)

	MerkleRoot(hashes)

	for i := range hashes {
		if hashes[i] != original[i] {
			t.Errorf("MerkleRoot mutated input at index %d", i)
		}
	}
}
