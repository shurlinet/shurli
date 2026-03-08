package p2pnet

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
)

// Transfer protocol constants.
const (
	TransferProtocol = "/shurli/file-transfer/1.0.0"
	transferVersion  = 0x01

	// Wire message types.
	transferTypeFile   = 0x01
	transferTypeAccept = 0x02
	transferTypeReject = 0x03

	// Limits.
	maxFilenameLen  = 4096       // max filename length in bytes
	maxFileSize     = 1 << 40    // 1 TB max single file
	transferBufSize = 64 * 1024  // 64 KB copy buffer
)

// TransferHeader is sent by the sender before file data.
type TransferHeader struct {
	Filename string   // base name only (no path components)
	Size     int64    // file size in bytes
	Checksum [32]byte // SHA-256 of file content
}

// marshalHeader writes the transfer header to a writer.
// Wire format: version(1) + type(1) + nameLen(2) + name(var) + size(8) + sha256(32)
func marshalHeader(w io.Writer, h *TransferHeader) error {
	nameBytes := []byte(h.Filename)
	if len(nameBytes) > maxFilenameLen {
		return fmt.Errorf("filename too long: %d bytes (max %d)", len(nameBytes), maxFilenameLen)
	}

	buf := make([]byte, 4+len(nameBytes)+8+32)
	buf[0] = transferVersion
	buf[1] = transferTypeFile
	binary.BigEndian.PutUint16(buf[2:4], uint16(len(nameBytes)))
	copy(buf[4:4+len(nameBytes)], nameBytes)
	off := 4 + len(nameBytes)
	binary.BigEndian.PutUint64(buf[off:off+8], uint64(h.Size))
	copy(buf[off+8:off+40], h.Checksum[:])

	_, err := w.Write(buf)
	return err
}

// unmarshalHeader reads a transfer header from a reader.
func unmarshalHeader(r io.Reader) (*TransferHeader, error) {
	// Read version + type + nameLen
	var prefix [4]byte
	if _, err := io.ReadFull(r, prefix[:]); err != nil {
		return nil, fmt.Errorf("read header prefix: %w", err)
	}

	if prefix[0] != transferVersion {
		return nil, fmt.Errorf("unsupported transfer version: %d", prefix[0])
	}
	if prefix[1] != transferTypeFile {
		return nil, fmt.Errorf("unexpected message type: %d", prefix[1])
	}

	nameLen := binary.BigEndian.Uint16(prefix[2:4])
	if nameLen > maxFilenameLen {
		return nil, fmt.Errorf("filename too long: %d", nameLen)
	}

	// Read name + size + checksum
	rest := make([]byte, int(nameLen)+8+32)
	if _, err := io.ReadFull(r, rest); err != nil {
		return nil, fmt.Errorf("read header body: %w", err)
	}

	h := &TransferHeader{
		Filename: string(rest[:nameLen]),
		Size:     int64(binary.BigEndian.Uint64(rest[nameLen : nameLen+8])),
	}
	copy(h.Checksum[:], rest[nameLen+8:nameLen+40])

	if h.Size < 0 || h.Size > maxFileSize {
		return nil, fmt.Errorf("invalid file size: %d", h.Size)
	}

	// Sanitize filename: strip path components, reject traversal.
	h.Filename = filepath.Base(h.Filename)
	if h.Filename == "." || h.Filename == ".." || h.Filename == "/" {
		return nil, fmt.Errorf("invalid filename: %q", h.Filename)
	}

	return h, nil
}

// writeResponse writes a single-byte accept/reject response.
func writeResponse(w io.Writer, msgType byte) error {
	_, err := w.Write([]byte{msgType})
	return err
}

// readResponse reads a single-byte response.
func readResponse(r io.Reader) (byte, error) {
	var b [1]byte
	_, err := io.ReadFull(r, b[:])
	return b[0], err
}

