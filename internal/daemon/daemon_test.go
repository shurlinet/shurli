package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"

	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// --- Mock runtime ---

type mockRuntime struct {
	version   string
	startTime time.Time
	pingProto string
}

func (m *mockRuntime) Network() *p2pnet.Network       { return nil }
func (m *mockRuntime) ConfigFile() string              { return "/mock/config.yaml" }
func (m *mockRuntime) AuthKeysPath() string            { return "" }
func (m *mockRuntime) GaterForHotReload() GaterReloader { return nil }
func (m *mockRuntime) Version() string                 { return m.version }
func (m *mockRuntime) StartTime() time.Time            { return m.startTime }
func (m *mockRuntime) PingProtocolID() string          { return m.pingProto }
func (m *mockRuntime) ConnectToPeer(_ context.Context, _ peer.ID) error { return nil }

func newMockRuntime() *mockRuntime {
	return &mockRuntime{
		version:   "test-0.1.0",
		startTime: time.Now().Add(-60 * time.Second),
		pingProto: "/peerup/ping/1.0.0",
	}
}

// --- Helper to create a test server ---

func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	rt := newMockRuntime()
	srv := NewServer(rt, socketPath, cookiePath, "test-0.1.0")
	return srv, dir
}

// --- Tests ---

func TestGenerateCookie(t *testing.T) {
	token, err := generateCookie()
	if err != nil {
		t.Fatalf("generateCookie failed: %v", err)
	}
	if len(token) != 64 { // 32 bytes = 64 hex chars
		t.Errorf("expected 64-char hex token, got %d chars", len(token))
	}

	// Generate another - should be different
	token2, err := generateCookie()
	if err != nil {
		t.Fatalf("second generateCookie failed: %v", err)
	}
	if token == token2 {
		t.Error("two generated cookies should not be identical")
	}
}

func TestAuthMiddleware_ValidToken(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.authToken = "test-secret-token"

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("OK"))
	})

	handler := srv.authMiddleware(inner)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer test-secret-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuthMiddleware_MissingToken(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.authToken = "test-secret-token"

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	})

	handler := srv.authMiddleware(inner)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}

	var errResp ErrorResponse
	json.NewDecoder(rec.Body).Decode(&errResp)
	if errResp.Error == "" {
		t.Error("expected error message in response")
	}
}

func TestAuthMiddleware_WrongToken(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.authToken = "test-secret-token"

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("inner handler should not be called")
	})

	handler := srv.authMiddleware(inner)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestRespondJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	respondJSON(rec, http.StatusOK, map[string]string{"hello": "world"})

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %s", ct)
	}

	var envelope DataResponse
	var data map[string]string
	body := rec.Body.Bytes()
	json.Unmarshal(body, &envelope)
	// Re-marshal the Data field to get it as map
	dataBytes, _ := json.Marshal(envelope.Data)
	json.Unmarshal(dataBytes, &data)
	if data["hello"] != "world" {
		t.Errorf("expected data.hello=world, got %v", data)
	}
}

func TestRespondText(t *testing.T) {
	rec := httptest.NewRecorder()
	respondText(rec, http.StatusOK, "hello world\n")

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("expected text/plain, got %s", ct)
	}
	if body := rec.Body.String(); body != "hello world\n" {
		t.Errorf("expected 'hello world\\n', got %q", body)
	}
}

func TestRespondError(t *testing.T) {
	rec := httptest.NewRecorder()
	respondError(rec, http.StatusBadRequest, "something went wrong")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}

	var errResp ErrorResponse
	json.NewDecoder(rec.Body).Decode(&errResp)
	if errResp.Error != "something went wrong" {
		t.Errorf("expected error 'something went wrong', got %q", errResp.Error)
	}
}

func TestWantsText_QueryParam(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/status?format=text", nil)
	if !wantsText(req) {
		t.Error("expected wantsText=true for ?format=text")
	}
}

func TestWantsText_AcceptHeader(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/status", nil)
	req.Header.Set("Accept", "text/plain")
	if !wantsText(req) {
		t.Error("expected wantsText=true for Accept: text/plain")
	}
}

func TestWantsText_Default(t *testing.T) {
	req := httptest.NewRequest("GET", "/v1/status", nil)
	if wantsText(req) {
		t.Error("expected wantsText=false for default request")
	}
}

