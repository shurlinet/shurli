package p2pnet

import (
	"log/slog"
	"sync"
)

// EventBus dispatches network events to registered handlers.
// Thread-safe; handlers are called synchronously in registration order.
type EventBus struct {
	mu       sync.RWMutex
	handlers map[uint64]EventHandler
	nextID   uint64
}

// NewEventBus creates a new event bus.
func NewEventBus() *EventBus {
	return &EventBus{
		handlers: make(map[uint64]EventHandler),
	}
}

// Subscribe registers a handler and returns a function to unsubscribe.
func (b *EventBus) Subscribe(handler EventHandler) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.handlers[id] = handler
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		delete(b.handlers, id)
		b.mu.Unlock()
	}
}

// Emit dispatches an event to all registered handlers.
// Handlers are called under a read lock; they must not call Subscribe/unsubscribe.
// A panicking handler is recovered so it cannot crash the event bus or other handlers.
func (b *EventBus) Emit(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	for _, h := range b.handlers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event handler panicked", "event", e.Type, "panic", r)
				}
			}()
			h(e)
		}()
	}
}
