package realtime

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

func TestPolicyAllowed(t *testing.T) {
	staff := Viewer{UserID: 1, Staff: true}
	user7 := Viewer{UserID: 7}
	cases := []struct {
		topic string
		v     Viewer
		want  bool
	}{
		{"server.heartbeat", user7, true},
		{"server.heartbeat", staff, true},
		{"plex.stream.started", user7, false},
		{"plex.stream.started", staff, true},
		{"release.grabbed", user7, false},
		{"file.removed", user7, false},
		{"queue.progress", user7, false}, // download hashes and progress: staff only
		{"queue.progress", staff, true},
		{"request.updated", user7, false}, // everyone's requests: staff only
		{"request.updated", staff, true},
		{"something.new", user7, false}, // unknown topics are staff-only
		{"something.new", staff, true},
		{"user.7.request.updated", user7, true},
		{"user.8.request.updated", user7, false},
		{"user.70.request.updated", user7, false}, // id boundary
		{"user.7", user7, false},                  // no event name: not a per-user event
		{"user.7.request.updated", staff, false},  // personal: not even staff
		{"user.1.request.updated", staff, true},   // staff still get their own
	}
	for _, c := range cases {
		if got := allowed(c.topic, c.v); got != c.want {
			t.Errorf("allowed(%q, %+v) = %v, want %v", c.topic, c.v, got, c.want)
		}
	}
}

// A requester's socket used to receive every bus event, including who is streaming
// what on Plex. Now it gets the heartbeat and its own events only.
func TestHubFiltersByViewer(t *testing.T) {
	bus := eventbus.New(nil)
	h := NewHub(nil)
	staff := h.Connect(Viewer{UserID: 1, Staff: true})
	requester := h.Connect(Viewer{UserID: 7})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go h.Run(ctx, bus)
	time.Sleep(20 * time.Millisecond) // let Run subscribe

	for _, topic := range []string{
		"plex.stream.started", "server.heartbeat", "user.7.request.updated",
		"user.8.request.updated", "something.unknown",
	} {
		bus.Publish(topic, map[string]string{"x": "y"})
	}
	bus.Publish("server.heartbeat", "done") // end marker: everyone gets the heartbeat

	got := func(c *Client) []string {
		var topics []string
		for {
			select {
			case m := <-c.Send():
				var ev struct {
					Topic string `json:"topic"`
					Data  any    `json:"data"`
				}
				if err := json.Unmarshal(m, &ev); err != nil {
					t.Fatal(err)
				}
				if ev.Data == "done" {
					return topics
				}
				topics = append(topics, ev.Topic)
			case <-time.After(2 * time.Second):
				t.Fatalf("timed out; got %v so far", topics)
			}
		}
	}

	if g, want := got(requester), []string{"server.heartbeat", "user.7.request.updated"}; !reflect.DeepEqual(g, want) {
		t.Errorf("requester got %v, want %v", g, want)
	}
	if g, want := got(staff), []string{"plex.stream.started", "server.heartbeat", "something.unknown"}; !reflect.DeepEqual(g, want) {
		t.Errorf("staff got %v, want %v", g, want)
	}
}
