package validate

import "testing"

func TestCheckPasswordStrength(t *testing.T) {
	tests := []struct {
		name     string
		password string
		want     PasswordStrength
	}{
		{"too short", "Ab1!", PasswordWeak},
		{"common password", "password123", PasswordWeak},
		{"common password case insensitive", "Password123", PasswordWeak},
		{"only lowercase", "abcdefghij", PasswordWeak},
		{"only digits", "1234567890", PasswordWeak},
		{"lower+digit only", "abcdef123", PasswordWeak},
		{"upper+lower+digit", "Abcdef123", PasswordFair},
		{"lower+digit+symbol", "abcdef1!", PasswordFair},
		{"upper+lower+symbol", "Abcdefg!", PasswordFair},
		{"all four classes", "Abcdef1!", PasswordStrong},
		{"three classes long passphrase", "correcthorseBattery9", PasswordStrong},
		{"upper+lower+digit 16+ chars", "Abcdefghij123456", PasswordStrong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CheckPasswordStrength(tt.password)
			if got != tt.want {
				t.Errorf("CheckPasswordStrength(%q) = %s, want %s", tt.password, got, tt.want)
			}
		})
	}
}

func TestPasswordAcceptable(t *testing.T) {
	if PasswordAcceptable("password") {
		t.Error("common password should not be acceptable")
	}
	if PasswordAcceptable("abcdefgh") {
		t.Error("single-class password should not be acceptable")
	}
	if !PasswordAcceptable("Abcdef1!") {
		t.Error("4-class password should be acceptable")
	}
	if !PasswordAcceptable("Abcdef123") {
		t.Error("3-class password should be acceptable")
	}
}

func TestStrengthString(t *testing.T) {
	if PasswordWeak.String() != "weak" {
		t.Errorf("got %q, want weak", PasswordWeak.String())
	}
	if PasswordFair.String() != "fair" {
		t.Errorf("got %q, want fair", PasswordFair.String())
	}
	if PasswordStrong.String() != "strong" {
		t.Errorf("got %q, want strong", PasswordStrong.String())
	}
}
