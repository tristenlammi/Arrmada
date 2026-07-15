package realtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

func TestConnectBroadcastDisconnect(t *testing.T) {
	h := NewHub(nil)
	c := h.Connect()
	if h.Count() != 1 {
		t.Fatalf("expected 1 client, got %d", h.Count())
	}

	h.broadcast([]byte("hello"))
	select {
	case m := <-c.Send():
		if string(m) != "hello" {
			t.Fatalf("unexpected message %q", m)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for broadcast")
	}

	h.Disconnect(c)
	if h.Count() != 0 {
		t.Fatalf("expected 0 clients after disconnect, got %d", h.Count())
	}
}

func TestRunForwardsBusEvents(t *testing.T) {
	bus := eventbus.New(nil)
	h := NewHub(nil)
	c := h.Connect()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx, bus)
	time.Sleep(20 * time.Millisecond) // let Run subscribe

	bus.Publish("server.heartbeat", map[string]int{"uptime_seconds": 5})

	select {
	case m := <-c.Send():
		s := string(m)
		if !strings.Contains(s, "server.heartbeat") || !strings.Contains(s, "uptime_seconds") {
			t.Fatalf("unexpected payload: %s", s)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded event")
	}
}
