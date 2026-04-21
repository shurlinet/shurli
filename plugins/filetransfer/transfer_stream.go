package filetransfer

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/reedsolomon"
	"github.com/zeebo/blake3"
	"golang.org/x/time/rate"
)

// rateLimitedWriter wraps an io.Writer with a shared token-bucket rate limiter.
// Multiple writers sharing the same *rate.Limiter coordinate their aggregate send rate.
// Used to prevent QUIC transfers from saturating the network link (bufferbloat fix).
type rateLimitedWriter struct {
	w       io.Writer
	limiter *rate.Limiter
	ctx     context.Context
}

func (rlw *rateLimitedWriter) Write(p []byte) (int, error) {
	if err := rlw.limiter.WaitN(rlw.ctx, len(p)); err != nil {
		return 0, err
	}
	return rlw.w.Write(p)
}

// Streaming protocol wire constants.
const (
	// msgStreamChunk is a self-describing chunk frame with embedded hash, offset, and decompressed size.
	msgStreamChunk = 0x0A

	// msgTrailer is sent after all chunks with the chunk count and Merkle root.
	msgTrailer = 0x0B

	// streamChunkHeaderSize is the wire header size for a streaming chunk frame:
	// msgType(1) + fileIdx(2) + chunkIdx(4) + offset(8) + hash(32) + decompSize(4) + dataLen(4) = 55
	streamChunkHeaderSize = 55

	// Maximum file count in a single transfer (directory with millions of files is unreasonable).
	maxFileCount = 100000

	// maxTotalTransferSize is the maximum total size for directory transfers (10 TB).
	// Individual files are still capped at maxFileSize (1 TB). [R4-IMP1]
	maxTotalTransferSize = 10 << 40

	// maxHeaderSize limits memory during header parsing. Wraps reader in LimitedReader. [R3-SEC3, N5]
	maxHeaderSize = 32 << 20 // 32 MB

	// producerChanBuffer bounds chunks held between the disk reader and the
	// network sender. With the FT-Y #14 tier bump, a single chunk can be up to
	// 4 MB on the wire, so deep buffers translate into large resident memory.
	// 8 slots x 4 MB = 32 MB worst case per outgoing transfer. maxConcurrentTransfers
	// (10) caps the global worst case at ~320 MB.
	producerChanBuffer = 8

	// parityFileIdx is a sentinel fileIdx value that marks parity (erasure coding) chunks.
	// Parity chunks don't map to any file in the file table. Using this sentinel prevents
	// writeChunkGlobal from writing parity data into actual files (S1 fix).
	// Value 0xFFFF is safe: maxFileCount=100000 < 65535, so no valid file index collides.
	parityFileIdx = 0xFFFF
)

// fileEntry describes a single file in the transfer header's file table.
// Revised per F3 to include optional metadata (permissions, timestamps).
type fileEntry struct {
	Path      string // relative path (forward slashes, sanitized)
	Size      int64  // file size in bytes
	MetaFlags uint8  // bitmask: 0x01=has permissions, 0x02=has mtime
	Mode      uint32 // unix permissions (only if MetaFlags&0x01)
	Mtime     int64  // unix timestamp seconds (only if MetaFlags&0x02)
}

// Metadata flag constants for fileEntry.MetaFlags.
const (
	metaHasMode  = 0x01 // file has permissions metadata
	metaHasMtime = 0x02 // file has modification time metadata
)

// fileSlice maps a portion of a global-offset chunk to a specific file. [F1, N3]
type fileSlice struct {
	FileIdx     uint16 // which file in the file table
	LocalOffset int64  // byte offset within this file
	DataOffset  int    // offset within the chunk's decompressed data
	Length      int    // bytes to write to this file
}

// streamChunk is a self-describing chunk that flows from the producer goroutine
// to the sender goroutine(s) via buffered channels.
type streamChunk struct {
	fileIdx    uint16   // which file this chunk starts in (hint for cross-file CDC)
	chunkIdx   int      // sequential chunk index (global, across all files)
	offset     int64    // global byte offset within the concatenated stream
	hash       [32]byte // BLAKE3 hash of original (uncompressed) data
	decompSize uint32   // decompressed size
	data       []byte   // wire data (possibly compressed with zstd)
}

// producerResult is returned by the chunk producer goroutine after it finishes
// reading the entire file (or all files in a directory).
type producerResult struct {
	chunkHashes   [][32]byte       // all chunk hashes in order (for Merkle root)
	chunkSizes    []uint32         // all decompressed sizes in order
	anyCompressed bool             // whether any chunk was actually compressed
	erasure       *erasureTrailer  // R4-SEC1 Batch 2: trailer from erasureEncoder (nil if erasure disabled)
	skippedHashes map[int][32]byte // chunk hashes for selectively skipped chunks (sparse trailer)
	err           error
}

// --- multiFileReader: io.Reader over concatenated files with lazy open [F1, F8, I11, N9] ---

// multiFileReader presents multiple files as a single continuous byte stream.
// Files are opened lazily (one at a time) to avoid fd exhaustion on large directories.
// Implements io.Reader contract: returns io.EOF only when ALL files are exhausted.
// Critical: Read() MUST NOT return io.EOF at file boundaries (R3-IMP1).
type multiFileReader struct {
	files      []fileEntry // file table from header
	filePaths  []string    // absolute paths to each file
	cumOffsets []int64     // cumulative byte offsets: cumOffsets[i] = sum of files[0..i-1].Size

	currentIdx  int      // index of currently open file
	currentFile *os.File // currently open file handle (nil if not yet opened or all done)
	globalPos   int64    // current position in global stream (for fileIndexAt)
}

// newMultiFileReader creates a reader over concatenated files.
// cumOffsets is precomputed: cumOffsets[0]=0, cumOffsets[i]=cumOffsets[i-1]+files[i-1].Size.
func newMultiFileReader(files []fileEntry, filePaths []string, cumOffsets []int64) *multiFileReader {
	return &multiFileReader{
		files:      files,
		filePaths:  filePaths,
		cumOffsets: cumOffsets,
	}
}

// Read implements io.Reader. Seamlessly crosses file boundaries without returning
// premature EOF. Only returns io.EOF when all files are exhausted. [R3-IMP1]
func (mfr *multiFileReader) Read(p []byte) (int, error) {
	totalRead := 0

	for totalRead < len(p) {
		// Skip empty files (Size=0).
		for mfr.currentIdx < len(mfr.files) && mfr.files[mfr.currentIdx].Size == 0 {
			mfr.currentIdx++
		}

		// All files exhausted.
		if mfr.currentIdx >= len(mfr.files) {
			if totalRead > 0 {
				return totalRead, nil
			}
			return 0, io.EOF
		}

		// Lazy open: open file on first read.
		if mfr.currentFile == nil {
			f, err := os.Open(mfr.filePaths[mfr.currentIdx])
			if err != nil {
				return totalRead, fmt.Errorf("open file %d (%s): %w", mfr.currentIdx, mfr.files[mfr.currentIdx].Path, err)
			}
			mfr.currentFile = f
		}

		// Read from current file.
		n, err := mfr.currentFile.Read(p[totalRead:])
		totalRead += n
		mfr.globalPos += int64(n)

		if err == io.EOF {
			// Current file exhausted. Close it, advance to next.
			mfr.currentFile.Close()
			mfr.currentFile = nil
			mfr.currentIdx++
			// Do NOT return io.EOF here - continue to next file.
			continue
		}
		if err != nil {
			return totalRead, err
		}
	}

	return totalRead, nil
}

// fileIndexAt returns the file index for a given global byte offset.
// Uses binary search on cumulative offsets. [N9]
func (mfr *multiFileReader) fileIndexAt(globalOffset int64) uint16 {
	if len(mfr.files) == 0 {
		return 0
	}
	// Binary search: find largest i where cumOffsets[i] <= globalOffset.
	idx := sort.Search(len(mfr.cumOffsets), func(i int) bool {
		return mfr.cumOffsets[i] > globalOffset
	}) - 1
	if idx < 0 {
		idx = 0
	}
	// Clamp to valid file table range.
	if idx >= len(mfr.files) {
		idx = len(mfr.files) - 1
	}
	// Skip empty files at this offset (they share the same cumOffset as the next file).
	for idx < len(mfr.files)-1 && mfr.files[idx].Size == 0 {
		idx++
	}
	return uint16(idx)
}

// Close closes the currently open file handle, if any.
func (mfr *multiFileReader) Close() error {
	if mfr.currentFile != nil {
		err := mfr.currentFile.Close()
		mfr.currentFile = nil
		return err
	}
	return nil
}

// --- globalToLocal: map global offset range to per-file slices [F1, N3, R3-IMP8] ---

// globalToLocal maps a chunk's global offset and size to one or more fileSlice entries.
// Handles cross-file boundary chunks (the chunk spans two or more files).
// Skips empty files at boundaries (R3-IMP8).
func globalToLocal(globalOffset int64, decompSize int, files []fileEntry, cumOffsets []int64) []fileSlice {
	if decompSize == 0 || len(files) == 0 {
		return nil
	}

	// Find starting file index via binary search.
	startIdx := sort.Search(len(cumOffsets), func(i int) bool {
		return cumOffsets[i] > globalOffset
	}) - 1
	if startIdx < 0 {
		startIdx = 0
	}

	var slices []fileSlice
	remaining := decompSize
	dataOffset := 0

	for i := startIdx; i < len(files) && remaining > 0; i++ {
		// Skip empty files (R3-IMP8).
		if files[i].Size == 0 {
			continue
		}

		fileStart := cumOffsets[i]
		fileEnd := fileStart + files[i].Size

		// Chunk range in global coords.
		chunkStart := globalOffset + int64(dataOffset)
		if chunkStart >= fileEnd {
			continue
		}
		if chunkStart < fileStart {
			// This shouldn't happen with valid cumulative offsets. If it does,
			// the binary search or offset data is wrong. Skip this file rather
			// than silently writing wrong data at offset 0.
			continue
		}

		localOff := chunkStart - fileStart
		capacity := files[i].Size - localOff
		writeLen := int64(remaining)
		if writeLen > capacity {
			writeLen = capacity
		}

		slices = append(slices, fileSlice{
			FileIdx:     uint16(i),
			LocalOffset: localOff,
			DataOffset:  dataOffset,
			Length:      int(writeLen),
		})

		dataOffset += int(writeLen)
		remaining -= int(writeLen)
	}

	return slices
}

