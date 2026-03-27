package sdk

import (
	"testing"
)

func TestProtocolID(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    string
	}{
		{"file-transfer", "1.0.0", "/shurli/file-transfer/1.0.0"},
		{"invite", "1.0.0", "/shurli/invite/1.0.0"},
		{"ssh", "1.0.0", "/shurli/ssh/1.0.0"},
		{"presence", "1.0.0", "/shurli/presence/1.0.0"},
		{"zkp-auth", "1.0.0", "/shurli/zkp-auth/1.0.0"},
	}

	for _, tt := range tests {
		got := ProtocolID(tt.name, tt.version)
		if got != tt.want {
			t.Errorf("ProtocolID(%q, %q) = %q, want %q", tt.name, tt.version, got, tt.want)
		}
	}
}

func TestProtocolID_PanicsOnEmpty(t *testing.T) {
	tests := []struct {
		label   string
		name    string
		version string
	}{
		{"empty name", "", "1.0.0"},
		{"empty version", "ssh", ""},
		{"both empty", "", ""},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			defer func() {
				if r := recover(); r == nil {
					t.Errorf("ProtocolID(%q, %q) should have panicked", tt.name, tt.version)
				}
			}()
			ProtocolID(tt.name, tt.version)
		})
	}
}

func TestProtocolID_PanicsOnSlash(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ProtocolID with slash in name should have panicked")
		}
	}()
	ProtocolID("bad/name", "1.0.0")
}

func TestProtocolID_PanicsOnWhitespace(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("ProtocolID with whitespace should have panicked")
		}
	}()
	ProtocolID("bad name", "1.0.0")
}

func TestValidateProtocolID(t *testing.T) {
	valid := []string{
		"/shurli/file-transfer/1.0.0",
		"/shurli/ssh/1.0.0",
		"/shurli/invite/1.0.0",
		"/shurli/zkp-auth/1.0.0",
	}
	for _, id := range valid {
		if err := ValidateProtocolID(id); err != nil {
			t.Errorf("ValidateProtocolID(%q) = %v, want nil", id, err)
		}
	}

	invalid := []string{
		"/ipfs/kad/1.0.0",       // wrong prefix
		"/shurli/",              // missing name+version
		"/shurli/ssh",           // missing version
		"shurli/ssh/1.0.0",     // missing leading slash
		"/shurli/ ssh/1.0.0",   // whitespace in name
		"/shurli/ssh/ 1.0.0",   // whitespace in version
	}
	for _, id := range invalid {
		if err := ValidateProtocolID(id); err == nil {
			t.Errorf("ValidateProtocolID(%q) = nil, want error", id)
		}
	}
}

func TestProtocolID_MatchesDHTPrefix(t *testing.T) {
	// ProtocolPrefix should match DHTProtocolPrefix (both are "/shurli").
	if ProtocolPrefix != DHTProtocolPrefix {
		t.Errorf("ProtocolPrefix = %q, DHTProtocolPrefix = %q, should match", ProtocolPrefix, DHTProtocolPrefix)
	}
}
