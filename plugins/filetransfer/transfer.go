package filetransfer

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"golang.org/x/text/unicode/norm"
	"golang.org/x/time/rate"

	"github.com/shurlinet/shurli/pkg/sdk"
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
	msgManifest       = 0x01 // sender -> receiver: file manifest
	msgAccept         = 0x02 // receiver -> sender: accept transfer
	msgReject         = 0x03 // receiver -> sender: reject transfer
	msgChunk          = 0x04 // sender -> receiver: chunk data
	msgTransferDone   = 0x05 // sender -> receiver: all chunks sent
	msgResumeRequest  = 0x06 // receiver -> sender: resume with bitfield
	msgResumeResponse = 0x07 // sender -> receiver: resume acknowledged
	msgRejectReason   = 0x08 // receiver -> sender: reject with reason byte
	msgWorkerHello    = 0x09 // sender -> receiver: parallel worker stream identification

	// Reject reasons (sent after msgRejectReason).
	RejectReasonNone  byte = 0x00 // no reason disclosed (same as silent msgReject)
	RejectReasonSpace byte = 0x01 // insufficient disk space
	RejectReasonBusy  byte = 0x02 // receiver busy
	RejectReasonSize  byte = 0x03 // file too large

	// Reject hints (#40 F11): follow reason byte, tell sender how to react.
	RejectHintNone            byte = 0x00 // no hint (legacy receivers)
	RejectHintTransient       byte = 0x01 // transient: retry with backoff
	RejectHintPendingApproval byte = 0x02 // ask-mode: user hasn't decided yet, wait
	RejectHintAtCapacity      byte = 0x03 // at capacity: try later or different peer

	// Manifest flags (bitmask).
	flagCompressed = 0x01 // zstd compression enabled

	// Security limits.
	maxFilenameLen  = 4096     // max filename length in bytes
	maxFileSize     = 1 << 40  // 1 TB max single file
	maxChunkCount   = 1 << 20  // 1M chunks max per transfer
	maxManifestSize = 40 << 20 // 40 MB max manifest wire size (IF12-4: tightened from 64MB)
	// maxChunkWireSize caps the post-compression wire size of a single chunk.
	// This value is coupled to ChunkTarget() in chunker.go: the largest tier's
	// max MUST be <= maxChunkWireSize, otherwise the sender produces chunks
	// the receiver refuses. With FT-Y #14 the biggest tier returns 4 MB max
	// (decompressed), and zstd compression never expands, so 4 MB is both the
	// tier's uncompressed max AND the wire cap. Bumping ChunkTarget tiers
	// without bumping this constant silently truncates the top tier.
	maxChunkWireSize     = 4 << 20 // 4 MB max single chunk on wire (compressed)
	maxDecompressedChunk = 8 << 20 // 8 MB max decompressed chunk
	// maxParityBudgetBytes hard-caps how much parity a receiver will buffer
	// in memory for any single transfer, independent of the sender's declared
	// totalSize. [B2-F1] Without this cap, a malicious sender declaring a
	// maxTotalTransferSize (10 TB) transfer would be granted a 5 TB parity
	// budget against receiver RAM via the `totalSize/2` heuristic.
	//
	// KNOWN LIMITATION (verified by physical test 2026-04-17): this 512 MB
	// cap is effectively the ceiling for tier-5 RS transfers (4 MB max
	// chunks) around 4-6 GB file size. 8 GB tier-5 generates ~1 GiB
	// parity state and trips the cap; raising the cap is not a solution
	// because a 2 GB MemoryMax receiver is already at ~2 GB total RSS
	// (parity + stripe buffers + QUIC + runtime) on 8 GB transfers.
	//
	// The architectural fix is Option C / FT-Y item #23 (MANDATORY before
	// merge to main): per-stripe reconstruction that releases parity as
	// each stripe completes, bounding memory to O(stripeSize x maxChunkSize)
	// ~400 MB independent of file size. AI-model transfers over Shurli
	// are expected to be 100+ GB scale; the current accumulate-all design
	// is architecturally incapable of handling them regardless of this cap.
	maxParityBudgetBytes   = 512 << 20 // 512 MB hard cap (see Option C / item #23)
	maxConcurrentTransfers = 20        // default global inbound transfer limit (configurable via max_inbound_transfers)
	maxPerPeerTransfers    = 5         // default per-peer inbound limit (configurable via max_per_peer_transfers)
	maxTrackedTransfers    = 10000     // max tracked transfer entries

	// Timeouts.
	transferStreamDeadline = 1 * time.Hour   // max wall-clock for entire transfer
	askModeTimeout         = 5 * time.Minute // receiver approval timeout in ask mode
)

func init() {
	sdk.MustValidateProtocolIDs(
		TransferProtocol,
		BrowseProtocol,
		DownloadProtocol,
		MultiPeerProtocol,
		CancelProtocol,
	)
}

// ReceiveMode controls how incoming transfers are handled.
type ReceiveMode string

