package automation

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/store"
)

// reviewTestCoord is a store-backed coordinator with movies, books and music wired, so
// a review of one kind can be checked against items of the others sharing the same ids.
func reviewTestCoord(t *testing.T) (*Coordinator, *books.Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.Default()
	bk := books.NewService(st.DB(), nil, log)
	c := &Coordinator{
		db:     st.DB(),
		log:    log,
		movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		books:  bk,
		music:  music.NewService(st.DB(), nil, log),
	}
	return c, bk, context.Background()
}

func seedReview(t *testing.T, c *Coordinator, mediaType string, expectedID int64, contentPath string) int64 {
	t.Helper()
	res, err := c.db.Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, expected_title, reason)
		VALUES ('abc', 'Some.Release', ?, ?, ?, 'Expected', 'held')`, contentPath, mediaType, expectedID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func mustExec(t *testing.T, c *Coordinator, q string, args ...any) {
	t.Helper()
	if _, err := c.db.Exec(q, args...); err != nil {
		t.Fatalf("seed %q: %v", q, err)
	}
}

// A movie id sent for a book review used to be looked up as a BOOK id, filing the
// download under whichever book shared that number. It must be refused, untouched.
func TestImportReviewRejectsTargetOfAnotherKind(t *testing.T) {
	c, bk, ctx := reviewTestCoord(t)
	added, _ := bk.AddWorks(ctx, []metadata.BookResult{{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}, "", true)
	if len(added) != 1 {
		t.Fatalf("seed book: got %d", len(added))
	}
	mustExec(t, c, `INSERT INTO movies (tmdb_id, title, monitored) VALUES (1, 'Dune', 1)`)
	var before int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM book_events`).Scan(&before)

	id := seedReview(t, c, "book", added[0].ID, t.TempDir())
	err := c.ImportReview(ctx, id, added[0].ID, "movie")
	if !errors.Is(err, ErrWrongTargetKind) {
		t.Fatalf("want ErrWrongTargetKind, got %v", err)
	}
	var after int
	_ = c.db.QueryRow(`SELECT COUNT(*) FROM book_events`).Scan(&after)
	if after != before {
		t.Errorf("a refused import wrote %d book event(s)", after-before)
	}
	var status string
	_ = c.db.QueryRow(`SELECT status FROM import_reviews WHERE id = ?`, id).Scan(&status)
	if status != "pending" {
		t.Errorf("a refused import resolved the review (status %q)", status)
	}
}

// An unmatched review carries id 0. "Import anyway" with no target used to look up
// album 0 and answer 500; it must say a target is needed instead.
func TestImportReviewWithNoItemNeedsTarget(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	for _, kind := range []string{"music", "book"} {
		id := seedReview(t, c, kind, 0, t.TempDir())
		err := c.ImportReview(ctx, id, 0, "")
		if !errors.Is(err, ErrNeedsTarget) {
			t.Errorf("%s review with no item: want ErrNeedsTarget, got %v", kind, err)
		}
	}
}

// The music picker lists albums, labelled with their artist, and never anything else;
// q matches the album title or the artist name.
func TestReviewTargetsForMusicListsAlbums(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	mustExec(t, c, `INSERT INTO artists (id, mbid, name) VALUES (1, 'a1', 'Radiohead'), (2, 'a2', 'Portishead')`)
	mustExec(t, c, `INSERT INTO albums (id, artist_id, mbid, title, year) VALUES
		(1, 1, 'r1', 'OK Computer', 1997), (2, 1, 'r2', 'Kid A', 2000), (3, 2, 'r3', 'Dummy', 1994)`)
	// A movie with the same id as an album must never show up in a music picker.
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, monitored) VALUES (1, 1, 'OK Computer: The Movie', 1)`)

	id := seedReview(t, c, "music", 0, t.TempDir())
	all, err := c.ReviewTargets(ctx, id, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 albums, got %+v", all)
	}
	for _, tg := range all {
		if tg.Kind != "music" {
			t.Errorf("non-album target offered: %+v", tg)
		}
		if tg.Title == "OK Computer" && tg.Subtitle != "Radiohead" {
			t.Errorf("album subtitle = %q, want the artist", tg.Subtitle)
		}
	}

	byArtist, _ := c.ReviewTargets(ctx, id, "radio", 0)
	if len(byArtist) != 2 {
		t.Errorf("q=radio should match both Radiohead albums, got %+v", byArtist)
	}
	byTitle, _ := c.ReviewTargets(ctx, id, "dumm", 0)
	if len(byTitle) != 1 || byTitle[0].ID != 3 {
		t.Errorf("q=dumm should match only Dummy, got %+v", byTitle)
	}
	// LIKE wildcards in the query are literal, not "match anything".
	if pct, _ := c.ReviewTargets(ctx, id, "%", 0); len(pct) != 0 {
		t.Errorf("q=%% matched %d albums; it should be literal", len(pct))
	}
}

// Book reviews list books with the author as the subtitle, filterable by either.
func TestReviewTargetsForBooksListsBooks(t *testing.T) {
	c, bk, ctx := reviewTestCoord(t)
	_, _ = bk.AddWorks(ctx, []metadata.BookResult{
		{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
		{Key: "OL2W", Title: "Mistborn", Author: "Brandon Sanderson"},
	}, "", true)
	mustExec(t, c, `INSERT INTO movies (tmdb_id, title, monitored) VALUES (1, 'Dune', 1)`)

	id := seedReview(t, c, "book", 0, t.TempDir())
	got, err := c.ReviewTargets(ctx, id, "sanderson", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Mistborn" || got[0].Subtitle != "Brandon Sanderson" || got[0].Kind != "book" {
		t.Errorf("q=sanderson → %+v", got)
	}
	all, _ := c.ReviewTargets(ctx, id, "", 0)
	if len(all) != 2 {
		t.Errorf("want 2 books and no movies, got %+v", all)
	}
}

// With Music off there is nothing to pick from, and the picker says what to turn on.
func TestReviewTargetsModuleOff(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	c.music = nil
	id := seedReview(t, c, "music", 0, t.TempDir())
	_, err := c.ReviewTargets(ctx, id, "", 0)
	if !errors.Is(err, ErrModuleOff) || err.Error() != "Turn on Music to import this" {
		t.Errorf("want ErrModuleOff naming Music, got %v", err)
	}
}
