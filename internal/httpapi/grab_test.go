package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// trackerKey is the indexer's apikey. A Prowlarr- or Jackett-synced indexer embeds it in
// every enclosure and GUID it returns, which is exactly what must never reach a browser.
const trackerKey = "SECRETKEY0123"

// fakeTracker is a Torznab indexer whose links carry its apikey, and which records
// every download it serves.
type fakeTracker struct {
	mu      sync.Mutex
	fetched []string // request URIs of /dl/ fetches
	srv     *httptest.Server
}

func newFakeTracker(t *testing.T) *fakeTracker {
	ft := &fakeTracker{}
	ft.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/dl/") {
			ft.mu.Lock()
			ft.fetched = append(ft.fetched, r.URL.RequestURI())
			ft.mu.Unlock()
			fmt.Fprint(w, "d4:infod4:name5:alpha12:piece lengthi16384e6:pieces0:6:lengthi1eee")
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		item := func(n int, title string) string {
			link := fmt.Sprintf("%s/dl/%d?apikey=%s&amp;file=x", ft.srv.URL, n, trackerKey)
			return fmt.Sprintf(`<item><title>%s</title><guid>%s</guid><comments>%s/details/%d?apikey=%s</comments>
				<enclosure url="%s" type="application/x-bittorrent"/><size>4000000000</size>
				<torznab:attr name="seeders" value="50"/></item>`, title, link, ft.srv.URL, n, trackerKey, link)
		}
		fmt.Fprint(w, `<rss xmlns:torznab="http://torznab.com/schemas/2015/feed"><channel>`+
			item(1, "Alpha.2001.1080p.BluRay.x264-GRP")+
			item(2, "Show.S01E01.1080p.WEB-DL.x264-GRP")+
			item(3, "Timothy Zahn - Thrawn [EPUB]")+
			`</channel></rss>`)
	}))
	t.Cleanup(ft.srv.Close)
	return ft
}

func (ft *fakeTracker) fetches() []string {
	ft.mu.Lock()
	defer ft.mu.Unlock()
	return append([]string(nil), ft.fetched...)
}

