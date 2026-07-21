package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// --- API key redaction ---

// A transport failure produces a *url.Error carrying the full request URL —
// api_key included — and handlers write metadata error strings back to API
// clients. No error leaving this package may contain the key.
func TestTMDBErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // guarantee a connection-refused transport error

	tm := NewTMDB("SECRET")
	tm.base = srv.URL
	_, err := tm.SearchMovie(context.Background(), "matrix")
	if err == nil {
		t.Fatal("expected a transport error from the closed server")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error string leaks the api key: %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("expected the api key to be replaced with REDACTED: %v", err)
	}
}

// Discover list calls share get(); their errors must be redacted too (and never cached).
func TestTMDBDiscoverErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	tm := NewTMDB("SECRET")
	tm.base = srv.URL
	_, err := tm.Trending(context.Background(), "movie")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error string leaks the api key: %v", err)
	}
}

// OMDb puts its key in the query string the same way.
func TestOMDbErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	o := NewOMDb("SECRET")
	o.base = srv.URL
	_, err := o.Ratings(context.Background(), "tt0133093")
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error string leaks the api key: %v", err)
	}
}

// The query-escaped form of the key must be scrubbed too.
func TestSanitizeErrHandlesEscapedKey(t *testing.T) {
	raw := "https://example.test/3/movie?api_key=" + url.QueryEscape("se cret+key")
	err := errors.New(`Get "` + raw + `": connection refused`)
	got := sanitizeErr(raw, err)
	if strings.Contains(got.Error(), "se+cret%2Bkey") || strings.Contains(got.Error(), "se cret+key") {
		t.Errorf("escaped key leaked: %v", got)
	}
	if !strings.Contains(got.Error(), "REDACTED") {
		t.Errorf("expected REDACTED marker: %v", got)
	}
}

// --- discover list caching ---

// A second call within the TTL must be served from cache (one upstream hit), and
// distinct endpoints/params must not collide.
func TestDiscoverListCaching(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"results":[{"id":1,"title":"A","media_type":"movie","release_date":"2020-01-01","poster_path":"/p.jpg","vote_count":900}]}`))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		items, err := tm.Trending(ctx, "movie")
		if err != nil {
			t.Fatalf("trending: %v", err)
		}
		if len(items) != 1 || items[0].Title != "A" {
			t.Fatalf("unexpected items: %+v", items)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("expected 1 upstream hit for repeated trending, got %d", got)
	}

	if _, err := tm.Popular(ctx, "movie"); err != nil { // different endpoint -> cache miss
		t.Fatalf("popular: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected a second upstream hit for a different endpoint, got %d", got)
	}
}

// Errors must never be cached: after a failure, the next call retries upstream.
func TestDiscoverListDoesNotCacheErrors(t *testing.T) {
	var hits atomic.Int32
	fail := atomic.Bool{}
	fail.Store(true)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if fail.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"results":[{"id":1,"title":"A","media_type":"movie","release_date":"2020-01-01","poster_path":"/p.jpg","vote_count":900}]}`))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	if _, err := tm.Upcoming(ctx, "movie"); err == nil {
		t.Fatal("expected an error from the 500 response")
	}
	fail.Store(false)
	items, err := tm.Upcoming(ctx, "movie")
	if err != nil {
		t.Fatalf("expected recovery after upstream heals, got %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("unexpected items: %+v", items)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected 2 upstream hits (error not cached), got %d", got)
	}
}

