package filetransfer

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/shurlinet/shurli/pkg/sdk"
)

// Multi-peer download coordination using RaptorQ fountain codes.
//
// When a file is available from multiple peers, the receiver requests
// interleaved symbols from each peer in parallel. Peer i generates symbol
// IDs i, i+N, i+2N, ... (where N = numPeers), guaranteeing zero overlap.
// Fast peers naturally deliver more symbols; the receiver decodes each
// block as soon as K symbols arrive from ANY combination of sources.
// This gives additive bandwidth: speed = sum(peers) for equal speeds,
// speed = max(peers) when one peer dominates.
//
// Wire protocol: the receiver sends a msgMultiPeerRequest with the root hash,
// peer index, and peer count. Each peer responds with the manifest and then
// streams interleaved RaptorQ symbols via msgFountainSymbol frames.

// MultiPeerProtocol is the protocol ID for multi-peer fountain-coded downloads.
const MultiPeerProtocol = "/shurli/file-multi-peer/1.0.0"

// Multi-peer wire constants.
const (
	// msgMultiPeerRequest is sent by receiver to request symbols from a peer.
	// Wire: msgMultiPeerRequest(1) + rootHash(32) + peerIndex(2) + numPeers(2) + maxSymbolsPerBlock(4)
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

// addSymbol feeds a received symbol into the appropriate block decoder.
// Returns true if all blocks have been decoded.
// Thread-safe: holds mu for the entire check-create-decode cycle to prevent
// TOCTOU races where two goroutines create duplicate decoders for the same block.
func (s *multiPeerSession) addSymbol(blockIndex int, symbolID uint32, data []byte) (bool, error) {
	select {
	case <-s.done:
		return true, nil // already complete
	default:
	}

	s.mu.Lock()

	// Already decoded this block?
	if _, ok := s.decoded[blockIndex]; ok {
		s.mu.Unlock()
		return s.isComplete(), nil
	}

	// Get or create decoder (inline, under same lock).
	dec, ok := s.decoders[blockIndex]
	if !ok {
		if blockIndex < 0 || blockIndex >= s.blockCount {
			s.mu.Unlock()
			return false, fmt.Errorf("block index out of range: %d", blockIndex)
		}
		var err error
		dec, err = newRaptorQDecoder(s.blockSizes[blockIndex])
		if err != nil {
			s.mu.Unlock()
			return false, fmt.Errorf("create decoder for block %d: %w", blockIndex, err)
		}
		s.decoders[blockIndex] = dec
	}

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

// interleavedSymbolCount returns how many symbols a single peer should generate
// per block so that any single fast peer can decode alone. Each peer produces
// K + repair symbols with interleaved IDs (peerIndex, peerIndex+numPeers, ...).
// The receiver decodes as soon as K symbols arrive from ANY combination of peers.
func interleavedSymbolCount(k uint32) uint32 {
	repair := uint32(float64(k) * raptorqRepairRatio)
	if repair < 1 {
		repair = 1
	}
	return k + repair
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

// --- Protocol handler (sender side) ---

// HandleMultiPeerRequest returns a StreamHandler that serves multi-peer
// fountain-coded download requests. When a peer requests symbols for a file
// (identified by root hash), this handler reads the local file, encodes each
// chunk with RaptorQ, and sends the requested symbol range.
func (ts *TransferService) HandleMultiPeerRequest() sdk.StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()

		remotePeer := s.Conn().RemotePeer()
		short := remotePeer.String()[:16] + "..."

		// Rate limit multi-peer requests (same limiter as inbound transfers).
		peerKey := remotePeer.String()
		if ts.rateLimiter != nil && !ts.rateLimiter.allow(peerKey) {
			slog.Warn("file-multi-peer: rate limit exceeded", "peer", short)
			s.Reset()
			return
		}

		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Read msgMultiPeerRequest: type(1) + rootHash(32) + peerIndex(2) + numPeers(2) + maxSymbolsPerBlock(4)
		var header [41]byte
		if _, err := io.ReadFull(s, header[:]); err != nil {
			slog.Debug("file-multi-peer: read request failed", "peer", short, "error", err)
			return
		}

		if header[0] != msgMultiPeerRequest {
			slog.Debug("file-multi-peer: unexpected message type", "peer", short, "type", header[0])
			return
		}

		var rootHash [32]byte
		copy(rootHash[:], header[1:33])
		// Wire: peerIndex(2) + numPeers(2) + maxSymbolsPerBlock(4)
		peerIndex := int(binary.BigEndian.Uint16(header[33:35]))
		numPeers := int(binary.BigEndian.Uint16(header[35:37]))
		maxSymbolsPerBlock := binary.BigEndian.Uint32(header[37:41])

		// Security bounds. numPeers >= 2 enforced to match receiver-side
		// minimum (single-peer downloads use the standard transfer protocol).
		if numPeers < 2 || numPeers > 64 {
			slog.Warn("file-multi-peer: invalid numPeers", "peer", short, "numPeers", numPeers)
			return
		}
		if peerIndex >= numPeers {
			slog.Warn("file-multi-peer: invalid peerIndex", "peer", short, "peerIndex", peerIndex, "numPeers", numPeers)
			return
		}
		if maxSymbolsPerBlock > 100000 {
			slog.Warn("file-multi-peer: excessive maxSymbolsPerBlock", "peer", short, "count", maxSymbolsPerBlock)
			return
		}
		// maxSymbolsPerBlock=0 means auto: server computes from actual K per block.
		// This ensures a single fast peer always generates enough symbols to decode alone.
		// Non-zero values < 4 are rejected: requesting fewer than 4 symbols per block
		// wastes server CPU (full file chunking + RaptorQ encoding) for no useful output.
		autoSymbols := maxSymbolsPerBlock == 0
		if !autoSymbols && maxSymbolsPerBlock < 4 {
			slog.Warn("file-multi-peer: maxSymbolsPerBlock too small", "peer", short, "count", maxSymbolsPerBlock)
			return
		}

		// Look up file by root hash.
		localPath, ok := ts.LookupHash(rootHash)
		if !ok {
			slog.Debug("file-multi-peer: unknown root hash", "peer", short)
			return
		}

		// Read the file and chunk with FastCDC.
		f, err := os.Open(localPath)
		if err != nil {
			slog.Warn("file-multi-peer: open file failed", "peer", short, "path", localPath, "error", err)
			return
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			slog.Warn("file-multi-peer: stat file failed", "peer", short, "error", err)
			return
		}
		fileSize := fi.Size()

		var chunks [][]byte
		var hashes [][32]byte
		var sizes []uint32
		chunkErr := ChunkReader(f, fileSize, func(c Chunk) error {
			chunks = append(chunks, append([]byte(nil), c.Data...))
			hashes = append(hashes, c.Hash)
			sizes = append(sizes, uint32(len(c.Data)))
			return nil
		})
		f.Close()
		if chunkErr != nil {
			slog.Warn("file-multi-peer: chunking failed", "peer", short, "error", chunkErr)
			return
		}
		if len(chunks) == 0 {
			slog.Warn("file-multi-peer: empty chunking result", "peer", short)
			return
		}
		computedRoot := sdk.MerkleRoot(hashes)
		if computedRoot != rootHash {
			slog.Warn("file-multi-peer: root hash mismatch on re-chunk", "peer", short)
			return
		}

		manifest := &transferManifest{
			Filename:    filepath.Base(localPath),
			FileSize:    fileSize,
			ChunkCount:  len(chunks),
			RootHash:    rootHash,
			ChunkHashes: hashes,
			ChunkSizes:  sizes,
		}

		// Write manifest message.
		manifestBytes, marshalErr := marshalManifest(manifest)
		if marshalErr != nil {
			slog.Warn("file-multi-peer: marshal manifest failed", "peer", short, "error", marshalErr)
			return
		}

		// Send: msgMultiPeerManifest(1) + len(4) + manifestBytes
		var mHeader [5]byte
		mHeader[0] = msgMultiPeerManifest
		binary.BigEndian.PutUint32(mHeader[1:5], uint32(len(manifestBytes)))
		if _, err := s.Write(mHeader[:]); err != nil {
			return
		}
		if _, err := s.Write(manifestBytes); err != nil {
			return
		}

		slog.Info("file-multi-peer: serving interleaved symbols",
			"peer", short, "file", manifest.Filename,
			"peerIndex", peerIndex, "numPeers", numPeers,
			"maxPerBlock", maxSymbolsPerBlock, "blocks", len(chunks))

		// For each block (chunk), encode with RaptorQ and send interleaved symbols.
		// Interleaving: this peer generates symbol IDs peerIndex, peerIndex+numPeers,
		// peerIndex+2*numPeers, ... ensuring zero overlap between peers.
		// Fast peers deliver more symbols before the receiver has K; slow peers
		// contribute what they can. Decode triggers when ANY K symbols arrive.
		for blockIdx, chunk := range chunks {
			enc, encErr := newRaptorQEncoder(chunk)
			if encErr != nil {
				slog.Warn("file-multi-peer: encode failed", "peer", short, "block", blockIdx, "error", encErr)
				return
			}

			k := enc.sourceSymbolCount()
			// Per-block symbol limit: auto mode uses K + repair (enough for
			// a single fast peer to decode alone). Client-specified cap is
			// used as an upper bound when non-zero.
			blockLimit := interleavedSymbolCount(k)
			if !autoSymbols && maxSymbolsPerBlock < blockLimit {
				blockLimit = maxSymbolsPerBlock
			}
			// Cap symbol IDs at 2*K to avoid unbounded generation.
			// Use uint64 arithmetic to prevent uint32 overflow wrap-around
			// which would cause an infinite loop (sid wraps to a valid value,
			// bypassing the maxID check).
			maxID := uint64(k) * 2
			sent := uint32(0)
			for i := uint64(0); sent < blockLimit; i++ {
				sid64 := uint64(peerIndex) + i*uint64(numPeers)
				if sid64 >= maxID {
					break
				}
				sid := uint32(sid64)
				sym := enc.genSymbol(sid)

				// Wire: msgFountainSymbol(1) + blockIndex(4) + symbolID(4) + dataLen(4) + data
				var symHeader [13]byte
				symHeader[0] = msgFountainSymbol
				binary.BigEndian.PutUint32(symHeader[1:5], uint32(blockIdx))
				binary.BigEndian.PutUint32(symHeader[5:9], sid)
				binary.BigEndian.PutUint32(symHeader[9:13], uint32(len(sym)))
				if _, err := s.Write(symHeader[:]); err != nil {
					return
				}
				if _, err := s.Write(sym); err != nil {
					return
				}
				sent++
			}
		}

		slog.Info("file-multi-peer: done serving", "peer", short, "file", manifest.Filename)
	}
}

// marshalManifest serializes a manifest to bytes for multi-peer transport.
func marshalManifest(m *transferManifest) ([]byte, error) {
	// Simple binary format: chunkCount(4) + fileSize(8) + rootHash(32)
	// + nameLen(2) + name + chunkCount * (hash(32) + size(4))
	nameBytes := []byte(m.Filename)
	if len(nameBytes) > 65535 {
		return nil, fmt.Errorf("filename too long: %d bytes", len(nameBytes))
	}

	size := 4 + 8 + 32 + 2 + len(nameBytes) + m.ChunkCount*(32+4)
	buf := make([]byte, size)
	off := 0

	binary.BigEndian.PutUint32(buf[off:off+4], uint32(m.ChunkCount))
	off += 4
	binary.BigEndian.PutUint64(buf[off:off+8], uint64(m.FileSize))
	off += 8
	copy(buf[off:off+32], m.RootHash[:])
	off += 32
	binary.BigEndian.PutUint16(buf[off:off+2], uint16(len(nameBytes)))
	off += 2
	copy(buf[off:off+len(nameBytes)], nameBytes)
	off += len(nameBytes)

	for i := 0; i < m.ChunkCount; i++ {
		copy(buf[off:off+32], m.ChunkHashes[i][:])
		off += 32
		binary.BigEndian.PutUint32(buf[off:off+4], m.ChunkSizes[i])
		off += 4
	}
	return buf, nil
}

// unmarshalManifest deserializes a manifest from multi-peer transport format.
func unmarshalManifest(data []byte) (*transferManifest, error) {
	if len(data) < 46 { // 4+8+32+2 minimum
		return nil, fmt.Errorf("manifest too short: %d bytes", len(data))
	}

	off := 0
	chunkCount := int(binary.BigEndian.Uint32(data[off : off+4]))
	off += 4
	if chunkCount < 0 || chunkCount > maxChunkCount {
		return nil, fmt.Errorf("invalid chunk count: %d", chunkCount)
	}

	fileSize := int64(binary.BigEndian.Uint64(data[off : off+8]))
	off += 8

	var rootHash [32]byte
	copy(rootHash[:], data[off:off+32])
	off += 32

	nameLen := int(binary.BigEndian.Uint16(data[off : off+2]))
	off += 2
	if off+nameLen > len(data) {
		return nil, fmt.Errorf("truncated filename")
	}
	filename := sanitizeRelativePath(string(data[off : off+nameLen]))
	off += nameLen
	if filename == "" {
		return nil, fmt.Errorf("invalid filename after sanitization")
	}

	need := off + chunkCount*(32+4)
	if need > len(data) {
		return nil, fmt.Errorf("truncated chunk data: need %d, have %d", need, len(data))
	}

	hashes := make([][32]byte, chunkCount)
	sizes := make([]uint32, chunkCount)
	for i := 0; i < chunkCount; i++ {
		copy(hashes[i][:], data[off:off+32])
		off += 32
		sizes[i] = binary.BigEndian.Uint32(data[off : off+4])
		off += 4
	}

	return &transferManifest{
		Filename:    filename,
		FileSize:    fileSize,
		ChunkCount:  chunkCount,
		RootHash:    rootHash,
		ChunkHashes: hashes,
		ChunkSizes:  sizes,
	}, nil
}

// --- Multi-peer download coordinator (receiver side) ---

// MultiPeerStreamOpener is a function that opens a stream to a specific peer
// for the multi-peer download protocol.
type MultiPeerStreamOpener func(peerID peer.ID) (network.Stream, error)

// DownloadMultiPeer downloads a file from multiple peers using RaptorQ fountain
// codes. Each peer sends a non-overlapping symbol range. The receiver assembles
// the file from decoded blocks.
//
// peers must contain at least 2 peer IDs. If fewer are available, use single-peer
// download instead (ReceiveFrom).
//
// rootHash identifies the file. All peers must have the same file (same root hash).
func (ts *TransferService) DownloadMultiPeer(
	ctx context.Context,
	rootHash [32]byte,
	peers []peer.ID,
	openStream MultiPeerStreamOpener,
	destDir string,
) (*TransferProgress, error) {
	if len(peers) < 2 {
		return nil, fmt.Errorf("multi-peer download requires at least 2 peers, got %d", len(peers))
	}

	if destDir == "" {
		destDir = ts.receiveDir
	}
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	maxPeers := ts.multiPeerMaxPeers
	if len(peers) > maxPeers {
		peers = peers[:maxPeers]
	}
	numPeers := len(peers)

	// Get manifest from first peer to know file structure.
	firstStream, err := openStream(peers[0])
	if err != nil {
		return nil, fmt.Errorf("connect to first peer: %w", err)
	}

	manifest, err := requestMultiPeerManifest(firstStream, rootHash, 0, numPeers)
	if err != nil {
		firstStream.Close()
		return nil, fmt.Errorf("get manifest from first peer: %w", err)
	}

	// Verify root hash matches.
	computedRoot := sdk.MerkleRoot(manifest.ChunkHashes)
	if computedRoot != rootHash {
		firstStream.Close()
		return nil, fmt.Errorf("manifest root hash mismatch")
	}

	// Size limit check.
	if ts.maxSize > 0 && manifest.FileSize > ts.maxSize {
		firstStream.Close()
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", manifest.FileSize, ts.maxSize)
	}

	// Disk space check.
	if err := checkDiskSpaceAt(destDir, manifest.FileSize); err != nil {
		firstStream.Close()
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
		"multi-peer", "download", manifest.ChunkCount, false)
	progress.setStatus("active")
	// D1 fix: create a cancellable context so CancelTransfer can stop this download.
	// The cancel func is registered on the progress object.
	dlCtx, dlCancel := context.WithCancel(ctx)
	progress.setCancelFunc(dlCancel)
	ts.logEvent(EventLogStarted, "multi-peer-download", "multi-peer", manifest.Filename, manifest.FileSize, 0, "", "")

	// Launch background download.
	go func() {
		defer dlCancel()
		dlStart := time.Now()
		session := newMultiPeerSession(manifest, progress)

		var wg sync.WaitGroup
		errCh := make(chan error, numPeers)

		// First peer: already has a stream, manifest received.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer firstStream.Close()
			if err := receiveSymbolsFromPeer(dlCtx, firstStream, session); err != nil {
				slog.Warn("file-multi-peer: peer 0 failed", "error", err)
				errCh <- err
			}
		}()

		// Remaining peers.
		for i := 1; i < numPeers; i++ {
			peerIdx := i
			peerID := peers[peerIdx]
			wg.Add(1)
			go func() {
				defer wg.Done()
				stream, openErr := openStream(peerID)
				if openErr != nil {
					slog.Warn("file-multi-peer: connect failed",
						"peer_index", peerIdx, "error", openErr)
					errCh <- openErr
					return
				}
				defer stream.Close()

				// Send request and verify manifest matches peer 0's.
				peerManifest, reqErr := requestMultiPeerManifest(stream, rootHash, peerIdx, numPeers)
				if reqErr != nil {
					slog.Warn("file-multi-peer: request failed",
						"peer_index", peerIdx, "error", reqErr)
					errCh <- reqErr
					return
				}
				// Security: verify peer's manifest root hash matches peer 0's.
				// A malicious peer could serve a different file with the same
				// root hash request, feeding wrong symbols into the decoder.
				peerRoot := sdk.MerkleRoot(peerManifest.ChunkHashes)
				if peerRoot != rootHash {
					slog.Warn("file-multi-peer: peer manifest root hash mismatch, excluding",
						"peer_index", peerIdx)
					errCh <- fmt.Errorf("peer %d: manifest root hash mismatch", peerIdx)
					return
				}
				// Verify chunk sizes match peer 0's manifest. Merkle root only
				// covers hashes, not sizes. A malicious peer could send correct
				// hashes but inflated sizes, causing OOM in decoder allocation.
				if peerManifest.ChunkCount != manifest.ChunkCount {
					slog.Warn("file-multi-peer: peer chunk count mismatch, excluding",
						"peer_index", peerIdx,
						"peer_chunks", peerManifest.ChunkCount,
						"expected", manifest.ChunkCount)
					errCh <- fmt.Errorf("peer %d: chunk count mismatch", peerIdx)
					return
				}
				for ci := 0; ci < manifest.ChunkCount; ci++ {
					if peerManifest.ChunkSizes[ci] != manifest.ChunkSizes[ci] {
						slog.Warn("file-multi-peer: peer chunk size mismatch, excluding",
							"peer_index", peerIdx, "chunk", ci,
							"peer_size", peerManifest.ChunkSizes[ci],
							"expected", manifest.ChunkSizes[ci])
						errCh <- fmt.Errorf("peer %d: chunk %d size mismatch", peerIdx, ci)
						return
					}
				}

				if err := receiveSymbolsFromPeer(dlCtx, stream, session); err != nil {
					slog.Warn("file-multi-peer: peer failed",
						"peer_index", peerIdx, "error", err)
					errCh <- err
				}
			}()
		}

		// Wait for all peers or completion.
		doneCh := make(chan struct{})
		go func() {
			wg.Wait()
			close(doneCh)
		}()

		select {
		case <-session.done:
			// Session complete, all blocks decoded.
		case <-doneCh:
			// All peers finished (maybe before session is complete).
		case <-dlCtx.Done():
			progress.finish(dlCtx.Err())
			ts.markCompleted(progress.ID)
			return
		}

		if !session.isComplete() {
			progress.finish(fmt.Errorf("multi-peer download incomplete: %d/%d blocks decoded",
				session.blocksDecoded.Load(), session.blockCount))
			ts.markCompleted(progress.ID)
			return
		}

		// Assemble file from decoded blocks.
		blocks, resultsErr := session.results()
		if resultsErr != nil {
			progress.finish(resultsErr)
			ts.markCompleted(progress.ID)
			return
		}

		// Verify each block hash.
		for i, block := range blocks {
			if verifyErr := session.verifyBlock(i, block); verifyErr != nil {
				progress.finish(verifyErr)
				ts.markCompleted(progress.ID)
				return
			}
		}

		// Write assembled file atomically.
		tmpPath, tmpFile, createErr := createTempFileIn(destDir, manifest.Filename)
		if createErr != nil {
			progress.finish(createErr)
			ts.markCompleted(progress.ID)
			return
		}

		var writeErr error
		for _, block := range blocks {
			if _, wErr := tmpFile.Write(block); wErr != nil {
				writeErr = wErr
				break
			}
		}
		if writeErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			progress.finish(writeErr)
			ts.markCompleted(progress.ID)
			return
		}

		if syncErr := tmpFile.Sync(); syncErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			progress.finish(syncErr)
			ts.markCompleted(progress.ID)
			return
		}
		tmpFile.Close()

		finalPath := filepath.Join(destDir, filepath.Base(manifest.Filename))
		finalPath, fpErr := nonCollidingPath(finalPath)
		if fpErr != nil {
			os.Remove(tmpPath)
			progress.finish(fpErr)
			ts.markCompleted(progress.ID)
			return
		}

		if renameErr := os.Rename(tmpPath, finalPath); renameErr != nil {
			os.Remove(tmpPath)
			progress.finish(renameErr)
			ts.markCompleted(progress.ID)
			return
		}
		os.Chmod(finalPath, 0644)

		progress.finish(nil)
		ts.markCompleted(progress.ID)

		// Register hash for future multi-peer requests.
		ts.RegisterHash(rootHash, finalPath)

		dur := time.Since(dlStart).Truncate(time.Millisecond).String()
		slog.Info("file-multi-peer: download complete",
			"file", manifest.Filename, "size", manifest.FileSize,
			"peers", numPeers, "duration", dur)
		ts.logEvent(EventLogCompleted, "multi-peer-download", "multi-peer", manifest.Filename, manifest.FileSize, manifest.FileSize, "", dur)
	}()

	return progress, nil
}

