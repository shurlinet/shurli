// Grant delivery protocol: /shurli/grant/1.0.0
//
// Delivers macaroon capability tokens from issuing nodes to receiving peers.
// Also handles revocation notices and token refresh requests.
//
// Wire format: type(1) + length(4, big-endian) + JSON payload.
// Max payload: 8192 bytes. Stream timeout: 10 seconds.
//
// Trust model: recipients only accept grants from nodes in their
// authorized_keys (prevents random nodes pushing tokens).
package grants

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	libp2pnet "github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/macaroon"
)

const (
	// GrantProtocolID is the libp2p protocol for grant delivery.
	GrantProtocolID = "/shurli/grant/1.0.0"

	// Wire message types.
	msgGrantDeliver = 0x01 // issuer -> peer: here's your token
	msgGrantRevoke  = 0x02 // issuer -> peer: your token is revoked
	msgGrantAck     = 0x03 // peer -> issuer: acknowledged
	msgGrantRefresh = 0x04 // peer -> issuer: please refresh my token

	// Limits.
	grantMaxPayload = 8192
	grantTimeout    = 10 * time.Second

	// Rate limiting: max deliveries accepted per minute per peer.
	grantMaxPerMinute = 5

	// D4: protocol version for downgrade protection.
	// Reject messages from older protocol versions.
	GrantProtocolVersion    = 1
	GrantMinProtocolVersion = 1
)

// GrantDelivery is the wire message for delivering a token.
type GrantDelivery struct {
	Version   int      `json:"version,omitempty"`     // D4: protocol version (0 = pre-D4, treated as 1)
	Token     string   `json:"token"`                 // base64-encoded macaroon
	Services  []string `json:"services,omitempty"`     // service restrictions
	ExpiresAt string   `json:"expires_at,omitempty"`   // RFC3339
	Permanent bool     `json:"permanent,omitempty"`
}

// GrantRevocation is the wire message for revoking a token.
type GrantRevocation struct {
	Version int    `json:"version,omitempty"` // D4: protocol version
	Reason  string `json:"reason,omitempty"`
}

// GrantAck is the acknowledgement message.
type GrantAck struct {
	Status string `json:"status"` // "accepted" or "rejected"
	Reason string `json:"reason,omitempty"`
}

// GrantRefreshRequest is sent by a peer to request a token refresh.
type GrantRefreshRequest struct {
	Version int    `json:"version,omitempty"` // D4: protocol version
	TokenID string `json:"token_id"`          // grant ID prefix for audit trail
}

// GrantRefreshResponse is the issuer's response to a refresh request.
type GrantRefreshResponse struct {
	Status string `json:"status"`           // "refreshed" or "rejected"
	Reason string `json:"reason,omitempty"` // rejection reason
	Token  string `json:"token,omitempty"`  // new base64-encoded token if refreshed
}

// rateLimitEntry tracks deliveries from a single peer.
type rateLimitEntry struct {
	count    int
	windowAt time.Time
}

// GrantProtocol manages P2P delivery and receipt of grant tokens.
type GrantProtocol struct {
	host       host.Host
	pouch      *Pouch
	store      *Store             // issuer-side grant store (for handling refresh requests)
	queue      *DeliveryQueue
	trustCheck func(peer.ID) bool // returns true if peer is in authorized_keys
	logger     *slog.Logger

	// Rate limiting for inbound deliveries.
	rateMu    sync.Mutex
	rateLimit map[peer.ID]*rateLimitEntry

	// Background queue flush loop.
	stopCh         chan struct{}
	doneCh         chan struct{}
	flushStarted   bool
}

// NewGrantProtocol creates a new grant delivery protocol handler.
// trustCheck should return true if the remote peer is authorized (in authorized_keys).
// store is optional - required only on issuing nodes to handle refresh requests.
func NewGrantProtocol(h host.Host, pouch *Pouch, store *Store, queue *DeliveryQueue, trustCheck func(peer.ID) bool) *GrantProtocol {
	return &GrantProtocol{
		host:       h,
		pouch:      pouch,
		store:      store,
		queue:      queue,
		trustCheck: trustCheck,
		logger:     slog.Default(),
		rateLimit:  make(map[peer.ID]*rateLimitEntry),
		stopCh:     make(chan struct{}),
		doneCh:     make(chan struct{}),
	}
}

// Register registers the inbound stream handler on the host.
func (gp *GrantProtocol) Register() {
	gp.host.SetStreamHandler(protocol.ID(GrantProtocolID), gp.handleInbound)
	gp.logger.Info("grant-protocol: registered", "protocol", GrantProtocolID)
}

