package relay

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/pkg/p2pnet"
)

// UnsealProtocol is the libp2p protocol ID for remote vault unseal.
const UnsealProtocol = "/shurli/relay-unseal/1.0.0"

// Wire format:
//   Request:  [1 version] [2 BE passphrase-len] [N passphrase] [1 TOTP-len] [M TOTP]
//   Response: [1 status] [1 msg-len] [N message]
//
// Status codes:
//   0x01 = success (unsealed)
//   0x00 = error (message follows)

const (
	unsealWireVersion byte = 0x01
	unsealStatusOK    byte = 0x01
	unsealStatusErr   byte = 0x00
	maxPassphraseLen       = 1024
	maxTOTPLen             = 16

	// iOS-style lockout: generous for typos, escalating for brute force.
	// First 4 failures: immediate retry allowed.
	// 5th failure: 1 minute lockout.
	// 6th: 5 minutes. 7th: 15 minutes. 8th+: 1 hour.
	unsealFreeAttempts = 4
)

// unsealLockoutSchedule maps the failure count (after free attempts) to lockout duration.
// iOS-style escalation: generous for typos, then escalating, then permanent block.
// Index 0 = 5th failure, index 1 = 6th failure, etc.
// After exhausting the schedule, the peer is permanently blocked from remote unseal.
var unsealLockoutSchedule = []time.Duration{
	1 * time.Minute,  // 5th failure
	5 * time.Minute,  // 6th failure
	15 * time.Minute, // 7th failure
	1 * time.Hour,    // 8th failure
	1 * time.Hour,    // 9th failure
	1 * time.Hour,    // 10th failure (last chance)
}

// peerLockout tracks consecutive failed unseal attempts for a single peer.
type peerLockout struct {
	failures    int
	lockedUntil time.Time
	blocked     bool // permanently blocked after exhausting all lockout levels
}

// UnsealHandler handles remote unseal requests from admin peers.
type UnsealHandler struct {
	Vault        *vault.Vault
	AuthKeysPath string
	Metrics      *p2pnet.Metrics // nil-safe: metrics are optional

	mu       sync.Mutex
	lockouts map[peer.ID]*peerLockout
}

// NewUnsealHandler creates a handler for the remote unseal protocol.
func NewUnsealHandler(v *vault.Vault, authKeysPath string) *UnsealHandler {
	return &UnsealHandler{
		Vault:        v,
		AuthKeysPath: authKeysPath,
		lockouts:     make(map[peer.ID]*peerLockout),
	}
}

