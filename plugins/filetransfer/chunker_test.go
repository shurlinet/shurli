package filetransfer

import (
	"bytes"
	"crypto/rand"
	mrand "math/rand"
	"testing"
)

func TestChunkTargetAdaptive(t *testing.T) {
	// Cases cover each FT-Y #14 tier plus a boundary on both sides.
	tests := []struct {
		fileSize    int64
		expectedMin int
		expectedAvg int
		expectedMax int
	}{
		{1 << 20, 64 << 10, 128 << 10, 256 << 10},    // 1 MB -> tier 1
		{63 << 20, 64 << 10, 128 << 10, 256 << 10},   // just under 64 MB -> tier 1
		{64 << 20, 128 << 10, 256 << 10, 512 << 10},  // 64 MB boundary -> tier 2
		{100 << 20, 128 << 10, 256 << 10, 512 << 10}, // 100 MB -> tier 2
		{500 << 20, 128 << 10, 256 << 10, 512 << 10}, // 500 MB -> tier 2
		{512 << 20, 256 << 10, 512 << 10, 1 << 20},   // 512 MB boundary -> tier 3
		{1 << 30, 256 << 10, 512 << 10, 1 << 20},     // 1 GB -> tier 3
		{2 << 30, 512 << 10, 1 << 20, 2 << 20},       // 2 GB boundary -> tier 4
		{5 << 30, 512 << 10, 1 << 20, 2 << 20},       // 5 GB -> tier 4
		{8 << 30, 1 << 20, 2 << 20, 4 << 20},         // 8 GB boundary -> tier 5
		{28 << 30, 1 << 20, 2 << 20, 4 << 20},        // 28 GB (TS-3 physical) -> tier 5
	}

	for _, tt := range tests {
		minS, avg, maxS := ChunkTarget(tt.fileSize)
		if minS != tt.expectedMin || avg != tt.expectedAvg || maxS != tt.expectedMax {
			t.Errorf("ChunkTarget(%d): got (min=%d,avg=%d,max=%d), want (min=%d,avg=%d,max=%d)",
				tt.fileSize, minS, avg, maxS, tt.expectedMin, tt.expectedAvg, tt.expectedMax)
		}
	}
}

// TestChunkTargetTopTierWithinWireLimit guards the coupling between the
// largest ChunkTarget tier's max and maxChunkWireSize. FT-Y #14 bumped the top
// tier to 4 MB max; if someone bumps ChunkTarget again without also bumping
// maxChunkWireSize, the sender will produce chunks the receiver rejects.
func TestChunkTargetTopTierWithinWireLimit(t *testing.T) {
	_, _, maxS := ChunkTarget(1 << 40) // 1 TB -> always top tier
	if maxS > maxChunkWireSize {
		t.Fatalf("top tier max %d exceeds maxChunkWireSize %d", maxS, maxChunkWireSize)
	}
}

