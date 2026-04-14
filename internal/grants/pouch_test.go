package grants

import (
	"os"
	"path/filepath"
	"strings"
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

func TestPouchDelegateBasic(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	// Create a token with max_delegations=3 and add to pouch.
	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String()) // original grantee doesn't matter for this test
	token.AddFirstPartyCaveat("max_delegations=3")

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Delegate to target.
	subToken, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, 0)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// Verify sub-token has delegate_to and max_delegations caveats.
	hasDelegateTo := false
	hasMaxDel := false
	for _, c := range subToken.Caveats {
		if c == "delegate_to="+target.String() {
			hasDelegateTo = true
		}
		if c == "max_delegations=0" {
			// Requested 0, original was 3, decremented would be 2, but 0 < 2 so 0 wins.
			hasMaxDel = true
		}
	}
	if !hasDelegateTo {
		t.Error("sub-token should have delegate_to caveat")
	}
	if !hasMaxDel {
		t.Error("sub-token should have max_delegations=0 caveat")
	}

	// Verify the sub-token verifies against the root key.
	ctx := macaroon.VerifyContext{
		PeerID:     target.String(),
		DelegateTo: target.String(),
		Now:        time.Now(),
	}
	v := macaroon.DefaultVerifier(ctx)
	if err := subToken.Verify(rootKey, v); err != nil {
		t.Fatalf("sub-token should verify: %v", err)
	}
}

func TestPouchDelegateNoDelegationAllowed(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	// Token without max_delegations caveat: default is 0 (no delegation).
	token := testMacaroon(t)
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	_, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, 0)
	if err == nil {
		t.Fatal("should fail: token does not allow delegation")
	}
}

func TestPouchDelegateUnlimited(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String())
	token.AddFirstPartyCaveat("max_delegations=-1") // unlimited

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	subToken, err := p.Delegate(issuer, target, 0, nil, 5, 0)
	if err != nil {
		t.Fatalf("delegate with unlimited: %v", err)
	}

	// Should have max_delegations=5 (caller requested).
	found := false
	for _, c := range subToken.Caveats {
		if c == "max_delegations=5" {
			found = true
		}
	}
	if !found {
		t.Error("sub-token should have max_delegations=5 from caller request")
	}
}

func TestPouchDelegateLimitedDecrement(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String())
	token.AddFirstPartyCaveat("max_delegations=3")

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Request unlimited (-1) but original is 3, so decremented to 2.
	subToken, err := p.Delegate(issuer, target, 0, nil, -1, 0)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	found := false
	for _, c := range subToken.Caveats {
		if c == "max_delegations=2" {
			found = true
		}
	}
	if !found {
		t.Errorf("sub-token should have max_delegations=2 (decremented from 3), got caveats: %v", subToken.Caveats)
	}
}

func TestPouchDelegateExpired(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	token := testMacaroon(t)
	token.AddFirstPartyCaveat("max_delegations=3")
	p.Add(issuer, token, nil, time.Now().Add(1*time.Millisecond), false)
	time.Sleep(5 * time.Millisecond)

	_, err := p.Delegate(issuer, target, 0, nil, 0, 0)
	if err == nil {
		t.Fatal("should fail: token expired")
	}
}

func TestPouchDelegateChainedMaxDelegations(t *testing.T) {
	// Regression test for S1: extractMaxDelegations must return the MINIMUM
	// value when multiple max_delegations caveats exist (delegation chain).
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	// Simulate a token from a delegation chain:
	// original had max_delegations=5, first delegatee added max_delegations=2
	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String())
	token.AddFirstPartyCaveat("max_delegations=5")  // original
	token.AddFirstPartyCaveat("max_delegations=2")  // added by first delegatee

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	subToken, err := p.Delegate(issuer, target, 0, nil, 0, 0)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// The new max_delegations should be based on min(5,2)-1 = 1, capped by request(0) = 0.
	found := false
	for _, c := range subToken.Caveats {
		if c == "max_delegations=0" {
			found = true
		}
	}
	if !found {
		t.Errorf("chained token should have max_delegations=0 (min(5,2)=2, decremented=1, request=0 wins), got caveats: %v", subToken.Caveats)
	}
}

