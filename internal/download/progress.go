package download

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
)

// TopicQueueProgress carries the live progress of every unfinished download, for the
// progress bars. Staff-only under the realtime policy (it isn't a per-user topic), and it
// holds hashes and numbers only — no titles.
const TopicQueueProgress = eventbus.TopicQueueProgress

// progressEvery is how often the publisher looks at the queue while someone is connected.
const progressEvery = 2 * time.Second

// Publisher is the part of the event bus the progress publisher needs.
type Publisher interface {
	Publish(topic string, data any)
}

// ProgressItem is one unfinished download on queue.progress. Progress is rounded to half a
// percent, so a bar that hasn't visibly moved doesn't cost a message.
type ProgressItem struct {
	Hash       string  `json:"hash"`
	Progress   float64 `json:"progress"`
	ETASeconds int64   `json:"eta_seconds"`
	State      string  `json:"state"`
}

// progressList is what one tick would publish: every unfinished download, rounded.
func progressList(items []Item) []ProgressItem {
	out := []ProgressItem{}
	for _, it := range items {
		if it.Complete() {
			continue
		}
		out = append(out, ProgressItem{
			Hash:       it.Hash,
			Progress:   math.Round(it.Progress*200) / 200,
			ETASeconds: it.ETASeconds,
			State:      it.Phase(),
		})
	}
	// The client's own order isn't stable between reads; sorted, an unchanged queue
	// compares equal.
	sort.Slice(out, func(i, j int) bool { return out[i].Hash < out[j].Hash })
	return out
}

// sameProgress reports whether two ticks would say the same thing. ETA alone isn't a
// change worth a message: it wobbles every second while the progress stands still.
func sameProgress(a, b []ProgressItem) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Hash != b[i].Hash || a[i].Progress != b[i].Progress || a[i].State != b[i].State {
			return false
		}
	}
	return true
}

// progressPublisher remembers what it last said, so it only speaks on a change.
type progressPublisher struct {
	pub  Publisher
	last []ProgressItem
	sent bool // anything published yet (an empty list counts once, on the way to idle)
}

// tick publishes the queue's progress when it differs from the last message. Going idle
// publishes one empty list, so open bars can clear; staying idle publishes nothing.
func (p *progressPublisher) tick(items []Item) {
	cur := progressList(items)
	if len(cur) == 0 && (!p.sent || len(p.last) == 0) {
		return // idle and already said so (or never had anything to say)
	}
	if p.sent && sameProgress(cur, p.last) {
		return
	}
	p.pub.Publish(TopicQueueProgress, cur)
	p.last, p.sent = cur, true
}

// RunProgress publishes queue.progress every two seconds while active() says someone is
// listening (the websocket hub has a client) and the queue has moved. It reads the shared
// snapshot, so it adds no client calls of its own while a page is polling anyway. A read
// that fails publishes nothing: an outage isn't progress, and the page's own poll says so.
// Returns when ctx is cancelled.
func RunProgress(ctx context.Context, svc *Service, pub Publisher, active func() bool) {
	p := &progressPublisher{pub: pub}
	t := time.NewTicker(progressEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if active != nil && !active() {
			// Nobody to tell. Forget what was said, so whoever connects next gets the
			// current state on the first tick rather than only the next change.
			p.last, p.sent = nil, false
			continue
		}
		snap, err := svc.Snapshot(ctx)
		if err != nil {
			continue
		}
		p.tick(snap.Items)
	}
}
