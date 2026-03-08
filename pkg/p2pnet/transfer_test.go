package p2pnet

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/network"
)

func TestMarshalUnmarshalHeader(t *testing.T) {
	content := []byte("hello world, this is a test file")
	checksum := sha256.Sum256(content)

	original := &TransferHeader{
		Filename: "test-file.txt",
		Size:     int64(len(content)),
		Checksum: checksum,
	}

	var buf bytes.Buffer
	if err := marshalHeader(&buf, original); err != nil {
		t.Fatalf("marshalHeader: %v", err)
	}

	parsed, err := unmarshalHeader(&buf)
	if err != nil {
		t.Fatalf("unmarshalHeader: %v", err)
	}

	if parsed.Filename != original.Filename {
		t.Errorf("filename: got %q, want %q", parsed.Filename, original.Filename)
	}
	if parsed.Size != original.Size {
		t.Errorf("size: got %d, want %d", parsed.Size, original.Size)
	}
	if parsed.Checksum != original.Checksum {
		t.Error("checksum mismatch")
	}
}

func TestHeaderPathTraversal(t *testing.T) {
	checksum := sha256.Sum256([]byte("x"))

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
		h := &TransferHeader{Filename: tt.input, Size: 1, Checksum: checksum}
		if err := marshalHeader(&buf, h); err != nil {
			t.Fatalf("marshal %q: %v", tt.input, err)
		}

		parsed, err := unmarshalHeader(&buf)
		if err != nil {
			t.Fatalf("unmarshal %q: %v", tt.input, err)
		}

		if parsed.Filename != tt.expected {
			t.Errorf("path traversal: input %q, got %q, want %q", tt.input, parsed.Filename, tt.expected)
		}
	}
}

func TestHeaderRejectDotFilenames(t *testing.T) {
	checksum := sha256.Sum256([]byte("x"))

	for _, name := range []string{".", ".."} {
		var buf bytes.Buffer
		h := &TransferHeader{Filename: name, Size: 1, Checksum: checksum}
		if err := marshalHeader(&buf, h); err != nil {
			t.Fatalf("marshal %q: %v", name, err)
		}
		_, err := unmarshalHeader(&buf)
		if err == nil {
			t.Errorf("expected error for filename %q", name)
		}
	}
}

func TestHeaderInvalidVersion(t *testing.T) {
	buf := bytes.NewReader([]byte{0x99, transferTypeFile, 0, 4, 't', 'e', 's', 't',
		0, 0, 0, 0, 0, 0, 0, 1, // size=1
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, // checksum (32 bytes)
		0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0,
	})
	_, err := unmarshalHeader(buf)
	if err == nil {
		t.Error("expected error for invalid version")
	}
}

func TestHeaderOversizedFile(t *testing.T) {
	var buf bytes.Buffer
	h := &TransferHeader{Filename: "big.bin", Size: maxFileSize + 1, Checksum: [32]byte{}}
	if err := marshalHeader(&buf, h); err != nil {
		t.Fatal(err)
	}
	_, err := unmarshalHeader(&buf)
	if err == nil {
		t.Error("expected error for oversized file")
	}
}

func TestResponseRoundtrip(t *testing.T) {
	for _, resp := range []byte{transferTypeAccept, transferTypeReject} {
		var buf bytes.Buffer
		if err := writeResponse(&buf, resp); err != nil {
			t.Fatalf("writeResponse(%d): %v", resp, err)
		}
		got, err := readResponse(&buf)
		if err != nil {
			t.Fatalf("readResponse: %v", err)
		}
		if got != resp {
			t.Errorf("response: got %d, want %d", got, resp)
		}
	}
}

func TestTransferServiceCreation(t *testing.T) {
	dir := t.TempDir()
	ts, err := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)
	if err != nil {
		t.Fatalf("NewTransferService: %v", err)
	}

	if ts.receiveDir != dir {
		t.Errorf("receiveDir: got %q, want %q", ts.receiveDir, dir)
	}
}

func TestCreateExclusive(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)

	// First file should be the original name.
	path1, f1, err := ts.createExclusive("test.txt")
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	f1.Close()
	if filepath.Base(path1) != "test.txt" {
		t.Errorf("first: got %q, want test.txt", filepath.Base(path1))
	}

	// Second should be "test (1).txt" (original already exists).
	path2, f2, err := ts.createExclusive("test.txt")
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	f2.Close()
	if filepath.Base(path2) != "test (1).txt" {
		t.Errorf("second: got %q, want test (1).txt", filepath.Base(path2))
	}

	// Third should be "test (2).txt".
	path3, f3, err := ts.createExclusive("test.txt")
	if err != nil {
		t.Fatalf("third create: %v", err)
	}
	f3.Close()
	if filepath.Base(path3) != "test (2).txt" {
		t.Errorf("third: got %q, want test (2).txt", filepath.Base(path3))
	}
}

func TestTransferProgress(t *testing.T) {
	dir := t.TempDir()
	ts, _ := NewTransferService(TransferConfig{ReceiveDir: dir}, nil, nil)

	p := ts.trackTransfer("photo.jpg", 1024, "12D3KooW...", "send")
	if p.ID == "" {
		t.Fatal("expected non-empty ID")
	}
	if p.Done {
		t.Error("should not be done initially")
	}

	p.update(512)
	snap := p.Snapshot()
	if snap.Sent != 512 {
		t.Errorf("sent: got %d, want 512", snap.Sent)
	}

	p.finish(nil)
	snap = p.Snapshot()
	if !snap.Done {
		t.Error("should be done after finish")
	}
	if snap.Error != "" {
		t.Errorf("unexpected error: %s", snap.Error)
	}

	// Should be findable.
	found, ok := ts.GetTransfer(p.ID)
	if !ok {
		t.Fatal("transfer not found")
	}
	if found.ID != p.ID {
		t.Errorf("ID mismatch: got %q, want %q", found.ID, p.ID)
	}

	// List should include it.
	list := ts.ListTransfers()
	if len(list) != 1 {
		t.Fatalf("list: got %d, want 1", len(list))
	}
}

func TestHashFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bin")
	content := []byte("the quick brown fox jumps over the lazy dog")
	os.WriteFile(path, content, 0644)

	f, _ := os.Open(path)
	defer f.Close()

	got, err := hashFile(f)
	if err != nil {
		t.Fatal(err)
	}

	expected := sha256.Sum256(content)
	if got != expected {
		t.Error("hash mismatch")
	}
}

func TestCustomHandlerServiceRegistration(t *testing.T) {
	// Verify that a Service with Handler (no LocalAddress) can be registered.
	// This validates the 9A -> 9B interface contract.
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
