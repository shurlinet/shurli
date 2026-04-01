package sdk

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// --- Bitfield ---

// bitfield is a packed bit array tracking which chunks have been received.
// Each bit corresponds to a chunk index. Bit N is set when chunk N is received.
type bitfield struct {
	bits []byte
	n    int // total number of chunks
}

// newBitfield creates a zero-initialized bitfield for n chunks.
func newBitfield(n int) *bitfield {
	return &bitfield{
		bits: make([]byte, (n+7)/8),
		n:    n,
	}
}

// set marks chunk index as received.
func (bf *bitfield) set(index int) {
	if index >= 0 && index < bf.n {
		bf.bits[index/8] |= 1 << (uint(index) % 8)
	}
}

// has returns true if chunk index has been received.
func (bf *bitfield) has(index int) bool {
	if index < 0 || index >= bf.n {
		return false
	}
	return bf.bits[index/8]&(1<<(uint(index)%8)) != 0
}

// count returns the number of set bits (received chunks).
func (bf *bitfield) count() int {
	c := 0
	for _, b := range bf.bits {
		c += popcount8(b)
	}
	return c
}

// popcount8 counts set bits in a byte.
func popcount8(b byte) int {
	// Brian Kernighan's bit counting.
	c := 0
	for b != 0 {
		b &= b - 1
		c++
	}
	return c
}

// missing returns the number of chunks not yet received.
func (bf *bitfield) missing() int {
	return bf.n - bf.count()
}

// --- Checkpoint ---

// Checkpoint magic: "SHCK" (Shurli Checkpoint).
const (
	ckptMagic0  = 'S'
	ckptMagic1  = 'H'
	ckptMagic2  = 'C'
	ckptMagic3  = 'K'
	ckptVersion = 0x01

	maxBitfieldSize = maxChunkCount/8 + 1 // ~128 KB for 1M chunks

	// checkpointSaveInterval is the minimum time between periodic checkpoint saves
	// during a transfer. Prevents excessive disk I/O on fast LANs where chunks
	// arrive every few milliseconds.
	checkpointSaveInterval = 5 * time.Second
)

// transferCheckpoint represents a resumable transfer state on disk.
// Keyed by contentKey (BLAKE3 of sorted file paths + sizes) for cross-session
// resume (R3-IMP5, R4-IMP2). Stores streaming protocol state: file table,
// received bitfield, per-chunk hashes and sizes, temp file paths.
type transferCheckpoint struct {
	contentKey [32]byte     // BLAKE3 of sorted paths + sizes (resume key)
	files      []fileEntry  // file table from header
	totalSize  int64        // declared total size
	flags      uint8        // compression, erasure flags
	have       *bitfield    // which chunks have been received
	hashes     [][32]byte   // per-chunk hashes (dense, indexed by chunkIdx)
	sizes      []uint32     // per-chunk decompressed sizes (dense)
	tmpPaths   []string     // temp file paths per file entry (base name only)
}

// checkpointPath returns the checkpoint file path for a given content key.
// Uses contentKey (not rootHash) so checkpoints survive across sessions (R3-IMP5).
func checkpointPath(receiveDir string, ck [32]byte) string {
	h := fmt.Sprintf("%x", ck[:8]) // 16 hex chars
	return filepath.Join(receiveDir, ".shurli-ckpt-"+h)
}