// grabServer is the real router over one movie (1, "Alpha"), one show (1, "Show") and
// one book (1, "Thrawn"), a fake Torznab indexer and a fake qBittorrent.
func grabServer(t *testing.T) (*routeServer, *fakeTracker, *fakeQbit) {
	t.Helper()
	ft := newFakeTracker(t)
	q := &fakeQbit{}
	qsrv := q.server(t)
	s := newRouteServer(t, func(d *Deps) {
		ctx := context.Background()
		db := d.Store.DB()
		ix := indexer.NewService(db, d.Log, "")
		if _, err := ix.Create(ctx, indexer.Indexer{Name: "Tracker", Kind: indexer.KindTorznab, URL: ft.srv.URL, APIKey: trackerKey, Priority: 10, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		dl := download.NewService(db, d.Log)
		if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: qsrv.URL, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		mv := movies.NewService(db, nil, nil, t.TempDir(), "", nil, d.Log)
		sv := series.NewService(db, nil, t.TempDir(), d.Log)
		bk := books.NewService(db, nil, d.Log)
		co := automation.New(mv, ix, dl, quality.NewService(db), db, eventbus.New(d.Log), d.Log, t.TempDir())
		co.SetSeries(sv, nil)
		co.SetBooks(bk)
		d.Indexers, d.Downloads, d.Movies, d.Series, d.Books, d.Automation = ix, dl, mv, sv, bk, co

		if _, err := db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (1, 1, 'Alpha', 2001, 1, 'announced'), (2, 2, 'Bravo', 2001, 1, 'announced')`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (1, 9, 'Show', 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO seasons (series_id, season_number) VALUES (1, 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO episodes (series_id, season_number, episode_number) VALUES (1, 1, 1)`); err != nil {
			t.Fatal(err)
		}
		if _, err := books.NewRepo(db).Create(ctx, books.Book{OLKey: "OL1W", Title: "Thrawn", Author: "Timothy Zahn"}); err != nil {
			t.Fatal(err)
		}
	})
	return s, ft, q
}

// releasesOf decodes a releases response and checks every release carries a token.
func releasesOf(t *testing.T, rec *httptest.ResponseRecorder, wantTokens bool) []automation.RankedRelease {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Releases []automation.RankedRelease `json:"releases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Releases) == 0 {
		t.Fatalf("no releases — the scan would prove nothing: %s", rec.Body)
	}
	for _, r := range list.Releases {
		if hasToken := r.Token != ""; hasToken != wantTokens {
			t.Errorf("release %q token = %q, want a token: %v", r.Title, r.Token, wantTokens)
		}
	}
	return list.Releases
}

// noSecrets fails if body carries the indexer's apikey, a download link or its field.
func noSecrets(t *testing.T, what string, body string) {
	t.Helper()
	for _, bad := range []string{trackerKey, "apikey", "/dl/", "download_url"} {
		if strings.Contains(body, bad) {
			t.Errorf("%s: response contains %q: %s", what, bad, body)
		}
	}
}

// No interactive-search or quality-test response carries the indexer's apikey or a
// download link; the search results carry tokens, the quality test (which never grabs)
// carries none.
func TestReleaseResponsesCarryNoSecrets(t *testing.T) {
	s, ft, _ := grabServer(t)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	for _, p := range []string{"/api/v1/movies/1/releases", "/api/v1/series/1/releases", "/api/v1/series/1/releases?season=1&episode=1", "/api/v1/books/1/releases"} {
		rec := s.do("GET", p, mgr)
		releasesOf(t, rec, true)
		noSecrets(t, p, rec.Body.String())
	}
	for _, body := range []string{`{"profile":{},"movie_id":1}`, `{"profile":{},"series_id":1,"season":1}`} {
		rec := s.doJSON("POST", "/api/v1/quality/test", mgr, body)
		releasesOf(t, rec, false)
		noSecrets(t, "quality test "+body, rec.Body.String())
	}
	if f := ft.fetches(); len(f) != 0 {
		t.Fatalf("searching fetched downloads: %v", f)
	}
}

func tokenFor(t *testing.T, rels []automation.RankedRelease, title string) string {
	t.Helper()
	for _, r := range rels {
		if r.Title == title {
			return r.Token
		}
	}
	t.Fatalf("no release %q in %+v", title, rels)
	return ""
}

// Grabs take a token, resolve it on the server and fetch the link stored under it; a
// token for another title, kind or user is refused, an unknown one is 410, and a body
// carrying a download link is an invalid body — the server fetches nothing.
func TestGrabByToken(t *testing.T) {
	s, ft, q := grabServer(t)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, other := s.user(t, "mgr2@example.com", auth.RoleManager)

	movieTok := tokenFor(t, releasesOf(t, s.do("GET", "/api/v1/movies/1/releases", mgr), true), "Alpha.2001.1080p.BluRay.x264-GRP")
	seriesTok := tokenFor(t, releasesOf(t, s.do("GET", "/api/v1/series/1/releases?season=1", mgr), true), "Show.S01E01.1080p.WEB-DL.x264-GRP")
	bookTok := tokenFor(t, releasesOf(t, s.do("GET", "/api/v1/books/1/releases", mgr), true), "Timothy Zahn - Thrawn [EPUB]")

	refused := []struct {
		name, path, body string
		c                *http.Cookie
		want             int
	}{
		{"another movie", "/api/v1/grab", `{"token":"` + movieTok + `","movie_id":2}`, mgr, http.StatusBadRequest},
		{"another user", "/api/v1/grab", `{"token":"` + movieTok + `","movie_id":1}`, other, http.StatusBadRequest},
		{"no token", "/api/v1/grab", `{"movie_id":1}`, mgr, http.StatusBadRequest},
		{"unknown token", "/api/v1/grab", `{"token":"bm90LWEtdG9rZW4","movie_id":1}`, mgr, http.StatusGone},
		{"a movie token on a show", "/api/v1/series/1/grab", `{"token":"` + movieTok + `"}`, mgr, http.StatusBadRequest},
		{"a show token on a book", "/api/v1/books/1/grab", `{"token":"` + seriesTok + `"}`, mgr, http.StatusBadRequest},
		{"a movie token on another movie's blocklist", "/api/v1/movies/2/blocklist", `{"token":"` + movieTok + `"}`, mgr, http.StatusBadRequest},
		{"legacy movie grab", "/api/v1/grab", `{"indexer":"Tracker","download_url":"http://10.0.0.1/x","title":"Alpha","movie_id":1}`, mgr, http.StatusBadRequest},
		{"legacy series grab", "/api/v1/series/1/grab", `{"indexer":"Tracker","download_url":"http://10.0.0.1/x","title":"Show"}`, mgr, http.StatusBadRequest},
		{"legacy book grab", "/api/v1/books/1/grab", `{"indexer":"Tracker","download_url":"http://10.0.0.1/x","title":"Thrawn"}`, mgr, http.StatusBadRequest},
		{"legacy blocklist", "/api/v1/movies/1/blocklist", `{"title":"Alpha","download_url":"http://10.0.0.1/x"}`, mgr, http.StatusBadRequest},
	}
	for _, tc := range refused {
		if rec := s.doJSON("POST", tc.path, tc.c, tc.body); rec.Code != tc.want {
			t.Errorf("%s: HTTP %d, want %d: %s", tc.name, rec.Code, tc.want, rec.Body)
		}
	}
	if f := ft.fetches(); len(f) != 0 {
		t.Fatalf("a refused grab fetched %v", f)
	}
	if paths, _ := q.snapshot(); len(paths) != 0 {
		t.Fatalf("a refused grab reached the client: %v", paths)
	}

	for _, tc := range []struct{ path, body, wantFetch string }{
		{"/api/v1/grab", `{"token":"` + movieTok + `","movie_id":1}`, "/dl/1?apikey=" + trackerKey},
		{"/api/v1/series/1/grab", `{"token":"` + seriesTok + `"}`, "/dl/2?apikey=" + trackerKey},
		{"/api/v1/books/1/grab", `{"token":"` + bookTok + `"}`, "/dl/3?apikey=" + trackerKey},
	} {
		before := len(ft.fetches())
		rec := s.doJSON("POST", tc.path, mgr, tc.body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", tc.path, rec.Code, rec.Body)
		}
		noSecrets(t, tc.path, rec.Body.String())
		f := ft.fetches()
		if len(f) != before+1 || !strings.HasPrefix(f[len(f)-1], tc.wantFetch) {
			t.Fatalf("%s: fetched %v, want the stored link %s", tc.path, f, tc.wantFetch)
		}
	}
	if paths, _ := q.snapshot(); !strings.Contains(strings.Join(paths, " "), "torrents/add") {
		t.Fatalf("the client never got the torrent: %v", paths)
	}

	// The series grab took the search's scope from its token.
	var scope string
	if err := s.st.DB().QueryRow(`SELECT scope FROM grabs WHERE media_type = 'series'`).Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != "S01" {
		t.Errorf("series grab scope = %q, want S01 (the season search it came from)", scope)
	}

	// Blocklisting by token records the release under its own title.
	if rec := s.doJSON("POST", "/api/v1/movies/1/blocklist", mgr, `{"token":"`+movieTok+`"}`); rec.Code != http.StatusOK {
		t.Fatalf("blocklist: HTTP %d: %s", rec.Code, rec.Body)
	}
	rec := s.do("GET", "/api/v1/movies/1/blocklist", mgr)
	if !strings.Contains(rec.Body.String(), "Alpha.2001.1080p.BluRay.x264-GRP") {
		t.Errorf("blocklist = %s", rec.Body)
	}
	noSecrets(t, "blocklist", rec.Body.String())
	// A title-only block still works.
	if rec := s.doJSON("POST", "/api/v1/movies/1/blocklist", mgr, `{"title":"Alpha.2001.720p.WEB-GRP"}`); rec.Code != http.StatusOK {
		t.Fatalf("title-only blocklist: HTTP %d: %s", rec.Code, rec.Body)
	}
}
