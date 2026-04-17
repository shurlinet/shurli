package filetransfer

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/zeebo/blake3"
	"golang.org/x/sync/singleflight"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// Multi-peer download coordination using work-stealing raw chunk transfer.
//
// Architecture: the receiver coordinates N peers, each serving a subset of blocks
// (chunks). Fast peers naturally get more blocks via a shared work queue. No
// encoding overhead — raw chunk bytes over reliable QUIC streams.
//
// Wire protocol: receiver sends msgMultiPeerRequest with root hash, each peer
// responds with manifest then serves blocks on demand via msgBlockRequest/msgBlockData.

// MultiPeerProtocol is the protocol ID for multi-peer block-level downloads.
// Stays at 1.0.0 (D2: never deployed to production, all nodes deploy simultaneously).
const MultiPeerProtocol = "/shurli/file-multi-peer/1.0.0"

// Multi-peer wire constants.
const (
	// msgMultiPeerRequest is sent by receiver to initiate a multi-peer session.
	// Wire: type(1) + rootHash(32) + flags(4) = 37 bytes.
	msgMultiPeerRequest = 0x0A

	// msgMultiPeerManifest is sent by sender in response to a request.
	// Wire: type(1) + manifestLen(4) + manifestBytes.
	msgMultiPeerManifest = 0x0C

	// msgBlockRequest is sent by receiver to request a specific block.
	// Wire: type(1) + blockIndex(4) = 5 bytes.
	msgBlockRequest = 0x0D

	// msgBlockData is sent by sender with block content.
	// Wire: type(1) + blockIndex(4) + flags(1) + decompSize(4) + dataLen(4) + data.
	msgBlockData = 0x0E

	// msgMultiPeerDone is sent by receiver when all blocks are complete.
	// Wire: type(1) = 1 byte.
	msgMultiPeerDone = 0x0F

	// msgBlockError is sent by sender when a block cannot be served.
	// Wire: type(1) + blockIndex(4) + errLen(2) + errMsg.
	msgBlockError = 0x10

	// msgMultiPeerReject is sent by sender to reject a session.
	// Wire: type(1) + reason(1) = 2 bytes.
	msgMultiPeerReject = 0x11
)

// Block data flags byte.
const (
	blockFlagCompressed = 0x01
)

// Request flags (IF11-3: future-proof protocol).
const (
	flagMPCompressionSupported = 1 << 0
	flagMPResumeSupported      = 1 << 1
)

// Reject reasons (IF5-1).
const (
	rejectAtCapacity        byte = 0x01
	rejectBandwidthExceeded byte = 0x02
	rejectFileNotFound      byte = 0x03
)

// Multi-peer operational constants.
const (
	multiPeerPipelineDepth   = 4                 // D3: fixed pipeline depth
	multiPeerSlowRatio       = 10                // D4: 10x slower than fastest = slow
	multiPeerMaxReconnect    = 3                 // IF11-1: max reconnections per peer
	multiPeerMaxConsecErrors = 5                 // IF8-1: consecutive block errors → unreliable
	multiPeerDisconnectAfter = 10                // IF8-1: consecutive errors → disconnect
	multiPeerManifestTimeout = 3 * time.Minute   // IF5-6: manifest exchange timeout
	multiPeerBlockTimeout    = 2 * time.Minute   // IF4-1: per-block deadline
	multiPeerMinBlocksSpeed  = 3                 // IF-I24: min blocks before speed measurement
	maxBoundaryCacheEntries  = 32                // D6: LRU-32 boundary cache
	maxScanSharedFiles       = 50                // IF13-3: bounded share scan
	maxPerPeerServing        = 3                 // IF13-1: per-peer serving limit
)

// --- Types ---

// chunkBoundary stores metadata from FastCDC boundary scan (no data retained).
// Memory: ~40 bytes per chunk. 2000 chunks = 80KB (C2: 1500x reduction from old design).
type chunkBoundary struct {
	offset int64
	size   uint32
	hash   [32]byte
}

// boundaryCache caches FastCDC scan results per root hash (I3, IF4-3).
// Thread-safe. LRU eviction at maxBoundaryCacheEntries.
type boundaryCache struct {
	mu       sync.RWMutex
	cache    map[[32]byte][]chunkBoundary
	negCache map[[32]byte]time.Time // IF10-5: negative cache with 30s TTL
	order    [][32]byte             // LRU order (most recent at end)
	sf       singleflight.Group
}

func newBoundaryCache() *boundaryCache {
	return &boundaryCache{
		cache:    make(map[[32]byte][]chunkBoundary),
		negCache: make(map[[32]byte]time.Time),
	}
}

// getOrScan returns cached boundaries or scans the file. Uses singleflight to
// prevent duplicate scans for concurrent requests (IF4-3).
func (c *boundaryCache) getOrScan(rootHash [32]byte, scanner func() ([]chunkBoundary, error)) ([]chunkBoundary, error) {
	// Check positive cache.
	c.mu.RLock()
	if entry, ok := c.cache[rootHash]; ok {
		c.mu.RUnlock()
		c.touch(rootHash)
		return entry, nil
	}
	// Check negative cache (IF10-5).
	if expiry, ok := c.negCache[rootHash]; ok && time.Now().Before(expiry) {
		c.mu.RUnlock()
		return nil, fmt.Errorf("cached scan failure for this file")
	}
	c.mu.RUnlock()

	// Singleflight: concurrent requests for same hash share one scan.
	key := hex.EncodeToString(rootHash[:])
	result, err, _ := c.sf.Do(key, func() (any, error) {
		return scanner()
	})
	if err != nil {
		c.mu.Lock()
		c.negCache[rootHash] = time.Now().Add(30 * time.Second)
		c.mu.Unlock()
		return nil, err
	}

	boundaries := result.([]chunkBoundary)
	c.put(rootHash, boundaries)
	return boundaries, nil
}

func (c *boundaryCache) put(rootHash [32]byte, boundaries []chunkBoundary) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.cache[rootHash]; exists {
		// Inline LRU touch (don't call touch() — it takes the same lock → deadlock).
		for i, h := range c.order {
			if h == rootHash {
				c.order = append(c.order[:i], c.order[i+1:]...)
				c.order = append(c.order, rootHash)
				break
			}
		}
		return
	}
	c.cache[rootHash] = boundaries
	c.order = append(c.order, rootHash)
	for len(c.cache) > maxBoundaryCacheEntries {
		evict := c.order[0]
		c.order = c.order[1:]
		delete(c.cache, evict)
	}
	// Clean expired negative cache entries to prevent unbounded growth.
	now := time.Now()
	for k, expiry := range c.negCache {
		if now.After(expiry) {
			delete(c.negCache, k)
		}
	}
}

// touch promotes a cache entry to most-recently-used. Takes its own lock.
// MUST NOT be called from put() — use inline touch instead (same lock → deadlock).
func (c *boundaryCache) touch(rootHash [32]byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i, h := range c.order {
		if h == rootHash {
			c.order = append(c.order[:i], c.order[i+1:]...)
			c.order = append(c.order, rootHash)
			break
		}
	}
}

// blockQueue coordinates work-stealing between peer goroutines (IF-2).
// Peers claim blocks from primary queue first, then retry queue (failed blocks).
// Thread-safe via channel semantics + mutex for bitfield.
type blockQueue struct {
	blocks    chan int      // buffered(chunkCount), pre-filled with non-resumed block indices
	retry     chan int      // buffered(chunkCount), for failed/re-queued blocks
	done      chan struct{} // closed when all blocks complete
	completed atomic.Int32
	total     int
	closeOnce sync.Once

	// IF10-1: bitfield is NOT thread-safe; mutex protects concurrent set() calls.
	haveMu sync.Mutex
	have   *bitfield

	// IF13-2: monotonic transferred bytes counter (atomic Add, never Set).
	transferredBytes atomic.Int64
}

