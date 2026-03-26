package grants

import (
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestGrantCachePutGet(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	receipt := &GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 2 << 30, // 2GB
		SessionDuration:  2 * time.Hour,
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	}
	gc.Put(receipt)

	got := gc.Get(relay1)
	if got == nil {
		t.Fatal("expected receipt, got nil")
	}
	if got.GrantDuration != 2*time.Hour {
		t.Errorf("GrantDuration = %v, want 2h", got.GrantDuration)
	}
	if got.SessionDataLimit != 2<<30 {
		t.Errorf("SessionDataLimit = %d, want %d", got.SessionDataLimit, 2<<30)
	}

	// Get returns a copy, not a reference.
	got.GrantDuration = 99 * time.Hour
	got2 := gc.Get(relay1)
	if got2.GrantDuration != 2*time.Hour {
		t.Error("Get returned a reference, not a copy")
	}
}

func TestGrantCacheMultipleRelays(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)
	relay2 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})
	gc.Put(&GrantReceipt{
		RelayPeerID:   relay2,
		GrantDuration: 4 * time.Hour,
		Permanent:     true,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})

	if gc.Len() != 2 {
		t.Fatalf("Len = %d, want 2", gc.Len())
	}
	if gc.Get(relay1).GrantDuration != 1*time.Hour {
		t.Error("relay1 duration mismatch")
	}
	if !gc.Get(relay2).Permanent {
		t.Error("relay2 should be permanent")
	}
}

func TestGrantCacheDelete(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})
	gc.Delete(relay1)

	if gc.Get(relay1) != nil {
		t.Error("expected nil after delete")
	}
	if gc.Len() != 0 {
		t.Error("expected empty cache after delete")
	}
}

func TestGrantCachePersistenceRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_cache.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	relay1 := genPeerID(t)
	now := time.Now().Truncate(time.Second) // JSON time precision

	// Write.
	gc := NewGrantCache(hmacKey)
	gc.SetPersistPath(path)
	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 1 << 30,
		SessionDuration:  1 * time.Hour,
		Permanent:        false,
		ReceivedAt:       now,
		IssuedAt:         now.Add(-1 * time.Second),
	})

	// Reload.
	gc2, err := LoadGrantCache(path, hmacKey)
	if err != nil {
		t.Fatalf("LoadGrantCache: %v", err)
	}

	got := gc2.Get(relay1)
	if got == nil {
		t.Fatal("expected receipt after reload")
	}
	if got.GrantDuration != 2*time.Hour {
		t.Errorf("GrantDuration = %v, want 2h", got.GrantDuration)
	}
	if got.SessionDataLimit != 1<<30 {
		t.Errorf("SessionDataLimit = %d, want %d", got.SessionDataLimit, 1<<30)
	}
	if got.SessionDuration != 1*time.Hour {
		t.Errorf("SessionDuration = %v, want 1h", got.SessionDuration)
	}

	// Circuit counters should NOT be persisted.
	if got.CircuitBytesSent != 0 || got.CircuitBytesReceived != 0 {
		t.Error("circuit counters should be zero after reload")
	}
}

func TestGrantCacheHMACIntegrity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_cache.json")
	hmacKey := []byte("test-hmac-key-32-bytes-long!!!!!")

	gc := NewGrantCache(hmacKey)
	gc.SetPersistPath(path)
	gc.Put(&GrantReceipt{
		RelayPeerID:   genPeerID(t),
		GrantDuration: 1 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})

	// Tamper with the file.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Flip a byte in the middle.
	data[len(data)/2] ^= 0xFF
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}

	_, err = LoadGrantCache(path, hmacKey)
	if err == nil {
		t.Fatal("expected HMAC verification failure on tampered file")
	}
}

func TestGrantCacheExpiryCleanup(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	// Insert an already-expired receipt.
	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Second,
		ReceivedAt:    time.Now().Add(-2 * time.Second), // expired 1s ago
		IssuedAt:      time.Now().Add(-2 * time.Second),
	})

	if gc.Len() != 1 {
		t.Fatal("expected 1 entry before cleanup")
	}

	gc.cleanExpired()

	if gc.Len() != 0 {
		t.Fatal("expected 0 entries after cleanup")
	}
}

func TestGrantCachePermanentNeverExpires(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID: relay1,
		Permanent:   true,
		ReceivedAt:  time.Now().Add(-100 * 24 * time.Hour), // 100 days ago
		IssuedAt:    time.Now().Add(-100 * 24 * time.Hour),
	})

	gc.cleanExpired()

	if gc.Len() != 1 {
		t.Fatal("permanent grant should never be cleaned up")
	}

	got := gc.Get(relay1)
	if got.Expired() {
		t.Error("permanent grant should never report expired")
	}
	if got.Remaining() <= 0 {
		t.Error("permanent grant should have positive remaining")
	}
}

