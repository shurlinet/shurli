package validate

import (
	"regexp"
	"strings"
)

// MaxRelayMessageLen is the maximum length of a relay MOTD or goodbye message.
const MaxRelayMessageLen = 280

// urlPattern matches common URL schemes and www. prefixes.
var urlPattern = regexp.MustCompile(`(?i)(https?://|ftp://|://|www\.)\S+`)

// emailPattern matches email-like patterns.
var emailPattern = regexp.MustCompile(`[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}`)

// SanitizeRelayMessage cleans a relay operator message (MOTD or goodbye)
// to prevent abuse: URL stripping, email stripping, non-printable removal,
// and length truncation. Returns the sanitized string.
func SanitizeRelayMessage(msg string) string {
	// Strip URLs.
	msg = urlPattern.ReplaceAllString(msg, "[link removed]")

	// Strip emails.
	msg = emailPattern.ReplaceAllString(msg, "[email removed]")

	// Allow only ASCII printable characters and basic whitespace.
	var b strings.Builder
	b.Grow(len(msg))
	for _, r := range msg {
		if r == '\n' || r == '\r' || r == '\t' {
			b.WriteRune(' ')
		} else if r >= 0x20 && r <= 0x7E {
			b.WriteRune(r)
		}
		// Drop everything else (non-ASCII, control chars).
	}
	msg = b.String()

	// Collapse multiple spaces.
	msg = strings.Join(strings.Fields(msg), " ")

	// Truncate.
	if len(msg) > MaxRelayMessageLen {
		msg = msg[:MaxRelayMessageLen]
	}

	return strings.TrimSpace(msg)
}
