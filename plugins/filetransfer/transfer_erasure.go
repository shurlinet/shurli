package filetransfer

import (
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"

	"github.com/klauspost/reedsolomon"
	"github.com/shurlinet/shurli/pkg/sdk"
)

// Erasure coding constants.
const (
	// flagErasureCoded indicates the manifest includes parity chunks.
	flagErasureCoded = 0x02

	// defaultStripeSize is the number of data chunks per RS stripe.
	// 100 data + 10 parity (at 10%) = 110 total, well within RS limits.
	//
	// Coupling (ChunkTarget + R4-SEC1 Batch 2): peak resident RS memory is
	// O(defaultStripeSize x max-chunk). With FT-Y #14's 4 MB max-chunk tier
	// a full stripe raw buffer is 100 x 4 MB = 400 MB. encodeStripe pads
	// shards to max-chunk size and allocates (stripeSize + parityCount) shards,
	// so peak momentary memory during the reed-solomon call is ~880 MB:
	// 400 MB raw stripeBuf + 440 MB padded shards + 40 MB parity output.
	// erasureEncoder nil-s stripeBuf entries immediately after encodeStripe so
	// GC reclaims the raw buffer promptly; sustained peak after the call is
	// ~400 MB while the next stripe fills. RS is still gated off by the
	// transport classifier (LAN + Direct disable RS), so sustained peak only
	// applies to WAN/relay transfers with the top chunk tier active.
	defaultStripeSize = 100

	// minStripeSize rejects degenerate stripe layouts from malicious senders.
	// stripeSize < 2 means RS has no recovery capability (1 data shard, 1+ parity
	// covers nothing the data shard itself already provides). A malicious sender
	// declaring stripeSize=1 could also force reconstruction to iterate
	// chunkCount stripes, each allocating max-chunk-sized padded shards.
	// [B2-F11]
	minStripeSize = 2

	// maxAcceptedStripeSize caps the stripe size a receiver will accept in an
	// erasure trailer. rsReconstruct allocates O(stripeSize x maxChunkSize)
	// per stripe for padded shards plus parityCount padded parity shards; an
	// unchecked stripeSize bounded only by maxChunkCount would let a
	// malicious sender force multi-GB reconstruction allocations against a
	// daemon running under MemoryMax=2G. The cap at 2x defaultStripeSize
	// keeps worst-case reconstruction below ~1.6 GB (200 x 8 MB) while
	// leaving headroom for future legitimate larger-stripe configurations.
	// [Batch 2b audit round 3]
	maxAcceptedStripeSize = defaultStripeSize * 2

	// maxParityOverhead caps erasure overhead at 50%.
	maxParityOverhead = 0.50

	// maxParityCount limits total parity chunks.
	maxParityCount = maxChunkCount / 2
)

// erasureParams describes the RS configuration for a transfer.
type erasureParams struct {
	StripeSize  int // data chunks per stripe
	ParityCount int // total parity chunks across all stripes
}

// computeErasureParams calculates stripe layout for a given data chunk count and overhead.
func computeErasureParams(dataCount int, overhead float64) erasureParams {
	if overhead <= 0 || dataCount == 0 {
		return erasureParams{}
	}
	if overhead > maxParityOverhead {
		overhead = maxParityOverhead
	}

	stripeSize := defaultStripeSize
	if dataCount < stripeSize {
		stripeSize = dataCount
	}

	totalParity := 0
	for off := 0; off < dataCount; off += stripeSize {
		end := off + stripeSize
		if end > dataCount {
			end = dataCount
		}
		totalParity += stripeParityCount(end-off, overhead)
	}

	return erasureParams{
		StripeSize:  stripeSize,
		ParityCount: totalParity,
	}
}

