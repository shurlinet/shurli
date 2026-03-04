package identity

import (
	"strings"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGenerateSeed(t *testing.T) {
	mnemonic, entropy, err := GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	if len(entropy) != SeedEntropyLen {
		t.Fatalf("entropy length %d, expected %d", len(entropy), SeedEntropyLen)
	}
	words := strings.Fields(mnemonic)
	if len(words) != SeedWordCount {
		t.Fatalf("word count %d, expected %d", len(words), SeedWordCount)
	}
}

func TestGenerateSeed_Unique(t *testing.T) {
	m1, _, _ := GenerateSeed()
	m2, _, _ := GenerateSeed()
	if m1 == m2 {
		t.Fatal("two consecutive seeds should not be identical")
	}
}

func TestSeedFromMnemonic_RoundTrip(t *testing.T) {
	mnemonic, entropy, err := GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}

	recovered, err := SeedFromMnemonic(mnemonic)
	if err != nil {
		t.Fatalf("SeedFromMnemonic: %v", err)
	}

	if len(recovered) != len(entropy) {
		t.Fatalf("length mismatch: %d vs %d", len(recovered), len(entropy))
	}
	for i := range entropy {
		if recovered[i] != entropy[i] {
			t.Fatalf("byte %d differs: %02x vs %02x", i, recovered[i], entropy[i])
		}
	}
}

func TestDeriveIdentityKey_Deterministic(t *testing.T) {
	_, entropy, err := GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}

	key1, err := DeriveIdentityKey(entropy)
	if err != nil {
		t.Fatal(err)
	}
	key2, err := DeriveIdentityKey(entropy)
	if err != nil {
		t.Fatal(err)
	}

	id1, _ := peer.IDFromPrivateKey(key1)
	id2, _ := peer.IDFromPrivateKey(key2)
	if id1 != id2 {
		t.Fatalf("same entropy produced different peer IDs: %s vs %s", id1, id2)
	}
}

func TestDeriveIdentityKey_DifferentSeeds(t *testing.T) {
	_, e1, _ := GenerateSeed()
	_, e2, _ := GenerateSeed()

	k1, _ := DeriveIdentityKey(e1)
	k2, _ := DeriveIdentityKey(e2)

	id1, _ := peer.IDFromPrivateKey(k1)
	id2, _ := peer.IDFromPrivateKey(k2)
	if id1 == id2 {
		t.Fatal("different seeds produced same peer ID")
	}
}

func TestDeriveVaultKey_Deterministic(t *testing.T) {
	_, entropy, _ := GenerateSeed()

	vk1, err := DeriveVaultKey(entropy)
	if err != nil {
		t.Fatal(err)
	}
	vk2, err := DeriveVaultKey(entropy)
	if err != nil {
		t.Fatal(err)
	}

	if len(vk1) != 32 {
		t.Fatalf("vault key length %d, expected 32", len(vk1))
	}
	for i := range vk1 {
		if vk1[i] != vk2[i] {
			t.Fatal("same entropy produced different vault keys")
		}
	}
}

func TestDeriveKeys_Independence(t *testing.T) {
	_, entropy, _ := GenerateSeed()

	idKey, err := DeriveIdentityKey(entropy)
	if err != nil {
		t.Fatal(err)
	}
	vaultKey, err := DeriveVaultKey(entropy)
	if err != nil {
		t.Fatal(err)
	}

	// Get raw identity key bytes for comparison.
	idRaw, err := idKey.Raw()
	if err != nil {
		t.Fatal(err)
	}

	// Identity key and vault key must be different (different HKDF domains).
	if len(idRaw) >= 32 && len(vaultKey) == 32 {
		same := true
		for i := 0; i < 32; i++ {
			if idRaw[i] != vaultKey[i] {
				same = false
				break
			}
		}
		if same {
			t.Fatal("identity key and vault key should be cryptographically independent")
		}
	}
}

func TestValidateMnemonic_Valid(t *testing.T) {
	m, _, err := GenerateSeed()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateMnemonic(m); err != nil {
		t.Fatalf("valid mnemonic failed: %v", err)
	}
}

func TestValidateMnemonic_WrongWordCount(t *testing.T) {
	err := ValidateMnemonic("abandon ability able")
	if err == nil {
		t.Fatal("expected error for 3-word mnemonic")
	}
}

func TestValidateMnemonic_InvalidWord(t *testing.T) {
	m, _, _ := GenerateSeed()
	words := strings.Fields(m)
	words[5] = "xyzzyplugh"
	err := ValidateMnemonic(strings.Join(words, " "))
	if err == nil {
		t.Fatal("expected error for invalid word")
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

func TestEntropyToMnemonic_KnownVector(t *testing.T) {
	// BIP39 test vector: all-zero entropy.
	entropy := make([]byte, 32)
	m, err := EntropyToMnemonic(entropy)
	if err != nil {
		t.Fatal(err)
	}
	words := strings.Fields(m)
	if words[0] != "abandon" {
		t.Fatalf("expected first word 'abandon', got %q", words[0])
	}
	if err := ValidateMnemonic(m); err != nil {
		t.Fatalf("all-zero mnemonic failed validation: %v", err)
	}
}

func TestWordlistSize(t *testing.T) {
	if len(Bip39Wordlist) != 2048 {
		t.Fatalf("expected 2048 words, got %d", len(Bip39Wordlist))
	}
	if len(bip39WordIndex) != 2048 {
		t.Fatalf("expected 2048 index entries, got %d", len(bip39WordIndex))
	}
}
