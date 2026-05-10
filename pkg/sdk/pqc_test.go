package sdk

import (
	"context"
	"testing"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"
)

// mockPQCConn is a minimal network.Conn for testing inspectConn and pqcFirstLog.
type mockPQCConn struct {
	security  protocol.ID
	transport string
	limited   bool
	remotePID peer.ID
	remoteMA  ma.Multiaddr
}

func (m *mockPQCConn) ConnState() network.ConnectionState {
	return network.ConnectionState{
		Security:  m.security,
		Transport: m.transport,
	}
}
func (m *mockPQCConn) Stat() network.ConnStats {
	s := network.ConnStats{}
	if m.limited {
		s.Limited = true
	}
	return s
}
func (m *mockPQCConn) RemotePeer() peer.ID          { return m.remotePID }
func (m *mockPQCConn) RemoteMultiaddr() ma.Multiaddr { return m.remoteMA }

// Remaining interface stubs (not used by inspectConn/LogIfPQ).
func (m *mockPQCConn) LocalPeer() peer.ID                                       { return "" }
func (m *mockPQCConn) RemotePublicKey() crypto.PubKey                           { return nil }
func (m *mockPQCConn) LocalMultiaddr() ma.Multiaddr                             { return nil }
func (m *mockPQCConn) ID() string                                               { return "" }
func (m *mockPQCConn) Scope() network.ConnScope                                 { return nil }
func (m *mockPQCConn) Close() error                                             { return nil }
func (m *mockPQCConn) CloseWithError(_ network.ConnErrorCode) error             { return nil }
func (m *mockPQCConn) NewStream(_ context.Context) (network.Stream, error)      { return nil, nil }
func (m *mockPQCConn) GetStreams() []network.Stream                             { return nil }
func (m *mockPQCConn) IsClosed() bool                                           { return false }
func (m *mockPQCConn) As(_ any) bool                                            { return false }

func TestInspectConn_PQNoise(t *testing.T) {
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	conn := &mockPQCConn{
		security:  "/pq-noise/1",
		transport: "tcp",
		remotePID: pid,
	}

	info := inspectConn(conn)

	if !info.PQ {
		t.Error("PQ Noise connection should report PQ=true")
	}
	if info.Security != "/pq-noise/1" {
		t.Errorf("Security: got %q, want %q", info.Security, "/pq-noise/1")
	}
	if info.Transport != "tcp" {
		t.Errorf("Transport: got %q, want %q", info.Transport, "tcp")
	}
}

func TestInspectConn_ClassicalNoise(t *testing.T) {
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	conn := &mockPQCConn{
		security:  "/noise",
		transport: "tcp",
		remotePID: pid,
	}

	info := inspectConn(conn)

	if info.PQ {
		t.Error("classical Noise should report PQ=false")
	}
	if info.Security != "/noise" {
		t.Errorf("Security: got %q, want %q", info.Security, "/noise")
	}
}

func TestInspectConn_Relay(t *testing.T) {
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	conn := &mockPQCConn{
		security:  "/pq-noise/1",
		transport: "tcp",
		limited:   true,
		remotePID: pid,
	}

	info := inspectConn(conn)

	if info.Transport != "relay" {
		t.Errorf("limited conn should be 'relay', got %q", info.Transport)
	}
	if !info.PQ {
		t.Error("PQ Noise relay should still report PQ=true")
	}
}

func TestPQCFirstLog_FiresOnPQNoise(t *testing.T) {
	// Create a fresh pqcFirstLog (not the package singleton).
	logger := &pqcFirstLog{}

	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	conn := &mockPQCConn{
		security:  "/pq-noise/1",
		transport: "tcp",
		remotePID: pid,
	}

	// Should not panic and should set done=true.
	logger.LogIfPQ(conn)

	if !logger.done.Load() {
		t.Error("pqcFirstLog.done should be true after PQ Noise connection")
	}

	// Second call should be fast-path (done=true).
	logger.LogIfPQ(conn)
}

func TestPQCFirstLog_DoesNotFireOnClassical(t *testing.T) {
	logger := &pqcFirstLog{}

	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	conn := &mockPQCConn{
		security:  "/noise",
		transport: "tcp",
		remotePID: pid,
	}

	logger.LogIfPQ(conn)

	if logger.done.Load() {
		t.Error("pqcFirstLog.done should be false for classical connection")
	}
}

func TestPQCFirstLog_NilSafe(t *testing.T) {
	logger := &pqcFirstLog{}
	logger.LogIfPQ(nil) // should not panic
}
