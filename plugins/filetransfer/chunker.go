package filetransfer

import (
	"io"

	"github.com/zeebo/blake3"
)

// FastCDC content-defined chunking with BLAKE3 hashing.
// Single-pass: chunks and hashes simultaneously.
//
// Algorithm: gear-hash rolling window with three cut points (min, avg, max).
// Gear table maps each byte to a 64-bit pseudorandom value, creating a
// position-dependent hash that's cheap to update incrementally.
//
// References:
//   - Wen Xia et al., "FastCDC: a Fast and Efficient Content-Defined Chunking
//     Approach for Data Deduplication" (USENIX ATC 2016)

// ChunkTarget selects adaptive chunk sizes based on file size.
// Smaller files get smaller chunks for better dedup granularity.
// Larger files get bigger chunks to reduce manifest overhead.
func ChunkTarget(fileSize int64) (minSize, avgSize, maxSize int) {
	switch {
	case fileSize < 250<<20: // < 250 MB
		return 64 << 10, 128 << 10, 256 << 10 // 64K / 128K / 256K
	case fileSize < 1<<30: // < 1 GB
		return 128 << 10, 256 << 10, 512 << 10 // 128K / 256K / 512K
	case fileSize < 4<<30: // < 4 GB
		return 256 << 10, 512 << 10, 1 << 20 // 256K / 512K / 1M
	default: // 4 GB+
		return 512 << 10, 1 << 20, 2 << 20 // 512K / 1M / 2M
	}
}

// Chunk represents a single content-defined chunk with its BLAKE3 hash.
type Chunk struct {
	Data   []byte   // chunk content
	Hash   [32]byte // BLAKE3-256 hash
	Offset int64    // byte offset in original file
}

// ChunkReader reads from r and produces content-defined chunks.
// Each chunk is hashed with BLAKE3. Returns io.EOF sentinel when done.
//
// The callback is called for each chunk. If it returns an error, chunking stops.
func ChunkReader(r io.Reader, fileSize int64, cb func(Chunk) error) error {
	minSize, avgSize, maxSize := ChunkTarget(fileSize)

	// Masks for gear-hash cut-point detection.
	// Fewer bits = easier to match = more frequent cuts.
	// avgMask targets the average chunk size; largeMask is more permissive
	// (used after avgSize to prevent chunks from growing too large).
	avgBits := bitsForSize(avgSize)
	avgMask := uint64(1<<avgBits - 1)
	largeMask := uint64(1<<(avgBits-2) - 1) // 2 fewer bits = 4x more likely to cut

	buf := make([]byte, maxSize)
	var offset int64
	carry := 0 // leftover bytes from previous read

	for {
		// Fill buffer.
		n, readErr := io.ReadAtLeast(r, buf[carry:], 1)
		n += carry
		carry = 0

		if n == 0 {
			if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
				return nil
			}
			return readErr
		}

		pos := 0
		for pos < n {
			remaining := n - pos

			// If the remaining data could be a final partial read,
			// and we haven't reached EOF yet, save it as carry.
			if remaining < minSize && readErr == nil {
				copy(buf, buf[pos:n])
				carry = remaining
				break
			}

			// Determine cut point in buf[pos:pos+remaining].
			end := pos + remaining
			if end-pos > maxSize {
				end = pos + maxSize
			}

			cutAt := pos + minSize // skip minimum
			if cutAt > end {
				cutAt = end
			}

			var fp uint64
			// Phase 1: scan up to avgSize with strict mask.
			limit := pos + avgSize
			if limit > end {
				limit = end
			}
			for cutAt < limit {
				fp = (fp << 1) + gearTable[buf[cutAt]]
				if fp&avgMask == 0 {
					cutAt++
					goto emit
				}
				cutAt++
			}
			// Phase 2: scan from avgSize to maxSize with relaxed mask.
			for cutAt < end {
				fp = (fp << 1) + gearTable[buf[cutAt]]
				if fp&largeMask == 0 {
					cutAt++
					goto emit
				}
				cutAt++
			}

		emit:
			chunkData := make([]byte, cutAt-pos)
			copy(chunkData, buf[pos:cutAt])

			hash := blake3.Sum256(chunkData)

			if err := cb(Chunk{
				Data:   chunkData,
				Hash:   hash,
				Offset: offset,
			}); err != nil {
				return err
			}

			offset += int64(cutAt - pos)
			pos = cutAt
		}

		if readErr == io.EOF || readErr == io.ErrUnexpectedEOF {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

// bitsForSize returns the number of bits needed to represent size,
// used to derive the gear-hash mask for a target chunk size.
// For a target of 128K, this returns 17 (2^17 = 131072).
func bitsForSize(size int) int {
	bits := 0
	s := size
	for s > 1 {
		s >>= 1
		bits++
	}
	return bits
}

// gearTable is a precomputed table of 256 pseudorandom 64-bit values
// used by the gear-hash rolling function. Generated from BLAKE3("gear-table-seed")
// to be deterministic and reproducible.
var gearTable [256]uint64

func init() {
	// Derive gear table deterministically from BLAKE3.
	// Each entry is 8 bytes from BLAKE3 XOF seeded with the index.
	for i := 0; i < 256; i++ {
		h := blake3.New()
		h.Write([]byte("shurli-gear-table"))
		h.Write([]byte{byte(i)})
		var buf [8]byte
		h.Digest().Read(buf[:])
		gearTable[i] = uint64(buf[0]) | uint64(buf[1])<<8 | uint64(buf[2])<<16 |
			uint64(buf[3])<<24 | uint64(buf[4])<<32 | uint64(buf[5])<<40 |
			uint64(buf[6])<<48 | uint64(buf[7])<<56
	}
}
