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
	ID         string            `json:"id"`          // opaque share ID (never reveals filesystem path)
	Path       string            `json:"path"`        // absolute path on sharer's filesystem (NEVER sent to peers)
	Name       string            `json:"name"`        // user-friendly name shown to peers (defaults to dir/file basename)
	Peers      map[peer.ID]bool  `json:"-"`           // allowed peers (nil = all authorized)
	PeerIDs    []string          `json:"peers"`       // serialization form
	Persistent bool              `json:"persistent"`  // survive daemon restart
	SharedAt   time.Time         `json:"shared_at"`
	IsDir      bool              `json:"is_dir"`
}

// BrowseEntry is a single item returned by the browse protocol.
// Path is ALWAYS relative to the share root. Never contains absolute paths.
// ShareID identifies which share this entry belongs to (for download requests).
type BrowseEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`      // relative path within shared root (NEVER absolute)
	ShareID string `json:"share_id"`  // opaque share identifier for download
	Size    int64  `json:"size"`
	IsDir   bool   `json:"is_dir"`
	ModTime int64  `json:"mod_time"`  // unix timestamp
}

// ShareRegistry manages shared paths and their per-peer ACLs.
// Thread-safe. Lives in the daemon. Persistent shares survive restarts.
type ShareRegistry struct {
	mu              sync.RWMutex
	shares          map[string]*ShareEntry // path -> entry
	persistPath     string                 // file path for persistent share storage
	browseRateLimit *transferRateLimiter    // per-peer browse rate limiter (nil = disabled)
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

// SetBrowseRateLimit sets the per-peer browse request rate limit (max per minute).
// Call with 0 to disable. Must be called before HandleBrowse is registered.
func (r *ShareRegistry) SetBrowseRateLimit(maxPerMin int) {
	if maxPerMin > 0 {
		r.browseRateLimit = newTransferRateLimiter(maxPerMin)
	}
}

// persistentShareFile is the JSON structure written to disk.
type persistentShareFile struct {
	Shares []persistentShareEntry `json:"shares"`
}

// persistentShareEntry is the serializable form of a ShareEntry.
type persistentShareEntry struct {
	ID         string   `json:"id"`
	Path       string   `json:"path"`
	Name       string   `json:"name,omitempty"`
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
			ID:       entry.ID,
			Path:     entry.Path,
			Name:     entry.Name,
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

		// Restore or generate share ID.
		shareID := pe.ID
		if shareID == "" {
			shareID = generateShareID() // backward compat: old files without ID
		}
		shareName := pe.Name
		if shareName == "" {
			shareName = filepath.Base(pe.Path)
		}
		reg.shares[pe.Path] = &ShareEntry{
			ID:         shareID,
			Path:       pe.Path,
			Name:       shareName,
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

// generateShareID creates an opaque share ID. Uses random hex to avoid
// leaking any information about the shared path or timing.
func generateShareID() string {
	return "share-" + randomHex(8)
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
	// Reuse existing ID if re-sharing same path.
	existingID := ""
	if existing, ok := r.shares[absPath]; ok {
		existingID = existing.ID
	}
	if existingID == "" {
		existingID = generateShareID()
	}
	r.shares[absPath] = &ShareEntry{
		ID:         existingID,
		Path:       absPath,
		Name:       filepath.Base(absPath),
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

// LookupShareByID finds a share entry by its opaque share ID.
// Returns the entry and whether the given peer has access.
func (r *ShareRegistry) LookupShareByID(shareID string, peerID peer.ID) (*ShareEntry, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, entry := range r.shares {
		if entry.ID == shareID {
			if !r.peerAllowed(entry, peerID) {
				return nil, false
			}
			return entry, true
		}
	}
	return nil, false
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
// All paths in the response are RELATIVE to the share root. The absolute
// server-side path is NEVER exposed to remote peers.
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
				Path:    filepath.Base(share.Path),
				ShareID: share.ID,
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
				Path:    de.Name(),
				ShareID: share.ID,
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

		// Per-peer browse rate limit check.
		if r.browseRateLimit != nil && !r.browseRateLimit.allow(remotePeer.String()) {
			slog.Warn("file-browse: rate limit exceeded",
				"peer", remotePeer.String()[:16]+"...")
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
		// browseRoot is now shareID or shareID/subpath.
		// Parse share ID and optional subpath.
		shareID, subPath := browseRoot, ""
		if idx := strings.IndexByte(browseRoot, '/'); idx >= 0 {
			shareID = browseRoot[:idx]
			subPath = browseRoot[idx+1:]
		}

		share, ok := r.LookupShareByID(shareID, peerID)
		if !ok {
			writeBrowseError(s, "access denied")
			return
		}

		// Use os.Root for atomic path safety.
		root, err := os.OpenRoot(share.Path)
		if err != nil {
			writeBrowseError(s, "not found")
			return
		}
		defer root.Close()

		dirPath := share.Path
		if subPath != "" {
			// Validate subpath is within share using os.Root.
			sub, err := root.Open(subPath)
			if err != nil {
				writeBrowseError(s, "not found")
				return
			}
			sub.Close()
			dirPath = filepath.Join(share.Path, subPath)
		}

		entries = r.listDirectory(dirPath, share.Path, share.ID)
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
	// Read requested path: format is "shareID/relativePath" or "shareID"
	// for single-file shares. NEVER accepts absolute paths.
	var pathLen uint16
	if err := binary.Read(s, binary.BigEndian, &pathLen); err != nil {
		writeDownloadError(s, "invalid request")
		return
	}
	if pathLen == 0 || pathLen > maxPathLength {
		writeDownloadError(s, "invalid path length")
		return
	}

	pathBuf := make([]byte, pathLen)
	if _, err := io.ReadFull(s, pathBuf); err != nil {
		return
	}
	requestedPath := string(pathBuf)

	// Reject absolute paths (security: never let clients reference server filesystem).
	if filepath.IsAbs(requestedPath) {
		writeDownloadError(s, "absolute paths not allowed")
		return
	}

	// Parse shareID and relative path.
	shareID, relPath := requestedPath, ""
	if idx := strings.IndexByte(requestedPath, '/'); idx >= 0 {
		shareID = requestedPath[:idx]
		relPath = requestedPath[idx+1:]
	}

	// Look up the share and verify peer access.
	share, ok := r.LookupShareByID(shareID, peerID)
	if !ok {
		writeDownloadError(s, "access denied")
		return
	}

	// Use os.Root for atomic path traversal safety.
	// This prevents TOCTOU races, symlink escapes, and ../../../ attacks.
	root, err := os.OpenRoot(share.Path)
	if err != nil {
		writeDownloadError(s, "not found")
		return
	}
	defer root.Close()

	// For single-file shares without a subpath, use the share's basename.
	if relPath == "" && !share.IsDir {
		relPath = filepath.Base(share.Path)
		// Single file share: open the parent as root, file as relative.
		root.Close()
		root, err = os.OpenRoot(filepath.Dir(share.Path))
		if err != nil {
			writeDownloadError(s, "not found")
			return
		}
	}

	if relPath == "" {
		writeDownloadError(s, "no file specified")
		return
	}

	// Open within the jailed root. os.Root blocks traversal atomically.
	f, err := root.Open(relPath)
	if err != nil {
		writeDownloadError(s, "not found")
		return
	}

	info, err := f.Stat()
	f.Close()
	if err != nil {
		writeDownloadError(s, "not found")
		return
	}

	if info.IsDir() {
		// Return directory listing with relative paths.
		absDir := filepath.Join(share.Path, relPath)
		entries := r.listDirectory(absDir, share.Path, share.ID)
		data, _ := json.Marshal(entries)
		var header [5]byte
		header[0] = msgBrowseResponse
		binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
		s.Write(header[:])
		s.Write(data)
		return
	}

	// Single file: confirm exists and return metadata (no absolute paths).
	if !info.Mode().IsRegular() {
		writeDownloadError(s, "not a regular file")
		return
	}

	entry := BrowseEntry{
		Name:    info.Name(),
		Path:    relPath,
		ShareID: share.ID,
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
// relativeTo is the share root - all paths in the result are relative to it.
// shareID is included in each entry for download requests.
func (r *ShareRegistry) listDirectory(dirPath, relativeTo, shareID string) []BrowseEntry {
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

		// Build path relative to share root.
		fullPath := filepath.Join(dirPath, de.Name())
		relPath, err := filepath.Rel(relativeTo, fullPath)
		if err != nil {
			relPath = de.Name()
		}

		entries = append(entries, BrowseEntry{
			Name:    de.Name(),
			Path:    relPath,
			ShareID: shareID,
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

		// Reject absolute paths (never let clients reference server filesystem).
		if filepath.IsAbs(requestedPath) {
			writeDownloadError(s, "absolute paths not allowed")
			s.Close()
			return
		}

		// Parse shareID and relative path: "shareID/file.bin" or "shareID".
		shareID, relPath := requestedPath, ""
		if idx := strings.IndexByte(requestedPath, '/'); idx >= 0 {
			shareID = requestedPath[:idx]
			relPath = requestedPath[idx+1:]
		}

		// Look up the share and verify peer access.
		share, ok := r.LookupShareByID(shareID, remotePeer)
		if !ok {
			writeDownloadError(s, "access denied")
			s.Close()
			return
		}

		// Use os.Root for atomic path traversal safety.
		root, err := os.OpenRoot(share.Path)
		if err != nil {
			writeDownloadError(s, "not found")
			s.Close()
			return
		}
		defer root.Close()

		// For single-file shares without a subpath, use the share's basename.
		if relPath == "" && !share.IsDir {
			relPath = filepath.Base(share.Path)
			root.Close()
			root, err = os.OpenRoot(filepath.Dir(share.Path))
			if err != nil {
				writeDownloadError(s, "not found")
				s.Close()
				return
			}
		}

		if relPath == "" {
			writeDownloadError(s, "no file specified")
			s.Close()
			return
		}

		// Open within the jailed root. os.Root blocks traversal atomically.
		f, err := root.Open(relPath)
		if err != nil {
			writeDownloadError(s, "not found")
			s.Close()
			return
		}

		info, err := f.Stat()
		f.Close()
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

		if !info.Mode().IsRegular() {
			writeDownloadError(s, "not a regular file")
			s.Close()
			return
		}

		// Resolve the full filesystem path for SendFile (within jailed root).
		filePath := filepath.Join(share.Path, relPath)
		if !share.IsDir {
			filePath = share.Path
		}

		short := remotePeer.String()[:16] + "..."
		slog.Info("file-download: serving file",
			"peer", short, "path", info.Name(),
			"size", info.Size())

		// Send the file using existing chunked transfer (SHFT format).
		_, sendErr := ts.SendFile(s, filePath)
		if sendErr != nil {
			slog.Error("file-download: send failed",
				"peer", short, "path", info.Name(), "error", sendErr)
			writeDownloadError(s, "internal error")
			s.Close()
		}
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
	mu             sync.Mutex
	pending        []*QueuedTransfer
	active         map[string]*QueuedTransfer
	maxActive      int
	maxPending     int // max total pending items (0 = unlimited)
	maxPerPeer     int // max pending items per peer (0 = unlimited)
}

// NewTransferQueue creates a queue with the given concurrency limit.
func NewTransferQueue(maxActive int) *TransferQueue {
	if maxActive < 1 {
		maxActive = 3
	}
	return &TransferQueue{
		active:     make(map[string]*QueuedTransfer),
		maxActive:  maxActive,
		maxPending: 1000, // match persistence limit
		maxPerPeer: 100,  // prevent one peer from monopolizing queue
	}
}

// ErrQueueFull is returned when the outbound queue has reached its pending limit.
var ErrQueueFull = fmt.Errorf("transfer queue is full")

// ErrPeerQueueFull is returned when a single peer has too many pending transfers.
var ErrPeerQueueFull = fmt.Errorf("per-peer queue limit reached")

// Enqueue adds a transfer to the queue. Returns the queued transfer's ID.
// Returns ErrQueueFull if the global queue limit is reached, or
// ErrPeerQueueFull if the per-peer limit is reached.
func (q *TransferQueue) Enqueue(filePath, peerID, direction string, priority TransferPriority) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	if q.maxPending > 0 && len(q.pending) >= q.maxPending {
		return "", ErrQueueFull
	}

	// Per-peer limit.
	if q.maxPerPeer > 0 {
		peerCount := 0
		for _, qt := range q.pending {
			if qt.PeerID == peerID {
				peerCount++
			}
		}
		if peerCount >= q.maxPerPeer {
			return "", ErrPeerQueueFull
		}
	}

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

	return id, nil
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
