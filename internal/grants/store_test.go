package grants

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"

	"github.com/shurlinet/shurli/internal/macaroon"
)

func genKeys(t *testing.T) ([]byte, []byte) {
	t.Helper()
	rootKey := make([]byte, 32)
	hmacKey := make([]byte, 32)
	if _, err := rand.Read(rootKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(hmacKey); err != nil {
		t.Fatal(err)
	}
	return rootKey, hmacKey
}

func genPeerID(t *testing.T) peer.ID {
	t.Helper()
	priv, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

func TestGrantAndCheck(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	// No grant yet.
	if s.Check(pid, "file-transfer", 0) {
		t.Fatal("should not have grant before creation")
	}

	// Create grant.
	g, err := s.Grant(pid, 1*time.Hour, nil, false, 0, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if g.PeerID != pid {
		t.Fatalf("peer ID mismatch")
	}
	if g.Permanent {
		t.Fatal("should not be permanent")
	}

	// Check passes.
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("should have grant after creation")
	}
	if !s.Check(pid, "file-browse", 0) {
		t.Fatal("empty services = all allowed")
	}
}

func TestGrantWithServices(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	_, err := s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	if !s.Check(pid, "file-browse", 0) {
		t.Fatal("file-browse should be allowed")
	}
	if s.Check(pid, "file-transfer", 0) {
		t.Fatal("file-transfer should NOT be allowed (not in services list)")
	}
}

func TestGrantPermanent(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 0, nil, true, 0, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !g.Permanent {
		t.Fatal("should be permanent")
	}
	if g.Expired() {
		t.Fatal("permanent grant should not be expired")
	}
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("permanent grant should pass check")
	}
}

func TestRevoke(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	var revokedPeer peer.ID
	s.SetOnRevoke(func(p peer.ID) {
		revokedPeer = p
	})

	s.Grant(pid, 1*time.Hour, nil, false, 0, 0)
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("should have grant")
	}

	if err := s.Revoke(pid); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if s.Check(pid, "file-transfer", 0) {
		t.Fatal("should not have grant after revoke")
	}
	if revokedPeer != pid {
		t.Fatal("onRevoke callback not called")
	}
}

func TestRevokeNonexistent(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	if err := s.Revoke(pid); err == nil {
		t.Fatal("revoking nonexistent grant should fail")
	}
}

func TestExtend(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	s.Grant(pid, 10*time.Minute, nil, false, 0, 0)

	if err := s.Extend(pid, 2*time.Hour); err != nil {
		t.Fatalf("extend: %v", err)
	}

	// Verify the grant is still valid.
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("extended grant should still be valid")
	}

	// Verify remaining time is close to 2 hours.
	s.mu.RLock()
	g := s.grants[pid]
	s.mu.RUnlock()
	remaining := g.Remaining()
	if remaining < 1*time.Hour+50*time.Minute {
		t.Fatalf("remaining time %v is too short", remaining)
	}
}

func TestExtendNonexistent(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	if err := s.Extend(pid, 1*time.Hour); err == nil {
		t.Fatal("extending nonexistent grant should fail")
	}
}

func TestExpiredGrantFailsCheck(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	// Create a grant that expires immediately.
	s.Grant(pid, 1*time.Millisecond, nil, false, 0, 0)
	time.Sleep(5 * time.Millisecond)

	if s.Check(pid, "file-transfer", 0) {
		t.Fatal("expired grant should fail check")
	}
}

func TestList(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)

	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	s.Grant(pid1, 1*time.Hour, nil, false, 0, 0)
	s.Grant(pid2, 1*time.Hour, []string{"file-browse"}, false, 0, 0)

	list := s.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 grants, got %d", len(list))
	}
}

func TestExpiringWithin(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)

	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	s.Grant(pid1, 5*time.Minute, nil, false, 0, 0)
	s.Grant(pid2, 2*time.Hour, nil, false, 0, 0)

	expiring := s.ExpiringWithin(10 * time.Minute)
	if len(expiring) != 1 {
		t.Fatalf("expected 1 expiring grant, got %d", len(expiring))
	}
}

