package grants

import (
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
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
	if s.Check(pid, "file-transfer") {
		t.Fatal("should not have grant before creation")
	}

	// Create grant.
	g, err := s.Grant(pid, 1*time.Hour, nil, false, 0)
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
	if !s.Check(pid, "file-transfer") {
		t.Fatal("should have grant after creation")
	}
	if !s.Check(pid, "file-browse") {
		t.Fatal("empty services = all allowed")
	}
}

func TestGrantWithServices(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	_, err := s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}

	if !s.Check(pid, "file-browse") {
		t.Fatal("file-browse should be allowed")
	}
	if s.Check(pid, "file-transfer") {
		t.Fatal("file-transfer should NOT be allowed (not in services list)")
	}
}

func TestGrantPermanent(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 0, nil, true, 0)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if !g.Permanent {
		t.Fatal("should be permanent")
	}
	if g.Expired() {
		t.Fatal("permanent grant should not be expired")
	}
	if !s.Check(pid, "file-transfer") {
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

	s.Grant(pid, 1*time.Hour, nil, false, 0)
	if !s.Check(pid, "file-transfer") {
		t.Fatal("should have grant")
	}

	if err := s.Revoke(pid); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if s.Check(pid, "file-transfer") {
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

	s.Grant(pid, 10*time.Minute, nil, false, 0)

	if err := s.Extend(pid, 2*time.Hour); err != nil {
		t.Fatalf("extend: %v", err)
	}

	// Verify the grant is still valid.
	if !s.Check(pid, "file-transfer") {
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
	s.Grant(pid, 1*time.Millisecond, nil, false, 0)
	time.Sleep(5 * time.Millisecond)

	if s.Check(pid, "file-transfer") {
		t.Fatal("expired grant should fail check")
	}
}

func TestList(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)

	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	s.Grant(pid1, 1*time.Hour, nil, false, 0)
	s.Grant(pid2, 1*time.Hour, []string{"file-browse"}, false, 0)

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

	s.Grant(pid1, 5*time.Minute, nil, false, 0)
	s.Grant(pid2, 2*time.Hour, nil, false, 0)

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
	s.Grant(pid, 1*time.Hour, []string{"file-transfer", "file-browse"}, false, 0)

	// Load into a new store.
	s2, err := Load(path, rootKey, hmacKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if !s2.Check(pid, "file-transfer") {
		t.Fatal("loaded store should have the grant")
	}
	if !s2.Check(pid, "file-browse") {
		t.Fatal("loaded store should allow file-browse")
	}
	if s2.Check(pid, "file-download") {
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
	s.Grant(pid, 1*time.Hour, nil, false, 0)

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
	s.Grant(pid, 1*time.Hour, nil, false, 0)

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
	s.Grant(pid, 1*time.Hour, nil, false, 0)

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
	s.Grant(pid, 5*time.Millisecond, nil, false, 0)
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

	s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0)
	s.Grant(pid, 2*time.Hour, nil, false, 0) // replace

	// New grant should have no service restriction.
	if !s.Check(pid, "file-transfer") {
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

	s.Grant(pid, 1*time.Hour, []string{"file-browse"}, false, 0)

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

	s.Grant(pid, 1*time.Hour, nil, false, 0)

	// Tamper with the token's caveats.
	s.mu.Lock()
	g := s.grants[pid]
	if len(g.Token.Caveats) > 0 {
		g.Token.Caveats[0] = "peer_id=12D3KooWFAKEFAKEFAKE"
	}
	s.mu.Unlock()

	if s.Check(pid, "file-transfer") {
		t.Fatal("tampered token should fail verification")
	}
}

func TestGrantWithMaxDelegations(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 1*time.Hour, nil, false, 3)
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
	if !s.Check(pid, "file-transfer") {
		t.Fatal("grant with max_delegations should verify")
	}
}

func TestGrantWithoutMaxDelegations(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	s := NewStore(rootKey, hmacKey)
	pid := genPeerID(t)

	g, err := s.Grant(pid, 1*time.Hour, nil, false, 0)
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

	s.Grant(pid, 1*time.Hour, nil, false, 5)
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
