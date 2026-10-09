package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// A title's detail record is fetched from TMDB once and then served from the cache, for a
// movie and a show alike ("series" and "tv" are the same entry).
func TestMediaDetailsCached(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		switch {
		case strings.HasPrefix(r.URL.Path, "/movie/603"):
			_, _ = w.Write([]byte(`{"id":603,"title":"The Matrix","release_date":"1999-03-31","imdb_id":"tt0133093","vote_average":8.2}`))
		case strings.HasPrefix(r.URL.Path, "/tv/1399"):
			_, _ = w.Write([]byte(`{"id":1399,"name":"Game of Thrones","first_air_date":"2011-04-17","external_ids":{"imdb_id":"tt0944947"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	tm := NewTMDB("k")
	tm.base = srv.URL
	tm.SetDiskCache(testDiskCache(t))
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		d, err := tm.MediaDetails(ctx, "movie", 603)
		if err != nil || d.Title != "The Matrix" || d.IMDBID != "tt0133093" || d.Ratings.TMDB != 8.2 {
			t.Fatalf("movie detail #%d: %v %+v", i, err, d)
		}
		d.Ratings.IMDB = "changed by a caller" // must not leak into the next answer
	}
	if d, _ := tm.MediaDetails(ctx, "movie", 603); d.Ratings.IMDB != "" {
		t.Errorf("a caller's change reached the cache: %+v", d.Ratings)
	}
	for _, media := range []string{"series", "tv"} {
		if d, err := tm.MediaDetails(ctx, media, 1399); err != nil || d.Title != "Game of Thrones" {
			t.Fatalf("%s detail: %v %+v", media, err, d)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("TMDB hits = %d, want 2 (one per title)", got)
	}
	// An error isn't kept: a missing title asks again.
	for i := 0; i < 2; i++ {
		if _, err := tm.MediaDetails(ctx, "movie", 1); err == nil {
			t.Fatal("a 404 detail reported success")
		}
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Errorf("TMDB hits after two failed lookups = %d, want 4", got)
	}
}

// Ratings are cached, a title OMDb doesn't know is cached as empty (no error), and a quota
// error is returned, never cached, and kept as the last error until a request works.
func TestOMDbRatingsCachedIncludingNotFound(t *testing.T) {
	var hits int32
	var quota atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if quota.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"Response":"False","Error":"Request limit reached!"}`))
			return
		}
		switch r.URL.Query().Get("i") {
		case "tt0133093":
			_, _ = w.Write([]byte(`{"Response":"True","imdbRating":"8.7","Metascore":"73","Ratings":[{"Source":"Rotten Tomatoes","Value":"83%"}]}`))
		default:
			_, _ = w.Write([]byte(`{"Response":"False","Error":"Incorrect IMDb ID."}`))
		}
	}))
	defer srv.Close()
	o := NewOMDb("k")
	o.base = srv.URL
	o.SetDiskCache(testDiskCache(t))
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		rt, err := o.Ratings(ctx, "tt0133093")
		if err != nil || rt.IMDB != "8.7" || rt.RottenTomatoes != "83%" || rt.Metacritic != "73/100" {
			t.Fatalf("ratings #%d: %v %+v", i, err, rt)
		}
		if rt, err := o.Ratings(ctx, "tt9999999"); err != nil || rt != (Ratings{}) {
			t.Fatalf("not found #%d: %v %+v, want empty and no error", i, err, rt)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 2 {
		t.Errorf("OMDb hits = %d, want 2 (one per title)", got)
	}
	if msg, _ := o.LastError(); msg != "" {
		t.Errorf("last error after working requests = %q", msg)
	}

	quota.Store(true)
	for i := 0; i < 2; i++ {
		if _, err := o.Ratings(ctx, "tt0111161"); err == nil || !strings.Contains(err.Error(), "Request limit reached!") {
			t.Fatalf("quota #%d: err = %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 4 {
		t.Errorf("OMDb hits = %d, want 4: a quota error must not be cached", got)
	}
	msg, at := o.LastError()
	if !strings.Contains(msg, "Request limit reached!") || at.IsZero() {
		t.Errorf("LastError = %q at %v", msg, at)
	}
	// Cached titles still answer while the quota is spent.
	if rt, err := o.Ratings(ctx, "tt0133093"); err != nil || rt.IMDB != "8.7" {
		t.Errorf("cached ratings during a spent quota: %v %+v", err, rt)
	}

	// A new key: the old key's error says nothing about it.
	o.key = func() string { return "k2" }
	if msg, _ := o.LastError(); msg != "" {
		t.Errorf("LastError after a key change = %q", msg)
	}
}
