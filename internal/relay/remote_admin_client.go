package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	libp2phost "github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// RemoteAdminClient connects to a running relay over a libp2p P2P stream
// using the /shurli/relay-admin/1.0.0 protocol. It provides the same
// high-level methods as AdminClient but operates over the network instead
// of a local Unix socket.
type RemoteAdminClient struct {
	host    libp2phost.Host
	relayID peer.ID
}

// NewRemoteAdminClient creates a client for remote relay administration.
func NewRemoteAdminClient(host libp2phost.Host, relayID peer.ID) *RemoteAdminClient {
	return &RemoteAdminClient{
		host:    host,
		relayID: relayID,
	}
}

// do sends a remote admin request over a P2P stream and returns the response.
// Matches the AdminClient.do() return signature for easy drop-in replacement.
func (c *RemoteAdminClient) do(method, path string, body io.Reader) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), remoteAdminTimeout)
	defer cancel()

	s, err := c.host.NewStream(ctx, c.relayID, protocol.ID(RemoteAdminProtocol))
	if err != nil {
		return nil, 0, fmt.Errorf("failed to open remote admin stream: %w", err)
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(remoteAdminTimeout))

	// Build request frame.
	var bodyBytes json.RawMessage
	if body != nil {
		b, err := io.ReadAll(io.LimitReader(body, maxRemoteAdminRequest))
		if err != nil {
			return nil, 0, fmt.Errorf("failed to read request body: %w", err)
		}
		bodyBytes = b
	}

	reqFrame, err := json.Marshal(RemoteAdminRequest{
		Method: method,
		Path:   path,
		Body:   bodyBytes,
	})
	if err != nil {
		return nil, 0, fmt.Errorf("failed to marshal request: %w", err)
	}

	// Write length-prefixed request frame.
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(reqFrame)))
	if _, err := s.Write(lenBuf[:]); err != nil {
		return nil, 0, fmt.Errorf("failed to write frame length: %w", err)
	}
	if _, err := s.Write(reqFrame); err != nil {
		return nil, 0, fmt.Errorf("failed to write frame: %w", err)
	}
	s.CloseWrite()

	// Read length-prefixed response frame.
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		return nil, 0, fmt.Errorf("failed to read response length: %w", err)
	}
	respLen := binary.BigEndian.Uint32(lenBuf[:])
	if respLen == 0 || respLen > maxRemoteAdminResponse {
		return nil, 0, fmt.Errorf("invalid response frame length: %d", respLen)
	}

	respBuf := make([]byte, respLen)
	if _, err := io.ReadFull(s, respBuf); err != nil {
		return nil, 0, fmt.Errorf("failed to read response frame: %w", err)
	}

	var resp RemoteAdminResponse
	if err := json.Unmarshal(respBuf, &resp); err != nil {
		return nil, 0, fmt.Errorf("failed to decode response: %w", err)
	}

	return resp.Body, resp.Status, nil
}

// --- High-level methods (mirror AdminClient interface) ---

// Unseal sends a password (and optional TOTP code and Yubikey response) to unseal the relay vault.
func (c *RemoteAdminClient) Unseal(password, totpCode string, yubikeyResponse []byte) error {
	req := UnsealRequest{
		Passphrase:      password,
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
func (c *RemoteAdminClient) Seal() error {
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
func (c *RemoteAdminClient) SealStatus() (*SealStatusResponse, error) {
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

// NOTE: InitVault intentionally removed from RemoteAdminClient.
// Vault initialization requires seed material which must never travel over the network.
// Use 'shurli relay vault init' locally on the relay server (SSH or physical access).

// CreateInvite creates a macaroon-backed invite deposit.
func (c *RemoteAdminClient) CreateInvite(caveats []string, ttlSec int) (map[string]string, error) {
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
func (c *RemoteAdminClient) ListInvites() ([]map[string]any, error) {
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
func (c *RemoteAdminClient) RevokeInvite(id string) error {
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
func (c *RemoteAdminClient) ModifyInvite(id string, addCaveats []string) error {
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

// CreateGroup creates a pairing group and returns the invite codes.
func (c *RemoteAdminClient) CreateGroup(count, ttlSec, expiresSec int, namespace string) (*PairResponse, error) {
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
		return nil, parseAdminError(data, status)
	}
	var resp PairResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}
	return &resp, nil
}

// ListGroups returns all active pairing groups.
func (c *RemoteAdminClient) ListGroups() ([]GroupInfo, error) {
	data, status, err := c.do("GET", "/v1/pair", nil)
	if err != nil {
		return nil, err
	}
	if status >= 400 {
		return nil, fmt.Errorf("relay returned HTTP %d", status)
	}

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
		if g.ExpiresAt != "" {
			if t, err := parseTimeStr(g.ExpiresAt); err == nil {
				result[i].ExpiresAt = t
			}
		}
	}
	return result, nil
}

// RevokeGroup revokes a pairing group by ID.
func (c *RemoteAdminClient) RevokeGroup(id string) error {
	data, status, err := c.do("DELETE", "/v1/pair/"+id, nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// ListPeers returns all authorized peers from the relay's authorized_keys.
func (c *RemoteAdminClient) ListPeers() ([]AuthorizedPeerInfo, error) {
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
func (c *RemoteAdminClient) ListConnectedPeers() ([]ConnectedPeerInfo, error) {
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
func (c *RemoteAdminClient) AuthorizePeer(peerID, comment string) error {
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
func (c *RemoteAdminClient) SetPeerAttr(peerID, key, value string) error {
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
func (c *RemoteAdminClient) DeauthorizePeer(peerID string) error {
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
func (c *RemoteAdminClient) AuthReload() error {
	data, status, err := c.do("POST", "/v1/auth/reload", nil)
	if err != nil {
		return err
	}
	if status >= 400 {
		return parseAdminError(data, status)
	}
	return nil
}

// ZKPTreeRebuild triggers a Merkle tree rebuild from authorized_keys.
func (c *RemoteAdminClient) ZKPTreeRebuild() (map[string]any, error) {
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
func (c *RemoteAdminClient) ZKPTreeInfo() (*ZKPTreeInfoResponse, error) {
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

// GetMOTDStatus returns the current MOTD and goodbye status.
func (c *RemoteAdminClient) GetMOTDStatus() (*MOTDStatusResponse, error) {
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
func (c *RemoteAdminClient) SetMOTD(message string) error {
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
func (c *RemoteAdminClient) ClearMOTD() error {
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
func (c *RemoteAdminClient) SetGoodbye(message string) error {
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
func (c *RemoteAdminClient) RetractGoodbye() error {
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
func (c *RemoteAdminClient) GoodbyeShutdown(message string) error {
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

// --- Relay grant methods ---

// RelayGrant creates a time-limited data access grant for a peer.
func (c *RemoteAdminClient) RelayGrant(peerID string, durationSecs int, services []string, permanent bool) (*RelayGrantInfo, error) {
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
func (c *RemoteAdminClient) RelayGrants() ([]RelayGrantInfo, error) {
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
func (c *RemoteAdminClient) RelayRevoke(peerID string) error {
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
func (c *RemoteAdminClient) RelayExtend(peerID string, durationSecs int) error {
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
