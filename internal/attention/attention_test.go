package attention

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

var quiet = slog.New(slog.DiscardHandler)

// fakeBus records what was published.
type fakeBus struct {
	mu     sync.Mutex
	topics []string
	counts []Counts
}

func (b *fakeBus) Publish(topic string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.topics = append(b.topics, topic)
	if m, ok := data.(map[string]any); ok {
		if c, ok := m["counts"].(Counts); ok {
			b.counts = append(b.counts, c)
		}
	}
}

func (b *fakeBus) n() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.topics)
}

// clock is a settable time source shared by the service and its frame.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func staticFrame(f Frame) FrameFunc {
	return func(context.Context) *Frame { cp := f; return &cp }
}

type fakeHealth []health.Warning

func (h fakeHealth) Warnings() []health.Warning { return h }

func seeded(t *testing.T) (*store.Store, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st, context.Background()
}

func mustExec(t *testing.T, st *store.Store, q string, args ...any) {
	t.Helper()
	if _, err := st.DB().Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func byKey(items []Item) map[string]Item {
	out := map[string]Item{}
	for _, it := range items {
		out[it.Key] = it
	}
	return out
}

// The acceptance case: two pending requests, one held review, one errored torrent and
// one stalled for two hours read as requests=2, reviews=1, downloads=2, with stable keys
// and the right links. A requester's approved request and a resolved review don't count.
func TestRefreshAggregatesSeededSources(t *testing.T) {
	st, ctx := seeded(t)
	mustExec(t, st, `INSERT INTO requests (media_type, tmdb_id, title, year, status, requested_by, requested_by_name)
		VALUES ('movie', 1, 'Dune', 2021, 'pending', 7, 'Sam'), ('series', 2, 'Severance', 2022, 'pending', 8, 'Alex'),
		       ('movie', 3, 'Alien', 1979, 'approved', 7, 'Sam')`)
	mustExec(t, st, `INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, expected_title, reason, reason_code, status)
		VALUES ('aaa', 'Some.Release.1080p', '/dl/x', 'movie', 1, 'Dune', 'looks like another film', 'mismatch', 'pending'),
		       ('bbb', 'Old.Release', '/dl/y', 'movie', 2, 'Old', 'whatever', 'mismatch', 'resolved')`)

	clk := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	reqSvc := requests.NewService(st.DB(), nil, nil, nil, nil, nil, nil, "", quiet)
	coord := automation.New(nil, nil, nil, nil, st.DB(), nil, quiet, "")
	frame := Frame{OK: true, Whole: true,
		Queue: []download.Item{
			{Hash: "ERR1", Name: "Broken.Torrent", RawState: "error", State: "error"},
			{Hash: "STALL1", Name: "Dead.Torrent", RawState: "stalledDL", State: "downloading", Progress: 0.2, RemainingBytes: 10},
			{Hash: "OK1", Name: "Fine.Torrent", RawState: "downloading", State: "downloading", Progress: 0.5, RemainingBytes: 10},
		},
		// The record's stall clock says it last moved two hours ago.
		Acq: map[string]automation.Acquisition{"stall1": {Title: "Dead Release", ProgressAt: clk.now().Add(-2 * time.Hour)}},
	}
	bus := &fakeBus{}
	s := New(staticFrame(frame), bus, quiet, Requests(reqSvc), Reviews(coord), Downloads(), Health(fakeHealth(nil)))
	s.now = clk.now
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	snap := s.Current()
	want := Counts{Requests: 2, Reviews: 1, Downloads: 2, Total: 5}
	if snap.Counts != want {
		t.Fatalf("counts = %+v, want %+v", snap.Counts, want)
	}
	items := byKey(snap.Items)
	for _, k := range []string{"request:1", "request:2", "review:1", "download:err1", "stalled:stall1"} {
		if _, ok := items[k]; !ok {
			t.Errorf("missing item %q in %v", k, snap.Items)
		}
	}
	if it := items["request:1"]; it.Title != "Sam requested Dune (2021)" || it.Link != "/requests" || it.LinkKey != "requests" || it.Since == 0 {
		t.Errorf("request item = %+v", it)
	}
	if it := items["review:1"]; it.Link != "/review" || it.Title != "Dune is held: it looks like a different title" {
		t.Errorf("review item = %+v", it)
	}
	if it := items["download:err1"]; it.Level != LevelError || it.Link != "/downloads?show=problems" {
		t.Errorf("errored item = %+v", it)
	}
	if it := items["stalled:stall1"]; it.Title != "Dead Release has stalled" || it.Since != clk.now().Add(-2*time.Hour).UnixMilli() {
		t.Errorf("stalled item = %+v", it)
	}
	// Errors first.
	if snap.Items[0].Level != LevelError {
		t.Errorf("first item %+v isn't the error", snap.Items[0])
	}
	groups := map[string]Group{}
	for _, g := range snap.Groups {
		groups[g.Kind] = g
	}
	if g := groups[KindRequest]; g.Count != 2 || g.Title != "2 requests are waiting for approval" || g.Link != "/requests" || len(g.Sample) != 2 {
		t.Errorf("request group = %+v", g)
	}
	if g := groups[KindDownload]; g.Count != 1 || g.Level != LevelError {
		t.Errorf("download group = %+v", g)
	}
	if bus.n() != 1 || bus.topics[0] != TopicChanged || bus.counts[0] != want {
		t.Errorf("published %v %v, want one %s with the counts", bus.topics, bus.counts, TopicChanged)
	}

	// Approving the requests drops the count on the next refresh.
	mustExec(t, st, `UPDATE requests SET status = 'approved'`)
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Current().Counts; got.Requests != 0 || got.Total != 3 {
		t.Errorf("after approving: %+v", got)
	}
}

// A download that has just stalled isn't news; one stalled for an hour is. Without a
// record of the torrent, the hour is counted from when this run first saw it stalled, and
// a torrent that picks up again starts over.
func TestStalledOnlyAfterAnHour(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	stalled := download.Item{Hash: "H", Name: "Slow", RawState: "stalledDL", Progress: 0.1, RemainingBytes: 1}
	q := []download.Item{stalled}
	var mu sync.Mutex
	frame := func(context.Context) *Frame {
		mu.Lock()
		defer mu.Unlock()
		return &Frame{OK: true, Whole: true, Queue: append([]download.Item(nil), q...)}
	}
	s := New(frame, nil, quiet, Downloads())
	s.now = clk.now
	ctx := context.Background()
	refresh := func() Counts {
		t.Helper()
		if err := s.Refresh(ctx); err != nil {
			t.Fatal(err)
		}
		return s.Current().Counts
	}
	if c := refresh(); c.Downloads != 0 {
		t.Fatalf("just stalled: %+v", c)
	}
	clk.add(59 * time.Minute)
	if c := refresh(); c.Downloads != 0 {
		t.Fatalf("after 59m: %+v", c)
	}
	clk.add(2 * time.Minute)
	if c := refresh(); c.Downloads != 1 {
		t.Fatalf("after 61m: %+v", c)
	}
	// Moving again resets it; stalling again starts a fresh hour.
	mu.Lock()
	q[0].RawState = "downloading"
	mu.Unlock()
	if c := refresh(); c.Downloads != 0 {
		t.Fatalf("moving again: %+v", c)
	}
	mu.Lock()
	q[0].RawState = "stalledDL"
	mu.Unlock()
	clk.add(30 * time.Minute)
	if c := refresh(); c.Downloads != 0 {
		t.Fatalf("stalled again for 0m: %+v", c)
	}
}

// With the client unreadable the downloads source reports nothing (the health finding
// says the client is down); the endpoint still answers from the snapshot.
func TestUnreadableQueueReportsHealthNotDownloads(t *testing.T) {
	s := New(staticFrame(Frame{}), nil, quiet, Downloads(),
		Health(fakeHealth{{Key: "downloads.client.1", Level: health.LevelError, Message: "qBittorrent isn't answering",
			Link: "/downloadclients", LinkKey: "downloadClients", Since: time.Unix(1000, 0)}}))
	if err := s.Refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap := s.Current()
	if snap.Counts.Downloads != 0 || snap.Counts.Health != 1 || snap.Counts.HealthErrors != 1 {
		t.Fatalf("counts = %+v", snap.Counts)
	}
	it := snap.Items[0]
	if it.Key != "health:downloads.client.1" || it.LinkKey != "downloadClients" || it.Since != 1000_000 {
		t.Errorf("health item = %+v", it)
	}
}

// flaky fails when told to.
type flaky struct {
	mu   sync.Mutex
	fail bool
	n    int
}

func (*flaky) Name() string { return "flaky" }
func (f *flaky) Collect(context.Context, *Frame) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("database is locked")
	}
	var out []Item
	for i := 0; i < f.n; i++ {
		out = append(out, Item{Key: KindReview + ":" + itoa(i+1), Kind: KindReview, Title: "x"})
	}
	return out, nil
}

