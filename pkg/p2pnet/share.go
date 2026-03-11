package p2pnet

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

// Share protocol constants.
const (
	// BrowseProtocol is the protocol ID for browsing shared content.
	BrowseProtocol = "/shurli/file-browse/1.0.0"

	// DownloadProtocol is the protocol ID for receiver-initiated file download.
	// Single-stream: receiver sends path, sharer verifies ACL, then sends file
	// data using the existing SHFT chunked transfer format.
	DownloadProtocol = "/shurli/file-download/1.0.0"

	// Browse wire messages.
	msgBrowseRequest  = 0x01
	msgBrowseResponse = 0x02
	msgBrowseError    = 0x03
	msgDownloadReq    = 0x04

	// Download wire error marker.
	msgDownloadError = 0xFF

	// Limits.
	maxSharesPerPeer  = 100
	maxBrowseResults  = 500
	maxPathLength     = 4096
	browseTimeout     = 30 * time.Second
	maxShareEntrySize = 1 << 20 // 1 MB for browse response
)

// ShareEntry represents a single shared path with its ACL.
type ShareEntry struct {
	Path       string            `json:"path"`        // absolute path on sharer's filesystem
	Peers      map[peer.ID]bool  `json:"-"`           // allowed peers (nil = all authorized)
	PeerIDs    []string          `json:"peers"`       // serialization form
	Persistent bool              `json:"persistent"`  // survive daemon restart
	SharedAt   time.Time         `json:"shared_at"`
	IsDir      bool              `json:"is_dir"`
}

// BrowseEntry is a single item returned by the browse protocol.
type BrowseEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`     // relative path within shared root
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"` // unix timestamp
}

// ShareRegistry manages shared paths and their per-peer ACLs.
// Thread-safe. Lives in the daemon. Persistent shares survive restarts.
type ShareRegistry struct {
	mu          sync.RWMutex
	shares      map[string]*ShareEntry // path -> entry
	persistPath string                 // file path for persistent share storage
}

// NewShareRegistry creates an empty share registry.
func NewShareRegistry() *ShareRegistry {
	return &ShareRegistry{
		shares: make(map[string]*ShareEntry),
	}
}

// SetPersistPath sets the file path used for auto-saving persistent shares.
func (r *ShareRegistry) SetPersistPath(path string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persistPath = path
}

// persistentShareFile is the JSON structure written to disk.
type persistentShareFile struct {
	Shares []persistentShareEntry `json:"shares"`
}

// persistentShareEntry is the serializable form of a ShareEntry.
type persistentShareEntry struct {
	Path       string   `json:"path"`
	PeerIDs    []string `json:"peers,omitempty"`
	SharedAt   int64    `json:"shared_at"`
	IsDir      bool     `json:"is_dir"`
}

// SavePersistent writes all persistent shares to the given path.
// Uses atomic write (tmp + rename) to prevent corruption.
func (r *ShareRegistry) SavePersistent(path string) error {
	r.mu.RLock()
	var entries []persistentShareEntry
	for _, entry := range r.shares {
		if !entry.Persistent {
			continue
		}
		pe := persistentShareEntry{
			Path:     entry.Path,
			SharedAt: entry.SharedAt.Unix(),
			IsDir:    entry.IsDir,
		}
		if entry.Peers != nil {
			for pid := range entry.Peers {
				pe.PeerIDs = append(pe.PeerIDs, pid.String())
			}
		}
		entries = append(entries, pe)
	}
	r.mu.RUnlock()

	data, err := json.MarshalIndent(persistentShareFile{Shares: entries}, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal shares: %w", err)
	}

	// Atomic write: tmp file + rename.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// LoadShareRegistry loads persistent shares from a JSON file.
// Returns an empty registry if the file does not exist.
func LoadShareRegistry(path string) (*ShareRegistry, error) {
	reg := NewShareRegistry()
	reg.persistPath = path

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return reg, nil
		}
		return nil, fmt.Errorf("read shares file: %w", err)
	}

	var file persistentShareFile
	if err := json.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("parse shares file: %w", err)
	}

	for _, pe := range file.Shares {
		var peerMap map[peer.ID]bool
		if len(pe.PeerIDs) > 0 {
			peerMap = make(map[peer.ID]bool, len(pe.PeerIDs))
			for _, pidStr := range pe.PeerIDs {
				pid, err := peer.Decode(pidStr)
				if err != nil {
					slog.Warn("share-persistence: skipping invalid peer ID", "peer", pidStr, "err", err)
					continue
				}
				peerMap[pid] = true
			}
		}

		reg.shares[pe.Path] = &ShareEntry{
			Path:       pe.Path,
			Peers:      peerMap,
			Persistent: true,
			SharedAt:   time.Unix(pe.SharedAt, 0),
			IsDir:      pe.IsDir,
		}
	}

	slog.Info("share-persistence: loaded shares", "count", len(file.Shares))
	return reg, nil
}

