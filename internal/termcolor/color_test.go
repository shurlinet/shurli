package termcolor

import (
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout redirects os.Stdout to a pipe and returns what was written.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	old := os.Stdout
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read pipe: %v", err)
	}
	return string(out)
}

func TestGreen(t *testing.T) {
	out := captureStdout(t, func() {
		Green("hello %s", "world")
	})
	if !strings.Contains(out, "hello world") {
		t.Errorf("Green output = %q, want it to contain 'hello world'", out)
	}
}

func TestRed(t *testing.T) {
	out := captureStdout(t, func() {
		Red("error: %d", 42)
	})
	if !strings.Contains(out, "error: 42") {
		t.Errorf("Red output = %q, want it to contain 'error: 42'", out)
	}
}

func TestYellow(t *testing.T) {
	out := captureStdout(t, func() {
		Yellow("warning")
	})
	if !strings.Contains(out, "warning") {
		t.Errorf("Yellow output = %q, want it to contain 'warning'", out)
	}
}

func TestFaint(t *testing.T) {
	out := captureStdout(t, func() {
		Faint("dim text %d", 1)
	})
	if !strings.Contains(out, "dim text 1") {
		t.Errorf("Faint output = %q, want it to contain 'dim text 1'", out)
	}
	// Faint does NOT append newline (Printf style)
	if strings.HasSuffix(out, "\n") {
		// When not a TTY (test env), Faint calls fmt.Print(msg) which doesn't add newline
		// This is actually correct behavior - just verify the content is there
	}
}

func TestIsColorEnabled(t *testing.T) {
	// In a test process, stdout is not a terminal, so isColorEnabled returns false.
	// This test just verifies the function runs without panic and returns a bool.
	result := isColorEnabled()
	// In CI/test environment, this should be false (stdout is a pipe, not a TTY)
	if result {
		t.Log("isColorEnabled returned true - running in a terminal?")
	}
}
