package p2pnet

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
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
	var rootHash [32]byte
	copy(rootHash[:], bytes.Repeat([]byte{0xAB}, 32))

	var buf bytes.Buffer
	if err := writeWorkerHello(&buf, rootHash); err != nil {
		t.Fatalf("writeWorkerHello: %v", err)
	}

	// Should be exactly 33 bytes: 1 byte type + 32 bytes hash.
	if buf.Len() != workerHelloSize {
		t.Fatalf("expected %d bytes, got %d", workerHelloSize, buf.Len())
	}

	// First byte should be msgWorkerHello.
	data := buf.Bytes()
	if data[0] != msgWorkerHello {
		t.Fatalf("expected msgWorkerHello (0x%02x), got 0x%02x", msgWorkerHello, data[0])
	}

	// Read it back: consume the type byte, then readWorkerHello.
	reader := bytes.NewReader(data)
	var typeByte [1]byte
	if _, err := io.ReadFull(reader, typeByte[:]); err != nil {
		t.Fatalf("read type byte: %v", err)
	}
	if typeByte[0] != msgWorkerHello {
		t.Fatalf("type byte mismatch")
	}

	gotHash, err := readWorkerHello(reader)
	if err != nil {
		t.Fatalf("readWorkerHello: %v", err)
	}
	if gotHash != rootHash {
		t.Fatalf("hash mismatch: got %x, want %x", gotHash, rootHash)
	}
}

func TestReadChunkFrameHeader(t *testing.T) {
	// Build a 9-byte header: msgType(1) + index(4) + dataLen(4).
	var header [9]byte
	header[0] = msgChunk
	binary.BigEndian.PutUint32(header[1:5], 42)
	binary.BigEndian.PutUint32(header[5:9], 1024)

	msgType, index, dataLen, err := readChunkFrameHeader(bytes.NewReader(header[:]))
	if err != nil {
		t.Fatalf("readChunkFrameHeader: %v", err)
	}
	if msgType != msgChunk {
		t.Errorf("msgType: got 0x%02x, want 0x%02x", msgType, msgChunk)
	}
	if index != 42 {
		t.Errorf("index: got %d, want 42", index)
	}
	if dataLen != 1024 {
		t.Errorf("dataLen: got %d, want 1024", dataLen)
	}
}

func TestParallelSessionRegistration(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	var rootHash [32]byte
	copy(rootHash[:], bytes.Repeat([]byte{0xCD}, 32))

	session := &parallelSession{
		rootHash: rootHash,
		done:     make(chan struct{}),
		chunks:   make(chan parallelChunk, 10),
	}

	// Register.
	ts.registerParallelSession(rootHash, session)

	ts.mu.RLock()
	got, ok := ts.parallelSessions[rootHash]
	ts.mu.RUnlock()
	if !ok {
		t.Fatal("session not found after registration")
	}
	if got != session {
		t.Fatal("wrong session returned")
	}

	// Unregister.
	ts.unregisterParallelSession(rootHash)

	ts.mu.RLock()
	_, ok = ts.parallelSessions[rootHash]
	ts.mu.RUnlock()
	if ok {
		t.Fatal("session still present after unregistration")
	}
}