// savePersistentIfNeeded saves persistent shares if a persist path is configured.
// Called internally after Share/Unshare. Caller must NOT hold r.mu.
func (r *ShareRegistry) savePersistentIfNeeded() {
	r.mu.RLock()
	path := r.persistPath
	r.mu.RUnlock()

	if path == "" {
		return
	}
	if err := r.SavePersistent(path); err != nil {
		slog.Warn("share-persistence: auto-save failed", "err", err)
	}
}

// Share adds or updates a shared path.
func (r *ShareRegistry) Share(path string, peers []peer.ID, persistent bool) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	// Validate path exists.
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("path not accessible: %w", err)
	}

	// Build peer map.
	var peerMap map[peer.ID]bool
	if len(peers) > 0 {
		peerMap = make(map[peer.ID]bool, len(peers))
		for _, p := range peers {
			peerMap[p] = true
		}
	}

	r.mu.Lock()
	r.shares[absPath] = &ShareEntry{
		Path:       absPath,
		Peers:      peerMap,
		Persistent: persistent,
		SharedAt:   time.Now(),
		IsDir:      info.IsDir(),
	}
	r.mu.Unlock()

	r.savePersistentIfNeeded()
	return nil
}

// Unshare removes a shared path.
func (r *ShareRegistry) Unshare(path string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	r.mu.Lock()
	if _, ok := r.shares[absPath]; !ok {
		r.mu.Unlock()
		return fmt.Errorf("path %q not shared", absPath)
	}
	delete(r.shares, absPath)
	r.mu.Unlock()

	r.savePersistentIfNeeded()
	return nil
}

// ListShares returns all shares, optionally filtered by peer.
func (r *ShareRegistry) ListShares(forPeer *peer.ID) []*ShareEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*ShareEntry
	for _, entry := range r.shares {
		if forPeer != nil && !r.peerAllowed(entry, *forPeer) {
			continue
		}
		result = append(result, entry)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Path < result[j].Path
	})
	return result
}

// LookupShare finds a share entry by exact path.
func (r *ShareRegistry) LookupShare(path string) (*ShareEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	entry, ok := r.shares[path]
	return entry, ok
}

