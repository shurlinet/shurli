package p2pnet

import (
	"bufio"
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
	msgChunk           = 0x04 // sender -> receiver: chunk data
	msgTransferDone    = 0x05 // sender -> receiver: all chunks sent
	msgResumeRequest   = 0x06 // receiver -> sender: resume with bitfield
	msgResumeResponse  = 0x07 // sender -> receiver: resume acknowledged
	msgRejectReason    = 0x08 // receiver -> sender: reject with reason byte
	msgWorkerHello     = 0x09 // sender -> receiver: parallel worker stream identification

	// Reject reasons (sent after msgRejectReason).
	RejectReasonNone  byte = 0x00 // no reason disclosed (same as silent msgReject)
	RejectReasonSpace byte = 0x01 // insufficient disk space
	RejectReasonBusy  byte = 0x02 // receiver busy
	RejectReasonSize  byte = 0x03 // file too large

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
	ChunkSizes  []uint32   // per-chunk decompressed sizes (for sparse writes)

	// Erasure coding (only present if flagErasureCoded is set in Flags).
	StripeSize   int        // data chunks per RS stripe
	ParityCount  int        // total parity chunks
	ParityHashes [][32]byte // per-parity BLAKE3 hashes
	ParitySizes  []uint32   // per-parity shard sizes
}

// TransferConfig configures the transfer service.
type TransferConfig struct {
	ReceiveDir      string      // directory for received files
	MaxSize         int64       // max file size (0 = unlimited)
	ReceiveMode     ReceiveMode // default: contacts
	Compress        bool        // enable zstd compression (default: true)
	ErasureOverhead float64     // RS parity overhead (0.10 = 10%, 0 = disabled)
}

// PendingTransfer represents an inbound transfer waiting for user approval in ask mode.
type PendingTransfer struct {
	ID       string    `json:"id"`
	Filename string    `json:"filename"`
	Size     int64     `json:"size"`
	PeerID   string    `json:"peer_id"`
	Time     time.Time `json:"time"`

	// Internal: channel for approval decision. Not serialized.
	decision chan transferDecision
}

// transferDecision carries the user's accept/reject decision for a pending transfer.
type transferDecision struct {
	accept bool
	reason byte   // reject reason (only meaningful if !accept)
	dest   string // override receive directory (only meaningful if accept)
}

// RejectReasonString returns a human-readable string for a reject reason byte.
func RejectReasonString(reason byte) string {
	switch reason {
	case RejectReasonSpace:
		return "insufficient disk space"
	case RejectReasonBusy:
		return "receiver busy"
	case RejectReasonSize:
		return "file too large"
	default:
		return "declined"
	}
}

