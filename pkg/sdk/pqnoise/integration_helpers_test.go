package pqnoise_test

import (
	"context"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	"github.com/libp2p/go-libp2p/p2p/transport/tcp"
	ma "github.com/multiformats/go-multiaddr"
)

// tcpTransport is the TCP transport constructor for integration tests.
var tcpTransport = tcp.NewTCPTransport

// mockConnForGater satisfies network.Conn for testing InterceptUpgraded.
type mockConnForGater struct {
	security  string
	transport string // "quic-v1", "tcp", etc.
	peerID    peer.ID
}

func (m *mockConnForGater) ConnState() network.ConnectionState {
	return network.ConnectionState{
		Security:  protocol.ID(m.security),
		Transport: m.transport,
	}
}

func (m *mockConnForGater) RemotePeer() peer.ID         { return m.peerID }
func (m *mockConnForGater) LocalPeer() peer.ID          { return "" }
func (m *mockConnForGater) RemotePublicKey() crypto.PubKey { return nil }
func (m *mockConnForGater) RemoteMultiaddr() ma.Multiaddr { return nil }
func (m *mockConnForGater) LocalMultiaddr() ma.Multiaddr  { return nil }
func (m *mockConnForGater) ID() string                    { return "mock-conn" }
func (m *mockConnForGater) Stat() network.ConnStats       { return network.ConnStats{} }
func (m *mockConnForGater) Scope() network.ConnScope      { return nil }
func (m *mockConnForGater) Close() error                  { return nil }
func (m *mockConnForGater) CloseWithError(_ network.ConnErrorCode) error { return nil }
func (m *mockConnForGater) NewStream(_ context.Context) (network.Stream, error) { return nil, nil }
func (m *mockConnForGater) GetStreams() []network.Stream  { return nil }
func (m *mockConnForGater) IsClosed() bool                { return false }
func (m *mockConnForGater) As(_ any) bool                 { return false }
