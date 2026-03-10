package p2pnet

import (
	"bytes"
	"crypto/rand"
	"sync"
	"testing"
)

func TestPeerSymbolRange(t *testing.T) {
	tests := []struct {
		name      string
		k         uint32
		peerIndex int
		numPeers  int
		wantStart uint32
		wantCount uint32
	}{
		{"single-peer", 100, 0, 1, 0, 120},            // 100 + 20% repair
		{"two-peers-first", 100, 0, 2, 0, 60},          // 50 + 10 repair
		{"two-peers-second", 100, 1, 2, 60, 60},        // starts after first
		{"four-peers-third", 100, 2, 4, 60, 30},        // 25 + 5 repair, starts at 2*30
		{"small-k-single", 2, 0, 1, 0, 3},              // 2 + min 1 repair
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			start, count := peerSymbolRange(tt.k, tt.peerIndex, tt.numPeers)
			if start != tt.wantStart {
				t.Errorf("startID: got %d, want %d", start, tt.wantStart)
			}
			if count != tt.wantCount {
				t.Errorf("count: got %d, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestPeerSymbolRangesNoOverlap(t *testing.T) {
	k := uint32(200)
	numPeers := 4

	var ranges []struct{ start, end uint32 }
	for i := 0; i < numPeers; i++ {
		start, count := peerSymbolRange(k, i, numPeers)
		ranges = append(ranges, struct{ start, end uint32 }{start, start + count})
	}

	// Verify no overlap between consecutive ranges.
	for i := 1; i < len(ranges); i++ {
		if ranges[i].start < ranges[i-1].end {
			t.Errorf("peer %d range [%d,%d) overlaps with peer %d range [%d,%d)",
				i, ranges[i].start, ranges[i].end,
				i-1, ranges[i-1].start, ranges[i-1].end)
		}
	}
}

func TestMultiPeerSessionSinglePeer(t *testing.T) {
	// Simulate single-peer fountain download.
	blockSize := uint32(raptorqSymbolSize * 5) // 5 symbols per block
	blockCount := 3
	blockData := make([][]byte, blockCount)
	blockHashes := make([][32]byte, blockCount)
	blockSizes := make([]uint32, blockCount)

	for i := 0; i < blockCount; i++ {
		blockData[i] = make([]byte, blockSize)
		rand.Read(blockData[i])
		blockHashes[i] = blake3Hash(blockData[i])
		blockSizes[i] = blockSize
	}

	manifest := &transferManifest{
		Filename:    "test.bin",
		FileSize:    int64(blockSize) * int64(blockCount),
		ChunkCount:  blockCount,
		RootHash:    MerkleRoot(blockHashes),
		ChunkHashes: blockHashes,
		ChunkSizes:  blockSizes,
	}

	progress := &TransferProgress{}
	session := newMultiPeerSession(manifest, progress)

	// Encode each block and feed source symbols.
	for bi := 0; bi < blockCount; bi++ {
		enc, err := newRaptorQEncoder(blockData[bi])
		if err != nil {
			t.Fatalf("encode block %d: %v", bi, err)
		}

		k := enc.sourceSymbolCount()
		for sid := uint32(0); sid < k; sid++ {
			sym := enc.genSymbol(sid)
			complete, err := session.addSymbol(bi, sid, sym)
			if err != nil {
				t.Fatalf("addSymbol block=%d sym=%d: %v", bi, sid, err)
			}
			if complete && bi < blockCount-1 {
				t.Fatalf("complete too early at block %d sym %d", bi, sid)
			}
		}
	}

	if !session.isComplete() {
		t.Fatal("expected session to be complete")
	}

	results, err := session.results()
	if err != nil {
		t.Fatalf("results: %v", err)
	}

	for i, got := range results {
		if !bytes.Equal(got, blockData[i]) {
			t.Errorf("block %d data mismatch", i)
		}
		if err := session.verifyBlock(i, got); err != nil {
			t.Errorf("block %d verify: %v", i, err)
		}
	}
}

func TestMultiPeerSessionTwoPeers(t *testing.T) {
	// Simulate two peers each sending non-overlapping repair symbols.
	blockSize := uint32(raptorqSymbolSize * 8) // 8 symbols per block
	blockCount := 2
	blockData := make([][]byte, blockCount)
	blockHashes := make([][32]byte, blockCount)
	blockSizes := make([]uint32, blockCount)

	for i := 0; i < blockCount; i++ {
		blockData[i] = make([]byte, blockSize)
		rand.Read(blockData[i])
		blockHashes[i] = blake3Hash(blockData[i])
		blockSizes[i] = blockSize
	}

	manifest := &transferManifest{
		Filename:    "multi.bin",
		FileSize:    int64(blockSize) * int64(blockCount),
		ChunkCount:  blockCount,
		RootHash:    MerkleRoot(blockHashes),
		ChunkHashes: blockHashes,
		ChunkSizes:  blockSizes,
	}

	progress := &TransferProgress{}
	session := newMultiPeerSession(manifest, progress)

	// Encode each block.
	encoders := make([]*raptorqEncoder, blockCount)
	for bi := 0; bi < blockCount; bi++ {
		var err error
		encoders[bi], err = newRaptorQEncoder(blockData[bi])
		if err != nil {
			t.Fatalf("encode block %d: %v", bi, err)
		}
	}

	k := encoders[0].sourceSymbolCount()

	// Peer 0: sends first half of source symbols.
	// Peer 1: sends second half + repair symbols.
	half := k / 2

	var wg sync.WaitGroup
	wg.Add(2)

	// Peer 0: symbols 0..half-1
	go func() {
		defer wg.Done()
		for bi := 0; bi < blockCount; bi++ {
			for sid := uint32(0); sid < half; sid++ {
				sym := encoders[bi].genSymbol(sid)
				session.addSymbol(bi, sid, sym)
			}
		}
	}()

	// Peer 1: symbols half..k-1 + a few repair symbols
	go func() {
		defer wg.Done()
		for bi := 0; bi < blockCount; bi++ {
			for sid := half; sid < k+5; sid++ { // +5 repair for RaptorQ probabilistic margin
				sym := encoders[bi].genSymbol(sid)
				session.addSymbol(bi, sid, sym)
			}
		}
	}()

	wg.Wait()

	if !session.isComplete() {
		t.Fatalf("expected complete, got %d/%d blocks",
			session.blocksDecoded.Load(), blockCount)
	}

	results, err := session.results()
	if err != nil {
		t.Fatalf("results: %v", err)
	}

	for i, got := range results {
		if !bytes.Equal(got, blockData[i]) {
			t.Errorf("block %d data mismatch", i)
		}
	}
}

func TestMultiPeerSessionRepairOnly(t *testing.T) {
	// One peer sends only repair symbols (no source symbols at all).
	// RaptorQ should still reconstruct from K repair symbols.
	blockSize := uint32(raptorqSymbolSize * 4) // 4 symbols per block
	data := make([]byte, blockSize)
	rand.Read(data)

	hash := blake3Hash(data)
	manifest := &transferManifest{
		Filename:    "repair-only.bin",
		FileSize:    int64(blockSize),
		ChunkCount:  1,
		RootHash:    MerkleRoot([][32]byte{hash}),
		ChunkHashes: [][32]byte{hash},
		ChunkSizes:  []uint32{blockSize},
	}

	progress := &TransferProgress{}
	session := newMultiPeerSession(manifest, progress)

	enc, err := newRaptorQEncoder(data)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}

	k := enc.sourceSymbolCount()

	// Send ONLY repair symbols (starting at K).
	for sid := k; sid < k*2+5; sid++ {
		sym := enc.genSymbol(sid)
		complete, err := session.addSymbol(0, sid, sym)
		if err != nil {
			t.Fatalf("addSymbol %d: %v", sid, err)
		}
		if complete {
			break
		}
	}

	if !session.isComplete() {
		t.Fatal("expected complete from repair-only symbols")
	}

	results, err := session.results()
	if err != nil {
		t.Fatalf("results: %v", err)
	}

	if !bytes.Equal(results[0], data) {
		t.Fatal("data mismatch from repair-only reconstruction")
	}
}

func TestMultiPeerDefaultConfig(t *testing.T) {
	cfg := defaultMultiPeerConfig()
	if cfg.MaxPeers < 1 {
		t.Errorf("MaxPeers should be >= 1, got %d", cfg.MaxPeers)
	}
	if cfg.PeerTimeout <= 0 {
		t.Errorf("PeerTimeout should be > 0, got %v", cfg.PeerTimeout)
	}
}
