package eventbus

import (
	"testing"
	"time"
)

func TestPublishReachesTopicSubscriber(t *testing.T) {
	b := New(nil)
	ch, cancel := b.Subscribe("release.grabbed")
	defer cancel()

	b.Publish("release.grabbed", 42)

	select {
	case ev := <-ch:
		if ev.Topic != "release.grabbed" || ev.Data.(int) != 42 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestWildcardReceivesAll(t *testing.T) {
	b := New(nil)
	ch, cancel := b.Subscribe("*")
	defer cancel()

	b.Publish("a", nil)
	b.Publish("b", nil)

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case ev := <-ch:
			got[ev.Topic] = true
		case <-time.After(time.Second):
			t.Fatal("timed out")
		}
	}
	if !got["a"] || !got["b"] {
		t.Fatalf("expected both topics, got %v", got)
	}
}

func TestUnsubscribedReceivesNothing(t *testing.T) {
	b := New(nil)
	ch, _ := b.Subscribe("x")

	// A different topic must not be delivered.
	b.Publish("y", nil)
	select {
	case ev := <-ch:
		t.Fatalf("did not expect event, got %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestCancelStopsDelivery(t *testing.T) {
	b := New(nil)
	ch, cancel := b.Subscribe("x")
	cancel()

	// Channel is closed; publishing must not panic and the channel must drain closed.
	b.Publish("x", nil)
	if _, ok := <-ch; ok {
		t.Fatal("expected channel to be closed after cancel")
	}
}
