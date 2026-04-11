package sdk

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/core/transport"
)

// managedConn wraps a transport.CapableConn (established via Transport.Dial)
// into a network.Conn. These connections are NOT registered with the swarm.
// They are owned and tracked by PathProtector for backup relay paths.
//
// Design ref: R4-F1, R6-C1, R6-I2, R7-I1, V9.
type managedConn struct {
	transport.CapableConn
	relayPeerID peer.ID // relay server this circuit goes through

	// Resource manager for stream scope allocation (R6-C1).
	resourceManager network.ResourceManager

	// Bandwidth tracking for managed conn traffic (R4-F4).
	// nil when BandwidthTracker is disabled. Wraps stream Read/Write.
	bwTracker *BandwidthTracker

	// Stream tracking for GetStreams() and safety reaper (R6-I2).
	mu      sync.Mutex
	streams map[*managedStream]struct{}

	// Unique connection ID.
	id string

	// Stream ID counter.
	nextID atomic.Uint64

	// closed tracks whether this conn has been closed.
	closed atomic.Bool

	// created is when this managed conn was established.
	created time.Time

	// onStreamError is called when NewStream fails (R4-F10).
	// Used by PathProtector to detect dead managed conns reactively.
	onStreamError func()
}

// newManagedConn wraps a transport.CapableConn into a managed network.Conn.
func newManagedConn(cc transport.CapableConn, relayPeerID peer.ID, rm network.ResourceManager, bw *BandwidthTracker, id string) *managedConn {
	return &managedConn{
		CapableConn:     cc,
		relayPeerID:     relayPeerID,
		resourceManager: rm,
		bwTracker:       bw,
		streams:         make(map[*managedStream]struct{}),
		id:              id,
		created:         time.Now(),
	}
}

// --- network.Conn interface implementation ---

// ID returns a unique identifier for this managed connection.
func (mc *managedConn) ID() string {
	return mc.id
}

// NewStream opens a new stream on the managed connection.
// Skips AllowLimitedConn check (R7-I1): we created this conn intentionally
// as a relay backup. The opt-in happened at PathProtector level.
// Allocates stream scope from ResourceManager (R6-C1).
func (mc *managedConn) NewStream(ctx context.Context) (network.Stream, error) {
	if mc.closed.Load() {
		return nil, fmt.Errorf("managed conn closed")
	}

	// Allocate stream scope from ResourceManager (R6-C1).
	// Match swarm's pattern: swarm_conn.go:224-227.
	var scope network.StreamManagementScope
	if mc.resourceManager != nil {
		var err error
		scope, err = mc.resourceManager.OpenStream(mc.RemotePeer(), network.DirOutbound)
		if err != nil {
			return nil, fmt.Errorf("resource manager: %w", err)
		}
	}

	// Open raw muxed stream on the underlying CapableConn.
	raw, err := mc.CapableConn.OpenStream(ctx)
	if err != nil {
		if scope != nil {
			scope.Done()
		}
		// R4-F10: notify PathProtector of dead managed conn reactively.
		if mc.onStreamError != nil {
			mc.onStreamError()
		}
		return nil, err
	}

	ms := &managedStream{
		MuxedStream: raw,
		conn:        mc,
		scope:       scope,
		streamID:    fmt.Sprintf("managed-%d", mc.nextID.Add(1)),
		created:     time.Now(),
	}

	mc.mu.Lock()
	mc.streams[ms] = struct{}{}
	mc.mu.Unlock()

	return ms, nil
}

// GetStreams returns all open streams on this managed connection.
func (mc *managedConn) GetStreams() []network.Stream {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	result := make([]network.Stream, 0, len(mc.streams))
	for s := range mc.streams {
		result = append(result, s)
	}
	return result
}

// Stat returns connection statistics. Limited=true for relay connections.
// Opened set to creation time of the managed conn.
func (mc *managedConn) Stat() network.ConnStats {
	return network.ConnStats{
		Stats: network.Stats{
			Direction: network.DirOutbound,
			Opened:    mc.created,
			Limited:   true, // relay connection is always limited
		},
		NumStreams: mc.streamCount(),
	}
}

func (mc *managedConn) streamCount() int {
	mc.mu.Lock()
	defer mc.mu.Unlock()
	return len(mc.streams)
}

// Close closes the managed connection and all its streams.
func (mc *managedConn) Close() error {
	if mc.closed.Swap(true) {
		return nil // already closed
	}
	// Close all tracked streams first.
	mc.mu.Lock()
	for s := range mc.streams {
		s.resetNoRemove()
	}
	mc.streams = make(map[*managedStream]struct{})
	mc.mu.Unlock()

	return mc.CapableConn.Close()
}

