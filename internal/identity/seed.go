package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io"
	"strings"

	libp2pcrypto "github.com/libp2p/go-libp2p/core/crypto"
	"golang.org/x/crypto/hkdf"
)

const (
	// SeedWordCount is the number of BIP39 words in a seed phrase.
	SeedWordCount = 24
	// SeedEntropyLen is the byte length of BIP39 entropy (256 bits).
	SeedEntropyLen = 32
)

// HKDF domain separators. Different domains produce cryptographically
// independent keys from the same seed, like Bitcoin HD wallet derivation paths.
const (
	hkdfDomainIdentity  = "shurli/identity/v1"
	hkdfDomainVault     = "shurli/vault/v1"
	hkdfDomainNamespace = "shurli/identity/v1/ns:" // + namespace name
)

// bip39WordIndex maps words to their BIP39 index for validation.
var bip39WordIndex map[string]int

func init() {
	bip39WordIndex = make(map[string]int, 2048)
	for i, w := range Bip39Wordlist {
		bip39WordIndex[w] = i
	}
}

// GenerateSeed generates a new 24-word BIP39 mnemonic and the raw entropy bytes.
// The mnemonic is the human-readable backup. The entropy bytes are used for
// key derivation via HKDF.
func GenerateSeed() (mnemonic string, entropy []byte, err error) {
	entropy = make([]byte, SeedEntropyLen)
	if _, err := rand.Read(entropy); err != nil {
		return "", nil, fmt.Errorf("generating entropy: %w", err)
	}

	mnemonic, err = EntropyToMnemonic(entropy)
	if err != nil {
		return "", nil, err
	}

	return mnemonic, entropy, nil
}

// SeedFromMnemonic converts a BIP39 mnemonic back to its raw entropy bytes.
// Validates the checksum before returning.
func SeedFromMnemonic(mnemonic string) ([]byte, error) {
	if err := ValidateMnemonic(mnemonic); err != nil {
		return nil, err
	}

	words := strings.Fields(mnemonic)
	var bits [264]bool
	for i, word := range words {
		idx := bip39WordIndex[strings.ToLower(word)]
		for b := 0; b < 11; b++ {
			bits[i*11+b] = (idx>>(10-b))&1 == 1
		}
	}

	// Extract the 256-bit entropy (discard 8-bit checksum).
	entropy := bitsToBytes(bits[:256])
	return entropy, nil
}

// DeriveIdentityKey derives an Ed25519 private key from seed entropy using
// HKDF with the identity domain separator. Same seed always produces the
// same peer ID.
func DeriveIdentityKey(entropy []byte) (libp2pcrypto.PrivKey, error) {
	if len(entropy) != SeedEntropyLen {
		return nil, fmt.Errorf("entropy must be %d bytes, got %d", SeedEntropyLen, len(entropy))
	}

	// HKDF-SHA256: extract + expand with domain separator.
	hkdfReader := hkdf.New(sha256.New, entropy, nil, []byte(hkdfDomainIdentity))

	// Ed25519 seed is 32 bytes.
	seed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(hkdfReader, seed); err != nil {
		return nil, fmt.Errorf("HKDF expand: %w", err)
	}

	// Create Ed25519 key from deterministic seed.
	stdKey := ed25519.NewKeyFromSeed(seed)

	// Wrap as libp2p key.
	privKey, _, err := libp2pcrypto.KeyPairFromStdKey(&stdKey)
	if err != nil {
		return nil, fmt.Errorf("converting to libp2p key: %w", err)
	}

	// Zero the intermediate seed.
	zeroBytes(seed)

	return privKey, nil
}

// DeriveNamespaceKey derives a per-network ephemeral Ed25519 key from the
// master private key and a namespace string. This prevents cross-network peer
// ID correlation: the same node appears as a different peer on each namespace.
//
// Derivation: HKDF-SHA256(masterKeySeed, info="shurli/identity/v1/ns:<namespace>")
// The master key's 32-byte Ed25519 seed is used as HKDF input keying material.
// Returns nil error and the original key unchanged if namespace is empty
// (global network uses master identity for backward compatibility).
func DeriveNamespaceKey(masterKey libp2pcrypto.PrivKey, namespace string) (libp2pcrypto.PrivKey, error) {
	if namespace == "" {
		return masterKey, nil
	}

	// Extract the 32-byte Ed25519 seed from the master private key.
	raw, err := masterKey.Raw()
	if err != nil {
		return nil, fmt.Errorf("extracting master key bytes: %w", err)
	}
	// libp2p Ed25519 Raw() returns 64 bytes (seed || public). We need the first 32.
	if len(raw) < ed25519.SeedSize {
		return nil, fmt.Errorf("master key too short: %d bytes", len(raw))
	}
	masterSeed := raw[:ed25519.SeedSize]

	// HKDF-SHA256 with namespace-specific domain separator.
	domain := hkdfDomainNamespace + namespace
	hkdfReader := hkdf.New(sha256.New, masterSeed, nil, []byte(domain))

	nsSeed := make([]byte, ed25519.SeedSize)
	if _, err := io.ReadFull(hkdfReader, nsSeed); err != nil {
		zeroBytes(masterSeed)
		return nil, fmt.Errorf("HKDF expand for namespace %q: %w", namespace, err)
	}

	// Zero the extracted master seed.
	zeroBytes(masterSeed)

	// Create Ed25519 key from derived seed.
	stdKey := ed25519.NewKeyFromSeed(nsSeed)
	privKey, _, err := libp2pcrypto.KeyPairFromStdKey(&stdKey)
	if err != nil {
		zeroBytes(nsSeed)
		return nil, fmt.Errorf("converting namespace key: %w", err)
	}

	zeroBytes(nsSeed)
	return privKey, nil
}