func TestGrantCacheRevocationOrdering(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)
	now := time.Now()

	// Grant issued at T=10.
	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 2 * time.Hour,
		ReceivedAt:    now,
		IssuedAt:      now.Add(-10 * time.Second), // issued 10s ago
	})

	// Stale revocation issued at T=5 (before grant). Should be ignored (H12).
	gc.HandleRevocation(relay1, now.Add(-15*time.Second))

	if gc.Get(relay1) == nil {
		t.Fatal("stale revocation should not clear a newer grant")
	}

	// Fresh revocation issued after the grant. Should clear.
	gc.HandleRevocation(relay1, now)

	if gc.Get(relay1) != nil {
		t.Fatal("fresh revocation should clear the grant")
	}
}

func TestGrantCacheCircuitBytesTracking(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 1 << 30, // 1GB
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	})

	// Track some bytes.
	gc.TrackCircuitBytes(relay1, "send", 500<<20) // 500MB sent
	gc.TrackCircuitBytes(relay1, "recv", 200<<20) // 200MB received

	got := gc.Get(relay1)
	if got.CircuitBytesSent != 500<<20 {
		t.Errorf("CircuitBytesSent = %d, want %d", got.CircuitBytesSent, 500<<20)
	}
	if got.CircuitBytesReceived != 200<<20 {
		t.Errorf("CircuitBytesReceived = %d, want %d", got.CircuitBytesReceived, 200<<20)
	}

	// Check budget.
	sendBudget := got.RemainingBudget("send")
	expectedSend := int64(1<<30) - int64(500<<20)
	if sendBudget != expectedSend {
		t.Errorf("send budget = %d, want %d", sendBudget, expectedSend)
	}

	// Check HasSufficientBudget.
	if !gc.HasSufficientBudget(relay1, 100<<20, "send") { // 100MB should fit
		t.Error("100MB should fit in remaining send budget")
	}
	if gc.HasSufficientBudget(relay1, 600<<20, "send") { // 600MB should NOT fit
		t.Error("600MB should NOT fit in remaining send budget")
	}

	// Reset counters.
	gc.ResetCircuitCounters(relay1)
	got = gc.Get(relay1)
	if got.CircuitBytesSent != 0 || got.CircuitBytesReceived != 0 {
		t.Error("circuit counters should be zero after reset")
	}
}

func TestGrantCacheUnlimitedBudget(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 0, // unlimited
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	})

	got := gc.Get(relay1)
	if !got.HasSufficientBudget(1<<40, "send") { // 1TB should fit unlimited
		t.Error("unlimited budget should accept any size")
	}
}

func TestGrantCacheNonExistentFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nonexistent.json")

	gc, err := LoadGrantCache(path, nil)
	if err != nil {
		t.Fatalf("expected no error for non-existent file, got: %v", err)
	}
	if gc.Len() != 0 {
		t.Error("expected empty cache from non-existent file")
	}
}

func TestGrantCacheClockDrift(t *testing.T) {
	r := &GrantReceipt{
		ReceivedAt: time.Now(),
		IssuedAt:   time.Now().Add(-30 * time.Second), // relay 30s behind
	}
	drift := r.ClockDrift()
	if drift < 29*time.Second || drift > 31*time.Second {
		t.Errorf("ClockDrift = %v, expected ~30s", drift)
	}
}

func TestGrantCacheReceiptExpiry(t *testing.T) {
	now := time.Now()

	// Active receipt.
	active := &GrantReceipt{
		GrantDuration: 2 * time.Hour,
		ReceivedAt:    now,
	}
	if active.Expired() {
		t.Error("active receipt should not be expired")
	}
	if active.Remaining() <= 0 {
		t.Error("active receipt should have positive remaining")
	}
	expiry := active.ExpiresAt()
	if expiry.Before(now.Add(1*time.Hour + 59*time.Minute)) {
		t.Error("ExpiresAt too early")
	}

	// Expired receipt.
	expired := &GrantReceipt{
		GrantDuration: 1 * time.Second,
		ReceivedAt:    now.Add(-5 * time.Second),
	}
	if !expired.Expired() {
		t.Error("expired receipt should report expired")
	}
	if expired.Remaining() != 0 {
		t.Errorf("expired receipt remaining = %v, want 0", expired.Remaining())
	}
}

func TestGrantCacheReplaceExisting(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})
	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 4 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})

	if gc.Len() != 1 {
		t.Fatalf("expected 1 entry after replace, got %d", gc.Len())
	}
	got := gc.Get(relay1)
	if got.GrantDuration != 4*time.Hour {
		t.Errorf("expected replaced duration 4h, got %v", got.GrantDuration)
	}
}

func TestGrantCacheAllSnapshot(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)
	relay2 := genPeerID(t)

	gc.Put(&GrantReceipt{RelayPeerID: relay1, GrantDuration: 1 * time.Hour, ReceivedAt: time.Now(), IssuedAt: time.Now()})
	gc.Put(&GrantReceipt{RelayPeerID: relay2, GrantDuration: 2 * time.Hour, ReceivedAt: time.Now(), IssuedAt: time.Now()})

	all := gc.All()
	if len(all) != 2 {
		t.Fatalf("All() returned %d entries, want 2", len(all))
	}

	// Mutating snapshot should not affect cache.
	all[0].GrantDuration = 99 * time.Hour
	got := gc.Get(all[0].RelayPeerID)
	if got.GrantDuration == 99*time.Hour {
		t.Error("All() returned references, not copies")
	}
}

