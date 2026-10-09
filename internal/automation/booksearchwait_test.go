package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// emptyBookCoord is a store-backed coordinator whose only indexer answers every search
// with nothing, recording what it was asked. The clock starts at `now`.
func emptyBookCoord(t *testing.T, now time.Time) (*Coordinator, *books.Service, func() []string, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var mu sync.Mutex
	var asked []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query().Get("q"); q != "" {
			mu.Lock()
			asked = append(asked, q)
			mu.Unlock()
		}
		w.Header().Set("Content-Type", "application/rss+xml")
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel></channel></rss>`)
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := indexer.NewService(st.DB(), log, nil)
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
		now:       func() time.Time { return now },
	}
	got := func() []string {
		mu.Lock()
		defer mu.Unlock()
		out := append([]string(nil), asked...)
		asked = nil
		return out
	}
	return c, bk, got, context.Background()
}

// addBooks adds authorless monitored books named title001…, so each search is one query
// naming exactly that book.
func addBooks(t *testing.T, bk *books.Service, ctx context.Context, n int) []books.Book {
	t.Helper()
	var works []metadata.BookResult
	for i := 1; i <= n; i++ {
		works = append(works, metadata.BookResult{Key: fmt.Sprintf("OL%dW", i), Title: fmt.Sprintf("Zqx%03d", i)})
	}
	added, _ := bk.AddWorks(ctx, works, "", true)
	if len(added) != n {
		t.Fatalf("seeded %d books, want %d", len(added), n)
	}
	return added
}

func setSearchState(t *testing.T, c *Coordinator, id int64, last time.Time, misses int) {
	t.Helper()
	if _, err := c.db.Exec(`UPDATE books SET last_search_at = ?, search_misses = ? WHERE id = ?`,
		last.UTC().Format("2006-01-02 15:04:05"), misses, id); err != nil {
		t.Fatal(err)
	}
}

// A book that has come up empty five times is searched weekly: not at six days, yes at
// seven. It is never dropped.
func TestBookSweepFollowsLadder(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c, bk, asked, ctx := emptyBookCoord(t, now)
	b := addBooks(t, bk, ctx, 1)[0]

	setSearchState(t, c, b.ID, now.Add(-6*24*time.Hour), 5)
	c.SearchBooksMissing(ctx)
	if q := asked(); len(q) != 0 {
		t.Errorf("searched at six days: %v", q)
	}
	setSearchState(t, c, b.ID, now.Add(-7*24*time.Hour), 5)
	c.SearchBooksMissing(ctx)
	if q := asked(); len(q) != 1 {
		t.Errorf("at seven days: asked %v, want one search", q)
	}
	if _, misses := bk.SearchState(ctx, b.ID); misses != 6 {
		t.Errorf("misses = %d, want 6", misses)
	}

	// Long past the old two-try limit, it is still searched once its month is up.
	setSearchState(t, c, b.ID, now.Add(-31*24*time.Hour), 50)
	c.SearchBooksMissing(ctx)
	if q := asked(); len(q) != 1 {
		t.Errorf("at 50 misses after a month: asked %v, want one search", q)
	}
}

// One sweep searches at most its cap of books: never-searched first, then the longest
// waiting. The rest are left for the next sweep. (A cap of 4 stands in for the real 40:
// every search waits out the indexer's request throttle.)
func TestBookSweepCapOldestFirst(t *testing.T) {
	const capN = 4
	if bookSweepCap != 40 {
		t.Errorf("bookSweepCap = %d; the private-tracker load was sized for 40", bookSweepCap)
	}
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	c, bk, asked, ctx := emptyBookCoord(t, now)
	all := addBooks(t, bk, ctx, capN+1)
	// Two have been searched before; both are due, the first longer ago.
	older, newer := all[0], all[1]
	setSearchState(t, c, older.ID, now.Add(-30*24*time.Hour), 3)
	setSearchState(t, c, newer.ID, now.Add(-8*24*time.Hour), 3)

	c.searchBooksMissing(ctx, capN)
	q := asked()
	if len(q) != capN {
		t.Fatalf("searched %d books, want the cap %d", len(q), capN)
	}
	seen := strings.Join(q, " ")
	if strings.Contains(seen, newer.Title) {
		t.Error("the most recently searched book went ahead of the cap")
	}
	if !strings.Contains(seen, older.Title) {
		t.Error("the longest-waiting book was left out")
	}
	// The next sweep picks up the one left over.
	c.searchBooksMissing(ctx, capN)
	if q := asked(); len(q) != 1 || !strings.Contains(q[0], newer.Title) {
		t.Errorf("second sweep asked %v, want only %s", q, newer.Title)
	}
}

// A book that already has every edition its profile wants is not searched and doesn't
// take a place under the cap.
func TestBookSweepSkipsCompleteBooks(t *testing.T) {
	c, bk, asked, ctx := emptyBookCoord(t, time.Now())
	b := addBooks(t, bk, ctx, 1)[0]
	if err := books.NewRepo(c.db).SetEdition(ctx, b.ID, books.KindEbook, "/library/x.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
	c.SearchBooksMissing(ctx)
	if q := asked(); len(q) != 0 {
		t.Errorf("a complete book was searched: %v", q)
	}
	if _, misses := bk.SearchState(ctx, b.ID); misses != 0 {
		t.Errorf("a complete book collected %d misses", misses)
	}
}
