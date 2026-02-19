package auth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddPeer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	if err := AddPeer(path, pid, "test node"); err != nil {
		t.Fatalf("AddPeer: %v", err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), pid) {
		t.Error("file should contain peer ID")
	}
	if !strings.Contains(string(data), "# test node") {
		t.Error("file should contain comment")
	}
}

func TestAddPeerDuplicate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	if err := AddPeer(path, pid, ""); err != nil {
		t.Fatal(err)
	}

	err := AddPeer(path, pid, "")
	if err == nil {
		t.Error("expected error for duplicate peer")
	}
	if !strings.Contains(err.Error(), "already authorized") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAddPeerInvalidID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	if err := AddPeer(path, "not-valid", ""); err == nil {
		t.Error("expected error for invalid peer ID")
	}
}

func TestAddPeerSanitizesComment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	// Comment with newline injection attempt
	if err := AddPeer(path, pid, "legit\nmalicious-peer-id # injected"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 1 {
		t.Errorf("expected 1 line (newline stripped), got %d lines", len(lines))
	}
}

func TestRemovePeer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	AddPeer(path, pid1, "node-1")
	AddPeer(path, pid2, "node-2")

	if err := RemovePeer(path, pid1); err != nil {
		t.Fatalf("RemovePeer: %v", err)
	}

	entries, _ := ListPeers(path)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry after removal, got %d", len(entries))
	}
	if entries[0].Comment != "node-2" {
		t.Errorf("wrong peer remained: %q", entries[0].Comment)
	}
}

func TestRemovePeerNotFound(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	AddPeer(path, pid1, "")

	err := RemovePeer(path, pid2)
	if err == nil {
		t.Error("expected error for peer not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestRemovePeerInvalidID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	if err := RemovePeer(path, "garbage"); err == nil {
		t.Error("expected error for invalid peer ID")
	}
}

func TestListPeers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	AddPeer(path, pid1, "alpha")
	AddPeer(path, pid2, "beta")

	entries, err := ListPeers(path)
	if err != nil {
		t.Fatalf("ListPeers: %v", err)
	}

	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
}

func TestListPeersMissingFile(t *testing.T) {
	entries, err := ListPeers("/nonexistent/authorized_keys")
	if err != nil {
		t.Fatalf("missing file should return nil, got error: %v", err)
	}
	if entries != nil {
		t.Errorf("expected nil entries for missing file, got %d", len(entries))
	}
}

func TestListPeersPreservesComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid := genPeerIDStr(t)
	content := "# Header comment\n" + pid + "  # my node\n"
	os.WriteFile(path, []byte(content), 0600)

	entries, err := ListPeers(path)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Comment != "my node" {
		t.Errorf("comment = %q, want %q", entries[0].Comment, "my node")
	}
}

func TestRemovePeerPreservesInvalidLines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	// File contains a valid peer and an invalid line
	content := "not-a-valid-peer-id\n" + pid1 + "  # target\n"
	os.WriteFile(path, []byte(content), 0600)

	if err := RemovePeer(path, pid1); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "not-a-valid-peer-id") {
		t.Error("invalid peer ID line should be preserved")
	}
	if strings.Contains(string(data), "target") {
		t.Error("removed peer should not be in file")
	}
}

func TestRemovePeerMissingFile(t *testing.T) {
	pid := genPeerIDStr(t)
	err := RemovePeer("/nonexistent/authorized_keys", pid)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestRemovePeerPreservesFileComments(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "authorized_keys")

	pid1 := genPeerIDStr(t)
	pid2 := genPeerIDStr(t)
	content := "# Authorized peers\n" + pid1 + "  # remove-me\n" + pid2 + "  # keep-me\n"
	os.WriteFile(path, []byte(content), 0600)

	if err := RemovePeer(path, pid1); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# Authorized peers") {
		t.Error("header comment should be preserved")
	}
	if strings.Contains(string(data), "remove-me") {
		t.Error("removed peer should not be in file")
	}
	if !strings.Contains(string(data), "keep-me") {
		t.Error("other peer should be preserved")
	}
}
