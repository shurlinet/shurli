package p2pnet

import (
	"bytes"
	"testing"
)

// --- Erasure params tests ---

func TestComputeErasureParams(t *testing.T) {
	tests := []struct {
		name        string
		dataCount   int
		overhead    float64
		wantStripe  int
		wantParity  int
	}{
		{"zero overhead", 10, 0, 0, 0},
		{"zero data", 0, 0.1, 0, 0},
		{"small file 10 chunks 10%", 10, 0.1, 10, 1},
		{"small file 5 chunks 10%", 5, 0.1, 5, 1},
		{"exact stripe boundary", 100, 0.1, 100, 10},
		{"two stripes", 200, 0.1, 100, 20},
		{"partial stripe", 150, 0.1, 100, 15}, // 100->10 + 50->5
		{"high overhead capped", 10, 0.9, 10, 5}, // capped at 50%
		{"single chunk", 1, 0.1, 1, 1},           // min 1 parity
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ep := computeErasureParams(tt.dataCount, tt.overhead)
			if ep.StripeSize != tt.wantStripe {
				t.Errorf("stripeSize: got %d, want %d", ep.StripeSize, tt.wantStripe)
			}
			if ep.ParityCount != tt.wantParity {
				t.Errorf("parityCount: got %d, want %d", ep.ParityCount, tt.wantParity)
			}
		})
	}
}

// --- Encode/decode roundtrip ---

func TestEncodeStripeRoundtrip(t *testing.T) {
	// Create 5 data chunks of varying sizes.
	dataChunks := [][]byte{
		bytes.Repeat([]byte("A"), 100),
		bytes.Repeat([]byte("B"), 80),
		bytes.Repeat([]byte("C"), 120),
		bytes.Repeat([]byte("D"), 90),
		bytes.Repeat([]byte("E"), 110),
	}

	parityCount := 2
	parity, err := encodeStripe(dataChunks, parityCount)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	if len(parity) != parityCount {
		t.Fatalf("parity count: got %d, want %d", len(parity), parityCount)
	}

	// Simulate losing chunk 1 and reconstruct.
	dataSizes := []uint32{100, 80, 120, 90, 110}
	maxSize := 120 // max chunk size

	dataShards := make([][]byte, len(dataChunks))
	for i, c := range dataChunks {
		shard := make([]byte, maxSize)
		copy(shard, c)
		dataShards[i] = shard
	}

	// "Lose" chunk 1.
	dataShards[1] = nil

	parityShards := make([][]byte, parityCount)
	for i, p := range parity {
		shard := make([]byte, maxSize)
		copy(shard, p)
		parityShards[i] = shard
	}

	reconstructed, err := reconstructStripe(dataShards, parityShards, dataSizes)
	if err != nil {
		t.Fatalf("reconstructStripe: %v", err)
	}

	// Verify chunk 1 was recovered.
	if !bytes.Equal(reconstructed[1], dataChunks[1]) {
		t.Errorf("chunk 1 not recovered correctly: got %d bytes, want %d", len(reconstructed[1]), len(dataChunks[1]))
	}

	// Verify all chunks match.
	for i, c := range dataChunks {
		if !bytes.Equal(reconstructed[i], c) {
			t.Errorf("chunk %d mismatch after reconstruction", i)
		}
	}
}

func TestEncodeStripeMultipleLosses(t *testing.T) {
	dataChunks := make([][]byte, 10)
	for i := range dataChunks {
		dataChunks[i] = bytes.Repeat([]byte{byte(i)}, 64)
	}

	parityCount := 3
	parity, err := encodeStripe(dataChunks, parityCount)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}

	dataSizes := make([]uint32, 10)
	for i := range dataSizes {
		dataSizes[i] = 64
	}

	maxSize := 64
	dataShards := make([][]byte, 10)
	for i, c := range dataChunks {
		shard := make([]byte, maxSize)
		copy(shard, c)
		dataShards[i] = shard
	}

	// Lose 3 chunks (maximum recoverable with 3 parity).
	dataShards[2] = nil
	dataShards[5] = nil
	dataShards[8] = nil

	parityShards := make([][]byte, parityCount)
	for i, p := range parity {
		shard := make([]byte, maxSize)
		copy(shard, p)
		parityShards[i] = shard
	}

	reconstructed, err := reconstructStripe(dataShards, parityShards, dataSizes)
	if err != nil {
		t.Fatalf("reconstructStripe: %v", err)
	}

	for i, c := range dataChunks {
		if !bytes.Equal(reconstructed[i], c) {
			t.Errorf("chunk %d mismatch", i)
		}
	}
}

