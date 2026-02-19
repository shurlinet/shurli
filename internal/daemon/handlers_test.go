package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/satindergrewal/peer-up/internal/config"
	"github.com/satindergrewal/peer-up/pkg/p2pnet"
)

// --- Mock runtime with a real p2pnet.Network ---

type networkMockRuntime struct {
	net          *p2pnet.Network
	version      string
	startTime    time.Time
	pingProto    string
	authKeysPath string
	gater        GaterReloader
}

func (m *networkMockRuntime) Network() *p2pnet.Network         { return m.net }
func (m *networkMockRuntime) ConfigFile() string               { return "/mock/config.yaml" }
func (m *networkMockRuntime) AuthKeysPath() string             { return m.authKeysPath }
func (m *networkMockRuntime) GaterForHotReload() GaterReloader { return m.gater }
func (m *networkMockRuntime) Version() string                  { return m.version }
func (m *networkMockRuntime) StartTime() time.Time             { return m.startTime }
func (m *networkMockRuntime) PingProtocolID() string           { return m.pingProto }
func (m *networkMockRuntime) ConnectToPeer(_ context.Context, _ peer.ID) error {
	return nil
}

// mockGater implements GaterReloader for testing auth add/remove.
type mockGater struct {
	reloadErr   error
	reloadCount int
}

func (m *mockGater) ReloadFromFile() error {
	m.reloadCount++
	return m.reloadErr
}

// genHandlerPeerID generates a random peer ID for handler tests.
func genHandlerPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer ID: %v", err)
	}
	return pid
}

// newTestNetwork creates a minimal p2pnet.Network for handler testing.
func newTestNetwork(t *testing.T) *p2pnet.Network {
	t.Helper()
	dir := t.TempDir()
	net, err := p2pnet.New(&p2pnet.Config{
		KeyFile: filepath.Join(dir, "test.key"),
	})
	if err != nil {
		t.Fatalf("create test network: %v", err)
	}
	t.Cleanup(func() { net.Close() })
	return net
}

// newListeningTestNetwork creates a p2pnet.Network that listens on localhost TCP.
// This allows two test networks to connect to each other directly.
func newListeningTestNetwork(t *testing.T) *p2pnet.Network {
	t.Helper()
	dir := t.TempDir()
	net, err := p2pnet.New(&p2pnet.Config{
		KeyFile: filepath.Join(dir, "test.key"),
		Config: &config.Config{
			Network: config.NetworkConfig{
				ListenAddresses: []string{"/ip4/127.0.0.1/tcp/0"},
			},
		},
	})
	if err != nil {
		t.Fatalf("create listening test network: %v", err)
	}
	t.Cleanup(func() { net.Close() })
	return net
}

// newNetworkServer creates a Server backed by a real p2pnet.Network.
func newNetworkServer(t *testing.T) (*Server, *networkMockRuntime) {
	t.Helper()
	dir := t.TempDir()
	socketPath := filepath.Join(dir, "test.sock")
	cookiePath := filepath.Join(dir, ".test-cookie")

	net := newTestNetwork(t)
	rt := &networkMockRuntime{
		net:       net,
		version:   "test-0.1.0",
		startTime: time.Now().Add(-60 * time.Second),
		pingProto: "/peerup/ping/1.0.0",
	}

	srv := NewServer(rt, socketPath, cookiePath, "test-0.1.0")
	return srv, rt
}

// --- isPeerupAgent ---

func TestIsPeerupAgent(t *testing.T) {
	tests := []struct {
		agent string
		want  bool
	}{
		{"peerup/1.0.0", true},
		{"peerup/0.1.0-dev", true},
		{"relay-server/1.0.0", true},
		{"kubo/0.20.0", false},
		{"", false},
		{"other-agent", false},
	}

	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			got := isPeerupAgent(tt.agent)
			if got != tt.want {
				t.Errorf("isPeerupAgent(%q) = %v, want %v", tt.agent, got, tt.want)
			}
		})
	}
}

// --- handleStatus ---

func TestHandleStatus_JSON(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/status", nil)
	rec := httptest.NewRecorder()
	srv.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var status StatusResponse
	json.Unmarshal(dataBytes, &status)

	if status.PeerID == "" {
		t.Error("PeerID should not be empty")
	}
	if status.Version != "test-0.1.0" {
		t.Errorf("Version = %q", status.Version)
	}
	if status.UptimeSeconds < 59 {
		t.Errorf("UptimeSeconds = %d, expected >= 59", status.UptimeSeconds)
	}
}

