package sdk

import (
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

// RelayGrantChecker provides relay grant information for transfer decisions.
// Implemented by grants.GrantCache via structural typing (no import needed).
type RelayGrantChecker interface {
	// GrantStatus returns grant info for a relay. ok=false if no cached/valid grant.
	GrantStatus(relayID peer.ID) (remaining time.Duration, budget int64, sessionDuration time.Duration, ok bool)
	// HasSufficientBudget checks if the session budget can handle fileSize.
	HasSufficientBudget(relayID peer.ID, fileSize int64, direction string) bool
	// TrackCircuitBytes increments the byte counter for a relay circuit.
	TrackCircuitBytes(relayID peer.ID, direction string, n int64)
	// ResetCircuitCounters resets per-circuit byte counters (new circuit).
	ResetCircuitCounters(relayID peer.ID)
}

// relayTransferInfo holds pre-transfer grant check results.
type relayTransferInfo struct {
	IsRelayed       bool
	RelayPeerID     peer.ID
	GrantActive     bool
	GrantRemaining  time.Duration
	SessionBudget   int64 // remaining bytes in current session
	SessionDuration time.Duration
	BudgetOK        bool // budget >= file size
	TimeOK          bool // grant remaining >= estimated transfer time
}

// Smart reconnection constants.
const (
	relayReconnectInitialDelay = 2 * time.Second
	relayReconnectMaxDelay     = 32 * time.Second
	relayReconnectMaxAttempts  = 5
)

// relayPeerFromStream extracts the relay peer ID from a relayed connection.
// Returns empty peer.ID if the stream is not relayed.
func relayPeerFromStream(s network.Stream) peer.ID {
	if !s.Conn().Stat().Limited {
		return ""
	}
	return relayPeerFromAddr(s.Conn().RemoteMultiaddr())
}

// relayPeerFromAddr extracts the relay peer ID from a circuit relay multiaddr.
// Returns empty peer.ID if the address is not a circuit relay address.
// Used by hasAnyActiveRelayGrant to check connections and peerstore addresses.
func relayPeerFromAddr(addr ma.Multiaddr) peer.ID {
	var lastP2P peer.ID
	foundCircuit := false
	ma.ForEach(addr, func(c ma.Component) bool {
		switch c.Protocol().Code {
		case ma.P_P2P:
			if !foundCircuit {
				pid, err := peer.Decode(c.Value())
				if err == nil {
					lastP2P = pid
				}
			}
		case ma.P_CIRCUIT:
			foundCircuit = true
		}
		return true
	})
	if !foundCircuit {
		return ""
	}
	return lastP2P
}

// RelayPeerFromAddrStr extracts the relay peer ID string from a circuit relay
// multiaddr string. Returns empty string if the address is not a relay circuit.
func RelayPeerFromAddrStr(addrStr string) string {
	maddr, err := ma.NewMultiaddr(addrStr)
	if err != nil {
		return ""
	}
	pid := relayPeerFromAddr(maddr)
	if pid == "" {
		return ""
	}
	return pid.String()
}

// checkRelayGrant performs pre-transfer grant checks for a relayed connection.
// Returns transfer info with grant status and logs user-facing messages.
func (ts *TransferService) checkRelayGrant(s network.Stream, fileSize int64, direction string) relayTransferInfo {
	info := relayTransferInfo{}

	if ts.grantChecker == nil {
		return info
	}

	relayID := relayPeerFromStream(s)
	if relayID == "" {
		return info
	}

	info.IsRelayed = true
	info.RelayPeerID = relayID

	remaining, budget, sessionDur, ok := ts.grantChecker.GrantStatus(relayID)
	if !ok {
		slog.Warn("relay-grant: no active grant for relay, transfer may fail",
			"relay", shortPeerStr(relayID))
		return info
	}

	info.GrantActive = true
	info.GrantRemaining = remaining
	info.SessionBudget = budget
	info.SessionDuration = sessionDur
	info.BudgetOK = ts.grantChecker.HasSufficientBudget(relayID, fileSize, direction)
	info.TimeOK = true

	// Estimate transfer time at a conservative 200 KB/s for relay path.
	// Use seconds arithmetic to avoid time.Duration overflow on large files.
	// Computed once, reused for both the decision and the log message.
	const relaySpeedEstimate = 200 * 1024 // bytes per second
	var estimatedSeconds int64
	if fileSize > 0 {
		estimatedSeconds = fileSize / relaySpeedEstimate
		if estimatedSeconds < 1 {
			estimatedSeconds = 1
		}
		if remaining != time.Duration(math.MaxInt64) && remaining.Seconds() < float64(estimatedSeconds) {
			info.TimeOK = false
		}
		// Also check session duration (H11): a single session may be shorter
		// than the grant. Transfer must fit within one session, not just the grant.
		if sessionDur > 0 && sessionDur.Seconds() < float64(estimatedSeconds) {
			info.TimeOK = false
		}
	}

	// Log grant status (user-facing).
	budgetStr := "unlimited"
	if budget < math.MaxInt64 {
		budgetStr = FormatBytes(budget)
	}
	remainStr := "permanent"
	if remaining != time.Duration(math.MaxInt64) {
		remainStr = remaining.Truncate(time.Second).String()
	}
	sessionStr := "unlimited"
	if sessionDur > 0 {
		sessionStr = sessionDur.Truncate(time.Second).String()
	}
	estimateStr := "n/a"
	if estimatedSeconds > 0 {
		estimateStr = fmt.Sprintf("~%s at ~200KB/s", (time.Duration(estimatedSeconds) * time.Second).Truncate(time.Second))
	}

	if info.BudgetOK && info.TimeOK {
		slog.Info("relay-grant: proceeding",
			"relay", shortPeerStr(relayID),
			"grant_remaining", remainStr,
			"session_budget", budgetStr,
			"file_size", FormatBytes(fileSize),
			"estimate", estimateStr)
	} else {
		slog.Info("relay-grant: transfer check",
			"relay", shortPeerStr(relayID),
			"grant_remaining", remainStr,
			"session_budget", budgetStr,
			"session_duration", sessionStr,
			"file_size", FormatBytes(fileSize),
			"budget_ok", info.BudgetOK,
			"time_ok", info.TimeOK)
	}

	if !info.BudgetOK {
		slog.Warn("relay-grant: insufficient session budget, will establish new circuit",
			"relay", shortPeerStr(relayID),
			"need", FormatBytes(fileSize),
			"have", budgetStr)
	}

	if !info.TimeOK {
		slog.Warn("relay-grant: insufficient time remaining",
			"relay", shortPeerStr(relayID),
			"grant_remaining", remainStr,
			"session_duration", sessionStr,
			"estimated_transfer", estimateStr)
	}

	return info
}

// isRelaySessionExpiry checks if a failed relayed transfer should be retried
// due to session expiry (H11: session expired but grant still active).
// Returns false for application-level errors (rejection, file errors) that
// won't be fixed by establishing a new circuit.
func (ts *TransferService) isRelaySessionExpiry(relayID peer.ID, transferErr error) bool {
	if ts.grantChecker == nil || relayID == "" || transferErr == nil {
		return false
	}

	// Exclude application-level errors that a reconnect can't fix.
	errMsg := transferErr.Error()
	for _, pattern := range []string{
		"rejected",        // peer rejected the transfer
		"file too large",  // size limit
		"disk space",      // receiver full
		"open file",       // local file error
		"stat file",       // local file error
		"chunk file",      // local file error
		"cancelled",       // user or context cancelled
		"grant expires",   // we already decided not to transfer
		"access denied",   // auth failure
	} {
		if strings.Contains(errMsg, pattern) {
			return false
		}
	}

	_, _, _, ok := ts.grantChecker.GrantStatus(relayID)
	if !ok {
		return false // grant expired or revoked
	}
	// Grant is still active + error is transport-level. Likely a session expiry.
	return true
}

// relayReconnectDelay returns the backoff delay for a relay reconnection attempt.
func relayReconnectDelay(attempt int) time.Duration {
	delay := relayReconnectInitialDelay
	for i := 0; i < attempt; i++ {
		delay *= 2
		if delay > relayReconnectMaxDelay {
			delay = relayReconnectMaxDelay
			break
		}
	}
	return delay
}

// makeChunkTracker creates a callback for per-chunk byte tracking (H7).
// Returns nil if the stream is not relayed or no grant checker is configured.
func (ts *TransferService) makeChunkTracker(s network.Stream, direction string) func(bytesOnWire int64) {
	if ts.grantChecker == nil {
		return nil
	}
	relayID := relayPeerFromStream(s)
	if relayID == "" {
		return nil
	}
	return func(bytesOnWire int64) {
		ts.grantChecker.TrackCircuitBytes(relayID, direction, bytesOnWire)
	}
}

// closeRelayConns closes all relay (limited) connections to a peer routed
// through a specific relay. This forces PathDialer to establish a new connection
// on the next openStream, picking a relay based on current budget ranking.
func (ts *TransferService) closeRelayConns(relayID peer.ID, targetPeerIDStr string) {
	if ts.connsToPeer == nil || relayID == "" {
		return
	}
	targetPeerID, err := peer.Decode(targetPeerIDStr)
	if err != nil {
		slog.Warn("relay-grant: closeRelayConns failed to decode peer ID", "error", err)
		return
	}
	conns := ts.connsToPeer(targetPeerID)
	for _, conn := range conns {
		if !conn.Stat().Limited {
			continue // keep direct connections
		}
		connRelay := relayPeerFromAddr(conn.RemoteMultiaddr())
		if connRelay == relayID {
			conn.Close()
			slog.Info("relay-grant: closed relay connection for budget switch",
				"relay", shortPeerStr(relayID),
				"peer", shortPeerStr(targetPeerID))
		}
	}
}

// shortPeerStr returns a truncated peer ID string for logging.
func shortPeerStr(pid peer.ID) string {
	s := pid.String()
	if len(s) > 16 {
		return s[:16] + "..."
	}
	return s
}

// FormatBytes formats a byte count for user-facing display (e.g. "1.2 GB", "500 MB").
func FormatBytes(b int64) string {
	if b >= 1<<30 {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	}
	if b >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	}
	if b >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", b)
}
