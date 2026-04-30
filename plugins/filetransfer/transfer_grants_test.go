package filetransfer

import (
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/libp2p/go-libp2p/core/protocol"
	ma "github.com/multiformats/go-multiaddr"

	"github.com/shurlinet/shurli/pkg/sdk"
)

// --- Mock grant checker ---

type mockGrantChecker struct {
	grants          map[peer.ID]*mockGrant
	trackCalls      atomic.Int64
	resetCalls      atomic.Int64
	lastTrackRelay  peer.ID
	lastTrackDir    string
	lastTrackBytes  int64
}

type mockGrant struct {
	remaining       time.Duration
	budget          int64
	sessionDuration time.Duration
}

func newMockGrantChecker() *mockGrantChecker {
	return &mockGrantChecker{grants: make(map[peer.ID]*mockGrant)}
}

func (m *mockGrantChecker) GrantStatus(relayID peer.ID) (time.Duration, int64, time.Duration, bool) {
	g, ok := m.grants[relayID]
	if !ok {
		return 0, 0, 0, false
	}
	return g.remaining, g.budget, g.sessionDuration, true
}

func (m *mockGrantChecker) HasSufficientBudget(relayID peer.ID, fileSize int64, direction string) bool {
	g, ok := m.grants[relayID]
	if !ok {
		return false
	}
	return g.budget >= fileSize
}

func (m *mockGrantChecker) TrackCircuitBytes(relayID peer.ID, direction string, n int64) {
	m.trackCalls.Add(1)
	m.lastTrackRelay = relayID
	m.lastTrackDir = direction
	m.lastTrackBytes = n
}

func (m *mockGrantChecker) ResetCircuitCounters(relayID peer.ID) {
	m.resetCalls.Add(1)
}

// --- Mock conn for relay detection ---

type mockRelayConn struct {
	limited     bool
	remotePeer  peer.ID
	remoteAddr  ma.Multiaddr
	network.Conn // embed for unimplemented methods
}

func (c *mockRelayConn) Stat() network.ConnStats {
	return network.ConnStats{Stats: network.Stats{Limited: c.limited}}
}

func (c *mockRelayConn) RemotePeer() peer.ID {
	return c.remotePeer
}

func (c *mockRelayConn) RemoteMultiaddr() ma.Multiaddr {
	return c.remoteAddr
}

// --- Mock stream for interface satisfaction ---

type mockStream struct {
	readDeadline  time.Time
	writeDeadline time.Time
}

func (m *mockStream) Read(p []byte) (int, error)                       { return 0, nil }
func (m *mockStream) Write(p []byte) (int, error)                      { return len(p), nil }
func (m *mockStream) Close() error                                      { return nil }
func (m *mockStream) CloseWrite() error                                  { return nil }
func (m *mockStream) CloseRead() error                                   { return nil }
func (m *mockStream) Reset() error                                       { return nil }
func (m *mockStream) ResetWithError(network.StreamErrorCode) error       { return nil }
func (m *mockStream) SetDeadline(t time.Time) error                      { return nil }
func (m *mockStream) SetReadDeadline(t time.Time) error                  { m.readDeadline = t; return nil }
func (m *mockStream) SetWriteDeadline(t time.Time) error                 { m.writeDeadline = t; return nil }
func (m *mockStream) ID() string                                         { return "test" }
func (m *mockStream) Protocol() protocol.ID                              { return "/test/1.0.0" }
func (m *mockStream) SetProtocol(protocol.ID) error                      { return nil }
func (m *mockStream) Stat() network.Stats                                { return network.Stats{} }
func (m *mockStream) Conn() network.Conn                                 { return nil }
func (m *mockStream) Scope() network.StreamScope                         { return nil }

// --- Mock stream wrapping conn ---

type mockRelayStream struct {
	conn *mockRelayConn
	mockStream // embed base mock for other methods
}

func (s *mockRelayStream) Conn() network.Conn {
	return s.conn
}

// --- Test helpers ---

const (
	testRelayID  = "12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN"
	testTargetID = "12D3KooWLin1iSvAoMvaBZCrwjCQkGWvUADXmHgHSru5R6PzCo9o"
)

// makeRelayStream creates a mock relayed stream through testRelayID.
func makeRelayStream(t *testing.T) *mockRelayStream {
	t.Helper()
	circuitAddr := "/ip4/203.0.113.50/tcp/4001/p2p/" + testRelayID + "/p2p-circuit/p2p/" + testTargetID
	addr, err := ma.NewMultiaddr(circuitAddr)
	if err != nil {
		t.Fatalf("multiaddr parse: %v", err)
	}
	return &mockRelayStream{
		conn: &mockRelayConn{
			limited:    true,
			remotePeer: peer.ID(testTargetID),
			remoteAddr: addr,
		},
	}
}

