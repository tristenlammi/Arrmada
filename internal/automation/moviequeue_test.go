package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/store"
)

func newQueueRunner(t *testing.T) *jobs.Runner {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, err := jobs.New(context.Background(), st.DB(), slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown(2 * time.Second) })
	return r
}

func searchSpec(id int64, fn func(ctx context.Context) error) jobs.Spec {
	return jobs.Spec{Kind: "movie.search", Target: fmt.Sprintf("movie:%d", id), Class: jobs.ClassIndexerSearch, Timeout: time.Minute,
		Fn: func(ctx context.Context, _ *jobs.Progress) (any, error) { return nil, fn(ctx) }}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Clicking Search twice on the same movie queues it once; the movie is busy until the
// search has run.
func TestMovieQueueDedupe(t *testing.T) {
	r := newQueueRunner(t)
	c := &Coordinator{}
	release := make(chan struct{})
	var calls atomic.Int32
	spec := searchSpec(7, func(ctx context.Context) error { calls.Add(1); <-release; return nil })
	first, err := c.EnqueueMovieSearch(context.Background(), r, 7, spec)
	if err != nil || first.Existing || first.JobID == 0 {
		t.Fatalf("first = %+v, %v", first, err)
	}
	second, err := c.EnqueueMovieSearch(context.Background(), r, 7, spec)
	if err != nil || !second.Existing || second.JobID != first.JobID {
		t.Fatalf("second = %+v, %v; want the first job back", second, err)
	}
	// A different kind of search for the same movie is different work.
	up := spec
	up.Kind = "movie.upgrade"
	if q, _ := c.EnqueueMovieSearch(context.Background(), r, 7, up); q.Existing {
		t.Fatalf("an upgrade joined the missing search: %+v", q)
	}
	if !c.MovieSearchBusy(7) || c.MovieSearchBusy(8) {
		t.Fatal("busy is wrong while queued")
	}
	close(release)
	waitFor(t, "the queue to empty", func() bool { return !c.MovieSearchBusy(7) })
	if n := calls.Load(); n != 2 {
		t.Fatalf("ran %d searches, want 2 (one search, one upgrade)", n)
	}
	if st := c.MovieSearchQueue(); len(st.Running)+len(st.Queued) != 0 {
		t.Fatalf("queue after = %+v", st)
	}
}

// However many are queued, at most two movie searches run at once, every one runs, and
// the waiting ones get positions in order.
func TestMovieQueueConcurrencyBound(t *testing.T) {
	r := newQueueRunner(t)
	c := &Coordinator{}
	var inFlight, peak, done atomic.Int32
	gate := make(chan struct{})
	fn := func(ctx context.Context) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-gate
		time.Sleep(2 * time.Millisecond)
		inFlight.Add(-1)
		done.Add(1)
		return nil
	}
	const n = 12
	var last MovieQueued
	for i := int64(1); i <= n; i++ {
		q, err := c.EnqueueMovieSearch(context.Background(), r, i, searchSpec(i, fn))
		if err != nil {
			t.Fatal(err)
		}
		last = q
	}
	waitFor(t, "two searches to start", func() bool { return len(c.MovieSearchQueue().Running) == 2 })
	if st := c.MovieSearchQueue(); len(st.Queued) != n-2 {
		t.Fatalf("queue = %d running, %d queued", len(st.Running), len(st.Queued))
	}
	if last.Position < 1 || last.Position > n {
		t.Fatalf("last position = %d", last.Position)
	}
	close(gate)
	waitFor(t, "every search to run", func() bool { return done.Load() == n })
	if p := peak.Load(); p > 2 {
		t.Fatalf("%d searches ran at once, want at most 2", p)
	}
}