func TestPersistAndLoad(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")

	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(path)

	pid := genPeerID(t)
	s.Grant(pid, 1*time.Hour, []string{"file-transfer", "file-browse"}, false, 0, 0)

	// Load into a new store.
	s2, err := Load(path, rootKey, hmacKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !s2.Check(pid, "file-transfer", 0) {
		t.Fatal("loaded store should have the grant")
	}
	if !s2.Check(pid, "file-browse", 0) {
		t.Fatal("loaded store should allow file-browse")
	}
	if s2.Check(pid, "file-download", 0) {
		t.Fatal("loaded store should NOT allow file-download (not in services)")
	}
}

func TestPersistHMACTamperDetection(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")

	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(path)

	pid := genPeerID(t)
	s.Grant(pid, 1*time.Hour, nil, false, 0, 0)

	// Tamper with the file.
	data, _ := os.ReadFile(path)
	tampered := append(data[:len(data)-5], []byte("XXXXX")...)
	os.WriteFile(path, tampered, 0600)

	_, err := Load(path, rootKey, hmacKey)
	if err == nil {
		t.Fatal("should detect tampered file")
	}
}

func TestPersistWrongKey(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")

	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(path)

	pid := genPeerID(t)
	s.Grant(pid, 1*time.Hour, nil, false, 0, 0)

	// Load with wrong HMAC key.
	wrongKey := make([]byte, 32)
	rand.Read(wrongKey)
	_, err := Load(path, rootKey, wrongKey)
	if err == nil {
		t.Fatal("should fail with wrong HMAC key")
	}
}

func TestLoadNonexistentFile(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s, err := Load("/nonexistent/path/grants.json", rootKey, hmacKey)
	if err != nil {
		t.Fatalf("load nonexistent: %v", err)
	}
	if len(s.List()) != 0 {
		t.Fatal("should have empty grants")
	}
}

func TestSymlinkRejection(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	linkPath := filepath.Join(dir, "grants.json")

	// Create a real file, then symlink to it.
	os.WriteFile(realPath, []byte("{}"), 0600)
	os.Symlink(realPath, linkPath)

	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(linkPath)

	pid := genPeerID(t)
	s.Grant(pid, 1*time.Hour, nil, false, 0, 0)

	// The save should have detected the symlink. Check the real file is NOT corrupted.
	data, _ := os.ReadFile(realPath)
	if string(data) != "{}" {
		t.Fatal("symlink write should have been rejected, real file should be unchanged")
	}
}

func TestCleanup(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)

	pid := genPeerID(t)
	s.Grant(pid, 5*time.Millisecond, nil, false, 0, 0)
	time.Sleep(10 * time.Millisecond)

	s.cleanExpired()

	if len(s.List()) != 0 {
		t.Fatal("expired grant should have been cleaned up")
	}
}

func TestGrantReplaceExisting(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0, 0)
	s.Grant(pid, 2*time.Hour, nil, false, 0, 0) // replace

	// New grant should have no service restriction.
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("replacement grant should allow all services")
	}
}

func TestLoadRejectsUnsignedGrantsFile(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")

	// Write a grants file with grants but no HMAC signature.
	unsigned := `{"version":1,"grants":[{"peer_id":"12D3KooWFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFAKEFA","expires_at":"2030-01-01T00:00:00Z","created_at":"2026-01-01T00:00:00Z"}]}`
	os.WriteFile(path, []byte(unsigned), 0600)

	_, err := Load(path, rootKey, hmacKey)
	if err == nil {
		t.Fatal("should reject unsigned grants file when hmacKey is set")
	}
}

func TestListReturnsCopies(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0, 0)

	list := s.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 grant, got %d", len(list))
	}

	// Mutate the returned copy - should NOT affect the store.
	list[0].Services = append(list[0].Services, "evil-service")

	// Verify store is unchanged.
	s.mu.RLock()
	g := s.grants[pid]
	s.mu.RUnlock()
	if len(g.Services) != 1 || g.Services[0] != "file-browse" {
		t.Fatal("List() should return copies, not references to internal state")
	}
}

