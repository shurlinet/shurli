package p2pnet

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"golang.org/x/sys/unix"
)

// Transfer protocol constants.
const (
	// TransferProtocol is the libp2p protocol ID for chunked file transfer.
	TransferProtocol = "/shurli/file-transfer/2.0.0"

	// SHFT wire format version.
	shftVersion = 0x02

	// SHFT magic bytes: "SHFT" (Shurli File Transfer).
	shftMagic0 = 'S'
	shftMagic1 = 'H'
	shftMagic2 = 'F'
	shftMagic3 = 'T'

	// Wire message types.
	msgManifest     = 0x01 // sender -> receiver: file manifest
	msgAccept       = 0x02 // receiver -> sender: accept transfer
	msgReject       = 0x03 // receiver -> sender: reject transfer
	msgChunk        = 0x04 // sender -> receiver: chunk data
	msgTransferDone = 0x05 // sender -> receiver: all chunks sent

	// Manifest flags (bitmask).
	flagCompressed = 0x01 // zstd compression enabled

	// Security limits.
	maxFilenameLen         = 4096       // max filename length in bytes
	maxFileSize            = 1 << 40    // 1 TB max single file
	maxChunkCount          = 1 << 20    // 1M chunks max per transfer
	maxManifestSize        = 64 << 20   // 64 MB max manifest wire size
	maxChunkWireSize       = 4 << 20    // 4 MB max single chunk on wire (compressed)
	maxDecompressedChunk   = 8 << 20    // 8 MB max decompressed chunk
	maxDecompressRatio     = 10         // abort if decompressed > 10x compressed
	maxConcurrentTransfers = 10         // global inbound transfer limit
	maxPerPeerTransfers    = 3          // per-peer inbound limit
	maxTrackedTransfers    = 10000      // max tracked transfer entries

	// Timeouts.
	transferStreamDeadline = 1 * time.Hour // max wall-clock for entire transfer
	askModeTimeout         = 5 * time.Minute // receiver approval timeout in ask mode
)

// ReceiveMode controls how incoming transfers are handled.
type ReceiveMode string

const (
	ReceiveModeOff      ReceiveMode = "off"      // reject all
	ReceiveModeContacts ReceiveMode = "contacts"  // auto-accept from authorized peers (default)
	ReceiveModeAsk      ReceiveMode = "ask"       // queue for manual approval
	ReceiveModeOpen     ReceiveMode = "open"      // accept from any authorized peer
)

// TransferProgress tracks the progress of an active transfer.
type TransferProgress struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	Transferred int64     `json:"transferred"`
	ChunksTotal int       `json:"chunks_total"`
	ChunksDone  int       `json:"chunks_done"`
	Compressed  bool      `json:"compressed"`
	PeerID      string    `json:"peer_id"`
	Direction   string    `json:"direction"` // "send" or "receive"
	Status      string    `json:"status"`    // "pending", "active", "complete", "failed", "rejected"
	StartTime   time.Time `json:"start_time"`
	Done        bool      `json:"done"`
	Error       string    `json:"error,omitempty"`

	mu sync.Mutex
}

func (p *TransferProgress) updateChunks(transferred int64, chunksDone int) {
	p.mu.Lock()
	p.Transferred = transferred
	p.ChunksDone = chunksDone
	p.mu.Unlock()
}

func (p *TransferProgress) setStatus(status string) {
	p.mu.Lock()
	p.Status = status
	p.mu.Unlock()
}

func (p *TransferProgress) finish(err error) {
	p.mu.Lock()
	p.Done = true
	if err != nil {
		p.Error = err.Error()
		p.Status = "failed"
	} else {
		p.Status = "complete"
	}
	p.mu.Unlock()
}