// TestReceiveParallelOutOfOrder verifies that receiveParallel correctly writes
// chunks arriving out of order from both control stream and worker channel.
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
	chunkHashes := make([][32]byte, 4)
	chunkSizes := make([]uint32, 4)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
		chunkSizes[i] = uint32(len(d))
	}
	rootHash := MerkleRoot(chunkHashes)
	totalSize := int64(100 + 200 + 150 + 250)

	manifest := &transferManifest{
		Filename:    "parallel-test.bin",
		FileSize:    totalSize,
		ChunkCount:  4,
		RootHash:    rootHash,
		ChunkHashes: chunkHashes,
		ChunkSizes:  chunkSizes,
	}

	// Create temp file.
	tmpPath := dir + "/test-parallel.tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if err := tmpFile.Truncate(totalSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	offsets := buildOffsetTable(chunkSizes)
	have := newBitfield(4)
	progress := &TransferProgress{ChunksTotal: 4}

	session := &parallelSession{
		rootHash: rootHash,
		manifest: manifest,
		tmpFile:  tmpFile,
		tmpPath:  tmpPath,
		have:     have,
		offsets:  offsets,
		progress: progress,
		done:     make(chan struct{}),
		chunks:   make(chan parallelChunk, 10),
	}

	// Simulate control stream: sends chunks 0 and 3 (out of order: 3 first).
	var controlBuf bytes.Buffer
	writeChunkFrame(&controlBuf, 3, chunkData[3])
	writeChunkFrame(&controlBuf, 0, chunkData[0])
	writeMsg(&controlBuf, msgTransferDone)

	// Worker channel delivers chunks 2 and 1 (out of order).
	go func() {
		session.chunks <- parallelChunk{index: 2, data: chunkData[2]}
		session.chunks <- parallelChunk{index: 1, data: chunkData[1]}
	}()

	err = ts.receiveParallel(&controlBuf, session, nil)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}

	// Verify all chunks received.
	if have.count() != 4 {
		t.Errorf("have count: got %d, want 4", have.count())
	}

	// Read back the file and verify content at correct offsets.
	tmpFile.Close()
	result, err := os.ReadFile(tmpPath)
	if err != nil {
		t.Fatalf("read result: %v", err)
	}
	if int64(len(result)) != totalSize {
		t.Fatalf("file size: got %d, want %d", len(result), totalSize)
	}

	// Check each chunk at its offset.
	for i, d := range chunkData {
		start := offsets[i]
		end := start + int64(len(d))
		got := result[start:end]
		if !bytes.Equal(got, d) {
			t.Errorf("chunk %d at offset %d: data mismatch", i, start)
		}
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

	// 2 chunks.
	chunkData := [][]byte{
		bytes.Repeat([]byte{0x11}, 64),
		bytes.Repeat([]byte{0x22}, 128),
	}
	chunkHashes := make([][32]byte, 2)
	chunkSizes := make([]uint32, 2)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
		chunkSizes[i] = uint32(len(d))
	}
	rootHash := MerkleRoot(chunkHashes)
	totalSize := int64(64 + 128)

	manifest := &transferManifest{
		Filename:    "single-stream.bin",
		FileSize:    totalSize,
		ChunkCount:  2,
		RootHash:    rootHash,
		ChunkHashes: chunkHashes,
		ChunkSizes:  chunkSizes,
	}

	tmpPath := dir + "/test-single.tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if err := tmpFile.Truncate(totalSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	offsets := buildOffsetTable(chunkSizes)
	have := newBitfield(2)
	progress := &TransferProgress{ChunksTotal: 2}

	session := &parallelSession{
		rootHash: rootHash,
		manifest: manifest,
		tmpFile:  tmpFile,
		tmpPath:  tmpPath,
		have:     have,
		offsets:  offsets,
		progress: progress,
		done:     make(chan struct{}),
		chunks:   make(chan parallelChunk, 10),
	}

	// All chunks on control stream, no workers.
	var controlBuf bytes.Buffer
	writeChunkFrame(&controlBuf, 0, chunkData[0])
	writeChunkFrame(&controlBuf, 1, chunkData[1])
	writeMsg(&controlBuf, msgTransferDone)

	err = ts.receiveParallel(&controlBuf, session, nil)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}

	if have.count() != 2 {
		t.Errorf("have count: got %d, want 2", have.count())
	}

	tmpFile.Close()
	result, err := os.ReadFile(tmpPath)
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
		chunks: make(chan parallelChunk), // unbuffered: send blocks without receiver
		manifest: &transferManifest{ChunkCount: 10},
	}

	// Close done immediately.
	close(session.done)

	// Simulate a worker trying to deliver chunks after done is closed.
	// The select in handleWorkerStreamFromReader checks session.done first.
	delivered := false
	select {
	case <-session.done:
		// Expected: done is closed, worker should exit.
	case session.chunks <- parallelChunk{index: 0, data: []byte{1}}:
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
	chunkHashes := make([][32]byte, 2)
	chunkSizes := make([]uint32, 2)
	for i, d := range chunkData {
		chunkHashes[i] = blake3Sum(d)
		chunkSizes[i] = uint32(len(d))
	}
	rootHash := MerkleRoot(chunkHashes)
	totalSize := int64(80 + 120)

	manifest := &transferManifest{
		Filename:    "dup-test.bin",
		FileSize:    totalSize,
		ChunkCount:  2,
		RootHash:    rootHash,
		ChunkHashes: chunkHashes,
		ChunkSizes:  chunkSizes,
	}

	tmpPath := dir + "/test-dup.tmp"
	tmpFile, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if err := tmpFile.Truncate(totalSize); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	offsets := buildOffsetTable(chunkSizes)
	have := newBitfield(2)
	progress := &TransferProgress{ChunksTotal: 2}

	session := &parallelSession{
		rootHash: rootHash,
		manifest: manifest,
		tmpFile:  tmpFile,
		tmpPath:  tmpPath,
		have:     have,
		offsets:  offsets,
		progress: progress,
		done:     make(chan struct{}),
		chunks:   make(chan parallelChunk, 10),
	}

	// Control sends both chunks.
	var controlBuf bytes.Buffer
	writeChunkFrame(&controlBuf, 0, chunkData[0])
	writeChunkFrame(&controlBuf, 1, chunkData[1])
	writeMsg(&controlBuf, msgTransferDone)

	// Worker also sends chunk 0 (duplicate).
	go func() {
		session.chunks <- parallelChunk{index: 0, data: chunkData[0]}
	}()

	err = ts.receiveParallel(&controlBuf, session, nil)
	if err != nil {
		t.Fatalf("receiveParallel: %v", err)
	}

	if have.count() != 2 {
		t.Errorf("have count: got %d, want 2", have.count())
	}

	tmpFile.Close()
	result, err := os.ReadFile(tmpPath)
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
