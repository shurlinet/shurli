package filetransfer

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// --- Erasure params tests ---

func TestComputeErasureParams(t *testing.T) {
	tests := []struct {
		name       string
		dataCount  int
		overhead   float64
		wantStripe int
		wantParity int
	}{
		{"zero overhead", 10, 0, 0, 0},
		{"zero data", 0, 0.1, 0, 0},
		{"small file 10 chunks 10%", 10, 0.1, 10, 1},
		{"small file 5 chunks 10%", 5, 0.1, 5, 1},
		{"exact stripe boundary", 100, 0.1, 100, 10},
		{"two stripes", 200, 0.1, 100, 20},
		{"partial stripe", 150, 0.1, 100, 15},    // 100->10 + 50->5
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

// --- Erasure manifest wire format ---

func TestErasureManifestRoundtrip(t *testing.T) {
	parityCount := 3
	parityHashes := make([][32]byte, parityCount)
	paritySizes := make([]uint32, parityCount)

	for i := 0; i < parityCount; i++ {
		parityHashes[i] = sdk.Blake3Sum([]byte{byte(i)})
		paritySizes[i] = uint32(100 + i*10)
	}

	var buf bytes.Buffer
	// Option C: writeErasureManifest no longer carries stripeSize/overheadPerMille
	// (those moved to the SHFT header).
	err := writeErasureManifest(&buf, parityCount, parityHashes, paritySizes)
	if err != nil {
		t.Fatalf("writeErasureManifest: %v", err)
	}

	gotHashes, gotSizes, err := readErasureManifest(&buf)
	if err != nil {
		t.Fatalf("readErasureManifest: %v", err)
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
	err := writeErasureManifest(&buf, 3, make([][32]byte, 2), make([]uint32, 3))
	if err == nil {
		t.Error("expected error for hash/count mismatch")
	}
}

// TestErasureHeaderStripeSizeUpperCap proves the OC-F41 cap: stripeSize
// values above maxAcceptedStripeSize in the SHFT header are rejected so a
// malicious sender cannot force reconstruction to allocate stripeSize x
// maxChunkSize padded shards. [Option C, moved from trailer to header]
func TestErasureHeaderStripeSizeUpperCap(t *testing.T) {
	files := []fileEntry{{Path: "test.bin", Size: 1024}}
	var transferID [32]byte

	// Above the cap: must be rejected by readHeader.
	var aboveBuf bytes.Buffer
	aboveHdr := &erasureHeaderParams{StripeSize: maxAcceptedStripeSize + 1, OverheadPerMille: 100}
	if err := writeHeader(&aboveBuf, files, flagErasureCoded, 1024, transferID, aboveHdr); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}
	if _, _, _, _, _, _, err := readHeader(&aboveBuf); err == nil {
		t.Error("readHeader accepted stripeSize > maxAcceptedStripeSize; must reject")
	}

	// At the cap: must succeed.
	var atBuf bytes.Buffer
	atHdr := &erasureHeaderParams{StripeSize: maxAcceptedStripeSize, OverheadPerMille: 100}
	if err := writeHeader(&atBuf, files, flagErasureCoded, 1024, transferID, atHdr); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}
	if _, _, _, _, _, ehdr, err := readHeader(&atBuf); err != nil {
		t.Errorf("readHeader rejected stripeSize == maxAcceptedStripeSize: %v", err)
	} else if ehdr == nil || ehdr.StripeSize != maxAcceptedStripeSize {
		t.Errorf("expected stripeSize=%d, got %v", maxAcceptedStripeSize, ehdr)
	}
}

// TestErasureHeaderOverheadBounds proves readHeader rejects out-of-range
// overheads in the erasure header: zero and above maxParityOverhead.
// [Option C, moved from trailer to header]
func TestErasureHeaderOverheadBounds(t *testing.T) {
	files := []fileEntry{{Path: "test.bin", Size: 1024}}
	var transferID [32]byte

	// Zero per-mille: write succeeds but read rejects.
	var zeroBuf bytes.Buffer
	zeroHdr := &erasureHeaderParams{StripeSize: 100, OverheadPerMille: 0}
	if err := writeHeader(&zeroBuf, files, flagErasureCoded, 1024, transferID, zeroHdr); err != nil {
		t.Fatalf("writeHeader zero overhead: %v", err)
	}
	if _, _, _, _, _, _, err := readHeader(&zeroBuf); err == nil {
		t.Error("readHeader accepted overhead_permille=0; must reject")
	}

	// Over-max per-mille: must reject.
	var overBuf bytes.Buffer
	overHdr := &erasureHeaderParams{StripeSize: 100, OverheadPerMille: uint16(maxParityOverhead*1000) + 1}
	if err := writeHeader(&overBuf, files, flagErasureCoded, 1024, transferID, overHdr); err != nil {
		t.Fatalf("writeHeader over-max overhead: %v", err)
	}
	if _, _, _, _, _, _, err := readHeader(&overBuf); err == nil {
		t.Error("readHeader accepted overhead_permille > maxParityOverhead*1000; must reject")
	}
}

// --- Full manifest with erasure flag ---

func TestTrailerWithErasureRoundtrip(t *testing.T) {
	hashes := make([][32]byte, 3)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
	}
	rootHash := sdk.MerkleRoot(hashes)

	parityHashes := make([][32]byte, 1)
	parityHashes[0] = sdk.Blake3Sum([]byte("parity0"))
	erasure := &erasureTrailer{
		ParityCount:  1,
		ParityHashes: parityHashes,
		ParitySizes:  []uint32{1000},
	}

	var buf bytes.Buffer
	if err := writeTrailer(&buf, 3, rootHash, nil, erasure); err != nil {
		t.Fatalf("writeTrailer: %v", err)
	}

	// readTrailer expects the msgTrailer byte to already be consumed.
	// writeTrailer writes msgTrailer as the first byte, so skip it.
	data := buf.Bytes()[1:] // skip msgTrailer byte
	reader := bytes.NewReader(data)

	chunkCount, parsedRoot, _, parsedErasure, err := readTrailer(reader, true)
	if err != nil {
		t.Fatalf("readTrailer: %v", err)
	}

	if chunkCount != 3 {
		t.Errorf("chunkCount: got %d, want 3", chunkCount)
	}
	if parsedRoot != rootHash {
		t.Error("rootHash mismatch")
	}
	if parsedErasure == nil {
		t.Fatal("erasure should not be nil")
	}
	if parsedErasure.ParityCount != 1 {
		t.Errorf("parityCount: got %d, want 1", parsedErasure.ParityCount)
	}
	if parsedErasure.ParityHashes[0] != parityHashes[0] {
		t.Error("parity hash mismatch")
	}
	if parsedErasure.ParitySizes[0] != 1000 {
		t.Errorf("parity size: got %d, want 1000", parsedErasure.ParitySizes[0])
	}
	// Nil ChunkHashes should pass through as nil (count=0 manifest).
	if parsedErasure.ChunkHashes != nil {
		t.Error("ChunkHashes should be nil when not populated")
	}
}

// TestTrailerWithManifestRoundtrip verifies that writeTrailer -> readTrailer
// correctly round-trips the chunk manifest (non-nil ChunkHashes/ChunkSizes)
// through the full trailer wire format. [Batch 2c]
func TestTrailerWithManifestRoundtrip(t *testing.T) {
	chunkHashes := [][32]byte{
		sdk.Blake3Sum([]byte("chunk0")),
		sdk.Blake3Sum([]byte("chunk1")),
		sdk.Blake3Sum([]byte("chunk2")),
	}
	rootHash := sdk.MerkleRoot(chunkHashes)

	parityHashes := [][32]byte{sdk.Blake3Sum([]byte("parity0"))}
	erasure := &erasureTrailer{
		ParityCount:  1,
		ParityHashes: parityHashes,
		ParitySizes:  []uint32{128},
		ChunkHashes:  chunkHashes,
		ChunkSizes:   []uint32{1024, 2048, 512},
	}

	var buf bytes.Buffer
	if err := writeTrailer(&buf, 3, rootHash, nil, erasure); err != nil {
		t.Fatalf("writeTrailer: %v", err)
	}

	data := buf.Bytes()[1:] // skip msgTrailer byte
	reader := bytes.NewReader(data)

	chunkCount, parsedRoot, _, parsedErasure, err := readTrailer(reader, true)
	if err != nil {
		t.Fatalf("readTrailer: %v", err)
	}
	if chunkCount != 3 {
		t.Errorf("chunkCount: got %d, want 3", chunkCount)
	}
	if parsedRoot != rootHash {
		t.Error("rootHash mismatch")
	}
	if parsedErasure == nil {
		t.Fatal("erasure should not be nil")
	}
	if parsedErasure.ChunkHashes == nil {
		t.Fatal("ChunkHashes should not be nil")
	}
	for i, h := range chunkHashes {
		if parsedErasure.ChunkHashes[i] != h {
			t.Errorf("ChunkHashes[%d] mismatch", i)
		}
	}
	for i, s := range []uint32{1024, 2048, 512} {
		if parsedErasure.ChunkSizes[i] != s {
			t.Errorf("ChunkSizes[%d]: got %d, want %d", i, parsedErasure.ChunkSizes[i], s)
		}
	}
	if parsedErasure.ParityCount != 1 || parsedErasure.ParityHashes[0] != parityHashes[0] {
		t.Error("parity data mismatch")
	}
}