// HandleStream processes an incoming unseal stream.
func (h *UnsealHandler) HandleStream(s network.Stream) {
	defer s.Close()
	remotePeer := s.Conn().RemotePeer()
	short := remotePeer.String()[:16] + "..."

	// Only admin peers can unseal.
	if !auth.IsAdmin(h.AuthKeysPath, remotePeer) {
		slog.Warn("unseal: rejected non-admin peer", "peer", short)
		h.recordMetric("denied")
		writeUnsealResponse(s, unsealStatusErr, "permission denied: admin role required")
		return
	}

	// Check lockout (iOS-style escalating backoff).
	if locked, remaining := h.isLockedOut(remotePeer); locked {
		if remaining < 0 {
			slog.Warn("unseal: permanently blocked peer attempted unseal", "peer", short)
			h.recordMetric("blocked")
			writeUnsealResponse(s, unsealStatusErr, "permanently blocked: unseal via SSH on the relay server")
			return
		}
		slog.Warn("unseal: locked out", "peer", short, "remaining", remaining.Round(time.Second))
		h.recordMetric("locked_out")
		msg := fmt.Sprintf("locked out: try again in %s", formatLockoutRemaining(remaining))
		writeUnsealResponse(s, unsealStatusErr, msg)
		return
	}

	// Set read deadline.
	s.SetReadDeadline(time.Now().Add(30 * time.Second))

	// Read version byte.
	var versionBuf [1]byte
	if _, err := io.ReadFull(s, versionBuf[:]); err != nil {
		slog.Warn("unseal: failed to read version", "peer", short, "err", err)
		writeUnsealResponse(s, unsealStatusErr, "protocol error")
		return
	}
	if versionBuf[0] != unsealWireVersion {
		slog.Warn("unseal: unsupported version", "peer", short, "version", versionBuf[0])
		writeUnsealResponse(s, unsealStatusErr, "unsupported protocol version")
		return
	}

	// Read passphrase length (2 bytes BE).
	var passLenBuf [2]byte
	if _, err := io.ReadFull(s, passLenBuf[:]); err != nil {
		slog.Warn("unseal: failed to read passphrase length", "peer", short, "err", err)
		writeUnsealResponse(s, unsealStatusErr, "protocol error")
		return
	}
	passLen := binary.BigEndian.Uint16(passLenBuf[:])
	if passLen == 0 || passLen > maxPassphraseLen {
		slog.Warn("unseal: invalid passphrase length", "peer", short, "len", passLen)
		writeUnsealResponse(s, unsealStatusErr, "invalid passphrase length")
		return
	}

	// Read passphrase.
	passBuf := make([]byte, passLen)
	if _, err := io.ReadFull(s, passBuf); err != nil {
		slog.Warn("unseal: failed to read passphrase", "peer", short, "err", err)
		writeUnsealResponse(s, unsealStatusErr, "protocol error")
		return
	}
	passphrase := string(passBuf)

	// Read TOTP length (1 byte).
	var totpLenBuf [1]byte
	if _, err := io.ReadFull(s, totpLenBuf[:]); err != nil {
		slog.Warn("unseal: failed to read TOTP length", "peer", short, "err", err)
		writeUnsealResponse(s, unsealStatusErr, "protocol error")
		return
	}

	var totpCode string
	if totpLenBuf[0] > 0 {
		if totpLenBuf[0] > maxTOTPLen {
			writeUnsealResponse(s, unsealStatusErr, "invalid TOTP length")
			return
		}
		totpBuf := make([]byte, totpLenBuf[0])
		if _, err := io.ReadFull(s, totpBuf); err != nil {
			slog.Warn("unseal: failed to read TOTP code", "peer", short, "err", err)
			writeUnsealResponse(s, unsealStatusErr, "protocol error")
			return
		}
		totpCode = string(totpBuf)
	}

	// Attempt unseal.
	if err := h.Vault.Unseal(passphrase, totpCode); err != nil {
		switch {
		case errors.Is(err, vault.ErrInvalidPassphrase):
			h.recordFailure(remotePeer)
			failures := h.getFailures(remotePeer)
			slog.Warn("unseal: invalid passphrase", "peer", short, "failures", failures)
			h.recordMetric("failure")
			writeUnsealResponse(s, unsealStatusErr, h.failureMessage("invalid passphrase", failures))
		case errors.Is(err, vault.ErrInvalidTOTP):
			h.recordFailure(remotePeer)
			failures := h.getFailures(remotePeer)
			slog.Warn("unseal: invalid TOTP", "peer", short, "failures", failures)
			h.recordMetric("failure")
			writeUnsealResponse(s, unsealStatusErr, h.failureMessage("invalid TOTP code", failures))
		case errors.Is(err, vault.ErrVaultAlreadyUnsealed):
			h.recordMetric("success")
			writeUnsealResponse(s, unsealStatusOK, "already unsealed")
		default:
			slog.Error("unseal: unexpected error", "peer", short, "err", err)
			h.recordMetric("error")
			writeUnsealResponse(s, unsealStatusErr, "internal error")
		}
		return
	}

	// Success: reset lockout for this peer.
	h.resetLockout(remotePeer)
	h.recordMetric("success")
	if h.Metrics != nil {
		h.Metrics.VaultSealOpsTotal.WithLabelValues("unseal_remote").Inc()
		h.Metrics.VaultSealed.Set(0)
	}
	slog.Info("vault unsealed remotely", "peer", short)
	writeUnsealResponse(s, unsealStatusOK, "unsealed")
}

// isLockedOut checks whether the peer is currently in a lockout period.
// Returns true and the remaining duration if locked out.
// A permanently blocked peer returns remaining = -1.
func (h *UnsealHandler) isLockedOut(p peer.ID) (bool, time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()

	lo, exists := h.lockouts[p]
	if !exists {
		return false, 0
	}

	if lo.blocked {
		return true, -1
	}

	remaining := time.Until(lo.lockedUntil)
	if remaining <= 0 {
		return false, 0
	}
	return true, remaining
}

// recordFailure increments the failure count and sets the lockout timer.
// After exhausting all lockout levels, the peer is permanently blocked.
func (h *UnsealHandler) recordFailure(p peer.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()

	lo, exists := h.lockouts[p]
	if !exists {
		lo = &peerLockout{}
		h.lockouts[p] = lo
	}

	lo.failures++

	// First unsealFreeAttempts failures: no lockout (typo grace period).
	if lo.failures <= unsealFreeAttempts {
		return
	}

	// Escalating lockout after free attempts are exhausted.
	idx := lo.failures - unsealFreeAttempts - 1

	// Past the end of the schedule: permanently blocked.
	if idx >= len(unsealLockoutSchedule) {
		lo.blocked = true
		h.incLockedGauge()
		slog.Warn("unseal: peer permanently blocked after exhausting all attempts", "peer", p.String()[:16]+"...")
		return
	}

	// First lockout entry for this peer: increment gauge.
	if idx == 0 {
		h.incLockedGauge()
	}
	lo.lockedUntil = time.Now().Add(unsealLockoutSchedule[idx])
}

