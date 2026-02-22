package relay

import (
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func genPeerID(t testing.TB) peer.ID {
	t.Helper()
	priv, _, _ := crypto.GenerateKeyPair(crypto.Ed25519, -1)
	pid, _ := peer.IDFromPrivateKey(priv)
	return pid
}

func TestCreateGroupSingle(t *testing.T) {
	ts := NewTokenStore()
	tokens, groupID, err := ts.CreateGroup(1, time.Hour, "", 0)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("got %d tokens, want 1", len(tokens))
	}
	if len(tokens[0]) != TokenSize {
		t.Errorf("token size = %d, want %d", len(tokens[0]), TokenSize)
	}
	if groupID == "" {
		t.Error("group ID should not be empty")
	}
	if ts.ActiveGroupCount() != 1 {
		t.Error("should have 1 active group")
	}
}

func TestCreateGroupMultiple(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, err := ts.CreateGroup(5, time.Hour, "mynet", 0)
	if err != nil {
		t.Fatalf("CreateGroup: %v", err)
	}
	if len(tokens) != 5 {
		t.Fatalf("got %d tokens, want 5", len(tokens))
	}
	// Verify all tokens are unique
	seen := make(map[string]bool)
	for i, tok := range tokens {
		key := string(tok)
		if seen[key] {
			t.Errorf("token %d is a duplicate", i)
		}
		seen[key] = true
	}
}

func TestCreateGroupInvalidCount(t *testing.T) {
	ts := NewTokenStore()
	_, _, err := ts.CreateGroup(0, time.Hour, "", 0)
	if err == nil {
		t.Error("should reject count=0")
	}
}

func TestValidateAndUseSolo(t *testing.T) {
	ts := NewTokenStore()
	tokens, groupID, _ := ts.CreateGroup(1, time.Hour, "", 0)
	pid := genPeerID(t)

	group, idx, err := ts.ValidateAndUse(tokens[0], pid, "dad")
	if err != nil {
		t.Fatalf("ValidateAndUse: %v", err)
	}
	if group.ID != groupID {
		t.Errorf("group ID mismatch")
	}
	if idx != 0 {
		t.Errorf("slot index = %d, want 0", idx)
	}
	if !ts.IsGroupComplete(groupID) {
		t.Error("solo group should be complete after one use")
	}
}

func TestValidateAndUsePair(t *testing.T) {
	ts := NewTokenStore()
	tokens, groupID, _ := ts.CreateGroup(2, time.Hour, "", 0)
	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	// First peer joins
	_, idx1, err := ts.ValidateAndUse(tokens[0], pid1, "mum")
	if err != nil {
		t.Fatalf("first use: %v", err)
	}
	if ts.IsGroupComplete(groupID) {
		t.Error("should not be complete after 1 of 2")
	}

	// Second peer joins
	_, idx2, err := ts.ValidateAndUse(tokens[1], pid2, "dad")
	if err != nil {
		t.Fatalf("second use: %v", err)
	}
	if idx1 == idx2 {
		t.Error("slot indices should differ")
	}
	if !ts.IsGroupComplete(groupID) {
		t.Error("should be complete after 2 of 2")
	}

	// Verify peer discovery
	peers1 := ts.GetGroupPeers(groupID, idx1)
	if len(peers1) != 1 || peers1[0].PeerID != pid2 {
		t.Errorf("peer1 should see peer2, got %d peers", len(peers1))
	}
	peers2 := ts.GetGroupPeers(groupID, idx2)
	if len(peers2) != 1 || peers2[0].PeerID != pid1 {
		t.Errorf("peer2 should see peer1, got %d peers", len(peers2))
	}
}

func TestValidateAndUseRejectsDoubleUse(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(1, time.Hour, "", 0)
	pid := genPeerID(t)

	_, _, err := ts.ValidateAndUse(tokens[0], pid, "mum")
	if err != nil {
		t.Fatalf("first use: %v", err)
	}

	// Same token again
	_, _, err = ts.ValidateAndUse(tokens[0], genPeerID(t), "dad")
	if err == nil {
		t.Error("should reject double use")
	}
}

func TestValidateAndUseRejectsExpired(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(1, time.Millisecond, "", 0)

	time.Sleep(5 * time.Millisecond)

	_, _, err := ts.ValidateAndUse(tokens[0], genPeerID(t), "mum")
	if err == nil {
		t.Error("should reject expired token")
	}
}

func TestValidateAndUseRejectsUnknown(t *testing.T) {
	ts := NewTokenStore()
	ts.CreateGroup(1, time.Hour, "", 0)

	fakeToken := make([]byte, TokenSize)
	_, _, err := ts.ValidateAndUse(fakeToken, genPeerID(t), "attacker")
	if err == nil {
		t.Error("should reject unknown token")
	}
}

func TestValidateAndUseRejectsWrongSize(t *testing.T) {
	ts := NewTokenStore()
	_, _, err := ts.ValidateAndUse([]byte("short"), genPeerID(t), "x")
	if err == nil {
		t.Error("should reject wrong-size token")
	}
}

func TestBurnedAfterMaxAttempts(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(1, time.Hour, "", 0)

	// Burn the code with failed attempts
	for i := 0; i < maxAttempts; i++ {
		ts.RecordFailedAttempt(tokens[0])
	}

	// Now even with correct token, it should be burned
	_, _, err := ts.ValidateAndUse(tokens[0], genPeerID(t), "mum")
	if err == nil {
		t.Error("should reject burned token")
	}
}

