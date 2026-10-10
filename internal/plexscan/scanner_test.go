package plexscan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

type fakeSettings struct {
	mu sync.Mutex
	m  map[string]string
}

func (f *fakeSettings) Get(_ context.Context, key, def string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if v, ok := f.m[key]; ok {
		return v
	}
	return def
}
func (f *fakeSettings) Set(_ context.Context, key, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key] = value
	return nil
}
func (f *fakeSettings) GetBool(ctx context.Context, key string, def bool) bool {
	v := f.Get(ctx, key, "")
	if v == "" {
		return def
	}
	b, _ := strconv.ParseBool(v)
	return b
}
func (f *fakeSettings) SetBool(ctx context.Context, key string, v bool) error {
	return f.Set(ctx, key, strconv.FormatBool(v))
}

type refresh struct{ section, path string }

type fakePlex struct {
	mu        sync.Mutex
	libs      []plex.Library
	refreshes []refresh
	libCalls  int
	fail      error
}

func (f *fakePlex) Libraries(context.Context) ([]plex.Library, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.libCalls++
	return f.libs, nil
}
func (f *fakePlex) RefreshPath(_ context.Context, key, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return f.fail
	}
	f.refreshes = append(f.refreshes, refresh{key, dir})
	return nil
}
func (f *fakePlex) RefreshSection(_ context.Context, key string) error {
	return f.RefreshPath(context.Background(), key, "")
}
func (f *fakePlex) got() []refresh {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]refresh(nil), f.refreshes...)
}

type harness struct {
	s       *Scanner
	plex    *fakePlex
	set     *fakeSettings
	clock   time.Time
	clients int // how many times the scanner built a client
	conf    bool
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		plex: &fakePlex{libs: []plex.Library{
			{Key: "1", Title: "Movies", Type: "movie", Locations: []string{"/data/media/movies"}},
			{Key: "2", Title: "TV", Type: "show", Locations: []string{"/data/media/tv"}},
		}},
		set:   &fakeSettings{m: map[string]string{}},
		clock: time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC),
		conf:  true,
	}
	h.s = New(Options{
		Client:     func(context.Context) Client { h.clients++; return h.plex },
		Configured: func(context.Context) bool { return h.conf },
		Settings:   h.set,
		Roots: func(context.Context) map[string]string {
			return map[string]string{KindMovie: "/movies", KindShow: "/tv"}
		},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	h.s.now = func() time.Time { return h.clock }
	h.s.exists = func(string) bool { return true } // the folders in these tests are made up
	return h
}

func (h *harness) advance(d time.Duration) {
	h.clock = h.clock.Add(d)
	h.s.runDue(context.Background())
}

func TestScannerDebounce(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		h.s.Request(KindMovie, "/movies/Heat (1995)")
		h.advance(time.Second)
	}
	if got := h.plex.got(); len(got) != 0 {
		t.Fatalf("scanned while changes were still arriving: %v", got)
	}
	h.advance(15 * time.Second)
	got := h.plex.got()
	if len(got) != 1 || got[0] != (refresh{"1", "/data/media/movies/Heat (1995)"}) {
		t.Fatalf("refreshes = %v, want one of the Plex-side folder", got)
	}
	if st := h.s.LastScan(); st.Path != "/data/media/movies/Heat (1995)" || st.Section != "Movies" || st.How != HowGuessed || st.Error != "" {
		t.Errorf("status = %+v", st)
	}
}

// A steady trickle can't postpone the scan forever: a minute after the first change it
// goes out regardless.
func TestScannerMaxWait(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 70; i++ {
		h.s.Request(KindShow, "/tv/Show/Season 01")
		h.advance(5 * time.Second)
		if len(h.plex.got()) > 0 {
			if i*5 < 55 {
				t.Fatalf("scanned after %ds, before the one-minute cap", i*5)
			}
			return
		}
	}
	t.Fatal("never scanned under a steady trickle of changes")
}

func TestScannerAncestorCoalesce(t *testing.T) {
	h := newHarness(t)
	h.s.Request(KindShow, "/tv/Show/Season 01")
	h.s.Request(KindShow, "/tv/Show/Season 02")
	h.s.Request(KindShow, "/tv/Show")              // covers both seasons
	h.s.Request(KindShow, "/tv/Show/Season 03")    // covered by the show
	h.s.Request(KindMovie, "/movies/Show")         // another kind stays separate
	h.s.Request(KindShow, "/tv/Show Two/Season 1") // a sibling with a common prefix isn't covered
	if n := h.s.Pending(); n != 3 {
		t.Fatalf("pending = %d, want 3", n)
	}
	h.advance(16 * time.Second)
	got := map[refresh]int{}
	for _, r := range h.plex.got() {
		got[r]++
	}
	want := map[refresh]int{
		{"2", "/data/media/tv/Show"}:              1,
		{"2", "/data/media/tv/Show Two/Season 1"}: 1,
		{"1", "/data/media/movies/Show"}:          1,
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("refreshes = %v, want %v", got, want)
	}
}

func TestScannerRateLimitPerPath(t *testing.T) {
	h := newHarness(t)
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(16 * time.Second)
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(16 * time.Second) // 32s after the first scan: held back
	if n := len(h.plex.got()); n != 1 {
		t.Fatalf("refreshes = %d within the minute, want 1", n)
	}
	h.advance(45 * time.Second) // a minute after the first scan: the held change goes out
	if n := len(h.plex.got()); n != 2 {
		t.Fatalf("refreshes = %d, want the held one sent after the minute", n)
	}
}