// computeCumulativeOffsets builds the cumulative offset table from a file table.
// cumOffsets[i] = sum of files[0..i-1].Size. cumOffsets[0] = 0.
func computeCumulativeOffsets(files []fileEntry) []int64 {
	cum := make([]int64, len(files)+1)
	for i, f := range files {
		cum[i+1] = cum[i] + f.Size
	}
	return cum
}

// --- Streaming wire format: Header ---

// sortFileTable sorts files and their corresponding absolute paths by relative path
// for deterministic wire ordering (I7). Both slices are reordered in-place to stay in sync.
// Validates no duplicate paths (I10) or case-insensitive collisions on darwin/windows (R3-SEC7).
//
// Ordering (TE-69): comparison is byte-order (Go's builtin `<` on strings
// compares bytewise), NOT locale- or filesystem-aware. This makes the sort
// deterministic across platforms (macOS APFS case-insensitive vs Linux ext4
// case-sensitive vs Windows NTFS) so the contentKey derived downstream is
// identical no matter who ran filepath.WalkDir. Do NOT replace with
// collate/strings-aware comparison: the content key in transfer_stream.go
// contentKey() and the receiver's checkpoint resume both depend on this exact
// byte ordering.
func sortFileTable(files []fileEntry, filePaths []string) error {
	if len(files) != len(filePaths) {
		return fmt.Errorf("file table / path count mismatch: %d vs %d", len(files), len(filePaths))
	}

	// Build index for synchronized sort. Byte-order compare (TE-69).
	indices := make([]int, len(files))
	for i := range indices {
		indices[i] = i
	}
	sort.Slice(indices, func(a, b int) bool {
		return files[indices[a]].Path < files[indices[b]].Path
	})

	// Apply permutation to both slices.
	sortedFiles := make([]fileEntry, len(files))
	sortedPaths := make([]string, len(filePaths))
	for i, idx := range indices {
		sortedFiles[i] = files[idx]
		sortedPaths[i] = filePaths[idx]
	}
	copy(files, sortedFiles)
	copy(filePaths, sortedPaths)

	// Reject duplicate paths (I10) and case-insensitive duplicates on darwin/windows (R3-SEC7).
	seen := make(map[string]bool, len(files))
	caseInsensitive := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	for i, f := range files {
		key := f.Path
		if caseInsensitive {
			key = strings.ToLower(f.Path)
		}
		if seen[key] {
			return fmt.Errorf("duplicate path in file table: %s (file %d)", f.Path, i)
		}
		seen[key] = true
	}

	return nil
}

// writeHeader writes the SHFT streaming header to w.
//
// Wire layout:
//
//	magic(4) + version(1) + flags(1) + fileCount(2) + totalSize(8) + transferID(32)
//	+ [fileCount x (pathLen(2) + path(var) + fileSize(8) + metaFlags(1) + [mode(4)] + [mtime(8)])]
//
// IMPORTANT: Caller must call sortFileTable(files, filePaths) BEFORE writeHeader
// to ensure deterministic ordering (I7) and synchronized file table / path arrays.
// transferID is a random 32-byte session identifier for worker stream routing.
func writeHeader(w io.Writer, files []fileEntry, flags uint8, totalSize int64, transferID [32]byte, erasure *erasureHeaderParams) error {
	if len(files) == 0 {
		return fmt.Errorf("empty file table")
	}
	if len(files) > maxFileCount {
		return fmt.Errorf("too many files: %d (max %d)", len(files), maxFileCount)
	}

	// Validate totalSize matches sum of file sizes (catch caller bugs early).
	var sumSize int64
	for _, f := range files {
		sumSize += f.Size
	}
	if sumSize != totalSize {
		return fmt.Errorf("totalSize %d does not match file sizes sum %d", totalSize, sumSize)
	}

	// Fixed prefix: magic(4) + version(1) + flags(1) + fileCount(2) + totalSize(8) + transferID(32) = 48 bytes.
	var prefix [48]byte
	prefix[0] = shftMagic0
	prefix[1] = shftMagic1
	prefix[2] = shftMagic2
	prefix[3] = shftMagic3
	prefix[4] = shftVersion
	prefix[5] = flags
	binary.BigEndian.PutUint16(prefix[6:8], uint16(len(files)))
	binary.BigEndian.PutUint64(prefix[8:16], uint64(totalSize))
	copy(prefix[16:48], transferID[:])

	if _, err := w.Write(prefix[:]); err != nil {
		return fmt.Errorf("write header prefix: %w", err)
	}

	// File table entries with metadata (F3).
	for i, f := range files {
		pathBytes := []byte(f.Path)
		if len(pathBytes) > maxFilenameLen {
			return fmt.Errorf("file %d path too long: %d bytes", i, len(pathBytes))
		}
		var pathLenBuf [2]byte
		binary.BigEndian.PutUint16(pathLenBuf[:], uint16(len(pathBytes)))
		if _, err := w.Write(pathLenBuf[:]); err != nil {
			return fmt.Errorf("write file %d path length: %w", i, err)
		}
		if _, err := w.Write(pathBytes); err != nil {
			return fmt.Errorf("write file %d path: %w", i, err)
		}
		var sizeBuf [8]byte
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(f.Size))
		if _, err := w.Write(sizeBuf[:]); err != nil {
			return fmt.Errorf("write file %d size: %w", i, err)
		}

		// MetaFlags + conditional fields (F3).
		metaBuf := [1]byte{f.MetaFlags}
		if _, err := w.Write(metaBuf[:]); err != nil {
			return fmt.Errorf("write file %d meta flags: %w", i, err)
		}
		if f.MetaFlags&metaHasMode != 0 {
			var modeBuf [4]byte
			binary.BigEndian.PutUint32(modeBuf[:], f.Mode)
			if _, err := w.Write(modeBuf[:]); err != nil {
				return fmt.Errorf("write file %d mode: %w", i, err)
			}
		}
		if f.MetaFlags&metaHasMtime != 0 {
			var mtimeBuf [8]byte
			binary.BigEndian.PutUint64(mtimeBuf[:], uint64(f.Mtime))
			if _, err := w.Write(mtimeBuf[:]); err != nil {
				return fmt.Errorf("write file %d mtime: %w", i, err)
			}
		}
	}

	// Erasure header fields (Option C, OC-F16): stripeSize(2) + overheadPerMille(2)
	// written after the file table when flagErasureCoded is set. Enables the
	// receiver to initialize per-stripe parity tracking before any chunks arrive.
	if erasure != nil && flags&flagErasureCoded != 0 {
		var ehdr [4]byte
		binary.BigEndian.PutUint16(ehdr[0:2], uint16(erasure.StripeSize))
		binary.BigEndian.PutUint16(ehdr[2:4], erasure.OverheadPerMille)
		if _, err := w.Write(ehdr[:]); err != nil {
			return fmt.Errorf("write erasure header: %w", err)
		}
	}

	return nil
}

