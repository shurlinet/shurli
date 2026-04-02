package relay

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"

	"github.com/libp2p/go-libp2p/core/connmgr"
	"github.com/libp2p/go-libp2p/core/event"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/peerstore"
	"github.com/libp2p/go-libp2p/core/protocol"
	proto "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/proto"
	ma "github.com/multiformats/go-multiaddr"
)

// LimitingHost wraps a host.Host to intercept relay HOP/STOP streams and apply
// per-peer data budgets via limitedStream wrappers. This is the key insight from
// Incident #20: enforcement at a different architectural layer than where the
// problem manifests, using zero fork changes.
//
// The relay calls h.SetStreamHandler(ProtoIDv2Hop, ...) and h.NewStream(ctx, dest, ProtoIDv2Stop).
// LimitingHost intercepts both:
//   - SetStreamHandler for HOP: wraps the handler so incoming HOP streams get a
//     limitedStream before the relay touches them.
//   - NewStream for STOP: wraps the returned stream with dest peer's data limit.
//
// Other protocols (grant-receipt, admin, MOTD, ZKP, etc.) pass through unwrapped (C2/C3).
type LimitingHost struct {
	host    host.Host
	tracker *BudgetTracker
	acl     *CircuitACL
}

// NewLimitingHost creates a host wrapper that enforces per-peer data budgets
// on relay circuit streams. Pass the returned host to relayv2.New() instead
// of the raw host.
func NewLimitingHost(h host.Host, tracker *BudgetTracker, acl *CircuitACL) *LimitingHost {
	return &LimitingHost{
		host:    h,
		tracker: tracker,
		acl:     acl,
	}
}

// --- host.Host interface implementation ---
// All methods delegate to the inner host except SetStreamHandler,
// SetStreamHandlerMatch, and NewStream.

func (lh *LimitingHost) ID() peer.ID                        { return lh.host.ID() }
func (lh *LimitingHost) Peerstore() peerstore.Peerstore     { return lh.host.Peerstore() }
func (lh *LimitingHost) Addrs() []ma.Multiaddr              { return lh.host.Addrs() }
func (lh *LimitingHost) Network() network.Network            { return lh.host.Network() }
func (lh *LimitingHost) Mux() protocol.Switch                { return lh.host.Mux() }
func (lh *LimitingHost) Connect(ctx context.Context, pi peer.AddrInfo) error {
	return lh.host.Connect(ctx, pi)
}
func (lh *LimitingHost) RemoveStreamHandler(pid protocol.ID) { lh.host.RemoveStreamHandler(pid) }
func (lh *LimitingHost) Close() error                        { return lh.host.Close() }
func (lh *LimitingHost) ConnManager() connmgr.ConnManager    { return lh.host.ConnManager() }
func (lh *LimitingHost) EventBus() event.Bus                 { return lh.host.EventBus() }

// SetStreamHandler intercepts the HOP protocol handler (C3).
// For HOP streams, the original handler is wrapped to inject a limitedStream
// before the relay processes the circuit. All other protocols pass through.
func (lh *LimitingHost) SetStreamHandler(pid protocol.ID, handler network.StreamHandler) {
	if pid == protocol.ID(proto.ProtoIDv2Hop) {
		lh.host.SetStreamHandler(pid, lh.wrapHopHandler(handler))
		return
	}
	lh.host.SetStreamHandler(pid, handler)
}

// SetStreamHandlerMatch intercepts HOP protocol handler registration (MF5 defense-in-depth).
func (lh *LimitingHost) SetStreamHandlerMatch(pid protocol.ID, match func(protocol.ID) bool, handler network.StreamHandler) {
	if pid == protocol.ID(proto.ProtoIDv2Hop) {
		lh.host.SetStreamHandlerMatch(pid, match, lh.wrapHopHandler(handler))
		return
	}
	lh.host.SetStreamHandlerMatch(pid, match, handler)
}

// NewStream intercepts STOP protocol streams (C2).
// When the relay opens a STOP stream to the destination peer, the returned
// stream is wrapped with the destination's data budget. All other protocols
// pass through unwrapped.
func (lh *LimitingHost) NewStream(ctx context.Context, p peer.ID, pids ...protocol.ID) (network.Stream, error) {
	s, err := lh.host.NewStream(ctx, p, pids...)
	if err != nil {
		return nil, err
	}

	// Only wrap STOP protocol streams (C2).
	if !isStopProtocol(pids) {
		return s, nil
	}

	// Admin peers bypass entirely (SEC4).
	if lh.acl != nil && lh.acl.IsAdmin(p) {
		return s, nil
	}

	return lh.wrapStreamForPeer(s, p), nil
}