// saveCheckpoint persists the checkpoint to disk atomically via tmp+rename (N10).
func (c *transferCheckpoint) save(receiveDir string) error {
	finalPath := checkpointPath(receiveDir, c.contentKey)
	tmpPath := finalPath + ".tmp"

	f, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create checkpoint tmp: %w", err)
	}

	closed := false
	success := false
	defer func() {
		if !closed {
			f.Close()
		}
		if !success {
			os.Remove(tmpPath)
		}
	}()

	// Section 1: Magic + version.
	header := [5]byte{ckptMagic0, ckptMagic1, ckptMagic2, ckptMagic3, ckptVersion}
	if _, err := f.Write(header[:]); err != nil {
		return err
	}

	// Section 2: Content key.
	if _, err := f.Write(c.contentKey[:]); err != nil {
		return err
	}

	// Section 3: Transfer metadata (flags + totalSize + file table).
	var metaBuf [9]byte // flags(1) + totalSize(8)
	metaBuf[0] = c.flags
	binary.BigEndian.PutUint64(metaBuf[1:9], uint64(c.totalSize))
	if _, err := f.Write(metaBuf[:]); err != nil {
		return err
	}

	// File count + file table entries.
	if err := writeCheckpointFileTable(f, c.files); err != nil {
		return fmt.Errorf("write file table: %w", err)
	}

	// Section 4: Chunk state.
	chunkCount := len(c.hashes)
	var chunkBuf [4]byte
	binary.BigEndian.PutUint32(chunkBuf[:], uint32(chunkCount))
	if _, err := f.Write(chunkBuf[:]); err != nil {
		return err
	}

	// Bitfield.
	bfBytes := c.have.bits
	var bfLen [4]byte
	binary.BigEndian.PutUint32(bfLen[:], uint32(len(bfBytes)))
	if _, err := f.Write(bfLen[:]); err != nil {
		return err
	}
	if _, err := f.Write(bfBytes); err != nil {
		return err
	}

	// Per-chunk hashes (dense array, chunkCount * 32 bytes).
	for i := 0; i < chunkCount; i++ {
		if _, err := f.Write(c.hashes[i][:]); err != nil {
			return fmt.Errorf("write hash %d: %w", i, err)
		}
	}

	// Per-chunk sizes (dense array, chunkCount * 4 bytes).
	var sizeBuf [4]byte
	for i := 0; i < chunkCount; i++ {
		binary.BigEndian.PutUint32(sizeBuf[:], c.sizes[i])
		if _, err := f.Write(sizeBuf[:]); err != nil {
			return fmt.Errorf("write size %d: %w", i, err)
		}
	}

	// Section 5: Temp file paths.
	if err := writeCheckpointTmpPaths(f, c.tmpPaths); err != nil {
		return fmt.Errorf("write tmp paths: %w", err)
	}

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync checkpoint: %w", err)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close checkpoint tmp: %w", err)
	}
	closed = true

	// Atomic rename (N10).
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename checkpoint: %w", err)
	}

	success = true
	return nil
}

// writeCheckpointFileTable writes the file table to a checkpoint.
// Format: fileCount(2) + [pathLen(2) + path(var) + size(8) + metaFlags(1) + [mode(4)] + [mtime(8)]] * N
func writeCheckpointFileTable(w io.Writer, files []fileEntry) error {
	var countBuf [2]byte
	binary.BigEndian.PutUint16(countBuf[:], uint16(len(files)))
	if _, err := w.Write(countBuf[:]); err != nil {
		return err
	}

	for i, f := range files {
		pathBytes := []byte(f.Path)
		var pathLenBuf [2]byte
		binary.BigEndian.PutUint16(pathLenBuf[:], uint16(len(pathBytes)))
		if _, err := w.Write(pathLenBuf[:]); err != nil {
			return fmt.Errorf("file %d path len: %w", i, err)
		}
		if _, err := w.Write(pathBytes); err != nil {
			return fmt.Errorf("file %d path: %w", i, err)
		}
		var sizeBuf [8]byte
		binary.BigEndian.PutUint64(sizeBuf[:], uint64(f.Size))
		if _, err := w.Write(sizeBuf[:]); err != nil {
			return fmt.Errorf("file %d size: %w", i, err)
		}
		metaBuf := [1]byte{f.MetaFlags}
		if _, err := w.Write(metaBuf[:]); err != nil {
			return fmt.Errorf("file %d meta: %w", i, err)
		}
		if f.MetaFlags&metaHasMode != 0 {
			var modeBuf [4]byte
			binary.BigEndian.PutUint32(modeBuf[:], f.Mode)
			if _, err := w.Write(modeBuf[:]); err != nil {
				return fmt.Errorf("file %d mode: %w", i, err)
			}
		}
		if f.MetaFlags&metaHasMtime != 0 {
			var mtimeBuf [8]byte
			binary.BigEndian.PutUint64(mtimeBuf[:], uint64(f.Mtime))
			if _, err := w.Write(mtimeBuf[:]); err != nil {
				return fmt.Errorf("file %d mtime: %w", i, err)
			}
		}
	}
	return nil
}

