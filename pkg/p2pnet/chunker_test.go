package p2pnet

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestChunkTargetAdaptive(t *testing.T) {
	tests := []struct {
		fileSize         int64
		expectedAvg      int
	}{
		{100 << 20, 128 << 10},   // 100 MB -> 128K avg
		{500 << 20, 256 << 10},   // 500 MB -> 256K avg
		{2 << 30, 512 << 10},     // 2 GB -> 512K avg
		{10 << 30, 1 << 20},      // 10 GB -> 1M avg
	}

	for _, tt := range tests {
		_, avg, _ := ChunkTarget(tt.fileSize)
		if avg != tt.expectedAvg {
			t.Errorf("ChunkTarget(%d): avg=%d, want %d", tt.fileSize, avg, tt.expectedAvg)
		}
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