// readHeader reads an SHFT streaming header from r.
// Returns the file table, total size, flags, transfer ID, precomputed
// cumulative offsets, and erasure header params (nil when no erasure).
// Wraps reader in LimitedReader to prevent header bomb attacks (R3-SEC3).
func readHeader(r io.Reader) ([]fileEntry, int64, uint8, [32]byte, []int64, *erasureHeaderParams, error) {
	var zeroID [32]byte

	// Wrap in LimitedReader to enforce maxHeaderSize during parsing (R3-SEC3).
	lr := &io.LimitedReader{R: r, N: maxHeaderSize}

	// Read prefix: magic(4) + version(1) + flags(1) + fileCount(2) + totalSize(8) + transferID(32).
	var prefix [48]byte
	if _, err := io.ReadFull(lr, prefix[:]); err != nil {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read header prefix: %w", err)
	}

	if prefix[0] != shftMagic0 || prefix[1] != shftMagic1 ||
		prefix[2] != shftMagic2 || prefix[3] != shftMagic3 {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("invalid magic bytes: not SHFT")
	}
	if prefix[4] != shftVersion {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("unsupported SHFT version: %d (expected %d)", prefix[4], shftVersion)
	}

	flags := prefix[5]
	fileCount := int(binary.BigEndian.Uint16(prefix[6:8]))
	totalSize := int64(binary.BigEndian.Uint64(prefix[8:16]))
	var transferID [32]byte
	copy(transferID[:], prefix[16:48])

	if fileCount == 0 {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("empty file table")
	}
	if fileCount > maxFileCount {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("too many files: %d (max %d)", fileCount, maxFileCount)
	}
	if totalSize < 0 || totalSize > maxTotalTransferSize {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("invalid total size: %d", totalSize)
	}

	// Read file table from LimitedReader.
	files := make([]fileEntry, fileCount)
	caseInsensitive := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	seenPaths := make(map[string]bool, fileCount)
	var sumSize int64

	for i := 0; i < fileCount; i++ {
		var pathLenBuf [2]byte
		if _, err := io.ReadFull(lr, pathLenBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d path length: %w", i, err)
		}
		pathLen := int(binary.BigEndian.Uint16(pathLenBuf[:]))
		if pathLen > maxFilenameLen {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("file %d path too long: %d", i, pathLen)
		}

		pathBuf := make([]byte, pathLen)
		if _, err := io.ReadFull(lr, pathBuf); err != nil {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d path: %w", i, err)
		}

		var sizeBuf [8]byte
		if _, err := io.ReadFull(lr, sizeBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d size: %w", i, err)
		}
		fSize := int64(binary.BigEndian.Uint64(sizeBuf[:]))

		// Per-file size validation (R4-IMP1).
		// Check fSize >= 0: uint64 > MaxInt64 wraps to negative int64.
		if fSize < 0 || fSize > maxFileSize {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("file %d invalid size: %d", i, fSize)
		}

		// Read metadata flags (F3).
		var metaBuf [1]byte
		if _, err := io.ReadFull(lr, metaBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d meta flags: %w", i, err)
		}
		metaFlags := metaBuf[0]

		var mode uint32
		if metaFlags&metaHasMode != 0 {
			var modeBuf [4]byte
			if _, err := io.ReadFull(lr, modeBuf[:]); err != nil {
				return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d mode: %w", i, err)
			}
			mode = binary.BigEndian.Uint32(modeBuf[:])
		}

		var mtime int64
		if metaFlags&metaHasMtime != 0 {
			var mtimeBuf [8]byte
			if _, err := io.ReadFull(lr, mtimeBuf[:]); err != nil {
				return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read file %d mtime: %w", i, err)
			}
			mtime = int64(binary.BigEndian.Uint64(mtimeBuf[:]))
		}

		// Sanitize path.
		sanitized := sanitizeRelativePath(string(pathBuf))
		if sanitized == "" {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("file %d path empty after sanitization", i)
		}

		// Duplicate path detection (I10) + case-insensitive on darwin/windows (R3-SEC7).
		pathKey := sanitized
		if caseInsensitive {
			pathKey = strings.ToLower(sanitized)
		}
		if seenPaths[pathKey] {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("duplicate path in file table: %s (file %d)", sanitized, i)
		}
		seenPaths[pathKey] = true

		files[i] = fileEntry{
			Path:      sanitized,
			Size:      fSize,
			MetaFlags: metaFlags,
			Mode:      mode,
			Mtime:     mtime,
		}
		sumSize += fSize
	}

	// Cross-check: sum of file sizes should match totalSize.
	if sumSize != totalSize {
		return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("file sizes sum %d does not match header totalSize %d", sumSize, totalSize)
	}

	// Precompute cumulative offsets for globalToLocal lookups.
	cumOffsets := computeCumulativeOffsets(files)

	// Read erasure header params if flagErasureCoded is set (Option C, OC-F41).
	// Placed after file table, conditional on the flag. Validates bounds
	// identically to the old readErasureManifest to block malicious values.
	var ehdr *erasureHeaderParams
	if flags&flagErasureCoded != 0 {
		var ehdrBuf [4]byte
		if _, err := io.ReadFull(lr, ehdrBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("read erasure header: %w", err)
		}
		stripeSize := int(binary.BigEndian.Uint16(ehdrBuf[0:2]))
		overheadPerMille := binary.BigEndian.Uint16(ehdrBuf[2:4])

		if stripeSize < minStripeSize || stripeSize > maxAcceptedStripeSize {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("invalid stripe size: %d (must be %d..%d)", stripeSize, minStripeSize, maxAcceptedStripeSize)
		}
		maxPerMille := uint16(maxParityOverhead * 1000)
		if overheadPerMille == 0 || overheadPerMille > maxPerMille {
			return nil, 0, 0, zeroID, nil, nil, fmt.Errorf("invalid erasure overhead per-mille: %d (must be 1..%d)", overheadPerMille, maxPerMille)
		}
		ehdr = &erasureHeaderParams{
			StripeSize:       stripeSize,
			OverheadPerMille: overheadPerMille,
		}
	}

	return files, totalSize, flags, transferID, cumOffsets, ehdr, nil
}

// --- Streaming wire format: Chunk frame ---

// writeStreamChunkFrame writes a self-describing chunk frame to w.
//
// Wire layout:
//
//	msgStreamChunk(1) + fileIdx(2) + chunkIdx(4) + offset(8) + hash(32) + decompSize(4) + dataLen(4) + data(var)
func writeStreamChunkFrame(w io.Writer, sc streamChunk) error {
	if len(sc.data) > maxChunkWireSize {
		return fmt.Errorf("chunk %d too large: %d bytes", sc.chunkIdx, len(sc.data))
	}

	var header [streamChunkHeaderSize]byte
	header[0] = msgStreamChunk
	binary.BigEndian.PutUint16(header[1:3], sc.fileIdx)
	binary.BigEndian.PutUint32(header[3:7], uint32(sc.chunkIdx))
	binary.BigEndian.PutUint64(header[7:15], uint64(sc.offset))
	copy(header[15:47], sc.hash[:])
	binary.BigEndian.PutUint32(header[47:51], sc.decompSize)
	binary.BigEndian.PutUint32(header[51:55], uint32(len(sc.data)))

	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	_, err := w.Write(sc.data)
	return err
}

// readStreamChunkFrame reads a streaming chunk frame, trailer, or done signal.
// Returns the chunk data and the message type byte.
// For msgStreamChunk: sc is populated with all fields.
// For msgTrailer: returns empty sc with msgType = msgTrailer (call readTrailer next).
// For msgTransferDone: returns empty sc with msgType = msgTransferDone.
// Validates chunkIdx < maxChunkCount to prevent memory exhaustion (R3-SEC1).
func readStreamChunkFrame(r io.Reader) (sc streamChunk, msgType byte, err error) {
	var typeByte [1]byte
	if _, err := io.ReadFull(r, typeByte[:]); err != nil {
		return streamChunk{}, 0, fmt.Errorf("read stream frame type: %w", err)
	}

	switch typeByte[0] {
	case msgTransferDone:
		return streamChunk{}, msgTransferDone, nil
	case msgTrailer:
		return streamChunk{}, msgTrailer, nil
	case msgStreamChunk:
		// Read remaining header: fileIdx(2) + chunkIdx(4) + offset(8) + hash(32) + decompSize(4) + dataLen(4) = 54 bytes.
		var rest [54]byte
		if _, err := io.ReadFull(r, rest[:]); err != nil {
			return streamChunk{}, 0, fmt.Errorf("read stream chunk header: %w", err)
		}

		sc.fileIdx = binary.BigEndian.Uint16(rest[0:2])
		sc.chunkIdx = int(binary.BigEndian.Uint32(rest[2:6]))
		sc.offset = int64(binary.BigEndian.Uint64(rest[6:14]))
		copy(sc.hash[:], rest[14:46])
		sc.decompSize = binary.BigEndian.Uint32(rest[46:50])
		dataLen := int(binary.BigEndian.Uint32(rest[50:54]))

		// Validate chunkIdx against maxChunkCount (R3-SEC1).
		// Without this, orderedHashes() can allocate 64GB on a single malicious frame.
		if sc.chunkIdx >= maxChunkCount {
			return streamChunk{}, 0, fmt.Errorf("chunk index %d out of range (max %d)", sc.chunkIdx, maxChunkCount)
		}

		// Validate offset is non-negative (uint64 > MaxInt64 wraps to negative int64).
		if sc.offset < 0 {
			return streamChunk{}, 0, fmt.Errorf("chunk %d has negative offset: %d", sc.chunkIdx, sc.offset)
		}

		// Validate decompressed size (I8).
		if sc.decompSize > maxDecompressedChunk {
			return streamChunk{}, 0, fmt.Errorf("chunk %d decompressed size too large: %d (max %d)", sc.chunkIdx, sc.decompSize, maxDecompressedChunk)
		}

		if dataLen > maxChunkWireSize {
			return streamChunk{}, 0, fmt.Errorf("chunk %d data too large: %d bytes", sc.chunkIdx, dataLen)
		}

		sc.data = make([]byte, dataLen)
		if _, err := io.ReadFull(r, sc.data); err != nil {
			return streamChunk{}, 0, fmt.Errorf("read chunk %d data: %w", sc.chunkIdx, err)
		}

		return sc, msgStreamChunk, nil
	default:
		return streamChunk{}, 0, fmt.Errorf("unexpected stream frame type: 0x%02x", typeByte[0])
	}
}

// --- Streaming wire format: Accept response with selective rejection [F2] ---

// writeAcceptBitfield writes an accept response with per-file acceptance bitfield.
// Each bit corresponds to one file in the file table (1=accept, 0=reject).
// For full accept (all files): all bits set. Single-file: 1 byte with bit 0 set.
//
// Wire layout: msgAccept(1) + bitfieldLen(2) + bitfield(var)
func writeAcceptBitfield(w io.Writer, fileCount int, accepted *bitfield) error {
	expectedLen := (fileCount + 7) / 8
	if len(accepted.bits) != expectedLen {
		return fmt.Errorf("bitfield size mismatch: got %d, want %d", len(accepted.bits), expectedLen)
	}

	buf := make([]byte, 1+2+expectedLen)
	buf[0] = msgAccept
	binary.BigEndian.PutUint16(buf[1:3], uint16(expectedLen))
	copy(buf[3:], accepted.bits)
	_, err := w.Write(buf)
	return err
}

