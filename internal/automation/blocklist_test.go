package automation

import (
	"errors"
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
	c.addBlockMusic(ctx, book.ID, "Radiohead - Discography (1993-2016) [FLAC]", "", "manually blocklisted")

	all, total, err := c.ListAllBlocks(ctx, BlockFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 4 || len(all) != 4 {
		t.Fatalf("all: %d rows (total %d), want 4", len(all), total)
	}
	if all[0].Type != "music" || !all[0].Discography || all[0].ItemTitle != "Radiohead" {
		t.Errorf("newest first, discography named by its artist: %+v", all[0])
	}

	books, _, _ := c.ListAllBlocks(ctx, BlockFilter{Type: "book"})
	if len(books) != 1 || books[0].ItemID != book.ID || books[0].ItemTitle != "Dune" || books[0].Indexer != "MAM" {
		t.Errorf("book filter: %+v", books)
	}
	globals, _, _ := c.ListAllBlocks(ctx, BlockFilter{Type: "global"})
	if len(globals) != 1 || globals[0].ItemID != 0 || globals[0].ItemTitle != "" || globals[0].Reason != "executables" {
		t.Errorf("global filter: %+v", globals)
	}
	movies, _, _ := c.ListAllBlocks(ctx, BlockFilter{Type: "movie"})
	if len(movies) != 1 || movies[0].ItemTitle != "Not Dune" {
		t.Errorf("movie filter: %+v", movies)
	}

	// q matches the release title, or the title of what it's blocked for, literally.
	if got, _, _ := c.ListAllBlocks(ctx, BlockFilter{Q: "legit"}); len(got) != 1 || got[0].Type != "global" {
		t.Errorf("q=legit: %+v", got)
	}
	if got, _, _ := c.ListAllBlocks(ctx, BlockFilter{Q: "dune"}); len(got) != 2 {
		t.Errorf("q=dune should match the book (release) and the movie (title): %+v", got)
	}
	if got, _, _ := c.ListAllBlocks(ctx, BlockFilter{Q: "%"}); len(got) != 0 {
		t.Errorf("q=%% matched %d rows; it should be literal", len(got))
	}
	if got, total, _ := c.ListAllBlocks(ctx, BlockFilter{Limit: 1, Offset: 1}); len(got) != 1 || total != 4 || got[0].Type != "movie" {
		t.Errorf("page 2 of 1: %+v (total %d)", got, total)
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