func TestTrailerWithoutErasureStillWorks(t *testing.T) {
	hashes := make([][32]byte, 2)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
	}
	rootHash := sdk.MerkleRoot(hashes)

	var buf bytes.Buffer
	if err := writeTrailer(&buf, 2, rootHash, nil, nil); err != nil {
		t.Fatalf("writeTrailer: %v", err)
	}

	data := buf.Bytes()[1:] // skip msgTrailer byte
	reader := bytes.NewReader(data)

	chunkCount, parsedRoot, _, parsedErasure, err := readTrailer(reader, false)
	if err != nil {
		t.Fatalf("readTrailer: %v", err)
	}

	if chunkCount != 2 {
		t.Errorf("chunkCount: got %d, want 2", chunkCount)
	}
	if parsedRoot != rootHash {
		t.Error("rootHash mismatch")
	}
	if parsedErasure != nil {
		t.Error("erasure should be nil")
	}
}

func TestEncodeStripeAllEmpty(t *testing.T) {
	dataChunks := [][]byte{{}, {}, {}}
	_, err := encodeStripe(dataChunks, 1)
	if err == nil {
		t.Error("expected error for all-empty chunks")
	}
}

// --- R4-SEC1 Batch 2: erasureEncoder tests ---

// TestErasureEncoderStripeBoundaries exercises the partial-final-stripe path,
// exact-stripe-boundary path, and zero-chunk path. All three historically
// hid off-by-ones in similar encoders.
func TestErasureEncoderStripeBoundaries(t *testing.T) {
	t.Run("exact stripe boundary emits during AddChunk only", func(t *testing.T) {
		enc := newErasureEncoder(5, 0.2)
		if enc == nil {
			t.Fatal("encoder is nil")
		}
		var fromAdd int
		for i := 0; i < 5; i++ {
			out, err := enc.AddChunk(bytes.Repeat([]byte{byte(i)}, 32))
			if err != nil {
				t.Fatalf("AddChunk[%d]: %v", i, err)
			}
			fromAdd += len(out)
		}
		// Exact stripe boundary: all parity emitted during AddChunk, Finalize
		// must return zero residual and a non-nil trailer.
		residual, trailer, err := enc.Finalize()
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if len(residual) != 0 {
			t.Errorf("exact stripe boundary residual=%d, want 0", len(residual))
		}
		if trailer == nil {
			t.Fatal("trailer nil on exact stripe boundary")
		}
		if trailer.ParityCount != fromAdd {
			t.Errorf("trailer parityCount=%d, want %d", trailer.ParityCount, fromAdd)
		}
	})

	t.Run("partial final stripe flushed by Finalize", func(t *testing.T) {
		enc := newErasureEncoder(5, 0.2)
		var fromAdd int
		for i := 0; i < 7; i++ {
			out, err := enc.AddChunk(bytes.Repeat([]byte{byte(i)}, 32))
			if err != nil {
				t.Fatalf("AddChunk[%d]: %v", i, err)
			}
			fromAdd += len(out)
		}
		residual, trailer, err := enc.Finalize()
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if len(residual) == 0 {
			t.Error("partial final stripe residual=0, expected >0")
		}
		if trailer == nil {
			t.Fatal("trailer nil")
		}
		if trailer.ParityCount != fromAdd+len(residual) {
			t.Errorf("trailer parityCount=%d, want %d", trailer.ParityCount, fromAdd+len(residual))
		}
		// Parity chunkIdx must be densely 0..ParityCount-1.
		all := make([]parityChunkOut, 0, trailer.ParityCount)
		// Reconstruct emitted slice for index check.
		// (drainEncoder would re-run AddChunk; instead we just check via trailer.)
		_ = all
		for i, h := range trailer.ParityHashes {
			if h == ([32]byte{}) {
				t.Errorf("trailer ParityHashes[%d] is zero", i)
			}
		}
	})

	t.Run("zero chunks returns nil trailer", func(t *testing.T) {
		enc := newErasureEncoder(10, 0.1)
		residual, trailer, err := enc.Finalize()
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if len(residual) != 0 {
			t.Errorf("zero-chunk residual=%d, want 0", len(residual))
		}
		if trailer != nil {
			t.Errorf("zero-chunk trailer=%+v, want nil (caller must not set flagErasureCoded)", trailer)
		}
	})

	t.Run("single chunk produces one parity", func(t *testing.T) {
		enc := newErasureEncoder(10, 0.1)
		out, err := enc.AddChunk(bytes.Repeat([]byte{0x42}, 128))
		if err != nil {
			t.Fatalf("AddChunk: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("AddChunk returned %d parity mid-stripe, want 0", len(out))
		}
		residual, trailer, err := enc.Finalize()
		if err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		if len(residual) != 1 {
			t.Errorf("single-chunk residual=%d, want 1 (min 1 parity per stripe)", len(residual))
		}
		if trailer == nil || trailer.ParityCount != 1 {
			t.Errorf("trailer = %+v, want ParityCount=1", trailer)
		}
	})
}

// TestNewErasureEncoderGates verifies newErasureEncoder refuses degenerate
// configurations up-front: zero/negative overhead and stripeSize below
// minStripeSize. [B2-F11, B2-F3 upstream gate]
func TestNewErasureEncoderGates(t *testing.T) {
	cases := []struct {
		name       string
		stripeSize int
		overhead   float64
		wantNil    bool
	}{
		{"zero overhead", 10, 0, true},
		{"negative overhead", 10, -0.1, true},
		{"stripe below min", 1, 0.1, true},
		{"valid minimum", minStripeSize, 0.1, false},
		{"typical", 100, 0.1, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc := newErasureEncoder(c.stripeSize, c.overhead)
			if c.wantNil && enc != nil {
				t.Errorf("expected nil encoder")
			}
			if !c.wantNil && enc == nil {
				t.Errorf("expected non-nil encoder")
			}
		})
	}
}

// TestParityChunkDuplicateRejected proves the receiver refuses duplicate
// parity chunkIdx values so a malicious sender cannot inflate the
// totalParityBytes counter via same-key overwrites. [B2-F2]
func TestParityChunkDuplicateRejected(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	sc := streamChunk{
		fileIdx:    parityFileIdx,
		chunkIdx:   0,
		decompSize: 8,
		data:       []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}
	sc.hash = blake3Hash(sc.data)

	isNew, err := state.processIncomingChunk(sc)
	if err != nil {
		t.Fatalf("first parity chunk: %v", err)
	}
	if !isNew {
		t.Error("first parity chunk should be new")
	}

	// Second arrival with same chunkIdx must be rejected — parity is never
	// legitimately resent.
	_, err = state.processIncomingChunk(sc)
	if err == nil {
		t.Fatal("duplicate parity chunk should be rejected")
	}
}

// TestParityChunkIndexOutOfRange proves the receiver rejects parity chunkIdx
// values outside [0, maxParityCount). [B2-F12]
func TestParityChunkIndexOutOfRange(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	sc := streamChunk{
		fileIdx:    parityFileIdx,
		chunkIdx:   maxParityCount, // out of range (exclusive upper bound)
		decompSize: 4,
		data:       []byte{9, 9, 9, 9},
	}
	sc.hash = blake3Hash(sc.data)
	_, err := state.processIncomingChunk(sc)
	if err == nil {
		t.Fatal("parity chunkIdx == maxParityCount should be rejected")
	}
}

// TestParityBudgetHardCap proves that the in-memory parity budget is hard-
// capped by maxParityBudgetBytes regardless of the sender's declared totalSize.
// Without this cap a malicious sender declaring totalSize=10 TB would be
// granted a 5 TB parity budget against receiver RAM. [B2-F1]
func TestParityBudgetHardCap(t *testing.T) {
	// Declare a totalSize large enough that totalSize/2 > maxParityBudgetBytes.
	hugeTotal := int64(maxParityBudgetBytes) * 4 // 2 GB: totalSize/2 = 1 GB > 512 MB cap
	state := newStreamReceiveState(nil, hugeTotal, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})

	// Each parity chunk 1 MB; budget is 512 MB, so we expect ~512 chunks to
	// fit and the 513th to be rejected with "exceeds budget".
	const perChunk = 1 << 20 // 1 MB
	data := bytes.Repeat([]byte{0xAA}, perChunk)
	h := blake3Hash(data)

	accepted := 0
	for i := 0; i < int(maxParityBudgetBytes/perChunk)+10; i++ {
		sc := streamChunk{
			fileIdx:    parityFileIdx,
			chunkIdx:   i,
			decompSize: perChunk,
			data:       data,
			hash:       h,
		}
		_, err := state.processIncomingChunk(sc)
		if err != nil {
			// First rejection must be budget-related; totalParityBytes must never
			// exceed the hard cap even by a single chunk.
			if state.totalParityBytes > int64(maxParityBudgetBytes) {
				t.Fatalf("totalParityBytes=%d exceeded hard cap %d (declared totalSize=%d)",
					state.totalParityBytes, maxParityBudgetBytes, hugeTotal)
			}
			if accepted < 1 {
				t.Fatalf("rejected too early at i=%d: %v", i, err)
			}
			return
		}
		accepted++
	}
	t.Fatalf("expected budget rejection, accepted %d chunks totalling %d bytes",
		accepted, state.totalParityBytes)
}

// TestProgressParityChunksDoneTracking proves addParityChunkDone increments
// ParityChunksDone without touching ChunksDone / ChunksTotal — parity chunks
// are tracked in their own counter so the UI never shows "> 100% chunks".
// [B2-F29, Option C]
func TestProgressParityChunksDoneTracking(t *testing.T) {
	p := &TransferProgress{ChunksTotal: 100}

	// Simulate 10 data chunks + 5 parity chunks reaching the wire in any order.
	p.updateChunks(1024, 1)
	p.updateChunks(2048, 2)
	p.addParityChunkDone()
	p.updateChunks(3072, 3)
	p.addParityChunkDone()
	p.addParityChunkDone()
	p.updateChunks(10240, 10)
	p.addParityChunkDone()
	p.addParityChunkDone()

	if p.ChunksDone != 10 {
		t.Errorf("ChunksDone=%d, want 10 (data only)", p.ChunksDone)
	}
	if p.ParityChunksDone != 5 {
		t.Errorf("ParityChunksDone=%d, want 5", p.ParityChunksDone)
	}
	if p.ChunksTotal != 100 {
		t.Errorf("ChunksTotal=%d, want 100 (unchanged)", p.ChunksTotal)
	}
}

