package p2pnet

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
)

// --- Wire format tests ---

func TestManifestRoundtrip(t *testing.T) {
	hashes := make([][32]byte, 3)
	hashes[0] = blake3Sum([]byte("chunk0"))
	hashes[1] = blake3Sum([]byte("chunk1"))
	hashes[2] = blake3Sum([]byte("chunk2"))

	original := &transferManifest{
		Filename:    "test-file.txt",
		FileSize:    1024 * 1024,
		ChunkCount:  3,
		Flags:       flagCompressed,
		RootHash:    MerkleRoot(hashes),
		ChunkHashes: hashes,
	}

	var buf bytes.Buffer
	if err := writeManifest(&buf, original); err != nil {
		t.Fatalf("writeManifest: %v", err)
	}

	parsed, err := readManifest(&buf)
	if err != nil {
		t.Fatalf("readManifest: %v", err)
	}

	if parsed.Filename != original.Filename {
		t.Errorf("filename: got %q, want %q", parsed.Filename, original.Filename)
	}
	if parsed.FileSize != original.FileSize {
		t.Errorf("fileSize: got %d, want %d", parsed.FileSize, original.FileSize)
	}
	if parsed.ChunkCount != original.ChunkCount {
		t.Errorf("chunkCount: got %d, want %d", parsed.ChunkCount, original.ChunkCount)
	}
	if parsed.Flags != original.Flags {
		t.Errorf("flags: got %d, want %d", parsed.Flags, original.Flags)
	}
	if parsed.RootHash != original.RootHash {
		t.Error("rootHash mismatch")
	}
	for i := range parsed.ChunkHashes {
		if parsed.ChunkHashes[i] != original.ChunkHashes[i] {
			t.Errorf("chunk hash %d mismatch", i)
		}
	}
}

func TestManifestPathTraversal(t *testing.T) {
	hash := blake3Sum([]byte("x"))
	hashes := [][32]byte{hash}

	tests := []struct {
		input    string
		expected string
	}{
		{"../../../etc/passwd", "passwd"},
		{"/etc/shadow", "shadow"},
		{"../../secret.txt", "secret.txt"},
		{"normal-file.txt", "normal-file.txt"},
		{"sub/dir/file.txt", "file.txt"},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		m := &transferManifest{
			Filename:    tt.input,
			FileSize:    1,
			ChunkCount:  1,
			RootHash:    MerkleRoot(hashes),
			ChunkHashes: hashes,
		}
		if err := writeManifest(&buf, m); err != nil {
			t.Fatalf("write %q: %v", tt.input, err)
		}
		parsed, err := readManifest(&buf)
		if err != nil {
			t.Fatalf("read %q: %v", tt.input, err)
		}
		if parsed.Filename != tt.expected {
			t.Errorf("traversal: input %q, got %q, want %q", tt.input, parsed.Filename, tt.expected)
		}
	}
}

func TestManifestRejectDotFilenames(t *testing.T) {
	hash := blake3Sum([]byte("x"))
	hashes := [][32]byte{hash}

	for _, name := range []string{".", ".."} {
		var buf bytes.Buffer
		m := &transferManifest{
			Filename:    name,
			FileSize:    1,
			ChunkCount:  1,
			RootHash:    MerkleRoot(hashes),
			ChunkHashes: hashes,
		}
		if err := writeManifest(&buf, m); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
		_, err := readManifest(&buf)
		if err == nil {
			t.Errorf("expected error for filename %q", name)
		}
	}
}

func TestManifestInvalidMagic(t *testing.T) {
	buf := bytes.NewReader([]byte{0x99, 0x99, 0x99, 0x99, shftVersion, 0})
	_, err := readManifest(buf)
	if err == nil {
		t.Error("expected error for invalid magic")
	}
}

func TestManifestInvalidVersion(t *testing.T) {
	buf := bytes.NewReader([]byte{shftMagic0, shftMagic1, shftMagic2, shftMagic3, 0x99, 0})
	_, err := readManifest(buf)
	if err == nil {
		t.Error("expected error for invalid version")
	}
}

func TestManifestOversizedFile(t *testing.T) {
	hash := blake3Sum([]byte("x"))
	hashes := [][32]byte{hash}
	var buf bytes.Buffer
	m := &transferManifest{
		Filename:    "big.bin",
		FileSize:    maxFileSize + 1,
		ChunkCount:  1,
		RootHash:    MerkleRoot(hashes),
		ChunkHashes: hashes,
	}
	if err := writeManifest(&buf, m); err != nil {
		t.Fatal(err)
	}
	_, err := readManifest(&buf)
	if err == nil {
		t.Error("expected error for oversized file")
	}
}

func TestChunkFrameRoundtrip(t *testing.T) {
	data := []byte("hello world this is chunk data")

	var buf bytes.Buffer
	if err := writeChunkFrame(&buf, 42, data); err != nil {
		t.Fatalf("writeChunkFrame: %v", err)
	}

	idx, got, err := readChunkFrame(&buf)
	if err != nil {
		t.Fatalf("readChunkFrame: %v", err)
	}
	if idx != 42 {
		t.Errorf("index: got %d, want 42", idx)
	}
	if !bytes.Equal(got, data) {
		t.Error("data mismatch")
	}
}