func TestTruncateStr(t *testing.T) {
	if truncateStr("short", 16) != "short" {
		t.Fatal("short string should be unchanged")
	}
	if truncateStr("a-very-long-string-here", 8) != "a-very-l" {
		t.Fatal("long string should be truncated")
	}
	if truncateStr("", 16) != "" {
		t.Fatal("empty string should be unchanged")
	}
}

func TestTokenTamperFailsCheck(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	s.Grant(pid, 1*time.Hour, nil, false, 0, 0)

	// Tamper with the token's caveats.
	s.mu.Lock()
	g := s.grants[pid]
	if len(g.Token.Caveats) > 0 {
		g.Token.Caveats[0] = "peer_id=12D3KooWFAKEFAKEFAKE"
	}
	s.mu.Unlock()

	if s.Check(pid, "file-transfer", 0) {
		t.Fatal("tampered token should fail verification")
	}
}

func TestGrantWithMaxDelegations(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 1*time.Hour, nil, false, 3, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	if g.MaxDelegations != 3 {
		t.Fatalf("expected MaxDelegations=3, got %d", g.MaxDelegations)
	}

	// Token should have max_delegations caveat.
	found := false
	for _, c := range g.Token.Caveats {
		if c == "max_delegations=3" {
			found = true
		}
	}
	if !found {
		t.Errorf("token should have max_delegations=3 caveat, got %v", g.Token.Caveats)
	}

	// Grant should still verify.
	if !s.Check(pid, "file-transfer", 0) {
		t.Fatal("grant with max_delegations should verify")
	}
}

func TestGrantWithoutMaxDelegations(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 1*time.Hour, nil, false, 0, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Token MUST have max_delegations=0 caveat (prevents delegate_to injection).
	found := false
	for _, c := range g.Token.Caveats {
		if c == "max_delegations=0" {
			found = true
		}
	}
	if !found {
		t.Fatal("token must have explicit max_delegations=0 caveat")
	}
}

func TestExtendPreservesMaxDelegations(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	s.Grant(pid, 1*time.Hour, nil, false, 5, 0)
	s.Extend(pid, 2*time.Hour)

	s.mu.RLock()
	g := s.grants[pid]
	s.mu.RUnlock()

	if g.MaxDelegations != 5 {
		t.Fatalf("Extend should preserve MaxDelegations, got %d", g.MaxDelegations)
	}

	found := false
	for _, c := range g.Token.Caveats {
		if c == "max_delegations=5" {
			found = true
		}
	}
	if !found {
		t.Error("extended token should still have max_delegations=5")
	}
}

func TestStoreAuditLogIntegration(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	auditKey := make([]byte, 32)
	rand.Read(auditKey)

	al, err := NewAuditLog(auditPath, auditKey)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuditLog(al)

	// Grant, extend, refresh, revoke - all should produce audit entries.
	s.Grant(pid, 1*time.Hour, nil, false, 0, 0, GrantOptions{AutoRefresh: true, MaxRefreshes: 5})
	s.Extend(pid, 2*time.Hour)
	s.Refresh(pid)
	s.Revoke(pid)

	entries, err := al.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("expected 4 audit entries, got %d", len(entries))
	}

	// Verify event types in order.
	expected := []AuditEvent{AuditGrantCreated, AuditGrantExtended, AuditGrantRefreshed, AuditGrantRevoked}
	for i, exp := range expected {
		if entries[i].Event != exp {
			t.Errorf("entry %d: expected %s, got %s", i, exp, entries[i].Event)
		}
	}

	// Verify chain integrity.
	count, err := al.Verify()
	if err != nil {
		t.Fatalf("audit chain verification failed: %v", err)
	}
	if count != 4 {
		t.Fatalf("expected 4 verified entries, got %d", count)
	}
}