func TestGrantCacheStopWithoutStartCleanup(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	dir := t.TempDir()
	path := filepath.Join(dir, "grant_cache.json")
	gc.SetPersistPath(path)

	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Hour,
		ReceivedAt:    time.Now(),
		IssuedAt:      time.Now(),
	})

	// Stop without StartCleanup should not panic and should persist.
	gc.Stop()

	// Verify file was written.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("expected cache file to be persisted on Stop()")
	}
}

func TestGrantCacheOversizedFileRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grant_cache.json")

	// Write a file larger than 1MB.
	big := make([]byte, 1<<20+1)
	for i := range big {
		big[i] = ' '
	}
	if err := os.WriteFile(path, big, 0600); err != nil {
		t.Fatal(err)
	}

	_, err := LoadGrantCache(path, nil)
	if err == nil {
		t.Fatal("expected error for oversized cache file")
	}
}

func TestGrantCacheConcurrentPutGet(t *testing.T) {
	gc := NewGrantCache(nil)
	relays := make([]peer.ID, 10)
	for i := range relays {
		relays[i] = genPeerID(t)
	}

	// Hammer the cache from multiple goroutines.
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			relay := relays[idx]
			for j := 0; j < 100; j++ {
				gc.Put(&GrantReceipt{
					RelayPeerID:      relay,
					GrantDuration:    time.Duration(j) * time.Minute,
					SessionDataLimit: int64(j) * 1024,
					ReceivedAt:       time.Now(),
					IssuedAt:         time.Now(),
				})
				gc.Get(relay)
				gc.TrackCircuitBytes(relay, "send", 1024)
				gc.HasSufficientBudget(relay, 512, "send")
				gc.Len()
				gc.All()
			}
		}(i)
	}
	wg.Wait()

	// Verify no data corruption.
	if gc.Len() != 10 {
		t.Errorf("expected 10 entries after concurrent writes, got %d", gc.Len())
	}
}

func TestGrantCacheGrantStatus(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	// No grant - should return ok=false.
	_, _, _, ok := gc.GrantStatus(relay1)
	if ok {
		t.Error("should return ok=false for missing grant")
	}

	// Active grant.
	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 2 << 30,
		SessionDuration:  30 * time.Minute,
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	})

	rem, budget, sessDur, ok := gc.GrantStatus(relay1)
	if !ok {
		t.Fatal("should return ok=true for active grant")
	}
	if rem < 1*time.Hour || rem > 2*time.Hour+time.Second {
		t.Errorf("remaining = %s, expected ~2h", rem)
	}
	if budget != 2<<30 {
		t.Errorf("budget = %d, want %d", budget, 2<<30)
	}
	if sessDur != 30*time.Minute {
		t.Errorf("sessionDuration = %s, want 30m", sessDur)
	}

	// Expired grant.
	gc.Put(&GrantReceipt{
		RelayPeerID:   relay1,
		GrantDuration: 1 * time.Millisecond,
		ReceivedAt:    time.Now().Add(-1 * time.Second), // already expired
		IssuedAt:      time.Now().Add(-1 * time.Second),
	})
	_, _, _, ok = gc.GrantStatus(relay1)
	if ok {
		t.Error("should return ok=false for expired grant")
	}

	// Permanent grant.
	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		Permanent:        true,
		SessionDataLimit: 0, // unlimited
		SessionDuration:  0,
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	})
	rem, budget, _, ok = gc.GrantStatus(relay1)
	if !ok {
		t.Fatal("should return ok=true for permanent grant")
	}
	if rem != time.Duration(math.MaxInt64) {
		t.Errorf("permanent grant remaining = %s, want MaxInt64", rem)
	}
	if budget != math.MaxInt64 {
		t.Errorf("permanent grant budget = %d, want MaxInt64", budget)
	}
}

func TestGrantCacheCircuitBytesOverflowClamp(t *testing.T) {
	gc := NewGrantCache(nil)
	relay1 := genPeerID(t)

	gc.Put(&GrantReceipt{
		RelayPeerID:      relay1,
		GrantDuration:    2 * time.Hour,
		SessionDataLimit: 1 << 30,
		ReceivedAt:       time.Now(),
		IssuedAt:         time.Now(),
	})

	// Track near MaxInt64 to test overflow clamping.
	gc.TrackCircuitBytes(relay1, "send", math.MaxInt64-100)
	gc.TrackCircuitBytes(relay1, "send", 200) // Would overflow without clamp

	got := gc.Get(relay1)
	if got.CircuitBytesSent != math.MaxInt64 {
		t.Errorf("expected CircuitBytesSent clamped to MaxInt64, got %d", got.CircuitBytesSent)
	}

	// Negative values should be ignored.
	gc.ResetCircuitCounters(relay1)
	gc.TrackCircuitBytes(relay1, "send", -100)
	got = gc.Get(relay1)
	if got.CircuitBytesSent != 0 {
		t.Errorf("negative TrackCircuitBytes should be ignored, got %d", got.CircuitBytesSent)
	}
}