func newBlockQueue(total int, have *bitfield) *blockQueue {
	q := &blockQueue{
		blocks: make(chan int, total),
		retry:  make(chan int, total),
		done:   make(chan struct{}),
		total:  total,
		have:   have,
	}
	// Pre-fill primary queue with blocks not yet completed (for resume).
	alreadyDone := 0
	for i := 0; i < total; i++ {
		if have.has(i) {
			alreadyDone++
		} else {
			q.blocks <- i
		}
	}
	q.completed.Store(int32(alreadyDone))
	if alreadyDone >= total {
		q.closeOnce.Do(func() { close(q.done) })
	}
	return q
}

// claim returns the next block to serve. Priority: retry > primary.
// Returns (index, true) or (-1, false) if done/cancelled.
// Slow peers only check retry queue (IF-I23).
func (q *blockQueue) claim(ctx context.Context, slowPeer bool) (int, bool) {
	// Non-blocking check retry first (priority).
	select {
	case idx := <-q.retry:
		return idx, true
	default:
	}
	if slowPeer {
		// Slow peers only serve retries — don't take primary work from fast peers.
		select {
		case idx := <-q.retry:
			return idx, true
		case <-q.done:
			return -1, false
		case <-ctx.Done():
			return -1, false
		}
	}
	// Blocking: retry or primary or done or cancel.
	select {
	case idx := <-q.retry:
		return idx, true
	case idx := <-q.blocks:
		return idx, true
	case <-q.done:
		return -1, false
	case <-ctx.Done():
		return -1, false
	}
}

// tryClaim is a non-blocking version of claim. Returns (-1, false) immediately
// if no block is available. Used when the pipeline still has in-flight requests
// to prevent deadlock (IF-DEADLOCK-1).
func (q *blockQueue) tryClaim(slowPeer bool) (int, bool) {
	select {
	case idx := <-q.retry:
		return idx, true
	default:
	}
	if slowPeer {
		return -1, false // slow peers only get retries
	}
	select {
	case idx := <-q.blocks:
		return idx, true
	default:
		return -1, false
	}
}

// markComplete records a block as done and checks for session completion (IF-3, IF10-1).
func (q *blockQueue) markComplete(blockIndex int, blockSize int64) {
	q.haveMu.Lock()
	q.have.set(blockIndex)
	q.haveMu.Unlock()
	q.transferredBytes.Add(blockSize)
	if q.completed.Add(1) == int32(q.total) {
		q.closeOnce.Do(func() { close(q.done) })
	}
}

// requeue puts a block back for another peer to serve (IF3-9: non-blocking).
func (q *blockQueue) requeue(blockIndex int) {
	select {
	case q.retry <- blockIndex:
	default:
		slog.Error("multi-peer: retry channel full, block dropped", "block", blockIndex)
	}
}

// checkpointSnapshot returns a safe copy of the bitfield for checkpoint saving (IF10-4).
func (q *blockQueue) checkpointSnapshot() *bitfield {
	q.haveMu.Lock()
	snap := &bitfield{bits: make([]byte, len(q.have.bits)), n: q.have.n}
	copy(snap.bits, q.have.bits)
	q.haveMu.Unlock()
	return snap
}

// peerState tracks per-peer health for the 3-strike system (D9).
type peerState struct {
	peerID       peer.ID
	peerIdx      int
	strikes      int
	onParole     bool
	banned       bool
	slow         bool
	consecErrors int // IF8-1: consecutive block errors
	transport    sdk.TransportType
	reconnects   int
	blocksOK     atomic.Int32
	blocksFail   atomic.Int32
	bytesRecv    atomic.Int64
	startTime    time.Time
}

// speed returns blocks per second (IF-I23). Returns 0 if too few blocks measured.
func (ps *peerState) speed() float64 {
	n := ps.blocksOK.Load()
	if n < multiPeerMinBlocksSpeed {
		return 0 // IF-I24: cold-start protection
	}
	elapsed := time.Since(ps.startTime).Seconds()
	if elapsed <= 0 {
		return 0
	}
	return float64(n) / elapsed
}

// multiPeerSession coordinates a multi-peer download.
type multiPeerSession struct {
	manifest       *transferManifest
	queue          *blockQueue
	tmpFile        *os.File
	tmpPath        string
	offsets        []int64 // cumulative block offsets for WriteAt (I8)
	contentKey     [32]byte
	progress       *TransferProgress
	strikeThreshold int

	// D1/IF3-5: track streams for cancel reset.
	peerStreamsMu sync.Mutex
	peerStreams    []network.Stream

	// Speed tracking (IF-I23).
	fastestSpeed atomic.Uint64 // float64 bits via math.Float64bits
}

func (s *multiPeerSession) addPeerStream(stream network.Stream) {
	s.peerStreamsMu.Lock()
	s.peerStreams = append(s.peerStreams, stream)
	s.peerStreamsMu.Unlock()
}

// resetAllStreams forcefully closes all peer streams (IF3-5, IF6-5).
// Unblocks goroutines stuck in io.ReadFull.
func (s *multiPeerSession) resetAllStreams() {
	s.peerStreamsMu.Lock()
	streams := s.peerStreams
	s.peerStreams = nil
	s.peerStreamsMu.Unlock()
	for _, stream := range streams {
		stream.Reset()
	}
}

// updateFastestSpeed CAS-updates the global fastest speed (IF-I23).
func (s *multiPeerSession) updateFastestSpeed(speed float64) {
	bits := math.Float64bits(speed)
	for {
		old := s.fastestSpeed.Load()
		if math.Float64frombits(old) >= speed {
			return
		}
		if s.fastestSpeed.CompareAndSwap(old, bits) {
			return
		}
	}
}

// --- Block buffer pool (IF4-2: GC pressure at scale) ---

var blockBufPool = sync.Pool{
	New: func() any { return make([]byte, maxDecompressedChunk) },
}

// --- Wire format helpers ---

func writeBlockData(s network.Stream, blockIndex int, flags byte, decompSize uint32, data []byte) error {
	var header [14]byte
	header[0] = msgBlockData
	binary.BigEndian.PutUint32(header[1:5], uint32(blockIndex))
	header[5] = flags
	binary.BigEndian.PutUint32(header[6:10], decompSize)
	binary.BigEndian.PutUint32(header[10:14], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return err
	}
	if _, err := s.Write(data); err != nil {
		return err
	}
	return nil
}

