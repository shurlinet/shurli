package sdk

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/text/unicode/norm"
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
	maxConcurrentTransfers = 10         // global inbound transfer limit
	maxPerPeerTransfers    = 3          // per-peer inbound limit
	maxTrackedTransfers    = 10000      // max tracked transfer entries

	// Timeouts.
	transferStreamDeadline = 1 * time.Hour // max wall-clock for entire transfer
	askModeTimeout         = 5 * time.Minute // receiver approval timeout in ask mode
)

func init() {
	MustValidateProtocolIDs(
		TransferProtocol,
		BrowseProtocol,
		DownloadProtocol,
		MultiPeerProtocol,
	)
}

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

// failureTracker tracks per-peer transfer failure counts for backoff enforcement.
type failureTracker struct {
	mu        sync.Mutex
	peers     map[string]*failureRecord
	threshold int
	window    time.Duration
	block     time.Duration
}

type failureRecord struct {
	failures  []time.Time // timestamps of recent failures
	blockedUntil time.Time
}

func newFailureTracker(threshold int, window, block time.Duration) *failureTracker {
	return &failureTracker{
		peers:     make(map[string]*failureRecord),
		threshold: threshold,
		window:    window,
		block:     block,
	}
}

// recordFailure records a transfer failure for a peer.
func (ft *failureTracker) recordFailure(peerID string) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	rec, ok := ft.peers[peerID]
	if !ok {
		rec = &failureRecord{}
		ft.peers[peerID] = rec
	}

	now := time.Now()
	rec.failures = append(rec.failures, now)

	// Trim failures outside the window.
	cutoff := now.Add(-ft.window)
	start := 0
	for start < len(rec.failures) && rec.failures[start].Before(cutoff) {
		start++
	}
	if start > 0 {
		rec.failures = rec.failures[start:]
	}

	// Check threshold.
	if len(rec.failures) >= ft.threshold {
		rec.blockedUntil = now.Add(ft.block)
		rec.failures = nil // reset after blocking
	}
}

// isBlocked returns true if a peer is currently blocked due to failure backoff.
func (ft *failureTracker) isBlocked(peerID string) bool {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	rec, ok := ft.peers[peerID]
	if !ok {
		return false
	}
	if time.Now().Before(rec.blockedUntil) {
		return true
	}
	return false
}

// cleanup removes stale failure records.
func (ft *failureTracker) cleanup() {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	now := time.Now()
	for k, rec := range ft.peers {
		if now.After(rec.blockedUntil) && len(rec.failures) == 0 {
			delete(ft.peers, k)
		}
	}
}

// bandwidthTracker tracks per-peer bytes transferred per hour.
type bandwidthTracker struct {
	mu     sync.Mutex
	peers  map[string]*bandwidthRecord
	budget int64 // max bytes per peer per hour
}

type bandwidthRecord struct {
	bytes     int64
	windowEnd time.Time
}

func newBandwidthTracker(budget int64) *bandwidthTracker {
	return &bandwidthTracker{
		peers:  make(map[string]*bandwidthRecord),
		budget: budget,
	}
}

// check returns true if the peer has budget remaining for the given size.
// peerBudget overrides the global budget: -1 = unlimited, 0 = use global, >0 = per-peer limit.
func (bt *bandwidthTracker) check(peerID string, size int64, peerBudget int64) bool {
	if peerBudget < 0 {
		return true // unlimited
	}

	budget := bt.budget
	if peerBudget > 0 {
		budget = peerBudget
	}

	bt.mu.Lock()
	defer bt.mu.Unlock()

	now := time.Now()
	rec, ok := bt.peers[peerID]
	if !ok || now.After(rec.windowEnd) {
		return size <= budget
	}
	return (rec.bytes + size) <= budget
}

// record adds bytes to a peer's usage in the current hour window.
func (bt *bandwidthTracker) record(peerID string, bytes int64) {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	now := time.Now()
	rec, ok := bt.peers[peerID]
	if !ok || now.After(rec.windowEnd) {
		bt.peers[peerID] = &bandwidthRecord{
			bytes:     bytes,
			windowEnd: now.Add(1 * time.Hour),
		}
		return
	}
	rec.bytes += bytes
}

// cleanup removes stale bandwidth records.
func (bt *bandwidthTracker) cleanup() {
	bt.mu.Lock()
	defer bt.mu.Unlock()

	now := time.Now()
	for k, rec := range bt.peers {
		if now.After(rec.windowEnd) {
			delete(bt.peers, k)
		}
	}
}

// ParseByteSize parses a human-readable byte size string into bytes.
// Supports: "unlimited" (returns -1), plain numbers, and suffixes
// KB, MB, GB, TB (case-insensitive, binary: 1MB = 1048576).
func ParseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "unlimited") {
		return -1, nil
	}
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	// Scan digits only (no dots, no signs).
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	numStr := s[:i]
	suffix := strings.TrimSpace(s[i:])

	if numStr == "" {
		return 0, fmt.Errorf("no numeric value in %q", s)
	}

	var num int64
	if _, err := fmt.Sscanf(numStr, "%d", &num); err != nil {
		return 0, fmt.Errorf("invalid number %q: %w", numStr, err)
	}
	if num < 0 {
		return 0, fmt.Errorf("negative size not allowed: %d", num)
	}

	var multiplier int64
	switch strings.ToUpper(suffix) {
	case "", "B":
		multiplier = 1
	case "KB", "K":
		multiplier = 1024
	case "MB", "M":
		multiplier = 1024 * 1024
	case "GB", "G":
		multiplier = 1024 * 1024 * 1024
	case "TB", "T":
		multiplier = 1024 * 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unknown suffix %q", suffix)
	}

	result := num * multiplier
	if num != 0 && result/num != multiplier {
		return 0, fmt.Errorf("value overflows int64: %d%s", num, suffix)
	}
	return result, nil
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

	mu           sync.Mutex
	cancelFunc   func() // D1 fix: called by CancelTransfer to stop underlying I/O (e.g. stream.Reset for receives)
	relayTracker func(int64) // per-chunk relay grant byte tracking (H7)
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
// Also calls relayTracker for grant circuit byte tracking (H7).
func (p *TransferProgress) addWireBytes(n int64) {
	p.mu.Lock()
	p.CompressedSize += n
	tracker := p.relayTracker
	p.mu.Unlock()
	if tracker != nil {
		tracker(n)
	}
}

// setRelayTracker registers a per-chunk relay grant byte tracker (H7).
func (p *TransferProgress) setRelayTracker(f func(int64)) {
	p.mu.Lock()
	p.relayTracker = f
	p.mu.Unlock()
}

// setCancelFunc registers a function that CancelTransfer will call to stop the
// underlying I/O for this transfer (e.g. stream.Reset for receive transfers).
func (p *TransferProgress) setCancelFunc(f func()) {
	p.mu.Lock()
	p.cancelFunc = f
	p.mu.Unlock()
}

func (p *TransferProgress) setStatus(status string) {
	p.mu.Lock()
	p.Status = status
	p.mu.Unlock()
}