// IsPathShared checks if the given path is within any shared directory
// and the peer is allowed to access it.
func (r *ShareRegistry) IsPathShared(path string, peerID peer.ID) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, entry := range r.shares {
		if !r.peerAllowed(entry, peerID) {
			continue
		}
		// Exact match.
		if path == entry.Path {
			return true
		}
		// path is within a shared directory.
		if entry.IsDir && strings.HasPrefix(path, entry.Path+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// peerAllowed checks if a peer can access this share. nil peers map = all authorized.
func (r *ShareRegistry) peerAllowed(entry *ShareEntry, peerID peer.ID) bool {
	if entry.Peers == nil {
		return true // all authorized peers
	}
	return entry.Peers[peerID]
}

// BrowseForPeer returns browsable entries for a specific peer.
func (r *ShareRegistry) BrowseForPeer(peerID peer.ID) []BrowseEntry {
	shares := r.ListShares(&peerID)

	var entries []BrowseEntry
	for _, share := range shares {
		info, err := os.Stat(share.Path)
		if err != nil {
			continue
		}

		if !share.IsDir {
			// Single file share.
			entries = append(entries, BrowseEntry{
				Name:    filepath.Base(share.Path),
				Path:    share.Path,
				Size:    info.Size(),
				IsDir:   false,
				ModTime: info.ModTime().Unix(),
			})
			continue
		}

		// Directory: list top-level contents.
		dirEntries, err := os.ReadDir(share.Path)
		if err != nil {
			continue
		}

		for _, de := range dirEntries {
			if len(entries) >= maxBrowseResults {
				break
			}

			deInfo, err := de.Info()
			if err != nil {
				continue
			}

			// Skip hidden files and symlinks.
			if strings.HasPrefix(de.Name(), ".") {
				continue
			}
			if deInfo.Mode()&fs.ModeSymlink != 0 {
				continue
			}

			entries = append(entries, BrowseEntry{
				Name:    de.Name(),
				Path:    filepath.Join(share.Path, de.Name()),
				Size:    deInfo.Size(),
				IsDir:   de.IsDir(),
				ModTime: deInfo.ModTime().Unix(),
			})
		}
	}

	return entries
}

// --- Browse protocol handler ---

// HandleBrowse returns a stream handler for the browse protocol.
// Only responds to peers who have shares visible to them.
// Non-authorized peers get a stream reset (no error, no info leakage).
func (r *ShareRegistry) HandleBrowse() StreamHandler {
	return func(serviceName string, s network.Stream) {
		defer s.Close()

		remotePeer := s.Conn().RemotePeer()
		s.SetDeadline(time.Now().Add(browseTimeout))

		// Check if peer has any visible shares.
		shares := r.ListShares(&remotePeer)
		if len(shares) == 0 {
			// Silent reset - no information leakage.
			s.Reset()
			return
		}

		// Read request type.
		var msgType [1]byte
		if _, err := io.ReadFull(s, msgType[:]); err != nil {
			return
		}

		switch msgType[0] {
		case msgBrowseRequest:
			r.handleBrowseRequest(s, remotePeer)
		case msgDownloadReq:
			r.handleDownloadRequest(s, remotePeer)
		default:
			writeBrowseError(s, "unknown request type")
		}
	}
}

func (r *ShareRegistry) handleBrowseRequest(s network.Stream, peerID peer.ID) {
	// Optional: read a path prefix to browse within a shared directory.
	var pathLen uint16
	if err := binary.Read(s, binary.BigEndian, &pathLen); err != nil {
		writeBrowseError(s, "invalid request")
		return
	}

	var browseRoot string
	if pathLen > 0 {
		if pathLen > maxPathLength {
			writeBrowseError(s, "path too long")
			return
		}
		buf := make([]byte, pathLen)
		if _, err := io.ReadFull(s, buf); err != nil {
			return
		}
		browseRoot = string(buf)
	}

	var entries []BrowseEntry
	if browseRoot == "" {
		entries = r.BrowseForPeer(peerID)
	} else {
		// Browse within a specific shared directory.
		if !r.IsPathShared(browseRoot, peerID) {
			writeBrowseError(s, "access denied")
			return
		}
		entries = r.listDirectory(browseRoot, peerID)
	}

	// Serialize response.
	data, err := json.Marshal(entries)
	if err != nil {
		writeBrowseError(s, "internal error")
		return
	}

	if len(data) > maxShareEntrySize {
		writeBrowseError(s, "response too large")
		return
	}

	// Write response: msgBrowseResponse + len(4) + json data.
	var header [5]byte
	header[0] = msgBrowseResponse
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return
	}
	s.Write(data)
}

func (r *ShareRegistry) handleDownloadRequest(s network.Stream, peerID peer.ID) {
	// Read requested path.
	var pathLen uint16
	if err := binary.Read(s, binary.BigEndian, &pathLen); err != nil {
		writeBrowseError(s, "invalid request")
		return
	}
	if pathLen == 0 || pathLen > maxPathLength {
		writeBrowseError(s, "invalid path length")
		return
	}

	pathBuf := make([]byte, pathLen)
	if _, err := io.ReadFull(s, pathBuf); err != nil {
		return
	}
	requestedPath := string(pathBuf)

	// Sanitize: resolve symlinks, ensure within shared path.
	absPath, err := filepath.Abs(requestedPath)
	if err != nil {
		writeBrowseError(s, "invalid path")
		return
	}

	// Verify access.
	if !r.IsPathShared(absPath, peerID) {
		writeBrowseError(s, "access denied")
		return
	}

	info, err := os.Stat(absPath)
	if err != nil {
		writeBrowseError(s, "not found")
		return
	}

	if info.IsDir() {
		// Return directory listing.
		entries := r.listDirectory(absPath, peerID)
		data, _ := json.Marshal(entries)
		var header [5]byte
		header[0] = msgBrowseResponse
		binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
		s.Write(header[:])
		s.Write(data)
		return
	}

	// Single file download: the caller opens a file-transfer stream
	// separately. We just confirm the file exists and is accessible.
	// Response: msgBrowseResponse + file info as JSON.
	entry := BrowseEntry{
		Name:    filepath.Base(absPath),
		Path:    absPath,
		Size:    info.Size(),
		IsDir:   false,
		ModTime: info.ModTime().Unix(),
	}
	data, _ := json.Marshal(entry)
	var header [5]byte
	header[0] = msgBrowseResponse
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	s.Write(header[:])
	s.Write(data)
}

// listDirectory returns entries for a directory path.
func (r *ShareRegistry) listDirectory(dirPath string, peerID peer.ID) []BrowseEntry {
	dirEntries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil
	}

	var entries []BrowseEntry
	for _, de := range dirEntries {
		if len(entries) >= maxBrowseResults {
			break
		}

		// Skip hidden files and symlinks.
		if strings.HasPrefix(de.Name(), ".") {
			continue
		}

		info, err := de.Info()
		if err != nil {
			continue
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			continue
		}

		entries = append(entries, BrowseEntry{
			Name:    de.Name(),
			Path:    filepath.Join(dirPath, de.Name()),
			Size:    info.Size(),
			IsDir:   de.IsDir(),
			ModTime: info.ModTime().Unix(),
		})
	}
	return entries
}

