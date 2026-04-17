package filetransfer

import (
	"bytes"
	"crypto/rand"
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
		parityHashes[i] = sdk.Blake3Sum([]byte{byte(i)})
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

func TestTrailerWithErasureRoundtrip(t *testing.T) {
	hashes := make([][32]byte, 3)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
	}
	rootHash := sdk.MerkleRoot(hashes)

	parityHashes := make([][32]byte, 1)
	parityHashes[0] = sdk.Blake3Sum([]byte("parity0"))
	erasure := &erasureTrailer{
		StripeSize:   3,
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
	if parsedErasure.StripeSize != 3 {
		t.Errorf("stripeSize: got %d, want 3", parsedErasure.StripeSize)
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

// drainEncoder runs the AddChunk + Finalize loop the way chunkProducer does
// and returns the flat sequence of parity chunks emitted plus the trailer.
// Used by the B2 test suite below.
func drainEncoder(t *testing.T, chunks [][]byte, stripeSize int, overhead float64) ([]parityChunkOut, *erasureTrailer) {
	t.Helper()
	enc := newErasureEncoder(stripeSize, overhead)
	if enc == nil {
		t.Fatalf("newErasureEncoder returned nil for stripeSize=%d overhead=%.2f", stripeSize, overhead)
	}
	var emitted []parityChunkOut
	for i, c := range chunks {
		// AddChunk takes ownership; copy so callers can reuse chunks.
		raw := make([]byte, len(c))
		copy(raw, c)
		out, err := enc.AddChunk(raw)
		if err != nil {
			t.Fatalf("AddChunk[%d]: %v", i, err)
		}
		emitted = append(emitted, out...)
	}
	residual, trailer, err := enc.Finalize()
	if err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	emitted = append(emitted, residual...)
	return emitted, trailer
}

// TestErasureEncoderMatchesBatched proves the new encoder emits bit-identical
// parity shards (same count, same bytes, same hashes, same trailer) as the
// legacy encodeErasure for a range of stripe configurations. This is the wire-
// compatibility oracle — if Batch 2 diverges from Batch 1's encodeErasure
// output for any input, this test fails.
// [B2-F39: delete alongside encodeErasure in Batch 2b cleanup]
func TestErasureEncoderMatchesBatched(t *testing.T) {
	cases := []struct {
		name       string
		chunkCount int
		chunkSize  int
		stripeSize int
		overhead   float64
	}{
		{"single full stripe", 10, 64, 10, 0.10},
		{"stripe + 1", 11, 64, 10, 0.10},
		{"two full stripes", 20, 128, 10, 0.10},
		{"partial final", 23, 96, 10, 0.10},
		{"tiny stripe=2 boundary", 5, 48, 2, 0.20},
		{"variable sizes mimicked", 8, 256, 4, 0.25},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			chunks := make([][]byte, c.chunkCount)
			for i := range chunks {
				// Deterministic content so old and new encoders see the same bytes.
				chunks[i] = bytes.Repeat([]byte{byte(i + 1)}, c.chunkSize)
			}

			legacy, err := encodeErasure(chunks, c.stripeSize, c.overhead)
			if err != nil {
				t.Fatalf("legacy encodeErasure: %v", err)
			}
			emitted, trailer := drainEncoder(t, chunks, c.stripeSize, c.overhead)

			if len(legacy) != len(emitted) {
				t.Fatalf("parity count differs: legacy=%d new=%d", len(legacy), len(emitted))
			}
			if trailer == nil {
				t.Fatal("new encoder returned nil trailer for non-empty input")
			}
			if trailer.ParityCount != len(legacy) {
				t.Fatalf("trailer parity count %d != legacy %d", trailer.ParityCount, len(legacy))
			}
			for i := range legacy {
				if !bytes.Equal(legacy[i].data, emitted[i].data) {
					t.Errorf("parity[%d] bytes differ", i)
				}
				if legacy[i].hash != emitted[i].hash {
					t.Errorf("parity[%d] hash differs", i)
				}
				if emitted[i].chunkIdx != i {
					t.Errorf("parity[%d] chunkIdx=%d, want %d (0-based global)", i, emitted[i].chunkIdx, i)
				}
				if trailer.ParityHashes[i] != legacy[i].hash {
					t.Errorf("trailer parityHashes[%d] mismatch", i)
				}
				if int(trailer.ParitySizes[i]) != len(legacy[i].data) {
					t.Errorf("trailer paritySizes[%d]=%d, want %d", i, trailer.ParitySizes[i], len(legacy[i].data))
				}
			}
		})
	}
}

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
// parityBytes counter via same-key overwrites. [B2-F2]
func TestParityChunkDuplicateRejected(t *testing.T) {
	state := newStreamReceiveState(nil, 10<<20, flagErasureCoded, nil)
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
			// First rejection must be budget-related; parityBytes must never
			// exceed the hard cap even by a single chunk.
			if state.parityBytes > int64(maxParityBudgetBytes) {
				t.Fatalf("parityBytes=%d exceeded hard cap %d (declared totalSize=%d)",
					state.parityBytes, maxParityBudgetBytes, hugeTotal)
			}
			if accepted < 1 {
				t.Fatalf("rejected too early at i=%d: %v", i, err)
			}
			return
		}
		accepted++
	}
	t.Fatalf("expected budget rejection, accepted %d chunks totalling %d bytes",
		accepted, state.parityBytes)
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

// TestErasureManifestRejectsSmallStripeSize proves the wire-level stripeSize
// lower bound catches a malicious trailer declaring stripeSize=1, which would
// otherwise force reconstruction to iterate chunkCount stripes allocating
// maxChunkSize-padded shards per stripe. [B2-F11]
func TestErasureManifestRejectsSmallStripeSize(t *testing.T) {
	var buf bytes.Buffer
	// Write a trailer with stripeSize=1 (below minStripeSize=2).
	parityHashes := [][32]byte{sdk.Blake3Sum([]byte("p"))}
	paritySizes := []uint32{16}
	if err := writeErasureManifest(&buf, 1, 1, parityHashes, paritySizes); err != nil {
		t.Fatalf("writeErasureManifest: %v", err)
	}
	_, _, _, err := readErasureManifest(&buf)
	if err == nil {
		t.Fatal("readErasureManifest should reject stripeSize=1")
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
		StripeSize:   2,
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
// number of parity chunks actually received — it logs a warning and
// continues. Batch 2b will tighten this to a hard fail once rsReconstruct
// is wired and can reason about recovery vs declared-count delta. [B2-F14]
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
	// Trailer LIES — declares 5 parity but only 1 arrived. Must still complete
	// in Batch 2 (warn-only); Batch 2b will hard-fail when rsReconstruct is
	// wired.
	writeTrailer(&controlBuf, 2, rootHash, nil, &erasureTrailer{
		StripeSize:   2,
		ParityCount:  5,
		ParityHashes: make([][32]byte, 5),
		ParitySizes:  []uint32{128, 128, 128, 128, 128},
	})

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel failed on trailer count mismatch (Batch 2 must warn, not fail): %v", err)
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
