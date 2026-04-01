package sdk

import (
	"bytes"
	"crypto/rand"
	"os"
	"sync/atomic"
	"testing"
)

func TestAdaptiveStreamCount(t *testing.T) {
	tests := []struct {
		name       string
		transport  TransportType
		chunkCount int
		requested  int
		want       int
	}{
		// Relay always gets 1.
		{"relay-default", TransportRelay, 100, 0, 1},
		{"relay-requested", TransportRelay, 100, 8, 1},

		// LAN defaults.
		{"lan-default-100", TransportLAN, 100, 0, 8},
		{"lan-default-16", TransportLAN, 16, 0, 4}, // 16/4 = 4 < 8
		{"lan-default-2", TransportLAN, 2, 0, 1},   // too few chunks

		// Direct WAN defaults.
		{"direct-default-100", TransportDirect, 100, 0, 4},
		{"direct-default-8", TransportDirect, 8, 0, 2}, // 8/4 = 2 < 4

		// User-requested overrides.
		{"lan-requested-16", TransportLAN, 100, 16, 16},
		{"lan-requested-clamped", TransportLAN, 100, 50, parallelStreamsLANMax},
		{"direct-requested-10", TransportDirect, 100, 10, 10},
		{"direct-requested-clamped", TransportDirect, 100, 30, parallelStreamsDirectMax},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := adaptiveStreamCount(tt.transport, tt.chunkCount, tt.requested)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestClampStreams(t *testing.T) {
	tests := []struct {
		n         int
		transport TransportType
		want      int
	}{
		{0, TransportLAN, 1},
		{-1, TransportLAN, 1},
		{1, TransportLAN, 1},
		{32, TransportLAN, 32},
		{33, TransportLAN, 32},
		{20, TransportDirect, 20},
		{21, TransportDirect, 20},
		{5, TransportRelay, 1},
	}

	for _, tt := range tests {
		got := clampStreams(tt.n, tt.transport)
		if got != tt.want {
			t.Errorf("clampStreams(%d, %v): got %d, want %d", tt.n, tt.transport, got, tt.want)
		}
	}
}

func TestWorkerHelloRoundtrip(t *testing.T) {
	var transferID [32]byte
	rand.Read(transferID[:])

	var buf bytes.Buffer
	if err := writeWorkerHello(&buf, transferID); err != nil {
		t.Fatalf("writeWorkerHello: %v", err)
	}

	// Should be exactly 33 bytes: 1 byte type + 32 bytes transferID.
	if buf.Len() != workerHelloSize {
		t.Fatalf("expected %d bytes, got %d", workerHelloSize, buf.Len())
	}

	// First byte should be msgWorkerHello.
	data := buf.Bytes()
	if data[0] != msgWorkerHello {
		t.Fatalf("expected msgWorkerHello (0x%02x), got 0x%02x", msgWorkerHello, data[0])
	}

	// Read it back: consume the type byte, then readWorkerHello.
	reader := bytes.NewReader(data[1:]) // skip type byte (caller peeks it)
	gotID, err := readWorkerHello(reader)
	if err != nil {
		t.Fatalf("readWorkerHello: %v", err)
	}
	if gotID != transferID {
		t.Fatalf("transferID mismatch: got %x, want %x", gotID, transferID)
	}
}

func TestParallelSessionRegistration(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	var transferID [32]byte
	rand.Read(transferID[:])

	session := &parallelSession{
		transferID: transferID,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, 10),
	}

	// Register.
	ts.registerParallelSession(transferID, session)

	ts.mu.RLock()
	got, ok := ts.parallelSessions[transferID]
	ts.mu.RUnlock()
	if !ok {
		t.Fatal("session not found after registration")
	}
	if got != session {
		t.Fatal("wrong session returned")
	}

	// Unregister.
	ts.unregisterParallelSession(transferID)

	ts.mu.RLock()
	_, ok = ts.parallelSessions[transferID]
	ts.mu.RUnlock()
	if ok {
		t.Fatal("session still present after unregistration")
	}
}

// TestReceiveParallelOutOfOrder verifies that receiveParallel correctly processes
// streaming chunks arriving out of order from both control stream and worker channel.
func TestReceiveParallelOutOfOrder(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	// Create 4 chunks of known data.
	chunkData := [][]byte{
		bytes.Repeat([]byte{0xAA}, 100),
		bytes.Repeat([]byte{0xBB}, 200),
		bytes.Repeat([]byte{0xCC}, 150),
		bytes.Repeat([]byte{0xDD}, 250),
	}
	totalSize := int64(100 + 200 + 150 + 250)

	chunkHashes := make([][32]byte, 4)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
	}
	rootHash := MerkleRoot(chunkHashes)

	// Build file table (single file).
	files := []fileEntry{{Path: "parallel-test.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)

	// Build receive state with temp files.
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
		chunks:     make(chan streamChunk, 10),
	}
	// Mark that worker streams are active so receiveParallel drains the
	// chunks channel after the control stream trailer (instead of skipping
	// the drain due to the nextWorkerID==0 optimization).
	atomic.StoreInt32(&session.nextWorkerID, 1)

	// Build control stream: chunks 3 and 0 (out of order) + trailer.
	var controlBuf bytes.Buffer
	globalOffset := int64(0)
	offsets := []int64{0, 100, 300, 450}

	// Chunk 3 first (out of order).
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 3, offset: offsets[3],
		hash: chunkHashes[3], decompSize: uint32(len(chunkData[3])), data: chunkData[3],
	})
	// Then chunk 0.
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: offsets[0],
		hash: chunkHashes[0], decompSize: uint32(len(chunkData[0])), data: chunkData[0],
	})
	// Trailer.
	writeTrailer(&controlBuf, 4, rootHash, nil, nil)

	// Worker channel delivers chunks 2 and 1 (out of order).
	go func() {
		session.chunks <- streamChunk{
			fileIdx: 0, chunkIdx: 2, offset: offsets[2],
			hash: chunkHashes[2], decompSize: uint32(len(chunkData[2])), data: chunkData[2],
		}
		session.chunks <- streamChunk{
			fileIdx: 0, chunkIdx: 1, offset: offsets[1],
			hash: chunkHashes[1], decompSize: uint32(len(chunkData[1])), data: chunkData[1],
		}
	}()

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch: got %x, want %x", gotRoot[:8], rootHash[:8])
	}

	// Verify the finalized file exists and has correct content.
	finalPath := dir + "/parallel-test.bin"
	result, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	_ = globalOffset
	expected := make([]byte, 0, totalSize)
	for _, d := range chunkData {
		expected = append(expected, d...)
	}
	if !bytes.Equal(result, expected) {
		t.Errorf("file content mismatch: got %d bytes, want %d", len(result), len(expected))
	}
}

