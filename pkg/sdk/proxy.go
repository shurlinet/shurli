package sdk

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"golang.org/x/net/netutil"
)

// HalfCloseConn is a connection that supports half-close (CloseWrite).
// Both ServiceConn (libp2p streams) and tcpHalfCloser (TCP connections) implement this.
type HalfCloseConn interface {
	io.ReadWriteCloser
	CloseWrite() error
}

// tcpHalfCloser adapts a net.Conn to support CloseWrite via type assertion.
type tcpHalfCloser struct{ net.Conn }

func (t *tcpHalfCloser) CloseWrite() error {
	if tc, ok := t.Conn.(*net.TCPConn); ok {
		return tc.CloseWrite()
	}
	return nil
}

// BidirectionalProxy copies data between two half-close-capable connections.
// It uses two goroutines for each direction, propagates half-close (CloseWrite)
// when one side finishes sending, and waits for both directions to complete.
// logPrefix identifies the connection in log messages (e.g., "ssh", "proxy").
func BidirectionalProxy(a, b HalfCloseConn, logPrefix string) {
	aDone := make(chan struct{})
	bDone := make(chan struct{})

	// a → b
	go func() {
		defer close(aDone)
		_, err := io.Copy(b, a)
		if err != nil && err != io.EOF {
			slog.Warn("copy error", "prefix", logPrefix, "direction", "a→b", "error", err)
		}
		b.CloseWrite()
	}()

	// b → a
	go func() {
		defer close(bDone)
		_, err := io.Copy(a, b)
		if err != nil && err != io.EOF {
			slog.Warn("copy error", "prefix", logPrefix, "direction", "b→a", "error", err)
		}
		a.CloseWrite()
	}()

	<-aDone
	<-bDone
	a.Close()
	b.Close()
}

// countingConn wraps a HalfCloseConn to count bytes transferred via Prometheus metrics.
type countingConn struct {
	HalfCloseConn
	metrics   *Metrics
	service   string
	direction string // "rx" or "tx"
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.HalfCloseConn.Read(p)
	if n > 0 {
		c.metrics.ProxyBytesTotal.WithLabelValues(c.direction, c.service).Add(float64(n))
	}
	return n, err
}

func (c *countingConn) Write(p []byte) (int, error) {
	n, err := c.HalfCloseConn.Write(p)
	if n > 0 {
		c.metrics.ProxyBytesTotal.WithLabelValues(c.direction, c.service).Add(float64(n))
	}
	return n, err
}

// InstrumentedBidirectionalProxy wraps BidirectionalProxy with metrics.
// When metrics is nil, it delegates directly to BidirectionalProxy.
func InstrumentedBidirectionalProxy(a, b HalfCloseConn, service string, metrics *Metrics) {
	if metrics == nil {
		BidirectionalProxy(a, b, service)
		return
	}

	metrics.ProxyConnectionsTotal.WithLabelValues(service).Inc()
	metrics.ProxyActiveConns.WithLabelValues(service).Inc()
	start := time.Now()

	defer func() {
		metrics.ProxyActiveConns.WithLabelValues(service).Dec()
		metrics.ProxyDurationSeconds.WithLabelValues(service).Observe(time.Since(start).Seconds())
	}()

	ca := &countingConn{HalfCloseConn: a, metrics: metrics, service: service, direction: "rx"}
	cb := &countingConn{HalfCloseConn: b, metrics: metrics, service: service, direction: "tx"}
	BidirectionalProxy(ca, cb, service)
}

// ProxyStreamToTCP creates a bidirectional proxy between a libp2p stream and a local TCP service.
func ProxyStreamToTCP(stream network.Stream, tcpAddr string) error {
	tcpConn, err := net.DialTimeout("tcp", tcpAddr, 10*time.Second)
	if err != nil {
		return err
	}
	BidirectionalProxy(&serviceStream{stream: stream}, &tcpHalfCloser{tcpConn}, "proxy")
	return nil
}

// DefaultMaxProxyConns is the per-proxy connection limit (SEC-5).
// Protects both local TCP stack and remote peer's stream limits.
const DefaultMaxProxyConns = 64

