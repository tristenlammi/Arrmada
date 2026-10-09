package automation

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeQbit is a qBittorrent stand-in: torrents/info answers with the torrents set on it,
// and every add and delete is recorded, in order, so a test can see which came first.
type fakeQbit struct {
	mu       sync.Mutex
	torrents []map[string]any
	calls    []string // "add <url>" / "delete <hash>"
}

func (f *fakeQbit) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/info", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.torrents)
	})
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.calls = append(f.calls, "add "+r.FormValue("urls"))
		f.mu.Unlock()
		fmt.Fprint(w, "Ok.")
	})
	mux.HandleFunc("/api/v2/torrents/delete", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		f.calls = append(f.calls, "delete "+r.FormValue("hashes"))
		f.mu.Unlock()
		fmt.Fprint(w, "Ok.")
	})
	return mux
}

func (f *fakeQbit) callLog() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// stalledTorrent is a torrent sitting in the client with no peers and 10% done.
func (f *fakeQbit) stalledTorrent(hash, name, category string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.torrents = append(f.torrents, map[string]any{
		"hash": hash, "name": name, "state": "stalledDL", "progress": 0.1,
		"size": 8 << 30, "amount_left": 7 << 30, "category": category, "num_seeds": 0,
	})
}

// fakeTorznab answers every search with the same feed, or fails every request while down.
type fakeTorznab struct {
	mu       sync.Mutex
	releases []string // titles on offer
	down     bool
	searches int
}

func (f *fakeTorznab) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.searches++
		if f.down {
			http.Error(w, "bad gateway", http.StatusBadGateway)
			return
		}
		var b strings.Builder
		b.WriteString(`<?xml version="1.0" encoding="UTF-8"?><rss version="2.0" xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`)
		for _, title := range f.releases {
			fmt.Fprintf(&b, `<item><title>%s</title><link>%s</link><size>%d</size><torznab:attr name="seeders" value="25"/></item>`,
				html.EscapeString(title), html.EscapeString(magnetFor(title)), int64(4<<30))
		}
		b.WriteString(`</channel></rss>`)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, b.String())
	})
}

func (f *fakeTorznab) offer(titles ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releases = titles
}

func (f *fakeTorznab) searchCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.searches
}

// hashFor is a stable 40-hex info hash for a release title.
func hashFor(title string) string {
	sum := sha1.Sum([]byte(title))
	return hex.EncodeToString(sum[:])
}

func magnetFor(title string) string { return "magnet:?xt=urn:btih:" + hashFor(title) }

// stallHarness is a store-backed coordinator whose indexer and download client are the
// fakes above, reached over HTTP through the real services.
type stallHarness struct {
	c      *Coordinator
	ctx    context.Context
	qbit   *fakeQbit
	ix     *fakeTorznab
	events <-chan eventbus.Event
}

