package p2pnet

import (
	"bytes"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestShareRegistryBasic(t *testing.T) {
	dir := t.TempDir()
	subFile := filepath.Join(dir, "test.txt")
	os.WriteFile(subFile, []byte("hello"), 0644)

	reg := NewShareRegistry()

	// Share with all peers.
	if err := reg.Share(subFile, nil, false); err != nil {
		t.Fatalf("Share: %v", err)
	}

	shares := reg.ListShares(nil)
	if len(shares) != 1 {
		t.Fatalf("expected 1 share, got %d", len(shares))
	}
	if shares[0].Path != subFile {
		t.Errorf("path: got %q, want %q", shares[0].Path, subFile)
	}
	if shares[0].IsDir {
		t.Error("expected file, got dir")
	}

	// Unshare.
	if err := reg.Unshare(subFile); err != nil {
		t.Fatalf("Unshare: %v", err)
	}
	shares = reg.ListShares(nil)
	if len(shares) != 0 {
		t.Fatalf("expected 0 shares after unshare, got %d", len(shares))
	}
}

func TestShareRegistryPeerACL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret.txt")
	os.WriteFile(file, []byte("secret"), 0644)

	reg := NewShareRegistry()

	peerA, _ := peer.Decode("12D3KooWA7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")
	peerB, _ := peer.Decode("12D3KooWB7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")

	// Share only with peerA.
	if err := reg.Share(file, []peer.ID{peerA}, false); err != nil {
		t.Fatalf("Share: %v", err)
	}

	// peerA can see it.
	shares := reg.ListShares(&peerA)
	if len(shares) != 1 {
		t.Errorf("peerA: expected 1 share, got %d", len(shares))
	}

	// peerB cannot.
	shares = reg.ListShares(&peerB)
	if len(shares) != 0 {
		t.Errorf("peerB: expected 0 shares, got %d", len(shares))
	}

	// IsPathShared respects ACL.
	if !reg.IsPathShared(file, peerA) {
		t.Error("peerA should have access")
	}
	if reg.IsPathShared(file, peerB) {
		t.Error("peerB should not have access")
	}
}

func TestShareRegistryDirectoryAccess(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "photos")
	os.MkdirAll(subDir, 0755)
	subFile := filepath.Join(subDir, "vacation.jpg")
	os.WriteFile(subFile, []byte("jpeg data"), 0644)

	reg := NewShareRegistry()

	peerA, _ := peer.Decode("12D3KooWA7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")

	// Share the directory.
	if err := reg.Share(subDir, nil, false); err != nil {
		t.Fatalf("Share: %v", err)
	}

	// File within shared directory should be accessible.
	if !reg.IsPathShared(subFile, peerA) {
		t.Error("file within shared dir should be accessible")
	}

	// File outside shared directory should not.
	outsideFile := filepath.Join(dir, "private.txt")
	if reg.IsPathShared(outsideFile, peerA) {
		t.Error("file outside shared dir should not be accessible")
	}
}

func TestShareRegistryBrowse(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "file1.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(dir, "file2.txt"), []byte("world"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("hidden"), 0644)
	os.MkdirAll(filepath.Join(dir, "subdir"), 0755)

	reg := NewShareRegistry()

	peerA, _ := peer.Decode("12D3KooWA7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")

	if err := reg.Share(dir, nil, false); err != nil {
		t.Fatalf("Share: %v", err)
	}

	entries := reg.BrowseForPeer(peerA)

	// Should have file1, file2, subdir (not .hidden).
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d: %v", len(entries), entries)
	}

	names := make(map[string]bool)
	for _, e := range entries {
		names[e.Name] = true
	}
	if names[".hidden"] {
		t.Error("hidden file should be excluded")
	}
	if !names["file1.txt"] || !names["file2.txt"] || !names["subdir"] {
		t.Errorf("missing expected entries: %v", names)
	}
}