func writeBlockError(s network.Stream, blockIndex int, msg string) error {
	data := []byte(msg)
	if len(data) > 65535 {
		data = data[:65535]
	}
	var header [7]byte
	header[0] = msgBlockError
	binary.BigEndian.PutUint32(header[1:5], uint32(blockIndex))
	binary.BigEndian.PutUint16(header[5:7], uint16(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return err
	}
	_, err := s.Write(data)
	return err
}

func writeMultiPeerReject(s network.Stream, reason byte) error {
	_, err := s.Write([]byte{msgMultiPeerReject, reason})
	return err
}

func sendBlockRequest(s network.Stream, blockIndex int) error {
	var buf [5]byte
	buf[0] = msgBlockRequest
	binary.BigEndian.PutUint32(buf[1:5], uint32(blockIndex))
	_, err := s.Write(buf[:])
	return err
}

// multiPeerError provides per-peer rejection details for diagnostic logging (IF16-4).
type multiPeerError struct {
	peerErrors map[string]string // short peer ID → reason
}

func (e *multiPeerError) Error() string {
	if len(e.peerErrors) == 0 {
		return "multi-peer download failed: no peers available"
	}
	parts := make([]string, 0, len(e.peerErrors))
	for pid, reason := range e.peerErrors {
		parts = append(parts, pid+": "+reason)
	}
	return fmt.Sprintf("multi-peer failed (%s)", joinStrings(parts, ", "))
}

func joinStrings(parts []string, sep string) string {
	if len(parts) == 0 {
		return ""
	}
	result := parts[0]
	for _, p := range parts[1:] {
		result += sep + p
	}
	return result
}

func multiPeerRejectString(reason byte) string {
	switch reason {
	case rejectAtCapacity:
		return "peer at capacity"
	case rejectBandwidthExceeded:
		return "bandwidth budget exceeded"
	case rejectFileNotFound:
		return "file not found"
	default:
		return fmt.Sprintf("declined (reason 0x%02x)", reason)
	}
}

// --- Sender side (HandleMultiPeerRequest) ---

// HandleMultiPeerRequest returns a StreamHandler that serves multi-peer
// block-level download requests. Reads the file lazily via boundary scan +
// on-demand ReadAt. Memory: O(boundary metadata) not O(file size) (C2).
func (ts *TransferService) HandleMultiPeerRequest() sdk.StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()

		remotePeer := s.Conn().RemotePeer()
		peerKey := remotePeer.String()
		short := peerKey
		if len(short) > 16 {
			short = short[:16] + "..."
		}

		// IF3-1: Acquire inbound semaphore (prevents unbounded multi-peer sessions).
		select {
		case ts.inboundSem <- struct{}{}:
			defer func() { <-ts.inboundSem }()
		default:
			slog.Warn("multi-peer: at capacity, rejecting", "peer", short)
			writeMultiPeerReject(s, rejectAtCapacity)
			ts.logEvent(EventLogMultiPeerRejected, "multi-peer-serve", peerKey, "", 0, 0, "at capacity", "")
			return
		}

		// IF3-6/IF13-1: Per-peer serving limit (separate from peerInbound).
		ts.mu.Lock()
		if ts.peerServing == nil {
			ts.peerServing = make(map[string]int)
		}
		if ts.peerServing[peerKey] >= maxPerPeerServing {
			ts.mu.Unlock()
			slog.Warn("multi-peer: per-peer serving limit", "peer", short)
			writeMultiPeerReject(s, rejectAtCapacity)
			return
		}
		ts.peerServing[peerKey]++
		ts.mu.Unlock()
		defer func() {
			ts.mu.Lock()
			ts.peerServing[peerKey]--
			if ts.peerServing[peerKey] <= 0 {
				delete(ts.peerServing, peerKey)
			}
			ts.mu.Unlock()
		}()

		// Rate limit.
		if ts.rateLimiter != nil && !ts.rateLimiter.allow(peerKey) {
			slog.Warn("multi-peer: rate limit exceeded", "peer", short)
			writeMultiPeerReject(s, rejectAtCapacity)
			return
		}

		// IF12-1: Global outbound bandwidth limit.
		if ts.maxTotalServedPerHour > 0 {
			currentHour := time.Now().Unix() / 3600
			if currentHour != ts.servedBytesHour.Load() {
				ts.totalServedBytes.Store(0)
				ts.servedBytesHour.Store(currentHour)
			}
			if ts.totalServedBytes.Load() >= ts.maxTotalServedPerHour {
				writeMultiPeerReject(s, rejectBandwidthExceeded)
				return
			}
		}

		s.SetDeadline(time.Now().Add(multiPeerManifestTimeout))

		// Read request: type(1) + rootHash(32) + flags(4) = 37 bytes.
		var header [37]byte
		if _, err := io.ReadFull(s, header[:]); err != nil {
			slog.Debug("multi-peer: read request failed", "peer", short, "error", err)
			return
		}
		if header[0] != msgMultiPeerRequest {
			slog.Debug("multi-peer: unexpected message type", "peer", short, "type", header[0])
			return
		}

		var rootHash [32]byte
		copy(rootHash[:], header[1:33])
		requestFlags := binary.BigEndian.Uint32(header[33:37])
		compressionOK := requestFlags&flagMPCompressionSupported != 0

		// Look up file by root hash (IF6-2/IF6-3: try registry then scan shares).
		localPath, ok := ts.lookupHashOrScan(rootHash)
		if !ok {
			slog.Debug("multi-peer: file not found", "peer", short)
			writeMultiPeerReject(s, rejectFileNotFound)
			ts.logEvent(EventLogMultiPeerRejected, "multi-peer-serve", peerKey, "", 0, 0, "file not found", "")
			return
		}

		// Open file and keep fd for entire session (C3: prevents TOCTOU).
		f, err := os.Open(localPath)
		if err != nil {
			slog.Warn("multi-peer: open file failed", "peer", short, "error", err)
			writeMultiPeerReject(s, rejectFileNotFound)
			return
		}
		defer f.Close()

		fi, err := f.Stat()
		if err != nil {
			slog.Warn("multi-peer: stat failed", "peer", short, "error", err)
			return
		}
		fileSize := fi.Size()

		// IF3-2: Bandwidth budget check.
		// Verified-LAN classification ensures routed-private peers (CGNAT,
		// Docker, VPN, multi-WAN cross-links) cannot silently skip the
		// budget by matching RFC 1918 — only mDNS-verified LAN does.
		if ts.bandwidthTracker != nil {
			peerBudget := int64(0)
			if ts.peerBudgetFunc != nil {
				peerBudget = ts.peerBudgetFunc(peerKey)
			}
			transport := sdk.VerifiedTransport(s, ts.hasVerifiedLANConn)
			isLAN := transport == sdk.TransportLAN
			if !isLAN && !ts.bandwidthTracker.check(peerKey, fileSize, peerBudget) {
				slog.Warn("multi-peer: bandwidth budget exceeded", "peer", short)
				writeMultiPeerReject(s, rejectBandwidthExceeded)
				ts.logEvent(EventLogMultiPeerRejected, "multi-peer-serve", peerKey, "", 0, 0, "bandwidth exceeded", "")
				return
			}
		}

		// Boundary scan with cache (I3, IF4-3).
		boundaries, scanErr := ts.boundaries.getOrScan(rootHash, func() ([]chunkBoundary, error) {
			return scanBoundaries(f, fileSize)
		})
		if scanErr != nil {
			slog.Warn("multi-peer: boundary scan failed", "peer", short, "error", scanErr)
			return
		}

		// Verify root hash matches (IF-I22: file changed since hash registration).
		hashes := make([][32]byte, len(boundaries))
		for i, b := range boundaries {
			hashes[i] = b.hash
		}
		if sdk.MerkleRoot(hashes) != rootHash {
			slog.Warn("multi-peer: root hash mismatch on re-scan", "peer", short)
			return
		}

		chunkCount := len(boundaries)
		sizes := make([]uint32, chunkCount)
		for i, b := range boundaries {
			sizes[i] = b.size
		}

		manifest := &transferManifest{
			Filename:    filepath.Base(localPath),
			FileSize:    fileSize,
			ChunkCount:  chunkCount,
			RootHash:    rootHash,
			ChunkHashes: hashes,
			ChunkSizes:  sizes,
		}

		// Send manifest.
		manifestBytes, err := marshalManifest(manifest)
		if err != nil {
			slog.Warn("multi-peer: marshal manifest failed", "peer", short, "error", err)
			return
		}
		var mHeader [5]byte
		mHeader[0] = msgMultiPeerManifest
		binary.BigEndian.PutUint32(mHeader[1:5], uint32(len(manifestBytes)))
		if _, err := s.Write(mHeader[:]); err != nil {
			return
		}
		if _, err := s.Write(manifestBytes); err != nil {
			return
		}

		slog.Info("multi-peer: serving blocks", "peer", short, "file", manifest.Filename,
			"blocks", chunkCount, "size", fileSize)

		// IF-S1: Served-block dedup bitfield.
		servedBlocks := newBitfield(chunkCount)
		var totalBytesSent int64

		// Compression probe: test first 3 blocks (D7, IF5-8).
		compressBlocks := compressionOK && ts.compress
		probeCount := 0
		probeFails := 0

		// Block serving loop: read requests, serve blocks.
		for {
			s.SetDeadline(time.Now().Add(multiPeerBlockTimeout)) // IF4-1: per-block deadline

			var reqBuf [5]byte
			if _, err := io.ReadFull(s, reqBuf[:1]); err != nil {
				if err == io.EOF {
					break // peer done
				}
				break
			}

			switch reqBuf[0] {
			case msgMultiPeerDone:
				// I14: receiver signals session complete.
				slog.Debug("multi-peer: receiver done", "peer", short)
				goto done

			case msgBlockRequest:
				// Read remaining 4 bytes of block index.
				if _, err := io.ReadFull(s, reqBuf[1:5]); err != nil {
					goto done // stream error — exit loop (not just switch)
				}
				blockIndex := int(binary.BigEndian.Uint32(reqBuf[1:5]))

				// Validate (IF14-5).
				if blockIndex < 0 || blockIndex >= chunkCount {
					writeBlockError(s, blockIndex, "block index out of range")
					goto done // protocol error
				}

				// IF-S1: Duplicate block rejection (S4).
				if servedBlocks.has(blockIndex) {
					writeBlockError(s, blockIndex, "duplicate block request")
					continue
				}

				// Read block from disk via ReadAt (I2: lazy chunking).
				boundary := boundaries[blockIndex]
				buf := blockBufPool.Get().([]byte)
				buf = buf[:boundary.size]
				n, readErr := f.ReadAt(buf, boundary.offset)
				if readErr != nil && readErr != io.EOF {
					blockBufPool.Put(buf[:cap(buf)])
					writeBlockError(s, blockIndex, "disk read error")
					continue
				}
				if uint32(n) != boundary.size {
					blockBufPool.Put(buf[:cap(buf)])
					writeBlockError(s, blockIndex, "short read")
					continue
				}

				// Compression (D7, IF-I9).
				var sendData []byte
				var flags byte
				decompSize := boundary.size
				if compressBlocks {
					compressed, wasCompressed := compressChunk(buf[:n])
					if wasCompressed {
						sendData = compressed
						flags = blockFlagCompressed
					} else {
						sendData = buf[:n]
						if probeCount < 3 {
							probeFails++
							if probeFails >= 3 {
								compressBlocks = false // incompressible file
							}
						}
					}
					probeCount++
				} else {
					sendData = buf[:n]
				}

				// Send block data.
				if err := writeBlockData(s, blockIndex, flags, decompSize, sendData); err != nil {
					blockBufPool.Put(buf[:cap(buf)])
					goto done // stream error — exit loop
				}

				blockBufPool.Put(buf[:cap(buf)])
				servedBlocks.set(blockIndex)
				totalBytesSent += int64(len(sendData))

			default:
				slog.Debug("multi-peer: unexpected message in block loop", "peer", short, "type", reqBuf[0])
				goto done
			}
		}

	done:
		// IF10-2: Record served bytes for bandwidth tracking.
		if totalBytesSent > 0 {
			if ts.bandwidthTracker != nil {
				ts.bandwidthTracker.record(peerKey, totalBytesSent)
			}
			ts.totalServedBytes.Add(totalBytesSent)
		}
		slog.Info("multi-peer: done serving", "peer", short, "bytes_sent", totalBytesSent)
	}
}

