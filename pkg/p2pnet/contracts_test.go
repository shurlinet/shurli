package p2pnet

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

// --- Resolver interface tests ---

// mockResolver implements Resolver for testing.
type mockResolver struct {
	names map[string]peer.ID
}

func (m *mockResolver) Resolve(name string) (peer.ID, error) {
	if pid, ok := m.names[name]; ok {
		return pid, nil
	}
	return "", ErrNameNotFound
}

func TestNameResolverWithFallback(t *testing.T) {
	// Generate a deterministic peer ID for testing.
	fallbackPID, err := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	if err != nil {
		t.Fatal(err)
	}
	localPID, err := peer.Decode("12D3KooWGC6TkPybRPaMgP5bQCCFByY6LFkBfYBKZEciqm1u7o4R")
	if err != nil {
		t.Fatal(err)
	}

	fallback := &mockResolver{names: map[string]peer.ID{
		"remote-box": fallbackPID,
	}}

	r := newNameResolverFrom(fallback)

	// Register a local name.
	if err := r.Register("laptop", localPID); err != nil {
		t.Fatal(err)
	}

	// Local name should resolve.
	got, err := r.Resolve("laptop")
	if err != nil {
		t.Fatalf("local resolve failed: %v", err)
	}
	if got != localPID {
		t.Fatalf("local resolve: got %s, want %s", got, localPID)
	}

	// Fallback name should resolve.
	got, err = r.Resolve("remote-box")
	if err != nil {
		t.Fatalf("fallback resolve failed: %v", err)
	}
	if got != fallbackPID {
		t.Fatalf("fallback resolve: got %s, want %s", got, fallbackPID)
	}

	// Local name takes priority over fallback.
	fallback.names["laptop"] = fallbackPID // add conflict
	got, err = r.Resolve("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if got != localPID {
		t.Fatalf("priority: local should win, got %s", got)
	}

	// Unknown name with valid peer ID should still decode.
	got, err = r.Resolve(localPID.String())
	if err != nil {
		t.Fatalf("peer ID decode failed: %v", err)
	}
	if got != localPID {
		t.Fatalf("peer ID decode: got %s, want %s", got, localPID)
	}

	// Fully unknown name should fail.
	_, err = r.Resolve("nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown name")
	}
}

// --- EventBus tests ---

func TestEventBus_SubscribeAndEmit(t *testing.T) {
	bus := NewEventBus()

	var received []Event
	unsub := bus.Subscribe(func(e Event) {
		received = append(received, e)
	})

	bus.Emit(Event{Type: EventPeerConnected, ServiceName: "ssh"})
	bus.Emit(Event{Type: EventServiceRegistered, ServiceName: "http"})

	if len(received) != 2 {
		t.Fatalf("expected 2 events, got %d", len(received))
	}
	if received[0].Type != EventPeerConnected {
		t.Errorf("event 0: got type %d, want %d", received[0].Type, EventPeerConnected)
	}
	if received[1].ServiceName != "http" {
		t.Errorf("event 1: got service %q, want %q", received[1].ServiceName, "http")
	}

	// Unsubscribe and verify no more events.
	unsub()
	bus.Emit(Event{Type: EventPeerDisconnected})
	if len(received) != 2 {
		t.Fatalf("after unsub: expected 2 events, got %d", len(received))
	}
}

func TestEventBus_MultipleHandlers(t *testing.T) {
	bus := NewEventBus()

	var count1, count2 atomic.Int32
	bus.Subscribe(func(e Event) { count1.Add(1) })
	unsub2 := bus.Subscribe(func(e Event) { count2.Add(1) })

	bus.Emit(Event{Type: EventStreamOpened})
	if count1.Load() != 1 || count2.Load() != 1 {
		t.Fatalf("both handlers should fire: got %d, %d", count1.Load(), count2.Load())
	}

	unsub2()
	bus.Emit(Event{Type: EventStreamClosed})
	if count1.Load() != 2 {
		t.Fatalf("handler 1 should still fire: got %d", count1.Load())
	}
	if count2.Load() != 1 {
		t.Fatalf("handler 2 should not fire after unsub: got %d", count2.Load())
	}
}

func TestEventBus_ConcurrentEmit(t *testing.T) {
	bus := NewEventBus()

	var total atomic.Int64
	bus.Subscribe(func(e Event) { total.Add(1) })

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bus.Emit(Event{Type: EventPeerConnected})
		}()
	}
	wg.Wait()

	if total.Load() != 100 {
		t.Fatalf("expected 100 events, got %d", total.Load())
	}
}

// --- Authorizer interface tests ---

// mockAuthorizer implements Authorizer for testing.
type mockAuthorizer struct {
	allowed map[peer.ID]bool
}

func (m *mockAuthorizer) IsAuthorized(p peer.ID) bool {
	return m.allowed[p]
}

func TestAuthorizerInterface(t *testing.T) {
	pid, _ := peer.Decode("12D3KooWDpJ7As7BWAwRMfu1VU2WCqNjvq387JEYKDBj4kx6nXTN")
	unknown, _ := peer.Decode("12D3KooWGC6TkPybRPaMgP5bQCCFByY6LFkBfYBKZEciqm1u7o4R")

	auth := &mockAuthorizer{allowed: map[peer.ID]bool{pid: true}}

	// Verify it satisfies the Authorizer interface.
	var a Authorizer = auth
	if !a.IsAuthorized(pid) {
		t.Error("expected authorized")
	}
	if a.IsAuthorized(unknown) {
		t.Error("expected not authorized")
	}
}