// writeBrowseError sends an error response on the browse stream.
func writeBrowseError(w io.Writer, msg string) {
	data := []byte(msg)
	var header [5]byte
	header[0] = msgBrowseError
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	w.Write(header[:])
	w.Write(data)
}

// --- Client-side browse ---

// BrowseResult is the client-side result of a browse operation.
type BrowseResult struct {
	Entries []BrowseEntry `json:"entries"`
	Error   string        `json:"error,omitempty"`
}

// BrowsePeer sends a browse request to a remote peer and returns the result.
func BrowsePeer(s network.Stream, subPath string) (*BrowseResult, error) {
	s.SetDeadline(time.Now().Add(browseTimeout))

	// Send browse request.
	var msgBuf [1]byte
	msgBuf[0] = msgBrowseRequest
	if _, err := s.Write(msgBuf[:]); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	// Send path (empty = root browse).
	pathBytes := []byte(subPath)
	if err := binary.Write(s, binary.BigEndian, uint16(len(pathBytes))); err != nil {
		return nil, fmt.Errorf("write path length: %w", err)
	}
	if len(pathBytes) > 0 {
		if _, err := s.Write(pathBytes); err != nil {
			return nil, fmt.Errorf("write path: %w", err)
		}
	}

	// Read response header.
	var respHeader [5]byte
	if _, err := io.ReadFull(s, respHeader[:]); err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	respType := respHeader[0]
	dataLen := binary.BigEndian.Uint32(respHeader[1:])

	if dataLen > maxShareEntrySize {
		return nil, fmt.Errorf("response too large: %d bytes", dataLen)
	}

	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("read response data: %w", err)
	}

	if respType == msgBrowseError {
		return &BrowseResult{Error: string(data)}, nil
	}

	var entries []BrowseEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &BrowseResult{Entries: entries}, nil
}