// scanBoundaries runs FastCDC boundary scan on an open file. Returns metadata
// only (no data retained). Memory: O(chunkCount * 40 bytes) (C2, IF-1).
func scanBoundaries(f *os.File, fileSize int64) ([]chunkBoundary, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var boundaries []chunkBoundary
	if err := ChunkReader(f, fileSize, func(c Chunk) error {
		boundaries = append(boundaries, chunkBoundary{
			offset: c.Offset,
			size:   uint32(len(c.Data)),
			hash:   c.Hash,
		})
		return nil
	}); err != nil {
		return nil, err
	}
	return boundaries, nil
}

// lookupHashOrScan tries the hash registry first, then scans shares (IF6-2/IF6-3).
func (ts *TransferService) lookupHashOrScan(rootHash [32]byte) (string, bool) {
	if path, ok := ts.LookupHash(rootHash); ok {
		return path, true
	}
	return ts.scanSharesForHash(rootHash)
}

// scanSharesForHash scans shared files to find one matching the root hash (IF6-3, IF13-3).
// Bounded at maxScanSharedFiles to prevent disk I/O amplification.
func (ts *TransferService) scanSharesForHash(rootHash [32]byte) (string, bool) {
	// IF12-3: Only scan for authorized peers. In current architecture, all peers
	// reaching this code path are already authorized (connection gater enforces auth).
	// For future public networks: add peer auth level check here.
	if ts.sharedFileLister == nil {
		return "", false
	}
	files := ts.sharedFileLister()
	if len(files) > maxScanSharedFiles {
		slog.Warn("multi-peer: too many shared files for hash scan", "count", len(files))
		files = files[:maxScanSharedFiles]
	}
	for _, path := range files {
		fi, err := os.Stat(path)
		if err != nil || fi.IsDir() {
			continue
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		var chunkHashes [][32]byte
		scanErr := ChunkReader(f, fi.Size(), func(c Chunk) error {
			chunkHashes = append(chunkHashes, c.Hash)
			return nil
		})
		f.Close()
		if scanErr != nil {
			continue
		}
		if sdk.MerkleRoot(chunkHashes) == rootHash {
			ts.RegisterHash(rootHash, path)
			return path, true
		}
	}
	return "", false
}

// SetSharedFileLister sets the callback for listing shared file paths (IF13-3).
// Called by plugin.Start() to wire the share registry to the transfer service.
func (ts *TransferService) SetSharedFileLister(f func() []string) {
	ts.sharedFileLister = f
}

// --- Receiver side (DownloadMultiPeer) ---

// MultiPeerStreamOpener opens a stream to a peer for multi-peer download.
type MultiPeerStreamOpener func(peerID peer.ID) (network.Stream, error)

// DownloadMultiPeer downloads a file from multiple peers using work-stealing
// block-level transfer. Each peer serves a subset of blocks. Fast peers get more.
// Memory: O(1 block) not O(file_size) (C7).
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

	// IF-8 + TS-3: Race all peers for manifest in parallel (Tail Slayer pattern).
	// Each peer is an independent path. First valid manifest wins, losing streams closed.
	// Eliminates sequential 3-minute timeout per peer.
	var manifest *transferManifest
	var firstStream network.Stream
	var firstPeerIdx int
	requestFlags := uint32(flagMPCompressionSupported | flagMPResumeSupported)
	peerErrors := make(map[string]string) // IF16-4: per-peer rejection reasons

	type manifestResult struct {
		manifest *transferManifest
		stream   network.Stream
		peerIdx  int
		err      error
		shortPID string
	}

	manifestCh := make(chan manifestResult, numPeers)
	var manifestWg sync.WaitGroup

	for i, pid := range peers {
		manifestWg.Add(1)
		go func(idx int, peerID peer.ID) {
			defer manifestWg.Done()
			shortPID := peerID.String()
			if len(shortPID) > 16 {
				shortPID = shortPID[:16]
			}
			stream, err := openStream(peerID)
			if err != nil {
				manifestCh <- manifestResult{err: err, peerIdx: idx, shortPID: shortPID}
				return
			}
			m, err := requestMultiPeerManifest(stream, rootHash, requestFlags)
			if err != nil {
				stream.Close()
				manifestCh <- manifestResult{err: err, peerIdx: idx, shortPID: shortPID}
				return
			}
			// Verify manifest (S10, C12, C13).
			computedRoot := sdk.MerkleRoot(m.ChunkHashes)
			if computedRoot != rootHash {
				stream.Close()
				manifestCh <- manifestResult{
					err: fmt.Errorf("manifest root hash mismatch"), peerIdx: idx, shortPID: shortPID,
				}
				return
			}
			if err := validateManifestSizes(m); err != nil {
				stream.Close()
				manifestCh <- manifestResult{err: err, peerIdx: idx, shortPID: shortPID}
				return
			}
			manifestCh <- manifestResult{manifest: m, stream: stream, peerIdx: idx, shortPID: shortPID}
		}(i, pid)
	}

	// Close channel when all goroutines finish.
	go func() {
		manifestWg.Wait()
		close(manifestCh)
	}()

	// Collect results: first valid manifest wins, proceed IMMEDIATELY.
	// Do NOT wait for slow peers (3-minute timeout would defeat hedging).
	for r := range manifestCh {
		if r.err != nil {
			peerErrors[r.shortPID] = r.err.Error()
			slog.Warn("multi-peer: manifest failed", "peer_index", r.peerIdx, "error", r.err)
			continue
		}
		// First valid manifest - take it and stop waiting.
		manifest = r.manifest
		firstStream = r.stream
		firstPeerIdx = r.peerIdx
		break
	}

	if manifest == nil {
		return nil, &multiPeerError{peerErrors: peerErrors}
	}

	// Background: drain remaining manifest results, close loser streams,
	// and cross-verify manifests for consistency (TS-3 plan requirement).
	winnerRoot := rootHash
	winnerFilename := manifest.Filename
	winnerFileSize := manifest.FileSize
	go func() {
		for r := range manifestCh {
			if r.err != nil {
				slog.Debug("multi-peer: late manifest failed",
					"peer_index", r.peerIdx, "error", r.err)
				continue
			}
			// Cross-verify: metadata should match the winner.
			// Root hash match is cryptographically guaranteed (verified in goroutine),
			// but filename/size mismatch indicates a peer serving different content
			// under the same hash - log as warning for diagnostics.
			if r.manifest.Filename != winnerFilename || r.manifest.FileSize != winnerFileSize {
				slog.Warn("multi-peer: manifest metadata mismatch from peer",
					"peer_index", r.peerIdx,
					"winner_root", fmt.Sprintf("%x", winnerRoot[:8]),
					"peer_filename", r.manifest.Filename,
					"winner_filename", winnerFilename,
					"peer_size", r.manifest.FileSize,
					"winner_size", winnerFileSize)
			}
			slog.Debug("multi-peer: closing loser manifest stream",
				"peer_index", r.peerIdx, "peer", r.shortPID)
			r.stream.Close()
		}
	}()

	// Size limit.
	if ts.maxSize > 0 && manifest.FileSize > ts.maxSize {
		firstStream.Close()
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", manifest.FileSize, ts.maxSize)
	}

	// Compute content key with multi-peer salt (IF-5).
	contentKey := multiPeerContentKey(rootHash)

	// Check for existing checkpoint (D8: resume support).
	ckptPath := checkpointPath(destDir, contentKey)
	var existingHave *bitfield

	// IF-10: Concurrent download protection via lock file.
	lockPath := filepath.Join(destDir, fmt.Sprintf(".shurli-mp-%x.lock", contentKey[:8]))
	lockFile, lockErr := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if lockErr != nil {
		// Check if stale (> 2 hours).
		if info, err := os.Stat(lockPath); err == nil && time.Since(info.ModTime()) > 2*time.Hour {
			os.Remove(lockPath)
			lockFile, lockErr = os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		}
		if lockErr != nil {
			firstStream.Close()
			return nil, fmt.Errorf("another download for this file is in progress")
		}
	}
	lockFile.Close()
	// Lock file deleted inside background goroutine on completion, NOT here.
	// DownloadMultiPeer returns immediately; defer would delete the lock
	// while the download is still running.

	// Try to load checkpoint.
	ckpt, loadErr := loadCheckpointFile(destDir, contentKey)
	if loadErr == nil && ckpt.contentKey == contentKey {
		existingHave = ckpt.have
		slog.Info("multi-peer: resuming from checkpoint",
			"blocks_done", existingHave.count(), "total", manifest.ChunkCount)
	} else if loadErr != nil && !os.IsNotExist(loadErr) {
		// IF4-4: Corrupt checkpoint — start fresh.
		slog.Warn("multi-peer: corrupt checkpoint, starting fresh", "error", loadErr)
		os.Remove(ckptPath)
	}

	if existingHave == nil {
		existingHave = newBitfield(manifest.ChunkCount)
	}

	// Disk space check (IF3-11: adjusted for resume).
	var neededBytes int64
	for i := 0; i < manifest.ChunkCount; i++ {
		if !existingHave.has(i) {
			neededBytes += int64(manifest.ChunkSizes[i])
		}
	}
	if err := checkDiskSpaceAt(destDir, neededBytes); err != nil {
		firstStream.Close()
		return nil, fmt.Errorf("insufficient disk space: %w", err)
	}

	// Compute block offsets for WriteAt (I8).
	offsets := computeBlockOffsets(manifest)

	// Create or open pre-allocated temp file (I9, IF-11).
	tmpPath := filepath.Join(destDir, fmt.Sprintf(".shurli-mp-%x.tmp", contentKey[:8]))
	tmpFile, err := os.OpenFile(tmpPath, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		firstStream.Close()
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	// Pre-allocate (I7: sparse file).
	if err := tmpFile.Truncate(manifest.FileSize); err != nil {
		tmpFile.Close()
		os.Remove(tmpPath)
		firstStream.Close()
		return nil, fmt.Errorf("pre-allocate temp file: %w", err)
	}

	// Create block queue.
	queue := newBlockQueue(manifest.ChunkCount, existingHave)

	// Track transfer.
	progress := ts.trackTransfer(manifest.Filename, manifest.FileSize,
		"multi-peer", "download", manifest.ChunkCount, false)
	progress.setStatus("active")
	progress.initStreams(numPeers)

	// Set initial progress from checkpoint.
	alreadyDone := int(queue.completed.Load())
	if alreadyDone > 0 {
		var doneBytes int64
		for i := 0; i < manifest.ChunkCount; i++ {
			if existingHave.has(i) {
				doneBytes += int64(manifest.ChunkSizes[i])
			}
		}
		queue.transferredBytes.Store(doneBytes)
		progress.updateChunks(doneBytes, alreadyDone)
	}

	// D1: cancellable context.
	dlCtx, dlCancel := context.WithCancel(ctx)
	progress.setCancelFunc(dlCancel)
	ts.logEvent(EventLogStarted, "multi-peer-download", "multi-peer",
		manifest.Filename, manifest.FileSize, 0, "", "")

	session := &multiPeerSession{
		manifest:        manifest,
		queue:           queue,
		tmpFile:         tmpFile,
		tmpPath:         tmpPath,
		offsets:         offsets,
		contentKey:      contentKey,
		progress:        progress,
		strikeThreshold: ts.multiPeerStrikeThreshold,
	}

	// Launch background download.
	go func() {
		defer dlCancel()
		defer os.Remove(lockPath) // Lock file lives for entire download duration.
		tmpFileClosed := false
		defer func() {
			if !tmpFileClosed {
				tmpFile.Close()
			}
		}()
		dlStart := time.Now()

		var wg sync.WaitGroup
		allPeersDone := make(chan struct{})

		// Save checkpoint helper.
		saveCheckpointFn := func() {
			snap := queue.checkpointSnapshot()
			ckpt := &transferCheckpoint{
				contentKey: contentKey,
				files:      []fileEntry{{Path: manifest.Filename, Size: manifest.FileSize}},
				totalSize:  manifest.FileSize,
				have:       snap,
				hashes:     manifest.ChunkHashes,
				sizes:      manifest.ChunkSizes,
				tmpPaths:   []string{filepath.Base(tmpPath)},
			}
			if err := ckpt.save(destDir); err != nil {
				slog.Warn("multi-peer: checkpoint save failed", "error", err)
			}
		}

		// Peer goroutine function.
		// runPeer handles the pipeline block loop for a single peer.
		// Caller is responsible for wg.Done(). Stream must have manifest already exchanged.
		runPeer := func(peerIdx int, pid peer.ID, stream network.Stream, ps *peerState) {
			exitReason := "unknown"
			streamOwned := true
			defer func() {
				slog.Info("multi-peer: peer goroutine exited",
					"peer_index", peerIdx, "reason", exitReason,
					"blocks_ok", ps.blocksOK.Load(), "blocks_fail", ps.blocksFail.Load(),
					"bytes", ps.bytesRecv.Load(), "slow", ps.slow, "banned", ps.banned,
					"strikes", ps.strikes, "reconnects", ps.reconnects)
				if streamOwned && stream != nil {
					stream.Close()
				}
			}()

			// Caller has already verified manifest for this stream.
			session.addPeerStream(stream)
			ps.startTime = time.Now()
			ps.transport = sdk.VerifiedTransport(stream, ts.hasVerifiedLANConn)
			// H7: per-peer relay grant byte tracker. Each peer stream may go through
			// a different relay, so each needs its own tracker.
			peerRelayTracker := ts.makeChunkTracker(stream, "recv")
			lastSave := time.Now()

			// Pipeline: pre-fill then loop (IF-9).
			pipeline := make([]int, 0, multiPeerPipelineDepth)

			// Re-queue all pipeline blocks on exit (IF-4).
			defer func() {
				for _, idx := range pipeline {
					queue.requeue(idx)
				}
			}()

			// Pre-fill pipeline.
			for len(pipeline) < multiPeerPipelineDepth {
				idx, ok := queue.claim(dlCtx, ps.slow)
				if !ok {
					break
				}
				if err := sendBlockRequest(stream, idx); err != nil {
					// IF14-1: re-queue immediately before it's lost.
					queue.requeue(idx)
					exitReason = "prefill-send-error"
					return
				}
				pipeline = append(pipeline, idx)
			}

			// Main pipeline loop.
			for len(pipeline) > 0 {
				// Read response (IF15-1: dispatch on first byte).
				stream.SetDeadline(time.Now().Add(multiPeerBlockTimeout))

				var typeByte [1]byte
				if _, err := io.ReadFull(stream, typeByte[:]); err != nil {
					// Stream error — try reconnect (IF11-1).
					if ps.reconnects < multiPeerMaxReconnect {
						stream.Close()
						streamOwned = false
						newStream, openErr := openStream(pid)
						if openErr != nil {
							exitReason = "reconnect-open-failed"
							return
						}
						stream = newStream
						streamOwned = true
						session.addPeerStream(newStream)
						ps.transport = sdk.VerifiedTransport(newStream, ts.hasVerifiedLANConn)
						peerRelayTracker = ts.makeChunkTracker(newStream, "recv") // H7: refresh relay tracker for new stream
						ps.reconnects++
						slog.Info("multi-peer: peer reconnected",
							"peer_index", ps.peerIdx, "reconnects", ps.reconnects)

						// Re-exchange manifest on new stream and verify root hash.
						reconnManifest, mErr := requestMultiPeerManifest(newStream, rootHash, requestFlags)
						if mErr != nil {
							exitReason = "reconnect-manifest-failed"
							return
						}
						if sdk.MerkleRoot(reconnManifest.ChunkHashes) != rootHash {
							exitReason = "reconnect-manifest-mismatch"
							return
						}

						// Re-send all pipeline block requests.
						for _, idx := range pipeline {
							if err := sendBlockRequest(stream, idx); err != nil {
								exitReason = "reconnect-resend-failed"
								return
							}
						}
						continue // retry reading response
					}
					exitReason = "max-reconnects-exhausted"
					return
				}

				expectedBlock := pipeline[0]

				switch typeByte[0] {
				case msgBlockData:
					// Read: blockIndex(4) + flags(1) + decompSize(4) + dataLen(4) = 13 bytes.
					var dataHeader [13]byte
					if _, err := io.ReadFull(stream, dataHeader[:]); err != nil {
						exitReason = "read-block-header"
						return
					}
					blockIndex := int(binary.BigEndian.Uint32(dataHeader[0:4]))
					flags := dataHeader[4]
					decompSize := binary.BigEndian.Uint32(dataHeader[5:9])
					dataLen := binary.BigEndian.Uint32(dataHeader[9:13])

					// Security: validate data length and decompSize before allocation.
					if blockIndex < 0 || blockIndex >= manifest.ChunkCount {
						exitReason = fmt.Sprintf("block-index-oob:%d", blockIndex)
						return
					}
					maxLen := manifest.ChunkSizes[blockIndex]
					if dataLen > maxLen {
						exitReason = fmt.Sprintf("oversized-block:%d-len:%d-max:%d", blockIndex, dataLen, maxLen)
						return
					}
					if decompSize > uint32(maxDecompressedChunk) {
						exitReason = fmt.Sprintf("decomp-too-large:%d", decompSize)
						return
					}

					// Read block data.
					blockData := make([]byte, dataLen)
					if _, err := io.ReadFull(stream, blockData); err != nil {
						exitReason = "read-block-data"
						return
					}

					// IF-S3: verify response order.
					if blockIndex != expectedBlock {
						slog.Warn("multi-peer: out-of-order response", "peer_index", peerIdx,
							"expected", expectedBlock, "got", blockIndex)
						ps.strikes++
						exitReason = fmt.Sprintf("out-of-order:expected=%d,got=%d", expectedBlock, blockIndex)
						return
					}

					// Remove from pipeline head.
					pipeline = pipeline[1:]

					// Wire bytes tracking (C11) + relay grant tracking (H7).
					wireBytes := int64(14 + dataLen)
					progress.addWireBytes(wireBytes)
					progress.updateStream(peerIdx, wireBytes)
					ps.bytesRecv.Add(wireBytes)
					if peerRelayTracker != nil {
						peerRelayTracker(wireBytes)
					}

					// Process block: decompress, validate, write (D7, IF8-2, C8).
					blockFailed := false
					var raw []byte
					if flags&blockFlagCompressed != 0 {
						decompressed, decErr := decompressChunk(blockData, int(decompSize))
						if decErr != nil {
							slog.Warn("multi-peer: decompress failed", "peer_index", peerIdx,
								"block", blockIndex, "error", decErr)
							ps.strikes++
							if ps.strikes >= session.strikeThreshold {
								ps.banned = true
								slog.Warn("multi-peer: peer banned",
									"peer_index", ps.peerIdx, "strikes", ps.strikes)
								queue.requeue(blockIndex)
								exitReason = "banned-decompress"
								return
							}
							ps.onParole = true
							queue.requeue(blockIndex)
							blockFailed = true
						} else {
							raw = decompressed
						}
					} else {
						raw = blockData
					}

					if !blockFailed && uint32(len(raw)) != manifest.ChunkSizes[blockIndex] {
						slog.Warn("multi-peer: block size mismatch", "peer_index", peerIdx,
							"block", blockIndex, "expected", manifest.ChunkSizes[blockIndex],
							"got", len(raw))
						ps.strikes++
						if ps.strikes >= session.strikeThreshold {
							ps.banned = true
							slog.Warn("multi-peer: peer banned",
								"peer_index", ps.peerIdx, "strikes", ps.strikes)
							queue.requeue(blockIndex)
							exitReason = "banned-size-mismatch"
							return
						}
						ps.onParole = true
						queue.requeue(blockIndex)
						blockFailed = true
					}

					if !blockFailed {
						hash := blake3Hash(raw)
						if hash != manifest.ChunkHashes[blockIndex] {
							ps.strikes++
							ps.blocksFail.Add(1)
							if ps.strikes >= session.strikeThreshold {
								ps.banned = true
								slog.Warn("multi-peer: peer banned",
									"peer_index", ps.peerIdx, "strikes", ps.strikes)
								queue.requeue(blockIndex)
								exitReason = "banned-hash-mismatch"
								return
							}
							ps.onParole = true
							queue.requeue(blockIndex)
							blockFailed = true
						}
					}

					if !blockFailed {
						if ps.onParole {
							ps.onParole = false
						}
						ps.consecErrors = 0
						ps.blocksOK.Add(1)

						if _, wErr := tmpFile.WriteAt(raw, offsets[blockIndex]); wErr != nil {
							saveCheckpointFn()
							progress.finish(fmt.Errorf("disk write error at block %d: %w", blockIndex, wErr))
							// Don't call markCompleted here — dlCancel triggers the outer
							// goroutine's ctx.Done path which handles markCompleted.
							dlCancel()
							exitReason = "disk-write-error"
							return
						}

						queue.markComplete(blockIndex, int64(len(raw)))
						progress.updateChunks(queue.transferredBytes.Load(), int(queue.completed.Load()))

						// Speed tracking for diagnostics (slow demotion removed:
						// average-since-start vs all-time-peak caused both peers
						// to be marked slow, creating deadlock IF-DEADLOCK-2).
						// Work-stealing handles speed differences naturally — fast
						// peers claim more blocks by processing faster.

						if time.Since(lastSave) >= checkpointSaveInterval {
							saveCheckpointFn()
							lastSave = time.Now()
						}
					}

					if ps.banned {
						exitReason = "banned-post-block"
						return
					}
					// Non-blocking claim when pipeline has work: prevents deadlock
					// where slow peer blocks in claim() while holding unreceived
					// pipeline responses (IF-DEADLOCK-1).
					if len(pipeline) > 0 {
						// Try non-blocking claim first.
						idx, ok := queue.tryClaim(ps.slow)
						if ok {
							if err := sendBlockRequest(stream, idx); err != nil {
								queue.requeue(idx)
								exitReason = "send-block-request-error"
								return
							}
							pipeline = append(pipeline, idx)
						}
						// Even if no new block claimed, continue draining pipeline.
					} else {
						// Pipeline empty — blocking claim is safe.
						idx, ok := queue.claim(dlCtx, ps.slow)
						if !ok {
							exitReason = "claim-failed-done-or-cancelled"
							return
						}
						if err := sendBlockRequest(stream, idx); err != nil {
							queue.requeue(idx)
							exitReason = "send-block-request-error"
							return
						}
						pipeline = append(pipeline, idx)
					}

				case msgBlockError:
					// Read: blockIndex(4) + errLen(2) = 6 bytes.
					var errHeader [6]byte
					if _, err := io.ReadFull(stream, errHeader[:]); err != nil {
						exitReason = "read-error-header"
						return
					}
					respBlockIndex := int(binary.BigEndian.Uint32(errHeader[0:4]))
					errLen := binary.BigEndian.Uint16(errHeader[4:6])
					if errLen > 0 {
						discard := make([]byte, errLen)
						if _, err := io.ReadFull(stream, discard); err != nil {
							exitReason = "read-error-body"
							return
						}
					}

					// Use expectedBlock for requeue, not sender-supplied index.
					// A malicious sender could inject arbitrary block indices.
					if respBlockIndex != expectedBlock {
						// Protocol error — sender returned error for wrong block.
						ps.strikes++
						exitReason = fmt.Sprintf("error-wrong-block:expected=%d,got=%d", expectedBlock, respBlockIndex)
						return
					}
					pipeline = pipeline[1:]

					// IF8-1: Track consecutive errors.
					ps.consecErrors++
					if ps.consecErrors >= multiPeerDisconnectAfter {
						slog.Warn("multi-peer: too many consecutive errors, disconnecting",
							"peer_index", peerIdx, "errors", ps.consecErrors)
						queue.requeue(expectedBlock)
						exitReason = "too-many-consec-errors"
						return
					}
					if ps.consecErrors >= multiPeerMaxConsecErrors {
						ps.slow = true // demote to retry-only
					}
					queue.requeue(expectedBlock)

					// Claim next (non-blocking if pipeline has work).
					if len(pipeline) > 0 {
						idx, ok := queue.tryClaim(ps.slow)
						if ok {
							if err := sendBlockRequest(stream, idx); err != nil {
								queue.requeue(idx)
								exitReason = "error-send-request"
								return
							}
							pipeline = append(pipeline, idx)
						}
					} else {
						idx, ok := queue.claim(dlCtx, ps.slow)
						if !ok {
							exitReason = "error-claim-failed"
							return
						}
						if err := sendBlockRequest(stream, idx); err != nil {
							queue.requeue(idx)
							exitReason = "error-send-request"
							return
						}
						pipeline = append(pipeline, idx)
					}

				default:
					slog.Warn("multi-peer: unexpected response type", "peer_index", peerIdx,
						"type", typeByte[0])
					exitReason = fmt.Sprintf("unexpected-type:0x%02x", typeByte[0])
					return
				}
			}

			// Send done signal to sender (I14).
			exitReason = "pipeline-drained-clean"
			stream.Write([]byte{msgMultiPeerDone})
		}

		// Launch first peer (already has stream + manifest).
		ps0 := &peerState{peerID: peers[firstPeerIdx], peerIdx: firstPeerIdx}
		wg.Add(1)
		go func() {
			defer wg.Done()
			runPeer(firstPeerIdx, peers[firstPeerIdx], firstStream, ps0)
		}()

		// Launch remaining peers.
		for i := 0; i < numPeers; i++ {
			if i == firstPeerIdx {
				continue
			}
			peerIdx := i
			pid := peers[peerIdx]
			ps := &peerState{peerID: pid, peerIdx: peerIdx}
			wg.Add(1)
			go func() {
				defer wg.Done()
				stream, err := openStream(pid)
				if err != nil {
					slog.Warn("multi-peer: connect failed", "peer_index", peerIdx, "error", err)
					return
				}
				// Verify manifest. (startTime set inside runPeer after manifest exchange)
				peerManifest, mErr := requestMultiPeerManifest(stream, rootHash, requestFlags)
				if mErr != nil {
					stream.Close()
					slog.Warn("multi-peer: peer manifest failed", "peer_index", peerIdx, "error", mErr)
					return
				}
				peerRoot := sdk.MerkleRoot(peerManifest.ChunkHashes)
				if peerRoot != rootHash {
					stream.Close()
					slog.Warn("multi-peer: peer manifest root mismatch", "peer_index", peerIdx)
					return
				}
				if peerManifest.ChunkCount != manifest.ChunkCount {
					stream.Close()
					return
				}
				for ci := 0; ci < manifest.ChunkCount; ci++ {
					if peerManifest.ChunkSizes[ci] != manifest.ChunkSizes[ci] {
						stream.Close()
						return
					}
				}

				// Call shared runPeer — no duplication, no double wg.Done.
				// runPeer handles pipeline, checkpoint, reconnect, strikes, everything.
				// runPeer also handles stream.Close() via its own defer.
				runPeer(peerIdx, pid, stream, ps)
				// Dead code below deleted — was the duplicated pipeline loop.
			}()
		}
		// Completion waiter goroutine (IF6-4).
		go func() {
			wg.Wait()
			close(allPeersDone)
		}()

		// Wait for completion (IF-I25: three exit paths).
		select {
		case <-queue.done:
			// IF6-1: Wait for ALL goroutines to exit before touching temp file.
			wg.Wait()
		case <-allPeersDone:
			// All peers exited. May be incomplete.
		case <-dlCtx.Done():
			// IF3-5: Cancel — reset all streams to unblock goroutines.
			session.resetAllStreams()
			wg.Wait()
			// IF3-3/IF7-1: Save checkpoint on cancel.
			saveCheckpointFn()
			progress.finish(dlCtx.Err())
			ts.markCompleted(progress.ID)
			ts.logEvent(EventLogCancelled, "multi-peer-download", "multi-peer",
				manifest.Filename, manifest.FileSize, queue.transferredBytes.Load(), "cancelled", "")
			return
		}

		if int(queue.completed.Load()) < queue.total {
			saveCheckpointFn()
			errMsg := fmt.Sprintf("incomplete: %d/%d blocks", queue.completed.Load(), queue.total)
			progress.finish(fmt.Errorf("multi-peer download %s", errMsg))
			ts.markCompleted(progress.ID)
			ts.logEvent(EventLogFailed, "multi-peer-download", "multi-peer",
				manifest.Filename, manifest.FileSize, queue.transferredBytes.Load(), errMsg, "")
			return
		}

		// IF15-2: Force final checkpoint save before finalization.
		saveCheckpointFn()

		// IF-7/IF9-3: Post-resume Merkle verification (re-read file, verify root hash).
		if _, err := tmpFile.Seek(0, io.SeekStart); err != nil {
			progress.finish(fmt.Errorf("seek for verification: %w", err))
			ts.markCompleted(progress.ID)
			return
		}
		var verifyHashes [][32]byte
		verifyErr := ChunkReader(tmpFile, manifest.FileSize, func(c Chunk) error {
			select {
			case <-dlCtx.Done():
				return dlCtx.Err()
			default:
			}
			verifyHashes = append(verifyHashes, c.Hash)
			return nil
		})
		if verifyErr != nil {
			progress.finish(fmt.Errorf("verification failed: %w", verifyErr))
			ts.markCompleted(progress.ID)
			ts.logEvent(EventLogFailed, "multi-peer-download", "multi-peer",
				manifest.Filename, manifest.FileSize, manifest.FileSize, "verification failed", "")
			return
		}
		if sdk.MerkleRoot(verifyHashes) != rootHash {
			// Corrupt resume data — delete temp + checkpoint, start fresh next time.
			os.Remove(tmpPath)
			removeStreamCheckpoint(destDir, contentKey)
			progress.finish(fmt.Errorf("corrupt resume data detected, starting fresh"))
			ts.markCompleted(progress.ID)
			ts.logEvent(EventLogFailed, "multi-peer-download", "multi-peer",
				manifest.Filename, manifest.FileSize, manifest.FileSize, "corrupt resume data", "")
			return
		}

		// Fsync and rename (I9).
		if err := tmpFile.Sync(); err != nil {
			progress.finish(fmt.Errorf("sync: %w", err))
			ts.markCompleted(progress.ID)
			return
		}
		tmpFile.Close()
		tmpFileClosed = true

		finalPath := filepath.Join(destDir, filepath.Base(manifest.Filename))
		finalPath, fpErr := nonCollidingPath(finalPath)
		if fpErr != nil {
			os.Remove(tmpPath)
			progress.finish(fpErr)
			ts.markCompleted(progress.ID)
			return
		}

		if err := os.Rename(tmpPath, finalPath); err != nil {
			os.Remove(tmpPath)
			progress.finish(err)
			ts.markCompleted(progress.ID)
			return
		}
		os.Chmod(finalPath, 0644)

		progress.finish(nil)
		ts.markCompleted(progress.ID)

		// I16: Register hash for future multi-peer serving.
		ts.RegisterHash(rootHash, finalPath)

		// IF-I15: Cleanup checkpoint.
		removeStreamCheckpoint(destDir, contentKey)

		// IF3-10: Desktop notification.
		if ts.notifier != nil {
			ts.notifier.Notify("multi-peer", manifest.Filename, manifest.FileSize)
		}

		dur := time.Since(dlStart).Truncate(time.Millisecond).String()
		elapsed := time.Since(dlStart).Seconds()
		speedMBs := ""
		if elapsed > 0 {
			speedMBs = fmt.Sprintf("%.1f MB/s", float64(manifest.FileSize)/elapsed/1024/1024)
		}
		slog.Info("multi-peer: download complete",
			"file", manifest.Filename, "size", manifest.FileSize,
			"peers", numPeers, "blocks", manifest.ChunkCount,
			"duration", dur, "speed", speedMBs)
		ts.logEvent(EventLogCompleted, "multi-peer-download", "multi-peer",
			manifest.Filename, manifest.FileSize, manifest.FileSize, "", dur)
	}()

	return progress, nil
}

// requestMultiPeerManifest sends a multi-peer request and reads the manifest
// response. Handles reject messages instantly (IF15-1: response type dispatch).
func requestMultiPeerManifest(s network.Stream, rootHash [32]byte, flags uint32) (*transferManifest, error) {
	s.SetDeadline(time.Now().Add(multiPeerManifestTimeout))

	// Write: type(1) + rootHash(32) + flags(4) = 37 bytes (I13).
	var header [37]byte
	header[0] = msgMultiPeerRequest
	copy(header[1:33], rootHash[:])
	binary.BigEndian.PutUint32(header[33:37], flags)
	if _, err := s.Write(header[:]); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// IF15-1: Read first byte to dispatch response type.
	var typeByte [1]byte
	if _, err := io.ReadFull(s, typeByte[:]); err != nil {
		return nil, fmt.Errorf("read response type: %w", err)
	}

	switch typeByte[0] {
	case msgMultiPeerReject:
		var reason [1]byte
		if _, err := io.ReadFull(s, reason[:]); err != nil {
			return nil, fmt.Errorf("peer rejected (reason unreadable: %w)", err)
		}
		return nil, fmt.Errorf("peer rejected: %s", multiPeerRejectString(reason[0]))

	case msgMultiPeerManifest:
		var lenBuf [4]byte
		if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
			return nil, fmt.Errorf("read manifest length: %w", err)
		}
		manifestLen := binary.BigEndian.Uint32(lenBuf[:])
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

	default:
		return nil, fmt.Errorf("unexpected response type: 0x%02x", typeByte[0])
	}
}