// Snapshot returns a copy safe for JSON serialization.
func (p *TransferProgress) Snapshot() TransferProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return TransferProgress{
		ID: p.ID, Filename: p.Filename, Size: p.Size,
		Transferred: p.Transferred, ChunksTotal: p.ChunksTotal,
		ChunksDone: p.ChunksDone, Compressed: p.Compressed,
		PeerID: p.PeerID, Direction: p.Direction, Status: p.Status,
		StartTime: p.StartTime, Done: p.Done, Error: p.Error,
	}
}

// Sent returns the transferred bytes (compatibility alias for CLI polling).
func (p *TransferProgress) Sent() int64 {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.Transferred
}

// transferManifest is the file manifest exchanged before chunk transfer.
type transferManifest struct {
	Filename    string     // base name only
	FileSize    int64      // original file size
	ChunkCount  int        // number of chunks
	Flags       uint8      // compression, etc.
	RootHash    [32]byte   // BLAKE3 Merkle root of all chunk hashes
	ChunkHashes [][32]byte // per-chunk BLAKE3 hashes (in order)
}

// TransferConfig configures the transfer service.
type TransferConfig struct {
	ReceiveDir  string      // directory for received files
	MaxSize     int64       // max file size (0 = unlimited)
	ReceiveMode ReceiveMode // default: contacts
	Compress    bool        // enable zstd compression (default: true)
}

// TransferService manages chunked file transfers over libp2p streams.
type TransferService struct {
	receiveDir  string
	maxSize     int64
	receiveMode ReceiveMode
	compress    bool
	metrics     *Metrics
	events      *EventBus

	inboundSem chan struct{}

	mu          sync.RWMutex
	transfers   map[string]*TransferProgress
	completed   []string
	nextID      int
	peerInbound map[string]int
}

// NewTransferService creates a new chunked transfer service.
func NewTransferService(cfg TransferConfig, metrics *Metrics, events *EventBus) (*TransferService, error) {
	dir := cfg.ReceiveDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, "Downloads", "shurli")
	}

	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create receive directory %s: %w", dir, err)
	}

	mode := cfg.ReceiveMode
	if mode == "" {
		mode = ReceiveModeContacts
	}

	compress := cfg.Compress
	// Default to true if not explicitly set.
	if cfg.ReceiveMode == "" && !cfg.Compress {
		compress = true
	}

	return &TransferService{
		receiveDir:  dir,
		maxSize:     cfg.MaxSize,
		receiveMode: mode,
		compress:    compress,
		metrics:     metrics,
		events:      events,
		inboundSem:  make(chan struct{}, maxConcurrentTransfers),
		transfers:   make(map[string]*TransferProgress),
		peerInbound: make(map[string]int),
	}, nil
}

// --- Wire format ---

// writeManifest serializes and writes the SHFT manifest to w.
//
// Wire layout:
//
//	magic(4) + version(1) + flags(1) + nameLen(2) + name(var)
//	+ fileSize(8) + chunkCount(4) + rootHash(32)
//	+ chunkHashes(chunkCount * 32)
func writeManifest(w io.Writer, m *transferManifest) error {
	nameBytes := []byte(m.Filename)
	if len(nameBytes) > maxFilenameLen {
		return fmt.Errorf("filename too long: %d bytes", len(nameBytes))
	}
	if m.ChunkCount > maxChunkCount {
		return fmt.Errorf("too many chunks: %d", m.ChunkCount)
	}

	headerSize := 4 + 1 + 1 + 2 + len(nameBytes) + 8 + 4 + 32
	totalSize := headerSize + m.ChunkCount*32
	if totalSize > maxManifestSize {
		return fmt.Errorf("manifest too large: %d bytes", totalSize)
	}

	buf := make([]byte, headerSize)
	buf[0] = shftMagic0
	buf[1] = shftMagic1
	buf[2] = shftMagic2
	buf[3] = shftMagic3
	buf[4] = shftVersion
	buf[5] = m.Flags
	binary.BigEndian.PutUint16(buf[6:8], uint16(len(nameBytes)))
	copy(buf[8:8+len(nameBytes)], nameBytes)
	off := 8 + len(nameBytes)
	binary.BigEndian.PutUint64(buf[off:off+8], uint64(m.FileSize))
	binary.BigEndian.PutUint32(buf[off+8:off+12], uint32(m.ChunkCount))
	copy(buf[off+12:off+44], m.RootHash[:])

	// Write header.
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("write manifest header: %w", err)
	}

	// Write chunk hashes.
	for i := 0; i < m.ChunkCount; i++ {
		if _, err := w.Write(m.ChunkHashes[i][:]); err != nil {
			return fmt.Errorf("write chunk hash %d: %w", i, err)
		}
	}

	return nil
}

