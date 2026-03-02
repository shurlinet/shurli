package zkp

import (
	"bytes"
	"fmt"
	"math/bits"
	"sort"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
)

// MerkleLeaf represents a single leaf in the Poseidon2 Merkle tree.
// Each leaf commits identity (pubkey), role, and reputation score.
type MerkleLeaf struct {
	PeerID      peer.ID
	Role        string
	Score       int // reputation score committed in leaf hash (0-100)
	PubKeyBytes [32]byte
	LeafHash    []byte // 32-byte Poseidon2 hash
}

// MerkleTree is a Poseidon2 Merkle tree built from authorized_keys.
// Leaves are sorted by hash for deterministic root computation.
type MerkleTree struct {
	Root   []byte       // 32-byte Poseidon2 root
	Depth  int          // tree depth (log2 of padded leaf count)
	Leaves []MerkleLeaf // sorted by leaf hash

	// nodes stores all tree hashes, level-ordered.
	// Level 0 = leaves (padded to power of 2), level Depth = root.
	// Each level has half the nodes of the previous level.
	nodes [][][]byte
}

// MerkleProof contains the sibling hashes and path direction bits
// needed to verify a leaf's membership in the tree.
type MerkleProof struct {
	LeafHash []byte   // the leaf being proved
	Path     [][]byte // sibling hashes, one per level (bottom to top)
	PathBits []bool   // direction bits: false=left, true=right
	Root     []byte   // the tree root at time of proof generation
}

// zeroLeafHash is the Poseidon2 hash of a padding leaf (34 zero field elements:
// 32 pubkey bytes + role + score). Used for padding the tree to the next power of 2.
var zeroLeafHash = func() []byte {
	elems := make([]fr.Element, 34)
	return NativePoseidon2(elems...)
}()

// BuildMerkleTree constructs a Poseidon2 Merkle tree from the authorized_keys file.
// All peers get score=0 (default). For binding reputation scores, use
// BuildMerkleTreeWithScores instead.
func BuildMerkleTree(authKeysPath string) (*MerkleTree, error) {
	return BuildMerkleTreeWithScores(authKeysPath, nil)
}

// BuildMerkleTreeWithScores constructs a Poseidon2 Merkle tree from authorized_keys
// with per-peer reputation scores committed in each leaf. Scores bind each peer's
// reputation to their identity in the tree, preventing score inflation in range proofs.
// Peers not in the scores map default to score=0.
func BuildMerkleTreeWithScores(authKeysPath string, scores map[peer.ID]int) (*MerkleTree, error) {
	entries, err := auth.ListPeers(authKeysPath)
	if err != nil {
		return nil, fmt.Errorf("reading authorized_keys: %w", err)
	}
	if len(entries) == 0 {
		return nil, ErrTreeEmpty
	}

	leaves, err := entriesToLeaves(entries, scores)
	if err != nil {
		return nil, err
	}

	return buildTreeFromLeaves(leaves)
}

// BuildMerkleTreeFromLeaves builds a tree from pre-computed leaves.
// Useful for testing without needing an authorized_keys file.
func BuildMerkleTreeFromLeaves(leaves []MerkleLeaf) (*MerkleTree, error) {
	if len(leaves) == 0 {
		return nil, ErrTreeEmpty
	}
	return buildTreeFromLeaves(leaves)
}

// GenerateProof generates a Merkle inclusion proof for the given peer ID.
func (t *MerkleTree) GenerateProof(peerID peer.ID) (*MerkleProof, error) {
	leafIdx := -1
	for i, leaf := range t.Leaves {
		if leaf.PeerID == peerID {
			leafIdx = i
			break
		}
	}
	if leafIdx < 0 {
		return nil, ErrPeerNotInTree
	}

	path := make([][]byte, t.Depth)
	pathBits := make([]bool, t.Depth)

	idx := leafIdx
	for level := 0; level < t.Depth; level++ {
		if idx%2 == 0 {
			path[level] = t.nodes[level][idx+1]
			pathBits[level] = false // we're on the left
		} else {
			path[level] = t.nodes[level][idx-1]
			pathBits[level] = true // we're on the right
		}
		idx /= 2
	}

	return &MerkleProof{
		LeafHash: t.Leaves[leafIdx].LeafHash,
		Path:     path,
		PathBits: pathBits,
		Root:     t.Root,
	}, nil
}

