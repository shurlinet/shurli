package p2pnet

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
		ChunkSizes:  []uint32{4096, 8192, 2048},
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
	for i := range parsed.ChunkSizes {
		if parsed.ChunkSizes[i] != original.ChunkSizes[i] {
			t.Errorf("chunk size %d: got %d, want %d", i, parsed.ChunkSizes[i], original.ChunkSizes[i])
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
		{"../../../etc/passwd", "etc/passwd"},
		{"/etc/shadow", "etc/shadow"},
		{"../../secret.txt", "secret.txt"},
		{"normal-file.txt", "normal-file.txt"},
		{"sub/dir/file.txt", "sub/dir/file.txt"},
	}

	for _, tt := range tests {
		var buf bytes.Buffer
		m := &transferManifest{
			Filename:    tt.input,
			FileSize:    1,
			ChunkCount:  1,
			RootHash:    MerkleRoot(hashes),
			ChunkHashes: hashes,
			ChunkSizes:  []uint32{1},
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
			ChunkSizes:  []uint32{1},
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
		ChunkSizes:  []uint32{1},
	}
	if err := writeManifest(&buf, m); err != nil {
		t.Fatal(err)
	}
	_, err := readManifest(&buf)
	if err == nil {
		t.Error("expected error for oversized file")
	}
}

func TestManifestChunkSizesMismatch(t *testing.T) {
	hash := blake3Sum([]byte("x"))
	hashes := [][32]byte{hash}
	var buf bytes.Buffer
	m := &transferManifest{
		Filename:    "test.bin",
		FileSize:    1,
		ChunkCount:  1,
		RootHash:    MerkleRoot(hashes),
		ChunkHashes: hashes,
		ChunkSizes:  []uint32{1, 2}, // 2 sizes for 1 chunk
	}
	err := writeManifest(&buf, m)
	if err == nil {
		t.Error("expected error for chunk sizes count mismatch")
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
	for _, msg := range []byte{msgAccept, msgReject, msgResumeResponse} {
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

// --- Bitfield tests ---

func TestBitfield(t *testing.T) {
	bf := newBitfield(100)

	if bf.count() != 0 {
		t.Errorf("empty bitfield count: got %d, want 0", bf.count())
	}
	if bf.missing() != 100 {
		t.Errorf("empty bitfield missing: got %d, want 100", bf.missing())
	}

	bf.set(0)
	bf.set(50)
	bf.set(99)

	if !bf.has(0) || !bf.has(50) || !bf.has(99) {
		t.Error("expected set bits to be present")
	}
	if bf.has(1) || bf.has(49) || bf.has(98) {
		t.Error("expected unset bits to be absent")
	}
	if bf.count() != 3 {
		t.Errorf("count: got %d, want 3", bf.count())
	}
	if bf.missing() != 97 {
		t.Errorf("missing: got %d, want 97", bf.missing())
	}

	// Out of bounds: should not panic.
	bf.set(-1)
	bf.set(100)
	if bf.has(-1) || bf.has(100) {
		t.Error("out of bounds should return false")
	}
}

func TestBitfieldAllSet(t *testing.T) {
	bf := newBitfield(16)
	for i := 0; i < 16; i++ {
		bf.set(i)
	}
	if bf.count() != 16 {
		t.Errorf("all set count: got %d, want 16", bf.count())
	}
	if bf.missing() != 0 {
		t.Errorf("all set missing: got %d, want 0", bf.missing())
	}
}

func TestBitfieldOddSize(t *testing.T) {
	// 7 chunks = 1 byte, last bit partially used.
	bf := newBitfield(7)
	for i := 0; i < 7; i++ {
		bf.set(i)
	}
	if bf.count() != 7 {
		t.Errorf("7-bit count: got %d, want 7", bf.count())
	}
	// Bit 7 should not exist.
	if bf.has(7) {
		t.Error("bit 7 should not exist in 7-bit bitfield")
	}
}

// --- Resume wire protocol tests ---

func TestResumeRequestRoundtrip(t *testing.T) {
	bf := newBitfield(100)
	bf.set(0)
	bf.set(42)
	bf.set(99)

	var buf bytes.Buffer
	if err := writeResumeRequest(&buf, bf); err != nil {
		t.Fatalf("writeResumeRequest: %v", err)
	}

	// Read the type byte first (as readMsg would).
	typeByte, err := readMsg(&buf)
	if err != nil {
		t.Fatalf("readMsg: %v", err)
	}
	if typeByte != msgResumeRequest {
		t.Fatalf("type: got %d, want %d", typeByte, msgResumeRequest)
	}

	// Read the payload.
	bfData, err := readResumePayload(&buf)
	if err != nil {
		t.Fatalf("readResumePayload: %v", err)
	}

	// Reconstruct and verify.
	got := &bitfield{
		bits: make([]byte, (100+7)/8),
		n:    100,
	}
	copy(got.bits, bfData)

	if !got.has(0) || !got.has(42) || !got.has(99) {
		t.Error("resume bitfield lost set bits")
	}
	if got.has(1) || got.has(50) {
		t.Error("resume bitfield has spurious bits")
	}
}

// --- Checkpoint tests ---

func TestCheckpointRoundtrip(t *testing.T) {
	dir := t.TempDir()

	hashes := make([][32]byte, 5)
	for i := range hashes {
		hashes[i] = blake3Sum([]byte{byte(i)})
	}

	manifest := &transferManifest{
		Filename:    "resume-test.bin",
		FileSize:    5000,
		ChunkCount:  5,
		Flags:       flagCompressed,
		RootHash:    MerkleRoot(hashes),
		ChunkHashes: hashes,
		ChunkSizes:  []uint32{1000, 1000, 1000, 1000, 1000},
	}

	have := newBitfield(5)
	have.set(0)
	have.set(2)
	have.set(4)

	tmpPath := filepath.Join(dir, ".shurli-tmp-abc123-resume-test.bin")
	os.WriteFile(tmpPath, make([]byte, 5000), 0600)

	ckpt := &transferCheckpoint{
		manifest: manifest,
		have:     have,
		tmpPath:  tmpPath,
	}

	if err := ckpt.save(dir); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// Verify checkpoint file exists.
	ckptPath := checkpointPath(dir, manifest.RootHash)
	if _, err := os.Stat(ckptPath); err != nil {
		t.Fatalf("checkpoint file missing: %v", err)
	}

	// Load it back.
	loaded, err := loadCheckpoint(dir, manifest.RootHash)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}

	if loaded.manifest.Filename != manifest.Filename {
		t.Errorf("filename: got %q, want %q", loaded.manifest.Filename, manifest.Filename)
	}
	if loaded.manifest.FileSize != manifest.FileSize {
		t.Errorf("fileSize: got %d, want %d", loaded.manifest.FileSize, manifest.FileSize)
	}
	if loaded.manifest.ChunkCount != manifest.ChunkCount {
		t.Errorf("chunkCount: got %d, want %d", loaded.manifest.ChunkCount, manifest.ChunkCount)
	}
	if loaded.manifest.RootHash != manifest.RootHash {
		t.Error("rootHash mismatch")
	}
	if loaded.have.count() != 3 {
		t.Errorf("have count: got %d, want 3", loaded.have.count())
	}
	if !loaded.have.has(0) || !loaded.have.has(2) || !loaded.have.has(4) {
		t.Error("loaded bitfield lost set bits")
	}
	if loaded.have.has(1) || loaded.have.has(3) {
		t.Error("loaded bitfield has spurious bits")
	}
	if filepath.Base(loaded.tmpPath) != filepath.Base(tmpPath) {
		t.Errorf("tmpPath: got %q, want %q", filepath.Base(loaded.tmpPath), filepath.Base(tmpPath))
	}

	// Remove checkpoint.
	removeCheckpoint(dir, manifest.RootHash)
	if _, err := os.Stat(ckptPath); !os.IsNotExist(err) {
		t.Error("checkpoint should be removed")
	}
}

func TestCheckpointNotFound(t *testing.T) {
	dir := t.TempDir()
	var fakeHash [32]byte
	_, err := loadCheckpoint(dir, fakeHash)
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist, got: %v", err)
	}
}

// --- Offset table tests ---

func TestBuildOffsetTable(t *testing.T) {
	sizes := []uint32{1000, 2000, 500, 3000}
	offsets := buildOffsetTable(sizes)

	expected := []int64{0, 1000, 3000, 3500}
	for i, off := range offsets {
		if off != expected[i] {
			t.Errorf("offset[%d]: got %d, want %d", i, off, expected[i])
		}
	}
}

func TestBuildOffsetTableEmpty(t *testing.T) {
	offsets := buildOffsetTable(nil)
	if len(offsets) != 0 {
		t.Errorf("empty: got %d offsets, want 0", len(offsets))
	}
}

func TestPopcount8(t *testing.T) {
	tests := []struct {
		b    byte
		want int
	}{
		{0x00, 0},
		{0x01, 1},
		{0x0F, 4},
		{0xFF, 8},
		{0xAA, 4},
		{0x55, 4},
	}
	for _, tt := range tests {
		got := popcount8(tt.b)
		if got != tt.want {
			t.Errorf("popcount8(0x%02X): got %d, want %d", tt.b, got, tt.want)
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

func TestSanitizeRelativePath(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		// Path traversal attacks.
		{"../../etc/passwd", "etc/passwd"},
		{"../../../secret.txt", "secret.txt"},
		// Leading slash (absolute path prevention).
		{"/etc/shadow", "etc/shadow"},
		{"/absolute/path/file.txt", "absolute/path/file.txt"},
		// Normal relative paths preserved.
		{"mydir/subdir/file.txt", "mydir/subdir/file.txt"},
		{"simple.txt", "simple.txt"},
		// Mixed traversal and valid components.
		{"foo/../bar/baz.txt", "foo/bar/baz.txt"},
		{"./current/file.txt", "current/file.txt"},
		// Empty and dot-only paths.
		{"", ""},
		{".", ""},
		{"..", ""},
		{"../..", ""},
		// Backslash normalization.
		{"dir\\subdir\\file.txt", "dir/subdir/file.txt"},
		// Empty segments.
		{"dir//subdir///file.txt", "dir/subdir/file.txt"},
		// Control characters stripped from components.
		{"dir/fi\x00le.txt", "dir/file.txt"},
	}
	for _, tt := range tests {
		got := sanitizeRelativePath(tt.input)
		if got != tt.expected {
			t.Errorf("sanitizeRelativePath(%q): got %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestFinalPathCreatesSubdirectories(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// Should create subdirectories for relative paths.
	path, err := ts.finalPath("mydir/subdir/file.txt")
	if err != nil {
		t.Fatalf("finalPath with subdirs: %v", err)
	}

	expected := filepath.Join(dir, "mydir", "subdir", "file.txt")
	if path != expected {
		t.Errorf("finalPath: got %q, want %q", path, expected)
	}

	// Verify parent directories were created.
	parentDir := filepath.Join(dir, "mydir", "subdir")
	info, err := os.Stat(parentDir)
	if err != nil {
		t.Fatalf("parent dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("parent path is not a directory")
	}
}

func TestFinalPathSubdirCollision(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// Create the first file.
	path1, err := ts.finalPath("sub/file.txt")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	os.WriteFile(path1, []byte("x"), 0644)

	// Second call should get collision-avoidance name.
	path2, err := ts.finalPath("sub/file.txt")
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if filepath.Base(path2) != "file (1).txt" {
		t.Errorf("collision: got %q, want file (1).txt", filepath.Base(path2))
	}
	// Should be in the same subdirectory.
	if filepath.Dir(path2) != filepath.Dir(path1) {
		t.Errorf("collision should be in same dir: %q vs %q", filepath.Dir(path2), filepath.Dir(path1))
	}
}

func TestSendDirectoryNotADirectory(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// Create a regular file.
	f := filepath.Join(dir, "regular.txt")
	os.WriteFile(f, []byte("hello"), 0644)

	_, err := ts.SendDirectory(
		context.Background(),
		f,
		nil,
		SendOptions{},
	)
	if err == nil {
		t.Error("expected error for non-directory")
	}
}

func TestSendDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	emptyDir := filepath.Join(dir, "empty")
	os.Mkdir(emptyDir, 0755)

	_, err := ts.SendDirectory(
		context.Background(),
		emptyDir,
		nil,
		SendOptions{},
	)
	if err == nil {
		t.Error("expected error for empty directory")
	}
}

func TestSendDirectorySkipsSymlinksAndSpecialFiles(t *testing.T) {
	dir := t.TempDir()

	// Create directory structure.
	srcDir := filepath.Join(dir, "src")
	os.MkdirAll(filepath.Join(srcDir, "sub"), 0755)

	// Regular files.
	os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("bbb"), 0644)

	// Symlink (should be skipped).
	os.Symlink(filepath.Join(srcDir, "a.txt"), filepath.Join(srcDir, "link.txt"))

	// Walk and collect - we test the walk logic indirectly by verifying
	// SendDirectory rejects empty dirs but accepts dirs with regular files.
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)

	// This will fail at stream open (nil opener) but only AFTER walking - proving the walk found files.
	_, err := ts.SendDirectory(
		context.Background(),
		srcDir,
		func() (network.Stream, error) {
			return nil, fmt.Errorf("test: no stream")
		},
		SendOptions{},
	)
	// Should fail at stream open, not at "empty directory".
	if err == nil {
		t.Error("expected error from stream opener")
	}
	if strings.Contains(err.Error(), "empty") {
		t.Errorf("should not be empty error, got: %v", err)
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

// --- Timed receive mode tests ---

func TestSetTimedModeActivatesAndReverts(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir:  dir,
		ReceiveMode: ReceiveModeContacts,
		Compress:    true,
	}, nil, nil)

	if err := ts.SetTimedMode(100 * time.Millisecond); err != nil {
		t.Fatalf("SetTimedMode: %v", err)
	}

	// Should be in timed mode now.
	if got := ts.GetReceiveMode(); got != ReceiveModeTimed {
		t.Errorf("after SetTimedMode: got %q, want timed", got)
	}

	// Remaining should be > 0.
	if rem := ts.TimedModeRemaining(); rem <= 0 {
		t.Errorf("TimedModeRemaining: got %v, want > 0", rem)
	}

	// Wait for expiry.
	time.Sleep(200 * time.Millisecond)

	// Should have reverted to contacts.
	if got := ts.GetReceiveMode(); got != ReceiveModeContacts {
		t.Errorf("after expiry: got %q, want contacts", got)
	}

	// Remaining should be 0.
	if rem := ts.TimedModeRemaining(); rem != 0 {
		t.Errorf("TimedModeRemaining after expiry: got %v, want 0", rem)
	}
}

func TestSetReceiveModesCancelTimedMode(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir:  dir,
		ReceiveMode: ReceiveModeContacts,
		Compress:    true,
	}, nil, nil)

	if err := ts.SetTimedMode(1 * time.Hour); err != nil {
		t.Fatalf("SetTimedMode: %v", err)
	}

	// Cancel by switching to off.
	ts.SetReceiveMode(ReceiveModeOff)

	if got := ts.GetReceiveMode(); got != ReceiveModeOff {
		t.Errorf("after SetReceiveMode(off): got %q, want off", got)
	}

	// Timer should be cancelled, remaining = 0.
	if rem := ts.TimedModeRemaining(); rem != 0 {
		t.Errorf("TimedModeRemaining after cancel: got %v, want 0", rem)
	}
}

func TestSetTimedModeReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir:  dir,
		ReceiveMode: ReceiveModeAsk,
		Compress:    true,
	}, nil, nil)

	// First timed mode.
	if err := ts.SetTimedMode(1 * time.Hour); err != nil {
		t.Fatalf("SetTimedMode(1h): %v", err)
	}

	// Replace with shorter.
	if err := ts.SetTimedMode(100 * time.Millisecond); err != nil {
		t.Fatalf("SetTimedMode(100ms): %v", err)
	}

	// Should still revert to ask (original mode, not timed).
	time.Sleep(200 * time.Millisecond)

	if got := ts.GetReceiveMode(); got != ReceiveModeAsk {
		t.Errorf("after second expiry: got %q, want ask", got)
	}
}