// A source that fails keeps what it said last time, so a locked database doesn't make
// every badge drop to zero — and counts that don't change publish nothing.
func TestProviderErrorKeepsPreviousItemsAndPublishesOnlyOnChange(t *testing.T) {
	f := &flaky{n: 2}
	bus := &fakeBus{}
	s := New(nil, bus, quiet, f)
	ctx := context.Background()
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.fail = true
	f.mu.Unlock()
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if c := s.Current().Counts; c.Reviews != 2 {
		t.Fatalf("after a failure: %+v, want the previous 2 reviews", c)
	}
	if bus.n() != 1 {
		t.Errorf("published %d times, want once (the counts didn't change)", bus.n())
	}
	f.mu.Lock()
	f.fail, f.n = false, 1
	f.mu.Unlock()
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if bus.n() != 2 || bus.counts[1].Reviews != 1 {
		t.Errorf("published %v, want a second event with 1 review", bus.counts)
	}
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	if bus.n() != 2 {
		t.Errorf("an unchanged refresh published again (%d)", bus.n())
	}
}

// A kick refreshes within a moment, without waiting for the scheduled tick.
func TestKickRefreshes(t *testing.T) {
	f := &flaky{n: 1}
	s := New(nil, nil, quiet, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	s.Kick()
	s.Kick() // coalesced
	deadline := time.Now().Add(5 * time.Second)
	for s.Current() == nil && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.Current() == nil || s.Current().Counts.Reviews != 1 {
		t.Fatalf("no refresh after a kick: %+v", s.Current())
	}
	cancel()
	<-done
	var nilSvc *Service
	nilSvc.Kick() // safe
	if nilSvc.Current() != nil {
		t.Error("nil service has a snapshot")
	}
}

// Stuck searches are one aggregate item counting every title past the threshold, from
// the COUNT helpers over a seeded library.
func TestSearchesCountStuckTitles(t *testing.T) {
	st, ctx := seeded(t)
	db := st.DB()
	// Movies: one stuck, one with too few misses, one with its file, one unmonitored.
	mustExec(t, st, `INSERT INTO movies (tmdb_id, title, year, monitored, has_file, search_misses) VALUES
		(1, 'Stuck', 2020, 1, 0, 12), (2, 'Young', 2020, 1, 0, 3), (3, 'Have', 2020, 1, 1, 40), (4, 'Off', 2020, 0, 0, 40)`)
	// Series: one stuck with an aired missing episode; one stuck but only missing a future one.
	mustExec(t, st, `INSERT INTO series (id, tmdb_id, title, year, monitored, search_misses) VALUES
		(1, 10, 'Stuck Show', 2020, 1, 11), (2, 11, 'Waiting Show', 2020, 1, 11)`)
	mustExec(t, st, `INSERT INTO episodes (series_id, season_number, episode_number, air_date, monitored, has_file) VALUES
		(1, 1, 1, '2020-01-01', 1, 0), (2, 1, 1, '2999-01-01', 1, 0)`)
	// Books: one slowed to monthly with nothing, one slowed but complete, one still early.
	mustExec(t, st, `INSERT INTO books (ol_key, title, author, monitored, search_misses, ebook_path, audiobook_path) VALUES
		('OL1W', 'Lost', 'A', 1, 11, '', ''), ('OL2W', 'Done', 'B', 1, 11, '/e', '/a'), ('OL3W', 'Early', 'C', 1, 4, '', '')`)

	mv := movies.NewService(db, nil, nil, t.TempDir(), "", nil, quiet)
	sr := series.NewService(db, nil, t.TempDir(), quiet)
	bk := books.NewService(db, nil, quiet)
	s := New(nil, nil, quiet, Searches(
		func(ctx context.Context) (int, error) { return mv.CountSearchStuck(ctx, SearchStuckAfter) },
		func(ctx context.Context) (int, error) { return sr.CountSearchStuck(ctx, SearchStuckAfter) },
		bk.CountSearchGivenUp,
	))
	if err := s.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	snap := s.Current()
	if snap.Counts.Searches != 3 || len(snap.Items) != 1 {
		t.Fatalf("counts %+v items %v, want 3 stuck titles in one item", snap.Counts, snap.Items)
	}
	it := snap.Items[0]
	if it.Key != "search:stuck" || it.Count != 3 || it.Title != "3 titles still haven't found a release" || it.LinkKey != "downloadsSearching" {
		t.Errorf("search item = %+v", it)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Count != 3 || len(snap.Groups[0].Sample) != 0 {
		t.Errorf("groups = %+v", snap.Groups)
	}
}

// Items without a time of their own keep the time they were first seen across refreshes,
// and get a new one once they have gone and come back.
func TestSinceIsFirstSeen(t *testing.T) {
	clk := &clock{t: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)}
	f := &flaky{n: 1}
	s := New(nil, nil, quiet, f)
	s.now = clk.now
	ctx := context.Background()
	_ = s.Refresh(ctx)
	first := s.Current().Items[0].Since
	clk.add(time.Minute)
	_ = s.Refresh(ctx)
	if got := s.Current().Items[0].Since; got != first {
		t.Errorf("since moved: %d → %d", first, got)
	}
	f.mu.Lock()
	f.n = 0
	f.mu.Unlock()
	_ = s.Refresh(ctx)
	f.mu.Lock()
	f.n = 1
	f.mu.Unlock()
	clk.add(time.Minute)
	_ = s.Refresh(ctx)
	if got := s.Current().Items[0].Since; got == first {
		t.Error("a problem that came back kept its old since")
	}
}
