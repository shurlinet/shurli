package filetransfer

import "github.com/shurlinet/shurli/pkg/sdk"

// SendRequest is the body for POST /v1/send.
type SendRequest struct {
	Path       string `json:"path"`        // local file path to send
	Peer       string `json:"peer"`        // peer name or ID
	NoCompress bool   `json:"no_compress"` // disable zstd compression
	Streams    int    `json:"streams"`     // parallel stream count (0 = adaptive default)
	Priority   string `json:"priority"`    // "low", "normal" (default), "high"
}

// SendResponse is returned by POST /v1/send.
type SendResponse struct {
	TransferID string `json:"transfer_id"`
	Filename   string `json:"filename"`
	Size       int64  `json:"size"`
	PeerID     string `json:"peer_id"`
}

// TransferAcceptRequest is the body for POST /v1/transfers/{id}/accept.
type TransferAcceptRequest struct {
	Dest string `json:"dest,omitempty"` // override receive directory
}

// TransferRejectRequest is the body for POST /v1/transfers/{id}/reject.
type TransferRejectRequest struct {
	Reason string `json:"reason,omitempty"` // "space", "busy", "size"
}

// PendingTransferInfo is returned by GET /v1/transfers/pending.
type PendingTransferInfo struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	PeerID   string `json:"peer_id"`
	Time     string `json:"time"`
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
	Entries []sdk.BrowseEntry `json:"entries"`
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
}

// DownloadResponse is returned by POST /v1/download.
type DownloadResponse struct {
	TransferID string `json:"transfer_id"`
	FileName   string `json:"filename"`
	FileSize   int64  `json:"file_size"`
}