func TestServerStartStop(t *testing.T) {
	srv, dir := newTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Check cookie file exists
	cookiePath := filepath.Join(dir, ".test-cookie")
	if _, err := os.Stat(cookiePath); os.IsNotExist(err) {
		t.Error("cookie file should exist after Start")
	}

	// Check socket file exists
	socketPath := filepath.Join(dir, "test.sock")
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Error("socket file should exist after Start")
	}

	// Verify auth token was set
	if srv.authToken == "" {
		t.Error("auth token should be set after Start")
	}

	// Stop
	srv.Stop()

	// Check files cleaned up
	if _, err := os.Stat(cookiePath); !os.IsNotExist(err) {
		t.Error("cookie file should be removed after Stop")
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("socket file should be removed after Stop")
	}
}

func TestServerStaleSocketDetection(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	// Create a stale socket file (no listener behind it)
	os.WriteFile(socketPath, []byte{}, 0600)

	rt := newMockRuntime()
	srv := NewServer(rt, socketPath, cookiePath, "test")

	// Should succeed - stale socket is detected and removed
	if err := srv.Start(); err != nil {
		t.Fatalf("Start with stale socket should succeed: %v", err)
	}
	srv.Stop()
}

func TestServerDaemonAlreadyRunning(t *testing.T) {
	srv1, dir := newTestServer(t)

	if err := srv1.Start(); err != nil {
		t.Fatalf("First Start failed: %v", err)
	}
	defer srv1.Stop()

	// Try starting a second server on the same socket
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie2")
	rt := newMockRuntime()
	srv2 := NewServer(rt, socketPath, cookiePath, "test")

	err := srv2.Start()
	if err == nil {
		srv2.Stop()
		t.Fatal("Second Start should fail with ErrDaemonAlreadyRunning")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("expected 'already running' error, got: %v", err)
	}
}

func TestServerShutdownChannel(t *testing.T) {
	srv, _ := newTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// ShutdownCh should not be closed initially
	select {
	case <-srv.ShutdownCh():
		t.Fatal("ShutdownCh should not be closed before shutdown request")
	default:
		// Good
	}

	// Clean up
	srv.Stop()
}

func TestClientNewClient_SocketNotFound(t *testing.T) {
	_, err := NewClient("/nonexistent/socket", "/nonexistent/cookie")
	if err == nil {
		t.Fatal("expected error for nonexistent socket")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected 'not running' error, got: %v", err)
	}
}

func TestClientNewClient_CookieNotFound(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	os.WriteFile(socketPath, []byte{}, 0600) // Create socket file

	_, err := NewClient(socketPath, filepath.Join(dir, "nonexistent-cookie"))
	if err == nil {
		t.Fatal("expected error for missing cookie")
	}
	if !strings.Contains(err.Error(), "cookie") {
		t.Errorf("expected cookie-related error, got: %v", err)
	}
}

func TestClientIntegration(t *testing.T) {
	// This test creates a real server + client and tests end-to-end
	// communication. The mock runtime doesn't have a real P2P network,
	// so we can only test endpoints that don't require one.

	srv, dir := newTestServer(t)

	if err := srv.Start(); err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	defer srv.Stop()

	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	client, err := NewClient(socketPath, cookiePath)
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	// Test: status endpoint returns nil Network, so it will panic.
	// We'll test the shutdown endpoint instead which doesn't need Network.

	// Test shutdown via client
	err = client.Shutdown()
	if err != nil {
		t.Fatalf("Shutdown request failed: %v", err)
	}

	// ShutdownCh should be closed shortly
	select {
	case <-srv.ShutdownCh():
		// Good - shutdown was signaled
	case <-time.After(2 * time.Second):
		t.Fatal("ShutdownCh was not closed after shutdown request")
	}
}

