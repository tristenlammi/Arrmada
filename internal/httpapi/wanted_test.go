package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// wantedServer is a route server with movies, series, books and the coordinator wired,
// and no download client (so the queue reads as known and empty).
func wantedServer(t *testing.T, fj *fakeJobs) *routeServer {
	t.Helper()
	return newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		mv := movies.NewService(db, nil, nil, t.TempDir(), "", nil, d.Log)
		dl := download.NewService(db, d.Log)
		d.Movies, d.Downloads, d.Quality = mv, dl, quality.NewService(db)
		d.Series = series.NewService(db, nil, t.TempDir(), d.Log)
		d.Books = books.NewService(db, nil, d.Log)
		d.Automation = automation.New(mv, nil, dl, nil, db, nil, d.Log, "")
		if fj != nil {
			d.Jobs = fj
		}
	})
}

func mustExecAll(t *testing.T, s *routeServer, stmts ...string) {
	t.Helper()
	for _, q := range stmts {
		if _, err := s.st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func getWanted(t *testing.T, s *routeServer, c *http.Cookie, query string) wantedLists {
	t.Helper()
	rec := s.do("GET", "/api/v1/wanted"+query, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /wanted%s: HTTP %d: %s", query, rec.Code, rec.Body)
	}
	var out wantedLists
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func rowFor(rows []wantedRow, kind string, id int64) (wantedRow, bool) {
	for _, r := range rows {
		if r.MediaType == kind && r.ID == id {
			return r, true
		}
	}
	return wantedRow{}, false
}

// ACQ-18: every Searching row says what is really happening to the title — searching on
// its ladder (with the last search, the empty run and the next try), waiting on a pack
// that covers it, or held for review — and wanted books appear beside films and shows.
func TestWantedRowsSayWhatIsHappening(t *testing.T) {
	s := wantedServer(t, nil)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	lastHourAgo := time.Now().UTC().Add(-time.Hour).Format("2006-01-02 15:04:05")
	mustExecAll(t, s,
		// 1: searched three times for nothing, last an hour ago; 2: held in Review;
		// 3: downloading (on the Downloads tab, not Wanted); 4: not out yet.
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, last_search_at, search_misses)
			VALUES (1, 1, 'Alpha', 2001, 1, 'announced', '`+lastHourAgo+`', 3)`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (2, 2, 'Bravo', 2002, 1, 'announced')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (3, 3, 'Charlie', 2003, 1, 'announced')`,
		`INSERT INTO grabs (movie_id, title, media_type, info_hash, status) VALUES (2, 'Bravo.2002.1080p', 'movie', 'aaaa', 'held')`,
		`INSERT INTO import_reviews (hash, name, media_type, expected_id, reason, reason_code) VALUES ('aaaa', 'Bravo.2002.1080p', 'movie', 2, 'Looks like another film', 'mismatch')`,
		`INSERT INTO grabs (movie_id, title, media_type, info_hash, status) VALUES (3, 'Charlie.2003.1080p', 'movie', 'bbbb', 'grabbed')`,
		`INSERT INTO search_attempts (media_type, media_id, started_at, returned, outcome, top_reason, reasons_json)
			VALUES ('movie', 1, 1000, 30, 'none_suitable', 'bitrate_ceiling', '{"bitrate_ceiling":30}'),
			       ('movie', 1, 2000, 34, 'none_suitable', 'bitrate_ceiling', '{"bitrate_ceiling":34}')`,
		// Series 10: an ended show whose complete pack is still downloading (stalled);
		// series 11: S01 downloading, S02 still wanted.
		`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (10, 10, 'Whole Show', 1), (11, 11, 'Half Show', 1)`,
		`INSERT INTO episodes (series_id, season_number, episode_number, air_date, monitored, has_file) VALUES
			(10, 1, 1, '2020-01-01', 1, 0), (10, 2, 1, '2021-01-01', 1, 0),
			(11, 1, 1, '2020-01-01', 1, 0), (11, 2, 1, '2021-01-01', 1, 0)`,
		`INSERT INTO grabs (movie_id, title, media_type, info_hash, status, acq_scope, phase)
			VALUES (10, 'Whole.Show.Complete.Series.1080p', 'series', 'cccc', 'grabbed', 'complete', 'stalled'),
			       (11, 'Half.Show.S01.1080p', 'series', 'dddd', 'grabbed', 'S01', 'downloading')`,
		// A monitored book with no file: its profile wants an ebook.
		`INSERT INTO books (id, ol_key, title, author, monitored) VALUES (20, 'OL20W', 'Red Rising', 'Pierce Brown', 1)`,
	)
	// Movie 4 waits for its release (no date known, and not marked released yet).
	mustExecAll(t, s, `INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (4, 4, 'Delta', 2099, 1, 'released')`)

	w := getWanted(t, s, mgr, "")
	if !w.QueueKnown {
		t.Fatal("no download client: the queue is known (and empty)")
	}

	alpha, ok := rowFor(w.Searching, "movie", 1)
	if !ok {
		t.Fatalf("Alpha missing from Searching: %+v", w.Searching)
	}
	if alpha.State != wantedSearching || alpha.SearchMisses != 3 || alpha.LastSearchAt == "" {
		t.Errorf("Alpha = %+v, want searching with 3 misses and a last search", alpha)
	}
	// 3 misses back off 2 h; searched an hour ago, so the next try is about an hour out.
	if next, err := time.Parse(time.RFC3339, alpha.NextSearchAt); err != nil || alpha.Due ||
		next.Sub(time.Now()) < 50*time.Minute || next.Sub(time.Now()) > 70*time.Minute {
		t.Errorf("Alpha next search %q (due %v), want about an hour from now", alpha.NextSearchAt, alpha.Due)
	}
	if alpha.LastSearch == nil || alpha.LastSearch.EmptyTries != 2 || alpha.LastSearch.MainReason != "bitrate_ceiling" {
		t.Errorf("Alpha last search = %+v, want 2 empty tries, mostly over the bitrate ceiling", alpha.LastSearch)
	}

	bravo, ok := rowFor(w.Searching, "movie", 2)
	if !ok || bravo.State != wantedHeld || bravo.ReviewID == 0 || bravo.NextSearchAt != "" {
		t.Errorf("held movie = %+v (listed %v), want held_for_review with its review and no next search", bravo, ok)
	}
	if _, ok := rowFor(w.Searching, "movie", 3); ok {
		t.Error("a movie downloading is listed as wanted")
	}
	if _, ok := rowFor(w.Searching, "movie", 4); ok {
		t.Error("an unreleased movie is listed as searching")
	}
	if d, ok := rowFor(w.Upcoming, "movie", 4); !ok || d.State != wantedNotReleased {
		t.Errorf("unreleased movie = %+v (listed %v), want an Upcoming row", d, ok)
	}

	whole, ok := rowFor(w.Searching, "series", 10)
	if !ok || whole.State != wantedWaiting || whole.WaitingOn != "Whole.Show.Complete.Series.1080p" || !whole.Stalled || whole.NextSearchAt != "" {
		t.Errorf("show with its whole run downloading = %+v, want waiting_download on the pack, stalled", whole)
	}
	half, ok := rowFor(w.Searching, "series", 11)
	if !ok || half.State != wantedSearching || half.WaitingNote != "S01 downloading" || half.EpisodeCount != 2 {
		t.Errorf("show with one season downloading = %+v, want searching with 'S01 downloading'", half)
	}

	book, ok := rowFor(w.Searching, "book", 20)
	if !ok || book.State != wantedSearching || !book.Due || book.Byline != "Pierce Brown" || len(book.Missing) != 1 || book.Missing[0] != "Ebook" {
		t.Errorf("wanted book = %+v (listed %v), want a due searching row missing its ebook", book, ok)
	}

	// ?kind narrows the list (what the Movies Wanted view asks for).
	only := getWanted(t, s, mgr, "?kind=movie")
	for _, r := range append(only.Searching, only.Upcoming...) {
		if r.MediaType != "movie" {
			t.Errorf("?kind=movie listed a %s", r.MediaType)
		}
	}
	if rec := s.do("GET", "/api/v1/wanted?kind=film", mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("bad kind: HTTP %d, want 400", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/wanted", kid); rec.Code != http.StatusForbidden {
		t.Errorf("a requester read the Wanted view: HTTP %d", rec.Code)
	}
}

// With the download client down, a wanted title may already be downloading and the
// sweeps are paused: rows say "unknown", with no next search, never "nothing found".
func TestWantedClientDownIsUnknown(t *testing.T) {
	s := wantedServer(t, nil)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	mustExecAll(t, s, `INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (1, 1, 'Alpha', 2001, 1, 'announced')`)
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	if _, err := s.deps.Downloads.Create(context.Background(), download.Client{Name: "qBittorrent", Kind: download.KindQbittorrent, URL: deadURL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.deps.Downloads.InvalidateSnapshot()
	w := getWanted(t, s, mgr, "")
	if w.QueueKnown {
		t.Fatal("a dead client reads as a known queue")
	}
	if r, ok := rowFor(w.Searching, "movie", 1); !ok || r.State != wantedUnknown || r.NextSearchAt != "" || r.Due {
		t.Errorf("row with the client down = %+v, want unknown with no next search", r)
	}
}

// A torrent added by hand, named for a wanted film, holds the movie sweep off it — so the
// Wanted view says it's waiting on that torrent, not searching.
func TestWantedMovieWaitsOnAHandAddedTorrent(t *testing.T) {
	s := wantedServer(t, nil)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	mustExecAll(t, s, `INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (1, 1, 'Alpha', 2001, 1, 'announced'),
		(2, 2, 'Bravo', 2002, 1, 'announced')`)
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			_, _ = w.Write([]byte("Ok."))
		case "/api/v2/torrents/info":
			_, _ = w.Write([]byte(`[{"hash":"0000000000000000000000000000000000000001","name":"Alpha.2001.1080p.BluRay.x264-HAND","state":"downloading","progress":0.2,"size":100,"amount_left":80}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	if _, err := s.deps.Downloads.Create(context.Background(), download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: up.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s.deps.Downloads.InvalidateSnapshot()
	w := getWanted(t, s, mgr, "?kind=movie")
	if r, ok := rowFor(w.Searching, "movie", 1); !ok || r.State != wantedWaiting || r.WaitingOn != "Alpha.2001.1080p.BluRay.x264-HAND" || r.NextSearchAt != "" {
		t.Errorf("movie with a hand-added torrent = %+v, want waiting on it", r)
	}
	if r, ok := rowFor(w.Searching, "movie", 2); !ok || r.State != wantedSearching {
		t.Errorf("the other movie = %+v, want plain searching", r)
	}
}

// Search now clears the title's backoff and starts the same search job its own page's
// Search button does, for each kind; an unknown title is 404 and an unknown kind 400.
func TestWantedSearchNowResetsAndDispatches(t *testing.T) {
	fj := newFakeJobs(false)
	s := wantedServer(t, fj)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	mustExecAll(t, s,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, last_search_at, search_misses) VALUES (1, 1, 'Alpha', 2001, 1, 'announced', '2026-01-01 00:00:00', 6)`,
		`INSERT INTO series (id, tmdb_id, title, monitored, last_search_at, search_misses) VALUES (2, 2, 'Show', 1, '2026-01-01 00:00:00', 5)`,
		`INSERT INTO books (id, ol_key, title, monitored, last_search_at, search_misses) VALUES (3, 'OL3W', 'Book', 1, '2026-01-01 00:00:00', 12)`,
	)
	cases := []struct {
		path, table, kind, target string
		id                        int64
	}{
		{"/api/v1/wanted/movie/1/search", "movies", "movie.search", "movie:1", 1},
		{"/api/v1/wanted/series/2/search", "series", "series.search", "series:2", 2},
		{"/api/v1/wanted/book/3/search", "books", "book.search", "book:3", 3},
	}
	for _, c := range cases {
		rec := s.do("POST", c.path, mgr)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: HTTP %d: %s", c.path, rec.Code, rec.Body)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if id, _ := body["job_id"].(float64); id <= 0 {
			t.Errorf("%s: body %v, want a job to follow", c.path, body)
		}
		var misses int
		if err := s.st.DB().QueryRow(`SELECT search_misses FROM `+c.table+` WHERE id = ?`, c.id).Scan(&misses); err != nil {
			t.Fatal(err)
		}
		if misses != 0 {
			t.Errorf("%s: search_misses = %d after Search now, want 0", c.path, misses)
		}
		last := fj.submitted[len(fj.submitted)-1]
		if last.Kind != c.kind || last.Target != c.target {
			t.Errorf("%s started %s %s, want %s %s", c.path, last.Kind, last.Target, c.kind, c.target)
		}
	}
	// The movie page's own Search is the same job: a second click finds it running.
	if rec := s.do("POST", "/api/v1/movies/1/search", mgr); rec.Code != http.StatusAccepted || !jsonBool(rec.Body.Bytes(), "existing") {
		t.Errorf("the movie page's Search after Wanted's: HTTP %d %s, want the same job back", rec.Code, rec.Body)
	}

	for path, want := range map[string]int{
		"/api/v1/wanted/movie/99/search": http.StatusNotFound,
		"/api/v1/wanted/film/1/search":   http.StatusBadRequest,
		"/api/v1/wanted/music/1/search":  http.StatusNotFound, // Music is off by default
	} {
		if rec := s.do("POST", path, mgr); rec.Code != want {
			t.Errorf("%s: HTTP %d, want %d", path, rec.Code, want)
		}
	}
}

// Albums join the Wanted view only while Music is on: a released, incomplete album is
// searched (and Search now starts the album search), one not out yet is Upcoming.
func TestWantedAlbums(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		mv := movies.NewService(db, nil, nil, t.TempDir(), "", nil, d.Log)
		dl := download.NewService(db, d.Log)
		d.Movies, d.Downloads, d.Quality = mv, dl, quality.NewService(db)
		d.Music = music.NewService(db, nil, d.Log)
		d.Automation = automation.New(mv, nil, dl, nil, db, nil, d.Log, "")
		d.Jobs = fj
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	mustExecAll(t, s,
		`INSERT INTO artists (id, mbid, name, monitored) VALUES (1, 'a1', 'Radiohead', 1)`,
		`INSERT INTO albums (id, artist_id, mbid, title, year, monitored, search_misses, last_search_at) VALUES (1, 1, 'r1', 'Kid A', 2000, 1, 7, '2026-10-01 00:00:00')`,
		`INSERT INTO albums (id, artist_id, mbid, title, year, release_date, monitored) VALUES (2, 1, 'r2', 'Next One', 2099, '2099-05-01', 1)`,
	)
	if w := getWanted(t, s, mgr, ""); len(w.Searching)+len(w.Upcoming) != 0 {
		t.Fatalf("Music is off, but albums are listed: %+v", w)
	}
	if err := s.deps.Settings.SetBool(context.Background(), settings.KeyModuleMusic, true); err != nil {
		t.Fatal(err)
	}
	w := getWanted(t, s, mgr, "")
	kid, ok := rowFor(w.Searching, "music", 1)
	if !ok || kid.State != wantedSlowed || kid.Byline != "Radiohead" || kid.ArtistID != 1 {
		t.Errorf("album after 7 empty searches = %+v (listed %v), want searching weekly", kid, ok)
	}
	if next, ok := rowFor(w.Upcoming, "music", 2); !ok || next.AvailableAt != "2099-05-01" {
		t.Errorf("unreleased album = %+v (listed %v), want an Upcoming row with its date", next, ok)
	}
	if rec := s.do("POST", "/api/v1/wanted/music/1/search", mgr); rec.Code != http.StatusAccepted {
		t.Fatalf("album Search now: HTTP %d: %s", rec.Code, rec.Body)
	}
	if last := fj.submitted[len(fj.submitted)-1]; last.Kind != "album.search" || last.Target != "album:1" {
		t.Errorf("album Search now started %s %s", last.Kind, last.Target)
	}
}

func jsonBool(b []byte, key string) bool {
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	v, _ := m[key].(bool)
	return v
}
