package notify

import (
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func TestDesktopSink_Name(t *testing.T) {
	s := &DesktopSink{execCommand: exec.Command}
	if s.Name() != "desktop" {
		t.Errorf("Name() = %q, want %q", s.Name(), "desktop")
	}
}

func TestDesktopSink_Notify(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("desktop notifications only on darwin/linux")
	}

	var mu sync.Mutex
	var gotName string
	var gotArgs []string

	s := &DesktopSink{
		execCommand: func(name string, arg ...string) *exec.Cmd {
			mu.Lock()
			gotName = name
			gotArgs = arg
			mu.Unlock()
			// Use "true" as a no-op command that always succeeds.
			return exec.Command("true")
		},
	}

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmTestPeer123456", "alice", "grant created")
	if err := s.Notify(event); err != nil {
		t.Fatalf("Notify() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	switch runtime.GOOS {
	case "darwin":
		if gotName != "osascript" {
			t.Errorf("command = %q, want osascript", gotName)
		}
		if len(gotArgs) != 2 || gotArgs[0] != "-e" {
			t.Errorf("args = %v, want [-e <script>]", gotArgs)
		}
		// Verify the script contains the peer name.
		if len(gotArgs) == 2 && !strings.Contains(gotArgs[1], "alice") {
			t.Errorf("AppleScript should contain peer name 'alice', got: %s", gotArgs[1])
		}
	case "linux":
		if gotName != "notify-send" {
			t.Errorf("command = %q, want notify-send", gotName)
		}
		// Peer name is in the title, message is in the body.
		if len(gotArgs) != 2 || !strings.Contains(gotArgs[0], "alice") {
			t.Errorf("title should contain peer name 'alice', got args: %v", gotArgs)
		}
	}
}

func TestDesktopSink_NotifyWithPeerIDOnly(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("desktop notifications only on darwin/linux")
	}

	var mu sync.Mutex
	var gotArgs []string

	s := &DesktopSink{
		execCommand: func(name string, arg ...string) *exec.Cmd {
			mu.Lock()
			gotArgs = arg
			mu.Unlock()
			return exec.Command("true")
		},
	}

	event := NewEvent(EventGrantExpired, SeverityWarn, "QmVeryLongPeerIDThatShouldBeTruncated", "", "grant expired")
	if err := s.Notify(event); err != nil {
		t.Fatalf("Notify() error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()

	// Truncated peer ID should appear in the title (darwin: in script string, linux: in title arg).
	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "QmVeryLongPeerID") {
		t.Errorf("notification should contain truncated peer ID, got args: %v", gotArgs)
	}
}

func TestSanitizeAppleScript(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`hello`, `hello`},
		{`say "hi"`, `say \"hi\"`},
		{`back\slash`, `back\\slash`},
		{`inject\" & do shell script \"bad`, `inject\\\" & do shell script \\\"bad`},
		{"line1\nline2", "line1 line2"},
		{"tab\there", "tab\there"},
		{"null\x00byte", "nullbyte"},
	}
	for _, tt := range tests {
		got := sanitizeAppleScript(tt.in)
		if got != tt.want {
			t.Errorf("sanitizeAppleScript(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDesktopSink_ExecFailure(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("desktop notifications only on darwin/linux")
	}

	s := &DesktopSink{
		execCommand: func(name string, arg ...string) *exec.Cmd {
			return exec.Command("false") // always exits 1
		},
	}

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "test")
	err := s.Notify(event)
	if err == nil {
		t.Error("expected error from failed exec, got nil")
	}
}
