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

// erasureHeaderParams carries the stripe configuration written into the SHFT
// header when flagErasureCoded is set (Option C). The receiver uses these to
// initialize per-stripe parity tracking and eager reconstruction before any
// chunks arrive. Previously these fields lived only in the trailer, which
// forced the receiver to buffer ALL parity in memory until trailer time.
// [OC-F16, OC-F33]
type erasureHeaderParams struct {
	StripeSize       int    // data chunks per stripe (max; last stripe may be partial)
	OverheadPerMille uint16 // erasure overhead in per-mille (100 = 10%)
}

// paritySlot holds parity data for a single RS stripe on the receiver.
// Bounded by the dynamic inflight cap (maxInflightStripes). Created when the
// first parity chunk for a stripe arrives, freed after eager reconstruction
// or trailer sweep. [Option C, OC-F53 decoupled tracking]
type paritySlot struct {
	parity         map[int][]byte // local parity index -> raw data
	bytes          int64          // total parity bytes in this slot
	reconstructing bool           // OC-F18: prevents double reconstruction
	done           bool           // OC-F19: eagerly reconstructed, drop late parity
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
		ParityCount:  len(e.parityHashes),
		ParityHashes: e.parityHashes,
		ParitySizes:  e.paritySizes,
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

// writeErasureManifest appends erasure coding fields to the trailer wire.
// Written only if flagErasureCoded is set.
//
// Wire layout (Option C): parityCount(4) + parityHashes(P*32) + paritySizes(P*4)
//
// stripeSize and overheadPerMille moved to the SHFT header (Option C, OC-F28)
// so the receiver can initialize per-stripe parity tracking before any chunks
// arrive. The trailer retains only the parity count + per-parity metadata
// needed for Merkle verification and reconstruction.
func writeErasureManifest(w io.Writer, parityCount int, parityHashes [][32]byte, paritySizes []uint32) error {
	if parityCount != len(parityHashes) || parityCount != len(paritySizes) {
		return fmt.Errorf("parity count mismatch")
	}

	var header [4]byte
	binary.BigEndian.PutUint32(header[0:4], uint32(parityCount))
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

// readErasureManifest reads erasure coding fields from the trailer wire.
// stripeSize and overheadPerMille are now in the header (Option C, OC-F28);
// the trailer carries only parityCount + per-parity hashes and sizes.
//
// Wire layout (Option C): parityCount(4) + parityHashes(P*32) + paritySizes(P*4)
func readErasureManifest(r io.Reader) (parityHashes [][32]byte, paritySizes []uint32, err error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, nil, fmt.Errorf("read erasure header: %w", err)
	}

	parityCount := int(binary.BigEndian.Uint32(header[0:4]))
	if parityCount > maxParityCount {
		return nil, nil, fmt.Errorf("parity count too large: %d", parityCount)
	}

	parityHashes = make([][32]byte, parityCount)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, parityHashes[i][:]); err != nil {
			return nil, nil, fmt.Errorf("read parity hash %d: %w", i, err)
		}
	}

	paritySizes = make([]uint32, parityCount)
	sizeBuf := make([]byte, 4)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, sizeBuf); err != nil {
			return nil, nil, fmt.Errorf("read parity size %d: %w", i, err)
		}
		paritySizes[i] = binary.BigEndian.Uint32(sizeBuf)
	}

	return parityHashes, paritySizes, nil
}

// --- Chunk manifest wire format (Batch 2c) ---

// writeChunkManifest writes per-data-chunk hashes and decompressed sizes to
// the trailer wire. Enables receivers to recover truly missing chunks (frame
// never arrived) via RS reconstruction. Written between the sparse-hashes
// section and the parity section when erasure is active.
//
// Wire layout: count(4) + [hash(32) + decompSize(4)] * count
//
// When hashes is nil, writes count=0 (no-op manifest). This allows existing
// tests to pass nil ChunkHashes without breaking the wire format.
func writeChunkManifest(w io.Writer, hashes [][32]byte, sizes []uint32) error {
	count := len(hashes)
	if count == 0 {
		var zero [4]byte
		_, err := w.Write(zero[:])
		return err
	}
	if len(sizes) != count {
		return fmt.Errorf("chunk manifest: hash count %d != size count %d", count, len(sizes))
	}

	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(count))
	if _, err := w.Write(countBuf[:]); err != nil {
		return fmt.Errorf("write chunk manifest count: %w", err)
	}

	var entryBuf [36]byte // hash(32) + decompSize(4)
	for i := 0; i < count; i++ {
		copy(entryBuf[0:32], hashes[i][:])
		binary.BigEndian.PutUint32(entryBuf[32:36], sizes[i])
		if _, err := w.Write(entryBuf[:]); err != nil {
			return fmt.Errorf("write chunk manifest entry %d: %w", i, err)
		}
	}

	return nil
}