// readAcceptBitfield reads the accept bitfield after msgAccept type byte was consumed.
// Validates bitfield length matches expected file count (R3-SEC5).
func readAcceptBitfield(r io.Reader, fileCount int) (*bitfield, error) {
	var lenBuf [2]byte
	if _, err := io.ReadFull(r, lenBuf[:]); err != nil {
		return nil, fmt.Errorf("read accept bitfield length: %w", err)
	}
	bfLen := int(binary.BigEndian.Uint16(lenBuf[:]))

	expectedLen := (fileCount + 7) / 8
	if bfLen != expectedLen {
		return nil, fmt.Errorf("accept bitfield size mismatch: got %d, want %d (R3-SEC5)", bfLen, expectedLen)
	}

	bits := make([]byte, bfLen)
	if _, err := io.ReadFull(r, bits); err != nil {
		return nil, fmt.Errorf("read accept bitfield: %w", err)
	}

	return &bitfield{bits: bits, n: fileCount}, nil
}

// isFullAccept returns true if all files are accepted (no selective rejection).
// Checks only valid bits [0..n-1], ignoring trailing padding bits in the last byte.
func isFullAccept(bf *bitfield) bool {
	for i := 0; i < bf.n; i++ {
		if !bf.has(i) {
			return false
		}
	}
	return true
}

// isAllRejected returns true if all files are rejected (R4-IMP4).
// Checks only valid bits [0..n-1], ignoring trailing padding bits in the last byte.
func isAllRejected(bf *bitfield) bool {
	for i := 0; i < bf.n; i++ {
		if bf.has(i) {
			return false
		}
	}
	return true
}

// --- Streaming wire format: Trailer ---

// Trailer flag constants.
const (
	trailerFlagSparseHashes = 0x01 // trailer includes sparse hash list (selective rejection active)
)

// writeTrailer writes the end-of-transfer trailer.
//
// Wire layout:
//
//	msgTrailer(1) + flags(1) + chunkCount(4) + rootHash(32)
//	[if trailerFlagSparseHashes: + missingHashCount(4) + [chunkIdx(4)+hash(32)]*N]
//	[if erasure: + parityCount(4) + parityHashes(N*32) + paritySizes(N*4)]
func writeTrailer(w io.Writer, chunkCount int, rootHash [32]byte, sparseHashes map[int][32]byte, erasure *erasureTrailer) error {
	var flags byte
	if len(sparseHashes) > 0 {
		flags |= trailerFlagSparseHashes
	}

	var buf [38]byte // 1 + 1 + 4 + 32
	buf[0] = msgTrailer
	buf[1] = flags
	binary.BigEndian.PutUint32(buf[2:6], uint32(chunkCount))
	copy(buf[6:38], rootHash[:])
	if _, err := w.Write(buf[:]); err != nil {
		return fmt.Errorf("write trailer: %w", err)
	}

	// Sparse hash list for selective rejection (R3-IMP6 revised N1).
	// Written in ascending chunkIdx order for deterministic wire output.
	if flags&trailerFlagSparseHashes != 0 {
		var countBuf [4]byte
		binary.BigEndian.PutUint32(countBuf[:], uint32(len(sparseHashes)))
		if _, err := w.Write(countBuf[:]); err != nil {
			return fmt.Errorf("write sparse hash count: %w", err)
		}
		// Sort keys for deterministic output.
		sortedIdx := make([]int, 0, len(sparseHashes))
		for idx := range sparseHashes {
			sortedIdx = append(sortedIdx, idx)
		}
		sort.Ints(sortedIdx)
		for _, idx := range sortedIdx {
			hash := sparseHashes[idx]
			var entry [36]byte // chunkIdx(4) + hash(32)
			binary.BigEndian.PutUint32(entry[0:4], uint32(idx))
			copy(entry[4:36], hash[:])
			if _, err := w.Write(entry[:]); err != nil {
				return fmt.Errorf("write sparse hash %d: %w", idx, err)
			}
		}
	}

	// Erasure fields follow if present. stripeSize + overheadPerMille are in
	// the header (Option C); trailer carries only parityCount + hashes + sizes.
	if erasure != nil {
		if err := writeErasureManifest(w, erasure.ParityCount,
			erasure.ParityHashes, erasure.ParitySizes); err != nil {
			return fmt.Errorf("write erasure trailer: %w", err)
		}
	}

	return nil
}

// erasureTrailer holds erasure coding metadata for the trailer.
//
// StripeSize and OverheadPerMille moved to the SHFT header (Option C, OC-F28)
// so the receiver can initialize per-stripe parity tracking before any chunks
// arrive. The trailer retains only parity count + per-parity hashes and sizes.
type erasureTrailer struct {
	ParityCount  int
	ParityHashes [][32]byte
	ParitySizes  []uint32
}

// readTrailer reads the trailer after the msgTrailer byte has been consumed
// by readStreamChunkFrame.
//
// Wire layout (after msgTrailer byte):
//
//	flags(1) + chunkCount(4) + rootHash(32)
//	[if trailerFlagSparseHashes: + missingHashCount(4) + [chunkIdx(4)+hash(32)]*N]
//	[+ erasure fields if header flags indicate]
func readTrailer(r io.Reader, hasErasure bool) (chunkCount int, rootHash [32]byte, sparseHashes map[int][32]byte, erasure *erasureTrailer, err error) {
	var buf [37]byte // flags(1) + chunkCount(4) + rootHash(32)
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		return 0, [32]byte{}, nil, nil, fmt.Errorf("read trailer: %w", err)
	}

	flags := buf[0]
	chunkCount = int(binary.BigEndian.Uint32(buf[1:5]))
	copy(rootHash[:], buf[5:37])

	if chunkCount > maxChunkCount {
		return 0, [32]byte{}, nil, nil, fmt.Errorf("invalid trailer chunk count: %d", chunkCount)
	}

	// Read sparse hash list if present (N1, R3-IMP6).
	if flags&trailerFlagSparseHashes != 0 {
		var countBuf [4]byte
		if _, err := io.ReadFull(r, countBuf[:]); err != nil {
			return 0, [32]byte{}, nil, nil, fmt.Errorf("read sparse hash count: %w", err)
		}
		missingCount := int(binary.BigEndian.Uint32(countBuf[:]))
		if missingCount > maxChunkCount {
			return 0, [32]byte{}, nil, nil, fmt.Errorf("sparse hash count too large: %d", missingCount)
		}
		sparseHashes = make(map[int][32]byte, missingCount)
		for i := 0; i < missingCount; i++ {
			var entry [36]byte
			if _, err := io.ReadFull(r, entry[:]); err != nil {
				return 0, [32]byte{}, nil, nil, fmt.Errorf("read sparse hash %d: %w", i, err)
			}
			idx := int(binary.BigEndian.Uint32(entry[0:4]))
			if idx < 0 || idx >= maxChunkCount {
				return 0, [32]byte{}, nil, nil, fmt.Errorf("sparse hash index %d out of range (max %d)", idx, maxChunkCount)
			}
			var hash [32]byte
			copy(hash[:], entry[4:36])
			sparseHashes[idx] = hash
		}
	}

	if hasErasure {
		ph, ps, readErr := readErasureManifest(r)
		if readErr != nil {
			return 0, [32]byte{}, nil, nil, fmt.Errorf("read erasure trailer: %w", readErr)
		}
		erasure = &erasureTrailer{
			ParityCount:  len(ph),
			ParityHashes: ph,
			ParitySizes:  ps,
		}
	}

	return chunkCount, rootHash, sparseHashes, erasure, nil
}

// --- Chunk producer with context [N2, N9, C6] ---