// Unregister removes the stream handler.
func (gp *GrantProtocol) Unregister() {
	gp.host.RemoveStreamHandler(protocol.ID(GrantProtocolID))
}

// DeliverGrant sends a macaroon token to a remote peer.
// Returns nil on successful delivery (peer acknowledged).
func (gp *GrantProtocol) DeliverGrant(ctx context.Context, peerID peer.ID, token *macaroon.Macaroon, services []string, expiresAt time.Time, permanent bool) error {
	tokenB64, err := token.EncodeBase64()
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}

	delivery := GrantDelivery{
		Version:   GrantProtocolVersion,
		Token:     tokenB64,
		Services:  services,
		Permanent: permanent,
	}
	if !permanent {
		delivery.ExpiresAt = expiresAt.Format(time.RFC3339)
	}

	data, err := json.Marshal(delivery)
	if err != nil {
		return fmt.Errorf("marshal delivery: %w", err)
	}

	return gp.sendMessage(ctx, peerID, msgGrantDeliver, data)
}

// DeliverRevocation notifies a peer that their grant is revoked.
func (gp *GrantProtocol) DeliverRevocation(ctx context.Context, peerID peer.ID, reason string) error {
	rev := GrantRevocation{Version: GrantProtocolVersion, Reason: reason}
	data, err := json.Marshal(rev)
	if err != nil {
		return fmt.Errorf("marshal revocation: %w", err)
	}

	return gp.sendMessage(ctx, peerID, msgGrantRevoke, data)
}

// sendMessage opens a stream, sends a typed message, and waits for ack.
func (gp *GrantProtocol) sendMessage(ctx context.Context, peerID peer.ID, msgType byte, data []byte) error {
	if len(data) > grantMaxPayload {
		return fmt.Errorf("payload too large: %d > %d", len(data), grantMaxPayload)
	}

	ctx, cancel := context.WithTimeout(ctx, grantTimeout)
	defer cancel()

	// Allow limited (relay) connections for grant delivery.
	ctx = libp2pnet.WithAllowLimitedConn(ctx, GrantProtocolID)

	s, err := gp.host.NewStream(ctx, peerID, protocol.ID(GrantProtocolID))
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(grantTimeout))

	// Write: type(1) + length(4) + data.
	var header [5]byte
	header[0] = msgType
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return fmt.Errorf("write header: %w", err)
	}
	if _, err := s.Write(data); err != nil {
		return fmt.Errorf("write payload: %w", err)
	}

	// Read ack.
	ack, err := gp.readMessage(s)
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	if ack.msgType != msgGrantAck {
		return fmt.Errorf("unexpected response type: 0x%02x", ack.msgType)
	}

	var ackMsg GrantAck
	if err := json.Unmarshal(ack.data, &ackMsg); err != nil {
		return fmt.Errorf("parse ack: %w", err)
	}

	if ackMsg.Status != "accepted" {
		return fmt.Errorf("grant rejected: %s", ackMsg.Reason)
	}

	return nil
}

type wireMessage struct {
	msgType byte
	data    []byte
}

func (gp *GrantProtocol) readMessage(s libp2pnet.Stream) (*wireMessage, error) {
	var header [5]byte
	if _, err := io.ReadFull(s, header[:]); err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	msgType := header[0]
	dataLen := binary.BigEndian.Uint32(header[1:])

	if dataLen > grantMaxPayload {
		return nil, fmt.Errorf("payload too large: %d > %d", dataLen, grantMaxPayload)
	}

	data := make([]byte, dataLen)
	if _, err := io.ReadFull(s, data); err != nil {
		return nil, fmt.Errorf("read payload: %w", err)
	}

	return &wireMessage{msgType: msgType, data: data}, nil
}

func (gp *GrantProtocol) writeAck(s libp2pnet.Stream, status, reason string) {
	ack := GrantAck{Status: status, Reason: reason}
	data, _ := json.Marshal(ack)

	var header [5]byte
	header[0] = msgGrantAck
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		gp.logger.Warn("grant-protocol: failed to write ack header", "error", err)
		return
	}
	if _, err := s.Write(data); err != nil {
		gp.logger.Warn("grant-protocol: failed to write ack payload", "error", err)
	}
}

