package books

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// BOOK-08: the search state round-trips through List and Get, and the next search time is
// only given for a monitored book still missing something it wants.
func TestSearchStateOnBook(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	repo := NewRepo(db)
	ctx := context.Background()
	b, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	setState(t, db, b.ID, last, 3)

	got, err := repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := repo.List(ctx)
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %v, %v", all, err)
	}
	for _, x := range []Book{got, all[0]} {
		if x.LastSearchAt != "2026-10-01T09:00:00Z" || x.SearchMisses != 3 {
			t.Errorf("search state = %q / %d", x.LastSearchAt, x.SearchMisses)
		}
	}

	got.WantEbook = true
	got.FillNextSearch()
	if want := last.Add(SearchWait(3)).Format(time.RFC3339); got.NextSearchAt != want {
		t.Errorf("next search = %q, want %q", got.NextSearchAt, want)
	}

	// Every wanted edition here: nothing to search for.
	have := got
	have.Ebook = &BookFile{Path: "/library/dune.epub"}
	have.FillNextSearch()
	if have.NextSearchAt != "" {
		t.Errorf("a complete book has next_search_at %q", have.NextSearchAt)
	}
	// Not monitored: the sweep never looks.
	off := got
	off.Monitored = false
	off.FillNextSearch()
	if off.NextSearchAt != "" {
		t.Errorf("an unmonitored book has next_search_at %q", off.NextSearchAt)
	}
	// Never searched: due now, so no date.
	setState(t, db, b.ID, time.Time{}, 0)
	fresh, _ := repo.Get(ctx, b.ID)
	fresh.WantEbook = true
	fresh.FillNextSearch()
	if fresh.LastSearchAt != "" || fresh.NextSearchAt != "" {
		t.Errorf("a never-searched book reads %q / %q", fresh.LastSearchAt, fresh.NextSearchAt)
	}
}

func setState(t *testing.T, db *sql.DB, id int64, last time.Time, misses int) {
	t.Helper()
	var v any
	if !last.IsZero() {
		v = last.Format("2006-01-02 15:04:05")
	}
	if _, err := db.Exec(`UPDATE books SET last_search_at = ?, search_misses = ? WHERE id = ?`, v, misses, id); err != nil {
		t.Fatal(err)
	}
}
