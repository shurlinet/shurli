package zkp

import (
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr/poseidon2"
	"github.com/consensys/gnark/frontend"
	stdhash "github.com/consensys/gnark/std/hash"
	circuitperm "github.com/consensys/gnark/std/permutation/poseidon2"
)

// MaxTreeDepth is the maximum Merkle tree depth. Supports 2^20 (~1M) peers.
const MaxTreeDepth = 20

// Poseidon2 parameters for BN254: width=2 (compression), 6 full rounds, 50 partial rounds.
const (
	poseidon2Width         = 2
	poseidon2FullRounds    = 6
	poseidon2PartialRounds = 50
)

// nativePerm is a cached Poseidon2 permutation for Merkle internal node hashing.
// Safe for concurrent use (Compress uses only local variables).
var nativePerm = poseidon2.NewPermutation(poseidon2Width, poseidon2FullRounds, poseidon2PartialRounds)

// roleEncoding maps role strings to field-safe integer encodings.
// admin=1, member=2. Zero is reserved (unused/padding leaf).
func roleEncoding(role string) uint64 {
	switch role {
	case "admin":
		return 1
	case "member", "":
		return 2
	default:
		return 2 // unknown roles default to member
	}
}

// NativePoseidon2 computes a Poseidon2 hash over the given field elements
// outside of a ZKP circuit (for leaf hashing). Uses Merkle-Damgard construction
// with initial state = 0. Returns a 32-byte canonical field element.
func NativePoseidon2(inputs ...fr.Element) []byte {
	h := poseidon2.NewMerkleDamgardHasher()
	for i := range inputs {
		b := inputs[i].Marshal()
		h.Write(b)
	}
	return h.Sum(nil)
}

// NativePoseidon2Pair hashes exactly two 32-byte values using the Poseidon2
// compression function (for Merkle internal nodes). Inputs must be canonical
// BN254 field element representations (big-endian, < field modulus).
func NativePoseidon2Pair(left, right []byte) ([]byte, error) {
	return nativePerm.Compress(left, right)
}

// BytesToFieldElement converts a single byte to a BN254 field element.
func BytesToFieldElement(b byte) fr.Element {
	var e fr.Element
	e.SetUint64(uint64(b))
	return e
}

// PubKeyToFieldElements converts a 32-byte Ed25519 public key to 32 field elements
// (one per byte) plus role encoding and score. Returns 34 field elements total.
// This avoids BN254 field overflow (Ed25519 keys are 256 bits, BN254 scalar
// field is ~254 bits). Score is included for binding commitment in Merkle leaves.
func PubKeyToFieldElements(pubkey [32]byte, role string, score int) []fr.Element {
	elems := make([]fr.Element, 34)
	for i := 0; i < 32; i++ {
		elems[i].SetUint64(uint64(pubkey[i]))
	}
	elems[32].SetUint64(roleEncoding(role))
	elems[33].SetUint64(uint64(score))
	return elems
}

// ComputeLeafHash computes the Poseidon2 leaf hash for a peer's public key, role,
// and reputation score. The score is committed in the leaf, binding it to the peer's
// identity. This prevents score inflation: a peer cannot claim a higher score than
// what the relay committed in the tree.
func ComputeLeafHash(pubkey [32]byte, role string, score int) []byte {
	elems := PubKeyToFieldElements(pubkey, role, score)
	return NativePoseidon2(elems...)
}

// CircuitPoseidon2 computes Poseidon2 inside a gnark circuit using Merkle-Damgard
// construction. Uses NewPoseidon2FromParameters for BN254 support (the default
// NewPoseidon2 only supports BLS12-377).
func CircuitPoseidon2(api frontend.API, inputs ...frontend.Variable) (frontend.Variable, error) {
	p, err := circuitperm.NewPoseidon2FromParameters(api, poseidon2Width, poseidon2FullRounds, poseidon2PartialRounds)
	if err != nil {
		return nil, err
	}
	h := stdhash.NewMerkleDamgardHasher(api, p, 0)
	h.Write(inputs...)
	return h.Sum(), nil
}

// CircuitPoseidon2Pair hashes exactly two variables inside a gnark circuit
// using the Poseidon2 compression function (for Merkle internal nodes).
func CircuitPoseidon2Pair(api frontend.API, left, right frontend.Variable) (frontend.Variable, error) {
	p, err := circuitperm.NewPoseidon2FromParameters(api, poseidon2Width, poseidon2FullRounds, poseidon2PartialRounds)
	if err != nil {
		return nil, err
	}
	return p.Compress(left, right), nil
}