func TestWalkDirectory(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbbbb"), 0644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0755)
	os.WriteFile(filepath.Join(dir, "sub", "c.txt"), []byte("cc"), 0644)
	os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0644)

	dt, err := WalkDirectory(dir)
	if err != nil {
		t.Fatalf("WalkDirectory: %v", err)
	}

	// Should have: sub/, a.txt, b.txt, sub/c.txt (not .hidden).
	if len(dt.Files) != 4 {
		names := make([]string, len(dt.Files))
		for i, f := range dt.Files {
			names[i] = f.RelPath
		}
		t.Fatalf("expected 4 entries, got %d: %v", len(dt.Files), names)
	}

	// Dirs should come first.
	if !dt.Files[0].IsDir {
		t.Error("first entry should be a directory")
	}

	// Total size should be 3+5+2 = 10.
	if dt.TotalSize != 10 {
		t.Errorf("totalSize: got %d, want 10", dt.TotalSize)
	}

	files := dt.RegularFiles()
	if len(files) != 3 {
		t.Errorf("RegularFiles: got %d, want 3", len(files))
	}
}

func TestTransferQueue(t *testing.T) {
	q := NewTransferQueue(2)

	// Enqueue three items with different priorities.
	id1 := q.Enqueue("/file1", "peer1", "send", PriorityNormal)
	id2 := q.Enqueue("/file2", "peer2", "send", PriorityHigh)
	id3 := q.Enqueue("/file3", "peer3", "send", PriorityLow)

	// Dequeue should return highest priority first.
	qt := q.Dequeue()
	if qt == nil || qt.ID != id2 {
		t.Fatalf("expected high priority first, got %v", qt)
	}

	qt = q.Dequeue()
	if qt == nil || qt.ID != id1 {
		t.Fatalf("expected normal priority second, got %v", qt)
	}

	// Max active reached (2), dequeue should return nil.
	qt = q.Dequeue()
	if qt != nil {
		t.Fatal("expected nil when at max active")
	}

	// Complete one, should be able to dequeue again.
	q.Complete(id2)
	qt = q.Dequeue()
	if qt == nil || qt.ID != id3 {
		t.Fatalf("expected low priority third, got %v", qt)
	}

	// Cancel a pending item.
	id4 := q.Enqueue("/file4", "peer4", "send", PriorityNormal)
	if !q.Cancel(id4) {
		t.Fatal("cancel should succeed")
	}
	if q.Cancel(id4) {
		t.Fatal("second cancel should fail")
	}

	_ = id1
	_ = id3
}

func TestTransferQueuePending(t *testing.T) {
	q := NewTransferQueue(5)
	q.Enqueue("/a", "p1", "send", PriorityNormal)
	q.Enqueue("/b", "p2", "send", PriorityHigh)

	pending := q.Pending()
	if len(pending) != 2 {
		t.Fatalf("expected 2 pending, got %d", len(pending))
	}

	// High priority should be first.
	if pending[0].Priority != PriorityHigh {
		t.Error("expected high priority first in pending list")
	}
}

func TestShareNonexistentPath(t *testing.T) {
	reg := NewShareRegistry()
	err := reg.Share("/nonexistent/path/to/file", nil, false)
	if err == nil {
		t.Fatal("expected error for nonexistent path")
	}
}

func TestUnshareNotShared(t *testing.T) {
	reg := NewShareRegistry()
	err := reg.Unshare("/some/path")
	if err == nil {
		t.Fatal("expected error for unsharing non-shared path")
	}
}

