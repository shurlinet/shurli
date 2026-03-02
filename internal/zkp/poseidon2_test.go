package zkp

import (
	"bytes"
	"crypto/rand"
	"testing"

	"github.com/consensys/gnark-crypto/ecc"
	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/consensys/gnark/backend"
	"github.com/consensys/gnark/frontend"
	"github.com/consensys/gnark/test"
)

func TestNativePoseidon2Deterministic(t *testing.T) {
	var a, b fr.Element
	a.SetUint64(42)
	b.SetUint64(99)

	h1 := NativePoseidon2(a, b)
	h2 := NativePoseidon2(a, b)

	if !bytes.Equal(h1, h2) {
		t.Fatal("NativePoseidon2 is not deterministic")
	}
	if len(h1) != 32 {
		t.Fatalf("expected 32-byte hash, got %d", len(h1))
	}
}

func TestNativePoseidon2DifferentInputsDifferentHash(t *testing.T) {
	var a, b, c fr.Element
	a.SetUint64(1)
	b.SetUint64(2)
	c.SetUint64(3)

	h1 := NativePoseidon2(a, b)
	h2 := NativePoseidon2(a, c)

	if bytes.Equal(h1, h2) {
		t.Fatal("different inputs produced the same hash")
	}
}

func TestNativePoseidon2PairDeterministic(t *testing.T) {
	var a, b fr.Element
	a.SetUint64(10)
	b.SetUint64(20)

	left := a.Marshal()
	right := b.Marshal()

	h1, err := NativePoseidon2Pair(left, right)
	if err != nil {
		t.Fatalf("NativePoseidon2Pair error: %v", err)
	}
	h2, err := NativePoseidon2Pair(left, right)
	if err != nil {
		t.Fatalf("NativePoseidon2Pair error: %v", err)
	}

	if !bytes.Equal(h1, h2) {
		t.Fatal("NativePoseidon2Pair is not deterministic")
	}
	if len(h1) != 32 {
		t.Fatalf("expected 32-byte hash, got %d", len(h1))
	}
}

func TestNativePoseidon2PairNotCommutative(t *testing.T) {
	var a, b fr.Element
	a.SetUint64(10)
	b.SetUint64(20)

	h1, err := NativePoseidon2Pair(a.Marshal(), b.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	h2, err := NativePoseidon2Pair(b.Marshal(), a.Marshal())
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(h1, h2) {
		t.Fatal("Compress should not be commutative (feed-forward breaks symmetry)")
	}
}

func TestRoleEncoding(t *testing.T) {
	tests := []struct {
		role     string
		expected uint64
	}{
		{"admin", 1},
		{"member", 2},
		{"", 2},
		{"unknown", 2},
	}

	for _, tc := range tests {
		got := roleEncoding(tc.role)
		if got != tc.expected {
			t.Errorf("roleEncoding(%q) = %d, want %d", tc.role, got, tc.expected)
		}
	}
}

func TestPubKeyToFieldElements(t *testing.T) {
	var pubkey [32]byte
	for i := range pubkey {
		pubkey[i] = byte(i)
	}

	elems := PubKeyToFieldElements(pubkey, "admin", 75)
	if len(elems) != 34 {
		t.Fatalf("expected 34 elements, got %d", len(elems))
	}

	// Verify each byte maps correctly.
	for i := 0; i < 32; i++ {
		var expected fr.Element
		expected.SetUint64(uint64(i))
		if !elems[i].Equal(&expected) {
			t.Errorf("element %d: got %s, want %d", i, elems[i].String(), i)
		}
	}

	// Role encoding.
	var adminRole fr.Element
	adminRole.SetUint64(1)
	if !elems[32].Equal(&adminRole) {
		t.Errorf("role element: got %s, want 1 (admin)", elems[32].String())
	}

	// Score.
	var scoreElem fr.Element
	scoreElem.SetUint64(75)
	if !elems[33].Equal(&scoreElem) {
		t.Errorf("score element: got %s, want 75", elems[33].String())
	}
}

func TestComputeLeafHashDeterministic(t *testing.T) {
	var pubkey [32]byte
	rand.Read(pubkey[:])

	h1 := ComputeLeafHash(pubkey, "member", 50)
	h2 := ComputeLeafHash(pubkey, "member", 50)

	if !bytes.Equal(h1, h2) {
		t.Fatal("ComputeLeafHash is not deterministic")
	}
}

func TestComputeLeafHashRoleSensitive(t *testing.T) {
	var pubkey [32]byte
	rand.Read(pubkey[:])

	admin := ComputeLeafHash(pubkey, "admin", 50)
	member := ComputeLeafHash(pubkey, "member", 50)

	if bytes.Equal(admin, member) {
		t.Fatal("same pubkey with different roles should produce different leaf hashes")
	}
}

func TestComputeLeafHashPubKeySensitive(t *testing.T) {
	var pubkey1, pubkey2 [32]byte
	rand.Read(pubkey1[:])
	rand.Read(pubkey2[:])

	h1 := ComputeLeafHash(pubkey1, "member", 50)
	h2 := ComputeLeafHash(pubkey2, "member", 50)

	if bytes.Equal(h1, h2) {
		t.Fatal("different pubkeys should produce different leaf hashes")
	}
}

// consistencyCircuit verifies that the native and circuit Poseidon2 MD hashers
// produce the same result for the same inputs.
type consistencyCircuit struct {
	Inputs   [3]frontend.Variable
	Expected frontend.Variable `gnark:",public"`
}

func (c *consistencyCircuit) Define(api frontend.API) error {
	h, err := CircuitPoseidon2(api, c.Inputs[0], c.Inputs[1], c.Inputs[2])
	if err != nil {
		return err
	}
	api.AssertIsEqual(h, c.Expected)
	return nil
}

func TestCircuitPoseidon2MatchesNative(t *testing.T) {
	// Compute native hash.
	var a, b, c fr.Element
	a.SetUint64(7)
	b.SetUint64(13)
	c.SetUint64(42)

	nativeHash := NativePoseidon2(a, b, c)

	// Convert native hash to field element for circuit comparison.
	var expected fr.Element
	expected.SetBytes(nativeHash)

	circuit := &consistencyCircuit{}
	assignment := &consistencyCircuit{
		Inputs:   [3]frontend.Variable{7, 13, 42},
		Expected: expected,
	}

	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err != nil {
		t.Fatalf("circuit does not match native hash: %v", err)
	}
}

// pairConsistencyCircuit verifies that native and circuit Compress produce the same result.
type pairConsistencyCircuit struct {
	Left     frontend.Variable
	Right    frontend.Variable
	Expected frontend.Variable `gnark:",public"`
}

func (c *pairConsistencyCircuit) Define(api frontend.API) error {
	h, err := CircuitPoseidon2Pair(api, c.Left, c.Right)
	if err != nil {
		return err
	}
	api.AssertIsEqual(h, c.Expected)
	return nil
}

func TestCircuitPoseidon2PairMatchesNative(t *testing.T) {
	var a, b fr.Element
	a.SetUint64(100)
	b.SetUint64(200)

	nativeHash, err := NativePoseidon2Pair(a.Marshal(), b.Marshal())
	if err != nil {
		t.Fatalf("native pair hash failed: %v", err)
	}

	var expected fr.Element
	expected.SetBytes(nativeHash)

	circuit := &pairConsistencyCircuit{}
	assignment := &pairConsistencyCircuit{
		Left:     100,
		Right:    200,
		Expected: expected,
	}

	if err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField()); err != nil {
		t.Fatalf("circuit pair does not match native: %v", err)
	}
}