func newStallHarness(t *testing.T) *stallHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	qb, tz := &fakeQbit{}, &fakeTorznab{}
	qsrv := httptest.NewServer(qb.handler())
	t.Cleanup(qsrv.Close)
	tsrv := httptest.NewServer(tz.handler())
	t.Cleanup(tsrv.Close)

	ix := indexer.NewService(st.DB(), log, nil)
	if _, err := ix.Create(ctx, indexer.Indexer{Name: "Fake", Kind: indexer.KindTorznab, URL: tsrv.URL, Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: qsrv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	q := quality.NewService(st.DB())
	createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Any"})
	createDefault(t, q, quality.StoredProfile{MediaType: quality.MediaSeries, Name: "Any TV"})

	bus := eventbus.New(log)
	events, cancel := bus.Subscribe("download.stalled")
	t.Cleanup(cancel)
	c := &Coordinator{
		db: st.DB(), log: log, bus: bus, indexers: ix, downloads: dl, quality: q,
		movies:       movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		series:       series.NewService(st.DB(), nil, t.TempDir(), log),
		downloadsDir: t.TempDir(),
	}
	return &stallHarness{c: c, ctx: ctx, qbit: qb, ix: tz, events: events}
}

func (h *stallHarness) addMovie(t *testing.T, tmdb int, title string, year int) int64 {
	t.Helper()
	m, err := movies.NewRepo(h.c.db).Create(h.ctx, movies.Movie{TMDBID: tmdb, Title: title, Year: year, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	return m.ID
}

// addStalledGrab records a grab made seven hours ago with a one-hour stall window, for a
// torrent that's in the client but going nowhere.
func (h *stallHarness) addStalledGrab(t *testing.T, mediaType string, id int64, release, category string) int64 {
	t.Helper()
	res, err := h.c.db.Exec(`INSERT INTO grabs (movie_id, title, indexer, stall_minutes, media_type, info_hash, grabbed_at)
		VALUES (?, ?, 'Fake', 60, ?, ?, datetime('now', '-7 hours'))`, id, release, mediaType, hashFor(release))
	if err != nil {
		t.Fatal(err)
	}
	h.qbit.stalledTorrent(hashFor(release), release, category)
	gid, _ := res.LastInsertId()
	return gid
}

// tick runs one stall check after making every pending grab's last progress look older
// than its window — what the two-minute ticker sees once the window has really passed.
func (h *stallHarness) tick() {
	if _, err := h.c.db.Exec(`UPDATE grabs SET progress_at = ? WHERE progress_at > 0`, time.Now().Add(-2*time.Hour).UnixMilli()); err != nil {
		panic(err)
	}
	h.c.DetectStalled(h.ctx)
}

// observe runs the first stall check, which only records each grab's progress.
func (h *stallHarness) observe() { h.c.DetectStalled(h.ctx) }

func (h *stallHarness) status(t *testing.T, gid int64) string {
	t.Helper()
	return grabStatus(t, h.c, gid)
}

func (h *stallHarness) blocklistCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := h.c.db.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (h *stallHarness) movieEvents(t *testing.T, id int64, substr string) int {
	t.Helper()
	evs, err := h.c.movies.Events(h.ctx, id, 50)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range evs {
		if strings.Contains(e.Detail, substr) {
			n++
		}
	}
	return n
}

// stallEvents drains the download.stalled events published so far.
func (h *stallHarness) stallEvents() []map[string]any {
	var out []map[string]any
	for {
		select {
		case ev := <-h.events:
			out = append(out, ev.Data.(map[string]any))
		default:
			return out
		}
	}
}

// With an alternate on offer, the alternate is grabbed first, and only then is the stalled
// release blocklisted and removed — never the other way round.
func TestStallFailoverReplacesBeforeRemoving(t *testing.T) {
	h := newStallHarness(t)
	const stalled = "Rare.Film.1971.1080p.BluRay.x264-OLD"
	const alt = "Rare.Film.1971.1080p.WEB-DL.x264-NEW"
	mid := h.addMovie(t, 1, "Rare Film", 1971)
	gid := h.addStalledGrab(t, "movie", mid, stalled, "arrmada")
	h.ix.offer(stalled, alt)

	h.observe()
	h.tick()

	calls := h.qbit.callLog()
	want := []string{"add " + magnetFor(alt), "delete " + hashFor(stalled)}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("client calls = %q\nwant          %q (the replacement must be added before the stalled one goes)", calls, want)
	}
	if s := h.status(t, gid); s != "failed" {
		t.Errorf("stalled grab status = %q, want failed", s)
	}
	var reason string
	if err := h.c.db.QueryRow(`SELECT reason FROM blocklist WHERE norm_title = ?`, normTitle(stalled)).Scan(&reason); err != nil {
		t.Fatalf("the stalled release wasn't blocklisted: %v", err)
	}
	if !strings.Contains(reason, "replaced by "+alt) {
		t.Errorf("blocklist reason = %q", reason)
	}
	var newGrab int
	_ = h.c.db.QueryRow(`SELECT COUNT(*) FROM grabs WHERE title = ? AND status = 'grabbed'`, alt).Scan(&newGrab)
	if newGrab != 1 {
		t.Errorf("the replacement wasn't recorded as a grab")
	}
	if h.movieEvents(t, mid, "Stalled for 1h — replaced by "+alt) != 1 {
		t.Errorf("history should say what replaced the stalled release")
	}
	evs := h.stallEvents()
	if len(evs) != 1 || evs[0]["replaced"] != true || evs[0]["replacement"] != alt || evs[0]["kind"] != "movie" {
		t.Errorf("download.stalled events = %+v", evs)
	}
}

// With nothing else on offer the stalled torrent is the only copy there is: it stays in the
// client, nothing is blocklisted, and one "still waiting" line is written per window.
func TestStallFailoverKeepsTheOnlyCopy(t *testing.T) {
	h := newStallHarness(t)
	const stalled = "Rare.Film.1971.1080p.BluRay.x264-OLD"
	mid := h.addMovie(t, 1, "Rare Film", 1971)
	gid := h.addStalledGrab(t, "movie", mid, stalled, "arrmada")
	h.ix.offer(stalled) // only the stalled release itself, which is excluded

	h.observe()
	h.tick()
	searches := h.ix.searchCount()
	if searches == 0 {
		t.Fatal("the fail-over should have searched for an alternate")
	}
	// Two more checks inside the same window: one ordinary, one where the progress sample
	// has expired again. Neither may search again or write another line.
	h.observe()
	h.tick()

	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Fatalf("client calls = %q, want none — the only copy must stay", calls)
	}
	if n := h.blocklistCount(t); n != 0 {
		t.Errorf("blocklist rows = %d, want none", n)
	}
	if s := h.status(t, gid); s != "grabbed" {
		t.Errorf("grab status = %q, want it still grabbed", s)
	}
	if n := h.movieEvents(t, mid, "no other release found, still waiting on "+stalled); n != 1 {
		t.Errorf("still-waiting lines = %d, want exactly 1 per window", n)
	}
	if got := h.ix.searchCount(); got != searches {
		t.Errorf("searched %d more time(s) inside the window", got-searches)
	}

	// A window later it tries again, and says so again.
	if _, err := h.c.db.Exec(`UPDATE grabs SET still_waiting_at = ? WHERE id = ?`, time.Now().Add(-2*time.Hour).UnixMilli(), gid); err != nil {
		t.Fatal(err)
	}
	h.tick()
	if n := h.movieEvents(t, mid, "still waiting on "+stalled); n != 2 {
		t.Errorf("after a second window, still-waiting lines = %d, want 2", n)
	}
	if evs := h.stallEvents(); len(evs) != 2 || evs[0]["replaced"] != false {
		t.Errorf("download.stalled events = %+v, want two unreplaced", evs)
	}
}

// Every indexer down is not "no alternate exists" — but it's no licence to delete either.
func TestStallFailoverDuringOutageKeepsTheTorrent(t *testing.T) {
	h := newStallHarness(t)
	const stalled = "Rare.Film.1971.1080p.BluRay.x264-OLD"
	mid := h.addMovie(t, 1, "Rare Film", 1971)
	gid := h.addStalledGrab(t, "movie", mid, stalled, "arrmada")
	h.ix.down = true

	h.observe()
	h.tick()

	if calls := h.qbit.callLog(); len(calls) != 0 {
		t.Fatalf("client calls = %q, want none during an outage", calls)
	}
	if n := h.blocklistCount(t); n != 0 {
		t.Errorf("blocklist rows = %d, want none", n)
	}
	if s := h.status(t, gid); s != "grabbed" {
		t.Errorf("grab status = %q, want grabbed", s)
	}
	if h.movieEvents(t, mid, "indexers unavailable, still waiting on "+stalled) != 1 {
		t.Error("history should say the indexers were unavailable")
	}
}

// A torrent that vanished from a complete queue has no copy left to protect: it fails over
// the old way — blocklist, fail, search.
func TestStallFailoverVanishedTorrent(t *testing.T) {
	h := newStallHarness(t)
	const gone = "Rare.Film.1971.1080p.BluRay.x264-OLD"
	const alt = "Rare.Film.1971.1080p.WEB-DL.x264-NEW"
	mid := h.addMovie(t, 1, "Rare Film", 1971)
	gid := h.addStalledGrab(t, "movie", mid, gone, "arrmada")
	h.qbit.mu.Lock()
	h.qbit.torrents = nil // deleted by hand in the client
	h.qbit.mu.Unlock()
	h.ix.offer(gone, alt)

	h.observe() // a missing torrent is stalled on sight, no sample needed

	if s := h.status(t, gid); s != "failed" {
		t.Errorf("grab status = %q, want failed", s)
	}
	if n := h.blocklistCount(t); n != 1 {
		t.Errorf("blocklist rows = %d, want 1", n)
	}
	if calls := h.qbit.callLog(); len(calls) != 1 || calls[0] != "add "+magnetFor(alt) {
		t.Errorf("client calls = %q, want just the alternate added", calls)
	}
	if h.movieEvents(t, mid, gone+" disappeared from the download client") != 1 {
		t.Error("history should say the download disappeared")
	}
}

// Five stalled grabs: three are handled on the first check, the other two on the next.
func TestStallFailoverCapsPerCheck(t *testing.T) {
	h := newStallHarness(t)
	var gids []int64
	var offers []string
	for i, name := range []string{"Alpha", "Bravo", "Charlie", "Delta", "Echo"} {
		mid := h.addMovie(t, i+1, name+" Film", 2001)
		old := name + ".Film.2001.1080p.BluRay.x264-OLD"
		gids = append(gids, h.addStalledGrab(t, "movie", mid, old, "arrmada"))
		offers = append(offers, old, name+".Film.2001.1080p.WEB-DL.x264-NEW")
	}
	h.ix.offer(offers...)

	h.observe()
	h.tick()
	failed := func() int {
		n := 0
		for _, id := range gids {
			if h.status(t, id) == "failed" {
				n++
			}
		}
		return n
	}
	if n := failed(); n != maxStallFailoversPerCheck {
		t.Fatalf("first check failed over %d grabs, want %d", n, maxStallFailoversPerCheck)
	}
	h.tick()
	if n := failed(); n != len(gids) {
		t.Errorf("after the second check %d of %d are failed over", n, len(gids))
	}
}

// A stalled S03 pack is replaced with another S03 release — not with S04 episodes the show
// also wants, and not with the same pack re-listed.
func TestStallFailoverSeriesScopedToItsSeason(t *testing.T) {
	h := newStallHarness(t)
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 9, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	ep := func(s, e int) series.Episode {
		return series.Episode{SeasonNumber: s, EpisodeNumber: e, AirDate: "2021-01-01", Monitored: true}
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{
		{SeasonNumber: 3, Monitored: true, Episodes: []series.Episode{ep(3, 1), ep(3, 2)}},
		{SeasonNumber: 4, Monitored: true, Episodes: []series.Episode{ep(4, 1)}},
	}); err != nil {
		t.Fatal(err)
	}
	const stalled = "Show.S03.1080p.WEB-DL.x264-OLD"
	const alt = "Show.S03.1080p.WEB-DL.x264-NEW"
	const otherSeason = "Show.S04E01.1080p.WEB-DL.x264-CCC"
	gid := h.addStalledGrab(t, "series", sr.ID, stalled, seriesCategory)
	h.ix.offer(stalled, alt, otherSeason)

	h.observe()
	h.tick()

	calls := h.qbit.callLog()
	want := []string{"add " + magnetFor(alt), "delete " + hashFor(stalled)}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("client calls = %q\nwant          %q", calls, want)
	}
	if s := h.status(t, gid); s != "failed" {
		t.Errorf("grab status = %q, want failed", s)
	}
	evs, _ := h.c.series.Events(h.ctx, sr.ID, 20)
	found := false
	for _, e := range evs {
		if e.Event == "failed" && strings.Contains(e.Detail, "replaced by "+alt) {
			found = true
		}
	}
	if !found {
		t.Errorf("series history = %+v, want a 'failed … replaced by' line", evs)
	}
}

func TestStallSpan(t *testing.T) {
	for in, want := range map[int]string{45: "45 min", 60: "1h", 360: "6h", 90: "1h 30m"} {
		if got := stallSpan(in); got != want {
			t.Errorf("stallSpan(%d) = %q, want %q", in, got, want)
		}
	}
}