// readManifest reads and validates an SHFT manifest from r.
func readManifest(r io.Reader) (*transferManifest, error) {
	// Read magic + version + flags.
	var prefix [6]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read manifest prefix: %w", err)
	}

	if prefix[0] != shftMagic0 || prefix[1] != shftMagic1 ||
		prefix[2] != shftMagic2 || prefix[3] != shftMagic3 {
		return nil, fmt.Errorf("invalid magic bytes: not SHFT")
	}
	if prefix[4] != shftVersion {
		return nil, fmt.Errorf("unsupported SHFT version: %d (expected %d)", prefix[4], shftVersion)
	}

	m := &transferManifest{Flags: prefix[5]}

	// Read nameLen.
	var nameLenBuf [2]byte
	if _, err := io.ReadFull(r, nameLenBuf[:]); err != nil {
		return nil, fmt.Errorf("read name length: %w", err)
	}
	nameLen := binary.BigEndian.Uint16(nameLenBuf[:])
	if nameLen > maxFilenameLen {
		return nil, fmt.Errorf("filename too long: %d", nameLen)
	}

	// Read name + fileSize + chunkCount + rootHash.
	rest := make([]byte, int(nameLen)+8+4+32)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, fmt.Errorf("read manifest body: %w", err)
	}

	m.Filename = string(rest[:nameLen])
	m.FileSize = int64(binary.BigEndian.Uint64(rest[nameLen : nameLen+8]))
	m.ChunkCount = int(binary.BigEndian.Uint32(rest[nameLen+8 : nameLen+12]))
	copy(m.RootHash[:], rest[nameLen+12:nameLen+44])

	// Validate.
	if m.FileSize < 0 || m.FileSize > maxFileSize {
		return nil, fmt.Errorf("invalid file size: %d", m.FileSize)
	}
	if m.ChunkCount <= 0 || m.ChunkCount > maxChunkCount {
		return nil, fmt.Errorf("invalid chunk count: %d", m.ChunkCount)
	}

	// Sanitize filename.
	m.Filename = filepath.Base(m.Filename)
	if m.Filename == "." || m.Filename == ".." || m.Filename == "/" {
		return nil, fmt.Errorf("invalid filename: %q", m.Filename)
	}
	// Strip null bytes and control characters.
	m.Filename = sanitizeFilename(m.Filename)
	if m.Filename == "" {
		return nil, fmt.Errorf("filename is empty after sanitization")
	}

	// Read chunk hashes.
	m.ChunkHashes = make([][32]byte, m.ChunkCount)
	for i := 0; i < m.ChunkCount; i++ {
		if _, err := io.ReadFull(r, m.ChunkHashes[i][:]); err != nil {
			return nil, fmt.Errorf("read chunk hash %d: %w", i, err)
		}
	}

	return m, nil
}

// sanitizeFilename removes null bytes and control characters from a filename.
func sanitizeFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if r == 0 || r < 32 {
			continue // strip null bytes and control chars
		}
		b.WriteRune(r)
	}
	return b.String()
}

// writeChunkFrame writes a single chunk to the wire.
//
// Wire layout: type(1) + index(4) + dataLen(4) + data(var)
func writeChunkFrame(w io.Writer, index int, data []byte) error {
	if len(data) > maxChunkWireSize {
		return fmt.Errorf("chunk %d too large: %d bytes", index, len(data))
	}

	var header [9]byte
	header[0] = msgChunk
	binary.BigEndian.PutUint32(header[1:5], uint32(index))
	binary.BigEndian.PutUint32(header[5:9], uint32(len(data)))

	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(data)
	return err
}

