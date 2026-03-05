package relay

import (
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/validate"
)

// MOTDMessage represents a received MOTD or goodbye message.
type MOTDMessage struct {
	Type      byte   // motdTypeMOTD, motdTypeGoodbye, motdTypeRetract
	Message   string
	Timestamp int64
	RelayPeer peer.ID
}

// MOTDCallback is called when a valid MOTD/goodbye is received.
type MOTDCallback func(msg MOTDMessage)

// lastMOTDEntry tracks a received MOTD for dedup and status queries.
type lastMOTDEntry struct {
	Message   string
	Timestamp int64
	ShownAt   time.Time
}

// MOTDClient handles incoming MOTD streams from relays.
type MOTDClient struct {
	callback MOTDCallback
	dataDir  string // where to store goodbyes.json

	mu            sync.RWMutex
	lastMOTDShown map[peer.ID]lastMOTDEntry // dedup + message storage
}

// NewMOTDClient creates a client handler for relay MOTD messages.
func NewMOTDClient(callback MOTDCallback, dataDir string) *MOTDClient {
	return &MOTDClient{
		callback:      callback,
		dataDir:       dataDir,
		lastMOTDShown: make(map[peer.ID]lastMOTDEntry),
	}
}

// HandleStream processes an incoming MOTD stream from a relay.
func (c *MOTDClient) HandleStream(s network.Stream) {
	defer s.Close()
	remotePeer := s.Conn().RemotePeer()

	s.SetReadDeadline(time.Now().Add(10 * time.Second))

	// Read version byte.
	var versionBuf [1]byte
	if _, err := io.ReadFull(s, versionBuf[:]); err != nil {
		slog.Debug("motd-client: failed to read version", "err", err)
		return
	}
	if versionBuf[0] != motdWireVersion {
		slog.Debug("motd-client: unsupported version", "version", versionBuf[0])
		return
	}

	// Read type byte.
	var typeBuf [1]byte
	if _, err := io.ReadFull(s, typeBuf[:]); err != nil {
		slog.Debug("motd-client: failed to read type", "err", err)
		return
	}
	msgType := typeBuf[0]

	// Read message length.
	var lenBuf [2]byte
	if _, err := io.ReadFull(s, lenBuf[:]); err != nil {
		slog.Debug("motd-client: failed to read msg len", "err", err)
		return
	}
	msgLen := binary.BigEndian.Uint16(lenBuf[:])
	if msgLen > maxMOTDMessageLen {
		slog.Warn("motd-client: message too long", "len", msgLen)
		return
	}

	// Read message.
	msgBuf := make([]byte, msgLen)
	if msgLen > 0 {
		if _, err := io.ReadFull(s, msgBuf); err != nil {
			slog.Debug("motd-client: failed to read msg", "err", err)
			return
		}
	}

	// Read timestamp.
	var tsBuf [8]byte
	if _, err := io.ReadFull(s, tsBuf[:]); err != nil {
		slog.Debug("motd-client: failed to read timestamp", "err", err)
		return
	}
	timestamp := int64(binary.BigEndian.Uint64(tsBuf[:]))

	// Validate timestamp bounds: reject messages with timestamps too far in the
	// future (clock skew / manipulation) or too far in the past (replay / stale).
	// Goodbyes persist for days so the past window is generous; the future window
	// is tight since clocks should agree within minutes.
	now := time.Now().Unix()
	const (
		maxFutureSkew = 5 * 60       // 5 minutes
		maxPastAge    = 7 * 24 * 3600 // 7 days (covers stored goodbyes)
	)
	if timestamp > now+maxFutureSkew {
		slog.Warn("motd-client: timestamp too far in future", "peer", shortPeerID(remotePeer), "ts", timestamp, "now", now)
		return
	}
	if timestamp < now-maxPastAge {
		slog.Warn("motd-client: timestamp too old", "peer", shortPeerID(remotePeer), "ts", timestamp, "now", now)
		return
	}

	// Read signature (rest of stream).
	sig, err := io.ReadAll(io.LimitReader(s, 256))
	if err != nil {
		slog.Debug("motd-client: failed to read signature", "err", err)
		return
	}

	// Verify signature against relay's public key.
	pubKey := s.Conn().RemotePublicKey()
	if pubKey == nil {
		slog.Warn("motd-client: no public key for relay", "peer", shortPeerID(remotePeer))
		return
	}

	msg := string(msgBuf)
	signableData := buildSignableData(msgType, msg, timestamp)
	valid, err := pubKey.Verify(signableData, sig)
	if err != nil || !valid {
		slog.Warn("motd-client: invalid signature", "peer", shortPeerID(remotePeer))
		return
	}

	// Sanitize the message (defense in depth).
	msg = validate.SanitizeRelayMessage(msg)

	switch msgType {
	case motdTypeMOTD:
		// Dedup: don't re-show within 24h.
		c.mu.Lock()
		if entry, ok := c.lastMOTDShown[remotePeer]; ok && time.Since(entry.ShownAt) < 24*time.Hour {
			c.mu.Unlock()
			return
		}
		c.lastMOTDShown[remotePeer] = lastMOTDEntry{
			Message:   msg,
			Timestamp: timestamp,
			ShownAt:   time.Now(),
		}
		c.mu.Unlock()

		slog.Info("motd: received from relay", "peer", shortPeerID(remotePeer), "msg", msg)
		if c.callback != nil {
			c.callback(MOTDMessage{Type: motdTypeMOTD, Message: msg, Timestamp: timestamp, RelayPeer: remotePeer})
		}

	case motdTypeGoodbye:
		slog.Info("motd: goodbye from relay", "peer", shortPeerID(remotePeer), "msg", msg)
		c.storeGoodbye(remotePeer, msg, timestamp, sig)
		if c.callback != nil {
			c.callback(MOTDMessage{Type: motdTypeGoodbye, Message: msg, Timestamp: timestamp, RelayPeer: remotePeer})
		}

	case motdTypeRetract:
		slog.Info("motd: goodbye retracted by relay", "peer", shortPeerID(remotePeer))
		c.removeGoodbye(remotePeer)
		if c.callback != nil {
			c.callback(MOTDMessage{Type: motdTypeRetract, RelayPeer: remotePeer})
		}
	}
}

