// Package bus fans live updates out to connected dashboard clients.
package bus

import "sync"

const (
	MessageEvent    = "event"
	MessageSnapshot = "snapshot"
	MessageRun      = "run"
)

type Message struct {
	Type string `json:"type"`
	Data any    `json:"data"`
}

type Bus struct {
	mu          sync.RWMutex
	subscribers map[int]chan Message
	nextID      int
}

func New() *Bus {
	return &Bus{subscribers: make(map[int]chan Message)}
}

// Subscribe returns a channel of messages and a function that unsubscribes it.
func (b *Bus) Subscribe() (<-chan Message, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()

	id := b.nextID
	b.nextID++
	ch := make(chan Message, 32)
	b.subscribers[id] = ch

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if existing, ok := b.subscribers[id]; ok {
			delete(b.subscribers, id)
			close(existing)
		}
	}
}

// Publish delivers to every subscriber, dropping messages for any client that
// has stopped reading rather than stalling the scheduler behind a slow browser.
func (b *Bus) Publish(msg Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}
