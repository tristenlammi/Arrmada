package requests

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/movies"
)

// fakeLocator is the Plex library index in memory: titles are "in Plex" by key
// ("movie:603", "series:77", or "imdb:tt…" for a legacy-agent match).
type fakeLocator struct {
	mu         sync.Mutex
	configured bool
	have       map[string]bool
	built      time.Time
	refreshes  []time.Duration
}

func newFakeLocator() *fakeLocator { return &fakeLocator{configured: true, have: map[string]bool{}} }

func (f *fakeLocator) Configured(context.Context) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.configured
}

func (f *fakeLocator) Find(_ context.Context, media string, ids PlexIDs) (string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.built.IsZero() {
		return "", false // the index hasn't been read yet
	}
	for _, k := range []string{fmt.Sprintf("%s:%d", media, ids.TMDB), "imdb:" + ids.IMDB} {
		if f.have[k] {
			return "https://app.plex.tv/desktop/#!/server/m1/details?key=" + k, true
		}
	}
	return "", false
}

func (f *fakeLocator) IndexBuiltAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.built
}

func (f *fakeLocator) Refresh(after time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes = append(f.refreshes, after)
}

// rebuilt is an index read at t that has the given titles.
func (f *fakeLocator) rebuilt(t time.Time, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.built = t
	for _, k := range keys {
		f.have[k] = true
	}
}

// testClock is a clock tests move by hand.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// plexMovieFixture is a Service with Dune (TMDB 603) on disk, asked for by user 7 and
// approved, Plex set up and its index empty.
func plexMovieFixture(t *testing.T) (*Service, *fakeLocator, *testClock, *fakePush, Request) {
	t.Helper()
	s, push, _ := quietFixture(t, movies.Movie{TMDBID: 603, Title: "Dune", HasFile: true, IMDBID: "tt1160419"})
	s.coord = nil // nothing is downloading: Track reads the library alone
	loc := newFakeLocator()
	clk := &testClock{t: time.Now()}
	s.now = clk.now
	s.SetPlexLocator(loc)
	req, err := s.repo.Create(context.Background(), Request{MediaType: "movie", TMDBID: 603, Title: "Dune", Status: StatusApproved, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	return s, loc, clk, push, req
}

func plexInbox(t *testing.T, s *Service, uid int64) []string {
	t.Helper()
	inbox, err := s.repo.listUserNotifications(context.Background(), uid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, n := range inbox {
		out = append(out, n.Body)
	}
	return out
}

func stageOf(t *testing.T, s *Service, id int64) string {
	t.Helper()
	ctx := context.Background()
	list, _, err := s.List(ctx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	s.Track(ctx, list, nil, true)
	for _, r := range list {
		if r.ID == id && r.Tracking != nil {
			return r.Tracking.Stage
		}
	}
	return ""
}

// With Plex set up, the import's 'ready' waits: no inbox row while Plex hasn't got the
// title, the card says Adding to Plex, and once the index shows it one notice goes out —
// "on Plex" — and the request is stamped ready.
func TestReadyWaitsForPlex(t *testing.T) {
	s, loc, clk, push, req := plexMovieFixture(t)
	ctx := context.Background()

	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 7); len(got) != 0 {
		t.Fatalf("told before Plex had it: %v", got)
	}
	if got, _ := s.repo.Get(ctx, req.ID); got.onDiskAt == 0 || got.ReadyAt != 0 {
		t.Fatalf("on_disk_at %d ready_at %d, want waiting", got.onDiskAt, got.ReadyAt)
	}
	if st := stageOf(t, s, req.ID); st != StageAdding {
		t.Errorf("stage = %q, want %q", st, StageAdding)
	}
	if len(loc.refreshes) == 0 {
		t.Error("waiting asked for no index read")
	}

	// The index is read again and has it.
	clk.add(2 * time.Minute)
	loc.rebuilt(clk.now(), "movie:603")
	if err := s.CheckPlexWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	got := plexInbox(t, s, 7)
	if len(got) != 1 || got[0] != "“Dune” is ready to watch on Plex." {
		t.Fatalf("inbox = %v, want one 'ready to watch on Plex'", got)
	}
	if r, _ := s.repo.Get(ctx, req.ID); r.ReadyAt == 0 {
		t.Error("ready_at not stamped")
	}
	if st := stageOf(t, s, req.ID); st != StageAvailable {
		t.Errorf("stage = %q, want available", st)
	}
	// Later checks, sweeps and imports tell nobody again.
	_ = s.CheckPlexWaiting(ctx)
	_ = s.SweepReadyRequests(ctx)
	_ = s.NotifyMovieReady(ctx, 1)
	if got := plexInbox(t, s, 7); len(got) != 1 || len(push.sent()) != 1 {
		t.Errorf("after repeats: inbox %v, pushes %v", got, push.sent())
	}
}

// Without Plex — no locator, or one with no server set up — 'ready' goes out on import,
// worded as always, and nothing waits.
func TestReadyWithoutPlexImmediate(t *testing.T) {
	for _, tc := range []struct {
		name string
		loc  PlexLocator
	}{{"no locator", nil}, {"not configured", &fakeLocator{have: map[string]bool{}}}} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _, _, req := plexMovieFixture(t)
			s.plex = tc.loc
			ctx := context.Background()
			if err := s.NotifyMovieReady(ctx, 1); err != nil {
				t.Fatal(err)
			}
			if got := plexInbox(t, s, 7); !reflect.DeepEqual(got, []string{"“Dune” is ready to watch."}) {
				t.Fatalf("inbox = %v", got)
			}
			if r, _ := s.repo.Get(ctx, req.ID); r.onDiskAt != 0 || r.ReadyAt == 0 {
				t.Errorf("on_disk_at %d ready_at %d, want told at once", r.onDiskAt, r.ReadyAt)
			}
		})
	}
}

