package automation

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Every kind of blocklist row can be listed and filtered, with the title of what it
// blocks the release for joined from that kind's own table.
func TestListAllBlocks(t *testing.T) {
	c, bk, ctx := reviewTestCoord(t)
	added, _ := bk.AddWorks(ctx, []metadata.BookResult{{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}, "", true)
	book := added[0]
	// A movie and an album sharing the book's id: each row must take its own kind's title.
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, monitored) VALUES (?, 1, 'Not Dune', 1)`, book.ID)
	mustExec(t, c, `INSERT INTO artists (id, mbid, name) VALUES (?, 'a1', 'Radiohead')`, book.ID)
	mustExec(t, c, `INSERT INTO albums (id, artist_id, mbid, title) VALUES (?, ?, 'r1', 'OK Computer')`, book.ID, book.ID)

	c.addBlockBook(ctx, book.ID, "Frank Herbert - Dune [EPUB]", "MAM", "stalled")
	c.addBlockGlobal(ctx, "Totally.Legit.2024.1080p.exe", "Bad", "executables")
	_ = c.addBlock(ctx, book.ID, "Not.Dune.2020.1080p", "", "", "manually blocklisted")
	c.addBlockMusicArtist(ctx, book.ID, "Radiohead - Discography (1993-2016) [FLAC]", "", "manually blocklisted")

	pg, err := c.ListAllBlocks(ctx, BlockFilter{})
	if err != nil {
		t.Fatal(err)
	}
	all, total := pg.Items, pg.Total
	if total != 4 || len(all) != 4 {
		t.Fatalf("all: %d rows (total %d), want 4", len(all), total)
	}
	if all[0].Type != "music" || !all[0].Discography || all[0].ItemTitle != "Radiohead" {
		t.Errorf("newest first, discography named by its artist: %+v", all[0])
	}

	books := blockPageRows(t, c, BlockFilter{Type: "book"})
	if len(books) != 1 || books[0].ItemID != book.ID || books[0].ItemTitle != "Dune" || books[0].Indexer != "MAM" {
		t.Errorf("book filter: %+v", books)
	}
	globals := blockPageRows(t, c, BlockFilter{Type: "global"})
	if len(globals) != 1 || globals[0].ItemID != 0 || globals[0].ItemTitle != "" || globals[0].Reason != "executables" {
		t.Errorf("global filter: %+v", globals)
	}
	movies := blockPageRows(t, c, BlockFilter{Type: "movie"})
	if len(movies) != 1 || movies[0].ItemTitle != "Not Dune" {
		t.Errorf("movie filter: %+v", movies)
	}

	// q matches the release title, or the title of what it's blocked for, literally.
	if got := blockPageRows(t, c, BlockFilter{Q: "legit"}); len(got) != 1 || got[0].Type != "global" {
		t.Errorf("q=legit: %+v", got)
	}
	if got := blockPageRows(t, c, BlockFilter{Q: "dune"}); len(got) != 2 {
		t.Errorf("q=dune should match the book (release) and the movie (title): %+v", got)
	}
	if got := blockPageRows(t, c, BlockFilter{Q: "%"}); len(got) != 0 {
		t.Errorf("q=%% matched %d rows; it should be literal", len(got))
	}
	if pg, _ := c.ListAllBlocks(ctx, BlockFilter{Limit: 1, Offset: 1}); len(pg.Items) != 1 || pg.Total != 4 || pg.Items[0].Type != "movie" || pg.NewestID != all[0].ID {
		t.Errorf("page 2 of 1: %+v", pg)
	}

	// Unblocking a global entry takes it out of every title's blocked set.
	before, _ := c.blockedSetOf(ctx, 999, "series")
	if !before[normTitle("Totally.Legit.2024.1080p.exe")] {
		t.Fatal("a global row should block for every title")
	}
	if err := c.RemoveBlockEntry(ctx, globals[0].ID); err != nil {
		t.Fatal(err)
	}
	if after, _ := c.blockedSetOf(ctx, 999, "series"); after[normTitle("Totally.Legit.2024.1080p.exe")] {
		t.Error("unblocked global release is still blocked")
	}
	if err := c.RemoveBlockEntry(ctx, globals[0].ID); !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("second unblock: %v, want ErrBlockNotFound", err)
	}
}

// A film shown under another name (Settings → Library → Display titles) is listed by the
// name it's shown by, and a search finds it by either name.
func TestListAllBlocksDisplayTitle(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, display_title, monitored) VALUES (7, 7, 'The Travelling Players', 'Ο Θίασος', 1)`)
	_ = c.addBlock(ctx, 7, "Thiasos.1975.1080p.BluRay", "", "", "manually blocklisted")

	for _, q := range []string{"ίασος", "travelling"} {
		pg, err := c.ListAllBlocks(ctx, BlockFilter{Q: q})
		got := pg.Items
		if err != nil || len(got) != 1 || got[0].ItemTitle != "Ο Θίασος" {
			t.Errorf("q=%s: %+v %v, want the row named by its shown title", q, got, err)
		}
	}
	if got := blockPageRows(t, c, BlockFilter{Q: "dune"}); len(got) != 0 {
		t.Errorf("q=dune matched %+v", got)
	}
}