// TransferProgress tracks the progress of an active transfer.
type TransferProgress struct {
	ID        string    `json:"id"`
	Filename  string    `json:"filename"`
	Size      int64     `json:"size"`
	Sent      int64     `json:"sent"`
	PeerID    string    `json:"peer_id"`
	Direction string    `json:"direction"` // "send" or "receive"
	StartTime time.Time `json:"start_time"`
	Done      bool      `json:"done"`
	Error     string    `json:"error,omitempty"`

	mu sync.Mutex
}

func (p *TransferProgress) update(sent int64) {
	p.mu.Lock()
	p.Sent = sent
	p.mu.Unlock()
}

func (p *TransferProgress) finish(err error) {
	p.mu.Lock()
	p.Done = true
	if err != nil {
		p.Error = err.Error()
	}
	p.mu.Unlock()
}

// Snapshot returns a copy safe for JSON serialization.
func (p *TransferProgress) Snapshot() TransferProgress {
	p.mu.Lock()
	defer p.mu.Unlock()
	return TransferProgress{
		ID: p.ID, Filename: p.Filename, Size: p.Size, Sent: p.Sent,
		PeerID: p.PeerID, Direction: p.Direction, StartTime: p.StartTime,
		Done: p.Done, Error: p.Error,
	}
}

// maxTrackedTransfers caps the number of tracked transfer entries.
// Completed transfers are evicted oldest-first when this limit is hit.
const maxTrackedTransfers = 10000

// TransferService manages file transfers over libp2p streams.
type TransferService struct {
	receiveDir string
	maxSize    int64 // 0 = unlimited (up to maxFileSize)
	metrics    *Metrics
	events     *EventBus

	mu        sync.RWMutex
	transfers map[string]*TransferProgress
	nextID    int
}

// TransferConfig configures the transfer service.
type TransferConfig struct {
	ReceiveDir string // directory for received files
	MaxSize    int64  // max file size (0 = unlimited)
}

// NewTransferService creates a new transfer service.
func NewTransferService(cfg TransferConfig, metrics *Metrics, events *EventBus) (*TransferService, error) {
	dir := cfg.ReceiveDir
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("cannot determine home directory: %w", err)
		}
		dir = filepath.Join(home, "Downloads", "shurli")
	}

	// Expand ~ if present.
	if strings.HasPrefix(dir, "~/") {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, dir[2:])
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("cannot create receive directory %s: %w", dir, err)
	}

	return &TransferService{
		receiveDir: dir,
		maxSize:    cfg.MaxSize,
		metrics:    metrics,
		events:     events,
		transfers:  make(map[string]*TransferProgress),
	}, nil
}

// transferStreamDeadline is the maximum wall-clock time for a complete
// file transfer (header + data). Large files over slow relay links may
// need this increased.
const transferStreamDeadline = 30 * time.Minute

// HandleInbound returns a StreamHandler for receiving files.
// This is registered as a custom handler via Network.RegisterHandler.
func (ts *TransferService) HandleInbound() StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		remotePeer := s.Conn().RemotePeer()
		short := remotePeer.String()[:16] + "..."

		// Read header.
		header, err := unmarshalHeader(s)
		if err != nil {
			slog.Warn("file-transfer: bad header", "peer", short, "error", err)
			writeResponse(s, transferTypeReject)
			return
		}

		// Enforce size limit.
		if ts.maxSize > 0 && header.Size > ts.maxSize {
			slog.Warn("file-transfer: file too large",
				"peer", short, "file", header.Filename,
				"size", header.Size, "max", ts.maxSize)
			writeResponse(s, transferTypeReject)
			return
		}

		slog.Info("file-transfer: receiving",
			"peer", short, "file", header.Filename, "size", header.Size)

		// Accept.
		if err := writeResponse(s, transferTypeAccept); err != nil {
			slog.Error("file-transfer: accept write failed", "error", err)
			return
		}

		// Track progress.
		progress := ts.trackTransfer(header.Filename, header.Size, remotePeer.String(), "receive")

		// Receive file data with hash verification.
		err = ts.receiveFile(s, header, progress)
		progress.finish(err)

		if err != nil {
			slog.Error("file-transfer: receive failed",
				"peer", short, "file", header.Filename, "error", err)
		} else {
			slog.Info("file-transfer: received",
				"peer", short, "file", header.Filename,
				"size", header.Size, "path", filepath.Join(ts.receiveDir, header.Filename))
		}

		if ts.events != nil {
			evType := EventStreamClosed
			ts.events.Emit(Event{
				Type:        evType,
				PeerID:      remotePeer,
				ServiceName: "file-transfer",
			})
		}
	}
}

