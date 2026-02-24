package relay

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// mockGater implements AdminGaterInterface for testing.
type mockGater struct {
	mu                sync.Mutex
	enrollmentEnabled bool
	limit             int
	timeout           time.Duration
}

func (m *mockGater) SetEnrollmentMode(enabled bool, limit int, timeout time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enrollmentEnabled = enabled
	m.limit = limit
	m.timeout = timeout
}

func (m *mockGater) IsEnrollmentEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enrollmentEnabled
}

func tempPaths(t *testing.T) (socketPath, cookiePath string) {
	t.Helper()
	// Use /tmp with short random names to stay under macOS 104-byte Unix socket path limit.
	// t.TempDir() paths are too long (140+ bytes).
	suffix := fmt.Sprintf("%d", time.Now().UnixNano()%100000)
	dir, err := os.MkdirTemp("/tmp", "pu-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "a.sock"), filepath.Join(dir, "a.cookie")
}

// testRelayAddr is a synthetic relay multiaddr for test code encoding.
// Uses RFC 5737 TEST-NET-3 address and a fake peer ID.
const testRelayAddr = "/ip4/203.0.113.50/tcp/7777/p2p/12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"

func TestAdminServerStartStop(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Socket and cookie should exist.
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("socket not created: %v", err)
	}
	if _, err := os.Stat(cookie); err != nil {
		t.Errorf("cookie not created: %v", err)
	}

	srv.Stop()

	// Both should be cleaned up.
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Error("socket not cleaned up after Stop")
	}
	if _, err := os.Stat(cookie); !os.IsNotExist(err) {
		t.Error("cookie not cleaned up after Stop")
	}
}

func TestAdminServerStaleSocket(t *testing.T) {
	sock, cookie := tempPaths(t)

	// Create a stale socket file (not a real listener).
	os.WriteFile(sock, []byte("stale"), 0600)

	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start should succeed with stale socket: %v", err)
	}
	defer srv.Stop()
}

func TestAdminServerAuthRequired(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	// Request without auth should fail.
	client := &http.Client{
		Transport: unixTransport(sock),
	}

	resp, err := client.Get("http://relay-admin/v1/pair")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestAdminClientCreateGroup(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	resp, err := client.CreateGroup(2, 600, 0, "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	if resp.GroupID == "" {
		t.Error("group ID should not be empty")
	}
	if len(resp.Codes) != 2 {
		t.Errorf("got %d codes, want 2", len(resp.Codes))
	}
	if resp.ExpiresAt == "" {
		t.Error("expires_at should not be empty")
	}

	// Codes should be dash-separated base32 strings.
	for i, code := range resp.Codes {
		if !strings.Contains(code, "-") {
			t.Errorf("code %d should be dash-separated: %s", i, code)
		}
	}
}

func TestAdminClientCreateGroupEnrollment(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	// Enrollment should be disabled initially.
	if gater.IsEnrollmentEnabled() {
		t.Error("enrollment should be disabled initially")
	}

	_, err = client.CreateGroup(1, 600, 0, "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Enrollment should now be enabled.
	if !gater.IsEnrollmentEnabled() {
		t.Error("enrollment should be enabled after creating a group")
	}
}

func TestAdminClientListGroups(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	// Empty list initially.
	groups, err := client.ListGroups()
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(groups))
	}

	// Create one.
	_, err = client.CreateGroup(1, 600, 0, "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	groups, err = client.ListGroups()
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 1 {
		t.Errorf("expected 1 group, got %d", len(groups))
	}
	if groups[0].Total != 1 {
		t.Errorf("expected total=1, got %d", groups[0].Total)
	}
}

func TestAdminClientRevokeGroup(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	resp, err := client.CreateGroup(1, 600, 0, "")
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}

	// Enrollment enabled.
	if !gater.IsEnrollmentEnabled() {
		t.Error("enrollment should be enabled")
	}

	// Revoke.
	if err := client.RevokeGroup(resp.GroupID); err != nil {
		t.Fatalf("RevokeGroup: %v", err)
	}

	// Enrollment should be disabled (zero active groups).
	if gater.IsEnrollmentEnabled() {
		t.Error("enrollment should be disabled after revoking last group")
	}

	// List should be empty.
	groups, err := client.ListGroups()
	if err != nil {
		t.Fatalf("ListGroups: %v", err)
	}
	if len(groups) != 0 {
		t.Errorf("expected 0 groups after revoke, got %d", len(groups))
	}
}

func TestAdminClientRevokeNonexistent(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	err = client.RevokeGroup("nonexistent")
	if err == nil {
		t.Error("revoking nonexistent group should fail")
	}
}

func TestAdminClientNotRunning(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "missing.sock")
	cookie := filepath.Join(t.TempDir(), "missing.cookie")

	_, err := NewAdminClient(sock, cookie)
	if err == nil {
		t.Error("should fail when relay is not running")
	}
	if !strings.Contains(err.Error(), "relay is not running") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAdminClientMultipleGroupsEnrollment(t *testing.T) {
	sock, cookie := tempPaths(t)
	store := NewTokenStore()
	gater := &mockGater{}

	srv := NewAdminServer(store, gater, testRelayAddr, "", sock, cookie)
	if err := srv.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer srv.Stop()

	client, err := NewAdminClient(sock, cookie)
	if err != nil {
		t.Fatalf("NewAdminClient: %v", err)
	}

	// Create two groups.
	resp1, _ := client.CreateGroup(1, 600, 0, "")
	_, _ = client.CreateGroup(1, 600, 0, "")

	if !gater.IsEnrollmentEnabled() {
		t.Error("enrollment should be enabled with active groups")
	}

	// Revoke first group - enrollment should stay enabled (still 1 active).
	client.RevokeGroup(resp1.GroupID)
	if !gater.IsEnrollmentEnabled() {
		t.Error("enrollment should stay enabled with 1 active group remaining")
	}
}

// unixTransport returns an http.Transport that dials the given Unix socket.
func unixTransport(socketPath string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socketPath)
		},
	}
}