func TestExtractMaxDelegationsMostRestrictive(t *testing.T) {
	// Unit test for extractMaxDelegations finding the minimum.
	tests := []struct {
		caveats []string
		want    int
	}{
		{[]string{"max_delegations=5", "max_delegations=2"}, 2},
		{[]string{"max_delegations=-1", "max_delegations=3"}, 3},
		{[]string{"max_delegations=-1", "max_delegations=-1"}, -1},
		{[]string{"max_delegations=5", "max_delegations=0"}, 0},
		{[]string{"max_delegations=0"}, 0},
		{[]string{}, 0},                                          // no caveat = no delegation
		{[]string{"max_delegations=bad"}, 0},                     // malformed = deny
	}
	for _, tt := range tests {
		got := extractMaxDelegations(tt.caveats)
		if got != tt.want {
			t.Errorf("extractMaxDelegations(%v) = %d, want %d", tt.caveats, got, tt.want)
		}
	}
}

func TestPouchDelegateWithServiceRestriction(t *testing.T) {
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String())
	token.AddFirstPartyCaveat("max_delegations=1")

	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	subToken, err := p.Delegate(issuer, target, 0, []string{"file-browse"}, 0, 0)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// Should have service caveat.
	found := false
	for _, c := range subToken.Caveats {
		if c == "service=file-browse" {
			found = true
		}
	}
	if !found {
		t.Error("sub-token should have service=file-browse caveat")
	}
}

func TestPouchDelegateMultiHopEndToEnd(t *testing.T) {
	// End-to-end test: Node A -> B -> C -> D delegation chain.
	// D must be able to present the token and have it verified by Node A.
	rootKey, hmacKey := genKeys(t)

	peerB := genPeerID(t)
	peerC := genPeerID(t)
	peerD := genPeerID(t)

	// Step 1: Node A creates a grant for Peer B with max_delegations=3.
	store := NewStore(rootKey, hmacKey)
	grantB, err := store.Grant(peerB, 1*time.Hour, nil, false, 3, 0)
	if err != nil {
		t.Fatalf("grant to B: %v", err)
	}

	// Step 2: Peer B receives the token in their pouch.
	pouchB := NewPouch(hmacKey)
	pouchB.Add(peerB, grantB.Token, nil, grantB.ExpiresAt, false)

	// Step 3: Peer B delegates to Peer C with max_delegations=2.
	// (issuerID is peerB because the pouch is keyed by "who gave me this token",
	//  but for this test we simulate B holding A's token keyed by B's own ID)
	subTokenC, err := pouchB.Delegate(peerB, peerC, 30*time.Minute, nil, 1, 0)
	if err != nil {
		t.Fatalf("B delegate to C: %v", err)
	}

	// Step 4: Peer C receives the sub-token in their pouch.
	pouchC := NewPouch(hmacKey)
	pouchC.Add(peerB, subTokenC, nil, time.Now().Add(30*time.Minute), false)

	// Step 5: Peer C delegates to Peer D with max_delegations=0.
	subTokenD, err := pouchC.Delegate(peerB, peerD, 15*time.Minute, nil, 0, 0)
	if err != nil {
		t.Fatalf("C delegate to D: %v", err)
	}

	// Step 6: Peer D presents the token to Node A. Verify it works.
	// This simulates what TokenVerifier does on the inbound side.
	delegateTo := macaroon.ExtractDelegateTo(subTokenD.Caveats)
	if delegateTo != peerD.String() {
		t.Fatalf("ExtractDelegateTo should return D, got %q", delegateTo)
	}

	verifier := macaroon.DefaultVerifier(macaroon.VerifyContext{
		PeerID:     peerD.String(),
		Service:    "file-browse",
		DelegateTo: delegateTo,
		Now:        time.Now(),
	})

	if err := subTokenD.Verify(rootKey, verifier); err != nil {
		t.Fatalf("D's token should verify against Node A's root key: %v", err)
	}

	// Step 7: Verify D cannot further delegate (max_delegations exhausted).
	pouchD := NewPouch(hmacKey)
	pouchD.Add(peerB, subTokenD, nil, time.Now().Add(15*time.Minute), false)
	_, err = pouchD.Delegate(peerB, genPeerID(t), 5*time.Minute, nil, 0, 0)
	if err == nil {
		t.Fatal("D should not be able to delegate further (max_delegations=0)")
	}

	// Step 8: Verify an unauthorized peer E cannot use D's token.
	peerE := genPeerID(t)
	delegateToE := macaroon.ExtractDelegateTo(subTokenD.Caveats)
	verifierE := macaroon.DefaultVerifier(macaroon.VerifyContext{
		PeerID:     peerE.String(),
		Service:    "file-browse",
		DelegateTo: delegateToE, // still "D", not "E"
		Now:        time.Now(),
	})
	if err := subTokenD.Verify(rootKey, verifierE); err == nil {
		t.Fatal("E should not be able to use D's token")
	}
}