// --- Download protocol handler (sharer side) ---

// HandleDownload returns a stream handler for the download protocol.
// When a peer opens a download stream, the handler:
// 1. Reads the requested path
// 2. Verifies the peer has ACL access to the path
// 3. Sends the file using the SHFT chunked transfer format
//
// For directory downloads, the caller iterates browse entries and downloads
// each file separately.
func (r *ShareRegistry) HandleDownload(ts *TransferService) StreamHandler {
	return func(serviceName string, s network.Stream) {
		remotePeer := s.Conn().RemotePeer()

		// Check if peer has any visible shares.
		shares := r.ListShares(&remotePeer)
		if len(shares) == 0 {
			s.Reset()
			return
		}

		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Read requested path: pathLen(2) + path.
		var pathLen uint16
		if err := binary.Read(s, binary.BigEndian, &pathLen); err != nil {
			s.Close()
			return
		}
		if pathLen == 0 || pathLen > maxPathLength {
			writeDownloadError(s, "invalid path length")
			s.Close()
			return
		}

		pathBuf := make([]byte, pathLen)
		if _, err := io.ReadFull(s, pathBuf); err != nil {
			s.Close()
			return
		}
		requestedPath := string(pathBuf)

		// Sanitize: resolve to absolute, no traversal.
		absPath, err := filepath.Abs(requestedPath)
		if err != nil {
			writeDownloadError(s, "invalid path")
			s.Close()
			return
		}

		// No symlink following: resolve and verify it's still within shared tree.
		realPath, err := filepath.EvalSymlinks(absPath)
		if err != nil {
			writeDownloadError(s, "not found")
			s.Close()
			return
		}

		// Verify ACL.
		if !r.IsPathShared(realPath, remotePeer) {
			writeDownloadError(s, "access denied")
			s.Close()
			return
		}

		info, err := os.Stat(realPath)
		if err != nil {
			writeDownloadError(s, "not found")
			s.Close()
			return
		}

		if info.IsDir() {
			writeDownloadError(s, "cannot download directory; use browse + per-file download")
			s.Close()
			return
		}

		// Regular file only (no device files, pipes, sockets).
		if !info.Mode().IsRegular() {
			writeDownloadError(s, "not a regular file")
			s.Close()
			return
		}

		short := remotePeer.String()[:16] + "..."
		slog.Info("file-download: serving file",
			"peer", short, "path", filepath.Base(realPath),
			"size", info.Size())

		// Send the file using existing chunked transfer (SHFT format).
		// SendFile writes manifest, waits for accept/reject, then sends chunks.
		// The stream is closed by SendFile's background goroutine.
		_, sendErr := ts.SendFile(s, realPath)
		if sendErr != nil {
			slog.Error("file-download: send failed",
				"peer", short, "path", filepath.Base(realPath), "error", sendErr)
			writeDownloadError(s, "internal error")
			s.Close()
		}
		// SendFile runs in background and closes stream when done.
	}
}

// writeDownloadError sends an error on the download stream.
// Wire format: 0xFF + errLen(2) + errMsg.
func writeDownloadError(w io.Writer, msg string) {
	data := []byte(msg)
	var header [3]byte
	header[0] = msgDownloadError
	binary.BigEndian.PutUint16(header[1:], uint16(len(data)))
	w.Write(header[:])
	w.Write(data)
}