// makeDirectStream creates a mock direct (non-relay) stream.
func makeDirectStream(t *testing.T) *mockRelayStream {
	t.Helper()
	addr, _ := ma.NewMultiaddr("/ip4/203.0.113.1/tcp/4001")
	return &mockRelayStream{
		conn: &mockRelayConn{
			limited:    false,
			remotePeer: "peer-target",
			remoteAddr: addr,
		},
	}
}

// testRelayPeerID returns the decoded peer.ID for testRelayID.
func testRelayPeerID(t *testing.T) peer.ID {
	t.Helper()
	pid, err := peer.Decode(testRelayID)
	if err != nil {
		t.Fatalf("decode relay ID: %v", err)
	}
	return pid
}

// --- Tests ---

func TestRelayPeerFromStream_DirectConnection(t *testing.T) {
	s := makeDirectStream(t)
	got := relayPeerFromStream(s)
	if got != "" {
		t.Errorf("expected empty peer ID for direct connection, got %s", got)
	}
}

func TestRelayPeerFromStream_RelayedConnection(t *testing.T) {
	s := makeRelayStream(t)
	got := relayPeerFromStream(s)
	want := testRelayPeerID(t)
	if got != want {
		t.Errorf("expected relay peer %s, got %s", want, got)
	}
}

func TestCheckRelayGrant_NoGrantChecker(t *testing.T) {
	ts := &TransferService{} // no grantChecker
	s := makeRelayStream(t)
	info := ts.checkRelayGrant(s, 1000, "send")
	if info.IsRelayed {
		t.Error("should not detect relay without grant checker")
	}
}

func TestCheckRelayGrant_NoGrant(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	info := ts.checkRelayGrant(s, 1000, "send")
	if !info.IsRelayed {
		t.Error("should detect relayed connection")
	}
	if info.GrantActive {
		t.Error("should report no grant active")
	}
}

func TestCheckRelayGrant_SufficientBudgetAndTime(t *testing.T) {
	gc := newMockGrantChecker()
	gc.grants[testRelayPeerID(t)] = &mockGrant{
		remaining:       2 * time.Hour,
		budget:          1 << 30, // 1 GB
		sessionDuration: 2 * time.Hour,
	}

	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	// 500 MB file, 1 GB budget, 2 hours remaining.
	info := ts.checkRelayGrant(s, 500<<20, "send")
	if !info.GrantActive {
		t.Error("grant should be active")
	}
	if !info.BudgetOK {
		t.Error("budget should be sufficient")
	}
	if !info.TimeOK {
		t.Error("time should be sufficient")
	}
}

func TestCheckRelayGrant_InsufficientBudget(t *testing.T) {
	gc := newMockGrantChecker()
	gc.grants[testRelayPeerID(t)] = &mockGrant{
		remaining:       2 * time.Hour,
		budget:          200 << 20, // 200 MB
		sessionDuration: 2 * time.Hour,
	}

	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	// 500 MB file, 200 MB budget.
	info := ts.checkRelayGrant(s, 500<<20, "send")
	if !info.GrantActive {
		t.Error("grant should be active")
	}
	if info.BudgetOK {
		t.Error("budget should be insufficient")
	}
}

func TestCheckRelayGrant_InsufficientTime_GrantExpiring(t *testing.T) {
	gc := newMockGrantChecker()
	gc.grants[testRelayPeerID(t)] = &mockGrant{
		remaining:       30 * time.Second, // only 30s left
		budget:          math.MaxInt64,    // unlimited budget
		sessionDuration: 2 * time.Hour,
	}

	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	// 100 MB file at ~200KB/s = ~500s. Only 30s remaining.
	info := ts.checkRelayGrant(s, 100<<20, "send")
	if info.TimeOK {
		t.Error("time should be insufficient (30s left, need ~500s)")
	}
}

func TestCheckRelayGrant_InsufficientTime_SessionTooShort(t *testing.T) {
	gc := newMockGrantChecker()
	gc.grants[testRelayPeerID(t)] = &mockGrant{
		remaining:       24 * time.Hour,     // plenty of grant time
		budget:          math.MaxInt64,      // unlimited budget
		sessionDuration: 30 * time.Second,   // but session is only 30s
	}

	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	// 100 MB file at ~200KB/s = ~500s. Session is only 30s.
	info := ts.checkRelayGrant(s, 100<<20, "send")
	if info.TimeOK {
		t.Error("time should be insufficient (session 30s, transfer needs ~500s)")
	}
	if !info.GrantActive {
		t.Error("grant should still be active")
	}
}