func TestHandlerShutdown_Response(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.authToken = "test-token"

	mux := http.NewServeMux()
	srv.registerRoutes(mux)

	req := httptest.NewRequest("POST", "/v1/shutdown", nil)
	rec := httptest.NewRecorder()

	// Call handler directly (skip auth for unit test)
	srv.handleShutdown(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	body, _ := io.ReadAll(rec.Body)
	var envelope DataResponse
	json.Unmarshal(body, &envelope)
	dataMap, ok := envelope.Data.(map[string]interface{})
	if !ok {
		t.Fatalf("expected data to be a map, got %T", envelope.Data)
	}
	if dataMap["status"] != "shutting down" {
		t.Errorf("expected status='shutting down', got %v", dataMap["status"])
	}
}

// TestNetworkClientIntegration creates a real server+client with a p2pnet.Network
// and exercises every client method end-to-end. This covers all client methods
// (Status, Services, Peers, AuthList, Resolve, Expose, Unexpose, etc.) at ~100%.
func TestNetworkClientIntegration(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	net := newTestNetwork(t)
	authKeysPath := filepath.Join(dir, "authorized_keys")
	os.WriteFile(authKeysPath, nil, 0600) // empty but exists

	gater := &mockGater{}
	rt := &networkMockRuntime{
		net:          net,
		version:      "test-0.2.0",
		startTime:    time.Now().Add(-120 * time.Second),
		pingProto:    "/peerup/ping/1.0.0",
		authKeysPath: authKeysPath,
		gater:        gater,
	}

	srv := NewServer(rt, socketPath, cookiePath, "test-0.2.0")
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewClient(socketPath, cookiePath)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// --- Status ---
	t.Run("Status", func(t *testing.T) {
		resp, err := client.Status()
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if resp.PeerID == "" {
			t.Error("PeerID empty")
		}
		if resp.Version != "test-0.2.0" {
			t.Errorf("Version = %q", resp.Version)
		}
		if resp.UptimeSeconds < 119 {
			t.Errorf("UptimeSeconds = %d", resp.UptimeSeconds)
		}
	})

	t.Run("StatusText", func(t *testing.T) {
		text, err := client.StatusText()
		if err != nil {
			t.Fatalf("StatusText: %v", err)
		}
		for _, want := range []string{"peer_id:", "version:", "uptime:"} {
			if !strings.Contains(text, want) {
				t.Errorf("missing %q in text output", want)
			}
		}
	})

	// --- Services ---
	t.Run("Services_Empty", func(t *testing.T) {
		svcs, err := client.Services()
		if err != nil {
			t.Fatalf("Services: %v", err)
		}
		if len(svcs) != 0 {
			t.Errorf("got %d services, want 0", len(svcs))
		}
	})

	t.Run("ServicesText_Empty", func(t *testing.T) {
		_, err := client.ServicesText()
		if err != nil {
			t.Fatalf("ServicesText: %v", err)
		}
	})

	// --- Expose / Unexpose ---
	t.Run("Expose", func(t *testing.T) {
		if err := client.Expose("ssh", "localhost:22"); err != nil {
			t.Fatalf("Expose: %v", err)
		}

		// Verify via Services
		svcs, err := client.Services()
		if err != nil {
			t.Fatalf("Services after expose: %v", err)
		}
		if len(svcs) != 1 || svcs[0].Name != "ssh" {
			t.Errorf("expected 1 service 'ssh', got %v", svcs)
		}
	})

	t.Run("Unexpose", func(t *testing.T) {
		if err := client.Unexpose("ssh"); err != nil {
			t.Fatalf("Unexpose: %v", err)
		}

		svcs, err := client.Services()
		if err != nil {
			t.Fatalf("Services after unexpose: %v", err)
		}
		if len(svcs) != 0 {
			t.Errorf("expected 0 services, got %d", len(svcs))
		}
	})

	t.Run("Unexpose_NotFound", func(t *testing.T) {
		err := client.Unexpose("nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent service")
		}
	})

	// --- Peers ---
	t.Run("Peers", func(t *testing.T) {
		peers, err := client.Peers(false)
		if err != nil {
			t.Fatalf("Peers: %v", err)
		}
		// No peers connected in this test
		if len(peers) != 0 {
			t.Errorf("got %d peers, want 0", len(peers))
		}
	})

	t.Run("PeersAll", func(t *testing.T) {
		peers, err := client.Peers(true)
		if err != nil {
			t.Fatalf("Peers(all): %v", err)
		}
		_ = peers // just verifying no error
	})

	t.Run("PeersText", func(t *testing.T) {
		text, err := client.PeersText(false)
		if err != nil {
			t.Fatalf("PeersText: %v", err)
		}
		_ = text
	})

	// --- Auth ---
	pid := genHandlerPeerID(t)

	t.Run("AuthList_Empty", func(t *testing.T) {
		entries, err := client.AuthList()
		if err != nil {
			t.Fatalf("AuthList: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("got %d entries, want 0", len(entries))
		}
	})

	t.Run("AuthListText_Empty", func(t *testing.T) {
		text, err := client.AuthListText()
		if err != nil {
			t.Fatalf("AuthListText: %v", err)
		}
		_ = text
	})

	t.Run("AuthAdd", func(t *testing.T) {
		if err := client.AuthAdd(pid.String(), "test-peer"); err != nil {
			t.Fatalf("AuthAdd: %v", err)
		}

		entries, err := client.AuthList()
		if err != nil {
			t.Fatalf("AuthList: %v", err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry, got %d", len(entries))
		}
		if entries[0].PeerID != pid.String() {
			t.Errorf("PeerID = %q, want %q", entries[0].PeerID, pid.String())
		}
	})

	t.Run("AuthRemove", func(t *testing.T) {
		if err := client.AuthRemove(pid.String()); err != nil {
			t.Fatalf("AuthRemove: %v", err)
		}

		entries, err := client.AuthList()
		if err != nil {
			t.Fatalf("AuthList: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("expected 0 entries after remove, got %d", len(entries))
		}
	})

	// --- Resolve ---
	t.Run("Resolve_ByName", func(t *testing.T) {
		resolvePid := genHandlerPeerID(t)
		rt.net.RegisterName("home", resolvePid)

		resp, err := client.Resolve("home")
		if err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if resp.PeerID != resolvePid.String() {
			t.Errorf("PeerID = %q", resp.PeerID)
		}
		if resp.Source != "local_config" {
			t.Errorf("Source = %q", resp.Source)
		}
	})

	t.Run("ResolveText", func(t *testing.T) {
		text, err := client.ResolveText("home")
		if err != nil {
			t.Fatalf("ResolveText: %v", err)
		}
		if !strings.Contains(text, "→") {
			t.Errorf("text missing arrow: %q", text)
		}
	})

	t.Run("Resolve_NotFound", func(t *testing.T) {
		_, err := client.Resolve("nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent name")
		}
	})

	t.Run("ResolveText_NotFound", func(t *testing.T) {
		_, err := client.ResolveText("nonexistent")
		if err == nil {
			t.Fatal("expected error for nonexistent name (text)")
		}
	})

	// --- Disconnect ---
	t.Run("Disconnect_NotFound", func(t *testing.T) {
		err := client.Disconnect("proxy-999")
		if err == nil {
			t.Fatal("expected error for nonexistent proxy")
		}
	})

	t.Run("Disconnect_Exists", func(t *testing.T) {
		// Inject a mock proxy into the server's proxy map
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		close(done) // already "done"
		srv.mu.Lock()
		srv.proxies["test-proxy-1"] = &activeProxy{
			ID:      "test-proxy-1",
			Peer:    "test-peer",
			Service: "ssh",
			Listen:  ":0",
			cancel:  cancel,
			done:    done,
		}
		srv.mu.Unlock()
		_ = ctx

		if err := client.Disconnect("test-proxy-1"); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}

		// Verify removed
		srv.mu.Lock()
		_, exists := srv.proxies["test-proxy-1"]
		srv.mu.Unlock()
		if exists {
			t.Error("proxy should be removed after disconnect")
		}
	})

	// --- Shutdown (last - signals stop) ---
	t.Run("Shutdown", func(t *testing.T) {
		if err := client.Shutdown(); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		select {
		case <-srv.ShutdownCh():
			// Good
		case <-time.After(2 * time.Second):
			t.Fatal("ShutdownCh not closed after Shutdown()")
		}
	})
}

// TestP2PHandlerIntegration tests the P2P-dependent daemon handlers
// (ping, traceroute, connect) using two real p2pnet.Network instances
// connected on localhost. This covers the code paths that can't be
// reached with a single isolated host.
func TestP2PHandlerIntegration(t *testing.T) {
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	// Create two p2pnet.Network instances with localhost listen addresses
	// so they can dial each other directly.
	netA := newListeningTestNetwork(t)
	netB := newListeningTestNetwork(t)

	// Connect A → B directly on localhost
	bAddrs := netB.Host().Addrs()
	bInfo := peer.AddrInfo{ID: netB.Host().ID(), Addrs: bAddrs}
	ctx := context.Background()
	if err := netA.Host().Connect(ctx, bInfo); err != nil {
		t.Fatalf("connect A→B: %v", err)
	}

	// Register B's peer ID as "remote" on A's name resolver
	netA.RegisterName("remote", netB.Host().ID())

	// Register a ping-pong handler on B (reads "ping\n", writes "pong\n")
	pingProto := "/peerup/ping/1.0.0"
	netB.Host().SetStreamHandler(protocol.ID(pingProto), func(s network.Stream) {
		defer s.Close()
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		if err != nil {
			return
		}
		msg := strings.TrimSpace(string(buf[:n]))
		if msg == "ping" {
			s.Write([]byte("pong\n"))
		}
	})

	// Create daemon server backed by Network A
	gater := &mockGater{}
	rt := &networkMockRuntime{
		net:       netA,
		version:   "test-0.3.0",
		startTime: time.Now().Add(-60 * time.Second),
		pingProto: pingProto,
		gater:     gater,
	}

	srv := NewServer(rt, socketPath, cookiePath, "test-0.3.0")
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewClient(socketPath, cookiePath)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	// --- Ping (JSON) ---
	t.Run("Ping_JSON", func(t *testing.T) {
		resp, err := client.Ping("remote", 2, 100)
		if err != nil {
			t.Fatalf("Ping: %v", err)
		}
		if len(resp.Results) != 2 {
			t.Fatalf("expected 2 results, got %d", len(resp.Results))
		}
		for i, r := range resp.Results {
			if r.Error != "" {
				t.Errorf("result[%d] error: %s", i, r.Error)
			}
			if r.Path != "DIRECT" {
				t.Errorf("result[%d] path = %q, want DIRECT", i, r.Path)
			}
			if r.RttMs <= 0 {
				t.Errorf("result[%d] rtt = %f, want > 0", i, r.RttMs)
			}
		}
		if resp.Stats.Sent != 2 || resp.Stats.Received != 2 {
			t.Errorf("stats: sent=%d received=%d", resp.Stats.Sent, resp.Stats.Received)
		}
	})

	// --- Ping (text) ---
	t.Run("Ping_Text", func(t *testing.T) {
		text, err := client.PingText("remote", 1, 100)
		if err != nil {
			t.Fatalf("PingText: %v", err)
		}
		for _, want := range []string{"PING remote", "seq=1", "ping statistics"} {
			if !strings.Contains(text, want) {
				t.Errorf("text missing %q: %s", want, text)
			}
		}
	})

	// --- Ping with unresolvable peer ---
	t.Run("Ping_UnresolvablePeer", func(t *testing.T) {
		_, err := client.Ping("nonexistent", 1, 100)
		if err == nil {
			t.Fatal("expected error for unresolvable peer")
		}
	})

	// --- Traceroute (JSON) ---
	t.Run("Traceroute_JSON", func(t *testing.T) {
		// Use doJSON directly since TracerouteText only returns text
		req := TraceRequest{Peer: "remote"}
		body, _ := json.Marshal(req)
		var result p2pnet.TraceResult
		reqData, status, doErr := client.do("POST", "/v1/traceroute", strings.NewReader(string(body)), nil)
		if doErr != nil {
			t.Fatalf("traceroute request: %v", doErr)
		}
		if status != http.StatusOK {
			t.Fatalf("status = %d, body = %s", status, string(reqData))
		}
		// Decode the envelope
		var envelope DataResponse
		json.Unmarshal(reqData, &envelope)
		dataBytes, _ := json.Marshal(envelope.Data)
		json.Unmarshal(dataBytes, &result)

		if result.Path != "DIRECT" {
			t.Errorf("Path = %q, want DIRECT", result.Path)
		}
		if len(result.Hops) == 0 {
			t.Fatal("expected at least 1 hop")
		}
		if result.Hops[0].PeerID == "" {
			t.Error("first hop PeerID should not be empty")
		}
	})

	// --- Traceroute (text) ---
	t.Run("Traceroute_Text", func(t *testing.T) {
		text, err := client.TracerouteText("remote")
		if err != nil {
			t.Fatalf("TracerouteText: %v", err)
		}
		for _, want := range []string{"traceroute to remote", "path: [DIRECT]"} {
			if !strings.Contains(text, want) {
				t.Errorf("text missing %q: %s", want, text)
			}
		}
	})

	// --- Traceroute with unresolvable peer ---
	t.Run("Traceroute_UnresolvablePeer", func(t *testing.T) {
		_, err := client.TracerouteText("nonexistent")
		if err == nil {
			t.Fatal("expected error for unresolvable peer")
		}
	})

	// --- Connect (creates TCP listener + proxy entry) ---
	t.Run("Connect", func(t *testing.T) {
		// Expose a service on B so the stream protocol is registered
		netB.ExposeService("echo", "localhost:9999")
		defer netB.UnexposeService("echo")

		resp, err := client.Connect("remote", "echo", ":0")
		if err != nil {
			t.Fatalf("Connect: %v", err)
		}
		if resp.ID == "" {
			t.Error("proxy ID empty")
		}
		if resp.ListenAddress == "" {
			t.Error("listen address empty")
		}

		// Clean up: disconnect the proxy
		if err := client.Disconnect(resp.ID); err != nil {
			t.Fatalf("Disconnect: %v", err)
		}
	})

	// --- Connect with unresolvable peer ---
	t.Run("Connect_UnresolvablePeer", func(t *testing.T) {
		_, err := client.Connect("nonexistent", "ssh", ":0")
		if err == nil {
			t.Fatal("expected error for unresolvable peer")
		}
	})
}

func TestComputePingStats_Empty(t *testing.T) {
	stats := p2pnet.ComputePingStats(nil)
	if stats.Sent != 0 || stats.Received != 0 || stats.Lost != 0 {
		t.Errorf("empty stats should be all zeros, got %+v", stats)
	}
}

func TestComputePingStats_AllSuccess(t *testing.T) {
	results := []p2pnet.PingResult{
		{Seq: 1, RttMs: 10.0, Path: "DIRECT"},
		{Seq: 2, RttMs: 20.0, Path: "DIRECT"},
		{Seq: 3, RttMs: 30.0, Path: "RELAYED"},
	}

	stats := p2pnet.ComputePingStats(results)
	if stats.Sent != 3 {
		t.Errorf("expected Sent=3, got %d", stats.Sent)
	}
	if stats.Received != 3 {
		t.Errorf("expected Received=3, got %d", stats.Received)
	}
	if stats.Lost != 0 {
		t.Errorf("expected Lost=0, got %d", stats.Lost)
	}
	if stats.LossPct != 0.0 {
		t.Errorf("expected LossPct=0, got %f", stats.LossPct)
	}
	if stats.MinMs != 10.0 {
		t.Errorf("expected MinMs=10.0, got %f", stats.MinMs)
	}
	if stats.MaxMs != 30.0 {
		t.Errorf("expected MaxMs=30.0, got %f", stats.MaxMs)
	}
	if stats.AvgMs != 20.0 {
		t.Errorf("expected AvgMs=20.0, got %f", stats.AvgMs)
	}
}

func TestComputePingStats_WithErrors(t *testing.T) {
	results := []p2pnet.PingResult{
		{Seq: 1, RttMs: 10.0, Path: "DIRECT"},
		{Seq: 2, Error: "timeout"},
		{Seq: 3, RttMs: 30.0, Path: "RELAYED"},
	}

	stats := p2pnet.ComputePingStats(results)
	if stats.Sent != 3 {
		t.Errorf("expected Sent=3, got %d", stats.Sent)
	}
	if stats.Received != 2 {
		t.Errorf("expected Received=2, got %d", stats.Received)
	}
	if stats.Lost != 1 {
		t.Errorf("expected Lost=1, got %d", stats.Lost)
	}
	// 1/3 = 33.33...%
	if stats.LossPct < 33.0 || stats.LossPct > 34.0 {
		t.Errorf("expected LossPct ~33.3%%, got %f", stats.LossPct)
	}
}

func TestComputePingStats_AllErrors(t *testing.T) {
	results := []p2pnet.PingResult{
		{Seq: 1, Error: "timeout"},
		{Seq: 2, Error: "connection refused"},
	}

	stats := p2pnet.ComputePingStats(results)
	if stats.Received != 0 {
		t.Errorf("expected Received=0, got %d", stats.Received)
	}
	if stats.LossPct != 100.0 {
		t.Errorf("expected LossPct=100, got %f", stats.LossPct)
	}
	if stats.MinMs != 0.0 || stats.MaxMs != 0.0 || stats.AvgMs != 0.0 {
		t.Errorf("expected all RTT stats to be 0, got min=%f avg=%f max=%f", stats.MinMs, stats.AvgMs, stats.MaxMs)
	}
}