// writeCheckpointTmpPaths writes temp file paths to a checkpoint.
// Format: count(2) + [fileIdx(2) + pathLen(2) + path(var)] * N
// Only writes entries where tmpPaths[i] != "" (allocated files).
func writeCheckpointTmpPaths(w io.Writer, tmpPaths []string) error {
	// Count non-empty paths.
	var count int
	for _, p := range tmpPaths {
		if p != "" {
			count++
		}
	}

	var countBuf [2]byte
	binary.BigEndian.PutUint16(countBuf[:], uint16(count))
	if _, err := w.Write(countBuf[:]); err != nil {
		return err
	}

	for i, p := range tmpPaths {
		if p == "" {
			continue
		}
		baseName := filepath.Base(p)
		nameBytes := []byte(baseName)

		var entry [4]byte // fileIdx(2) + pathLen(2)
		binary.BigEndian.PutUint16(entry[0:2], uint16(i))
		binary.BigEndian.PutUint16(entry[2:4], uint16(len(nameBytes)))
		if _, err := w.Write(entry[:]); err != nil {
			return fmt.Errorf("tmp path %d header: %w", i, err)
		}
		if _, err := w.Write(nameBytes); err != nil {
			return fmt.Errorf("tmp path %d name: %w", i, err)
		}
	}
	return nil
}

// loadCheckpoint reads a checkpoint from disk. Returns os.ErrNotExist if none.
// Detects old checkpoint format (pre-streaming) by checking magic bytes and
// discards them gracefully (R3-IMP7, I2).
func loadCheckpoint(receiveDir string, ck [32]byte) (*transferCheckpoint, error) {
	path := checkpointPath(receiveDir, ck)

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Section 1: Magic + version.
	var header [5]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, fmt.Errorf("read checkpoint header: %w", err)
	}
	if header[0] != ckptMagic0 || header[1] != ckptMagic1 ||
		header[2] != ckptMagic2 || header[3] != ckptMagic3 {
		// Old format or corrupt file. Discard (I2).
		f.Close()
		os.Remove(path)
		return nil, os.ErrNotExist
	}
	if header[4] != ckptVersion {
		// Future version we can't read. Discard.
		f.Close()
		os.Remove(path)
		return nil, os.ErrNotExist
	}

	// Section 2: Content key.
	var storedKey [32]byte
	if _, err := io.ReadFull(f, storedKey[:]); err != nil {
		return nil, fmt.Errorf("read content key: %w", err)
	}
	if storedKey != ck {
		return nil, fmt.Errorf("checkpoint content key mismatch")
	}

	// Section 3: Transfer metadata.
	var metaBuf [9]byte
	if _, err := io.ReadFull(f, metaBuf[:]); err != nil {
		return nil, fmt.Errorf("read metadata: %w", err)
	}
	flags := metaBuf[0]
	totalSize := int64(binary.BigEndian.Uint64(metaBuf[1:9]))

	// File table.
	files, err := readCheckpointFileTable(f)
	if err != nil {
		return nil, fmt.Errorf("read file table: %w", err)
	}

	// Verify contentKey matches the file table (defense against corrupted checkpoint).
	computedKey := contentKey(files)
	if computedKey != ck {
		return nil, fmt.Errorf("checkpoint file table does not match content key")
	}

	// Section 4: Chunk state.
	var chunkCountBuf [4]byte
	if _, err := io.ReadFull(f, chunkCountBuf[:]); err != nil {
		return nil, fmt.Errorf("read chunk count: %w", err)
	}
	chunkCount := int(binary.BigEndian.Uint32(chunkCountBuf[:]))
	if chunkCount > maxChunkCount {
		return nil, fmt.Errorf("checkpoint chunk count too large: %d", chunkCount)
	}

	// Bitfield.
	var bfLen [4]byte
	if _, err := io.ReadFull(f, bfLen[:]); err != nil {
		return nil, fmt.Errorf("read bitfield length: %w", err)
	}
	bfSize := int(binary.BigEndian.Uint32(bfLen[:]))
	if bfSize > maxBitfieldSize {
		return nil, fmt.Errorf("bitfield too large: %d", bfSize)
	}
	bfData := make([]byte, bfSize)
	if _, err := io.ReadFull(f, bfData); err != nil {
		return nil, fmt.Errorf("read bitfield: %w", err)
	}

	// Reconstruct bitfield with proper size.
	have := newBitfield(chunkCount)
	copy(have.bits, bfData)

	// Per-chunk hashes.
	hashes := make([][32]byte, chunkCount)
	for i := 0; i < chunkCount; i++ {
		if _, err := io.ReadFull(f, hashes[i][:]); err != nil {
			return nil, fmt.Errorf("read hash %d: %w", i, err)
		}
	}

	// Per-chunk sizes.
	sizes := make([]uint32, chunkCount)
	var sizeBuf [4]byte
	for i := 0; i < chunkCount; i++ {
		if _, err := io.ReadFull(f, sizeBuf[:]); err != nil {
			return nil, fmt.Errorf("read size %d: %w", i, err)
		}
		sizes[i] = binary.BigEndian.Uint32(sizeBuf[:])
	}

	// Section 5: Temp file paths.
	tmpPaths, err := readCheckpointTmpPaths(f, len(files), receiveDir)
	if err != nil {
		return nil, fmt.Errorf("read tmp paths: %w", err)
	}

	return &transferCheckpoint{
		contentKey: ck,
		files:      files,
		totalSize:  totalSize,
		flags:      flags,
		have:       have,
		hashes:     hashes,
		sizes:      sizes,
		tmpPaths:   tmpPaths,
	}, nil
}

