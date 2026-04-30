package sdk

import (
	"log/slog"
	"sync"
)

// eventEntry pairs a handler with its subscription ID for ordered dispatch.
type eventEntry struct {
	id      uint64
	handler EventHandler
}

// EventBus dispatches network events to registered handlers.
// Thread-safe; handlers are called synchronously in registration order.
type EventBus struct {
	mu       sync.RWMutex
	handlers []eventEntry
	nextID   uint64
}

// NewEventBus creates a new event bus.
func NewEventBus() *EventBus {
	return &EventBus{}
}

// Subscribe registers a handler and returns a function to unsubscribe.
func (b *EventBus) Subscribe(handler EventHandler) func() {
	b.mu.Lock()
	id := b.nextID
	b.nextID++
	b.handlers = append(b.handlers, eventEntry{id: id, handler: handler})
	b.mu.Unlock()

	return func() {
		b.mu.Lock()
		for i, e := range b.handlers {
			if e.id == id {
				b.handlers = append(b.handlers[:i], b.handlers[i+1:]...)
				break
			}
		}
		b.mu.Unlock()
	}
}

// Emit dispatches an event to all registered handlers in registration order.
// Handlers are snapshot-copied before dispatch so they may safely call
// Subscribe/Unsubscribe without deadlocking.
// A panicking handler is recovered so it cannot crash the event bus or other handlers.
func (b *EventBus) Emit(e Event) {
	b.mu.RLock()
	snapshot := make([]EventHandler, len(b.handlers))
	for i, entry := range b.handlers {
		snapshot[i] = entry.handler
	}
	b.mu.RUnlock()

	for _, handler := range snapshot {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("event handler panicked", "event", e.Type, "panic", r)
				}
			}()
			handler(e)
		}()
	}
}