func TestSavePersistentRoundtrip(t *testing.T) {
	dir := t.TempDir()
	file1 := filepath.Join(dir, "keep.txt")
	file2 := filepath.Join(dir, "temp.txt")
	os.WriteFile(file1, []byte("persistent"), 0644)
	os.WriteFile(file2, []byte("ephemeral"), 0644)

	persistFile := filepath.Join(dir, "shares.json")

	// Create registry with one persistent and one non-persistent share.
	reg := NewShareRegistry()
	reg.SetPersistPath(persistFile)
	if err := reg.Share(file1, nil, true); err != nil {
		t.Fatalf("Share persistent: %v", err)
	}
	if err := reg.Share(file2, nil, false); err != nil {
		t.Fatalf("Share non-persistent: %v", err)
	}

	// Save explicitly.
	if err := reg.SavePersistent(persistFile); err != nil {
		t.Fatalf("SavePersistent: %v", err)
	}

	// Load into new registry.
	reg2, err := LoadShareRegistry(persistFile)
	if err != nil {
		t.Fatalf("LoadShareRegistry: %v", err)
	}

	shares := reg2.ListShares(nil)
	if len(shares) != 1 {
		t.Fatalf("expected 1 persistent share, got %d", len(shares))
	}
	if shares[0].Path != file1 {
		t.Errorf("path: got %q, want %q", shares[0].Path, file1)
	}
	if !shares[0].Persistent {
		t.Error("loaded share should be marked persistent")
	}
}

func TestSavePersistentWithPeerACL(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "acl.txt")
	os.WriteFile(file, []byte("data"), 0644)

	persistFile := filepath.Join(dir, "shares.json")

	peerA, _ := peer.Decode("12D3KooWA7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")
	peerB, _ := peer.Decode("12D3KooWB7e4tPH7RaBmRYDWNNYPxT5uxHoiT7aBmRPfT5VxmeZZ")

	reg := NewShareRegistry()
	if err := reg.Share(file, []peer.ID{peerA}, true); err != nil {
		t.Fatalf("Share: %v", err)
	}
	if err := reg.SavePersistent(persistFile); err != nil {
		t.Fatalf("SavePersistent: %v", err)
	}

	reg2, err := LoadShareRegistry(persistFile)
	if err != nil {
		t.Fatalf("LoadShareRegistry: %v", err)
	}

	// peerA should have access.
	if !reg2.IsPathShared(file, peerA) {
		t.Error("peerA should have access after reload")
	}
	// peerB should not.
	if reg2.IsPathShared(file, peerB) {
		t.Error("peerB should not have access after reload")
	}
}

func TestLoadShareRegistryMissingFile(t *testing.T) {
	dir := t.TempDir()
	persistFile := filepath.Join(dir, "nonexistent.json")

	reg, err := LoadShareRegistry(persistFile)
	if err != nil {
		t.Fatalf("LoadShareRegistry should not error on missing file: %v", err)
	}
	if len(reg.ListShares(nil)) != 0 {
		t.Error("expected empty registry from missing file")
	}
}

func TestAutoSaveOnShareAndUnshare(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "auto.txt")
	os.WriteFile(file, []byte("auto"), 0644)

	persistFile := filepath.Join(dir, "shares.json")

	reg := NewShareRegistry()
	reg.SetPersistPath(persistFile)

	// Share with persistent=true should auto-save.
	if err := reg.Share(file, nil, true); err != nil {
		t.Fatalf("Share: %v", err)
	}

	// File should exist now.
	if _, err := os.Stat(persistFile); err != nil {
		t.Fatalf("persist file not created after Share: %v", err)
	}

	// Load and verify.
	reg2, err := LoadShareRegistry(persistFile)
	if err != nil {
		t.Fatalf("LoadShareRegistry: %v", err)
	}
	if len(reg2.ListShares(nil)) != 1 {
		t.Fatal("expected 1 share after auto-save")
	}

	// Unshare should auto-save (removing it).
	if err := reg.Unshare(file); err != nil {
		t.Fatalf("Unshare: %v", err)
	}

	reg3, err := LoadShareRegistry(persistFile)
	if err != nil {
		t.Fatalf("LoadShareRegistry after unshare: %v", err)
	}
	if len(reg3.ListShares(nil)) != 0 {
		t.Fatal("expected 0 shares after unshare auto-save")
	}
}

