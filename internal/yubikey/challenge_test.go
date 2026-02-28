package yubikey

import (
	"testing"
)

func TestParseHexResponse(t *testing.T) {
	tests := []struct {
		input   string
		wantLen int
		wantErr bool
	}{
		{"abcdef0123456789", 8, false},
		{"AB CD EF 01", 4, false},
		{"ab:cd:ef:01", 4, false},
		{"", 0, true},
		{"xyz", 0, true},
		{"a", 0, true}, // odd length
	}

	for _, tt := range tests {
		b, err := parseHexResponse(tt.input)
		if tt.wantErr {
			if err == nil {
				t.Errorf("parseHexResponse(%q): expected error", tt.input)
			}
			continue
		}
		if err != nil {
			t.Errorf("parseHexResponse(%q): unexpected error: %v", tt.input, err)
			continue
		}
		if len(b) != tt.wantLen {
			t.Errorf("parseHexResponse(%q): len = %d, want %d", tt.input, len(b), tt.wantLen)
		}
	}
}

func TestParseHexResponseValues(t *testing.T) {
	b, err := parseHexResponse("deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0xde || b[1] != 0xad || b[2] != 0xbe || b[3] != 0xef {
		t.Errorf("got %x, want deadbeef", b)
	}
}

func TestYkmanInstalledDoesNotPanic(t *testing.T) {
	// Just verify it doesn't panic - result depends on test environment
	_ = YkmanInstalled()
}

func TestIsAvailableDoesNotPanic(t *testing.T) {
	// Just verify it doesn't panic - result depends on hardware
	_ = IsAvailable()
}

func TestSlotConstants(t *testing.T) {
	if Slot1 != 1 {
		t.Errorf("Slot1 = %d, want 1", Slot1)
	}
	if Slot2 != 2 {
		t.Errorf("Slot2 = %d, want 2", Slot2)
	}
}

func TestErrorMessages(t *testing.T) {
	// Verify error messages are meaningful
	errors := []error{
		ErrYkmanNotFound,
		ErrNoYubikey,
		ErrChallengeTimeout,
		ErrSlotNotConfigured,
	}
	for _, err := range errors {
		if err.Error() == "" {
			t.Error("error should have a non-empty message")
		}
	}
}
