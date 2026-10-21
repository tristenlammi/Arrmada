package issues

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeLibrary is a library of a few titles, by TMDB id. Movies have season/episode 0.
type fakeLibrary struct {
	mu    sync.Mutex
	items map[string]Item // "movie:603", "series:1399:1:4"
}

func key(media string, id int64, season, episode int) string {
	if media == MediaMovie {
		return fmt.Sprintf("movie:%d", id)
	}
	return fmt.Sprintf("series:%d:%d:%d", id, season, episode)
}

func (l *fakeLibrary) put(media string, tmdb int, libID int64, season, episode int, title, path string, size int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items[key(media, int64(tmdb), season, episode)] = Item{LibraryID: libID, TMDBID: tmdb, Title: title, Path: path, Size: size, Release: title + ".1080p-GRP"}
}

func (l *fakeLibrary) Find(_ context.Context, media string, tmdbID, season, episode int) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	it, ok := l.items[key(media, int64(tmdbID), season, episode)]
	if !ok {
		return Item{}, ErrNotInLibrary
	}
	return it, nil
}

func (l *fakeLibrary) Current(_ context.Context, t Target) (Item, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, it := range l.items {
		if it.LibraryID == t.LibraryID && strings.HasPrefix(k, t.MediaType+":") &&
			(t.MediaType == MediaMovie || strings.HasSuffix(k, fmt.Sprintf(":%d:%d", t.Season, t.Episode))) {
			return it, nil
		}
	}
	return Item{}, ErrNotInLibrary
}

func (l *fakeLibrary) EpisodesWithFiles(_ context.Context, tmdbID int) (string, []SeasonFiles, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	title := ""
	var eps []EpisodeFile
	for k, it := range l.items {
		var id, s, e int
		if _, err := fmt.Sscanf(k, "series:%d:%d:%d", &id, &s, &e); err == nil && id == tmdbID && it.Path != "" {
			title = it.Title
			eps = append(eps, EpisodeFile{Episode: e})
		}
	}
	if title == "" {
		return "", nil, ErrNotInLibrary
	}
	return title, []SeasonFiles{{Season: 1, Episodes: eps}}, nil
}

// told records every notice sent, per user and reference.
type told struct {
	mu   sync.Mutex
	got  []string // "user:ref"
	fail bool
}

func (t *told) notify(_ context.Context, users []int64, n Notice) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fail {
		return 0, errors.New("inbox down")
	}
	k := 0
	for _, u := range users {
		entry := fmt.Sprintf("%d:%s", u, n.Ref)
		dup := false
		for _, g := range t.got {
			if g == entry {
				dup = true
			}
		}
		if !dup { // the inbox's unique (user, ref) index
			t.got = append(t.got, entry)
			k++
		}
	}
	return k, nil
}

func (t *told) list() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]string(nil), t.got...)
}