func TestPouchDelegateInheritsParentExpiry(t *testing.T) {
	// Regression test: when delegating without explicit duration, the sub-token
	// inherits the parent's expires caveat. ExtractEarliestExpires must return
	// a valid (non-zero, future) time from the sub-token's caveats so the
	// delivery protocol can set correct pouch metadata.
	rootKey, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	parentExpiry := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	token := macaroon.New("test-node", rootKey, "test-grant")
	token.AddFirstPartyCaveat("peer_id=" + issuer.String())
	token.AddFirstPartyCaveat("max_delegations=3")
	token.AddFirstPartyCaveat("expires=" + parentExpiry.Format(time.RFC3339))

	p.Add(issuer, token, nil, parentExpiry, false)

	// Delegate with duration=0 (inherit parent's expiry).
	subToken, err := p.Delegate(issuer, target, 0, nil, 0, 0)
	if err != nil {
		t.Fatalf("delegate: %v", err)
	}

	// The sub-token must have an extractable expires caveat from the parent.
	extracted := macaroon.ExtractEarliestExpires(subToken.Caveats)
	if extracted.IsZero() {
		t.Fatal("sub-token should inherit parent's expires caveat, got zero time")
	}
	if !extracted.Equal(parentExpiry) {
		t.Errorf("extracted expiry %v != parent expiry %v", extracted, parentExpiry)
	}
}

// TestPouchDelegateTransportWidenRejected verifies that attempting to
// delegate with a transport mask wider than the parent's transport caveat
// is rejected at the pouch boundary. Without this check, widening silently
// narrows to the parent via AND — surprising the user.
func TestPouchDelegateTransportWidenRejected(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	// Parent token with transport=direct (no relay).
	token := testMacaroon(t)
	token.AddFirstPartyCaveat("max_delegations=2")
	token.AddFirstPartyCaveat("transport=direct")
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	// Try to delegate asking for relay — must fail fast.
	_, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, macaroon.TransportRelay)
	if err == nil {
		t.Fatal("expected widen error, got nil")
	}
	if !strings.Contains(err.Error(), "transport attenuation violation") {
		t.Errorf("error %q missing attenuation violation text", err.Error())
	}

	// Same-set delegation (direct → direct) must succeed.
	subToken, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, macaroon.TransportDirect)
	if err != nil {
		t.Fatalf("same-set delegate: %v", err)
	}
	if subToken == nil {
		t.Fatal("expected non-nil sub-token")
	}

	// Pure narrowing (direct → nothing new requested) must succeed.
	subToken2, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, 0)
	if err != nil {
		t.Fatalf("no-op transport delegate: %v", err)
	}
	if subToken2 == nil {
		t.Fatal("expected non-nil sub-token")
	}
}

// TestPouchDelegateTransportNarrowFromPermissive verifies the legit
// narrowing path: parent is wide (lan,direct,relay), child picks a subset.
func TestPouchDelegateTransportNarrowFromPermissive(t *testing.T) {
	_, hmacKey := genKeys(t)
	p := NewPouch(hmacKey)
	issuer := genPeerID(t)
	target := genPeerID(t)

	token := testMacaroon(t)
	token.AddFirstPartyCaveat("max_delegations=2")
	token.AddFirstPartyCaveat("transport=lan,direct,relay")
	p.Add(issuer, token, nil, time.Now().Add(1*time.Hour), false)

	subToken, err := p.Delegate(issuer, target, 30*time.Minute, nil, 0, macaroon.TransportLAN|macaroon.TransportDirect)
	if err != nil {
		t.Fatalf("narrow delegate: %v", err)
	}

	// Child token's effective mask is the intersection — LAN+Direct.
	eff, err := macaroon.EffectiveTransportMask(subToken.Caveats)
	if err != nil {
		t.Fatalf("effective mask: %v", err)
	}
	if eff != (macaroon.TransportLAN | macaroon.TransportDirect) {
		t.Errorf("effective mask = %d, want lan|direct (%d)", eff, macaroon.TransportLAN|macaroon.TransportDirect)
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