// chunkProducer reads files via multiFileReader, chunks them with FastCDC, optionally compresses,
// and sends streamChunks to ch. It returns the accumulated hashes and sizes for Merkle root
// computation via the done channel.
//
// ctx is used for cancellation (N2): on rejection, cancel ctx instead of close(ch) to avoid panic.
// Uses multiFileReader for cross-file CDC (F1, N9).
//
// If skip is non-nil (resume), chunks for set bits are hashed but not sent to ch.
// If acceptBitfield is non-nil (selective rejection), chunks touching only rejected files
// are hashed but not sent. Their hashes are collected in skippedHashes for the sparse trailer (R3-UA3).
// If encoder is non-nil, each raw chunk is fed to the incremental per-stripe RS encoder
// (R4-SEC1 Batch 2). Parity chunks are emitted through ch as they're produced, stripe by
// stripe, bounding peak memory at O(stripeSize * maxChunkSize) instead of O(totalSize).
// The resulting erasure trailer is returned via producerResult.erasure for writeTrailer.
func chunkProducer(
	ctx context.Context,
	files []fileEntry,
	filePaths []string,
	cumOffsets []int64,
	totalSize int64,
	useCompression bool,
	encoder *erasureEncoder,
	skip *bitfield,
	acceptBitfield *bitfield,
	ch chan<- streamChunk,
	done chan<- producerResult,
) {
	var (
		chunkHashes   [][32]byte
		chunkSizes    []uint32
		anyCompressed bool
		skippedHashes map[int][32]byte // chunk hashes for selectively skipped chunks
		erasure       *erasureTrailer
	)

	// Cache selective rejection state outside the hot loop.
	hasSelectiveRejection := acceptBitfield != nil && !isFullAccept(acceptBitfield)
	if hasSelectiveRejection {
		skippedHashes = make(map[int][32]byte)
	}

	// Build multiFileReader for cross-file CDC (F1, N9).
	mfr := newMultiFileReader(files, filePaths, cumOffsets)
	defer mfr.Close()

	// emitParity sends each parity chunk from the encoder through ch as a streamChunk
	// frame with fileIdx=parityFileIdx. Honors ctx cancellation so the producer exits
	// cleanly if the transfer is aborted mid-stripe. [R4-SEC1 Batch 2, B2-F6]
	emitParity := func(parity []parityChunkOut) error {
		for _, p := range parity {
			sc := streamChunk{
				fileIdx:    parityFileIdx,
				chunkIdx:   p.chunkIdx,
				offset:     0, // parity chunks don't map to file offsets
				hash:       p.hash,
				decompSize: uint32(len(p.data)),
				data:       p.data,
			}
			select {
			case ch <- sc:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}

	incompressibleCount := 0
	probeCount := 0
	globalChunkIdx := 0

	err := ChunkReader(mfr, totalSize, func(c Chunk) error {
		hash := c.Hash
		chunkHashes = append(chunkHashes, hash)
		chunkSizes = append(chunkSizes, uint32(len(c.Data)))

		// Feed the RS encoder BEFORE compression. The encoder takes a fresh
		// copy of the raw chunk and accumulates up to stripeSize entries; when
		// the stripe fills, it returns parity chunks to emit on the wire.
		// Parity covers ALL chunks (including resumed/rejected) so the wire
		// semantic matches the pre-Batch-2 encodeErasure(rawForRS) output.
		// [R4-SEC1 Batch 2, B2-F16, Q2 approved]
		if encoder != nil {
			raw := make([]byte, len(c.Data))
			copy(raw, c.Data)
			parity, encErr := encoder.AddChunk(raw)
			if encErr != nil {
				return encErr
			}
			if len(parity) > 0 {
				if emitErr := emitParity(parity); emitErr != nil {
					return emitErr
				}
			}
		}

		// Resume: record hash but don't send data.
		if skip != nil && skip.has(globalChunkIdx) {
			globalChunkIdx++
			return nil
		}

		// Selective rejection: check if this chunk touches any accepted file (R3-UA3).
		if hasSelectiveRejection {
			slices := globalToLocal(c.Offset, len(c.Data), files, cumOffsets)
			allRejected := true
			for _, sl := range slices {
				if acceptBitfield.has(int(sl.FileIdx)) {
					allRejected = false
					break
				}
			}
			if allRejected {
				// Don't send, but record hash for sparse trailer.
				skippedHashes[globalChunkIdx] = hash
				globalChunkIdx++
				return nil
			}
		}

		wireData := c.Data
		if useCompression {
			compressed, ok := compressChunk(c.Data)
			if ok {
				wireData = compressed
				anyCompressed = true
			} else {
				incompressibleCount++
			}
			probeCount++
			// After first 3 chunks: if none compressed, disable for rest.
			if probeCount == 3 && incompressibleCount == 3 {
				useCompression = false
				slog.Debug("file-transfer: compression disabled (incompressible data detected)")
			}
		}

		// Determine fileIdx hint for the chunk frame.
		fileIdx := mfr.fileIndexAt(c.Offset)

		sc := streamChunk{
			fileIdx:    fileIdx,
			chunkIdx:   globalChunkIdx,
			offset:     c.Offset,
			hash:       hash,
			decompSize: uint32(len(c.Data)),
			data:       wireData,
		}

		// Use ctx-aware send to avoid goroutine leak on rejection (N2, C6).
		select {
		case ch <- sc:
		case <-ctx.Done():
			return ctx.Err()
		}

		globalChunkIdx++
		return nil
	})

	// Flush the encoder's trailing partial stripe (if any) BEFORE closing ch,
	// so residual parity chunks reach the sender along the same channel. If
	// ChunkReader already failed, skip finalize — the error is authoritative.
	// [R4-SEC1 Batch 2]
	if err == nil && encoder != nil {
		residual, trailer, finErr := encoder.Finalize()
		if finErr != nil {
			err = finErr
		} else {
			if emitErr := emitParity(residual); emitErr != nil && err == nil {
				err = emitErr
			}
			erasure = trailer
		}
	}

	// Close the chunk channel to signal consumers (streamingSend, sendParallel)
	// that all chunks have been sent. Without this, `for sc := range ch` blocks
	// forever after the last chunk. Close BEFORE sending result to done so
	// consumers exit their range loop before reading the result.
	close(ch)

	result := producerResult{
		chunkHashes:   chunkHashes,
		chunkSizes:    chunkSizes,
		anyCompressed: anyCompressed,
		erasure:       erasure,
		skippedHashes: skippedHashes,
		err:           err,
	}

	// Always send the result. The done channel is buffered (cap 1) so this never blocks.
	// The previous ctx-aware select could discard the result on cancellation, causing
	// the consumer's <-done to deadlock.
	done <- result
}

// --- Receiver helpers ---

// streamReceiveState accumulates chunk metadata as streaming chunks arrive.
// It replaces the pre-computed offset table and manifest chunk data that the
// old protocol provided upfront.
type streamReceiveState struct {
	mu             sync.Mutex
	files          []fileEntry      // from header
	totalSize      int64            // from header
	cumOffsets     []int64          // precomputed cumulative offsets
	compressed     bool             // from header flags
	hasErasure     bool             // from header flags
	hashes         map[int][32]byte // chunkIdx -> hash (accumulated)
	sizes          map[int]uint32   // chunkIdx -> decompressed size (accumulated)
	maxChunkIdx    int              // highest chunk index seen
	receivedBytes  int64            // cumulative decompressed bytes received (R3-SEC4)
	acceptBitfield *bitfield        // per-file accept/reject (nil = full accept) (F2)

	// Duplicate chunk detection (R3-IMP3).
	receivedBitfield *bitfield

	// Destination root for symlink-safe file operations (R3-SEC2).
	destRoot *os.Root

	// Per-stripe RS parity tracking (Option C). Replaces the old flat
	// parityData map with per-stripe slots bounded by a dynamic inflight
	// cap. Memory becomes O(inflight_stripes x parityPerStripe x maxChunk)
	// instead of O(totalParity). [OC-F53 decoupled tracking]
	stripeSize          int                // from header (0 if no erasure)
	overhead            float64            // from header (0 if no erasure)
	parityPerFullStripe int                // stripeParityCount(stripeSize, overhead)
	maxInflightStripes  int                // dynamic cap from budget formula (OC-F10)
	stripeDataCounts    map[int]int        // stripe -> count of data chunks arrived
	paritySlots         map[int]*paritySlot // stripe -> parity storage (bounded)
	totalParityBytes    int64              // aggregate across all inflight slots
	totalParityReceived int                // monotonic wire counter (OC-F14)
	rsFullStripeEnc     reedsolomon.Encoder // reused across full-stripe reconstructions (R5)

	// Corrupted chunk tracking (Batch 2b). Populated when processIncomingChunk
	// sees a hash mismatch on a transfer that carries erasure parity. The
	// chunk's claimed hash + decompSize are already in hashes/sizes via
	// recordChunk, but its bytes were rejected (not written to tmpFiles).
	// rsReconstruct / reconstructSingleStripe consumes this set, recovers
	// bytes from parity, verifies against the claimed hash, and writes via
	// writeChunkGlobal. Only populated when hasErasure is true; on a transfer
	// without parity a hash mismatch is still a hard fail at receive time.
	// [B2-F32, Option C]
	corruptedChunks map[int]bool

	// Per-file temp files and state.
	tmpFiles []*os.File // one per file entry
	tmpPaths []string   // temp file paths (relative to destRoot)

	// keepTempFiles prevents cleanup from deleting temp files on disk.
	// Set to true when a checkpoint has been saved on error, so the partial
	// data survives for the next session's resume. Cleanup still closes file
	// handles but leaves the files on disk.
	keepTempFiles bool
}

// newStreamReceiveState creates a receive state from the header info.
func newStreamReceiveState(files []fileEntry, totalSize int64, flags uint8, cumOffsets []int64) *streamReceiveState {
	return &streamReceiveState{
		files:      files,
		totalSize:  totalSize,
		cumOffsets: cumOffsets,
		compressed: flags&flagCompressed != 0,
		hasErasure: flags&flagErasureCoded != 0,
		hashes:     make(map[int][32]byte, 1024),
		sizes:      make(map[int]uint32, 1024),
	}
}

// initPerStripeState initializes per-stripe parity tracking from the erasure
// header params. Must be called after readHeader when flagErasureCoded is set
// and before any chunks are processed. If resuming from checkpoint, call this
// AFTER restoreReceiveState so the pre-computed stripeDataCounts reflect
// already-received chunks. [Option C, OC-F5/F24]
func (s *streamReceiveState) initPerStripeState(ehdr *erasureHeaderParams) {
	s.stripeSize = ehdr.StripeSize
	s.overhead = overheadFromPerMille(ehdr.OverheadPerMille)
	s.parityPerFullStripe = stripeParityCount(s.stripeSize, s.overhead)
	s.stripeDataCounts = make(map[int]int)
	s.paritySlots = make(map[int]*paritySlot)

	// Dynamic inflight cap: max(2, min(8, budget / perStripeParity)).
	// Self-adapts to overhead: overhead=0.1 -> 6, overhead=0.5 -> 2. [OC-F10]
	perStripeParity := int64(s.parityPerFullStripe) * int64(maxDecompressedChunk)
	if perStripeParity > 0 {
		cap := int(maxParityBudgetBytes / perStripeParity)
		if cap < 2 {
			cap = 2
		}
		if cap > 8 {
			cap = 8
		}
		s.maxInflightStripes = cap
	} else {
		s.maxInflightStripes = 8
	}

	// Reusable encoder for full stripes (R5). Partial last stripe creates
	// its own encoder with different shard counts.
	enc, err := reedsolomon.New(s.stripeSize, s.parityPerFullStripe)
	if err == nil {
		s.rsFullStripeEnc = enc
	}

	// Pre-compute stripeDataCounts from already-received chunks (OC-F5/F24).
	// On resume, state.hashes is populated from the checkpoint. On fresh
	// start, hashes is empty and this loop is a no-op. Corrupted chunks
	// are excluded — they have hashes/sizes (from recordChunk) but their
	// bytes were rejected and need retransmission/reconstruction.
	// [Self-audit round 1 fix]
	s.mu.Lock()
	for chunkIdx := range s.hashes {
		if s.corruptedChunks[chunkIdx] {
			continue
		}
		stripeIdx := chunkIdx / s.stripeSize
		s.stripeDataCounts[stripeIdx]++
	}
	s.mu.Unlock()
}

// recordChunk stores a chunk's hash and size for later Merkle root verification.
// Returns true if this is a new chunk, false if duplicate (R3-IMP3).
func (s *streamReceiveState) recordChunk(chunkIdx int, hash [32]byte, decompSize uint32) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Duplicate detection (R3-IMP3).
	if s.receivedBitfield != nil && s.receivedBitfield.has(chunkIdx) {
		return false
	}
	if s.receivedBitfield != nil {
		s.receivedBitfield.set(chunkIdx)
	}

	s.hashes[chunkIdx] = hash
	s.sizes[chunkIdx] = decompSize
	if chunkIdx > s.maxChunkIdx {
		s.maxChunkIdx = chunkIdx
	}
	s.receivedBytes += int64(decompSize)
	return true
}

// ReceivedBytes returns the cumulative decompressed bytes received (thread-safe).
func (s *streamReceiveState) ReceivedBytes() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.receivedBytes
}

