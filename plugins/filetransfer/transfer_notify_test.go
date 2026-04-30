package filetransfer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTransferNotifier_NoneMode(t *testing.T) {
	n := NewTransferNotifier("none", "")
	if err := n.Notify("QmPeer123456789012345", "test.txt", 1024); err != nil {
		t.Fatalf("none mode should not error: %v", err)
	}
}

func TestTransferNotifier_EmptyMode(t *testing.T) {
	n := NewTransferNotifier("", "")
	if err := n.Notify("QmPeer123456789012345", "test.txt", 1024); err != nil {
		t.Fatalf("empty mode should not error: %v", err)
	}
}

func TestTransferNotifier_UnknownMode(t *testing.T) {
	n := NewTransferNotifier("invalid", "")
	if err := n.Notify("QmPeer123456789012345", "test.txt", 1024); err != nil {
		t.Fatalf("unknown mode should not error: %v", err)
	}
}

func TestTransferNotifier_CommandMode(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "notify-test.txt")

	template := "echo {from} {file} {size} > " + outFile
	n := NewTransferNotifier("command", template)

	if err := n.Notify("QmPeer123456789012345", "hello.txt", 2048); err != nil {
		t.Fatalf("command mode failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}

	output := strings.TrimSpace(string(data))
	if !strings.Contains(output, "QmPeer123456789012345") {
		t.Errorf("expected peer ID in output, got: %s", output)
	}
	if !strings.Contains(output, "hello.txt") {
		t.Errorf("expected filename in output, got: %s", output)
	}
	if !strings.Contains(output, "2048") {
		t.Errorf("expected file size in output, got: %s", output)
	}
}

func TestTransferNotifier_CommandEmptyTemplate(t *testing.T) {
	n := NewTransferNotifier("command", "")
	if err := n.Notify("QmPeer123456789012345", "test.txt", 1024); err != nil {
		t.Fatalf("empty command template should not error: %v", err)
	}
}

func TestTransferNotifier_HeadlessDetection(t *testing.T) {
	// Setting SSH_CONNECTION should make isHeadless() return true.
	t.Setenv("SSH_CONNECTION", "10.0.0.1 12345 10.0.0.2 22")
	if !isHeadless() {
		t.Error("expected headless=true with SSH_CONNECTION set")
	}

	// Clear SSH_CONNECTION.
	t.Setenv("SSH_CONNECTION", "")
	// On macOS (test environment), isHeadless should be false with no SSH.
	// On Linux without DISPLAY/WAYLAND, it would be true. Platform-dependent.
}

func TestTransferNotifier_SetMode(t *testing.T) {
	n := NewTransferNotifier("desktop", "")
	n.SetMode("none")
	// After setting to none, Notify should be a no-op.
	if err := n.Notify("QmPeer123456789012345", "test.txt", 1024); err != nil {
		t.Fatalf("none mode after SetMode should not error: %v", err)
	}
}

func TestTransferNotifier_SetCommand(t *testing.T) {
	dir := t.TempDir()
	outFile := filepath.Join(dir, "set-cmd-test.txt")

	n := NewTransferNotifier("command", "echo original > /dev/null")
	n.SetCommand("echo {file} > " + outFile)

	if err := n.Notify("QmPeer123456789012345", "updated.txt", 512); err != nil {
		t.Fatalf("command mode after SetCommand failed: %v", err)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("output file not created: %v", err)
	}
	if !strings.Contains(string(data), "updated.txt") {
		t.Errorf("expected updated filename in output, got: %s", string(data))
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}
	for _, tc := range tests {
		got := humanSize(tc.bytes)
		if got != tc.want {
			t.Errorf("humanSize(%d) = %q, want %q", tc.bytes, got, tc.want)
		}
	}
}

func TestShortPeerID(t *testing.T) {
	long := "QmPeer12345678901234567890"
	got := shortPeerID(long)
	if got != "QmPeer1234567890..." {
		t.Errorf("shortPeerID(%q) = %q, want QmPeer1234567890...", long, got)
	}

	short := "QmShort"
	got = shortPeerID(short)
	if got != "QmShort" {
		t.Errorf("shortPeerID(%q) = %q, want QmShort", short, got)
	}
}