// encodeStripe runs RS encoding on a single stripe of data chunks.
// Chunks are padded to the max size in the stripe. Returns parity shards.
func encodeStripe(dataChunks [][]byte, parityCount int) ([][]byte, error) {
	// Find max chunk size.
	maxSize := 0
	for _, c := range dataChunks {
		if len(c) > maxSize {
			maxSize = len(c)
		}
	}
	if maxSize == 0 {
		return nil, fmt.Errorf("all chunks empty")
	}

	totalShards := len(dataChunks) + parityCount
	shards := make([][]byte, totalShards)

	// Data shards: pad to maxSize.
	for i, c := range dataChunks {
		shard := make([]byte, maxSize)
		copy(shard, c)
		shards[i] = shard
	}

	// Parity shards: allocate.
	for i := len(dataChunks); i < totalShards; i++ {
		shards[i] = make([]byte, maxSize)
	}

	enc, err := reedsolomon.New(len(dataChunks), parityCount)
	if err != nil {
		return nil, fmt.Errorf("create RS encoder: %w", err)
	}

	if err := enc.Encode(shards); err != nil {
		return nil, fmt.Errorf("RS encode: %w", err)
	}

	// Extract parity shards.
	parity := make([][]byte, parityCount)
	for i := 0; i < parityCount; i++ {
		parity[i] = shards[len(dataChunks)+i]
	}

	return parity, nil
}

// --- Incremental per-stripe RS encoder (R4-SEC1, Batch 2) ---

// erasureEncoder produces parity chunks one stripe at a time, emitting them as
// each stripe fills. Replaces the O(totalSize) buffered encodeErasure path with
// O(stripeSize * maxChunkSize) peak memory: only one stripe of raw chunks is
// resident at any time.
//
// Wire layout is unchanged. Parity chunks carry fileIdx == parityFileIdx on the
// wire and are indexed by a 0-based global counter (not dataCount+i as in the
// legacy path). The fileIdx sentinel disambiguates parity from data chunks at
// the receiver, so the chunkIdx namespace is independent.
//
// Usage (producer goroutine):
//
//	enc := newErasureEncoder(stripeSize, overhead)
//	for each raw chunk:
//	    parity, err := enc.AddChunk(raw)   // raw is a copy owned by encoder
//	    for each p in parity: emit via ch as streamChunk{fileIdx: parityFileIdx, chunkIdx: p.idx}
//	parity, trailer, err := enc.Finalize()
//	emit final parity + store trailer in producerResult.erasure
type erasureEncoder struct {
	stripeSize    int
	overhead      float64
	stripeBuf     [][]byte   // current stripe's raw chunks (owned copies)
	nextParityIdx int        // 0-based global counter for parity chunkIdx
	parityHashes  [][32]byte // accumulated across stripes, trailer order
	paritySizes   []uint32   // accumulated across stripes, trailer order
}

// parityChunkOut carries a parity chunk plus the chunkIdx the producer should
// use when emitting it on the wire.
type parityChunkOut struct {
	chunkIdx int
	hash     [32]byte
	data     []byte
}

// newErasureEncoder returns an encoder ready to accept raw chunks. Returns nil
// if overhead is disabled; callers should gate AddChunk / Finalize on nil.
func newErasureEncoder(stripeSize int, overhead float64) *erasureEncoder {
	if overhead <= 0 || stripeSize < minStripeSize {
		return nil
	}
	if overhead > maxParityOverhead {
		overhead = maxParityOverhead
	}
	return &erasureEncoder{
		stripeSize: stripeSize,
		overhead:   overhead,
		stripeBuf:  make([][]byte, 0, stripeSize),
	}
}

// AddChunk appends raw to the current stripe. The encoder takes ownership of
// raw — callers must not mutate the slice after this call. When the stripe
// fills, the encoder encodes it, frees the raw buffers for GC, and returns
// the stripe's parity chunks. Returns nil when the stripe is not yet full.
func (e *erasureEncoder) AddChunk(raw []byte) ([]parityChunkOut, error) {
	e.stripeBuf = append(e.stripeBuf, raw)
	if len(e.stripeBuf) < e.stripeSize {
		return nil, nil
	}
	return e.flushStripe()
}

