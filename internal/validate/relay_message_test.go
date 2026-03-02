package validate

import (
	"strings"
	"testing"
)

func TestSanitizeRelayMessage_URL(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"Check https://example.com for details", "Check [link removed] for details"},
		{"Visit http://foo.bar/baz?q=1", "Visit [link removed]"},
		{"See www.example.com", "See [link removed]"},
		{"Use ftp://files.example.com/f", "Use [link removed]"},
		{"Custom ://evil.example.com/x", "Custom [link removed]"},
	}
	for _, tt := range tests {
		got := SanitizeRelayMessage(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeRelayMessage(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestSanitizeRelayMessage_Email(t *testing.T) {
	input := "Contact admin@relay.example.com for help"
	got := SanitizeRelayMessage(input)
	want := "Contact [email removed] for help"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeRelayMessage_NonASCII(t *testing.T) {
	input := "Hello \u4e16\u754c world \u00e9"
	got := SanitizeRelayMessage(input)
	want := "Hello world"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeRelayMessage_ControlChars(t *testing.T) {
	input := "Line1\nLine2\tTab\x00Null"
	got := SanitizeRelayMessage(input)
	want := "Line1 Line2 TabNull"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeRelayMessage_Truncation(t *testing.T) {
	input := strings.Repeat("a", 500)
	got := SanitizeRelayMessage(input)
	if len(got) != MaxRelayMessageLen {
		t.Errorf("len = %d, want %d", len(got), MaxRelayMessageLen)
	}
}

func TestSanitizeRelayMessage_Empty(t *testing.T) {
	got := SanitizeRelayMessage("")
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSanitizeRelayMessage_CollapseSpaces(t *testing.T) {
	input := "Hello    world   test"
	got := SanitizeRelayMessage(input)
	want := "Hello world test"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSanitizeRelayMessage_AllowedChars(t *testing.T) {
	input := "Relay shutting down @ 3pm (UTC). Migration to new hardware - ETA: 2h. Questions? Ask #support."
	got := SanitizeRelayMessage(input)
	if got != input {
		t.Errorf("got %q, want %q", got, input)
	}
}