// ReceivedCount returns the number of unique data chunks the receiver has
// accumulated (thread-safe). This is the count of distinct chunkIdx values in
// the hashes map — a monotonic non-decreasing value regardless of arrival
// order. Resume restores hashes from the checkpoint, so the count includes
// chunks recovered from prior sessions.
//
// Used by the receive progress loop to drive `ChunksDone` so out-of-order
// arrivals do not jitter the counter backwards (pre-Batch-2 bug where
// `ChunksDone = sc.chunkIdx + 1` dropped from 151 to 6 when chunk 150 arrived
// before chunk 5). [B2 audit round 2]
func (s *streamReceiveState) ReceivedCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.hashes)
}

// markCorrupted records chunkIdx as having failed hash verification during
// receive. The chunk's claimed hash + decompSize are already in hashes/sizes
// (recordChunk ran before the hash check). rsReconstruct reads this set at
// trailer time, rebuilds the bytes from RS parity, verifies against the
// claimed hash, and writes to tmpFiles. [Batch 2b, B2-F32]
func (s *streamReceiveState) markCorrupted(chunkIdx int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.corruptedChunks == nil {
		s.corruptedChunks = make(map[int]bool)
	}
	s.corruptedChunks[chunkIdx] = true
}

// corruptedList returns the corrupted chunk indices in ascending order.
// Thread-safe. [Batch 2b]
func (s *streamReceiveState) corruptedList() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.corruptedChunks) == 0 {
		return nil
	}
	out := make([]int, 0, len(s.corruptedChunks))
	for idx := range s.corruptedChunks {
		out = append(out, idx)
	}
	sort.Ints(out)
	return out
}

// ReceivedBitfield returns a copy of the received chunk bitfield (thread-safe).
// Used by TS-5b retry loop to build resume request from in-memory state
// (more current than on-disk checkpoint if save failed). R3-F2.
func (s *streamReceiveState) ReceivedBitfield() *bitfield {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receivedBitfield == nil {
		return nil
	}
	cp := &bitfield{
		bits: make([]byte, len(s.receivedBitfield.bits)),
		n:    s.receivedBitfield.n,
	}
	copy(cp.bits, s.receivedBitfield.bits)
	return cp
}

// checkReceivedBytes validates cumulative received bytes against totalSize (R3-SEC4).
// Returns error if receiver has accepted more data than the declared transfer size.
func (s *streamReceiveState) checkReceivedBytes() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.receivedBytes > s.totalSize+int64(maxDecompressedChunk) {
		return fmt.Errorf("received bytes %d exceed declared total %d (infinite stream attack)", s.receivedBytes, s.totalSize)
	}
	return nil
}

// initReceivedBitfield initializes the duplicate detection bitfield after
// the expected chunk count is known (or estimated). Call before receiving chunks.
func (s *streamReceiveState) initReceivedBitfield(estimatedChunks int) {
	if estimatedChunks > maxChunkCount {
		estimatedChunks = maxChunkCount
	}
	s.receivedBitfield = newBitfield(estimatedChunks)
}

// orderedHashes returns all accumulated hashes in order [0..maxChunkIdx].
// Used for Merkle root computation after all chunks are received.
// Safe: maxChunkIdx is bounded by readStreamChunkFrame validation (R3-SEC1).
func (s *streamReceiveState) orderedHashes() [][32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := s.maxChunkIdx + 1
	result := make([][32]byte, count)
	for i := 0; i < count; i++ {
		result[i] = s.hashes[i]
	}
	return result
}

// missingChunks returns indices of chunks not received, given expected count (R3-IMP4).
// Call after trailer to provide useful error messages instead of just "Merkle mismatch".
func (s *streamReceiveState) missingChunks(expectedCount int) []int {
	s.mu.Lock()
	defer s.mu.Unlock()

	var missing []int
	for i := 0; i < expectedCount; i++ {
		if _, ok := s.hashes[i]; !ok {
			missing = append(missing, i)
		}
	}
	return missing
}

// assembleFullHashList merges received chunk hashes with sparse hashes from trailer
// for Merkle verification when selective rejection is active (N1, R3-IMP6).
func (s *streamReceiveState) assembleFullHashList(chunkCount int, sparseHashes map[int][32]byte) [][32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([][32]byte, chunkCount)
	for i := 0; i < chunkCount; i++ {
		if h, ok := s.hashes[i]; ok {
			result[i] = h
		} else if h, ok := sparseHashes[i]; ok {
			result[i] = h
		}
		// else: zero hash (will cause Merkle mismatch - caught by verification)
	}
	return result
}

// tempFileName derives the deterministic temp file name for a given file index
// and basename. Kept in one place so allocation, finalize, and orphan cleanup
// (in loadCheckpoint's version-discard path) cannot drift.
func tempFileName(idx int, path string) string {
	return fmt.Sprintf(".shurli-tmp-%d-%s", idx, filepath.Base(path))
}

// allocateTempFiles creates temp files for each accepted file entry in destDir,
// pre-allocated to size. Opens destDir as os.Root for symlink traversal defense (R3-SEC2).
// Empty files (Size=0) still get temp files so they can receive metadata (F5).
//
// Fresh-start contract: this is called only when no valid checkpoint exists
// for the current content key. O_EXCL is used unconditionally — a pre-existing
// file with the same name is either (a) an in-flight concurrent transfer's
// temp file (unsafe to remove — would corrupt the other writer's output on
// finalize/rename) or (b) an orphan from a daemon crash with no checkpoint.
// (a) is kept safe by surfacing EEXIST; case (b) requires a user-facing
// manual cleanup (pre-existing behavior). Orphans left behind by a
// silently-discarded version-mismatched checkpoint are cleaned up
// authoritatively by loadCheckpoint using that checkpoint's own tmpPaths
// (see removeCheckpointTempFiles), which never collides with a concurrent
// transfer because each contentKey has at most one checkpoint on disk.
// Resume is handled by reopenTempFiles, which is a separate code path and
// never enters this function.
func (s *streamReceiveState) allocateTempFiles(destDir string) error {
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("open dest root %s: %w", destDir, err)
	}
	s.destRoot = root

	s.tmpFiles = make([]*os.File, len(s.files))
	s.tmpPaths = make([]string, len(s.files))

	for i, fe := range s.files {
		// Skip rejected files if selective rejection is active (F2).
		if s.acceptBitfield != nil && !s.acceptBitfield.has(i) {
			continue
		}

		tmpName := tempFileName(i, fe.Path)

		tmpFile, err := root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			// Orphan temp file from a prior interrupted transfer with no
			// checkpoint (daemon crash or manual checkpoint deletion). Safe
			// to remove: no checkpoint means no concurrent writer owns it.
			// Remove and retry once before failing.
			if root.Remove(tmpName) == nil {
				tmpFile, err = root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
			}
			if err != nil {
				s.cleanup()
				return fmt.Errorf("create temp file for %s: %w", fe.Path, err)
			}
		}
		// Store immediately so cleanup can find this file on any subsequent failure.
		s.tmpFiles[i] = tmpFile
		s.tmpPaths[i] = tmpName

		// Pre-allocate for non-empty files. Empty files still need temp file for metadata (F5).
		if fe.Size > 0 {
			if err := tmpFile.Truncate(fe.Size); err != nil {
				s.cleanup()
				return fmt.Errorf("pre-allocate %s: %w", fe.Path, err)
			}
		}
	}
	return nil
}

// reopenTempFiles re-opens existing temp files after a failover cleanup.
// Unlike allocateTempFiles (which creates new files with O_EXCL), this opens
// files that already exist on disk. Used when TS-5b failover resumes a transfer
// after cleanup() closed all handles and destRoot.
func (s *streamReceiveState) reopenTempFiles(destDir string) error {
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return fmt.Errorf("reopen dest root %s: %w", destDir, err)
	}
	s.destRoot = root

	s.tmpFiles = make([]*os.File, len(s.files))
	for i, tmpName := range s.tmpPaths {
		if tmpName == "" {
			continue // rejected or not allocated
		}
		f, err := root.OpenFile(tmpName, os.O_RDWR, 0600)
		if err != nil {
			s.cleanup()
			return fmt.Errorf("reopen temp file %s: %w", tmpName, err)
		}
		s.tmpFiles[i] = f
	}
	return nil
}