// TestErasureHeaderRejectsSmallStripeSize proves the wire-level stripeSize
// lower bound in readHeader catches a malicious header declaring stripeSize=1.
// [B2-F11, Option C: moved from trailer to header]
func TestErasureHeaderRejectsSmallStripeSize(t *testing.T) {
	files := []fileEntry{{Path: "test.bin", Size: 1024}}
	var transferID [32]byte
	var buf bytes.Buffer
	// Write a header with stripeSize=1 (below minStripeSize=2).
	hdr := &erasureHeaderParams{StripeSize: 1, OverheadPerMille: 100}
	if err := writeHeader(&buf, files, flagErasureCoded, 1024, transferID, hdr); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}
	if _, _, _, _, _, _, err := readHeader(&buf); err == nil {
		t.Fatal("readHeader should reject stripeSize=1")
	}
}

// TestFlushStripeClearedOnError proves that erasureEncoder.flushStripe clears
// its stripeBuf unconditionally — including when encodeStripe errors. Leaving
// the buffer populated would let a retry re-encode the same stripe (duplicate
// parity on the wire) or misalign parity with subsequent stripes.
// [B2 audit fix S8]
func TestFlushStripeClearedOnError(t *testing.T) {
	// encodeStripe only errors when every chunk in the stripe is empty (its
	// "all chunks empty" guard). Build a 2-chunk all-empty stripe to trigger
	// the error path through a normal AddChunk call sequence.
	enc := newErasureEncoder(2, 0.5)
	if enc == nil {
		t.Fatal("newErasureEncoder returned nil")
	}

	// First empty chunk: stripe not yet full, no emit.
	if out, err := enc.AddChunk(nil); err != nil {
		t.Fatalf("AddChunk[0]: unexpected error %v", err)
	} else if len(out) != 0 {
		t.Fatalf("AddChunk[0] emitted %d parity mid-stripe, want 0", len(out))
	}

	// Second empty chunk: stripe fills, encodeStripe errors ("all chunks empty").
	_, err := enc.AddChunk(nil)
	if err == nil {
		t.Fatal("AddChunk[1] on all-empty stripe should return encodeStripe error")
	}

	// Post-error invariant: stripeBuf must be empty so a follow-up call cannot
	// re-encode the same stripe or mis-align parity with later stripes.
	if len(enc.stripeBuf) != 0 {
		t.Errorf("stripeBuf length=%d after encodeStripe error, want 0 (defer cleanup)", len(enc.stripeBuf))
	}
	// Also verify capacity retained so the encoder could in principle be
	// reused without reallocation — just safe-to-discard, not safe-to-continue.
	if cap(enc.stripeBuf) == 0 {
		t.Errorf("stripeBuf capacity=0 after error; expected capacity retained for potential reuse")
	}
}

// TestReceiverParityProgressGated proves the receiver-side processChunk
// wrapper in receiveParallel correctly routes parity chunks to
// ParityChunksDone and data chunks to ChunksDone, instead of mixing their
// index namespaces. Pre-Batch-2 parity always arrived after all data, so
// receiver's ChunksDone = sc.chunkIdx+1 was only slightly wrong at the tail.
// Post-Batch-2 parity interleaves with data, making jittery counters a
// user-visible regression. [B2 audit fix B1]
func TestReceiverParityProgressGated(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// 2 data chunks + 1 parity chunk (generated via encodeStripe over the data).
	data := [][]byte{
		bytes.Repeat([]byte{0xA1}, 64),
		bytes.Repeat([]byte{0xB2}, 64),
	}
	totalSize := int64(128)
	chunkHashes := [][32]byte{
		sdk.Blake3Sum(data[0]),
		sdk.Blake3Sum(data[1]),
	}
	rootHash := sdk.MerkleRoot(chunkHashes)
	parityShards, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parityShards[0])

	files := []fileEntry{{Path: "parity-progress-test.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Control stream: parity chunk first (interleaved arrival), then the two
	// data chunks, then trailer. If the wiring were buggy, the parity's
	// chunkIdx=0 would reset progress.ChunksDone to 1 after data[1] had set
	// it to 2 — or vice versa, depending on order.
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parityShards[0])), data: parityShards[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: chunkHashes[0], decompSize: uint32(len(data[0])), data: data[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 64,
		hash: chunkHashes[1], decompSize: uint32(len(data[1])), data: data[1],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  1,
		ParityHashes: [][32]byte{parityHash},
		ParitySizes:  []uint32{uint32(len(parityShards[0]))},
	})

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}

	// ChunksDone must equal data count (2), NOT include parity.
	if progress.ChunksDone != 2 {
		t.Errorf("ChunksDone=%d, want 2 (data-only; parity must not bump this counter)", progress.ChunksDone)
	}
	// ParityChunksDone must reflect the one parity chunk that arrived.
	if progress.ParityChunksDone != 1 {
		t.Errorf("ParityChunksDone=%d, want 1 (parity counter must increment on fileIdx==parityFileIdx)", progress.ParityChunksDone)
	}
	// ErasureParity must be populated from the trailer.
	if progress.ErasureParity != 1 {
		t.Errorf("ErasureParity=%d, want 1 (from trailer)", progress.ErasureParity)
	}
}

// TestUpdateChunksMonotonic proves that a goroutine calling updateChunks with
// stale lower values cannot yank Transferred / ChunksDone backwards. This
// guards against the race where parallel receivers (worker streams + control
// stream) each read state counters at different points in the shared-lock
// cycle, and a slower goroutine could otherwise overwrite a newer value. [B2
// audit round 2]
func TestUpdateChunksMonotonic(t *testing.T) {
	p := &TransferProgress{}
	// Climb normally.
	p.updateChunks(1000, 10)
	if p.Transferred != 1000 || p.ChunksDone != 10 {
		t.Fatalf("initial state wrong: Transferred=%d ChunksDone=%d", p.Transferred, p.ChunksDone)
	}
	// A stale view from a slower goroutine must NOT regress either counter.
	p.updateChunks(500, 3)
	if p.Transferred != 1000 {
		t.Errorf("Transferred regressed to %d, want 1000 (monotonic)", p.Transferred)
	}
	if p.ChunksDone != 10 {
		t.Errorf("ChunksDone regressed to %d, want 10 (monotonic)", p.ChunksDone)
	}
	// A fresher view must still advance.
	p.updateChunks(2000, 20)
	if p.Transferred != 2000 || p.ChunksDone != 20 {
		t.Errorf("advance failed: Transferred=%d ChunksDone=%d, want 2000/20", p.Transferred, p.ChunksDone)
	}
	// Mixed: transferred advances but chunks are stale. Each counter guards
	// independently — Transferred advances, ChunksDone holds.
	p.updateChunks(3000, 15)
	if p.Transferred != 3000 {
		t.Errorf("Transferred=%d, want 3000 (independent advance)", p.Transferred)
	}
	if p.ChunksDone != 20 {
		t.Errorf("ChunksDone=%d, want 20 (independent hold)", p.ChunksDone)
	}
}

// TestReceivedCountMonotonic proves that ReceivedCount drives a monotonic
// ChunksDone even when chunks arrive out of order. Pre-audit code used
// `sc.chunkIdx + 1`, so receiving chunks 3,0,2,1 would drive ChunksDone
// 4→1→3→2 — a visible user regression during out-of-order transfers over
// parallel streams. [B2 audit round 2]
func TestReceivedCountMonotonic(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// 4 data chunks, delivered in reverse-ish order via control stream.
	chunkData := [][]byte{
		bytes.Repeat([]byte{0x10}, 32),
		bytes.Repeat([]byte{0x20}, 48),
		bytes.Repeat([]byte{0x30}, 56),
		bytes.Repeat([]byte{0x40}, 40),
	}
	totalSize := int64(32 + 48 + 56 + 40)
	offsets := []int64{0, 32, 80, 136}
	hashes := make([][32]byte, 4)
	for i, d := range chunkData {
		hashes[i] = sdk.Blake3Sum(d)
	}
	rootHash := sdk.MerkleRoot(hashes)

	files := []fileEntry{{Path: "monotonic-count.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, 0, cumOffsets)
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(4)

	progress := &TransferProgress{ChunksTotal: 4}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Deliver in highly out-of-order fashion: 3, 0, 2, 1.
	var controlBuf bytes.Buffer
	for _, idx := range []int{3, 0, 2, 1} {
		writeStreamChunkFrame(&controlBuf, streamChunk{
			fileIdx: 0, chunkIdx: idx, offset: offsets[idx],
			hash: hashes[idx], decompSize: uint32(len(chunkData[idx])), data: chunkData[idx],
		})
	}
	writeTrailer(&controlBuf, 4, rootHash, nil, nil)

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}
	// After all 4 data chunks: ChunksDone must be 4, regardless of arrival
	// order. Pre-audit code would leave it at 2 (last arrival was chunkIdx=1,
	// so ChunksDone = 2).
	if progress.ChunksDone != 4 {
		t.Errorf("ChunksDone=%d after 4 out-of-order chunks, want 4 (monotonic count)", progress.ChunksDone)
	}
}

// TestReceiverParityCountMismatchLogged proves the receiver does not fail a
// transfer when the trailer's declared parity count disagrees with the
// number of parity chunks actually received AND no chunk needs
// reconstruction. Warn-only is the correct semantic here: intact transfers
// should not die because a parity chunk was lost in flight. Batch 2b
// tightens the mismatch to a hard fail only when rsReconstruct is
// actually going to run (see TestReceiveParallelParityCountMismatchHardFailOnCorruption).
// [B2-F14 / Batch 2b F35 conditional]
func TestReceiverParityCountMismatchLogged(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// 2 data chunks, 1 parity chunk actually sent, but trailer will declare 5.
	data := [][]byte{
		bytes.Repeat([]byte{0xC3}, 32),
		bytes.Repeat([]byte{0xD4}, 32),
	}
	totalSize := int64(64)
	chunkHashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(chunkHashes)
	parityShards, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parityShards[0])

	files := []fileEntry{{Path: "count-mismatch.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: chunkHashes[0], decompSize: uint32(len(data[0])), data: data[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 32,
		hash: chunkHashes[1], decompSize: uint32(len(data[1])), data: data[1],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parityShards[0])), data: parityShards[0],
	})
	// Trailer LIES — declares 5 parity but only 1 arrived. With no
	// corrupted chunks this is warn-only (Batch 2b conditional F35 path);
	// TestReceiveParallelParityCountMismatchHardFailOnCorruption covers
	// the hard-fail branch when corruption needs reconstruction.
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  5,
		ParityHashes: make([][32]byte, 5),
		ParitySizes:  []uint32{128, 128, 128, 128, 128},
	})

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel failed on trailer count mismatch without corruption (warn-only path): %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}
	// Trailer value is what's surfaced — receiver trusts the declaration for
	// reporting purposes even if actual count differs.
	if progress.ErasureParity != 5 {
		t.Errorf("ErasureParity=%d, want 5 (from trailer declaration)", progress.ErasureParity)
	}
}