// CloseWithError closes the connection with an error code.
func (mc *managedConn) CloseWithError(errCode network.ConnErrorCode) error {
	return mc.Close()
}

// IsClosed returns whether the connection is closed.
func (mc *managedConn) IsClosed() bool {
	return mc.closed.Load()
}

// As returns false; managed conns don't wrap other conn types.
func (mc *managedConn) As(target any) bool {
	return false
}

// removeStream removes a stream from tracking (called by managedStream on close/reset).
func (mc *managedConn) removeStream(ms *managedStream) {
	mc.mu.Lock()
	delete(mc.streams, ms)
	mc.mu.Unlock()
}

// Compile-time check: managedConn implements network.Conn.
var _ network.Conn = (*managedConn)(nil)

// managedStream wraps a network.MuxedStream into a network.Stream.
// Tracks its parent managedConn for Conn() and stream lifecycle.
//
// Design ref: R4-F1, R6-I1, R6-I3, V16.
type managedStream struct {
	network.MuxedStream
	conn     *managedConn
	scope    network.StreamManagementScope // nil when resource manager disabled
	streamID string
	proto    protocol.ID
	created  time.Time
	closed   atomic.Bool
}

// ID returns a unique stream identifier.
func (ms *managedStream) ID() string {
	return ms.streamID
}

// Protocol returns the negotiated protocol ID.
func (ms *managedStream) Protocol() protocol.ID {
	return ms.proto
}

// SetProtocol sets the protocol ID (called during multistream-select).
func (ms *managedStream) SetProtocol(id protocol.ID) error {
	ms.proto = id
	return nil
}

// Stat returns stream statistics.
func (ms *managedStream) Stat() network.Stats {
	return network.Stats{
		Direction: network.DirOutbound,
		Opened:    ms.created,
		Limited:   true, // relay stream
	}
}

// Conn returns the parent managed connection as network.Conn.
func (ms *managedStream) Conn() network.Conn {
	return ms.conn
}

// Scope returns the stream's resource scope.
// Plugin code never calls Scope() (V16), but we return NullScope for safety.
// The actual StreamManagementScope is held internally for Done() cleanup (R6-I3).
func (ms *managedStream) Scope() network.StreamScope {
	return &network.NullScope{}
}

// Read wraps the underlying stream Read with bandwidth tracking (R4-F4).
func (ms *managedStream) Read(p []byte) (int, error) {
	n, err := ms.MuxedStream.Read(p)
	if n > 0 && ms.conn.bwTracker != nil {
		ms.conn.bwTracker.Counter().LogRecvMessage(int64(n))
		ms.conn.bwTracker.Counter().LogRecvMessageStream(int64(n), ms.proto, ms.conn.RemotePeer())
	}
	return n, err
}

// Write wraps the underlying stream Write with bandwidth tracking (R4-F4).
func (ms *managedStream) Write(p []byte) (int, error) {
	n, err := ms.MuxedStream.Write(p)
	if n > 0 && ms.conn.bwTracker != nil {
		ms.conn.bwTracker.Counter().LogSentMessage(int64(n))
		ms.conn.bwTracker.Counter().LogSentMessageStream(int64(n), ms.proto, ms.conn.RemotePeer())
	}
	return n, err
}

// Close closes the stream and releases resources (R6-I3).
func (ms *managedStream) Close() error {
	if ms.closed.Swap(true) {
		return nil
	}
	err := ms.MuxedStream.Close()
	ms.cleanup()
	return err
}

// Reset resets the stream and releases resources.
func (ms *managedStream) Reset() error {
	if ms.closed.Swap(true) {
		return nil
	}
	err := ms.MuxedStream.Reset()
	ms.cleanup()
	return err
}

// ResetWithError resets with an error code.
func (ms *managedStream) ResetWithError(errCode network.StreamErrorCode) error {
	if ms.closed.Swap(true) {
		return nil
	}
	err := ms.MuxedStream.ResetWithError(errCode)
	ms.cleanup()
	return err
}

// As returns false; managed streams don't wrap other stream types.
func (ms *managedStream) As(target any) bool {
	return false
}

// cleanup releases the stream scope and removes from parent tracking.
func (ms *managedStream) cleanup() {
	if ms.scope != nil {
		ms.scope.Done()
	}
	ms.conn.removeStream(ms)
}

// resetNoRemove resets the stream without removing from parent tracking.
// Used during managedConn.Close() which clears the map itself.
func (ms *managedStream) resetNoRemove() {
	if ms.closed.Swap(true) {
		return
	}
	ms.MuxedStream.Reset()
	if ms.scope != nil {
		ms.scope.Done()
	}
}

// Compile-time check: managedStream implements network.Stream.
var _ network.Stream = (*managedStream)(nil)

