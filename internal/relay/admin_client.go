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

// RelayInfoResponse holds the relay's peer ID and multiaddrs.
type RelayInfoResponse struct {
	PeerID     string   `json:"peer_id"`
	Multiaddrs []string `json:"multiaddrs"`
}

// GetInfo returns the relay's peer ID and multiaddrs from the running server.
func (c *AdminClient) GetInfo() (*RelayInfoResponse, error) {
	data, status, err := c.do("GET", "/v1/info", nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("info failed (HTTP %d): %s", status, data)
	}
	var resp RelayInfoResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	return &resp, nil
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

// Unseal sends a passphrase (and optional TOTP code and Yubikey response) to unseal the relay vault.
func (c *AdminClient) Unseal(passphrase, totpCode string, yubikeyResponse []byte) error {
	req := UnsealRequest{
		Passphrase:      passphrase,
		TOTPCode:        totpCode,
		YubikeyResponse: yubikeyResponse,
	}
	reqBody, _ := json.Marshal(req)

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
func (c *AdminClient) InitVault(seedBytes []byte, mnemonic, password string, enableTOTP bool, autoSealMins int) (*VaultInitResponse, error) {
	reqBody, _ := json.Marshal(VaultInitRequest{
		SeedBytes:    seedBytes,
		Mnemonic:     mnemonic,
		Password:     password,
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

// ZKPTreeRebuild triggers a Merkle tree rebuild from authorized_keys.
func (c *AdminClient) ZKPTreeRebuild() (map[string]any, error) {
	data, status, err := c.do("POST", "/v1/zkp/tree-rebuild", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var resp map[string]any
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	return resp, nil
}

// ZKPTreeInfo returns the current ZKP Merkle tree state.
func (c *AdminClient) ZKPTreeInfo() (*ZKPTreeInfoResponse, error) {
	data, status, err := c.do("GET", "/v1/zkp/tree-info", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var resp ZKPTreeInfoResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("invalid response: %w", err)
	}
	return &resp, nil
}

// ZKPProvingKey downloads the PLONK proving key from the relay.
// Returns the raw binary key data (~2 MB).
func (c *AdminClient) ZKPProvingKey() ([]byte, error) {
	data, status, err := c.do("GET", "/v1/zkp/proving-key", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	return data, nil
}

// ZKPVerifyingKey downloads the PLONK verifying key from the relay.
// Returns the raw binary key data (~34 KB).
func (c *AdminClient) ZKPVerifyingKey() ([]byte, error) {
	data, status, err := c.do("GET", "/v1/zkp/verifying-key", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	return data, nil
}

// ListPeers returns all authorized peers from the relay's authorized_keys.
func (c *AdminClient) ListPeers() ([]AuthorizedPeerInfo, error) {
	data, status, err := c.do("GET", "/v1/peers", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var resp []AuthorizedPeerInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return resp, nil
}

// ListConnectedPeers returns currently connected peers with network details.
func (c *AdminClient) ListConnectedPeers() ([]ConnectedPeerInfo, error) {
	data, status, err := c.do("GET", "/v1/peers/connected", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var resp []ConnectedPeerInfo
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return resp, nil
}

// AuthorizePeer adds a peer to the relay's authorized_keys and triggers reload.
func (c *AdminClient) AuthorizePeer(peerID, comment string) error {
	reqBody, _ := json.Marshal(map[string]string{
		"peer_id": peerID,
		"comment": comment,
	})
	data, status, err := c.do("POST", "/v1/peers/authorize", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// SetPeerAttr sets a key=value attribute on a peer in the relay's authorized_keys.
func (c *AdminClient) SetPeerAttr(peerID, key, value string) error {
	reqBody, _ := json.Marshal(map[string]string{
		"peer_id": peerID,
		"key":     key,
		"value":   value,
	})
	data, status, err := c.do("POST", "/v1/peers/set-attr", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// DeauthorizePeer removes a peer from the relay's authorized_keys and triggers reload.
func (c *AdminClient) DeauthorizePeer(peerID string) error {
	reqBody, _ := json.Marshal(map[string]string{
		"peer_id": peerID,
	})
	data, status, err := c.do("POST", "/v1/peers/deauthorize", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// AuthReload triggers a hot-reload of the relay's authorized_keys and gater.
// Also rebuilds the ZKP Merkle tree if ZKP auth is enabled.
func (c *AdminClient) AuthReload() error {
	data, status, err := c.do("POST", "/v1/auth/reload", nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// GetMOTDStatus returns the current MOTD and goodbye status.
func (c *AdminClient) GetMOTDStatus() (*MOTDStatusResponse, error) {
	data, status, err := c.do("GET", "/v1/motd", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var resp MOTDStatusResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &resp, nil
}

// SetMOTD sets the relay's MOTD message.
func (c *AdminClient) SetMOTD(message string) error {
	reqBody, _ := json.Marshal(map[string]string{"message": message})
	data, status, err := c.do("PUT", "/v1/motd", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// ClearMOTD clears the relay's MOTD message.
func (c *AdminClient) ClearMOTD() error {
	data, status, err := c.do("DELETE", "/v1/motd", nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// SetGoodbye sets a goodbye announcement (pushed to all connected peers).
func (c *AdminClient) SetGoodbye(message string) error {
	reqBody, _ := json.Marshal(map[string]string{"message": message})
	data, status, err := c.do("PUT", "/v1/goodbye", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// RetractGoodbye retracts an active goodbye announcement.
func (c *AdminClient) RetractGoodbye() error {
	data, status, err := c.do("DELETE", "/v1/goodbye", nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// GoodbyeShutdown sets a goodbye and triggers relay shutdown.
func (c *AdminClient) GoodbyeShutdown(message string) error {
	reqBody, _ := json.Marshal(map[string]string{"message": message})
	data, status, err := c.do("POST", "/v1/goodbye/shutdown", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// --- Relay grant client methods ---

// RelayGrant creates a time-limited data access grant for a peer.
func (c *AdminClient) RelayGrant(peerID string, durationSecs int, services []string, permanent bool) (*RelayGrantInfo, error) {
	reqBody, _ := json.Marshal(RelayGrantRequest{
		PeerID:      peerID,
		DurationSec: durationSecs,
		Services:    services,
		Permanent:   permanent,
	})
	data, status, err := c.do("POST", "/v1/relay-grant", strings.NewReader(string(reqBody)))
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var info RelayGrantInfo
	if err := json.Unmarshal(data, &info); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &info, nil
}

// RelayGrants lists all active relay data grants.
func (c *AdminClient) RelayGrants() ([]RelayGrantInfo, error) {
	data, status, err := c.do("GET", "/v1/relay-grants", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, parseAdminError(data, status)
	}
	var result []RelayGrantInfo
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return result, nil
}

// RelayRevoke revokes a relay data grant and terminates active circuits.
func (c *AdminClient) RelayRevoke(peerID string) error {
	reqBody, _ := json.Marshal(map[string]string{"peer_id": peerID})
	data, status, err := c.do("POST", "/v1/relay-revoke", strings.NewReader(string(reqBody)))
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// RelayExtend extends an existing relay data grant.
func (c *AdminClient) RelayExtend(peerID string, durationSecs int) error {
	reqBody, _ := json.Marshal(RelayExtendRequest{
		PeerID:      peerID,
		DurationSec: durationSecs,
	})
	data, status, err := c.do("POST", "/v1/relay-extend", strings.NewReader(string(reqBody)))
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
