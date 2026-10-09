package download

import (
	"context"
	"sync"
	"testing"
	"time"
)

type recordingPub struct {
	mu  sync.Mutex
	got [][]ProgressItem
}

func (r *recordingPub) Publish(topic string, data any) {
	if topic != TopicQueueProgress {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, data.([]ProgressItem))
}

func (r *recordingPub) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.got)
}

func dl(hash string, progress float64) Item {
	return Item{Hash: hash, Progress: progress, RemainingBytes: 1, RawState: "downloading", State: "downloading"}
}

func TestProgressPublishesOnlyChanges(t *testing.T) {
	pub := &recordingPub{}
	p := &progressPublisher{pub: pub}

	p.tick(nil) // idle from the start: nothing to say
	if pub.count() != 0 {
		t.Fatal("an idle queue published")
	}
	p.tick([]Item{dl("b", 0.500), dl("a", 0.2)})
	if pub.count() != 1 {
		t.Fatalf("first progress: %d publishes, want 1", pub.count())
	}
	if got := pub.got[0]; got[0].Hash != "a" || got[1].Hash != "b" {
		t.Errorf("not sorted by hash: %+v", got)
	}
	// Same numbers, other order, a new ETA: not a change.
	same := []Item{dl("a", 0.2), dl("b", 0.500)}
	same[0].ETASeconds = 99
	p.tick(same)
	// Moved by less than half a percent.
	p.tick([]Item{dl("a", 0.2), dl("b", 0.502)})
	if pub.count() != 1 {
		t.Fatalf("unchanged or barely moved: %d publishes, want still 1", pub.count())
	}
	// Moved by half a percent.
	p.tick([]Item{dl("a", 0.2), dl("b", 0.505)})
	if pub.count() != 2 {
		t.Fatalf("moved 0.5%%: %d publishes, want 2", pub.count())
	}
	// Finished downloads aren't progress: the queue going idle says so once, with an
	// empty list, and then nothing.
	done := Item{Hash: "a", Progress: 1}
	p.tick([]Item{done})
	if pub.count() != 3 || len(pub.got[2]) != 0 {
		t.Fatalf("going idle: %d publishes, last %+v; want one empty list", pub.count(), pub.got[len(pub.got)-1])
	}
	p.tick([]Item{done})
	p.tick(nil)
	if pub.count() != 3 {
		t.Fatalf("staying idle published again: %d", pub.count())
	}
}

// Nothing is read or published while nobody is connected.
func TestRunProgressQuietWithoutListeners(t *testing.T) {
	svc, fake, _ := snapshotFixture(t, "http://qb")
	pub := &recordingPub{}
	ctx, cancel := context.WithTimeout(context.Background(), progressEvery*2+500*time.Millisecond)
	defer cancel()
	RunProgress(ctx, svc, pub, func() bool { return false })
	if pub.count() != 0 || fake.lists.Load() != 0 {
		t.Fatalf("with no listeners: %d publishes, %d client reads", pub.count(), fake.lists.Load())
	}
}