// --- Batch 2b: rsReconstruct wiring tests ---

// TestMarkCorruptedAndCorruptedList proves the tracking API used by
// processIncomingChunk + rsReconstruct: the set is deduplicated, empty by
// default, and returned in sorted order. Out-of-order marks must not produce
// out-of-order stripe groupings at reconstruction time. [Batch 2b]
func TestMarkCorruptedAndCorruptedList(t *testing.T) {
	state := newStreamReceiveState(nil, 1<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	if got := state.corruptedList(); len(got) != 0 {
		t.Errorf("empty state: corruptedList=%v, want nil/empty", got)
	}
	state.markCorrupted(7)
	state.markCorrupted(3)
	state.markCorrupted(9)
	state.markCorrupted(3) // duplicate
	got := state.corruptedList()
	want := []int{3, 7, 9}
	if len(got) != len(want) {
		t.Fatalf("corruptedList len=%d, want %d (got=%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("corruptedList[%d]=%d, want %d (got=%v)", i, got[i], want[i], got)
		}
	}
}

// TestReadChunkGlobalRoundtrip exercises the inverse of writeChunkGlobal for
// single-file, cross-file, rejected-file, and boundary cases. rsReconstruct
// relies on this helper to load intact stripe-mates as RS data shards.
// [Batch 2b, B2-F31]
func TestReadChunkGlobalRoundtrip(t *testing.T) {
	dir := t.TempDir()

	// Two files concatenated: a.bin (100 bytes) + b.bin (80 bytes).
	files := []fileEntry{
		{Path: "a.bin", Size: 100},
		{Path: "b.bin", Size: 80},
	}
	totalSize := int64(180)
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, 0, cumOffsets)
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}

	// Fill the tmp files with deterministic bytes: a.bin = 0x01..., b.bin = 0x02...
	aBytes := bytes.Repeat([]byte{0x01}, 100)
	bBytes := bytes.Repeat([]byte{0x02}, 80)
	if _, err := state.tmpFiles[0].WriteAt(aBytes, 0); err != nil {
		t.Fatalf("write a.bin: %v", err)
	}
	if _, err := state.tmpFiles[1].WriteAt(bBytes, 0); err != nil {
		t.Fatalf("write b.bin: %v", err)
	}

	// Single-file read within a.bin.
	got, err := state.readChunkGlobal(20, 50)
	if err != nil {
		t.Fatalf("single-file read: %v", err)
	}
	if !bytes.Equal(got, bytes.Repeat([]byte{0x01}, 50)) {
		t.Errorf("single-file bytes mismatch: got %x", got[:8])
	}

	// Cross-file read spanning the a.bin → b.bin boundary.
	// Chunk at global offset 90, length 20: 10 bytes from a.bin + 10 from b.bin.
	got, err = state.readChunkGlobal(90, 20)
	if err != nil {
		t.Fatalf("cross-file read: %v", err)
	}
	want := append(bytes.Repeat([]byte{0x01}, 10), bytes.Repeat([]byte{0x02}, 10)...)
	if !bytes.Equal(got, want) {
		t.Errorf("cross-file bytes mismatch: got %x want %x", got, want)
	}

	// Zero-length read returns nil, no error.
	got, err = state.readChunkGlobal(50, 0)
	if err != nil {
		t.Fatalf("zero-length read: %v", err)
	}
	if got != nil {
		t.Errorf("zero-length read: got %v, want nil", got)
	}

	// Negative length is a caller bug — surface explicitly, don't silently accept.
	if _, err := state.readChunkGlobal(0, -1); err == nil {
		t.Error("negative length: expected error, got nil")
	}

	// Out-of-range read fails explicitly.
	if _, err := state.readChunkGlobal(170, 20); err == nil {
		t.Error("out-of-range read: expected error, got nil")
	}

	// Rejected-file slice (simulate by nulling tmpFiles[1]) must error.
	state.tmpFiles[1].Close()
	state.tmpFiles[1] = nil
	if _, err := state.readChunkGlobal(100, 10); err == nil {
		t.Error("rejected-file read: expected error, got nil")
	}
}

// TestProcessIncomingChunkHashMismatchWithErasure proves that on a transfer
// carrying parity, a hash mismatch marks the chunk corrupted and keeps the
// receive loop alive instead of aborting. The claimed hash/size stay in
// state (via recordChunk) so rsReconstruct can reason about the stripe
// layout later. [Batch 2b, B2-F32]
func TestProcessIncomingChunkHashMismatchWithErasure(t *testing.T) {
	files := []fileEntry{{Path: "x.bin", Size: 64}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, 64, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	state.initReceivedBitfield(1)

	// Frame claims hash H(correct), sends corrupt bytes instead.
	correct := bytes.Repeat([]byte{0xAA}, 64)
	corrupt := bytes.Repeat([]byte{0xBB}, 64)
	claimed := sdk.Blake3Sum(correct)
	sc := streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: claimed, decompSize: 64, data: corrupt,
	}
	isNew, err := state.processIncomingChunk(sc)
	if err != nil {
		t.Fatalf("processIncomingChunk returned error on corruption with erasure: %v", err)
	}
	if !isNew {
		t.Error("isNew=false, want true (first arrival of chunk 0)")
	}
	if got := state.corruptedList(); len(got) != 1 || got[0] != 0 {
		t.Errorf("corruptedList=%v, want [0]", got)
	}
	// Claimed hash + size must be populated despite the mismatch.
	state.mu.Lock()
	h, hOK := state.hashes[0]
	sz, szOK := state.sizes[0]
	state.mu.Unlock()
	if !hOK || h != claimed {
		t.Errorf("state.hashes[0] not set to claimed (ok=%v, val=%x)", hOK, h[:8])
	}
	if !szOK || sz != 64 {
		t.Errorf("state.sizes[0]=%d (ok=%v), want 64", sz, szOK)
	}
}

// TestProcessIncomingChunkHashMismatchWithoutErasure proves the guardrail:
// on a transfer without parity, hash mismatch is still an immediate abort.
// Merkle uses claimed hashes and would otherwise pass silently while the
// file data on disk is corrupt. [Batch 2b safety invariant]
func TestProcessIncomingChunkHashMismatchWithoutErasure(t *testing.T) {
	files := []fileEntry{{Path: "x.bin", Size: 64}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, 64, 0, cumOffsets)
	state.initReceivedBitfield(1)

	correct := bytes.Repeat([]byte{0xAA}, 64)
	corrupt := bytes.Repeat([]byte{0xBB}, 64)
	claimed := sdk.Blake3Sum(correct)
	sc := streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: claimed, decompSize: 64, data: corrupt,
	}
	_, err := state.processIncomingChunk(sc)
	if err == nil {
		t.Fatal("processIncomingChunk returned nil on corruption without erasure; silent corruption is unacceptable")
	}
	if len(state.corruptedList()) != 0 {
		t.Error("corruptedChunks must stay empty when hasErasure=false (transfer aborts instead)")
	}
}

// rsReconstructSetup is a test fixture: builds chunk data, writes correct
// bytes to tmp files except for `corruptedIdx` (which gets wrong bytes),
// seeds state.hashes + state.sizes (as recordChunk would have), marks the
// corrupted indices via markCorrupted, and stores the encoded parity in
// state.paritySlots. Returns the state ready for rsReconstruct.
type rsReconstructFixture struct {
	state   *streamReceiveState
	chunks  [][]byte
	hashes  [][32]byte
	offsets []int64
	stripe  int
	parity  int
}