// Finalize encodes the trailing partial stripe (if any) and returns both the
// remaining parity chunks and the fully populated trailer. Callers emit the
// parity via ch before writing the trailer on the control stream.
//
// Returns (nil, nil, nil) if the encoder received zero chunks — the caller
// should treat this as "no erasure trailer" and not set flagErasureCoded.
// [B2-F4]
func (e *erasureEncoder) Finalize() ([]parityChunkOut, *erasureTrailer, error) {
	var residual []parityChunkOut
	if len(e.stripeBuf) > 0 {
		var err error
		residual, err = e.flushStripe()
		if err != nil {
			return nil, nil, err
		}
	}

	// Zero chunks ever encoded means no trailer to emit. Caller MUST NOT set
	// flagErasureCoded for a zero-chunk transfer; readErasureManifest would
	// reject stripeSize=0 on the receiving side.
	if len(e.parityHashes) == 0 {
		return residual, nil, nil
	}

	trailer := &erasureTrailer{
		StripeSize:       e.stripeSize,
		ParityCount:      len(e.parityHashes),
		OverheadPerMille: overheadToPerMille(e.overhead),
		ParityHashes:     e.parityHashes,
		ParitySizes:      e.paritySizes,
	}
	return residual, trailer, nil
}

// flushStripe encodes the current stripeBuf, clears it for GC, and assigns
// parity chunkIdx values. [B2-F5] nil-s stripeBuf entries immediately after
// encodeStripe copies them into padded shards so GC can reclaim ~400 MB of
// raw buffer promptly; sustained peak drops from ~880 MB to ~400 MB between
// stripes.
//
// [B2 audit fix S8] stripeBuf is cleared unconditionally via defer, even when
// encodeStripe errors. Leaving the encoder with a non-empty stripeBuf after
// an error would allow a subsequent AddChunk or Finalize to re-encode the
// same stripe (duplicate parity on wire) or mis-align parity with later
// stripes. After an error the encoder must be in a safe-to-discard state —
// not a retryable state. Callers already abandon the encoder on error; the
// defer guarantees that assumption holds.
func (e *erasureEncoder) flushStripe() ([]parityChunkOut, error) {
	// Local reference to the backing array so encodeStripe can read it; the
	// defer then nil-s the entries through e.stripeBuf (same array).
	stripe := e.stripeBuf
	parityCount := stripeParityCount(len(stripe), e.overhead)

	defer func() {
		for i := range e.stripeBuf {
			e.stripeBuf[i] = nil
		}
		e.stripeBuf = e.stripeBuf[:0]
	}()

	shards, err := encodeStripe(stripe, parityCount)
	if err != nil {
		return nil, fmt.Errorf("encode stripe at parity offset %d: %w", e.nextParityIdx, err)
	}

	out := make([]parityChunkOut, len(shards))
	for i, data := range shards {
		hash := sdk.Blake3Sum(data)
		out[i] = parityChunkOut{
			chunkIdx: e.nextParityIdx,
			hash:     hash,
			data:     data,
		}
		e.parityHashes = append(e.parityHashes, hash)
		e.paritySizes = append(e.paritySizes, uint32(len(data)))
		e.nextParityIdx++
	}
	return out, nil
}

// reconstructStripe attempts to recover missing data chunks using parity.
// dataShards: data chunks (nil entries are missing). Must be padded to maxSize.
// parityShards: parity chunks (nil entries are missing).
// Returns reconstructed data shards trimmed to their original sizes.
func reconstructStripe(dataShards, parityShards [][]byte, dataSizes []uint32) ([][]byte, error) {
	shards := make([][]byte, len(dataShards)+len(parityShards))
	copy(shards, dataShards)
	copy(shards[len(dataShards):], parityShards)

	enc, err := reedsolomon.New(len(dataShards), len(parityShards))
	if err != nil {
		return nil, fmt.Errorf("create RS decoder: %w", err)
	}

	if err := enc.Reconstruct(shards); err != nil {
		return nil, fmt.Errorf("RS reconstruct: %w", err)
	}

	// Verify reconstruction.
	ok, err := enc.Verify(shards)
	if err != nil {
		return nil, fmt.Errorf("RS verify: %w", err)
	}
	if !ok {
		return nil, fmt.Errorf("RS verify failed after reconstruction")
	}

	// Trim data shards to original sizes.
	result := make([][]byte, len(dataShards))
	for i := range dataShards {
		if int(dataSizes[i]) <= len(shards[i]) {
			result[i] = shards[i][:dataSizes[i]]
		} else {
			result[i] = shards[i]
		}
	}

	return result, nil
}