func TestCheckRelayGrant_PermanentGrant(t *testing.T) {
	gc := newMockGrantChecker()
	gc.grants[testRelayPeerID(t)] = &mockGrant{
		remaining:       time.Duration(math.MaxInt64), // permanent
		budget:          math.MaxInt64,                // unlimited
		sessionDuration: 0,
	}

	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	info := ts.checkRelayGrant(s, 1<<40, "send") // 1 TB
	if !info.BudgetOK {
		t.Error("permanent grant should have sufficient budget")
	}
	if !info.TimeOK {
		t.Error("permanent grant should have sufficient time")
	}
}

func TestMakeChunkTracker_DirectConnection(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	s := makeDirectStream(t)

	tracker := ts.makeChunkTracker(s, "send")
	if tracker != nil {
		t.Error("should return nil tracker for direct connection")
	}
}

func TestMakeChunkTracker_RelayedConnection(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	tracker := ts.makeChunkTracker(s, "send")
	if tracker == nil {
		t.Fatal("should return tracker for relayed connection")
	}

	// Call tracker and verify TrackCircuitBytes is called.
	tracker(4096)
	if gc.trackCalls.Load() != 1 {
		t.Errorf("expected 1 TrackCircuitBytes call, got %d", gc.trackCalls.Load())
	}
	if gc.lastTrackBytes != 4096 {
		t.Errorf("expected 4096 bytes tracked, got %d", gc.lastTrackBytes)
	}
	if gc.lastTrackDir != "send" {
		t.Errorf("expected direction 'send', got %q", gc.lastTrackDir)
	}
}

func TestBudgetTracking_MultipleChunks(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	s := makeRelayStream(t)

	tracker := ts.makeChunkTracker(s, "recv")
	if tracker == nil {
		t.Fatal("should return tracker")
	}

	// Simulate 10 chunks.
	for i := 0; i < 10; i++ {
		tracker(1024)
	}
	if gc.trackCalls.Load() != 10 {
		t.Errorf("expected 10 TrackCircuitBytes calls, got %d", gc.trackCalls.Load())
	}
}

func TestAddWireBytes_CallsRelayTracker(t *testing.T) {
	var called int64
	p := &TransferProgress{}
	p.setRelayTracker(func(n int64) {
		called += n
	})

	p.addWireBytes(100)
	p.addWireBytes(200)
	p.addWireBytes(300)

	if called != 600 {
		t.Errorf("relay tracker received %d bytes, want 600", called)
	}
}

func TestAddWireBytes_NoRelayTracker(t *testing.T) {
	// No tracker set - should not panic.
	p := &TransferProgress{}
	p.addWireBytes(100)
	if p.CompressedSize != 100 {
		t.Errorf("CompressedSize = %d, want 100", p.CompressedSize)
	}
}

func TestIsRelaySessionExpiry_GrantActive(t *testing.T) {
	gc := newMockGrantChecker()
	relayID := testRelayPeerID(t)

	gc.grants[relayID] = &mockGrant{
		remaining:       1 * time.Hour,
		budget:          1 << 30,
		sessionDuration: 30 * time.Minute,
	}

	ts := &TransferService{grantChecker: gc}

	// Transport-level error (e.g. stream reset) with active grant = session expiry.
	transportErr := fmt.Errorf("stream reset")
	if !ts.isRelaySessionExpiry(relayID, transportErr) {
		t.Error("should return true for transport error when grant is still active")
	}
}

func TestIsRelaySessionExpiry_GrantExpired(t *testing.T) {
	gc := newMockGrantChecker()
	relayID := testRelayPeerID(t)
	// No grant in cache = expired/revoked.

	ts := &TransferService{grantChecker: gc}

	if ts.isRelaySessionExpiry(relayID, fmt.Errorf("stream reset")) {
		t.Error("should return false when no grant cached")
	}
}

func TestIsRelaySessionExpiry_NoChecker(t *testing.T) {
	ts := &TransferService{}
	if ts.isRelaySessionExpiry("some-peer", fmt.Errorf("stream reset")) {
		t.Error("should return false with no grant checker")
	}
}

func TestIsRelaySessionExpiry_EmptyPeerID(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	if ts.isRelaySessionExpiry("", fmt.Errorf("stream reset")) {
		t.Error("should return false for empty peer ID")
	}
}