// readChunkManifest reads per-data-chunk hashes and decompressed sizes from
// the trailer wire. expectedCount is the trailer's chunkCount; a mismatch
// (except count=0 for legacy/no-manifest trailers) is rejected. Per-entry
// decompSize is validated against maxDecompressedChunk.
//
// Returns nil, nil, nil when count=0 (no manifest present).
func readChunkManifest(r io.Reader, expectedCount int) ([][32]byte, []uint32, error) {
	var countBuf [4]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, nil, fmt.Errorf("read chunk manifest count: %w", err)
	}
	count := int(binary.BigEndian.Uint32(countBuf[:]))

	if count == 0 {
		return nil, nil, nil
	}
	if count > maxChunkCount {
		return nil, nil, fmt.Errorf("chunk manifest count too large: %d (max %d)", count, maxChunkCount)
	}
	if count != expectedCount {
		return nil, nil, fmt.Errorf("chunk manifest count %d does not match trailer chunkCount %d", count, expectedCount)
	}

	hashes := make([][32]byte, count)
	sizes := make([]uint32, count)
	var entryBuf [36]byte
	for i := 0; i < count; i++ {
		if _, err := io.ReadFull(r, entryBuf[:]); err != nil {
			return nil, nil, fmt.Errorf("read chunk manifest entry %d: %w", i, err)
		}
		copy(hashes[i][:], entryBuf[0:32])
		sz := binary.BigEndian.Uint32(entryBuf[32:36])
		if sz == 0 || sz > maxDecompressedChunk {
			return nil, nil, fmt.Errorf("chunk manifest entry %d: invalid decompSize %d (must be 1..%d)", i, sz, maxDecompressedChunk)
		}
		sizes[i] = sz
	}

	return hashes, sizes, nil
}

// --- RS reconstruction ---