// Plex never shows the title, and its index never even builds: after the grace period the
// Plex check sends the fallback wording once. Later checks — and a restarted service over
// the same database — send nothing more.
func TestReadyGraceFallback(t *testing.T) {
	s, _, clk, _, req := plexMovieFixture(t)
	ctx := context.Background()
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.add(29 * time.Minute)
	_ = s.CheckPlexWaiting(ctx)
	if got := plexInbox(t, s, 7); len(got) != 0 {
		t.Fatalf("sent before the grace period: %v", got)
	}
	clk.add(2 * time.Minute)
	if err := s.CheckPlexWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	want := []string{"“Dune” is ready — it may take a few more minutes to show up in Plex."}
	if got := plexInbox(t, s, 7); !reflect.DeepEqual(got, want) {
		t.Fatalf("inbox = %v, want %v", got, want)
	}
	if r, _ := s.repo.Get(ctx, req.ID); r.ReadyAt == 0 {
		t.Error("ready_at not stamped")
	}
	_ = s.CheckPlexWaiting(ctx)
	_ = s.SweepReadyRequests(ctx)
	restarted := &Service{repo: s.repo, movies: s.movies, log: s.log, now: clk.now}
	restarted.SetPlexLocator(newFakeLocator())
	_ = restarted.CheckPlexWaiting(ctx)
	_ = restarted.SweepReadyRequests(ctx)
	if got := plexInbox(t, s, 7); len(got) != 1 {
		t.Errorf("told again: %v", got)
	}
}

// The wait survives a restart with its clock: a service started 31 minutes after the
// file landed sends the fallback at its first check, not 30 minutes later.
func TestPlexWaitSurvivesRestart(t *testing.T) {
	s, _, clk, _, _ := plexMovieFixture(t)
	ctx := context.Background()
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	clk.add(31 * time.Minute)
	restarted := &Service{repo: s.repo, movies: s.movies, log: s.log, now: clk.now}
	restarted.SetPlexLocator(newFakeLocator())
	if err := restarted.CheckPlexWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 7); len(got) != 1 || !strings.Contains(got[0], "may take a few more minutes") {
		t.Fatalf("inbox = %v", got)
	}
}

// A file that goes again before anyone is told resets the wait; it starts afresh (and
// the grace period with it) when the file is back.
func TestPlexWaitResetsWhenTheFileGoes(t *testing.T) {
	s, _, clk, _, req := plexMovieFixture(t)
	ctx := context.Background()
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	lib := s.movies.(*fakeMovies)
	lib.mu.Lock()
	m := lib.byTMDB[603]
	m.HasFile = false
	lib.byTMDB[603] = m
	lib.mu.Unlock()
	clk.add(40 * time.Minute)
	if err := s.CheckPlexWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 7); len(got) != 0 {
		t.Fatalf("told about a file that's gone: %v", got)
	}
	if r, _ := s.repo.Get(ctx, req.ID); r.onDiskAt != 0 {
		t.Errorf("on_disk_at = %d, want reset", r.onDiskAt)
	}
}

// A legacy-agent library knows the film by its IMDb id only: the library record's ids
// find it.
func TestReadyFindsLegacyAgentTitles(t *testing.T) {
	s, loc, clk, _, _ := plexMovieFixture(t)
	ctx := context.Background()
	loc.rebuilt(clk.now(), "imdb:tt1160419")
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 7); len(got) != 1 || !strings.Contains(got[0], "on Plex") {
		t.Fatalf("inbox = %v, want told at once (Plex already had it)", got)
	}
}