// VerifyProof verifies a Merkle proof against the given root.
func VerifyProof(proof *MerkleProof, root []byte) (bool, error) {
	current := proof.LeafHash
	for i := range proof.Path {
		var left, right []byte
		if proof.PathBits[i] {
			left = proof.Path[i]
			right = current
		} else {
			left = current
			right = proof.Path[i]
		}
		var err error
		current, err = NativePoseidon2Pair(left, right)
		if err != nil {
			return false, fmt.Errorf("verification at level %d: %w", i, err)
		}
	}
	return bytes.Equal(current, root), nil
}

// LeafCount returns the number of real (non-padding) leaves.
func (t *MerkleTree) LeafCount() int {
	return len(t.Leaves)
}

// entriesToLeaves converts auth.PeerEntry values to MerkleLeaf values.
// Scores are looked up from the provided map; peers not in the map default to 0.
func entriesToLeaves(entries []auth.PeerEntry, scores map[peer.ID]int) ([]MerkleLeaf, error) {
	leaves := make([]MerkleLeaf, 0, len(entries))
	for _, entry := range entries {
		pubKey, err := entry.PeerID.ExtractPublicKey()
		if err != nil {
			return nil, fmt.Errorf("extracting pubkey for %s: %w", entry.PeerID, err)
		}
		raw, err := pubKey.Raw()
		if err != nil {
			return nil, fmt.Errorf("getting raw pubkey for %s: %w", entry.PeerID, err)
		}
		if len(raw) != 32 {
			return nil, fmt.Errorf("unexpected pubkey length for %s: %d (expected 32)", entry.PeerID, len(raw))
		}

		var pubkeyBytes [32]byte
		copy(pubkeyBytes[:], raw)

		role := entry.Role
		if role == "" {
			role = "member"
		}

		score := 0
		if scores != nil {
			if s, ok := scores[entry.PeerID]; ok {
				score = s
			}
		}

		leaves = append(leaves, MerkleLeaf{
			PeerID:      entry.PeerID,
			Role:        role,
			Score:       score,
			PubKeyBytes: pubkeyBytes,
			LeafHash:    ComputeLeafHash(pubkeyBytes, role, score),
		})
	}
	return leaves, nil
}

// buildTreeFromLeaves sorts leaves by hash, pads, and builds the tree bottom-up.
func buildTreeFromLeaves(leaves []MerkleLeaf) (*MerkleTree, error) {
	// Sort by leaf hash for deterministic ordering.
	sorted := make([]MerkleLeaf, len(leaves))
	copy(sorted, leaves)
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].LeafHash, sorted[j].LeafHash) < 0
	})

	n := len(sorted)
	paddedN := nextPowerOf2(n)
	depth := bits.TrailingZeros(uint(paddedN))

	if depth > MaxTreeDepth {
		return nil, fmt.Errorf("tree depth %d exceeds maximum %d", depth, MaxTreeDepth)
	}

	// Build leaf layer (level 0).
	leafHashes := make([][]byte, paddedN)
	for i := 0; i < n; i++ {
		leafHashes[i] = sorted[i].LeafHash
	}
	for i := n; i < paddedN; i++ {
		leafHashes[i] = zeroLeafHash
	}

	// Build tree bottom-up.
	nodes := make([][][]byte, depth+1)
	nodes[0] = leafHashes

	for level := 0; level < depth; level++ {
		parentCount := len(nodes[level]) / 2
		parents := make([][]byte, parentCount)
		for i := 0; i < parentCount; i++ {
			left := nodes[level][2*i]
			right := nodes[level][2*i+1]
			hash, err := NativePoseidon2Pair(left, right)
			if err != nil {
				return nil, fmt.Errorf("hashing at level %d, index %d: %w", level, i, err)
			}
			parents[i] = hash
		}
		nodes[level+1] = parents
	}

	return &MerkleTree{
		Root:   nodes[depth][0],
		Depth:  depth,
		Leaves: sorted,
		nodes:  nodes,
	}, nil
}

// nextPowerOf2 returns the smallest power of 2 >= n. Returns 1 for n <= 1.
func nextPowerOf2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	n |= n >> 32
	n++
	return n
}