func TestEncodeStripeTooManyLosses(t *testing.T) {
	dataChunks := make([][]byte, 5)
	for i := range dataChunks {
		dataChunks[i] = bytes.Repeat([]byte{byte(i)}, 32)
	}

	parityCount := 1
	parity, err := encodeStripe(dataChunks, parityCount)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}

	dataSizes := make([]uint32, 5)
	for i := range dataSizes {
		dataSizes[i] = 32
	}

	dataShards := make([][]byte, 5)
	for i, c := range dataChunks {
		shard := make([]byte, 32)
		copy(shard, c)
		dataShards[i] = shard
	}

	// Lose 2 chunks with only 1 parity - should fail.
	dataShards[0] = nil
	dataShards[1] = nil

	parityShards := make([][]byte, parityCount)
	for i, p := range parity {
		shard := make([]byte, 32)
		copy(shard, p)
		parityShards[i] = shard
	}

	_, err = reconstructStripe(dataShards, parityShards, dataSizes)
	if err == nil {
		t.Error("expected error when too many chunks lost")
	}
}

// --- Encode erasure (multi-stripe) ---

func TestEncodeErasure(t *testing.T) {
	chunks := make([][]byte, 10)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{byte(i)}, 50)
	}

	parity, err := encodeErasure(chunks, 10, 0.1)
	if err != nil {
		t.Fatalf("encodeErasure: %v", err)
	}

	// 10 chunks with 10% overhead = 1 parity chunk.
	if len(parity) != 1 {
		t.Errorf("parity count: got %d, want 1", len(parity))
	}

	// Verify each parity chunk has a non-zero hash.
	for i, pc := range parity {
		if pc.hash == [32]byte{} {
			t.Errorf("parity chunk %d has zero hash", i)
		}
		if len(pc.data) == 0 {
			t.Errorf("parity chunk %d has empty data", i)
		}
	}
}