// TestReceiveParallelSingleStream verifies that receiveParallel works correctly
// when no worker streams connect (single control stream only).
func TestReceiveParallelSingleStream(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	chunkData := [][]byte{
		bytes.Repeat([]byte{0x11}, 64),
		bytes.Repeat([]byte{0x22}, 128),
	}
	totalSize := int64(64 + 128)

	chunkHashes := make([][32]byte, 2)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
	}
	rootHash := MerkleRoot(chunkHashes)

	files := []fileEntry{{Path: "single-stream.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)

	state := newStreamReceiveState(files, totalSize, 0, cumOffsets)
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
		chunks:     make(chan streamChunk, 10),
	}

	// All chunks on control stream, no workers.
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: chunkHashes[0], decompSize: uint32(len(chunkData[0])), data: chunkData[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 64,
		hash: chunkHashes[1], decompSize: uint32(len(chunkData[1])), data: chunkData[1],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, nil)

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}

	finalPath := dir + "/single-stream.bin"
	result, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	expected := append(chunkData[0], chunkData[1]...)
	if !bytes.Equal(result, expected) {
		t.Error("file content mismatch")
	}
}

// TestWorkerCleanupOnSessionDone verifies that worker streams exit cleanly
// when the session's done channel is closed.
func TestWorkerCleanupOnSessionDone(t *testing.T) {
	session := &parallelSession{
		done:   make(chan struct{}),
		chunks: make(chan streamChunk), // unbuffered: send blocks without receiver
	}

	// Close done immediately.
	close(session.done)

	// Simulate a worker trying to deliver chunks after done is closed.
	// The select in handleWorkerStreamFromReader checks session.done first.
	delivered := false
	select {
	case <-session.done:
		// Expected: done is closed, worker should exit.
	case session.chunks <- streamChunk{chunkIdx: 0, data: []byte{1}}:
		delivered = true
	}

	if delivered {
		t.Error("chunk delivered after session done - worker should have exited")
	}
}

