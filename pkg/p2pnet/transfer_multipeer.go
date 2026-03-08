package p2pnet

import (
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Multi-peer download coordination using RaptorQ fountain codes.
//
// When a file is available from multiple peers, the receiver can request
// symbols from each peer in parallel. Each peer generates symbols starting
// from a different ID offset, so there's no wasted overlap. The receiver
// collects symbols from all sources and decodes when K symbols are available.
//
// Wire protocol: the receiver sends a msgMultiPeerRequest to each peer with
// the root hash + requested symbol ID range. The peer responds with RaptorQ
// symbols (not raw chunks) using msgFountainSymbol frames.

// Multi-peer wire constants.
const (
	// msgMultiPeerRequest is sent by receiver to request symbols from a peer.
	// Wire: msgMultiPeerRequest(1) + rootHash(32) + startSymbolID(4) + count(4)
	msgMultiPeerRequest = 0x0A

	// msgFountainSymbol carries a RaptorQ symbol.
	// Wire: msgFountainSymbol(1) + blockIndex(4) + symbolID(4) + dataLen(4) + data
	msgFountainSymbol = 0x0B

	// msgMultiPeerManifest is sent by peer in response to a multi-peer request
	// if the peer has the file. Contains the manifest so receiver can verify.
	// Wire: msgMultiPeerManifest(1) + manifestBytes
	msgMultiPeerManifest = 0x0C
)

// multiPeerConfig controls multi-peer download behavior.
type multiPeerConfig struct {
	// SymbolsPerPeer is the number of symbols requested from each peer.
	// More symbols = more redundancy but also more bandwidth.
	SymbolsPerPeer int

	// MaxPeers limits how many peers to download from simultaneously.
	MaxPeers int

	// Timeout per peer for the entire download.
	PeerTimeout time.Duration
}

// defaultMultiPeerConfig returns sensible defaults.
func defaultMultiPeerConfig() multiPeerConfig {
	return multiPeerConfig{
		SymbolsPerPeer: 0,   // 0 = auto (K/numPeers + repair overhead)
		MaxPeers:       4,
		PeerTimeout:    10 * time.Minute,
	}
}

// multiPeerSession coordinates fountain-coded downloads from multiple peers.
type multiPeerSession struct {
	rootHash [32]byte
	manifest *transferManifest

	// RaptorQ decoders, one per block.
	// Each block is one chunk of the file, encoded with RaptorQ for loss tolerance.
	mu       sync.Mutex
	decoders map[int]*raptorqDecoder // blockIndex -> decoder
	decoded  map[int][]byte          // blockIndex -> decoded data
	failed   map[int]error           // blockIndex -> decode error

	// Block partitioning: each chunk of the file is one RaptorQ block.
	blockCount int
	blockSizes []uint32

	// Progress tracking.
	symbolsReceived atomic.Int64
	blocksDecoded   atomic.Int32
	progress        *TransferProgress

	// Coordination.
	done chan struct{}
	once sync.Once
}

// newMultiPeerSession creates a session for multi-peer fountain-coded download.
func newMultiPeerSession(m *transferManifest, progress *TransferProgress) *multiPeerSession {
	return &multiPeerSession{
		rootHash:   m.RootHash,
		manifest:   m,
		decoders:   make(map[int]*raptorqDecoder),
		decoded:    make(map[int][]byte),
		failed:     make(map[int]error),
		blockCount: m.ChunkCount,
		blockSizes: m.ChunkSizes,
		progress:   progress,
		done:       make(chan struct{}),
	}
}

// getOrCreateDecoder returns or creates a decoder for a block.
func (s *multiPeerSession) getOrCreateDecoder(blockIndex int) (*raptorqDecoder, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if dec, ok := s.decoders[blockIndex]; ok {
		return dec, nil
	}

	if blockIndex < 0 || blockIndex >= s.blockCount {
		return nil, fmt.Errorf("block index out of range: %d", blockIndex)
	}

	dec, err := newRaptorQDecoder(s.blockSizes[blockIndex])
	if err != nil {
		return nil, fmt.Errorf("create decoder for block %d: %w", blockIndex, err)
	}
	s.decoders[blockIndex] = dec
	return dec, nil
}

// addSymbol feeds a received symbol into the appropriate block decoder.
// Returns true if all blocks have been decoded.
func (s *multiPeerSession) addSymbol(blockIndex int, symbolID uint32, data []byte) (bool, error) {
	select {
	case <-s.done:
		return true, nil // already complete
	default:
	}

	// Already decoded this block?
	s.mu.Lock()
	if _, ok := s.decoded[blockIndex]; ok {
		s.mu.Unlock()
		return s.isComplete(), nil
	}
	s.mu.Unlock()

	dec, err := s.getOrCreateDecoder(blockIndex)
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	canDecode, err := dec.addSymbol(symbolID, data)
	if err != nil {
		s.mu.Unlock()
		return false, fmt.Errorf("block %d symbol %d: %w", blockIndex, symbolID, err)
	}

	if canDecode {
		// Attempt immediate decode.
		ok, blockData, decErr := dec.decode()
		if decErr != nil {
			s.failed[blockIndex] = decErr
			s.mu.Unlock()
			return false, fmt.Errorf("block %d decode: %w", blockIndex, decErr)
		}
		if ok {
			s.decoded[blockIndex] = blockData
			delete(s.decoders, blockIndex) // free decoder memory
			s.mu.Unlock()

			s.blocksDecoded.Add(1)
			s.symbolsReceived.Add(1)

			slog.Debug("file-transfer: multi-peer block decoded",
				"block", blockIndex, "decoded", s.blocksDecoded.Load(),
				"total", s.blockCount)

			if s.isComplete() {
				s.once.Do(func() { close(s.done) })
				return true, nil
			}
			return false, nil
		}
	}
	s.mu.Unlock()

	s.symbolsReceived.Add(1)
	return false, nil
}

// isComplete returns true when all blocks have been decoded.
func (s *multiPeerSession) isComplete() bool {
	return int(s.blocksDecoded.Load()) >= s.blockCount
}

// results returns the decoded blocks in order. Only valid after isComplete() returns true.
func (s *multiPeerSession) results() ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(s.decoded) < s.blockCount {
		return nil, fmt.Errorf("incomplete: %d/%d blocks decoded", len(s.decoded), s.blockCount)
	}

	blocks := make([][]byte, s.blockCount)
	for i := 0; i < s.blockCount; i++ {
		data, ok := s.decoded[i]
		if !ok {
			return nil, fmt.Errorf("block %d missing", i)
		}
		blocks[i] = data
	}
	return blocks, nil
}

