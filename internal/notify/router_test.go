package notify

import (
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// mockSink records events for test assertions.
type mockSink struct {
	mu     sync.Mutex
	events []Event
	name   string
	err    error // if set, Notify returns this error
}

func (m *mockSink) Name() string { return m.name }

func (m *mockSink) Notify(event Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return m.err
}

func (m *mockSink) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.events)
}

func (m *mockSink) last() Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.events[len(m.events)-1]
}

func TestRouter_EmitToAllSinks(t *testing.T) {
	r := NewRouter(slog.Default())
	s1 := &mockSink{name: "test1"}
	s2 := &mockSink{name: "test2"}
	r.AddSink(s1)
	r.AddSink(s2)

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmTestPeer123456", "", "grant created")
	r.Emit(event)

	// Give goroutines time to fire.
	time.Sleep(50 * time.Millisecond)

	// LogSink (always first) + s1 + s2 = 3 sinks. s1 and s2 should each get 1 event.
	if s1.count() != 1 {
		t.Errorf("sink1 got %d events, want 1", s1.count())
	}
	if s2.count() != 1 {
		t.Errorf("sink2 got %d events, want 1", s2.count())
	}
	if s1.last().Type != EventGrantCreated {
		t.Errorf("sink1 event type = %s, want %s", s1.last().Type, EventGrantCreated)
	}
}

func TestRouter_Dedup(t *testing.T) {
	r := NewRouter(slog.Default())
	s := &mockSink{name: "dedup"}
	r.AddSink(s)

	event := NewEvent(EventGrantRevoked, SeverityWarn, "QmTestPeer123456", "", "revoked")
	r.Emit(event)
	r.Emit(event) // same ID, should be dropped

	time.Sleep(50 * time.Millisecond)

	if s.count() != 1 {
		t.Errorf("got %d events, want 1 (dedup should drop the second)", s.count())
	}
}

func TestRouter_DedupAllowsDifferentIDs(t *testing.T) {
	r := NewRouter(slog.Default())
	s := &mockSink{name: "dedup2"}
	r.AddSink(s)

	e1 := NewEvent(EventGrantCreated, SeverityInfo, "QmPeerA", "", "created")
	e2 := NewEvent(EventGrantCreated, SeverityInfo, "QmPeerB", "", "created")

	r.Emit(e1)
	r.Emit(e2)

	time.Sleep(50 * time.Millisecond)

	if s.count() != 2 {
		t.Errorf("got %d events, want 2", s.count())
	}
}

func TestRouter_SinkErrorDoesNotBlock(t *testing.T) {
	r := NewRouter(slog.Default())
	failing := &mockSink{name: "failing", err: errTestSink}
	good := &mockSink{name: "good"}
	r.AddSink(failing)
	r.AddSink(good)

	event := NewEvent(EventGrantExtended, SeverityInfo, "QmTestPeer", "", "extended")
	r.Emit(event)

	time.Sleep(50 * time.Millisecond)

	// Good sink should still receive the event even though failing sink errored.
	if good.count() != 1 {
		t.Errorf("good sink got %d events, want 1", good.count())
	}
}

var errTestSink = errors.New("test sink error")

func TestRouter_NameResolver(t *testing.T) {
	r := NewRouter(slog.Default())
	r.SetNameResolver(func(peerID string) string {
		if peerID == "QmTestPeer123456" {
			return "alice"
		}
		return ""
	})

	s := &mockSink{name: "names"}
	r.AddSink(s)

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmTestPeer123456", "", "created")
	r.Emit(event)

	time.Sleep(50 * time.Millisecond)

	if s.last().PeerName != "alice" {
		t.Errorf("peer_name = %q, want %q", s.last().PeerName, "alice")
	}
}