func (p *TransferProgress) finish(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// D1 fix: idempotent - first completion wins. Prevents a late success from
	// overwriting an earlier cancel (CancelTransfer + executeQueuedJob race).
	if p.Done {
		return
	}
	p.Done = true
	p.cancelFunc = nil // release stream reference
	if err != nil {
		p.Error = err.Error()
		p.Status = "failed"
	} else {
		p.Status = "complete"
	}
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

	RateLimit int // max transfer requests per peer per minute (default: 600, 0 = disabled)

	// DDoS defense settings.
	GlobalRateLimit  int   // max total inbound transfer requests per minute (default: 600, 0 = disabled)
	MaxQueuedPerPeer int   // max pending+active transfers per peer (default: 10)
	MinSpeedBytes    int   // minimum transfer speed bytes/sec (default: 1024, 0 = disabled)
	MinSpeedSeconds  int   // speed check window seconds (default: 30)
	MaxTempSize      int64         // max total .tmp file size bytes (default: 1GB, 0 = unlimited)
	TempFileExpiry   time.Duration // auto-expire .tmp files older than this (default: 1h, 0 = never)
	BandwidthBudget  int64 // max bytes per peer per hour (default: 100MB, 0 = unlimited)

	// PeerBudgetFunc returns the per-peer bandwidth budget override for a peer.
	// Returns: -1 = unlimited, 0 = use global default, >0 = bytes per hour.
	// If nil, global BandwidthBudget is used for all peers.
	PeerBudgetFunc func(peerID string) int64

	// Failure backoff.
	FailureBackoffThreshold int           // fails within window to trigger block (default: 3)
	FailureBackoffWindow    time.Duration // failure counting window (default: 5m)
	FailureBackoffBlock     time.Duration // block duration (default: 60s)

	// Queue persistence.
	QueueFile    string // path for persisted queue (empty = disabled)
	QueueHMACKey []byte // 32-byte HMAC key for queue file integrity

	// Relay grant checker for pre-transfer budget/time checks and per-chunk tracking.
	GrantChecker RelayGrantChecker

	// ConnsToPeer returns all connections to a peer. Used by SendFile to check
	// if a peer has any LAN connection when deciding whether to enable erasure
	// coding. If nil, only the stream's own connection is checked.
	ConnsToPeer func(peer.ID) []network.Conn
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
	queue         *TransferQueue
	queueReady    chan struct{}          // signaled when a new job is enqueued or a slot frees up
	pendingJobs   map[string]*queuedJob // queueID -> job, consumed by queue processor
	pendingJobsMu sync.Mutex
	queueCtx      context.Context
	queueCancel   context.CancelFunc

	mu          sync.RWMutex
	transfers   map[string]*TransferProgress
	completed   []string
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

	// DDoS defense subsystems (nil = disabled).
	globalRateLimiter *transferRateLimiter // single-key rate limiter for all inbound
	failureTracker    *failureTracker      // per-peer failure backoff
	bandwidthTracker  *bandwidthTracker    // per-peer hourly bandwidth budget
	peerBudgetFunc    func(string) int64   // per-peer budget override lookup
	maxQueuedPerPeer  int                  // max pending+active per peer (0 = no limit)
	minSpeedBytes     int                  // minimum transfer speed bytes/sec
	minSpeedSeconds   int                  // speed check window
	maxTempSize       int64                // max total .tmp file size (0 = unlimited)
	tempFileExpiry    time.Duration        // auto-expire stale .tmp files (0 = never)
	defenseCleanupStop context.CancelFunc  // stops the defense cleanup goroutine

	// Per-job cancel functions (D1 fix: CancelTransfer context propagation).
	// Keyed by transfer/queue ID. executeQueuedJob stores its cancel func here;
	// CancelTransfer calls it to propagate cancellation to the running goroutine.
	jobCancelMu sync.Mutex
	jobCancels  map[string]context.CancelFunc

	// Queue persistence.
	queueFile    string     // path for persisted queue file
	queueHMACKey []byte     // HMAC key for queue integrity
	persistMu    sync.Mutex // P7 fix: serializes persistQueue writes

	// Relay grant checker for budget/time checks and per-chunk tracking (H7).
	grantChecker RelayGrantChecker

	// connsToPeer returns all connections to a peer (for LAN detection across connections).
	connsToPeer func(peer.ID) []network.Conn
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
		queueReady:        make(chan struct{}, 10),
		pendingJobs:       make(map[string]*queuedJob),
		transfers:         make(map[string]*TransferProgress),
		peerInbound:       make(map[string]int),
		pending:           make(map[string]*PendingTransfer),
		multiPeerEnabled:  cfg.MultiPeerEnabled,
		multiPeerMaxPeers: multiPeerMaxPeers,
		multiPeerMinSize:  multiPeerMinSize,
		hashRegistry:      make(map[[32]byte]string),
		jobCancels:        make(map[string]context.CancelFunc),
		grantChecker:      cfg.GrantChecker,
		connsToPeer:       cfg.ConnsToPeer,
	}

	// Start the single queue processor goroutine.
	ts.queueCtx, ts.queueCancel = context.WithCancel(context.Background())
	go ts.runQueueProcessor()

	// Per-peer rate limiter (default 600/min = 10/sec, negative = disabled).
	// Must be high enough for directory transfers (one stream per file).
	rateLimit := cfg.RateLimit
	if rateLimit == 0 {
		rateLimit = 600 // default: 10/sec handles directories with hundreds of files
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

	// Global inbound rate limiter (default 30/min).
	globalLimit := cfg.GlobalRateLimit
	if globalLimit == 0 {
		globalLimit = 600 // default: matches per-peer limit for directory transfers
	}
	if globalLimit > 0 {
		ts.globalRateLimiter = newTransferRateLimiter(globalLimit)
	}

	// Per-peer queue depth limit (default 10).
	maxQueued := cfg.MaxQueuedPerPeer
	if maxQueued == 0 {
		maxQueued = 10
	}
	ts.maxQueuedPerPeer = maxQueued

	// Failure backoff (default: 3 fails in 5m = 60s block).
	fbThreshold := cfg.FailureBackoffThreshold
	if fbThreshold == 0 {
		fbThreshold = 3
	}
	fbWindow := cfg.FailureBackoffWindow
	if fbWindow == 0 {
		fbWindow = 5 * time.Minute
	}
	fbBlock := cfg.FailureBackoffBlock
	if fbBlock == 0 {
		fbBlock = 60 * time.Second
	}
	if fbThreshold > 0 {
		ts.failureTracker = newFailureTracker(fbThreshold, fbWindow, fbBlock)
	}

	// Minimum speed enforcement (default: 1024 bytes/sec, 30s window).
	ts.minSpeedBytes = cfg.MinSpeedBytes
	if ts.minSpeedBytes == 0 {
		ts.minSpeedBytes = 1024
	}
	ts.minSpeedSeconds = cfg.MinSpeedSeconds
	if ts.minSpeedSeconds == 0 {
		ts.minSpeedSeconds = 30
	}

	// Temp file size budget (default: 1 GB).
	ts.maxTempSize = cfg.MaxTempSize
	if ts.maxTempSize == 0 {
		ts.maxTempSize = 1 << 30 // 1 GB
	}

	// Temp file expiry (default: 1 hour).
	ts.tempFileExpiry = cfg.TempFileExpiry
	if ts.tempFileExpiry == 0 {
		ts.tempFileExpiry = time.Hour
	}

	// Bandwidth budget per peer (default: 100 MB/hour).
	// -1 = unlimited globally (but per-peer overrides still apply if PeerBudgetFunc is set).
	bwBudget := cfg.BandwidthBudget
	if bwBudget == 0 {
		bwBudget = 100 * 1024 * 1024
	}
	ts.peerBudgetFunc = cfg.PeerBudgetFunc
	if bwBudget > 0 || ts.peerBudgetFunc != nil {
		if bwBudget < 0 {
			// Global unlimited, but tracker needed for per-peer overrides.
			// Use max int64 as global budget (effectively unlimited).
			bwBudget = 1<<63 - 1
		}
		ts.bandwidthTracker = newBandwidthTracker(bwBudget)
	}

	// Queue persistence.
	ts.queueFile = cfg.QueueFile
	ts.queueHMACKey = cfg.QueueHMACKey

	// Defense cleanup goroutine (handles all periodic cleanup).
	defenseCtx, defenseCancel := context.WithCancel(context.Background())
	ts.defenseCleanupStop = defenseCancel
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				if ts.failureTracker != nil {
					ts.failureTracker.cleanup()
				}
				if ts.bandwidthTracker != nil {
					ts.bandwidthTracker.cleanup()
				}
				if ts.globalRateLimiter != nil {
					ts.globalRateLimiter.cleanup()
				}
				ts.cleanExpiredTempFiles()
			case <-defenseCtx.Done():
				return
			}
		}
	}()

	return ts, nil
}

// isDangerousRune returns true for characters that are dangerous in filenames:
// terminal escape sequences, invisible Unicode, BiDi overrides, and variation selectors.
// These can cause terminal injection (OSC 52 clipboard RCE), AI prompt injection
// (ASCII smuggling via Unicode Tags), or extension spoofing (BiDi RLO).
// See: CVE-2024-50349, CVE-2022-45872, CVE-2021-42574, OWASP LLM01:2025.
// NOTE: isUnsafeDisplayRune in internal/validate/display.go mirrors this logic
// for terminal display sanitization. Keep both in sync when adding new ranges.
func isDangerousRune(r rune) bool {
	// C0 control chars (U+0000-U+001F) - includes ESC (0x1b).
	if r <= 0x1F {
		return true
	}
	// DEL and C1 control chars (U+007F-U+009F).
	if r >= 0x7F && r <= 0x9F {
		return true
	}
	// Zero-width and invisible formatting characters.
	switch r {
	case 0x200B, // Zero Width Space
		0x200C, // Zero Width Non-Joiner
		0x200D, // Zero Width Joiner
		0x200E, // Left-to-Right Mark
		0x200F, // Right-to-Left Mark
		0x2060, // Word Joiner
		0x2062, // Invisible Times (Sneaky Bits encoding)
		0x2064, // Invisible Plus (Sneaky Bits encoding)
		0xFEFF, // Zero Width No-Break Space / BOM
		0x180E: // Mongolian Vowel Separator
		return true
	}
	// BiDi control characters (extension spoofing via RLO U+202E).
	if r >= 0x202A && r <= 0x202E {
		return true
	}
	if r >= 0x2066 && r <= 0x2069 {
		return true
	}
	// Unicode Tags block (ASCII smuggling for LLM prompt injection).
	if r >= 0xE0000 && r <= 0xE007F {
		return true
	}
	// Variation selectors (Sneaky Bits binary encoding).
	if r >= 0xFE00 && r <= 0xFE0F {
		return true
	}
	if r >= 0xE0100 && r <= 0xE01EF {
		return true
	}
	return false
}

