package automation

import (
	"context"
	"errors"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// blockHarness is the stall harness with books on and removals recorded.
func blockHarness(t *testing.T) (*stallHarness, *[]removeCall) {
	t.Helper()
	h := newStallHarness(t)
	h.c.books = books.NewService(h.c.db, nil, h.c.log)
	removed := &[]removeCall{}
	h.c.removeTorrent = func(_ context.Context, hash string, deleteData bool) error {
		*removed = append(*removed, removeCall{hash, deleteData})
		return nil
	}
	return h, removed
}

func blockRows(t *testing.T, h *stallHarness) map[string]int64 {
	t.Helper()
	rows, err := h.c.db.Query(`SELECT media_type, movie_id FROM blocklist`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var kind string
		var id int64
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatal(err)
		}
		out[kind] = id
	}
	return out
}

// Blocking an audiobook used to delete it and blocklist nothing, so the next sweep grabbed
// the same release again. It now blocks it for the book its grab was for, says so, and
// searches that book again.
func TestBlockReleaseBookGrab(t *testing.T) {
	h, removed := blockHarness(t)
	added, _ := h.c.books.AddWorks(h.ctx, []metadata.BookResult{{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"}}, "", true)
	if len(added) != 1 {
		t.Fatal("seed book")
	}
	book := added[0]
	release := "Frank Herbert - Dune (Audiobook) [M4B]"
	hash := hashFor(release)
	gid := addGrab(t, h.c, "book", book.ID, release, hash)

	before := h.ix.searchCount()
	got, err := h.c.BlockRelease(h.ctx, hash, release)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "book" || got.ID != book.ID || got.Title != "Dune" {
		t.Errorf("blocked for %+v, want the book Dune", got)
	}
	if rows := blockRows(t, h); rows["book"] != book.ID || len(rows) != 1 {
		t.Errorf("blocklist = %v, want one book row for %d", rows, book.ID)
	}
	set, err := h.c.blockedSetBook(h.ctx, book.ID)
	if err != nil || !set[normTitle(release)] {
		t.Errorf("blockedSetBook doesn't hold the release (%v)", err)
	}
	if len(*removed) != 1 || (*removed)[0] != (removeCall{hash, true}) {
		t.Errorf("removals = %+v, want the torrent with its data", *removed)
	}
	if s := grabStatus(t, h.c, gid); s != grabStatusFailed {
		t.Errorf("grab = %q, want failed", s)
	}
	if h.ix.searchCount() == before {
		t.Error("no search for another release")
	}
	evs, _ := h.c.books.Events(h.ctx, book.ID, 10)
	found := false
	for _, e := range evs {
		found = found || e.Event == "blocklisted"
	}
	if !found {
		t.Errorf("no 'blocklisted' event on the book: %+v", evs)
	}
}

// A year-less TV torrent with no grab, in the TV category, is blocked for the show —
// never for the movie of the same name, which it used to blocklist and re-search.
func TestBlockReleaseTVNeverMatchesAMovie(t *testing.T) {
	h, _ := blockHarness(t)
	movieID := h.addMovie(t, 1, "Fargo", 1996)
	meta := &importMeta{d: metadata.SeriesDetails{
		SeriesResult: metadata.SeriesResult{TMDBID: 60622, Title: "Fargo"},
		Seasons:      []metadata.SeasonDetails{{SeasonNumber: 5, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1}}}},
	}}
	h.c.series = series.NewService(h.c.db, meta, t.TempDir(), h.c.log)
	s, err := h.c.series.Add(h.ctx, 60622, "", true)
	if err != nil {
		t.Fatal(err)
	}
	release := "Fargo.S05E01.1080p.WEB.h264-GRP"
	hash := hashFor(release)
	h.qbit.stalledTorrent(hash, release, seriesCategory)

	got, err := h.c.ResolveBlock(h.ctx, hash, release)
	if err != nil {
		t.Fatal(err)
	}
	if got.Kind != "series" || got.ID != s.ID {
		t.Fatalf("resolved %+v, want the show", got)
	}
	if err := h.c.BlockResolved(h.ctx, hash, got); err != nil {
		t.Fatal(err)
	}
	rows := blockRows(t, h)
	if rows["series"] != s.ID {
		t.Errorf("no series row: %v", rows)
	}
	if _, ok := rows["movie"]; ok {
		t.Errorf("the movie %d was blocklisted for a TV release", movieID)
	}
}

// A torrent tied to nothing is left alone: no removal, no blocklist row.
func TestBlockReleaseNothingToBlock(t *testing.T) {
	h, removed := blockHarness(t)
	release := "Some.Unknown.Thing.2019.1080p.BluRay-GRP"
	hash := hashFor(release)
	h.qbit.stalledTorrent(hash, release, "")

	if _, err := h.c.BlockRelease(h.ctx, hash, release); !errors.Is(err, ErrNothingToBlock) {
		t.Fatalf("err = %v, want ErrNothingToBlock", err)
	}
	if len(*removed) != 0 {
		t.Errorf("removed an unlinked torrent: %+v", *removed)
	}
	if rows := blockRows(t, h); len(rows) != 0 {
		t.Errorf("blocklisted an unlinked release: %v", rows)
	}
}
