package filetransfer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/shurlinet/shurli/internal/config"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// daemonClient connects to a running daemon via its Unix socket.
// This is a self-contained client for plugin CLI commands that can't
// use cmd/shurli's unexported helpers (different package).
type daemonClient struct {
	httpClient *http.Client
	authToken  string
}

// newDaemonClient creates a daemon client using default config paths.
func newDaemonClient() (*daemonClient, error) {
	dir, err := config.DefaultConfigDir()
	if err != nil {
		return nil, fmt.Errorf("cannot determine config directory: %w", err)
	}
	socketPath := filepath.Join(dir, "shurli.sock")
	cookiePath := filepath.Join(dir, ".daemon-cookie")

	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("daemon not running (no socket at %s)", socketPath)
	}

	token, err := os.ReadFile(cookiePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read daemon cookie: %w", err)
	}

	return &daemonClient{
		authToken: strings.TrimSpace(string(token)),
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}, nil
}

// do sends an HTTP request to the daemon and returns the raw response body.
func (c *daemonClient) do(method, path string, body io.Reader, headers map[string]string) ([]byte, int, error) {
	url := "http://daemon" + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// errorResponse for parsing daemon errors.
type errorResponse struct {
	Error string `json:"error"`
}

// doJSON sends a request and decodes the JSON {"data": ...} envelope into target.
func (c *daemonClient) doJSON(method, path string, body io.Reader, target any) error {
	data, status, err := c.do(method, path, body, nil)
	if err != nil {
		return err
	}

	if status >= 400 {
		var errResp errorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return fmt.Errorf("daemon: %s", errResp.Error)
		}
		return fmt.Errorf("daemon returned HTTP %d", status)
	}

	if target != nil {
		var raw struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("failed to decode response: %w", err)
		}
		if err := json.Unmarshal(raw.Data, target); err != nil {
			return fmt.Errorf("failed to decode response data: %w", err)
		}
	}
	return nil
}

// doText sends a request with Accept: text/plain and returns the text body.
func (c *daemonClient) doText(method, path string, body io.Reader) (string, error) {
	data, status, err := c.do(method, path, body, map[string]string{"Accept": "text/plain"})
	if err != nil {
		return "", err
	}

	if status >= 400 {
		var errResp errorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return "", fmt.Errorf("daemon: %s", errResp.Error)
		}
		return "", fmt.Errorf("daemon returned HTTP %d", status)
	}

	return string(data), nil
}

// --- File sharing methods ---

// ShareAdd shares a path with specified peers (empty = all authorized).
func (c *daemonClient) ShareAdd(path string, peers []string, persistent *bool) error {
	req := ShareRequest{Path: path, Peers: peers, Persistent: persistent}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/shares", strings.NewReader(string(body)), nil)
}

// ShareRemove stops sharing a path.
func (c *daemonClient) ShareRemove(path string) error {
	req := UnshareRequest{Path: path}
	body, _ := json.Marshal(req)
	return c.doJSON("DELETE", "/v1/shares", strings.NewReader(string(body)), nil)
}

// ShareDeny removes a peer from a share's peer list.
func (c *daemonClient) ShareDeny(path, peerName string) error {
	req := ShareDenyRequest{Path: path, Peer: peerName}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/shares/deny", strings.NewReader(string(body)), nil)
}