func TestEncodeErasureZeroOverhead(t *testing.T) {
	chunks := [][]byte{{1, 2, 3}}
	parity, err := encodeErasure(chunks, 3, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parity != nil {
		t.Errorf("expected nil parity with zero overhead, got %d", len(parity))
	}
}

func TestEncodeErasureEmpty(t *testing.T) {
	parity, err := encodeErasure(nil, 10, 0.1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parity != nil {
		t.Error("expected nil parity for empty input")
	}
}

// --- Erasure manifest wire format ---

func TestErasureManifestRoundtrip(t *testing.T) {
	parityCount := 3
	stripeSize := 50
	parityHashes := make([][32]byte, parityCount)
	paritySizes := make([]uint32, parityCount)

	for i := 0; i < parityCount; i++ {
		parityHashes[i] = blake3Sum([]byte{byte(i)})
		paritySizes[i] = uint32(100 + i*10)
	}

	var buf bytes.Buffer
	err := writeErasureManifest(&buf, stripeSize, parityCount, parityHashes, paritySizes)
	if err != nil {
		t.Fatalf("writeErasureManifest: %v", err)
	}

	gotStripe, gotHashes, gotSizes, err := readErasureManifest(&buf)
	if err != nil {
		t.Fatalf("readErasureManifest: %v", err)
	}

	if gotStripe != stripeSize {
		t.Errorf("stripeSize: got %d, want %d", gotStripe, stripeSize)
	}
	if len(gotHashes) != parityCount {
		t.Fatalf("hash count: got %d, want %d", len(gotHashes), parityCount)
	}
	if len(gotSizes) != parityCount {
		t.Fatalf("size count: got %d, want %d", len(gotSizes), parityCount)
	}

	for i := 0; i < parityCount; i++ {
		if gotHashes[i] != parityHashes[i] {
			t.Errorf("hash %d mismatch", i)
		}
		if gotSizes[i] != paritySizes[i] {
			t.Errorf("size %d: got %d, want %d", i, gotSizes[i], paritySizes[i])
		}
	}
}

func TestErasureManifestMismatchCount(t *testing.T) {
	var buf bytes.Buffer
	err := writeErasureManifest(&buf, 10, 3, make([][32]byte, 2), make([]uint32, 3))
	if err == nil {
		t.Error("expected error for hash/count mismatch")
	}
}

// --- Full manifest with erasure flag ---

func TestManifestWithErasureRoundtrip(t *testing.T) {
	hashes := make([][32]byte, 3)
	for i := range hashes {
		hashes[i] = blake3Sum([]byte{byte(i)})
	}

	parityHashes := make([][32]byte, 1)
	parityHashes[0] = blake3Sum([]byte("parity0"))

	original := &transferManifest{
		Filename:     "erasure-test.bin",
		FileSize:     3000,
		ChunkCount:   3,
		Flags:        flagCompressed | flagErasureCoded,
		RootHash:     MerkleRoot(hashes),
		ChunkHashes:  hashes,
		ChunkSizes:   []uint32{1000, 1000, 1000},
		StripeSize:   3,
		ParityCount:  1,
		ParityHashes: parityHashes,
		ParitySizes:  []uint32{1000},
	}

	var buf bytes.Buffer
	if err := writeManifest(&buf, original); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	parsed, err := readManifest(&buf)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}

	if parsed.Flags&flagErasureCoded == 0 {
		t.Error("erasure flag not set on parsed manifest")
	}
	if parsed.StripeSize != original.StripeSize {
		t.Errorf("stripeSize: got %d, want %d", parsed.StripeSize, original.StripeSize)
	}
	if parsed.ParityCount != original.ParityCount {
		t.Errorf("parityCount: got %d, want %d", parsed.ParityCount, original.ParityCount)
	}
	if len(parsed.ParityHashes) != len(original.ParityHashes) {
		t.Fatalf("parity hash count: got %d, want %d", len(parsed.ParityHashes), len(original.ParityHashes))
	}
	if parsed.ParityHashes[0] != original.ParityHashes[0] {
		t.Error("parity hash mismatch")
	}
	if len(parsed.ParitySizes) != len(original.ParitySizes) {
		t.Fatalf("parity size count: got %d, want %d", len(parsed.ParitySizes), len(original.ParitySizes))
	}
	if parsed.ParitySizes[0] != original.ParitySizes[0] {
		t.Errorf("parity size: got %d, want %d", parsed.ParitySizes[0], original.ParitySizes[0])
	}
}

func TestManifestWithoutErasureStillWorks(t *testing.T) {
	hashes := make([][32]byte, 2)
	for i := range hashes {
		hashes[i] = blake3Sum([]byte{byte(i)})
	}

	original := &transferManifest{
		Filename:    "no-erasure.bin",
		FileSize:    2000,
		ChunkCount:  2,
		Flags:       flagCompressed, // no erasure flag
		RootHash:    MerkleRoot(hashes),
		ChunkHashes: hashes,
		ChunkSizes:  []uint32{1000, 1000},
	}

	var buf bytes.Buffer
	if err := writeManifest(&buf, original); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	parsed, err := readManifest(&buf)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}

	if parsed.Flags&flagErasureCoded != 0 {
		t.Error("erasure flag should not be set")
	}
	if parsed.StripeSize != 0 {
		t.Errorf("stripeSize should be 0, got %d", parsed.StripeSize)
	}
	if parsed.ParityCount != 0 {
		t.Errorf("parityCount should be 0, got %d", parsed.ParityCount)
	}
}

func TestEncodeStripeAllEmpty(t *testing.T) {
	dataChunks := [][]byte{{}, {}, {}}
	_, err := encodeStripe(dataChunks, 1)
	if err == nil {
		t.Error("expected error for all-empty chunks")
	}
}
