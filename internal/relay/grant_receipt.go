package relay

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log/slog"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/internal/grants"
)

// GrantReceiptProtocol is the upgraded grant notification protocol.
// Replaces GrantChangedProtocol with a full receipt payload.
const GrantReceiptProtocol = "/shurli/grant-receipt/1.0.0"

// GrantChecker is the subset of grants.Store needed for reconnect receipt delivery.
// Defined here (not in notify.go) because it's a grant receipt concern.
type GrantChecker interface {
	CheckAndGet(peerID peer.ID) *grants.Grant
}

// grantReceiptVersion is the wire format version.
const grantReceiptVersion byte = 0x01

// GrantReceiptSize is the total wire size: 1 + 8 + 8 + 4 + 1 + 8 + 32 = 62 bytes.
const GrantReceiptSize = 62

// GrantReceiptPayloadSize is the canonical payload size (without HMAC): 1 + 8 + 8 + 4 + 1 + 8 = 30 bytes.
const GrantReceiptPayloadSize = 30

// EncodeGrantReceipt encodes a grant receipt into the wire format.
//
// Wire format (62 bytes):
//
//	[1]       version (0x01)
//	[8 BE]    grant_duration_secs (relative, 0 for permanent)
//	[8 BE]    session_data_limit (bytes, 0 = unlimited)
//	[4 BE]    session_duration_secs (per circuit session)
//	[1]       permanent flag (0x00=no, 0x01=yes)
//	[8 BE]    issued_at (Unix seconds)
//	[32]      HMAC-SHA256(key, canonical payload above)
func EncodeGrantReceipt(grantDuration time.Duration, sessionDataLimit int64,
	sessionDuration time.Duration, permanent bool, issuedAt time.Time,
	hmacKey []byte) []byte {

	buf := make([]byte, GrantReceiptSize)

	// Version.
	buf[0] = grantReceiptVersion

	// Grant duration in seconds (0 for permanent grants).
	// Use integer division to avoid float64 truncation from Seconds().
	var durationSecs uint64
	if !permanent {
		durationSecs = uint64(grantDuration / time.Second)
	}
	binary.BigEndian.PutUint64(buf[1:9], durationSecs)

	// Session data limit (clamped to 0 if negative to prevent uint64 wrap).
	if sessionDataLimit < 0 {
		sessionDataLimit = 0
	}
	binary.BigEndian.PutUint64(buf[9:17], uint64(sessionDataLimit))

	// Session duration in seconds (integer division, not float conversion).
	binary.BigEndian.PutUint32(buf[17:21], uint32(sessionDuration/time.Second))

	// Permanent flag.
	if permanent {
		buf[21] = 0x01
	}

	// Issued at (Unix seconds).
	binary.BigEndian.PutUint64(buf[22:30], uint64(issuedAt.Unix()))

	// HMAC-SHA256 over canonical payload (first 30 bytes).
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(buf[:GrantReceiptPayloadSize])
	copy(buf[GrantReceiptPayloadSize:], mac.Sum(nil))

	return buf
}

// DecodedGrantReceipt holds the parsed fields from a grant receipt.
type DecodedGrantReceipt struct {
	GrantDuration    time.Duration
	SessionDataLimit int64
	SessionDuration  time.Duration
	Permanent        bool
	IssuedAt         time.Time
	HMAC             [32]byte
}

// DecodeGrantReceipt parses a grant receipt from the wire format.
// Returns the decoded receipt or an error if the data is malformed.
// Does NOT verify the HMAC - call VerifyGrantReceipt for that.
func DecodeGrantReceipt(data []byte) (*DecodedGrantReceipt, error) {
	if len(data) != GrantReceiptSize {
		return nil, fmt.Errorf("invalid receipt size: got %d, want %d", len(data), GrantReceiptSize)
	}

	if data[0] != grantReceiptVersion {
		return nil, fmt.Errorf("unsupported receipt version: 0x%02x", data[0])
	}

	r := &DecodedGrantReceipt{}

	// Cap duration to prevent time.Duration overflow (max ~292 years in nanoseconds).
	// maxDurationSecs is well above any real grant (max 365 days) but prevents wrap.
	const maxDurationSecs = uint64(1<<63-1) / uint64(time.Second)

	durationSecs := binary.BigEndian.Uint64(data[1:9])
	if durationSecs > maxDurationSecs {
		durationSecs = maxDurationSecs
	}
	r.GrantDuration = time.Duration(durationSecs) * time.Second

	r.SessionDataLimit = int64(binary.BigEndian.Uint64(data[9:17]))

	sessionDurationSecs := binary.BigEndian.Uint32(data[17:21])
	// uint32 max (~136 years in seconds) fits in time.Duration without overflow.
	r.SessionDuration = time.Duration(sessionDurationSecs) * time.Second

	r.Permanent = data[21] == 0x01

	issuedAtUnix := int64(binary.BigEndian.Uint64(data[22:30]))
	r.IssuedAt = time.Unix(issuedAtUnix, 0)

	copy(r.HMAC[:], data[GrantReceiptPayloadSize:])

	return r, nil
}

