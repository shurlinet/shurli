package p2pnet

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestCompressDecompressRoundtrip(t *testing.T) {
	// Compressible data (repeated pattern).
	data := bytes.Repeat([]byte("hello world, this is compressible data! "), 1000)

	compressed, ok := compressChunk(data)
	if !ok {
		t.Fatal("expected compression to succeed on compressible data")
	}
	if len(compressed) >= len(data) {
		t.Errorf("compressed %d should be smaller than original %d", len(compressed), len(data))
	}

	decompressed, err := decompressChunk(compressed, len(data)*2)
	if err != nil {
		t.Fatalf("decompressChunk: %v", err)
	}
	if !bytes.Equal(decompressed, data) {
		t.Error("round-trip data mismatch")
	}
}

func TestCompressIncompressible(t *testing.T) {
	// Random data is incompressible.
	data := make([]byte, 64*1024)
	rand.Read(data)

	result, ok := compressChunk(data)
	if ok {
		t.Error("expected compression to be skipped for random data")
	}
	// When skipped, original data is returned.
	if !bytes.Equal(result, data) {
		t.Error("should return original data when compression skipped")
	}
}

func TestDecompressMaxOutput(t *testing.T) {
	// Compress some data, then try to decompress with a limit smaller than actual.
	data := bytes.Repeat([]byte("A"), 100*1024)
	compressed, ok := compressChunk(data)
	if !ok {
		t.Fatal("expected compression to succeed")
	}

	// Try decompressing with a 1 KB limit (actual is 100 KB).
	_, err := decompressChunk(compressed, 1024)
	if err == nil {
		t.Error("expected error when max output is exceeded")
	}
}

func TestCompressEmpty(t *testing.T) {
	result, ok := compressChunk(nil)
	// Empty data compression behavior - should return original (not worth compressing).
	if ok {
		t.Log("empty data was 'compressed' - acceptable")
	}
	_ = result
}