// peerSymbolRange calculates the symbol ID range for a peer.
// Each peer gets a non-overlapping range of symbol IDs to generate.
// peerIndex is 0-based, numPeers is total peer count.
func peerSymbolRange(k uint32, peerIndex, numPeers int) (startID, count uint32) {
	if numPeers <= 0 {
		numPeers = 1
	}

	// Each peer sends K/numPeers source symbols + repair overhead.
	perPeer := k / uint32(numPeers)
	if perPeer < 1 {
		perPeer = 1
	}

	// Add repair overhead (20% extra symbols for loss tolerance).
	repairPerPeer := uint32(float64(perPeer) * raptorqRepairRatio)
	if repairPerPeer < 1 {
		repairPerPeer = 1
	}
	totalPerPeer := perPeer + repairPerPeer

	// Peer 0 starts at symbol 0 (source symbols).
	// Peer 1 starts after peer 0's range.
	// For peers beyond source range, they generate repair symbols.
	startID = uint32(peerIndex) * totalPerPeer
	count = totalPerPeer

	return startID, count
}

// verifyBlock verifies a decoded block against the manifest hash.
func (s *multiPeerSession) verifyBlock(blockIndex int, data []byte) error {
	if blockIndex < 0 || blockIndex >= s.blockCount {
		return fmt.Errorf("block index out of range: %d", blockIndex)
	}

	hash := blake3Hash(data)
	if hash != s.manifest.ChunkHashes[blockIndex] {
		return fmt.Errorf("block %d hash mismatch", blockIndex)
	}

	if uint32(len(data)) != s.manifest.ChunkSizes[blockIndex] {
		return fmt.Errorf("block %d size mismatch: got %d, want %d",
			blockIndex, len(data), s.manifest.ChunkSizes[blockIndex])
	}

	return nil
}