// ShareList returns all shared paths.
func (c *daemonClient) ShareList() ([]ShareInfo, error) {
	var resp []ShareInfo
	if err := c.doJSON("GET", "/v1/shares", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ShareListText returns shared paths as plain text.
func (c *daemonClient) ShareListText() (string, error) {
	return c.doText("GET", "/v1/shares", nil)
}

// Browse browses a remote peer's shared files.
func (c *daemonClient) Browse(peer, subPath string) (*BrowseResponse, error) {
	req := BrowseRequest{Peer: peer, SubPath: subPath}
	body, _ := json.Marshal(req)
	var resp BrowseResponse
	if err := c.doJSON("POST", "/v1/browse", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// BrowseText browses a remote peer's shared files, returns plain text.
func (c *daemonClient) BrowseText(peer, subPath string) (string, error) {
	req := BrowseRequest{Peer: peer, SubPath: subPath}
	body, _ := json.Marshal(req)
	return c.doText("POST", "/v1/browse", strings.NewReader(string(body)))
}

// --- Download methods ---

// Download initiates a receiver-side file download from a peer's shared files.
func (c *daemonClient) Download(peer, remotePath, localDest string, multiPeer bool, extraPeers []string) (*DownloadResponse, error) {
	req := DownloadRequest{Peer: peer, RemotePath: remotePath, LocalDest: localDest, MultiPeer: multiPeer, ExtraPeers: extraPeers}
	body, _ := json.Marshal(req)
	var resp DownloadResponse
	if err := c.doJSON("POST", "/v1/download", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// DownloadText initiates a download and returns a plain text summary.
func (c *daemonClient) DownloadText(peer, remotePath, localDest string) (string, error) {
	req := DownloadRequest{Peer: peer, RemotePath: remotePath, LocalDest: localDest}
	body, _ := json.Marshal(req)
	return c.doText("POST", "/v1/download", strings.NewReader(string(body)))
}

// --- File transfer methods ---

// Send initiates a file transfer to a peer via the daemon.
func (c *daemonClient) Send(filePath, peer string, noCompress bool, streams int, priority string) (*SendResponse, error) {
	req := SendRequest{Path: filePath, Peer: peer, NoCompress: noCompress, Streams: streams, Priority: priority}
	body, _ := json.Marshal(req)
	var resp SendResponse
	if err := c.doJSON("POST", "/v1/send", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TransferStatus returns the progress of a transfer by ID.
func (c *daemonClient) TransferStatus(id string) (*p2pnet.TransferProgress, error) {
	var resp p2pnet.TransferProgress
	if err := c.doJSON("GET", "/v1/transfers/"+id, nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TransferList returns all tracked transfers.
func (c *daemonClient) TransferList() ([]p2pnet.TransferSnapshot, error) {
	var resp []p2pnet.TransferSnapshot
	if err := c.doJSON("GET", "/v1/transfers", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TransferHistory returns recent transfer events from the log file.
func (c *daemonClient) TransferHistory(max int) ([]p2pnet.TransferEvent, error) {
	path := fmt.Sprintf("/v1/transfers/history?max=%d", max)
	var resp []p2pnet.TransferEvent
	if err := c.doJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TransferPending returns the list of transfers awaiting approval (ask mode).
func (c *daemonClient) TransferPending() ([]PendingTransferInfo, error) {
	var resp []PendingTransferInfo
	if err := c.doJSON("GET", "/v1/transfers/pending", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// TransferAccept approves a pending transfer, with an optional destination directory.
func (c *daemonClient) TransferAccept(id, dest string) error {
	req := TransferAcceptRequest{Dest: dest}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/transfers/"+id+"/accept", strings.NewReader(string(body)), nil)
}

// TransferReject rejects a pending transfer with an optional reason.
func (c *daemonClient) TransferReject(id, reason string) error {
	req := TransferRejectRequest{Reason: reason}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/transfers/"+id+"/reject", strings.NewReader(string(body)), nil)
}

// CancelTransfer cancels a queued or active transfer by ID.
func (c *daemonClient) CancelTransfer(id string) error {
	return c.doJSON("POST", "/v1/transfers/"+id+"/cancel", nil, nil)
}

// CleanTempFiles removes all incomplete .tmp files via the daemon.
func (c *daemonClient) CleanTempFiles() (int, int64, error) {
	var resp struct {
		FilesRemoved int   `json:"files_removed"`
		BytesFreed   int64 `json:"bytes_freed"`
	}
	if err := c.doJSON("POST", "/v1/clean", nil, &resp); err != nil {
		return 0, 0, err
	}
	return resp.FilesRemoved, resp.BytesFreed, nil
}
