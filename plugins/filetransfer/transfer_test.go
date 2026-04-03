package filetransfer

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// --- Streaming header wire format tests ---

func TestStreamingHeaderRoundtrip(t *testing.T) {
	files := []fileEntry{
		{Path: "dir/file1.txt", Size: 1024, MetaFlags: metaHasMode | metaHasMtime, Mode: 0644, Mtime: 1711900000},
		{Path: "dir/file2.bin", Size: 2048, MetaFlags: metaHasMode, Mode: 0755},
		{Path: "readme.md", Size: 512},
	}
	filePaths := []string{"/tmp/a", "/tmp/b", "/tmp/c"}
	if err := sortFileTable(files, filePaths); err != nil {
		t.Fatalf("sortFileTable: %v", err)
	}

	var totalSize int64
	for _, f := range files {
		totalSize += f.Size
	}

	var transferID [32]byte
	copy(transferID[:], "test-transfer-id-32-bytes-long!!")

	var buf bytes.Buffer
	if err := writeHeader(&buf, files, flagCompressed, totalSize, transferID); err != nil {
		t.Fatalf("writeHeader: %v", err)
	}

	parsedFiles, parsedTotal, parsedFlags, parsedID, cumOffsets, err := readHeader(&buf)
	if err != nil {
		t.Fatalf("readHeader: %v", err)
	}

	if parsedTotal != totalSize {
		t.Errorf("totalSize: got %d, want %d", parsedTotal, totalSize)
	}
	if parsedFlags != flagCompressed {
		t.Errorf("flags: got %d, want %d", parsedFlags, flagCompressed)
	}
	if parsedID != transferID {
		t.Error("transferID mismatch")
	}
	if len(parsedFiles) != len(files) {
		t.Fatalf("file count: got %d, want %d", len(parsedFiles), len(files))
	}
	for i, f := range parsedFiles {
		if f.Path != files[i].Path {
			t.Errorf("file %d path: got %q, want %q", i, f.Path, files[i].Path)
		}
		if f.Size != files[i].Size {
			t.Errorf("file %d size: got %d, want %d", i, f.Size, files[i].Size)
		}
		if f.MetaFlags != files[i].MetaFlags {
			t.Errorf("file %d metaFlags: got %d, want %d", i, f.MetaFlags, files[i].MetaFlags)
		}
		if f.Mode != files[i].Mode {
			t.Errorf("file %d mode: got %o, want %o", i, f.Mode, files[i].Mode)
		}
		if f.Mtime != files[i].Mtime {
			t.Errorf("file %d mtime: got %d, want %d", i, f.Mtime, files[i].Mtime)
		}
	}
	if len(cumOffsets) != len(files)+1 {
		t.Errorf("cumOffsets: got %d entries, want %d", len(cumOffsets), len(files)+1)
	}
}

func TestStreamingHeaderPathTraversal(t *testing.T) {
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
		files := []fileEntry{{Path: tt.input, Size: 1}}
		filePaths := []string{"/tmp/x"}

		var transferID [32]byte
		var buf bytes.Buffer
		if err := writeHeader(&buf, files, 0, 1, transferID); err != nil {
			t.Fatalf("write %q: %v", tt.input, err)
		}
		parsed, _, _, _, _, err := readHeader(&buf)
		if err != nil {
			t.Fatalf("read %q: %v", tt.input, err)
		}
		_ = filePaths
		if parsed[0].Path != tt.expected {
			t.Errorf("traversal: input %q, got %q, want %q", tt.input, parsed[0].Path, tt.expected)
		}
	}
}

func TestStreamingHeaderRejectDotFilenames(t *testing.T) {
	for _, name := range []string{".", ".."} {
		files := []fileEntry{{Path: name, Size: 1}}
		var transferID [32]byte
		var buf bytes.Buffer
		if err := writeHeader(&buf, files, 0, 1, transferID); err != nil {
			t.Fatalf("write %q: %v", name, err)
		}
		_, _, _, _, _, err := readHeader(&buf)
		if err == nil {
			t.Errorf("expected error for path %q", name)
		}
	}
}

func TestStreamingHeaderInvalidMagic(t *testing.T) {
	buf := bytes.NewReader([]byte{0x99, 0x99, 0x99, 0x99, shftVersion, 0})
	_, _, _, _, _, err := readHeader(buf)
	if err == nil {
		t.Error("expected error for invalid magic")
	}
}

func TestStreamingHeaderInvalidVersion(t *testing.T) {
	buf := bytes.NewReader([]byte{shftMagic0, shftMagic1, shftMagic2, shftMagic3, 0x99, 0})
	_, _, _, _, _, err := readHeader(buf)
	if err == nil {
		t.Error("expected error for invalid version")
	}
}

func TestStreamChunkFrameRoundtrip(t *testing.T) {
	data := []byte("hello world this is chunk data")
	hash := sdk.Blake3Sum(data)

	sc := streamChunk{
		fileIdx:    0,
		chunkIdx:   42,
		offset:     1024,
		hash:       hash,
		decompSize: uint32(len(data)),
		data:       data,
	}

	var buf bytes.Buffer
	if err := writeStreamChunkFrame(&buf, sc); err != nil {
		t.Fatalf("writeStreamChunkFrame: %v", err)
	}

	got, msgType, err := readStreamChunkFrame(&buf)
	if err != nil {
		t.Fatalf("readStreamChunkFrame: %v", err)
	}
	if msgType != msgStreamChunk {
		t.Errorf("msgType: got 0x%02x, want 0x%02x", msgType, msgStreamChunk)
	}
	if got.chunkIdx != 42 {
		t.Errorf("chunkIdx: got %d, want 42", got.chunkIdx)
	}
	if got.offset != 1024 {
		t.Errorf("offset: got %d, want 1024", got.offset)
	}
	if got.hash != hash {
		t.Error("hash mismatch")
	}
	if got.decompSize != uint32(len(data)) {
		t.Errorf("decompSize: got %d, want %d", got.decompSize, len(data))
	}
	if !bytes.Equal(got.data, data) {
		t.Error("data mismatch")
	}
}

