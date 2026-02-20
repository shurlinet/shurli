package p2pnet_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// newTestHost creates a minimal libp2p host for integration testing.
// Listens on a random localhost TCP port.
func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.NoSecurity,
		libp2p.DisableRelay(),
	)
	if err != nil {
		t.Fatalf("failed to create test host: %v", err)
	}
	t.Cleanup(func() { h.Close() })
	return h
}

// connectHosts connects host b to host a.
func connectHosts(t *testing.T, a, b host.Host) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := b.Connect(ctx, peer.AddrInfo{
		ID:    a.ID(),
		Addrs: a.Addrs(),
	})
	if err != nil {
		t.Fatalf("failed to connect hosts: %v", err)
	}
}

func TestTwoHostsStream(t *testing.T) {
	server := newTestHost(t)
	client := newTestHost(t)

	const testProtocol = protocol.ID("/test/echo/1.0.0")
	const testMessage = "hello peer-up"

	// Server: echo handler
	server.SetStreamHandler(testProtocol, func(s network.Stream) {
		defer s.Close()
		buf := make([]byte, 256)
		n, err := s.Read(buf)
		if err != nil && err != io.EOF {
			t.Errorf("server read error: %v", err)
			return
		}
		s.Write(buf[:n])
	})

	connectHosts(t, server, client)

	// Client: open stream and send message
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.NewStream(ctx, server.ID(), testProtocol)
	if err != nil {
		t.Fatalf("client NewStream error: %v", err)
	}
	defer stream.Close()

	_, err = stream.Write([]byte(testMessage))
	if err != nil {
		t.Fatalf("client write error: %v", err)
	}
	stream.CloseWrite()

	response, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("client read error: %v", err)
	}

	if string(response) != testMessage {
		t.Errorf("echo mismatch: got %q, want %q", string(response), testMessage)
	}
}

func TestTwoHostsHalfClose(t *testing.T) {
	server := newTestHost(t)
	client := newTestHost(t)

	const testProtocol = protocol.ID("/test/halfclose/1.0.0")

	// Server: read all, then write response, then close
	server.SetStreamHandler(testProtocol, func(s network.Stream) {
		data, err := io.ReadAll(s)
		if err != nil {
			t.Errorf("server ReadAll error: %v", err)
			s.Reset()
			return
		}
		// Reverse the data as response
		reversed := make([]byte, len(data))
		for i, b := range data {
			reversed[len(data)-1-i] = b
		}
		s.Write(reversed)
		s.Close()
	})

	connectHosts(t, server, client)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.NewStream(ctx, server.ID(), testProtocol)
	if err != nil {
		t.Fatalf("NewStream error: %v", err)
	}

	// Client: send data, half-close write, then read response
	_, err = stream.Write([]byte("abcdef"))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	stream.CloseWrite() // Signal: no more data from client

	response, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	stream.Close()

	if string(response) != "fedcba" {
		t.Errorf("half-close response: got %q, want %q", string(response), "fedcba")
	}
}

func TestDialWithRetry_Success(t *testing.T) {
	attempts := 0
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		attempts++
		if attempts < 3 {
			return nil, fmt.Errorf("transient failure %d", attempts)
		}
		// Return a mock conn on 3rd attempt
		return &mockServiceConn{}, nil
	}, 3)

	conn, err := dialFunc()
	if err != nil {
		t.Fatalf("DialWithRetry should succeed: %v", err)
	}
	if conn == nil {
		t.Fatal("DialWithRetry returned nil conn")
	}
	if attempts != 3 {
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
}

func TestDialWithRetry_AllFail(t *testing.T) {
	attempts := 0
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		attempts++
		return nil, fmt.Errorf("permanent failure")
	}, 2)

	_, err := dialFunc()
	if err == nil {
		t.Fatal("DialWithRetry should fail when all attempts fail")
	}
	if attempts != 3 { // 1 initial + 2 retries
		t.Errorf("expected 3 attempts, got %d", attempts)
	}
	if !strings.Contains(err.Error(), "all 3 attempts failed") {
		t.Errorf("error should mention attempt count: %v", err)
	}
}

