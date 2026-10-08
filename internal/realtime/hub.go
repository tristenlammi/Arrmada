// Package realtime fans internal events out to connected websocket clients, so
// the UI updates live (queues, activity, health) without polling. It bridges the
// event bus to browsers: subscribe once to the bus, then send each event to the
// clients the topic policy (policy.go) lets see it.
package realtime

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// Client is one connected websocket subscriber.
type Client struct {
	send   chan []byte
	viewer Viewer
}

// Send is the outbound message channel; closed when the client disconnects.
func (c *Client) Send() <-chan []byte { return c.send }

// Hub tracks connected clients and broadcasts to them.
type Hub struct {
	log     *slog.Logger
	mu      sync.RWMutex
	clients map[*Client]struct{}
}

// NewHub creates an empty hub.
func NewHub(log *slog.Logger) *Hub {
	return &Hub{log: log, clients: make(map[*Client]struct{})}
}

// Connect registers a new client with a buffered outbound channel. The viewer is fixed
// for the connection's life, so a role change takes effect on the next reconnect.
func (h *Hub) Connect(v Viewer) *Client {
	c := &Client{send: make(chan []byte, 32), viewer: v}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Disconnect removes a client and closes its channel.
func (h *Hub) Disconnect(c *Client) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c.send)
	}
	h.mu.Unlock()
}

// Count returns the number of connected clients.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// Run subscribes to every event on the bus and sends each, as JSON
// {"topic":...,"data":...}, to the clients allowed to see it. Each event is encoded
// once; only the policy check runs per client. Returns when ctx is cancelled.
func (h *Hub) Run(ctx context.Context, bus *eventbus.Bus) {
	events, cancel := bus.Subscribe("*")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			msg, err := json.Marshal(struct {
				Topic string `json:"topic"`
				Data  any    `json:"data"`
			}{Topic: ev.Topic, Data: ev.Data})
			if err != nil {
				if h.log != nil {
					h.log.Error("realtime marshal failed", "topic", ev.Topic, "err", err)
				}
				continue
			}
			h.broadcast(ev.Topic, msg)
		}
	}
}

func (h *Hub) broadcast(topic string, msg []byte) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for c := range h.clients {
		if !allowed(topic, c.viewer) {
			continue
		}
		select {
		case c.send <- msg:
		default:
			// Slow client: drop this message rather than stall the hub.
		}
	}
}