// validateManifestSizes checks C12 (sum of sizes == fileSize) and C13 (per-chunk bounds).
func validateManifestSizes(m *transferManifest) error {
	var total int64
	for i, size := range m.ChunkSizes {
		if size > uint32(maxChunkWireSize) {
			return fmt.Errorf("chunk %d size %d exceeds max %d", i, size, maxChunkWireSize)
		}
		total += int64(size)
	}
	if total != m.FileSize {
		return fmt.Errorf("chunk sizes sum %d != declared file size %d", total, m.FileSize)
	}
	return nil
}

// multiPeerContentKey derives a content key for multi-peer checkpoints.
// Uses "multi-peer" salt to prevent collision with single-peer checkpoints (IF-5).
func multiPeerContentKey(rootHash [32]byte) [32]byte {
	h := blake3.New()
	h.Write([]byte("shurli-multi-peer-v1"))
	h.Write(rootHash[:])
	var key [32]byte
	copy(key[:], h.Sum(nil))
	return key
}

// computeBlockOffsets returns cumulative byte offsets for WriteAt (I8).
func computeBlockOffsets(m *transferManifest) []int64 {
	offsets := make([]int64, m.ChunkCount)
	var off int64
	for i := 0; i < m.ChunkCount; i++ {
		offsets[i] = off
		off += int64(m.ChunkSizes[i])
	}
	return offsets
}

// loadCheckpointFile loads a multi-peer checkpoint from disk using the content key.
func loadCheckpointFile(destDir string, ck [32]byte) (*transferCheckpoint, error) {
	return loadCheckpoint(destDir, ck)
}

// --- Manifest wire format (kept from original, enhanced with C12/C13 validation) ---

// marshalManifest serializes a manifest to bytes for multi-peer transport.
func marshalManifest(m *transferManifest) ([]byte, error) {
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
	if len(data) < 46 {
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

	m := &transferManifest{
		Filename:    filename,
		FileSize:    fileSize,
		ChunkCount:  chunkCount,
		RootHash:    rootHash,
		ChunkHashes: hashes,
		ChunkSizes:  sizes,
	}

	// C12: sum of chunk sizes must equal file size.
	// C13: per-chunk size must be bounded.
	if err := validateManifestSizes(m); err != nil {
		return nil, err
	}

	return m, nil
}

// --- Unused symbol cleanup: old RaptorQ types/functions deleted per IF-I16 ---
// transfer_raptorq.go and transfer_raptorq_test.go kept intact as library.