func TestDialWithRetry_ImmediateSuccess(t *testing.T) {
	attempts := 0
	dialFunc := p2pnet.DialWithRetry(func() (p2pnet.ServiceConn, error) {
		attempts++
		return &mockServiceConn{}, nil
	}, 3)

	conn, err := dialFunc()
	if err != nil {
		t.Fatalf("DialWithRetry should succeed immediately: %v", err)
	}
	if conn == nil {
		t.Fatal("DialWithRetry returned nil conn")
	}
	if attempts != 1 {
		t.Errorf("expected 1 attempt on immediate success, got %d", attempts)
	}
}

func TestTCPListenerWithLocalService(t *testing.T) {
	// Start a mock local TCP service (echo server)
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to create echo listener: %v", err)
	}
	defer echoListener.Close()

	go func() {
		for {
			conn, err := echoListener.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				io.Copy(c, c)
			}(conn)
		}
	}()

	// Set up two libp2p hosts
	server := newTestHost(t)
	client := newTestHost(t)

	const svcProtocol = protocol.ID("/peerup/echo-test/1.0.0")

	// Server: proxy incoming streams to the local echo service
	server.SetStreamHandler(svcProtocol, func(s network.Stream) {
		defer s.Close()
		localConn, err := net.DialTimeout("tcp", echoListener.Addr().String(), 5*time.Second)
		if err != nil {
			s.Reset()
			return
		}
		defer localConn.Close()

		done := make(chan struct{})
		go func() {
			defer close(done)
			io.Copy(s, localConn)
			s.CloseWrite()
		}()
		io.Copy(localConn, s)
		if tc, ok := localConn.(*net.TCPConn); ok {
			tc.CloseWrite()
		}
		<-done
	})

	connectHosts(t, server, client)

	// Client: open stream to server's echo service
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream, err := client.NewStream(ctx, server.ID(), svcProtocol)
	if err != nil {
		t.Fatalf("NewStream error: %v", err)
	}

	testData := "hello through P2P to TCP echo"
	_, err = stream.Write([]byte(testData))
	if err != nil {
		t.Fatalf("write error: %v", err)
	}
	stream.CloseWrite()

	response, err := io.ReadAll(stream)
	if err != nil {
		t.Fatalf("read error: %v", err)
	}
	stream.Close()

	if string(response) != testData {
		t.Errorf("echo through P2P: got %q, want %q", string(response), testData)
	}
}

func TestUserAgentExchange(t *testing.T) {
	// Create two hosts with distinct UserAgent strings.
	// libp2p's Identify protocol exchanges UserAgent on connect.
	serverUA := "peerup/1.2.3"
	clientUA := "peerup/4.5.6"

	server, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.DisableRelay(),
		libp2p.UserAgent(serverUA),
	)
	if err != nil {
		t.Fatalf("failed to create server: %v", err)
	}
	t.Cleanup(func() { server.Close() })

	client, err := libp2p.New(
		libp2p.ListenAddrStrings("/ip4/127.0.0.1/tcp/0"),
		libp2p.DisableRelay(),
		libp2p.UserAgent(clientUA),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}
	t.Cleanup(func() { client.Close() })

	// Connect client -> server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Connect(ctx, peer.AddrInfo{ID: server.ID(), Addrs: server.Addrs()})
	if err != nil {
		t.Fatalf("connect error: %v", err)
	}

	// Identify runs asynchronously after connect  - wait briefly
	time.Sleep(500 * time.Millisecond)

	// Verify client sees server's UserAgent
	serverAgent, err := client.Peerstore().Get(server.ID(), "AgentVersion")
	if err != nil {
		t.Fatalf("failed to get server agent from client peerstore: %v", err)
	}
	if serverAgent != serverUA {
		t.Errorf("server UserAgent: got %q, want %q", serverAgent, serverUA)
	}

	// Verify server sees client's UserAgent
	clientAgent, err := server.Peerstore().Get(client.ID(), "AgentVersion")
	if err != nil {
		t.Fatalf("failed to get client agent from server peerstore: %v", err)
	}
	if clientAgent != clientUA {
		t.Errorf("client UserAgent: got %q, want %q", clientAgent, clientUA)
	}
}

// --- Ping tests ---