// readChunkFrame reads a single chunk frame from the wire.
// Returns the chunk index and data. Validates bounds.
func readChunkFrame(r io.Reader) (int, []byte, error) {
	var header [9]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, fmt.Errorf("read chunk header: %w", err)
	}

	msgType := header[0]
	if msgType == msgTransferDone {
		return -1, nil, nil // sentinel: transfer complete
	}
	if msgType != msgChunk {
		return 0, nil, fmt.Errorf("unexpected message type: %d (expected chunk)", msgType)
	}

	index := int(binary.BigEndian.Uint32(header[1:5]))
	dataLen := int(binary.BigEndian.Uint32(header[5:9]))

	if dataLen > maxChunkWireSize {
		return 0, nil, fmt.Errorf("chunk %d data too large: %d bytes", index, dataLen)
	}

	data := make([]byte, dataLen)
	if _, err := io.ReadFull(r, data); err != nil {
		return 0, nil, fmt.Errorf("read chunk %d data: %w", index, err)
	}

	return index, data, nil
}

// writeMsg writes a single-byte message (accept/reject/done).
func writeMsg(w io.Writer, msgType byte) error {
	_, err := w.Write([]byte{msgType})
	return err
}

// readMsg reads a single-byte message.
func readMsg(r io.Reader) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}

// --- TransferService: Send ---

// chunkEntry holds a chunk's hash and wire data for sending.
type chunkEntry struct {
	hash       [32]byte
	data       []byte // possibly compressed
	compressed bool
}

// SendFile chunks, compresses, and sends a file over a libp2p stream.
// Runs in background; returns a progress tracker immediately.
func (ts *TransferService) SendFile(s network.Stream, filePath string) (*TransferProgress, error) {
	remotePeer := s.Conn().RemotePeer()

	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if stat.IsDir() {
		f.Close()
		return nil, fmt.Errorf("cannot send directory (directory transfer is Phase E)")
	}
	if stat.Size() > maxFileSize {
		f.Close()
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", stat.Size(), maxFileSize)
	}

	// Phase 1: Chunk the file and collect hashes + data.
	var chunks []chunkEntry
	var chunkHashes [][32]byte

	useCompression := ts.compress

	err = ChunkReader(f, stat.Size(), func(c Chunk) error {
		wireData := c.Data
		if useCompression {
			compressed, ok := compressChunk(c.Data)
			if ok {
				wireData = compressed
			}
		}
		chunks = append(chunks, chunkEntry{
			hash:       c.Hash,
			data:       wireData,
			compressed: useCompression && len(wireData) < len(c.Data),
		})
		chunkHashes = append(chunkHashes, c.Hash)
		return nil
	})
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("chunk file: %w", err)
	}
	f.Close()

	rootHash := MerkleRoot(chunkHashes)

	var flags uint8
	if useCompression {
		flags |= flagCompressed
	}

	manifest := &transferManifest{
		Filename:    filepath.Base(filePath),
		FileSize:    stat.Size(),
		ChunkCount:  len(chunks),
		Flags:       flags,
		RootHash:    rootHash,
		ChunkHashes: chunkHashes,
	}

	progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
		remotePeer.String(), "send", manifest.ChunkCount, useCompression)

	go func() {
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		err := ts.sendChunked(s, manifest, chunks, progress)
		progress.finish(err)
		ts.markCompleted(progress.ID)

		short := remotePeer.String()[:16] + "..."
		if err != nil {
			slog.Error("file-transfer: send failed",
				"peer", short, "file", manifest.Filename, "error", err)
		} else {
			slog.Info("file-transfer: sent",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "chunks", manifest.ChunkCount)
		}

		if ts.events != nil {
			ts.events.Emit(Event{
				Type:        EventStreamClosed,
				PeerID:      remotePeer,
				ServiceName: "file-transfer",
			})
		}
	}()

	return progress, nil
}