// Expired entries are refetched.
func TestDiscoverListCacheExpiry(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"results":[]}`))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	if _, err := tm.Trending(ctx, "movie"); err != nil {
		t.Fatalf("trending: %v", err)
	}
	// Force the entry to be expired.
	tm.discMu.Lock()
	for k, e := range tm.discCache {
		e.exp = time.Now().Add(-time.Second)
		tm.discCache[k] = e
	}
	tm.discMu.Unlock()

	if _, err := tm.Trending(ctx, "movie"); err != nil {
		t.Fatalf("trending: %v", err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("expected refetch after expiry, got %d hits", got)
	}
}

// --- Upcoming: movie vs series ---

// Upcoming("series") must hit /tv/on_the_air (shows airing in the next 7 days) and
// normalize rows to media_type "series"; movie stays on /movie/upcoming. Crucially the
// two must not share a cache entry — a shared key would serve movie cards on the Series
// tab (or vice versa) for the whole 10-minute TTL.
func TestUpcomingRoutesAndCachesPerMediaType(t *testing.T) {
	var paths []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()
		switch r.URL.Path {
		case "/movie/upcoming":
			w.Write([]byte(`{"results":[{"id":1,"title":"Movie A","release_date":"2030-01-01","poster_path":"/m.jpg"}]}`))
		case "/tv/on_the_air":
			w.Write([]byte(`{"results":[{"id":2,"name":"Show B","first_air_date":"2030-02-02","poster_path":"/s.jpg"}]}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	for _, media := range []string{"series", "tv"} {
		items, err := tm.Upcoming(ctx, media)
		if err != nil {
			t.Fatalf("upcoming(%q): %v", media, err)
		}
		if len(items) != 1 || items[0].MediaType != "series" || items[0].Title != "Show B" || items[0].TMDBID != 2 {
			t.Fatalf("upcoming(%q) unexpected items: %+v", media, items)
		}
		if items[0].PosterURL == "" {
			t.Errorf("upcoming(%q): poster URL not built", media)
		}
	}

	for _, media := range []string{"movie", "", "bogus"} {
		items, err := tm.Upcoming(ctx, media)
		if err != nil {
			t.Fatalf("upcoming(%q): %v", media, err)
		}
		if len(items) != 1 || items[0].MediaType != "movie" || items[0].Title != "Movie A" {
			t.Fatalf("upcoming(%q) unexpected items: %+v", media, items)
		}
	}

	// Distinct cache keys: exactly one upstream fetch per media type, and both entries
	// coexist in the cache.
	mu.Lock()
	got := append([]string(nil), paths...)
	mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("expected exactly 2 upstream hits (one per media type), got %v", got)
	}
	tm.discMu.Lock()
	n := len(tm.discCache)
	_, haveMovie := tm.discCache["/movie/upcoming?"]
	_, haveSeries := tm.discCache["/tv/on_the_air?"]
	tm.discMu.Unlock()
	if n != 2 || !haveMovie || !haveSeries {
		t.Errorf("expected separate cache entries per media type, got %d entries (movie=%v series=%v)", n, haveMovie, haveSeries)
	}
}

// A slow /trending/weekly.json must fail fast on its own deadline rather than hanging
// the Books row until the 15s client timeout.
func TestTrendingBooksHasOwnDeadline(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the client gives up
	}))
	defer func() { close(release); srv.Close() }()

	// The client timeout stays generous (as in production); only the trending-specific
	// deadline is shrunk so the test doesn't have to wait it out.
	ol := &OpenLibrary{http: &http.Client{Timeout: 15 * time.Second}, base: srv.URL}
	prev := olTrendingTimeout
	olTrendingTimeout = 100 * time.Millisecond
	defer func() { olTrendingTimeout = prev }()

	start := time.Now()
	items, err := ol.TrendingBooks(context.Background())
	if err == nil {
		t.Fatal("expected a timeout error, got nil (an empty row would render as 'no results')")
	}
	if items != nil {
		t.Errorf("expected no items on timeout, got %+v", items)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("trending waited %v — it must not ride the 15s client timeout", elapsed)
	}
}

// Adult rows must never reach a discovery surface, and the browse floor must drop
// the obscure junk TMDB doesn't flag — while leaving explicit search untouched so
// a real-but-obscure title stays findable.
func TestDiscoverAdultAndVoteFloor(t *testing.T) {
	const body = `{"results":[
		{"id":1,"title":"Mainstream","media_type":"movie","release_date":"2020-01-01","poster_path":"/a.jpg","vote_count":900},
		{"id":2,"title":"Flagged Adult","media_type":"movie","release_date":"2020-01-01","poster_path":"/b.jpg","vote_count":900,"adult":true},
		{"id":3,"title":"Obscure Pink Film","media_type":"movie","release_date":"1998-01-01","poster_path":"/c.jpg","vote_count":3},
		{"id":4,"title":"Brazzers Presents","media_type":"movie","release_date":"2020-01-01","poster_path":"/d.jpg","vote_count":900}
	]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	// Browse: adult flag, adult title and the vote floor all apply.
	items, err := tm.Trending(ctx, "movie")
	if err != nil {
		t.Fatalf("trending: %v", err)
	}
	if len(items) != 1 || items[0].Title != "Mainstream" {
		t.Fatalf("browse should keep only the mainstream row, got %+v", items)
	}

	// Search: no vote floor (obscure titles stay findable), but adult still blocked.
	found, err := tm.Search(ctx, "anything")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(found) != 2 {
		t.Fatalf("search should keep mainstream + obscure, got %+v", found)
	}
	for _, it := range found {
		if it.Title == "Flagged Adult" || it.Title == "Brazzers Presents" {
			t.Errorf("adult row leaked into search: %q", it.Title)
		}
	}
}