func TestTrailerSignal(t *testing.T) {
	var rootHash [32]byte
	copy(rootHash[:], bytes.Repeat([]byte{0xAB}, 32))

	var buf bytes.Buffer
	if err := writeTrailer(&buf, 10, rootHash, nil, nil); err != nil {
		t.Fatalf("writeTrailer: %v", err)
	}

	_, msgType, err := readStreamChunkFrame(&buf)
	if err != nil {
		t.Fatalf("readStreamChunkFrame: %v", err)
	}
	if msgType != msgTrailer {
		t.Errorf("expected msgTrailer (0x%02x), got 0x%02x", msgTrailer, msgType)
	}

	chunkCount, gotRoot, _, _, err := readTrailer(&buf, false)
	if err != nil {
		t.Fatalf("readTrailer: %v", err)
	}
	if chunkCount != 10 {
		t.Errorf("chunkCount: got %d, want 10", chunkCount)
	}
	if gotRoot != rootHash {
		t.Error("root hash mismatch")
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

// --- Checkpoint tests (streaming protocol format) ---

func TestCheckpointRoundtrip(t *testing.T) {
	dir := t.TempDir()

	files := []fileEntry{
		{Path: "file1.txt", Size: 2000, MetaFlags: metaHasMode, Mode: 0644},
		{Path: "file2.bin", Size: 3000},
	}

	ck := contentKey(files)

	hashes := make([][32]byte, 5)
	sizes := make([]uint32, 5)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
		sizes[i] = 1000
	}

	have := newBitfield(5)
	have.set(0)
	have.set(2)
	have.set(4)

	// Create temp files so restoreReceiveState can find them.
	tmpName0 := ".shurli-tmp-0-file1.txt"
	tmpName1 := ".shurli-tmp-1-file2.bin"
	os.WriteFile(filepath.Join(dir, tmpName0), make([]byte, 2000), 0600)
	os.WriteFile(filepath.Join(dir, tmpName1), make([]byte, 3000), 0600)

	ckpt := &transferCheckpoint{
		contentKey: ck,
		files:      files,
		totalSize:  5000,
		flags:      flagCompressed,
		have:       have,
		hashes:     hashes,
		sizes:      sizes,
		tmpPaths:   []string{filepath.Join(dir, tmpName0), filepath.Join(dir, tmpName1)},
	}

	if err := ckpt.save(dir); err != nil {
		t.Fatalf("save checkpoint: %v", err)
	}

	// Verify checkpoint file exists.
	ckptPath := checkpointPath(dir, ck)
	if _, err := os.Stat(ckptPath); err != nil {
		t.Fatalf("checkpoint file missing: %v", err)
	}

	// Load it back.
	loaded, err := loadCheckpoint(dir, ck)
	if err != nil {
		t.Fatalf("load checkpoint: %v", err)
	}

	if loaded.contentKey != ck {
		t.Error("contentKey mismatch")
	}
	if loaded.totalSize != 5000 {
		t.Errorf("totalSize: got %d, want 5000", loaded.totalSize)
	}
	if loaded.flags != flagCompressed {
		t.Errorf("flags: got %d, want %d", loaded.flags, flagCompressed)
	}
	if len(loaded.files) != 2 {
		t.Fatalf("file count: got %d, want 2", len(loaded.files))
	}
	if loaded.files[0].Path != "file1.txt" {
		t.Errorf("file 0 path: got %q", loaded.files[0].Path)
	}
	if loaded.files[0].Mode != 0644 {
		t.Errorf("file 0 mode: got %o, want 644", loaded.files[0].Mode)
	}
	if loaded.files[1].Path != "file2.bin" {
		t.Errorf("file 1 path: got %q", loaded.files[1].Path)
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
	if len(loaded.hashes) != 5 {
		t.Fatalf("hash count: got %d, want 5", len(loaded.hashes))
	}
	for i, h := range loaded.hashes {
		if h != hashes[i] {
			t.Errorf("hash %d mismatch", i)
		}
	}
	for i, s := range loaded.sizes {
		if s != sizes[i] {
			t.Errorf("size %d: got %d, want %d", i, s, sizes[i])
		}
	}
	if len(loaded.tmpPaths) != 2 {
		t.Fatalf("tmpPath count: got %d, want 2", len(loaded.tmpPaths))
	}
	if filepath.Base(loaded.tmpPaths[0]) != tmpName0 {
		t.Errorf("tmpPath 0: got %q, want %q", filepath.Base(loaded.tmpPaths[0]), tmpName0)
	}
	if filepath.Base(loaded.tmpPaths[1]) != tmpName1 {
		t.Errorf("tmpPath 1: got %q, want %q", filepath.Base(loaded.tmpPaths[1]), tmpName1)
	}

	// Remove checkpoint.
	removeStreamCheckpoint(dir, ck)
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

func TestCheckpointOldFormatDiscarded(t *testing.T) {
	dir := t.TempDir()
	var ck [32]byte
	copy(ck[:], "test-content-key-for-old-format!")

	// Write a file with wrong magic (simulating old format).
	path := checkpointPath(dir, ck)
	os.WriteFile(path, []byte("SHFT\x02old-checkpoint-data"), 0600)

	// loadCheckpoint should detect wrong magic, delete the file, return not-exist.
	_, err := loadCheckpoint(dir, ck)
	if !os.IsNotExist(err) {
		t.Errorf("expected not-exist for old format, got: %v", err)
	}

	// File should have been deleted.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Error("old checkpoint file should have been deleted")
	}
}

func TestCheckpointRestoreReceiveState(t *testing.T) {
	dir := t.TempDir()

	files := []fileEntry{
		{Path: "data.bin", Size: 3000},
	}
	ck := contentKey(files)

	hashes := make([][32]byte, 3)
	sizes := make([]uint32, 3)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
		sizes[i] = 1000
	}

	have := newBitfield(3)
	have.set(0)
	have.set(1)

	tmpName := ".shurli-tmp-0-data.bin"
	os.WriteFile(filepath.Join(dir, tmpName), make([]byte, 3000), 0600)

	ckpt := &transferCheckpoint{
		contentKey: ck,
		files:      files,
		totalSize:  3000,
		flags:      0,
		have:       have,
		hashes:     hashes,
		sizes:      sizes,
		tmpPaths:   []string{filepath.Join(dir, tmpName)},
	}

	state, err := ckpt.restoreReceiveState(dir)
	if err != nil {
		t.Fatalf("restoreReceiveState: %v", err)
	}
	defer state.cleanup()

	// Verify restored state.
	if state.totalSize != 3000 {
		t.Errorf("totalSize: got %d, want 3000", state.totalSize)
	}
	if state.receivedBytes != 2000 {
		t.Errorf("receivedBytes: got %d, want 2000", state.receivedBytes)
	}
	if state.maxChunkIdx != 1 {
		t.Errorf("maxChunkIdx: got %d, want 1", state.maxChunkIdx)
	}
	if state.receivedBitfield == nil {
		t.Fatal("receivedBitfield should not be nil")
	}
	if !state.receivedBitfield.has(0) || !state.receivedBitfield.has(1) {
		t.Error("receivedBitfield lost set bits")
	}
	if state.receivedBitfield.has(2) {
		t.Error("receivedBitfield has spurious bit")
	}
	if state.tmpFiles[0] == nil {
		t.Error("temp file should be re-opened")
	}
}

func TestCheckpointRestoreBitfieldGrown(t *testing.T) {
	dir := t.TempDir()

	// Simulate a mid-transfer checkpoint: 3 of ~78 expected chunks received.
	// 10MB / 128KB avg = ~78 estimated chunks. Checkpoint saved after receiving 3.
	totalSize := int64(10 << 20) // 10 MB
	files := []fileEntry{{Path: "big.bin", Size: totalSize}}
	ck := contentKey(files)

	// Checkpoint saved with only 3 chunks (maxChunkIdx=2, so hashes/sizes length=3).
	hashes := make([][32]byte, 3)
	sizes := make([]uint32, 3)
	for i := range hashes {
		hashes[i] = sdk.Blake3Sum([]byte{byte(i)})
		sizes[i] = 131072 // 128K avg chunk
	}
	have := newBitfield(3)
	have.set(0)
	have.set(1)
	have.set(2)

	tmpName := ".shurli-tmp-0-big.bin"
	os.WriteFile(filepath.Join(dir, tmpName), make([]byte, 1000), 0600)

	ckpt := &transferCheckpoint{
		contentKey: ck,
		files:      files,
		totalSize:  totalSize,
		flags:      0,
		have:       have,
		hashes:     hashes,
		sizes:      sizes,
		tmpPaths:   []string{filepath.Join(dir, tmpName)},
	}

	state, err := ckpt.restoreReceiveState(dir)
	if err != nil {
		t.Fatalf("restoreReceiveState: %v", err)
	}
	defer state.cleanup()

	// The restored bitfield must be large enough for chunks beyond the checkpoint.
	est := estimateChunkCount(totalSize)
	if est <= 3 {
		t.Fatalf("test setup error: estimated chunks %d should be > 3", est)
	}
	if state.receivedBitfield.n < est {
		t.Errorf("bitfield.n = %d, too small for estimated %d chunks", state.receivedBitfield.n, est)
	}

	// Verify chunks 0-2 are marked as received.
	if !state.receivedBitfield.has(0) || !state.receivedBitfield.has(1) || !state.receivedBitfield.has(2) {
		t.Error("restored chunks should be marked as received")
	}

	// Verify we can set bits BEYOND the checkpoint's original count.
	// This was the bug: bitfield.n = 3, set(50) was silently ignored.
	state.receivedBitfield.set(50)
	if !state.receivedBitfield.has(50) {
		t.Error("bitfield should be able to track chunks beyond checkpoint count")
	}
}

func TestCheckpointTmpPathTraversalRejected(t *testing.T) {
	dir := t.TempDir()

	files := []fileEntry{{Path: "file.txt", Size: 100}}
	ck := contentKey(files)

	// Save a valid checkpoint first, then corrupt the tmp path on disk.
	ckpt := &transferCheckpoint{
		contentKey: ck,
		files:      files,
		totalSize:  100,
		flags:      0,
		have:       newBitfield(1),
		hashes:     make([][32]byte, 1),
		sizes:      []uint32{100},
		tmpPaths:   []string{filepath.Join(dir, ".shurli-tmp-0-file.txt")},
	}
	os.WriteFile(filepath.Join(dir, ".shurli-tmp-0-file.txt"), make([]byte, 100), 0600)

	if err := ckpt.save(dir); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Corrupt the checkpoint file: replace the tmp path name with a traversal path.
	// The tmp path section is at the end of the file. We'll just write a new file
	// with the traversal path using the internal write functions.
	ckpt.tmpPaths = []string{"../../etc/evil"}
	// Bypass writeCheckpointTmpPaths (which strips to base name) by writing directly.
	path := checkpointPath(dir, ck)
	data, _ := os.ReadFile(path)
	// Find the last 2 bytes of the file which is the tmp path count section.
	// Easier to just build a corrupted checkpoint with raw bytes.
	f, _ := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	f.Write(data[:len(data)-2-4-len(".shurli-tmp-0-file.txt")])
	// Write corrupted tmp path entry: count(2) + fileIdx(2) + pathLen(2) + path
	evilName := []byte("../../../etc/evil")
	var buf [6]byte
	binary.BigEndian.PutUint16(buf[0:2], 1) // count
	f.Write(buf[0:2])
	binary.BigEndian.PutUint16(buf[0:2], 0) // fileIdx
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(evilName)))
	f.Write(buf[0:4])
	f.Write(evilName)
	f.Close()

	// Load should reject the traversal path.
	_, err := loadCheckpoint(dir, ck)
	if err == nil {
		t.Fatal("expected error for path traversal in tmp name")
	}
	if !strings.Contains(err.Error(), "path traversal") {
		t.Errorf("expected path traversal error, got: %v", err)
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

func TestCompressionRatioOnProgress(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	p := ts.trackTransfer("test.bin", 1000, "peer1", "send", 10, true)
	p.addWireBytes(200)
	p.addWireBytes(200)

	snap := p.Snapshot()
	if snap.CompressedSize != 400 {
		t.Errorf("compressed_size: got %d, want 400", snap.CompressedSize)
	}
	if !snap.Compressed {
		t.Error("expected Compressed=true")
	}

	// Ratio: 1000 / 400 = 2.5
	ratio := float64(snap.Size) / float64(snap.CompressedSize)
	if ratio < 2.49 || ratio > 2.51 {
		t.Errorf("compression ratio: got %.2f, want 2.50", ratio)
	}

	// JSON includes compressed_size.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"compressed_size":400`) {
		t.Errorf("JSON missing compressed_size: %s", data)
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
		name, input, expected string
	}{
		{"normal", "normal.txt", "normal.txt"},
		{"null bytes", "has\x00null.txt", "hasnull.txt"},
		{"C0 control chars", "control\x01chars\x1f.txt", "controlchars.txt"},
		{"empty", "", ""},
		// Terminal escape injection (OSC 52 clipboard RCE, ANSI clear screen).
		{"ESC byte", "\x1b[2Jevil.txt", "[2Jevil.txt"},
		{"OSC 52 clipboard", "\x1b]52;c;Y3VybA==\afile.txt", "]52;c;Y3VybA==file.txt"},
		// DEL and C1 control chars.
		{"DEL", "file\x7fname.txt", "filename.txt"},
		{"C1 control", "file\u0085name.txt", "filename.txt"},
		{"C1 range", "file\u008Dname.txt", "filename.txt"},
		// Unicode Tags (ASCII smuggling for LLM prompt injection).
		{"Unicode Tag", "report\U000E0041\U000E0042.pdf", "report.pdf"},
		{"Tag cancel", "file\U000E007F.txt", "file.txt"},
		// Zero-width characters (invisible payloads).
		{"ZWSP", "photo\u200B.jpg", "photo.jpg"},
		{"ZWNJ", "file\u200C.txt", "file.txt"},
		{"ZWJ", "file\u200D.txt", "file.txt"},
		{"BOM", "\uFEFFfile.txt", "file.txt"},
		{"Word Joiner", "file\u2060.txt", "file.txt"},
		{"Invisible Times", "file\u2062.txt", "file.txt"},
		{"Invisible Plus", "file\u2064.txt", "file.txt"},
		{"Mongolian VS", "file\u180E.txt", "file.txt"},
		// BiDi control characters (extension spoofing via RLO U+202E).
		{"RLO", "data\u202Etxt.exe", "datatxt.exe"},
		{"LRO", "file\u202D.txt", "file.txt"},
		{"LRE", "file\u202A.txt", "file.txt"},
		{"RLE", "file\u202B.txt", "file.txt"},
		{"PDF", "file\u202C.txt", "file.txt"},
		{"LRI", "file\u2066.txt", "file.txt"},
		{"RLI", "file\u2067.txt", "file.txt"},
		{"FSI", "file\u2068.txt", "file.txt"},
		{"PDI", "file\u2069.txt", "file.txt"},
		{"LRM", "file\u200E.txt", "file.txt"},
		{"RLM", "file\u200F.txt", "file.txt"},
		// Variation selectors (Sneaky Bits binary encoding).
		{"VS1", "file\uFE00.txt", "file.txt"},
		{"VS16", "file\uFE0F.txt", "file.txt"},
		{"SVS", "file\U000E0100.txt", "file.txt"},
		// Legitimate Unicode must pass through unchanged.
		{"Japanese", "\u6771\u4eac.txt", "\u6771\u4eac.txt"},
		{"Arabic", "\u0645\u0644\u0641.txt", "\u0645\u0644\u0641.txt"},
		{"Emoji", "\U0001F600photo.jpg", "\U0001F600photo.jpg"},
		{"Korean", "\uD55C\uAD6D.txt", "\uD55C\uAD6D.txt"},
		{"Accented", "caf\u00E9.txt", "caf\u00E9.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sanitizeFilename(tt.input)
			if got != tt.expected {
				t.Errorf("sanitizeFilename(%q): got %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestSanitizeDisplayName(t *testing.T) {
	// SanitizeDisplayName uses the same isDangerousRune as sanitizeFilename.
	// Test the exported function specifically for CLI display safety.
	tests := []struct {
		name, input, expected string
	}{
		{"clean filename", "report.pdf", "report.pdf"},
		{"ANSI clear screen", "\x1b[2Jevil.txt", "[2Jevil.txt"},
		{"OSC 52 clipboard RCE", "\x1b]52;c;base64cmd\aclean.txt", "]52;c;base64cmdclean.txt"},
		{"Unicode Tags smuggling", "file\U000E0048\U000E0049.pdf", "file.pdf"},
		{"RLO extension spoof", "data\u202Etxt.exe", "datatxt.exe"},
		{"zero-width invisible", "photo\u200B\u200C\u200D.jpg", "photo.jpg"},
		{"Sneaky Bits", "file\u2062\u2064.txt", "file.txt"},
		{"Japanese preserved", "\u6771\u4eac\u30EC\u30DD\u30FC\u30C8.pdf", "\u6771\u4eac\u30EC\u30DD\u30FC\u30C8.pdf"},
		{"emoji preserved", "\U0001F4C4document.txt", "\U0001F4C4document.txt"},
		{"combined attack", "\x1b]52;c;cmd\a\u202E\U000E0041\u200Bfile.txt", "]52;c;cmdfile.txt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeDisplayName(tt.input)
			if got != tt.expected {
				t.Errorf("SanitizeDisplayName(%q): got %q, want %q", tt.input, got, tt.expected)
			}
		})
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
	svc := &sdk.Service{
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
	qID, err := ts.queue.Enqueue(testFile, "peer1", "send", PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
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
	qID, err := ts.queue.Enqueue(testFile, "peer1", "send", PriorityNormal)
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
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
	if err := ts.CancelTransfer(qID); err != nil {
		t.Fatalf("CancelTransfer should succeed for queued item: %v", err)
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
	if err := ts.CancelTransfer(qID); err == nil {
		t.Error("second CancelTransfer should return error")
	}
}

func TestListTransfersIncludesQueued(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	// Add items to queue (without executing).
	ts.queue.Enqueue("/file1", "peer1", "send", PriorityHigh)   //nolint:errcheck
	ts.queue.Enqueue("/file2", "peer2", "send", PriorityNormal) //nolint:errcheck

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

	if err := ts.CancelTransfer(p.ID); err != nil {
		t.Fatalf("CancelTransfer should succeed for active transfer: %v", err)
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

	if err := ts.CancelTransfer(p.ID); err == nil {
		t.Error("CancelTransfer should return error for completed transfer")
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

func TestStreamProgressTracking(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
	}, nil, nil)

	p := ts.trackTransfer("big.bin", 100000, "peer1", "send", 200, true)

	// Initialize 4 streams.
	p.initStreams(4)
	snap := p.Snapshot()
	if len(snap.StreamProgress) != 4 {
		t.Fatalf("stream count: got %d, want 4", len(snap.StreamProgress))
	}
	for i, sp := range snap.StreamProgress {
		if sp.ChunksDone != 0 || sp.BytesDone != 0 {
			t.Errorf("stream %d should be zero initially", i)
		}
	}

	// Deliver chunks to different streams.
	p.updateStream(0, 1024)
	p.updateStream(0, 2048)
	p.updateStream(1, 512)
	p.updateStream(2, 4096)
	p.updateStream(3, 768)
	p.updateStream(3, 256)

	snap = p.Snapshot()
	expected := []StreamInfo{
		{ChunksDone: 2, BytesDone: 3072},
		{ChunksDone: 1, BytesDone: 512},
		{ChunksDone: 1, BytesDone: 4096},
		{ChunksDone: 2, BytesDone: 1024},
	}
	for i, want := range expected {
		got := snap.StreamProgress[i]
		if got.ChunksDone != want.ChunksDone || got.BytesDone != want.BytesDone {
			t.Errorf("stream %d: got {%d, %d}, want {%d, %d}",
				i, got.ChunksDone, got.BytesDone, want.ChunksDone, want.BytesDone)
		}
	}
}

func TestStreamProgressSnapshot(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
	}, nil, nil)

	p := ts.trackTransfer("test.bin", 5000, "peer1", "send", 50, false)

	// No streams - snapshot should have nil/empty StreamProgress.
	snap := p.Snapshot()
	if len(snap.StreamProgress) != 0 {
		t.Fatalf("expected empty stream progress, got %d", len(snap.StreamProgress))
	}

	// Initialize and populate.
	p.initStreams(2)
	p.updateStream(0, 100)
	p.updateStream(1, 200)

	snap = p.Snapshot()
	if len(snap.StreamProgress) != 2 {
		t.Fatalf("stream count: got %d, want 2", len(snap.StreamProgress))
	}

	// Verify snapshot is a copy (modifying original doesn't affect snapshot).
	p.updateStream(0, 300)
	if snap.StreamProgress[0].BytesDone != 100 {
		t.Errorf("snapshot should be independent copy, got %d", snap.StreamProgress[0].BytesDone)
	}

	// JSON includes stream_progress.
	data, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"stream_progress"`) {
		t.Errorf("JSON missing stream_progress: %s", data)
	}
}

func TestStreamProgressDynamicGrow(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{
		ReceiveDir: dir,
	}, nil, nil)

	p := ts.trackTransfer("test.bin", 5000, "peer1", "receive", 50, false)

	// Without initStreams, updateStream should grow dynamically (receive side).
	p.updateStream(0, 100)
	p.updateStream(2, 200) // skip index 1

	snap := p.Snapshot()
	if len(snap.StreamProgress) != 3 {
		t.Fatalf("stream count: got %d, want 3", len(snap.StreamProgress))
	}
	if snap.StreamProgress[0].ChunksDone != 1 || snap.StreamProgress[0].BytesDone != 100 {
		t.Errorf("stream 0: got {%d, %d}, want {1, 100}",
			snap.StreamProgress[0].ChunksDone, snap.StreamProgress[0].BytesDone)
	}
	if snap.StreamProgress[1].ChunksDone != 0 {
		t.Errorf("stream 1 should be zero (skipped)")
	}
	if snap.StreamProgress[2].ChunksDone != 1 || snap.StreamProgress[2].BytesDone != 200 {
		t.Errorf("stream 2: got {%d, %d}, want {1, 200}",
			snap.StreamProgress[2].ChunksDone, snap.StreamProgress[2].BytesDone)
	}
}

// --- DDoS defense tests ---

func TestFailureTracker(t *testing.T) {
	ft := newFailureTracker(3, 5*time.Minute, 1*time.Second)

	// Not blocked initially.
	if ft.isBlocked("peer1") {
		t.Error("peer1 should not be blocked initially")
	}

	// Record 2 failures - not yet at threshold.
	ft.recordFailure("peer1")
	ft.recordFailure("peer1")
	if ft.isBlocked("peer1") {
		t.Error("peer1 should not be blocked after 2 failures")
	}

	// 3rd failure triggers block.
	ft.recordFailure("peer1")
	if !ft.isBlocked("peer1") {
		t.Error("peer1 should be blocked after 3 failures")
	}

	// Different peer should not be affected.
	if ft.isBlocked("peer2") {
		t.Error("peer2 should not be blocked")
	}

	// Wait for block to expire (1 second).
	time.Sleep(1100 * time.Millisecond)
	if ft.isBlocked("peer1") {
		t.Error("peer1 block should have expired")
	}
}

func TestFailureTrackerCleanup(t *testing.T) {
	ft := newFailureTracker(3, 5*time.Minute, 10*time.Millisecond)

	ft.recordFailure("peer1")
	ft.recordFailure("peer1")
	ft.recordFailure("peer1")
	// Now blocked.
	if !ft.isBlocked("peer1") {
		t.Error("expected peer1 blocked")
	}

	time.Sleep(20 * time.Millisecond)
	ft.cleanup()

	// After block expires and cleanup, record should be gone.
	ft.mu.Lock()
	_, exists := ft.peers["peer1"]
	ft.mu.Unlock()
	if exists {
		t.Error("peer1 should have been cleaned up")
	}
}

func TestBandwidthTracker(t *testing.T) {
	bt := newBandwidthTracker(1000) // 1000 bytes/hour

	// First check with small size should pass (0 = use global budget).
	if !bt.check("peer1", 500, 0) {
		t.Error("500 bytes should fit in 1000 budget")
	}

	// Record 500 bytes.
	bt.record("peer1", 500)

	// Another 500 should fit.
	if !bt.check("peer1", 500, 0) {
		t.Error("another 500 should fit (total 1000)")
	}

	bt.record("peer1", 500)

	// Now at budget, 1 more byte should fail.
	if bt.check("peer1", 1, 0) {
		t.Error("should be over budget")
	}

	// Different peer should have full budget.
	if !bt.check("peer2", 999, 0) {
		t.Error("peer2 should have full budget")
	}
}

func TestBandwidthTrackerSingleTransferOverBudget(t *testing.T) {
	bt := newBandwidthTracker(1000)

	// Single transfer larger than budget should fail (0 = use global budget).
	if bt.check("peer1", 1001, 0) {
		t.Error("1001 bytes should not fit in 1000 budget")
	}
}

func TestGlobalRateLimiter(t *testing.T) {
	// Use the existing transferRateLimiter with a single key for global limiting.
	rl := newTransferRateLimiter(3) // 3 per minute

	if !rl.allow("_global_") {
		t.Error("1st should pass")
	}
	if !rl.allow("_global_") {
		t.Error("2nd should pass")
	}
	if !rl.allow("_global_") {
		t.Error("3rd should pass")
	}
	if rl.allow("_global_") {
		t.Error("4th should be rejected (over limit)")
	}
}

func TestCountPeerPending(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Add some pending transfers.
	ts.mu.Lock()
	ts.pending["p1"] = &PendingTransfer{PeerID: "peer-A"}
	ts.pending["p2"] = &PendingTransfer{PeerID: "peer-A"}
	ts.pending["p3"] = &PendingTransfer{PeerID: "peer-B"}
	ts.mu.Unlock()

	ts.mu.RLock()
	countA := ts.countPeerPending("peer-A")
	countB := ts.countPeerPending("peer-B")
	countC := ts.countPeerPending("peer-C")
	ts.mu.RUnlock()

	if countA != 2 {
		t.Errorf("peer-A: got %d, want 2", countA)
	}
	if countB != 1 {
		t.Errorf("peer-B: got %d, want 1", countB)
	}
	if countC != 0 {
		t.Errorf("peer-C: got %d, want 0", countC)
	}
}

func TestCheckTempBudget(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Set a 1 KB temp budget.
	ts.maxTempSize = 1024

	// No temp files - should pass.
	if err := ts.checkTempBudget(); err != nil {
		t.Errorf("empty dir should pass: %v", err)
	}

	// Create a temp file under budget.
	os.WriteFile(filepath.Join(dir, ".shurli-tmp-abc"), make([]byte, 500), 0600)
	if err := ts.checkTempBudget(); err != nil {
		t.Errorf("500 bytes should be under 1024 budget: %v", err)
	}

	// Create another to exceed budget.
	os.WriteFile(filepath.Join(dir, ".shurli-tmp-def"), make([]byte, 600), 0600)
	if err := ts.checkTempBudget(); err == nil {
		t.Error("1100 bytes should exceed 1024 budget")
	}

	// Non-temp files should be ignored.
	os.Remove(filepath.Join(dir, ".shurli-tmp-abc"))
	os.Remove(filepath.Join(dir, ".shurli-tmp-def"))
	os.WriteFile(filepath.Join(dir, "regular-file.bin"), make([]byte, 2000), 0644)
	if err := ts.checkTempBudget(); err != nil {
		t.Errorf("regular files should not count: %v", err)
	}
}

func TestDDoSDefenseDefaults(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir, Compress: true}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Verify defaults are applied.
	if ts.globalRateLimiter == nil {
		t.Error("global rate limiter should be initialized (default 30/min)")
	}
	if ts.failureTracker == nil {
		t.Error("failure tracker should be initialized (default 3/5m/60s)")
	}
	if ts.bandwidthTracker == nil {
		t.Error("bandwidth tracker should be initialized (default 100MB/hour)")
	}
	if ts.maxQueuedPerPeer != 10 {
		t.Errorf("maxQueuedPerPeer: got %d, want 10", ts.maxQueuedPerPeer)
	}
	if ts.minSpeedBytes != 1024 {
		t.Errorf("minSpeedBytes: got %d, want 1024", ts.minSpeedBytes)
	}
	if ts.minSpeedSeconds != 30 {
		t.Errorf("minSpeedSeconds: got %d, want 30", ts.minSpeedSeconds)
	}
	if ts.maxTempSize != 1<<30 {
		t.Errorf("maxTempSize: got %d, want %d", ts.maxTempSize, 1<<30)
	}
}

// --- Queue persistence tests ---

func TestQueuePersistAndLoad(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: hmacKey,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	// Create a test file to queue.
	testFile := filepath.Join(dir, "test.bin")
	os.WriteFile(testFile, []byte("hello"), 0644)

	// Enqueue directly into the transfer queue.
	ts.queue.Enqueue(testFile, "12D3KooWTestPeer123456789012345678901234", "send", PriorityNormal) //nolint:errcheck

	// Persist.
	ts.persistQueue()

	// Verify file exists with 0600 permissions.
	info, err := os.Stat(queueFile)
	if err != nil {
		t.Fatalf("queue file should exist: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("permissions: got %o, want 0600", info.Mode().Perm())
	}

	// Load in a new service.
	ts2, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: hmacKey,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts2.Close()

	entries := ts2.loadPersistedQueue()
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].FilePath != testFile {
		t.Errorf("file path: got %q, want %q", entries[0].FilePath, testFile)
	}
	if entries[0].PeerID != "12D3KooWTestPeer123456789012345678901234" {
		t.Errorf("peer ID mismatch")
	}
}

func TestQueuePersistHMACTamperDetection(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: hmacKey,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	testFile := filepath.Join(dir, "test.bin")
	os.WriteFile(testFile, []byte("hello"), 0644)
	ts.queue.Enqueue(testFile, "12D3KooWTestPeer123456789012345678901234", "send", PriorityNormal) //nolint:errcheck
	ts.persistQueue()

	// Tamper with the file.
	data, _ := os.ReadFile(queueFile)
	tampered := bytes.Replace(data, []byte("test.bin"), []byte("evil.bin"), 1)
	os.WriteFile(queueFile, tampered, 0600)

	// Load should reject tampered file.
	entries := ts.loadPersistedQueue()
	if len(entries) != 0 {
		t.Error("tampered queue file should be rejected")
	}
}

func TestQueuePersistWrongKey(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")

	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: []byte("key-one-32-bytes-long!!!!!!!!!!!"),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	testFile := filepath.Join(dir, "test.bin")
	os.WriteFile(testFile, []byte("hello"), 0644)
	ts.queue.Enqueue(testFile, "12D3KooWTestPeer123456789012345678901234", "send", PriorityNormal) //nolint:errcheck
	ts.persistQueue()

	// Load with different key.
	ts2, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: []byte("key-two-32-bytes-long!!!!!!!!!!!"),
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts2.Close()

	entries := ts2.loadPersistedQueue()
	if len(entries) != 0 {
		t.Error("wrong HMAC key should reject queue file")
	}
}

func TestQueuePersistExpiredEntries(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	testFile := filepath.Join(dir, "test.bin")
	os.WriteFile(testFile, []byte("hello"), 0644)

	// Write a queue file with an expired entry directly.
	entry := persistedQueueEntry{
		ID:       "q-old",
		FilePath: testFile,
		PeerID:   "12D3KooWTestPeer123456789012345678901234",
		Priority: PriorityNormal,
		QueuedAt: time.Now().Add(-25 * time.Hour), // expired (>24h)
		Nonce:    "abc123",
	}
	entriesJSON, _ := json.Marshal([]persistedQueueEntry{entry})
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(entriesJSON)
	macSum := hex.EncodeToString(mac.Sum(nil))

	pq := persistedQueue{
		Version: queueFileVersion,
		HMAC:    macSum,
		Entries: []persistedQueueEntry{entry},
	}
	data, _ := json.MarshalIndent(pq, "", "  ")
	os.WriteFile(queueFile, data, 0600)

	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: hmacKey,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	entries := ts.loadPersistedQueue()
	if len(entries) != 0 {
		t.Error("expired entry should be filtered out")
	}
}

func TestQueuePersistMissingFile(t *testing.T) {
	dir := t.TempDir()
	queueFile := filepath.Join(dir, "queue.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	// Write a queue file with a valid entry but the file doesn't exist.
	entry := persistedQueueEntry{
		ID:       "q-missing",
		FilePath: filepath.Join(dir, "nonexistent.bin"),
		PeerID:   "12D3KooWTestPeer123456789012345678901234",
		Priority: PriorityNormal,
		QueuedAt: time.Now(),
		Nonce:    "abc123",
	}
	entriesJSON, _ := json.Marshal([]persistedQueueEntry{entry})
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(entriesJSON)
	macSum := hex.EncodeToString(mac.Sum(nil))

	pq := persistedQueue{
		Version: queueFileVersion,
		HMAC:    macSum,
		Entries: []persistedQueueEntry{entry},
	}
	data, _ := json.MarshalIndent(pq, "", "  ")
	os.WriteFile(queueFile, data, 0600)

	ts, err := NewTransferService(TransferConfig{
		ReceiveDir:   dir,
		Compress:     true,
		QueueFile:    queueFile,
		QueueHMACKey: hmacKey,
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	entries := ts.loadPersistedQueue()
	if len(entries) != 0 {
		t.Error("entry with missing file should be filtered out")
	}
}

func TestQueuePersistNoKeyDisabled(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{
		ReceiveDir: dir,
		Compress:   true,
		QueueFile:  filepath.Join(dir, "queue.json"),
		// No HMAC key - persistence should be disabled.
	}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ts.Close()

	ts.persistQueue()

	// File should not be created.
	if _, err := os.Stat(filepath.Join(dir, "queue.json")); err == nil {
		t.Error("queue file should not be created without HMAC key")
	}
}

func TestEmptyFileManifest(t *testing.T) {
	// Verify that a 0-byte file produces a valid manifest with 0 chunks.
	dir := t.TempDir()
	emptyFile := filepath.Join(dir, "empty.txt")
	os.WriteFile(emptyFile, nil, 0644)

	f, err := os.Open(emptyFile)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	info, _ := f.Stat()
	if info.Size() != 0 {
		t.Fatalf("expected 0 bytes, got %d", info.Size())
	}

	// Chunk the empty file.
	var chunks []Chunk
	err = ChunkReader(f, 0, func(c Chunk) error {
		chunks = append(chunks, c)
		return nil
	})
	if err != nil {
		t.Fatalf("ChunkReader on empty file: %v", err)
	}

	if len(chunks) != 0 {
		t.Errorf("empty file should produce 0 chunks, got %d", len(chunks))
	}

	// Merkle root of 0 hashes.
	root := sdk.MerkleRoot(nil)
	if root != [32]byte{} {
		t.Error("Merkle root of empty should be zero hash")
	}

	// Bitfield with 0 bits.
	bf := newBitfield(0)
	if bf.count() != 0 {
		t.Error("empty bitfield count should be 0")
	}
	if bf.missing() != 0 {
		t.Error("empty bitfield missing should be 0")
	}
}

func TestQueueBackpressure(t *testing.T) {
	q := NewTransferQueue(2)

	// Fill the queue to maxPending (1000), spreading across peers
	// to avoid hitting per-peer limit (100) first.
	for i := 0; i < q.maxPending; i++ {
		peer := fmt.Sprintf("peer-%d", i%20) // 20 peers, 50 each
		_, err := q.Enqueue("/file", peer, "send", PriorityNormal)
		if err != nil {
			t.Fatalf("enqueue %d failed unexpectedly: %v", i, err)
		}
	}

	// Next enqueue should fail with global limit.
	_, err := q.Enqueue("/overflow", "peer-new", "send", PriorityNormal)
	if err == nil {
		t.Fatal("expected ErrQueueFull when queue is at capacity")
	}
	if err != ErrQueueFull {
		t.Fatalf("expected ErrQueueFull, got: %v", err)
	}

	// After dequeue, should be able to enqueue again.
	q.Dequeue()
	_, err = q.Enqueue("/after-drain", "peer-new", "send", PriorityNormal)
	if err != nil {
		t.Fatalf("enqueue after drain should succeed: %v", err)
	}
}

func TestQueueConcurrent(t *testing.T) {
	q := NewTransferQueue(5)

	var wg sync.WaitGroup
	errCount := int64(0)
	successCount := int64(0)

	// 50 goroutines each enqueue 10 items.
	for g := 0; g < 50; g++ {
		wg.Add(1)
		go func(peer string) {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				_, err := q.Enqueue("/file", peer, "send", PriorityNormal)
				if err != nil {
					atomic.AddInt64(&errCount, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(fmt.Sprintf("peer-%d", g))
	}

	// Concurrently dequeue.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			qt := q.Dequeue()
			if qt != nil {
				q.Complete(qt.ID)
			}
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()

	total := successCount + errCount
	if total != 500 {
		t.Errorf("expected 500 total operations, got %d", total)
	}

	// Some should succeed, some may fail (queue full at 1000).
	if successCount == 0 {
		t.Error("expected at least some successful enqueues")
	}

	// Verify queue integrity: pending + active should be consistent.
	q.mu.Lock()
	pendingCount := len(q.pending)
	activeCount := len(q.active)
	q.mu.Unlock()

	t.Logf("concurrent test: %d success, %d full, pending=%d, active=%d",
		successCount, errCount, pendingCount, activeCount)
}

func TestQueuePerPeerLimit(t *testing.T) {
	q := NewTransferQueue(2)

	// Fill per-peer limit (100) for peer-A.
	for i := 0; i < q.maxPerPeer; i++ {
		_, err := q.Enqueue("/file", "peer-A", "send", PriorityNormal)
		if err != nil {
			t.Fatalf("enqueue %d for peer-A failed: %v", i, err)
		}
	}

	// Next enqueue for peer-A should fail with ErrPeerQueueFull.
	_, err := q.Enqueue("/overflow", "peer-A", "send", PriorityNormal)
	if err != ErrPeerQueueFull {
		t.Fatalf("expected ErrPeerQueueFull for peer-A, got: %v", err)
	}

	// peer-B should still be able to enqueue.
	_, err = q.Enqueue("/file", "peer-B", "send", PriorityNormal)
	if err != nil {
		t.Fatalf("peer-B enqueue should succeed: %v", err)
	}
}

func TestIsRetryableReject(t *testing.T) {
	tests := []struct {
		err  string
		want bool
	}{
		{"peer rejected transfer: receiver busy", true},
		{"peer rejected transfer: insufficient disk space", false},
		{"peer rejected transfer: file too large", false},
		{"peer rejected transfer", false},
		{"open stream: connection reset", false},
	}
	for _, tt := range tests {
		got := isRetryableReject(fmt.Errorf("%s", tt.err))
		if got != tt.want {
			t.Errorf("isRetryableReject(%q) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
