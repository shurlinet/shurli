package sdk

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// MaxHedgeFanOut is the maximum number of independent connection groups
	// to race across. Bounds goroutine + circuit reservation consumption.
	// Peers typically have 1-3 connections (direct + 1-2 relays).
	// Exported for use by the cancel protocol (sendMultiPathCancel).
	MaxHedgeFanOut = 3

	// hedgeStagger is the delay between launching hedged stream opens.
	// Prevents thundering herd on the receiving peer.
	hedgeStagger = 50 * time.Millisecond
)

// hedgeResult carries the outcome of one hedged stream open attempt.
type hedgeResult struct {
	stream    network.Stream
	groupType string
	err       error
}

// HedgedOpenStream opens a stream to a peer, racing across independent connection
// groups when multiple paths exist. First successfully-negotiated stream wins,
// loser streams are Reset(). Zero overhead when only one connection group exists.
//
// This is the TS-4 "always-on control signal hedging" primitive. It transparently
// hedges browse, download initiation, and other independent request-response
// operations across direct and relay paths.
//
// Security: uses OpenPluginStreamOnConn which runs the full security pipeline
// (policy check, transport check, grant header). Never bypasses security.
func HedgedOpenStream(ctx context.Context, n *Network, peerID peer.ID, serviceName string) (network.Stream, error) {
	groups := AllConnGroups(n.host, peerID, n.pathProtector)
	if len(groups) == 0 {
		return nil, fmt.Errorf("no connections to peer %s", peerID.String()[:16])
	}

	// Fast path: single group — no hedging, no goroutines, no channels (R2 F12).
	if len(groups) == 1 {
		conn := groups[0].Conns[0]
		slog.Info("hedged-stream: fast path (single group)",
			"peer", peerID.String()[:16],
			"service", serviceName,
			"group", groups[0].Type,
			"limited", conn.Stat().Limited,
			"local", conn.LocalMultiaddr(),
			"remote", conn.RemoteMultiaddr())
		s, err := n.OpenPluginStreamOnConn(ctx, peerID, serviceName, conn)
		if err != nil {
			return nil, err
		}
		return s, nil
	}

	// Multiple groups — race with staggered starts (Tail Slayer pattern).
	// Sort: direct first, then relay groups. Direct gets zero stagger (fires
	// immediately), relays are delayed. This ensures direct always wins when
	// the LAN path is healthy, regardless of map iteration order or warm
	// relay circuits.
	sort.Slice(groups, func(i, j int) bool {
		iDirect := groups[i].Type == "direct"
		jDirect := groups[j].Type == "direct"
		if iDirect != jDirect {
			return iDirect // direct before relay
		}
		return false // preserve relative order among relays
	})

	// Cap fan-out to prevent resource exhaustion (R2 F9).
	if len(groups) > MaxHedgeFanOut {
		groups = groups[:MaxHedgeFanOut]
	}

	groupTypes := make([]string, len(groups))
	for i, g := range groups {
		groupTypes[i] = g.Type
	}
	slog.Info("hedged-stream: racing",
		"peer", peerID.String()[:16],
		"service", serviceName,
		"groups", groupTypes)

	hedgeCtx, hedgeCancel := context.WithCancel(ctx)
	defer hedgeCancel()

	// Buffered channel: every goroutine sends exactly one result (TS-1 lesson R2-1).
	// Buffer capacity = len(groups) so goroutines never block.
	results := make(chan hedgeResult, len(groups))

	for i, g := range groups {
		go func(idx int, group ConnGroup) {
			// Staggered start: first fires immediately, rest wait idx*50ms.
			// Prevents thundering herd on receiving peer.
			if idx > 0 {
				stagger := time.NewTimer(time.Duration(idx) * hedgeStagger)
				select {
				case <-hedgeCtx.Done():
					if !stagger.Stop() {
						<-stagger.C // drain channel to prevent timer goroutine leak
					}
					results <- hedgeResult{err: fmt.Errorf("group %q: cancelled during stagger", group.Type)}
					return
				case <-stagger.C:
				}
			}

			// Pick first connection from the group (all conns in a group share
			// the same failure domain, so picking more than one is pointless).
			conn := group.Conns[0]
			s, err := n.OpenPluginStreamOnConn(hedgeCtx, peerID, serviceName, conn)
			if err != nil {
				results <- hedgeResult{groupType: group.Type, err: err}
				return
			}
			results <- hedgeResult{stream: s, groupType: group.Type}
		}(i, g)
	}

	// First-success-wins collector (R3-E5, TS-1 pattern).
	// Returns immediately on first success. Losers clean up in background.
	var firstErr error
	consumed := 0
	for range groups {
		r := <-results
		consumed++
		if r.err == nil {
			hedgeCancel() // cancel losing goroutines

			slog.Info("hedged-stream: winner",
				"peer", peerID.String()[:16],
				"service", serviceName,
				"group", r.groupType,
				"groups_total", len(groups),
				"local", r.stream.Conn().LocalMultiaddr(),
				"remote", r.stream.Conn().RemoteMultiaddr())

			// Background cleanup: drain remaining results and Reset loser streams.
			// remaining = total goroutines - consumed (including the winner).
			// Buffered channel ensures remaining goroutines can always send.
			go func(remaining int) {
				for i := 0; i < remaining; i++ {
					loser := <-results
					if loser.stream != nil {
						loser.stream.Reset()
						slog.Debug("hedged-stream: loser reset",
							"group", loser.groupType)
					}
				}
			}(len(groups) - consumed)

			return r.stream, nil
		}
		if firstErr == nil {
			firstErr = r.err
		}
	}

	// All groups failed.
	return nil, fmt.Errorf("all %d connection groups failed: %v", len(groups), firstErr)
}
