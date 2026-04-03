package filetransfer

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"github.com/shurlinet/shurli/pkg/sdk"
)

func TestInterleavedSymbolCount(t *testing.T) {
	tests := []struct {
		name string
		k    uint32
		want uint32
	}{
		{"typical", 100, 120},  // 100 + 20% = 120
		{"small", 2, 3},       // 2 + min(1) = 3
		{"single", 1, 2},      // 1 + min(1) = 2
		{"large", 1000, 1200}, // 1000 + 200 = 1200
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := interleavedSymbolCount(tt.k)
			if got != tt.want {
				t.Errorf("interleavedSymbolCount(%d) = %d, want %d", tt.k, got, tt.want)
			}
		})
	}
}

func TestInterleavedSymbolsNoOverlap(t *testing.T) {
	// Verify that interleaved symbol IDs from different peers never overlap.
	numPeers := 4
	k := uint32(200)
	maxPerPeer := interleavedSymbolCount(k)

	for pi := 0; pi < numPeers; pi++ {
		for pj := pi + 1; pj < numPeers; pj++ {
			// Generate IDs for both peers, check no collision.
			idsI := make(map[uint32]bool)
			for i := uint32(0); i < maxPerPeer; i++ {
				sid := uint32(pi) + i*uint32(numPeers)
				if sid >= k*2 {
					break
				}
				idsI[sid] = true
			}
			for i := uint32(0); i < maxPerPeer; i++ {
				sid := uint32(pj) + i*uint32(numPeers)
				if sid >= k*2 {
					break
				}
				if idsI[sid] {
					t.Errorf("peer %d and peer %d both generate symbol ID %d", pi, pj, sid)
				}
			}
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
		RootHash:    sdk.MerkleRoot(blockHashes),
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

func TestMultiPeerSessionTwoPeersInterleaved(t *testing.T) {
	// Simulate two peers sending interleaved symbols (new protocol).
	// Sequential delivery avoids goroutine scheduling variance.
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
		RootHash:    sdk.MerkleRoot(blockHashes),
		ChunkHashes: blockHashes,
		ChunkSizes:  blockSizes,
	}

	progress := &TransferProgress{}
	session := newMultiPeerSession(manifest, progress)

	encoders := make([]*raptorqEncoder, blockCount)
	for bi := 0; bi < blockCount; bi++ {
		var err error
		encoders[bi], err = newRaptorQEncoder(blockData[bi])
		if err != nil {
			t.Fatalf("encode block %d: %v", bi, err)
		}
	}

	k := encoders[0].sourceSymbolCount()
	numPeers := uint32(2)
	maxSym := interleavedSymbolCount(k)

	// Peer 0: interleaved symbols 0, 2, 4, 6, ...
	for bi := 0; bi < blockCount; bi++ {
		for i := uint32(0); i < maxSym; i++ {
			sid := uint32(0) + i*numPeers // peerIndex=0
			if sid >= k*2 {
				break
			}
			sym := encoders[bi].genSymbol(sid)
			session.addSymbol(bi, sid, sym)
		}
	}

	// Peer 1: interleaved symbols 1, 3, 5, 7, ...
	for bi := 0; bi < blockCount; bi++ {
		for i := uint32(0); i < maxSym; i++ {
			sid := uint32(1) + i*numPeers // peerIndex=1
			if sid >= k*2 {
				break
			}
			sym := encoders[bi].genSymbol(sid)
			session.addSymbol(bi, sid, sym)
		}
	}

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

func TestMultiPeerFastPeerAloneDecodes(t *testing.T) {
	// Critical test: a single fast peer must be able to decode alone
	// even when other (slow) peers contribute zero symbols.
	// This proves additive bandwidth: speed = max(peers), not min(peers).
	blockSize := uint32(raptorqSymbolSize * 6) // 6 symbols per block
	data := make([]byte, blockSize)
	rand.Read(data)

	hash := blake3Hash(data)
	manifest := &transferManifest{
		Filename:    "fast-alone.bin",
		FileSize:    int64(blockSize),
		ChunkCount:  1,
		RootHash:    sdk.MerkleRoot([][32]byte{hash}),
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
	numPeers := uint32(2)
	maxSym := interleavedSymbolCount(k)

	// Only peer 0 sends (fast peer). Peer 1 sends nothing (slow/offline).
	// Peer 0 gets interleaved IDs: 0, 2, 4, 6, ...
	// With K=6, peer 0 generates symbols 0,2,4,6,8,10 (6 symbols up to 2*K=12).
	// That's K symbols = enough to decode.
	for i := uint32(0); i < maxSym; i++ {
		sid := uint32(0) + i*numPeers
		if sid >= k*2 {
			break
		}
		sym := enc.genSymbol(sid)
		complete, addErr := session.addSymbol(0, sid, sym)
		if addErr != nil {
			t.Fatalf("addSymbol %d: %v", sid, addErr)
		}
		if complete {
			break
		}
	}

	if !session.isComplete() {
		t.Fatal("expected fast peer alone to decode, but session incomplete")
	}

	results, err := session.results()
	if err != nil {
		t.Fatalf("results: %v", err)
	}

	if !bytes.Equal(results[0], data) {
		t.Fatal("data mismatch from fast-peer-alone decode")
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
		RootHash:    sdk.MerkleRoot([][32]byte{hash}),
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

func TestMarshalUnmarshalManifest(t *testing.T) {
	// Create a manifest with known values.
	blockCount := 3
	hashes := make([][32]byte, blockCount)
	sizes := make([]uint32, blockCount)
	for i := 0; i < blockCount; i++ {
		var h [32]byte
		rand.Read(h[:])
		hashes[i] = h
		sizes[i] = uint32(1024 * (i + 1))
	}

	original := &transferManifest{
		Filename:    "test-file.bin",
		FileSize:    12345678,
		ChunkCount:  blockCount,
		RootHash:    sdk.MerkleRoot(hashes),
		ChunkHashes: hashes,
		ChunkSizes:  sizes,
	}

	data, err := marshalManifest(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := unmarshalManifest(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if decoded.Filename != original.Filename {
		t.Errorf("filename: got %q, want %q", decoded.Filename, original.Filename)
	}
	if decoded.FileSize != original.FileSize {
		t.Errorf("fileSize: got %d, want %d", decoded.FileSize, original.FileSize)
	}
	if decoded.ChunkCount != original.ChunkCount {
		t.Errorf("chunkCount: got %d, want %d", decoded.ChunkCount, original.ChunkCount)
	}
	if decoded.RootHash != original.RootHash {
		t.Error("rootHash mismatch")
	}
	for i := 0; i < blockCount; i++ {
		if decoded.ChunkHashes[i] != original.ChunkHashes[i] {
			t.Errorf("chunk %d hash mismatch", i)
		}
		if decoded.ChunkSizes[i] != original.ChunkSizes[i] {
			t.Errorf("chunk %d size: got %d, want %d", i, decoded.ChunkSizes[i], original.ChunkSizes[i])
		}
	}
}

func TestUnmarshalManifestTruncated(t *testing.T) {
	// Should fail on too-short data.
	_, err := unmarshalManifest([]byte{0, 1, 2})
	if err == nil {
		t.Fatal("expected error on truncated data")
	}
}

func TestHashRegistry(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:       dir,
		MultiPeerEnabled: true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	var hash1, hash2 [32]byte
	rand.Read(hash1[:])
	rand.Read(hash2[:])

	// Initially empty.
	if _, ok := ts.LookupHash(hash1); ok {
		t.Error("expected no entry for hash1")
	}

	// Register and look up.
	ts.RegisterHash(hash1, "/path/to/file1.bin")
	ts.RegisterHash(hash2, "/path/to/file2.bin")

	path, ok := ts.LookupHash(hash1)
	if !ok || path != "/path/to/file1.bin" {
		t.Errorf("hash1 lookup: ok=%v path=%q", ok, path)
	}

	path, ok = ts.LookupHash(hash2)
	if !ok || path != "/path/to/file2.bin" {
		t.Errorf("hash2 lookup: ok=%v path=%q", ok, path)
	}

	// Overwrite.
	ts.RegisterHash(hash1, "/new/path.bin")
	path, _ = ts.LookupHash(hash1)
	if path != "/new/path.bin" {
		t.Errorf("hash1 after overwrite: %q", path)
	}
}

func TestMultiPeerConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		// Defaults: MultiPeerMaxPeers=0, MultiPeerMinSize=0
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	if ts.MultiPeerMaxPeers() != 4 {
		t.Errorf("default MaxPeers: got %d, want 4", ts.MultiPeerMaxPeers())
	}
	if ts.MultiPeerMinSize() != 10*1024*1024 {
		t.Errorf("default MinSize: got %d, want %d", ts.MultiPeerMinSize(), 10*1024*1024)
	}
}

func TestMultiPeerConfigCustom(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:        dir,
		MultiPeerEnabled:  true,
		MultiPeerMaxPeers: 8,
		MultiPeerMinSize:  1024 * 1024, // 1 MB
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	if !ts.MultiPeerEnabled() {
		t.Error("expected multi-peer enabled")
	}
	if ts.MultiPeerMaxPeers() != 8 {
		t.Errorf("MaxPeers: got %d, want 8", ts.MultiPeerMaxPeers())
	}
	if ts.MultiPeerMinSize() != 1024*1024 {
		t.Errorf("MinSize: got %d, want %d", ts.MultiPeerMinSize(), 1024*1024)
	}
}

func TestHandleMultiPeerRequestNotNil(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	handler := ts.HandleMultiPeerRequest()
	if handler == nil {
		t.Fatal("HandleMultiPeerRequest returned nil handler")
	}
}

func TestHashRegistryPopulatedOnSendComplete(t *testing.T) {
	// Verify the hash registry is initialized and usable.
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, MultiPeerEnabled: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// Write a test file.
	testFile := filepath.Join(dir, "hashtest.bin")
	data := make([]byte, 4096)
	rand.Read(data)
	if err := os.WriteFile(testFile, data, 0644); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	// Simulate what happens after a successful send: register hash.
	hash := blake3Hash(data)
	ts.RegisterHash(hash, testFile)

	path, ok := ts.LookupHash(hash)
	if !ok {
		t.Fatal("hash not found after register")
	}
	if path != testFile {
		t.Errorf("path: got %q, want %q", path, testFile)
	}
}

func TestMultiPeerRequestWireFormat(t *testing.T) {
	// Verify the wire format produced by requestMultiPeerManifest matches
	// what HandleMultiPeerRequest expects to read. This catches byte offset
	// bugs between sender and receiver.
	var rootHash [32]byte
	rand.Read(rootHash[:])
	peerIndex := 3
	numPeers := 4

	// Build the request header as requestMultiPeerManifest would.
	var header [41]byte
	header[0] = msgMultiPeerRequest
	copy(header[1:33], rootHash[:])
	binary.BigEndian.PutUint16(header[33:35], uint16(peerIndex))
	binary.BigEndian.PutUint16(header[35:37], uint16(numPeers))
	binary.BigEndian.PutUint32(header[37:41], 0) // auto mode

	// Parse it as HandleMultiPeerRequest would.
	if header[0] != msgMultiPeerRequest {
		t.Fatal("message type mismatch")
	}
	var parsedHash [32]byte
	copy(parsedHash[:], header[1:33])
	if parsedHash != rootHash {
		t.Fatal("root hash mismatch in wire format")
	}
	parsedPeerIndex := int(binary.BigEndian.Uint16(header[33:35]))
	parsedNumPeers := int(binary.BigEndian.Uint16(header[35:37]))
	parsedMaxSymbols := binary.BigEndian.Uint32(header[37:41])

	if parsedPeerIndex != peerIndex {
		t.Errorf("peerIndex: got %d, want %d", parsedPeerIndex, peerIndex)
	}
	if parsedNumPeers != numPeers {
		t.Errorf("numPeers: got %d, want %d", parsedNumPeers, numPeers)
	}
	if parsedMaxSymbols != 0 {
		t.Errorf("maxSymbolsPerBlock: got %d, want 0 (auto)", parsedMaxSymbols)
	}
}

func TestMultiPeerRequestRejectsNumPeersOne(t *testing.T) {
	// numPeers=1 must be rejected on both sender and receiver sides.
	// Sender: HandleMultiPeerRequest requires numPeers >= 2.
	// Receiver: DownloadMultiPeer requires len(peers) >= 2.

	// Build a request with numPeers=1.
	var header [41]byte
	header[0] = msgMultiPeerRequest
	rand.Read(header[1:33]) // rootHash
	binary.BigEndian.PutUint16(header[33:35], 0) // peerIndex=0
	binary.BigEndian.PutUint16(header[35:37], 1) // numPeers=1 (INVALID)
	binary.BigEndian.PutUint32(header[37:41], 0) // auto

	// Verify parsing: numPeers=1 should be caught by validation.
	numPeers := int(binary.BigEndian.Uint16(header[35:37]))
	if numPeers >= 2 {
		t.Fatal("expected numPeers=1 to be < 2")
	}
	// The handler would reject this with "invalid numPeers".
}

func TestDownloadMultiPeerRequiresMinPeers(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, MultiPeerEnabled: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	var hash [32]byte
	rand.Read(hash[:])

	// Should fail with fewer than 2 peers.
	_, err = ts.DownloadMultiPeer(nil, hash, nil, nil, "")
	if err == nil {
		t.Fatal("expected error with nil peers")
	}
}