// RequestDownload opens a download request on a stream and reads the response.
// On success, the stream will have SHFT manifest data ready to read.
// On error, returns the error message from the sharer.
// The caller is responsible for reading the SHFT data after a successful return.
func RequestDownload(s network.Stream, remotePath string) error {
	s.SetDeadline(time.Now().Add(browseTimeout))

	// Send path request: pathLen(2) + path.
	pathBytes := []byte(remotePath)
	if err := binary.Write(s, binary.BigEndian, uint16(len(pathBytes))); err != nil {
		return fmt.Errorf("write path length: %w", err)
	}
	if _, err := s.Write(pathBytes); err != nil {
		return fmt.Errorf("write path: %w", err)
	}

	// Peek first byte to determine success or error.
	var firstByte [1]byte
	if _, err := io.ReadFull(s, firstByte[:]); err != nil {
		return fmt.Errorf("read response: %w", err)
	}

	if firstByte[0] == msgDownloadError {
		// Read error message: errLen(2) + errMsg.
		var errLen uint16
		if err := binary.Read(s, binary.BigEndian, &errLen); err != nil {
			return fmt.Errorf("read error length: %w", err)
		}
		if errLen > maxPathLength {
			return fmt.Errorf("error message too large")
		}
		errBuf := make([]byte, errLen)
		if _, err := io.ReadFull(s, errBuf); err != nil {
			return fmt.Errorf("read error message: %w", err)
		}
		return fmt.Errorf("remote: %s", string(errBuf))
	}

	// Success: the first byte is the start of SHFT magic.
	// We need to "unread" it. The caller needs this byte as part of the SHFT header.
	// Return the first byte info so the caller can reconstruct.
	// We'll use a different approach: the caller wraps the stream with a prefixed reader.
	// Store the first byte for the caller.
	s.SetDeadline(time.Time{}) // reset deadline for transfer
	return &downloadReady{firstByte: firstByte[0]}
}

// downloadReady signals that the download stream has SHFT data ready.
// The firstByte field contains the first byte already consumed (part of SHFT magic).
// Callers should check for this with errors.As() and use PrefixedReader().
type downloadReady struct {
	firstByte byte
}

func (d *downloadReady) Error() string {
	return "download ready"
}

// PrefixedReader returns a reader that replays the consumed first byte
// followed by the rest of the stream.
func (d *downloadReady) PrefixedReader(s io.Reader) io.Reader {
	return io.MultiReader(
		&singleByteReader{b: d.firstByte, read: false},
		s,
	)
}

// singleByteReader delivers exactly one byte, then EOF.
type singleByteReader struct {
	b    byte
	read bool
}

func (r *singleByteReader) Read(p []byte) (int, error) {
	if r.read || len(p) == 0 {
		return 0, io.EOF
	}
	r.read = true
	p[0] = r.b
	return 1, nil
}

// --- Directory transfer ---

// DirectoryTransfer coordinates sending all files in a directory.
type DirectoryTransfer struct {
	RootDir  string
	Files    []dirFileEntry
	TotalSize int64
}

// dirFileEntry is a file within a directory transfer.
type dirFileEntry struct {
	RelPath string // relative path from root
	AbsPath string // absolute filesystem path
	Size    int64
	IsDir   bool
}

// WalkDirectory builds a DirectoryTransfer from a directory path.
// Skips hidden files, symlinks, and special files.
func WalkDirectory(dirPath string) (*DirectoryTransfer, error) {
	absPath, err := filepath.Abs(dirPath)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", absPath)
	}

	dt := &DirectoryTransfer{RootDir: absPath}

	err = filepath.WalkDir(absPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden files/dirs.
		if strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		// Skip symlinks and special files.
		info, err := d.Info()
		if err != nil {
			return nil // skip unreadable entries
		}
		if !info.Mode().IsRegular() && !info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(absPath, path)
		if err != nil {
			return nil
		}

		// Skip the root directory itself.
		if relPath == "." {
			return nil
		}

		dt.Files = append(dt.Files, dirFileEntry{
			RelPath: relPath,
			AbsPath: path,
			Size:    info.Size(),
			IsDir:   info.IsDir(),
		})
		if info.Mode().IsRegular() {
			dt.TotalSize += info.Size()
		}

		return nil
	})

	if err != nil {
		return nil, fmt.Errorf("walk directory: %w", err)
	}

	// Sort: directories first, then files.
	sort.Slice(dt.Files, func(i, j int) bool {
		if dt.Files[i].IsDir != dt.Files[j].IsDir {
			return dt.Files[i].IsDir // dirs first
		}
		return dt.Files[i].RelPath < dt.Files[j].RelPath
	})

	slog.Debug("file-transfer: directory walk",
		"root", absPath,
		"files", len(dt.Files),
		"totalSize", dt.TotalSize)

	return dt, nil
}