// readCheckpointFileTable reads the file table from a checkpoint.
func readCheckpointFileTable(r io.Reader) ([]fileEntry, error) {
	var countBuf [2]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, fmt.Errorf("read file count: %w", err)
	}
	fileCount := int(binary.BigEndian.Uint16(countBuf[:]))
	if fileCount > maxFileCount {
		return nil, fmt.Errorf("file count too large: %d", fileCount)
	}

	files := make([]fileEntry, fileCount)
	for i := 0; i < fileCount; i++ {
		var pathLenBuf [2]byte
		if _, err := io.ReadFull(r, pathLenBuf[:]); err != nil {
			return nil, fmt.Errorf("file %d path len: %w", i, err)
		}
		pathLen := int(binary.BigEndian.Uint16(pathLenBuf[:]))
		if pathLen > maxFilenameLen {
			return nil, fmt.Errorf("file %d path too long: %d", i, pathLen)
		}
		pathBuf := make([]byte, pathLen)
		if _, err := io.ReadFull(r, pathBuf); err != nil {
			return nil, fmt.Errorf("file %d path: %w", i, err)
		}

		var sizeBuf [8]byte
		if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
			return nil, fmt.Errorf("file %d size: %w", i, err)
		}

		var metaBuf [1]byte
		if _, err := io.ReadFull(r, metaBuf[:]); err != nil {
			return nil, fmt.Errorf("file %d meta: %w", i, err)
		}

		var mode uint32
		if metaBuf[0]&metaHasMode != 0 {
			var modeBuf [4]byte
			if _, err := io.ReadFull(r, modeBuf[:]); err != nil {
				return nil, fmt.Errorf("file %d mode: %w", i, err)
			}
			mode = binary.BigEndian.Uint32(modeBuf[:])
		}

		var mtime int64
		if metaBuf[0]&metaHasMtime != 0 {
			var mtimeBuf [8]byte
			if _, err := io.ReadFull(r, mtimeBuf[:]); err != nil {
				return nil, fmt.Errorf("file %d mtime: %w", i, err)
			}
			mtime = int64(binary.BigEndian.Uint64(mtimeBuf[:]))
		}

		files[i] = fileEntry{
			Path:      string(pathBuf),
			Size:      int64(binary.BigEndian.Uint64(sizeBuf[:])),
			MetaFlags: metaBuf[0],
			Mode:      mode,
			Mtime:     mtime,
		}
	}
	return files, nil
}