func TestRouter_PreExpiryWarnings(t *testing.T) {
	r := NewRouter(slog.Default())
	s := &mockSink{name: "expiry"}
	r.AddSink(s)

	// Mock expiry checker that returns one expiring grant.
	checker := ExpiryCheckerFunc(func(d time.Duration) []ExpiryInfo {
		return []ExpiryInfo{{
			PeerID:    "QmExpiringPeer",
			PeerName:  "bob",
			ExpiresAt: time.Now().Add(5 * time.Minute),
			Remaining: 5 * time.Minute,
		}}
	})
	r.SetExpiryChecker(checker, 10*time.Minute, 50*time.Millisecond)
	r.Start()
	defer r.Stop()

	// Wait for at least one tick. Use polling instead of fixed sleep
	// to avoid flakiness under CPU contention (race detector, parallel tests).
	var found bool
	for i := 0; i < 40; i++ {
		time.Sleep(25 * time.Millisecond)
		if s.count() >= 1 {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected at least 1 expiry warning, got %d after 1s", s.count())
	}

	// Should be grant_expiring type.
	foundType := false
	s.mu.Lock()
	for _, e := range s.events {
		if e.Type == EventGrantExpiring {
			foundType = true
			break
		}
	}
	s.mu.Unlock()
	if !foundType {
		t.Error("no grant_expiring event found")
	}

	// Dedup: subsequent ticks should NOT produce another event for the same grant+expiry.
	countBefore := s.count()
	time.Sleep(200 * time.Millisecond)
	if s.count() != countBefore {
		t.Errorf("dedup failed: count went from %d to %d", countBefore, s.count())
	}
}

func TestRouter_StartStop(t *testing.T) {
	r := NewRouter(slog.Default())
	r.Start()

	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		r.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}

func TestRouter_SinksListIncludesLog(t *testing.T) {
	r := NewRouter(slog.Default())
	names := r.Sinks()
	if len(names) == 0 || names[0] != "log" {
		t.Errorf("first sink should be 'log', got %v", names)
	}
}

func TestRouter_ConcurrentEmit(t *testing.T) {
	r := NewRouter(slog.Default())
	var received atomic.Int64
	s := &countSink{received: &received}
	r.AddSink(s)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Each goroutine emits a unique event.
			e := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "test")
			r.Emit(e)
		}()
	}
	wg.Wait()
	time.Sleep(100 * time.Millisecond)

	if received.Load() != 100 {
		t.Errorf("received %d events, want 100", received.Load())
	}
}

type countSink struct {
	received *atomic.Int64
}

func (c *countSink) Name() string         { return "count" }
func (c *countSink) Notify(Event) error { c.received.Add(1); return nil }

func TestLogSink_SeverityRouting(t *testing.T) {
	// LogSink should not error regardless of severity.
	ls := NewLogSink(slog.Default())

	if err := ls.Notify(NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "info")); err != nil {
		t.Errorf("info event returned error: %v", err)
	}
	if err := ls.Notify(NewEvent(EventGrantRevoked, SeverityWarn, "QmPeer", "", "warn")); err != nil {
		t.Errorf("warn event returned error: %v", err)
	}
}

func TestEvent_WithMetadata(t *testing.T) {
	e := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "test")
	e2 := e.WithMetadata("key1", "val1").WithMetadata("key2", "val2")

	if e2.Metadata["key1"] != "val1" || e2.Metadata["key2"] != "val2" {
		t.Errorf("metadata = %v, want key1=val1 key2=val2", e2.Metadata)
	}

	// Original should not be mutated (no metadata).
	if e.Metadata != nil {
		t.Errorf("original event metadata should be nil, got %v", e.Metadata)
	}

	// Chaining on non-nil metadata must not mutate the intermediate.
	base := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "test").WithMetadata("a", "1")
	derived := base.WithMetadata("b", "2")
	if _, ok := base.Metadata["b"]; ok {
		t.Errorf("chained WithMetadata mutated original: base has key 'b'")
	}
	if derived.Metadata["a"] != "1" || derived.Metadata["b"] != "2" {
		t.Errorf("derived metadata = %v, want a=1 b=2", derived.Metadata)
	}
}

func TestRouter_SinkPanicRecovery(t *testing.T) {
	r := NewRouter(slog.Default())
	panicking := &panicSink{}
	good := &mockSink{name: "good"}
	r.AddSink(panicking)
	r.AddSink(good)

	event := NewEvent(EventGrantCreated, SeverityInfo, "QmPeer", "", "test")
	r.Emit(event)

	time.Sleep(50 * time.Millisecond)

	// Good sink must still receive the event despite the panic in the other sink.
	if good.count() != 1 {
		t.Errorf("good sink got %d events, want 1 (panic should not kill other sinks)", good.count())
	}
}

type panicSink struct{}

func (p *panicSink) Name() string        { return "panic" }
func (p *panicSink) Notify(Event) error { panic("intentional test panic") }

func TestRouter_StopWithoutStart(t *testing.T) {
	r := NewRouter(slog.Default())
	// Stop without Start should not panic or hang.
	r.Stop()
}

func TestExpiryCheckerFunc(t *testing.T) {
	called := false
	fn := ExpiryCheckerFunc(func(d time.Duration) []ExpiryInfo {
		called = true
		return nil
	})
	fn.ExpiringWithin(time.Minute)
	if !called {
		t.Error("ExpiryCheckerFunc was not called")
	}
}
