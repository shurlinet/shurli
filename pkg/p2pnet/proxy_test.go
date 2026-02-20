package p2pnet

import (
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

// memConn implements HalfCloseConn for simple tests (not BidirectionalProxy).
type memConn struct {
	r         io.Reader
	w         io.WriteCloser
	closeOnce sync.Once
	closedCh  chan struct{}
}

func (c *memConn) Read(p []byte) (int, error)  { return c.r.Read(p) }
func (c *memConn) Write(p []byte) (int, error) { return c.w.Write(p) }
func (c *memConn) Close() error {
	c.closeOnce.Do(func() { close(c.closedCh) })
	c.w.Close()
	return nil
}
func (c *memConn) CloseWrite() error { return c.w.Close() }

// --- BidirectionalProxy ---

// tcpConnPair creates two connected TCP connections for testing.
func tcpConnPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var srvConn net.Conn
	accepted := make(chan struct{})
	go func() {
		srvConn, _ = ln.Accept()
		close(accepted)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	<-accepted
	return client, srvConn
}

func TestBidirectionalProxy(t *testing.T) {
	// Create two TCP connection pairs: left and right
	leftClient, leftServer := tcpConnPair(t)
	rightClient, rightServer := tcpConnPair(t)

	// BidirectionalProxy connects leftServer ↔ rightClient
	done := make(chan struct{})
	go func() {
		BidirectionalProxy(
			&tcpHalfCloser{leftServer},
			&tcpHalfCloser{rightClient},
			"test",
		)
		close(done)
	}()

	// Write through: leftClient → leftServer → (proxy) → rightClient → rightServer
	msg := "hello proxy"
	go func() {
		leftClient.Write([]byte(msg))
		leftClient.(*net.TCPConn).CloseWrite()
	}()

	buf := make([]byte, 64)
	n, err := rightServer.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read from right: %v", err)
	}
	if string(buf[:n]) != msg {
		t.Errorf("got %q, want %q", string(buf[:n]), msg)
	}

	// Close the other direction to let proxy finish
	rightServer.Close()
	leftClient.Close()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("BidirectionalProxy did not return within timeout")
	}
}

// --- tcpHalfCloser ---

func TestTcpHalfCloser_CloseWrite(t *testing.T) {
	// Create a real TCP connection for testing CloseWrite
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	var srvConn net.Conn
	accepted := make(chan struct{})
	go func() {
		var err error
		srvConn, err = ln.Accept()
		if err != nil {
			return
		}
		close(accepted)
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	<-accepted
	defer srvConn.Close()

	// Wrap in tcpHalfCloser
	hc := &tcpHalfCloser{Conn: client}
	if err := hc.CloseWrite(); err != nil {
		t.Errorf("CloseWrite: %v", err)
	}

	// After CloseWrite, server should see EOF on read
	buf := make([]byte, 1)
	_, err = srvConn.Read(buf)
	if err != io.EOF {
		t.Errorf("expected EOF after CloseWrite, got: %v", err)
	}
}

func TestTcpHalfCloser_NonTCP(t *testing.T) {
	// net.Pipe returns non-TCP connections  - CloseWrite should return nil
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()

	hc := &tcpHalfCloser{Conn: a}
	if err := hc.CloseWrite(); err != nil {
		t.Errorf("CloseWrite on non-TCP should return nil, got: %v", err)
	}
}

// --- NewTCPListener, Addr, Close ---

func TestNewTCPListener(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		dial := func() (ServiceConn, error) {
			return nil, errors.New("not implemented")
		}
		l, err := NewTCPListener("127.0.0.1:0", dial)
		if err != nil {
			t.Fatalf("NewTCPListener: %v", err)
		}
		defer l.Close()

		addr := l.Addr()
		if addr == nil {
			t.Fatal("Addr() returned nil")
		}
		if !strings.HasPrefix(addr.String(), "127.0.0.1:") {
			t.Errorf("unexpected addr: %s", addr)
		}
	})

	t.Run("invalid addr", func(t *testing.T) {
		dial := func() (ServiceConn, error) { return nil, nil }
		_, err := NewTCPListener("not-a-valid-address-999999", dial)
		if err == nil {
			t.Error("expected error for invalid address")
		}
	})
}

func TestTCPListenerClose(t *testing.T) {
	dial := func() (ServiceConn, error) { return nil, errors.New("no") }
	l, err := NewTCPListener("127.0.0.1:0", dial)
	if err != nil {
		t.Fatalf("NewTCPListener: %v", err)
	}
	if err := l.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// --- TCPListener.Serve + handleConnection ---

func TestTCPListenerServe(t *testing.T) {
	// Create a pipe pair: one end will be the "service" connection
	svcRead, svcWrite := io.Pipe()

	dialCalled := make(chan struct{}, 1)
	dial := func() (ServiceConn, error) {
		dialCalled <- struct{}{}
		return &memConn{r: svcRead, w: svcWrite, closedCh: make(chan struct{})}, nil
	}

	l, err := NewTCPListener("127.0.0.1:0", dial)
	if err != nil {
		t.Fatalf("NewTCPListener: %v", err)
	}

	// Start serving in background
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- l.Serve()
	}()

	// Connect to the listener
	conn, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}

	// Wait for dial to be called
	select {
	case <-dialCalled:
	case <-time.After(5 * time.Second):
		t.Fatal("dialFunc was not called")
	}

	conn.Close()
	svcRead.Close()
	svcWrite.Close()

	// Close listener to stop Serve()
	l.Close()

	select {
	case err := <-serveErr:
		// Serve returns when listener is closed  - error is expected
		if err == nil {
			t.Error("expected error from Serve after Close")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after Close")
	}
}

func TestTCPListenerHandleConnection_DialError(t *testing.T) {
	// When dialFunc fails, handleConnection should close the TCP connection
	dial := func() (ServiceConn, error) {
		return nil, errors.New("service unreachable")
	}

	l, err := NewTCPListener("127.0.0.1:0", dial)
	if err != nil {
		t.Fatalf("NewTCPListener: %v", err)
	}

	go l.Serve()
	defer l.Close()

	// Connect  - handleConnection will be called, dialFunc fails, connection closed
	conn, err := net.DialTimeout("tcp", l.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// The connection should be closed by the handler after dial failure
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 1)
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("expected read to fail (connection should be closed)")
	}
}

// --- DialWithRetry ---

func TestDialWithRetry_FirstAttemptSucceeds(t *testing.T) {
	calls := 0
	dial := func() (ServiceConn, error) {
		calls++
		return &memConn{closedCh: make(chan struct{})}, nil
	}

	retried := DialWithRetry(dial, 3)
	conn, err := retried()
	if err != nil {
		t.Fatalf("DialWithRetry: %v", err)
	}
	if conn == nil {
		t.Fatal("expected non-nil conn")
	}
	if calls != 1 {
		t.Errorf("expected 1 call, got %d", calls)
	}
}

func TestDialWithRetry_AllFail(t *testing.T) {
	calls := 0
	dial := func() (ServiceConn, error) {
		calls++
		return nil, errors.New("refused")
	}

	// Use 0 retries to test the "no retry" path quickly
	retried := DialWithRetry(dial, 0)
	_, err := retried()
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Errorf("expected 1 call (0 retries), got %d", calls)
	}
	if !strings.Contains(err.Error(), "all 1 attempts failed") {
		t.Errorf("unexpected error: %v", err)
	}
}
