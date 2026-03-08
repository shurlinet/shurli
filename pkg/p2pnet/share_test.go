package p2pnet

import (
	"os"
	"path/filepath"
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
