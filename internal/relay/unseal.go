package relay

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/auth"
	"github.com/shurlinet/shurli/internal/vault"
	"github.com/shurlinet/shurli/pkg/sdk"
)

// UnsealProtocol is the libp2p protocol ID for remote vault unseal.
const UnsealProtocol = "/shurli/relay-unseal/1.0.0"

// Wire format (challenge-response with replay protection):
//
//   RELAY -> CLIENT (challenge):
//     [16 nonce]         random challenge nonce
//
//   CLIENT -> RELAY (request):
//     [1 version=0x02]   wire version
//     [16 nonce-echo]    must match the challenge nonce
//     [2 BE pass-len] [N passphrase] [1 TOTP-len] [M TOTP]
//
//   RELAY -> CLIENT (response):
//     [1 status] [1 msg-len] [N message]
//
// Status codes:
//   0x01 = success (unsealed)
//   0x00 = error (message follows)

const (
	unsealWireVersion byte = 0x02
	unsealStatusOK    byte = 0x01
	unsealStatusErr   byte = 0x00
	unsealNonceLen         = 16
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

// persistedLockout is the JSON-serializable form of peerLockout.
type persistedLockout struct {
	Failures    int       `json:"failures"`
	LockedUntil time.Time `json:"locked_until,omitempty"`
	Blocked     bool      `json:"blocked,omitempty"`
}

// lockoutState is the top-level JSON structure persisted to disk.
type lockoutState struct {
	Peers map[string]persistedLockout `json:"peers"`
}

// UnsealHandler handles remote unseal requests from admin peers.
type UnsealHandler struct {
	Vault        *vault.Vault
	AuthKeysPath string
	StateFile    string          // path to lockout state file (empty = no persistence)
	Metrics      *sdk.Metrics // nil-safe: metrics are optional

	mu       sync.Mutex
	lockouts map[peer.ID]*peerLockout
}

// NewUnsealHandler creates a handler for the remote unseal protocol.
// stateFile is the path to persist lockout state (empty string disables persistence).
func NewUnsealHandler(v *vault.Vault, authKeysPath, stateFile string) *UnsealHandler {
	h := &UnsealHandler{
		Vault:        v,
		AuthKeysPath: authKeysPath,
		StateFile:    stateFile,
		lockouts:     make(map[peer.ID]*peerLockout),
	}
	h.loadState()
	return h
}

// loadState restores lockout state from disk. Errors are logged but not fatal.
func (h *UnsealHandler) loadState() {
	if h.StateFile == "" {
		return
	}
	data, err := os.ReadFile(h.StateFile)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("unseal: failed to load lockout state", "file", h.StateFile, "err", err)
		}
		return
	}

	var state lockoutState
	if err := json.Unmarshal(data, &state); err != nil {
		slog.Warn("unseal: failed to parse lockout state", "file", h.StateFile, "err", err)
		return
	}

	for peerStr, pl := range state.Peers {
		pid, err := peer.Decode(peerStr)
		if err != nil {
			slog.Warn("unseal: skipping invalid peer ID in state", "peer", peerStr)
			continue
		}
		h.lockouts[pid] = &peerLockout{
			failures:    pl.Failures,
			lockedUntil: pl.LockedUntil,
			blocked:     pl.Blocked,
		}
	}
	slog.Info("unseal: restored lockout state", "peers", len(state.Peers))
}

// saveState persists lockout state to disk atomically. Must be called with mu held.
func (h *UnsealHandler) saveState() {
	if h.StateFile == "" {
		return
	}

	state := lockoutState{Peers: make(map[string]persistedLockout, len(h.lockouts))}
	for pid, lo := range h.lockouts {
		state.Peers[pid.String()] = persistedLockout{
			Failures:    lo.failures,
			LockedUntil: lo.lockedUntil,
			Blocked:     lo.blocked,
		}
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		slog.Error("unseal: failed to marshal lockout state", "err", err)
		return
	}

	// Atomic write: temp file + rename.
	tmp := h.StateFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		slog.Error("unseal: failed to write lockout state", "file", tmp, "err", err)
		return
	}
	if err := os.Rename(tmp, h.StateFile); err != nil {
		slog.Error("unseal: failed to rename lockout state", "err", err)
		os.Remove(tmp)
	}
}

// LockoutStateFile returns the conventional lockout state file path for a relay config dir.
func LockoutStateFile(configDir string) string {
	return filepath.Join(configDir, ".unseal-lockout.json")
}

