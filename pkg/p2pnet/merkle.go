package p2pnet

import "github.com/zeebo/blake3"

// blake3Sum computes the BLAKE3-256 hash of data.
func blake3Sum(data []byte) [32]byte {
	return blake3.Sum256(data)
}

// MerkleRoot computes the BLAKE3 Merkle root hash from a list of chunk hashes.
//
// Tree construction:
//   - Leaf: chunk hash (already BLAKE3)
//   - Internal: BLAKE3(left || right)
//   - Odd node: promoted to next level unchanged
//   - Single hash: returned as-is (file with one chunk)
//   - Empty list: zero hash
func MerkleRoot(hashes [][32]byte) [32]byte {
	if len(hashes) == 0 {
		return [32]byte{}
	}
	if len(hashes) == 1 {
		return hashes[0]
	}

	// Work on a copy to avoid mutating the input.
	level := make([][32]byte, len(hashes))
	copy(level, hashes)

	for len(level) > 1 {
		next := make([][32]byte, 0, (len(level)+1)/2)
		for i := 0; i < len(level); i += 2 {
			if i+1 < len(level) {
				// Pair: hash(left || right)
				var combined [64]byte
				copy(combined[:32], level[i][:])
				copy(combined[32:], level[i+1][:])
				next = append(next, blake3.Sum256(combined[:]))
			} else {
				// Odd node: promote
				next = append(next, level[i])
			}
		}
		level = next
	}

	return level[0]
}
