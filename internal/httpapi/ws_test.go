package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/realtime"
)

// The socket handler must tell the hub who's connecting: a requester's socket gets the
// heartbeat and its own events, never the household's Plex activity.
func TestWSFiltersTopicsByRole(t *testing.T) {
	bus := eventbus.New(nil)
	hub := realtime.NewHub(nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go hub.Run(ctx, bus)

	a := &api{deps: Deps{Realtime: hub, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	open := func(role auth.Role) *websocket.Conn {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			a.handleWS(w, withUser(r, &auth.User{ID: 7, Role: role}))
		}))
		t.Cleanup(srv.Close)
		c, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	requester, manager := open(auth.RoleRequester), open(auth.RoleManager)
	for deadline := time.Now().Add(2 * time.Second); hub.Count() < 2; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("sockets never registered with the hub")
		}
	}

	bus.Publish("plex.stream.started", map[string]string{"user": "someone", "title": "a film"})
	bus.Publish("user.7.request.updated", map[string]int{"id": 1})
	bus.Publish("server.heartbeat", "done")

	read := func(c *websocket.Conn) []string {
		var topics []string
		for {
			rctx, rcancel := context.WithTimeout(ctx, 2*time.Second)
			_, msg, err := c.Read(rctx)
			rcancel()
			if err != nil {
				t.Fatalf("read: %v (got %v so far)", err, topics)
			}
			var ev struct {
				Topic string `json:"topic"`
				Data  any    `json:"data"`
			}
			if err := json.Unmarshal(msg, &ev); err != nil {
				t.Fatal(err)
			}
			if ev.Data == "done" {
				return topics
			}
			topics = append(topics, ev.Topic)
		}
	}
	if got := read(requester); len(got) != 1 || got[0] != "user.7.request.updated" {
		t.Errorf("requester socket got %v, want only its own request event", got)
	}
	if got := read(manager); len(got) != 2 || got[0] != "plex.stream.started" {
		t.Errorf("manager socket got %v, want Plex activity and its own event", got)
	}
}
