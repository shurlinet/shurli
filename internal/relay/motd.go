package relay

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/validate"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// MOTDProtocol is the libp2p protocol ID for relay MOTD and goodbye messages.
const MOTDProtocol = "/shurli/relay-motd/1.0.0"

// Wire format: [1 version][1 type][2 BE msg-len][N msg][8 BE timestamp][64 Ed25519 sig]
// Types: 0x01 = MOTD, 0x02 = goodbye, 0x03 = retract
const (
	motdWireVersion byte = 0x01
	motdTypeMOTD    byte = 0x01
	motdTypeGoodbye byte = 0x02
	motdTypeRetract byte = 0x03

	maxMOTDMessageLen = 280
	motdDeliveryTimeout = 10 * time.Second
)

// persistedGoodbye is the JSON structure saved to disk.
type persistedGoodbye struct {
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
}

// MOTDHandler manages relay operator announcements and goodbye messages.
type MOTDHandler struct {
	host    host.Host
	privKey crypto.PrivKey
	metrics *p2pnet.Metrics

	mu          sync.RWMutex
	motd        string
	goodbye     string
	goodbyeFile string // path to persist goodbye on disk
	goodbyeSig  []byte
	goodbyeTime int64
}

// NewMOTDHandler creates a handler for relay MOTD and goodbye messages.
func NewMOTDHandler(h host.Host, privKey crypto.PrivKey, goodbyeFile string) *MOTDHandler {
	handler := &MOTDHandler{
		host:        h,
		privKey:     privKey,
		goodbyeFile: goodbyeFile,
	}
	// Load persisted goodbye if it exists.
	handler.loadPersistedGoodbye()
	return handler
}

// SetMetrics attaches metrics to the handler.
func (h *MOTDHandler) SetMetrics(m *p2pnet.Metrics) {
	h.metrics = m
}

// SetMOTD sets the current MOTD message (sanitized).
func (h *MOTDHandler) SetMOTD(msg string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.motd = validate.SanitizeRelayMessage(msg)
	slog.Info("motd: set", "len", len(h.motd))
}

// ClearMOTD clears the current MOTD.
func (h *MOTDHandler) ClearMOTD() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.motd = ""
	slog.Info("motd: cleared")
}

// MOTD returns the current MOTD message.
func (h *MOTDHandler) MOTD() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.motd
}

// SetGoodbye sets the goodbye announcement (persisted to disk).
func (h *MOTDHandler) SetGoodbye(msg string) error {
	sanitized := validate.SanitizeRelayMessage(msg)
	ts := time.Now().Unix()

	// Sign the goodbye.
	sig, err := h.signFrame(motdTypeGoodbye, sanitized, ts)
	if err != nil {
		return fmt.Errorf("failed to sign goodbye: %w", err)
	}

	h.mu.Lock()
	h.goodbye = sanitized
	h.goodbyeSig = sig
	h.goodbyeTime = ts
	h.mu.Unlock()

	// Persist to disk.
	if err := h.persistGoodbye(); err != nil {
		slog.Warn("motd: failed to persist goodbye", "err", err)
	}

	// Push to all connected peers.
	h.pushToAllPeers(motdTypeGoodbye, sanitized, ts, sig)

	slog.Info("motd: goodbye set", "msg", sanitized)
	return nil
}

// RetractGoodbye clears the goodbye and pushes a retract to all connected peers.
func (h *MOTDHandler) RetractGoodbye() error {
	ts := time.Now().Unix()

	sig, err := h.signFrame(motdTypeRetract, "", ts)
	if err != nil {
		return fmt.Errorf("failed to sign retract: %w", err)
	}

	h.mu.Lock()
	h.goodbye = ""
	h.goodbyeSig = nil
	h.goodbyeTime = 0
	h.mu.Unlock()

	// Remove persisted goodbye.
	if h.goodbyeFile != "" {
		os.Remove(h.goodbyeFile)
	}

	// Push retract to all connected peers.
	h.pushToAllPeers(motdTypeRetract, "", ts, sig)

	slog.Info("motd: goodbye retracted")
	return nil
}

// HasActiveGoodbye returns true if a goodbye message is active.
func (h *MOTDHandler) HasActiveGoodbye() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.goodbye != ""
}

// Goodbye returns the current goodbye message.
func (h *MOTDHandler) Goodbye() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.goodbye
}

// GoodbyeShutdown pushes the goodbye to all peers (with timeout) then signals shutdown.
// Returns after all peers have been notified (or timeout).
func (h *MOTDHandler) GoodbyeShutdown(msg string) error {
	if err := h.SetGoodbye(msg); err != nil {
		return err
	}
	// Give peers time to receive the goodbye.
	time.Sleep(5 * time.Second)
	return nil
}

// PushToPeer sends the current MOTD and any active goodbye to a single peer.
func (h *MOTDHandler) PushToPeer(peerID peer.ID) {
	h.mu.RLock()
	motd := h.motd
	goodbye := h.goodbye
	goodbyeSig := h.goodbyeSig
	goodbyeTime := h.goodbyeTime
	h.mu.RUnlock()

	if motd != "" {
		ts := time.Now().Unix()
		sig, err := h.signFrame(motdTypeMOTD, motd, ts)
		if err == nil {
			h.sendFrame(peerID, motdTypeMOTD, motd, ts, sig)
		}
	}

	if goodbye != "" && goodbyeSig != nil {
		h.sendFrame(peerID, motdTypeGoodbye, goodbye, goodbyeTime, goodbyeSig)
	}
}