// --- Erasure manifest wire format ---

// stripeParityCount returns how many parity chunks a stripe of
// stripeDataCount data chunks produces at the given overhead, matching the
// sender's encoder.flushStripe formula exactly. Single source of truth so
// receiver-side reconstruction and sender-side encoding cannot drift. [Batch
// 2b audit round 2]
func stripeParityCount(stripeDataCount int, overhead float64) int {
	p := int(float64(stripeDataCount)*overhead + 0.5)
	if p < 1 {
		p = 1
	}
	return p
}

// overheadToPerMille converts a float erasure overhead (0.10 = 10%) to the
// uint16 per-mille representation carried on the wire. Values outside the
// configured max are clamped to maxParityOverhead.
func overheadToPerMille(overhead float64) uint16 {
	if overhead <= 0 {
		return 0
	}
	if overhead > maxParityOverhead {
		overhead = maxParityOverhead
	}
	v := int(overhead*1000 + 0.5)
	if v > int(uint16(0xFFFF)) {
		v = int(uint16(0xFFFF))
	}
	return uint16(v)
}

// overheadFromPerMille converts the wire per-mille value back to float64.
func overheadFromPerMille(p uint16) float64 {
	return float64(p) / 1000.0
}

// writeErasureManifest appends erasure coding fields to the wire.
// Written only if flagErasureCoded is set.
//
// Wire layout: stripeSize(2) + parityCount(4) + overheadPerMille(2)
//
//   - parityHashes(P*32) + paritySizes(P*4)
//
// OverheadPerMille was introduced in Batch 2b audit round 2. Without it the
// receiver's rsReconstruct had to assume a fixed 0.1 overhead, which silently
// mismapped per-stripe parity offsets when the sender's configured overhead
// differed from the default. [Batch 2b audit]
func writeErasureManifest(w io.Writer, stripeSize, parityCount int, overheadPerMille uint16, parityHashes [][32]byte, paritySizes []uint32) error {
	if parityCount != len(parityHashes) || parityCount != len(paritySizes) {
		return fmt.Errorf("parity count mismatch")
	}

	var header [8]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(stripeSize))
	binary.BigEndian.PutUint32(header[2:6], uint32(parityCount))
	binary.BigEndian.PutUint16(header[6:8], overheadPerMille)
	if _, err := w.Write(header[:]); err != nil {
		return err
	}

	for i := 0; i < parityCount; i++ {
		if _, err := w.Write(parityHashes[i][:]); err != nil {
			return fmt.Errorf("write parity hash %d: %w", i, err)
		}
	}

	sizeBuf := make([]byte, 4)
	for i := 0; i < parityCount; i++ {
		binary.BigEndian.PutUint32(sizeBuf, paritySizes[i])
		if _, err := w.Write(sizeBuf); err != nil {
			return fmt.Errorf("write parity size %d: %w", i, err)
		}
	}

	return nil
}