func TestScannerNoopWhenUnconfigured(t *testing.T) {
	h := newHarness(t)
	h.conf = false
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(time.Minute)
	if h.s.Pending() != 0 || h.clients != 0 || h.plex.libCalls != 0 {
		t.Fatalf("unconfigured: pending %d, clients built %d, library reads %d", h.s.Pending(), h.clients, h.plex.libCalls)
	}

	h.conf = true
	if err := h.s.Save(context.Background(), false, nil); err != nil {
		t.Fatal(err)
	}
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(time.Minute)
	if h.s.Pending() != 0 || h.clients != 0 {
		t.Fatalf("switched off: pending %d, clients built %d", h.s.Pending(), h.clients)
	}
	// The view still answers without touching Plex's scan endpoints.
	if v := h.s.View(context.Background()); v.Enabled || !v.Configured {
		t.Errorf("view = %+v", v)
	}
}

// Plex being down costs a few retries and a status line, never an error to the caller.
func TestScannerRetriesThenGivesUp(t *testing.T) {
	h := newHarness(t)
	h.plex.fail = errors.New("connection refused")
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(16 * time.Second)
	if st := h.s.LastScan(); st.Error == "" {
		t.Fatalf("status after a failure = %+v", st)
	}
	if h.s.Pending() != 1 {
		t.Fatalf("a failed scan should wait for a retry")
	}
	for i := 0; i < 5; i++ {
		h.advance(retryAfter)
	}
	if h.s.Pending() != 0 {
		t.Fatalf("still retrying after %d attempts", maxAttempts)
	}
	h.plex.fail = nil
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(16 * time.Second)
	if n := len(h.plex.got()); n != 1 {
		t.Fatalf("refreshes after recovery = %d", n)
	}
	if st := h.s.LastScan(); st.Error != "" {
		t.Errorf("a good scan should clear the error: %+v", st)
	}
}

func TestScannerMassChangeFoldsIntoRoot(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < maxPendingPerKind+5; i++ {
		h.s.Request(KindMovie, fmt.Sprintf("/movies/Film %d (2000)", i))
	}
	if n := h.s.Pending(); n > maxPendingPerKind {
		t.Fatalf("pending = %d, want the burst folded", n)
	}
	h.advance(time.Minute)
	sawRoot := false
	for _, r := range h.plex.got() {
		if r == (refresh{"1", "/data/media/movies"}) {
			sawRoot = true
		}
	}
	if !sawRoot {
		t.Fatalf("no library-folder scan among %d refreshes", len(h.plex.got()))
	}
	if n := len(h.plex.got()); n > 10 {
		t.Fatalf("%d refreshes for one mass change", n)
	}
}

func TestScannerListenersAndSave(t *testing.T) {
	h := newHarness(t)
	var heard []string
	h.s.OnRefreshed(func(kind, key, p string) { heard = append(heard, kind+"|"+key+"|"+p) })
	if err := h.s.Save(context.Background(), true, []PathMap{{From: "/movies", To: "/srv/plex/films"}}); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Save(context.Background(), true, []PathMap{{From: "movies", To: "/x"}}); err == nil {
		t.Fatal("a relative path should be refused")
	}
	h.plex.libs = append(h.plex.libs, plex.Library{Key: "9", Title: "Films", Type: "movie", Locations: []string{"/srv/plex/films"}})
	h.s.dropSections()
	h.s.Request(KindMovie, "/movies/Heat (1995)")
	h.advance(16 * time.Second)
	if len(heard) != 1 || heard[0] != "movie|9|/srv/plex/films/Heat (1995)" {
		t.Fatalf("listeners heard %v", heard)
	}
	rv, err := h.s.ScanNow(context.Background(), KindMovie, false)
	if err != nil || rv.How != HowMapped || rv.PlexPath != "/srv/plex/films" {
		t.Fatalf("ScanNow = %+v, %v", rv, err)
	}
}

// A folder that's gone (a deleted movie, a renamed show's old folder) is scanned through
// its nearest parent that still exists, never above the library folder: Plex skips a
// folder that isn't there and would never notice what left it.
func TestScannerGoneFolderScansNearestParent(t *testing.T) {
	h := newHarness(t)
	h.s.exists = func(dir string) bool {
		d := filepath.ToSlash(dir)
		return d == "/tv/Show" || d == "/tv" || d == "/movies"
	}
	h.s.Request(KindShow, "/tv/Show/Season 09")
	h.s.Request(KindMovie, "/movies/Gone (1999)")
	h.s.Request(KindMovie, "/elsewhere/x") // outside the library folder: left as it is
	h.advance(16 * time.Second)
	got := map[refresh]bool{}
	for _, r := range h.plex.got() {
		got[r] = true
	}
	// /elsewhere/x can't be matched to Plex at all, so it scans the whole Movies section.
	for _, want := range []refresh{{"2", "/data/media/tv/Show"}, {"1", "/data/media/movies"}, {"1", ""}} {
		if !got[want] {
			t.Errorf("missing scan %v in %v", want, h.plex.got())
		}
	}
}
