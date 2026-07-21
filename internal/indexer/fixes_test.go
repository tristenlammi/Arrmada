package indexer

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// --- apikey redaction ---

// A transport failure produces a url.Error carrying the full request URL —
// apikey included — and that string ends up in the UI's per-indexer Errors map
// and the warn log. No error leaving this package may contain the key.
func TestSearchErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // guarantee a connection-refused transport error

	s := NewTorznabSearcher()
	_, err := s.Search(context.Background(),
		Indexer{Name: "leaky", URL: srv.URL, APIKey: "SECRET"}, SearchQuery{Text: "x"})
	if err == nil {
		t.Fatal("expected a transport error from the closed server")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error string leaks the apikey: %v", err)
	}
	if !strings.Contains(err.Error(), "REDACTED") {
		t.Errorf("expected the apikey to be replaced with REDACTED: %v", err)
	}
}

// Test() shares get(); its errors must be redacted too.
func TestTestErrorRedactsAPIKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()

	s := NewTorznabSearcher()
	err := s.Test(context.Background(), Indexer{Name: "leaky", URL: srv.URL, APIKey: "SECRET"})
	if err == nil {
		t.Fatal("expected a transport error")
	}
	if strings.Contains(err.Error(), "SECRET") {
		t.Errorf("error string leaks the apikey: %v", err)
	}
}

// --- pagination partial results ---

// An error on page N>0 must return the releases already fetched, not discard them.
func TestSearchKeepsEarlierPagesOnLatePageError(t *testing.T) {
	const pageSize = 35
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset > 0 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, page(0, pageSize, 500))
	}))
	defer srv.Close()

	s := NewTorznabSearcher()
	got, err := s.Search(context.Background(), Indexer{Name: "test", URL: srv.URL}, SearchQuery{Text: "x", Limit: 400})
	if err != nil {
		t.Fatalf("a late-page failure must not fail the search: %v", err)
	}
	if len(got) != pageSize {
		t.Errorf("got %d releases, want the %d from the successful first page", len(got), pageSize)
	}
}

// A first-page failure is still a failed search.
func TestSearchFirstPageErrorStillFails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	s := NewTorznabSearcher()
	if _, err := s.Search(context.Background(), Indexer{Name: "test", URL: srv.URL}, SearchQuery{Text: "x"}); err == nil {
		t.Fatal("a first-page failure must return an error")
	}
}

// --- ctx-aware tracker throttles ---

// A cancelled context must abort the rate-limit wait promptly instead of
// sleeping out the full delay (previously while holding the mutex).
func TestTrackerThrottlesHonorCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	tl := NewTorrentLeechSearcher(nil)
	tl.lastReq = time.Now()
	x := NewX1337Searcher(nil)
	x.lastReq = time.Now()
	m := NewMAMSearcher(nil)
	m.lastReq = time.Now()

	for name, fn := range map[string]func(context.Context) error{
		"torrentleech": tl.throttle,
		"1337x":        x.throttle,
		"myanonamouse": m.throttle,
	} {
		start := time.Now()
		if err := fn(ctx); err == nil {
			t.Errorf("%s: throttle ignored the cancelled context", name)
		}
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Errorf("%s: throttle blocked %v despite a cancelled context", name, elapsed)
		}
	}
}

// --- x1337 truncated body ---

// A body cut off mid-transfer must be an error, not a "valid" page with fewer results.
func TestX1337TruncatedBodyIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "4096")
		w.Write([]byte("<html>partial"))
	}))
	defer srv.Close()

	s := NewX1337Searcher(nil)
	if _, err := s.getHTML(context.Background(), srv.URL); err == nil {
		t.Fatal("a truncated body must surface a read error")
	}
}

// --- cross-indexer infohash dedup ---

func TestDedupeByInfoHash(t *testing.T) {
	priority := map[string]int{"A": 1, "B": 5}
	in := []Release{
		{Title: "dupe-b", InfoHash: "aaa", Seeders: 5, Indexer: "B"},
		{Title: "dupe-a", InfoHash: "AAA", Seeders: 9, Indexer: "A"}, // same hash (case-insensitive), more seeders → wins
		{Title: "nohash-1", Seeders: 1, Indexer: "A"},
		{Title: "nohash-2", Seeders: 1, Indexer: "B"}, // empty hashes never dedupe
		{Title: "tie-b", InfoHash: "bbb", Seeders: 3, Indexer: "B"},
		{Title: "tie-a", InfoHash: "bbb", Seeders: 3, Indexer: "A"}, // tie → higher priority (A) wins
	}
	out := dedupeByInfoHash(in, priority)
	if len(out) != 4 {
		t.Fatalf("got %d releases, want 4: %+v", len(out), out)
	}
	titles := map[string]bool{}
	for _, r := range out {
		titles[r.Title] = true
	}
	for _, want := range []string{"dupe-a", "nohash-1", "nohash-2", "tie-a"} {
		if !titles[want] {
			t.Errorf("missing %q in %v", want, titles)
		}
	}
	if titles["dupe-b"] || titles["tie-b"] {
		t.Errorf("worse duplicate survived: %v", titles)
	}
}

// --- server-side fetch of torznab grab URLs ---

func TestFetchTorrentPayloadBencodedBody(t *testing.T) {
	torrent := []byte("d8:announce7:htt/ann4:infod4:name1:xee")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(torrent)
	}))
	defer srv.Close()

	res, err := fetchTorrentPayload(context.Background(), srv.URL+"/dl/release.torrent?apikey=k")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if !bytes.Equal(res.File, torrent) {
		t.Errorf("file bytes = %q, want the raw torrent", res.File)
	}
	if res.URL != "" {
		t.Errorf("URL = %q, want empty when file bytes are returned", res.URL)
	}
	if res.Filename != "release.torrent" {
		t.Errorf("filename = %q, want release.torrent", res.Filename)
	}
}

func TestFetchTorrentPayloadMagnetRedirect(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, magnet, http.StatusFound)
	}))
	defer srv.Close()

	res, err := fetchTorrentPayload(context.Background(), srv.URL+"/grab")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.URL != magnet {
		t.Errorf("URL = %q, want the magnet from the redirect Location", res.URL)
	}
	if len(res.File) != 0 {
		t.Errorf("unexpected file bytes alongside a magnet")
	}
}

func TestFetchTorrentPayloadRejectsNonTorrentBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>an error page</html>"))
	}))
	defer srv.Close()

	if _, err := fetchTorrentPayload(context.Background(), srv.URL); err == nil {
		t.Fatal("an HTML body must be an error so Fetch falls back to URL passthrough")
	}
}

func TestFetchTorrentPayloadMagnetPassthrough(t *testing.T) {
	const magnet = "magnet:?xt=urn:btih:cafebabe"
	res, err := fetchTorrentPayload(context.Background(), magnet)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if res.URL != magnet {
		t.Errorf("URL = %q, want the magnet returned untouched", res.URL)
	}
}

// --- recent cache errors map isolation ---

func TestRecentCacheHandsOutIndependentErrorsMap(t *testing.T) {
	c := &recentCache{
		at: time.Now(), limit: 10,
		res: SearchResult{Errors: map[string]string{"idx": "boom"}},
	}
	got, ok := c.fresh(10)
	if !ok {
		t.Fatal("expected a cache hit")
	}
	got.Errors["idx"] = "mutated"
	got.Errors["new"] = "x"
	if c.res.Errors["idx"] != "boom" || len(c.res.Errors) != 1 {
		t.Errorf("caller mutated the cached errors map: %v", c.res.Errors)
	}
}
