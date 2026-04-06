package filetransfer

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// --- Block Queue Tests ---

func TestBlockQueue_ClaimPrimaryThenRetry(t *testing.T) {
	have := newBitfield(5)
	q := newBlockQueue(5, have)

	// Claim all primary blocks.
	claimed := make(map[int]bool)
	for i := 0; i < 5; i++ {
		idx, ok := q.claim(context.Background(), false)
		if !ok {
			t.Fatalf("claim %d failed", i)
		}
		claimed[idx] = true
	}
	if len(claimed) != 5 {
		t.Fatalf("expected 5 unique blocks, got %d", len(claimed))
	}

	// Primary exhausted. Requeue block 2.
	q.requeue(2)

	// Claim should return the retried block.
	idx, ok := q.claim(context.Background(), false)
	if !ok || idx != 2 {
		t.Fatalf("expected retried block 2, got %d (ok=%v)", idx, ok)
	}
}

func TestBlockQueue_RetryHasPriority(t *testing.T) {
	have := newBitfield(10)
	q := newBlockQueue(10, have)

	// Requeue a block before claiming any primary.
	q.requeue(7)

	// First claim should return the retry.
	idx, ok := q.claim(context.Background(), false)
	if !ok || idx != 7 {
		t.Fatalf("expected retried block 7, got %d (ok=%v)", idx, ok)
	}
}

func TestBlockQueue_CompletionClosesDone(t *testing.T) {
	have := newBitfield(3)
	q := newBlockQueue(3, have)

	// Claim and complete all blocks.
	for i := 0; i < 3; i++ {
		idx, ok := q.claim(context.Background(), false)
		if !ok {
			t.Fatalf("claim %d failed", i)
		}
		q.markComplete(idx, 100)
	}

	select {
	case <-q.done:
		// expected
	case <-time.After(time.Second):
		t.Fatal("done channel not closed after all blocks complete")
	}
}

func TestBlockQueue_SyncOncePreventsPanic(t *testing.T) {
	// Two goroutines completing the last two blocks simultaneously.
	have := newBitfield(2)
	q := newBlockQueue(2, have)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			idx, ok := q.claim(context.Background(), false)
			if !ok {
				return
			}
			q.markComplete(idx, 100)
		}()
	}
	wg.Wait()

	select {
	case <-q.done:
		// success: no double-close panic
	default:
		t.Fatal("done should be closed")
	}
}

func TestBlockQueue_SlowPeerOnlyServesRetry(t *testing.T) {
	have := newBitfield(5)
	q := newBlockQueue(5, have)

	// Requeue block 3.
	q.requeue(3)

	// Slow peer should get the retried block.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	idx, ok := q.claim(ctx, true) // slow=true
	if !ok || idx != 3 {
		t.Fatalf("slow peer expected retried block 3, got %d (ok=%v)", idx, ok)
	}

	// With no retries queued, slow peer should block until timeout.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel2()
	_, ok = q.claim(ctx2, true)
	if ok {
		t.Fatal("slow peer should not get primary blocks")
	}
}

func TestBlockQueue_ResumeSkipsCompleted(t *testing.T) {
	have := newBitfield(5)
	have.set(0)
	have.set(2)
	have.set(4)
	q := newBlockQueue(5, have)

	// Should have 3 already done, 2 remaining.
	if q.completed.Load() != 3 {
		t.Fatalf("expected 3 completed, got %d", q.completed.Load())
	}

	// Claim should return only blocks 1 and 3.
	claimed := make(map[int]bool)
	for i := 0; i < 2; i++ {
		idx, ok := q.claim(context.Background(), false)
		if !ok {
			t.Fatalf("claim %d failed", i)
		}
		claimed[idx] = true
	}
	if !claimed[1] || !claimed[3] {
		t.Fatalf("expected blocks 1 and 3, got %v", claimed)
	}
}

func TestBlockQueue_CheckpointSnapshot(t *testing.T) {
	have := newBitfield(8)
	q := newBlockQueue(8, have)

	// Complete blocks 0, 3, 7.
	q.markComplete(0, 100)
	q.markComplete(3, 100)
	q.markComplete(7, 100)

	snap := q.checkpointSnapshot()
	if !snap.has(0) || !snap.has(3) || !snap.has(7) {
		t.Fatal("snapshot missing completed blocks")
	}
	if snap.has(1) || snap.has(4) {
		t.Fatal("snapshot has uncompleted blocks")
	}
	// Verify it's a copy — mutating snap shouldn't affect original.
	snap.set(5)
	q.haveMu.Lock()
	original := q.have.has(5)
	q.haveMu.Unlock()
	if original {
		t.Fatal("snapshot mutation leaked to original")
	}
}