// requestMultiPeerManifest sends a multi-peer request to a peer and reads
// the manifest response. Returns the manifest.
func requestMultiPeerManifest(s network.Stream, rootHash [32]byte, peerIndex, numPeers int) (*transferManifest, error) {
	s.SetDeadline(time.Now().Add(30 * time.Second))

	// maxSymbolsPerBlock=0 signals the server to auto-compute from actual K.
	// The server knows the real block size; the client does not before receiving
	// the manifest. Setting 0 ensures a single fast peer always generates
	// enough symbols to decode alone, regardless of block size.
	maxSymbolsPerBlock := uint32(0)

	// Write: msgMultiPeerRequest(1) + rootHash(32) + peerIndex(2) + numPeers(2) + maxSymbolsPerBlock(4)
	var header [41]byte
	header[0] = msgMultiPeerRequest
	copy(header[1:33], rootHash[:])
	binary.BigEndian.PutUint16(header[33:35], uint16(peerIndex))
	binary.BigEndian.PutUint16(header[35:37], uint16(numPeers))
	binary.BigEndian.PutUint32(header[37:41], maxSymbolsPerBlock)
	if _, err := s.Write(header[:]); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Read manifest response: msgMultiPeerManifest(1) + len(4) + data
	var respHeader [5]byte
	if _, err := io.ReadFull(s, respHeader[:]); err != nil {
		return nil, fmt.Errorf("read manifest header: %w", err)
	}
	if respHeader[0] != msgMultiPeerManifest {
		return nil, fmt.Errorf("unexpected response type: 0x%02x", respHeader[0])
	}

	manifestLen := binary.BigEndian.Uint32(respHeader[1:5])
	if manifestLen > maxManifestSize {
		return nil, fmt.Errorf("manifest too large: %d bytes", manifestLen)
	}

	manifestData := make([]byte, manifestLen)
	if _, err := io.ReadFull(s, manifestData); err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	manifest, err := unmarshalManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	s.SetDeadline(time.Now().Add(10 * time.Minute))
	return manifest, nil
}