// readErasureManifest reads erasure coding fields from the wire and enforces
// bounds on every value before the caller touches it.
func readErasureManifest(r io.Reader) (stripeSize int, overheadPerMille uint16, parityHashes [][32]byte, paritySizes []uint32, err error) {
	var header [8]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, 0, nil, nil, fmt.Errorf("read erasure header: %w", err)
	}

	stripeSize = int(binary.BigEndian.Uint16(header[0:2]))
	parityCount := int(binary.BigEndian.Uint32(header[2:6]))
	overheadPerMille = binary.BigEndian.Uint16(header[6:8])

	if parityCount > maxParityCount {
		return 0, 0, nil, nil, fmt.Errorf("parity count too large: %d", parityCount)
	}
	// Upper bound: maxAcceptedStripeSize caps worst-case reconstruction
	// allocation from a malicious sender. The wire-level maxChunkCount cap
	// kept in the error message for diagnostics (nothing above
	// maxAcceptedStripeSize reaches reconstruction).
	if stripeSize < minStripeSize || stripeSize > maxAcceptedStripeSize {
		return 0, 0, nil, nil, fmt.Errorf("invalid stripe size: %d (must be %d..%d)", stripeSize, minStripeSize, maxAcceptedStripeSize)
	}
	// Overhead bounds: must be strictly positive (zero means no erasure,
	// which would never have reached this code path), and capped at
	// maxParityOverhead to block malicious senders from driving huge
	// per-stripe allocations. Upper bound in per-mille:
	// maxParityOverhead * 1000.
	maxPerMille := uint16(maxParityOverhead * 1000)
	if overheadPerMille == 0 || overheadPerMille > maxPerMille {
		return 0, 0, nil, nil, fmt.Errorf("invalid erasure overhead per-mille: %d (must be 1..%d)", overheadPerMille, maxPerMille)
	}

	parityHashes = make([][32]byte, parityCount)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, parityHashes[i][:]); err != nil {
			return 0, 0, nil, nil, fmt.Errorf("read parity hash %d: %w", i, err)
		}
	}

	paritySizes = make([]uint32, parityCount)
	sizeBuf := make([]byte, 4)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, sizeBuf); err != nil {
			return 0, 0, nil, nil, fmt.Errorf("read parity size %d: %w", i, err)
		}
		paritySizes[i] = binary.BigEndian.Uint32(sizeBuf)
	}

	return stripeSize, overheadPerMille, parityHashes, paritySizes, nil
}

// --- RS reconstruction ---

