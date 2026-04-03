package filetransfer

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// TransferNotifier sends notifications when incoming file transfers arrive.
// Three modes: "none" (default), "desktop" (OS-native), "command" (user template).
type TransferNotifier struct {
	mu      sync.RWMutex
	mode    string // "none", "desktop", "command"
	command string // template for command mode (placeholders: {from}, {file}, {size})
}

// NewTransferNotifier creates a notifier with the given mode and optional command template.
func NewTransferNotifier(mode, command string) *TransferNotifier {
	if mode == "" {
		mode = "none"
	}
	return &TransferNotifier{
		mode:    mode,
		command: command,
	}
}

// SetMode changes the notification mode at runtime (for hot-reload).
func (n *TransferNotifier) SetMode(mode string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if mode == "" {
		mode = "none"
	}
	n.mode = mode
}

// SetCommand changes the command template at runtime (for hot-reload).
func (n *TransferNotifier) SetCommand(cmd string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.command = cmd
}

// Notify sends a notification about an incoming file transfer.
// Returns nil if mode is "none" or if headless detection skips desktop mode.
func (n *TransferNotifier) Notify(from, fileName string, fileSize int64) error {
	n.mu.RLock()
	mode := n.mode
	command := n.command
	n.mu.RUnlock()

	switch mode {
	case "none", "":
		return nil
	case "desktop":
		return n.notifyDesktop(from, fileName, fileSize)
	case "command":
		return n.notifyCommand(command, from, fileName, fileSize)
	default:
		slog.Warn("transfer-notify: unknown mode, skipping", "mode", mode)
		return nil
	}
}

// isHeadless returns true if the environment looks like a headless/SSH session
// where desktop notifications would fail silently.
func isHeadless() bool {
	// SSH session - no local display.
	if os.Getenv("SSH_CONNECTION") != "" {
		return true
	}
	// No X11 or Wayland display on Linux.
	if runtime.GOOS == "linux" {
		if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
			return true
		}
	}
	return false
}

// notifyDesktop sends an OS-native desktop notification.
// macOS: osascript. Linux: notify-send. Headless: silently skipped.
func (n *TransferNotifier) notifyDesktop(from, fileName string, fileSize int64) error {
	if isHeadless() {
		slog.Debug("transfer-notify: headless environment, skipping desktop notification")
		return nil
	}

	title := "Shurli"
	body := fmt.Sprintf("Incoming file from %s: %s (%s)", shortPeerID(from), fileName, humanSize(fileSize))

	switch runtime.GOOS {
	case "darwin":
		script := fmt.Sprintf(`display notification %q with title %q`, body, title)
		cmd := exec.Command("osascript", "-e", script)
		if err := cmd.Run(); err != nil {
			slog.Debug("transfer-notify: osascript failed", "error", err)
			return fmt.Errorf("osascript: %w", err)
		}
	case "linux":
		cmd := exec.Command("notify-send", title, body)
		if err := cmd.Run(); err != nil {
			slog.Debug("transfer-notify: notify-send failed", "error", err)
			return fmt.Errorf("notify-send: %w", err)
		}
	default:
		slog.Debug("transfer-notify: desktop notifications not supported on this OS", "os", runtime.GOOS)
	}

	return nil
}

// notifyCommand executes a user-provided command template.
// Placeholders: {from} = peer ID, {file} = filename, {size} = file size in bytes.
// All placeholder values are shell-escaped to prevent command injection from
// attacker-controlled filenames (e.g., filenames containing $(), backticks, ;, |).
func (n *TransferNotifier) notifyCommand(template, from, fileName string, fileSize int64) error {
	if template == "" {
		return nil
	}

	expanded := template
	expanded = strings.ReplaceAll(expanded, "{from}", shellEscape(from))
	expanded = strings.ReplaceAll(expanded, "{file}", shellEscape(fileName))
	expanded = strings.ReplaceAll(expanded, "{size}", strconv.FormatInt(fileSize, 10))

	cmd := exec.Command("sh", "-c", expanded)
	if err := cmd.Run(); err != nil {
		slog.Debug("transfer-notify: command failed", "cmd", expanded, "error", err)
		return fmt.Errorf("notify command: %w", err)
	}

	return nil
}

// shellEscape wraps a string in single quotes for safe shell interpolation.
// Any embedded single quotes are escaped as '\'' (end quote, escaped quote, start quote).
// This prevents injection via $(), backticks, semicolons, pipes, etc.
func shellEscape(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// shortPeerID returns the first 16 chars of a peer ID + "..." for display.
func shortPeerID(id string) string {
	if len(id) > 16 {
		return id[:16] + "..."
	}
	return id
}

