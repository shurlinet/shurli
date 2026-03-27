package sdk

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"

	"github.com/klauspost/reedsolomon"
)

// Erasure coding constants.
const (
	// flagErasureCoded indicates the manifest includes parity chunks.
	flagErasureCoded = 0x02

	// defaultStripeSize is the number of data chunks per RS stripe.
	// 100 data + 10 parity (at 10%) = 110 total, well within RS limits.
	defaultStripeSize = 100

	// maxParityOverhead caps erasure overhead at 50%.
	maxParityOverhead = 0.50

	// maxParityCount limits total parity chunks.
	maxParityCount = maxChunkCount / 2
)

// parityChunk holds a generated parity shard with its BLAKE3 hash.
type parityChunk struct {
	hash [32]byte
	data []byte // raw parity (uncompressed, full shard size)
}

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
		n := end - off
		p := int(float64(n)*overhead + 0.5) // round
		if p < 1 {
			p = 1
		}
		totalParity += p
	}

	return erasureParams{
		StripeSize:  stripeSize,
		ParityCount: totalParity,
	}
}

// encodeErasure generates parity chunks for all data chunks.
// Data chunks are grouped into stripes of stripeSize. Each stripe produces
// ceil(stripeSize * overhead) parity shards via Reed-Solomon.
// Returns parity chunks in stripe order with their BLAKE3 hashes.
func encodeErasure(dataChunks [][]byte, stripeSize int, overhead float64) ([]parityChunk, error) {
	if len(dataChunks) == 0 || overhead <= 0 {
		return nil, nil
	}

	var allParity []parityChunk

	for off := 0; off < len(dataChunks); off += stripeSize {
		end := off + stripeSize
		if end > len(dataChunks) {
			end = len(dataChunks)
		}
		stripe := dataChunks[off:end]

		parityCount := int(float64(len(stripe))*overhead + 0.5)
		if parityCount < 1 {
			parityCount = 1
		}

		parity, err := encodeStripe(stripe, parityCount)
		if err != nil {
			return nil, fmt.Errorf("encode stripe at offset %d: %w", off, err)
		}

		for _, p := range parity {
			hash := blake3Sum(p)
			allParity = append(allParity, parityChunk{hash: hash, data: p})
		}
	}

	return allParity, nil
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

// writeErasureManifest appends erasure coding fields to the wire.
// Written only if flagErasureCoded is set.
//
// Wire layout: stripeSize(2) + parityCount(4) + parityHashes(P*32) + paritySizes(P*4)
func writeErasureManifest(w io.Writer, stripeSize, parityCount int, parityHashes [][32]byte, paritySizes []uint32) error {
	if parityCount != len(parityHashes) || parityCount != len(paritySizes) {
		return fmt.Errorf("parity count mismatch")
	}

	var header [6]byte
	binary.BigEndian.PutUint16(header[0:2], uint16(stripeSize))
	binary.BigEndian.PutUint32(header[2:6], uint32(parityCount))
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

// readErasureManifest reads erasure coding fields from the wire.
func readErasureManifest(r io.Reader) (stripeSize int, parityHashes [][32]byte, paritySizes []uint32, err error) {
	var header [6]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return 0, nil, nil, fmt.Errorf("read erasure header: %w", err)
	}

	stripeSize = int(binary.BigEndian.Uint16(header[0:2]))
	parityCount := int(binary.BigEndian.Uint32(header[2:6]))

	if parityCount > maxParityCount {
		return 0, nil, nil, fmt.Errorf("parity count too large: %d", parityCount)
	}
	if stripeSize <= 0 || stripeSize > maxChunkCount {
		return 0, nil, nil, fmt.Errorf("invalid stripe size: %d", stripeSize)
	}

	parityHashes = make([][32]byte, parityCount)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, parityHashes[i][:]); err != nil {
			return 0, nil, nil, fmt.Errorf("read parity hash %d: %w", i, err)
		}
	}

	paritySizes = make([]uint32, parityCount)
	sizeBuf := make([]byte, 4)
	for i := 0; i < parityCount; i++ {
		if _, err := io.ReadFull(r, sizeBuf); err != nil {
			return 0, nil, nil, fmt.Errorf("read parity size %d: %w", i, err)
		}
		paritySizes[i] = binary.BigEndian.Uint32(sizeBuf)
	}

	return stripeSize, parityHashes, paritySizes, nil
}