// sendChunked sends the manifest, waits for accept, then streams chunks.
func (ts *TransferService) sendChunked(w io.ReadWriter, m *transferManifest, chunks []chunkEntry, progress *TransferProgress) error {
	// Send manifest.
	if err := writeManifest(w, m); err != nil {
		return fmt.Errorf("send manifest: %w", err)
	}

	// Wait for accept/reject.
	resp, err := readMsg(w)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp == msgReject {
		return fmt.Errorf("peer rejected transfer")
	}
	if resp != msgAccept {
		return fmt.Errorf("unexpected response: %d", resp)
	}

	progress.setStatus("active")

	// Stream chunks.
	var totalSent int64
	for i, c := range chunks {
		if err := writeChunkFrame(w, i, c.data); err != nil {
			return fmt.Errorf("send chunk %d: %w", i, err)
		}
		totalSent += int64(len(c.data))
		progress.updateChunks(totalSent, i+1)
	}

	// Signal completion.
	return writeMsg(w, msgTransferDone)
}

// --- TransferService: Receive ---

// HandleInbound returns a StreamHandler for receiving chunked files.
func (ts *TransferService) HandleInbound() StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()

		remotePeer := s.Conn().RemotePeer()
		short := remotePeer.String()[:16] + "..."
		peerKey := remotePeer.String()

		// Receive mode check.
		if ts.receiveMode == ReceiveModeOff {
			slog.Debug("file-transfer: receive mode off, rejecting", "peer", short)
			writeMsg(s, msgReject)
			return
		}

		// Global capacity check.
		select {
		case ts.inboundSem <- struct{}{}:
			defer func() { <-ts.inboundSem }()
		default:
			slog.Warn("file-transfer: at capacity, rejecting",
				"peer", short, "max", maxConcurrentTransfers)
			writeMsg(s, msgReject)
			return
		}

		// Per-peer limit.
		ts.mu.Lock()
		if ts.peerInbound[peerKey] >= maxPerPeerTransfers {
			ts.mu.Unlock()
			slog.Warn("file-transfer: per-peer limit reached",
				"peer", short, "max", maxPerPeerTransfers)
			writeMsg(s, msgReject)
			return
		}
		ts.peerInbound[peerKey]++
		ts.mu.Unlock()
		defer func() {
			ts.mu.Lock()
			ts.peerInbound[peerKey]--
			if ts.peerInbound[peerKey] <= 0 {
				delete(ts.peerInbound, peerKey)
			}
			ts.mu.Unlock()
		}()

		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Read manifest.
		manifest, err := readManifest(s)
		if err != nil {
			slog.Warn("file-transfer: bad manifest", "peer", short, "error", err)
			writeMsg(s, msgReject)
			return
		}

		// Enforce size limit.
		if ts.maxSize > 0 && manifest.FileSize > ts.maxSize {
			slog.Warn("file-transfer: file too large",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "max", ts.maxSize)
			writeMsg(s, msgReject)
			return
		}

		// Pre-accept disk space check.
		if err := ts.checkDiskSpace(manifest.FileSize); err != nil {
			slog.Warn("file-transfer: insufficient disk space",
				"peer", short, "file", manifest.Filename, "error", err)
			writeMsg(s, msgReject)
			return
		}

		// Verify Merkle root matches chunk hashes.
		computedRoot := MerkleRoot(manifest.ChunkHashes)
		if computedRoot != manifest.RootHash {
			slog.Warn("file-transfer: manifest root hash mismatch", "peer", short)
			writeMsg(s, msgReject)
			return
		}

		slog.Info("file-transfer: receiving",
			"peer", short, "file", manifest.Filename,
			"size", manifest.FileSize, "chunks", manifest.ChunkCount,
			"compressed", manifest.Flags&flagCompressed != 0)

		// Accept.
		if err := writeMsg(s, msgAccept); err != nil {
			slog.Error("file-transfer: accept write failed", "error", err)
			return
		}

		compressed := manifest.Flags&flagCompressed != 0
		progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
			peerKey, "receive", manifest.ChunkCount, compressed)
		progress.setStatus("active")

		// Receive chunks.
		err = ts.receiveChunked(s, manifest, progress)
		progress.finish(err)
		ts.markCompleted(progress.ID)

		if err != nil {
			slog.Error("file-transfer: receive failed",
				"peer", short, "file", manifest.Filename, "error", err)
		} else {
			slog.Info("file-transfer: received",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize,
				"path", filepath.Join(ts.receiveDir, manifest.Filename))
		}

		if ts.events != nil {
			ts.events.Emit(Event{
				Type:        EventStreamClosed,
				PeerID:      remotePeer,
				ServiceName: "file-transfer",
			})
		}
	}
}