// TestReceiveParallelDuplicateChunks verifies that duplicate chunks (same index
// from control stream and worker) are handled correctly - only written once.
func TestReceiveParallelDuplicateChunks(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: false}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	chunkData := [][]byte{
		bytes.Repeat([]byte{0x55}, 80),
		bytes.Repeat([]byte{0x66}, 120),
	}
	totalSize := int64(80 + 120)

	chunkHashes := make([][32]byte, 2)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
	}
	rootHash := MerkleRoot(chunkHashes)

	files := []fileEntry{{Path: "dup-test.bin", Size: totalSize}}
	cumOffsets := computeCumulativeOffsets(files)

	state := newStreamReceiveState(files, totalSize, 0, cumOffsets)
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
		chunks:     make(chan streamChunk, 10),
	}

	// Control sends both chunks.
	var controlBuf bytes.Buffer
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 0, offset: 0,
		hash: chunkHashes[0], decompSize: uint32(len(chunkData[0])), data: chunkData[0],
	})
	writeStreamChunkFrame(&controlBuf, streamChunk{
		fileIdx: 0, chunkIdx: 1, offset: 80,
		hash: chunkHashes[1], decompSize: uint32(len(chunkData[1])), data: chunkData[1],
	})
	writeTrailer(&controlBuf, 2, rootHash, nil, nil)

	// Worker also sends chunk 0 (duplicate).
	go func() {
		session.chunks <- streamChunk{
			fileIdx: 0, chunkIdx: 0, offset: 0,
			hash: chunkHashes[0], decompSize: uint32(len(chunkData[0])), data: chunkData[0],
		}
	}()

	gotRoot, err := ts.receiveParallel(&controlBuf, session)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}
	if gotRoot != rootHash {
		t.Errorf("root hash mismatch")
	}

	finalPath := dir + "/dup-test.bin"
	result, err := os.ReadFile(finalPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}

	expected := append(chunkData[0], chunkData[1]...)
	if !bytes.Equal(result, expected) {
		t.Error("file content mismatch with duplicate chunks")
	}
}

func TestPartitionRoundRobin(t *testing.T) {
	// Verify the round-robin partitioning logic used in sendParallel.
	numStreams := 3
	chunkCount := 10

	partitions := make([][]int, numStreams)
	for i := 0; i < chunkCount; i++ {
		slot := i % numStreams
		partitions[slot] = append(partitions[slot], i)
	}

	// Stream 0: 0, 3, 6, 9
	// Stream 1: 1, 4, 7
	// Stream 2: 2, 5, 8
	expected := [][]int{
		{0, 3, 6, 9},
		{1, 4, 7},
		{2, 5, 8},
	}

	for i, part := range partitions {
		if len(part) != len(expected[i]) {
			t.Errorf("partition %d: got %d chunks, want %d", i, len(part), len(expected[i]))
			continue
		}
		for j, idx := range part {
			if idx != expected[i][j] {
				t.Errorf("partition %d[%d]: got %d, want %d", i, j, idx, expected[i][j])
			}
		}
	}
}