// RegularFiles returns only the regular files (not directories).
func (dt *DirectoryTransfer) RegularFiles() []dirFileEntry {
	var files []dirFileEntry
	for _, f := range dt.Files {
		if !f.IsDir {
			files = append(files, f)
		}
	}
	return files
}

// --- Transfer queue ---

// TransferPriority levels for queued transfers.
type TransferPriority int

const (
	PriorityLow    TransferPriority = 0
	PriorityNormal TransferPriority = 1
	PriorityHigh   TransferPriority = 2
)

// QueuedTransfer represents a transfer waiting in the queue.
type QueuedTransfer struct {
	ID        string           `json:"id"`
	FilePath  string           `json:"file_path"`
	PeerID    string           `json:"peer_id"`
	Priority  TransferPriority `json:"priority"`
	Direction string           `json:"direction"` // "send" or "download"
	QueuedAt  time.Time        `json:"queued_at"`
}

// TransferQueue manages ordered transfer execution.
type TransferQueue struct {
	mu       sync.Mutex
	pending  []*QueuedTransfer
	active   map[string]*QueuedTransfer
	maxActive int
}

// NewTransferQueue creates a queue with the given concurrency limit.
func NewTransferQueue(maxActive int) *TransferQueue {
	if maxActive < 1 {
		maxActive = 3
	}
	return &TransferQueue{
		active:    make(map[string]*QueuedTransfer),
		maxActive: maxActive,
	}
}

// Enqueue adds a transfer to the queue. Returns the queued transfer's ID.
func (q *TransferQueue) Enqueue(filePath, peerID, direction string, priority TransferPriority) string {
	q.mu.Lock()
	defer q.mu.Unlock()

	id := fmt.Sprintf("q-%d-%s", time.Now().UnixNano(), randomHex(4))
	qt := &QueuedTransfer{
		ID:        id,
		FilePath:  filePath,
		PeerID:    peerID,
		Priority:  priority,
		Direction: direction,
		QueuedAt:  time.Now(),
	}

	// Insert by priority (higher priority first, then FIFO within same priority).
	inserted := false
	for i, existing := range q.pending {
		if priority > existing.Priority {
			q.pending = append(q.pending[:i+1], q.pending[i:]...)
			q.pending[i] = qt
			inserted = true
			break
		}
	}
	if !inserted {
		q.pending = append(q.pending, qt)
	}

	return id
}

// Dequeue returns the next transfer to execute, or nil if queue is empty
// or max concurrent transfers are already running.
func (q *TransferQueue) Dequeue() *QueuedTransfer {
	q.mu.Lock()
	defer q.mu.Unlock()

	if len(q.active) >= q.maxActive || len(q.pending) == 0 {
		return nil
	}

	qt := q.pending[0]
	q.pending = q.pending[1:]
	q.active[qt.ID] = qt
	return qt
}

// Complete marks a queued transfer as done.
func (q *TransferQueue) Complete(id string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.active, id)
}

// Cancel removes a pending transfer from the queue.
func (q *TransferQueue) Cancel(id string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()

	for i, qt := range q.pending {
		if qt.ID == id {
			q.pending = append(q.pending[:i], q.pending[i+1:]...)
			return true
		}
	}
	return false
}

// Pending returns a snapshot of pending transfers.
func (q *TransferQueue) Pending() []*QueuedTransfer {
	q.mu.Lock()
	defer q.mu.Unlock()

	result := make([]*QueuedTransfer, len(q.pending))
	copy(result, q.pending)
	return result
}

// ActiveCount returns the number of currently executing transfers.
func (q *TransferQueue) ActiveCount() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.active)
}