// TestStoreAuditMetadataWithoutNotify verifies that audit entries contain
// metadata even when the notification router is NOT wired. This was a bug
// where Revoke/cleanExpired only captured metadata inside an `if onNotify != nil`
// guard, causing audit entries to lose context when notifications were off.
func TestStoreAuditMetadataWithoutNotify(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	// Deliberately NOT calling SetOnNotify - simulates no notification router.
	pid := genPeerID(t)

	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	auditKey := make([]byte, 32)
	rand.Read(auditKey)

	al, err := NewAuditLog(auditPath, auditKey)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuditLog(al)

	// Grant then revoke. Revoke audit entry must have metadata.
	s.Grant(pid, 1*time.Hour, []string{"file-transfer"}, false, 0, 0)
	s.Revoke(pid)

	entries, err := al.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	revokeEntry := entries[1]
	if revokeEntry.Event != AuditGrantRevoked {
		t.Fatalf("expected grant_revoked, got %s", revokeEntry.Event)
	}
	if revokeEntry.Metadata["expires_at"] == "" {
		t.Fatal("revoke audit entry missing expires_at metadata (was gated on onNotify)")
	}
	if revokeEntry.Metadata["services"] != "file-transfer" {
		t.Fatalf("revoke audit entry missing services metadata, got %q", revokeEntry.Metadata["services"])
	}
}

// TestStoreCleanExpiredAuditMetadata verifies that expired grant cleanup
// produces audit entries with metadata regardless of notification router state.
func TestStoreCleanExpiredAuditMetadata(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	// No SetOnNotify.
	pid := genPeerID(t)

	dir := t.TempDir()
	auditPath := filepath.Join(dir, "audit.log")
	auditKey := make([]byte, 32)
	rand.Read(auditKey)

	al, err := NewAuditLog(auditPath, auditKey)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuditLog(al)

	// Create a grant that expires immediately.
	s.Grant(pid, 1*time.Millisecond, []string{"file-browse"}, false, 0, 0)
	time.Sleep(5 * time.Millisecond)
	s.cleanExpired()

	entries, err := al.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (create + expire), got %d", len(entries))
	}

	expireEntry := entries[1]
	if expireEntry.Event != AuditGrantExpired {
		t.Fatalf("expected grant_expired, got %s", expireEntry.Event)
	}
	if expireEntry.Metadata["expires_at"] == "" {
		t.Fatal("expire audit entry missing expires_at metadata")
	}
	if expireEntry.Metadata["services"] != "file-browse" {
		t.Fatalf("expire audit entry missing services metadata, got %q", expireEntry.Metadata["services"])
	}
}

func TestStoreRateLimiter(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	rl := NewOpsRateLimiter(3, nil)
	s.SetRateLimiter(rl)

	// First 3 ops should succeed.
	for i := 0; i < 3; i++ {
		_, err := s.Grant(pid, 1*time.Hour, nil, false, 0, 0)
		if err != nil {
			t.Fatalf("grant %d should succeed: %v", i+1, err)
		}
	}

	// 4th should be rate limited.
	_, err := s.Grant(pid, 1*time.Hour, nil, false, 0, 0)
	if err == nil {
		t.Fatal("4th grant should be rate limited")
	}
}

func TestProtocolVersionCheck(t *testing.T) {
	// Version 0 treated as 1 (pre-D4 compat).
	if err := checkProtocolVersion(0); err != nil {
		t.Fatalf("version 0 should be accepted (pre-D4 compat): %v", err)
	}

	// Current version.
	if err := checkProtocolVersion(GrantProtocolVersion); err != nil {
		t.Fatalf("current version should be accepted: %v", err)
	}

	// Future version (higher than min).
	if err := checkProtocolVersion(GrantProtocolVersion + 1); err != nil {
		t.Fatalf("future version should be accepted: %v", err)
	}
}

