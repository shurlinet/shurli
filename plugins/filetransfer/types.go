package filetransfer

import "fmt"


// SendRequest is the body for POST /v1/send.
type SendRequest struct {
	Path       string `json:"path"`        // local file path to send
	Peer       string `json:"peer"`        // peer name or ID
	NoCompress bool   `json:"no_compress"` // disable zstd compression
	Streams    int    `json:"streams"`     // parallel stream count (0 = adaptive default)
	Priority   string `json:"priority"`    // "low", "normal" (default), "high"
	RateLimit  string `json:"rate_limit"`  // send rate limit e.g. "100M" (empty = service default)
}

// SendResponse is returned by POST /v1/send.
type SendResponse struct {
	TransferID string `json:"transfer_id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	PeerID     string `json:"peer_id"`
}

// TransferAcceptRequest is the body for POST /v1/transfers/{id}/accept.
// Selective file rejection (#18): Files and Exclude are 0-indexed file indices.
// - Omit both: accept all files (default, backward-compatible).
// - Files: accept ONLY these files, reject the rest.
// - Exclude: accept all EXCEPT these files.
// - Both present: 400 error (mutually exclusive).
// - Files or Exclude with empty array []: 400 error (R7-F3).
type TransferAcceptRequest struct {
	Dest    string `json:"dest,omitempty"`    // override receive directory
	Files   []int  `json:"files,omitempty"`   // 0-indexed file indices to accept (nil = all)
	Exclude []int  `json:"exclude,omitempty"` // 0-indexed file indices to reject (nil = none)
}

// TransferRejectRequest is the body for POST /v1/transfers/{id}/reject.
type TransferRejectRequest struct {
	Reason string `json:"reason,omitempty"` // "space", "busy", "size"
}

// PendingTransferInfo is returned by GET /v1/transfers/pending.
type PendingTransferInfo struct {
	ID         string            `json:"id"`
	Filename   string            `json:"filename"`
	Size       int64             `json:"size"`
	PeerID     string            `json:"peer_id"`
	Time       string            `json:"time"`
	FileCount  int               `json:"file_count"`            // total files in transfer
	Files      []PendingFileInfo `json:"files,omitempty"`       // per-file info (selective rejection #18)
	HasErasure bool              `json:"has_erasure,omitempty"` // sender uses erasure coding (gates selective rejection)
}

// PendingFileInfo describes a single file in a pending multi-file transfer.
// Index is 0-indexed (matches wire format and TransferAcceptRequest.Files).
type PendingFileInfo struct {
	Index int    `json:"index"` // 0-indexed position in the file table
	Path  string `json:"path"`  // relative path (sanitized, forward slashes)
	Size  int64  `json:"size"`  // file size in bytes
}

// FileSelection specifies which files to include or exclude in a download (#18).
// Nil = all files. Include and Exclude are mutually exclusive (0-indexed).
type FileSelection struct {
	Include []int // accept ONLY these file indices
	Exclude []int // accept all EXCEPT these file indices
}

// resolve converts a FileSelection into accepted 0-indexed file indices given fileCount.
// Returns nil for full accept (all files). Returns error for out-of-range indices.
func (s *FileSelection) resolve(fileCount int) ([]int, error) {
	if s == nil {
		return nil, nil
	}
	if len(s.Include) > 0 {
		// Validate all include indices are in range.
		for _, idx := range s.Include {
			if idx < 0 || idx >= fileCount {
				return nil, fmt.Errorf("file index %d out of range (transfer has %d files, valid range 0-%d)", idx, fileCount, fileCount-1)
			}
		}
		return s.Include, nil
	}
	if len(s.Exclude) > 0 {
		// Validate all exclude indices are in range.
		for _, idx := range s.Exclude {
			if idx < 0 || idx >= fileCount {
				return nil, fmt.Errorf("file index %d out of range (transfer has %d files, valid range 0-%d)", idx, fileCount, fileCount-1)
			}
		}
		excludeSet := make(map[int]bool, len(s.Exclude))
		for _, idx := range s.Exclude {
			excludeSet[idx] = true
		}
		var accepted []int
		for i := 0; i < fileCount; i++ {
			if !excludeSet[i] {
				accepted = append(accepted, i)
			}
		}
		return accepted, nil
	}
	return nil, nil
}

// ShareRequest is the body for POST /v1/shares.
type ShareRequest struct {
	Path       string   `json:"path"`                // path to share
	Peers      []string `json:"peers,omitempty"`      // peer IDs (empty = all authorized)
	Persistent *bool    `json:"persistent,omitempty"` // nil = use config default, true/false = explicit
}

// UnshareRequest is the body for DELETE /v1/shares.
type UnshareRequest struct {
	Path string `json:"path"`
}

// ShareDenyRequest is the body for POST /v1/shares/deny.
type ShareDenyRequest struct {
	Path string `json:"path"` // shared path
	Peer string `json:"peer"` // peer name or ID to remove
}

// BrowseRequest is the body for POST /v1/browse.
type BrowseRequest struct {
	Peer    string `json:"peer"`               // peer name or ID
	SubPath string `json:"sub_path,omitempty"` // browse within a shared directory
}

// BrowseResponse is returned by POST /v1/browse.
type BrowseResponse struct {
	Entries []BrowseEntry `json:"entries"`
	Error   string               `json:"error,omitempty"`
}

// ShareInfo is returned by GET /v1/shares.
type ShareInfo struct {
	Path       string   `json:"path"`
	Peers      []string `json:"peers,omitempty"`
	Persistent bool     `json:"persistent"`
	IsDir      bool     `json:"is_dir"`
	SharedAt   string   `json:"shared_at"`
}

// DownloadRequest is the body for POST /v1/download.
type DownloadRequest struct {
	Peer       string   `json:"peer"`                  // peer name or ID
	RemotePath string   `json:"remote_path"`           // path on the remote peer's share
	LocalDest  string   `json:"local_dest"`            // local directory to save into (empty = configured receive dir)
	MultiPeer  bool     `json:"multi_peer,omitempty"`  // enable multi-peer swarming download
	ExtraPeers []string `json:"extra_peers,omitempty"` // additional peer names/IDs that have the file
	Files      []int    `json:"files,omitempty"`       // 0-indexed: download ONLY these files (selective rejection #18)
	Exclude    []int    `json:"exclude,omitempty"`     // 0-indexed: download all EXCEPT these files (#18)
}

// DownloadResponse is returned by POST /v1/download.
type DownloadResponse struct {
	TransferID string `json:"transfer_id"`
	FileName   string `json:"filename"`
	FileSize   int64  `json:"file_size"`
	PeersUsed  int    `json:"peers_used,omitempty"` // IF3-7: multi-peer peer count
}
