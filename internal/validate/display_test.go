package validate

import (
	"strings"
	"testing"
)

func TestSanitizeForDisplay(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"clean ascii", "hello world", "hello world"},
		{"unicode safe", "cafe\u0301", "cafe\u0301"},
		{"emoji", "file \U0001F4E6.txt", "file \U0001F4E6.txt"},
		{"strip ESC", "hello\x1b[31mRED\x1b[0m", "hello[31mRED[0m"},
		{"strip NUL", "hello\x00world", "helloworld"},
		{"strip BEL", "hello\x07world", "helloworld"},
		{"strip tab", "hello\tworld", "helloworld"},
		{"strip newline", "hello\nworld", "helloworld"},
		{"strip CR", "hello\rworld", "helloworld"},
		{"strip BiDi RLO", "file\u202Etxt.exe", "filetxt.exe"},
		{"strip zero-width space", "pass\u200Bword", "password"},
		{"strip unicode tags", "text\U000E0061\U000E0062", "text"},
		{"strip variation selector", "star\uFE0F", "star"},
		{"strip C1 control", "hello\u0080world", "helloworld"},
		{"OSC title attack", "\x1b]0;pwned\x07safe", "]0;pwnedsafe"},
		{"OSC 52 clipboard", "\x1b]52;c;PAYLOAD\x07", "]52;c;PAYLOAD"},
		{"empty", "", ""},
		{"invalid utf8", "\xff\xfe", ""},
		{"truncate long", strings.Repeat("a", 1000), strings.Repeat("a", MaxDisplayLen)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SanitizeForDisplay(tt.input)
			if got != tt.want {
				t.Errorf("SanitizeForDisplay(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