func TestIsRelaySessionExpiry_AppErrorExcluded(t *testing.T) {
	gc := newMockGrantChecker()
	relayID := testRelayPeerID(t)
	gc.grants[relayID] = &mockGrant{
		remaining:       1 * time.Hour,
		budget:          1 << 30,
		sessionDuration: 30 * time.Minute,
	}
	ts := &TransferService{grantChecker: gc}

	// Application-level errors should NOT trigger reconnection.
	appErrors := []error{
		fmt.Errorf("peer rejected transfer"),
		fmt.Errorf("file too large: 500MB"),
		fmt.Errorf("insufficient disk space"),
		fmt.Errorf("cancelled"),
		fmt.Errorf("relay grant expires too soon"),
		fmt.Errorf("access denied"),
	}
	for _, err := range appErrors {
		if ts.isRelaySessionExpiry(relayID, err) {
			t.Errorf("should return false for app error: %q", err)
		}
	}
}

func TestIsRelaySessionExpiry_NilError(t *testing.T) {
	gc := newMockGrantChecker()
	relayID := testRelayPeerID(t)
	gc.grants[relayID] = &mockGrant{remaining: 1 * time.Hour}
	ts := &TransferService{grantChecker: gc}

	if ts.isRelaySessionExpiry(relayID, nil) {
		t.Error("should return false for nil error")
	}
}

func TestRelayReconnectDelay(t *testing.T) {
	tests := []struct {
		attempt int
		want    time.Duration
	}{
		{0, 2 * time.Second},
		{1, 4 * time.Second},
		{2, 8 * time.Second},
		{3, 16 * time.Second},
		{4, 32 * time.Second},
		{5, 32 * time.Second}, // capped
		{10, 32 * time.Second},
	}
	for _, tt := range tests {
		got := relayReconnectDelay(tt.attempt)
		if got != tt.want {
			t.Errorf("relayReconnectDelay(%d) = %s, want %s", tt.attempt, got, tt.want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1 << 20, "1.0 MB"},
		{500 << 20, "500.0 MB"},
		{1 << 30, "1.0 GB"},
		{3 << 30, "3.0 GB"},
	}
	for _, tt := range tests {
		got := sdk.FormatBytes(tt.input)
		if got != tt.want {
			t.Errorf("sdk.FormatBytes(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShortPeerStr(t *testing.T) {
	// Use a real peer ID that's long enough.
	longID, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	result := shortPeerStr(longID)
	if !strings.HasSuffix(result, "...") {
		t.Errorf("long peer should be truncated: %q", result)
	}
	if len(result) != 19 { // 16 + "..."
		t.Errorf("expected length 19, got %d: %q", len(result), result)
	}

	// Short string (won't happen in practice but tests the branch).
	shortResult := shortPeerStr(peer.ID("ab"))
	if strings.HasSuffix(shortResult, "...") {
		t.Errorf("short peer should not be truncated: %q", shortResult)
	}
}

func TestRelayGrantChecker_InterfaceSatisfied(t *testing.T) {
	// Verify mockGrantChecker satisfies the interface.
	var _ sdk.RelayGrantChecker = newMockGrantChecker()
}

func TestCheckRelayGrant_DirectConnection(t *testing.T) {
	gc := newMockGrantChecker()
	ts := &TransferService{grantChecker: gc}
	s := makeDirectStream(t)

	info := ts.checkRelayGrant(s, 1000, "send")
	if info.IsRelayed {
		t.Error("direct connection should not be flagged as relayed")
	}
}

// Test GrantCache.GrantStatus satisfies the interface contract.
func TestGrantStatusMethod(t *testing.T) {
	gc := newMockGrantChecker()
	relayID := testRelayPeerID(t)

	// No grant.
	_, _, _, ok := gc.GrantStatus(relayID)
	if ok {
		t.Error("should return ok=false for missing grant")
	}

	// Add grant.
	gc.grants[relayID] = &mockGrant{
		remaining:       1 * time.Hour,
		budget:          500 << 20,
		sessionDuration: 30 * time.Minute,
	}

	rem, budget, sessDur, ok := gc.GrantStatus(relayID)
	if !ok {
		t.Error("should return ok=true for existing grant")
	}
	if rem != 1*time.Hour {
		t.Errorf("remaining = %s, want 1h", rem)
	}
	if budget != 500<<20 {
		t.Errorf("budget = %d, want %d", budget, 500<<20)
	}
	if sessDur != 30*time.Minute {
		t.Errorf("sessionDuration = %s, want 30m", sessDur)
	}
}