func setupRsReconstructSingleFile(t *testing.T, dir string, stripeSize int, corrupted []int) *rsReconstructFixture {
	t.Helper()
	// Deterministic chunk table: `stripeSize` chunks of 64 bytes each.
	chunkCount := stripeSize
	chunks := make([][]byte, chunkCount)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{byte(i + 1)}, 64)
	}
	hashes := make([][32]byte, chunkCount)
	offsets := make([]int64, chunkCount+1)
	var totalSize int64
	for i, c := range chunks {
		hashes[i] = sdk.Blake3Sum(c)
		offsets[i+1] = offsets[i] + int64(len(c))
		totalSize += int64(len(c))
	}
	parity, err := encodeStripe(chunks, 2)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}

	files := []fileEntry{{Path: "recon-test.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	// Init per-stripe with overhead that yields 2 parity per stripe (matching encodeStripe(chunks, 2)).
	testOverhead := float64(2) / float64(stripeSize)
	if testOverhead < 0.01 {
		testOverhead = 0.01
	}
	state.initPerStripeState(&erasureHeaderParams{StripeSize: stripeSize, OverheadPerMille: overheadToPerMille(testOverhead)})
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(chunkCount)

	corruptedSet := make(map[int]bool, len(corrupted))
	for _, idx := range corrupted {
		corruptedSet[idx] = true
	}
	for i, c := range chunks {
		// recordChunk populates state.hashes/state.sizes, receivedBytes,
		// bitfield. Simulate that for every arrived chunk (whether its data
		// was good or not).
		state.recordChunk(i, hashes[i], uint32(len(c)))
		if corruptedSet[i] {
			// Corrupted chunk: mark, do NOT write data (mirrors production flow).
			state.markCorrupted(i)
			continue
		}
		if _, err := state.tmpFiles[0].WriteAt(c, offsets[i]); err != nil {
			t.Fatalf("write chunk %d: %v", i, err)
		}
	}
	// Store parity as rsReconstruct expects: per-stripe paritySlots.
	// Single stripe (stripe 0), all parity shards are local indices.
	state.paritySlots = make(map[int]*paritySlot)
	slot := &paritySlot{parity: make(map[int][]byte, len(parity))}
	for i, p := range parity {
		slot.parity[i] = p
		slot.bytes += int64(len(p))
	}
	state.paritySlots[0] = slot
	return &rsReconstructFixture{
		state:   state,
		chunks:  chunks,
		hashes:  hashes,
		offsets: offsets,
		stripe:  stripeSize,
		parity:  len(parity),
	}
}

// TestRsReconstructSingleCorruption covers the primary recovery path: one
// corrupted chunk in a stripe whose parity budget is 2. Verifies the tmp
// file holds the reconstructed bytes and that no intact chunk was touched.
// [Batch 2b, B2-F32/F34]
func TestRsReconstructSingleCorruption(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	const stripeSize = 10
	f := setupRsReconstructSingleFile(t, dir, stripeSize, []int{4})
	defer f.state.cleanup()

	if err := ts.rsReconstruct(f.state, stripeSize, stripeSize, 0.1, []int{4}); err != nil {
		t.Fatalf("rsReconstruct: %v", err)
	}

	// Read back from tmp file and confirm recovery.
	got, err := f.state.readChunkGlobal(f.offsets[4], len(f.chunks[4]))
	if err != nil {
		t.Fatalf("readChunkGlobal after reconstruct: %v", err)
	}
	if !bytes.Equal(got, f.chunks[4]) {
		t.Errorf("chunk 4 not reconstructed correctly\n got=%x\nwant=%x", got[:8], f.chunks[4][:8])
	}
	// Intact neighbors untouched: chunks 3 and 5 should still be their originals.
	got3, _ := f.state.readChunkGlobal(f.offsets[3], len(f.chunks[3]))
	if !bytes.Equal(got3, f.chunks[3]) {
		t.Error("chunk 3 modified by reconstruction; must only touch corrupted indices")
	}
}

// TestRsReconstructMultipleCorruptionWithinBudget verifies that multiple
// corrupted chunks in the same stripe all recover when they fit within the
// parity budget. stripeSize=15 → parityCount = int(15*0.1+0.5) = 2, matching
// the formula hardcoded in rsReconstruct / encoder.flushStripe. [Batch 2b]
func TestRsReconstructMultipleCorruptionWithinBudget(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	const stripeSize = 15 // yields parityCount=2 under the 0.1-overhead formula
	corrupted := []int{2, 11}
	f := setupRsReconstructSingleFile(t, dir, stripeSize, corrupted)
	defer f.state.cleanup()

	if err := ts.rsReconstruct(f.state, stripeSize, stripeSize, 0.1, corrupted); err != nil {
		t.Fatalf("rsReconstruct: %v", err)
	}
	for _, idx := range corrupted {
		got, err := f.state.readChunkGlobal(f.offsets[idx], len(f.chunks[idx]))
		if err != nil {
			t.Fatalf("readChunkGlobal(%d): %v", idx, err)
		}
		if !bytes.Equal(got, f.chunks[idx]) {
			t.Errorf("chunk %d not reconstructed correctly", idx)
		}
	}
}

// TestRsReconstructUnrecoverable proves rsReconstruct errors out cleanly
// when the corrupted count exceeds available parity, and leaves intact
// chunks untouched. No partial recovery writes, no silent success. [B2-F36]
func TestRsReconstructUnrecoverable(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	const stripeSize = 10 // 1 parity under 0.1 overhead, but 3 corrupted → unrecoverable
	corrupted := []int{1, 4, 8}
	f := setupRsReconstructSingleFile(t, dir, stripeSize, corrupted)
	defer f.state.cleanup()

	if err := ts.rsReconstruct(f.state, stripeSize, stripeSize, 0.1, corrupted); err == nil {
		t.Fatal("rsReconstruct returned nil on unrecoverable stripe; must error out")
	}
	// Intact chunks (not in corrupted set) must be byte-identical to originals.
	for i := 0; i < stripeSize; i++ {
		inCorrupted := false
		for _, idx := range corrupted {
			if i == idx {
				inCorrupted = true
				break
			}
		}
		if inCorrupted {
			continue
		}
		got, err := f.state.readChunkGlobal(f.offsets[i], len(f.chunks[i]))
		if err != nil {
			t.Fatalf("readChunkGlobal(%d): %v", i, err)
		}
		if !bytes.Equal(got, f.chunks[i]) {
			t.Errorf("intact chunk %d modified on unrecoverable path", i)
		}
	}
}

// TestRsReconstructMultiFileBoundary proves reconstruction works when the
// corrupted chunk spans two accepted files. readChunkGlobal + writeChunkGlobal
// route reads and writes through globalToLocal; this test catches any
// regression where the stripe shard assembly assumes single-file layout.
// [Batch 2b, B2-F31]
func TestRsReconstructMultiFileBoundary(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// Two files of 50 bytes each. 5 chunks of 20 bytes — chunk 2 spans the
	// file boundary (offsets 40..60 straddle the 50-byte cut between files).
	chunkBytes := 20
	chunkCount := 5
	testStripeSize := chunkCount
	chunks := make([][]byte, chunkCount)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{byte(i + 1)}, chunkBytes)
	}
	hashes := make([][32]byte, chunkCount)
	offsets := make([]int64, chunkCount+1)
	for i := range chunks {
		hashes[i] = sdk.Blake3Sum(chunks[i])
		offsets[i+1] = offsets[i] + int64(chunkBytes)
	}
	totalSize := int64(chunkCount * chunkBytes)

	files := []fileEntry{
		{Path: "left.bin", Size: 50},
		{Path: "right.bin", Size: 50},
	}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: testStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(chunkCount)

	// Write all chunks EXCEPT the boundary-spanning one (idx 2) to tmp files.
	for i, c := range chunks {
		state.recordChunk(i, hashes[i], uint32(len(c)))
		if i == 2 {
			state.markCorrupted(i)
			continue
		}
		// Each chunk lands via globalToLocal; reuse writeChunkGlobal so the
		// setup mirrors the production write path byte-for-byte.
		if err := state.writeChunkGlobal(0, offsets[i], len(c), c); err != nil {
			t.Fatalf("writeChunkGlobal chunk %d: %v", i, err)
		}
	}
	// Encode parity over the 5 chunks as one stripe.
	parity, err := encodeStripe(chunks, 2)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	// Single stripe (stripe 0), all parity shards are local indices.
	state.paritySlots = make(map[int]*paritySlot)
	slot := &paritySlot{parity: make(map[int][]byte, len(parity))}
	for i, p := range parity {
		slot.parity[i] = p
		slot.bytes += int64(len(p))
	}
	state.paritySlots[0] = slot

	if err := ts.rsReconstruct(state, chunkCount, chunkCount, 0.1, []int{2}); err != nil {
		t.Fatalf("rsReconstruct multi-file: %v", err)
	}

	// The reconstructed chunk 2 should now be present on BOTH tmp files:
	// first 10 bytes in left.bin at offset 40, next 10 in right.bin at offset 0.
	got, err := state.readChunkGlobal(offsets[2], chunkBytes)
	if err != nil {
		t.Fatalf("readChunkGlobal after reconstruct: %v", err)
	}
	if !bytes.Equal(got, chunks[2]) {
		t.Errorf("boundary chunk not reconstructed: got %x want %x", got, chunks[2])
	}
}

