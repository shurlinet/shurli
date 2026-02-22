package daemon

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// RuntimeInfo provides the daemon server with access to the P2P runtime.
// This interface decouples the daemon package from the cmd/peerup serveRuntime struct.
type RuntimeInfo interface {
	Network() *p2pnet.Network
	ConfigFile() string
	AuthKeysPath() string
	GaterForHotReload() GaterReloader // nil if gating disabled
	Version() string
	StartTime() time.Time
	PingProtocolID() string
	ConnectToPeer(ctx context.Context, peerID peer.ID) error // DHT + relay fallback
	Interfaces() *p2pnet.InterfaceSummary                    // nil before discovery
	PathTracker() *p2pnet.PathTracker                        // nil before bootstrap
	STUNResult() *p2pnet.STUNResult                          // nil before probe
	IsRelaying() bool                                        // true if peer relay enabled
}

// GaterReloader allows hot-reloading the authorized peers list.
type GaterReloader interface {
	ReloadFromFile() error // reload authorized_keys and update the gater
}

// activeProxy tracks a dynamically created TCP proxy.
type activeProxy struct {
	ID       string
	Peer     string
	Service  string
	Listen   string
	listener *p2pnet.TCPListener
	cancel   context.CancelFunc
	done     chan struct{} // closed when the proxy goroutine exits
}

// Server is the daemon's Unix socket HTTP API server.
type Server struct {
	runtime    RuntimeInfo
	httpServer *http.Server
	listener   net.Listener
	socketPath string
	cookiePath string
	authToken  string
	version    string
	shutdownCh chan struct{} // closed to signal shutdown to the daemon main loop

	// Optional observability (nil when telemetry disabled)
	metrics *p2pnet.Metrics
	audit   *p2pnet.AuditLogger

	mu      sync.Mutex
	proxies map[string]*activeProxy
	nextID  int
}

// NewServer creates a new daemon API server.
func NewServer(runtime RuntimeInfo, socketPath, cookiePath, version string) *Server {
	return &Server{
		runtime:    runtime,
		socketPath: socketPath,
		cookiePath: cookiePath,
		version:    version,
		shutdownCh: make(chan struct{}),
		proxies:    make(map[string]*activeProxy),
	}
}

// SetInstrumentation configures optional metrics and audit logging.
// Must be called before Start(). Both parameters are nil-safe.
func (s *Server) SetInstrumentation(metrics *p2pnet.Metrics, audit *p2pnet.AuditLogger) {
	s.metrics = metrics
	s.audit = audit
}

// ShutdownCh returns a channel that is closed when a shutdown is requested
// via the API (POST /v1/shutdown).
func (s *Server) ShutdownCh() <-chan struct{} {
	return s.shutdownCh
}

// Start creates the Unix socket, writes the cookie file, and starts serving.
// It returns immediately - the server runs in a background goroutine.
func (s *Server) Start() error {
	// Generate auth cookie
	token, err := generateCookie()
	if err != nil {
		return fmt.Errorf("failed to generate auth cookie: %w", err)
	}
	s.authToken = token

	// Check for stale socket
	if err := s.checkStaleSocket(); err != nil {
		return err
	}

	// Bind Unix socket with restrictive umask to avoid TOCTOU race.
	// Setting umask(0077) ensures the socket is created with 0600 permissions
	// atomically, eliminating the window between Listen() and Chmod().
	oldUmask := syscall.Umask(0077)
	listener, err := net.Listen("unix", s.socketPath)
	syscall.Umask(oldUmask)
	if err != nil {
		return fmt.Errorf("failed to listen on socket: %w", err)
	}

	// Write cookie AFTER socket is secured - prevents clients from reading
	// the cookie before the socket is ready to accept authenticated connections.
	if err := os.WriteFile(s.cookiePath, []byte(token), 0600); err != nil {
		listener.Close()
		os.Remove(s.socketPath)
		return fmt.Errorf("failed to write cookie file: %w", err)
	}
	slog.Info("daemon cookie written", "path", s.cookiePath)

	s.listener = listener

	// Set up HTTP routes
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.httpServer = &http.Server{
		Handler:      InstrumentHandler(s.authMiddleware(mux), s.metrics, s.audit),
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second, // longer for streaming ping
	}

	go func() {
		if err := s.httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("daemon server error", "error", err)
		}
	}()

	slog.Info("daemon API listening", "socket", s.socketPath)
	return nil
}

// Stop gracefully shuts down the HTTP server, closes all proxies,
// and cleans up the socket and cookie files.
func (s *Server) Stop() {
	slog.Info("daemon server shutting down")

	// Shutdown HTTP server
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	s.httpServer.Shutdown(ctx)

	// Close all active proxies
	s.mu.Lock()
	for id, proxy := range s.proxies {
		slog.Info("closing proxy", "id", id)
		proxy.cancel()
		if proxy.listener != nil {
			proxy.listener.Close()
		}
		<-proxy.done // wait for goroutine to exit
	}
	s.proxies = make(map[string]*activeProxy)
	s.mu.Unlock()

	// Clean up files
	os.Remove(s.socketPath)
	os.Remove(s.cookiePath)
	slog.Info("daemon server stopped")
}

// checkStaleSocket checks if a daemon is already running on the socket.
// If the socket exists but no daemon is listening, it removes the stale socket.
func (s *Server) checkStaleSocket() error {
	if _, err := os.Stat(s.socketPath); os.IsNotExist(err) {
		return nil // no socket, good to go
	}

	// Socket file exists - try connecting to it
	conn, err := net.DialTimeout("unix", s.socketPath, 2*time.Second)
	if err != nil {
		// Can't connect - stale socket, remove it
		slog.Info("removing stale daemon socket", "path", s.socketPath)
		os.Remove(s.socketPath)
		return nil
	}

	// Connection succeeded - another daemon is alive
	conn.Close()
	return fmt.Errorf("%w: socket %s is already in use", ErrDaemonAlreadyRunning, s.socketPath)
}

// generateCookie creates a 32-byte random hex token.
func generateCookie() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// authMiddleware checks the Authorization: Bearer <token> header on every request.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + s.authToken

		if auth != expected {
			respondError(w, http.StatusUnauthorized, "unauthorized: invalid or missing auth token")
			return
		}

		next.ServeHTTP(w, r)
	})
}
