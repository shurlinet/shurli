package grants

import (
	"sync"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestOpsRateLimiterAllowsUnderLimit(t *testing.T) {
	rl := NewOpsRateLimiter(10, nil)
	pid := genPeerID(t)

	for i := 0; i < 10; i++ {
		if !rl.Allow(pid) {
			t.Fatalf("should allow op %d (under limit)", i+1)
		}
	}
}

func TestOpsRateLimiterBlocksOverLimit(t *testing.T) {
	rl := NewOpsRateLimiter(5, nil)
	pid := genPeerID(t)

	for i := 0; i < 5; i++ {
		rl.Allow(pid)
	}

	if rl.Allow(pid) {
		t.Fatal("should block 6th op (over 5/min limit)")
	}
}

func TestOpsRateLimiterPerPeer(t *testing.T) {
	rl := NewOpsRateLimiter(3, nil)
	pid1 := genPeerID(t)
	pid2 := genPeerID(t)

	// Exhaust pid1's limit.
	for i := 0; i < 3; i++ {
		rl.Allow(pid1)
	}
	if rl.Allow(pid1) {
		t.Fatal("pid1 should be rate limited")
	}

	// pid2 should still be allowed.
	if !rl.Allow(pid2) {
		t.Fatal("pid2 should not be rate limited (separate peer)")
	}
}

func TestOpsRateLimiterNotification(t *testing.T) {
	var mu sync.Mutex
	var notified bool
	var notifiedPeer peer.ID

	rl := NewOpsRateLimiter(2, func(eventType string, pid peer.ID, meta map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		notified = true
		notifiedPeer = pid
	})

	pid := genPeerID(t)
	rl.Allow(pid)
	rl.Allow(pid)
	rl.Allow(pid) // this triggers the notification

	mu.Lock()
	defer mu.Unlock()
	if !notified {
		t.Fatal("notification should fire on rate limit violation")
	}
	if notifiedPeer != pid {
		t.Fatal("notification should include the correct peer")
	}
}

func TestOpsRateLimiterConcurrent(t *testing.T) {
	rl := NewOpsRateLimiter(100, nil)
	pid := genPeerID(t)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rl.Allow(pid)
		}()
	}
	wg.Wait()
}