// readCheckpointTmpPaths reads temp file paths from a checkpoint.
func readCheckpointTmpPaths(r io.Reader, fileCount int, receiveDir string) ([]string, error) {
	var countBuf [2]byte
	if _, err := io.ReadFull(r, countBuf[:]); err != nil {
		return nil, fmt.Errorf("read tmp path count: %w", err)
	}
	count := int(binary.BigEndian.Uint16(countBuf[:]))
	if count > fileCount {
		return nil, fmt.Errorf("tmp path count %d exceeds file count %d", count, fileCount)
	}

	tmpPaths := make([]string, fileCount)
	for i := 0; i < count; i++ {
		var entry [4]byte
		if _, err := io.ReadFull(r, entry[:]); err != nil {
			return nil, fmt.Errorf("tmp path %d header: %w", i, err)
		}
		fileIdx := int(binary.BigEndian.Uint16(entry[0:2]))
		nameLen := int(binary.BigEndian.Uint16(entry[2:4]))

		if fileIdx >= fileCount {
			return nil, fmt.Errorf("tmp path %d fileIdx %d out of range", i, fileIdx)
		}
		if nameLen > maxFilenameLen {
			return nil, fmt.Errorf("tmp path %d name too long: %d", i, nameLen)
		}

		nameBuf := make([]byte, nameLen)
		if _, err := io.ReadFull(r, nameBuf); err != nil {
			return nil, fmt.Errorf("tmp path %d name: %w", i, err)
		}

		// Defense in depth: validate name is a simple base name with no path separators
		// or traversal components. A corrupted checkpoint could store "../../../evil"
		// which filepath.Join would resolve outside receiveDir.
		name := string(nameBuf)
		if name != filepath.Base(name) || name == "." || name == ".." {
			return nil, fmt.Errorf("tmp path %d contains path traversal: %q", i, name)
		}

		tmpPaths[fileIdx] = filepath.Join(receiveDir, name)
	}
	return tmpPaths, nil
}

// cleanupTempFiles removes temp files referenced by this checkpoint.
func (c *transferCheckpoint) cleanupTempFiles(receiveDir string) {
	for _, p := range c.tmpPaths {
		if p == "" {
			continue
		}
		os.Remove(filepath.Join(receiveDir, filepath.Base(p)))
	}
}

// removeCheckpoint deletes the checkpoint file.
func removeCheckpoint(receiveDir string, ck [32]byte) {
	os.Remove(checkpointPath(receiveDir, ck))
}

// --- Checkpoint <-> streamReceiveState integration ---

// checkpointFromState creates a transferCheckpoint from the current receive state.
// Called periodically during receive and on error/cancellation to persist progress.
func checkpointFromState(state *streamReceiveState, ck [32]byte, flags uint8) *transferCheckpoint {
	state.mu.Lock()
	defer state.mu.Unlock()

	// Dense arrays from the sparse maps.
	chunkCount := state.maxChunkIdx + 1
	if chunkCount < 0 {
		chunkCount = 0
	}

	hashes := make([][32]byte, chunkCount)
	sizes := make([]uint32, chunkCount)
	for i := 0; i < chunkCount; i++ {
		if h, ok := state.hashes[i]; ok {
			hashes[i] = h
		}
		if s, ok := state.sizes[i]; ok {
			sizes[i] = s
		}
	}

	// Copy bitfield.
	var have *bitfield
	if state.receivedBitfield != nil {
		have = &bitfield{
			bits: make([]byte, len(state.receivedBitfield.bits)),
			n:    state.receivedBitfield.n,
		}
		copy(have.bits, state.receivedBitfield.bits)
	} else {
		have = newBitfield(chunkCount)
	}

	// Deep copy file table (defense against future mutations).
	filesCopy := make([]fileEntry, len(state.files))
	copy(filesCopy, state.files)

	// Collect tmp paths.
	tmpPaths := make([]string, len(state.tmpPaths))
	copy(tmpPaths, state.tmpPaths)

	return &transferCheckpoint{
		contentKey: ck,
		files:      filesCopy,
		totalSize:  state.totalSize,
		flags:      flags,
		have:       have,
		hashes:     hashes,
		sizes:      sizes,
		tmpPaths:   tmpPaths,
	}
}

