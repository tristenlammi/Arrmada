package automation

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/metadata"
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