// blockPageRows is one page of the Blocklist's rows, failing the test on an error.
func blockPageRows(t *testing.T, c *Coordinator, f BlockFilter) []BlockRow {
	t.Helper()
	pg, err := c.ListAllBlocks(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	return pg.Items
}

// Unblocking several ticked entries removes exactly those; ids that are gone, repeated
// or nonsense are skipped.
func TestRemoveBlockEntries(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	c.addBlockGlobal(ctx, "A.2024.1080p", "", "")
	c.addBlockGlobal(ctx, "B.2024.1080p", "", "")
	c.addBlockGlobal(ctx, "C.2024.1080p", "", "")
	rows := blockPageRows(t, c, BlockFilter{})
	n, err := c.RemoveBlockEntries(ctx, []int64{rows[0].ID, rows[1].ID, rows[1].ID, -4, 0, 99999})
	if err != nil || n != 2 {
		t.Fatalf("removed %d (%v), want 2", n, err)
	}
	if left := blockPageRows(t, c, BlockFilter{}); len(left) != 1 || left[0].ID != rows[2].ID {
		t.Errorf("left %+v, want only the unticked one", left)
	}
	if n, err := c.RemoveBlockEntries(ctx, nil); n != 0 || err != nil {
		t.Errorf("no ids: %d %v", n, err)
	}
}

// "Unblock everything matching" takes every entry the filter matches across all pages, and
// nothing else: other kinds, other searches, and anything blocked after the person looked
// stay blocked.
func TestRemoveMatchingBlocks(t *testing.T) {
	c, _, ctx := reviewTestCoord(t)
	mustExec(t, c, `INSERT INTO movies (id, tmdb_id, title, monitored) VALUES (5, 5, 'Stalled Film', 1)`)
	// More stalled globals than one page holds (the issue's list had 882 rows).
	for i := range maxBlockPage + 50 {
		c.addBlockGlobal(ctx, fmt.Sprintf("Junk.%d.2024.1080p", i), "", "stalled")
	}
	c.addBlockGlobal(ctx, "Keeper.2024.1080p", "", "fake")
	_ = c.addBlock(ctx, 5, "Junk.Movie.2024.1080p", "", "", "stalled")

	pg, err := c.ListAllBlocks(ctx, BlockFilter{Type: "global", Q: "junk"})
	if err != nil || pg.Total != maxBlockPage+50 || len(pg.Items) != maxBlockPage {
		t.Fatalf("list: total %d, %d rows (%v)", pg.Total, len(pg.Items), err)
	}
	// Blocked after the list was read: must survive.
	c.addBlockGlobal(ctx, "Junk.Late.2024.1080p", "", "stalled")

	if n, _ := c.RemoveMatchingBlocks(ctx, BlockFilter{Type: "global", Q: "junk"}, 0); n != 0 {
		t.Errorf("no through id removed %d, want 0", n)
	}
	n, err := c.RemoveMatchingBlocks(ctx, BlockFilter{Type: "global", Q: "junk", Limit: 1, Offset: 3}, pg.NewestID)
	if err != nil || n != maxBlockPage+50 {
		t.Fatalf("removed %d (%v), want %d", n, err, maxBlockPage+50)
	}
	left := map[string]bool{}
	for _, r := range blockPageRows(t, c, BlockFilter{}) {
		left[r.Title] = true
	}
	if len(left) != 3 || !left["Keeper.2024.1080p"] || !left["Junk.Movie.2024.1080p"] || !left["Junk.Late.2024.1080p"] {
		t.Errorf("left %v, want the other search, the other kind and the late one", left)
	}

	// A search that matches the title it's blocked for, not the release, counts too.
	pg, _ = c.ListAllBlocks(ctx, BlockFilter{Q: "stalled film"})
	if n, err := c.RemoveMatchingBlocks(ctx, BlockFilter{Q: "stalled film"}, pg.NewestID); err != nil || n != 1 {
		t.Errorf("by item title: removed %d (%v), want 1", n, err)
	}
}