// HandleStream processes an incoming unseal stream.
// The relay sends a 16-byte challenge nonce first, then reads the client request.
// v2 clients echo the nonce back (replay protection). v1 clients skip it (legacy).
func (h *UnsealHandler) HandleStream(s network.Stream) {
	defer s.Close()
	remotePeer := s.Conn().RemotePeer()
	short := remotePeer.String()[:16] + "..."

	// Generate and send challenge nonce FIRST (16 bytes).
	// The nonce must be sent before any error responses so the client's
	// ReadUnsealChallenge/ReadUnsealResponse protocol stays in sync.
	// Pre-nonce rejection (admin check, lockout) caused the client to
	// read error bytes as a nonce, then get EOF on the response read,
	// producing "failed to read unseal response: unexpected EOF".
	var nonce [unsealNonceLen]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		slog.Error("unseal: failed to generate nonce", "err", err)
		writeUnsealResponse(s, unsealStatusErr, "internal error")
		return
	}
	if _, err := s.Write(nonce[:]); err != nil {
		slog.Warn("unseal: failed to send nonce", "peer", short, "err", err)
		return
	}

	// Only admin peers can unseal.
	if !auth.IsAdmin(h.AuthKeysPath, remotePeer) {
		slog.Warn("unseal: rejected non-admin peer", "peer", short)
		h.recordMetric("denied")
		// Client will read the nonce, send a request (which we ignore),
		// then read this response with the human-readable error.
		drainUnsealRequest(s)
		writeUnsealResponse(s, unsealStatusErr, "permission denied: admin role required")
		return
	}

	// Check lockout (iOS-style escalating backoff).
	if locked, remaining := h.isLockedOut(remotePeer); locked {
		drainUnsealRequest(s)
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

	// Read and verify nonce echo (replay protection).
	var nonceEcho [unsealNonceLen]byte
	if _, err := io.ReadFull(s, nonceEcho[:]); err != nil {
		slog.Warn("unseal: failed to read nonce echo", "peer", short, "err", err)
		writeUnsealResponse(s, unsealStatusErr, "protocol error")
		return
	}
	if subtle.ConstantTimeCompare(nonce[:], nonceEcho[:]) != 1 {
		slog.Warn("unseal: nonce mismatch (replay?)", "peer", short)
		h.recordMetric("replay")
		writeUnsealResponse(s, unsealStatusErr, "nonce mismatch")
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

	// Attempt unseal. Remote unseal does not support Yubikey (requires
	// physical key on the relay server). Pass nil for yubikey response.
	if err := h.Vault.Unseal(passphrase, totpCode, nil); err != nil {
		switch {
		case errors.Is(err, vault.ErrInvalidPassword):
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
		case errors.Is(err, vault.ErrInvalidYubikey):
			h.recordFailure(remotePeer)
			slog.Warn("unseal: yubikey required (remote unseal not supported)", "peer", short)
			h.recordMetric("failure")
			writeUnsealResponse(s, unsealStatusErr, "yubikey required: unseal locally via admin socket")
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
		h.saveState()
		return
	}

	// Escalating lockout after free attempts are exhausted.
	idx := lo.failures - unsealFreeAttempts - 1

	// Past the end of the schedule: permanently blocked.
	if idx >= len(unsealLockoutSchedule) {
		lo.blocked = true
		h.incLockedGauge()
		slog.Warn("unseal: peer permanently blocked after exhausting all attempts", "peer", p.String()[:16]+"...")
		h.saveState()
		return
	}

	// First lockout entry for this peer: increment gauge.
	if idx == 0 {
		h.incLockedGauge()
	}
	lo.lockedUntil = time.Now().Add(unsealLockoutSchedule[idx])
	h.saveState()
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
	h.saveState()
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

// drainUnsealRequest reads and discards the client's unseal request after
// the nonce has been sent. This keeps the protocol in sync so the client
// reads the error response at the correct point (after sending its request).
func drainUnsealRequest(s network.Stream) {
	s.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 512)
	for {
		_, err := s.Read(buf)
		if err != nil {
			break
		}
	}
	s.SetReadDeadline(time.Time{}) // clear deadline for response write
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

// ReadUnsealChallenge reads the 16-byte challenge nonce from the relay.
// Must be called before sending the unseal request.
func ReadUnsealChallenge(r io.Reader) ([]byte, error) {
	nonce := make([]byte, unsealNonceLen)
	if _, err := io.ReadFull(r, nonce); err != nil {
		return nil, fmt.Errorf("reading unseal challenge: %w", err)
	}
	return nonce, nil
}

// EncodeUnsealRequest builds the v2 wire-format request with challenge nonce echo.
// The nonce is the 16-byte challenge received from ReadUnsealChallenge.
func EncodeUnsealRequest(nonce []byte, passphrase, totpCode string) []byte {
	passBytes := []byte(passphrase)
	totpBytes := []byte(totpCode)

	// [1 version] [16 nonce-echo] [2 BE pass-len] [N pass] [1 TOTP-len] [M TOTP]
	buf := make([]byte, 1+unsealNonceLen+2+len(passBytes)+1+len(totpBytes))
	buf[0] = unsealWireVersion
	copy(buf[1:1+unsealNonceLen], nonce)
	off := 1 + unsealNonceLen
	binary.BigEndian.PutUint16(buf[off:off+2], uint16(len(passBytes)))
	copy(buf[off+2:off+2+len(passBytes)], passBytes)
	buf[off+2+len(passBytes)] = byte(len(totpBytes))
	copy(buf[off+3+len(passBytes):], totpBytes)
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
