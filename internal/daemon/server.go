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

	"github.com/libp2p/go-libp2p/core/network"
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

// proxyGateTime is how long a proxy must survive to be considered stable (NOVEL-2 from autossh).
const proxyGateTime = 30 * time.Second

// proxyMaxQuickDeaths: after this many rapid failures, mark proxy as error and stop reconnecting.
const proxyMaxQuickDeaths = 3

// ProxyPortRetryInterval is how often we retry binding port_conflict proxies (EDGE-4).
const ProxyPortRetryInterval = 30 * time.Second

// activeProxy tracks a dynamically created TCP proxy (both ephemeral and persistent).
type activeProxy struct {
	ID       string
	Peer     string
	Service  string
	Listen   string
	Port     int // intended port (always set for persistent, used when listener is nil)
	listener *sdk.TCPListener
	cancel   context.CancelFunc
	done     chan struct{} // closed when the proxy goroutine exits

	// Persistent proxy fields (empty for ephemeral ~proxy-N proxies).
	persistent bool   // true = from proxies.json, survives daemon restart
	status     string // "active", "waiting", "disabled", "error: ...", "port_conflict"

	// GATETIME tracking (NOVEL-2).
	connectedAt     time.Time // when the proxy's peer was last seen connected
	quickDeathCount int       // connections dying within proxyGateTime
}