func TestSetTimedModeInvalidDuration(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	if err := ts.SetTimedMode(0); err == nil {
		t.Error("SetTimedMode(0) should fail")
	}
	if err := ts.SetTimedMode(-1 * time.Second); err == nil {
		t.Error("SetTimedMode(-1s) should fail")
	}
}

func TestTimedModeRemainingWhenNotActive(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	if rem := ts.TimedModeRemaining(); rem != 0 {
		t.Errorf("TimedModeRemaining when not timed: got %v, want 0", rem)
	}
}

func TestTransferServiceQueueInitialized(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:    dir,
		Compress:      true,
		MaxConcurrent: 3,
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	if ts.queue == nil {
		t.Fatal("queue should be initialized")
	}
	if ts.queue.maxActive != 3 {
		t.Errorf("queue maxActive: got %d, want 3", ts.queue.maxActive)
	}
}

func TestTransferServiceQueueDefaultMax(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}
	if ts.queue.maxActive != 5 {
		t.Errorf("queue maxActive: got %d, want 5 (default)", ts.queue.maxActive)
	}
}

func TestSubmitSendInvalidPath(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	_, err := ts.SubmitSend("/nonexistent/path", "peer1", PriorityNormal, nil, SendOptions{})
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestSubmitSendCreatesQueuedProgress(t *testing.T) {
	dir := t.TempDir()

	// Create a test file to send.
	testFile := filepath.Join(dir, "test.txt")
	os.WriteFile(testFile, []byte("hello"), 0644)

	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// We can't use a real streamOpener without libp2p, so test the queue tracking
	// directly via the queue + transfers map.
	qID := ts.queue.Enqueue(testFile, "peer1", "send", PriorityNormal)
	progress := &TransferProgress{
		ID:        qID,
		Filename:  "test.txt",
		PeerID:    "peer1",
		Direction: "send",
		Status:    "queued",
	}
	ts.mu.Lock()
	ts.transfers[qID] = progress
	ts.mu.Unlock()

	// Verify it appears in ListTransfers.
	transfers := ts.ListTransfers()
	found := false
	for i := range transfers {
		if transfers[i].ID == qID && transfers[i].Status == "queued" {
			found = true
			break
		}
	}
	if !found {
		t.Error("queued transfer not found in ListTransfers")
	}
}

func TestCancelQueuedTransfer(t *testing.T) {
	dir := t.TempDir()

	testFile := filepath.Join(dir, "cancel-me.txt")
	os.WriteFile(testFile, []byte("data"), 0644)

	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// Enqueue and track.
	qID := ts.queue.Enqueue(testFile, "peer1", "send", PriorityNormal)
	progress := &TransferProgress{
		ID:       qID,
		Filename: "cancel-me.txt",
		PeerID:   "peer1",
		Status:   "queued",
	}
	ts.mu.Lock()
	ts.transfers[qID] = progress
	ts.mu.Unlock()

	// Cancel should succeed.
	if !ts.CancelTransfer(qID) {
		t.Fatal("CancelTransfer should return true for queued item")
	}

	// Progress should show cancelled.
	ts.mu.RLock()
	p := ts.transfers[qID]
	ts.mu.RUnlock()
	snap := p.Snapshot()
	if snap.Status != "failed" || snap.Error != "cancelled" {
		t.Errorf("cancelled transfer: status=%q error=%q, want failed/cancelled", snap.Status, snap.Error)
	}

	// Cancel again should fail (already removed from queue).
	if ts.CancelTransfer(qID) {
		t.Error("second CancelTransfer should return false")
	}
}

func TestListTransfersIncludesQueued(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// Add items to queue (without executing).
	ts.queue.Enqueue("/file1", "peer1", "send", PriorityHigh)
	ts.queue.Enqueue("/file2", "peer2", "send", PriorityNormal)

	// Also track an active transfer.
	ts.trackTransfer("active.txt", 1024, "peer3", "send", 10, true)

	transfers := ts.ListTransfers()

	// Should have 2 queued + 1 active = 3.
	if len(transfers) != 3 {
		t.Fatalf("expected 3 transfers, got %d", len(transfers))
	}

	// Queued items should appear first with status "queued".
	queuedCount := 0
	for i := range transfers {
		if transfers[i].Status == "queued" {
			queuedCount++
		}
	}
	if queuedCount != 2 {
		t.Errorf("expected 2 queued transfers, got %d", queuedCount)
	}
}

func TestCancelActiveTransfer(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// Create an active (non-done) transfer.
	p := ts.trackTransfer("active.txt", 1024, "peer1", "send", 10, true)

	if !ts.CancelTransfer(p.ID) {
		t.Fatal("CancelTransfer should succeed for active transfer")
	}

	snap := p.Snapshot()
	if snap.Status != "failed" || snap.Error != "cancelled" {
		t.Errorf("cancelled active: status=%q error=%q, want failed/cancelled", snap.Status, snap.Error)
	}
}

func TestCancelCompletedTransferFails(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	p := ts.trackTransfer("done.txt", 1024, "peer1", "send", 10, true)
	p.finish(nil) // mark as complete

	if ts.CancelTransfer(p.ID) {
		t.Error("CancelTransfer should return false for completed transfer")
	}
}

func TestTransferPrefixMatch(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// Add some pending transfers with known IDs.
	ts.mu.Lock()
	ts.pending["pending-abc-1111"] = &PendingTransfer{
		ID: "pending-abc-1111", Filename: "a.txt",
		decision: make(chan transferDecision, 1),
	}
	ts.pending["pending-abc-2222"] = &PendingTransfer{
		ID: "pending-abc-2222", Filename: "b.txt",
		decision: make(chan transferDecision, 1),
	}
	ts.pending["pending-xyz-9999"] = &PendingTransfer{
		ID: "pending-xyz-9999", Filename: "c.txt",
		decision: make(chan transferDecision, 1),
	}
	ts.mu.Unlock()

	// Exact match.
	ts.mu.RLock()
	id, p, err := ts.findPendingByPrefix("pending-abc-1111")
	ts.mu.RUnlock()
	if err != nil || id != "pending-abc-1111" || p.Filename != "a.txt" {
		t.Fatalf("exact match failed: id=%q err=%v", id, err)
	}

	// Unique prefix.
	ts.mu.RLock()
	id, p, err = ts.findPendingByPrefix("pending-xyz")
	ts.mu.RUnlock()
	if err != nil || id != "pending-xyz-9999" || p.Filename != "c.txt" {
		t.Fatalf("unique prefix failed: id=%q err=%v", id, err)
	}

	// Ambiguous prefix.
	ts.mu.RLock()
	_, _, err = ts.findPendingByPrefix("pending-abc")
	ts.mu.RUnlock()
	if err == nil {
		t.Fatal("expected error for ambiguous prefix")
	}
	if !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("expected ambiguous error, got: %v", err)
	}

	// No match.
	ts.mu.RLock()
	_, _, err = ts.findPendingByPrefix("pending-zzz")
	ts.mu.RUnlock()
	if err == nil {
		t.Fatal("expected error for no match")
	}
	if !strings.Contains(err.Error(), "no pending") {
		t.Fatalf("expected 'no pending' error, got: %v", err)
	}
}