// A search that waits longer than its timeout behind others still gets its whole budget
// once it starts: the clock starts at dequeue, not at enqueue.
func TestMovieQueueTimeoutStartsAtDequeue(t *testing.T) {
	r := newQueueRunner(t)
	r.SetLimit(jobs.ClassIndexerSearch, 1)
	c := &Coordinator{}
	hold := searchSpec(1, func(ctx context.Context) error { time.Sleep(150 * time.Millisecond); return nil })
	if _, err := c.EnqueueMovieSearch(context.Background(), r, 1, hold); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the first search to start", func() bool { return len(c.MovieSearchQueue().Running) == 1 })
	var budget atomic.Int64
	waiting := searchSpec(2, func(ctx context.Context) error {
		if dl, ok := ctx.Deadline(); ok {
			budget.Store(int64(time.Until(dl)))
		}
		return ctx.Err()
	})
	waiting.Timeout = 80 * time.Millisecond
	q, err := c.EnqueueMovieSearch(context.Background(), r, 2, waiting)
	if err != nil || q.Position != 1 {
		t.Fatalf("queued = %+v, %v", q, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Wait(ctx, q.JobID); err != nil {
		t.Fatal(err)
	}
	j, _ := r.Get(ctx, q.JobID)
	if j.Status != jobs.StatusSucceeded {
		t.Fatalf("the waiting search ended %s (%s): its timeout ran while it waited", j.Status, j.Error)
	}
	if b := time.Duration(budget.Load()); b < 40*time.Millisecond {
		t.Fatalf("budget at start = %v, want most of 80ms", b)
	}
}

// Shutdown drops what is still waiting: nothing runs, and nothing stays busy.
func TestMovieQueueStopsOnCancel(t *testing.T) {
	r := newQueueRunner(t)
	r.SetLimit(jobs.ClassIndexerSearch, 1)
	c := &Coordinator{}
	running := make(chan struct{})
	block := searchSpec(1, func(ctx context.Context) error { close(running); <-ctx.Done(); return ctx.Err() })
	if _, err := c.EnqueueMovieSearch(context.Background(), r, 1, block); err != nil {
		t.Fatal(err)
	}
	<-running
	var ran atomic.Bool
	if _, err := c.EnqueueMovieSearch(context.Background(), r, 2, searchSpec(2, func(context.Context) error { ran.Store(true); return nil })); err != nil {
		t.Fatal(err)
	}
	r.Shutdown(2 * time.Second)
	if ran.Load() {
		t.Fatal("a queued search ran after shutdown")
	}
	if c.MovieSearchBusy(1) || c.MovieSearchBusy(2) {
		t.Fatal("a movie stayed busy after shutdown")
	}
	if _, err := c.EnqueueMovieSearch(context.Background(), r, 3, searchSpec(3, func(context.Context) error { return nil })); err == nil {
		t.Fatal("queued after shutdown")
	}
	if c.MovieSearchBusy(3) {
		t.Fatal("a refused search left its movie busy")
	}
}

// The queue announces each change with the counts after it, in order.
func TestMovieQueueEvents(t *testing.T) {
	r := newQueueRunner(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	bus := eventbus.New(log)
	evs, cancel := bus.Subscribe(TopicMovieSearchQueued, TopicMovieSearchStarted, TopicMovieSearchDone)
	defer cancel()
	c := &Coordinator{bus: bus, log: log}
	if _, err := c.EnqueueMovieSearch(context.Background(), r, 4, searchSpec(4, func(context.Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	var topics []string
	timeout := time.After(5 * time.Second)
	for len(topics) < 3 {
		select {
		case e := <-evs:
			topics = append(topics, e.Topic)
			d := e.Data.(map[string]any)
			if d["id"] != int64(4) || d["kind"] != "search" {
				t.Fatalf("event %s = %v", e.Topic, d)
			}
			if e.Topic == TopicMovieSearchDone && (d["depth"] != 0 || d["running"] != 0) {
				t.Fatalf("done counts = %v", d)
			}
		case <-timeout:
			t.Fatalf("events so far: %v", topics)
		}
	}
	if fmt.Sprint(topics) != fmt.Sprint([]string{TopicMovieSearchQueued, TopicMovieSearchStarted, TopicMovieSearchDone}) {
		t.Fatalf("topics = %v", topics)
	}
}

// The scheduled sweep leaves a movie alone while a search for it waits in the queue, and
// doesn't count that as a miss.
func TestSearchMissingSkipsBusy(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	r := newQueueRunner(t)
	r.SetLimit(jobs.ClassIndexerSearch, 1)
	release := make(chan struct{})
	defer close(release)
	// Something else holds the only slot, so the movie's search stays queued.
	if _, _, err := r.Submit(h.ctx, jobs.Spec{Kind: "hold", Class: jobs.ClassIndexerSearch, Fn: func(context.Context, *jobs.Progress) (any, error) {
		<-release
		return nil, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.EnqueueMovieSearch(h.ctx, r, mid, searchSpec(mid, func(context.Context) error { return nil })); err != nil {
		t.Fatal(err)
	}
	h.c.SearchMissing(h.ctx)
	if n := h.ix.searchCount(); n != 0 {
		t.Fatalf("the sweep searched a queued movie %d times", n)
	}
	if _, misses := h.c.movies.SearchState(h.ctx, mid); misses != 0 {
		t.Fatalf("a skipped movie counted %d misses", misses)
	}
}