// VerifyGrantReceipt checks the HMAC on a receipt's wire bytes.
func VerifyGrantReceipt(data []byte, hmacKey []byte) bool {
	if len(data) != GrantReceiptSize {
		return false
	}

	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(data[:GrantReceiptPayloadSize])
	expected := mac.Sum(nil)

	return hmac.Equal(data[GrantReceiptPayloadSize:], expected)
}

// NotifyGrantReceipt encodes a grant receipt and sends it to a connected peer.
// If the peer is not connected, returns nil (nothing to do).
// Session limits come from relay config, not grant store (H13).
// Permanent grants still carry session limits (H14).
//
// NOTE: This reads from the Grant pointer. Callers must ensure the Grant is
// either a clone (e.g., from CheckAndGet) or not concurrently modified.
// For handleRelayGrant, use EncodeGrantReceipt + sendGrantReceipt instead
// to avoid racing with concurrent Extend() calls on the store's shared pointer.
func NotifyGrantReceipt(ctx context.Context, h host.Host, targetPeerID peer.ID,
	grant *grants.Grant, sessionDataLimit int64, sessionDuration time.Duration,
	hmacKey []byte) error {

	if h.Network().Connectedness(targetPeerID) != network.Connected {
		return nil // not connected, will be delivered on reconnect
	}

	// Build receipt from grant + config session limits.
	var grantDuration time.Duration
	if !grant.Permanent {
		grantDuration = time.Until(grant.ExpiresAt)
		if grantDuration < 0 {
			grantDuration = 0
		}
	}

	receiptData := EncodeGrantReceipt(grantDuration, sessionDataLimit,
		sessionDuration, grant.Permanent, time.Now(), hmacKey)

	return sendGrantReceipt(ctx, h, targetPeerID, receiptData)
}

// sendGrantReceipt sends pre-encoded receipt bytes to a connected peer.
// Negotiates GrantReceiptProtocol with GrantChangedProtocol fallback.
// If the peer is not connected, returns nil.
func sendGrantReceipt(ctx context.Context, h host.Host, targetPeerID peer.ID,
	receiptData []byte) error {

	if h.Network().Connectedness(targetPeerID) != network.Connected {
		return nil
	}

	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	// Allow delivery over limited (relayed) connections (H15).
	streamCtx = network.WithAllowLimitedConn(streamCtx, GrantReceiptProtocol)
	s, err := h.NewStream(streamCtx, targetPeerID,
		protocol.ID(GrantReceiptProtocol),
		protocol.ID(GrantChangedProtocol)) // fallback to old protocol
	if err != nil {
		return fmt.Errorf("grant-receipt stream: %w", err)
	}
	defer s.Close()

	short := targetPeerID.String()
	if len(short) > 16 {
		short = short[:16] + "..."
	}

	// Check which protocol was negotiated.
	if s.Protocol() == protocol.ID(GrantChangedProtocol) {
		// Peer only supports old protocol - send 1-byte signal.
		if _, err := s.Write([]byte{notifyVersion}); err != nil {
			return fmt.Errorf("grant-changed write: %w", err)
		}
		slog.Info("grant-changed: notified peer (legacy)", "peer", short)
		return nil
	}

	// New protocol - send full receipt.
	if _, err := s.Write(receiptData); err != nil {
		return fmt.Errorf("grant-receipt write: %w", err)
	}

	slog.Info("grant-receipt: delivered to peer", "peer", short)
	return nil
}