func TestHandleStatus_Text(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/status?format=text", nil)
	rec := httptest.NewRecorder()
	srv.handleStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q", ct)
	}

	body := rec.Body.String()
	for _, want := range []string{"peer_id:", "version:", "uptime:", "connected_peers:", "listen_addresses:"} {
		if !bytes.Contains([]byte(body), []byte(want)) {
			t.Errorf("text output missing %q", want)
		}
	}
}

// --- handleServiceList ---

func TestHandleServiceList_Empty(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/services", nil)
	rec := httptest.NewRecorder()
	srv.handleServiceList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var services []ServiceInfo
	json.Unmarshal(dataBytes, &services)

	if len(services) != 0 {
		t.Errorf("got %d services, want 0", len(services))
	}
}

func TestHandleServiceList_WithServices(t *testing.T) {
	srv, rt := newNetworkServer(t)

	// Expose a service via the network
	if err := rt.net.ExposeService("ssh", "localhost:22"); err != nil {
		t.Fatalf("ExposeService: %v", err)
	}

	req := httptest.NewRequest("GET", "/v1/services", nil)
	rec := httptest.NewRecorder()
	srv.handleServiceList(rec, req)

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var services []ServiceInfo
	json.Unmarshal(dataBytes, &services)

	if len(services) != 1 {
		t.Fatalf("got %d services, want 1", len(services))
	}
	if services[0].Name != "ssh" {
		t.Errorf("Name = %q", services[0].Name)
	}
}

func TestHandleServiceList_Text(t *testing.T) {
	srv, rt := newNetworkServer(t)
	rt.net.ExposeService("ssh", "localhost:22")

	req := httptest.NewRequest("GET", "/v1/services?format=text", nil)
	rec := httptest.NewRecorder()
	srv.handleServiceList(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte("ssh")) {
		t.Errorf("text output missing 'ssh': %q", body)
	}
}

// --- handlePeerList ---

func TestHandlePeerList_Empty(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/peers", nil)
	rec := httptest.NewRecorder()
	srv.handlePeerList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var peers []PeerInfo
	json.Unmarshal(dataBytes, &peers)

	if len(peers) != 0 {
		t.Errorf("got %d peers, want 0", len(peers))
	}
}