func TestChunkReaderSmallFile(t *testing.T) {
	data := []byte("hello world, this is a small test file for chunking")
	r := bytes.NewReader(data)

	var chunks []Chunk
	err := ChunkReader(r, int64(len(data)), func(c Chunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatalf("ChunkReader: %v", err)
	}

	// Small file should produce exactly 1 chunk (below min size).
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	if !bytes.Equal(chunks[0].Data, data) {
		t.Error("chunk data mismatch")
	}
	if chunks[0].Offset != 0 {
		t.Errorf("offset: got %d, want 0", chunks[0].Offset)
	}

	// Hash should be non-zero.
	if chunks[0].Hash == [32]byte{} {
		t.Error("chunk hash should not be zero")
	}
}

func TestChunkReaderReassembly(t *testing.T) {
	// Generate random data larger than min chunk size to get multiple chunks.
	data := make([]byte, 512*1024) // 512 KB
	rand.Read(data)

	r := bytes.NewReader(data)
	var chunks []Chunk
	err := ChunkReader(r, int64(len(data)), func(c Chunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatalf("ChunkReader: %v", err)
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	// Reassemble and verify.
	var assembled bytes.Buffer
	for _, c := range chunks {
		assembled.Write(c.Data)
	}
	if !bytes.Equal(assembled.Bytes(), data) {
		t.Errorf("reassembled data mismatch: got %d bytes, want %d", assembled.Len(), len(data))
	}

	// Verify offsets are sequential.
	var expectedOffset int64
	for i, c := range chunks {
		if c.Offset != expectedOffset {
			t.Errorf("chunk %d offset: got %d, want %d", i, c.Offset, expectedOffset)
		}
		expectedOffset += int64(len(c.Data))
	}
}

func TestChunkReaderDeterministic(t *testing.T) {
	// Same data should produce same chunks.
	data := make([]byte, 256*1024)
	rand.Read(data)

	chunk := func() []Chunk {
		var chunks []Chunk
		ChunkReader(bytes.NewReader(data), int64(len(data)), func(c Chunk) error {
			chunks = append(chunks, c)
			return nil
		})
		return chunks
	}

	chunks1 := chunk()
	chunks2 := chunk()

	if len(chunks1) != len(chunks2) {
		t.Fatalf("non-deterministic: %d vs %d chunks", len(chunks1), len(chunks2))
	}
	for i := range chunks1 {
		if chunks1[i].Hash != chunks2[i].Hash {
			t.Errorf("chunk %d hash differs", i)
		}
	}
}

func TestChunkReaderEmpty(t *testing.T) {
	r := bytes.NewReader(nil)
	var chunks []Chunk
	err := ChunkReader(r, 0, func(c Chunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatalf("ChunkReader empty: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty file, got %d", len(chunks))
	}
}

func TestGearTableInitialized(t *testing.T) {
	// Verify gear table has no all-zero entries (would break cut detection).
	zeros := 0
	for _, v := range gearTable {
		if v == 0 {
			zeros++
		}
	}
	// Statistically impossible to have more than a handful of zeros.
	if zeros > 2 {
		t.Errorf("gear table has %d zero entries, expected near 0", zeros)
	}
}

// Benchmarks for FT-Y #14 tier throughput (TE-70). Each benchmark chunks a
// fixed 16 MB payload while advertising a fileSize hint that selects a
// particular ChunkTarget tier. Apples-to-apples: same input bytes, different
// chunk boundaries. Run with:
//
//	go test -run=^$ -bench=BenchmarkChunkReader_Tier -benchmem ./plugins/filetransfer/
//
// Reports throughput (MB/s via b.SetBytes), allocations per chunk, and
// allocated bytes per op. Use to validate that larger chunks actually improve
// throughput on a given host before shipping the tier change.
func benchmarkChunkReaderTier(b *testing.B, hintSize int64) {
	const payload = 16 << 20
	// Deterministic pseudo-random payload: seeded math/rand so each run is
	// reproducible across CI hosts. Entropy is required for the gear-hash to
	// produce realistic cut-point distributions; a constant pattern would
	// collapse to the maxSize fallback and under-report per-cut cost.
	data := make([]byte, payload)
	rng := mrand.New(mrand.NewSource(1))
	rng.Read(data)

	b.SetBytes(int64(payload))
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		r := bytes.NewReader(data)
		chunks := 0
		if err := ChunkReader(r, hintSize, func(c Chunk) error {
			chunks++
			return nil
		}); err != nil {
			b.Fatalf("ChunkReader: %v", err)
		}
		if chunks == 0 {
			b.Fatal("expected at least one chunk")
		}
	}
}

// Tier 1 (< 64 MB): 64K / 128K / 256K.
func BenchmarkChunkReader_Tier1_64K_128K_256K(b *testing.B) {
	benchmarkChunkReaderTier(b, 32<<20)
}

// Tier 2 (< 512 MB): 128K / 256K / 512K.
func BenchmarkChunkReader_Tier2_128K_256K_512K(b *testing.B) {
	benchmarkChunkReaderTier(b, 256<<20)
}

// Tier 3 (< 2 GB): 256K / 512K / 1M.
func BenchmarkChunkReader_Tier3_256K_512K_1M(b *testing.B) {
	benchmarkChunkReaderTier(b, 1<<30)
}

// Tier 4 (< 8 GB): 512K / 1M / 2M.
func BenchmarkChunkReader_Tier4_512K_1M_2M(b *testing.B) {
	benchmarkChunkReaderTier(b, 4<<30)
}

// Tier 5 (>= 8 GB): 1M / 2M / 4M (FT-Y #14 new top tier).
func BenchmarkChunkReader_Tier5_1M_2M_4M(b *testing.B) {
	benchmarkChunkReaderTier(b, 16<<30)
}