// TransferService manages chunked file transfers over libp2p streams.
type TransferService struct {
	receiveDir      string
	maxSize         int64
	receiveMode     ReceiveMode
	compress        bool
	erasureOverhead float64
	metrics         *Metrics
	events          *EventBus

	inboundSem chan struct{}

	mu          sync.RWMutex
	transfers   map[string]*TransferProgress
	completed   []string
	nextID      int
	peerInbound      map[string]int
	pending          map[string]*PendingTransfer // ask mode: transfers awaiting approval
	parallelSessions map[[32]byte]*parallelSession
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
		receiveDir:      dir,
		maxSize:         cfg.MaxSize,
		receiveMode:     mode,
		compress:        compress,
		erasureOverhead: cfg.ErasureOverhead,
		metrics:         metrics,
		events:          events,
		inboundSem:      make(chan struct{}, maxConcurrentTransfers),
		transfers:       make(map[string]*TransferProgress),
		peerInbound:     make(map[string]int),
		pending:         make(map[string]*PendingTransfer),
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

	if len(m.ChunkSizes) != m.ChunkCount {
		return fmt.Errorf("chunk sizes count mismatch: %d sizes for %d chunks", len(m.ChunkSizes), m.ChunkCount)
	}

	headerSize := 4 + 1 + 1 + 2 + len(nameBytes) + 8 + 4 + 32
	totalSize := headerSize + m.ChunkCount*32 + m.ChunkCount*4 // hashes + sizes
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

	// Write chunk sizes (decompressed, for sparse writes and resume).
	sizeBuf := make([]byte, 4)
	for i := 0; i < m.ChunkCount; i++ {
		binary.BigEndian.PutUint32(sizeBuf, m.ChunkSizes[i])
		if _, err := w.Write(sizeBuf); err != nil {
			return fmt.Errorf("write chunk size %d: %w", i, err)
		}
	}

	// Write erasure coding fields (only if flagErasureCoded).
	if m.Flags&flagErasureCoded != 0 {
		if err := writeErasureManifest(w, m.StripeSize, m.ParityCount, m.ParityHashes, m.ParitySizes); err != nil {
			return fmt.Errorf("write erasure manifest: %w", err)
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

	// Read chunk sizes.
	m.ChunkSizes = make([]uint32, m.ChunkCount)
	sizeBuf := make([]byte, 4)
	for i := 0; i < m.ChunkCount; i++ {
		if _, err := io.ReadFull(r, sizeBuf); err != nil {
			return nil, fmt.Errorf("read chunk size %d: %w", i, err)
		}
		m.ChunkSizes[i] = binary.BigEndian.Uint32(sizeBuf)
	}

	// Read erasure coding fields (only if flagErasureCoded).
	if m.Flags&flagErasureCoded != 0 {
		ss, ph, ps, err := readErasureManifest(r)
		if err != nil {
			return nil, fmt.Errorf("read erasure manifest: %w", err)
		}
		m.StripeSize = ss
		m.ParityCount = len(ph)
		m.ParityHashes = ph
		m.ParitySizes = ps
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

// writeRejectWithReason writes msgRejectReason followed by a reason byte.
func writeRejectWithReason(w io.Writer, reason byte) error {
	_, err := w.Write([]byte{msgRejectReason, reason})
	return err
}

// --- TransferService: Send ---

// chunkEntry holds a chunk's hash and wire data for sending.
type chunkEntry struct {
	hash       [32]byte
	data       []byte // possibly compressed
	compressed bool
}

// SendOptions configures a single send operation.
type SendOptions struct {
	NoCompress   bool         // override: disable compression for this transfer
	Streams      int          // parallel stream count (0 = adaptive default based on transport)
	StreamOpener streamOpener // opens additional streams to the same peer (required for parallel)
}

// SendFile chunks, compresses, and sends a file over a libp2p stream.
// Runs in background; returns a progress tracker immediately.
func (ts *TransferService) SendFile(s network.Stream, filePath string, opts ...SendOptions) (*TransferProgress, error) {
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

	// Phase 1: Chunk the file and collect hashes + data + sizes.
	var chunks []chunkEntry
	var chunkHashes [][32]byte
	var chunkSizes []uint32

	useCompression := ts.compress
	if len(opts) > 0 && opts[0].NoCompress {
		useCompression = false
	}

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
		chunkSizes = append(chunkSizes, uint32(len(c.Data)))
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
		ChunkSizes:  chunkSizes,
	}

	// Phase 2: Erasure coding (transport-aware).
	// Auto-enable on Direct WAN, OFF on LAN (reliable), relay already blocked.
	var parityEntries []parityChunk
	useErasure := ts.erasureOverhead > 0
	if useErasure {
		transport := ClassifyTransport(s)
		if transport == TransportLAN {
			useErasure = false // LAN is reliable, skip erasure overhead
		}
	}
	if useErasure && len(chunks) > 0 {
		// Collect decompressed data for RS encoding.
		// Re-read file to get original chunk data (chunks[] holds wire data which may be compressed).
		dataForRS := make([][]byte, len(chunks))
		rsFile, err := os.Open(filePath)
		if err != nil {
			return nil, fmt.Errorf("reopen file for erasure: %w", err)
		}
		i := 0
		err = ChunkReader(rsFile, stat.Size(), func(c Chunk) error {
			dataForRS[i] = c.Data
			i++
			return nil
		})
		rsFile.Close()
		if err != nil {
			return nil, fmt.Errorf("rechunk for erasure: %w", err)
		}

		params := computeErasureParams(len(chunks), ts.erasureOverhead)
		parityEntries, err = encodeErasure(dataForRS, params.StripeSize, ts.erasureOverhead)
		if err != nil {
			return nil, fmt.Errorf("erasure encode: %w", err)
		}

		manifest.Flags |= flagErasureCoded
		manifest.StripeSize = params.StripeSize
		manifest.ParityCount = len(parityEntries)
		manifest.ParityHashes = make([][32]byte, len(parityEntries))
		manifest.ParitySizes = make([]uint32, len(parityEntries))
		for j, p := range parityEntries {
			manifest.ParityHashes[j] = p.hash
			manifest.ParitySizes[j] = uint32(len(p.data))
		}
	}

	progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
		remotePeer.String(), "send", manifest.ChunkCount, useCompression)

	// Determine parallel stream count.
	// TODO: parallel receive is not yet wired (registerParallelSession never called
	// from the receive path), so force single stream until the receive side is complete.
	var opener streamOpener
	if len(opts) > 0 {
		opener = opts[0].StreamOpener
	}
	numStreams := 1

	go func() {
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		var err error
		if numStreams > 1 && opener != nil {
			err = ts.sendParallel(s, opener, manifest, chunks, parityEntries, progress, numStreams)
		} else {
			err = ts.sendChunked(s, manifest, chunks, parityEntries, progress)
		}
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

// sendChunked sends the manifest, waits for accept or resume, then streams chunks.
func (ts *TransferService) sendChunked(w io.ReadWriter, m *transferManifest, chunks []chunkEntry, parity []parityChunk, progress *TransferProgress) error {
	// Send manifest.
	if err := writeManifest(w, m); err != nil {
		return fmt.Errorf("send manifest: %w", err)
	}

	// Wait for accept/reject/resume.
	resp, err := readMsg(w)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	switch resp {
	case msgReject:
		return fmt.Errorf("peer rejected transfer")

	case msgRejectReason:
		// Announced reject: read the reason byte.
		reasonByte, err := readMsg(w)
		if err != nil {
			return fmt.Errorf("peer rejected transfer (could not read reason)")
		}
		return fmt.Errorf("peer rejected transfer: %s", RejectReasonString(reasonByte))

	case msgResumeRequest:
		// Peer has a partial checkpoint. Read the bitfield and send only missing chunks.
		bfData, err := readResumePayload(w)
		if err != nil {
			return fmt.Errorf("read resume payload: %w", err)
		}

		have := &bitfield{
			bits: make([]byte, (m.ChunkCount+7)/8),
			n:    m.ChunkCount,
		}
		copy(have.bits, bfData)

		// Acknowledge the resume.
		if err := writeMsg(w, msgResumeResponse); err != nil {
			return fmt.Errorf("send resume response: %w", err)
		}

		progress.setStatus("active")

		skipped := have.count()
		slog.Info("file-transfer: resuming",
			"file", m.Filename, "have", skipped, "total", m.ChunkCount,
			"remaining", m.ChunkCount-skipped)

		// Send only missing data chunks.
		var totalSent int64
		sent := 0
		for i, c := range chunks {
			if have.has(i) {
				continue
			}
			if err := writeChunkFrame(w, i, c.data); err != nil {
				return fmt.Errorf("send chunk %d: %w", i, err)
			}
			totalSent += int64(len(c.data))
			sent++
			progress.updateChunks(totalSent, skipped+sent)
		}

		// Send parity chunks (always resent on resume for reconstruction).
		if err := sendParityChunks(w, parity, m.ChunkCount); err != nil {
			return err
		}

		return writeMsg(w, msgTransferDone)

	case msgAccept:
		// Normal: send all data chunks.
		progress.setStatus("active")

		var totalSent int64
		for i, c := range chunks {
			if err := writeChunkFrame(w, i, c.data); err != nil {
				return fmt.Errorf("send chunk %d: %w", i, err)
			}
			totalSent += int64(len(c.data))
			progress.updateChunks(totalSent, i+1)
		}

		// Send parity chunks after data chunks.
		if err := sendParityChunks(w, parity, m.ChunkCount); err != nil {
			return err
		}

		return writeMsg(w, msgTransferDone)

	default:
		return fmt.Errorf("unexpected response: %d", resp)
	}
}

// sendParityChunks sends parity chunks with indices starting at dataCount.
func sendParityChunks(w io.Writer, parity []parityChunk, dataCount int) error {
	for i, p := range parity {
		if err := writeChunkFrame(w, dataCount+i, p.data); err != nil {
			return fmt.Errorf("send parity chunk %d: %w", i, err)
		}
	}
	return nil
}

// --- TransferService: Receive ---

// HandleInbound returns a StreamHandler for receiving chunked files.
func (ts *TransferService) HandleInbound() StreamHandler {
	return func(serviceName string, s network.Stream) {
		// Peek the first byte to detect parallel worker streams.
		// Worker streams start with msgWorkerHello and are ancillary to an
		// already-accepted control stream, so they skip all normal checks.
		// Peek the first byte to detect parallel worker streams.
		// Worker streams start with msgWorkerHello and are ancillary to an
		// already-accepted control stream, so they skip all normal checks.
		br := bufio.NewReaderSize(s, 4096)
		firstByte, peekErr := br.Peek(1)
		if peekErr == nil && firstByte[0] == msgWorkerHello {
			ts.handleWorkerStreamFromReader(s, br)
			return
		}

		// Use a combined reader/writer: reads from br (which replays the peeked byte),
		// writes to s directly.
		rw := struct {
			io.Reader
			io.Writer
		}{br, s}

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
			writeRejectWithReason(s, RejectReasonBusy)
			return
		}

		// Per-peer limit.
		ts.mu.Lock()
		if ts.peerInbound[peerKey] >= maxPerPeerTransfers {
			ts.mu.Unlock()
			slog.Warn("file-transfer: per-peer limit reached",
				"peer", short, "max", maxPerPeerTransfers)
			writeRejectWithReason(s, RejectReasonBusy)
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
		manifest, err := readManifest(rw)
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
			writeRejectWithReason(s, RejectReasonSize)
			return
		}

		// Pre-accept disk space check.
		if err := ts.checkDiskSpace(manifest.FileSize); err != nil {
			slog.Warn("file-transfer: insufficient disk space",
				"peer", short, "file", manifest.Filename, "error", err)
			writeRejectWithReason(s, RejectReasonSpace)
			return
		}

		// Verify Merkle root matches chunk hashes.
		computedRoot := MerkleRoot(manifest.ChunkHashes)
		if computedRoot != manifest.RootHash {
			slog.Warn("file-transfer: manifest root hash mismatch", "peer", short)
			writeMsg(s, msgReject)
			return
		}

		// Check for existing checkpoint (resume support).
		var ckpt *transferCheckpoint
		ckpt, _ = loadCheckpoint(ts.receiveDir, manifest.RootHash)
		if ckpt != nil {
			// Validate checkpoint matches this manifest.
			if ckpt.manifest.ChunkCount != manifest.ChunkCount ||
				ckpt.manifest.FileSize != manifest.FileSize {
				// Stale checkpoint, discard it.
				removeCheckpoint(ts.receiveDir, manifest.RootHash)
				os.Remove(ckpt.tmpPath)
				ckpt = nil
			} else if _, err := os.Stat(ckpt.tmpPath); err != nil {
				// Tmp file gone, discard checkpoint.
				removeCheckpoint(ts.receiveDir, manifest.RootHash)
				ckpt = nil
			}
		}

		compressed := manifest.Flags&flagCompressed != 0

		if ckpt != nil {
			// Resume: send resume request with bitfield.
			slog.Info("file-transfer: resuming",
				"peer", short, "file", manifest.Filename,
				"have", ckpt.have.count(), "total", manifest.ChunkCount)

			if err := writeResumeRequest(rw, ckpt.have); err != nil {
				slog.Error("file-transfer: resume request failed", "error", err)
				return
			}

			// Wait for resume response.
			resp, err := readMsg(rw)
			if err != nil || resp != msgResumeResponse {
				slog.Error("file-transfer: resume response failed",
					"error", err, "resp", resp)
				return
			}
		} else if ts.receiveMode == ReceiveModeAsk {
			// Ask mode: queue for manual approval with timeout.
			pendingID := fmt.Sprintf("pending-%d-%s", time.Now().UnixNano(), randomHex(4))
			pt := &PendingTransfer{
				ID:       pendingID,
				Filename: manifest.Filename,
				Size:     manifest.FileSize,
				PeerID:   peerKey,
				Time:     time.Now(),
				decision: make(chan transferDecision, 1),
			}

			ts.mu.Lock()
			ts.pending[pendingID] = pt
			ts.mu.Unlock()

			slog.Info("file-transfer: awaiting approval",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "id", pendingID)

			if ts.events != nil {
				ts.events.Emit(Event{
					Type:        EventTransferPending,
					PeerID:      remotePeer,
					ServiceName: "file-transfer",
					Detail:      pendingID,
				})
			}

			// Wait for decision or timeout.
			timer := time.NewTimer(askModeTimeout)
			defer timer.Stop()

			var decision transferDecision
			select {
			case decision = <-pt.decision:
				// User decided.
			case <-timer.C:
				// Timeout: silent reject.
				decision = transferDecision{accept: false, reason: RejectReasonBusy}
				slog.Info("file-transfer: ask mode timeout, rejecting",
					"peer", short, "file", manifest.Filename, "id", pendingID)
			}

			ts.removePending(pendingID)

			if !decision.accept {
				if decision.reason != RejectReasonNone {
					writeRejectWithReason(s, decision.reason)
				} else {
					writeMsg(s, msgReject)
				}
				return
			}

			// Override receive dir if specified.
			if decision.dest != "" {
				// Validate the override directory exists.
				if info, err := os.Stat(decision.dest); err != nil || !info.IsDir() {
					slog.Error("file-transfer: invalid accept dest", "dest", decision.dest)
					writeMsg(s, msgReject)
					return
				}
			}

			slog.Info("file-transfer: approved",
				"peer", short, "file", manifest.Filename, "id", pendingID)

			if err := writeMsg(s, msgAccept); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		} else {
			slog.Info("file-transfer: receiving",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "chunks", manifest.ChunkCount,
				"compressed", compressed)

			// Accept (fresh transfer - contacts/open mode).
			if err := writeMsg(s, msgAccept); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		}

		progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
			peerKey, "receive", manifest.ChunkCount, compressed)
		progress.setStatus("active")

		// Receive chunks (with resume support).
		err = ts.receiveChunked(rw, manifest, progress, ckpt)
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
// Supports both fresh transfers and resume from a checkpoint.
// Chunks may arrive out of order; writes use WriteAt with precomputed offsets.
// On interruption, a checkpoint is saved for later resume.
func (ts *TransferService) receiveChunked(r io.Reader, m *transferManifest, progress *TransferProgress, ckpt *transferCheckpoint) error {
	offsets := buildOffsetTable(m.ChunkSizes)

	var tmpPath string
	var tmpFile *os.File
	var have *bitfield
	var transferErr error

	if ckpt != nil {
		// Resume from checkpoint.
		tmpPath = ckpt.tmpPath
		have = ckpt.have
		var err error
		tmpFile, err = os.OpenFile(tmpPath, os.O_WRONLY, 0600)
		if err != nil {
			// Tmp file gone despite earlier stat check (race). Start fresh.
			ckpt = nil
		}
	}

	if ckpt == nil {
		// Fresh transfer.
		var err error
		tmpPath, tmpFile, err = ts.createTempFile(m.Filename)
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		have = newBitfield(m.ChunkCount)

		// Pre-allocate file to full size for sparse writes.
		if err := tmpFile.Truncate(m.FileSize); err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return fmt.Errorf("pre-allocate file: %w", err)
		}
	}

	// On exit: save checkpoint on error, clean up on success.
	defer func() {
		tmpFile.Close()
		if transferErr != nil && have.count() > 0 {
			// Save checkpoint so we can resume later.
			cp := &transferCheckpoint{
				manifest: m,
				have:     have,
				tmpPath:  tmpPath,
			}
			if saveErr := cp.save(ts.receiveDir); saveErr != nil {
				slog.Error("file-transfer: save checkpoint failed", "error", saveErr)
			} else {
				slog.Info("file-transfer: checkpoint saved",
					"file", m.Filename,
					"have", have.count(), "total", m.ChunkCount)
			}
		} else if transferErr != nil {
			// Error before any chunks received. Clean up temp file.
			os.Remove(tmpPath)
		} else {
			// Success. Temp file already renamed; remove checkpoint.
			removeCheckpoint(ts.receiveDir, m.RootHash)
		}
	}()

	compressed := m.Flags&flagCompressed != 0
	hasErasure := m.Flags&flagErasureCoded != 0
	totalExpected := m.ChunkCount + m.ParityCount // data + parity frames on wire

	// Parity chunk storage (in memory, not written to file).
	var parityData map[int][]byte
	if hasErasure && m.ParityCount > 0 {
		parityData = make(map[int][]byte, m.ParityCount)
	}

	// Track corrupted data chunks (for RS reconstruction).
	var corrupted []int

	// Seed progress with already-received data from checkpoint.
	var totalWritten int64
	if have.count() > 0 {
		for i := 0; i < m.ChunkCount; i++ {
			if have.has(i) {
				totalWritten += int64(m.ChunkSizes[i])
			}
		}
		progress.updateChunks(totalWritten, have.count())
	}

	// Receive data + parity chunks.
	framesRead := 0
	for framesRead < totalExpected-have.count() {
		index, wireData, err := readChunkFrame(r)
		if err != nil {
			transferErr = fmt.Errorf("read chunk: %w", err)
			return transferErr
		}
		if index == -1 {
			break // done signal
		}

		// Parity chunk (index >= ChunkCount).
		if index >= m.ChunkCount && index < m.ChunkCount+m.ParityCount {
			parityIdx := index - m.ChunkCount
			hash := blake3Sum(wireData)
			if hash != m.ParityHashes[parityIdx] {
				slog.Warn("file-transfer: parity chunk hash mismatch, skipping",
					"index", parityIdx)
			} else {
				parityData[parityIdx] = wireData
			}
			framesRead++
			continue
		}

		// Validate data chunk index bounds.
		if index < 0 || index >= m.ChunkCount {
			transferErr = fmt.Errorf("chunk index out of range: %d", index)
			return transferErr
		}
		if have.has(index) {
			framesRead++
			continue // duplicate, skip
		}

		// Decompress if needed.
		chunkData := wireData
		if compressed {
			maxDecomp := len(wireData) * maxDecompressRatio
			if maxDecomp > maxDecompressedChunk {
				maxDecomp = maxDecompressedChunk
			}
			decompressed, decErr := decompressChunk(wireData, maxDecomp)
			if decErr != nil {
				chunkData = wireData
			} else {
				chunkData = decompressed
			}
		}

		// Verify chunk hash BEFORE writing to disk.
		hash := blake3Hash(chunkData)
		if hash != m.ChunkHashes[index] {
			if hasErasure {
				// With erasure: note corruption, attempt RS reconstruction later.
				corrupted = append(corrupted, index)
				framesRead++
				continue
			}
			transferErr = fmt.Errorf("chunk %d hash mismatch: corrupted", index)
			return transferErr
		}

		// Verify size matches manifest.
		if uint32(len(chunkData)) != m.ChunkSizes[index] {
			transferErr = fmt.Errorf("chunk %d size mismatch: got %d, expected %d",
				index, len(chunkData), m.ChunkSizes[index])
			return transferErr
		}

		// Re-check disk space periodically (every 64 chunks).
		if framesRead%64 == 0 && framesRead > 0 {
			remaining := m.FileSize - totalWritten
			if err := ts.checkDiskSpace(remaining); err != nil {
				transferErr = fmt.Errorf("disk space check at chunk %d: %w", index, err)
				return transferErr
			}
		}

		// Write at correct offset (sparse write).
		if _, err := tmpFile.WriteAt(chunkData, offsets[index]); err != nil {
			transferErr = fmt.Errorf("write chunk %d at offset %d: %w", index, offsets[index], err)
			return transferErr
		}

		have.set(index)
		framesRead++
		totalWritten += int64(len(chunkData))
		progress.updateChunks(totalWritten, have.count())
	}

	// Read the done message (if not already consumed by the loop).
	if framesRead > 0 {
		doneIdx, _, err := readChunkFrame(r)
		if err != nil {
			transferErr = fmt.Errorf("read done signal: %w", err)
			return transferErr
		}
		if doneIdx != -1 {
			transferErr = fmt.Errorf("expected done signal, got chunk %d", doneIdx)
			return transferErr
		}
	}

	// RS reconstruction for corrupted/missing data chunks.
	if len(corrupted) > 0 && hasErasure {
		slog.Info("file-transfer: attempting RS reconstruction",
			"corrupted", len(corrupted), "parity_available", len(parityData))

		if err := ts.rsReconstruct(tmpFile, m, offsets, corrupted, parityData); err != nil {
			transferErr = fmt.Errorf("RS reconstruction: %w", err)
			return transferErr
		}

		// Mark reconstructed chunks as received.
		for _, idx := range corrupted {
			have.set(idx)
			totalWritten += int64(m.ChunkSizes[idx])
		}
		progress.updateChunks(totalWritten, have.count())
	} else if have.missing() > 0 {
		transferErr = fmt.Errorf("transfer incomplete: %d chunks missing", have.missing())
		return transferErr
	}

	// Flush to disk.
	if err := tmpFile.Sync(); err != nil {
		transferErr = fmt.Errorf("sync file: %w", err)
		return transferErr
	}
	tmpFile.Close()

	// Atomic rename to final path.
	finalPath, err := ts.finalPath(m.Filename)
	if err != nil {
		transferErr = fmt.Errorf("determine final path: %w", err)
		return transferErr
	}
	if err := os.Rename(tmpPath, finalPath); err != nil {
		transferErr = fmt.Errorf("rename temp to final: %w", err)
		return transferErr
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

// checkDiskSpace is defined in diskspace_unix.go / diskspace_windows.go.

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

// --- Ask mode: pending transfer management ---

// ListPending returns snapshots of all pending transfers awaiting approval.
func (ts *TransferService) ListPending() []PendingTransfer {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]PendingTransfer, 0, len(ts.pending))
	for _, p := range ts.pending {
		result = append(result, PendingTransfer{
			ID:       p.ID,
			Filename: p.Filename,
			Size:     p.Size,
			PeerID:   p.PeerID,
			Time:     p.Time,
		})
	}
	return result
}

// AcceptTransfer approves a pending transfer. Optional dest overrides the receive directory.
func (ts *TransferService) AcceptTransfer(id, dest string) error {
	ts.mu.RLock()
	p, ok := ts.pending[id]
	ts.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no pending transfer %q", id)
	}

	select {
	case p.decision <- transferDecision{accept: true, dest: dest}:
		return nil
	default:
		return fmt.Errorf("transfer %q already decided or timed out", id)
	}
}

// RejectTransfer rejects a pending transfer with an optional reason.
func (ts *TransferService) RejectTransfer(id string, reason byte) error {
	ts.mu.RLock()
	p, ok := ts.pending[id]
	ts.mu.RUnlock()
	if !ok {
		return fmt.Errorf("no pending transfer %q", id)
	}

	select {
	case p.decision <- transferDecision{accept: false, reason: reason}:
		return nil
	default:
		return fmt.Errorf("transfer %q already decided or timed out", id)
	}
}

// removePending removes a pending transfer from the map.
func (ts *TransferService) removePending(id string) {
	ts.mu.Lock()
	delete(ts.pending, id)
	ts.mu.Unlock()
}