func TestCleanExpired(t *testing.T) {
	ts := NewTokenStore()
	ts.CreateGroup(1, time.Millisecond, "", 0)
	ts.CreateGroup(1, time.Hour, "", 0)

	time.Sleep(5 * time.Millisecond)

	removed := ts.CleanExpired()
	if removed != 1 {
		t.Errorf("removed %d, want 1", removed)
	}
	if ts.ActiveGroupCount() != 1 {
		t.Errorf("active = %d, want 1", ts.ActiveGroupCount())
	}
}

func TestList(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(2, time.Hour, "testnet", 0)
	pid := genPeerID(t)
	ts.ValidateAndUse(tokens[0], pid, "mum")

	infos := ts.List()
	if len(infos) != 1 {
		t.Fatalf("got %d groups, want 1", len(infos))
	}
	if infos[0].Namespace != "testnet" {
		t.Errorf("namespace = %q", infos[0].Namespace)
	}
	if infos[0].Total != 2 {
		t.Errorf("total = %d, want 2", infos[0].Total)
	}
	if infos[0].Used != 1 {
		t.Errorf("used = %d, want 1", infos[0].Used)
	}
	if len(infos[0].Peers) != 1 || infos[0].Peers[0].Name != "mum" {
		t.Error("peer info mismatch")
	}
}

func TestRevoke(t *testing.T) {
	ts := NewTokenStore()
	tokens, groupID, _ := ts.CreateGroup(1, time.Hour, "", 0)

	if err := ts.Revoke(groupID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	// Token should no longer work
	_, _, err := ts.ValidateAndUse(tokens[0], genPeerID(t), "mum")
	if err == nil {
		t.Error("should reject token from revoked group")
	}

	// Second revoke should fail
	if err := ts.Revoke(groupID); err == nil {
		t.Error("should error on already-revoked group")
	}
}

func TestGetGroupPeersExcludesSelf(t *testing.T) {
	ts := NewTokenStore()
	tokens, groupID, _ := ts.CreateGroup(3, time.Hour, "", 0)
	pids := []peer.ID{genPeerID(t), genPeerID(t), genPeerID(t)}

	for i, tok := range tokens {
		ts.ValidateAndUse(tok, pids[i], []string{"mum", "dad", "bro"}[i])
	}

	// Each peer should see the other 2
	for i := 0; i < 3; i++ {
		peers := ts.GetGroupPeers(groupID, i)
		if len(peers) != 2 {
			t.Errorf("peer %d sees %d peers, want 2", i, len(peers))
		}
		for _, p := range peers {
			if p.PeerID == pids[i] {
				t.Errorf("peer %d should not see self", i)
			}
		}
	}
}

func TestGetGroupPeersUnknownGroup(t *testing.T) {
	ts := NewTokenStore()
	peers := ts.GetGroupPeers("nonexistent", 0)
	if peers != nil {
		t.Error("should return nil for unknown group")
	}
}

func TestGroupCount(t *testing.T) {
	ts := NewTokenStore()
	_, groupID, _ := ts.CreateGroup(3, time.Hour, "", 0)
	if ts.GroupCount(groupID) != 3 {
		t.Errorf("count = %d, want 3", ts.GroupCount(groupID))
	}
	if ts.GroupCount("nonexistent") != 0 {
		t.Error("unknown group should return 0")
	}
}

func TestPeerTTL(t *testing.T) {
	ts := NewTokenStore()
	_, groupID, _ := ts.CreateGroup(1, time.Hour, "", 30*time.Minute)

	// Verify PeerTTL is stored on the group
	ts.mu.RLock()
	group := ts.groups[groupID]
	ts.mu.RUnlock()

	if group.PeerTTL != 30*time.Minute {
		t.Errorf("PeerTTL = %v, want 30m", group.PeerTTL)
	}
}

func TestConcurrentValidation(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(10, time.Hour, "", 0)

	var wg sync.WaitGroup
	errors := make(chan error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			pid := genPeerID(t)
			_, _, err := ts.ValidateAndUse(tokens[idx], pid, "peer")
			if err != nil {
				errors <- err
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Errorf("concurrent validation error: %v", err)
	}
}

func TestConcurrentSameToken(t *testing.T) {
	ts := NewTokenStore()
	tokens, _, _ := ts.CreateGroup(1, time.Hour, "", 0)

	var wg sync.WaitGroup
	successes := make(chan int, 10)

	// 10 goroutines race on the same token
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pid := genPeerID(t)
			_, _, err := ts.ValidateAndUse(tokens[0], pid, "racer")
			if err == nil {
				successes <- 1
			}
		}()
	}

	wg.Wait()
	close(successes)

	count := 0
	for range successes {
		count++
	}
	if count != 1 {
		t.Errorf("exactly 1 goroutine should succeed, got %d", count)
	}
}

func TestUniformErrorMessages(t *testing.T) {
	// All token validation errors should have the same message
	// to prevent information leakage.
	expected := "pairing failed"
	if ErrTokenNotFound.Error() != expected {
		t.Errorf("ErrTokenNotFound = %q", ErrTokenNotFound.Error())
	}
	if ErrTokenUsed.Error() != expected {
		t.Errorf("ErrTokenUsed = %q", ErrTokenUsed.Error())
	}
	if ErrTokenBurned.Error() != expected {
		t.Errorf("ErrTokenBurned = %q", ErrTokenBurned.Error())
	}
	if ErrTokenExpired.Error() != expected {
		t.Errorf("ErrTokenExpired = %q", ErrTokenExpired.Error())
	}
}
