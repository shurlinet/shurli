package grants

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shurlinet/shurli/internal/macaroon"
)

func testMacaroon(t *testing.T) *macaroon.Macaroon {
	t.Helper()
	rootKey := make([]byte, 32)
	return macaroon.New("test-node", rootKey, "test-grant")
}

func TestPouchAddAndGet(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	// No token yet.
	if got := p.Get(issuer, "file-transfer"); got != nil {
		t.Fatal("should not have token before add")
	}

	// Add token.
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Get should return token for any service (nil services = all).
	if got := p.Get(issuer, "file-transfer"); got == nil {
		t.Fatal("should have token after add")
	}
	if got := p.Get(issuer, "file-browse"); got == nil {
		t.Fatal("nil services should match all")
	}
}

func TestPouchGetWithServices(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, []string{"file-browse"}, time.Now().Add(1*time.Hour), false)

	if got := p.Get(issuer, "file-browse"); got == nil {
		t.Fatal("file-browse should match")
	}
	if got := p.Get(issuer, "file-transfer"); got != nil {
		t.Fatal("file-transfer should NOT match")
	}
}

func TestPouchGetExpired(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, nil, time.Now().Add(1*time.Millisecond), false)
	time.Sleep(5 * time.Millisecond)

	if got := p.Get(issuer, "file-transfer"); got != nil {
		t.Fatal("expired token should not be returned")
	}
}

func TestPouchGetPermanent(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, nil, time.Time{}, true)

	if got := p.Get(issuer, "file-transfer"); got == nil {
		t.Fatal("permanent token should be returned")
	}
}

func TestPouchRemove(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	if !p.Remove(issuer) {
		t.Fatal("remove should return true for existing entry")
	}
	if p.Remove(issuer) {
		t.Fatal("remove should return false for nonexistent entry")
	}
	if got := p.Get(issuer, "file-transfer"); got != nil {
		t.Fatal("should not have token after remove")
	}
}

func TestPouchList(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)

	issuer1 := genPeerID(t)
	issuer2 := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer1, token, nil, time.Now().Add(1*time.Hour), false)
	p.Add(issuer2, token, []string{"file-browse"}, time.Now().Add(2*time.Hour), false)

	list := p.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(list))
	}
}

func TestPouchListReturnsCopies(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, []string{"file-browse"}, time.Now().Add(1*time.Hour), false)

	list := p.List()
	list[0].Services = append(list[0].Services, "evil-service")

	// Verify internal state unchanged.
	p.mu.RLock()
	e := p.entries[issuer]
	p.mu.RUnlock()
	if len(e.Services) != 1 || e.Services[0] != "file-browse" {
		t.Fatal("List() should return copies, not references")
	}
}

func TestPouchCleanExpired(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)

	issuer1 := genPeerID(t)
	issuer2 := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer1, token, nil, time.Now().Add(1*time.Millisecond), false)
	p.Add(issuer2, token, nil, time.Now().Add(1*time.Hour), false)

	time.Sleep(5 * time.Millisecond)

	removed := p.CleanExpired()
	if removed != 1 {
		t.Fatalf("expected 1 removed, got %d", removed)
	}

	list := p.List()
	if len(list) != 1 {
		t.Fatalf("expected 1 remaining, got %d", len(list))
	}
}

func TestPouchReplaceExisting(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, []string{"file-browse"}, time.Now().Add(1*time.Hour), false)
	p.Add(issuer, token, nil, time.Now().Add(2*time.Hour), false) // replace

	// Should now match all services.
	if got := p.Get(issuer, "file-transfer"); got == nil {
		t.Fatal("replacement should allow all services")
	}
}

func TestPouchPersistAndLoad(t *testing.T) {
	_, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_pouch.json")

	p := NewPouch(hmacKey)
	p.SetPersistPath(path)

	issuer := genPeerID(t)
	token := testMacaroon(t)
	p.Add(issuer, token, []string{"file-transfer"}, time.Now().Add(1*time.Hour), false)

	// Load into new pouch.
	p2, err := LoadPouch(path, hmacKey)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	if got := p2.Get(issuer, "file-transfer"); got == nil {
		t.Fatal("loaded pouch should have the token")
	}
	if got := p2.Get(issuer, "file-browse"); got != nil {
		t.Fatal("loaded pouch should NOT match file-browse")
	}
}

func TestPouchPersistHMACTamperDetection(t *testing.T) {
	_, hmacKey := genKeys(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_pouch.json")

	p := NewPouch(hmacKey)
	p.SetPersistPath(path)

	issuer := genPeerID(t)
	token := testMacaroon(t)
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Tamper with file.
	data, _ := os.ReadFile(path)
	tampered := append(data[:len(data)-5], []byte("XXXXX")...)
	os.WriteFile(path, tampered, 0600)

	_, err := LoadPouch(path, hmacKey)
	if err == nil {
		t.Fatal("should detect tampered file")
	}
}

func TestPouchLoadNonexistent(t *testing.T) {
	_, hmacKey := genKeys(t)
	p, err := LoadPouch("/nonexistent/path/grant_pouch.json", hmacKey)
	if err != nil {
		t.Fatalf("load nonexistent: %v", err)
	}
	if len(p.List()) != 0 {
		t.Fatal("should have empty entries")
	}
}

func TestPouchSymlinkRejection(t *testing.T) {
	_, hmacKey := genKeys(t)
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	linkPath := filepath.Join(dir, "grant_pouch.json")

	os.WriteFile(realPath, []byte("{}"), 0600)
	os.Symlink(realPath, linkPath)

	p := NewPouch(hmacKey)
	p.SetPersistPath(linkPath)

	issuer := genPeerID(t)
	token := testMacaroon(t)
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Real file should be unchanged.
	data, _ := os.ReadFile(realPath)
	if string(data) != "{}" {
		t.Fatal("symlink write should have been rejected")
	}
}

func TestPouchGetReturnsCopy(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	token := testMacaroon(t)

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	got := p.Get(issuer, "file-transfer")
	if got == nil {
		t.Fatal("should have token")
	}

	// Mutate returned token - should not affect pouch.
	got.AddFirstPartyCaveat("evil=true")

	got2 := p.Get(issuer, "file-transfer")
	for _, c := range got2.Caveats {
		if c == "evil=true" {
			t.Fatal("Get() should return a clone, not the internal token")
		}
	}
}