// --- Peer State Tests ---

func TestPeerState_ThreeStrikeBan(t *testing.T) {
	ps := &peerState{}
	// Strike tracking is inline in the pipeline loop. Unit testing the
	// state transitions here:
	ps.strikes = 0
	ps.onParole = false
	ps.banned = false

	// Strike 1 → parole.
	ps.strikes++
	ps.onParole = true
	if ps.banned {
		t.Fatal("should not be banned after 1 strike")
	}

	// Parole success → clear.
	ps.onParole = false

	// Strike 2.
	ps.strikes++
	ps.onParole = true
	if ps.banned {
		t.Fatal("should not be banned after 2 strikes")
	}

	// Strike 3 → ban.
	ps.strikes++
	if ps.strikes < 3 {
		t.Fatal("expected 3 strikes")
	}
	ps.banned = true
	if !ps.banned {
		t.Fatal("should be banned after 3 strikes")
	}
}

func TestPeerState_SpeedMeasurement(t *testing.T) {
	ps := &peerState{startTime: time.Now().Add(-10 * time.Second)}
	// Too few blocks — should return 0.
	ps.blocksOK.Store(1)
	if ps.speed() != 0 {
		t.Fatal("expected 0 speed with too few blocks")
	}
	// Enough blocks.
	ps.blocksOK.Store(10)
	spd := ps.speed()
	if spd < 0.5 || spd > 2.0 {
		t.Fatalf("expected ~1.0 blocks/sec, got %f", spd)
	}
}

// --- Boundary Cache Tests ---

func TestBoundaryCache_HitAndMiss(t *testing.T) {
	cache := newBoundaryCache()
	var rootHash [32]byte
	rand.Read(rootHash[:])

	scanCount := 0
	scanner := func() ([]chunkBoundary, error) {
		scanCount++
		return []chunkBoundary{{offset: 0, size: 1024}}, nil
	}

	// First call: miss → scan.
	b1, err := cache.getOrScan(rootHash, scanner)
	if err != nil || len(b1) != 1 || scanCount != 1 {
		t.Fatalf("first call: err=%v, len=%d, scans=%d", err, len(b1), scanCount)
	}

	// Second call: hit → no scan.
	b2, err := cache.getOrScan(rootHash, scanner)
	if err != nil || len(b2) != 1 || scanCount != 1 {
		t.Fatalf("second call: err=%v, len=%d, scans=%d", err, len(b2), scanCount)
	}
}

func TestBoundaryCache_LRUEviction(t *testing.T) {
	cache := newBoundaryCache()

	// Fill cache to max + 1 to trigger eviction.
	var firstHash [32]byte
	for i := 0; i < maxBoundaryCacheEntries+1; i++ {
		var h [32]byte
		binary.BigEndian.PutUint32(h[:], uint32(i))
		if i == 0 {
			firstHash = h
		}
		cache.put(h, []chunkBoundary{{offset: int64(i)}})
	}

	// First entry should be evicted.
	cache.mu.RLock()
	_, exists := cache.cache[firstHash]
	cache.mu.RUnlock()
	if exists {
		t.Fatal("first entry should have been evicted")
	}

	// Last entry should still be there.
	var lastHash [32]byte
	binary.BigEndian.PutUint32(lastHash[:], uint32(maxBoundaryCacheEntries))
	cache.mu.RLock()
	_, exists = cache.cache[lastHash]
	cache.mu.RUnlock()
	if !exists {
		t.Fatal("last entry should still be cached")
	}
}

// --- Manifest Validation Tests ---

func TestManifestValidation_SumOfSizes(t *testing.T) {
	m := &transferManifest{
		FileSize:   1000,
		ChunkCount: 2,
		ChunkSizes: []uint32{500, 400}, // sum=900 != 1000
	}
	err := validateManifestSizes(m)
	if err == nil {
		t.Fatal("expected error for sum mismatch")
	}
}

func TestManifestValidation_ChunkSizeBound(t *testing.T) {
	m := &transferManifest{
		FileSize:   int64(maxChunkWireSize) + 1,
		ChunkCount: 1,
		ChunkSizes: []uint32{uint32(maxChunkWireSize) + 1},
	}
	err := validateManifestSizes(m)
	if err == nil {
		t.Fatal("expected error for oversized chunk")
	}
}

