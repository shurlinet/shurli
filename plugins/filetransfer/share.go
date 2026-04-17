package filetransfer

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
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
	"github.com/shurlinet/shurli/pkg/sdk"
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

	// Download request types (C2: hash probe support).
	// requestTypeDownload is a full file download (sender calls SendFile).
	requestTypeDownload byte = 0x01
	// requestTypeProbe is a hash probe (sender chunks file, returns 45-byte response).
	requestTypeProbe byte = 0x02

	// probeResponseSize is the wire size of a hash probe response:
	// marker(1) + rootHash(32) + totalSize(8) + chunkCount(4) = 45 bytes.
	probeResponseSize = 45

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
	hmacKey         []byte                 // P6 fix: HMAC key for shares.json integrity
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

// SetHMACKey sets the HMAC key for shares.json integrity verification (P6 fix).
func (r *ShareRegistry) SetHMACKey(key []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hmacKey = key
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
	HMAC   string                 `json:"hmac,omitempty"` // P6 fix: HMAC-SHA256 integrity
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

	pf := persistentShareFile{Shares: entries}

	// P6 fix: compute HMAC over shares JSON if key is set.
	if len(r.hmacKey) > 0 {
		sharesJSON, _ := json.Marshal(entries)
		mac := hmac.New(sha256.New, r.hmacKey)
		mac.Write(sharesJSON)
		pf.HMAC = hex.EncodeToString(mac.Sum(nil))
	}

	data, err := json.MarshalIndent(pf, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal shares: %w", err)
	}

	// Atomic write: tmp file + rename.
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create dir: %w", err)
	}

	// P5 fix: write + fsync + rename for crash-safe persistence.
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync tmp: %w", err)
	}
	f.Close()
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
		// P12 fix: validate persisted paths. Skip entries with traversal or non-existent paths.
		cleanPath := filepath.Clean(pe.Path)
		if strings.Contains(cleanPath, "..") || cleanPath == "" {
			slog.Warn("share-persistence: skipping path with traversal", "path", pe.Path)
			continue
		}
		if _, err := os.Stat(cleanPath); err != nil {
			slog.Warn("share-persistence: skipping non-existent path", "path", cleanPath, "err", err)
			continue
		}
		pe.Path = cleanPath // use cleaned path

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

// Share adds or updates a shared path. If the path is already shared, the
// existing entry is returned (with peers merged if new peers are provided)
// instead of creating a duplicate. Returns the share entry and any error.
func (r *ShareRegistry) Share(path string, peers []peer.ID, persistent bool) (*ShareEntry, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve path: %w", err)
	}

	// Validate path exists.
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("path not accessible: %w", err)
	}

	// Build peer map from new peers.
	var newPeers map[peer.ID]bool
	if len(peers) > 0 {
		newPeers = make(map[peer.ID]bool, len(peers))
		for _, p := range peers {
			newPeers[p] = true
		}
	}

	r.mu.Lock()
	var entry *ShareEntry
	if existing, ok := r.shares[absPath]; ok {
		// Path already shared - return existing entry, merge new peers.
		if newPeers != nil {
			if existing.Peers == nil {
				// Share was open to all; now restricting to specific peers.
				slog.Info("share restricted from all-authorized to specific peers",
					"path", absPath, "peers", len(newPeers))
				existing.Peers = newPeers
			} else {
				for p := range newPeers {
					existing.Peers[p] = true
				}
			}
		}
		// Preserve existing persistence setting on merge.
		// Persistence is a property of the share, not the add operation.
		entry = existing
	} else {
		// New share.
		entry = &ShareEntry{
			ID:         generateShareID(),
			Path:       absPath,
			Name:       filepath.Base(absPath),
			Peers:      newPeers,
			Persistent: persistent,
			SharedAt:   time.Now(),
			IsDir:      info.IsDir(),
		}
		r.shares[absPath] = entry
	}
	r.mu.Unlock()

	r.savePersistentIfNeeded()
	return entry, nil
}

