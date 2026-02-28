package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

// AdminClient connects to a running relay's admin Unix socket.
type AdminClient struct {
	httpClient *http.Client
	authToken  string
}

// NewAdminClient creates a client for the relay admin socket.
func NewAdminClient(socketPath, cookiePath string) (*AdminClient, error) {
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("relay is not running (no admin socket at %s)", socketPath)
	}

	token, err := os.ReadFile(cookiePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read relay admin cookie: %w", err)
	}

	return &AdminClient{
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

// do sends an HTTP request and returns the response body.
func (c *AdminClient) do(method, path string, body io.Reader) ([]byte, int, error) {
	req, err := http.NewRequest(method, "http://relay-admin"+path, body)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.authToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("failed to connect to relay admin: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

// CreateGroup creates a pairing group and returns the invite codes.
func (c *AdminClient) CreateGroup(count, ttlSec, expiresSec int, namespace string) (*PairResponse, error) {
	reqBody, _ := json.Marshal(PairRequest{
		Count:          count,
		TTLSeconds:     ttlSec,
		Namespace:      namespace,
		ExpiresSeconds: expiresSec,
	})

	data, status, err := c.do("POST", "/v1/pair", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}

	if status >= 400 {
		var errResp map[string]string
		if json.Unmarshal(data, &errResp) == nil {
			if msg, ok := errResp["error"]; ok {
				return nil, fmt.Errorf("relay: %s", msg)
			}
		}
		return nil, fmt.Errorf("relay returned HTTP %d", status)
	}

	var resp PairResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &resp, nil
}

// ListGroups returns all active pairing groups.
func (c *AdminClient) ListGroups() ([]GroupInfo, error) {
	data, status, err := c.do("GET", "/v1/pair", nil)
	if err != nil {
		return nil, err
	}

	if status >= 400 {
		return nil, fmt.Errorf("relay returned HTTP %d", status)
	}

	// The response is a JSON array of group objects with string peer IDs.
	type peerJSON struct {
		PeerID string `json:"peer_id"`
		Name   string `json:"name"`
	}
	type groupJSON struct {
		ID        string     `json:"id"`
		Namespace string     `json:"namespace"`
		ExpiresAt string     `json:"expires_at"`
		Total     int        `json:"total"`
		Used      int        `json:"used"`
		Peers     []peerJSON `json:"peers"`
	}

	var groups []groupJSON
	if err := json.Unmarshal(data, &groups); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	result := make([]GroupInfo, len(groups))
	for i, g := range groups {
		result[i] = GroupInfo{
			ID:        g.ID,
			Namespace: g.Namespace,
			Total:     g.Total,
			Used:      g.Used,
		}
		// Parse ExpiresAt if present.
		if g.ExpiresAt != "" {
			if t, err := parseTimeStr(g.ExpiresAt); err == nil {
				result[i].ExpiresAt = t
			}
		}
	}
	return result, nil
}

// RevokeGroup revokes a pairing group by ID.
func (c *AdminClient) RevokeGroup(id string) error {
	data, status, err := c.do("DELETE", "/v1/pair/"+id, nil)
	if err != nil {
		return err
	}

	if status >= 400 {
		var errResp map[string]string
		if json.Unmarshal(data, &errResp) == nil {
			if msg, ok := errResp["error"]; ok {
				return fmt.Errorf("relay: %s", msg)
			}
		}
		return fmt.Errorf("relay returned HTTP %d", status)
	}

	return nil
}

// Unseal sends a passphrase (and optional TOTP code) to unseal the relay vault.
func (c *AdminClient) Unseal(passphrase, totpCode string) error {
	reqBody, _ := json.Marshal(map[string]string{
		"passphrase": passphrase,
		"totp_code":  totpCode,
	})

	data, status, err := c.do("POST", "/v1/unseal", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// Seal re-seals the relay vault.
func (c *AdminClient) Seal() error {
	data, status, err := c.do("POST", "/v1/seal", nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// SealStatus returns the current seal status of the relay vault.
func (c *AdminClient) SealStatus() (*SealStatusResponse, error) {
	data, status, err := c.do("GET", "/v1/seal-status", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}

	var resp SealStatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &resp, nil
}

// InitVault creates a new vault on the relay.
func (c *AdminClient) InitVault(passphrase string, enableTOTP bool, autoSealMins int) (*VaultInitResponse, error) {
	reqBody, _ := json.Marshal(VaultInitRequest{
		Passphrase:   passphrase,
		EnableTOTP:   enableTOTP,
		AutoSealMins: autoSealMins,
	})

	data, status, err := c.do("POST", "/v1/vault/init", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}

	var resp VaultInitResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &resp, nil
}

// CreateInvite creates a macaroon-backed invite deposit.
func (c *AdminClient) CreateInvite(caveats []string, ttlSec int) (map[string]string, error) {
	reqBody, _ := json.Marshal(map[string]any{
		"caveats":     caveats,
		"ttl_seconds": ttlSec,
	})

	data, status, err := c.do("POST", "/v1/invite", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}

	var resp map[string]string
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return resp, nil
}

// ListInvites returns all invite deposits.
func (c *AdminClient) ListInvites() ([]map[string]any, error) {
	data, status, err := c.do("GET", "/v1/invite", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}

	var resp []map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return resp, nil
}

// RevokeInvite revokes a pending invite deposit.
func (c *AdminClient) RevokeInvite(id string) error {
	data, status, err := c.do("DELETE", "/v1/invite/"+id, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// ModifyInvite adds caveats to a pending invite deposit (attenuation only).
func (c *AdminClient) ModifyInvite(id string, addCaveats []string) error {
	reqBody, _ := json.Marshal(map[string]any{
		"add_caveats": addCaveats,
	})

	data, status, err := c.do("PATCH", "/v1/invite/"+id, strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// parseAdminError extracts an error message from an admin API error response.
func parseAdminError(data []byte, status int) error {
	var errResp map[string]string
	if json.Unmarshal(data, &errResp) == nil {
		if msg, ok := errResp["error"]; ok {
			return fmt.Errorf("relay: %s", msg)
		}
	}
	return fmt.Errorf("relay returned HTTP %d", status)
}

// parseTimeStr parses common time formats.
func parseTimeStr(s string) (time.Time, error) {
	// Try RFC3339 first, then other common formats.
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z07:00",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse time: %s", s)
}
