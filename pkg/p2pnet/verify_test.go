package p2pnet

import (
	"strings"
	"testing"
)

// genTestPeerID is defined in naming_test.go

func TestComputeFingerprintDeterministic(t *testing.T) {
	a := genTestPeerID(t)
	b := genTestPeerID(t)

	emoji1, num1 := ComputeFingerprint(a, b)
	emoji2, num2 := ComputeFingerprint(a, b)

	if emoji1 != emoji2 {
		t.Error("fingerprint should be deterministic")
	}
	if num1 != num2 {
		t.Error("numeric code should be deterministic")
	}
}

func TestComputeFingerprintSymmetric(t *testing.T) {
	a := genTestPeerID(t)
	b := genTestPeerID(t)

	emoji1, num1 := ComputeFingerprint(a, b)
	emoji2, num2 := ComputeFingerprint(b, a) // reversed order

	if emoji1 != emoji2 {
		t.Error("fingerprint should be the same regardless of order")
	}
	if num1 != num2 {
		t.Error("numeric code should be the same regardless of order")
	}
}

func TestComputeFingerprintUnique(t *testing.T) {
	a := genTestPeerID(t)
	b := genTestPeerID(t)
	c := genTestPeerID(t)

	emoji1, _ := ComputeFingerprint(a, b)
	emoji2, _ := ComputeFingerprint(a, c)

	if emoji1 == emoji2 {
		t.Error("different peer pairs should produce different fingerprints")
	}
}

func TestComputeFingerprintFormat(t *testing.T) {
	a := genTestPeerID(t)
	b := genTestPeerID(t)

	emoji, numeric := ComputeFingerprint(a, b)

	// Emoji should have 4 emoji separated by spaces.
	parts := strings.Split(emoji, " ")
	if len(parts) != 4 {
		t.Errorf("emoji should have 4 parts, got %d: %q", len(parts), emoji)
	}

	// Numeric should be NNN-NNN format.
	if len(numeric) != 7 || numeric[3] != '-' {
		t.Errorf("numeric format wrong: %q", numeric)
	}

	t.Logf("Emoji: %s", emoji)
	t.Logf("Numeric: %s", numeric)
}

func TestFingerprintPrefix(t *testing.T) {
	a := genTestPeerID(t)
	b := genTestPeerID(t)

	prefix := FingerprintPrefix(a, b)

	if !strings.HasPrefix(prefix, "sha256:") {
		t.Errorf("prefix should start with sha256:, got %q", prefix)
	}
	// sha256: + 8 hex chars = 15 total
	if len(prefix) != 15 {
		t.Errorf("prefix length = %d, want 15: %q", len(prefix), prefix)
	}

	// Symmetric.
	prefix2 := FingerprintPrefix(b, a)
	if prefix != prefix2 {
		t.Error("fingerprint prefix should be symmetric")
	}
}

func TestEmojiTableComplete(t *testing.T) {
	// Verify all 256 entries are non-empty.
	for i, e := range emojiTable {
		if e == "" {
			t.Errorf("emojiTable[%d] is empty", i)
		}
	}

	// Check no duplicates.
	seen := make(map[string]int)
	for i, e := range emojiTable {
		if prev, ok := seen[e]; ok {
			t.Errorf("emojiTable[%d] = %s is duplicate of [%d]", i, e, prev)
		}
		seen[e] = i
	}
}
