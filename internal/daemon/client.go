package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// Client connects to a running daemon via its Unix socket.
type Client struct {
	httpClient *http.Client
	socketPath string
	authToken  string
}

// NewClient creates a new daemon client. It reads the auth cookie
// automatically from the cookie file next to the socket.
func NewClient(socketPath, cookiePath string) (*Client, error) {
	// Check socket exists
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrDaemonNotRunning, socketPath)
	}

	// Read auth cookie
	token, err := os.ReadFile(cookiePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read daemon cookie: %w", err)
	}

	c := &Client{
		socketPath: socketPath,
		authToken:  strings.TrimSpace(string(token)),
		httpClient: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}

	return c, nil
}

// do sends an HTTP request to the daemon and returns the raw response body.
func (c *Client) do(method, path string, body io.Reader, headers map[string]string) ([]byte, int, error) {
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

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// doJSON sends a request and decodes the JSON {"data": ...} envelope into target.
func (c *Client) doJSON(method, path string, body io.Reader, target any) error {
	data, status, err := c.do(method, path, body, nil)
	if err != nil {
		return err
	}

	if status >= 400 {
		var errResp ErrorResponse
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
func (c *Client) doText(method, path string, body io.Reader) (string, error) {
	data, status, err := c.do(method, path, body, map[string]string{"Accept": "text/plain"})
	if err != nil {
		return "", err
	}

	if status >= 400 {
		// Error responses are always JSON
		var errResp ErrorResponse
		if json.Unmarshal(data, &errResp) == nil && errResp.Error != "" {
			return "", fmt.Errorf("daemon: %s", errResp.Error)
		}
		return "", fmt.Errorf("daemon returned HTTP %d", status)
	}

	return string(data), nil
}

// --- Query methods ---

// Status returns the daemon's status.
func (c *Client) Status() (*StatusResponse, error) {
	var resp StatusResponse
	if err := c.doJSON("GET", "/v1/status", nil, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// StatusText returns the daemon's status as plain text.
func (c *Client) StatusText() (string, error) {
	return c.doText("GET", "/v1/status", nil)
}

// Services returns the list of registered services.
func (c *Client) Services() ([]ServiceInfo, error) {
	var resp []ServiceInfo
	if err := c.doJSON("GET", "/v1/services", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// ServicesText returns services as plain text.
func (c *Client) ServicesText() (string, error) {
	return c.doText("GET", "/v1/services", nil)
}

// Peers returns the list of connected peers. If all is true, includes non-peerup DHT peers.
func (c *Client) Peers(all bool) ([]PeerInfo, error) {
	path := "/v1/peers"
	if all {
		path += "?all=true"
	}
	var resp []PeerInfo
	if err := c.doJSON("GET", path, nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// PeersText returns peers as plain text. If all is true, includes non-peerup DHT peers.
func (c *Client) PeersText(all bool) (string, error) {
	path := "/v1/peers"
	if all {
		path += "?all=true"
	}
	return c.doText("GET", path, nil)
}

// AuthList returns the authorized peers.
func (c *Client) AuthList() ([]AuthEntry, error) {
	var resp []AuthEntry
	if err := c.doJSON("GET", "/v1/auth", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// AuthListText returns authorized peers as plain text.
func (c *Client) AuthListText() (string, error) {
	return c.doText("GET", "/v1/auth", nil)
}

// Paths returns per-peer path information (type, transport, IP version, RTT).
func (c *Client) Paths() ([]PathInfo, error) {
	var resp []PathInfo
	if err := c.doJSON("GET", "/v1/paths", nil, &resp); err != nil {
		return nil, err
	}
	return resp, nil
}

// PathsText returns per-peer path information as plain text.
func (c *Client) PathsText() (string, error) {
	return c.doText("GET", "/v1/paths", nil)
}

// --- Mutation methods ---

// AuthAdd adds an authorized peer.
func (c *Client) AuthAdd(peerID, comment string) error {
	req := AuthAddRequest{PeerID: peerID, Comment: comment}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/auth", strings.NewReader(string(body)), nil)
}

// AuthRemove removes an authorized peer.
func (c *Client) AuthRemove(peerID string) error {
	return c.doJSON("DELETE", "/v1/auth/"+peerID, nil, nil)
}

// Ping pings a peer via the daemon.
func (c *Client) Ping(peer string, count, intervalMs int) (*PingResponse, error) {
	req := PingRequest{Peer: peer, Count: count, IntervalMs: intervalMs}
	body, _ := json.Marshal(req)
	var resp PingResponse
	if err := c.doJSON("POST", "/v1/ping", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// PingText pings a peer via the daemon, returns plain text output.
func (c *Client) PingText(peer string, count, intervalMs int) (string, error) {
	req := PingRequest{Peer: peer, Count: count, IntervalMs: intervalMs}
	body, _ := json.Marshal(req)
	return c.doText("POST", "/v1/ping", strings.NewReader(string(body)))
}

// Traceroute traces the path to a peer and returns the result as JSON.
func (c *Client) Traceroute(peer string) (*p2pnet.TraceResult, error) {
	req := TraceRequest{Peer: peer}
	body, _ := json.Marshal(req)
	var resp p2pnet.TraceResult
	if err := c.doJSON("POST", "/v1/traceroute", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// TracerouteText traces the path to a peer, returns plain text output.
func (c *Client) TracerouteText(peer string) (string, error) {
	req := TraceRequest{Peer: peer}
	body, _ := json.Marshal(req)
	return c.doText("POST", "/v1/traceroute", strings.NewReader(string(body)))
}

// Resolve resolves a name to a peer ID.
func (c *Client) Resolve(name string) (*ResolveResponse, error) {
	req := ResolveRequest{Name: name}
	body, _ := json.Marshal(req)
	var resp ResolveResponse
	if err := c.doJSON("POST", "/v1/resolve", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ResolveText resolves a name, returns plain text output.
func (c *Client) ResolveText(name string) (string, error) {
	req := ResolveRequest{Name: name}
	body, _ := json.Marshal(req)
	return c.doText("POST", "/v1/resolve", strings.NewReader(string(body)))
}

// Connect creates a TCP proxy to a remote service via the daemon.
func (c *Client) Connect(peer, service, listen string) (*ConnectResponse, error) {
	req := ConnectRequest{Peer: peer, Service: service, Listen: listen}
	body, _ := json.Marshal(req)
	var resp ConnectResponse
	if err := c.doJSON("POST", "/v1/connect", strings.NewReader(string(body)), &resp); err != nil {
		return nil, err
	}
	return &resp, nil
}

// Disconnect tears down a proxy.
func (c *Client) Disconnect(id string) error {
	return c.doJSON("DELETE", "/v1/connect/"+id, nil, nil)
}

// Expose registers a service on the P2P host.
func (c *Client) Expose(name, localAddress string) error {
	req := ExposeRequest{Name: name, LocalAddress: localAddress}
	body, _ := json.Marshal(req)
	return c.doJSON("POST", "/v1/expose", strings.NewReader(string(body)), nil)
}

// Unexpose removes a service from the P2P host.
func (c *Client) Unexpose(name string) error {
	return c.doJSON("DELETE", "/v1/expose/"+name, nil, nil)
}

// Shutdown requests the daemon to shut down gracefully.
func (c *Client) Shutdown() error {
	return c.doJSON("POST", "/v1/shutdown", nil, nil)
}