// readChunkGlobal reads `length` bytes of chunk data starting at `globalOffset`
// from the receiver's on-disk tmpFiles. Inverse of writeChunkGlobal: walks the
// file slices produced by globalToLocal and assembles a single contiguous
// buffer by issuing ReadAt against each backing tmp file. Used by rsReconstruct
// to load intact stripe-mates as data shards for RS decoding. [Batch 2b, B2-F31]
//
// If any slice points at a tmp file that was rejected (s.tmpFiles[idx] == nil)
// the caller cannot assume this chunk is usable as a known data shard: the
// receiver never held those bytes. readChunkGlobal returns an explicit error
// in that case; rsReconstruct treats the chunk as a missing data shard (nil
// entry in the RS input), spending a parity slot to cover it. This keeps the
// function's contract simple ("returns the chunk's bytes exactly, or errors")
// and keeps the selective-rejection-versus-erasure interaction explicit at
// the call site.
func (s *streamReceiveState) readChunkGlobal(globalOffset int64, length int) ([]byte, error) {
	if length == 0 {
		return nil, nil
	}
	if length < 0 {
		return nil, fmt.Errorf("read chunk: negative length %d", length)
	}
	if globalOffset < 0 || globalOffset+int64(length) > s.totalSize {
		return nil, fmt.Errorf("read chunk at offset %d + size %d exceeds total size %d", globalOffset, length, s.totalSize)
	}

	slices := globalToLocal(globalOffset, length, s.files, s.cumOffsets)
	if len(slices) == 0 {
		return nil, fmt.Errorf("chunk at offset %d mapped to no file slices", globalOffset)
	}

	buf := make([]byte, length)
	covered := 0
	for _, sl := range slices {
		idx := int(sl.FileIdx)
		if idx >= len(s.tmpFiles) || s.tmpFiles[idx] == nil {
			return nil, fmt.Errorf("chunk touches unallocated file %d (selective rejection?)", idx)
		}
		if sl.DataOffset+sl.Length > length {
			return nil, fmt.Errorf("chunk slice overflow: offset %d + length %d > buffer %d", sl.DataOffset, sl.Length, length)
		}
		n, err := s.tmpFiles[idx].ReadAt(buf[sl.DataOffset:sl.DataOffset+sl.Length], sl.LocalOffset)
		if err != nil {
			return nil, fmt.Errorf("read file %d at offset %d: %w", idx, sl.LocalOffset, err)
		}
		if n != sl.Length {
			return nil, fmt.Errorf("short read file %d at offset %d: got %d want %d", idx, sl.LocalOffset, n, sl.Length)
		}
		covered += n
	}
	if covered != length {
		return nil, fmt.Errorf("slice coverage %d != requested length %d", covered, length)
	}
	return buf, nil
}

// writeChunkGlobal writes decompressed chunk data using globalToLocal mapping.
// Handles cross-file boundary chunks by splitting data across multiple temp files (N3, F1).
// Validates bounds before writing (C3: fileIdx range, C4: offset+size within totalSize).
func (s *streamReceiveState) writeChunkGlobal(fileIdx uint16, globalOffset int64, decompSize int, data []byte) error {
	// C3: validate fileIdx is within file table range.
	if int(fileIdx) >= len(s.files) {
		return fmt.Errorf("file index %d out of range (have %d files)", fileIdx, len(s.files))
	}

	// C4: validate offset+size doesn't exceed declared total (prevents disk exhaustion).
	if globalOffset+int64(decompSize) > s.totalSize {
		return fmt.Errorf("chunk at offset %d + size %d exceeds total size %d", globalOffset, decompSize, s.totalSize)
	}

	slices := globalToLocal(globalOffset, decompSize, s.files, s.cumOffsets)
	if len(slices) == 0 && decompSize > 0 {
		return fmt.Errorf("chunk at offset %d mapped to no file slices", globalOffset)
	}

	for _, sl := range slices {
		idx := int(sl.FileIdx)
		if idx >= len(s.tmpFiles) || s.tmpFiles[idx] == nil {
			// File was rejected or not allocated - skip write.
			continue
		}
		if sl.DataOffset+sl.Length > len(data) {
			return fmt.Errorf("chunk data underflow: need %d bytes at offset %d, have %d", sl.Length, sl.DataOffset, len(data))
		}
		if _, err := s.tmpFiles[idx].WriteAt(data[sl.DataOffset:sl.DataOffset+sl.Length], sl.LocalOffset); err != nil {
			return fmt.Errorf("write to file %d at offset %d: %w", sl.FileIdx, sl.LocalOffset, err)
		}
	}
	return nil
}

// finalize renames all temp files to their final paths and applies metadata.
// All operations go through destRoot for symlink traversal defense (R3-SEC2).
// Strips setuid/setgid/sticky from permissions (N6).
func (s *streamReceiveState) finalize() error {
	if s.destRoot == nil {
		return fmt.Errorf("finalize called without allocateTempFiles")
	}

	for i, fe := range s.files {
		if s.tmpFiles[i] == nil {
			continue // rejected or empty-but-unaccepted
		}

		if err := s.tmpFiles[i].Sync(); err != nil {
			return fmt.Errorf("sync %s: %w", fe.Path, err)
		}
		s.tmpFiles[i].Close()
		s.tmpFiles[i] = nil

		// Create parent directories within the root (R3-SEC2: MkdirAll on Root is safe).
		if dir := filepath.Dir(fe.Path); dir != "." {
			if err := s.destRoot.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create parent dirs for %s: %w", fe.Path, err)
			}
		}

		// Determine final name within root.
		// Bug #31 fix: if the file already exists with the same size, it is
		// almost certainly from a previous successful transfer of the same
		// content (e.g., sender falsely reported failure and user re-sent).
		// In that case, just overwrite — creating a "(1)" duplicate is never
		// what the user wants. Only add a collision suffix when the existing
		// file has a DIFFERENT size (genuinely different content, same name).
		finalPath := fe.Path
		if existInfo, err := s.destRoot.Stat(finalPath); err == nil {
			if !existInfo.Mode().IsRegular() || existInfo.Size() != fe.Size {
				// Different content, same name — find a non-colliding name.
				ext := filepath.Ext(finalPath)
				base := strings.TrimSuffix(finalPath, ext)
				found := false
				for n := 1; n < 10000; n++ {
					candidate := fmt.Sprintf("%s (%d)%s", base, n, ext)
					if _, err := s.destRoot.Stat(candidate); err != nil {
						finalPath = candidate
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("cannot find non-colliding path for %s after 10000 attempts", fe.Path)
				}
			} else {
				// Same size, same name — overwrite. The previous file is from a
				// prior successful transfer of the same content (e.g., sender saw
				// false failure and user re-sent). Rename atomically replaces.
				slog.Info("file-transfer: overwriting existing file (same size, re-delivery)",
					"path", fe.Path, "size", fe.Size)
			}
		}

		// Rename within root (R3-SEC2: safe, cannot escape).
		if err := s.destRoot.Rename(s.tmpPaths[i], finalPath); err != nil {
			return fmt.Errorf("rename %s -> %s: %w", s.tmpPaths[i], finalPath, err)
		}
		s.tmpPaths[i] = "" // mark as renamed

		// Apply metadata (F3).
		if fe.MetaFlags&metaHasMode != 0 {
			// Strip setuid/setgid/sticky bits for security (N6).
			s.destRoot.Chmod(finalPath, os.FileMode(fe.Mode&0777))
		} else {
			s.destRoot.Chmod(finalPath, 0644)
		}
		if fe.MetaFlags&metaHasMtime != 0 {
			mtime := unixToTime(fe.Mtime)
			s.destRoot.Chtimes(finalPath, mtime, mtime)
		}
	}
	return nil
}

// cleanup closes file handles and optionally removes temp files. Safe to call multiple times.
// MUST be called via defer in the receive path to prevent orphan leaks on ungraceful shutdown.
// When keepTempFiles is true (checkpoint was saved), only closes handles and destRoot
// but leaves temp files on disk for the next session's resume. [R4-SEC2]
func (s *streamReceiveState) cleanup() {
	for i := range s.tmpFiles {
		if s.tmpFiles[i] != nil {
			if err := s.tmpFiles[i].Close(); err != nil {
				slog.Debug("file-transfer: cleanup close error", "file", s.tmpPaths[i], "err", err)
			}
			s.tmpFiles[i] = nil
		}
		if s.tmpPaths[i] != "" && !s.keepTempFiles {
			if s.destRoot != nil {
				if err := s.destRoot.Remove(s.tmpPaths[i]); err != nil {
					slog.Debug("file-transfer: cleanup remove error", "file", s.tmpPaths[i], "err", err)
				}
			}
			s.tmpPaths[i] = ""
		}
	}
	if s.destRoot != nil {
		s.destRoot.Close()
		s.destRoot = nil
	}
}

// --- Shared chunk processing [Q1 fix: single implementation for streamingReceive + receiveParallel] ---