// RunMOTDNotifier subscribes to peer identification events and pushes
// MOTD + active goodbye to newly connected peers.
func (h *MOTDHandler) RunMOTDNotifier(ctx context.Context) {
	sub, err := h.host.EventBus().Subscribe(new(event.EvtPeerIdentificationCompleted))
	if err != nil {
		slog.Error("motd-notifier: subscribe failed", "err", err)
		return
	}
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-sub.Out():
			if !ok {
				return
			}
			e := evt.(event.EvtPeerIdentificationCompleted)
			// Push MOTD and goodbye to the newly identified peer.
			go h.PushToPeer(e.Peer)
		}
	}
}

// --- Wire format ---

// signFrame signs the frame data: [type][msg-bytes][timestamp-BE-8]
func (h *MOTDHandler) signFrame(msgType byte, msg string, timestamp int64) ([]byte, error) {
	data := buildSignableData(msgType, msg, timestamp)
	sig, err := h.privKey.Sign(data)
	if err != nil {
		return nil, err
	}
	return sig, nil
}

// buildSignableData constructs the bytes to sign/verify.
func buildSignableData(msgType byte, msg string, timestamp int64) []byte {
	msgBytes := []byte(msg)
	data := make([]byte, 1+len(msgBytes)+8)
	data[0] = msgType
	copy(data[1:], msgBytes)
	binary.BigEndian.PutUint64(data[1+len(msgBytes):], uint64(timestamp))
	return data
}

// encodeFrame builds the wire frame: [1 version][1 type][2 BE msg-len][N msg][8 BE timestamp][sig]
func encodeFrame(msgType byte, msg string, timestamp int64, sig []byte) []byte {
	msgBytes := []byte(msg)
	frame := make([]byte, 1+1+2+len(msgBytes)+8+len(sig))
	frame[0] = motdWireVersion
	frame[1] = msgType
	binary.BigEndian.PutUint16(frame[2:4], uint16(len(msgBytes)))
	copy(frame[4:], msgBytes)
	off := 4 + len(msgBytes)
	binary.BigEndian.PutUint64(frame[off:off+8], uint64(timestamp))
	copy(frame[off+8:], sig)
	return frame
}

// sendFrame opens a stream and sends a single MOTD frame to a peer.
func (h *MOTDHandler) sendFrame(peerID peer.ID, msgType byte, msg string, timestamp int64, sig []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), motdDeliveryTimeout)
	defer cancel()

	s, err := h.host.NewStream(ctx, peerID, protocol.ID(MOTDProtocol))
	if err != nil {
		slog.Debug("motd: failed to open stream", "peer", shortPeerID(peerID), "err", err)
		return
	}
	defer s.Close()

	s.SetWriteDeadline(time.Now().Add(motdDeliveryTimeout))
	frame := encodeFrame(msgType, msg, timestamp, sig)
	if _, err := s.Write(frame); err != nil {
		slog.Debug("motd: failed to send", "peer", shortPeerID(peerID), "err", err)
	}
}

// pushToAllPeers sends a frame to all currently connected peers.
func (h *MOTDHandler) pushToAllPeers(msgType byte, msg string, timestamp int64, sig []byte) {
	peers := h.host.Network().Peers()
	for _, p := range peers {
		go h.sendFrame(p, msgType, msg, timestamp, sig)
	}
	slog.Info("motd: pushed to connected peers", "type", msgType, "count", len(peers))
}

// --- Persistence ---

func (h *MOTDHandler) persistGoodbye() error {
	if h.goodbyeFile == "" {
		return nil
	}
	h.mu.RLock()
	data, err := json.Marshal(persistedGoodbye{
		Message:   h.goodbye,
		Timestamp: h.goodbyeTime,
		Signature: h.goodbyeSig,
	})
	h.mu.RUnlock()
	if err != nil {
		return err
	}
	return os.WriteFile(h.goodbyeFile, data, 0600)
}

func (h *MOTDHandler) loadPersistedGoodbye() {
	if h.goodbyeFile == "" {
		return
	}

	fi, err := os.Stat(h.goodbyeFile)
	if err != nil {
		return
	}
	// M-1: reject unreasonably large goodbye files (max 4 KB).
	if fi.Size() > 4096 {
		slog.Warn("motd: persisted goodbye file too large, ignoring", "size", fi.Size())
		return
	}

	data, err := os.ReadFile(h.goodbyeFile)
	if err != nil {
		return
	}
	var pg persistedGoodbye
	if err := json.Unmarshal(data, &pg); err != nil {
		return
	}
	if pg.Message == "" {
		return
	}

	// H-1: re-verify signature before trusting persisted data.
	if h.privKey != nil {
		pubKey := h.privKey.GetPublic()
		signableData := buildSignableData(motdTypeGoodbye, pg.Message, pg.Timestamp)
		valid, verErr := pubKey.Verify(signableData, pg.Signature)
		if verErr != nil || !valid {
			slog.Warn("motd: persisted goodbye has invalid signature, discarding", "err", verErr)
			os.Remove(h.goodbyeFile)
			return
		}
	}

	h.goodbye = pg.Message
	h.goodbyeTime = pg.Timestamp
	h.goodbyeSig = pg.Signature
	slog.Info("motd: loaded persisted goodbye (signature verified)", "msg", pg.Message)
}