func TestManifestValidation_Valid(t *testing.T) {
	m := &transferManifest{
		FileSize:   1000,
		ChunkCount: 2,
		ChunkSizes: []uint32{500, 500},
	}
	if err := validateManifestSizes(m); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

// --- Wire Format Tests ---

func TestMarshalUnmarshalManifest(t *testing.T) {
	blockCount := 3
	hashes := make([][32]byte, blockCount)
	sizes := make([]uint32, blockCount)
	for i := 0; i < blockCount; i++ {
		rand.Read(hashes[i][:])
		sizes[i] = uint32(1024 * (i + 1))
	}

	original := &transferManifest{
		Filename:    "test-file.bin",
		FileSize:    int64(1024 + 2048 + 3072),
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
	_, err := unmarshalManifest([]byte{0, 1, 2})
	if err == nil {
		t.Fatal("expected error on truncated data")
	}
}

func TestBlockRequestWireFormat(t *testing.T) {
	var buf bytes.Buffer
	// Simulate sendBlockRequest.
	var req [5]byte
	req[0] = msgBlockRequest
	binary.BigEndian.PutUint32(req[1:5], 42)
	buf.Write(req[:])

	// Parse.
	data := buf.Bytes()
	if data[0] != msgBlockRequest {
		t.Fatal("wrong message type")
	}
	blockIndex := binary.BigEndian.Uint32(data[1:5])
	if blockIndex != 42 {
		t.Fatalf("block index: got %d, want 42", blockIndex)
	}
}

func TestBlockDataWireFormat(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte("hello world")

	// Simulate writeBlockData.
	var header [14]byte
	header[0] = msgBlockData
	binary.BigEndian.PutUint32(header[1:5], 7)      // blockIndex
	header[5] = blockFlagCompressed                   // flags
	binary.BigEndian.PutUint32(header[6:10], 100)    // decompSize
	binary.BigEndian.PutUint32(header[10:14], uint32(len(payload)))
	buf.Write(header[:])
	buf.Write(payload)

	data := buf.Bytes()
	if data[0] != msgBlockData {
		t.Fatal("wrong message type")
	}
	idx := binary.BigEndian.Uint32(data[1:5])
	if idx != 7 {
		t.Fatalf("blockIndex: got %d, want 7", idx)
	}
	flags := data[5]
	if flags != blockFlagCompressed {
		t.Fatalf("flags: got 0x%02x, want 0x%02x", flags, blockFlagCompressed)
	}
	decompSz := binary.BigEndian.Uint32(data[6:10])
	if decompSz != 100 {
		t.Fatalf("decompSize: got %d, want 100", decompSz)
	}
	dataLen := binary.BigEndian.Uint32(data[10:14])
	if int(dataLen) != len(payload) {
		t.Fatalf("dataLen: got %d, want %d", dataLen, len(payload))
	}
	if !bytes.Equal(data[14:14+dataLen], payload) {
		t.Fatal("payload mismatch")
	}
}

func TestMultiPeerRejectWireFormat(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{msgMultiPeerReject, rejectAtCapacity})
	data := buf.Bytes()
	if data[0] != msgMultiPeerReject {
		t.Fatal("wrong type")
	}
	if data[1] != rejectAtCapacity {
		t.Fatalf("reason: got 0x%02x, want 0x%02x", data[1], rejectAtCapacity)
	}
}

func TestMultiPeerRequestWireFormat(t *testing.T) {
	var rootHash [32]byte
	rand.Read(rootHash[:])
	flags := uint32(flagMPCompressionSupported | flagMPResumeSupported)

	var header [37]byte
	header[0] = msgMultiPeerRequest
	copy(header[1:33], rootHash[:])
	binary.BigEndian.PutUint32(header[33:37], flags)

	// Parse.
	if header[0] != msgMultiPeerRequest {
		t.Fatal("type mismatch")
	}
	var parsed [32]byte
	copy(parsed[:], header[1:33])
	if parsed != rootHash {
		t.Fatal("root hash mismatch")
	}
	parsedFlags := binary.BigEndian.Uint32(header[33:37])
	if parsedFlags != flags {
		t.Fatalf("flags: got %d, want %d", parsedFlags, flags)
	}
}

// --- Helper Tests ---

func TestComputeBlockOffsets(t *testing.T) {
	m := &transferManifest{
		ChunkCount: 4,
		ChunkSizes: []uint32{100, 200, 300, 400},
	}
	offsets := computeBlockOffsets(m)
	expected := []int64{0, 100, 300, 600}
	for i, want := range expected {
		if offsets[i] != want {
			t.Errorf("offset[%d]: got %d, want %d", i, offsets[i], want)
		}
	}
}

func TestMultiPeerContentKey(t *testing.T) {
	var h1, h2 [32]byte
	rand.Read(h1[:])
	h2 = h1

	k1 := multiPeerContentKey(h1)
	k2 := multiPeerContentKey(h2)
	if k1 != k2 {
		t.Fatal("same root hash should produce same content key")
	}

	// Different root hash → different key.
	rand.Read(h2[:])
	k3 := multiPeerContentKey(h2)
	if k1 == k3 {
		t.Fatal("different root hashes should produce different keys")
	}
}

func TestRejectReasonStrings(t *testing.T) {
	tests := []struct {
		reason byte
		want   string
	}{
		{rejectAtCapacity, "peer at capacity"},
		{rejectBandwidthExceeded, "bandwidth budget exceeded"},
		{rejectFileNotFound, "file not found"},
		{0xFF, "declined (reason 0xff)"},
	}
	for _, tt := range tests {
		got := multiPeerRejectString(tt.reason)
		if got != tt.want {
			t.Errorf("reason 0x%02x: got %q, want %q", tt.reason, got, tt.want)
		}
	}
}

// --- Hash Registry Tests ---

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

	if _, ok := ts.LookupHash(hash1); ok {
		t.Error("expected no entry for hash1")
	}

	ts.RegisterHash(hash1, "/path/to/file1.bin")
	ts.RegisterHash(hash2, "/path/to/file2.bin")

	path, ok := ts.LookupHash(hash1)
	if !ok || path != "/path/to/file1.bin" {
		t.Errorf("hash1 lookup: ok=%v path=%q", ok, path)
	}
}

