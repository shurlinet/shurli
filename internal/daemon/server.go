package daemon

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/grants"
	"github.com/shurlinet/shurli/internal/notify"
	"github.com/shurlinet/shurli/internal/platform"
	"github.com/shurlinet/shurli/pkg/sdk"
	"github.com/shurlinet/shurli/pkg/plugin"
)

// RuntimeInfo provides the daemon server with access to the P2P runtime.
// This interface decouples the daemon package from the cmd/shurli serveRuntime struct.
type RuntimeInfo interface {
	Network() *sdk.Network
	ConfigFile() string
	AuthKeysPath() string
	GaterForHotReload() GaterReloader // nil if gating disabled
	Version() string
	StartTime() time.Time
	PingProtocolID() string
	ConnectToPeer(ctx context.Context, peerID peer.ID) error // DHT + relay fallback
	Interfaces() *sdk.InterfaceSummary                    // nil before discovery
	PathTracker() *sdk.PathTracker                        // nil before bootstrap
	PathProtector() *sdk.PathProtector                   // nil before bootstrap (TS-5)
	BandwidthTracker() *sdk.BandwidthTracker              // nil when disabled
	RelayHealth() *sdk.RelayHealth                        // nil when disabled
	STUNResult() *sdk.STUNResult                          // nil before probe
	IsRelaying() bool                                        // true if peer relay enabled
	RelayAddresses() []string                                // relay multiaddrs from config
	RelayNameFromConfig(peerID string) string                // config-based relay name lookup
	DiscoveryNetwork() string                                // DHT namespace (empty = global)
	RelayMOTDs() []MOTDInfo                                  // MOTD/goodbye messages from relays
	ConfigReloader() ConfigReloader                          // nil if reload not supported
	GrantStore() *grants.Store                                // nil before initialization
	GrantPouch() *grants.Pouch                                // nil before initialization
	GrantProtocol() *grants.GrantProtocol                     // nil before initialization
	GrantsAutoRefresh() bool                                  // config default for auto-refresh
	GrantsMaxRefreshDuration() string                         // config default for max refresh duration (e.g. "3d")
	NotifyRouter() *notify.Router                             // nil before initialization
	PeerManager() *sdk.PeerManager                         // nil before initialization
	GrantCacheSnapshot() []*grants.GrantReceipt               // nil if no grant cache
}

// GaterReloader allows hot-reloading the authorized peers list.
type GaterReloader interface {
	ReloadFromFile() error // reload authorized_keys and update the gater
}

// ConfigReloadResult describes what changed during a config reload.
type ConfigReloadResult struct {
	Changed []string `json:"changed"`           // list of changed fields (e.g. "transfer.receive_mode")
	Reverted []string `json:"reverted,omitempty"` // fields that were rolled back due to errors
}

// ConfigReloader allows hot-reloading config from disk without daemon restart.
type ConfigReloader interface {
	ReloadConfig() (*ConfigReloadResult, error)
}

// ConfigReloadState tracks the last config reload for self-healing and admin visibility.
type ConfigReloadState struct {
	LastReloadTime      time.Time `json:"last_reload_time,omitempty"`
	LastSuccess         bool      `json:"last_success"`
	LastError           string    `json:"last_error,omitempty"`
	LastChanged         []string  `json:"last_changed,omitempty"`
	LastReverted        []string  `json:"last_reverted,omitempty"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	TotalReloads        int       `json:"total_reloads"`
	TotalFailures       int       `json:"total_failures"`
}

// activeInvite tracks a pending async invite (relay-stored).
type activeInvite struct {
	id          string
	groupID     string
	codes       []string
	cancel      context.CancelFunc
}

// activeProxy tracks a dynamically created TCP proxy.
type activeProxy struct {
	ID       string
	Peer     string
	Service  string
	Listen   string
	listener *sdk.TCPListener
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

	// Optional plugin registry (nil if plugin system not initialized)
	registry *plugin.Registry

	// Optional observability (nil when telemetry disabled)
	metrics *sdk.Metrics
	audit   *sdk.AuditLogger

	mu           sync.Mutex
	proxies      map[string]*activeProxy
	pendingInvite *activeInvite // nil when no invite active
	nextID       int
	locked       bool // sensitive ops disabled when true (default: true)

	// Config reload self-healing state
	reloadState ConfigReloadState
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
		locked:     true, // sensitive ops locked by default
	}
}

// SetInstrumentation configures optional metrics and audit logging.
// Must be called before Start(). Both parameters are nil-safe.
func (s *Server) SetInstrumentation(metrics *sdk.Metrics, audit *sdk.AuditLogger) {
	s.metrics = metrics
	s.audit = audit
}

// SetRegistry configures the plugin registry for plugin management API endpoints.
// Must be called before Start(). Nil-safe (plugin endpoints return 503 if nil).
func (s *Server) SetRegistry(r *plugin.Registry) {
	s.registry = r
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
	restoreUmask := platform.RestrictiveUmask()
	listener, err := net.Listen("unix", s.socketPath)
	restoreUmask()
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

	// Cancel any active invite
	s.mu.Lock()
	if s.pendingInvite != nil {
		s.pendingInvite.cancel()
		s.pendingInvite = nil
	}
	s.mu.Unlock()

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

		if subtle.ConstantTimeCompare([]byte(auth), []byte(expected)) != 1 {
			RespondError(w, http.StatusUnauthorized, "unauthorized: invalid or missing auth token")
			return
		}

		next.ServeHTTP(w, r)
	})
}
