package notify

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// DesktopSink sends OS-native desktop notifications.
// macOS: osascript "display notification". Linux: notify-send.
// Headless environments (no DISPLAY on Linux) are auto-detected and silently skipped.
type DesktopSink struct {
	// execCommand is the function used to create exec.Cmd.
	// Defaults to exec.Command. Override in tests.
	execCommand func(name string, arg ...string) *exec.Cmd
}

// NewDesktopSink creates a DesktopSink. Returns nil if the platform
// is unsupported or the environment is headless (no notification tool available).
func NewDesktopSink() *DesktopSink {
	s := &DesktopSink{execCommand: exec.Command}
	if !s.isAvailable() {
		return nil
	}
	return s
}

func (s *DesktopSink) Name() string { return "desktop" }

func (s *DesktopSink) Notify(event Event) error {
	title := "Shurli"
	body := event.Message
	if event.PeerName != "" {
		body = event.PeerName + ": " + body
	} else if event.PeerID != "" {
		short := event.PeerID
		if len(short) > 16 {
			short = short[:16] + "..."
		}
		body = short + ": " + body
	}

	switch runtime.GOOS {
	case "darwin":
		return s.notifyDarwin(title, body)
	case "linux":
		return s.notifyLinux(title, body)
	default:
		return fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func (s *DesktopSink) notifyDarwin(title, body string) error {
	escaped := sanitizeAppleScript(body)
	script := fmt.Sprintf(`display notification "%s" with title "%s"`, escaped, title)
	cmd := s.execCommand("osascript", "-e", script)
	return cmd.Run()
}

// sanitizeAppleScript escapes a string for safe embedding in an AppleScript
// double-quoted literal. Handles backslashes, quotes, and control characters
// that would break the string boundary or corrupt the script.
func sanitizeAppleScript(s string) string {
	// 1. Escape backslashes first (before anything else).
	s = strings.ReplaceAll(s, `\`, `\\`)
	// 2. Escape double quotes.
	s = strings.ReplaceAll(s, `"`, `\"`)
	// 3. Replace newlines with spaces, drop control chars except tab.
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\r' {
			b.WriteRune(' ')
		} else if r >= 0x20 || r == '\t' {
			b.WriteRune(r)
		}
		// Drop other control chars (0x00-0x1F except \t).
	}
	return b.String()
}

func (s *DesktopSink) notifyLinux(title, body string) error {
	cmd := s.execCommand("notify-send", title, body)
	return cmd.Run()
}

// isAvailable returns true if this platform can show desktop notifications.
func (s *DesktopSink) isAvailable() bool {
	switch runtime.GOOS {
	case "darwin":
		_, err := exec.LookPath("osascript")
		return err == nil
	case "linux":
		// Headless check: no DISPLAY means no desktop session to notify.
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return false
		}
		_, err := exec.LookPath("notify-send")
		return err == nil
	default:
		return false
	}
}