// handleInbound handles incoming grant delivery/revocation/refresh streams.
func (gp *GrantProtocol) handleInbound(s libp2pnet.Stream) {
	defer s.Close()

	remotePeer := s.Conn().RemotePeer()
	short := shortPeerID(remotePeer)

	s.SetDeadline(time.Now().Add(grantTimeout))

	// Rate limit check (applies to all message types).
	if !gp.checkRateLimit(remotePeer) {
		gp.logger.Warn("grant-protocol: rate limited", "peer", short)
		gp.writeAck(s, "rejected", "rate limited")
		return
	}

	msg, err := gp.readMessage(s)
	if err != nil {
		gp.logger.Warn("grant-protocol: read failed", "peer", short, "error", err)
		return
	}

	switch msg.msgType {
	case msgGrantDeliver, msgGrantRevoke:
		// Trust check: only accept deliveries/revocations from authorized nodes.
		if gp.trustCheck != nil && !gp.trustCheck(remotePeer) {
			gp.logger.Warn("grant-protocol: rejected from unauthorized peer", "peer", short, "type", msg.msgType)
			gp.writeAck(s, "rejected", "not authorized")
			return
		}
		if gp.pouch == nil {
			gp.logger.Warn("grant-protocol: no pouch configured, rejecting", "peer", short)
			gp.writeAck(s, "rejected", "not configured to receive grants")
			return
		}
		if msg.msgType == msgGrantDeliver {
			gp.handleDelivery(s, remotePeer, msg.data)
		} else {
			gp.handleRevocation(s, remotePeer, msg.data)
		}
	case msgGrantRefresh:
		// Refresh requests come from grantees, not authorized nodes.
		// Authentication is via Store.Refresh() which checks the peer has an active auto-refresh grant.
		gp.handleRefresh(s, remotePeer, msg.data)
	default:
		gp.logger.Warn("grant-protocol: unknown message type", "peer", short, "type", msg.msgType)
		gp.writeAck(s, "rejected", "unknown message type")
	}
}

// checkProtocolVersion validates the version field in a wire message.
// Version 0 is treated as version 1 (pre-D4 nodes that don't set it).
func checkProtocolVersion(version int) error {
	if version == 0 {
		version = 1 // pre-D4 compatibility
	}
	if version < GrantMinProtocolVersion {
		return fmt.Errorf("protocol version %d below minimum %d", version, GrantMinProtocolVersion)
	}
	return nil
}

func (gp *GrantProtocol) handleDelivery(s libp2pnet.Stream, remotePeer peer.ID, data []byte) {
	short := shortPeerID(remotePeer)

	var delivery GrantDelivery
	if err := json.Unmarshal(data, &delivery); err != nil {
		gp.logger.Warn("grant-protocol: invalid delivery", "peer", short, "error", err)
		gp.writeAck(s, "rejected", "invalid payload")
		return
	}

	// D4: protocol downgrade protection.
	if err := checkProtocolVersion(delivery.Version); err != nil {
		gp.logger.Warn("grant-protocol: version rejected", "peer", short, "error", err)
		gp.writeAck(s, "rejected", err.Error())
		return
	}

	token, err := macaroon.DecodeBase64(delivery.Token)
	if err != nil {
		gp.logger.Warn("grant-protocol: invalid token", "peer", short, "error", err)
		gp.writeAck(s, "rejected", "invalid token")
		return
	}

	var expiresAt time.Time
	if !delivery.Permanent && delivery.ExpiresAt != "" {
		expiresAt, err = time.Parse(time.RFC3339, delivery.ExpiresAt)
		if err != nil {
			gp.logger.Warn("grant-protocol: invalid expires_at", "peer", short, "error", err)
			gp.writeAck(s, "rejected", "invalid expires_at")
			return
		}

		// Don't store already-expired tokens.
		if time.Now().After(expiresAt) {
			gp.logger.Info("grant-protocol: received expired token, ignoring", "peer", short)
			gp.writeAck(s, "rejected", "token already expired")
			return
		}
	}

	gp.pouch.Add(remotePeer, token, delivery.Services, expiresAt, delivery.Permanent)

	gp.logger.Info("grant-protocol: accepted grant",
		"issuer", short,
		"services", delivery.Services,
		"permanent", delivery.Permanent)

	gp.writeAck(s, "accepted", "")
}

func (gp *GrantProtocol) handleRevocation(s libp2pnet.Stream, remotePeer peer.ID, data []byte) {
	short := shortPeerID(remotePeer)

	var rev GrantRevocation
	if err := json.Unmarshal(data, &rev); err != nil {
		gp.logger.Warn("grant-protocol: invalid revocation", "peer", short, "error", err)
		gp.writeAck(s, "rejected", "invalid payload")
		return
	}

	// D4: protocol downgrade protection.
	if err := checkProtocolVersion(rev.Version); err != nil {
		gp.logger.Warn("grant-protocol: version rejected", "peer", short, "error", err)
		gp.writeAck(s, "rejected", err.Error())
		return
	}

	removed := gp.pouch.Remove(remotePeer)

	gp.logger.Info("grant-protocol: revocation received",
		"issuer", short,
		"had_token", removed,
		"reason", rev.Reason)

	gp.writeAck(s, "accepted", "")
}

