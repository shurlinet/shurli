package validate

import (
	"strings"
	"unicode"
)

// PasswordStrength represents the assessed strength of a password.
type PasswordStrength int

const (
	PasswordWeak   PasswordStrength = iota // Rejected: too simple
	PasswordFair                           // Acceptable minimum
	PasswordStrong                         // Good complexity
)

// String returns the display label for the strength level.
func (s PasswordStrength) String() string {
	switch s {
	case PasswordWeak:
		return "weak"
	case PasswordFair:
		return "fair"
	case PasswordStrong:
		return "strong"
	default:
		return "unknown"
	}
}

// MinPasswordLen is the minimum acceptable password length.
const MinPasswordLen = 8

// commonPasswords is a small blocklist of the most frequently used passwords.
// Checked case-insensitively.
var commonPasswords = map[string]bool{
	"password":    true,
	"12345678":    true,
	"123456789":   true,
	"1234567890":  true,
	"qwerty123":   true,
	"qwertyuiop":  true,
	"password1":   true,
	"password123": true,
	"iloveyou":    true,
	"letmein":     true,
	"welcome":     true,
	"monkey123":   true,
	"dragon123":   true,
	"master123":   true,
	"abc12345":    true,
	"abcdefgh":    true,
	"trustno1":    true,
	"changeme":    true,
	"admin123":    true,
	"passw0rd":    true,
}

// CheckPasswordStrength evaluates a password and returns its strength level.
// Passwords shorter than MinPasswordLen or in the common blocklist are always Weak.
// Strength is based on character class diversity (uppercase, lowercase, digit, symbol):
//   - 4 classes -> Strong
//   - 3 classes -> Fair (or Strong if len >= 16)
//   - 2 or fewer classes -> Weak
func CheckPasswordStrength(password string) PasswordStrength {
	if len(password) < MinPasswordLen {
		return PasswordWeak
	}

	if commonPasswords[strings.ToLower(password)] {
		return PasswordWeak
	}

	var hasUpper, hasLower, hasDigit, hasSymbol bool
	for _, r := range password {
		switch {
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsDigit(r):
			hasDigit = true
		default:
			hasSymbol = true
		}
	}

	classes := 0
	if hasUpper {
		classes++
	}
	if hasLower {
		classes++
	}
	if hasDigit {
		classes++
	}
	if hasSymbol {
		classes++
	}

	switch {
	case classes >= 4:
		return PasswordStrong
	case classes >= 3:
		if len(password) >= 16 {
			return PasswordStrong
		}
		return PasswordFair
	default:
		return PasswordWeak
	}
}

// PasswordAcceptable returns true if the password meets minimum requirements.
func PasswordAcceptable(password string) bool {
	return CheckPasswordStrength(password) >= PasswordFair
}