// DenyPeer removes a peer from a share's peer list.
func (r *ShareRegistry) DenyPeer(path string, peerID peer.ID) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	err = func() error {
		r.mu.Lock()
		defer r.mu.Unlock()

		entry, ok := r.shares[absPath]
		if !ok {
			return fmt.Errorf("path %q not shared", absPath)
		}
		if entry.Peers == nil {
			return fmt.Errorf("share is open to all authorized peers; use --to to restrict first")
		}
		if !entry.Peers[peerID] {
			return fmt.Errorf("peer not in this share's peer list")
		}
		if len(entry.Peers) == 1 {
			return fmt.Errorf("cannot remove the last peer; use 'shurli share remove' to unshare entirely")
		}
		delete(entry.Peers, peerID)
		return nil
	}()
	if err != nil {
		return err
	}

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
func (r *ShareRegistry) HandleBrowse() sdk.StreamHandler {
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
// 1. Reads the requested path + requestType (C2)
// 2. Verifies the peer has ACL access to the path
// 3. Dispatches: requestType=0x01 sends file (SHFT streaming),
//    requestType=0x02 returns 45-byte hash probe response
//
// Wire format received: pathLen(2) + path + requestType(1).
func (r *ShareRegistry) HandleDownload(ts *TransferService) sdk.StreamHandler {
	return func(serviceName string, s network.Stream) {
		remotePeer := s.Conn().RemotePeer()

		// Stream ownership: all paths close the stream except requestTypeDownload,
		// where SendFile's background goroutine takes ownership and closes it.
		streamOwned := false
		defer func() {
			if !streamOwned {
				s.Close()
			}
		}()

		// Check if peer has any visible shares.
		shares := r.ListShares(&remotePeer)
		if len(shares) == 0 {
			s.Reset()
			return
		}

		s.SetDeadline(time.Now().Add(transferStreamDeadline))

		// Read requested path: pathLen(2) + path + requestType(1).
		var pathLen uint16
		if err := binary.Read(s, binary.BigEndian, &pathLen); err != nil {
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

		// Read request type (C2).
		var reqType [1]byte
		if _, err := io.ReadFull(s, reqType[:]); err != nil {
			return
		}

		// Reject absolute paths (never let clients reference server filesystem).
		if filepath.IsAbs(requestedPath) {
			writeDownloadError(s, "absolute paths not allowed")
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
			return
		}

		// Use os.Root for atomic path traversal safety.
		// For single-file shares, open parent dir as root (os.OpenRoot fails on files).
		rootPath := share.Path
		if !share.IsDir {
			rootPath = filepath.Dir(share.Path)
			if relPath == "" {
				relPath = filepath.Base(share.Path)
			}
		}
		root, err := os.OpenRoot(rootPath)
		if err != nil {
			writeDownloadError(s, "not found")
			return
		}
		defer root.Close()

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
			writeDownloadError(s, "cannot download directory; use browse + per-file download")
			return
		}

		if !info.Mode().IsRegular() {
			writeDownloadError(s, "not a regular file")
			return
		}

		// Resolve the full filesystem path (within jailed root).
		filePath := filepath.Join(share.Path, relPath)
		if !share.IsDir {
			filePath = share.Path
		}

		short := remotePeer.String()[:16] + "..."

		switch reqType[0] {
		case requestTypeDownload:
			// Full download: send via streaming SHFT protocol.
			// SendFile spawns a background goroutine that owns the stream.
			slog.Info("file-download: serving file",
				"peer", short, "path", info.Name(),
				"size", info.Size())

			_, sendErr := ts.SendFile(s, filePath)
			if sendErr != nil {
				slog.Error("file-download: send failed",
					"peer", short, "path", info.Name(), "error", sendErr)
				writeDownloadError(s, "internal error")
				return // defer closes stream
			}
			// SendFile's goroutine now owns the stream. Don't close it.
			streamOwned = true

		case requestTypeProbe:
			// Rate limit probe requests (CPU DoS: each probe chunks the entire file).
			// Reuse browse rate limiter since probes are metadata queries.
			if r.browseRateLimit != nil && !r.browseRateLimit.allow(remotePeer.String()) {
				slog.Warn("file-download: probe rate limit exceeded",
					"peer", short)
				writeDownloadError(s, "rate limit exceeded")
				return
			}

			// Hash probe (C2): chunk the file, compute MerkleRoot, return 45-byte response.
			slog.Info("file-download: hash probe",
				"peer", short, "path", info.Name(),
				"size", info.Size())

			// Timeout bounds the chunking duration. For a 1TB file at ~200 MB/s
			// disk read, chunking takes ~80s. 2 minutes covers worst case with
			// margin. The per-chunk ctx.Done() check in handleHashProbe aborts
			// early on timeout. If the peer disconnects, the final Write fails
			// and the handler exits cleanly.
			probeCtx, probeCancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer probeCancel()
			probeErr := handleHashProbe(probeCtx, s, root, relPath, info.Size(), ts.RegisterHash)
			if probeErr != nil {
				slog.Error("file-download: probe failed",
					"peer", short, "path", info.Name(), "error", probeErr)
				writeDownloadError(s, "internal error")
			}
			// defer closes stream

		default:
			writeDownloadError(s, "unknown request type")
			// defer closes stream
		}
	}
}

// handleHashProbe chunks a file within the jailed root, computes the Merkle
// root, and writes the 45-byte probe response:
// 'P'(1) + rootHash(32) + totalSize(8) + chunkCount(4).
//
// The returned hash is specific to this file's chunking strategy: ChunkTarget
// uses fileSize to determine chunk boundaries. If the same file is later served
// as part of a directory transfer, the directory uses totalSize (sum of all files)
// for ChunkTarget, producing different chunk boundaries and a different hash.
// Multi-peer download must probe the exact share entry being downloaded. [R3-UA2]
//
// Security: opens file through os.Root to prevent TOCTOU symlink attacks between
// the stat in HandleDownload and the open here. Checks stream context on each
// chunk to abort if the peer disconnects mid-probe. [C2]
func handleHashProbe(ctx context.Context, w io.Writer, root *os.Root, relPath string, fileSize int64, onHash func([32]byte, string)) error {
	// writeProbeResponse builds and writes the 45-byte probe response.
	writeProbeResponse := func(rootHash [32]byte, size int64, chunkCount int) error {
		var resp [probeResponseSize]byte
		resp[0] = 'P'
		copy(resp[1:33], rootHash[:])
		binary.BigEndian.PutUint64(resp[33:41], uint64(size))
		binary.BigEndian.PutUint32(resp[41:45], uint32(chunkCount))
		_, err := w.Write(resp[:])
		return err
	}

	// Empty file: deterministic zero hash, no chunking needed.
	if fileSize == 0 {
		return writeProbeResponse(sdk.MerkleRoot(nil), 0, 0)
	}

	f, err := root.Open(relPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	// Collect chunk hashes via ChunkReader.
	// Check context every chunk to abort early if peer disconnected or timed out.
	var chunkHashes [][32]byte
	if err := ChunkReader(f, fileSize, func(c Chunk) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		chunkHashes = append(chunkHashes, c.Hash)
		return nil
	}); err != nil {
		return fmt.Errorf("chunk file: %w", err)
	}

	rootHash := sdk.MerkleRoot(chunkHashes)
	// IF6-2: Register hash so multi-peer serving can find this file.
	// The probe already paid the chunking cost — RegisterHash is free.
	if onHash != nil {
		onHash(rootHash, filepath.Join(root.Name(), relPath))
	}
	return writeProbeResponse(rootHash, fileSize, len(chunkHashes))
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

// sendDownloadRequest writes the download request wire format and reads the
// first response byte. Shared by RequestDownload and RequestProbe.
// Wire format sent: pathLen(2) + path + requestType(1).
// Returns the first response byte on success, or an error if the remote
// sent an error response (0xFF + errLen + errMsg).
func sendDownloadRequest(s network.Stream, remotePath string, requestType byte, deadline time.Duration) (byte, error) {
	s.SetDeadline(time.Now().Add(deadline))

	pathBytes := []byte(remotePath)
	if err := binary.Write(s, binary.BigEndian, uint16(len(pathBytes))); err != nil {
		return 0, fmt.Errorf("write path length: %w", err)
	}
	if _, err := s.Write(pathBytes); err != nil {
		return 0, fmt.Errorf("write path: %w", err)
	}
	if _, err := s.Write([]byte{requestType}); err != nil {
		return 0, fmt.Errorf("write request type: %w", err)
	}

	// Read first response byte to determine success or error.
	var firstByte [1]byte
	if _, err := io.ReadFull(s, firstByte[:]); err != nil {
		return 0, fmt.Errorf("read response: %w", err)
	}

	if firstByte[0] == msgDownloadError {
		var errLen uint16
		if err := binary.Read(s, binary.BigEndian, &errLen); err != nil {
			return 0, fmt.Errorf("read error length: %w", err)
		}
		if errLen > maxPathLength {
			return 0, fmt.Errorf("error message too large")
		}
		errBuf := make([]byte, errLen)
		if _, err := io.ReadFull(s, errBuf); err != nil {
			return 0, fmt.Errorf("read error message: %w", err)
		}
		return 0, fmt.Errorf("remote: %s", string(errBuf))
	}

	return firstByte[0], nil
}

// RequestDownload sends a download request (requestType=0x01) and returns a
// reader that replays the consumed first byte followed by the rest of the
// stream. The caller reads SHFT streaming data from the returned reader.
func RequestDownload(s network.Stream, remotePath string) (io.Reader, error) {
	firstByte, err := sendDownloadRequest(s, remotePath, requestTypeDownload, browseTimeout)
	if err != nil {
		return nil, err
	}

	s.SetDeadline(time.Time{}) // reset deadline for transfer

	// Prepend the consumed first byte (part of SHFT magic) back onto the stream.
	prefixed := io.MultiReader(
		&singleByteReader{b: firstByte},
		s,
	)
	return prefixed, nil
}

// HashProbeResult holds the response from a hash probe request (C2).
type HashProbeResult struct {
	RootHash   [32]byte
	TotalSize  int64
	ChunkCount uint32
}

// RequestProbe sends a hash probe request (requestType=0x02) and reads the
// 45-byte probe response: 'P'(1) + rootHash(32) + totalSize(8) + chunkCount(4).
// Used by multi-peer download to discover the file's Merkle root hash before
// fanning out to multiple peers (C2).
func RequestProbe(s network.Stream, remotePath string) (*HashProbeResult, error) {
	// Probe needs longer deadline: server must chunk the entire file
	// to compute MerkleRoot (~2.5s per 500MB). 2 minutes covers up to ~1TB.
	firstByte, err := sendDownloadRequest(s, remotePath, requestTypeProbe, 2*time.Minute)
	if err != nil {
		return nil, err
	}

	if firstByte != 'P' {
		return nil, fmt.Errorf("unexpected probe response marker: 0x%02x", firstByte)
	}

	// Read remaining 44 bytes.
	var rest [probeResponseSize - 1]byte
	if _, err := io.ReadFull(s, rest[:]); err != nil {
		return nil, fmt.Errorf("read probe response: %w", err)
	}

	var rootHash [32]byte
	copy(rootHash[:], rest[:32])

	return &HashProbeResult{
		RootHash:   rootHash,
		TotalSize:  int64(binary.BigEndian.Uint64(rest[32:40])),
		ChunkCount: binary.BigEndian.Uint32(rest[40:44]),
	}, nil
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

	// Transfer ID: `t-YYYYMMDD-{hex}`. Stable from submission through
	// completion. The `t-` prefix replaces the old `q-` which visually
	// suggested "queued" even after the transfer went active; the status
	// column ("queued"/"active"/"complete") is the source of truth for state.
	// The date prefix keeps human-level forensics without the noise of a
	// full nanosecond timestamp.
	id := fmt.Sprintf("t-%s-%s", time.Now().Format("20060102"), randomHex(4))
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

// Requeue moves an active transfer back to the pending queue for retry.
// The existing ID is preserved so progress tracking continues seamlessly.
func (q *TransferQueue) Requeue(id, filePath, peerID, direction string, priority TransferPriority) {
	q.mu.Lock()
	defer q.mu.Unlock()

	// Remove from active.
	delete(q.active, id)

	qt := &QueuedTransfer{
		ID:        id,
		FilePath:  filePath,
		PeerID:    peerID,
		Priority:  priority,
		Direction: direction,
		QueuedAt:  time.Now(),
	}

	// Insert by priority (same logic as Enqueue).
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