// receiveSymbolsFromPeer reads fountain symbols from a peer stream and feeds
// them into the multi-peer session. Symbol IDs are interleaved by the sender
// (peerIndex + i*numPeers), so there is zero overlap between peers. The
// receiver simply collects symbols from all peers and decodes each block
// as soon as K symbols arrive from any combination of sources.
func receiveSymbolsFromPeer(ctx context.Context, s network.Stream, session *multiPeerSession) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-session.done:
			return nil // download complete
		default:
		}

		// Read: msgFountainSymbol(1) + blockIndex(4) + symbolID(4) + dataLen(4)
		var symHeader [13]byte
		if _, err := io.ReadFull(s, symHeader[:]); err != nil {
			if err == io.EOF {
				return nil // peer done sending
			}
			return fmt.Errorf("read symbol header: %w", err)
		}

		if symHeader[0] != msgFountainSymbol {
			return fmt.Errorf("unexpected message type: 0x%02x", symHeader[0])
		}

		blockIndex := int(binary.BigEndian.Uint32(symHeader[1:5]))
		symbolID := binary.BigEndian.Uint32(symHeader[5:9])
		dataLen := binary.BigEndian.Uint32(symHeader[9:13])

		// Validate bounds before allocating memory.
		if blockIndex < 0 || blockIndex >= session.blockCount {
			return fmt.Errorf("block index out of range: %d (max %d)", blockIndex, session.blockCount)
		}
		if dataLen > uint32(raptorqSymbolSize*2) {
			return fmt.Errorf("symbol too large: %d bytes", dataLen)
		}

		symData := make([]byte, dataLen)
		if _, err := io.ReadFull(s, symData); err != nil {
			return fmt.Errorf("read symbol data: %w", err)
		}

		complete, err := session.addSymbol(blockIndex, symbolID, symData)
		if err != nil {
			slog.Debug("file-multi-peer: add symbol error",
				"block", blockIndex, "symbol", symbolID, "error", err)
			continue // non-fatal: might be a corrupted symbol
		}

		if complete {
			return nil
		}
	}
}