type fixture struct {
	s     *Service
	db    *sql.DB
	lib   *fakeLibrary
	told  *told
	staff []int64 // issue ids staff were alerted about
	users map[string]int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{db: st.DB().(*sql.DB), lib: &fakeLibrary{items: map[string]Item{}}, told: &told{}, users: map[string]int64{}}
	for _, name := range []string{"alice", "bob", "carol"} {
		res, err := f.db.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, 'x', 'requester')`, name)
		if err != nil {
			t.Fatal(err)
		}
		f.users[name], _ = res.LastInsertId()
	}
	f.s = NewService(f.db, f.lib, nil)
	f.s.SetNotifier(f.told.notify)
	f.s.SetStaffAlert(func(_ context.Context, i Issue, _ map[string]any) { f.staff = append(f.staff, i.ID) })
	f.lib.put(MediaMovie, 603, 11, 0, 0, "The Matrix", "/m/The Matrix (1999)/The Matrix.mkv", 4000)
	f.lib.put(MediaMovie, 604, 12, 0, 0, "The Matrix Reloaded", "", 0) // no file yet
	f.lib.put(MediaSeries, 1399, 21, 1, 4, "Severance", "/tv/Severance/S01E04.mkv", 900)
	f.lib.put(MediaSeries, 1399, 21, 1, 5, "Severance", "/tv/Severance/S01E05.mkv", 950)
	return f
}

func (f *fixture) report(t *testing.T, who, media string, tmdb, season, episode int, category, note string) (Issue, bool) {
	t.Helper()
	i, joined, err := f.s.Create(context.Background(), Report{MediaType: media, TMDBID: tmdb, Season: season, Episode: episode,
		Category: category, Note: note, UserID: f.users[who], UserName: who})
	if err != nil {
		t.Fatalf("report by %s: %v", who, err)
	}
	return i, joined
}

func TestCreateValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	alice := f.users["alice"]
	cases := []struct {
		name string
		r    Report
		want error
		bad  bool // an InvalidError
	}{
		{"unknown item", Report{MediaType: MediaMovie, TMDBID: 999, Category: "audio"}, ErrNotInLibrary, false},
		{"item without a file", Report{MediaType: MediaMovie, TMDBID: 604, Category: "audio"}, ErrNoFile, false},
		{"bad category", Report{MediaType: MediaMovie, TMDBID: 603, Category: "smell"}, nil, true},
		{"note too long", Report{MediaType: MediaMovie, TMDBID: 603, Category: "audio", Note: strings.Repeat("é", NoteMax+1)}, nil, true},
		{"episode not in the show", Report{MediaType: MediaSeries, TMDBID: 1399, Season: 1, Episode: 9, Category: "audio"}, ErrNotInLibrary, false},
		{"series without an episode", Report{MediaType: MediaSeries, TMDBID: 1399, Season: 1, Category: "audio"}, nil, true},
		{"not a media type issues cover", Report{MediaType: "book", TMDBID: 603, Category: "audio"}, nil, true},
	}
	for _, c := range cases {
		c.r.UserID, c.r.UserName = alice, "alice"
		_, _, err := f.s.Create(ctx, c.r)
		var inv *InvalidError
		switch {
		case c.bad && !errors.As(err, &inv):
			t.Errorf("%s: err = %v, want an InvalidError", c.name, err)
		case !c.bad && !errors.Is(err, c.want):
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	// A note at the limit is fine, and a movie's season/episode are ignored.
	i, _, err := f.s.Create(ctx, Report{MediaType: MediaMovie, TMDBID: 603, Season: 3, Episode: 2, Category: "audio",
		Note: strings.Repeat("é", NoteMax), UserID: alice, UserName: "alice"})
	if err != nil || i.Season != 0 || i.Episode != 0 || i.Title != "The Matrix" || i.LibraryID != 11 {
		t.Fatalf("valid report: %+v, %v", i, err)
	}
	var n int
	_ = f.db.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	if n != 1 {
		t.Errorf("issues recorded = %d, want only the valid one", n)
	}
}

// The adult filter is always on: a library title it matches can't be reported, and the
// answer reads like a title that isn't there.
func TestCreateRefusesAdultTitles(t *testing.T) {
	f := newFixture(t)
	f.lib.put(MediaMovie, 777, 31, 0, 0, "Brazzers Night", "/m/x.mkv", 1)
	_, _, err := f.s.Create(context.Background(), Report{MediaType: MediaMovie, TMDBID: 777, Category: "video", UserID: f.users["alice"]})
	if !errors.Is(err, ErrNotInLibrary) {
		t.Fatalf("adult title: err = %v, want ErrNotInLibrary", err)
	}
	f.lib.put(MediaSeries, 778, 32, 1, 1, "Brazzers Night", "/tv/x.mkv", 1)
	if _, err := f.s.Episodes(context.Background(), 778); !errors.Is(err, ErrNotInLibrary) {
		t.Fatalf("adult show's episodes: err = %v, want ErrNotInLibrary", err)
	}
}

func TestDedupeJoinsReporters(t *testing.T) {
	f := newFixture(t)
	first, joined := f.report(t, "alice", MediaSeries, 1399, 1, 4, "audio", "no English audio")
	if joined {
		t.Fatal("first report joined something")
	}
	second, joined := f.report(t, "bob", MediaSeries, 1399, 1, 4, "audio", "sound is Spanish")
	if !joined || second.ID != first.ID {
		t.Fatalf("second report: joined=%v id=%d, want joined to %d", joined, second.ID, first.ID)
	}
	if second.ReporterCount != 2 || second.Note != "no English audio" {
		t.Errorf("joined issue: reporters=%d note=%q", second.ReporterCount, second.Note)
	}
	// The same person again: still two reporters, their newer note kept.
	again, joined := f.report(t, "bob", MediaSeries, 1399, 1, 4, "audio", "still Spanish")
	if !joined || again.ReporterCount != 2 {
		t.Errorf("repeat by bob: joined=%v reporters=%d", joined, again.ReporterCount)
	}
	// A different episode, or a different problem, is its own issue.
	other, joined := f.report(t, "bob", MediaSeries, 1399, 1, 5, "audio", "")
	if joined || other.ID == first.ID {
		t.Error("S01E05 joined the S01E04 issue")
	}
	sub, joined := f.report(t, "bob", MediaSeries, 1399, 1, 4, "subtitles", "")
	if joined || sub.ID == first.ID {
		t.Error("a subtitles report joined the sound issue")
	}
	// Staff heard about each new issue once, and not about the joins.
	if len(f.staff) != 3 {
		t.Errorf("staff alerts = %v, want 3 (one per new issue)", f.staff)
	}
	// Once closed, a new report opens a fresh issue.
	if _, err := f.s.Resolve(context.Background(), first.ID, "", 0, "admin"); err != nil {
		t.Fatal(err)
	}
	fresh, joined := f.report(t, "carol", MediaSeries, 1399, 1, 4, "audio", "")
	if joined || fresh.ID == first.ID {
		t.Error("a report after the issue was resolved joined the closed one")
	}
}

// Two people reporting the same problem at the same moment end up on one issue.
func TestConcurrentReportsShareOneIssue(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	ids := make([]int64, 3)
	for k, who := range []string{"alice", "bob", "carol"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			i, _, err := f.s.Create(context.Background(), Report{MediaType: MediaMovie, TMDBID: 603, Category: "video", UserID: f.users[who], UserName: who})
			if err != nil {
				t.Errorf("%s: %v", who, err)
			}
			ids[k] = i.ID
		}()
	}
	wg.Wait()
	if ids[0] != ids[1] || ids[1] != ids[2] {
		t.Fatalf("concurrent reports opened several issues: %v", ids)
	}
	i, err := f.s.Get(context.Background(), ids[0])
	if err != nil || i.ReporterCount != 3 {
		t.Fatalf("issue: reporters=%d err=%v", i.ReporterCount, err)
	}
}

func TestRequesterCannotListOthers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	shared, _ := f.report(t, "alice", MediaMovie, 603, 0, 0, "video", "alice's note")
	f.report(t, "bob", MediaMovie, 603, 0, 0, "video", "bob's note")
	f.report(t, "alice", MediaSeries, 1399, 1, 4, "audio", "only alice")
	f.report(t, "carol", MediaSeries, 1399, 1, 5, "audio", "only carol")

	// Bob sees only the issue he reported, without alice's name or note.
	list, total, counts, err := f.s.List(ctx, ListFilter{UserID: f.users["bob"]})
	if err != nil {
		t.Fatal(err)
	}
	ShapeForRequester(list, f.users["bob"])
	if total != 1 || len(list) != 1 || list[0].ID != shared.ID || counts.Open != 1 {
		t.Fatalf("bob's list: total=%d counts=%+v %+v", total, counts, list)
	}
	got := list[0]
	if got.Note != "" || got.ReportedByName != "" || got.ReportedBy != 0 || got.Reporters != nil || got.MyNote != "bob's note" {
		t.Errorf("bob's view leaks another reporter: %+v", got)
	}
	// Asking for someone else's issue by id finds nothing.
	list, total, _, _ = f.s.List(ctx, ListFilter{UserID: f.users["bob"], ID: list[0].ID + 2})
	if total != 0 || len(list) != 0 {
		t.Errorf("bob read carol's issue by id: %+v", list)
	}
	// Staff see everything.
	list, total, counts, _ = f.s.List(ctx, ListFilter{})
	if total != 3 || counts.Open != 3 || len(list[len(list)-1].Reporters) != 2 {
		t.Errorf("staff list: total=%d counts=%+v", total, counts)
	}
}

func TestResolveNotifiesEachReporterOnce(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	i, _ := f.report(t, "alice", MediaSeries, 1399, 1, 4, "audio", "")
	f.report(t, "bob", MediaSeries, 1399, 1, 4, "audio", "")
	done, err := f.s.Resolve(ctx, i.ID, "Swapped in a copy with English audio.", 1, "admin")
	if err != nil || done.Status != StatusResolved || done.ResolvedByName != "admin" {
		t.Fatalf("resolve: %+v, %v", done, err)
	}
	ref := fmt.Sprintf("series:1399:issue:%d:resolved", i.ID)
	want := []string{fmt.Sprintf("%d:%s", f.users["alice"], ref), fmt.Sprintf("%d:%s", f.users["bob"], ref)}
	if got := f.told.list(); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("told = %v, want %v", got, want)
	}
	// Closing again is refused and tells nobody; the retry sweep has nothing to resend.
	if _, err := f.s.Resolve(ctx, i.ID, "", 1, "admin"); !errors.Is(err, ErrClosed) {
		t.Errorf("second resolve: %v, want ErrClosed", err)
	}
	if _, err := f.s.Dismiss(ctx, i.ID, "", 1, "admin"); !errors.Is(err, ErrClosed) {
		t.Errorf("dismiss after resolve: %v, want ErrClosed", err)
	}
	if err := f.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.told.list(); len(got) != 2 {
		t.Errorf("after the sweep, told = %v, want still 2", got)
	}
	n := closedNotice(done)
	if n.Body != "The problem you reported with “Severance” S01E04 is fixed. Swapped in a copy with English audio." {
		t.Errorf("notice body = %q", n.Body)
	}
}

// A notice that couldn't be delivered is retried by the sweep until everyone has it.
func TestSweepRetriesUntold(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	i, _ := f.report(t, "alice", MediaMovie, 603, 0, 0, "video", "")
	f.told.fail = true
	if _, err := f.s.Dismiss(ctx, i.ID, "That's how the film looks.", 1, "admin"); err != nil {
		t.Fatal(err)
	}
	if len(f.told.list()) != 0 {
		t.Fatal("told someone while the inbox was down")
	}
	f.told.fail = false
	if err := f.s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if got := f.told.list(); len(got) != 1 || !strings.HasSuffix(got[0], fmt.Sprintf("movie:603:issue:%d:dismissed", i.ID)) {
		t.Fatalf("after the sweep, told = %v", got)
	}
	// Told now: the next sweep leaves it alone.
	var toldAt int64
	_ = f.db.QueryRow(`SELECT told_at FROM issues WHERE id = ?`, i.ID).Scan(&toldAt)
	if toldAt == 0 {
		t.Error("told_at not stamped after everyone heard")
	}
	// An issue closed too long ago isn't retried.
	f.s.now = func() time.Time { return time.Now().Add(8 * 24 * time.Hour) }
	if list, _ := untold(ctx, f.db, f.s.now().Add(-toldRetryFor).Unix()); len(list) != 0 {
		t.Errorf("stale untold: %+v", list)
	}
}

// N6: the "Problem reported" alert leaves the app, so an email account name goes out as
// the part before the @ only.
func TestStaffAlertReporterNameIsNoEmail(t *testing.T) {
	f := newFixture(t)
	var name any
	f.s.SetStaffAlert(func(_ context.Context, _ Issue, data map[string]any) { name = data["reported_by_name"] })
	if _, _, err := f.s.Create(context.Background(), Report{MediaType: MediaMovie, TMDBID: 603, Category: "audio",
		UserID: f.users["alice"], UserName: "alice.smith@example.com"}); err != nil {
		t.Fatal(err)
	}
	if name != "alice.smith" {
		t.Fatalf("reported_by_name = %v", name)
	}
}