// reconstructSingleStripe recovers corrupted data chunks in one RS stripe
// using parity from a paritySlot. Reads intact stripe-mates from tmpFiles,
// combines with parity, decodes via ReconstructSome (R4: only rebuilds
// needed shards), verifies BLAKE3 hashes, and writes recovered bytes back
// to tmpFiles. Used by both eager mid-transfer reconstruction and trailer-
// time sweep. [Option C, OC-F29]
//
// enc is a reusable RS encoder for full stripes (R5). Pass nil for partial
// stripes or when no reusable encoder is available; a new encoder will be
// created.
//
// Returns nil if no corrupted chunks exist in this stripe.
func (s *streamReceiveState) reconstructSingleStripe(
	stripeIdx, stripeStart, stripeEnd int,
	slot *paritySlot,
	enc reedsolomon.Encoder,
) error {
	stripeDataCount := stripeEnd - stripeStart

	// Use the actual parity count from the slot (what the sender delivered),
	// not a recomputation from overhead. The sender's encoder determines the
	// parity count; overhead is used for stripe layout but the slot holds
	// the authoritative parity shard set for reconstruction.
	s.mu.Lock()
	parityCount := len(slot.parity)
	s.mu.Unlock()
	if parityCount == 0 {
		return fmt.Errorf("stripe %d: no parity shards available", stripeIdx)
	}

	// Collect corrupted indices in this stripe.
	s.mu.Lock()
	var corruptedInStripe []int
	for idx := range s.corruptedChunks {
		if idx >= stripeStart && idx < stripeEnd {
			corruptedInStripe = append(corruptedInStripe, idx)
		}
	}
	s.mu.Unlock()

	if len(corruptedInStripe) == 0 {
		return nil
	}
	if len(corruptedInStripe) > parityCount {
		return fmt.Errorf("stripe %d: %d corrupted chunks exceed %d parity (unrecoverable)",
			stripeIdx, len(corruptedInStripe), parityCount)
	}

	corruptedSet := make(map[int]bool, len(corruptedInStripe))
	for _, idx := range corruptedInStripe {
		corruptedSet[idx] = true
	}

	// Snapshot sizes for the stripe range under lock.
	s.mu.Lock()
	chunkSizes := make([]uint32, stripeDataCount)
	for i := 0; i < stripeDataCount; i++ {
		sz, ok := s.sizes[stripeStart+i]
		if !ok {
			s.mu.Unlock()
			return fmt.Errorf("chunk %d size unknown (selective rejection with erasure unsupported)", stripeStart+i)
		}
		chunkSizes[i] = sz
	}
	s.mu.Unlock()

	// Compute global byte offsets for chunks in [stripeStart, stripeEnd).
	// Requires sizes of all preceding chunks. [OC-F50]
	s.mu.Lock()
	baseOffset := int64(0)
	for i := 0; i < stripeStart; i++ {
		baseOffset += int64(s.sizes[i])
	}
	s.mu.Unlock()
	offsets := make([]int64, stripeDataCount)
	off := baseOffset
	for i := 0; i < stripeDataCount; i++ {
		offsets[i] = off
		off += int64(chunkSizes[i])
	}

	// Max chunk size across the stripe = RS shard size (padding target).
	maxSize := 0
	for _, sz := range chunkSizes {
		if int(sz) > maxSize {
			maxSize = int(sz)
		}
	}

	// Read intact stripe-mates from tmpFiles. Corrupted chunks are nil.
	dataShards := make([][]byte, stripeDataCount)
	for i := 0; i < stripeDataCount; i++ {
		globalIdx := stripeStart + i
		if corruptedSet[globalIdx] {
			continue
		}
		size := int(chunkSizes[i])
		chunkBytes, err := s.readChunkGlobal(offsets[i], size)
		if err != nil {
			continue // unreadable = nil shard, decoder needs more parity
		}
		if len(chunkBytes) == maxSize {
			dataShards[i] = chunkBytes
		} else {
			padded := make([]byte, maxSize)
			copy(padded, chunkBytes)
			dataShards[i] = padded
		}
	}

	// Count nil data shards against parity budget.
	nilData := 0
	for _, d := range dataShards {
		if d == nil {
			nilData++
		}
	}
	if nilData > parityCount {
		return fmt.Errorf("stripe %d: %d data shards unavailable exceed %d parity",
			stripeIdx, nilData, parityCount)
	}

	// Load parity shards from slot.
	parityShards := make([][]byte, parityCount)
	s.mu.Lock()
	for p := 0; p < parityCount; p++ {
		data, ok := slot.parity[p]
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
			s.mu.Unlock()
			return fmt.Errorf("stripe %d: parity %d size %d exceeds maxSize %d",
				stripeIdx, p, len(data), maxSize)
		}
	}
	s.mu.Unlock()

	// Count total missing shards against parity budget.
	nilParity := 0
	for _, p := range parityShards {
		if p == nil {
			nilParity++
		}
	}
	if nilData+nilParity > parityCount {
		return fmt.Errorf("stripe %d: total missing %d (data=%d, parity=%d) exceed %d parity",
			stripeIdx, nilData+nilParity, nilData, nilParity, parityCount)
	}

	// Build the combined shard array for RS.
	shards := make([][]byte, stripeDataCount+parityCount)
	copy(shards, dataShards)
	copy(shards[stripeDataCount:], parityShards)

	// Create or reuse encoder. [R5: encoder reuse for full stripes]
	// Only reuse when shard counts match exactly — partial stripes and
	// stripes with extra parity need a fresh encoder.
	if enc != nil && (stripeDataCount != s.stripeSize || parityCount != s.parityPerFullStripe) {
		enc = nil // mismatch, create fresh
	}
	if enc == nil {
		var err error
		enc, err = reedsolomon.New(stripeDataCount, parityCount)
		if err != nil {
			return fmt.Errorf("stripe %d: create RS decoder: %w", stripeIdx, err)
		}
	}

	// ReconstructSome: only rebuild corrupted data shards. [R4]
	required := make([]bool, len(shards))
	for _, idx := range corruptedInStripe {
		required[idx-stripeStart] = true
	}
	if err := enc.ReconstructSome(shards, required); err != nil {
		return fmt.Errorf("stripe %d: RS reconstruct: %w", stripeIdx, err)
	}

	// Verify reconstruction.
	ok, err := enc.Verify(shards)
	if err != nil {
		return fmt.Errorf("stripe %d: RS verify: %w", stripeIdx, err)
	}
	if !ok {
		return fmt.Errorf("stripe %d: RS verify failed after reconstruction", stripeIdx)
	}

	// Verify each reconstructed chunk hash and write to disk.
	for _, idx := range corruptedInStripe {
		localIdx := idx - stripeStart
		chunk := shards[localIdx]
		if int(chunkSizes[localIdx]) < len(chunk) {
			chunk = chunk[:chunkSizes[localIdx]]
		}

		computed := sdk.Blake3Sum(chunk)
		s.mu.Lock()
		claimed := s.hashes[idx]
		s.mu.Unlock()
		if computed != claimed {
			return fmt.Errorf("chunk %d reconstructed hash mismatch (RS recovered wrong bytes)", idx)
		}

		if err := s.writeChunkGlobal(0, offsets[localIdx], len(chunk), chunk); err != nil {
			return fmt.Errorf("write reconstructed chunk %d: %w", idx, err)
		}

		// Clear from corruptedChunks and mark as received so checkpoint
		// doesn't force retransmission. For Batch 2b corrupted chunks the
		// bit was already set by recordChunk (no-op). For Batch 2c missing
		// chunks populated from the trailer manifest the bit was never set;
		// without this, TS-5b retry and checkpoint resume would retransmit
		// chunks that are already correct on disk. [R6-F1]
		s.mu.Lock()
		delete(s.corruptedChunks, idx)
		if s.receivedBitfield != nil {
			s.receivedBitfield.set(idx)
		}
		s.mu.Unlock()
	}

	return nil
}

