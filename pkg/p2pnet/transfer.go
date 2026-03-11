package p2pnet

import (
	"bufio"
	"context"
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
	ReceiveModeTimed    ReceiveMode = "timed"     // temporarily open, reverts after duration
)

// rateBucket tracks transfer request count per peer within a fixed window.
type rateBucket struct {
	count     int
	windowEnd time.Time
}

// transferRateLimiter enforces per-peer transfer request rate limits.
type transferRateLimiter struct {
	mu        sync.Mutex
	peers     map[string]*rateBucket
	maxPerMin int           // max requests per 60s window
	window    time.Duration // window size (60s)
}

func newTransferRateLimiter(maxPerMin int) *transferRateLimiter {
	return &transferRateLimiter{
		peers:     make(map[string]*rateBucket),
		maxPerMin: maxPerMin,
		window:    60 * time.Second,
	}
}

// allow checks if a peer is within rate limits. Returns true if allowed.
func (rl *transferRateLimiter) allow(peerID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	b, ok := rl.peers[peerID]
	if !ok || now.After(b.windowEnd) {
		// New window.
		rl.peers[peerID] = &rateBucket{count: 1, windowEnd: now.Add(rl.window)}
		return true
	}

	b.count++
	return b.count <= rl.maxPerMin
}

// cleanup removes stale entries older than the window.
func (rl *transferRateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	for k, b := range rl.peers {
		if now.After(b.windowEnd) {
			delete(rl.peers, k)
		}
	}
}

// StreamInfo tracks per-stream progress for parallel transfers.
type StreamInfo struct {
	ChunksDone int   `json:"chunks_done"`
	BytesDone  int64 `json:"bytes_done"`
}