// Books are never in Plex: their 'ready' goes out at once with Plex set up.
func TestBooksNotGated(t *testing.T) {
	s, loc, _, _, _ := plexMovieFixture(t)
	ctx := context.Background()
	if !loc.Configured(ctx) {
		t.Fatal("fixture: Plex not set up")
	}
	book, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune Messiah", Status: StatusApproved, RequestedBy: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.notifyReady(ctx, book); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 8); !reflect.DeepEqual(got, []string{"“Dune Messiah” is ready to read."}) {
		t.Fatalf("inbox = %v", got)
	}
	if r, _ := s.repo.Get(ctx, book.ID); r.onDiskAt != 0 || r.ReadyAt == 0 {
		t.Errorf("book on_disk_at %d ready_at %d", r.onDiskAt, r.ReadyAt)
	}
	if addingToPlex(book, true) {
		t.Error("a book reads Adding to Plex")
	}
}

// The card: complete and not told reads 'adding' only while 'ready' waits for Plex.
func TestTrackAddingStage(t *testing.T) {
	approved := Request{Status: StatusApproved, MediaType: "movie"}
	for _, tc := range []struct {
		name  string
		rq    Request
		gated bool
		want  bool
	}{
		{"waiting", Request{Status: StatusApproved, MediaType: "movie", onDiskAt: 5}, false, true},
		{"plex set up, not judged yet", approved, true, true},
		{"no plex", approved, false, false},
		{"told", Request{Status: StatusApproved, MediaType: "series", ReadyAt: 9, onDiskAt: 5}, true, false},
		{"declined but on disk", Request{Status: StatusDeclined, MediaType: "movie"}, true, false},
		{"book", Request{Status: StatusApproved, MediaType: "book"}, true, false},
	} {
		if got := addingToPlex(tc.rq, tc.gated); got != tc.want {
			t.Errorf("%s: adding = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// A show Plex already has (an earlier season) only counts as having the new files in an
// index read plexSettle after they landed; until then the request waits and asks for a read.
func TestSeriesWaitsForAFreshIndex(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2))
	loc := newFakeLocator()
	clk := &testClock{t: time.Now()}
	f.s.now = clk.now
	f.s.SetPlexLocator(loc)
	loc.rebuilt(clk.now().Add(-time.Hour), "series:77") // S1 has been in Plex for ages
	sr, err := f.shows().Add(f.ctx, 77, "", false)
	if err != nil {
		t.Fatal(err)
	}
	fillSeason(t, f, sr.ID, 1, 2)
	req, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2}, KnownSeasons: []int{1, 2}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	readyRef := fmt.Sprintf("series:77:r%d", req.ID)
	fillSeason(t, f, sr.ID, 2, 2)
	if err := f.s.NotifySeriesReady(f.ctx, sr.ID); err != nil {
		t.Fatal(err)
	}
	if contains(refs(inbox(t, f, 7)), readyRef) {
		t.Fatal("told on the strength of an index read before the season landed")
	}
	if len(loc.refreshes) == 0 || loc.refreshes[len(loc.refreshes)-1] < plexSettle {
		t.Errorf("refreshes = %v, want a read asked for after the settle time", loc.refreshes)
	}
	// An index read one minute on is still too early; one three minutes on counts.
	clk.add(time.Minute)
	loc.rebuilt(clk.now())
	_ = f.s.CheckPlexWaiting(f.ctx)
	if contains(refs(inbox(t, f, 7)), readyRef) {
		t.Fatal("told after an index read inside the settle time")
	}
	clk.add(2 * time.Minute)
	loc.rebuilt(clk.now())
	if err := f.s.CheckPlexWaiting(f.ctx); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, n := range inbox(t, f, 7) {
		if n.Ref == readyRef {
			body = n.Body
		}
	}
	if body != "Season 2 of “Show” is ready to watch on Plex." {
		t.Fatalf("ready body = %q", body)
	}
}

// A request for several seasons: each season's own notice waits for Plex too, is sent
// once, and leaves the Plex check's list once sent; the final 'ready' waits in turn.
func TestSeasonNoticesWaitForPlex(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2, 2))
	loc := newFakeLocator()
	clk := &testClock{t: time.Now()}
	f.s.now = clk.now
	f.s.SetPlexLocator(loc)
	sr, err := f.shows().Add(f.ctx, 77, "", false)
	if err != nil {
		t.Fatal(err)
	}
	req, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2, 3}, KnownSeasons: []int{1, 2, 3}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	s2 := fmt.Sprintf("series:77:r%d:s2", req.ID)
	fillSeason(t, f, sr.ID, 2, 2)
	if err := f.s.NotifySeriesReady(f.ctx, sr.ID); err != nil {
		t.Fatal(err)
	}
	if contains(refs(inbox(t, f, 7)), s2) {
		t.Fatal("season notice sent before Plex had it")
	}
	if w, _ := f.s.repo.ListPlexWaiting(f.ctx); len(w) != 1 {
		t.Fatalf("waiting = %d rows, want the request", len(w))
	}
	clk.add(3 * time.Minute)
	loc.rebuilt(clk.now(), "series:77")
	if err := f.s.CheckPlexWaiting(f.ctx); err != nil {
		t.Fatal(err)
	}
	if !contains(refs(inbox(t, f, 7)), s2) {
		t.Fatalf("refs = %v, want the S2 notice", refs(inbox(t, f, 7)))
	}
	if w, _ := f.s.repo.ListPlexWaiting(f.ctx); len(w) != 0 {
		t.Errorf("a told season keeps the request on the Plex check's list")
	}
	// The last season lands: the final notice waits for a fresh read too.
	fillSeason(t, f, sr.ID, 3, 2)
	_ = f.s.NotifySeriesReady(f.ctx, sr.ID)
	final := fmt.Sprintf("series:77:r%d", req.ID)
	if contains(refs(inbox(t, f, 7)), final) {
		t.Fatal("final notice before a fresh index read")
	}
	clk.add(3 * time.Minute)
	loc.rebuilt(clk.now())
	_ = f.s.CheckPlexWaiting(f.ctx)
	_ = f.s.CheckPlexWaiting(f.ctx)
	got := refs(inbox(t, f, 7))
	n := 0
	for _, r := range got {
		if r == s2 || r == final {
			n++
		}
	}
	if !contains(got, final) || n != 2 {
		t.Errorf("refs = %v, want S2 and the final notice once each", got)
	}
}