// receiveChunked reads chunks, verifies each hash, and assembles the file.
func (ts *TransferService) receiveChunked(r io.Reader, m *transferManifest, progress *TransferProgress) error {
	// Create temp file for atomic write.
	tmpPath, tmpFile, err := ts.createTempFile(m.Filename)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	defer func() {
		tmpFile.Close()
		os.Remove(tmpPath) // cleanup on error; no-op if renamed
	}()

	compressed := m.Flags&flagCompressed != 0
	var totalWritten int64

	for i := 0; i < m.ChunkCount; i++ {
		index, wireData, err := readChunkFrame(r)
		if err != nil {
			return fmt.Errorf("read chunk: %w", err)
		}
		if index == -1 {
			// Premature done signal.
			return fmt.Errorf("sender signaled done after %d/%d chunks", i, m.ChunkCount)
		}
		if index != i {
			return fmt.Errorf("chunk out of order: got %d, expected %d", index, i)
		}

		// Decompress if needed.
		chunkData := wireData
		if compressed {
			// Compression bomb check: limit decompressed size.
			maxDecomp := len(wireData) * maxDecompressRatio
			if maxDecomp > maxDecompressedChunk {
				maxDecomp = maxDecompressedChunk
			}
			decompressed, decErr := decompressChunk(wireData, maxDecomp)
			if decErr != nil {
				// May be uncompressed if compression was skipped for this chunk.
				// Try using raw data and verify hash below.
				chunkData = wireData
			} else {
				chunkData = decompressed
			}
		}

		// Verify chunk hash BEFORE writing to disk.
		hash := blake3Hash(chunkData)
		if hash != m.ChunkHashes[i] {
			return fmt.Errorf("chunk %d hash mismatch: corrupted", i)
		}

		// Re-check disk space periodically (every 64 chunks).
		if i%64 == 0 && i > 0 {
			remaining := m.FileSize - totalWritten
			if err := ts.checkDiskSpace(remaining); err != nil {
				return fmt.Errorf("disk space check at chunk %d: %w", i, err)
			}
		}

		// Write to disk.
		if _, err := tmpFile.Write(chunkData); err != nil {
			return fmt.Errorf("write chunk %d: %w", i, err)
		}
		totalWritten += int64(len(chunkData))

		progress.updateChunks(totalWritten, i+1)
	}

	// Read the done message.
	doneIdx, _, err := readChunkFrame(r)
	if err != nil {
		return fmt.Errorf("read done signal: %w", err)
	}
	if doneIdx != -1 {
		return fmt.Errorf("expected done signal, got chunk %d", doneIdx)
	}

	// Flush to disk.
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("sync file: %w", err)
	}
	tmpFile.Close()

	// Atomic rename to final path.
	finalPath, err := ts.finalPath(m.Filename)
	if err != nil {
		return fmt.Errorf("determine final path: %w", err)
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename temp to final: %w", err)
	}

	// Set file permissions.
	os.Chmod(finalPath, 0644)

	return nil
}