// rsReconstruct recovers data chunks that failed hash verification during
// receive by combining the intact stripe-mates (read from on-disk tmpFiles)
// with RS parity (accumulated in state.parityData) and decoding. Writes the
// reconstructed bytes back via writeChunkGlobal after verifying the BLAKE3
// hash matches the sender's claimed hash (already stored in state.hashes via
// recordChunk before the original hash check failed).
//
// Single-pass contract [B2-F36]: runs exactly once per transfer at trailer
// time. Either every corrupted chunk is recovered or the whole transfer fails;
// there is no retry loop, so a malicious sender cannot force repeated CPU
// spending by crafting stripe boundaries that almost-but-not-quite decode.
//
// Scope [Batch 2b]: recovers only chunks whose frames arrived (size + claimed
// hash live in state.sizes / state.hashes). Truly missing chunks, whose frames
// never arrived, are unrecoverable without per-chunk size metadata in the
// trailer; the existing missingChunks check in receiveParallel catches those
// before this function is called.
//
// Selective rejection [Batch 2b]: a stripe whose layout needs sizes for
// selectively-rejected chunks (sizes missing from state.sizes) cannot be
// reconstructed; the function fails fast with a clear error. Adding size to
// the sparse trailer is a future protocol change, not part of Batch 2b.
//
// overhead is the sender's configured erasure overhead carried over the wire
// in the erasure trailer (overheadFromPerMille). Using the sender's value
// instead of a hardcoded 0.1 is what makes reconstruction correct for
// non-default configurations (erasure_overhead: 0.15, 0.20, ...); a
// receiver-side hardcode would mismap per-stripe parity offsets whenever the
// sender's per-stripe parity count differs from floor(stripeSize*0.1+0.5).
// [Batch 2b audit round 2]
func (ts *TransferService) rsReconstruct(state *streamReceiveState, chunkCount int, stripeSize int, overhead float64, corruptedIndices []int) error {
	if stripeSize < minStripeSize {
		return fmt.Errorf("invalid stripe size: %d (must be >= %d)", stripeSize, minStripeSize)
	}
	if chunkCount <= 0 {
		return fmt.Errorf("invalid chunk count: %d", chunkCount)
	}
	if overhead <= 0 || overhead > maxParityOverhead {
		return fmt.Errorf("invalid erasure overhead: %f (must be in (0, %f])", overhead, maxParityOverhead)
	}
	if len(corruptedIndices) == 0 {
		return nil
	}

	// Group corrupted indices by stripe. Indices outside [0, chunkCount) are
	// silently dropped rather than aborting reconstruction: a malicious
	// sender can flood corrupt frames with chunkIdx above the trailer's
	// declared chunkCount (the per-frame upper bound is maxChunkCount, not
	// the transfer's declared total). Their hashes never enter Merkle
	// (assembleFullHashList iterates only 0..chunkCount), their bytes are
	// never written to tmp files (writeChunkGlobal C3/C4 bounds), so there
	// is nothing meaningful to reconstruct for them — and letting an
	// attacker kill recovery by injecting one out-of-range corrupted frame
	// would trade a real recovery for a fabricated one. [Batch 2b audit r3]
	corruptedByStripe := make(map[int][]int)
	droppedOutOfRange := 0
	for _, idx := range corruptedIndices {
		if idx < 0 || idx >= chunkCount {
			droppedOutOfRange++
			continue
		}
		stripeIdx := idx / stripeSize
		corruptedByStripe[stripeIdx] = append(corruptedByStripe[stripeIdx], idx)
	}
	if droppedOutOfRange > 0 {
		slog.Debug("file-transfer: ignoring out-of-range corrupted indices", "count", droppedOutOfRange, "chunkCount", chunkCount)
	}
	if len(corruptedByStripe) == 0 {
		// Every flagged corruption was out of range; nothing to do.
		return nil
	}

	// Precompute global offsets from state.sizes. Requires every data chunk's
	// size to be known on the receiver. Fails fast otherwise (see selective
	// rejection note above).
	state.mu.Lock()
	offsets := make([]int64, chunkCount+1)
	for i := 0; i < chunkCount; i++ {
		sz, ok := state.sizes[i]
		if !ok {
			state.mu.Unlock()
			return fmt.Errorf("chunk %d size unknown on receiver (selective rejection with erasure is unsupported in this stripe)", i)
		}
		offsets[i+1] = offsets[i] + int64(sz)
	}
	// Snapshot chunk sizes (full list) so stripe loop doesn't re-lock.
	chunkSizes := make([]uint32, chunkCount)
	for i := 0; i < chunkCount; i++ {
		chunkSizes[i] = state.sizes[i]
	}
	state.mu.Unlock()

	numStripes := (chunkCount + stripeSize - 1) / stripeSize
	parityOffset := 0

	for s := 0; s < numStripes; s++ {
		start := s * stripeSize
		end := start + stripeSize
		if end > chunkCount {
			end = chunkCount
		}
		stripeDataCount := end - start

		// Per-stripe parity count derived through stripeParityCount — the
		// single source of truth that sender's encoder.flushStripe shares.
		parityCount := stripeParityCount(stripeDataCount, overhead)

		corruptedInStripe := corruptedByStripe[s]
		if len(corruptedInStripe) == 0 {
			parityOffset += parityCount
			continue
		}
		if len(corruptedInStripe) > parityCount {
			return fmt.Errorf("stripe %d: %d corrupted chunks exceed %d parity (unrecoverable)",
				s, len(corruptedInStripe), parityCount)
		}

		corruptedSet := make(map[int]bool, len(corruptedInStripe))
		for _, idx := range corruptedInStripe {
			corruptedSet[idx] = true
		}

		// Max chunk size across the stripe = RS shard size (padding target).
		maxSize := 0
		for i := 0; i < stripeDataCount; i++ {
			if int(chunkSizes[start+i]) > maxSize {
				maxSize = int(chunkSizes[start+i])
			}
		}

		// Read intact stripe-mates from tmpFiles. Corrupted chunks contribute
		// nil so RS knows to reconstruct them. readChunkGlobal returns an
		// error for chunks touching rejected files; we also treat those as
		// nil shards — the stripe then has more "unknowns" to reconstruct,
		// which is bounded by the parity count check above.
		dataShards := make([][]byte, stripeDataCount)
		for i := 0; i < stripeDataCount; i++ {
			globalIdx := start + i
			if corruptedSet[globalIdx] {
				continue
			}
			size := int(chunkSizes[globalIdx])
			chunkBytes, err := state.readChunkGlobal(offsets[globalIdx], size)
			if err != nil {
				// Cannot read this stripe-mate (rejected file slice). Count it
				// as missing; decoder will need another parity slot.
				continue
			}
			if len(chunkBytes) == maxSize {
				dataShards[i] = chunkBytes
			} else {
				padded := make([]byte, maxSize)
				copy(padded, chunkBytes)
				dataShards[i] = padded
			}
		}

		// Count nil data shards (corrupted + unreadable) against parity budget.
		nilData := 0
		for _, d := range dataShards {
			if d == nil {
				nilData++
			}
		}
		if nilData > parityCount {
			return fmt.Errorf("stripe %d: %d data shards unavailable (corrupted=%d, unreadable=%d) exceed %d parity",
				s, nilData, len(corruptedInStripe), nilData-len(corruptedInStripe), parityCount)
		}

		// Load parity shards for this stripe. A missing parity is nil; RS
		// decoder tolerates missing shards as long as data+parity losses
		// together fit within the parity budget.
		parityShards := make([][]byte, parityCount)
		state.mu.Lock()
		for p := 0; p < parityCount; p++ {
			globalParityIdx := parityOffset + p
			data, ok := state.parityData[globalParityIdx]
			if !ok {
				continue
			}
			if len(data) == maxSize {
				parityShards[p] = data
			} else if len(data) < maxSize {
				padded := make([]byte, maxSize)
				copy(padded, data)
				parityShards[p] = padded
			} else {
				// Parity larger than the stripe's maxSize means the sender's
				// stripe layout differs from the receiver's view — almost
				// always a protocol violation. Fail rather than silently
				// truncate (which would corrupt RS math).
				state.mu.Unlock()
				return fmt.Errorf("stripe %d: parity chunk %d size %d exceeds stripe maxSize %d",
					s, globalParityIdx, len(data), maxSize)
			}
		}
		state.mu.Unlock()

		// Count total missing shards (data nil + parity nil) against parity budget.
		nilParity := 0
		for _, p := range parityShards {
			if p == nil {
				nilParity++
			}
		}
		if nilData+nilParity > parityCount {
			return fmt.Errorf("stripe %d: total missing shards %d (data=%d, parity=%d) exceed %d parity",
				s, nilData+nilParity, nilData, nilParity, parityCount)
		}

		dataSizesStripe := chunkSizes[start:end]
		reconstructed, err := reconstructStripe(dataShards, parityShards, dataSizesStripe)
		if err != nil {
			return fmt.Errorf("stripe %d reconstruct: %w", s, err)
		}

		// Verify each reconstructed corrupted chunk against the claimed hash
		// stored by recordChunk, then write the bytes back via
		// writeChunkGlobal. The fileIdx argument to writeChunkGlobal is only
		// used for a bounds check (< len(s.files)); the actual file routing
		// happens via globalToLocal inside the function. Passing 0 is safe as
		// long as len(s.files) > 0, which holds for any non-empty transfer
		// (empty transfers never set flagErasureCoded per B2-F3).
		for _, idx := range corruptedInStripe {
			localIdx := idx - start
			chunk := reconstructed[localIdx]

			computed := sdk.Blake3Sum(chunk)
			state.mu.Lock()
			claimed := state.hashes[idx]
			state.mu.Unlock()
			if computed != claimed {
				return fmt.Errorf("chunk %d reconstructed hash does not match claimed hash (RS recovered wrong bytes)", idx)
			}

			if err := state.writeChunkGlobal(0, offsets[idx], len(chunk), chunk); err != nil {
				return fmt.Errorf("write reconstructed chunk %d: %w", idx, err)
			}

			// [Batch 2b self-audit] Clear the index from state.corruptedChunks
			// now that bytes are on disk. If rsReconstruct later errors on a
			// subsequent stripe, saveCheckpointOnError calls checkpointFromState
			// which clears have-bits for every index still in corruptedChunks —
			// without this removal, a successfully-reconstructed chunk would be
			// redundantly retransmitted on the next resume.
			state.mu.Lock()
			delete(state.corruptedChunks, idx)
			state.mu.Unlock()
		}

		parityOffset += parityCount
	}

	return nil
}
