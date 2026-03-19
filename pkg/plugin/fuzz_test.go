package plugin

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// F1: Fuzz plugin config parsing (plugin name validation as proxy).
func FuzzPluginConfigParse(f *testing.F) {
	// Seeds: real configs + attack payloads.
	seeds := []string{
		"filetransfer",
		"wake-on-lan",
		"a",
		"ab",
		"",
		"../../../etc/passwd",
		"\x00\x00\x00",
		string(make([]byte, 200)),
		"a-b-c-d",
		"UPPERCASE",
		"has spaces",
		"has\ttab",
		"has\nnewline",
		"shell;injection",
		"$(command)",
		"`backtick`",
		"-starts-with-dash",
		"ends-with-dash-",
	}

	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Must not panic.
		err := validatePluginName(input)

		// If accepted, verify invariants.
		if err == nil {
			if len(input) == 0 || len(input) > 64 {
				t.Errorf("accepted name with length %d (must be 1-64)", len(input))
			}
			if input[0] == '-' || input[len(input)-1] == '-' {
				t.Error("accepted name starting or ending with hyphen")
			}
			for _, c := range input {
				if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
					t.Errorf("accepted name with invalid char %q", string(c))
				}
			}
			// No path traversal.
			if strings.Contains(input, "..") {
				t.Error("accepted name with path traversal")
			}
			// No null bytes.
			if strings.ContainsRune(input, 0) {
				t.Error("accepted name with null byte")
			}
		}
	})
}

// F2: Fuzz plugin name validation.
func FuzzPluginNameValidation(f *testing.F) {
	f.Add("filetransfer")
	f.Add("")
	f.Add(strings.Repeat("x", 100))
	f.Add("../etc")
	f.Add("\x00")

	f.Fuzz(func(t *testing.T, name string) {
		err := validatePluginName(name)
		if err == nil {
			// Accepted names must match the regex pattern.
			if len(name) < 1 || len(name) > 64 {
				t.Errorf("accepted name length %d out of range", len(name))
			}
		}
	})
}

// F3: Fuzz command name validation.
func FuzzCommandNameValidation(f *testing.F) {
	f.Add("send")
	f.Add("file-transfer")
	f.Add("")
	f.Add(";rm -rf /")
	f.Add("$(whoami)")
	f.Add("`id`")
	f.Add(strings.Repeat("a", 100))

	f.Fuzz(func(t *testing.T, name string) {
		valid := isValidCommandName(name)
		if valid {
			// Must be safe for shell completion.
			for _, c := range name {
				if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
					t.Errorf("accepted unsafe command name char %q in %q", string(c), name)
				}
			}
			if len(name) == 0 || len(name) > 64 {
				t.Errorf("accepted command name with length %d", len(name))
			}
		}
	})
}

// F4: Fuzz protocol name validation.
func FuzzProtocolNameValidation(f *testing.F) {
	f.Add("file-transfer")
	f.Add("ping")
	f.Add("relay-pair")
	f.Add("")
	f.Add(strings.Repeat("x", 100))

	f.Fuzz(func(t *testing.T, name string) {
		err := validateProtocolName(name)
		if err == nil {
			// Reserved names should be caught separately.
			// Validation only checks format, not reservation.
			if len(name) < 1 || len(name) > 64 {
				t.Errorf("accepted protocol name length %d", len(name))
			}
		}
	})
}

// F5: Fuzz bash completion output.
func FuzzBashCompletion(f *testing.F) {
	f.Add("normal-command")
	f.Add("$(whoami)")
	f.Add("`id`")
	f.Add("cmd;rm")
	f.Add("cmd\nnewline")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		if !utf8.ValidString(name) {
			return
		}
		// isValidCommandName must reject anything with shell metacharacters.
		if isValidCommandName(name) {
			// Accepted names must be safe for shell embedding.
			for _, c := range name {
				switch c {
				case '$', '`', ';', '|', '&', '\n', '\r', '\'', '"', '\\', '(', ')', '{', '}':
					t.Errorf("accepted name with shell metachar %q", string(c))
				}
			}
		}
	})
}

// F6: Fuzz fish completion output.
func FuzzFishCompletion(f *testing.F) {
	f.Add("send")
	f.Add("it's")
	f.Add("cmd'injection")
	f.Add("")

	f.Fuzz(func(t *testing.T, name string) {
		if !utf8.ValidString(name) {
			return
		}
		if isValidCommandName(name) {
			// No unescaped single quotes in accepted names.
			if strings.Contains(name, "'") {
				t.Errorf("accepted name with single quote: %q", name)
			}
		}
	})
}

// F7: Fuzz troff escape.
func FuzzTroffEscape(f *testing.F) {
	f.Add("normal description")
	f.Add(".TH MALICIOUS")
	f.Add("\\fBbold\\fR")
	f.Add("line1\nline2")
	f.Add("")

	f.Fuzz(func(t *testing.T, input string) {
		if !utf8.ValidString(input) {
			return
		}
		// escapeTroff must handle any input without panic.
		escaped := escapeTroff(input)
		// No leading dot (troff directive).
		if len(escaped) > 0 && escaped[0] == '.' {
			t.Errorf("escaped output starts with dot: %q", escaped)
		}
		// All backslashes doubled.
		// Count single backslashes (not part of \\).
		for i := 0; i < len(escaped); i++ {
			if escaped[i] == '\\' {
				if i+1 >= len(escaped) || escaped[i+1] != '\\' {
					t.Errorf("unescaped backslash at position %d in %q", i, escaped)
				}
				i++ // skip the doubled backslash
			}
		}
	})
}