// handleRefresh processes a refresh request from a peer (issuer-side).
func (gp *GrantProtocol) handleRefresh(s libp2pnet.Stream, remotePeer peer.ID, data []byte) {
	short := shortPeerID(remotePeer)

	if gp.store == nil {
		gp.logger.Warn("grant-protocol: refresh request but no store configured", "peer", short)
		gp.writeRefreshResponse(s, "rejected", "not an issuing node", "")
		return
	}

	var req GrantRefreshRequest
	if err := json.Unmarshal(data, &req); err != nil {
		gp.logger.Warn("grant-protocol: invalid refresh request", "peer", short, "error", err)
		gp.writeRefreshResponse(s, "rejected", "invalid payload", "")
		return
	}

	// D4: protocol downgrade protection.
	if err := checkProtocolVersion(req.Version); err != nil {
		gp.logger.Warn("grant-protocol: version rejected", "peer", short, "error", err)
		gp.writeRefreshResponse(s, "rejected", err.Error(), "")
		return
	}

	grant, err := gp.store.Refresh(remotePeer)
	if err != nil {
		gp.logger.Info("grant-protocol: refresh rejected", "peer", short, "reason", err.Error())
		gp.writeRefreshResponse(s, "rejected", err.Error(), "")
		return
	}

	tokenB64, err := grant.Token.EncodeBase64()
	if err != nil {
		gp.logger.Error("grant-protocol: failed to encode refreshed token", "peer", short, "error", err)
		gp.writeRefreshResponse(s, "rejected", "internal error", "")
		return
	}

	gp.logger.Info("grant-protocol: refresh granted", "peer", short,
		"refreshes_used", grant.RefreshesUsed, "max_refreshes", grant.MaxRefreshes)
	gp.writeRefreshResponse(s, "refreshed", "", tokenB64)
}

// writeRefreshResponse sends a GrantRefreshResponse back to the requesting peer.
func (gp *GrantProtocol) writeRefreshResponse(s libp2pnet.Stream, status, reason, token string) {
	resp := GrantRefreshResponse{Status: status, Reason: reason, Token: token}
	data, _ := json.Marshal(resp)

	var header [5]byte
	header[0] = msgGrantAck
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		gp.logger.Warn("grant-protocol: failed to write refresh response header", "error", err)
		return
	}
	if _, err := s.Write(data); err != nil {
		gp.logger.Warn("grant-protocol: failed to write refresh response payload", "error", err)
	}
}

// RequestRefresh sends a refresh request to the issuing node and returns the response.
// Called by the pouch's background refresh loop.
func (gp *GrantProtocol) RequestRefresh(ctx context.Context, issuerID peer.ID, tokenID string) (*GrantRefreshResponse, error) {
	req := GrantRefreshRequest{Version: GrantProtocolVersion, TokenID: tokenID}
	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal refresh request: %w", err)
	}

	if len(data) > grantMaxPayload {
		return nil, fmt.Errorf("payload too large: %d > %d", len(data), grantMaxPayload)
	}

	ctx, cancel := context.WithTimeout(ctx, grantTimeout)
	defer cancel()

	ctx = libp2pnet.WithAllowLimitedConn(ctx, GrantProtocolID)

	s, err := gp.host.NewStream(ctx, issuerID, protocol.ID(GrantProtocolID))
	if err != nil {
		return nil, fmt.Errorf("open stream: %w", err)
	}
	defer s.Close()

	s.SetDeadline(time.Now().Add(grantTimeout))

	// Write: type(1) + length(4) + data.
	var header [5]byte
	header[0] = msgGrantRefresh
	binary.BigEndian.PutUint32(header[1:], uint32(len(data)))
	if _, err := s.Write(header[:]); err != nil {
		return nil, fmt.Errorf("write header: %w", err)
	}
	if _, err := s.Write(data); err != nil {
		return nil, fmt.Errorf("write payload: %w", err)
	}

	// Read response (comes back as msgGrantAck with GrantRefreshResponse JSON).
	ack, err := gp.readMessage(s)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp GrantRefreshResponse
	if err := json.Unmarshal(ack.data, &resp); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	return &resp, nil
}

