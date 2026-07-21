package metadata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
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
		w.Write([]byte(`{"results":[{"id":1,"title":"A","media_type":"movie","release_date":"2020-01-01","poster_path":"/p.jpg"}]}`))
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
		w.Write([]byte(`{"results":[{"id":1,"title":"A","media_type":"movie","release_date":"2020-01-01","poster_path":"/p.jpg"}]}`))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	ctx := context.Background()

	if _, err := tm.Upcoming(ctx); err == nil {
		t.Fatal("expected an error from the 500 response")
	}
	fail.Store(false)
	items, err := tm.Upcoming(ctx)
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