const (
	ReceiveModeOff      ReceiveMode = "off"      // reject all
	ReceiveModeContacts ReceiveMode = "contacts" // auto-accept from authorized peers (default)
	ReceiveModeAsk      ReceiveMode = "ask"      // queue for manual approval
	ReceiveModeOpen     ReceiveMode = "open"     // accept from any authorized peer
	ReceiveModeTimed    ReceiveMode = "timed"    // temporarily open, reverts after duration
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
	failures     []time.Time // timestamps of recent failures
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

// StreamInfo tracks per-stream progress for parallel transfers.
type StreamInfo struct {
	ChunksDone int   `json:"chunks_done"`
	BytesDone  int64 `json:"bytes_done"`
}

// TransferProgress tracks the progress of an active transfer.
type TransferProgress struct {
	ID              string  `json:"id"`
	Filename        string  `json:"filename"`
	Size            int64   `json:"size"`
	Transferred     int64   `json:"transferred"`
	ChunksTotal     int     `json:"chunks_total"`
	ChunksDone      int     `json:"chunks_done"`
	Compressed      bool    `json:"compressed"`
	CompressedSize  int64   `json:"compressed_size,omitempty"`  // total wire bytes (compressed)
	ErasureParity   int     `json:"erasure_parity,omitempty"`   // total parity chunks declared in trailer (0 if disabled)
	ErasureOverhead float64 `json:"erasure_overhead,omitempty"` // configured overhead (e.g. 0.10)
	// ParityChunksDone counts parity chunks that have actually crossed the wire
	// for this transfer. Incremented by the sender as parity chunks are flushed
	// through sendParallel/sendSingleStream (one counter per transfer, gated on
	// streamChunk.fileIdx == parityFileIdx). [B2-F29, R4-SEC1 Batch 2]
	// ChunksDone stays data-only (matches trailer semantic); ParityChunksDone
	// is surfaced separately so "110 of 100 chunks" does not appear in UI.
	ParityChunksDone int          `json:"parity_chunks_done,omitempty"`
	StreamProgress   []StreamInfo `json:"stream_progress,omitempty"` // per-stream progress (parallel only)
	Failovers        int          `json:"failovers,omitempty"`       // TS-5b: number of path failovers (F10)
	PeerID           string       `json:"peer_id"`
	Direction        string       `json:"direction"` // "send" or "receive"
	Status           string       `json:"status"`    // "pending", "active", "complete", "failed", "rejected"
	StartTime        time.Time    `json:"start_time"`
	EndTime          time.Time    `json:"end_time,omitempty"`
	Done             bool         `json:"done"`
	Error            string       `json:"error,omitempty"`

	mu           sync.Mutex
	cancelFunc   func()      // D1 fix: called by CancelTransfer to stop underlying I/O (e.g. stream.Reset for receives)
	relayTracker func(int64) // per-chunk relay grant byte tracking (H7)
	postFinish   func()      // #40 F7: called once at finish() to release peerInbound immediately

	// TS-4: cancel protocol routing. Not exported to JSON/API (R4-S4).
	transferID   [32]byte // transfer session ID for cancel protocol routing
	remotePeerID peer.ID  // remote peer for sendMultiPathCancel
}

// updateChunks records wire progress from a single goroutine's point of view.
// [B2 audit round 2] Monotonic: ChunksDone and Transferred only move forward,
// never backward. Concurrent receivers (parallel worker streams + control
// stream) each observe the progress counters at different points in the
// shared state-lock cycle, so a goroutine that fell behind could otherwise
// overwrite a higher value committed by a goroutine that raced ahead. The
// monotonic guard eliminates that jitter while preserving the fast-path
// behaviour for single-stream transfers (old-max comparison is one int
// compare under an already-held lock).
func (p *TransferProgress) updateChunks(transferred int64, chunksDone int) {
	p.mu.Lock()
	if transferred > p.Transferred {
		p.Transferred = transferred
	}
	if chunksDone > p.ChunksDone {
		p.ChunksDone = chunksDone
	}
	p.mu.Unlock()
}

// addParityChunkDone increments the live count of parity chunks that have
// crossed the wire for this transfer. Called from sendParallel/sendSingleStream
// when a streamChunk with fileIdx == parityFileIdx is written. Data ChunksDone
// is NOT updated here — parity is tracked separately so the UI doesn't show
// "110/100 data chunks" when erasure is on. [B2-F29, R4-SEC1 Batch 2]
func (p *TransferProgress) addParityChunkDone() {
	p.mu.Lock()
	p.ParityChunksDone++
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
	// D1 fix: idempotent - first completion wins. Prevents a late success from
	// overwriting an earlier cancel (CancelTransfer + executeQueuedJob race).
	if p.Done {
		p.mu.Unlock()
		return
	}
	p.Done = true
	p.EndTime = time.Now()
	p.cancelFunc = nil // release stream reference
	pf := p.postFinish
	p.postFinish = nil // one-shot
	if err != nil {
		p.Error = err.Error()
		p.Status = "failed"
	} else {
		p.Status = "complete"
	}
	p.mu.Unlock()

	// #40 F7: release peerInbound immediately at transfer completion,
	// not when HandleInbound returns. The defer in HandleInbound is the
	// idempotent safety net (releasePeerInbound checks peerInboundReleased).
	if pf != nil {
		pf()
	}
}

// TransferSnapshot is a mutex-free copy of TransferProgress, safe for JSON
// serialization and value passing.
type TransferSnapshot struct {
	ID               string       `json:"id"`
	Filename         string       `json:"filename"`
	Size             int64        `json:"size"`
	Transferred      int64        `json:"transferred"`
	ChunksTotal      int          `json:"chunks_total"`
	ChunksDone       int          `json:"chunks_done"`
	Compressed       bool         `json:"compressed"`
	CompressedSize   int64        `json:"compressed_size,omitempty"`
	ErasureParity    int          `json:"erasure_parity,omitempty"`
	ErasureOverhead  float64      `json:"erasure_overhead,omitempty"`
	ParityChunksDone int          `json:"parity_chunks_done,omitempty"`
	StreamProgress   []StreamInfo `json:"stream_progress,omitempty"`
	Failovers        int          `json:"failovers,omitempty"`
	PeerID           string       `json:"peer_id"`
	Direction        string       `json:"direction"`
	Status           string       `json:"status"`
	StartTime        time.Time    `json:"start_time"`
	EndTime          time.Time    `json:"end_time,omitempty"`
	Done             bool              `json:"done"`
	Error            string            `json:"error,omitempty"`
	PendingFiles     []PendingFileInfo `json:"pending_files,omitempty"` // R7-F2: file list for awaiting_approval (selective rejection #18)
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
		ErasureParity:  p.ErasureParity, ErasureOverhead: p.ErasureOverhead,
		ParityChunksDone: p.ParityChunksDone,
		Failovers:        p.Failovers,
		PeerID:           p.PeerID, Direction: p.Direction, Status: p.Status,
		StartTime: p.StartTime, EndTime: p.EndTime,
		Done: p.Done, Error: p.Error,
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

	// Multi-peer work-stealing block download.
	MultiPeerEnabled         bool  // enable multi-peer downloads (default: true)
	MultiPeerMaxPeers        int   // max peers to download from simultaneously (default: 4)
	MultiPeerMinSize         int64 // min file size for multi-peer (default: 10 MB)
	MultiPeerStrikeThreshold int   // hash mismatches before peer ban (default: 3, 1 for public) (IF12-2)
	MaxServedBytesPerHour    int64 // max total bytes served outbound per hour (0 = unlimited) (IF12-1)

	// Inbound capacity.
	MaxInboundTransfers int // global concurrent inbound limit (default: 20, min: 1)
	MaxPerPeerTransfers int // per-peer concurrent inbound limit (default: 5, min: 1)

	RateLimit int // max transfer requests per peer per minute (default: 600, 0 = disabled)

	// DDoS defense settings.
	GlobalRateLimit  int           // max total inbound transfer requests per minute (default: 600, 0 = disabled)
	MaxQueuedPerPeer int           // max pending+active transfers per peer (default: 10)
	MinSpeedBytes    int           // minimum transfer speed bytes/sec (default: 1024, 0 = disabled)
	MinSpeedSeconds  int           // speed check window seconds (default: 30)
	MaxTempSize      int64         // max total .tmp file size bytes (default: 1GB, 0 = unlimited)
	TempFileExpiry   time.Duration // auto-expire .tmp files older than this (default: 1h, 0 = never)
	BandwidthBudget  int64         // max bytes per peer per hour (default: 100MB, 0 = unlimited)
	SendRateLimit    int64         // max send bytes/sec per transfer (0 = unlimited)

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
	GrantChecker sdk.RelayGrantChecker

	// ConnsToPeer returns all connections to a peer. Used by SendFile to check
	// if a peer has any LAN connection when deciding whether to enable erasure
	// coding. If nil, only the stream's own connection is checked.
	ConnsToPeer func(peer.ID) []network.Conn

	// HasVerifiedLANConn returns true if the peer has at least one live non-relay
	// connection whose remote IP is mDNS-verified as on the local LAN. This is
	// the authoritative trust-making "is this peer on our LAN?" signal. Bare
	// RFC 1918 checks are unreliable (CGNAT, Docker, VPN, multi-WAN routed-
	// private all pass bare-mask but are not LAN). If nil, the transfer
	// service treats every non-relay peer as WAN (conservative: more RS +
	// bandwidth budget applied than strictly needed).
	HasVerifiedLANConn func(peer.ID) bool
}

// PendingTransfer represents an inbound transfer waiting for user approval in ask mode.
type PendingTransfer struct {
	ID       string    `json:"id"`
	Filename string    `json:"filename"`
	Size     int64     `json:"size"`
	PeerID   string    `json:"peer_id"`
	Time     time.Time `json:"time"`

	// Internal fields for selective rejection (#18). Not serialized.
	files      []fileEntry           // file table from wire header
	hasErasure bool                  // sender uses erasure coding (gates selective rejection, F8)
	decision   chan transferDecision
}

// transferDecision carries the user's accept/reject decision for a pending transfer.
type transferDecision struct {
	accept        bool
	reason        byte   // reject reason (only meaningful if !accept)
	dest          string // override receive directory (only meaningful if accept)
	acceptedFiles []int  // 0-indexed file indices to accept (nil = all) (#18)
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
	metrics         *sdk.Metrics
	events          *sdk.EventBus
	logger          *TransferLogger
	notifier        *TransferNotifier

	inboundSem          chan struct{}
	maxPerPeerTransfers int // per-peer concurrent inbound limit (configurable, default 5)

	// Outbound transfer queue with priority ordering and concurrency limit.
	queue         *TransferQueue
	queueReady    chan struct{}         // signaled when a new job is enqueued or a slot frees up
	pendingJobs   map[string]*queuedJob // queueID -> job, consumed by queue processor
	pendingJobsMu sync.Mutex
	queueCtx      context.Context
	queueCancel   context.CancelFunc

	mu               sync.RWMutex
	transfers        map[string]*TransferProgress
	completed        []string
	peerInbound      map[string]int
	peerPreAccept    map[string]int     // goroutines between entry and resource acquisition (#40 R11-F1)
	peerSlotNotify   chan struct{}       // closed+recreated when peerInbound decrements (#40 R7-F1)
	pending          map[string]*PendingTransfer // ask mode: transfers awaiting approval
	parallelSessions map[[32]byte]*parallelSession

	// Timed mode: temporarily switches to open/contacts then reverts.
	timedCancel   context.CancelFunc // cancels the timer goroutine (nil = no active timer)
	timedGen      uint64             // generation counter to identify active timer
	timedPrevMode ReceiveMode        // mode to revert to when timer expires
	timedDeadline time.Time          // when the timer expires

	// Multi-peer download config.
	multiPeerEnabled         bool
	multiPeerMaxPeers        int
	multiPeerMinSize         int64
	multiPeerStrikeThreshold int

	// Hash registry: maps root hash -> local file path for multi-peer serving.
	hashMu       sync.RWMutex
	hashRegistry map[[32]byte]string

	// Multi-peer serving state (IF13-1, IF12-1).
	peerServing           map[string]int  // per-peer active serving session count
	boundaries            *boundaryCache  // FastCDC boundary scan cache (I3)
	sharedFileLister      func() []string // callback for listing shared files (IF13-3)
	totalServedBytes      atomic.Int64    // global outbound bytes counter (IF12-1)
	servedBytesHour       atomic.Int64    // epoch hour for counter reset
	maxTotalServedPerHour int64           // 0 = unlimited (IF12-1)

	// Per-peer transfer request rate limiter (nil = disabled).
	rateLimiter     *transferRateLimiter
	rateLimiterStop context.CancelFunc

	// DDoS defense subsystems (nil = disabled).
	globalRateLimiter  *transferRateLimiter // single-key rate limiter for all inbound
	failureTracker     *failureTracker      // per-peer failure backoff
	bandwidthTracker   *bandwidthTracker    // per-peer hourly bandwidth budget
	peerBudgetFunc     func(string) int64   // per-peer budget override lookup
	maxQueuedPerPeer   int                  // max pending+active per peer (0 = no limit)
	minSpeedBytes      int                  // minimum transfer speed bytes/sec
	minSpeedSeconds    int                  // speed check window
	maxTempSize        int64                // max total .tmp file size (0 = unlimited)
	tempFileExpiry     time.Duration        // auto-expire stale .tmp files (0 = never)
	defenseCleanupStop context.CancelFunc   // stops the defense cleanup goroutine

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
	grantChecker sdk.RelayGrantChecker

	// connsToPeer returns all connections to a peer (for LAN detection across connections).
	connsToPeer func(peer.ID) []network.Conn

	// hasVerifiedLANConn returns true if the peer has at least one live non-relay
	// connection with an mDNS-verified LAN remote IP. Authoritative trust-making
	// signal for LAN classification; avoids bare-RFC1918 false positives
	// (CGNAT, Docker, VPN, routed-private via multi-WAN).
	hasVerifiedLANConn func(peer.ID) bool

	// TS-4: Multi-path cancel support.
	// hostRef is the libp2p host, needed by sendMultiPathCancel to enumerate connections.
	hostRef host.Host

	// TS-5: PathProtector for path protection during transfers.
	pathProtector *sdk.PathProtector

	// TS-5b: Network reference and service name for failover retry.
	// The retry loop uses HedgedOpenStream(networkRef, ..., downloadServiceName)
	// to open streams on backup paths with full security pipeline (R2-F2, R2-F12).
	networkRef          *sdk.Network
	downloadServiceName string

	// activeSends maps transferID -> active send info for outbound sends.
	// The cancel handler looks up this map when a remote receiver sends a cancel.
	// Separate mutex from ts.mu to avoid contention (R2-I5).
	activeSendsMu sync.RWMutex
	activeSends   map[[32]byte]activeSendEntry

	// cancelRateLimiter bounds cancel messages per peer (R2-S1).
	cancelRateLimiter *transferRateLimiter

	// Application-level send rate limit (bytes/sec). 0 = unlimited.
	sendRateLimit int64
}

// activeSendEntry tracks an outbound send for cancel protocol routing.
// Stores the remote peer ID so the cancel handler can verify the sender (R2-S audit).
type activeSendEntry struct {
	cancel     func()  // context cancel for the send goroutine
	remotePeer peer.ID // the peer we're sending TO
}

// newSendRateLimiter creates a rate limiter for a single transfer.
// Returns nil if no rate limit is configured (0 or negative = unlimited).
func (ts *TransferService) newSendRateLimiter(override int64) *rate.Limiter {
	limit := override
	if limit <= 0 {
		limit = ts.sendRateLimit
	}
	if limit <= 0 {
		return nil
	}
	// Burst must be >= max single write (maxChunkWireSize + header) so WaitN never
	// returns ErrExceed. Cap at limit to avoid over-buffering at high rates.
	minBurst := maxChunkWireSize + streamChunkHeaderSize
	burst := minBurst
	if int64(burst) < limit {
		burst = int(limit)
	}
	// Defensive cap: prevent int overflow on 32-bit (Go int is platform-sized).
	const maxBurst = 1 << 30 // 1 GB
	if burst > maxBurst {
		burst = maxBurst
	}
	return rate.NewLimiter(rate.Limit(limit), burst)
}

// NewTransferService creates a new chunked transfer service.
func NewTransferService(cfg TransferConfig, metrics *sdk.Metrics, events *sdk.EventBus) (*TransferService, error) {
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
	if multiPeerMaxPeers > 16 {
		multiPeerMaxPeers = 16 // cap to prevent goroutine explosion from misconfiguration
	}
	multiPeerMinSize := cfg.MultiPeerMinSize
	if multiPeerMinSize <= 0 {
		multiPeerMinSize = 10 * 1024 * 1024
	}
	multiPeerStrikeThreshold := cfg.MultiPeerStrikeThreshold
	if multiPeerStrikeThreshold < 1 {
		multiPeerStrikeThreshold = 3 // default: 3 strikes (authorized networks)
	}

	maxInbound := cfg.MaxInboundTransfers
	if maxInbound < 1 {
		maxInbound = 20
	}
	maxPerPeer := cfg.MaxPerPeerTransfers
	if maxPerPeer < 1 {
		maxPerPeer = 5
	}

	ts := &TransferService{
		receiveDir:               dir,
		maxSize:                  cfg.MaxSize,
		receiveMode:              mode,
		compress:                 compress,
		erasureOverhead:          cfg.ErasureOverhead,
		metrics:                  metrics,
		events:                   events,
		logger:                   logger,
		notifier:                 notifier,
		inboundSem:               make(chan struct{}, maxInbound),
		maxPerPeerTransfers:      maxPerPeer,
		queue:                    NewTransferQueue(maxConcurrent),
		queueReady:               make(chan struct{}, 10),
		pendingJobs:              make(map[string]*queuedJob),
		transfers:                make(map[string]*TransferProgress),
		peerInbound:              make(map[string]int),
		peerPreAccept:            make(map[string]int),
		peerSlotNotify:           make(chan struct{}),
		pending:                  make(map[string]*PendingTransfer),
		multiPeerEnabled:         cfg.MultiPeerEnabled,
		multiPeerMaxPeers:        multiPeerMaxPeers,
		multiPeerMinSize:         multiPeerMinSize,
		multiPeerStrikeThreshold: multiPeerStrikeThreshold,
		maxTotalServedPerHour:    cfg.MaxServedBytesPerHour,
		hashRegistry:             make(map[[32]byte]string),
		peerServing:              make(map[string]int),
		boundaries:               newBoundaryCache(),
		jobCancels:               make(map[string]context.CancelFunc),
		grantChecker:             cfg.GrantChecker,
		connsToPeer:              cfg.ConnsToPeer,
		hasVerifiedLANConn:       cfg.HasVerifiedLANConn,
		activeSends:              make(map[[32]byte]activeSendEntry),
		cancelRateLimiter:        newTransferRateLimiter(cancelRateMax),
		sendRateLimit:            cfg.SendRateLimit,
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
				if ts.cancelRateLimiter != nil {
					ts.cancelRateLimiter.cleanup()
				}
				ts.cleanExpiredTempFiles()
				ts.evictCompletedTransfers() // #40 F8
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
	_, err := w.Write([]byte{msgRejectReason, reason, RejectHintNone})
	return err
}

// writeRejectWithHint writes a reject with both reason and hint byte (#40 F11).
func writeRejectWithHint(w io.Writer, reason, hint byte) error {
	_, err := w.Write([]byte{msgRejectReason, reason, hint})
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
	NoCompress           bool         // override: disable compression for this transfer
	Streams              int          // parallel stream count (0 = adaptive default based on transport)
	StreamOpener         streamOpener // opens additional streams to the same peer (required for parallel)
	RelativeName         string       // override manifest filename (e.g., "subdir/file.txt" for directory transfer)
	RateLimitBytesPerSec int64        // per-transfer send rate limit (0 = use service default)

	// internalShadow hides the internal streaming progress from ts.transfers.
	// Set by executeQueuedJob: the queued job's q-* progress already appears in
	// the tracked list and pollSendProgress mirrors updates into it. Tracking
	// the internal xfer-* progress as well produces duplicate rows in
	// `shurli transfers`. Lowercase so external callers cannot set it.
	internalShadow bool
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

	// TS-5: protect relay paths during transfer (tag created here, protect inside goroutine).
	protectTag := fmt.Sprintf("send-%x", transferID[:8])

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
	// [B2-F3] Gate on totalSize > 0. Zero-byte transfers have no chunks to
	// encode and newErasureEncoder would refuse the empty stripe anyway;
	// setting flagErasureCoded would emit a zero-chunk trailer that
	// readErasureManifest rejects (stripeSize < minStripeSize).
	//
	// Skip RS on mDNS-verified LAN (reliable link, overhead unnecessary).
	// Bare RFC 1918 is NOT trusted — Starlink CGNAT, Docker, VPN, and
	// multi-WAN routed-private subnets all pass bare-mask but aren't LAN.
	// When verification is unavailable (callback nil or no mDNS discovery),
	// default to RS ON — correctness over perf.
	useErasure := ts.erasureOverhead > 0 && totalSize > 0
	if useErasure && ts.hasVerifiedLANConn != nil &&
		ts.hasVerifiedLANConn(s.Conn().RemotePeer()) {
		useErasure = false
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

	// When internalShadow is set (queued send path), the caller already owns
	// the user-facing progress and will mirror updates via pollSendProgress.
	// Skip ts.transfers so the CLI shows one row per logical transfer.
	internalShadow := len(opts) > 0 && opts[0].internalShadow
	var progress *TransferProgress
	if internalShadow {
		progress = newShadowSendProgress(displayName, totalSize,
			remotePeer.String(), estimatedChunks, useCompression)
	} else {
		progress = ts.trackTransfer(displayName, totalSize,
			remotePeer.String(), "send", estimatedChunks, useCompression)
	}
	// D1 fix: register stream reset so CancelTransfer can stop the send goroutine.
	progress.setCancelFunc(func() { s.Reset() })
	// TS-4: store transferID + remotePeer for cancel protocol routing (R4-C2).
	progress.mu.Lock()
	progress.transferID = transferID
	progress.remotePeerID = remotePeer
	progress.mu.Unlock()

	// H7: set per-chunk relay grant byte tracker if stream goes through a relay.
	if tracker := ts.makeChunkTracker(s, "send"); tracker != nil {
		progress.setRelayTracker(tracker)
	}

	go func() {
		defer s.Close()
		// TS-5: protect/unprotect relay paths for entire goroutine lifecycle.
		// Context-based liveness prevents reaper from killing idle backup circuits
		// during long transfers (TS-5b reaper bug fix).
		sendCtx, sendCtxCancel := context.WithCancel(context.Background())
		defer sendCtxCancel()
		if ts.pathProtector != nil {
			ts.pathProtector.ProtectWithContext(sendCtx, remotePeer, protectTag)
		}
		defer func() {
			if ts.pathProtector != nil {
				ts.pathProtector.Unprotect(remotePeer, protectTag)
			}
		}()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		sendStart := time.Now()
		ts.logEvent(EventLogStarted, "send", remotePeer.String(), displayName, totalSize, 0, "", "")

		// Parallel stream config from SendOptions.
		var opener streamOpener
		var streams int
		var perTransferRate int64
		if len(opts) > 0 {
			opener = opts[0].StreamOpener
			streams = opts[0].Streams
			perTransferRate = opts[0].RateLimitBytesPerSec
		}
		// Determine actual stream count based on transport + chunk estimate.
		// VerifiedTransport so routed-private-IPv4 paths (CGNAT, Docker, VPN,
		// multi-WAN cross-link) don't trigger LAN stream counts on WAN links.
		if opener != nil {
			transport := sdk.VerifiedTransport(s, ts.hasVerifiedLANConn)
			streams = adaptiveStreamCount(transport, estimatedChunks, streams)
		} else {
			streams = 1
		}

		sendLimiter := ts.newSendRateLimiter(perTransferRate)
		rootHash, sendErr := ts.streamingSend(s, files, filePaths, cumOffsets, totalSize,
			flags, transferID, useCompression, useErasure, opener, streams, progress, sendLimiter)
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
			ts.events.Emit(sdk.Event{
				Type:        sdk.EventStreamClosed,
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
	sendLimiter *rate.Limiter,
) ([32]byte, error) {
	var zero [32]byte

	// Write header. Include erasure stripe config when erasure is active
	// (Option C: receiver needs this before any chunks arrive).
	var erasureHdr *erasureHeaderParams
	if useErasure {
		erasureHdr = &erasureHeaderParams{
			StripeSize:       defaultStripeSize,
			OverheadPerMille: overheadToPerMille(ts.erasureOverhead),
		}
	}
	if err := writeHeader(rw, files, flags, totalSize, transferID, erasureHdr); err != nil {
		return zero, fmt.Errorf("write header: %w", err)
	}

	// #40 R7-F5: if receiver takes >10s to respond, show "awaiting-approval".
	// R4-TE-2: check p.Done before overwriting status — the timer callback
	// can race with finish() if the response arrives around the 10s mark.
	awaitTimer := time.AfterFunc(10*time.Second, func() {
		if progress != nil {
			progress.mu.Lock()
			if !progress.Done {
				progress.Status = "awaiting-approval"
			}
			progress.mu.Unlock()
		}
	})

	// Wait for accept/reject/resume.
	resp, err := readMsg(rw)
	awaitTimer.Stop()
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
		// #40 F11: read optional hint byte. Older receivers send only 2 bytes
		// (msgRejectReason + reason); the hint byte is absent. Set a short
		// deadline so the read returns quickly on EOF/old-format instead of
		// blocking until stream close.
		if ns, ok := rw.(interface{ SetReadDeadline(time.Time) error }); ok {
			ns.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		}
		hintByte, hintErr := readMsg(rw)
		if hintErr != nil {
			hintByte = RejectHintNone // old receiver, no hint
		}
		_ = hintByte // hint reserved for future retry behavior tuning
		if reasonByte == RejectReasonBusy {
			return zero, fmt.Errorf("peer rejected transfer: %w", errReceiverBusy)
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
			// R6-F7: adjust sender progress to accepted-only total size.
			// Without this, sender progress never reaches 100% for selective rejection.
			if progress != nil {
				var acceptedSize int64
				for i, f := range files {
					if bf.has(i) {
						acceptedSize += f.Size
					}
				}
				progress.mu.Lock()
				progress.Size = acceptedSize
				progress.mu.Unlock()
			}
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

	// TS-4: Register in activeSends so remote receiver can cancel via cancel protocol (R3-C1).
	// Registration is inside streamingSend (goroutine context), not in SendFile,
	// to avoid cancel arriving before the goroutine starts.
	// Read remotePeerID from progress (set by SendFile before launching goroutine).
	progress.mu.Lock()
	sendRemotePeer := progress.remotePeerID
	progress.mu.Unlock()
	ts.activeSendsMu.Lock()
	ts.activeSends[transferID] = activeSendEntry{cancel: cancel, remotePeer: sendRemotePeer}
	ts.activeSendsMu.Unlock()
	defer func() {
		ts.activeSendsMu.Lock()
		delete(ts.activeSends, transferID)
		ts.activeSendsMu.Unlock()
	}()

	// Build the incremental per-stripe RS encoder if erasure is enabled.
	// newErasureEncoder returns nil when overhead <= 0 or stripeSize is
	// degenerate; chunkProducer treats a nil encoder as "no erasure". The
	// encoder replaces the legacy O(totalSize) rawForRS buffer with O(stripe)
	// peak memory. [R4-SEC1 Batch 2]
	//
	// Stripe size: always defaultStripeSize regardless of the transfer's chunk
	// count. For transfers smaller than defaultStripeSize the final partial
	// stripe handles the remainder; using the chunk-count estimate to shrink
	// stripeSize up-front would produce many more stripes than necessary if
	// the estimate under-counted, with no memory benefit. The encoder naturally
	// handles partial stripes via Finalize.
	var encoder *erasureEncoder
	if useErasure {
		encoder = newErasureEncoder(defaultStripeSize, ts.erasureOverhead)
		if encoder != nil {
			progress.mu.Lock()
			progress.ErasureOverhead = ts.erasureOverhead
			progress.mu.Unlock()
		}
	}

	go chunkProducer(ctx, files, filePaths, cumOffsets, totalSize,
		useCompression, encoder, skipBitfield, acceptBitfield, ch, done)

	if sendLimiter != nil {
		slog.Info("file-transfer: send rate limited",
			"limit", sdk.FormatBytes(ts.sendRateLimit)+"ps",
			"streams", numStreams)
	}

	// Distribute chunks: parallel (N worker streams) or single stream.
	var result producerResult
	if numStreams > 1 && openStream != nil {
		var parallelErr error
		result, parallelErr = ts.sendParallel(rw, openStream, transferID, ch, done, progress, numStreams, sendLimiter, ctx)
		if parallelErr != nil {
			cancel()
			return zero, parallelErr
		}
	} else {
		// Single-stream: read from producer, write directly.
		var singleW io.Writer = rw
		if sendLimiter != nil {
			singleW = &rateLimitedWriter{w: rw, limiter: sendLimiter, ctx: ctx}
		}
		var totalSent int64 // decompressed (logical) bytes sent
		chunksSent := 0
		for sc := range ch {
			if err := writeStreamChunkFrame(singleW, sc); err != nil {
				cancel()
				<-done
				return zero, fmt.Errorf("send chunk %d: %w", sc.chunkIdx, err)
			}
			// [B2-F29, R4-SEC1 Batch 2] Parity chunks do NOT increment ChunksDone.
			// ChunksTotal is set to len(result.chunkHashes) (data count) below, so
			// counting parity here would produce displays like "110/100 chunks".
			// Parity crossing the wire is surfaced via ParityChunksDone instead.
			if sc.fileIdx == parityFileIdx {
				progress.addParityChunkDone()
			} else {
				totalSent += int64(sc.decompSize)
				chunksSent++
				progress.updateChunks(totalSent, chunksSent)
			}
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
	rootHash := sdk.MerkleRoot(result.chunkHashes)

	// R4-SEC1 Batch 2: parity chunks have already been emitted by the
	// chunkProducer goroutine as each stripe filled (and the final partial
	// stripe at Finalize). The trailer is populated by the encoder in-band
	// and returned via producerResult.erasure. Surface the parity count for
	// the CLI (ErasureOverhead was set at encoder construction).
	if result.erasure != nil {
		progress.mu.Lock()
		progress.ErasureParity = result.erasure.ParityCount
		progress.mu.Unlock()

		// [Batch 2c] Attach per-data-chunk hashes and sizes to the erasure
		// trailer for missing-chunk recovery. The receiver uses these to
		// populate claimed hash + decompSize for chunks whose frames never
		// arrived, enabling RS reconstruction of truly missing data.
		result.erasure.ChunkHashes = result.chunkHashes
		result.erasure.ChunkSizes = result.chunkSizes
	}

	// Write trailer.
	if err := writeTrailer(rw, len(result.chunkHashes), rootHash, result.skippedHashes, result.erasure); err != nil {
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
func (ts *TransferService) HandleInbound() sdk.StreamHandler {
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
			writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
			return
		}

		// --- #40 restructured flow: peerPreAccept + deferred resource acquisition ---

		// Per-peer pre-accept counter (#40 R11-F1): bounds goroutines between
		// entry and resource acquisition. A goroutine is in exactly ONE state:
		// peerPreAccept, pending (ask-mode), or peerInbound (active transfer).
		ts.mu.Lock()
		peerTotal := ts.peerPreAccept[peerKey] + ts.peerInbound[peerKey] + ts.countPeerPending(peerKey)
		if ts.maxQueuedPerPeer > 0 && peerTotal >= ts.maxQueuedPerPeer {
			ts.mu.Unlock()
			slog.Warn("file-transfer: per-peer queue depth exceeded",
				"peer", short, "total", peerTotal, "max", ts.maxQueuedPerPeer)
			writeRejectWithHint(s, RejectReasonBusy, RejectHintAtCapacity)
			return
		}
		// #40 R4-TE-1: global cap on total pre-accept goroutines across all peers.
		// Without this, N authorized peers x maxQueuedPerPeer = unbounded goroutines
		// all reading headers simultaneously (each holds a 4KB bufio reader + goroutine stack).
		globalPreAcceptCap := cap(ts.inboundSem) * 2 // 2x global inbound capacity
		totalPreAccept := 0
		for _, v := range ts.peerPreAccept {
			totalPreAccept += v
		}
		if totalPreAccept >= globalPreAcceptCap {
			ts.mu.Unlock()
			slog.Warn("file-transfer: global pre-accept limit exceeded",
				"total", totalPreAccept, "max", globalPreAcceptCap)
			writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
			return
		}
		ts.peerPreAccept[peerKey]++
		ts.mu.Unlock()
		preAcceptReleased := false
		defer func() {
			if !preAcceptReleased {
				ts.mu.Lock()
				ts.peerPreAccept[peerKey]--
				if ts.peerPreAccept[peerKey] <= 0 {
					delete(ts.peerPreAccept, peerKey)
				}
				ts.mu.Unlock()
			}
		}()

		// Per-peer rate limit check (#40 SEC-F4: moved before header read).
		if ts.rateLimiter != nil && !ts.rateLimiter.allow(peerKey) {
			slog.Warn("file-transfer: rate limit exceeded", "peer", short)
			ts.logEvent(EventLogSpamBlocked, "receive", peerKey, "", 0, 0, "rate limit exceeded", "")
			s.Reset()
			return
		}

		// Short header deadline (I9): 30s to read header, extend after accept.
		s.SetDeadline(time.Now().Add(30 * time.Second))

		// Read streaming header.
		files, totalSize, flags, transferID, cumOffsets, erasureHdr, err := readHeader(rw)
		if err != nil {
			slog.Warn("file-transfer: bad header", "peer", short, "error", err)
			writeMsg(s, msgReject)
			return
		}
		_ = erasureHdr // wired into state below via initPerStripeState
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
		transport := sdk.VerifiedTransport(s, ts.hasVerifiedLANConn)
		if transport != sdk.TransportLAN && ts.bandwidthTracker != nil {
			var peerBudget int64
			if ts.peerBudgetFunc != nil {
				peerBudget = ts.peerBudgetFunc(peerKey)
			}
			if !ts.bandwidthTracker.check(peerKey, totalSize, peerBudget) {
				slog.Warn("file-transfer: bandwidth budget exceeded",
					"peer", short, "file", displayName, "size", totalSize)
				writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
				ts.logEvent(EventLogSpamBlocked, "receive", peerKey, displayName, totalSize, 0, "bandwidth budget exceeded", "")
				return
			}
		}

		// Temp file budget check.
		if err := ts.checkTempBudget(); err != nil {
			slog.Warn("file-transfer: temp budget exceeded",
				"peer", short, "file", displayName, "error", err)
			writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
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
		var acceptedFileIndices []int // #18: nil = full accept, non-nil = selective
		isAskMode := ts.receiveMode == ReceiveModeAsk

		if isAskMode {
			// Ask mode: release peerPreAccept (now counted in pending instead).
			ts.mu.Lock()
			ts.peerPreAccept[peerKey]--
			if ts.peerPreAccept[peerKey] <= 0 {
				delete(ts.peerPreAccept, peerKey)
			}
			ts.mu.Unlock()
			preAcceptReleased = true

			// Ask mode: queue for manual approval with timeout.
			// NO inboundSem or peerInbound held during wait (#40 F2, F3).
			pendingID := fmt.Sprintf("pending-%d-%s", time.Now().UnixNano(), randomHex(4))
			pt := &PendingTransfer{
				ID:         pendingID,
				Filename:   displayName,
				Size:       totalSize,
				PeerID:     peerKey,
				Time:       time.Now(),
				files:      files,                 // #18: store for selective rejection
				hasErasure: erasureHdr != nil,      // #18: gate selective rejection (F8)
				decision:   make(chan transferDecision, 1),
			}

			ts.mu.Lock()
			ts.pending[pendingID] = pt
			ts.mu.Unlock()

			slog.Info("file-transfer: awaiting approval",
				"peer", short, "file", displayName,
				"size", totalSize, "id", pendingID)

			if ts.events != nil {
				ts.events.Emit(sdk.Event{
					Type:        sdk.EventTransferPending,
					PeerID:      remotePeer,
					ServiceName: "file-transfer",
					Detail:      pendingID,
				})
			}

			timer := time.NewTimer(askModeTimeout)
			defer timer.Stop()

			// #40 F10: poll conn health during ask-mode wait.
			var decision transferDecision
			timedOut := false
			connClosed := false
			pollTicker := time.NewTicker(5 * time.Second)
			defer pollTicker.Stop()
		askWait:
			for {
				select {
				case decision = <-pt.decision:
					break askWait
				case <-timer.C:
					timedOut = true
					decision = transferDecision{accept: false, reason: RejectReasonBusy}
					slog.Info("file-transfer: ask mode timeout, rejecting",
						"peer", short, "file", displayName, "id", pendingID)
					break askWait
				case <-pollTicker.C:
					if s.Conn().IsClosed() {
						connClosed = true
						decision = transferDecision{accept: false, reason: RejectReasonNone}
						slog.Info("file-transfer: sender disconnected during ask mode",
							"peer", short, "id", pendingID)
						break askWait
					}
				}
			}

			ts.removePending(pendingID)

			if !decision.accept {
				if connClosed {
					ts.logEvent(EventLogCancelled, "receive", peerKey, displayName, totalSize, 0, "sender disconnected", "")
				} else if decision.reason != RejectReasonNone {
					writeRejectWithReason(s, decision.reason)
				} else {
					writeMsg(s, msgReject)
				}
				if timedOut {
					ts.logEvent(EventLogCancelled, "receive", peerKey, displayName, totalSize, 0, "ask mode timeout", "")
				} else if !connClosed {
					ts.logEvent(EventLogRejected, "receive", peerKey, displayName, totalSize, 0, "user rejected", "")
				}
				return
			}

			// #40 R5-F2: reset stream deadline after ask-mode acceptance.
			// Use transferStreamDeadline (1h) not 30s — post-accept work includes
			// checkpoint load + temp file allocation which can be slow on disk.
			// The I9 line further down sets the same deadline redundantly; that's fine.
			s.SetDeadline(time.Now().Add(transferStreamDeadline))

			// #40 R5-F3: re-check disk space after ask-mode wait.
			if err := ts.checkDiskSpace(totalSize); err != nil {
				slog.Warn("file-transfer: insufficient disk space (post-accept recheck)",
					"peer", short, "file", displayName, "error", err)
				writeRejectWithReason(s, RejectReasonSpace)
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

			acceptedFileIndices = decision.acceptedFiles // #18: propagate selective rejection
			if acceptedFileIndices != nil {
				slog.Info("file-transfer: approved (selective)",
					"peer", short, "file", displayName, "id", pendingID,
					"accepted", len(acceptedFileIndices), "total", len(files))
				ts.logEvent(EventLogAccepted, "receive", peerKey, displayName, totalSize, 0,
					fmt.Sprintf("selective: %d/%d files", len(acceptedFileIndices), len(files)), "")
			} else {
				slog.Info("file-transfer: approved",
					"peer", short, "file", displayName, "id", pendingID)
				ts.logEvent(EventLogAccepted, "receive", peerKey, displayName, totalSize, 0, "", "")
			}
		} else {
			// Non-ask mode: release peerPreAccept before acquiring peerInbound.
			ts.mu.Lock()
			ts.peerPreAccept[peerKey]--
			if ts.peerPreAccept[peerKey] <= 0 {
				delete(ts.peerPreAccept, peerKey)
			}
			ts.mu.Unlock()
			preAcceptReleased = true

			slog.Info("file-transfer: receiving",
				"peer", short, "file", displayName,
				"size", totalSize, "files", len(files),
				"compressed", flags&flagCompressed != 0)
			ts.logEvent(EventLogAccepted, "receive", peerKey, displayName, totalSize, 0, "", "")
		}

		// --- #40 post-acceptance resource acquisition (F2, F3, R5-F1, R7-F1/F2/F3) ---
		// Acquire peerInbound first (per-peer), then inboundSem (global).
		// Ask-mode: wait with timeout + cancel support. Non-ask: immediate reject.

		// #40 R7-F4: register a visible progress entry during slot wait so
		// CancelTransfer can find and abort it. Only for ask-mode (non-ask is
		// immediate, no wait). The entry is replaced by the real progress below.
		var slotWaitCtx context.Context
		var slotWaitCancel context.CancelFunc
		var slotWaitProgress *TransferProgress
		if isAskMode {
			slotWaitCtx, slotWaitCancel = context.WithCancel(context.Background())
			slotWaitProgress = ts.trackTransfer(displayName, totalSize,
				peerKey, "receive", 0, false)
			slotWaitProgress.setStatus("acquiring-slot")
			slotWaitProgress.mu.Lock()
			slotWaitProgress.cancelFunc = func() { slotWaitCancel() }
			slotWaitProgress.mu.Unlock()
			// Register cancel func so CancelTransfer calls it.
			ts.jobCancelMu.Lock()
			ts.jobCancels[slotWaitProgress.ID] = slotWaitCancel
			ts.jobCancelMu.Unlock()
		}
		defer func() {
			if slotWaitCancel != nil {
				slotWaitCancel()
			}
			if slotWaitProgress != nil {
				ts.jobCancelMu.Lock()
				delete(ts.jobCancels, slotWaitProgress.ID)
				ts.jobCancelMu.Unlock()
				// Remove the slot-wait entry if it was never upgraded to real progress.
				ts.mu.Lock()
				if p, ok := ts.transfers[slotWaitProgress.ID]; ok {
					p.mu.Lock()
					isSlot := p.Status == "acquiring-slot"
					p.mu.Unlock()
					if isSlot {
						delete(ts.transfers, slotWaitProgress.ID)
					}
				}
				ts.mu.Unlock()
			}
		}()

		if isAskMode {
			// Channel-notify wait for peerInbound slot (#40 R7-F1).
			deadline := time.NewTimer(askModeTimeout)
			defer deadline.Stop()
			acquired := false
		peerSlotWait:
			for {
				ts.mu.Lock()
				if ts.peerInbound[peerKey] < ts.maxPerPeerTransfers {
					ts.peerInbound[peerKey]++
					ts.mu.Unlock()
					acquired = true
					break peerSlotWait
				}
				// Snapshot notify channel under lock to avoid race.
				notify := ts.peerSlotNotify
				ts.mu.Unlock()
				select {
				case <-notify:
					continue peerSlotWait
				case <-deadline.C:
					break peerSlotWait
				case <-slotWaitCtx.Done():
					break peerSlotWait
				}
			}
			if !acquired {
				if slotWaitProgress != nil {
					if slotWaitCtx.Err() != nil {
						slotWaitProgress.finish(fmt.Errorf("cancelled"))
					} else {
						slotWaitProgress.finish(fmt.Errorf("slot wait timeout"))
					}
				}
				if slotWaitCtx.Err() != nil {
					slog.Info("file-transfer: slot wait cancelled by user",
						"peer", short)
					writeMsg(s, msgReject)
				} else {
					slog.Warn("file-transfer: per-peer slot wait timeout after accept",
						"peer", short, "max", ts.maxPerPeerTransfers)
					writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
				}
				return
			}
		} else {
			// Non-ask: immediate per-peer check.
			ts.mu.Lock()
			if ts.peerInbound[peerKey] >= ts.maxPerPeerTransfers {
				ts.mu.Unlock()
				slog.Warn("file-transfer: per-peer limit reached",
					"peer", short, "max", ts.maxPerPeerTransfers)
				writeRejectWithHint(s, RejectReasonBusy, RejectHintAtCapacity)
				return
			}
			ts.peerInbound[peerKey]++
			ts.mu.Unlock()
		}
		// peerInbound acquired — defer decrement with broadcast (#40 F7, R7-F1).
		// sync.Once guarantees exactly-once execution even when called from
		// multiple goroutines (CancelTransfer calls finish()->postFinish from
		// HTTP handler goroutine; HandleInbound defer runs in stream goroutine).
		var releasePeerInboundOnce sync.Once
		releasePeerInbound := func() {
			releasePeerInboundOnce.Do(func() {
				ts.mu.Lock()
				ts.peerInbound[peerKey]--
				if ts.peerInbound[peerKey] <= 0 {
					delete(ts.peerInbound, peerKey)
				}
				// Broadcast: close+recreate notify channel to wake all waiters.
				close(ts.peerSlotNotify)
				ts.peerSlotNotify = make(chan struct{})
				ts.mu.Unlock()
			})
		}
		defer releasePeerInbound()

		// Acquire inboundSem (global capacity).
		if isAskMode {
			// #40 R7-F2: wait with timeout after ask-mode acceptance.
			semTimer := time.NewTimer(askModeTimeout)
			defer semTimer.Stop()
			select {
			case ts.inboundSem <- struct{}{}:
			case <-semTimer.C:
				if slotWaitProgress != nil {
					slotWaitProgress.finish(fmt.Errorf("global capacity wait timeout"))
				}
				slog.Warn("file-transfer: global capacity wait timeout after accept",
					"peer", short)
				writeRejectWithHint(s, RejectReasonBusy, RejectHintTransient)
				return
			case <-slotWaitCtx.Done():
				if slotWaitProgress != nil {
					slotWaitProgress.finish(fmt.Errorf("cancelled"))
				}
				slog.Info("file-transfer: slot wait cancelled", "peer", short)
				writeMsg(s, msgReject)
				return
			}
		} else {
			// Non-ask: immediate global capacity check.
			select {
			case ts.inboundSem <- struct{}{}:
			default:
				slog.Warn("file-transfer: at capacity, rejecting",
					"peer", short, "max", cap(ts.inboundSem))
				writeRejectWithHint(s, RejectReasonBusy, RejectHintAtCapacity)
				return
			}
		}
		defer func() { <-ts.inboundSem }()

		// TS-5: protect relay paths during inbound transfer (C3).
		// Context-based liveness prevents reaper from killing idle backup circuits
		// during long transfers (TS-5b reaper bug fix).
		recvProtectTag := fmt.Sprintf("recv-%x", transferID[:8])
		recvCtx, recvCtxCancel := context.WithCancel(context.Background())
		defer recvCtxCancel()
		if ts.pathProtector != nil {
			ts.pathProtector.ProtectWithContext(recvCtx, remotePeer, recvProtectTag)
			defer ts.pathProtector.Unprotect(remotePeer, recvProtectTag)
		}

		// Compute content key for cross-session resume (R3-IMP5, R4-IMP2).
		ck := contentKey(files)

		// Check for existing checkpoint (resume support).
		var resumeState *streamReceiveState
		var resumeBitfield *bitfield
		ckpt, ckptErr := loadCheckpoint(destDir, ck)
		if ckptErr == nil && ckpt != nil {
			// Validate checkpoint matches current transfer.
			if ckpt.totalSize == totalSize && len(ckpt.files) == len(files) && ckpt.flags == flags {
				restored, restoreErr := ckpt.restoreReceiveState(destDir)
				if restoreErr == nil {
					// F11: compare checkpoint's accept bitfield with new decision.
					// If user changed file selection, delete checkpoint and start fresh.
					if !acceptBitsMatch(ckpt.acceptBits, acceptedFileIndices, len(files)) {
						slog.Info("file-transfer: file selection changed, discarding checkpoint",
							"peer", short, "file", displayName)
						restored.cleanup()
						ckpt.cleanupTempFiles(destDir)
						removeStreamCheckpoint(destDir, ck)
					} else {
						resumeState = restored
						resumeBitfield = ckpt.have
						slog.Info("file-transfer: resuming from checkpoint",
							"peer", short, "file", displayName,
							"have", ckpt.have.count(), "total_est", len(ckpt.hashes))
					}
				} else {
					slog.Debug("file-transfer: checkpoint restore failed, starting fresh",
						"error", restoreErr)
					// Clean up stale temp files from failed restore.
					ckpt.cleanupTempFiles(destDir)
					removeStreamCheckpoint(destDir, ck)
				}
			} else {
				// Flags/size mismatch - discard stale checkpoint and temp files.
				slog.Debug("file-transfer: checkpoint flags/size mismatch, starting fresh")
				ckpt.cleanupTempFiles(destDir)
				removeStreamCheckpoint(destDir, ck)
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
			// Fresh transfer: send accept bitfield (F2, #18 selective rejection).
			acceptBF := newBitfield(len(files))
			if acceptedFileIndices != nil {
				// Selective: only set bits for accepted files.
				for _, idx := range acceptedFileIndices {
					acceptBF.set(idx)
				}
				// F5: all-rejected check (defense in depth, AcceptTransfer also checks).
				if isAllRejected(acceptBF) {
					writeMsg(s, msgReject)
					ts.logEvent(EventLogRejected, "receive", peerKey, displayName, totalSize, 0, "all files excluded", "")
					return
				}
				slog.Info("file-transfer: selective accept",
					"peer", short, "file", displayName,
					"accepted", len(acceptedFileIndices), "total", len(files))
			} else {
				// Full accept: set all bits.
				for i := range files {
					acceptBF.set(i)
				}
			}
			if err := writeAcceptBitfield(rw, len(files), acceptBF); err != nil {
				slog.Error("file-transfer: accept write failed", "error", err)
				return
			}
		}

		// Extend deadline after accept/resume (I9).
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Compute effective size and chunk estimate for selective rejection (#18, F12).
		effectiveSize := totalSize
		if acceptedFileIndices != nil && len(acceptedFileIndices) < len(files) {
			effectiveSize = 0
			for _, idx := range acceptedFileIndices {
				effectiveSize += files[idx].Size
			}
		}
		estimatedChunks := estimateChunkCount(effectiveSize)

		// Create or reuse streaming receive state.
		var state *streamReceiveState
		if resumeState != nil {
			state = resumeState
		} else {
			state = newStreamReceiveState(files, totalSize, flags, cumOffsets)

			// F13: wire accept bitfield into state for selective rejection.
			// Must be set BEFORE allocateTempFiles (which checks it to skip rejected files).
			if acceptedFileIndices != nil {
				bf := newBitfield(len(files))
				for _, idx := range acceptedFileIndices {
					bf.set(idx)
				}
				state.acceptBitfield = bf
			}

			// Allocate temp files for each accepted file entry.
			if err := state.allocateTempFiles(destDir); err != nil {
				slog.Error("file-transfer: allocate temp files failed", "error", err)
				return
			}

			// Initialize duplicate detection bitfield (R3-IMP3).
			state.initReceivedBitfield(estimatedChunks)
		}

		// Initialize per-stripe parity tracking (Option C). Must run for both
		// fresh and resumed states so the receiver can route parity to slots.
		if erasureHdr != nil {
			state.initPerStripeState(erasureHdr)
		}
		defer state.cleanup() // R4-SEC2: always clean up temp files on any exit

		// #40 R7-F4: remove slot-wait placeholder before creating real progress.
		// Delete jobCancels FIRST so CancelTransfer can't fire a stale cancel
		// while the transfers entry is already gone (R5-TE-2 race fix).
		if slotWaitProgress != nil {
			ts.jobCancelMu.Lock()
			delete(ts.jobCancels, slotWaitProgress.ID)
			ts.jobCancelMu.Unlock()
			ts.mu.Lock()
			delete(ts.transfers, slotWaitProgress.ID)
			ts.mu.Unlock()
			slotWaitProgress = nil // prevent defer from re-deleting
		}

		progress := ts.trackTransfer(displayName, effectiveSize,
			peerKey, "receive", estimatedChunks, flags&flagCompressed != 0)
		progress.setStatus("active")
		// #40 F7: release peerInbound at finish() for immediate slot availability.
		progress.mu.Lock()
		progress.postFinish = releasePeerInbound
		// TS-4: store transferID + remotePeer for cancel protocol routing (R4-C2).
		progress.transferID = transferID
		progress.remotePeerID = remotePeer
		progress.mu.Unlock()

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
			// R8-F3: log selective info in completion.
			if acceptedFileIndices != nil {
				slog.Info("file-transfer: received (selective)",
					"peer", short, "file", displayName,
					"accepted", len(acceptedFileIndices), "total_files", len(files),
					"size", effectiveSize)
			} else {
				slog.Info("file-transfer: received",
					"peer", short, "file", displayName,
					"size", totalSize, "files", len(files))
			}
			if acceptedFileIndices != nil {
				ts.logEventSelective(EventLogCompleted, "receive", peerKey, displayName, effectiveSize, effectiveSize, "", dur, len(acceptedFileIndices), len(files))
			} else {
				ts.logEvent(EventLogCompleted, "receive", peerKey, displayName, effectiveSize, effectiveSize, "", dur)
			}
			if transport != sdk.TransportLAN && ts.bandwidthTracker != nil {
				ts.bandwidthTracker.record(peerKey, effectiveSize)
			}
		}

		if ts.events != nil {
			ts.events.Emit(sdk.Event{
				Type:        sdk.EventStreamClosed,
				PeerID:      remotePeer,
				ServiceName: "file-transfer",
			})
		}
	}
}

// blake3Hash computes BLAKE3-256 of data.
func blake3Hash(data []byte) [32]byte {
	// Import is in chunker.go; use zeebo/blake3 directly.
	return sdk.Blake3Sum(data)
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

// newShadowSendProgress builds an xfer-* progress object that is NOT registered
// in ts.transfers. Used by the queued send path where a parent q-* progress
// already represents the transfer for CLI/listing purposes. Eliminates the
// duplicate q-*/xfer-* rows that used to appear in `shurli transfers`.
func newShadowSendProgress(filename string, size int64, peerID string, chunkCount int, compressed bool) *TransferProgress {
	return &TransferProgress{
		ID:          fmt.Sprintf("xfer-%s", randomHex(6)),
		Filename:    filename,
		Size:        size,
		ChunksTotal: chunkCount,
		Compressed:  compressed,
		PeerID:      peerID,
		Direction:   "send",
		Status:      "pending",
		StartTime:   time.Now(),
	}
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

// evictCompletedTransfers removes completed transfer entries older than 5 minutes
// from ts.transfers. Prevents unbounded memory growth (#40 F8).
func (ts *TransferService) evictCompletedTransfers() {
	cutoff := time.Now().Add(-5 * time.Minute)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for id, p := range ts.transfers {
		p.mu.Lock()
		done := p.Done
		end := p.EndTime
		p.mu.Unlock()
		if done && !end.IsZero() && end.Before(cutoff) {
			delete(ts.transfers, id)
		}
	}
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
	activeIDs := make(map[string]bool, len(ts.transfers))
	for _, p := range ts.transfers {
		activeTransfers = append(activeTransfers, p.Snapshot())
		activeIDs[p.ID] = true
	}
	ts.mu.RUnlock()

	// Include queued (pending) transfers as synthetic progress entries.
	// Skip queue entries that already have an active transfer (same ID)
	// to avoid duplicate lines when a queued job becomes active.
	queued := ts.queue.Pending()
	result := make([]TransferSnapshot, 0, len(activeTransfers)+len(queued))
	for _, qt := range queued {
		if activeIDs[qt.ID] {
			continue
		}
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

	// TS-5b: Send-side failover via requeue.
	maxSendFailovers    = 3  // max path failover requeues per job
	maxTotalJobAttempts = 10 // cap across ALL retry mechanisms (R5-F8)
)

// queuedJob holds everything needed to execute a queued transfer.
type queuedJob struct {
	queueID         string
	filePath        string
	isDir           bool
	peerID          string
	priority        TransferPriority
	opts            SendOptions
	openStream      streamOpener
	progress        *TransferProgress // synthetic "queued" progress visible to CLI
	retryCount      int               // number of retries so far
	relayReconnects int               // relay session expiry reconnection attempts (H11)
	lastRelayPeerID peer.ID           // relay peer from last attempt (for session expiry detection)

	// TS-5b: Send-side failover state.
	failoverAttempts int   // path failover requeue count
	cumulativeBytes  int64 // bytes transferred before failover (R3-F5: progress continuity)
}

// totalAttempts returns the sum of all retry mechanisms for this job (R5-F8).
func (j *queuedJob) totalAttempts() int {
	return j.retryCount + j.relayReconnects + j.failoverAttempts
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

	// Compute size for queue progress display.
	var queueSize int64
	if info.IsDir() {
		filepath.WalkDir(filePath, func(_ string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d == nil || !d.Type().IsRegular() {
				return walkErr
			}
			if fi, err := d.Info(); err == nil {
				queueSize += fi.Size()
			}
			return nil
		})
	} else {
		queueSize = info.Size()
	}

	progress := &TransferProgress{
		ID:        queueID,
		Filename:  filepath.Base(filePath),
		PeerID:    peerID,
		Size:      queueSize,
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
							"dir_size", sdk.FormatBytes(dirSize),
							"relay_budget", sdk.FormatBytes(grantInfo.SessionBudget))

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
									sdk.FormatBytes(dirSize), sdk.FormatBytes(retryCheck.SessionBudget))
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
			// Bug #30 fix: use SendFile directly (it handles directories natively)
			// with internalShadow + pollSendProgress, matching the single-file path.
			// This gives live progress updates, proper cancel propagation, and correct
			// Transferred accounting for TS-5b failover (cumulativeBytes).
			stream, streamErr := job.openStream()
			if streamErr != nil {
				finalErr = fmt.Errorf("open stream for directory: %w", streamErr)
			} else {
				sendOpts := job.opts
				sendOpts.internalShadow = true
				sendProgress, sendErr := ts.SendFile(stream, job.filePath, sendOpts)
				if sendErr != nil {
					finalErr = sendErr
				} else {
					finalErr = pollSendProgress(jobCtx, stream, sendProgress, job.progress, job.cumulativeBytes)
				}
			}
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
									"file_size", sdk.FormatBytes(fileSize),
									"relay_budget", sdk.FormatBytes(recheckInfo.SessionBudget))

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
											sdk.FormatBytes(fileSize), sdk.FormatBytes(finalCheck.SessionBudget))
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
				// We need to wait for it to complete. internalShadow keeps the
				// xfer-* progress out of ts.transfers so the CLI shows one
				// row per logical transfer (q-* only) instead of q-* + xfer-*.
				sendOpts := job.opts
				sendOpts.internalShadow = true
				sendProgress, sendErr := ts.SendFile(stream, job.filePath, sendOpts)
				if sendErr != nil {
					finalErr = sendErr
				} else {
					// Copy the real transfer's progress into our queued progress tracker.
					// D1 fix: check jobCtx.Done() so cancel propagates to the polling loop.
					// When cancelled, reset the stream to stop the underlying SendFile goroutine.
					finalErr = pollSendProgress(jobCtx, stream, sendProgress, job.progress, job.cumulativeBytes)
				}
			}
		}
	}

	// TS-5b: Send-side path failover via requeue (R5-F1).
	// Checked FIRST before relay-session-expiry (R5-F4). Uses the existing requeue
	// pattern: PathDialer + managed relay handles path selection on re-execution.
	//
	// R5-F7 KNOWN LIMITATION: For directory sends (SendDirectory), cancel-before-retry
	// is not performed because SendDirectory doesn't expose the internal transferID.
	// The old goroutine times out via QUIC idle timeout (~30s). This is bounded and
	// acceptable. Fix path: add TransferID() method to SendDirectory's return value.
	if finalErr != nil && job.totalAttempts() < maxTotalJobAttempts &&
		job.failoverAttempts < maxSendFailovers &&
		classifyTransferError(finalErr, ts.queueCtx) == retryableNetwork {

		job.failoverAttempts++

		// R3-F5: Accumulate transferred bytes for progress continuity.
		// The next pollSendProgress call adds this offset.
		job.progress.mu.Lock()
		job.cumulativeBytes = job.progress.Transferred
		job.progress.Failovers++
		job.progress.mu.Unlock()

		// R5-F4: Reset circuit counters for relay cleanup (belt-and-suspenders).
		if job.lastRelayPeerID != "" && ts.grantChecker != nil {
			ts.grantChecker.ResetCircuitCounters(job.lastRelayPeerID)
		}

		delay := failoverBackoff(job.failoverAttempts)
		slog.Info("file-transfer: send path failover",
			"id", job.queueID, "attempt", job.failoverAttempts, "delay", delay)
		ts.logEvent(EventLogPathFailover, "send", job.peerID, job.filePath, 0,
			job.cumulativeBytes, finalErr.Error(),
			fmt.Sprintf("attempt=%d", job.failoverAttempts))
		job.progress.setStatus("path-failover")

		select {
		case <-jobCtx.Done():
			job.progress.finish(fmt.Errorf("cancelled during send failover backoff"))
			ts.queue.Complete(job.queueID)
			return
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
			return // Don't complete — job retries via requeue.
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
			ts.queue.Complete(job.queueID)
			return
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
			ts.queue.Complete(job.queueID)
			return
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
	ts.markCompleted(job.progress.ID) // #40 F5: was missing, entries leaked in ts.transfers

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

// errReceiverBusy is the typed error for receiver busy rejections (#40 F9).
var errReceiverBusy = errors.New("receiver busy")

// isRetryableReject returns true if the error indicates a transient rejection
// that should be retried (receiver busy, rate limit, etc.).
func isRetryableReject(err error) bool {
	if errors.Is(err, errReceiverBusy) {
		return true
	}
	// Backward compat: string match for errors from older receivers.
	return strings.Contains(err.Error(), "receiver busy")
}

// pollSendProgress copies progress from a SendFile operation into the queued job's
// progress tracker. Exits when the send completes or the context is cancelled.
// On cancel, resets the stream to stop the underlying SendFile goroutine.
// cumulativeOffset is added to Transferred for TS-5b progress continuity (R3-F5).
func pollSendProgress(ctx context.Context, stream network.Stream, sendProgress, jobProgress *TransferProgress, cumulativeOffset int64) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		snap := sendProgress.Snapshot()
		jobProgress.mu.Lock()
		jobProgress.Size = snap.Size
		jobProgress.Transferred = cumulativeOffset + snap.Transferred
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
	var cancelTransferID [32]byte
	var cancelRemotePeer peer.ID
	found := false

	ts.mu.Lock()
	if p, ok := ts.transfers[resolved]; ok {
		// Read p.Done, p.cancelFunc, and TS-4 fields under p.mu.
		p.mu.Lock()
		done := p.Done
		if !done {
			found = true
			progressCancel = p.cancelFunc
			cancelTransferID = p.transferID
			cancelRemotePeer = p.remotePeerID
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

	// TS-4: Send cancel on all available paths in background (R2-I8).
	// Fire-and-forget — existing s.Reset() already handled the primary path.
	if ts.hostRef != nil && cancelRemotePeer != "" {
		var zeroID [32]byte
		if cancelTransferID != zeroID {
			go sendMultiPathCancel(ts.hostRef, cancelRemotePeer, cancelTransferID, ts.pathProtector)
		}
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
	Version int                   `json:"version"`
	HMAC    string                `json:"hmac"` // hex-encoded HMAC-SHA256 of entries JSON
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
		if e.IsDir() || !isShurliTempFile(e.Name()) {
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
		if e.IsDir() || !isShurliTempFile(e.Name()) {
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

// isShurliTempFile returns true if the filename matches any Shurli temp file prefix.
// IF9-1: includes multi-peer temp files (.shurli-mp-*) and orphaned checkpoints (.shurli-ckpt-*).
func isShurliTempFile(name string) bool {
	return strings.HasPrefix(name, ".shurli-tmp-") ||
		strings.HasPrefix(name, ".shurli-mp-") ||
		strings.HasPrefix(name, ".shurli-ckpt-")
}

// isActiveTempFile returns true if the temp file was modified recently enough
// to be part of an active download (IF9-7: don't delete active downloads).
func isActiveTempFile(info os.FileInfo) bool {
	return time.Since(info.ModTime()) < 5*time.Minute
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
		name := e.Name()
		// IF9-1: Clean multi-peer temp files (.shurli-mp-*) and orphaned checkpoints (.shurli-ckpt-*).
		if e.IsDir() || (!strings.HasPrefix(name, ".shurli-tmp-") &&
			!strings.HasPrefix(name, ".shurli-mp-") &&
			!strings.HasPrefix(name, ".shurli-ckpt-")) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// IF9-7: Skip multi-peer temp files that may be actively in use.
		if (strings.HasPrefix(name, ".shurli-mp-") || strings.HasPrefix(name, ".shurli-ckpt-")) && isActiveTempFile(info) {
			continue
		}
		path := filepath.Join(ts.receiveDir, name)
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

// SetHost sets the libp2p host reference needed for multi-path cancel (TS-4).
// Called from plugin.Start() after TransferService creation.
func (ts *TransferService) SetHost(h host.Host) {
	ts.hostRef = h
}

// SetPathProtector sets the path protector for relay path protection during transfers (TS-5).
// Called from plugin.Start() after TransferService creation.
func (ts *TransferService) SetPathProtector(pp *sdk.PathProtector) {
	ts.pathProtector = pp
}

// SetNetwork sets the SDK network reference and download service name for
// TS-5b failover. The retry loop uses HedgedOpenStream to open streams on
// backup paths with the full security pipeline (R2-F2, R2-F12).
func (ts *TransferService) SetNetwork(n *sdk.Network, downloadSvc string) {
	ts.networkRef = n
	ts.downloadServiceName = downloadSvc
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

// logEventSelective logs a transfer event with selective file rejection info (#18 R8-F3).
func (ts *TransferService) logEventSelective(eventType, direction, peerID, fileName string, fileSize, bytesDone int64, errStr, duration string, acceptedFiles, totalFiles int) {
	if ts.logger == nil {
		return
	}
	ts.logger.Log(TransferEvent{
		Timestamp:     time.Now(),
		EventType:     eventType,
		Direction:     direction,
		PeerID:        peerID,
		FileName:      fileName,
		FileSize:      fileSize,
		BytesDone:     bytesDone,
		Error:         errStr,
		Duration:      duration,
		AcceptedFiles: acceptedFiles,
		TotalFiles:    totalFiles,
	})
}

// --- Ask mode: pending transfer management ---

// ListPending returns snapshots of all pending transfers awaiting approval.
// Includes per-file info for selective rejection (#18).
func (ts *TransferService) ListPending() []PendingTransfer {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]PendingTransfer, 0, len(ts.pending))
	for _, p := range ts.pending {
		pt := PendingTransfer{
			ID:         p.ID,
			Filename:   p.Filename,
			Size:       p.Size,
			PeerID:     p.PeerID,
			Time:       p.Time,
			hasErasure: p.hasErasure,
		}
		// Copy file entries so the caller can't mutate the pending state.
		if len(p.files) > 0 {
			pt.files = make([]fileEntry, len(p.files))
			copy(pt.files, p.files)
		}
		result = append(result, pt)
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

// AcceptTransfer approves a pending transfer with optional file selection (#18).
// Supports short ID prefix matching (like git).
//
// acceptedFiles: 0-indexed file indices to accept (nil = all files).
// Validates indices against the file table. Gates selective rejection when
// erasure coding is active (F8) or when all files would be rejected (F5).
func (ts *TransferService) AcceptTransfer(id, dest string, acceptedFiles []int) error {
	ts.mu.RLock()
	_, p, err := ts.findPendingByPrefix(id)
	ts.mu.RUnlock()
	if err != nil {
		return err
	}

	// Validate file selection against the pending transfer's file table.
	if acceptedFiles != nil {
		fileCount := len(p.files)
		if fileCount == 0 {
			return fmt.Errorf("transfer %q has no file list for selective rejection", id)
		}

		// F8: gate selective rejection when erasure is active.
		if p.hasErasure {
			return fmt.Errorf("selective file rejection is not available for this transfer because " +
				"the sender uses erasure coding (typical for WAN/relay transfers). " +
				"Accept all files with 'shurli accept %s', or reject with 'shurli reject %s'", id, id)
		}

		// F2/F7: validate indices are in range.
		seen := make(map[int]bool, len(acceptedFiles))
		for _, idx := range acceptedFiles {
			if idx < 0 || idx >= fileCount {
				return fmt.Errorf("file index %d out of range (transfer has %d files, valid range 0-%d)", idx, fileCount, fileCount-1)
			}
			seen[idx] = true
		}
		// F6: deduplicate.
		deduped := make([]int, 0, len(seen))
		for idx := range seen {
			deduped = append(deduped, idx)
		}
		acceptedFiles = deduped

		// F5: if all files are excluded, that's a full reject.
		if len(acceptedFiles) == 0 {
			return fmt.Errorf("no files selected; use 'shurli reject %s' to reject the entire transfer", id)
		}
		// R7-F9: if selective rejection results in all files rejected.
		if len(acceptedFiles) == fileCount {
			// All files accepted = full accept, clear selection.
			acceptedFiles = nil
		}
	}

	select {
	case p.decision <- transferDecision{accept: true, dest: dest, acceptedFiles: acceptedFiles}:
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

// downloadContext holds all state from a download negotiation, needed by
// receiveParallel and the TS-5b retry loop. Returned by downloadNegotiate (R6-F1).
type downloadContext struct {
	rw              io.ReadWriter
	files           []fileEntry
	totalSize       int64
	flags           uint8
	transferID      [32]byte
	cumOffsets      []int64
	displayName     string
	contentKey      [32]byte
	estimatedChunks int
	session         *parallelSession
	state           *streamReceiveState  // non-nil if checkpoint restored or passed in
	erasureHdr      *erasureHeaderParams // Option C: stripe config from header
}

// TS-5b failover constants.
const (
	maxDownloadFailovers = 3  // max consecutive failures without progress (F9)
	maxTotalFailovers    = 10 // max total failovers per transfer regardless of progress (R2-F7)

	// Backoff between failover attempts (F9).
	failoverBackoff0 = 500 * time.Millisecond
	failoverBackoff1 = 2 * time.Second
	failoverBackoff2 = 5 * time.Second
)

// failoverBackoff returns the delay for a given failover attempt number (F9).
func failoverBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return failoverBackoff0
	case attempt == 2:
		return failoverBackoff1
	default:
		return failoverBackoff2
	}
}

// downloadNegotiate opens a download on a new stream and negotiates resume
// from in-memory state or disk checkpoint. Called by the TS-5b retry loop
// inside ReceiveFrom's goroutine (R6-F1, R4-F1, F13).
//
// NOTE: This function's negotiation flow parallels ReceiveFrom's first-attempt
// path (lines 3776-3903). Per R4-F1, the first attempt stays in ReceiveFrom
// for synchronous error return semantics. Changes to negotiation logic must be
// applied in BOTH places.
//
// existingState: non-nil on retry (use in-memory bitfield for resume, skip disk).
// nil on first attempt from disk checkpoint path (normal ReceiveFrom handles that).
//
// On error, any registered session is cleaned up before returning.
func (ts *TransferService) downloadNegotiate(
	s network.Stream,
	remotePath, destDir string,
	existingState *streamReceiveState,
) (*downloadContext, error) {
	// Send download request on the new stream.
	prefixed, err := RequestDownload(s, remotePath)
	if err != nil {
		return nil, fmt.Errorf("download request: %w", err)
	}

	rw := struct {
		io.Reader
		io.Writer
	}{prefixed, s}

	s.SetDeadline(time.Now().Add(transferStreamDeadline))

	// Read header from sender.
	files, totalSize, flags, transferID, cumOffsets, erasureHdr, err := readHeader(rw)
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

	// Size limit check.
	if ts.maxSize > 0 && totalSize > ts.maxSize {
		writeMsg(s, msgReject)
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", totalSize, ts.maxSize)
	}

	// Disk space check (R6-F4: re-check on retry, account for existing temp data).
	_ = erasureHdr // wired into downloadContext for initPerStripeState
	neededSpace := totalSize
	if existingState != nil {
		neededSpace = totalSize - existingState.ReceivedBytes()
		if neededSpace < 0 {
			neededSpace = 0
		}
	}
	if err := checkDiskSpaceAt(destDir, neededSpace); err != nil {
		writeRejectWithReason(s, RejectReasonSpace)
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	// Compute content key.
	ck := contentKey(files)

	// Check if content changed on sender (R4-F6).
	if existingState != nil {
		// Compare content key from new header vs previous state.
		// If mismatch: file changed, can't resume — start fresh.
		prevCK := contentKey(existingState.files)
		if ck != prevCK {
			existingState.cleanup()
			existingState = nil
			slog.Info("file-download: content changed on sender since last attempt, starting fresh")
		}
	}

	// Register parallel session.
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

	if existingState != nil {
		// Resume from in-memory state (R3-F2): build bitfield directly
		// from existingState's receivedBitfield, skip disk checkpoint.
		resumeBF := existingState.ReceivedBitfield()
		if resumeBF == nil {
			resumeBF = newBitfield(0)
		}
		if err := writeResumeRequest(rw, resumeBF); err != nil {
			ts.unregisterParallelSession(transferID)
			return nil, fmt.Errorf("write resume request: %w", err)
		}
		resp, respErr := readMsg(rw)
		if respErr != nil || resp != msgResumeResponse {
			ts.unregisterParallelSession(transferID)
			return nil, fmt.Errorf("resume response: err=%v resp=0x%02x", respErr, resp)
		}
	} else {
		// Check for disk checkpoint.
		ckpt, ckptErr := loadCheckpoint(destDir, ck)
		if ckptErr == nil && ckpt != nil &&
			ckpt.totalSize == totalSize && len(ckpt.files) == len(files) && ckpt.flags == flags {
			restored, restoreErr := ckpt.restoreReceiveState(destDir)
			if restoreErr == nil {
				existingState = restored
				resumeBF := ckpt.have
				if err := writeResumeRequest(rw, resumeBF); err != nil {
					ts.unregisterParallelSession(transferID)
					restored.cleanup()
					return nil, fmt.Errorf("write resume request: %w", err)
				}
				resp, respErr := readMsg(rw)
				if respErr != nil || resp != msgResumeResponse {
					ts.unregisterParallelSession(transferID)
					restored.cleanup()
					return nil, fmt.Errorf("resume response: err=%v resp=0x%02x", respErr, resp)
				}
				// Initialize per-stripe state on the restored state so parity
				// routing works immediately when chunks arrive on the new
				// stream. [Deep audit fix: Bug #2]
				if erasureHdr != nil {
					restored.initPerStripeState(erasureHdr)
				}
				slog.Info("file-download: resuming from checkpoint",
					"file", displayName, "have", ckpt.have.count())
			} else {
				slog.Debug("file-download: checkpoint restore failed, starting fresh", "error", restoreErr)
				ckpt.cleanupTempFiles(destDir)
				removeStreamCheckpoint(destDir, ck)
			}
		} else if ckptErr == nil && ckpt != nil {
			// Checkpoint exists but mismatches — clean up.
			slog.Debug("file-download: checkpoint flags/size mismatch, starting fresh")
			ckpt.cleanupTempFiles(destDir)
			removeStreamCheckpoint(destDir, ck)
		}

		if existingState == nil {
			// Fresh transfer: send accept bitfield.
			// R6-F19: mirrors ReceiveFrom's selective bitfield logic.
			// On retry (existingState != nil), the resume path preserves
			// the original accept bitfield from the first attempt's state.
			acceptBF := newBitfield(len(files))
			for i := range files {
				acceptBF.set(i)
			}
			if err := writeAcceptBitfield(rw, len(files), acceptBF); err != nil {
				ts.unregisterParallelSession(transferID)
				return nil, fmt.Errorf("write accept: %w", err)
			}
		}
	}

	estimatedChunks := estimateChunkCount(totalSize)

	return &downloadContext{
		rw:              rw,
		files:           files,
		totalSize:       totalSize,
		flags:           flags,
		transferID:      transferID,
		cumOffsets:      cumOffsets,
		displayName:     displayName,
		contentKey:      ck,
		estimatedChunks: estimatedChunks,
		session:         session,
		state:           existingState,
		erasureHdr:      erasureHdr,
	}, nil
}

// ReceiveFrom initiates a receiver-side download. It sends a download request
// on the given stream, reads the SHFT manifest from the sharer, auto-accepts,
// and receives the file to destDir (or the default receive directory if empty).
//
// This is the inverse of a push transfer: the receiver opens the stream and
// pulls data. The sharer's HandleDownload handler calls SendFile(), which
// writes SHFT manifest + chunks. This method reads that data.
// ReceiveFrom initiates a download from a remote peer's shared file.
// sel: file selection for selective download (#18). nil = download all files.
func (ts *TransferService) ReceiveFrom(s network.Stream, remotePath, destDir string, sel ...*FileSelection) (*TransferProgress, error) {
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
	files, totalSize, flags, transferID, cumOffsets, erasureHdr2, err := readHeader(rw)
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

	// TS-5: protect relay paths during download (C3).
	// F32 fix: Protect/Unprotect moved into the background goroutine so
	// managed relay connections stay protected for the entire transfer
	// lifecycle (including TS-5b retry). The old defer here fired when
	// ReceiveFrom returned (immediately), not when the goroutine finished.
	dlProtectTag := fmt.Sprintf("dl-%x", transferID[:8])

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

	// #18: resolve file selection ONCE so we can compare against checkpoint
	// and use throughout the function (eliminates multiple sel[0].resolve() calls).
	// Validation happens here (not deferred to the fresh path).
	var dlNewAccepted []int // nil = full download
	if len(sel) > 0 && sel[0] != nil {
		var selErr error
		dlNewAccepted, selErr = sel[0].resolve(len(files))
		if selErr != nil {
			return nil, fmt.Errorf("selective download: %w", selErr)
		}
		if dlNewAccepted != nil && len(dlNewAccepted) >= len(files) {
			dlNewAccepted = nil // all files = full accept
		}
	}

	// Check for existing checkpoint (resume support).
	var resumeState *streamReceiveState
	var resumeBitfield *bitfield
	ckpt, ckptErr := loadCheckpoint(destDir, ck)
	if ckptErr == nil && ckpt != nil {
		if ckpt.totalSize == totalSize && len(ckpt.files) == len(files) && ckpt.flags == flags {
			restored, restoreErr := ckpt.restoreReceiveState(destDir)
			if restoreErr == nil {
				// F11: compare checkpoint's accept bitfield with current selection.
				// Mirrors HandleInbound's acceptBitsMatch check (transfer.go:2060).
				if !acceptBitsMatch(ckpt.acceptBits, dlNewAccepted, len(files)) {
					slog.Info("file-download: file selection changed, discarding checkpoint",
						"peer", short, "file", displayName)
					restored.cleanup()
					ckpt.cleanupTempFiles(destDir)
					removeStreamCheckpoint(destDir, ck)
				} else {
					resumeState = restored
					resumeBitfield = ckpt.have
					slog.Info("file-download: resuming from checkpoint",
						"peer", short, "file", displayName,
						"have", ckpt.have.count(), "total_est", len(ckpt.hashes))
				}
			} else {
				slog.Debug("file-download: checkpoint restore failed, starting fresh",
					"error", restoreErr)
				ckpt.cleanupTempFiles(destDir)
				removeStreamCheckpoint(destDir, ck)
			}
		} else {
			slog.Debug("file-download: checkpoint flags/size mismatch, starting fresh")
			ckpt.cleanupTempFiles(destDir)
			removeStreamCheckpoint(destDir, ck)
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
		// Fresh transfer: build accept bitfield (#18 selective download).
		// Uses dlNewAccepted (resolved and validated once above, before checkpoint check).

		// F8: gate selective rejection when erasure is active.
		if dlNewAccepted != nil && erasureHdr2 != nil {
			ts.unregisterParallelSession(transferID)
			return nil, fmt.Errorf("selective file rejection is not available: sender uses erasure coding (WAN/relay). Download all files or skip the download")
		}

		acceptBF := newBitfield(len(files))
		if dlNewAccepted != nil {
			for _, idx := range dlNewAccepted {
				if idx >= 0 && idx < len(files) {
					acceptBF.set(idx)
				}
			}
			if isAllRejected(acceptBF) {
				ts.unregisterParallelSession(transferID)
				return nil, fmt.Errorf("all files excluded; nothing to download")
			}
		} else {
			for i := range files {
				acceptBF.set(i)
			}
		}
		if err := writeAcceptBitfield(rw, len(files), acceptBF); err != nil {
			ts.unregisterParallelSession(transferID)
			return nil, fmt.Errorf("write accept: %w", err)
		}
	}

	// Compute effective size for progress (#18 F12, F13, R8-F3).
	// Uses dlNewAccepted (resolved ONCE above for checkpoint comparison).
	// The goroutine uses dlNewAccepted for:
	// 1. state.acceptBitfield (F13 - allocateTempFiles skips rejected files)
	// 2. completion logging (R8-F3 - AcceptedFiles/TotalFiles in transfer history)
	// 3. effective size (F12 - progress reaches 100%)
	effectiveSize := totalSize
	if dlNewAccepted != nil {
		acceptSet := make(map[int]bool, len(dlNewAccepted))
		for _, idx := range dlNewAccepted {
			acceptSet[idx] = true
		}
		var acceptedSize int64
		for i, f := range files {
			if acceptSet[i] {
				acceptedSize += f.Size
			}
		}
		if acceptedSize > 0 {
			effectiveSize = acceptedSize
		}
	}
	estimatedChunks := estimateChunkCount(effectiveSize)
	progress := ts.trackTransfer(displayName, effectiveSize,
		peerKey, "download", estimatedChunks, flags&flagCompressed != 0)
	progress.setStatus("active")
	// TS-4: store transferID + remotePeer for cancel protocol routing (R4-C2).
	progress.mu.Lock()
	progress.transferID = transferID
	progress.remotePeerID = remotePeer
	progress.mu.Unlock()

	if tracker := ts.makeChunkTracker(s, "recv"); tracker != nil {
		progress.setRelayTracker(tracker)
	}

	ts.logEvent(EventLogStarted, "download", peerKey, displayName, totalSize, 0, "", "")

	// Receive via streaming protocol in background with TS-5b automatic failover.
	go func() {
		// R2-F5: Track active stream via closure variable so defer closes the CURRENT stream.
		activeStream := network.Stream(s)
		defer func() { activeStream.Close() }()

		// R3-F1: Track current transferID via closure for correct session deregistration.
		currentTransferID := transferID
		defer func() { ts.unregisterParallelSession(currentTransferID) }()

		// F32 fix: Protect/Unprotect inside goroutine so managed relay
		// connections stay protected for the entire transfer lifecycle.
		// Context-based liveness prevents reaper from killing idle backup circuits
		// during long transfers (TS-5b reaper bug fix).
		dlCtx, dlCtxCancel := context.WithCancel(context.Background())
		defer dlCtxCancel()
		if ts.pathProtector != nil {
			ts.pathProtector.ProtectWithContext(dlCtx, remotePeer, dlProtectTag)
			defer ts.pathProtector.Unprotect(remotePeer, dlProtectTag)
		}

		recvStart := time.Now()

		var state *streamReceiveState
		if resumeState != nil {
			state = resumeState
		} else {
			state = newStreamReceiveState(files, totalSize, flags, cumOffsets)

			// F13: wire accept bitfield into state for selective download.
			// Must be set BEFORE allocateTempFiles (which checks it to skip rejected files).
			// HandleInbound does this at the parallel code path; this is the ReceiveFrom mirror.
			// Uses dlNewAccepted resolved once before the goroutine (no re-resolution).
			if dlNewAccepted != nil {
				bf := newBitfield(len(files))
				for _, idx := range dlNewAccepted {
					bf.set(idx)
				}
				state.acceptBitfield = bf
			}

			if allocErr := state.allocateTempFiles(destDir); allocErr != nil {
				progress.finish(allocErr)
				ts.markCompleted(progress.ID)
				return
			}

			state.initReceivedBitfield(estimatedChunks)
		}

		// Initialize per-stripe parity tracking (Option C).
		if erasureHdr2 != nil {
			state.initPerStripeState(erasureHdr2)
		}

		// Wire state and progress into session now that they're ready.
		session.state = state
		session.progress = progress

		progress.setCancelFunc(func() {
			activeStream.Reset()
			session.resetWorkerStreams()
		})

		// TS-5b: Retry loop for automatic failover.
		var recvErr error
		var rootHash [32]byte
		totalFailovers := 0
		consecutiveFailures := 0
		currentSession := session
		currentRW := io.ReadWriter(rw)

		for {
			// R2-F7: Record bytes before this attempt to detect progress.
			bytesBeforeAttempt := state.ReceivedBytes()

			rootHash, recvErr = ts.receiveParallel(currentRW, currentSession)

			// Success or non-retryable error — exit loop.
			if recvErr == nil {
				break
			}

			// Classify the error (F7, R5-F5).
			category := classifyTransferError(recvErr, ts.queueCtx)
			if category != retryableNetwork {
				break
			}

			// R2-F7: Progress-based retry reset. If any new chunks were received
			// on this attempt, reset consecutive failures (survives unlimited
			// blips as long as progress is made between each).
			if state.ReceivedBytes() > bytesBeforeAttempt {
				consecutiveFailures = 0
			}

			// Check retry limits (F9, R2-F7).
			consecutiveFailures++
			totalFailovers++
			if consecutiveFailures > maxDownloadFailovers || totalFailovers > maxTotalFailovers {
				break
			}

			// R2-F20: Check if user cancelled during or before retry.
			if progress.Snapshot().Done {
				break
			}

			// F14: Check for backup path. If no alternative connections exist yet
			// (e.g. network switch killed all connections), wait for the daemon to
			// re-establish a connection via relay on the new network.
			if ts.networkRef == nil || ts.downloadServiceName == "" {
				break
			}
			groups := sdk.AllConnGroups(ts.networkRef.Host(), remotePeer, ts.pathProtector)
			if len(groups) <= 1 {
				// No backup path yet. Wait for reconnection before giving up.
				// Network switch (WiFi→cellular) kills all connections including
				// managed relay circuits. The daemon will reconnect to relays on
				// the new network, then establish a relay circuit to the peer.
				// Poll with increasing intervals up to 30s total.
				reconnectDeadline := time.After(60 * time.Second)
				reconnectTick := time.NewTicker(2 * time.Second)
				reconnected := false
				slog.Info("file-download: waiting for reconnection to peer",
					"peer", short, "file", displayName)
				progress.setStatus("reconnecting")

			reconnectLoop:
				for {
					select {
					case <-ts.queueCtx.Done():
						reconnectTick.Stop()
						recvErr = fmt.Errorf("cancelled during reconnection wait")
						goto done
					case <-reconnectDeadline:
						break reconnectLoop
					case <-reconnectTick.C:
						if progress.Snapshot().Done {
							break reconnectLoop // user cancelled
						}
						// Clear stale state blocking reconnection (same as ConnectToPeer).
						for _, c := range ts.networkRef.Host().Network().ConnsToPeer(remotePeer) {
							if c.Stat().Limited {
								c.Close()
							}
						}
						ts.networkRef.ClearDialBackoffs([]peer.ID{remotePeer})
						ts.networkRef.ResetBlackHoles()
						connectCtx, connectCancel := context.WithTimeout(ts.queueCtx, 10*time.Second)
						ts.networkRef.Host().Connect(connectCtx, peer.AddrInfo{ID: remotePeer})
						connectCancel()
						groups = sdk.AllConnGroups(ts.networkRef.Host(), remotePeer, ts.pathProtector)
						if len(groups) >= 1 {
							reconnected = true
							break reconnectLoop
						}
					}
				}
				reconnectTick.Stop()

				if !reconnected {
					slog.Info("file-download: no backup path, will retry",
						"peer", short, "file", displayName,
						"attempt", totalFailovers, "max", maxTotalFailovers)
					recvErr = fmt.Errorf("no backup path for failover")
					continue
				}
				slog.Info("file-download: peer reconnected, proceeding with failover",
					"peer", short, "file", displayName,
					"groups", len(groups))
			}

			// F21: Detect if failover is from direct to relay and log appropriately.
			failoverPathType := "path"
			for _, g := range groups {
				if g.Type != "direct" {
					failoverPathType = "direct-to-relay"
					break
				}
			}
			slog.Info("file-download: "+failoverPathType+" failover",
				"peer", short, "file", displayName,
				"attempt", totalFailovers, "consecutive", consecutiveFailures,
				"error", recvErr)

			// F10/F39: Fire failover event (R3-F7: no EventLogFailed during retry).
			ts.logEvent(EventLogPathFailover, "download", peerKey, displayName, totalSize,
				progress.Sent(), recvErr.Error(),
				fmt.Sprintf("attempt=%d", totalFailovers))

			// F10: Update progress for failover (R3-F4: no EventLogFailed yet).
			progress.mu.Lock()
			progress.Failovers++
			progress.mu.Unlock()
			progress.setStatus("path-failover")

			// F1: Cancel old sender before retry. Best-effort, fire-and-forget (R2-F6).
			go sendMultiPathCancel(ts.hostRef, remotePeer, currentTransferID, ts.pathProtector)

			// R3-F8: Clean up state between retries (close handles, keep files).
			state.cleanup()

			// F3: Deregister old session before opening new stream.
			ts.unregisterParallelSession(currentTransferID)

			// F9: Backoff before retry.
			delay := failoverBackoff(consecutiveFailures)
			select {
			case <-ts.queueCtx.Done():
				// R2-F17: Daemon shutdown during backoff.
				recvErr = fmt.Errorf("cancelled during failover backoff")
				goto done
			case <-time.After(delay):
			}

			// R2-F20: Re-check user cancel after backoff.
			if progress.Snapshot().Done {
				recvErr = fmt.Errorf("cancelled during failover")
				goto done
			}

			// Open new stream on backup path via HedgedOpenStream (R2-F2).
			newStream, hedgeErr := sdk.HedgedOpenStream(
				ts.queueCtx, ts.networkRef, remotePeer, ts.downloadServiceName)
			if hedgeErr != nil {
				// All existing connection groups failed (dead connections from
				// network switch). Wait for daemon to reconnect on new network,
				// then retry HedgedOpenStream.
				slog.Info("file-download: all paths dead, waiting for reconnection",
					"peer", short, "file", displayName, "error", hedgeErr)
				progress.setStatus("reconnecting")

				reconnectDeadline := time.After(60 * time.Second)
				reconnectTick := time.NewTicker(2 * time.Second)
				reconnected := false

			hedgeReconnectLoop:
				for {
					select {
					case <-ts.queueCtx.Done():
						reconnectTick.Stop()
						recvErr = fmt.Errorf("cancelled during reconnection wait")
						goto done
					case <-reconnectDeadline:
						break hedgeReconnectLoop
					case <-reconnectTick.C:
						if progress.Snapshot().Done {
							break hedgeReconnectLoop
						}
						// Clear stale state that blocks reconnection after network switch:
						// - Close dead relay connections (fool libp2p into thinking peer is connected)
						// - Clear dial backoffs (libp2p won't retry recently-failed addresses)
						// - Reset black hole detector (blocks all relay dials after consecutive failures)
						// - Re-add relay circuit addresses to peerstore (may only have direct addrs)
						// Same logic as ConnectToPeer (serve_common.go:1457-1478).
						for _, c := range ts.networkRef.Host().Network().ConnsToPeer(remotePeer) {
							if c.Stat().Limited {
								c.Close()
							}
						}
						ts.networkRef.ClearDialBackoffs([]peer.ID{remotePeer})
						ts.networkRef.ResetBlackHoles()
						connectCtx, connectCancel := context.WithTimeout(ts.queueCtx, 10*time.Second)
						ts.networkRef.Host().Connect(connectCtx, peer.AddrInfo{ID: remotePeer})
						connectCancel()
						newStream, hedgeErr = sdk.HedgedOpenStream(
							ts.queueCtx, ts.networkRef, remotePeer, ts.downloadServiceName)
						if hedgeErr == nil {
							reconnected = true
							break hedgeReconnectLoop
						}
					}
				}
				reconnectTick.Stop()

				if !reconnected {
					slog.Info("file-download: reconnection timed out, will retry",
						"peer", short, "file", displayName,
						"attempt", totalFailovers, "max", maxTotalFailovers)
					recvErr = fmt.Errorf("failover stream: %w", hedgeErr)
					// Don't break — continue the retry loop. The next iteration
					// will check retry limits and try another 60s reconnect window.
					// Total window: maxDownloadFailovers × 60s = 3 minutes.
					continue
				}
				slog.Info("file-download: reconnected, resuming transfer",
					"peer", short, "file", displayName)
			}

			// F4: Check relay budget on failover path. Awareness only (per project rules:
			// enforcement is server-side). Still attempt failover — partial progress +
			// checkpoint is better than nothing.
			if ts.grantChecker != nil {
				relayID := relayPeerFromStream(newStream)
				if relayID != "" {
					remaining := totalSize - state.ReceivedBytes()
					grantInfo := ts.checkRelayGrant(newStream, remaining, "recv")
					if grantInfo.GrantActive && !grantInfo.BudgetOK {
						slog.Warn("file-download: failover to relay with insufficient budget",
							"peer", short, "relay_budget", sdk.FormatBytes(grantInfo.SessionBudget),
							"remaining", sdk.FormatBytes(remaining),
							"hint", "extend budget with: shurli auth grant")
					}
				}
			}

			// Negotiate download on new stream with in-memory state (R3-F2, R6-F1).
			dlCtx, negErr := ts.downloadNegotiate(newStream, remotePath, destDir, state)
			if negErr != nil {
				newStream.Close()
				// If negotiation failed due to stream reset (stale managed circuit),
				// mark the managed circuit as dead and retry — the next iteration
				// will enter the reconnection wait loop and get a fresh connection.
				negCategory := classifyTransferError(negErr, ts.queueCtx)
				if negCategory == retryableNetwork {
					slog.Info("file-download: negotiate failed on stale path, retrying",
						"peer", short, "error", negErr)
					recvErr = fmt.Errorf("failover negotiate: %w", negErr)
					continue
				}
				recvErr = fmt.Errorf("failover negotiate: %w", negErr)
				break
			}

			// Update tracking for new session.
			activeStream = newStream
			currentTransferID = dlCtx.transferID
			currentSession = dlCtx.session
			state = dlCtx.state
			currentRW = dlCtx.rw

			// Set up state if downloadNegotiate returned nil (fresh start after content change).
			if state == nil {
				state = newStreamReceiveState(dlCtx.files, dlCtx.totalSize, dlCtx.flags, dlCtx.cumOffsets)
				if allocErr := state.allocateTempFiles(destDir); allocErr != nil {
					recvErr = allocErr
					break
				}
				state.initReceivedBitfield(dlCtx.estimatedChunks)
				if dlCtx.erasureHdr != nil {
					state.initPerStripeState(dlCtx.erasureHdr)
				}
			} else if state.destRoot == nil {
				// TS-5b failover: cleanup() closed destRoot and tmpFiles.
				// Re-open existing temp files so writeChunkGlobal and finalize work.
				if reopenErr := state.reopenTempFiles(destDir); reopenErr != nil {
					recvErr = fmt.Errorf("reopen temp files after failover: %w", reopenErr)
					break
				}
				// Re-initialize per-stripe state for the new stream's erasure
				// config. cleanup() doesn't clear stripe fields, but the new
				// header may carry different params. initPerStripeState creates
				// fresh maps, so stale entries from the prior session are
				// discarded. [Deep audit fix: Bug #1]
				if dlCtx.erasureHdr != nil {
					state.initPerStripeState(dlCtx.erasureHdr)
				}
			}

			// Wire new state into session.
			currentSession.state = state
			currentSession.progress = progress

			// R3-F3: Recreate relay tracker for new stream.
			if tracker := ts.makeChunkTracker(newStream, "recv"); tracker != nil {
				progress.setRelayTracker(tracker)
			}

			// Update progress with new transferID (F35).
			progress.mu.Lock()
			progress.transferID = dlCtx.transferID
			progress.mu.Unlock()

			// Update cancel func for new stream.
			progress.setCancelFunc(func() {
				activeStream.Reset()
				currentSession.resetWorkerStreams()
			})

			progress.setStatus("active")

			// F9: consecutive failures already reset by R2-F7 progress check at loop top
			// if chunks were received on the previous attempt.
		}

	done:
		// Final cleanup of state (closes handles; keepTempFiles preserves data if checkpoint saved).
		state.cleanup()

		// Register hash for multi-peer serving on success.
		if recvErr == nil {
			if len(files) == 1 {
				ts.RegisterHash(rootHash, filepath.Join(destDir, files[0].Path))
			} else if prefix := extractCommonPrefix(files); prefix != "" {
				ts.RegisterHash(rootHash, filepath.Join(destDir, prefix))
			}
		}

		// R3-F4: finish/completed/log events OUTSIDE retry loop.
		progress.finish(recvErr)
		ts.markCompleted(progress.ID)

		dur := time.Since(recvStart).Truncate(time.Millisecond).String()
		if recvErr != nil {
			// F40: Actionable error message on final failure.
			received := progress.Sent()
			failoverDetail := ""
			if totalFailovers > 0 {
				failoverDetail = fmt.Sprintf(", %d failover attempts", totalFailovers)
			}
			resumeHint := ""
			if received > 0 {
				resumeHint = fmt.Sprintf(". Resume with: shurli download %s:%s", short, remotePath)
			}
			slog.Error(fmt.Sprintf("file-download: failed (received %s/%s%s)%s",
				sdk.FormatBytes(received), sdk.FormatBytes(totalSize),
				failoverDetail, resumeHint),
				"peer", short, "file", displayName, "error", recvErr)
			ts.logEvent(EventLogFailed, "download", peerKey, displayName, totalSize, received, recvErr.Error(), dur)
		} else {
			// R8-F3: log selective info in download completion.
			if dlNewAccepted != nil {
				slog.Info("file-download: received (selective)",
					"peer", short, "file", displayName,
					"accepted", len(dlNewAccepted), "total_files", len(files),
					"size", effectiveSize, "dest", destDir)
				ts.logEventSelective(EventLogCompleted, "download", peerKey, displayName, effectiveSize, effectiveSize, "", dur, len(dlNewAccepted), len(files))
			} else {
				slog.Info("file-download: received",
					"peer", short, "file", displayName,
					"size", totalSize, "dest", destDir)
				ts.logEvent(EventLogCompleted, "download", peerKey, displayName, totalSize, totalSize, "", dur)
			}
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