// sanitizeFilename removes dangerous characters from a filename for filesystem safety.
// Strips control chars, terminal escapes, invisible Unicode, BiDi overrides,
// and variation selectors. Safe Unicode (Japanese, Arabic, emoji) passes through.
func sanitizeFilename(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if !isDangerousRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SanitizeDisplayName removes dangerous characters from a filename for safe terminal
// display and AI agent consumption. Exported for use in CLI display code.
// This is the display-layer defense against:
//   - Terminal injection: ANSI/OSC escape sequences (clipboard RCE via OSC 52)
//   - AI prompt injection: Unicode Tags (invisible to humans, interpreted by LLM tokenizers)
//   - Extension spoofing: BiDi overrides (U+202E makes text render reversed)
//   - Invisible payloads: zero-width characters, variation selectors
func SanitizeDisplayName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		if !isDangerousRune(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// sanitizeRelativePath cleans a relative path for safe use under a destination directory.
// It strips leading slashes, ".." components, empty segments, and backslashes.
// Applies NFKC normalization to prevent homoglyph path confusion (Cyrillic 'о' vs Latin 'o').
// Strips dangerous Unicode from each component (terminal escapes, invisible chars, BiDi).
// Returns only the base filename if the path resolves to something unsafe.
func sanitizeRelativePath(name string) string {
	// NFKC normalization: collapses compatibility equivalents, prevents homoglyph attacks.
	name = norm.NFKC.String(name)

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
		// Strip dangerous characters from each component (control chars,
		// terminal escapes, invisible Unicode, BiDi overrides).
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

// estimateChunkCount returns an approximate chunk count for progress display.
// The exact count is unknown until chunking completes (content-defined chunking).
func estimateChunkCount(totalSize int64) int {
	if totalSize <= 0 {
		return 0
	}
	_, avg, _ := ChunkTarget(totalSize)
	est := int(totalSize / int64(avg))
	if est < 1 {
		est = 1
	}
	return est
}

// extractCommonPrefix extracts the top-level directory name from a file table.
// Returns the common path prefix (first component) shared by all files.
// For a directory "mydir" with files ["mydir/a.txt", "mydir/sub/b.txt"], returns "mydir".
func extractCommonPrefix(files []fileEntry) string {
	if len(files) == 0 {
		return ""
	}
	// Split first file's path to get the first component.
	first := files[0].Path
	idx := strings.IndexByte(first, '/')
	if idx < 0 {
		return "" // single file, no prefix
	}
	prefix := first[:idx]

	// Verify all files share this prefix.
	for _, f := range files[1:] {
		if !strings.HasPrefix(f.Path, prefix+"/") {
			return "" // mixed prefixes
		}
	}
	return prefix
}

// SendOptions configures a single send operation.
type SendOptions struct {
	NoCompress   bool         // override: disable compression for this transfer
	Streams      int          // parallel stream count (0 = adaptive default based on transport)
	StreamOpener streamOpener // opens additional streams to the same peer (required for parallel)
	RelativeName string       // override manifest filename (e.g., "subdir/file.txt" for directory transfer)
}

// SendFile sends a file or directory over a libp2p stream using the streaming protocol.
// Supports both single files and directories (merged per FT-Y plan).
// Runs in background; returns a progress tracker immediately.
//
// Streaming protocol flow:
//
//	writeHeader -> wait for accept/reject/resume -> chunkProducer -> stream chunks -> writeTrailer
//
// Incompressible detection: if the first 3 chunks fail to compress
// (ratio >= 0.95), compression is disabled for remaining chunks.
func (ts *TransferService) SendFile(s network.Stream, filePath string, opts ...SendOptions) (*TransferProgress, error) {
	remotePeer := s.Conn().RemotePeer()

	info, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat path: %w", err)
	}

	// Build file table with metadata (F3).
	var files []fileEntry
	var filePaths []string
	var totalSize int64

	if info.IsDir() {
		// Walk directory, collect regular files with metadata.
		dirBase := filepath.Base(filePath)
		err = filepath.WalkDir(filePath, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			// Skip symlinks, device files, sockets (regular files only).
			if !d.Type().IsRegular() {
				return nil
			}
			fi, fiErr := d.Info()
			if fiErr != nil {
				return fiErr
			}
			if fi.Size() > maxFileSize {
				return fmt.Errorf("file %s too large: %d bytes (max %d)", path, fi.Size(), maxFileSize)
			}
			rel, relErr := filepath.Rel(filePath, path)
			if relErr != nil {
				return relErr
			}
			relPath := filepath.ToSlash(filepath.Join(dirBase, rel))
			fe := fileEntry{
				Path:      relPath,
				Size:      fi.Size(),
				MetaFlags: metaHasMode | metaHasMtime,
				Mode:      uint32(fi.Mode().Perm()),
				Mtime:     fi.ModTime().Unix(),
			}
			files = append(files, fe)
			filePaths = append(filePaths, path)
			totalSize += fi.Size()
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk directory: %w", err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("directory is empty: %s", filePath)
		}
		if totalSize > maxTotalTransferSize {
			return nil, fmt.Errorf("directory too large: %d bytes (max %d)", totalSize, maxTotalTransferSize)
		}
	} else {
		// Single file.
		if info.Size() > maxFileSize {
			return nil, fmt.Errorf("file too large: %d bytes (max %d)", info.Size(), maxFileSize)
		}
		manifestName := filepath.Base(filePath)
		if len(opts) > 0 && opts[0].RelativeName != "" {
			manifestName = opts[0].RelativeName
		}
		fe := fileEntry{
			Path:      manifestName,
			Size:      info.Size(),
			MetaFlags: metaHasMode | metaHasMtime,
			Mode:      uint32(info.Mode().Perm()),
			Mtime:     info.ModTime().Unix(),
		}
		files = []fileEntry{fe}
		filePaths = []string{filePath}
		totalSize = info.Size()
	}

	// Sort file table for deterministic ordering (I7, I10, R3-SEC7).
	if err := sortFileTable(files, filePaths); err != nil {
		return nil, fmt.Errorf("sort file table: %w", err)
	}

	// Compute cumulative offsets for cross-file CDC.
	cumOffsets := computeCumulativeOffsets(files)

	// Generate transfer ID (random per session).
	var transferID [32]byte
	rand.Read(transferID[:])

	// Determine compression.
	useCompression := ts.compress
	if len(opts) > 0 && opts[0].NoCompress {
		useCompression = false
	}

	var flags uint8
	if useCompression {
		flags |= flagCompressed
	}

	// Erasure coding decision (transport-aware).
	useErasure := ts.erasureOverhead > 0
	if useErasure {
		transport := ClassifyTransport(s)
		if transport == TransportLAN {
			useErasure = false
		} else if transport == TransportDirect && ts.connsToPeer != nil {
			if anyConnIsLAN(ts.connsToPeer(s.Conn().RemotePeer())) {
				useErasure = false
			}
		}
	}
	if useErasure {
		flags |= flagErasureCoded
	}

	// Display name for progress tracking (R4-IMP6).
	displayName := files[0].Path
	if len(files) > 1 {
		if prefix := extractCommonPrefix(files); prefix != "" {
			displayName = prefix
		} else {
			displayName = filepath.Base(filePath)
		}
	}

	estimatedChunks := estimateChunkCount(totalSize)

	progress := ts.trackTransfer(displayName, totalSize,
		remotePeer.String(), "send", estimatedChunks, useCompression)
	// D1 fix: register stream reset so CancelTransfer can stop the send goroutine.
	progress.setCancelFunc(func() { s.Reset() })

	// H7: set per-chunk relay grant byte tracker if stream goes through a relay.
	if tracker := ts.makeChunkTracker(s, "send"); tracker != nil {
		progress.setRelayTracker(tracker)
	}

	go func() {
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		sendStart := time.Now()
		ts.logEvent(EventLogStarted, "send", remotePeer.String(), displayName, totalSize, 0, "", "")

		// Parallel stream config from SendOptions.
		var opener streamOpener
		var streams int
		if len(opts) > 0 {
			opener = opts[0].StreamOpener
			streams = opts[0].Streams
		}
		// Determine actual stream count based on transport + chunk estimate.
		if opener != nil {
			transport := ClassifyTransport(s)
			streams = adaptiveStreamCount(transport, estimatedChunks, streams)
		} else {
			streams = 1
		}

		rootHash, sendErr := ts.streamingSend(s, files, filePaths, cumOffsets, totalSize,
			flags, transferID, useCompression, useErasure, opener, streams, progress)
		progress.finish(sendErr)
		ts.markCompleted(progress.ID)

		short := remotePeer.String()[:16] + "..."
		dur := time.Since(sendStart).Truncate(time.Millisecond).String()
		if sendErr != nil {
			slog.Error("file-transfer: send failed",
				"peer", short, "file", displayName, "error", sendErr)
			ts.logEvent(EventLogFailed, "send", remotePeer.String(), displayName, totalSize, progress.Sent(), sendErr.Error(), dur)
		} else {
			slog.Info("file-transfer: sent",
				"peer", short, "file", displayName,
				"size", totalSize, "chunks", progress.ChunksDone)
			ts.logEvent(EventLogCompleted, "send", remotePeer.String(), displayName, totalSize, totalSize, "", dur)
			// Register hash so this node can serve multi-peer requests.
			ts.RegisterHash(rootHash, filePath)
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

// streamingSend executes the streaming protocol: writeHeader -> accept -> chunkProducer -> stream -> trailer.
// Returns the computed Merkle root hash on success.
//
// When numStreams > 1 and openStream is non-nil, chunks are distributed across parallel
// worker streams via sendParallel. Otherwise, all chunks go on the control stream.
func (ts *TransferService) streamingSend(
	rw io.ReadWriter,
	files []fileEntry, filePaths []string, cumOffsets []int64,
	totalSize int64, flags uint8, transferID [32]byte,
	useCompression, useErasure bool,
	openStream streamOpener, numStreams int,
	progress *TransferProgress,
) ([32]byte, error) {
	var zero [32]byte

	// Write header.
	if err := writeHeader(rw, files, flags, totalSize, transferID); err != nil {
		return zero, fmt.Errorf("write header: %w", err)
	}

	// Wait for accept/reject/resume.
	resp, err := readMsg(rw)
	if err != nil {
		return zero, fmt.Errorf("read response: %w", err)
	}

	var acceptBitfield *bitfield
	var skipBitfield *bitfield

	switch resp {
	case msgReject:
		return zero, fmt.Errorf("peer rejected transfer")

	case msgRejectReason:
		reasonByte, readErr := readMsg(rw)
		if readErr != nil {
			return zero, fmt.Errorf("peer rejected transfer (could not read reason)")
		}
		return zero, fmt.Errorf("peer rejected transfer: %s", RejectReasonString(reasonByte))

	case msgAccept:
		// Read accept bitfield (F2).
		bf, bfErr := readAcceptBitfield(rw, len(files))
		if bfErr != nil {
			return zero, fmt.Errorf("read accept bitfield: %w", bfErr)
		}
		// All-zero bitfield = full rejection (R4-IMP4).
		if isAllRejected(bf) {
			return zero, fmt.Errorf("peer rejected all files")
		}
		if !isFullAccept(bf) {
			acceptBitfield = bf
		}

	case msgResumeRequest:
		// Resume: read bitfield of chunks the receiver already has.
		bfData, bfErr := readResumePayload(rw)
		if bfErr != nil {
			return zero, fmt.Errorf("read resume payload: %w", bfErr)
		}
		// Estimate chunk count for bitfield sizing.
		estChunks := estimateChunkCount(totalSize)
		skipBitfield = &bitfield{
			bits: make([]byte, (estChunks+7)/8),
			n:    estChunks,
		}
		copy(skipBitfield.bits, bfData)

		if err := writeMsg(rw, msgResumeResponse); err != nil {
			return zero, fmt.Errorf("send resume response: %w", err)
		}

		slog.Info("file-transfer: resuming",
			"have", skipBitfield.count(), "total_est", estChunks)

	default:
		return zero, fmt.Errorf("unexpected response: 0x%02x", resp)
	}

	progress.setStatus("active")

	// Launch chunk producer goroutine (N2, N9, F1).
	ch := make(chan streamChunk, producerChanBuffer)
	done := make(chan producerResult, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go chunkProducer(ctx, files, filePaths, cumOffsets, totalSize,
		useCompression, useErasure, skipBitfield, acceptBitfield, ch, done)

	// Distribute chunks: parallel (N worker streams) or single stream.
	var result producerResult
	if numStreams > 1 && openStream != nil {
		var parallelErr error
		result, parallelErr = ts.sendParallel(rw, openStream, transferID, ch, done, progress, numStreams)
		if parallelErr != nil {
			cancel()
			return zero, parallelErr
		}
	} else {
		// Single-stream: read from producer, write directly.
		var totalSent int64
		chunksSent := 0
		for sc := range ch {
			if err := writeStreamChunkFrame(rw, sc); err != nil {
				cancel()
				<-done
				return zero, fmt.Errorf("send chunk %d: %w", sc.chunkIdx, err)
			}
			totalSent += int64(len(sc.data))
			chunksSent++
			progress.updateChunks(totalSent, chunksSent)
			progress.addWireBytes(int64(len(sc.data)))
		}
		result = <-done
		if result.err != nil {
			return zero, fmt.Errorf("chunk producer: %w", result.err)
		}
	}

	// Update progress with actual chunk count (I3).
	progress.mu.Lock()
	progress.ChunksTotal = len(result.chunkHashes)
	progress.mu.Unlock()

	// Compute Merkle root.
	rootHash := MerkleRoot(result.chunkHashes)

	// Handle erasure coding (R4-SEC1: current approach buffers all data).
	var erasure *erasureTrailer
	if useErasure && len(result.rawForRS) > 0 {
		params := computeErasureParams(len(result.chunkHashes), ts.erasureOverhead)
		parityEntries, rsErr := encodeErasure(result.rawForRS, params.StripeSize, ts.erasureOverhead)
		if rsErr != nil {
			return zero, fmt.Errorf("erasure encode: %w", rsErr)
		}

		// Set erasure info on progress for CLI display.
		progress.mu.Lock()
		progress.ErasureParity = len(parityEntries)
		progress.ErasureOverhead = ts.erasureOverhead
		progress.mu.Unlock()

		// Send parity chunks as streaming frames (after all data chunks).
		// Use parityFileIdx sentinel so receiver doesn't write parity to file data (S1 fix).
		for i, p := range parityEntries {
			parityIdx := len(result.chunkHashes) + i
			sc := streamChunk{
				fileIdx:    parityFileIdx,
				chunkIdx:   parityIdx,
				offset:     0, // parity chunks don't map to file offsets
				hash:       p.hash,
				decompSize: uint32(len(p.data)),
				data:       p.data,
			}
			if err := writeStreamChunkFrame(rw, sc); err != nil {
				return zero, fmt.Errorf("send parity chunk %d: %w", i, err)
			}
			progress.addWireBytes(int64(len(p.data)))
		}

		// Build erasure trailer.
		parityHashes := make([][32]byte, len(parityEntries))
		paritySizes := make([]uint32, len(parityEntries))
		for i, p := range parityEntries {
			parityHashes[i] = p.hash
			paritySizes[i] = uint32(len(p.data))
		}
		erasure = &erasureTrailer{
			StripeSize:   params.StripeSize,
			ParityCount:  len(parityEntries),
			ParityHashes: parityHashes,
			ParitySizes:  paritySizes,
		}
	}

	// Write trailer.
	if err := writeTrailer(rw, len(result.chunkHashes), rootHash, result.skippedHashes, erasure); err != nil {
		return zero, fmt.Errorf("write trailer: %w", err)
	}

	return rootHash, nil
}

// SendDirectory is now merged into SendFile. Directories are detected automatically.
// This wrapper exists for backward compatibility with executeQueuedJob.
// It opens a single stream and sends the entire directory as one transfer (I4).
func (ts *TransferService) SendDirectory(ctx context.Context, dirPath string, openStream func() (network.Stream, error), opts SendOptions) ([]*TransferProgress, error) {
	// Validate path before opening stream (matches old behavior).
	info, err := os.Stat(dirPath)
	if err != nil {
		return nil, fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("not a directory: %s", dirPath)
	}

	// Check for empty directory before opening a stream.
	isEmpty := true
	filepath.WalkDir(dirPath, func(path string, d os.DirEntry, _ error) error {
		if path != dirPath && d != nil && d.Type().IsRegular() {
			isEmpty = false
			return filepath.SkipAll
		}
		return nil
	})
	if isEmpty {
		return nil, fmt.Errorf("directory is empty: %s", dirPath)
	}

	stream, streamErr := openStream()
	if streamErr != nil {
		return nil, fmt.Errorf("open stream: %w", streamErr)
	}
	progress, sendErr := ts.SendFile(stream, dirPath, opts)
	if sendErr != nil {
		stream.Close()
		return nil, sendErr
	}
	// Wait for completion (SendFile runs in background).
	for {
		snap := progress.Snapshot()
		if snap.Done {
			if snap.Error != "" {
				return []*TransferProgress{progress}, fmt.Errorf("transfer failed: %s", snap.Error)
			}
			break
		}
		select {
		case <-ctx.Done():
			return []*TransferProgress{progress}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return []*TransferProgress{progress}, nil
}

// --- TransferService: Receive ---

// HandleInbound returns a StreamHandler for receiving files via the streaming protocol.
// Reads SHFT streaming header, validates, accepts/rejects, then receives chunks
// via readStreamChunkFrame and verifies via trailer Merkle root.
func (ts *TransferService) HandleInbound() StreamHandler {
	return func(serviceName string, s network.Stream) {
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

		// Failure backoff check (before any resource allocation).
		if ts.failureTracker != nil && ts.failureTracker.isBlocked(peerKey) {
			slog.Warn("file-transfer: peer blocked (failure backoff)", "peer", short)
			ts.logEvent(EventLogSpamBlocked, "receive", peerKey, "", 0, 0, "failure backoff", "")
			s.Reset()
			return
		}

		// Global rate limit (all peers combined).
		if ts.globalRateLimiter != nil && !ts.globalRateLimiter.allow("_global_") {
			slog.Warn("file-transfer: global rate limit exceeded")
			ts.logEvent(EventLogSpamBlocked, "receive", "", "", 0, 0, "global rate limit exceeded", "")
			writeRejectWithReason(s, RejectReasonBusy)
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

		// Per-peer queue depth limit (pending + active).
		ts.mu.Lock()
		peerTotal := ts.peerInbound[peerKey] + ts.countPeerPending(peerKey)
		if ts.maxQueuedPerPeer > 0 && peerTotal >= ts.maxQueuedPerPeer {
			ts.mu.Unlock()
			slog.Warn("file-transfer: per-peer queue depth exceeded",
				"peer", short, "total", peerTotal, "max", ts.maxQueuedPerPeer)
			writeRejectWithReason(s, RejectReasonBusy)
			return
		}

		// Per-peer concurrent limit.
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

		// Per-peer rate limit check (before parsing header to save CPU).
		if ts.rateLimiter != nil && !ts.rateLimiter.allow(peerKey) {
			slog.Warn("file-transfer: rate limit exceeded", "peer", short)
			ts.logEvent(EventLogSpamBlocked, "receive", peerKey, "", 0, 0, "rate limit exceeded", "")
			s.Reset()
			return
		}

		// Short header deadline (I9): 30s to read header, extend after accept.
		s.SetDeadline(time.Now().Add(30 * time.Second))

		// Read streaming header.
		files, totalSize, flags, transferID, cumOffsets, err := readHeader(rw)
		if err != nil {
			slog.Warn("file-transfer: bad header", "peer", short, "error", err)
			writeMsg(s, msgReject)
			return
		}
		// Display name for notification and progress (R4-IMP6).
		var displayName string
		if len(files) == 1 {
			displayName = files[0].Path
		} else {
			if prefix := extractCommonPrefix(files); prefix != "" {
				displayName = fmt.Sprintf("%s (%d files)", prefix, len(files))
			} else {
				displayName = fmt.Sprintf("%d files", len(files))
			}
		}

		ts.logEvent(EventLogRequestReceived, "receive", peerKey, displayName, totalSize, 0, "", "")

		// Notify user about incoming transfer request.
		if ts.notifier != nil {
			if notifyErr := ts.notifier.Notify(peerKey, displayName, totalSize); notifyErr != nil {
				slog.Debug("file-transfer: notification failed", "error", notifyErr)
			}
		}

		// Enforce size limit.
		if ts.maxSize > 0 && totalSize > ts.maxSize {
			slog.Warn("file-transfer: too large",
				"peer", short, "file", displayName, "size", totalSize, "max", ts.maxSize)
			writeRejectWithReason(s, RejectReasonSize)
			ts.logEvent(EventLogRejected, "receive", peerKey, displayName, totalSize, 0, "file too large", "")
			return
		}

		// Per-peer bandwidth budget check (WAN only).
		transport := ClassifyTransport(s)
		if transport != TransportLAN && ts.bandwidthTracker != nil {
			var peerBudget int64
			if ts.peerBudgetFunc != nil {
				peerBudget = ts.peerBudgetFunc(peerKey)
			}
			if !ts.bandwidthTracker.check(peerKey, totalSize, peerBudget) {
				slog.Warn("file-transfer: bandwidth budget exceeded",
					"peer", short, "file", displayName, "size", totalSize)
				writeRejectWithReason(s, RejectReasonBusy)
				ts.logEvent(EventLogSpamBlocked, "receive", peerKey, displayName, totalSize, 0, "bandwidth budget exceeded", "")
				return
			}
		}

		// Temp file budget check.
		if err := ts.checkTempBudget(); err != nil {
			slog.Warn("file-transfer: temp budget exceeded",
				"peer", short, "file", displayName, "error", err)
			writeRejectWithReason(s, RejectReasonBusy)
			ts.logEvent(EventLogSpamBlocked, "receive", peerKey, displayName, totalSize, 0, "temp file budget exceeded", "")
			return
		}

		// Pre-accept disk space check.
		if err := ts.checkDiskSpace(totalSize); err != nil {
			slog.Warn("file-transfer: insufficient disk space",
				"peer", short, "file", displayName, "error", err)
			writeRejectWithReason(s, RejectReasonSpace)
			ts.logEvent(EventLogDiskSpaceRejected, "receive", peerKey, displayName, totalSize, 0, "insufficient disk space", "")
			return
		}

		destDir := ts.receiveDir

		if ts.receiveMode == ReceiveModeAsk {
			// Ask mode: queue for manual approval with timeout.
			pendingID := fmt.Sprintf("pending-%d-%s", time.Now().UnixNano(), randomHex(4))
			pt := &PendingTransfer{
				ID:       pendingID,
				Filename: displayName,
				Size:     totalSize,
				PeerID:   peerKey,
				Time:     time.Now(),
				decision: make(chan transferDecision, 1),
			}

			ts.mu.Lock()
			ts.pending[pendingID] = pt
			ts.mu.Unlock()

			slog.Info("file-transfer: awaiting approval",
				"peer", short, "file", displayName,
				"size", totalSize, "id", pendingID)

			if ts.events != nil {
				ts.events.Emit(Event{
					Type:        EventTransferPending,
					PeerID:      remotePeer,
					ServiceName: "file-transfer",
					Detail:      pendingID,
				})
			}

			timer := time.NewTimer(askModeTimeout)
			defer timer.Stop()

			var decision transferDecision
			timedOut := false
			select {
			case decision = <-pt.decision:
			case <-timer.C:
				timedOut = true
				decision = transferDecision{accept: false, reason: RejectReasonBusy}
				slog.Info("file-transfer: ask mode timeout, rejecting",
					"peer", short, "file", displayName, "id", pendingID)
			}

			ts.removePending(pendingID)

			if !decision.accept {
				if decision.reason != RejectReasonNone {
					writeRejectWithReason(s, decision.reason)
				} else {
					writeMsg(s, msgReject)
				}
				if timedOut {
					ts.logEvent(EventLogCancelled, "receive", peerKey, displayName, totalSize, 0, "ask mode timeout", "")
				} else {
					ts.logEvent(EventLogRejected, "receive", peerKey, displayName, totalSize, 0, "user rejected", "")
				}
				return
			}

			if decision.dest != "" {
				if dInfo, dErr := os.Stat(decision.dest); dErr != nil || !dInfo.IsDir() {
					slog.Error("file-transfer: invalid accept dest", "dest", decision.dest)
					writeMsg(s, msgReject)
					return
				}
				destDir = decision.dest
			}

			slog.Info("file-transfer: approved",
				"peer", short, "file", displayName, "id", pendingID)
			ts.logEvent(EventLogAccepted, "receive", peerKey, displayName, totalSize, 0, "", "")
		} else {
			slog.Info("file-transfer: receiving",
				"peer", short, "file", displayName,
				"size", totalSize, "files", len(files),
				"compressed", flags&flagCompressed != 0)
			ts.logEvent(EventLogAccepted, "receive", peerKey, displayName, totalSize, 0, "", "")
		}

		// Compute content key for cross-session resume (R3-IMP5, R4-IMP2).
		ck := contentKey(files)

		// Check for existing checkpoint (resume support).
		var resumeState *streamReceiveState
		var resumeBitfield *bitfield
		ckpt, ckptErr := loadCheckpoint(destDir, ck)
		if ckptErr == nil && ckpt != nil {
			// Validate checkpoint matches current transfer.
			if ckpt.totalSize == totalSize && len(ckpt.files) == len(files) {
				restored, restoreErr := ckpt.restoreReceiveState(destDir)
				if restoreErr == nil {
					resumeState = restored
					resumeBitfield = ckpt.have
					slog.Info("file-transfer: resuming from checkpoint",
						"peer", short, "file", displayName,
						"have", ckpt.have.count(), "total_est", len(ckpt.hashes))
				} else {
					slog.Debug("file-transfer: checkpoint restore failed, starting fresh",
						"error", restoreErr)
				}
			}
		}

		// Register parallel session BEFORE accept so worker streams can attach
		// immediately after the sender receives the accept response. On fast LANs,
		// worker hello can arrive before allocateTempFiles completes if registration
		// happens after accept. The worker handler only needs transferID + controlPID +
		// channels for routing - state/progress are set below before receiveParallel.
		session := &parallelSession{
			transferID: transferID,
			controlPID: s.Conn().RemotePeer(),
			contentKey: ck,
			receiveDir: destDir,
			flags:      flags,
			done:       make(chan struct{}),
			chunks:     make(chan streamChunk, producerChanBuffer),
		}
		ts.registerParallelSession(transferID, session)
		defer ts.unregisterParallelSession(transferID)

		if resumeState != nil {
			// Resume: send resume request with checkpoint bitfield.
			if err := writeResumeRequest(rw, resumeBitfield); err != nil {
				slog.Error("file-transfer: resume request write failed", "error", err)
				resumeState.cleanup()
				return
			}
			// Read sender's resume acknowledgment before entering chunk receive loop.
			// Without this, receiveParallel reads msgResumeResponse (0x07) as a chunk
			// frame type and fails with "unexpected stream frame type".
			resp, respErr := readMsg(rw)
			if respErr != nil || resp != msgResumeResponse {
				slog.Error("file-transfer: resume response failed",
					"error", respErr, "resp", resp)
				resumeState.cleanup()
				return
			}
		} else {
			// Fresh transfer: send accept with full-accept bitfield (F2).
			acceptBF := newBitfield(len(files))
			for i := range files {
				acceptBF.set(i)
			}
			if err := writeAcceptBitfield(rw, len(files), acceptBF); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		}

		// Extend deadline after accept/resume (I9).
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Create or reuse streaming receive state.
		estimatedChunks := estimateChunkCount(totalSize)
		var state *streamReceiveState
		if resumeState != nil {
			state = resumeState
		} else {
			state = newStreamReceiveState(files, totalSize, flags, cumOffsets)

			// Allocate temp files for each accepted file entry.
			if err := state.allocateTempFiles(destDir); err != nil {
				slog.Error("file-transfer: allocate temp files failed", "error", err)
				return
			}

			// Initialize duplicate detection bitfield (R3-IMP3).
			state.initReceivedBitfield(estimatedChunks)
		}
		defer state.cleanup() // R4-SEC2: always clean up temp files on any exit

		progress := ts.trackTransfer(displayName, totalSize,
			peerKey, "receive", estimatedChunks, flags&flagCompressed != 0)
		progress.setStatus("active")

		// H7: relay grant byte tracker.
		if tracker := ts.makeChunkTracker(s, "recv"); tracker != nil {
			progress.setRelayTracker(tracker)
		}

		ts.logEvent(EventLogStarted, "receive", peerKey, displayName, totalSize, 0, "", "")

		// Wire state and progress into session now that they're ready.
		session.state = state
		session.progress = progress

		// D1 fix: compose cancel func to reset control + any worker streams.
		progress.setCancelFunc(func() {
			s.Reset()
			session.resetWorkerStreams()
		})

		// Receive via parallel-capable streaming receive loop.
		rootHash, recvErr := ts.receiveParallel(rw, session)

		// Register hash for multi-peer serving on success.
		if recvErr == nil {
			if len(files) == 1 {
				ts.RegisterHash(rootHash, filepath.Join(destDir, files[0].Path))
			} else if prefix := extractCommonPrefix(files); prefix != "" {
				ts.RegisterHash(rootHash, filepath.Join(destDir, prefix))
			}
		}

		progress.finish(recvErr)
		ts.markCompleted(progress.ID)

		dur := time.Since(recvStart).Truncate(time.Millisecond).String()
		if recvErr != nil {
			slog.Error("file-transfer: receive failed",
				"peer", short, "file", displayName, "error", recvErr)
			ts.logEvent(EventLogFailed, "receive", peerKey, displayName, totalSize, progress.Sent(), recvErr.Error(), dur)
			ts.recordTransferFailure(peerKey)
		} else {
			slog.Info("file-transfer: received",
				"peer", short, "file", displayName,
				"size", totalSize, "files", len(files))
			ts.logEvent(EventLogCompleted, "receive", peerKey, displayName, totalSize, totalSize, "", dur)
			if transport != TransportLAN && ts.bandwidthTracker != nil {
				ts.bandwidthTracker.record(peerKey, totalSize)
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
		hint := fmt.Sprintf("cannot create file in download directory: %s", ts.receiveDir)
		if os.IsPermission(err) || strings.Contains(err.Error(), "read-only") {
			hint += "\nIf the directory was created after the daemon started, restart the daemon:" +
				"\n  sudo systemctl restart shurli-daemon"
		}
		return "", nil, fmt.Errorf("%s: %w", hint, err)
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

	id := fmt.Sprintf("xfer-%s", randomHex(6))

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

// Retry constants for transient failures (e.g., receiver busy).
const (
	maxSendRetries    = 5
	initialRetryDelay = 2 * time.Second
)

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
	retryCount         int     // number of retries so far
	relayReconnects    int     // relay session expiry reconnection attempts (H11)
	lastRelayPeerID    peer.ID // relay peer from last attempt (for session expiry detection)
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

	queueID, err := ts.queue.Enqueue(filePath, peerID, "send", priority)
	if err != nil {
		return nil, fmt.Errorf("queue full: %w", err)
	}

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

	// Store job for the queue processor to pick up.
	ts.pendingJobsMu.Lock()
	ts.pendingJobs[queueID] = job
	ts.pendingJobsMu.Unlock()

	// Signal the queue processor that a new job is available.
	select {
	case ts.queueReady <- struct{}{}:
	default:
	}

	ts.persistQueue()

	return progress, nil
}

// runQueueProcessor is the single goroutine that dequeues jobs and dispatches
// them for execution. This replaces the old per-job goroutine spin-wait which
// had a bug where goroutines could steal and complete each other's queue items.
func (ts *TransferService) runQueueProcessor() {
	cleanupTicker := time.NewTicker(5 * time.Minute)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ts.queueCtx.Done():
			return
		case <-ts.queueReady:
		case <-cleanupTicker.C:
			// Evict stale pendingJobs entries (older than 24h).
			ts.pendingJobsMu.Lock()
			for id, job := range ts.pendingJobs {
				if time.Since(job.progress.StartTime) > 24*time.Hour {
					delete(ts.pendingJobs, id)
					job.progress.finish(fmt.Errorf("expired: queued for over 24 hours"))
				}
			}
			ts.pendingJobsMu.Unlock()
			continue
		case <-time.After(500 * time.Millisecond):
		}

		// Drain all available slots.
		for {
			qt := ts.queue.Dequeue()
			if qt == nil {
				break // no items or at capacity
			}

			// Look up the job.
			ts.pendingJobsMu.Lock()
			job, ok := ts.pendingJobs[qt.ID]
			if ok {
				delete(ts.pendingJobs, qt.ID)
			}
			ts.pendingJobsMu.Unlock()

			if !ok {
				// Job was cancelled before processor got to it.
				ts.queue.Complete(qt.ID)
				continue
			}

			go ts.executeQueuedJob(job)
		}
	}
}

// executeQueuedJob runs a single queued transfer to completion.
// D1 fix: creates a per-job context derived from queueCtx so CancelTransfer
// can propagate cancellation to the running goroutine and underlying I/O.
func (ts *TransferService) executeQueuedJob(job *queuedJob) {
	// D1 fix: if already cancelled between dequeue and goroutine start, skip work.
	if job.progress.Snapshot().Done {
		ts.queue.Complete(job.queueID)
		select {
		case ts.queueReady <- struct{}{}:
		default:
		}
		return
	}

	// Create per-job context so CancelTransfer can cancel this specific job.
	jobCtx, jobCancel := context.WithCancel(ts.queueCtx)
	defer jobCancel()

	// Register the cancel func so CancelTransfer can call it.
	ts.jobCancelMu.Lock()
	ts.jobCancels[job.queueID] = jobCancel
	ts.jobCancelMu.Unlock()
	defer func() {
		ts.jobCancelMu.Lock()
		delete(ts.jobCancels, job.queueID)
		ts.jobCancelMu.Unlock()
	}()

	job.progress.setStatus("active")

	var finalErr error
	if job.isDir {
		// Pre-transfer relay grant check for directory sends.
		// Open a probe stream to detect relay and check grant, then close it.
		// SendDirectory opens a single stream for the entire directory (I4).
		if ts.grantChecker != nil {
			probeStream, probeErr := job.openStream()
			if probeErr == nil {
				relayID := relayPeerFromStream(probeStream)
				job.lastRelayPeerID = relayID
				if relayID != "" {
					dirSize := int64(0)
					// Walk directory to get total size for grant check.
					filepath.WalkDir(job.filePath, func(_ string, d os.DirEntry, walkErr error) error {
						if walkErr != nil {
							return walkErr // propagate errors (deleted files, permission issues)
						}
						if d != nil && d.Type().IsRegular() {
							if fi, err := d.Info(); err == nil {
								dirSize += fi.Size()
							}
						}
						return nil
					})
					grantInfo := ts.checkRelayGrant(probeStream, dirSize, "send")

					if grantInfo.GrantActive && !grantInfo.BudgetOK {
						// FT-Y #9: Budget insufficient for directory. Close relay
						// connection and retry through a better relay.
						probeStream.Close()
						ts.grantChecker.ResetCircuitCounters(relayID)
						ts.closeRelayConns(relayID, job.peerID)
						slog.Info("relay-grant: closing low-budget relay for directory transfer, retrying",
							"old_relay", shortPeerStr(relayID),
							"dir_size", FormatBytes(dirSize),
							"relay_budget", FormatBytes(grantInfo.SessionBudget))

						retryStream, retryErr := job.openStream()
						if retryErr != nil {
							finalErr = fmt.Errorf("relay reconnect for directory transfer: %w", retryErr)
						} else {
							retryRelayID := relayPeerFromStream(retryStream)
							if retryRelayID != "" {
								job.lastRelayPeerID = retryRelayID
							}
							retryCheck := ts.checkRelayGrant(retryStream, dirSize, "send")
							retryStream.Close()
							if retryCheck.GrantActive && !retryCheck.BudgetOK {
								finalErr = fmt.Errorf("directory size (%s) exceeds relay session limit (%s) on all available relays",
									FormatBytes(dirSize), FormatBytes(retryCheck.SessionBudget))
							} else if retryCheck.GrantActive && !retryCheck.TimeOK {
								finalErr = fmt.Errorf("relay grant expires too soon for directory transfer (remaining: %s)",
									retryCheck.GrantRemaining.Truncate(time.Second))
							}
						}
					} else if grantInfo.GrantActive && !grantInfo.TimeOK {
						probeStream.Close()
						finalErr = fmt.Errorf("relay grant expires too soon for directory transfer (remaining: %s)",
							grantInfo.GrantRemaining.Truncate(time.Second))
					} else {
						probeStream.Close()
					}
				} else {
					probeStream.Close()
				}
			}
		}
		if finalErr == nil {
			_, finalErr = ts.SendDirectory(jobCtx, job.filePath, job.openStream, job.opts)
		}
	} else {
		stream, err := job.openStream()
		if err != nil {
			finalErr = fmt.Errorf("open stream: %w", err)
		} else {
			// Pre-transfer relay grant check: budget + time (H7).
			if ts.grantChecker != nil {
				relayID := relayPeerFromStream(stream)
				job.lastRelayPeerID = relayID
				if relayID != "" {
					fileSize := int64(0)
					if fi, statErr := os.Stat(job.filePath); statErr == nil {
						fileSize = fi.Size()
					}
					grantInfo := ts.checkRelayGrant(stream, fileSize, "send")

					// Budget insufficient: close stream and reopen for fresh circuit.
					if grantInfo.GrantActive && !grantInfo.BudgetOK {
						stream.Close()
						ts.grantChecker.ResetCircuitCounters(relayID)
						newStream, reopenErr := job.openStream()
						if reopenErr != nil {
							finalErr = fmt.Errorf("relay reconnect for fresh budget: %w", reopenErr)
							stream = nil
						} else {
							stream = newStream
							// Re-verify relay on new stream (may route through different relay).
							newRelayID := relayPeerFromStream(newStream)
							if newRelayID != "" {
								job.lastRelayPeerID = newRelayID
							}
							slog.Info("relay-grant: new circuit established for fresh budget",
								"relay", shortPeerStr(job.lastRelayPeerID))

							// Re-check after reopen: both budget and time.
							recheckInfo := ts.checkRelayGrant(newStream, fileSize, "send")
							if recheckInfo.GrantActive && !recheckInfo.BudgetOK {
								// FT-Y #9: Still insufficient after reopen (same low-budget relay).
								// Close the relay CONNECTION to force PathDialer to pick a
								// different relay (now budget-ranked by RelayDiscovery).
								stream.Close()
								stream = nil
								ts.closeRelayConns(recheckInfo.RelayPeerID, job.peerID)
								slog.Info("relay-grant: closing low-budget relay connection, retrying through better relay",
									"old_relay", shortPeerStr(recheckInfo.RelayPeerID),
									"file_size", FormatBytes(fileSize),
									"relay_budget", FormatBytes(recheckInfo.SessionBudget))

								retryStream, retryErr := job.openStream()
								if retryErr != nil {
									finalErr = fmt.Errorf("relay reconnect through better relay: %w", retryErr)
								} else {
									stream = retryStream
									retryRelayID := relayPeerFromStream(retryStream)
									if retryRelayID != "" {
										job.lastRelayPeerID = retryRelayID
									}
									// Final budget check on the new relay.
									finalCheck := ts.checkRelayGrant(retryStream, fileSize, "send")
									if finalCheck.GrantActive && !finalCheck.BudgetOK {
										stream.Close()
										stream = nil
										finalErr = fmt.Errorf("file size (%s) exceeds relay session limit (%s) on all available relays",
											FormatBytes(fileSize), FormatBytes(finalCheck.SessionBudget))
									} else if finalCheck.GrantActive && !finalCheck.TimeOK {
										stream.Close()
										stream = nil
										finalErr = fmt.Errorf("relay grant expires too soon for transfer (remaining: %s)",
											finalCheck.GrantRemaining.Truncate(time.Second))
									}
								}
							} else if recheckInfo.GrantActive && !recheckInfo.TimeOK {
								stream.Close()
								stream = nil
								if recheckInfo.SessionDuration > 0 && recheckInfo.SessionDuration < recheckInfo.GrantRemaining {
									finalErr = fmt.Errorf("file too large for relay circuit session (%s session, need ~%ds at ~200KB/s)",
										recheckInfo.SessionDuration.Truncate(time.Second),
										fileSize/(200*1024))
								} else {
									finalErr = fmt.Errorf("relay grant expires too soon for transfer (remaining: %s)",
										recheckInfo.GrantRemaining.Truncate(time.Second))
								}
							}
						}
					}

					// Time insufficient on original check: abort (don't waste relay bandwidth).
					if grantInfo.GrantActive && !grantInfo.TimeOK && finalErr == nil {
						if stream != nil {
							stream.Close()
						}
						finalErr = fmt.Errorf("relay grant expires too soon for transfer (remaining: %s)",
							grantInfo.GrantRemaining.Truncate(time.Second))
					}
				}
			}

			if finalErr == nil && stream != nil {
				// SendFile runs in background and updates progress internally.
				// We need to wait for it to complete.
				sendProgress, sendErr := ts.SendFile(stream, job.filePath, job.opts)
				if sendErr != nil {
					finalErr = sendErr
				} else {
					// Copy the real transfer's progress into our queued progress tracker.
					// D1 fix: check jobCtx.Done() so cancel propagates to the polling loop.
					// When cancelled, reset the stream to stop the underlying SendFile goroutine.
					finalErr = pollSendProgress(jobCtx, stream, sendProgress, job.progress)
				}
			}
		}
	}

	// H11: Relay session expiry reconnection. If a relayed transfer failed and the
	// grant is still active, the failure was likely a session expiry. Retry with
	// exponential backoff (new circuit = fresh session budget + reset counters).
	if finalErr != nil && !isRetryableReject(finalErr) &&
		job.lastRelayPeerID != "" && job.relayReconnects < relayReconnectMaxAttempts &&
		ts.isRelaySessionExpiry(job.lastRelayPeerID, finalErr) {

		job.relayReconnects++
		delay := relayReconnectDelay(job.relayReconnects - 1)
		slog.Info("relay-grant: session likely expired, reconnecting with fresh circuit",
			"id", job.queueID, "relay", shortPeerStr(job.lastRelayPeerID),
			"attempt", job.relayReconnects, "delay", delay)

		if ts.grantChecker != nil {
			ts.grantChecker.ResetCircuitCounters(job.lastRelayPeerID)
		}
		job.progress.setStatus("relay-reconnecting")

		select {
		case <-jobCtx.Done():
			job.progress.finish(fmt.Errorf("cancelled during relay reconnect backoff"))
		case <-time.After(delay):
			job.progress.setStatus("queued")
			ts.pendingJobsMu.Lock()
			ts.pendingJobs[job.queueID] = job
			ts.pendingJobsMu.Unlock()
			ts.queue.Requeue(job.queueID, job.filePath, job.peerID, "send", job.priority)
			select {
			case ts.queueReady <- struct{}{}:
			default:
			}
			return
		}
	}

	// Retry on transient "receiver busy" rejection.
	if finalErr != nil && isRetryableReject(finalErr) && job.retryCount < maxSendRetries {
		job.retryCount++
		delay := initialRetryDelay * time.Duration(1<<(job.retryCount-1)) // 2s, 4s, 8s, 16s, 32s
		slog.Info("queued transfer: receiver busy, retrying",
			"id", job.queueID, "peer", job.peerID, "attempt", job.retryCount, "delay", delay)
		job.progress.setStatus("retrying")

		select {
		case <-jobCtx.Done():
			job.progress.finish(fmt.Errorf("cancelled during retry backoff"))
		case <-time.After(delay):
			// Re-queue: put job back and signal processor.
			job.progress.setStatus("queued")
			ts.pendingJobsMu.Lock()
			ts.pendingJobs[job.queueID] = job
			ts.pendingJobsMu.Unlock()
			// Re-enqueue in the priority queue so the processor picks it up.
			ts.queue.Requeue(job.queueID, job.filePath, job.peerID, "send", job.priority)
			select {
			case ts.queueReady <- struct{}{}:
			default:
			}
			return // Don't complete or finish - the job will be retried.
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

	// Free the queue slot and signal the processor to dispatch more.
	ts.queue.Complete(job.queueID)
	select {
	case ts.queueReady <- struct{}{}:
	default:
	}
	ts.persistQueue()
}

// isRetryableReject returns true if the error indicates a transient rejection
// that should be retried (receiver busy, rate limit, etc.).
func isRetryableReject(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "receiver busy")
}

// pollSendProgress copies progress from a SendFile operation into the queued job's
// progress tracker. Exits when the send completes or the context is cancelled.
// On cancel, resets the stream to stop the underlying SendFile goroutine.
func pollSendProgress(ctx context.Context, stream network.Stream, sendProgress, jobProgress *TransferProgress) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		snap := sendProgress.Snapshot()
		jobProgress.mu.Lock()
		jobProgress.Size = snap.Size
		jobProgress.Transferred = snap.Transferred
		jobProgress.ChunksTotal = snap.ChunksTotal
		jobProgress.ChunksDone = snap.ChunksDone
		jobProgress.Compressed = snap.Compressed
		jobProgress.CompressedSize = snap.CompressedSize
		jobProgress.ErasureParity = snap.ErasureParity
		jobProgress.ErasureOverhead = snap.ErasureOverhead
		if len(snap.StreamProgress) > 0 {
			jobProgress.StreamProgress = make([]StreamInfo, len(snap.StreamProgress))
			copy(jobProgress.StreamProgress, snap.StreamProgress)
		}
		jobProgress.mu.Unlock()

		if snap.Done {
			if snap.Error != "" {
				return fmt.Errorf("%s", snap.Error)
			}
			return nil
		}

		select {
		case <-ctx.Done():
			stream.Reset()
			return fmt.Errorf("cancelled")
		case <-ticker.C:
		}
	}
}

// CancelTransfer cancels a queued or active transfer by ID or unique prefix.
// Returns nil on success, error if not found, ambiguous, or pending (use reject instead).
func (ts *TransferService) CancelTransfer(id string) error {
	// Resolve prefix to full ID.
	resolved, err := ts.resolveTransferID(id)
	if err != nil {
		// Check if this ID matches a pending approval transfer (ask mode).
		// If so, guide the user to use reject instead of cancel.
		ts.mu.RLock()
		for pid := range ts.pending {
			if pid == id || strings.HasPrefix(pid, id) {
				ts.mu.RUnlock()
				return fmt.Errorf("transfer %q is awaiting approval; use 'shurli reject %s' instead", id, id)
			}
		}
		ts.mu.RUnlock()
		return err
	}

	// Try queue first (pending items).
	if ts.queue.Cancel(resolved) {
		// Remove from pending jobs map so processor doesn't pick it up.
		ts.pendingJobsMu.Lock()
		delete(ts.pendingJobs, resolved)
		ts.pendingJobsMu.Unlock()

		// Mark progress as failed/cancelled.
		ts.mu.Lock()
		if p, ok := ts.transfers[resolved]; ok {
			p.finish(fmt.Errorf("cancelled"))
		}
		ts.mu.Unlock()

		// Notify processor that a slot freed up.
		select {
		case ts.queueReady <- struct{}{}:
		default:
		}
		ts.persistQueue()
		return nil
	}

	// D1 fix: for active transfers, cancel the per-job context and progress-level
	// cancel func to propagate cancellation to the running goroutine.
	// Collect cancel actions under lock, execute outside - stream.Reset() (called
	// by progressCancel) could block on network I/O and must not hold locks.
	ts.jobCancelMu.Lock()
	jobCancel, hasJobCancel := ts.jobCancels[resolved]
	ts.jobCancelMu.Unlock()

	var progressCancel func()
	found := false

	ts.mu.Lock()
	if p, ok := ts.transfers[resolved]; ok {
		// Read p.Done and p.cancelFunc under p.mu (they are protected by p.mu, not ts.mu).
		p.mu.Lock()
		done := p.Done
		if !done {
			found = true
			progressCancel = p.cancelFunc
		}
		p.mu.Unlock()
		if !done {
			p.finish(fmt.Errorf("cancelled"))
		}
	}
	ts.mu.Unlock()

	if !found {
		return fmt.Errorf("transfer %q not found or already completed", id)
	}

	// Execute cancel actions outside all locks.
	if hasJobCancel {
		jobCancel()
	}
	if progressCancel != nil {
		progressCancel()
	}
	return nil
}

// resolveTransferID resolves an exact ID or unique prefix to the full transfer ID.
// Searches both active transfers and queued items.
func (ts *TransferService) resolveTransferID(prefix string) (string, error) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()

	// Exact match first.
	if _, ok := ts.transfers[prefix]; ok {
		return prefix, nil
	}

	// Prefix match across active transfers.
	var matches []string
	for id := range ts.transfers {
		if strings.HasPrefix(id, prefix) {
			matches = append(matches, id)
		}
	}

	// Also check queue IDs.
	for _, qt := range ts.queue.Pending() {
		if qt.ID == prefix {
			return prefix, nil
		}
		if strings.HasPrefix(qt.ID, prefix) {
			matches = append(matches, qt.ID)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("transfer %q not found", prefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous prefix %q, matches: %s", prefix, strings.Join(matches, ", "))
	}
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

// --- Queue persistence ---

const (
	queueFileVersion = 1
	queueMaxEntries  = 1000
	queueMaxFileSize = 10 << 20 // 10 MB max queue file
	queueEntryTTL    = 24 * time.Hour
)

// persistedQueue is the JSON structure written to disk.
type persistedQueue struct {
	Version int                  `json:"version"`
	HMAC    string               `json:"hmac"` // hex-encoded HMAC-SHA256 of entries JSON
	Entries []persistedQueueEntry `json:"entries"`
}

// persistedQueueEntry is a single queued transfer entry.
type persistedQueueEntry struct {
	ID       string           `json:"id"`
	FilePath string           `json:"file_path"`
	PeerID   string           `json:"peer_id"`
	Priority TransferPriority `json:"priority"`
	QueuedAt time.Time        `json:"queued_at"`
	Nonce    string           `json:"nonce"` // random per-entry, prevents replay
}

// FlushQueue persists the current outbound queue to disk. Called by plugin Stop()
// to ensure queue state survives daemon shutdown (P3 fix).
func (ts *TransferService) FlushQueue() {
	ts.persistQueue()
}

// persistQueue writes the current outbound queue to disk with HMAC integrity.
// P7 fix: serialized by persistMu to prevent concurrent writes.
func (ts *TransferService) persistQueue() {
	ts.persistMu.Lock()
	defer ts.persistMu.Unlock()

	if ts.queueFile == "" || len(ts.queueHMACKey) == 0 {
		return
	}

	// Gather pending queue items.
	pending := ts.queue.Pending()

	entries := make([]persistedQueueEntry, 0, len(pending))
	for _, qt := range pending {
		entries = append(entries, persistedQueueEntry{
			ID:       qt.ID,
			FilePath: qt.FilePath,
			PeerID:   qt.PeerID,
			Priority: qt.Priority,
			QueuedAt: qt.QueuedAt,
			Nonce:    randomHex(8),
		})
	}

	// Truncate to max entries (keep newest).
	if len(entries) > queueMaxEntries {
		entries = entries[len(entries)-queueMaxEntries:]
	}

	// Compute HMAC over entries JSON.
	entriesJSON, err := json.Marshal(entries)
	if err != nil {
		slog.Warn("queue-persist: marshal failed", "error", err)
		return
	}

	mac := hmac.New(sha256.New, ts.queueHMACKey)
	mac.Write(entriesJSON)
	macSum := hex.EncodeToString(mac.Sum(nil))

	pq := persistedQueue{
		Version: queueFileVersion,
		HMAC:    macSum,
		Entries: entries,
	}

	data, err := json.MarshalIndent(pq, "", "  ")
	if err != nil {
		slog.Warn("queue-persist: marshal failed", "error", err)
		return
	}

	// Atomic write: tmp + rename, 0600 permissions.
	dir := filepath.Dir(ts.queueFile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		slog.Warn("queue-persist: create dir failed", "error", err)
		return
	}

	tmp := ts.queueFile + ".tmp"
	// P5 fix: write + fsync + rename for crash-safe persistence.
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		slog.Warn("queue-persist: create failed", "error", err)
		return
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		slog.Warn("queue-persist: write failed", "error", err)
		return
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		slog.Warn("queue-persist: fsync failed", "error", err)
		return
	}
	f.Close()
	if err := os.Rename(tmp, ts.queueFile); err != nil {
		os.Remove(tmp)
		slog.Warn("queue-persist: rename failed", "error", err)
		return
	}

	slog.Debug("queue-persist: saved", "entries", len(entries))
}

// loadPersistedQueue reads and validates the persisted queue file.
// Returns valid entries (TTL-checked, path-validated, HMAC-verified).
func (ts *TransferService) loadPersistedQueue() []persistedQueueEntry {
	if ts.queueFile == "" || len(ts.queueHMACKey) == 0 {
		return nil
	}

	data, err := os.ReadFile(ts.queueFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("queue-persist: read failed", "error", err)
		}
		return nil
	}

	if int64(len(data)) > queueMaxFileSize {
		slog.Warn("queue-persist: file too large, ignoring", "size", len(data))
		return nil
	}

	var pq persistedQueue
	if err := json.Unmarshal(data, &pq); err != nil {
		slog.Warn("queue-persist: parse failed", "error", err)
		return nil
	}

	if pq.Version != queueFileVersion {
		slog.Warn("queue-persist: unknown version", "version", pq.Version)
		return nil
	}

	// Verify HMAC.
	entriesJSON, err := json.Marshal(pq.Entries)
	if err != nil {
		slog.Warn("queue-persist: re-marshal failed", "error", err)
		return nil
	}

	mac := hmac.New(sha256.New, ts.queueHMACKey)
	mac.Write(entriesJSON)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(pq.HMAC), []byte(expectedMAC)) {
		slog.Warn("queue-persist: HMAC verification failed, ignoring queue file")
		return nil
	}

	// Filter: TTL check, path validation, bounded count.
	now := time.Now()
	var valid []persistedQueueEntry
	for _, e := range pq.Entries {
		if now.Sub(e.QueuedAt) > queueEntryTTL {
			slog.Debug("queue-persist: entry expired", "id", e.ID, "age", now.Sub(e.QueuedAt))
			continue
		}
		if e.PeerID == "" || len(e.PeerID) < 10 {
			slog.Debug("queue-persist: invalid peer ID", "id", e.ID)
			continue
		}
		if _, err := os.Stat(e.FilePath); err != nil {
			slog.Debug("queue-persist: path gone", "id", e.ID, "path", e.FilePath)
			continue
		}
		valid = append(valid, e)
		if len(valid) >= queueMaxEntries {
			break
		}
	}

	slog.Info("queue-persist: loaded entries", "total", len(pq.Entries), "valid", len(valid))
	return valid
}

// RequeuePersisted reloads persisted queue entries and re-submits them.
// Must be called AFTER the network is ready (stream openers need working connections).
// streamFactory creates a stream opener for a given peer ID.
func (ts *TransferService) RequeuePersisted(streamFactory func(peerID string) func() (network.Stream, error)) {
	entries := ts.loadPersistedQueue()
	if len(entries) == 0 {
		return
	}

	for _, e := range entries {
		opener := streamFactory(e.PeerID)
		_, err := ts.SubmitSend(e.FilePath, e.PeerID, e.Priority, opener, SendOptions{})
		if err != nil {
			slog.Warn("queue-persist: re-enqueue failed",
				"id", e.ID, "file", filepath.Base(e.FilePath), "error", err)
		} else {
			slog.Info("queue-persist: re-enqueued",
				"file", filepath.Base(e.FilePath), "peer", e.PeerID[:16]+"...")
		}
	}

	// Remove the old queue file since entries are now in the live queue.
	os.Remove(ts.queueFile)
}

// countPeerPending returns the number of pending (ask-mode) transfers for a peer.
// Caller must hold ts.mu (read or write).
func (ts *TransferService) countPeerPending(peerKey string) int {
	count := 0
	for _, p := range ts.pending {
		if p.PeerID == peerKey {
			count++
		}
	}
	return count
}

// checkTempBudget returns an error if the total size of .tmp files in the
// receive directory exceeds the configured budget.
func (ts *TransferService) checkTempBudget() error {
	if ts.maxTempSize <= 0 {
		return nil
	}

	entries, err := os.ReadDir(ts.receiveDir)
	if err != nil {
		return nil // can't read dir, don't block transfer
	}

	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".shurli-tmp-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
	}

	if total >= ts.maxTempSize {
		return fmt.Errorf("temp file budget exceeded: %d bytes (limit %d)", total, ts.maxTempSize)
	}
	return nil
}

// cleanExpiredTempFiles removes .tmp files older than the configured expiry.
func (ts *TransferService) cleanExpiredTempFiles() {
	if ts.tempFileExpiry <= 0 {
		return
	}

	entries, err := os.ReadDir(ts.receiveDir)
	if err != nil {
		return
	}

	cutoff := time.Now().Add(-ts.tempFileExpiry)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".shurli-tmp-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			path := filepath.Join(ts.receiveDir, e.Name())
			if err := os.Remove(path); err == nil {
				slog.Info("file-transfer: expired temp file removed", "file", e.Name(), "age", time.Since(info.ModTime()).Truncate(time.Second))
			}
		}
	}
}

// CleanTempFiles removes all .tmp files in the receive directory and returns
// the number of files removed and total bytes reclaimed.
func (ts *TransferService) CleanTempFiles() (int, int64) {
	entries, err := os.ReadDir(ts.receiveDir)
	if err != nil {
		return 0, 0
	}

	var count int
	var total int64
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), ".shurli-tmp-") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		path := filepath.Join(ts.receiveDir, e.Name())
		if err := os.Remove(path); err == nil {
			count++
			total += info.Size()
		}
	}
	return count, total
}

// recordTransferFailure records a transfer failure for backoff tracking.
func (ts *TransferService) recordTransferFailure(peerKey string) {
	if ts.failureTracker != nil {
		ts.failureTracker.recordFailure(peerKey)
	}
}

// Close releases resources held by the transfer service, including
// closing the transfer log file. Should be called during daemon shutdown.
func (ts *TransferService) Close() error {
	// Stop the queue processor goroutine.
	if ts.queueCancel != nil {
		ts.queueCancel()
	}

	// Clean up stale pendingJobs (processor may not have drained them all).
	ts.pendingJobsMu.Lock()
	for id := range ts.pendingJobs {
		delete(ts.pendingJobs, id)
	}
	ts.pendingJobsMu.Unlock()

	if ts.rateLimiterStop != nil {
		ts.rateLimiterStop()
	}
	if ts.defenseCleanupStop != nil {
		ts.defenseCleanupStop()
	}
	if ts.logger != nil {
		return ts.logger.Close()
	}
	return nil
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

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return nil, fmt.Errorf("create destination directory: %w", err)
	}

	// Send the download request and get a reader that replays the consumed first byte.
	prefixed, err := RequestDownload(s, remotePath)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}

	rw := struct {
		io.Reader
		io.Writer
	}{prefixed, s}

	remotePeer := s.Conn().RemotePeer()
	peerKey := remotePeer.String()
	short := peerKey[:16] + "..."

	s.SetDeadline(time.Now().Add(transferStreamDeadline))

	// Read streaming header (I1: updated from readManifest to readHeader).
	files, totalSize, flags, transferID, cumOffsets, err := readHeader(rw)
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}
	// Display name.
	var displayName string
	if len(files) == 1 {
		displayName = files[0].Path
	} else {
		if prefix := extractCommonPrefix(files); prefix != "" {
			displayName = fmt.Sprintf("%s (%d files)", prefix, len(files))
		} else {
			displayName = fmt.Sprintf("%d files", len(files))
		}
	}

	ts.logEvent(EventLogRequestReceived, "download", peerKey, displayName, totalSize, 0, "", "")

	// Size limit check.
	if ts.maxSize > 0 && totalSize > ts.maxSize {
		writeMsg(s, msgReject)
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", totalSize, ts.maxSize)
	}

	// Disk space check.
	if err := checkDiskSpaceAt(destDir, totalSize); err != nil {
		writeRejectWithReason(s, RejectReasonSpace)
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	slog.Info("file-download: receiving",
		"peer", short, "file", displayName,
		"size", totalSize, "files", len(files))
	ts.logEvent(EventLogAccepted, "download", peerKey, displayName, totalSize, 0, "", "")

	// Compute content key for cross-session resume (R3-IMP5, R4-IMP2).
	ck := contentKey(files)

	// Check for existing checkpoint (resume support).
	var resumeState *streamReceiveState
	var resumeBitfield *bitfield
	ckpt, ckptErr := loadCheckpoint(destDir, ck)
	if ckptErr == nil && ckpt != nil {
		if ckpt.totalSize == totalSize && len(ckpt.files) == len(files) {
			restored, restoreErr := ckpt.restoreReceiveState(destDir)
			if restoreErr == nil {
				resumeState = restored
				resumeBitfield = ckpt.have
				slog.Info("file-download: resuming from checkpoint",
					"peer", short, "file", displayName,
					"have", ckpt.have.count(), "total_est", len(ckpt.hashes))
			} else {
				slog.Debug("file-download: checkpoint restore failed, starting fresh",
					"error", restoreErr)
			}
		}
	}

	// Register parallel session BEFORE accept so worker streams can attach
	// immediately after the sender receives the accept. Same race fix as HandleInbound.
	session := &parallelSession{
		transferID: transferID,
		controlPID: s.Conn().RemotePeer(),
		contentKey: ck,
		receiveDir: destDir,
		flags:      flags,
		done:       make(chan struct{}),
		chunks:     make(chan streamChunk, producerChanBuffer),
	}
	ts.registerParallelSession(transferID, session)

	if resumeState != nil {
		// Resume: send resume request with checkpoint bitfield.
		if err := writeResumeRequest(rw, resumeBitfield); err != nil {
			ts.unregisterParallelSession(transferID)
			resumeState.cleanup()
			return nil, fmt.Errorf("write resume request: %w", err)
		}
		// Read sender's resume acknowledgment before entering chunk receive loop.
		// Without this, receiveParallel reads msgResumeResponse (0x07) as a chunk
		// frame type and fails with "unexpected stream frame type".
		resp, respErr := readMsg(rw)
		if respErr != nil || resp != msgResumeResponse {
			ts.unregisterParallelSession(transferID)
			resumeState.cleanup()
			return nil, fmt.Errorf("resume response: err=%v resp=0x%02x", respErr, resp)
		}
	} else {
		// Fresh transfer: auto-accept with full-accept bitfield (receiver initiated this download).
		acceptBF := newBitfield(len(files))
		for i := range files {
			acceptBF.set(i)
		}
		if err := writeAcceptBitfield(rw, len(files), acceptBF); err != nil {
			ts.unregisterParallelSession(transferID)
			return nil, fmt.Errorf("write accept: %w", err)
		}
	}

	estimatedChunks := estimateChunkCount(totalSize)
	progress := ts.trackTransfer(displayName, totalSize,
		peerKey, "download", estimatedChunks, flags&flagCompressed != 0)
	progress.setStatus("active")

	if tracker := ts.makeChunkTracker(s, "recv"); tracker != nil {
		progress.setRelayTracker(tracker)
	}

	ts.logEvent(EventLogStarted, "download", peerKey, displayName, totalSize, 0, "", "")

	// Receive via streaming protocol in background.
	go func() {
		defer s.Close()
		defer ts.unregisterParallelSession(transferID)
		recvStart := time.Now()

		var state *streamReceiveState
		if resumeState != nil {
			state = resumeState
		} else {
			state = newStreamReceiveState(files, totalSize, flags, cumOffsets)

			if allocErr := state.allocateTempFiles(destDir); allocErr != nil {
				progress.finish(allocErr)
				ts.markCompleted(progress.ID)
				return
			}

			state.initReceivedBitfield(estimatedChunks)
		}
		defer state.cleanup()

		// Wire state and progress into session now that they're ready.
		session.state = state
		session.progress = progress

		progress.setCancelFunc(func() {
			s.Reset()
			session.resetWorkerStreams()
		})

		// Receive via parallel-capable streaming receive loop.
		rootHash, recvErr := ts.receiveParallel(rw, session)

		// Register hash for multi-peer serving on success.
		if recvErr == nil {
			if len(files) == 1 {
				ts.RegisterHash(rootHash, filepath.Join(destDir, files[0].Path))
			} else if prefix := extractCommonPrefix(files); prefix != "" {
				ts.RegisterHash(rootHash, filepath.Join(destDir, prefix))
			}
		}

		progress.finish(recvErr)
		ts.markCompleted(progress.ID)

		dur := time.Since(recvStart).Truncate(time.Millisecond).String()
		if recvErr != nil {
			slog.Error("file-download: receive failed",
				"peer", short, "file", displayName, "error", recvErr)
			ts.logEvent(EventLogFailed, "download", peerKey, displayName, totalSize, progress.Sent(), recvErr.Error(), dur)
		} else {
			slog.Info("file-download: received",
				"peer", short, "file", displayName,
				"size", totalSize, "dest", destDir)
			ts.logEvent(EventLogCompleted, "download", peerKey, displayName, totalSize, totalSize, "", dur)
		}
	}()

	return progress, nil
}

// ProbeRootHash opens a download stream to a peer, sends a hash probe request
// (requestType=0x02), and reads the 45-byte probe response containing the
// file's Merkle root hash. This is used by multi-peer download to discover
// the content hash before fanning out to multiple peers (C2).
//
// The handler side chunks the file and computes MerkleRoot without streaming
// any data back. Cost: ~2.5s for 500MB (FastCDC + BLAKE3).
func (ts *TransferService) ProbeRootHash(openStream func() (network.Stream, error), remotePath string) ([32]byte, error) {
	var zero [32]byte

	stream, err := openStream()
	if err != nil {
		return zero, fmt.Errorf("open stream: %w", err)
	}
	defer stream.Close()

	probe, err := RequestProbe(stream, remotePath)
	if err != nil {
		return zero, fmt.Errorf("probe request: %w", err)
	}

	slog.Debug("file-download: probe result",
		"rootHash", fmt.Sprintf("%x", probe.RootHash[:8]),
		"totalSize", probe.TotalSize,
		"chunkCount", probe.ChunkCount)

	return probe.RootHash, nil
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