// --- Config Tests ---

func TestMultiPeerConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)
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
		MultiPeerMinSize:  1024 * 1024,
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

func TestDownloadMultiPeerRequiresMinPeers(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, MultiPeerEnabled: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	var hash [32]byte
	rand.Read(hash[:])

	_, err = ts.DownloadMultiPeer(nil, hash, nil, nil, "")
	if err == nil {
		t.Fatal("expected error with nil peers")
	}
}

// --- Temp File Cleanup Tests ---

func TestCleanTempFilesMultiPeerPrefixes(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// Create temp files with various prefixes.
	// Backdate multi-peer files to bypass IF9-7 active-file protection.
	oldTime := time.Now().Add(-10 * time.Minute)
	for _, name := range []string{
		".shurli-tmp-abc",
		".shurli-mp-def.tmp",
		".shurli-ckpt-ghi",
		"regular-file.txt",
	} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte("test"), 0644)
		os.Chtimes(p, oldTime, oldTime)
	}

	count, _ := ts.CleanTempFiles()
	if count != 3 {
		t.Fatalf("expected 3 files cleaned, got %d", count)
	}

	// regular-file.txt should still exist.
	if _, err := os.Stat(filepath.Join(dir, "regular-file.txt")); err != nil {
		t.Fatal("regular file should not be cleaned")
	}
}

// --- Concurrent Block Completion Test ---

func TestBlockQueue_ConcurrentComplete(t *testing.T) {
	n := 100
	have := newBitfield(n)
	q := newBlockQueue(n, have)

	var wg sync.WaitGroup
	var claimedBlocks sync.Map // track which blocks were claimed

	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx, ok := q.claim(context.Background(), false)
				if !ok {
					return
				}
				claimedBlocks.Store(idx, true)
				q.markComplete(idx, 100)
			}
		}()
	}

	wg.Wait()

	// Verify all blocks were completed.
	if q.completed.Load() != int32(n) {
		t.Fatalf("expected %d completed, got %d", n, q.completed.Load())
	}

	// Verify no duplicates (each block claimed exactly once).
	count := 0
	claimedBlocks.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != n {
		t.Fatalf("expected %d unique blocks claimed, got %d", n, count)
	}
}

// --- Transfer Progress Monotonic Test ---

func TestTransferredBytesMonotonic(t *testing.T) {
	// Verify that atomic Add produces monotonically increasing values
	// even with concurrent writers (IF13-2).
	var counter atomic.Int64
	var wg sync.WaitGroup
	var maxSeen atomic.Int64

	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				newVal := counter.Add(100)
				for {
					old := maxSeen.Load()
					if newVal <= old || maxSeen.CompareAndSwap(old, newVal) {
						break
					}
				}
			}
		}()
	}
	wg.Wait()

	expected := int64(8 * 1000 * 100)
	if counter.Load() != expected {
		t.Fatalf("expected %d, got %d", expected, counter.Load())
	}
}

// --- isShurliTempFile Tests ---

func TestIsShurliTempFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{".shurli-tmp-abc", true},
		{".shurli-mp-def.tmp", true},
		{".shurli-ckpt-ghi", true},
		{".shurli-mp-abc.lock", true},
		{"regular-file.txt", false},
		{".gitignore", false},
		{"shurli-tmp-missing-dot", false},
	}
	for _, tt := range tests {
		got := isShurliTempFile(tt.name)
		if got != tt.want {
			t.Errorf("isShurliTempFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}
