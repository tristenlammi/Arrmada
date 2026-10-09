package requests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// drain collects what's been published so far, as "topic status" lines.
func drain(t *testing.T, events <-chan eventbus.Event) []string {
	t.Helper()
	var out []string
	for {
		select {
		case ev := <-events:
			data := ev.Data.(map[string]any)
			if _, ok := data["title"]; ok {
				t.Errorf("%s carries a title: %v", ev.Topic, data)
			}
			if ev.Topic != eventbus.TopicRequestUpdated {
				if _, ok := data["requested_by"]; ok {
					t.Errorf("a per-user event names the requester: %v", data)
				}
			}
			out = append(out, fmt.Sprintf("%s %v", ev.Topic, data["status"]))
		case <-time.After(100 * time.Millisecond):
			return out
		}
	}
}

func TestRequestUpdatedEvents(t *testing.T) {
	s := newTestService(t)
	s.bus = eventbus.New(nil)
	events, cancel := s.bus.Subscribe("*")
	defer cancel()
	ctx := context.Background()

	req, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 7}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, events); fmt.Sprint(got) != "[request.updated pending user.7.request.updated pending]" {
		t.Errorf("create: %v", got)
	}

	// A second user subscribes: both of them hear about it.
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 42, Title: "Heat", RequestedBy: 8}, false); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, events); fmt.Sprint(got) != "[request.updated pending user.7.request.updated pending user.8.request.updated pending]" {
		t.Errorf("subscribe: %v", got)
	}

	if err := s.Decline(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, events); fmt.Sprint(got) != "[request.updated declined user.7.request.updated declined user.8.request.updated declined]" {
		t.Errorf("decline: %v", got)
	}

	if err := s.Delete(ctx, req.ID); err != nil {
		t.Fatal(err)
	}
	if got := drain(t, events); fmt.Sprint(got) != "[request.updated deleted user.7.request.updated deleted user.8.request.updated deleted]" {
		t.Errorf("delete: %v", got)
	}
}

// Ready is announced once, however often the ready sweep runs.
func TestRequestReadyPublishesOnce(t *testing.T) {
	s := newTestService(t)
	ctx := context.Background()
	if _, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: 11, Title: "Alien", RequestedBy: 7}, false); err != nil {
		t.Fatal(err)
	}
	s.bus = eventbus.New(nil)
	events, cancel := s.bus.Subscribe("*")
	defer cancel()

	_ = s.notifyRequester(ctx, "movie", 11, "")
	_ = s.notifyRequester(ctx, "movie", 11, "")
	if got := drain(t, events); fmt.Sprint(got) != "[request.updated available user.7.request.updated available]" {
		t.Errorf("ready: %v", got)
	}
}
