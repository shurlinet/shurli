package sdk

import (
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/protocol"
)

// TestManagedConnInterfaceCompliance verifies managedConn satisfies network.Conn
// and managedStream satisfies network.Stream at compile time.
func TestManagedConnInterfaceCompliance(t *testing.T) {
	// Compile-time checks are already in managed_conn.go via:
	//   var _ network.Conn = (*managedConn)(nil)
	//   var _ network.Stream = (*managedStream)(nil)
	// This test documents the intent.
	t.Log("interface compliance verified at compile time")
}

// TestManagedConnStatLimited verifies Stat() reports Limited=true for relay conns.
func TestManagedConnStatLimited(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-1",
		created: time.Now(),
	}
	stat := mc.Stat()
	if !stat.Limited {
		t.Error("managedConn.Stat().Limited should be true")
	}
	if stat.Direction != network.DirOutbound {
		t.Error("managedConn.Stat().Direction should be DirOutbound")
	}
}

// TestManagedConnStreamTracking verifies streams are tracked and removed correctly.
func TestManagedConnStreamTracking(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-2",
		created: time.Now(),
	}

	// Create mock streams.
	ms1 := &managedStream{conn: mc, streamID: "s1", created: time.Now()}
	ms2 := &managedStream{conn: mc, streamID: "s2", created: time.Now()}

	mc.mu.Lock()
	mc.streams[ms1] = struct{}{}
	mc.streams[ms2] = struct{}{}
	mc.mu.Unlock()

	if mc.streamCount() != 2 {
		t.Errorf("expected 2 streams, got %d", mc.streamCount())
	}

	streams := mc.GetStreams()
	if len(streams) != 2 {
		t.Errorf("expected 2 streams from GetStreams, got %d", len(streams))
	}

	// Remove one stream.
	mc.removeStream(ms1)
	if mc.streamCount() != 1 {
		t.Errorf("expected 1 stream after remove, got %d", mc.streamCount())
	}

	mc.removeStream(ms2)
	if mc.streamCount() != 0 {
		t.Errorf("expected 0 streams after remove, got %d", mc.streamCount())
	}
}

// TestManagedStreamStat verifies stream Stat() returns correct values.
func TestManagedStreamStat(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-3",
		created: time.Now(),
	}
	ms := &managedStream{
		conn:     mc,
		streamID: "s1",
		created:  time.Now(),
	}

	stat := ms.Stat()
	if !stat.Limited {
		t.Error("managedStream.Stat().Limited should be true")
	}
	if stat.Direction != network.DirOutbound {
		t.Error("managedStream.Stat().Direction should be DirOutbound")
	}
}

// TestManagedStreamConn verifies Conn() returns the parent managedConn.
func TestManagedStreamConn(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-4",
		created: time.Now(),
	}
	ms := &managedStream{conn: mc, streamID: "s1", created: time.Now()}

	if ms.Conn() != mc {
		t.Error("managedStream.Conn() should return parent managedConn")
	}
}

// TestManagedStreamProtocol verifies SetProtocol/Protocol work.
func TestManagedStreamProtocol(t *testing.T) {
	ms := &managedStream{streamID: "s1", created: time.Now()}

	if ms.Protocol() != "" {
		t.Error("Protocol() should be empty initially")
	}

	proto := protocol.ID("/shurli/test/1.0.0")
	if err := ms.SetProtocol(proto); err != nil {
		t.Fatalf("SetProtocol: %v", err)
	}
	if ms.Protocol() != proto {
		t.Errorf("Protocol() = %q, want %q", ms.Protocol(), proto)
	}
}

// TestManagedConnAs verifies As() returns false.
func TestManagedConnAs(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-5",
		created: time.Now(),
	}
	var target *managedConn
	if mc.As(&target) {
		t.Error("As() should return false")
	}
}

// TestManagedConnIsClosed verifies IsClosed state transitions.
func TestManagedConnIsClosed(t *testing.T) {
	mc := &managedConn{
		streams: make(map[*managedStream]struct{}),
		id:      "test-6",
		created: time.Now(),
	}
	if mc.IsClosed() {
		t.Error("should not be closed initially")
	}
	mc.closed.Store(true)
	if !mc.IsClosed() {
		t.Error("should be closed after Store(true)")
	}
}

// TestManagedConnScope verifies Scope() returns non-nil NullScope.
func TestManagedStreamScope(t *testing.T) {
	ms := &managedStream{streamID: "s1", created: time.Now()}
	scope := ms.Scope()
	if scope == nil {
		t.Error("Scope() should return non-nil NullScope")
	}
}