// restoreReceiveState reconstructs a streamReceiveState from a checkpoint.
// Re-opens existing temp files (does NOT re-allocate/truncate - they already have data).
// Returns the restored state with all chunk metadata pre-populated.
func (c *transferCheckpoint) restoreReceiveState(destDir string) (*streamReceiveState, error) {
	cumOffsets := computeCumulativeOffsets(c.files)

	state := newStreamReceiveState(c.files, c.totalSize, c.flags, cumOffsets)

	// Restore chunk metadata from checkpoint arrays.
	for i := 0; i < len(c.hashes); i++ {
		if c.have.has(i) {
			state.hashes[i] = c.hashes[i]
			state.sizes[i] = c.sizes[i]
			state.receivedBytes += int64(c.sizes[i])
			if i > state.maxChunkIdx {
				state.maxChunkIdx = i
			}
		}
	}

	// Restore received bitfield, grown to cover all expected chunks.
	// The checkpoint's bitfield has n = maxChunkIdx+1 at save time, which may be
	// less than the total expected chunks (checkpoint saved mid-transfer). New chunks
	// beyond the old n arrive during resume. Without growing, receivedBitfield.set()
	// silently ignores them (index >= n guard), breaking duplicate detection.
	estimatedChunks := estimateChunkCount(c.totalSize)
	bfSize := estimatedChunks
	if c.have.n > bfSize {
		bfSize = c.have.n
	}
	state.receivedBitfield = newBitfield(bfSize)
	copy(state.receivedBitfield.bits, c.have.bits)

	// Re-open temp files (they already have partial data).
	root, err := os.OpenRoot(destDir)
	if err != nil {
		return nil, fmt.Errorf("open dest root for resume: %w", err)
	}
	state.destRoot = root

	state.tmpFiles = make([]*os.File, len(c.files))
	state.tmpPaths = make([]string, len(c.files))

	for i, tmpPath := range c.tmpPaths {
		if tmpPath == "" {
			continue
		}
		baseName := filepath.Base(tmpPath)

		// Verify temp file still exists.
		tmpFile, err := root.OpenFile(baseName, os.O_RDWR, 0600)
		if err != nil {
			// Temp file gone (cleaned up, disk error, etc). Can't resume this file.
			// Clean up other opened files and fail.
			state.cleanup()
			return nil, fmt.Errorf("reopen temp file %s for resume: %w", baseName, err)
		}
		state.tmpFiles[i] = tmpFile
		state.tmpPaths[i] = baseName
	}

	return state, nil
}

// --- Resume wire protocol ---

// writeResumeRequest writes msgResumeRequest + bitfieldLen(4) + bitfield.
func writeResumeRequest(w io.Writer, bf *bitfield) error {
	bfBytes := bf.bits
	buf := make([]byte, 1+4+len(bfBytes))
	buf[0] = msgResumeRequest
	binary.BigEndian.PutUint32(buf[1:5], uint32(len(bfBytes)))
	copy(buf[5:], bfBytes)
	_, err := w.Write(buf)
	return err
}

// readResumePayload reads the bitfield data after msgResumeRequest type was consumed.
func readResumePayload(r io.Reader) ([]byte, error) {
	var bfLen [4]byte
	if _, err := io.ReadFull(r, bfLen[:]); err != nil {
		return nil, fmt.Errorf("read resume bitfield length: %w", err)
	}
	size := binary.BigEndian.Uint32(bfLen[:])
	if size > maxBitfieldSize {
		return nil, fmt.Errorf("resume bitfield too large: %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read resume bitfield: %w", err)
	}
	return data, nil
}

// --- Offset table ---

// buildOffsetTable computes the byte offset of each chunk in the reconstructed file.
func buildOffsetTable(chunkSizes []uint32) []int64 {
	offsets := make([]int64, len(chunkSizes))
	var off int64
	for i, sz := range chunkSizes {
		offsets[i] = off
		off += int64(sz)
	}
	return offsets
}