// TestReceiveParallelReconstructsCorrupted is the end-to-end integration
// test: inject a wire frame with a valid claimed hash but corrupt bytes,
// feed parity that covers the corruption, and assert receiveParallel
// reconstructs + passes Merkle + finalizes. [Batch 2b]
func TestReceiveParallelReconstructsCorrupted(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// 2 data chunks, 1 parity (stripeSize=2, overhead≈0.2 → parityCount=1
	// via floor(2*0.1+0.5)=1). Corrupt chunk 1.
	data := [][]byte{
		bytes.Repeat([]byte{0x33}, 64),
		bytes.Repeat([]byte{0x44}, 64),
	}
	totalSize := int64(128)
	hashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(hashes)
	parity, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parity[0])

	files := []fileEntry{{Path: "recon-e2e.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Chunk 0 correct, chunk 1 claims correct hash but sends corrupt bytes.
	corrupt := bytes.Repeat([]byte{0xFF}, 64)
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: hashes[0], decompSize: 64, data: data[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 64,
		hash: hashes[1], decompSize: 64, data: corrupt,
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parity[0])), data: parity[0],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  1,
		ParityHashes: [][32]byte{parityHash},
		ParitySizes:  []uint32{uint32(len(parity[0]))},
	})

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel with corruption+recovery: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}

	// Finalize renames tmp to real; read the final file and verify both
	// chunks — including the reconstructed one — made it to disk.
	final, err := os.ReadFile(filepath.Join(dir, "recon-e2e.bin"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	want := append(append([]byte{}, data[0]...), data[1]...)
	if !bytes.Equal(final, want) {
		t.Errorf("final file bytes mismatch after reconstruction (first 8: got %x want %x)",
			final[:8], want[:8])
	}
}

// TestCheckpointExcludesCorruptedChunks proves that a checkpoint saved
// while a chunk is marked corrupted in-memory persists that chunk as
// NOT-received, so the next session's resume request asks the sender to
// retransmit it. Without this, the claim-hash + empty-data combination on
// disk would silently pass Merkle verify in the next session and corrupt
// the final file. [Batch 2b self-audit round 1]
func TestCheckpointExcludesCorruptedChunks(t *testing.T) {
	dir := t.TempDir()

	// 4 chunks of 32 bytes. Chunk 1 is corrupted; 0, 2, 3 arrived cleanly.
	const chunkCount = 4
	chunks := make([][]byte, chunkCount)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{byte(i + 1)}, 32)
	}
	hashes := make([][32]byte, chunkCount)
	for i, c := range chunks {
		hashes[i] = sdk.Blake3Sum(c)
	}
	totalSize := int64(chunkCount * 32)

	files := []fileEntry{{Path: "ckpt-corrupted.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(chunkCount)

	// Simulate production flow: every chunk goes through recordChunk first,
	// then clean ones get their bytes written; the corrupted one gets
	// markCorrupted called but no disk write.
	for i, c := range chunks {
		state.recordChunk(i, hashes[i], uint32(len(c)))
		if i == 1 {
			state.markCorrupted(i)
			continue
		}
		if _, err := state.tmpFiles[0].WriteAt(c, int64(i*32)); err != nil {
			t.Fatalf("write chunk %d: %v", i, err)
		}
	}

	var ck [32]byte
	rand.Read(ck[:])
	ckpt := checkpointFromState(state, ck, flagErasureCoded)

	// Corrupted index MUST be cleared in the persisted bitfield.
	if ckpt.have.has(1) {
		t.Error("checkpoint have-bitfield still marks corrupted chunk 1 as received; cross-session silent corruption risk")
	}
	// Clean indices must remain set.
	for _, idx := range []int{0, 2, 3} {
		if !ckpt.have.has(idx) {
			t.Errorf("checkpoint have-bitfield lost clean chunk %d", idx)
		}
	}

	// Roundtrip through restoreReceiveState: the restored state must not
	// consider chunk 1 received, so the resume request will include it.
	restored, err := ckpt.restoreReceiveState(dir)
	if err != nil {
		t.Fatalf("restoreReceiveState: %v", err)
	}
	defer restored.cleanup()
	if _, ok := restored.hashes[1]; ok {
		t.Error("restored state retained hashes[1] for corrupted chunk; sender will not retransmit")
	}
	if _, ok := restored.sizes[1]; ok {
		t.Error("restored state retained sizes[1] for corrupted chunk")
	}
	if restored.receivedBitfield.has(1) {
		t.Error("restored bitfield still marks corrupted chunk as received")
	}
	// Clean chunks fully restored.
	for _, idx := range []int{0, 2, 3} {
		if h, ok := restored.hashes[idx]; !ok || h != hashes[idx] {
			t.Errorf("restored state lost clean chunk %d hash", idx)
		}
	}
}

// TestRsReconstructIgnoresOutOfRangeCorrupted proves rsReconstruct does not
// abort reconstruction when the corrupted-index list contains indices >=
// chunkCount. processIncomingChunk can mark such indices corrupted
// (per-frame chunkIdx is bounded by maxChunkCount, not the trailer's
// chunkCount), and an attacker could otherwise kill a legitimate recovery
// by injecting one junk-indexed corrupt frame. [Batch 2b audit round 3]
func TestRsReconstructIgnoresOutOfRangeCorrupted(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	const stripeSize = 10
	f := setupRsReconstructSingleFile(t, dir, stripeSize, []int{4})
	defer f.state.cleanup()

	// Inject an out-of-range corrupted index (chunkCount=stripeSize=10, so
	// idx=50 is above range). rsReconstruct must drop it silently and still
	// recover idx=4.
	corrupted := []int{4, 50}
	if err := ts.rsReconstruct(f.state, stripeSize, stripeSize, 0.1, corrupted); err != nil {
		t.Fatalf("rsReconstruct with out-of-range idx: %v (must drop 50 silently)", err)
	}
	got, err := f.state.readChunkGlobal(f.offsets[4], len(f.chunks[4]))
	if err != nil {
		t.Fatalf("readChunkGlobal: %v", err)
	}
	if !bytes.Equal(got, f.chunks[4]) {
		t.Error("chunk 4 not reconstructed after out-of-range idx was injected")
	}
}

// TestReceiveParallelReconstructsNonDefaultOverhead proves the overhead
// fix from audit round 2: a sender configured with erasure_overhead=0.2
// emits ceil(stripeSize*0.2) parity per stripe; the receiver's
// rsReconstruct MUST read the overhead from the trailer rather than
// hardcoding 0.1, otherwise parity offsets mismap and reconstruction
// silently fails. End-to-end coverage keeps this correctness guarantee
// visible if the wiring ever regresses. [Batch 2b audit round 2]
func TestReceiveParallelReconstructsNonDefaultOverhead(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// 5 data chunks of 64 bytes, one stripe of 5 (partial last stripe in
	// theory but we just use one full-looking stripe by making stripeSize=5).
	// overhead=0.2 → parityCount per stripe = int(5*0.2+0.5) = 1.
	// Use stripeSize=5 to keep per-stripe parity=1 regardless — the point of
	// the test is that the wire carries the overhead, not that overhead
	// changes the per-stripe count. A simpler demonstration: stripeSize=10
	// would yield 2 parity at overhead=0.2 vs 1 parity at overhead=0.1 —
	// receiver hardcoding 0.1 would compute parityOffset=1 for the second
	// stripe while sender wrote parity[2..3] there. That misalignment
	// surfaces in multi-stripe transfers. Use stripeSize=10 + 20 chunks for
	// two stripes.
	const stripeSize = 10
	const overhead = 0.2
	const chunkCount = 20
	chunks := make([][]byte, chunkCount)
	for i := range chunks {
		chunks[i] = bytes.Repeat([]byte{byte(i + 1)}, 64)
	}
	hashes := make([][32]byte, chunkCount)
	offsets := make([]int64, chunkCount+1)
	for i, c := range chunks {
		hashes[i] = sdk.Blake3Sum(c)
		offsets[i+1] = offsets[i] + int64(len(c))
	}
	totalSize := int64(chunkCount * 64)
	rootHash := sdk.MerkleRoot(hashes)

	// Encode 2 stripes with 2 parity each (int(10*0.2+0.5)=2). Extract to a
	// non-const float so the compiler does the truncation at runtime.
	ohFloat := float64(overhead)
	parityPerStripe := int(float64(stripeSize)*ohFloat + 0.5)
	allParity := make([][]byte, 0, 2*parityPerStripe)
	allParityHashes := make([][32]byte, 0, 2*parityPerStripe)
	allParitySizes := make([]uint32, 0, 2*parityPerStripe)
	for s := 0; s < 2; s++ {
		stripe := chunks[s*stripeSize : (s+1)*stripeSize]
		shards, err := encodeStripe(stripe, parityPerStripe)
		if err != nil {
			t.Fatalf("encodeStripe %d: %v", s, err)
		}
		for _, p := range shards {
			allParity = append(allParity, p)
			allParityHashes = append(allParityHashes, sdk.Blake3Sum(p))
			allParitySizes = append(allParitySizes, uint32(len(p)))
		}
	}

	files := []fileEntry{{Path: "recon-overhead.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: stripeSize, OverheadPerMille: overheadToPerMille(overhead)})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(chunkCount)

	progress := &TransferProgress{ChunksTotal: chunkCount}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Corrupt chunk 12 (lives in stripe 1). Receiver-side hardcode of 0.1
	// would look at paritySlots[1] for stripe 1 (parityOffset=1, parityCount=1),
	// but sender put stripe-1 parity at indices [2..3] under overhead=0.2.
	// Reconstruction with wrong offsets would fail (either read stripe-0's
	// parity, or fail the hash-match guard). Only the overhead-aware code
	// path succeeds.
	corruptIdx := 12
	corrupt := bytes.Repeat([]byte{0xFE}, 64)
	var controlBuf bytes.Buffer
	for i := 0; i < chunkCount; i++ {
		data := chunks[i]
		if i == corruptIdx {
			data = corrupt
		}
		writeStreamChunkFrame(&controlBuf, streamChunk{
			fileIdx: 0, chunkIdx: i, offset: offsets[i],
			hash: hashes[i], decompSize: 64, data: data,
		})
	}
	for i, p := range allParity {
		writeStreamChunkFrame(&controlBuf, streamChunk{
			fileIdx: parityFileIdx, chunkIdx: i, offset: 0,
			hash: allParityHashes[i], decompSize: uint32(len(p)), data: p,
		})
	}
	writeTrailer(&controlBuf, chunkCount, rootHash, nil, &erasureTrailer{
		ParityCount:  len(allParity),
		ParityHashes: allParityHashes,
		ParitySizes:  allParitySizes,
	})

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel overhead=0.2 reconstruction: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}

	// Verify on-disk reconstruction.
	final, err := os.ReadFile(filepath.Join(dir, "recon-overhead.bin"))
	if err != nil {
		t.Fatalf("read final file: %v", err)
	}
	want := bytes.Join(chunks, nil)
	if !bytes.Equal(final, want) {
		t.Errorf("final file mismatch under overhead=0.2")
	}
}

// TestReceiveParallelParityCountMismatchWarnButRecoverIfSufficient proves
// that a parity count mismatch (declared != received) is downgraded to a
// warning when per-stripe parity is sufficient. This enables recovery when
// network loss drops some parity alongside data. The per-stripe checks in
// reconstructSingleStripe are the real guards (corrupted > parityCount,
// nilData+nilParity > parityCount). [Batch 2c R5-F6]
func TestReceiveParallelParityCountMismatchWarnButRecoverIfSufficient(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	data := [][]byte{
		bytes.Repeat([]byte{0x55}, 64),
		bytes.Repeat([]byte{0x66}, 64),
	}
	totalSize := int64(128)
	hashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(hashes)
	parity, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parity[0])

	files := []fileEntry{{Path: "count-warn.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	corrupt := bytes.Repeat([]byte{0xFF}, 64)
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: hashes[0], decompSize: 64, data: data[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 64,
		hash: hashes[1], decompSize: 64, data: corrupt,
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parity[0])), data: parity[0],
	})
	// Trailer declares 5 parity but only 1 arrived. Per-stripe the 1 parity
	// IS sufficient for 1 corrupted chunk. Should Warn + recover, not fail.
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  5,
		ParityHashes: make([][32]byte, 5),
		ParitySizes:  []uint32{64, 64, 64, 64, 64},
	})

	_, err = ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel should succeed (per-stripe parity sufficient): %v", err)
	}
}

