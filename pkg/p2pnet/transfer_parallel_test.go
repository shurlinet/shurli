package p2pnet

import (
	"bytes"
	"encoding/binary"
	"io"
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