// leafConsistencyCircuit verifies that the native leaf hash matches the circuit
// computation for a 34-element input (32 pubkey bytes + role encoding + score).
type leafConsistencyCircuit struct {
	PubKeyBytes [32]frontend.Variable
	RoleEnc     frontend.Variable
	Score       frontend.Variable
	Expected    frontend.Variable `gnark:",public"`
}

func (c *leafConsistencyCircuit) Define(api frontend.API) error {
	inputs := make([]frontend.Variable, 34)
	for i := 0; i < 32; i++ {
		inputs[i] = c.PubKeyBytes[i]
	}
	inputs[32] = c.RoleEnc
	inputs[33] = c.Score
	h, err := CircuitPoseidon2(api, inputs...)
	if err != nil {
		return err
	}
	api.AssertIsEqual(h, c.Expected)
	return nil
}

func TestLeafHashCircuitMatchesNative(t *testing.T) {
	var pubkey [32]byte
	// Use deterministic test key.
	for i := range pubkey {
		pubkey[i] = byte(i * 7)
	}

	nativeHash := ComputeLeafHash(pubkey, "admin", 80)

	var expected fr.Element
	expected.SetBytes(nativeHash)

	circuit := &leafConsistencyCircuit{}
	assignment := &leafConsistencyCircuit{
		RoleEnc:  1,  // admin
		Score:    80,
		Expected: expected,
	}
	for i := 0; i < 32; i++ {
		assignment.PubKeyBytes[i] = uint64(pubkey[i])
	}

	if err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField()); err != nil {
		t.Fatalf("leaf circuit does not match native hash: %v", err)
	}
}

func TestCircuitPoseidon2WrongInputFails(t *testing.T) {
	var a, b, c fr.Element
	a.SetUint64(7)
	b.SetUint64(13)
	c.SetUint64(42)

	nativeHash := NativePoseidon2(a, b, c)
	var expected fr.Element
	expected.SetBytes(nativeHash)

	circuit := &consistencyCircuit{}
	// Provide wrong inputs (99 instead of 42).
	assignment := &consistencyCircuit{
		Inputs:   [3]frontend.Variable{7, 13, 99},
		Expected: expected,
	}

	err := test.IsSolved(circuit, assignment, ecc.BN254.ScalarField())
	if err == nil {
		t.Fatal("circuit should reject wrong inputs")
	}
}

func BenchmarkNativePoseidon2_34Elements(b *testing.B) {
	var pubkey [32]byte
	rand.Read(pubkey[:])
	elems := PubKeyToFieldElements(pubkey, "member", 50)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		NativePoseidon2(elems...)
	}
}

func BenchmarkNativePoseidon2Pair(b *testing.B) {
	var a, c fr.Element
	a.SetUint64(1)
	c.SetUint64(2)
	left := a.Marshal()
	right := c.Marshal()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nativePerm.Compress(left, right)
	}
}

// Ensure the test helpers satisfy the gnark circuit interface.
var (
	_ frontend.Circuit = (*consistencyCircuit)(nil)
	_ frontend.Circuit = (*pairConsistencyCircuit)(nil)
	_ frontend.Circuit = (*leafConsistencyCircuit)(nil)
)

// Suppress unused import warnings for test infrastructure.
var _ = backend.PLONK