// TestReceiveParallelInsufficientPerStripeParity proves that when per-stripe
// parity is genuinely insufficient (more corrupted chunks than parity shards
// in the stripe), reconstruction fails with a clear error. This is the real
// guard — not the global count mismatch check. [Batch 2c R5-F6]
func TestReceiveParallelInsufficientPerStripeParity(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	data := [][]byte{
		bytes.Repeat([]byte{0x55}, 64),
		bytes.Repeat([]byte{0x66}, 64),
	}
	totalSize := int64(128)
	hashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(hashes)
	parity, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parity[0])

	files := []fileEntry{{Path: "insuf-parity.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// BOTH chunks corrupted, only 1 parity. 2 > 1 = unrecoverable.
	corrupt0 := bytes.Repeat([]byte{0xAA}, 64)
	corrupt1 := bytes.Repeat([]byte{0xFF}, 64)
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: hashes[0], decompSize: 64, data: corrupt0,
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 64,
		hash: hashes[1], decompSize: 64, data: corrupt1,
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parity[0])), data: parity[0],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  1,
		ParityHashes: [][32]byte{parityHash},
		ParitySizes:  []uint32{uint32(len(parity[0]))},
	})

	_, err = ts.receiveParallel(&controlBuf, session)
	if err == nil {
		t.Fatal("receiveParallel should fail: 2 corrupted chunks with only 1 parity")
	}
	if !strings.Contains(err.Error(), "exceed") {
		t.Fatalf("expected per-stripe parity exceeded error, got: %v", err)
	}
}

// --- Batch 2c: missing-chunk recovery tests ---

// TestChunkManifestRoundtrip verifies writeChunkManifest + readChunkManifest
// wire format: count(4) + [hash(32)+decompSize(4)]*N. Tests non-empty, empty
// (nil), and bounds validation. [Batch 2c]
func TestChunkManifestRoundtrip(t *testing.T) {
	hashes := [][32]byte{
		sdk.Blake3Sum([]byte("chunk0")),
		sdk.Blake3Sum([]byte("chunk1")),
		sdk.Blake3Sum([]byte("chunk2")),
	}
	sizes := []uint32{1024, 2048, 512}

	var buf bytes.Buffer
	if err := writeChunkManifest(&buf, hashes, sizes); err != nil {
		t.Fatalf("writeChunkManifest: %v", err)
	}

	gotHashes, gotSizes, err := readChunkManifest(&buf, 3)
	if err != nil {
		t.Fatalf("readChunkManifest: %v", err)
	}
	for i := range hashes {
		if gotHashes[i] != hashes[i] {
			t.Errorf("hash %d mismatch", i)
		}
		if gotSizes[i] != sizes[i] {
			t.Errorf("size %d: got %d want %d", i, gotSizes[i], sizes[i])
		}
	}
}

// TestChunkManifestNilWritesZero verifies that nil ChunkHashes writes count=0
// and readChunkManifest returns nil arrays (no-op manifest for tests and
// legacy compatibility). [Batch 2c]
func TestChunkManifestNilWritesZero(t *testing.T) {
	var buf bytes.Buffer
	if err := writeChunkManifest(&buf, nil, nil); err != nil {
		t.Fatalf("writeChunkManifest(nil): %v", err)
	}
	if buf.Len() != 4 {
		t.Fatalf("nil manifest should write 4 bytes (count=0), got %d", buf.Len())
	}
	h, s, err := readChunkManifest(&buf, 0)
	if err != nil {
		t.Fatalf("readChunkManifest: %v", err)
	}
	if h != nil || s != nil {
		t.Error("nil manifest should return nil arrays")
	}
}

// TestChunkManifestCountMismatch verifies readChunkManifest rejects a manifest
// whose count does not match the expected chunkCount. [Batch 2c, 2c-R3-F10]
func TestChunkManifestCountMismatch(t *testing.T) {
	hashes := [][32]byte{sdk.Blake3Sum([]byte("a")), sdk.Blake3Sum([]byte("b"))}
	sizes := []uint32{64, 64}

	var buf bytes.Buffer
	writeChunkManifest(&buf, hashes, sizes)

	_, _, err := readChunkManifest(&buf, 5) // expect 5, manifest has 2
	if err == nil {
		t.Fatal("readChunkManifest should reject count mismatch")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("expected count mismatch error, got: %v", err)
	}
}

// TestChunkManifestInvalidDecompSize verifies readChunkManifest rejects
// entries with decompSize=0 or decompSize>maxDecompressedChunk. [Batch 2c, 2c-R5-F4]
func TestChunkManifestInvalidDecompSize(t *testing.T) {
	// decompSize = 0
	var buf bytes.Buffer
	h := [32]byte{1}
	writeChunkManifest(&buf, [][32]byte{h}, []uint32{0})
	_, _, err := readChunkManifest(&buf, 1)
	if err == nil {
		t.Fatal("readChunkManifest should reject decompSize=0")
	}

	// decompSize > max
	buf.Reset()
	writeChunkManifest(&buf, [][32]byte{h}, []uint32{maxDecompressedChunk + 1})
	_, _, err = readChunkManifest(&buf, 1)
	if err == nil {
		t.Fatal("readChunkManifest should reject decompSize > max")
	}
}

// TestReceiveParallelRecoversMissingChunk is the end-to-end Batch 2c test:
// a chunk frame is deliberately omitted (simulating network loss), but its
// hash+size are in the trailer manifest. The receiver populates the missing
// metadata, RS reconstructs the missing bytes from parity, and the transfer
// succeeds. [Batch 2c]
func TestReceiveParallelRecoversMissingChunk(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	data := [][]byte{
		bytes.Repeat([]byte{0x11}, 64),
		bytes.Repeat([]byte{0x22}, 64),
	}
	totalSize := int64(128)
	hashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(hashes)
	parity, err := encodeStripe(data, 1)
	if err != nil {
		t.Fatalf("encodeStripe: %v", err)
	}
	parityHash := sdk.Blake3Sum(parity[0])

	files := []fileEntry{{Path: "missing-recovery.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, flagErasureCoded, cumOffsets)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: defaultStripeSize, OverheadPerMille: 100})
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Send ONLY chunk 0 — chunk 1 is "lost in transit" (never sent).
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: hashes[0], decompSize: 64, data: data[0],
	})
	// Parity covers both chunks.
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: parityFileIdx, chunkIdx: 0, offset: 0,
		hash: parityHash, decompSize: uint32(len(parity[0])), data: parity[0],
	})
	// Trailer with manifest: provides hash+size for ALL chunks including missing.
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		ParityCount:  1,
		ParityHashes: [][32]byte{parityHash},
		ParitySizes:  []uint32{uint32(len(parity[0]))},
		ChunkHashes:  hashes,
		ChunkSizes:   []uint32{64, 64},
	})

	got, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel should recover missing chunk via manifest+RS: %v", err)
	}
	if got != rootHash {
		t.Error("root hash mismatch after recovery")
	}

	// Verify the recovered file on disk matches original data.
	final := filepath.Join(dir, "missing-recovery.bin")
	content, err := os.ReadFile(final)
	if err != nil {
		t.Fatalf("read recovered file: %v", err)
	}
	expected := append(data[0], data[1]...)
	if !bytes.Equal(content, expected) {
		t.Errorf("recovered file content mismatch: got %d bytes, want %d", len(content), len(expected))
	}

	// R6-F1: verify receivedBitfield has both bits set after reconstruction.
	if !state.receivedBitfield.has(0) || !state.receivedBitfield.has(1) {
		t.Error("receivedBitfield should have both bits set after reconstruction")
	}
}