// wrapHopHandler returns a stream handler that wraps the HOP stream
// with the source peer's data budget before passing to the relay.
func (lh *LimitingHost) wrapHopHandler(handler network.StreamHandler) network.StreamHandler {
	return func(s network.Stream) {
		p := s.Conn().RemotePeer()

		// Admin peers bypass entirely (SEC4).
		if lh.acl != nil && lh.acl.IsAdmin(p) {
			handler(s)
			return
		}

		handler(lh.wrapStreamForPeer(s, p))
	}
}

// wrapStreamForPeer creates a limitedStream for the given peer.
//
// Grant peers: cumulative budget from BudgetTracker (shared across circuits).
// Non-grant peers: per-circuit budget = relay's default session_data_limit (I10).
func (lh *LimitingHost) wrapStreamForPeer(s network.Stream, p peer.ID) network.Stream {
	if lh.tracker.HasBudget(p) {
		// Grant peer: use cumulative tracker.
		return newLimitedStreamCumulative(s, p, lh.tracker)
	}
	// Non-grant peer: per-circuit budget with local counter (I10).
	return newLimitedStreamLocal(s, lh.tracker.DefaultLimit())
}

// isStopProtocol checks if the requested protocols include the relay STOP protocol.
func isStopProtocol(pids []protocol.ID) bool {
	for _, pid := range pids {
		if pid == protocol.ID(proto.ProtoIDv2Stop) {
			return true
		}
	}
	return false
}

// --- limitedStream ---

// limitedStream wraps a network.Stream with a data budget.
// Read() and Write() decrement a budget counter (combined bidirectional, C8).
// When budget is exhausted: Read returns io.EOF, Write returns io.ErrClosedPipe (C7).
//
// Two modes:
//   - Cumulative (grant peers): counter in BudgetTracker, shared across circuits.
//   - Local (non-grant peers): counter is stream-local, fresh per circuit.
//
// Thread-safe: relay spawns 2 goroutines per circuit that read/write concurrently.
// Max overshoot: 2 * BufferSize (2 * 2048 = 4KB) per circuit (SEC3).
type limitedStream struct {
	network.Stream
	peerID    peer.ID        // set for cumulative mode
	tracker   *BudgetTracker // set for cumulative mode (nil for local)
	remaining *atomic.Int64  // set for local mode (nil for cumulative)

	// TE3-I1: log budget exhaustion exactly once per stream.
	exhaustOnce sync.Once
	budget      int64 // initial budget for logging
}

// newLimitedStreamCumulative creates a limitedStream that decrements the
// peer's cumulative budget in the BudgetTracker.
func newLimitedStreamCumulative(s network.Stream, p peer.ID, tracker *BudgetTracker) *limitedStream {
	return &limitedStream{
		Stream:  s,
		peerID:  p,
		tracker: tracker,
		budget:  tracker.RemainingBudget(p),
	}
}

// newLimitedStreamLocal creates a limitedStream with a fresh per-circuit budget.
// Used for non-grant authorized peers (I10).
func newLimitedStreamLocal(s network.Stream, budget int64) *limitedStream {
	r := &atomic.Int64{}
	r.Store(budget)
	return &limitedStream{
		Stream:    s,
		remaining: r,
		budget:    budget,
	}
}

func (ls *limitedStream) Read(p []byte) (int, error) {
	if ls.budgetRemaining() <= 0 {
		ls.logExhaustion()
		return 0, io.EOF // C7: relay expects EOF to clean up circuit
	}
	n, err := ls.Stream.Read(p)
	if n > 0 {
		ls.consumeBytes(int64(n))
	}
	return n, err
}

func (ls *limitedStream) Write(p []byte) (int, error) {
	if ls.budgetRemaining() <= 0 {
		ls.logExhaustion()
		return 0, io.ErrClosedPipe // C7: relay treats as write failure
	}
	n, err := ls.Stream.Write(p)
	if n > 0 {
		ls.consumeBytes(int64(n))
	}
	return n, err
}

// budgetRemaining returns the current remaining budget.
func (ls *limitedStream) budgetRemaining() int64 {
	if ls.tracker != nil {
		return ls.tracker.RemainingBudget(ls.peerID)
	}
	r := ls.remaining.Load()
	if r < 0 {
		return 0
	}
	return r
}

// consumeBytes decrements the appropriate counter.
func (ls *limitedStream) consumeBytes(n int64) {
	if ls.tracker != nil {
		ls.tracker.ConsumeBytes(ls.peerID, n)
		return
	}
	ls.remaining.Add(-n)
}

// logExhaustion logs budget exhaustion once per stream (TE3-I1).
func (ls *limitedStream) logExhaustion() {
	ls.exhaustOnce.Do(func() {
		short := ls.peerID.String()
		if short == "" {
			short = "non-grant"
		} else if len(short) > 16 {
			short = short[:16] + "..."
		}
		slog.Info("relay budget: exhausted",
			"peer", short,
			"initial_budget", ls.budget)
	})
}