// receiveFile writes stream data to disk and verifies the SHA-256 checksum.
func (ts *TransferService) receiveFile(r io.Reader, header *TransferHeader, progress *TransferProgress) error {
	// Create file atomically with O_EXCL to avoid TOCTOU races
	// when multiple peers send files with the same name concurrently.
	destPath, f, err := ts.createExclusive(header.Filename)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	hasher := sha256.New()
	writer := io.MultiWriter(f, hasher)

	var received int64
	buf := make([]byte, transferBufSize)
	for received < header.Size {
		remaining := header.Size - received
		readSize := int64(len(buf))
		if remaining < readSize {
			readSize = remaining
		}
		n, err := r.Read(buf[:readSize])
		if n > 0 {
			if _, werr := writer.Write(buf[:n]); werr != nil {
				os.Remove(destPath)
				return fmt.Errorf("write file: %w", werr)
			}
			received += int64(n)
			progress.update(received)
		}
		if err != nil {
			if err == io.EOF && received == header.Size {
				break
			}
			os.Remove(destPath)
			return fmt.Errorf("read stream: %w (received %d/%d)", err, received, header.Size)
		}
	}

	// Flush to disk before verifying checksum.
	if err := f.Sync(); err != nil {
		os.Remove(destPath)
		return fmt.Errorf("sync file: %w", err)
	}

	// Verify checksum.
	var got [32]byte
	copy(got[:], hasher.Sum(nil))
	if got != header.Checksum {
		os.Remove(destPath)
		return fmt.Errorf("checksum mismatch: file corrupted during transfer")
	}

	return nil
}

// createExclusive atomically creates a non-colliding file in the receive directory.
// Uses O_CREATE|O_EXCL to avoid TOCTOU races when multiple peers send files
// with the same name concurrently.
func (ts *TransferService) createExclusive(filename string) (string, *os.File, error) {
	path := filepath.Join(ts.receiveDir, filename)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err == nil {
		return path, f, nil
	}
	if !os.IsExist(err) {
		return "", nil, err
	}

	ext := filepath.Ext(filename)
	base := strings.TrimSuffix(filename, ext)
	for i := 1; i < 10000; i++ {
		candidate := filepath.Join(ts.receiveDir, fmt.Sprintf("%s (%d)%s", base, i, ext))
		f, err = os.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err == nil {
			return candidate, f, nil
		}
		if !os.IsExist(err) {
			return "", nil, err
		}
	}
	// Exhausted suffixes; fall back to overwrite.
	f, err = os.Create(path)
	if err != nil {
		return "", nil, err
	}
	return path, f, nil
}

