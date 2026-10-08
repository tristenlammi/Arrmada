package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestSweepOutcome(t *testing.T) {
	outage := &indexer.AllFailedError{Errors: map[string]string{"A": "down"}}
	for _, tc := range []struct {
		name        string
		err         error
		searched    bool
		grabbed     int
		reset, miss bool
	}{
		{"every indexer failed", outage, true, 0, false, false},
		{"no indexer serves it", indexer.ErrNoIndexers, true, 0, false, false},
		{"other error", errors.New("db locked"), true, 0, false, false},
		{"nothing wanted", nil, false, 0, false, false},
		{"searched, nothing grabbed", nil, true, 0, false, true},
		{"grabbed", nil, true, 2, true, false},
	} {
		reset, miss := sweepOutcome(tc.err, tc.searched, tc.grabbed)
		if reset != tc.reset || miss != tc.miss {
			t.Errorf("%s: reset=%v miss=%v, want reset=%v miss=%v", tc.name, reset, miss, tc.reset, tc.miss)
		}
	}
}

// outageBookCoord is a store-backed coordinator whose only indexer always fails, and
// whose download client list is empty (nothing in flight).
func outageBookCoord(t *testing.T) (*Coordinator, *books.Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := indexer.NewService(st.DB(), log, "")
	if _, err := ix.Create(context.Background(), indexer.Indexer{
		Name: "Books", Kind: indexer.KindTorznab, URL: srv.URL, Priority: 10, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	bk := books.NewService(st.DB(), nil, log)
	c := &Coordinator{
		db: st.DB(), log: log, books: bk, indexers: ix,
		downloads: download.NewService(st.DB(), log),
		quality:   quality.NewService(st.DB()),
	}
	return c, bk, context.Background()
}

// A wanted book searched while every indexer is down must not use up its automatic
// tries. Books get two; counting outage searches dropped them from automatic search for
// good.
func TestBookSweepDuringOutageRecordsNoMiss(t *testing.T) {
	c, bk, ctx := outageBookCoord(t)
	added, _ := bk.AddWorks(ctx, []metadata.BookResult{{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}, "", true)
	if len(added) != 1 {
		t.Fatalf("seed book: got %d", len(added))
	}
	id := added[0].ID

	_, err := c.searchBookOnce(ctx, id)
	if !indexer.IsOutage(err) {
		t.Fatalf("searchBookOnce should return the outage, got %v", err)
	}

	// Twice — the old give-up point.
	c.SearchBooksMissing(ctx)
	c.SearchBooksMissing(ctx)
	if _, misses := bk.SearchState(ctx, id); misses != 0 {
		t.Errorf("outage searches counted %d miss(es); want 0", misses)
	}
	if _, giveUp := bookSearchWait(0); giveUp {
		t.Error("a book with no misses must still be searched automatically")
	}
}

func TestOutageTallyStops(t *testing.T) {
	down := &indexer.AllFailedError{Errors: map[string]string{"A": "timeout"}}

	// One query that fails on its own doesn't stall the titles behind it.
	var o outageTally
	if !o.note(down) || o.stop() {
		t.Fatal("a single outage should be noted but not stop the sweep")
	}
	if o.note(nil) || o.stop() {
		t.Fatal("a search that ran should end the streak")
	}
	o.note(down)
	if o.stop() {
		t.Fatal("the streak should have restarted after the search that ran")
	}
	o.note(down)
	if !o.stop() {
		t.Errorf("%d outages in a row should stop the sweep", outageStopAfter)
	}
	if o.titles != 3 {
		t.Errorf("titles = %d, want 3", o.titles)
	}

	// Nothing serves this media type: true of every title, so stop at once.
	var none outageTally
	if !none.note(indexer.ErrNoIndexers) || !none.stop() {
		t.Error("ErrNoIndexers should stop the sweep immediately")
	}
}

// A movie sweep during a total outage stops after a couple of searches instead of
// asking the dead indexers about every wanted title (a revoked key or bad tracker
// login would otherwise fail again for every title, every five minutes), and records
// no miss on anything, so every title is searched normally once the indexers are back.
func TestMovieSweepStopsOnOutage(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var mu sync.Mutex
	searched := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "" {
			mu.Lock()
			searched[strings.Fields(q)[0]] = true
			mu.Unlock()
		}
		http.Error(w, "bad gateway", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := indexer.NewService(st.DB(), log, "")
	if _, err := ix.Create(context.Background(), indexer.Indexer{
		Name: "Movies", Kind: indexer.KindTorznab, URL: srv.URL, Priority: 10, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{
		db: st.DB(), log: log, indexers: ix,
		movies:    movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		downloads: download.NewService(st.DB(), log),
		quality:   quality.NewService(st.DB()),
	}
	ctx := context.Background()
	for i, title := range []string{"Alpha", "Bravo", "Charlie"} {
		if _, err := st.DB().Exec(`INSERT INTO movies (tmdb_id, title, year, monitored, min_availability)
			VALUES (?, ?, 2001, 1, 'announced')`, i+1, title); err != nil {
			t.Fatal(err)
		}
	}

	c.SearchMissing(ctx)

	mu.Lock()
	n := len(searched)
	mu.Unlock()
	if n != outageStopAfter {
		t.Errorf("searched %d titles during the outage (%v); want the sweep to stop after %d", n, searched, outageStopAfter)
	}
	var misses int
	var last string
	_ = st.DB().QueryRow(`SELECT COALESCE(SUM(search_misses), 0), COALESCE(MAX(last_search_at), '') FROM movies`).Scan(&misses, &last)
	if misses != 0 || last != "" {
		t.Errorf("the outage left misses=%d last_search_at=%q; want nothing recorded", misses, last)
	}
}
