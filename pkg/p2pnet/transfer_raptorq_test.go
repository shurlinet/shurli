package p2pnet

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestRaptorQRoundtrip(t *testing.T) {
	// Create test data (multiple symbols worth).
	dataSize := raptorqSymbolSize * 10 // 10 source symbols
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	// Encode.
	enc, err := newRaptorQEncoder(data)
	if err != nil {
		t.Fatalf("newRaptorQEncoder: %v", err)
	}

	k := enc.sourceSymbolCount()
	if k == 0 {
		t.Fatal("expected k > 0")
	}
	t.Logf("source symbols K=%d, repair=%d", k, enc.repairSymbolCount())

	// Decode using only source symbols (should work perfectly).
	dec, err := newRaptorQDecoder(uint32(dataSize))
	if err != nil {
		t.Fatalf("newRaptorQDecoder: %v", err)
	}

	for i := uint32(0); i < k; i++ {
		sym := enc.genSymbol(i)
		canDecode, err := dec.addSymbol(i, sym)
		if err != nil {
			t.Fatalf("addSymbol(%d): %v", i, err)
		}
		if i == k-1 && !canDecode {
			t.Fatal("expected canDecode=true after adding all K source symbols")
		}
	}

	ok, decoded, err := dec.decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("decoded data does not match original")
	}
}

func TestRaptorQRepairRecovery(t *testing.T) {
	// Test recovery using repair symbols to replace missing source symbols.
	dataSize := raptorqSymbolSize * 8
	data := make([]byte, dataSize)
	if _, err := rand.Read(data); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}

	enc, err := newRaptorQEncoder(data)
	if err != nil {
		t.Fatalf("newRaptorQEncoder: %v", err)
	}

	k := enc.sourceSymbolCount()
	repairCount := enc.repairSymbolCount()
	t.Logf("K=%d, repair=%d", k, repairCount)

	dec, err := newRaptorQDecoder(uint32(dataSize))
	if err != nil {
		t.Fatalf("newRaptorQDecoder: %v", err)
	}

	// Skip first 2 source symbols, add the rest.
	skipCount := uint32(2)
	if skipCount > k/2 {
		skipCount = 1
	}
	for i := skipCount; i < k; i++ {
		sym := enc.genSymbol(i)
		dec.addSymbol(i, sym)
	}

	// Add repair symbols to compensate for skipped source symbols.
	// Need at least skipCount repair symbols.
	for i := uint32(0); i < skipCount+2; i++ { // +2 extra for safety margin
		repairID := k + i
		sym := enc.genSymbol(repairID)
		canDecode, err := dec.addSymbol(repairID, sym)
		if err != nil {
			t.Fatalf("addSymbol(repair %d): %v", repairID, err)
		}
		if canDecode {
			break
		}
	}

	ok, decoded, err := dec.decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true after adding repair symbols")
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("decoded data does not match original")
	}
}

func TestRaptorQSmallData(t *testing.T) {
	// Test with data smaller than one symbol.
	data := []byte("hello world - small data test")

	enc, err := newRaptorQEncoder(data)
	if err != nil {
		t.Fatalf("newRaptorQEncoder: %v", err)
	}

	k := enc.sourceSymbolCount()
	t.Logf("small data: K=%d", k)

	dec, err := newRaptorQDecoder(uint32(len(data)))
	if err != nil {
		t.Fatalf("newRaptorQDecoder: %v", err)
	}

	for i := uint32(0); i < k; i++ {
		sym := enc.genSymbol(i)
		dec.addSymbol(i, sym)
	}

	ok, decoded, err := dec.decode()
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !bytes.Equal(decoded, data) {
		t.Fatalf("decoded data mismatch: got %q, want %q", decoded, data)
	}
}