// TCPListener creates a local TCP listener that forwards connections to a P2P service.
// Tracks active connections for graceful shutdown (F9) and limits concurrent
// connections via netutil.LimitListener (SEC-5).
type TCPListener struct {
	listener net.Listener
	dialFunc func() (ServiceConn, error)

	mu    sync.Mutex
	conns map[net.Conn]struct{} // tracked for graceful shutdown
	wg    sync.WaitGroup        // counts active handleConnection goroutines
}

// NewTCPListener creates a new TCP listener for a P2P service.
// Binds to localAddr and wraps with a connection limiter (SEC-5).
func NewTCPListener(localAddr string, dialFunc func() (ServiceConn, error)) (*TCPListener, error) {
	raw, err := net.Listen("tcp", localAddr)
	if err != nil {
		return nil, err
	}

	return &TCPListener{
		listener: netutil.LimitListener(raw, DefaultMaxProxyConns),
		dialFunc: dialFunc,
		conns:    make(map[net.Conn]struct{}),
	}, nil
}

// Serve accepts connections and forwards them to the P2P service.
func (l *TCPListener) Serve() error {
	for {
		conn, err := l.listener.Accept()
		if err != nil {
			return err
		}

		// F13: Set explicit TCP keepalive for consistent dead-connection detection.
		if tc, ok := conn.(*net.TCPConn); ok {
			tc.SetKeepAlive(true)
			tc.SetKeepAlivePeriod(30 * time.Second)
		}

		l.wg.Add(1)
		go l.handleConnection(conn)
	}
}

// handleConnection handles a single TCP connection with tracking.
func (l *TCPListener) handleConnection(tcpConn net.Conn) {
	defer l.wg.Done()

	l.mu.Lock()
	l.conns[tcpConn] = struct{}{}
	l.mu.Unlock()

	defer func() {
		l.mu.Lock()
		delete(l.conns, tcpConn)
		l.mu.Unlock()
	}()

	serviceConn, err := l.dialFunc()
	if err != nil {
		slog.Error("failed to dial P2P service", "error", err)
		tcpConn.Close()
		return
	}

	BidirectionalProxy(&tcpHalfCloser{tcpConn}, serviceConn, "proxy")
}

// Close closes the TCP listener (stops accepting new connections).
func (l *TCPListener) Close() error {
	return l.listener.Close()
}

// GracefulClose closes the listener, sets a deadline on active connections,
// and waits for all handleConnection goroutines to finish (F9).
func (l *TCPListener) GracefulClose(timeout time.Duration) {
	l.listener.Close()

	// Set deadline on all tracked connections to break io.Copy.
	deadline := time.Now().Add(timeout)
	l.mu.Lock()
	for c := range l.conns {
		c.SetDeadline(deadline)
	}
	l.mu.Unlock()

	// Wait for all goroutines to return.
	done := make(chan struct{})
	go func() {
		l.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout + time.Second):
		// Force close any remaining.
		l.mu.Lock()
		for c := range l.conns {
			c.Close()
		}
		l.mu.Unlock()
	}
}

// Addr returns the listener's network address.
func (l *TCPListener) Addr() net.Addr {
	return l.listener.Addr()
}

// ActiveConns returns the number of active proxy connections.
func (l *TCPListener) ActiveConns() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.conns)
}

// DialWithRetry wraps a dial function with exponential backoff retry.
// maxRetries is the number of retries after the first attempt (0 = no retry).
// Returns a new dial function that retries on failure.
func DialWithRetry(dialFunc func() (ServiceConn, error), maxRetries int) func() (ServiceConn, error) {
	return func() (ServiceConn, error) {
		var lastErr error
		delay := time.Second
		for attempt := 0; attempt <= maxRetries; attempt++ {
			conn, err := dialFunc()
			if err == nil {
				if attempt > 0 {
					slog.Info("connection succeeded", "attempt", attempt+1, "max", maxRetries+1)
				}
				return conn, nil
			}
			lastErr = err
			if attempt < maxRetries {
				slog.Warn("connection attempt failed",
					"attempt", attempt+1, "max", maxRetries+1, "error", err, "retry_in", delay)
				time.Sleep(delay)
				delay *= 2
				if delay > 60*time.Second {
					delay = 60 * time.Second
				}
			}
		}
		return nil, fmt.Errorf("all %d attempts failed: %w", maxRetries+1, lastErr)
	}
}