// registerPingHandler sets up the ping-pong stream handler on a host.
func registerPingHandler(t *testing.T, h host.Host, protoID string) {
	t.Helper()
	h.SetStreamHandler(protocol.ID(protoID), func(s network.Stream) {
		defer s.Close()
		buf := make([]byte, 64)
		n, _ := s.Read(buf)
		msg := strings.TrimSpace(string(buf[:n]))
		if msg == "ping" {
			s.Write([]byte("pong\n"))
		}
	})
}

func TestPingPeer_Connected(t *testing.T) {
	const pingProto = "/peerup/ping/1.0.0"

	server := newTestHost(t)
	client := newTestHost(t)
	registerPingHandler(t, server, pingProto)
	connectHosts(t, server, client)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := p2pnet.PingPeer(ctx, client, server.ID(), pingProto, 3, 100*time.Millisecond)

	var results []p2pnet.PingResult
	for r := range ch {
		results = append(results, r)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	stats := p2pnet.ComputePingStats(results)
	if stats.Received != 3 {
		t.Errorf("expected 3 received, got %d", stats.Received)
	}
	if stats.LossPct != 0 {
		t.Errorf("expected 0%% loss, got %.0f%%", stats.LossPct)
	}

	for i, r := range results {
		if r.Error != "" {
			t.Errorf("ping %d: unexpected error: %s", i+1, r.Error)
		}
		if r.Seq != i+1 {
			t.Errorf("ping %d: expected seq=%d, got seq=%d", i+1, i+1, r.Seq)
		}
		if r.RttMs <= 0 {
			t.Errorf("ping %d: RTT should be positive, got %.3f", i+1, r.RttMs)
		}
		if r.Path != "DIRECT" {
			t.Errorf("ping %d: expected DIRECT path, got %s", i+1, r.Path)
		}
	}
}

func TestPingPeer_NotConnected_Fails(t *testing.T) {
	// This test proves the bug: if peers aren't connected and the host
	// has no addresses for the target, PingPeer fails with "no addresses".
	// This is the scenario ConnectToPeer was added to fix.
	const pingProto = "/peerup/ping/1.0.0"

	server := newTestHost(t)
	client := newTestHost(t)
	registerPingHandler(t, server, pingProto)
	// Deliberately NOT connecting hosts

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := p2pnet.PingPeer(ctx, client, server.ID(), pingProto, 1, time.Second)

	result := <-ch
	if result.Error == "" {
		t.Fatal("expected error when pinging unconnected peer, got success")
	}
	if !strings.Contains(result.Error, "no addresses") {
		t.Errorf("expected 'no addresses' error, got: %s", result.Error)
	}
}

func TestPingPeer_AddressInPeerstore_AutoConnects(t *testing.T) {
	// This test proves the fix: if the peer's addresses are in the peerstore
	// (which ConnectToPeer ensures via DHT/relay), PingPeer succeeds even
	// without a pre-existing connection  - libp2p dials automatically.
	const pingProto = "/peerup/ping/1.0.0"

	server := newTestHost(t)
	client := newTestHost(t)
	registerPingHandler(t, server, pingProto)

	// NOT calling connectHosts  - instead, just add server's addresses
	// to client's peerstore (simulating what ConnectToPeer does)
	client.Peerstore().AddAddrs(server.ID(), server.Addrs(), time.Hour)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ch := p2pnet.PingPeer(ctx, client, server.ID(), pingProto, 2, 100*time.Millisecond)

	var results []p2pnet.PingResult
	for r := range ch {
		results = append(results, r)
	}

	stats := p2pnet.ComputePingStats(results)
	if stats.Received != 2 {
		t.Errorf("expected 2 received, got %d (lost: %d)", stats.Received, stats.Lost)
	}
	for i, r := range results {
		if r.Error != "" {
			t.Errorf("ping %d: %s", i+1, r.Error)
		}
	}
}

// mockServiceConn implements ServiceConn for testing DialWithRetry.
type mockServiceConn struct{}

func (m *mockServiceConn) Read(p []byte) (int, error)  { return 0, io.EOF }
func (m *mockServiceConn) Write(p []byte) (int, error)  { return len(p), nil }
func (m *mockServiceConn) Close() error                 { return nil }
func (m *mockServiceConn) CloseWrite() error             { return nil }
