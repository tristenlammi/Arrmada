// Package eventbus is Arrmada's in-process publish/subscribe hub. Modules emit
// events (release.grabbed, movie.downloaded, …) and the UI's websocket hub and the admin
// alerts (notify) subscribe. Delivery is asynchronous and non-blocking: a slow subscriber
// never stalls a publisher — its events are dropped (counted and logged) instead.
//
// Because of that, the bus is for the UI and admin alerts only: things where a lost
// message costs a stale screen or a missed ping. Work that must happen after an event —
// reindexing, a requester's "ready" message, cache refreshes — is either a direct call or
// a durable internal/outbox row. internal/archtest enforces this: only internal/realtime
// and internal/notify may Subscribe.
package eventbus

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Event is a single published message.
type Event struct {
	Topic string
	Data  any
}

type subscriber struct {
	topics map[string]bool // "*" matches every topic
	ch     chan Event
}

// Bus is a concurrency-safe event hub.
type Bus struct {
	mu      sync.RWMutex
	log     *slog.Logger
	subs    map[*subscriber]struct{}
	bufSize int

	dropMu sync.Mutex
	drops  map[string]*atomic.Uint64 // topic → events dropped on a full subscriber buffer
}

// New creates a Bus. Each subscriber gets a buffered channel of bufSize events.
func New(log *slog.Logger) *Bus {
	return &Bus{
		log:     log,
		subs:    make(map[*subscriber]struct{}),
		bufSize: 64,
	}
}

// Subscribe returns a receive-only channel of events for the given topics (pass
// "*" to receive everything) and a cancel func that unsubscribes and closes the
// channel. Always call cancel when done.
func (b *Bus) Subscribe(topics ...string) (<-chan Event, func()) {
	s := &subscriber{topics: make(map[string]bool, len(topics)), ch: make(chan Event, b.bufSize)}
	for _, t := range topics {
		s.topics[t] = true
	}

	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subs, s)
			close(s.ch) // safe: Publish sends under RLock, this runs under Lock
			b.mu.Unlock()
		})
	}
	return s.ch, cancel
}

// Publish delivers an event to every matching subscriber. It never blocks: if a
// subscriber's buffer is full, the event is dropped and a warning is logged.
func (b *Bus) Publish(topic string, data any) {
	ev := Event{Topic: topic, Data: data}

	b.mu.RLock()
	defer b.mu.RUnlock()
	for s := range b.subs {
		if !s.topics["*"] && !s.topics[topic] {
			continue
		}
		select {
		case s.ch <- ev:
		default:
			b.countDrop(topic)
			if b.log != nil {
				b.log.Warn("event dropped: subscriber buffer full", "topic", topic)
			}
		}
	}
}

func (b *Bus) countDrop(topic string) {
	b.dropMu.Lock()
	if b.drops == nil {
		b.drops = map[string]*atomic.Uint64{}
	}
	c, ok := b.drops[topic]
	if !ok {
		c = new(atomic.Uint64)
		b.drops[topic] = c
	}
	b.dropMu.Unlock()
	c.Add(1)
}

// Drops reports, per topic, how many events were dropped because a subscriber's buffer
// was full, since the process started. Topics with no drops are absent.
func (b *Bus) Drops() map[string]uint64 {
	b.dropMu.Lock()
	defer b.dropMu.Unlock()
	out := make(map[string]uint64, len(b.drops))
	for t, c := range b.drops {
		out[t] = c.Load()
	}
	return out
}