// SendFile sends a file to a peer over an open libp2p stream.
// The caller is responsible for opening the stream (via DialService or host.NewStream).
func (ts *TransferService) SendFile(s network.Stream, filePath string) (*TransferProgress, error) {
	remotePeer := s.Conn().RemotePeer()

	// Open and stat the file.
	f, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	// NOTE: f is NOT deferred here. The background goroutine owns the file
	// handle and closes it when done. This prevents a use-after-close race.

	stat, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("stat file: %w", err)
	}
	if stat.IsDir() {
		f.Close()
		return nil, fmt.Errorf("cannot send directory (use shurli send --recursive for directories)")
	}
	if stat.Size() > maxFileSize {
		f.Close()
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", stat.Size(), maxFileSize)
	}

	// Compute SHA-256 checksum.
	checksum, err := hashFile(f)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("hash file: %w", err)
	}
	// Seek back to start for sending.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, fmt.Errorf("seek: %w", err)
	}

	header := &TransferHeader{
		Filename: filepath.Base(filePath),
		Size:     stat.Size(),
		Checksum: checksum,
	}

	// Track progress.
	progress := ts.trackTransfer(header.Filename, header.Size, remotePeer.String(), "send")

	// Send in background so caller can poll progress.
	go func() {
		defer f.Close()
		defer s.Close()
		s.SetDeadline(time.Now().Add(transferStreamDeadline))
		err := ts.sendFileData(s, f, header, progress)
		progress.finish(err)

		if err != nil {
			slog.Error("file-transfer: send failed",
				"peer", remotePeer.String()[:16]+"...",
				"file", header.Filename, "error", err)
		} else {
			slog.Info("file-transfer: sent",
				"peer", remotePeer.String()[:16]+"...",
				"file", header.Filename, "size", header.Size)
		}

		if ts.events != nil {
			ts.events.Emit(Event{
				Type:        EventStreamClosed,
				PeerID:      remotePeer,
				ServiceName: "file-transfer",
			})
		}
	}()

	return progress, nil
}

// sendFileData sends the header, waits for accept, then streams file data.
func (ts *TransferService) sendFileData(s network.Stream, f *os.File, header *TransferHeader, progress *TransferProgress) error {
	// Send header.
	if err := marshalHeader(s, header); err != nil {
		return fmt.Errorf("send header: %w", err)
	}

	// Wait for accept/reject.
	resp, err := readResponse(s)
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if resp == transferTypeReject {
		return fmt.Errorf("peer rejected transfer")
	}
	if resp != transferTypeAccept {
		return fmt.Errorf("unexpected response: %d", resp)
	}

	// Stream file data.
	var sent int64
	buf := make([]byte, transferBufSize)
	for sent < header.Size {
		n, err := f.Read(buf)
		if n > 0 {
			if _, werr := s.Write(buf[:n]); werr != nil {
				return fmt.Errorf("write stream: %w", werr)
			}
			sent += int64(n)
			progress.update(sent)
		}
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("read file: %w", err)
		}
	}

	return nil
}

func (ts *TransferService) trackTransfer(filename string, size int64, peerID, direction string) *TransferProgress {
	ts.mu.Lock()
	defer ts.mu.Unlock()

	// Evict completed transfers when at capacity.
	if len(ts.transfers) >= maxTrackedTransfers {
		ts.evictCompleted()
	}

	ts.nextID++
	id := fmt.Sprintf("xfer-%d", ts.nextID)

	p := &TransferProgress{
		ID:        id,
		Filename:  filename,
		Size:      size,
		PeerID:    peerID,
		Direction: direction,
		StartTime: time.Now(),
	}
	ts.transfers[id] = p
	return p
}

// evictCompleted removes the oldest completed transfers. Caller must hold ts.mu.
func (ts *TransferService) evictCompleted() {
	var oldest string
	var oldestTime time.Time
	for id, p := range ts.transfers {
		snap := p.Snapshot()
		if !snap.Done {
			continue
		}
		if oldest == "" || snap.StartTime.Before(oldestTime) {
			oldest = id
			oldestTime = snap.StartTime
		}
	}
	if oldest != "" {
		delete(ts.transfers, oldest)
	}
}

// GetTransfer returns the progress of a transfer by ID.
func (ts *TransferService) GetTransfer(id string) (*TransferProgress, bool) {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	p, ok := ts.transfers[id]
	return p, ok
}

// ListTransfers returns snapshots of all tracked transfers.
func (ts *TransferService) ListTransfers() []TransferProgress {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	result := make([]TransferProgress, 0, len(ts.transfers))
	for _, p := range ts.transfers {
		result = append(result, p.Snapshot())
	}
	return result
}

// hashFile computes the SHA-256 of an open file without closing it.
func hashFile(f *os.File) ([32]byte, error) {
	var h hash.Hash = sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}