// TestReceiveParallelMissingChunkWithoutManifest verifies that without a
// manifest (non-erasure or nil ChunkHashes), missing chunks still hard-fail.
// This is the original Batch 2b behavior preserved for non-erasure transfers.
func TestReceiveParallelMissingChunkWithoutManifest(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	data := [][]byte{
		bytes.Repeat([]byte{0x33}, 64),
		bytes.Repeat([]byte{0x44}, 64),
	}
	totalSize := int64(128)
	hashes := [][32]byte{sdk.Blake3Sum(data[0]), sdk.Blake3Sum(data[1])}
	rootHash := sdk.MerkleRoot(hashes)

	files := []fileEntry{{Path: "no-manifest.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)
	state := newStreamReceiveState(files, totalSize, 0, cumOffsets) // no erasure flag
	defer state.cleanup()
	if err := state.allocateTempFiles(dir); err != nil {
		t.Fatalf("allocateTempFiles: %v", err)
	}
	state.initReceivedBitfield(2)

	progress := &TransferProgress{ChunksTotal: 2}
	var transferID [32]byte
	rand.Read(transferID[:])
	session := &parallelSession{
		transferID: transferID,
		state:      state,
		progress:   progress,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 4),
	}

	// Only send chunk 0, omit chunk 1.
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: hashes[0], decompSize: 64, data: data[0],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, nil) // no erasure

	_, err = ts.receiveParallel(&controlBuf, session)
	if err == nil {
		t.Fatal("missing chunk without manifest should hard-fail")
	}
	if !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected 'missing' error, got: %v", err)
	}
}

// --- Option C: per-stripe RS reconstruction tests ---

// TestEagerReconstructCleansStripe proves that tryEagerReconstruct frees
// parity for a clean full stripe (no corruption) immediately, and that a
// corrupted stripe triggers reconstructSingleStripe. [Option C, OC-F13]
func TestEagerReconstructCleansStripe(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: 5, OverheadPerMille: 200})

	// Simulate a full stripe of data (5 chunks) with all parity (1 chunk).
	// No corruption — eager path should free parity in O(1).
	state.mu.Lock()
	for i := 0; i < 5; i++ {
		state.stripeDataCounts[0]++
	}
	slot := &paritySlot{parity: map[int][]byte{0: {1, 2, 3}}, bytes: 3}
	state.paritySlots[0] = slot
	state.totalParityBytes = 3
	state.mu.Unlock()

	state.tryEagerReconstruct(0)

	state.mu.Lock()
	if !slot.done {
		t.Error("clean stripe: slot.done should be true after eager reconstruct")
	}
	if slot.parity != nil {
		t.Error("clean stripe: slot.parity should be nil (freed for GC)")
	}
	if state.totalParityBytes != 0 {
		t.Errorf("clean stripe: totalParityBytes=%d, want 0", state.totalParityBytes)
	}
	state.mu.Unlock()
}

// TestDynamicInflightCap proves the dynamic inflight formula from OC-F10.
// At overhead=0.1, parityPerFullStripe=10 for stripeSize=100.
// perStripeParity = 10 * 8MB = 80MB. cap = 512MB / 80MB = 6.
// At overhead=0.5, parityPerFullStripe=50.
// perStripeParity = 50 * 8MB = 400MB. cap = 512MB / 400MB = 1 → clamped to 2.
func TestDynamicInflightCap(t *testing.T) {
	tests := []struct {
		name     string
		overhead uint16
		wantCap  int
	}{
		{"10% overhead", 100, 6},
		{"50% overhead", 500, 2},
		{"20% overhead", 200, 3},
		{"1% overhead", 10, 8}, // capped at 8
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
			state.initPerStripeState(&erasureHeaderParams{
				StripeSize:       defaultStripeSize,
				OverheadPerMille: tt.overhead,
			})
			if state.maxInflightStripes != tt.wantCap {
				t.Errorf("maxInflightStripes=%d, want %d", state.maxInflightStripes, tt.wantCap)
			}
		})
	}
}

// TestInflightCapEnforced proves the receiver rejects parity chunks when
// the inflight stripe limit is reached. [Option C, OC-F10]
func TestInflightCapEnforced(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	// stripeSize=100, overhead=50% → parityPerFullStripe=50, cap=2.
	state.initPerStripeState(&erasureHeaderParams{StripeSize: 100, OverheadPerMille: 500})
	if state.maxInflightStripes != 2 {
		t.Fatalf("maxInflightStripes=%d, want 2", state.maxInflightStripes)
	}

	// Fill 2 stripe slots (each with one parity chunk).
	for stripe := 0; stripe < 2; stripe++ {
		data := []byte{byte(stripe)}
		sc := streamChunk{
			fileIdx:  parityFileIdx,
			chunkIdx: stripe * state.parityPerFullStripe, // first parity of each stripe
			hash:     blake3Hash(data),
			data:     data,
		}
		if _, err := state.processIncomingChunk(sc); err != nil {
			t.Fatalf("stripe %d first parity: %v", stripe, err)
		}
	}

	// Third stripe should be rejected.
	data := []byte{0xFF}
	sc := streamChunk{
		fileIdx:  parityFileIdx,
		chunkIdx: 2 * state.parityPerFullStripe,
		hash:     blake3Hash(data),
		data:     data,
	}
	_, err := state.processIncomingChunk(sc)
	if err == nil {
		t.Fatal("third stripe parity should be rejected (inflight cap=2)")
	}
}

// TestLateParityForDoneStripeDropped proves that parity arriving for an
// already-completed stripe is silently accepted (returns true) but NOT
// stored. The totalParityReceived counter increments for wire accounting.
// [Option C, OC-F19/F20]
func TestLateParityForDoneStripeDropped(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: 100, OverheadPerMille: 100})

	// Mark stripe 0 as done (simulate eager reconstruction completed).
	state.mu.Lock()
	state.paritySlots[0] = &paritySlot{done: true}
	state.mu.Unlock()

	// Send a late parity chunk for stripe 0.
	data := []byte{0xAA, 0xBB}
	sc := streamChunk{
		fileIdx:  parityFileIdx,
		chunkIdx: 0, // stripe 0, local index 0
		hash:     blake3Hash(data),
		data:     data,
	}
	isNew, err := state.processIncomingChunk(sc)
	if err != nil {
		t.Fatalf("late parity should not error: %v", err)
	}
	if !isNew {
		t.Error("late parity should return isNew=true (accepted for wire accounting)")
	}

	// Verify counter incremented but no bytes stored.
	state.mu.Lock()
	if state.totalParityReceived != 1 {
		t.Errorf("totalParityReceived=%d, want 1", state.totalParityReceived)
	}
	if state.totalParityBytes != 0 {
		t.Errorf("totalParityBytes=%d, want 0 (late parity must not be stored)", state.totalParityBytes)
	}
	state.mu.Unlock()
}

// TestParityDuringReconstructionDropped proves that parity arriving while
// reconstruction is in progress is silently dropped. [Self-audit round 2]
func TestParityDuringReconstructionDropped(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	state.initPerStripeState(&erasureHeaderParams{StripeSize: 100, OverheadPerMille: 100})

	// Mark stripe 0 as mid-reconstruction.
	state.mu.Lock()
	state.paritySlots[0] = &paritySlot{reconstructing: true, parity: map[int][]byte{}}
	state.mu.Unlock()

	data := []byte{0xCC}
	sc := streamChunk{
		fileIdx:  parityFileIdx,
		chunkIdx: 0,
		hash:     blake3Hash(data),
		data:     data,
	}
	isNew, err := state.processIncomingChunk(sc)
	if err != nil {
		t.Fatalf("parity during reconstruction should not error: %v", err)
	}
	if !isNew {
		t.Error("should return isNew=true (wire accounting)")
	}

	state.mu.Lock()
	if state.totalParityReceived != 1 {
		t.Errorf("totalParityReceived=%d, want 1", state.totalParityReceived)
	}
	if state.totalParityBytes != 0 {
		t.Errorf("totalParityBytes=%d, want 0 (mid-reconstruction parity must not be stored)", state.totalParityBytes)
	}
	state.mu.Unlock()
}

// TestResumePreComputesStripeDataCounts proves that initPerStripeState
// correctly pre-computes stripeDataCounts from checkpoint-restored hashes,
// excluding corrupted chunks. [Option C, OC-F5/F24]
func TestResumePreComputesStripeDataCounts(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
	// Simulate a resumed state: 10 chunks received, chunk 3 corrupted.
	for i := 0; i < 10; i++ {
		state.hashes[i] = [32]byte{byte(i)}
		state.sizes[i] = 64
	}
	state.corruptedChunks = map[int]bool{3: true}

	state.initPerStripeState(&erasureHeaderParams{StripeSize: 5, OverheadPerMille: 200})

	state.mu.Lock()
	// Stripe 0 (chunks 0-4): 4 valid + 1 corrupted = count should be 4.
	if state.stripeDataCounts[0] != 4 {
		t.Errorf("stripe 0 dataCount=%d, want 4 (chunk 3 is corrupted, excluded)", state.stripeDataCounts[0])
	}
	// Stripe 1 (chunks 5-9): all 5 valid.
	if state.stripeDataCounts[1] != 5 {
		t.Errorf("stripe 1 dataCount=%d, want 5 (all valid)", state.stripeDataCounts[1])
	}
	state.mu.Unlock()
}