// TransferProgress tracks the progress of an active transfer.
type TransferProgress struct {
	ID          string    `json:"id"`
	Filename    string    `json:"filename"`
	Size        int64     `json:"size"`
	Transferred int64     `json:"transferred"`
	ChunksTotal int       `json:"chunks_total"`
	ChunksDone  int       `json:"chunks_done"`
	Compressed      bool      `json:"compressed"`
	CompressedSize  int64     `json:"compressed_size,omitempty"`  // total wire bytes (compressed)
	ErasureParity   int       `json:"erasure_parity,omitempty"`   // number of parity chunks (0 if disabled)
	ErasureOverhead float64   `json:"erasure_overhead,omitempty"` // configured overhead (e.g. 0.10)
	StreamProgress  []StreamInfo `json:"stream_progress,omitempty"` // per-stream progress (parallel only)
	PeerID          string    `json:"peer_id"`
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

// initStreams initializes per-stream progress counters for N streams.
func (p *TransferProgress) initStreams(n int) {
	p.mu.Lock()
	p.StreamProgress = make([]StreamInfo, n)
	p.mu.Unlock()
}

// updateStream increments a specific stream's counters.
// Grows the slice if needed (receive side discovers workers dynamically).
func (p *TransferProgress) updateStream(streamIdx int, chunkBytes int64) {
	if streamIdx < 0 {
		return
	}
	p.mu.Lock()
	for streamIdx >= len(p.StreamProgress) {
		p.StreamProgress = append(p.StreamProgress, StreamInfo{})
	}
	p.StreamProgress[streamIdx].ChunksDone++
	p.StreamProgress[streamIdx].BytesDone += chunkBytes
	p.mu.Unlock()
}

// addWireBytes adds n bytes to CompressedSize (tracks compressed wire bytes).
func (p *TransferProgress) addWireBytes(n int64) {
	p.mu.Lock()
	p.CompressedSize += n
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

// TransferSnapshot is a mutex-free copy of TransferProgress, safe for JSON
// serialization and value passing.
type TransferSnapshot struct {
	ID              string       `json:"id"`
	Filename        string       `json:"filename"`
	Size            int64        `json:"size"`
	Transferred     int64        `json:"transferred"`
	ChunksTotal     int          `json:"chunks_total"`
	ChunksDone      int          `json:"chunks_done"`
	Compressed      bool         `json:"compressed"`
	CompressedSize  int64        `json:"compressed_size,omitempty"`
	ErasureParity   int          `json:"erasure_parity,omitempty"`
	ErasureOverhead float64      `json:"erasure_overhead,omitempty"`
	StreamProgress  []StreamInfo `json:"stream_progress,omitempty"`
	PeerID          string       `json:"peer_id"`
	Direction       string       `json:"direction"`
	Status          string       `json:"status"`
	StartTime       time.Time    `json:"start_time"`
	Done            bool         `json:"done"`
	Error           string       `json:"error,omitempty"`
}

// Snapshot returns a mutex-free copy safe for JSON serialization.
func (p *TransferProgress) Snapshot() TransferSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snap := TransferSnapshot{
		ID: p.ID, Filename: p.Filename, Size: p.Size,
		Transferred: p.Transferred, ChunksTotal: p.ChunksTotal,
		ChunksDone: p.ChunksDone, Compressed: p.Compressed,
		CompressedSize: p.CompressedSize,
		ErasureParity: p.ErasureParity, ErasureOverhead: p.ErasureOverhead,
		PeerID: p.PeerID, Direction: p.Direction, Status: p.Status,
		StartTime: p.StartTime, Done: p.Done, Error: p.Error,
	}
	if len(p.StreamProgress) > 0 {
		snap.StreamProgress = make([]StreamInfo, len(p.StreamProgress))
		copy(snap.StreamProgress, p.StreamProgress)
	}
	return snap
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
	LogPath         string      // path for transfer event log (empty = disabled)
	Notify          string      // notification mode: "none" (default), "desktop", "command"
	NotifyCommand   string      // command template for "command" mode ({from}, {file}, {size})
	MaxConcurrent   int         // max concurrent outbound transfers (default: 5, min: 1)

	// Multi-peer swarming download using RaptorQ fountain codes.
	MultiPeerEnabled  bool  // enable multi-peer downloads (default: true)
	MultiPeerMaxPeers int   // max peers to download from simultaneously (default: 4)
	MultiPeerMinSize  int64 // min file size for multi-peer (default: 10 MB)

	RateLimit int // max transfer requests per peer per minute (default: 10, 0 = disabled)
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
	logger          *TransferLogger
	notifier        *TransferNotifier

	inboundSem chan struct{}

	// Outbound transfer queue with priority ordering and concurrency limit.
	queue      *TransferQueue
	queueReady chan struct{} // signaled when a queue slot frees up

	mu          sync.RWMutex
	transfers   map[string]*TransferProgress
	completed   []string
	nextID      int
	peerInbound      map[string]int
	pending          map[string]*PendingTransfer // ask mode: transfers awaiting approval
	parallelSessions map[[32]byte]*parallelSession

	// Timed mode: temporarily switches to open/contacts then reverts.
	timedCancel  context.CancelFunc // cancels the timer goroutine (nil = no active timer)
	timedGen     uint64             // generation counter to identify active timer
	timedPrevMode ReceiveMode       // mode to revert to when timer expires
	timedDeadline time.Time         // when the timer expires

	// Multi-peer download config.
	multiPeerEnabled  bool
	multiPeerMaxPeers int
	multiPeerMinSize  int64

	// Hash registry: maps root hash -> local file path for multi-peer serving.
	hashMu       sync.RWMutex
	hashRegistry map[[32]byte]string

	// Per-peer transfer request rate limiter (nil = disabled).
	rateLimiter   *transferRateLimiter
	rateLimiterStop context.CancelFunc
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

	var logger *TransferLogger
	if cfg.LogPath != "" {
		var err error
		logger, err = NewTransferLogger(cfg.LogPath)
		if err != nil {
			return nil, fmt.Errorf("transfer log: %w", err)
		}
	}

	notifier := NewTransferNotifier(cfg.Notify, cfg.NotifyCommand)

	maxConcurrent := cfg.MaxConcurrent
	if maxConcurrent < 1 {
		maxConcurrent = 5
	}

	multiPeerMaxPeers := cfg.MultiPeerMaxPeers
	if multiPeerMaxPeers < 1 {
		multiPeerMaxPeers = 4
	}
	multiPeerMinSize := cfg.MultiPeerMinSize
	if multiPeerMinSize <= 0 {
		multiPeerMinSize = 10 * 1024 * 1024 // 10 MB
	}

	ts := &TransferService{
		receiveDir:        dir,
		maxSize:           cfg.MaxSize,
		receiveMode:       mode,
		compress:          compress,
		erasureOverhead:   cfg.ErasureOverhead,
		metrics:           metrics,
		events:            events,
		logger:            logger,
		notifier:          notifier,
		inboundSem:        make(chan struct{}, maxConcurrentTransfers),
		queue:             NewTransferQueue(maxConcurrent),
		queueReady:        make(chan struct{}, 1),
		transfers:         make(map[string]*TransferProgress),
		peerInbound:       make(map[string]int),
		pending:           make(map[string]*PendingTransfer),
		multiPeerEnabled:  cfg.MultiPeerEnabled,
		multiPeerMaxPeers: multiPeerMaxPeers,
		multiPeerMinSize:  multiPeerMinSize,
		hashRegistry:      make(map[[32]byte]string),
	}

	// Per-peer rate limiter (default 10/min, negative = disabled).
	rateLimit := cfg.RateLimit
	if rateLimit == 0 {
		rateLimit = 10 // default
	}
	if rateLimit > 0 {
		ts.rateLimiter = newTransferRateLimiter(rateLimit)
		ctx, cancel := context.WithCancel(context.Background())
		ts.rateLimiterStop = cancel
		go func() {
			ticker := time.NewTicker(5 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					ts.rateLimiter.cleanup()
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	return ts, nil
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

	// Sanitize filename: preserve relative paths but strip traversal attacks.
	m.Filename = sanitizeRelativePath(m.Filename)
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

// sanitizeRelativePath cleans a relative path for safe use under a destination directory.
// It strips leading slashes, ".." components, empty segments, and backslashes.
// Returns only the base filename if the path resolves to something unsafe.
func sanitizeRelativePath(name string) string {
	// Normalize backslashes to forward slashes (Windows compat).
	name = strings.ReplaceAll(name, "\\", "/")

	var parts []string
	for _, part := range strings.Split(name, "/") {
		// Skip empty parts and current-dir markers.
		if part == "" || part == "." {
			continue
		}
		// Skip parent-dir traversal.
		if part == ".." {
			continue
		}
		// Strip null bytes and control characters from each component.
		part = sanitizeFilename(part)
		if part != "" {
			parts = append(parts, part)
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "/")
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
	// Read message type first (1 byte) before committing to the full 9-byte header.
	// The done signal (msgTransferDone) is only 1 byte via writeMsg, not a full frame.
	var typeByte [1]byte
	if _, err := io.ReadFull(r, typeByte[:]); err != nil {
		return 0, nil, fmt.Errorf("read chunk header: %w", err)
	}

	if typeByte[0] == msgTransferDone {
		return -1, nil, nil // sentinel: transfer complete
	}
	if typeByte[0] != msgChunk {
		return 0, nil, fmt.Errorf("unexpected message type: %d (expected chunk)", typeByte[0])
	}

	// Read remaining 8 bytes: index(4) + dataLen(4).
	var rest [8]byte
	if _, err := io.ReadFull(r, rest[:]); err != nil {
		return 0, nil, fmt.Errorf("read chunk header: %w", err)
	}

	index := int(binary.BigEndian.Uint32(rest[0:4]))
	dataLen := int(binary.BigEndian.Uint32(rest[4:8]))

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
	RelativeName string       // override manifest filename (e.g., "subdir/file.txt" for directory transfer)
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
		return nil, fmt.Errorf("cannot send directory directly; use SendDirectory()")
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

	manifestName := filepath.Base(filePath)
	if len(opts) > 0 && opts[0].RelativeName != "" {
		manifestName = opts[0].RelativeName
	}

	manifest := &transferManifest{
		Filename:    manifestName,
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

	// Set erasure info on progress for CLI display.
	if useErasure && len(parityEntries) > 0 {
		progress.mu.Lock()
		progress.ErasureParity = len(parityEntries)
		progress.ErasureOverhead = ts.erasureOverhead
		progress.mu.Unlock()
	}

	// Determine parallel stream count based on transport type.
	var opener streamOpener
	var requestedStreams int
	if len(opts) > 0 {
		opener = opts[0].StreamOpener
		requestedStreams = opts[0].Streams
	}
	transport := ClassifyTransport(s)
	numStreams := adaptiveStreamCount(transport, len(chunks), requestedStreams)

	go func() {
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		sendStart := time.Now()
		ts.logEvent(EventLogStarted, "send", remotePeer.String(), manifest.Filename, manifest.FileSize, 0, "", "")

		var err error
		if numStreams > 1 && opener != nil {
			err = ts.sendParallel(s, opener, manifest, chunks, parityEntries, progress, numStreams)
		} else {
			err = ts.sendChunked(s, manifest, chunks, parityEntries, progress)
		}
		progress.finish(err)
		ts.markCompleted(progress.ID)

		short := remotePeer.String()[:16] + "..."
		dur := time.Since(sendStart).Truncate(time.Millisecond).String()
		if err != nil {
			slog.Error("file-transfer: send failed",
				"peer", short, "file", manifest.Filename, "error", err)
			ts.logEvent(EventLogFailed, "send", remotePeer.String(), manifest.Filename, manifest.FileSize, progress.Sent(), err.Error(), dur)
		} else {
			slog.Info("file-transfer: sent",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "chunks", manifest.ChunkCount)
			ts.logEvent(EventLogCompleted, "send", remotePeer.String(), manifest.Filename, manifest.FileSize, manifest.FileSize, "", dur)
			// Register hash so this node can serve multi-peer requests for this file.
			ts.RegisterHash(manifest.RootHash, filePath)
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

// SendDirectory walks a directory and sends each regular file to the peer
// sequentially, preserving relative directory structure in filenames.
// openStream is called once per file to get a fresh stream.
// Returns progress trackers for all files sent.
func (ts *TransferService) SendDirectory(ctx context.Context, dirPath string, openStream func() (network.Stream, error), opts SendOptions) ([]*TransferProgress, error) {
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dirPath)
	}

	// Collect regular files with their relative paths.
	type fileEntry struct {
		absPath  string
		relPath  string
	}
	var files []fileEntry
	dirBase := filepath.Base(dirPath)

	err = filepath.WalkDir(dirPath, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// Skip symlinks, device files, sockets (regular files only).
		if !d.Type().IsRegular() {
			return nil
		}
		rel, relErr := filepath.Rel(dirPath, path)
		if relErr != nil {
			return relErr
		}
		// Prefix with directory name so receiver gets "mydir/subdir/file.txt".
		files = append(files, fileEntry{
			absPath: path,
			relPath: filepath.Join(dirBase, rel),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("directory is empty: %s", dirPath)
	}

	// Send each file sequentially, one stream per file.
	var allProgress []*TransferProgress
	for _, fe := range files {
		if ctx.Err() != nil {
			return allProgress, ctx.Err()
		}

		stream, streamErr := openStream()
		if streamErr != nil {
			return allProgress, fmt.Errorf("open stream for %s: %w", fe.relPath, streamErr)
		}

		fileOpts := opts
		// Use forward slashes in relative name for cross-platform wire format.
		fileOpts.RelativeName = filepath.ToSlash(fe.relPath)

		progress, sendErr := ts.SendFile(stream, fe.absPath, fileOpts)
		if sendErr != nil {
			stream.Close()
			return allProgress, fmt.Errorf("send %s: %w", fe.relPath, sendErr)
		}
		allProgress = append(allProgress, progress)

		// Wait for this file to complete before starting the next (sequential).
		for {
			snap := progress.Snapshot()
			if snap.Done {
				if snap.Error != "" {
					return allProgress, fmt.Errorf("transfer %s failed: %s", fe.relPath, snap.Error)
				}
				break
			}
			select {
			case <-ctx.Done():
				return allProgress, ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}

	return allProgress, nil
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
			progress.addWireBytes(int64(len(c.data)))
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
			progress.addWireBytes(int64(len(c.data)))
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

		recvStart := time.Now()

		// Receive mode check.
		if ts.receiveMode == ReceiveModeOff {
			slog.Debug("file-transfer: receive mode off, rejecting", "peer", short)
			writeMsg(s, msgReject)
			ts.logEvent(EventLogRejected, "receive", peerKey, "", 0, 0, "receive mode off", "")
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

		// Per-peer rate limit check (before parsing manifest to save CPU).
		if ts.rateLimiter != nil && !ts.rateLimiter.allow(peerKey) {
			slog.Warn("file-transfer: rate limit exceeded",
				"peer", short)
			ts.logEvent(EventLogSpamBlocked, "receive", peerKey, "", 0, 0, "rate limit exceeded", "")
			s.Reset()
			return
		}

		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Read manifest.
		manifest, err := readManifest(rw)
		if err != nil {
			slog.Warn("file-transfer: bad manifest", "peer", short, "error", err)
			writeMsg(s, msgReject)
			return
		}

		ts.logEvent(EventLogRequestReceived, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

		// Notify user about incoming transfer request.
		if ts.notifier != nil {
			if err := ts.notifier.Notify(peerKey, manifest.Filename, manifest.FileSize); err != nil {
				slog.Debug("file-transfer: notification failed", "error", err)
			}
		}

		// Enforce size limit.
		if ts.maxSize > 0 && manifest.FileSize > ts.maxSize {
			slog.Warn("file-transfer: file too large",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "max", ts.maxSize)
			writeRejectWithReason(s, RejectReasonSize)
			ts.logEvent(EventLogRejected, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "file too large", "")
			return
		}

		// Pre-accept disk space check.
		if err := ts.checkDiskSpace(manifest.FileSize); err != nil {
			slog.Warn("file-transfer: insufficient disk space",
				"peer", short, "file", manifest.Filename, "error", err)
			writeRejectWithReason(s, RejectReasonSpace)
			ts.logEvent(EventLogDiskSpaceRejected, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "insufficient disk space", "")
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
			ts.logEvent(EventLogResumed, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

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
			timedOut := false
			select {
			case decision = <-pt.decision:
				// User decided.
			case <-timer.C:
				// Timeout: silent reject.
				timedOut = true
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
				if timedOut {
					ts.logEvent(EventLogCancelled, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "ask mode timeout", "")
				} else {
					ts.logEvent(EventLogRejected, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "user rejected", "")
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
			ts.logEvent(EventLogAccepted, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

			if err := writeMsg(s, msgAccept); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		} else {
			slog.Info("file-transfer: receiving",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize, "chunks", manifest.ChunkCount,
				"compressed", compressed)
			ts.logEvent(EventLogAccepted, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

			// Accept (fresh transfer - contacts/open mode).
			if err := writeMsg(s, msgAccept); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		}

		progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
			peerKey, "receive", manifest.ChunkCount, compressed)
		progress.setStatus("active")
		ts.logEvent(EventLogStarted, "receive", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

		// Set up parallel receive session so worker streams can deliver chunks.
		// Even if the sender uses a single stream, receiveParallel handles it
		// (control stream reader works the same, worker channel just stays empty).
		offsets := buildOffsetTable(manifest.ChunkSizes)
		var have *bitfield
		var tmpPath string
		var tmpFile *os.File
		hasErasure := manifest.Flags&flagErasureCoded != 0

		// Set erasure info on progress for CLI display.
		if hasErasure && manifest.ParityCount > 0 {
			progress.mu.Lock()
			progress.ErasureParity = manifest.ParityCount
			// Compute actual overhead from manifest data.
			if manifest.ChunkCount > 0 {
				progress.ErasureOverhead = float64(manifest.ParityCount) / float64(manifest.ChunkCount)
			}
			progress.mu.Unlock()
		}

		if ckpt != nil {
			tmpPath = ckpt.tmpPath
			have = ckpt.have
			var openErr error
			tmpFile, openErr = os.OpenFile(tmpPath, os.O_WRONLY, 0600)
			if openErr != nil {
				ckpt = nil
			}
		}
		if ckpt == nil {
			have = newBitfield(manifest.ChunkCount)
			var createErr error
			tmpPath, tmpFile, createErr = ts.createTempFile(manifest.Filename)
			if createErr != nil {
				slog.Error("file-transfer: create temp file failed", "error", createErr)
				return
			}
			if truncErr := tmpFile.Truncate(manifest.FileSize); truncErr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				slog.Error("file-transfer: pre-allocate file failed", "error", truncErr)
				return
			}
		}

		session := &parallelSession{
			rootHash:   manifest.RootHash,
			manifest:   manifest,
			tmpFile:    tmpFile,
			tmpPath:    tmpPath,
			have:       have,
			offsets:    offsets,
			progress:   progress,
			compressed: compressed,
			hasErasure: hasErasure,
			done:       make(chan struct{}),
			chunks:     make(chan parallelChunk, 64),
		}
		if hasErasure && manifest.ParityCount > 0 {
			session.parityData = make(map[int][]byte, manifest.ParityCount)
		}

		ts.registerParallelSession(manifest.RootHash, session)

		err = ts.receiveParallel(rw, session, ckpt)

		ts.unregisterParallelSession(manifest.RootHash)

		// Post-receive: finalize file or save checkpoint.
		if err != nil && have.count() > 0 {
			cp := &transferCheckpoint{manifest: manifest, have: have, tmpPath: tmpPath}
			if saveErr := cp.save(ts.receiveDir); saveErr != nil {
				slog.Error("file-transfer: save checkpoint failed", "error", saveErr)
			}
			tmpFile.Close()
		} else if err != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
		} else {
			// Success: flush, rename, clean up.
			if syncErr := tmpFile.Sync(); syncErr != nil {
				err = fmt.Errorf("sync file: %w", syncErr)
				tmpFile.Close()
			} else {
				tmpFile.Close()
				finalPath, fpErr := ts.finalPath(manifest.Filename)
				if fpErr != nil {
					err = fmt.Errorf("determine final path: %w", fpErr)
				} else if renameErr := os.Rename(tmpPath, finalPath); renameErr != nil {
					err = fmt.Errorf("rename temp to final: %w", renameErr)
				} else {
					os.Chmod(finalPath, 0644)
					removeCheckpoint(ts.receiveDir, manifest.RootHash)
				}
			}
		}
		progress.finish(err)
		ts.markCompleted(progress.ID)

		dur := time.Since(recvStart).Truncate(time.Millisecond).String()
		if err != nil {
			slog.Error("file-transfer: receive failed",
				"peer", short, "file", manifest.Filename, "error", err)
			ts.logEvent(EventLogFailed, "receive", peerKey, manifest.Filename, manifest.FileSize, progress.Sent(), err.Error(), dur)
		} else {
			slog.Info("file-transfer: received",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize,
				"path", filepath.Join(ts.receiveDir, manifest.Filename))
			ts.logEvent(EventLogCompleted, "receive", peerKey, manifest.Filename, manifest.FileSize, manifest.FileSize, "", dur)
			// Register hash so this node can serve multi-peer requests for this file.
			finalPath, _ := ts.finalPath(manifest.Filename)
			if finalPath != "" {
				ts.RegisterHash(manifest.RootHash, finalPath)
			}
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
	// Compute remaining frames once: live have.count() changes each iteration.
	framesRemaining := totalExpected - have.count()
	framesRead := 0
	for framesRead < framesRemaining {
		index, wireData, err := readChunkFrame(r)
		if err != nil {
			transferErr = fmt.Errorf("read chunk: %w", err)
			return transferErr
		}
		if index == -1 {
			break // done signal
		}
		progress.addWireBytes(int64(len(wireData)))

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

		// Log progress milestones at 25%, 50%, 75%.
		if m.FileSize > 0 {
			pct := int(totalWritten * 100 / m.FileSize)
			prevPct := int((totalWritten - int64(len(chunkData))) * 100 / m.FileSize)
			peerID := progress.PeerID
			switch {
			case pct >= 75 && prevPct < 75:
				ts.logEvent(EventLogProgress75, "receive", peerID, m.Filename, m.FileSize, totalWritten, "", "")
			case pct >= 50 && prevPct < 50:
				ts.logEvent(EventLogProgress50, "receive", peerID, m.Filename, m.FileSize, totalWritten, "", "")
			case pct >= 25 && prevPct < 25:
				ts.logEvent(EventLogProgress25, "receive", peerID, m.Filename, m.FileSize, totalWritten, "", "")
			}
		}
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
	// Use base name for temp file to avoid needing subdirectories for temp storage.
	base := filepath.Base(filename)
	tmpPath := filepath.Join(ts.receiveDir, ".shurli-tmp-"+randomHex(8)+"-"+base)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, err
	}
	return tmpPath, f, nil
}

// finalPath determines a non-colliding final path for the received file.
// If filename contains directory separators (e.g., "subdir/file.txt"),
// subdirectories are created under the receive directory.
func (ts *TransferService) finalPath(filename string) (string, error) {
	path := filepath.Join(ts.receiveDir, filename)

	// Create parent directories for relative paths (e.g., "mydir/subdir/file.txt").
	dir := filepath.Dir(path)
	if dir != ts.receiveDir {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("create directories: %w", err)
		}
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		f.Close()
		os.Remove(path) // remove the empty file; rename will replace it
		return path, nil
	}
	if !os.IsExist(err) {
		return "", err
	}

	base := filepath.Base(filename)
	ext := filepath.Ext(base)
	nameOnly := strings.TrimSuffix(base, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", nameOnly, i, ext))
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

// ListTransfers returns snapshots of all tracked transfers, including queued items.
func (ts *TransferService) ListTransfers() []TransferSnapshot {
	ts.mu.RLock()
	activeTransfers := make([]TransferSnapshot, 0, len(ts.transfers))
	for _, p := range ts.transfers {
		activeTransfers = append(activeTransfers, p.Snapshot())
	}
	ts.mu.RUnlock()

	// Include queued (pending) transfers as synthetic progress entries.
	queued := ts.queue.Pending()
	result := make([]TransferSnapshot, 0, len(activeTransfers)+len(queued))
	for _, qt := range queued {
		result = append(result, TransferSnapshot{
			ID:        qt.ID,
			Filename:  filepath.Base(qt.FilePath),
			PeerID:    qt.PeerID,
			Direction: qt.Direction,
			Status:    "queued",
			StartTime: qt.QueuedAt,
		})
	}
	result = append(result, activeTransfers...)
	return result
}

// queuedJob holds everything needed to execute a queued transfer.
type queuedJob struct {
	queueID    string
	filePath   string
	isDir      bool
	peerID     string
	priority   TransferPriority
	opts       SendOptions
	openStream streamOpener
	progress   *TransferProgress // synthetic "queued" progress visible to CLI
}

// SubmitSend enqueues an outbound transfer. If a slot is available it starts
// immediately; otherwise it waits in the priority queue. Returns a progress
// tracker with status "queued" or "active".
func (ts *TransferService) SubmitSend(filePath, peerID string, priority TransferPriority, openStream streamOpener, opts SendOptions) (*TransferProgress, error) {
	// Validate path exists before queuing.
	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("cannot access path: %w", err)
	}

	queueID := ts.queue.Enqueue(filePath, peerID, "send", priority)

	progress := &TransferProgress{
		ID:        queueID,
		Filename:  filepath.Base(filePath),
		PeerID:    peerID,
		Direction: "send",
		Status:    "queued",
		StartTime: time.Now(),
	}

	// Track the queued progress so CLI can poll it.
	ts.mu.Lock()
	ts.transfers[queueID] = progress
	ts.mu.Unlock()

	job := &queuedJob{
		queueID:    queueID,
		filePath:   filePath,
		isDir:      info.IsDir(),
		peerID:     peerID,
		priority:   priority,
		opts:       opts,
		openStream: openStream,
		progress:   progress,
	}

	go ts.processQueuedJob(job)

	return progress, nil
}

// processQueuedJob waits for a queue slot, then executes the transfer.
func (ts *TransferService) processQueuedJob(job *queuedJob) {
	// Spin-wait on the queue until this job is dequeued (has a slot).
	for {
		qt := ts.queue.Dequeue()
		if qt != nil && qt.ID == job.queueID {
			// This job got a slot.
			break
		}
		if qt != nil {
			// Put it back - different job got dequeued. This shouldn't happen
			// because each job has its own goroutine, but be safe.
			ts.queue.Complete(qt.ID)
		}
		// Wait for a signal that a slot freed up, or poll.
		select {
		case <-ts.queueReady:
		case <-time.After(500 * time.Millisecond):
		}
	}

	job.progress.setStatus("active")

	var finalErr error
	if job.isDir {
		_, finalErr = ts.SendDirectory(context.Background(), job.filePath, job.openStream, job.opts)
	} else {
		stream, err := job.openStream()
		if err != nil {
			finalErr = fmt.Errorf("open stream: %w", err)
		} else {
			// SendFile runs in background and updates progress internally.
			// We need to wait for it to complete.
			sendProgress, sendErr := ts.SendFile(stream, job.filePath, job.opts)
			if sendErr != nil {
				finalErr = sendErr
			} else {
				// Copy the real transfer's progress into our queued progress tracker.
				// Poll until send completes.
				for {
					snap := sendProgress.Snapshot()
					job.progress.mu.Lock()
					job.progress.Size = snap.Size
					job.progress.Transferred = snap.Transferred
					job.progress.ChunksTotal = snap.ChunksTotal
					job.progress.ChunksDone = snap.ChunksDone
					job.progress.Compressed = snap.Compressed
					job.progress.mu.Unlock()
					if snap.Done {
						if snap.Error != "" {
							finalErr = fmt.Errorf("%s", snap.Error)
						}
						break
					}
					time.Sleep(200 * time.Millisecond)
				}
			}
		}
	}

	job.progress.finish(finalErr)

	if finalErr != nil {
		slog.Error("queued transfer failed",
			"id", job.queueID, "path", job.filePath, "peer", job.peerID, "error", finalErr)
	} else {
		slog.Info("queued transfer complete",
			"id", job.queueID, "path", job.filePath, "peer", job.peerID)
	}

	// Free the queue slot and notify waiting jobs.
	ts.queue.Complete(job.queueID)
	select {
	case ts.queueReady <- struct{}{}:
	default:
	}
}

// CancelTransfer cancels a queued or active transfer by ID.
// Returns true if the transfer was found and cancelled.
func (ts *TransferService) CancelTransfer(id string) bool {
	// Try queue first (pending items).
	if ts.queue.Cancel(id) {
		// Mark progress as failed/cancelled.
		ts.mu.Lock()
		if p, ok := ts.transfers[id]; ok {
			p.finish(fmt.Errorf("cancelled"))
		}
		ts.mu.Unlock()

		// Notify waiters that a slot freed up.
		select {
		case ts.queueReady <- struct{}{}:
		default:
		}
		return true
	}

	// For active transfers, mark as cancelled (best effort - the goroutine
	// will see the status change on next progress check).
	ts.mu.Lock()
	defer ts.mu.Unlock()
	if p, ok := ts.transfers[id]; ok && !p.Done {
		p.finish(fmt.Errorf("cancelled"))
		return true
	}
	return false
}

// SetReceiveMode changes the receive mode at runtime.
// If a timed mode timer is running, it is cancelled.
func (ts *TransferService) SetReceiveMode(mode ReceiveMode) {
	ts.mu.Lock()
	if ts.timedCancel != nil {
		ts.timedCancel()
		ts.timedCancel = nil
		ts.timedDeadline = time.Time{}
	}
	ts.receiveMode = mode
	ts.mu.Unlock()
}

// SetTimedMode temporarily switches to open mode for the given duration,
// then reverts to the previous mode. If a timed mode is already active,
// it is replaced (old timer cancelled).
func (ts *TransferService) SetTimedMode(duration time.Duration) error {
	if duration <= 0 {
		return fmt.Errorf("timed mode duration must be positive")
	}

	ts.mu.Lock()
	// Cancel any existing timed mode.
	if ts.timedCancel != nil {
		ts.timedCancel()
	}

	// Save the current mode to revert to (unless already in timed mode,
	// in which case keep the original saved mode).
	if ts.receiveMode != ReceiveModeTimed {
		ts.timedPrevMode = ts.receiveMode
	}
	ts.receiveMode = ReceiveModeTimed
	ts.timedDeadline = time.Now().Add(duration)
	ts.timedGen++
	gen := ts.timedGen

	ctx, cancel := context.WithCancel(context.Background())
	ts.timedCancel = cancel
	ts.mu.Unlock()

	slog.Info("file-transfer: timed mode activated",
		"duration", duration.String(),
		"revert_to", string(ts.timedPrevMode))

	go func() {
		timer := time.NewTimer(duration)
		defer timer.Stop()
		select {
		case <-timer.C:
			ts.mu.Lock()
			// Only revert if we are still the active timed mode (not replaced).
			if ts.timedGen == gen {
				prev := ts.timedPrevMode
				ts.receiveMode = prev
				ts.timedCancel = nil
				ts.timedDeadline = time.Time{}
				ts.mu.Unlock()
				slog.Info("file-transfer: timed mode expired, reverted",
					"mode", string(prev))
			} else {
				ts.mu.Unlock()
			}
		case <-ctx.Done():
			// Cancelled by SetReceiveMode or a new SetTimedMode call.
		}
	}()

	return nil
}

// TimedModeRemaining returns the remaining duration for timed mode.
// Returns 0 if timed mode is not active.
func (ts *TransferService) TimedModeRemaining() time.Duration {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	if ts.timedCancel == nil || ts.timedDeadline.IsZero() {
		return 0
	}
	remaining := time.Until(ts.timedDeadline)
	if remaining < 0 {
		return 0
	}
	return remaining
}

// GetReceiveMode returns the current receive mode.
func (ts *TransferService) GetReceiveMode() ReceiveMode {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	return ts.receiveMode
}

// SetReceiveDir changes the receive directory at runtime.
func (ts *TransferService) SetReceiveDir(dir string) {
	ts.mu.Lock()
	ts.receiveDir = dir
	ts.mu.Unlock()
}

// SetCompress changes the compression setting at runtime.
func (ts *TransferService) SetCompress(enabled bool) {
	ts.mu.Lock()
	ts.compress = enabled
	ts.mu.Unlock()
}

// SetMaxSize changes the max file size limit at runtime.
func (ts *TransferService) SetMaxSize(maxBytes int64) {
	ts.mu.Lock()
	ts.maxSize = maxBytes
	ts.mu.Unlock()
}

// SetNotifyMode changes the notification mode at runtime (for hot-reload).
func (ts *TransferService) SetNotifyMode(mode string) {
	if ts.notifier != nil {
		ts.notifier.SetMode(mode)
	}
}

// SetNotifyCommand changes the notification command template at runtime (for hot-reload).
func (ts *TransferService) SetNotifyCommand(cmd string) {
	if ts.notifier != nil {
		ts.notifier.SetCommand(cmd)
	}
}

// ReceiveDir returns the receive directory path.
func (ts *TransferService) ReceiveDir() string {
	return ts.receiveDir
}

// MultiPeerEnabled returns whether multi-peer download is enabled.
func (ts *TransferService) MultiPeerEnabled() bool {
	return ts.multiPeerEnabled
}

// MultiPeerMaxPeers returns the max peers for multi-peer download.
func (ts *TransferService) MultiPeerMaxPeers() int {
	return ts.multiPeerMaxPeers
}

// MultiPeerMinSize returns the min file size for multi-peer download.
func (ts *TransferService) MultiPeerMinSize() int64 {
	return ts.multiPeerMinSize
}

// --- Hash Registry ---

// RegisterHash maps a file's Merkle root hash to its local path.
// Called after successful send, receive, or share operations.
func (ts *TransferService) RegisterHash(rootHash [32]byte, localPath string) {
	ts.hashMu.Lock()
	ts.hashRegistry[rootHash] = localPath
	ts.hashMu.Unlock()
}

// LookupHash returns the local file path for a given root hash, if known.
func (ts *TransferService) LookupHash(rootHash [32]byte) (string, bool) {
	ts.hashMu.RLock()
	path, ok := ts.hashRegistry[rootHash]
	ts.hashMu.RUnlock()
	return path, ok
}

// LogPath returns the transfer event log file path (empty if logging disabled).
func (ts *TransferService) LogPath() string {
	if ts.logger == nil {
		return ""
	}
	return ts.logger.path
}

// logEvent writes a structured transfer event to the log file.
// No-op if logging is disabled.
func (ts *TransferService) logEvent(eventType, direction, peerID, fileName string, fileSize, bytesDone int64, errStr, duration string) {
	if ts.logger == nil {
		return
	}
	ts.logger.Log(TransferEvent{
		Timestamp: time.Now(),
		EventType: eventType,
		Direction: direction,
		PeerID:    peerID,
		FileName:  fileName,
		FileSize:  fileSize,
		BytesDone: bytesDone,
		Error:     errStr,
		Duration:  duration,
	})
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

// findPendingByPrefix resolves a transfer ID or unique prefix to the full ID
// and PendingTransfer. Exact match is tried first, then prefix match.
func (ts *TransferService) findPendingByPrefix(prefix string) (string, *PendingTransfer, error) {
	// Exact match first.
	if p, ok := ts.pending[prefix]; ok {
		return prefix, p, nil
	}

	// Prefix match.
	var matches []string
	var match *PendingTransfer
	for id, p := range ts.pending {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
			match = p
		}
	}

	switch len(matches) {
	case 0:
		return "", nil, fmt.Errorf("no pending transfer matching %q", prefix)
	case 1:
		return matches[0], match, nil
	default:
		return "", nil, fmt.Errorf("ambiguous prefix %q, matches: %s", prefix, strings.Join(matches, ", "))
	}
}

// AcceptTransfer approves a pending transfer. Optional dest overrides the receive directory.
// Supports short ID prefix matching (like git).
func (ts *TransferService) AcceptTransfer(id, dest string) error {
	ts.mu.RLock()
	_, p, err := ts.findPendingByPrefix(id)
	ts.mu.RUnlock()
	if err != nil {
		return err
	}

	select {
	case p.decision <- transferDecision{accept: true, dest: dest}:
		return nil
	default:
		return fmt.Errorf("transfer %q already decided or timed out", id)
	}
}

// RejectTransfer rejects a pending transfer with an optional reason.
// Supports short ID prefix matching (like git).
func (ts *TransferService) RejectTransfer(id string, reason byte) error {
	ts.mu.RLock()
	_, p, err := ts.findPendingByPrefix(id)
	ts.mu.RUnlock()
	if err != nil {
		return err
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

// ReceiveFrom initiates a receiver-side download. It sends a download request
// on the given stream, reads the SHFT manifest from the sharer, auto-accepts,
// and receives the file to destDir (or the default receive directory if empty).
//
// This is the inverse of a push transfer: the receiver opens the stream and
// pulls data. The sharer's HandleDownload handler calls SendFile(), which
// writes SHFT manifest + chunks. This method reads that data.
func (ts *TransferService) ReceiveFrom(s network.Stream, remotePath, destDir string) (*TransferProgress, error) {
	if destDir == "" {
		destDir = ts.receiveDir
	}

	// Ensure destDir exists.
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	// Send the download request (path).
	err := RequestDownload(s, remotePath)
	if err == nil {
		return nil, fmt.Errorf("unexpected: download request returned nil error without ready signal")
	}

	ready, ok := err.(*downloadReady)
	if !ok {
		// Actual error from remote.
		return nil, err
	}

	// Create a combined reader that replays the consumed first byte.
	r := ready.PrefixedReader(s)
	rw := struct {
		io.Reader
		io.Writer
	}{r, s}

	remotePeer := s.Conn().RemotePeer()
	peerKey := remotePeer.String()
	short := peerKey[:16] + "..."

	s.SetDeadline(time.Now().Add(transferStreamDeadline))

	// Read manifest (SHFT header).
	manifest, err := readManifest(rw)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}

	ts.logEvent(EventLogRequestReceived, "download", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

	// Size limit check.
	if ts.maxSize > 0 && manifest.FileSize > ts.maxSize {
		writeMsg(s, msgReject)
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", manifest.FileSize, ts.maxSize)
	}

	// Disk space check using destDir.
	if err := checkDiskSpaceAt(destDir, manifest.FileSize); err != nil {
		writeRejectWithReason(s, RejectReasonSpace)
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	// Verify Merkle root.
	computedRoot := MerkleRoot(manifest.ChunkHashes)
	if computedRoot != manifest.RootHash {
		writeMsg(s, msgReject)
		return nil, fmt.Errorf("manifest root hash mismatch")
	}

	compressed := manifest.Flags&flagCompressed != 0

	slog.Info("file-download: receiving",
		"peer", short, "file", manifest.Filename,
		"size", manifest.FileSize, "chunks", manifest.ChunkCount)
	ts.logEvent(EventLogAccepted, "download", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

	// Auto-accept (receiver initiated this download).
	if err := writeMsg(s, msgAccept); err != nil {
		return nil, fmt.Errorf("write accept: %w", err)
	}

	progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
		peerKey, "download", manifest.ChunkCount, compressed)
	progress.setStatus("active")
	ts.logEvent(EventLogStarted, "download", peerKey, manifest.Filename, manifest.FileSize, 0, "", "")

	// Receive chunks (reuses the parallel receive path).
	go func() {
		defer s.Close()
		recvStart := time.Now()

		offsets := buildOffsetTable(manifest.ChunkSizes)
		have := newBitfield(manifest.ChunkCount)
		hasErasure := manifest.Flags&flagErasureCoded != 0

		// Set erasure info on progress for CLI display.
		if hasErasure && manifest.ParityCount > 0 {
			progress.mu.Lock()
			progress.ErasureParity = manifest.ParityCount
			if manifest.ChunkCount > 0 {
				progress.ErasureOverhead = float64(manifest.ParityCount) / float64(manifest.ChunkCount)
			}
			progress.mu.Unlock()
		}

		tmpPath, tmpFile, createErr := createTempFileIn(destDir, manifest.Filename)
		if createErr != nil {
			progress.finish(createErr)
			ts.markCompleted(progress.ID)
			return
		}
		if truncErr := tmpFile.Truncate(manifest.FileSize); truncErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			progress.finish(truncErr)
			ts.markCompleted(progress.ID)
			return
		}

		session := &parallelSession{
			rootHash:   manifest.RootHash,
			manifest:   manifest,
			tmpFile:    tmpFile,
			tmpPath:    tmpPath,
			have:       have,
			offsets:    offsets,
			progress:   progress,
			compressed: compressed,
			hasErasure: hasErasure,
			done:       make(chan struct{}),
			chunks:     make(chan parallelChunk, 64),
		}
		if hasErasure && manifest.ParityCount > 0 {
			session.parityData = make(map[int][]byte, manifest.ParityCount)
		}

		ts.registerParallelSession(manifest.RootHash, session)
		recvErr := ts.receiveParallel(rw, session, nil)
		ts.unregisterParallelSession(manifest.RootHash)

		if recvErr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
		} else {
			if syncErr := tmpFile.Sync(); syncErr != nil {
				recvErr = fmt.Errorf("sync file: %w", syncErr)
				tmpFile.Close()
			} else {
				tmpFile.Close()
				finalPath := filepath.Join(destDir, filepath.Base(manifest.Filename))
				finalPath, fpErr := nonCollidingPath(finalPath)
				if fpErr != nil {
					recvErr = fmt.Errorf("determine final path: %w", fpErr)
				} else if renameErr := os.Rename(tmpPath, finalPath); renameErr != nil {
					recvErr = fmt.Errorf("rename temp to final: %w", renameErr)
				} else {
					os.Chmod(finalPath, 0644)
				}
			}
		}

		progress.finish(recvErr)
		ts.markCompleted(progress.ID)

		dur := time.Since(recvStart).Truncate(time.Millisecond).String()
		if recvErr != nil {
			slog.Error("file-download: receive failed",
				"peer", short, "file", manifest.Filename, "error", recvErr)
			ts.logEvent(EventLogFailed, "download", peerKey, manifest.Filename, manifest.FileSize, progress.Sent(), recvErr.Error(), dur)
		} else {
			slog.Info("file-download: received",
				"peer", short, "file", manifest.Filename,
				"size", manifest.FileSize,
				"dest", destDir)
			ts.logEvent(EventLogCompleted, "download", peerKey, manifest.Filename, manifest.FileSize, manifest.FileSize, "", dur)
			// Register hash so this node can serve multi-peer requests for this file.
			fp := filepath.Join(destDir, filepath.Base(manifest.Filename))
			ts.RegisterHash(manifest.RootHash, fp)
		}
	}()

	return progress, nil
}

// ProbeRootHash opens a download stream to a peer, reads just enough of the
// SHFT manifest to extract the root hash, then closes the stream. This is used
// by multi-peer download to discover the file's root hash before fanning out.
func (ts *TransferService) ProbeRootHash(openStream func() (network.Stream, error), remotePath string) ([32]byte, error) {
	var zero [32]byte

	stream, err := openStream()
	if err != nil {
		return zero, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	stream.SetDeadline(time.Now().Add(30 * time.Second))

	// Send download request.
	reqErr := RequestDownload(stream, remotePath)
	if reqErr == nil {
		return zero, fmt.Errorf("unexpected: download request returned nil")
	}
	ready, ok := reqErr.(*downloadReady)
	if !ok {
		return zero, reqErr
	}

	// Read manifest from the prefixed reader.
	r := ready.PrefixedReader(stream)
	rw := struct {
		io.Reader
		io.Writer
	}{r, stream}

	manifest, manifestErr := readManifest(rw)
	if manifestErr != nil {
		return zero, fmt.Errorf("read manifest: %w", manifestErr)
	}

	// We got the root hash. Reject the transfer (we'll use multi-peer instead).
	writeMsg(stream, msgReject)

	return manifest.RootHash, nil
}

// createTempFileIn creates a temp file in the given directory.
func createTempFileIn(dir, filename string) (string, *os.File, error) {
	base := filepath.Base(filename)
	tmpPath := filepath.Join(dir, ".shurli-tmp-"+randomHex(8)+"-"+base)
	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", nil, err
	}
	return tmpPath, f, nil
}

// nonCollidingPath returns a path that doesn't collide with existing files.
// If path doesn't exist, returns it as-is. Otherwise appends (1), (2), etc.
func nonCollidingPath(path string) (string, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		f.Close()
		os.Remove(path) // remove empty placeholder; rename will replace it
		return path, nil
	}
	if !os.IsExist(err) {
		return "", err
	}

	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	nameOnly := strings.TrimSuffix(base, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", nameOnly, i, ext))
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
	return path, nil // fall back
}