// --- RS reconstruction ---

// rsReconstruct recovers corrupted data chunks using parity, then writes them to the file.
func (ts *TransferService) rsReconstruct(tmpFile *os.File, m *transferManifest, offsets []int64, corrupted []int, parityData map[int][]byte) error {
	if m.StripeSize <= 0 {
		return fmt.Errorf("invalid stripe size: %d", m.StripeSize)
	}

	// Group corrupted indices by stripe.
	corruptedByStripe := make(map[int][]int) // stripe index -> corrupted chunk indices
	for _, idx := range corrupted {
		stripeIdx := idx / m.StripeSize
		corruptedByStripe[stripeIdx] = append(corruptedByStripe[stripeIdx], idx)
	}

	// Process each affected stripe.
	parityOffset := 0 // running parity index offset
	for s := 0; s < (m.ChunkCount+m.StripeSize-1)/m.StripeSize; s++ {
		start := s * m.StripeSize
		end := start + m.StripeSize
		if end > m.ChunkCount {
			end = m.ChunkCount
		}
		stripeDataCount := end - start
		parityCount := int(float64(stripeDataCount)*0.1 + 0.5)
		if parityCount < 1 {
			parityCount = 1
		}

		corruptedInStripe := corruptedByStripe[s]
		if len(corruptedInStripe) == 0 {
			parityOffset += parityCount
			continue
		}

		if len(corruptedInStripe) > parityCount {
			return fmt.Errorf("stripe %d: %d corrupted chunks but only %d parity available",
				s, len(corruptedInStripe), parityCount)
		}

		// Find max chunk size in this stripe (shard size for RS).
		maxSize := 0
		for i := start; i < end; i++ {
			if int(m.ChunkSizes[i]) > maxSize {
				maxSize = int(m.ChunkSizes[i])
			}
		}

		// Build corrupted set for fast lookup.
		corruptedSet := make(map[int]bool, len(corruptedInStripe))
		for _, idx := range corruptedInStripe {
			corruptedSet[idx] = true
		}

		// Read data shards from file (pad to maxSize). Nil for corrupted.
		dataShards := make([][]byte, stripeDataCount)
		for i := start; i < end; i++ {
			if corruptedSet[i] {
				dataShards[i-start] = nil
				continue
			}
			buf := make([]byte, maxSize)
			n, err := tmpFile.ReadAt(buf[:m.ChunkSizes[i]], offsets[i])
			if err != nil {
				return fmt.Errorf("read data chunk %d for reconstruction: %w", i, err)
			}
			_ = n
			dataShards[i-start] = buf
		}

		// Collect parity shards for this stripe.
		parityShards := make([][]byte, parityCount)
		for p := 0; p < parityCount; p++ {
			globalParityIdx := parityOffset + p
			if data, ok := parityData[globalParityIdx]; ok {
				// Pad parity to maxSize if needed.
				if len(data) < maxSize {
					padded := make([]byte, maxSize)
					copy(padded, data)
					parityShards[p] = padded
				} else {
					parityShards[p] = data[:maxSize]
				}
			}
			// nil if missing
		}

		// Reconstruct.
		dataSizes := m.ChunkSizes[start:end]
		reconstructed, err := reconstructStripe(dataShards, parityShards, dataSizes)
		if err != nil {
			return fmt.Errorf("stripe %d reconstruction: %w", s, err)
		}

		// Verify and write reconstructed chunks.
		for _, idx := range corruptedInStripe {
			localIdx := idx - start
			chunk := reconstructed[localIdx]

			// Verify BLAKE3 hash of reconstructed data.
			hash := blake3Sum(chunk)
			if hash != m.ChunkHashes[idx] {
				return fmt.Errorf("chunk %d hash mismatch after reconstruction", idx)
			}

			// Write to file at correct offset.
			if _, err := tmpFile.WriteAt(chunk, offsets[idx]); err != nil {
				return fmt.Errorf("write reconstructed chunk %d: %w", idx, err)
			}
		}

		parityOffset += parityCount
	}

	return nil
}
