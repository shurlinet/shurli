package filetransfer

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/shurlinet/shurli/pkg/sdk"
)

const (
	// CancelProtocol is the libp2p protocol ID for multi-path cancel.
	// Registered via host.SetStreamHandler, NOT via plugin service registry (R2-C4).
	CancelProtocol = "/shurli/transfer-cancel/1.0.0"

	// cancelStreamDeadline prevents slowloris: cancel stream must deliver
	// 32 bytes within this window or be dropped (R2-S5).
	cancelStreamDeadline = 5 * time.Second

	// cancelRateMax bounds cancel messages per peer per minute
	// to prevent lock-contention DoS (R2-S1).
	cancelRateMax = 10
)

// RegisterCancelHandler registers the multi-path cancel protocol handler on the host.
// Called from plugin.Start(). Must be paired with UnregisterCancelHandler in plugin.Stop() (R3-C3).
func RegisterCancelHandler(h host.Host, ts *TransferService) {
	h.SetStreamHandler(protocol.ID(CancelProtocol), func(s network.Stream) {
		defer s.Close()

		remotePeer := s.Conn().RemotePeer()
		short := remotePeer.String()[:16] + "..."

		// Rate limit before any map lookup (R2-S1).
		if ts.cancelRateLimiter != nil && !ts.cancelRateLimiter.allow(remotePeer.String()) {
			slog.Debug("transfer-cancel: rate limited", "peer", short)
			return
		}

		// Read exactly 32 bytes (transferID) with deadline (R2-S5, R2-I7).
		s.SetDeadline(time.Now().Add(cancelStreamDeadline))
		var transferID [32]byte
		if _, err := io.ReadFull(s, transferID[:]); err != nil {
			slog.Debug("transfer-cancel: read failed", "peer", short, "error", err)
			return
		}

		// Truncate transferID in logs (R3-S2).
		idShort := fmt.Sprintf("%x", transferID[:8])

		// Check sender side: am I sending to this peer and they want to cancel? (R3-C2)
		ts.activeSendsMu.RLock()
		sendEntry, hasSend := ts.activeSends[transferID]
		ts.activeSendsMu.RUnlock()

		if hasSend {
			// Verify the cancel comes from the peer we're actually sending to.
			// Without this, an attacker who knows a transferID could cancel
			// someone else's transfer (audit Round 2 security gap fix).
			if sendEntry.remotePeer != remotePeer {
				slog.Warn("transfer-cancel: sender peer mismatch",
					"peer", short, "id", idShort,
					"expected", sendEntry.remotePeer.String()[:16])
				return
			}
			slog.Debug("transfer-cancel: cancelling send", "peer", short, "id", idShort)
			// Fire cancel in background goroutine to avoid blocking handler (R2-I6).
			go sendEntry.cancel()
			return
		}

		// Check receiver side: am I receiving from this peer and they want to cancel? (R3-C2)
		ts.mu.RLock()
		session, hasSession := ts.parallelSessions[transferID]
		ts.mu.RUnlock()

		if hasSession && session.controlPID == remotePeer {
			slog.Debug("transfer-cancel: cancelling receive", "peer", short, "id", idShort)
			// Fire cancel in background goroutine (R2-I6).
			// Uses closeDone() to prevent double-close race with receiveParallel (TS-4 audit fix).
			go func() {
				session.closeDone()
				session.resetWorkerStreams()
			}()
			return
		}

		// Neither sender nor receiver — transfer already completed or unknown ID.
		// Identical timing as valid cancel (R2-S2: no information leak).
		slog.Debug("transfer-cancel: no matching transfer", "peer", short, "id", idShort)
	})
}

// UnregisterCancelHandler removes the cancel protocol handler from the host.
// Called from plugin.Stop() to prevent handler access after TransferService is closed (R3-C3).
func UnregisterCancelHandler(h host.Host) {
	h.RemoveStreamHandler(protocol.ID(CancelProtocol))
}

// sendMultiPathCancel sends a cancel message to a peer on ALL available connections
// (up to maxHedgeFanOut). Fire-and-forget — does not wait for delivery confirmation.
// Uses raw conn.NewStream + SelectProtoOrFail (not OpenPluginStreamOnConn, because
// the cancel protocol is not a plugin service — R4-I6).
//
// Called from CancelTransfer in a background goroutine (R2-I8).
func sendMultiPathCancel(h host.Host, peerID peer.ID, transferID [32]byte) {
	conns := h.Network().ConnsToPeer(peerID)
	if len(conns) == 0 {
		slog.Debug("transfer-cancel: no connections for multi-path cancel",
			"peer", peerID.String()[:16])
		return
	}

	// Cap fan-out (R2 F9). For cancel, we use ALL connections (not ConnGroups
	// filtering) because cancel needs maximum delivery probability (R3-I6).
	if len(conns) > sdk.MaxHedgeFanOut {
		conns = conns[:sdk.MaxHedgeFanOut]
	}

	idShort := fmt.Sprintf("%x", transferID[:8])
	slog.Debug("transfer-cancel: sending multi-path cancel",
		"peer", peerID.String()[:16], "id", idShort, "paths", len(conns))

	var wg sync.WaitGroup
	for _, conn := range conns {
		wg.Add(1)
		go func(c network.Conn) {
			defer wg.Done()

			ctx, ctxCancel := context.WithTimeout(context.Background(), cancelStreamDeadline)
			defer ctxCancel()

			s, err := sdk.OpenStreamOnConn(ctx, c, protocol.ID(CancelProtocol))
			if err != nil {
				// Expected when remote doesn't support cancel protocol (old node).
				// Not an error — fall back to existing s.Reset() behavior.
				return
			}
			defer s.Close()

			s.SetDeadline(time.Now().Add(cancelStreamDeadline))
			if _, err := s.Write(transferID[:]); err != nil {
				return
			}
		}(conn)
	}

	// Wait for all sends to complete (they're bounded by cancelStreamDeadline).
	wg.Wait()
}

