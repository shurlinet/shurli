package zkp

import (
	"strings"
	"testing"

	"github.com/shurlinet/shurli/internal/identity"
)

func TestGenerateMnemonic(t *testing.T) {
	m, err := GenerateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(m)
	if len(words) != 24 {
		t.Fatalf("expected 24 words, got %d", len(words))
	}

	// All words must be in the BIP39 wordlist.
	for i, w := range words {
		found := false
		for _, wl := range identity.Bip39Wordlist {
			if wl == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("word %d (%q) not in BIP39 wordlist", i, w)
		}
	}
}

func TestGenerateMnemonic_Unique(t *testing.T) {
	m1, _ := GenerateMnemonic()
	m2, _ := GenerateMnemonic()
	if m1 == m2 {
		t.Fatal("two consecutive mnemonics should not be identical")
	}
}

func TestValidateMnemonic_Valid(t *testing.T) {
	m, err := GenerateMnemonic()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMnemonic(m); err != nil {
		t.Fatalf("valid mnemonic failed validation: %v", err)
	}
}

func TestValidateMnemonic_WrongWordCount(t *testing.T) {
	err := ValidateMnemonic("abandon ability able")
	if err == nil {
		t.Fatal("expected error for 3-word mnemonic")
	}
	if !strings.Contains(err.Error(), "expected 24 words") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMnemonic_InvalidWord(t *testing.T) {
	m, _ := GenerateMnemonic()
	words := strings.Fields(m)
	words[5] = "xyzzyplugh"
	err := ValidateMnemonic(strings.Join(words, " "))
	if err == nil {
		t.Fatal("expected error for invalid word")
	}
	if !strings.Contains(err.Error(), "not in BIP39 wordlist") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidateMnemonic_BadChecksum(t *testing.T) {
	// Deterministic bad-checksum test: 24x "abandon" (index 0) encodes
	// 256 zero-bits of entropy + 8 zero-bits of checksum. The actual
	// checksum of 32 zero-bytes is SHA256(0x00*32)[0] = 0x66, not 0x00.
	// So this mnemonic is guaranteed to fail checksum validation.
	bad := strings.Repeat("abandon ", 24)
	bad = strings.TrimSpace(bad)
	err := ValidateMnemonic(bad)
	if err == nil {
		t.Fatal("expected checksum error for known-bad mnemonic")
	}
}

func TestMnemonicToSeedBytes_Deterministic(t *testing.T) {
	m, _ := GenerateMnemonic()
	s1 := MnemonicToSeedBytes(m)
	s2 := MnemonicToSeedBytes(m)
	if len(s1) != 32 {
		t.Fatalf("expected 32-byte seed, got %d", len(s1))
	}
	for i := range s1 {
		if s1[i] != s2[i] {
			t.Fatal("same mnemonic should produce same seed bytes")
		}
	}
}

func TestMnemonicToSeedBytes_Different(t *testing.T) {
	m1, _ := GenerateMnemonic()
	m2, _ := GenerateMnemonic()
	s1 := MnemonicToSeedBytes(m1)
	s2 := MnemonicToSeedBytes(m2)
	same := true
	for i := range s1 {
		if s1[i] != s2[i] {
			same = false
			break
		}
	}
	if same {
		t.Fatal("different mnemonics should produce different seed bytes")
	}
}

func TestWordlistSize(t *testing.T) {
	if len(identity.Bip39Wordlist) != 2048 {
		t.Fatalf("expected 2048 words, got %d", len(identity.Bip39Wordlist))
	}
}
