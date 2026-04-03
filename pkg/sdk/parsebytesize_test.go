package sdk

import (
	"testing"
)

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input   string
		want    int64
		wantErr bool
	}{
		// Valid sizes.
		{"100", 100, false},
		{"100B", 100, false},
		{"1KB", 1024, false},
		{"1K", 1024, false},
		{"1MB", 1 << 20, false},
		{"1M", 1 << 20, false},
		{"500MB", 500 * (1 << 20), false},
		{"1GB", 1 << 30, false},
		{"1G", 1 << 30, false},
		{"1TB", 1 << 40, false},
		{"1T", 1 << 40, false},
		{"0", 0, false},
		{"0MB", 0, false},

		// Case insensitive.
		{"1kb", 1024, false},
		{"1mb", 1 << 20, false},
		{"1gb", 1 << 30, false},
		{"500Mb", 500 * (1 << 20), false},

		// Whitespace handling.
		{"  1GB  ", 1 << 30, false},
		{"1 GB", 1 << 30, false},

		// Unlimited.
		{"unlimited", -1, false},
		{"UNLIMITED", -1, false},
		{"Unlimited", -1, false},

		// Errors.
		{"", 0, true},          // empty
		{"GB", 0, true},        // no number
		{"abc", 0, true},       // no number
		{"-5MB", 0, true},      // negative (dash not in digit set)
		{"1.5GB", 0, true},     // dot not in digit set, "1" parsed, ".5GB" is unknown suffix
		{"1XB", 0, true},       // unknown suffix
		{"1 2 MB", 0, true},    // multiple numbers
	}

	for _, tt := range tests {
		got, err := ParseByteSize(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("ParseByteSize(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseByteSize_Overflow(t *testing.T) {
	// int64 max is ~9.2 exabytes. 9999999TB would overflow.
	huge := "9999999TB"
	_, err := ParseByteSize(huge)
	if err == nil {
		t.Errorf("ParseByteSize(%q) should overflow, got no error", huge)
	}
}

