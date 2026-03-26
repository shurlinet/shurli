package validate

import (
	"strings"
	"unicode/utf8"
)

// MaxDisplayLen is the default maximum length for peer-controlled strings
// displayed in the terminal. Prevents oversized strings from flooding output.
const MaxDisplayLen = 512

// SanitizeForDisplay strips dangerous characters from a peer-controlled string
// before displaying it in a terminal. Removes:
//   - C0 control characters (U+0000-U+001F) including ESC (terminal injection)
//   - DEL and C1 control characters (U+007F-U+009F)
//   - Zero-width and invisible formatting characters
//   - BiDi overrides (extension spoofing via RLO U+202E)
//   - Unicode Tags block (ASCII smuggling for LLM prompt injection)
//   - Variation selectors (Sneaky Bits binary encoding)
//
// Safe Unicode (Japanese, Arabic, emoji, accented Latin) passes through.
// Truncates to MaxDisplayLen bytes. Returns empty string for invalid UTF-8.
func SanitizeForDisplay(s string) string {
	if !utf8.ValidString(s) {
		return ""
	}
	if len(s) > MaxDisplayLen {
		s = truncateUTF8(s, MaxDisplayLen)
	}

	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isUnsafeDisplayRune(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isUnsafeDisplayRune returns true for characters that should never appear in
// terminal output from peer-controlled data. Mirrors isDangerousRune in
// pkg/p2pnet/transfer.go but lives in validate/ for use across all layers.
func isUnsafeDisplayRune(r rune) bool {
	// C0 control chars (U+0000-U+001F) - includes ESC (0x1B), NUL, BEL, etc.
	if r <= 0x1F {
		return true
	}
	// DEL and C1 control chars (U+007F-U+009F).
	if r >= 0x7F && r <= 0x9F {
		return true
	}
	// Zero-width and invisible formatting characters.
	switch r {
	case 0x200B, // Zero Width Space
		0x200C, // Zero Width Non-Joiner
		0x200D, // Zero Width Joiner
		0x200E, // Left-to-Right Mark
		0x200F, // Right-to-Left Mark
		0x2060, // Word Joiner
		0x2062, // Invisible Times
		0x2064, // Invisible Plus
		0xFEFF, // Zero Width No-Break Space / BOM
		0x180E: // Mongolian Vowel Separator
		return true
	}
	// BiDi control characters.
	if r >= 0x202A && r <= 0x202E {
		return true
	}
	if r >= 0x2066 && r <= 0x2069 {
		return true
	}
	// Unicode Tags block (LLM prompt injection).
	if r >= 0xE0000 && r <= 0xE007F {
		return true
	}
	// Variation selectors.
	if r >= 0xFE00 && r <= 0xFE0F {
		return true
	}
	if r >= 0xE0100 && r <= 0xE01EF {
		return true
	}
	return false
}

// truncateUTF8 truncates s to at most maxBytes without splitting a multi-byte rune.
func truncateUTF8(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	for maxBytes > 0 && !utf8.RuneStart(s[maxBytes]) {
		maxBytes--
	}
	return s[:maxBytes]
}