// rsReconstruct recovers data chunks that failed hash verification during
// receive. Sweeps all stripes, skipping those already handled by eager
// per-stripe reconstruction (slot.done). Used at trailer time for the last
// partial stripe and any full stripes that weren't eagerly reconstructed.
// [Option C refactor of Batch 2b rsReconstruct]
//
// overhead is the sender's configured erasure overhead from the header
// (overheadFromPerMille). [Batch 2b audit round 2]
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

	// Filter out-of-range indices. [Batch 2b audit r3]
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
		return nil
	}

	numStripes := (chunkCount + stripeSize - 1) / stripeSize

	for s := 0; s < numStripes; s++ {
		if _, hasCorrruption := corruptedByStripe[s]; !hasCorrruption {
			continue
		}

		start := s * stripeSize
		end := start + stripeSize
		if end > chunkCount {
			end = chunkCount
		}

		// Skip stripes already handled by eager reconstruction. [OC-F19]
		state.mu.Lock()
		slot := state.paritySlots[s]
		if slot != nil && slot.done {
			state.mu.Unlock()
			continue
		}
		// If no slot exists (no parity arrived for this stripe), fail.
		if slot == nil {
			state.mu.Unlock()
			return fmt.Errorf("stripe %d: no parity data available for reconstruction", s)
		}
		state.mu.Unlock()

		// Use reusable encoder for full stripes. [R5]
		var enc reedsolomon.Encoder
		if end-start == stripeSize {
			state.mu.Lock()
			enc = state.rsFullStripeEnc
			state.mu.Unlock()
		}

		if err := state.reconstructSingleStripe(s, start, end, slot, enc); err != nil {
			return err
		}

		// Mark stripe done and free parity. [OC-F13]
		state.mu.Lock()
		state.totalParityBytes -= slot.bytes
		slot.parity = nil
		slot.bytes = 0
		slot.done = true
		state.mu.Unlock()
	}

	return nil
}
