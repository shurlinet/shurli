package p2pnet

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
)

// transferCheckpoint represents a resumable transfer state on disk.
type transferCheckpoint struct {
	manifest *transferManifest
	have     *bitfield
	tmpPath  string // path to the partial file on disk
}

// checkpointPath returns the checkpoint file path for a given root hash.
func checkpointPath(receiveDir string, rootHash [32]byte) string {
	h := fmt.Sprintf("%x", rootHash[:8]) // 16 hex chars
	return filepath.Join(receiveDir, ".shurli-ckpt-"+h)
}

// saveCheckpoint persists the checkpoint to disk atomically.
func (c *transferCheckpoint) save(receiveDir string) error {
	path := checkpointPath(receiveDir, c.manifest.RootHash)

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create checkpoint: %w", err)
	}
	defer f.Close()

	// Magic + version.
	header := [5]byte{ckptMagic0, ckptMagic1, ckptMagic2, ckptMagic3, ckptVersion}
	if _, err := f.Write(header[:]); err != nil {
		return err
	}

	// Manifest (reuse existing serializer).
	if err := writeManifest(f, c.manifest); err != nil {
		return fmt.Errorf("write manifest to checkpoint: %w", err)
	}

	// Bitfield: length(4) + data.
	bfBytes := c.have.bits
	var bfLen [4]byte
	binary.BigEndian.PutUint32(bfLen[:], uint32(len(bfBytes)))
	if _, err := f.Write(bfLen[:]); err != nil {
		return err
	}
	if _, err := f.Write(bfBytes); err != nil {
		return err
	}

	// Tmp file name (base name only, relative to receive dir).
	relName := filepath.Base(c.tmpPath)
	nameBytes := []byte(relName)
	var nameLen [2]byte
	binary.BigEndian.PutUint16(nameLen[:], uint16(len(nameBytes)))
	if _, err := f.Write(nameLen[:]); err != nil {
		return err
	}
	if _, err := f.Write(nameBytes); err != nil {
		return err
	}

	return f.Sync()
}

// loadCheckpoint reads a checkpoint from disk. Returns os.ErrNotExist if none.
func loadCheckpoint(receiveDir string, rootHash [32]byte) (*transferCheckpoint, error) {
	path := checkpointPath(receiveDir, rootHash)

	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Magic + version.
	var header [5]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, fmt.Errorf("read checkpoint header: %w", err)
	}
	if header[0] != ckptMagic0 || header[1] != ckptMagic1 ||
		header[2] != ckptMagic2 || header[3] != ckptMagic3 {
		return nil, fmt.Errorf("invalid checkpoint magic")
	}
	if header[4] != ckptVersion {
		return nil, fmt.Errorf("unsupported checkpoint version: %d", header[4])
	}

	// Manifest.
	manifest, err := readManifest(f)
	if err != nil {
		return nil, fmt.Errorf("read manifest from checkpoint: %w", err)
	}
	if manifest.RootHash != rootHash {
		return nil, fmt.Errorf("checkpoint root hash mismatch")
	}

	// Bitfield.
	var bfLen [4]byte
	if _, err := io.ReadFull(f, bfLen[:]); err != nil {
		return nil, fmt.Errorf("read bitfield length: %w", err)
	}
	bfSize := binary.BigEndian.Uint32(bfLen[:])
	if bfSize > maxBitfieldSize {
		return nil, fmt.Errorf("bitfield too large: %d", bfSize)
	}
	bfData := make([]byte, bfSize)
	if _, err := io.ReadFull(f, bfData); err != nil {
		return nil, fmt.Errorf("read bitfield: %w", err)
	}

	// Tmp file name.
	var nameLen [2]byte
	if _, err := io.ReadFull(f, nameLen[:]); err != nil {
		return nil, fmt.Errorf("read tmp name length: %w", err)
	}
	nameSize := binary.BigEndian.Uint16(nameLen[:])
	if nameSize > maxFilenameLen {
		return nil, fmt.Errorf("tmp name too long: %d", nameSize)
	}
	nameBytes := make([]byte, nameSize)
	if _, err := io.ReadFull(f, nameBytes); err != nil {
		return nil, fmt.Errorf("read tmp name: %w", err)
	}

	have := &bitfield{
		bits: make([]byte, (manifest.ChunkCount+7)/8),
		n:    manifest.ChunkCount,
	}
	copy(have.bits, bfData)

	return &transferCheckpoint{
		manifest: manifest,
		have:     have,
		tmpPath:  filepath.Join(receiveDir, string(nameBytes)),
	}, nil
}

// removeCheckpoint deletes the checkpoint file.
func removeCheckpoint(receiveDir string, rootHash [32]byte) {
	os.Remove(checkpointPath(receiveDir, rootHash))
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