// getFailures returns the current failure count for a peer.
func (h *UnsealHandler) getFailures(p peer.ID) int {
	h.mu.Lock()
	defer h.mu.Unlock()

	if lo, exists := h.lockouts[p]; exists {
		return lo.failures
	}
	return 0
}

// resetLockout clears failure tracking for a peer (called on successful unseal).
func (h *UnsealHandler) resetLockout(p peer.ID) {
	h.mu.Lock()
	defer h.mu.Unlock()

	if lo, exists := h.lockouts[p]; exists && (lo.blocked || !lo.lockedUntil.IsZero()) {
		h.decLockedGauge()
	}
	delete(h.lockouts, p)
}

// recordMetric increments the vault unseal counter. Nil-safe.
func (h *UnsealHandler) recordMetric(result string) {
	if h.Metrics != nil {
		h.Metrics.VaultUnsealTotal.WithLabelValues(result).Inc()
	}
}

// incLockedGauge increments the locked-peers gauge. Nil-safe.
func (h *UnsealHandler) incLockedGauge() {
	if h.Metrics != nil {
		h.Metrics.VaultUnsealLockedPeers.Inc()
	}
}

// decLockedGauge decrements the locked-peers gauge. Nil-safe.
func (h *UnsealHandler) decLockedGauge() {
	if h.Metrics != nil {
		h.Metrics.VaultUnsealLockedPeers.Dec()
	}
}

// failureMessage builds the response message, appending lockout info if applicable.
func (h *UnsealHandler) failureMessage(reason string, failures int) string {
	if failures <= unsealFreeAttempts {
		remaining := unsealFreeAttempts - failures
		if remaining == 1 {
			return fmt.Sprintf("%s (%d attempt remaining before lockout)", reason, remaining)
		}
		return fmt.Sprintf("%s (%d attempts remaining before lockout)", reason, remaining)
	}

	idx := failures - unsealFreeAttempts - 1

	// Past the schedule: permanently blocked.
	if idx >= len(unsealLockoutSchedule) {
		return fmt.Sprintf("%s (permanently blocked: unseal via SSH on the relay server)", reason)
	}

	return fmt.Sprintf("%s (locked for %s)", reason, formatLockoutRemaining(unsealLockoutSchedule[idx]))
}

// formatLockoutRemaining formats a duration for human display.
func formatLockoutRemaining(d time.Duration) string {
	switch {
	case d >= time.Hour:
		h := int(d.Hours())
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	case d >= time.Minute:
		m := int(d.Minutes())
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	default:
		s := int(d.Seconds())
		if s == 1 {
			return "1 second"
		}
		return fmt.Sprintf("%d seconds", s)
	}
}

func writeUnsealResponse(s network.Stream, status byte, msg string) {
	if len(msg) > 255 {
		msg = msg[:255]
	}
	buf := make([]byte, 2+len(msg))
	buf[0] = status
	buf[1] = byte(len(msg))
	copy(buf[2:], msg)
	s.Write(buf)
}

// EncodeUnsealRequest builds the wire-format request for the unseal protocol.
// Used by the client side (CLI) to construct the stream payload.
func EncodeUnsealRequest(passphrase, totpCode string) []byte {
	passBytes := []byte(passphrase)
	totpBytes := []byte(totpCode)

	// [1 version] [2 BE pass-len] [N pass] [1 TOTP-len] [M TOTP]
	buf := make([]byte, 1+2+len(passBytes)+1+len(totpBytes))
	buf[0] = unsealWireVersion
	binary.BigEndian.PutUint16(buf[1:3], uint16(len(passBytes)))
	copy(buf[3:3+len(passBytes)], passBytes)
	buf[3+len(passBytes)] = byte(len(totpBytes))
	copy(buf[4+len(passBytes):], totpBytes)
	return buf
}

// ReadUnsealResponse reads a response from the unseal protocol stream.
// Returns the status (0x01 = OK, 0x00 = error) and the message.
func ReadUnsealResponse(r io.Reader) (bool, string, error) {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return false, "", err
	}
	status := header[0]
	msgLen := header[1]

	if msgLen > 0 {
		msgBuf := make([]byte, msgLen)
		if _, err := io.ReadFull(r, msgBuf); err != nil {
			return false, "", err
		}
		return status == unsealStatusOK, string(msgBuf), nil
	}

	return status == unsealStatusOK, "", nil
}
