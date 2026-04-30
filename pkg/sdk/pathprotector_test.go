package sdk

import (
	"context"
	"testing"
	"time"

	"github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/peer"
)

// newTestPathProtector creates a minimal PathProtector with a real libp2p host for testing.
func newTestPathProtector(t *testing.T) *PathProtector {
	t.Helper()
	h, err := libp2p.New(libp2p.NoListenAddrs)
	if err != nil {
		t.Fatalf("create test host: %v", err)
	}
	t.Cleanup(func() { h.Close() })

	pp := NewPathProtector(h, nil, NewLANRegistry(), nil)
	t.Cleanup(func() { pp.Close() })
	return pp
}

func TestPathProtectorProtectUnprotect(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-1")

	// Initially not protected.
	if pp.IsProtected(pid) {
		t.Error("should not be protected initially")
	}

	// Protect with tag.
	pp.Protect(pid, "xfer-abc")
	if !pp.IsProtected(pid) {
		t.Error("should be protected after Protect")
	}

	// Second tag.
	pp.Protect(pid, "xfer-xyz")
	if !pp.IsProtected(pid) {
		t.Error("should still be protected with two tags")
	}

	// Remove first tag.
	pp.Unprotect(pid, "xfer-abc")
	if !pp.IsProtected(pid) {
		t.Error("should still be protected with one tag remaining")
	}

	// Remove second tag.
	pp.Unprotect(pid, "xfer-xyz")
	if pp.IsProtected(pid) {
		t.Error("should not be protected after all tags removed")
	}
}

func TestPathProtectorForceUnprotectAll(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-2")

	pp.Protect(pid, "tag1")
	pp.Protect(pid, "tag2")
	pp.Protect(pid, "tag3")

	if !pp.IsProtected(pid) {
		t.Error("should be protected")
	}

	pp.ForceUnprotectAll(pid)
	if pp.IsProtected(pid) {
		t.Error("should not be protected after ForceUnprotectAll")
	}
}

func TestPathProtectorMaxProtectedPeers(t *testing.T) {
	pp := newTestPathProtector(t)

	// Protect MaxProtectedPeers peers.
	for i := 0; i < MaxProtectedPeers; i++ {
		pid := peer.ID("peer-" + string(rune('A'+i)))
		pp.Protect(pid, "tag")
	}

	// The next one should be silently skipped.
	overflow := peer.ID("peer-overflow")
	pp.Protect(overflow, "tag")
	if pp.IsProtected(overflow) {
		t.Error("overflow peer should not be protected when cap reached")
	}
}

func TestPathProtectorDuplicateTag(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-3")

	pp.Protect(pid, "same-tag")
	pp.Protect(pid, "same-tag") // duplicate, should overwrite timestamp

	// Only one unprotect needed.
	pp.Unprotect(pid, "same-tag")
	if pp.IsProtected(pid) {
		t.Error("should not be protected after removing the single tag")
	}
}

func TestPathProtectorUnprotectNonexistent(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-4")

	// Should not panic.
	pp.Unprotect(pid, "nonexistent")
	pp.ForceUnprotectAll(pid)
}

func TestPathProtectorManagedGroups_Empty(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-5")

	groups := pp.ManagedGroups(pid)
	if len(groups) != 0 {
		t.Errorf("expected 0 managed groups, got %d", len(groups))
	}
}

func TestPathProtectorManagedPaths_Empty(t *testing.T) {
	pp := newTestPathProtector(t)

	paths := pp.ManagedPaths()
	if paths != nil {
		t.Errorf("expected nil managed paths, got %v", paths)
	}
}

func TestPathProtectorManagedConnsForCancel_Empty(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-6")

	conns := pp.ManagedConnsForCancel(pid)
	if conns != nil {
		t.Errorf("expected nil, got %v", conns)
	}
}

func TestPathProtectorClose(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-7")

	pp.Protect(pid, "tag")
	pp.Close()

	// After close, should be unprotected (tags cleared).
	if pp.IsProtected(pid) {
		t.Error("should not be protected after Close")
	}
}

func TestPathProtectorTagTimestamp(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-8")

	before := time.Now()
	pp.Protect(pid, "timestamped")
	after := time.Now()

	pp.mu.RLock()
	info, ok := pp.tags[pid]["timestamped"]
	pp.mu.RUnlock()

	if !ok {
		t.Fatal("tag not found")
	}
	if info.created.Before(before) || info.created.After(after) {
		t.Errorf("tag timestamp %v not between %v and %v", info.created, before, after)
	}
	if info.ctx != nil {
		t.Error("legacy Protect should have nil ctx")
	}
}

func TestProtectWithContext_LiveContext(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-ctx-live")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pp.ProtectWithContext(ctx, pid, "long-transfer")

	if !pp.IsProtected(pid) {
		t.Fatal("peer should be protected")
	}

	// Verify context is stored.
	pp.mu.RLock()
	info, ok := pp.tags[pid]["long-transfer"]
	pp.mu.RUnlock()
	if !ok {
		t.Fatal("tag not found")
	}
	if info.ctx == nil {
		t.Fatal("ProtectWithContext should store non-nil ctx")
	}
	if info.ctx.Err() != nil {
		t.Fatal("context should be alive")
	}
}

func TestReaper_SkipsLiveContext(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-reaper-skip")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pp.ProtectWithContext(ctx, pid, "long-transfer")

	// Backdate the tag creation to make it look old to time-based check.
	pp.mu.Lock()
	info := pp.tags[pid]["long-transfer"]
	info.created = time.Now().Add(-10 * time.Minute) // well past orphanTimeout
	pp.tags[pid]["long-transfer"] = info
	pp.mu.Unlock()

	// Run reaper — should NOT kill the protection because context is alive.
	pp.reap()

	if !pp.IsProtected(pid) {
		t.Fatal("reaper should NOT have killed protection with live context")
	}
}

func TestReaper_CleansDeadContext(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-reaper-dead")

	ctx, cancel := context.WithCancel(context.Background())
	pp.ProtectWithContext(ctx, pid, "crashed-transfer")

	// Simulate goroutine exit: cancel the context.
	cancel()

	// Backdate the tag so time-based check also considers it old.
	pp.mu.Lock()
	info := pp.tags[pid]["crashed-transfer"]
	info.created = time.Now().Add(-10 * time.Minute)
	pp.tags[pid]["crashed-transfer"] = info
	pp.mu.Unlock()

	// Run reaper — should clean up because context is dead + Unprotect was never called.
	pp.reap()

	if pp.IsProtected(pid) {
		t.Fatal("reaper should have cleaned up dead context protection")
	}
}

func TestReaper_MixedTags_LiveContextKeepsAll(t *testing.T) {
	pp := newTestPathProtector(t)
	pid := peer.ID("test-peer-mixed")

	// Legacy tag (time-based, old).
	pp.Protect(pid, "old-tag")
	pp.mu.Lock()
	info := pp.tags[pid]["old-tag"]
	info.created = time.Now().Add(-10 * time.Minute)
	pp.tags[pid]["old-tag"] = info
	pp.mu.Unlock()

	// Context-based tag (alive).
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pp.ProtectWithContext(ctx, pid, "live-tag")

	// Run reaper — live context tag should prevent ALL tags from being reaped.
	pp.reap()

	if !pp.IsProtected(pid) {
		t.Fatal("live context tag should prevent reaping of all tags for this peer")
	}
}