// blake3Hash computes BLAKE3-256 of data.
func blake3Hash(data []byte) [32]byte {
	// Import is in chunker.go; use zeebo/blake3 directly.
	return blake3Sum(data)
}

// createTempFile creates a temporary file in the receive directory.
func (ts *TransferService) createTempFile(filename string) (string, *os.File, error) {
	tmpPath := filepath.Join(ts.receiveDir, ".shurli-tmp-"+randomHex(8)+"-"+filename)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, err
	}
	return tmpPath, f, nil
}

// finalPath determines a non-colliding final path for the received file.
func (ts *TransferService) finalPath(filename string) (string, error) {
	path := filepath.Join(ts.receiveDir, filename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		f.Close()
		os.Remove(path) // remove the empty file; rename will replace it
		return path, nil
	}
	if !os.IsExist(err) {
		return "", err
	}

	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(ts.receiveDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		f, err = os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			f.Close()
			os.Remove(candidate)
			return candidate, nil
		}
		if !os.IsExist(err) {
			return "", err
		}
	}
	return path, nil // fall back to overwrite
}

// checkDiskSpace verifies that the receive directory has enough free space.
func (ts *TransferService) checkDiskSpace(needed int64) error {
	var stat unix.Statfs_t
	if err := unix.Statfs(ts.receiveDir, &stat); err != nil {
		return fmt.Errorf("statfs: %w", err)
	}
	available := int64(stat.Bavail) * int64(stat.Bsize)
	// Require at least 10% headroom above needed.
	required := needed + needed/10
	if available < required {
		return fmt.Errorf("insufficient disk space: need %d bytes, have %d", required, available)
	}
	return nil
}

// randomHex returns n random hex bytes.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- Transfer tracking ---

func (ts *TransferService) trackTransfer(filename string, size int64, peerID, direction string, chunkCount int, compressed bool) *TransferProgress {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	for len(ts.transfers) >= maxTrackedTransfers {
		before := len(ts.transfers)
		ts.evictCompleted()
		if len(ts.transfers) >= before {
			break
		}
	}

	ts.nextID++
	id := fmt.Sprintf("xfer-%d", ts.nextID)

	p := &TransferProgress{
		ID:          id,
		Filename:    filename,
		Size:        size,
		ChunksTotal: chunkCount,
		Compressed:  compressed,
		PeerID:      peerID,
		Direction:   direction,
		Status:      "pending",
		StartTime:   time.Now(),
	}
	ts.transfers[id] = p
	return p
}

func (ts *TransferService) evictCompleted() {
	for len(ts.completed) > 0 {
		id := ts.completed[0]
		ts.completed = ts.completed[1:]
		if _, ok := ts.transfers[id]; ok {
			delete(ts.transfers, id)
			return
		}
	}
	if len(ts.completed) == 0 && cap(ts.completed) > maxTrackedTransfers {
		ts.completed = nil
	}
}

func (ts *TransferService) markCompleted(id string) {
	ts.mu.Lock()
	ts.completed = append(ts.completed, id)
	ts.mu.Unlock()
}

// GetTransfer returns the progress of a transfer by ID.
func (ts *TransferService) GetTransfer(id string) (*TransferProgress, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	p, ok := ts.transfers[id]
	return p, ok
}

// ListTransfers returns snapshots of all tracked transfers.
func (ts *TransferService) ListTransfers() []TransferProgress {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]TransferProgress, 0, len(ts.transfers))
	for _, p := range ts.transfers {
		result = append(result, p.Snapshot())
	}
	return result
}

// SetReceiveMode changes the receive mode at runtime.
func (ts *TransferService) SetReceiveMode(mode ReceiveMode) {
	ts.mu.Lock()
	ts.receiveMode = mode
	ts.mu.Unlock()
}

// ReceiveDir returns the receive directory path.
func (ts *TransferService) ReceiveDir() string {
	return ts.receiveDir
}