// DeriveVaultKey derives a 32-byte vault root key from seed entropy using
// HKDF with the vault domain separator. Cryptographically independent from
// the identity key (different domain separator).
func DeriveVaultKey(entropy []byte) ([]byte, error) {
	if len(entropy) != SeedEntropyLen {
		return nil, fmt.Errorf("entropy must be %d bytes, got %d", SeedEntropyLen, len(entropy))
	}

	hkdfReader := hkdf.New(sha256.New, entropy, nil, []byte(hkdfDomainVault))

	vaultKey := make([]byte, 32)
	if _, err := io.ReadFull(hkdfReader, vaultKey); err != nil {
		return nil, fmt.Errorf("HKDF expand: %w", err)
	}

	return vaultKey, nil
}

// ValidateMnemonic checks that a mnemonic is a valid 24-word BIP39 phrase
// with correct checksum.
func ValidateMnemonic(mnemonic string) error {
	words := strings.Fields(mnemonic)
	if len(words) != SeedWordCount {
		return fmt.Errorf("expected %d words, got %d", SeedWordCount, len(words))
	}

	// Decode words to 11-bit indices.
	var bits [264]bool
	for i, word := range words {
		idx, ok := bip39WordIndex[strings.ToLower(word)]
		if !ok {
			return fmt.Errorf("word %d (%q) not in BIP39 wordlist", i+1, word)
		}
		for b := 0; b < 11; b++ {
			bits[i*11+b] = (idx>>(10-b))&1 == 1
		}
	}

	// Extract entropy (first 256 bits) and checksum (last 8 bits).
	entropy := bitsToBytes(bits[:256])
	checksumBits := bits[256:]

	// Verify checksum: SHA256(entropy), take first 8 bits.
	hash := sha256.Sum256(entropy)
	for i := 0; i < 8; i++ {
		expected := (hash[0]>>(7-i))&1 == 1
		if checksumBits[i] != expected {
			return fmt.Errorf("checksum mismatch at bit %d", i)
		}
	}

	return nil
}

// EntropyToMnemonic converts 32 bytes of entropy to a 24-word BIP39 mnemonic.
func EntropyToMnemonic(entropy []byte) (string, error) {
	if len(entropy) != SeedEntropyLen {
		return "", fmt.Errorf("entropy must be %d bytes, got %d", SeedEntropyLen, len(entropy))
	}

	// SHA256 checksum.
	hash := sha256.Sum256(entropy)
	checksumByte := hash[0]

	// Build 264 bits: 256 entropy + 8 checksum.
	var bits [264]bool
	for i := 0; i < 256; i++ {
		bits[i] = (entropy[i/8]>>(7-i%8))&1 == 1
	}
	for i := 0; i < 8; i++ {
		bits[256+i] = (checksumByte>>(7-i))&1 == 1
	}

	// Split into 24 groups of 11 bits.
	words := make([]string, SeedWordCount)
	for i := 0; i < SeedWordCount; i++ {
		var idx int
		for b := 0; b < 11; b++ {
			if bits[i*11+b] {
				idx |= 1 << (10 - b)
			}
		}
		words[i] = Bip39Wordlist[idx]
	}

	return strings.Join(words, " "), nil
}

// SeedFromCustomPassphrase derives 32 bytes of entropy from an arbitrary
// passphrase using SHA-256. This is weaker than BIP39 (no checksum, no typo
// detection, no standard recovery) and should only be used when the user
// explicitly acknowledges the risks.
func SeedFromCustomPassphrase(passphrase string) []byte {
	hash := sha256.Sum256([]byte(passphrase))
	return hash[:]
}

// bitsToBytes converts a bit slice to bytes.
func bitsToBytes(bits []bool) []byte {
	out := make([]byte, len(bits)/8)
	for i, b := range bits {
		if b {
			out[i/8] |= 1 << (7 - i%8)
		}
	}
	return out
}