func TestTransferRateLimit(t *testing.T) {
	rl := newTransferRateLimiter(3)

	peer := "QmTestPeer1234"

	// First 3 should pass.
	for i := 0; i < 3; i++ {
		if !rl.allow(peer) {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}

	// 4th should be rejected.
	if rl.allow(peer) {
		t.Fatal("4th request should be rate-limited")
	}

	// Different peer should be fine.
	if !rl.allow("QmOtherPeer5678") {
		t.Fatal("different peer should be allowed")
	}

	// Simulate window expiry by manipulating the bucket.
	rl.mu.Lock()
	rl.peers[peer].windowEnd = time.Now().Add(-1 * time.Second)
	rl.mu.Unlock()

	// Should be allowed again after window reset.
	if !rl.allow(peer) {
		t.Fatal("should be allowed after window reset")
	}

	// Cleanup should remove expired entries.
	rl.mu.Lock()
	rl.peers["QmStalePeer"] = &rateBucket{count: 1, windowEnd: time.Now().Add(-2 * time.Minute)}
	rl.mu.Unlock()

	rl.cleanup()

	rl.mu.Lock()
	_, staleExists := rl.peers["QmStalePeer"]
	rl.mu.Unlock()
	if staleExists {
		t.Fatal("stale peer entry should have been cleaned up")
	}
}

func TestTransferLogEventConstants(t *testing.T) {
	// Verify the new event type constants exist and have expected values.
	if EventLogSpamBlocked != "spam_blocked" {
		t.Fatalf("EventLogSpamBlocked = %q, want %q", EventLogSpamBlocked, "spam_blocked")
	}
	if EventLogDiskSpaceRejected != "disk_space_rejected" {
		t.Fatalf("EventLogDiskSpaceRejected = %q, want %q", EventLogDiskSpaceRejected, "disk_space_rejected")
	}

	// Verify they can be logged without panic.
	dir := t.TempDir()
	logPath := filepath.Join(dir, "test-transfer.log")
	logger, err := NewTransferLogger(logPath)
	if err != nil {
		t.Fatalf("NewTransferLogger: %v", err)
	}
	defer logger.Close()

	logger.Log(TransferEvent{EventType: EventLogSpamBlocked, PeerID: "QmTest", FileName: ""})
	logger.Log(TransferEvent{EventType: EventLogDiskSpaceRejected, PeerID: "QmTest", FileName: "big.dat", FileSize: 999999})

	events, err := ReadTransferEvents(logPath, 10)
	if err != nil {
		t.Fatalf("ReadTransferEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(events))
	}
	if events[0].EventType != EventLogDiskSpaceRejected {
		t.Errorf("events[0] = %q, want disk_space_rejected (newest first)", events[0].EventType)
	}
	if events[1].EventType != EventLogSpamBlocked {
		t.Errorf("events[1] = %q, want spam_blocked", events[1].EventType)
	}
}

func TestErasureFieldsOnProgress(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	p := ts.trackTransfer("test.bin", 10000, "peer1", "send", 100, true)

	// Initially zero.
	snap := p.Snapshot()
	if snap.ErasureParity != 0 || snap.ErasureOverhead != 0 {
		t.Fatal("erasure fields should be zero initially")
	}

	// Set erasure fields.
	p.mu.Lock()
	p.ErasureParity = 10
	p.ErasureOverhead = 0.10
	p.mu.Unlock()

	snap = p.Snapshot()
	if snap.ErasureParity != 10 {
		t.Errorf("ErasureParity = %d, want 10", snap.ErasureParity)
	}
	if snap.ErasureOverhead != 0.10 {
		t.Errorf("ErasureOverhead = %f, want 0.10", snap.ErasureOverhead)
	}
}