// StartQueueFlush starts a background loop that flushes pending deliveries
// when peers connect. Subscribes to libp2p peer connectedness events.
func (gp *GrantProtocol) StartQueueFlush() {
	gp.flushStarted = true
	if gp.queue == nil {
		close(gp.doneCh)
		return
	}

	sub, err := gp.host.EventBus().Subscribe(new(event.EvtPeerConnectednessChanged))
	if err != nil {
		gp.logger.Error("grant-protocol: failed to subscribe to peer events", "error", err)
		close(gp.doneCh)
		return
	}

	go func() {
		defer close(gp.doneCh)
		defer sub.Close()

		for {
			select {
			case <-gp.stopCh:
				return
			case evt, ok := <-sub.Out():
				if !ok {
					return
				}
				e := evt.(event.EvtPeerConnectednessChanged)
				if e.Connectedness == libp2pnet.Connected {
					gp.flushQueue(e.Peer)
				}
			}
		}
	}()

	gp.logger.Info("grant-protocol: queue flush loop started")
}

// Stop signals the background queue flush loop to stop and waits for it.
// Safe to call even if StartQueueFlush was never called.
func (gp *GrantProtocol) Stop() {
	if !gp.flushStarted {
		return
	}
	close(gp.stopCh)
	<-gp.doneCh
}

// flushQueue delivers all pending items for the given peer.
func (gp *GrantProtocol) flushQueue(peerID peer.ID) {
	items := gp.queue.Dequeue(peerID)
	if len(items) == 0 {
		return
	}

	short := shortPeerID(peerID)
	gp.logger.Info("grant-protocol: flushing queued deliveries", "peer", short, "count", len(items))

	for _, item := range items {
		ctx, cancel := context.WithTimeout(context.Background(), grantTimeout)
		err := gp.sendMessage(ctx, peerID, item.MsgType, item.Payload)
		cancel()
		if err != nil {
			gp.logger.Warn("grant-protocol: queued delivery failed",
				"peer", short, "type", item.MsgType, "error", err)
			// Re-enqueue on failure (best effort, don't loop forever).
			if reqErr := gp.queue.Enqueue(peerID, item.MsgType, item.Payload); reqErr != nil {
				gp.logger.Warn("grant-protocol: re-enqueue failed", "peer", short, "error", reqErr)
			}
		} else {
			gp.logger.Info("grant-protocol: queued delivery succeeded", "peer", short, "type", item.MsgType)
		}
	}
}

// EnqueueGrant serializes a grant delivery and adds it to the offline queue.
func (gp *GrantProtocol) EnqueueGrant(peerID peer.ID, token *macaroon.Macaroon, services []string, expiresAt time.Time, permanent bool) error {
	if gp.queue == nil {
		return fmt.Errorf("no delivery queue configured")
	}

	tokenB64, err := token.EncodeBase64()
	if err != nil {
		return fmt.Errorf("encode token: %w", err)
	}

	delivery := GrantDelivery{
		Version:   GrantProtocolVersion,
		Token:     tokenB64,
		Services:  services,
		Permanent: permanent,
	}
	if !permanent {
		delivery.ExpiresAt = expiresAt.Format(time.RFC3339)
	}

	payload, err := json.Marshal(delivery)
	if err != nil {
		return fmt.Errorf("marshal delivery: %w", err)
	}

	return gp.queue.Enqueue(peerID, msgGrantDeliver, payload)
}

// EnqueueRevocation serializes a revocation notice and adds it to the offline queue.
func (gp *GrantProtocol) EnqueueRevocation(peerID peer.ID, reason string) error {
	if gp.queue == nil {
		return fmt.Errorf("no delivery queue configured")
	}

	rev := GrantRevocation{Version: GrantProtocolVersion, Reason: reason}
	payload, err := json.Marshal(rev)
	if err != nil {
		return fmt.Errorf("marshal revocation: %w", err)
	}

	return gp.queue.Enqueue(peerID, msgGrantRevoke, payload)
}

// checkRateLimit returns true if the peer is within rate limits.
func (gp *GrantProtocol) checkRateLimit(peerID peer.ID) bool {
	gp.rateMu.Lock()
	defer gp.rateMu.Unlock()

	now := time.Now()

	// Prune stale entries (older than 2 minutes) to prevent unbounded map growth.
	if len(gp.rateLimit) > 50 {
		for pid, e := range gp.rateLimit {
			if now.Sub(e.windowAt) > 2*time.Minute {
				delete(gp.rateLimit, pid)
			}
		}
	}

	entry, exists := gp.rateLimit[peerID]
	if !exists {
		gp.rateLimit[peerID] = &rateLimitEntry{count: 1, windowAt: now}
		return true
	}

	// Reset window if more than a minute has passed.
	if now.Sub(entry.windowAt) > time.Minute {
		entry.count = 1
		entry.windowAt = now
		return true
	}

	entry.count++
	return entry.count <= grantMaxPerMinute
}
