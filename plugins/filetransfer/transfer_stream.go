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

	"github.com/zeebo/blake3"
)

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

	// Producer channel buffer: how many chunks can be buffered between disk reader and network sender.
	producerChanBuffer = 16

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
	rawForRS      [][]byte         // raw chunk data for erasure coding (nil if not needed)
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
func sortFileTable(files []fileEntry, filePaths []string) error {
	if len(files) != len(filePaths) {
		return fmt.Errorf("file table / path count mismatch: %d vs %d", len(files), len(filePaths))
	}

	// Build index for synchronized sort.
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
func writeHeader(w io.Writer, files []fileEntry, flags uint8, totalSize int64, transferID [32]byte) error {
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

	return nil
}

// readHeader reads an SHFT streaming header from r.
// Returns the file table, total size, flags, transfer ID, and precomputed cumulative offsets.
// Wraps reader in LimitedReader to prevent header bomb attacks (R3-SEC3).
func readHeader(r io.Reader) ([]fileEntry, int64, uint8, [32]byte, []int64, error) {
	var zeroID [32]byte

	// Wrap in LimitedReader to enforce maxHeaderSize during parsing (R3-SEC3).
	lr := &io.LimitedReader{R: r, N: maxHeaderSize}

	// Read prefix: magic(4) + version(1) + flags(1) + fileCount(2) + totalSize(8) + transferID(32).
	var prefix [48]byte
	if _, err := io.ReadFull(lr, prefix[:]); err != nil {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("read header prefix: %w", err)
	}

	if prefix[0] != shftMagic0 || prefix[1] != shftMagic1 ||
		prefix[2] != shftMagic2 || prefix[3] != shftMagic3 {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("invalid magic bytes: not SHFT")
	}
	if prefix[4] != shftVersion {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("unsupported SHFT version: %d (expected %d)", prefix[4], shftVersion)
	}

	flags := prefix[5]
	fileCount := int(binary.BigEndian.Uint16(prefix[6:8]))
	totalSize := int64(binary.BigEndian.Uint64(prefix[8:16]))
	var transferID [32]byte
	copy(transferID[:], prefix[16:48])

	if fileCount == 0 {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("empty file table")
	}
	if fileCount > maxFileCount {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("too many files: %d (max %d)", fileCount, maxFileCount)
	}
	if totalSize < 0 || totalSize > maxTotalTransferSize {
		return nil, 0, 0, zeroID, nil, fmt.Errorf("invalid total size: %d", totalSize)
	}

	// Read file table from LimitedReader.
	files := make([]fileEntry, fileCount)
	caseInsensitive := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	seenPaths := make(map[string]bool, fileCount)
	var sumSize int64

	for i := 0; i < fileCount; i++ {
		var pathLenBuf [2]byte
		if _, err := io.ReadFull(lr, pathLenBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d path length: %w", i, err)
		}
		pathLen := int(binary.BigEndian.Uint16(pathLenBuf[:]))
		if pathLen > maxFilenameLen {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("file %d path too long: %d", i, pathLen)
		}

		pathBuf := make([]byte, pathLen)
		if _, err := io.ReadFull(lr, pathBuf); err != nil {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d path: %w", i, err)
		}

		var sizeBuf [8]byte
		if _, err := io.ReadFull(lr, sizeBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d size: %w", i, err)
		}
		fSize := int64(binary.BigEndian.Uint64(sizeBuf[:]))

		// Per-file size validation (R4-IMP1).
		// Check fSize >= 0: uint64 > MaxInt64 wraps to negative int64.
		if fSize < 0 || fSize > maxFileSize {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("file %d invalid size: %d", i, fSize)
		}

		// Read metadata flags (F3).
		var metaBuf [1]byte
		if _, err := io.ReadFull(lr, metaBuf[:]); err != nil {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d meta flags: %w", i, err)
		}
		metaFlags := metaBuf[0]

		var mode uint32
		if metaFlags&metaHasMode != 0 {
			var modeBuf [4]byte
			if _, err := io.ReadFull(lr, modeBuf[:]); err != nil {
				return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d mode: %w", i, err)
			}
			mode = binary.BigEndian.Uint32(modeBuf[:])
		}

		var mtime int64
		if metaFlags&metaHasMtime != 0 {
			var mtimeBuf [8]byte
			if _, err := io.ReadFull(lr, mtimeBuf[:]); err != nil {
				return nil, 0, 0, zeroID, nil, fmt.Errorf("read file %d mtime: %w", i, err)
			}
			mtime = int64(binary.BigEndian.Uint64(mtimeBuf[:]))
		}

		// Sanitize path.
		sanitized := sanitizeRelativePath(string(pathBuf))
		if sanitized == "" {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("file %d path empty after sanitization", i)
		}

		// Duplicate path detection (I10) + case-insensitive on darwin/windows (R3-SEC7).
		pathKey := sanitized
		if caseInsensitive {
			pathKey = strings.ToLower(sanitized)
		}
		if seenPaths[pathKey] {
			return nil, 0, 0, zeroID, nil, fmt.Errorf("duplicate path in file table: %s (file %d)", sanitized, i)
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
		return nil, 0, 0, zeroID, nil, fmt.Errorf("file sizes sum %d does not match header totalSize %d", sumSize, totalSize)
	}

	// Precompute cumulative offsets for globalToLocal lookups.
	cumOffsets := computeCumulativeOffsets(files)

	return files, totalSize, flags, transferID, cumOffsets, nil
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
//	[if erasure: + stripeSize(2) + parityCount(4) + parityHashes(N*32) + paritySizes(N*4)]
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

	// Erasure fields follow if present.
	if erasure != nil {
		if err := writeErasureManifest(w, erasure.StripeSize, erasure.ParityCount,
			erasure.ParityHashes, erasure.ParitySizes); err != nil {
			return fmt.Errorf("write erasure trailer: %w", err)
		}
	}

	return nil
}

// erasureTrailer holds erasure coding metadata for the trailer.
type erasureTrailer struct {
	StripeSize   int
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
		ss, ph, ps, readErr := readErasureManifest(r)
		if readErr != nil {
			return 0, [32]byte{}, nil, nil, fmt.Errorf("read erasure trailer: %w", readErr)
		}
		erasure = &erasureTrailer{
			StripeSize:   ss,
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
// If bufferForErasure is true, raw (decompressed) chunk data is saved for RS encoding.
func chunkProducer(
	ctx context.Context,
	files []fileEntry,
	filePaths []string,
	cumOffsets []int64,
	totalSize int64,
	useCompression bool,
	bufferForErasure bool,
	skip *bitfield,
	acceptBitfield *bitfield,
	ch chan<- streamChunk,
	done chan<- producerResult,
) {
	var (
		chunkHashes    [][32]byte
		chunkSizes     []uint32
		rawForRS       [][]byte
		anyCompressed  bool
		skippedHashes  map[int][32]byte // chunk hashes for selectively skipped chunks
	)

	// Cache selective rejection state outside the hot loop.
	hasSelectiveRejection := acceptBitfield != nil && !isFullAccept(acceptBitfield)
	if hasSelectiveRejection {
		skippedHashes = make(map[int][32]byte)
	}

	// Build multiFileReader for cross-file CDC (F1, N9).
	mfr := newMultiFileReader(files, filePaths, cumOffsets)
	defer mfr.Close()

	incompressibleCount := 0
	probeCount := 0
	globalChunkIdx := 0

	err := ChunkReader(mfr, totalSize, func(c Chunk) error {
		hash := c.Hash
		chunkHashes = append(chunkHashes, hash)
		chunkSizes = append(chunkSizes, uint32(len(c.Data)))

		if bufferForErasure {
			raw := make([]byte, len(c.Data))
			copy(raw, c.Data)
			rawForRS = append(rawForRS, raw)
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

	// Close the chunk channel to signal consumers (streamingSend, sendParallel)
	// that all chunks have been sent. Without this, `for sc := range ch` blocks
	// forever after the last chunk. Close BEFORE sending result to done so
	// consumers exit their range loop before reading the result.
	close(ch)

	result := producerResult{
		chunkHashes:   chunkHashes,
		chunkSizes:    chunkSizes,
		anyCompressed: anyCompressed,
		rawForRS:      rawForRS,
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

	// Parity chunk storage. Parity chunks (fileIdx == parityFileIdx) are accumulated
	// here instead of being written to temp files. Used for RS reconstruction.
	// Budget enforced: max parityCount entries, max parityBytes total.
	parityData  map[int][]byte // parityIdx -> raw parity data
	parityBytes int64          // cumulative parity bytes stored (memory budget)

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

// allocateTempFiles creates temp files for each accepted file entry in destDir,
// pre-allocated to size. Opens destDir as os.Root for symlink traversal defense (R3-SEC2).
// Empty files (Size=0) still get temp files so they can receive metadata (F5).
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

		// Generate temp file name relative to destRoot.
		tmpName := fmt.Sprintf(".shurli-tmp-%d-%s", i, filepath.Base(fe.Path))

		tmpFile, err := root.OpenFile(tmpName, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			s.cleanup()
			return fmt.Errorf("create temp file for %s: %w", fe.Path, err)
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

		// Determine non-colliding final name within root.
		finalPath := fe.Path
		if _, err := s.destRoot.Stat(finalPath); err == nil {
			// Path exists, find a non-colliding name.
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
		hash := blake3Hash(sc.data)
		if hash != sc.hash {
			return false, fmt.Errorf("parity chunk %d hash mismatch", sc.chunkIdx)
		}
		s.mu.Lock()
		if s.parityData == nil {
			s.parityData = make(map[int][]byte)
		}
		// Enforce parity budget: count bounded by maxChunkCount/2 (matches sender's
		// maxParityCount), bytes bounded by totalSize/2 + 8MB tolerance. Without this,
		// a malicious sender can exhaust receiver memory by flooding parity-flagged chunks
		// (each up to 4MB, up to 1M indices = 4TB addressable).
		if len(s.parityData) >= maxChunkCount/2 {
			s.mu.Unlock()
			return false, fmt.Errorf("parity chunk count %d exceeds limit %d", len(s.parityData), maxChunkCount/2)
		}
		parityBudget := s.totalSize/2 + int64(maxDecompressedChunk)
		if s.parityBytes+int64(len(sc.data)) > parityBudget {
			s.mu.Unlock()
			return false, fmt.Errorf("parity bytes %d would exceed budget %d", s.parityBytes+int64(len(sc.data)), parityBudget)
		}
		s.parityData[sc.chunkIdx] = sc.data
		s.parityBytes += int64(len(sc.data))
		// DO NOT store parity hashes in s.hashes - that map is used for Merkle root
		// computation which must only include data chunk hashes. Parity hashes stored
		// separately in parityData are verified via the erasure trailer.
		s.mu.Unlock()
		return true, nil
	}

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
		// TODO(RS): When RS reconstruction is wired, track corrupted indices
		// here instead of failing. For now, fail on any hash mismatch.
		return false, fmt.Errorf("chunk %d hash mismatch: corrupted", sc.chunkIdx)
	}

	// Write via globalToLocal mapping (N3, F1).
	if writeErr := s.writeChunkGlobal(sc.fileIdx, sc.offset, len(chunkData), chunkData); writeErr != nil {
		return false, fmt.Errorf("write chunk %d: %w", sc.chunkIdx, writeErr)
	}

	return true, nil
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
