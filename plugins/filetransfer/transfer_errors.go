package filetransfer

import (
	"context"
	"errors"
	"strings"
)

// retryCategory classifies transfer errors for TS-5b failover decisions.
// Three categories keep TS-5b (network path failover) and existing relay-session
// retry (relay-specific recovery) cleanly separated (R5-F5).
type retryCategory int

const (
	// notRetryable: checkpoint and stop. User retries manually.
	// Default for unknown errors (R3-F11).
	notRetryable retryCategory = iota

	// retryableNetwork: network path died. TS-5b handles: open new stream
	// on backup path, resume via checkpoint.
	retryableNetwork

	// retryableRelay: relay-specific failure (budget exhausted, session expired).
	// Existing relay-session-expiry handler handles (reconnect with fresh circuit).
	retryableRelay
)

// classifyTransferError categorizes a transfer error for retry decisions.
// parentCtx is the daemon/plugin context — used to distinguish per-attempt
// timeout (retryable) from daemon shutdown (not retryable) per R4-F10.
//
// Every matched string is documented here so future libp2p upgrades can update.
func classifyTransferError(err error, parentCtx context.Context) retryCategory {
	if err == nil {
		return notRetryable
	}

	// User or daemon cancellation — never retry (F7, R2-F17).
	if errors.Is(err, context.Canceled) {
		return notRetryable
	}

	// context.DeadlineExceeded: could be per-attempt timeout (retryable) or
	// daemon shutdown (not retryable). Check parent context (R4-F10).
	if errors.Is(err, context.DeadlineExceeded) {
		if parentCtx != nil && parentCtx.Err() != nil {
			return notRetryable // daemon shutting down
		}
		return retryableNetwork // per-attempt timeout, try another path
	}

	msg := err.Error()

	// Strip "remote: " prefix for server-side errors (R4-F3).
	remoteSuffix := ""
	if strings.HasPrefix(msg, "remote: ") {
		remoteSuffix = strings.TrimPrefix(msg, "remote: ")
	}

	// --- NOT retryable: logic errors, auth failures, local resource issues ---

	// Merkle/hash mismatch — data corruption, retrying downloads same bad data (R2-F10).
	if strings.Contains(msg, "Merkle root mismatch") || strings.Contains(msg, "hash mismatch") {
		return notRetryable
	}

	// Permission/auth failures (F7).
	if strings.Contains(msg, "access denied") || strings.Contains(msg, "permission denied") ||
		remoteSuffix == "access denied" || remoteSuffix == "not found" {
		return notRetryable
	}
	if strings.Contains(msg, "grant expired") || strings.Contains(msg, "not authorized") {
		return notRetryable
	}

	// Peer rejection (non-busy).
	if strings.Contains(msg, "peer rejected") {
		return notRetryable
	}

	// Local resource exhaustion (F7).
	if strings.Contains(msg, "insufficient disk space") || strings.Contains(msg, "file too large") {
		return notRetryable
	}

	// Finalize errors — all data received, local issue (R3-F15).
	if strings.Contains(msg, "finalize:") {
		return notRetryable
	}

	// Cancelled (non-context variant).
	if msg == "cancelled" {
		return notRetryable
	}

	// File changed on sender — checkpoint invalid (F18).
	if strings.Contains(msg, "content changed") {
		return notRetryable
	}

	// --- Retryable (relay-specific) ---

	// Relay budget exhausted — need fresh circuit, not backup path (R5-F5).
	if strings.Contains(msg, "budget") && strings.Contains(msg, "relay") {
		return retryableRelay
	}
	if strings.Contains(msg, "session limit") || strings.Contains(msg, "session expired") {
		return retryableRelay
	}

	// --- Retryable (network) — TS-5b failover candidates ---

	// Stream/connection reset — primary path died.
	if strings.Contains(msg, "stream reset") || strings.Contains(msg, "connection reset") {
		return retryableNetwork
	}

	// I/O errors.
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "EOF") {
		return retryableNetwork
	}

	// Transfer incomplete — chunks missing, resume handles it (R4-F4).
	if strings.Contains(msg, "transfer incomplete") {
		return retryableNetwork
	}

	// Control stream read failure — network died mid-transfer.
	if strings.Contains(msg, "control read:") {
		return retryableNetwork
	}

	// Remote transient errors (R4-F3).
	if remoteSuffix == "busy" || remoteSuffix == "rate limit exceeded" ||
		remoteSuffix == "internal error" {
		return retryableNetwork
	}

	// Resource manager limit — transient (F7).
	if strings.Contains(msg, "resource limit") {
		return retryableNetwork
	}

	// Default: unknown error = NOT retryable (R3-F11).
	// Safer to checkpoint and let user retry than to loop on unrecoverable error.
	return notRetryable
}