// GetStoredGoodbye retrieves a stored goodbye for a relay peer.
// Returns the message, timestamp, and true if found.
func (c *MOTDClient) GetStoredGoodbye(relayPeer peer.ID) (string, int64, bool) {
	goodbyes := c.loadGoodbyes()
	key := relayPeer.String()
	if g, ok := goodbyes[key]; ok {
		return g.Message, g.Timestamp, true
	}
	return "", 0, false
}

// GetLastMOTD retrieves the last received MOTD for a relay peer.
// Returns the message, timestamp, and true if found (within the 24h dedup window).
func (c *MOTDClient) GetLastMOTD(relayPeer peer.ID) (string, int64, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if entry, ok := c.lastMOTDShown[relayPeer]; ok {
		return entry.Message, entry.Timestamp, true
	}
	return "", 0, false
}

// --- Goodbye persistence ---

type storedGoodbye struct {
	Message   string `json:"message"`
	Timestamp int64  `json:"timestamp"`
	Signature []byte `json:"signature"`
}

func (c *MOTDClient) goodbyesPath() string {
	return filepath.Join(c.dataDir, "goodbyes.json")
}

func (c *MOTDClient) loadGoodbyes() map[string]storedGoodbye {
	path := c.goodbyesPath()
	fi, err := os.Stat(path)
	if err != nil {
		return make(map[string]storedGoodbye)
	}
	// Reject unreasonably large goodbye files (max 64 KB for client-side
	// which may store goodbyes from multiple relays).
	if fi.Size() > 64*1024 {
		return make(map[string]storedGoodbye)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string]storedGoodbye)
	}
	var goodbyes map[string]storedGoodbye
	if err := json.Unmarshal(data, &goodbyes); err != nil {
		return make(map[string]storedGoodbye)
	}
	return goodbyes
}

func (c *MOTDClient) saveGoodbyes(goodbyes map[string]storedGoodbye) {
	data, err := json.MarshalIndent(goodbyes, "", "  ")
	if err != nil {
		return
	}
	os.WriteFile(c.goodbyesPath(), data, 0600)
}

func (c *MOTDClient) storeGoodbye(relayPeer peer.ID, msg string, timestamp int64, sig []byte) {
	goodbyes := c.loadGoodbyes()
	goodbyes[relayPeer.String()] = storedGoodbye{
		Message:   msg,
		Timestamp: timestamp,
		Signature: sig,
	}
	c.saveGoodbyes(goodbyes)
}

func (c *MOTDClient) removeGoodbye(relayPeer peer.ID) {
	goodbyes := c.loadGoodbyes()
	delete(goodbyes, relayPeer.String())
	c.saveGoodbyes(goodbyes)
}

// CleanLastMOTDShown evicts expired entries from the dedup map.
// Entries older than 24 hours are removed. Safe to call periodically.
func (c *MOTDClient) CleanLastMOTDShown() {
	cutoff := time.Now().Add(-24 * time.Hour)
	c.mu.Lock()
	for k, entry := range c.lastMOTDShown {
		if entry.ShownAt.Before(cutoff) {
			delete(c.lastMOTDShown, k)
		}
	}
	c.mu.Unlock()
}

// VerifyStoredGoodbye re-verifies a stored goodbye signature against a public key.
func VerifyStoredGoodbye(pubKey crypto.PubKey, msg string, timestamp int64, sig []byte) bool {
	data := buildSignableData(motdTypeGoodbye, msg, timestamp)
	valid, err := pubKey.Verify(data, sig)
	return err == nil && valid
}