func TestGrantWithTransportCaveat(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	// Grant authorizes lan+direct+relay on file-download.
	_, err := s.Grant(pid, 1*time.Hour, []string{"file-download"}, false, 0, 0,
		GrantOptions{Transports: macaroon.TransportLAN | macaroon.TransportDirect | macaroon.TransportRelay})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Token must contain the transport caveat.
	found := false
	for _, c := range s.grants[pid].Token.Caveats {
		if c == "transport=lan,direct,relay" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected transport caveat, got %v", s.grants[pid].Token.Caveats)
	}

	// Check over each transport — all should pass under this mask.
	for _, tr := range []macaroon.TransportType{macaroon.TransportLAN, macaroon.TransportDirect, macaroon.TransportRelay} {
		if !s.Check(pid, "file-download", tr) {
			t.Errorf("transport %d should pass: all bits allowed", tr)
		}
	}
}

func TestGrantTransportCaveatNarrowed(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	// Grant only allows direct.
	_, err := s.Grant(pid, 1*time.Hour, []string{"file-download"}, false, 0, 0,
		GrantOptions{Transports: macaroon.TransportDirect})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	if !s.Check(pid, "file-download", macaroon.TransportDirect) {
		t.Error("direct transport should pass on direct-only grant")
	}
	if s.Check(pid, "file-download", macaroon.TransportRelay) {
		t.Error("relay transport must NOT pass on direct-only grant")
	}
	if s.Check(pid, "file-download", macaroon.TransportLAN) {
		t.Error("lan transport must NOT pass on direct-only grant")
	}
	// 0 transport skips the check — any service grant matches.
	if !s.Check(pid, "file-download", 0) {
		t.Error("zero transport must skip caveat check")
	}
}

func TestGrantTransportCaveatExtendPreserves(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	_, err := s.Grant(pid, 1*time.Hour, []string{"file-download"}, false, 0, 0,
		GrantOptions{Transports: macaroon.TransportLAN | macaroon.TransportRelay})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Extend and verify caveat is still present on rebuilt token.
	if err := s.Extend(pid, 30*time.Minute); err != nil {
		t.Fatalf("extend: %v", err)
	}
	if !s.Check(pid, "file-download", macaroon.TransportRelay) {
		t.Error("relay transport should still pass after extend")
	}
	if s.Check(pid, "file-download", macaroon.TransportDirect) {
		t.Error("direct transport must still be rejected after extend")
	}
}

// TestGrantTransportCaveatPersistence verifies that transport caveats survive
// Save/Load round-trip. Catches regressions in JSON encoding, macaroon bytes,
// or the Grant.Transports field.
func TestGrantTransportCaveatPersistence(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")

	s := NewStore(rootKey, hmacKey)
	s.SetPersistPath(path)
	pid := genPeerID(t)

	_, err := s.Grant(pid, 1*time.Hour, []string{"file-download"}, false, 0, 20*1024*1024*1024, // 20GB
		GrantOptions{Transports: macaroon.TransportLAN | macaroon.TransportDirect | macaroon.TransportRelay})
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	// Load into a fresh store.
	s2, err := Load(path, rootKey, hmacKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	g2 := s2.CheckAndGet(pid)
	if g2 == nil {
		t.Fatal("loaded store should have the grant")
	}
	if g2.Transports != (macaroon.TransportLAN | macaroon.TransportDirect | macaroon.TransportRelay) {
		t.Errorf("loaded grant Transports = %d, want %d", g2.Transports,
			macaroon.TransportLAN|macaroon.TransportDirect|macaroon.TransportRelay)
	}
	if g2.DataBudget != 20*1024*1024*1024 {
		t.Errorf("loaded grant DataBudget = %d, want 20GB", g2.DataBudget)
	}

	// Caveat must be present on the persisted token (not just on the Grant field).
	foundCaveat := false
	for _, c := range g2.Token.Caveats {
		if c == "transport=lan,direct,relay" {
			foundCaveat = true
			break
		}
	}
	if !foundCaveat {
		t.Errorf("persisted token missing transport caveat, got %v", g2.Token.Caveats)
	}

	// Verification against every transport should pass.
	for _, tr := range []macaroon.TransportType{macaroon.TransportLAN, macaroon.TransportDirect, macaroon.TransportRelay} {
		if !s2.Check(pid, "file-download", tr) {
			t.Errorf("loaded grant should pass Check for transport %d", tr)
		}
	}
}
