package p2pnet

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

// MultiPeerProtocol is the protocol ID for multi-peer fountain-coded downloads.
const MultiPeerProtocol = "/shurli/file-multi-peer/1.0.0"

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

// --- Protocol handler (sender side) ---

// HandleMultiPeerRequest returns a StreamHandler that serves multi-peer
// fountain-coded download requests. When a peer requests symbols for a file
// (identified by root hash), this handler reads the local file, encodes each
// chunk with RaptorQ, and sends the requested symbol range.
func (ts *TransferService) HandleMultiPeerRequest() StreamHandler {
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

		// Read msgMultiPeerRequest: type(1) + rootHash(32) + startSymbolID(4) + count(4)
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
		startSymbolID := binary.BigEndian.Uint32(header[33:37])
		symbolCount := binary.BigEndian.Uint32(header[37:41])

		// Security: bound symbol count.
		if symbolCount > 100000 {
			slog.Warn("file-multi-peer: excessive symbol count", "peer", short, "count", symbolCount)
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
		computedRoot := MerkleRoot(hashes)
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

		slog.Info("file-multi-peer: serving symbols",
			"peer", short, "file", manifest.Filename,
			"start", startSymbolID, "count", symbolCount,
			"blocks", len(chunks))

		// For each block (chunk), encode with RaptorQ and send requested symbol range.
		for blockIdx, chunk := range chunks {
			enc, encErr := newRaptorQEncoder(chunk)
			if encErr != nil {
				slog.Warn("file-multi-peer: encode failed", "peer", short, "block", blockIdx, "error", encErr)
				return
			}

			k := enc.sourceSymbolCount()
			// Determine which symbols this request covers for this block.
			// The startSymbolID and count are per-block: each peer gets the same
			// range applied to every block.
			end := startSymbolID + symbolCount
			if end > k*2 {
				end = k * 2 // cap at 2x source symbols
			}

			for sid := startSymbolID; sid < end; sid++ {
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

	manifest, firstStartID, firstCount, err := requestMultiPeerManifest(firstStream, rootHash, 0, numPeers)
	if err != nil {
		firstStream.Close()
		return nil, fmt.Errorf("get manifest from first peer: %w", err)
	}

	// Verify root hash matches.
	computedRoot := MerkleRoot(manifest.ChunkHashes)
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

		// First peer: already has a stream and known symbol range.
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer firstStream.Close()
			if err := receiveSymbolsFromPeer(dlCtx, firstStream, session, firstStartID, firstCount); err != nil {
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

				// Get symbol range for this peer.
				// Use the first block's K to compute ranges.
				k := uint32(0)
				if manifest.ChunkCount > 0 && manifest.ChunkSizes[0] > 0 {
					k = (manifest.ChunkSizes[0] + raptorqSymbolSize - 1) / raptorqSymbolSize
				}
				if k < 1 {
					k = 1
				}
				startID, count := peerSymbolRange(k, peerIdx, numPeers)

				// Send request + skip manifest (we already have it).
				_, _, _, reqErr := requestMultiPeerManifest(stream, rootHash, peerIdx, numPeers)
				if reqErr != nil {
					slog.Warn("file-multi-peer: request failed",
						"peer_index", peerIdx, "error", reqErr)
					errCh <- reqErr
					return
				}

				if err := receiveSymbolsFromPeer(dlCtx, stream, session, startID, count); err != nil {
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
// the manifest response. Returns the manifest and the symbol range for this peer.
func requestMultiPeerManifest(s network.Stream, rootHash [32]byte, peerIndex, numPeers int) (*transferManifest, uint32, uint32, error) {
	s.SetDeadline(time.Now().Add(30 * time.Second))

	// Compute symbol range. We need to estimate K from a reasonable block size.
	// We'll use a default estimate and refine after getting the manifest.
	// For the request, compute a provisional range based on a typical block size.
	estimatedK := uint32(256) // reasonable default for ~256KB block
	startID, count := peerSymbolRange(estimatedK, peerIndex, numPeers)

	// Write: msgMultiPeerRequest(1) + rootHash(32) + startSymbolID(4) + count(4)
	var header [41]byte
	header[0] = msgMultiPeerRequest
	copy(header[1:33], rootHash[:])
	binary.BigEndian.PutUint32(header[33:37], startID)
	binary.BigEndian.PutUint32(header[37:41], count)
	if _, err := s.Write(header[:]); err != nil {
		return nil, 0, 0, fmt.Errorf("write request: %w", err)
	}

	// Read manifest response: msgMultiPeerManifest(1) + len(4) + data
	var respHeader [5]byte
	if _, err := io.ReadFull(s, respHeader[:]); err != nil {
		return nil, 0, 0, fmt.Errorf("read manifest header: %w", err)
	}
	if respHeader[0] != msgMultiPeerManifest {
		return nil, 0, 0, fmt.Errorf("unexpected response type: 0x%02x", respHeader[0])
	}

	manifestLen := binary.BigEndian.Uint32(respHeader[1:5])
	if manifestLen > maxManifestSize {
		return nil, 0, 0, fmt.Errorf("manifest too large: %d bytes", manifestLen)
	}

	manifestData := make([]byte, manifestLen)
	if _, err := io.ReadFull(s, manifestData); err != nil {
		return nil, 0, 0, fmt.Errorf("read manifest: %w", err)
	}

	manifest, err := unmarshalManifest(manifestData)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("parse manifest: %w", err)
	}

	// Now that we have the manifest, compute the actual symbol range
	// using the real block size.
	if manifest.ChunkCount > 0 && manifest.ChunkSizes[0] > 0 {
		actualK := (manifest.ChunkSizes[0] + raptorqSymbolSize - 1) / raptorqSymbolSize
		if actualK >= 1 {
			startID, count = peerSymbolRange(actualK, peerIndex, numPeers)
		}
	}

	s.SetDeadline(time.Now().Add(10 * time.Minute))
	return manifest, startID, count, nil
}

// receiveSymbolsFromPeer reads fountain symbols from a peer stream and feeds
// them into the multi-peer session. Returns when the stream ends, the context
// is cancelled, or an error occurs.
func receiveSymbolsFromPeer(ctx context.Context, s network.Stream, session *multiPeerSession, startID, count uint32) error {
	_ = startID // the server determines which symbols to send based on the request
	_ = count

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