// newPlaceholderProxy creates a proxy entry with no listener and a pre-closed done channel.
// Used for disabled and port_conflict proxies that have no goroutine to wait on.
func newPlaceholderProxy(name, peerName, service, listen, status string, port int) *activeProxy {
	done := make(chan struct{})
	close(done) // No goroutine to wait for — immediately unblocks <-done in Stop()/Remove.
	return &activeProxy{
		ID:         name,
		Peer:       peerName,
		Service:    service,
		Listen:     listen,
		Port:       port,
		persistent: true,
		status:     status,
		done:       done,
		cancel:     func() {},
	}
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

	// Persistent proxy store (nil until SetProxyStore called).
	proxyStore *proxyStore

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

// SetProxyStore configures persistent proxy storage.
// Must be called before RestoreProxies(). Nil-safe.
func (s *Server) SetProxyStore(store *proxyStore) {
	s.proxyStore = store
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

	// Close all active proxies (F9: GracefulClose for conn tracking).
	s.mu.Lock()
	for id, proxy := range s.proxies {
		slog.Info("closing proxy", "id", id)
		proxy.cancel()
		if proxy.listener != nil {
			proxy.listener.GracefulClose(5 * time.Second)
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

// RestoreProxies restores persistent proxies from proxies.json.
// Called AFTER bootstrap completes (F3: peers must be reachable).
// Each enabled proxy gets its TCP listener bound immediately.
// Status is set to "waiting" — the peer event loop flips to "active".
func (s *Server) RestoreProxies() {
	if s.proxyStore == nil {
		return
	}

	entries := s.proxyStore.All()
	if len(entries) == 0 {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// F12: clean up orphaned ephemeral proxies from a crashed session.
	for id, proxy := range s.proxies {
		if !proxy.persistent {
			slog.Info("cleaning orphaned ephemeral proxy", "id", id)
			proxy.cancel()
			if proxy.listener != nil {
				proxy.listener.Close()
			}
			delete(s.proxies, id)
		}
	}

	restored := 0
	for _, entry := range entries {
		listenAddr := fmt.Sprintf("127.0.0.1:%d", entry.Port)

		if !entry.Enabled {
			s.proxies[entry.Name] = newPlaceholderProxy(
				entry.Name, entry.Peer, entry.Service, listenAddr, "disabled", entry.Port)
			continue
		}

		// R4: Bind to explicit 127.0.0.1 (not localhost).
		proxy := s.startPersistentProxy(entry.Name, entry.Peer, entry.Service, listenAddr, entry.Port)
		if proxy != nil {
			s.proxies[entry.Name] = proxy
			restored++
		}
	}

	if restored > 0 {
		slog.Info("restored persistent proxies", "count", restored, "total", len(entries))
	}
}

// startPersistentProxy binds a TCP listener and starts serving for a persistent proxy.
// Does NOT require the peer to be connected (EDGE-3). Returns nil on port conflict.
// Caller must hold s.mu.
func (s *Server) startPersistentProxy(name, peerName, service, listenAddr string, port int) *activeProxy {
	pnet := s.runtime.Network()

	// Create dial function with retry (F6: 5 attempts for persistent proxies).
	dialFunc := sdk.DialWithRetry(func() (sdk.ServiceConn, error) {
		targetPeerID, err := pnet.ResolveName(peerName)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %q: %w", peerName, err)
		}
		return pnet.ConnectToService(targetPeerID, service)
	}, 5)

	listener, err := sdk.NewTCPListener(listenAddr, dialFunc)
	if err != nil {
		slog.Warn("proxy port conflict, will retry", "name", name, "addr", listenAddr, "error", err)
		return newPlaceholderProxy(name, peerName, service, listenAddr, "port_conflict", port)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	proxy := &activeProxy{
		ID:         name,
		Peer:       peerName,
		Service:    service,
		Listen:     listener.Addr().String(),
		Port:       port,
		listener:   listener,
		cancel:     cancel,
		done:       done,
		persistent: true,
		status:     "waiting",
	}

	go func() {
		defer close(done)
		<-ctx.Done()
		listener.GracefulClose(5 * time.Second)
	}()

	go func() {
		if err := listener.Serve(); err != nil {
			select {
			case <-ctx.Done():
			default:
				slog.Error("persistent proxy listener stopped", "name", name, "error", err)
			}
		}
	}()

	slog.Info("proxy listener bound", "name", name, "peer", peerName, "service", service, "listen", proxy.Listen)
	return proxy
}

// OnPeerConnected is called when a peer connects (via libp2p event bus subscription).
// Flips persistent proxies targeting that peer from "waiting" to "active".
func (s *Server) OnPeerConnected(pid peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pnet := s.runtime.Network()
	for _, proxy := range s.proxies {
		if !proxy.persistent || proxy.status == "disabled" {
			continue
		}
		targetPeerID, err := pnet.ResolveName(proxy.Peer)
		if err != nil {
			continue
		}
		if targetPeerID == pid {
			if proxy.status == "waiting" || proxy.status == "port_conflict" {
				// If port_conflict, try rebinding.
				if proxy.status == "port_conflict" && proxy.listener == nil {
					restarted := s.startPersistentProxy(proxy.ID, proxy.Peer, proxy.Service, proxy.Listen, proxy.Port)
					if restarted != nil && restarted.status != "port_conflict" {
						*proxy = *restarted
					}
				}
				if proxy.listener != nil {
					proxy.status = "active"
					proxy.connectedAt = time.Now()
					proxy.quickDeathCount = 0
					slog.Info("proxy active", "name", proxy.ID, "peer", proxy.Peer)
				}
			}
		}
	}
}

// OnPeerDisconnected is called when a peer disconnects.
// Flips persistent proxies targeting that peer from "active" to "waiting".
// Applies GATETIME logic (NOVEL-2): rapid disconnects increment quickDeathCount.
func (s *Server) OnPeerDisconnected(pid peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pnet := s.runtime.Network()
	for _, proxy := range s.proxies {
		if !proxy.persistent || proxy.status != "active" {
			continue
		}
		targetPeerID, err := pnet.ResolveName(proxy.Peer)
		if err != nil {
			continue
		}
		if targetPeerID == pid {
			// GATETIME: if connection died within 30s, it's likely a config/auth problem.
			if !proxy.connectedAt.IsZero() && time.Since(proxy.connectedAt) < proxyGateTime {
				proxy.quickDeathCount++
				if proxy.quickDeathCount >= proxyMaxQuickDeaths {
					proxy.status = "error: peer connection unstable (3 rapid failures)"
					slog.Warn("proxy error: rapid failures", "name", proxy.ID, "peer", proxy.Peer, "deaths", proxy.quickDeathCount)
					continue
				}
			}
			proxy.status = "waiting"
			slog.Info("proxy waiting (peer disconnected)", "name", proxy.ID, "peer", proxy.Peer)
		}
	}
}

// OnPeerDeauthorized is called when a peer is removed from authorized_keys (F2).
// Stops all persistent proxies targeting that peer.
func (s *Server) OnPeerDeauthorized(pid peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()

	pnet := s.runtime.Network()
	for _, proxy := range s.proxies {
		if !proxy.persistent {
			continue
		}
		targetPeerID, err := pnet.ResolveName(proxy.Peer)
		if err != nil {
			continue
		}
		if targetPeerID == pid {
			proxy.status = "error: peer not authorized"
			slog.Warn("proxy stopped: peer deauthorized", "name", proxy.ID, "peer", proxy.Peer)
		}
	}
}

// ProxyStatusList returns status info for all proxies (persistent + ephemeral).
func (s *Server) ProxyStatusList() []ProxyStatusInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	result := make([]ProxyStatusInfo, 0, len(s.proxies))
	for _, proxy := range s.proxies {
		if !proxy.persistent {
			continue // ephemeral proxies don't appear in persistent proxy list
		}

		info := ProxyStatusInfo{
			Name:    proxy.ID,
			Peer:    proxy.Peer,
			Service: proxy.Service,
			Port:    proxy.Port,
			Listen:  proxy.Listen,
			Status:  proxy.status,
			Enabled: proxy.status != "disabled",
		}

		result = append(result, info)
	}
	return result
}

// DetectAlreadyConnected checks all persistent proxies and flips "waiting" to "active"
// for any peer that is already connected. Called once after RestoreProxies + event loop
// subscription, to catch peers that connected during bootstrap before the subscription started.
func (s *Server) DetectAlreadyConnected() {
	s.mu.Lock()
	defer s.mu.Unlock()

	pnet := s.runtime.Network()
	h := pnet.Host()
	for _, proxy := range s.proxies {
		if !proxy.persistent || proxy.status != "waiting" {
			continue
		}
		targetPeerID, err := pnet.ResolveName(proxy.Peer)
		if err != nil {
			continue
		}
		if h.Network().Connectedness(targetPeerID) == network.Connected {
			proxy.status = "active"
			proxy.connectedAt = time.Now()
			slog.Info("proxy active (peer already connected)", "name", proxy.ID, "peer", proxy.Peer)
		}
	}
}

// RetryPortConflicts attempts to rebind all port_conflict proxies (EDGE-4).
// Called periodically from the proxy event loop.
func (s *Server) RetryPortConflicts() {
	s.mu.Lock()
	defer s.mu.Unlock()

	for name, proxy := range s.proxies {
		if proxy.status != "port_conflict" || proxy.listener != nil {
			continue
		}
		restarted := s.startPersistentProxy(proxy.ID, proxy.Peer, proxy.Service, proxy.Listen, proxy.Port)
		if restarted != nil && restarted.status != "port_conflict" {
			s.proxies[name] = restarted
			slog.Info("proxy port conflict resolved", "name", name, "listen", restarted.Listen)
		}
	}
}

// PollProxyStatus polls peer connectedness for all persistent proxies and
// flips status to match reality. Catches missed EvtPeerConnectednessChanged
// events (e.g., relay connections established by PathDialer/reconnect, item #33).
// Called periodically from the proxy event loop's 30s ticker.
func (s *Server) PollProxyStatus() {
	s.mu.Lock()
	defer s.mu.Unlock()

	pnet := s.runtime.Network()
	h := pnet.Host()
	for _, proxy := range s.proxies {
		if !proxy.persistent || proxy.status == "disabled" {
			continue
		}
		targetPeerID, err := pnet.ResolveName(proxy.Peer)
		if err != nil {
			continue
		}
		connected := h.Network().Connectedness(targetPeerID) == network.Connected

		switch {
		case proxy.status == "waiting" && connected:
			proxy.status = "active"
			proxy.connectedAt = time.Now()
			proxy.quickDeathCount = 0
			slog.Info("proxy active (poll detected connection)", "name", proxy.ID, "peer", proxy.Peer)

		case proxy.status == "active" && !connected:
			// Missed disconnect event — correct status without GATETIME
			// (this is stale detection, not a fresh disconnect).
			proxy.status = "waiting"
			slog.Info("proxy waiting (poll detected disconnection)", "name", proxy.ID, "peer", proxy.Peer)
		}
	}
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