// A 'ready' notice for a title Plex has carries the title's Watch on Plex page: on the
// inbox row, in the Web Push (next to the title's own address) and at the end of the
// personal Apprise message. A notice sent after the grace period carries none.
func TestReadyNoticeCarriesPlexLink(t *testing.T) {
	s, loc, clk, _, _ := plexMovieFixture(t)
	ctx := context.Background()
	pushes := &urlPush{}
	s.push = pushes
	var apprise []string
	s.userApprise.resolver = fixedResolver{"push.example.com": "93.184.216.34"}
	s.userApprise.send = func(_ context.Context, _, _, body string) error { apprise = append(apprise, body); return nil }
	if _, err := s.repo.db.ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash, apprise_url) VALUES (7, 'kid', 'requester', 'x', 'gotify://push.example.com/token')`); err != nil {
		t.Fatal(err)
	}
	loc.rebuilt(clk.now(), "movie:603")
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	want := "https://app.plex.tv/desktop/#!/server/m1/details?key=movie:603"
	inbox, err := s.repo.listUserNotifications(ctx, 7)
	if err != nil || len(inbox) != 1 || inbox[0].PlexURL != want {
		t.Fatalf("inbox = %+v (%v), want one row linking %q", inbox, err, want)
	}
	pushes.mu.Lock()
	gotPush := append([]string(nil), pushes.urls...)
	gotPlex := append([]string(nil), pushes.plex...)
	pushes.mu.Unlock()
	if !reflect.DeepEqual(gotPush, []string{"/discover/movie/603"}) || !reflect.DeepEqual(gotPlex, []string{want}) {
		t.Errorf("push opens %v, plex %v", gotPush, gotPlex)
	}
	if len(apprise) != 1 || apprise[0] != "“Dune” is ready to watch on Plex.\nWatch: "+want {
		t.Errorf("apprise = %q", apprise)
	}

	// The fallback has no link to give.
	late, err := s.repo.Create(ctx, Request{MediaType: "movie", TMDBID: 604, Title: "Arrival", Status: StatusApproved, RequestedBy: 8})
	if err != nil {
		t.Fatal(err)
	}
	lib := s.movies.(*fakeMovies)
	lib.mu.Lock()
	lib.byTMDB[604] = movies.Movie{ID: 2, TMDBID: 604, Title: "Arrival", HasFile: true}
	lib.mu.Unlock()
	_ = s.notifyReady(ctx, late)
	clk.add(31 * time.Minute)
	_ = s.CheckPlexWaiting(ctx)
	if inbox, _ := s.repo.listUserNotifications(ctx, 8); len(inbox) != 1 || inbox[0].PlexURL != "" {
		t.Errorf("fallback inbox = %+v, want one row with no link", inbox)
	}
}
