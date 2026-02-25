package qr

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	q, err := New("hello", Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if q == nil {
		t.Fatal("New returned nil")
	}
	if q.Content != "hello" {
		t.Errorf("Content = %q, want %q", q.Content, "hello")
	}
	if q.Level != Medium {
		t.Errorf("Level = %d, want %d", q.Level, Medium)
	}
	if q.VersionNumber < 1 || q.VersionNumber > 40 {
		t.Errorf("VersionNumber = %d, want 1-40", q.VersionNumber)
	}
}

func TestNewAllLevels(t *testing.T) {
	levels := []struct {
		name  string
		level RecoveryLevel
	}{
		{"Low", Low},
		{"Medium", Medium},
		{"High", High},
		{"Highest", Highest},
	}

	for _, tt := range levels {
		t.Run(tt.name, func(t *testing.T) {
			q, err := New("test content", tt.level)
			if err != nil {
				t.Fatalf("New with %s: %v", tt.name, err)
			}
			if q == nil {
				t.Fatalf("New with %s returned nil", tt.name)
			}
		})
	}
}

func TestNewLongContent(t *testing.T) {
	// URL-length content (typical invite code use case)
	content := "https://shurli.dev/join/ABCD-EFGH-IJKL-MNOP-QRST-UVWX-YZ12-3456-7890"
	q, err := New(content, Medium)
	if err != nil {
		t.Fatalf("New long content: %v", err)
	}
	if q.Content != content {
		t.Errorf("Content mismatch")
	}
}

func TestNewEmptyContent(t *testing.T) {
	// Empty content is expected to fail - no data to encode
	_, err := New("", Medium)
	if err == nil {
		t.Error("expected error for empty content")
	}
}

func TestBitmap(t *testing.T) {
	q, err := New("hello", Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	bm := q.Bitmap()
	if len(bm) == 0 {
		t.Fatal("Bitmap returned empty array")
	}
	// QR codes are square
	if len(bm) != len(bm[0]) {
		t.Errorf("Bitmap not square: %dx%d", len(bm), len(bm[0]))
	}
	// Minimum QR code is version 1 = 21 modules + 8 border = 29
	if len(bm) < 21 {
		t.Errorf("Bitmap too small: %d, expected >= 21", len(bm))
	}
}

func TestBitmapWithoutBorder(t *testing.T) {
	q, err := New("hello", Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	q.DisableBorder = true

	bm := q.Bitmap()
	if len(bm) == 0 {
		t.Fatal("Bitmap returned empty array")
	}

	// Without border, should be smaller than with border
	q2, _ := New("hello", Medium)
	bm2 := q2.Bitmap()
	if len(bm) >= len(bm2) {
		t.Errorf("borderless bitmap (%d) should be smaller than bordered (%d)", len(bm), len(bm2))
	}
}

func TestToSmallString(t *testing.T) {
	q, err := New("hello", Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	s := q.ToSmallString(false)
	if s == "" {
		t.Fatal("ToSmallString returned empty string")
	}
	// Should contain block characters
	if !strings.ContainsAny(s, "█▀▄ ") {
		t.Error("ToSmallString should contain block characters")
	}
	// Should have multiple lines
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) < 5 {
		t.Errorf("ToSmallString produced %d lines, expected >= 5", len(lines))
	}
}

func TestToSmallStringInverse(t *testing.T) {
	q, err := New("hello", Medium)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	normal := q.ToSmallString(false)
	inverse := q.ToSmallString(true)

	if normal == inverse {
		t.Error("normal and inverse output should differ")
	}
	if inverse == "" {
		t.Fatal("inverse output should not be empty")
	}
}

func TestNewNumericContent(t *testing.T) {
	// Numeric content uses a more efficient encoding mode
	q, err := New("12345678901234567890", Medium)
	if err != nil {
		t.Fatalf("New numeric: %v", err)
	}
	if q.VersionNumber < 1 {
		t.Errorf("VersionNumber = %d", q.VersionNumber)
	}
}

func TestNewAlphanumericContent(t *testing.T) {
	// Alphanumeric content (uppercase + digits + some symbols)
	q, err := New("HELLO WORLD 123", Medium)
	if err != nil {
		t.Fatalf("New alphanumeric: %v", err)
	}
	if q.VersionNumber < 1 {
		t.Errorf("VersionNumber = %d", q.VersionNumber)
	}
}