func TestAtomicWrite(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "atomic.txt")
	os.WriteFile(file, []byte("data"), 0644)

	persistFile := filepath.Join(dir, "subdir", "shares.json")

	reg := NewShareRegistry()
	if err := reg.Share(file, nil, true); err != nil {
		t.Fatalf("Share: %v", err)
	}

	// SavePersistent should create subdirectory.
	if err := reg.SavePersistent(persistFile); err != nil {
		t.Fatalf("SavePersistent: %v", err)
	}

	// No .tmp file should remain.
	if _, err := os.Stat(persistFile + ".tmp"); !os.IsNotExist(err) {
		t.Error("tmp file should not exist after successful save")
	}

	// Verify JSON is valid and human-readable.
	data, err := os.ReadFile(persistFile)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("persist file is empty")
	}
	// Should be indented JSON.
	if data[0] != '{' {
		t.Errorf("expected JSON object, got %q", string(data[:1]))
	}
}

func TestDownloadProtocolConstants(t *testing.T) {
	// Verify protocol ID is valid.
	if DownloadProtocol != "/shurli/file-download/1.0.0" {
		t.Errorf("unexpected protocol: %s", DownloadProtocol)
	}
	// Error marker must be distinct from SHFT magic first byte.
	if msgDownloadError != 0xFF {
		t.Errorf("msgDownloadError should be 0xFF, got 0x%02X", msgDownloadError)
	}
}

func TestWriteDownloadError(t *testing.T) {
	var buf bytes.Buffer
	writeDownloadError(&buf, "access denied")

	data := buf.Bytes()
	if len(data) < 3 {
		t.Fatalf("too short: %d bytes", len(data))
	}
	if data[0] != msgDownloadError {
		t.Errorf("first byte: got 0x%02X, want 0xFF", data[0])
	}
	errLen := binary.BigEndian.Uint16(data[1:3])
	if int(errLen) != len("access denied") {
		t.Errorf("error length: got %d, want %d", errLen, len("access denied"))
	}
	if string(data[3:]) != "access denied" {
		t.Errorf("error message: got %q, want %q", string(data[3:]), "access denied")
	}
}

func TestDownloadReadySingleByteReader(t *testing.T) {
	ready := &downloadReady{firstByte: 'S'}

	// PrefixedReader should return the consumed byte followed by the rest.
	rest := bytes.NewReader([]byte("HFT-rest"))
	r := ready.PrefixedReader(rest)

	all, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(all) != "SHFT-rest" {
		t.Errorf("got %q, want %q", string(all), "SHFT-rest")
	}
}

func TestNonCollidingPath(t *testing.T) {
	dir := t.TempDir()

	// First call: no collision.
	path1 := filepath.Join(dir, "file.txt")
	result, err := nonCollidingPath(path1)
	if err != nil {
		t.Fatalf("nonCollidingPath: %v", err)
	}
	if result != path1 {
		t.Errorf("expected %q, got %q", path1, result)
	}

	// Create the file so next call collides.
	os.WriteFile(path1, []byte("data"), 0644)

	result2, err := nonCollidingPath(path1)
	if err != nil {
		t.Fatalf("nonCollidingPath collision: %v", err)
	}
	expected := filepath.Join(dir, "file (1).txt")
	if result2 != expected {
		t.Errorf("expected %q, got %q", expected, result2)
	}

	// Create (1) too.
	os.WriteFile(expected, []byte("data"), 0644)
	result3, err := nonCollidingPath(path1)
	if err != nil {
		t.Fatalf("nonCollidingPath double collision: %v", err)
	}
	expected3 := filepath.Join(dir, "file (2).txt")
	if result3 != expected3 {
		t.Errorf("expected %q, got %q", expected3, result3)
	}
}

func TestCreateTempFileIn(t *testing.T) {
	dir := t.TempDir()

	path, f, err := createTempFileIn(dir, "test.txt")
	if err != nil {
		t.Fatalf("createTempFileIn: %v", err)
	}
	defer f.Close()

	if !strings.HasPrefix(filepath.Base(path), ".shurli-tmp-") {
		t.Errorf("temp file name should start with .shurli-tmp-, got %q", filepath.Base(path))
	}
	if !strings.HasSuffix(path, "-test.txt") {
		t.Errorf("temp file name should end with -test.txt, got %q", filepath.Base(path))
	}
	// File should exist.
	if _, err := os.Stat(path); err != nil {
		t.Errorf("temp file should exist: %v", err)
	}
}