// processIncomingChunk handles a single streaming chunk from any source (control stream or worker).
// This is the canonical chunk processing path. receiveParallel calls this for all chunk sources
// (control stream + worker streams) instead of duplicating the logic (Q1 fix).
//
// Returns true if the chunk was new and processed, false if duplicate.
// Handles parity chunks (fileIdx == parityFileIdx) by storing them separately (S1 fix).
func (s *streamReceiveState) processIncomingChunk(sc streamChunk) (bool, error) {
	// Parity chunk detection (S1 fix). Parity chunks have fileIdx == parityFileIdx sentinel.
	// They don't map to any file and must NOT be written via writeChunkGlobal.
	if sc.fileIdx == parityFileIdx {
		// [B2-F12] Bound parity chunkIdx to maxParityCount.
		if sc.chunkIdx < 0 || sc.chunkIdx >= maxParityCount {
			return false, fmt.Errorf("parity chunk index %d out of range [0..%d)", sc.chunkIdx, maxParityCount)
		}
		hash := blake3Hash(sc.data)
		if hash != sc.hash {
			return false, fmt.Errorf("parity chunk %d hash mismatch", sc.chunkIdx)
		}

		// Per-stripe routing (Option C). Compute stripe from global parity index.
		if s.parityPerFullStripe <= 0 {
			return false, fmt.Errorf("parity chunk %d arrived but no stripe config", sc.chunkIdx)
		}
		stripeIdx := sc.chunkIdx / s.parityPerFullStripe
		localIdx := sc.chunkIdx % s.parityPerFullStripe

		// Validate stripe index (OC-F22).
		maxStripeIdx := maxChunkCount/s.stripeSize + 1
		if stripeIdx >= maxStripeIdx {
			return false, fmt.Errorf("parity stripe index %d out of range (max %d)", stripeIdx, maxStripeIdx)
		}

		s.mu.Lock()
		// Check if stripe is already done or mid-reconstruction (OC-F19).
		// Late parity for a done stripe is silently dropped. Parity arriving
		// during active reconstruction is also dropped — reconstructSingleStripe
		// already has a snapshot of parity shards and won't see new entries.
		// [Self-audit round 2 fix: added slot.reconstructing check]
		slot := s.paritySlots[stripeIdx]
		if slot != nil && (slot.done || slot.reconstructing) {
			s.totalParityReceived++ // wire accounting (OC-F20)
			s.mu.Unlock()
			return true, nil
		}

		// Inflight cap check — count active (not done) slots.
		if slot == nil {
			activeSlots := 0
			for _, sl := range s.paritySlots {
				if !sl.done {
					activeSlots++
				}
			}
			if activeSlots >= s.maxInflightStripes {
				s.mu.Unlock()
				return false, fmt.Errorf("parity inflight stripe limit %d exceeded", s.maxInflightStripes)
			}
			slot = &paritySlot{parity: make(map[int][]byte)}
			s.paritySlots[stripeIdx] = slot
		}

		// Duplicate check within slot (B2-F2).
		if _, exists := slot.parity[localIdx]; exists {
			s.mu.Unlock()
			return false, fmt.Errorf("duplicate parity chunk %d (stripe %d)", sc.chunkIdx, stripeIdx)
		}

		// Global parity budget (OC-F12 belt-and-braces).
		if s.totalParityBytes+int64(len(sc.data)) > maxParityBudgetBytes {
			s.mu.Unlock()
			return false, fmt.Errorf("parity bytes %d would exceed budget %d",
				s.totalParityBytes+int64(len(sc.data)), maxParityBudgetBytes)
		}

		slot.parity[localIdx] = sc.data
		slot.bytes += int64(len(sc.data))
		s.totalParityBytes += int64(len(sc.data))
		s.totalParityReceived++
		parityComplete := len(slot.parity) >= s.parityPerFullStripe
		s.mu.Unlock()

		// Check eager reconstruction trigger (parity side).
		if parityComplete {
			s.tryEagerReconstruct(stripeIdx)
		}
		return true, nil
	}

	// --- Data chunk path ---

	// Duplicate detection (R3-IMP3).
	isNew := s.recordChunk(sc.chunkIdx, sc.hash, sc.decompSize)
	if !isNew {
		return false, nil
	}

	// Received bytes safety check (R3-SEC4).
	if checkErr := s.checkReceivedBytes(); checkErr != nil {
		return false, checkErr
	}

	// Decompress if needed.
	chunkData := sc.data
	if s.compressed && len(sc.data) < int(sc.decompSize) {
		decompressed, decompErr := decompressChunk(sc.data, int(sc.decompSize))
		if decompErr != nil {
			slog.Debug("file-transfer: decompression failed, using raw",
				"chunk", sc.chunkIdx, "error", decompErr)
		} else {
			chunkData = decompressed
		}
	}

	// Verify hash.
	hash := blake3Hash(chunkData)
	if hash != sc.hash {
		// [Batch 2b, B2-F32] Erasure-recoverable: mark corrupted, don't write bad data.
		if !s.hasErasure {
			return false, fmt.Errorf("chunk %d hash mismatch: corrupted (no erasure parity)", sc.chunkIdx)
		}
		s.markCorrupted(sc.chunkIdx)

		// Track per-stripe data count even for corrupted chunks (OC-F4).
		if s.stripeSize > 0 {
			stripeIdx := sc.chunkIdx / s.stripeSize
			s.mu.Lock()
			s.stripeDataCounts[stripeIdx]++
			dataCount := s.stripeDataCounts[stripeIdx]
			s.mu.Unlock()
			if dataCount == s.stripeSize {
				s.tryEagerReconstruct(stripeIdx)
			}
		}
		return true, nil
	}

	// Write via globalToLocal mapping (N3, F1). OC-F17: write completes
	// BEFORE incrementing stripeDataCounts so reconstruction reads stable data.
	if writeErr := s.writeChunkGlobal(sc.fileIdx, sc.offset, len(chunkData), chunkData); writeErr != nil {
		return false, fmt.Errorf("write chunk %d: %w", sc.chunkIdx, writeErr)
	}

	// Per-stripe data count tracking + eager trigger (Option C).
	if s.stripeSize > 0 {
		stripeIdx := sc.chunkIdx / s.stripeSize
		s.mu.Lock()
		s.stripeDataCounts[stripeIdx]++
		dataCount := s.stripeDataCounts[stripeIdx]
		s.mu.Unlock()
		if dataCount == s.stripeSize {
			s.tryEagerReconstruct(stripeIdx)
		}
	}

	return true, nil
}

// tryEagerReconstruct checks if a full stripe is ready for eager per-stripe
// reconstruction and executes it if so. For clean stripes (no corruption),
// frees parity immediately in O(1). For corrupted stripes, runs RS decode
// (~200ms at tier-5) then frees parity. [Option C, OC-F9]
//
// Safe to call from multiple goroutines; OC-F18 reconstructing flag prevents
// double entry. Errors are logged but do not fail the transfer — trailer
// sweep retries any stripes that failed eager reconstruction.
func (s *streamReceiveState) tryEagerReconstruct(stripeIdx int) {
	s.mu.Lock()
	dataCount := s.stripeDataCounts[stripeIdx]
	slot := s.paritySlots[stripeIdx]

	// Both conditions required: full stripe of data AND full parity.
	if dataCount < s.stripeSize || slot == nil || len(slot.parity) < s.parityPerFullStripe {
		s.mu.Unlock()
		return
	}
	if slot.done || slot.reconstructing {
		s.mu.Unlock()
		return
	}

	// Check if any corruption exists in this stripe.
	stripeStart := stripeIdx * s.stripeSize
	stripeEnd := stripeStart + s.stripeSize
	hasCorruption := false
	for idx := range s.corruptedChunks {
		if idx >= stripeStart && idx < stripeEnd {
			hasCorruption = true
			break
		}
	}

	if !hasCorruption {
		// Clean stripe — free parity immediately. [OC-F13]
		s.totalParityBytes -= slot.bytes
		slot.parity = nil
		slot.bytes = 0
		slot.done = true
		s.mu.Unlock()
		return
	}

	// Mark as reconstructing to prevent double entry (OC-F18).
	slot.reconstructing = true
	s.mu.Unlock()

	// Reconstruct (potentially expensive). Use reusable encoder for full stripes (R5).
	err := s.reconstructSingleStripe(stripeIdx, stripeStart, stripeEnd, slot, s.rsFullStripeEnc)

	s.mu.Lock()
	slot.reconstructing = false
	if err != nil {
		// Don't mark done — trailer sweep will retry.
		s.mu.Unlock()
		slog.Warn("file-transfer: eager reconstruction failed, deferring to trailer",
			"stripe", stripeIdx, "error", err)
		return
	}
	// Free parity after successful reconstruction. [OC-F13]
	s.totalParityBytes -= slot.bytes
	slot.parity = nil
	slot.bytes = 0
	slot.done = true
	s.mu.Unlock()
}

// --- Content-based resume key [R3-IMP5, R4-IMP2] ---

// contentKey computes a deterministic key for checkpoint matching across sessions.
// Key = BLAKE3(fileCount + [pathLen + path + size]*N). Length-prefixed to prevent
// ambiguous concatenation. Independent of transferID (which is random per session).
func contentKey(files []fileEntry) [32]byte {
	h := blake3.New()
	// Files are already sorted by path (sortFileTable sorts them, readHeader preserves order).
	var countBuf [4]byte
	binary.BigEndian.PutUint32(countBuf[:], uint32(len(files)))
	h.Write(countBuf[:])
	for _, f := range files {
		pathBytes := []byte(f.Path)
		var pathLenBuf [2]byte
		binary.BigEndian.PutUint16(pathLenBuf[:], uint16(len(pathBytes)))
		h.Write(pathLenBuf[:])
		h.Write(pathBytes)
		var sizeBuf [8]byte
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(f.Size))
		h.Write(sizeBuf[:])
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}

// --- Helper: unix timestamp conversion ---

func unixToTime(secs int64) time.Time {
	return time.Unix(secs, 0)
}