func TestHandlePeerList_Text(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/peers?format=text", nil)
	rec := httptest.NewRecorder()
	srv.handlePeerList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestHandlePeerList_AllFlag(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("GET", "/v1/peers?all=true", nil)
	rec := httptest.NewRecorder()
	srv.handlePeerList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

// --- handleAuthList ---

func TestHandleAuthList_EmptyPath(t *testing.T) {
	srv, _ := newNetworkServer(t)
	// authKeysPath is "" by default → returns empty list

	req := httptest.NewRequest("GET", "/v1/auth", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandleAuthList_WithFile(t *testing.T) {
	srv, rt := newNetworkServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")

	pid := genHandlerPeerID(t)
	os.WriteFile(authPath, []byte(pid.String()+"  # test peer\n"), 0600)
	rt.authKeysPath = authPath

	req := httptest.NewRequest("GET", "/v1/auth", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthList(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var entries []AuthEntry
	json.Unmarshal(dataBytes, &entries)

	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Comment != "test peer" {
		t.Errorf("Comment = %q", entries[0].Comment)
	}
}

func TestHandleAuthList_Text(t *testing.T) {
	srv, rt := newNetworkServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")

	pid := genHandlerPeerID(t)
	os.WriteFile(authPath, []byte(pid.String()+"\n"), 0600)
	rt.authKeysPath = authPath

	req := httptest.NewRequest("GET", "/v1/auth?format=text", nil)
	rec := httptest.NewRecorder()
	srv.handleAuthList(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := rec.Body.String()
	if !bytes.Contains([]byte(body), []byte(pid.String())) {
		t.Errorf("text output missing peer ID")
	}
}

// --- handleAuthAdd ---

func TestHandleAuthAdd_Success(t *testing.T) {
	srv, rt := newNetworkServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")
	rt.authKeysPath = authPath
	rt.gater = &mockGater{}

	pid := genHandlerPeerID(t)
	body, _ := json.Marshal(AuthAddRequest{PeerID: pid.String(), Comment: "test"})

	req := httptest.NewRequest("POST", "/v1/auth", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAuthAdd(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// Verify file was written
	data, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !bytes.Contains(data, []byte(pid.String())) {
		t.Error("peer ID not found in auth file")
	}
}

func TestHandleAuthAdd_MissingPeerID(t *testing.T) {
	srv, rt := newNetworkServer(t)
	rt.authKeysPath = "/tmp/test-auth"

	body, _ := json.Marshal(AuthAddRequest{PeerID: ""})
	req := httptest.NewRequest("POST", "/v1/auth", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAuthAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAuthAdd_GatingDisabled(t *testing.T) {
	srv, rt := newNetworkServer(t)
	rt.authKeysPath = "" // gating disabled

	pid := genHandlerPeerID(t)
	body, _ := json.Marshal(AuthAddRequest{PeerID: pid.String()})
	req := httptest.NewRequest("POST", "/v1/auth", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleAuthAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleAuthAdd_InvalidBody(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("POST", "/v1/auth", bytes.NewReader([]byte("not json")))
	rec := httptest.NewRecorder()
	srv.handleAuthAdd(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- handleAuthRemove ---

func TestHandleAuthRemove_Success(t *testing.T) {
	srv, rt := newNetworkServer(t)
	dir := t.TempDir()
	authPath := filepath.Join(dir, "authorized_keys")

	pid := genHandlerPeerID(t)
	os.WriteFile(authPath, []byte(pid.String()+"\n"), 0600)
	rt.authKeysPath = authPath
	rt.gater = &mockGater{}

	req := httptest.NewRequest("DELETE", "/v1/auth/"+pid.String(), nil)
	req.SetPathValue("peer_id", pid.String())
	rec := httptest.NewRecorder()
	srv.handleAuthRemove(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleAuthRemove_GatingDisabled(t *testing.T) {
	srv, rt := newNetworkServer(t)
	rt.authKeysPath = ""

	req := httptest.NewRequest("DELETE", "/v1/auth/someid", nil)
	req.SetPathValue("peer_id", "someid")
	rec := httptest.NewRecorder()
	srv.handleAuthRemove(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- handleResolve ---

func TestHandleResolve_ByName(t *testing.T) {
	srv, rt := newNetworkServer(t)

	pid := genHandlerPeerID(t)
	rt.net.RegisterName("home", pid)

	body, _ := json.Marshal(ResolveRequest{Name: "home"})
	req := httptest.NewRequest("POST", "/v1/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleResolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var resp ResolveResponse
	json.Unmarshal(dataBytes, &resp)

	if resp.PeerID != pid.String() {
		t.Errorf("PeerID = %q, want %q", resp.PeerID, pid.String())
	}
	if resp.Source != "local_config" {
		t.Errorf("Source = %q, want 'local_config'", resp.Source)
	}
}

func TestHandleResolve_ByPeerID(t *testing.T) {
	srv, _ := newNetworkServer(t)

	pid := genHandlerPeerID(t)
	body, _ := json.Marshal(ResolveRequest{Name: pid.String()})
	req := httptest.NewRequest("POST", "/v1/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleResolve(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	var envelope DataResponse
	json.NewDecoder(rec.Body).Decode(&envelope)
	dataBytes, _ := json.Marshal(envelope.Data)
	var resp ResolveResponse
	json.Unmarshal(dataBytes, &resp)

	if resp.Source != "peer_id" {
		t.Errorf("Source = %q, want 'peer_id'", resp.Source)
	}
}

func TestHandleResolve_NotFound(t *testing.T) {
	srv, _ := newNetworkServer(t)

	body, _ := json.Marshal(ResolveRequest{Name: "nonexistent"})
	req := httptest.NewRequest("POST", "/v1/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleResolve(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleResolve_Text(t *testing.T) {
	srv, rt := newNetworkServer(t)
	pid := genHandlerPeerID(t)
	rt.net.RegisterName("home", pid)

	body, _ := json.Marshal(ResolveRequest{Name: "home"})
	req := httptest.NewRequest("POST", "/v1/resolve", bytes.NewReader(body))
	req.Header.Set("Accept", "text/plain")
	rec := httptest.NewRecorder()
	srv.handleResolve(rec, req)

	if ct := rec.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q", ct)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("→")) {
		t.Errorf("text output missing arrow: %q", rec.Body.String())
	}
}

func TestHandleResolve_EmptyName(t *testing.T) {
	srv, _ := newNetworkServer(t)

	body, _ := json.Marshal(ResolveRequest{Name: ""})
	req := httptest.NewRequest("POST", "/v1/resolve", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleResolve(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- handleExpose / handleUnexpose ---

func TestHandleExpose_Success(t *testing.T) {
	srv, _ := newNetworkServer(t)

	body, _ := json.Marshal(ExposeRequest{Name: "ssh", LocalAddress: "localhost:22"})
	req := httptest.NewRequest("POST", "/v1/expose", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleExpose(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleExpose_MissingFields(t *testing.T) {
	srv, _ := newNetworkServer(t)

	tests := []struct {
		name string
		req  ExposeRequest
	}{
		{"no name", ExposeRequest{LocalAddress: "localhost:22"}},
		{"no address", ExposeRequest{Name: "ssh"}},
		{"both empty", ExposeRequest{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.req)
			req := httptest.NewRequest("POST", "/v1/expose", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			srv.handleExpose(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleUnexpose_Success(t *testing.T) {
	srv, rt := newNetworkServer(t)
	rt.net.ExposeService("ssh", "localhost:22")

	req := httptest.NewRequest("DELETE", "/v1/expose/ssh", nil)
	req.SetPathValue("name", "ssh")
	rec := httptest.NewRecorder()
	srv.handleUnexpose(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestHandleUnexpose_NotFound(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("DELETE", "/v1/expose/nonexistent", nil)
	req.SetPathValue("name", "nonexistent")
	rec := httptest.NewRecorder()
	srv.handleUnexpose(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// --- handleDisconnect ---

func TestHandleDisconnect_NotFound(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("DELETE", "/v1/connect/proxy-999", nil)
	req.SetPathValue("id", "proxy-999")
	rec := httptest.NewRecorder()
	srv.handleDisconnect(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestHandleDisconnect_EmptyID(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("DELETE", "/v1/connect/", nil)
	req.SetPathValue("id", "")
	rec := httptest.NewRecorder()
	srv.handleDisconnect(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- SocketPath / Listener ---

func TestSocketPath(t *testing.T) {
	srv, _ := newNetworkServer(t)
	if srv.SocketPath() == "" {
		t.Error("SocketPath should not be empty")
	}
}

func TestListenerNilBeforeStart(t *testing.T) {
	srv, _ := newNetworkServer(t)
	if srv.Listener() != nil {
		t.Error("Listener should be nil before Start")
	}
}

// --- handlePing / handleTraceroute input validation ---

func TestHandlePing_EmptyPeer(t *testing.T) {
	srv, _ := newNetworkServer(t)

	body, _ := json.Marshal(PingRequest{Peer: ""})
	req := httptest.NewRequest("POST", "/v1/ping", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handlePing(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandlePing_InvalidBody(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("POST", "/v1/ping", bytes.NewReader([]byte("bad")))
	rec := httptest.NewRecorder()
	srv.handlePing(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleTraceroute_EmptyPeer(t *testing.T) {
	srv, _ := newNetworkServer(t)

	body, _ := json.Marshal(TraceRequest{Peer: ""})
	req := httptest.NewRequest("POST", "/v1/traceroute", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	srv.handleTraceroute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandleTraceroute_InvalidBody(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("POST", "/v1/traceroute", bytes.NewReader([]byte("bad")))
	rec := httptest.NewRecorder()
	srv.handleTraceroute(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

// --- handleConnect input validation ---

func TestHandleConnect_MissingFields(t *testing.T) {
	srv, _ := newNetworkServer(t)

	tests := []struct {
		name string
		req  ConnectRequest
	}{
		{"no peer", ConnectRequest{Service: "ssh", Listen: ":0"}},
		{"no service", ConnectRequest{Peer: "home", Listen: ":0"}},
		{"no listen", ConnectRequest{Peer: "home", Service: "ssh"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(tt.req)
			req := httptest.NewRequest("POST", "/v1/connect", bytes.NewReader(body))
			rec := httptest.NewRecorder()
			srv.handleConnect(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestHandleConnect_InvalidBody(t *testing.T) {
	srv, _ := newNetworkServer(t)

	req := httptest.NewRequest("POST", "/v1/connect", bytes.NewReader([]byte("bad")))
	rec := httptest.NewRecorder()
	srv.handleConnect(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