func TestDoneSignal(t *testing.T) {
	var buf bytes.Buffer
	if err := writeMsg(&buf, msgTransferDone); err != nil {
		t.Fatal(err)
	}
	// Pad with extra bytes so readChunkFrame can read 9 bytes.
	buf.Write(make([]byte, 8))

	idx, _, err := readChunkFrame(&buf)
	if err != nil {
		t.Fatalf("readChunkFrame: %v", err)
	}
	if idx != -1 {
		t.Errorf("expected -1 (done sentinel), got %d", idx)
	}
}

func TestMsgRoundtrip(t *testing.T) {
	for _, msg := range []byte{msgAccept, msgReject} {
		var buf bytes.Buffer
		if err := writeMsg(&buf, msg); err != nil {
			t.Fatalf("writeMsg(%d): %v", msg, err)
		}
		got, err := readMsg(&buf)
		if err != nil {
			t.Fatalf("readMsg: %v", err)
		}
		if got != msg {
			t.Errorf("msg: got %d, want %d", got, msg)
		}
	}
}

// --- TransferService tests ---

func TestTransferServiceCreation(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	if ts.receiveDir != dir {
		t.Errorf("receiveDir: got %q, want %q", ts.receiveDir, dir)
	}
	if ts.receiveMode != ReceiveModeContacts {
		t.Errorf("receiveMode: got %q, want %q", ts.receiveMode, ReceiveModeContacts)
	}
}

func TestFinalPath(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// First should be the original name.
	path1, err := ts.finalPath("test.txt")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if filepath.Base(path1) != "test.txt" {
		t.Errorf("first: got %q, want test.txt", filepath.Base(path1))
	}
	// Create the file so next call detects collision.
	os.WriteFile(path1, []byte("x"), 0644)

	// Second should be "test (1).txt".
	path2, err := ts.finalPath("test.txt")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if filepath.Base(path2) != "test (1).txt" {
		t.Errorf("second: got %q, want test (1).txt", filepath.Base(path2))
	}
}

func TestTransferProgress(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	p := ts.trackTransfer("photo.jpg", 1024, "12D3KooW...", "send", 10, true)
	if p.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if p.Done {
		t.Error("should not be done initially")
	}
	if p.ChunksTotal != 10 {
		t.Errorf("chunks_total: got %d, want 10", p.ChunksTotal)
	}

	p.updateChunks(512, 5)
	snap := p.Snapshot()
	if snap.Transferred != 512 {
		t.Errorf("transferred: got %d, want 512", snap.Transferred)
	}
	if snap.ChunksDone != 5 {
		t.Errorf("chunks_done: got %d, want 5", snap.ChunksDone)
	}

	p.finish(nil)
	snap = p.Snapshot()
	if !snap.Done {
		t.Error("should be done after finish")
	}
	if snap.Status != "complete" {
		t.Errorf("status: got %q, want complete", snap.Status)
	}
	if snap.Error != "" {
		t.Errorf("unexpected error: %s", snap.Error)
	}

	found, ok := ts.GetTransfer(p.ID)
	if !ok {
		t.Fatal("transfer not found")
	}
	if found.ID != p.ID {
		t.Errorf("ID mismatch: got %q, want %q", found.ID, p.ID)
	}

	list := ts.ListTransfers()
	if len(list) != 1 {
		t.Fatalf("list: got %d, want 1", len(list))
	}
}

func TestTransferServiceReceiveMode(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir:  dir,
		ReceiveMode: ReceiveModeOff,
		Compress:    true,
	}, nil, nil)

	if ts.receiveMode != ReceiveModeOff {
		t.Errorf("receiveMode: got %q, want off", ts.receiveMode)
	}

	ts.SetReceiveMode(ReceiveModeAsk)
	ts.mu.RLock()
	mode := ts.receiveMode
	ts.mu.RUnlock()
	if mode != ReceiveModeAsk {
		t.Errorf("after set: got %q, want ask", mode)
	}
}

func TestSanitizeFilename(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"normal.txt", "normal.txt"},
		{"has\x00null.txt", "hasnull.txt"},
		{"control\x01chars\x1f.txt", "controlchars.txt"},
		{"", ""},
	}
	for _, tt := range tests {
		got := sanitizeFilename(tt.input)
		if got != tt.expected {
			t.Errorf("sanitize(%q): got %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDiskSpaceCheck(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// Requesting 0 bytes should succeed.
	if err := ts.checkDiskSpace(0); err != nil {
		t.Errorf("checkDiskSpace(0): %v", err)
	}

	// Requesting an absurdly large amount should fail.
	err := ts.checkDiskSpace(1 << 60)
	if err == nil {
		t.Error("expected error for 1 EB disk space check")
	}
}

func TestCustomHandlerServiceRegistration(t *testing.T) {
	svc := &Service{
		Name:     "test-plugin",
		Protocol: "/shurli/test-plugin/1.0.0",
		Handler: func(serviceName string, s network.Stream) {
			s.Close()
		},
		Enabled: true,
	}

	if svc.LocalAddress != "" {
		t.Error("LocalAddress should be empty for custom handler")
	}
	if svc.Handler == nil {
		t.Error("Handler should be set")
	}
}
