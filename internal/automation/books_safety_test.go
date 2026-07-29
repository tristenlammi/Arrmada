package automation

import (
	"context"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

func bookTestCoord(t *testing.T) (*Coordinator, *books.Service, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	svc := books.NewService(st.DB(), nil, slog.Default())
	return &Coordinator{books: svc, log: slog.Default()}, svc, context.Background()
}

// The grab path had no check that a release is for the book it was searched for. Book
// indexers fuzzy-match, so "Frank Herbert Dune" returns Dune Messiah — which could out-score
// Dune, be grabbed for it, and satisfy that edition forever.
func TestReleasesForThisBookRejectsSequel(t *testing.T) {
	c, svc, ctx := bookTestCoord(t)
	// AddWorks creates rows straight from metadata (no network fetch), which is what the
	// author-catalogue bulk add uses.
	added, _ := svc.AddWorks(ctx, []metadata.BookResult{
		{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
		{Key: "OL2W", Title: "Dune Messiah", Author: "Frank Herbert"},
	}, "", true)
	if len(added) != 2 {
		t.Fatalf("expected 2 books added, got %d", len(added))
	}
	dune, messiah := added[0], added[1]

	releases := []indexer.Release{
		{Title: "Frank Herbert - Dune [EPUB]"},
		{Title: "Frank Herbert - Dune Messiah [EPUB]"},
		{Title: "Brandon Sanderson - Mistborn [EPUB]"}, // not in the library at all
	}

	kept := c.releasesForThisBook(ctx, dune, releases)
	if len(kept) != 1 || kept[0].Title != "Frank Herbert - Dune [EPUB]" {
		t.Errorf("Dune should keep only its own release, got %+v", titles(kept))
	}

	kept = c.releasesForThisBook(ctx, messiah, releases)
	if len(kept) != 1 || kept[0].Title != "Frank Herbert - Dune Messiah [EPUB]" {
		t.Errorf("Dune Messiah should keep only its own release, got %+v", titles(kept))
	}
}

// A landed ebook must not mark a still-downloading AUDIOBOOK grab as imported — that made
// ManageSeeding delete the audiobook torrent with its data before it could import.
func TestBookEditionLandedIsPerEdition(t *testing.T) {
	withEbook := books.Book{Ebook: &books.BookFile{Path: "/x.epub"}, HasFile: true}
	withAudio := books.Book{Audiobook: &books.BookFile{Path: "/x.m4b"}, HasFile: true}

	if bookEditionLanded(withEbook, "Some Book [M4B]") {
		t.Error("a landed ebook must NOT mark an audiobook grab as imported")
	}
	if !bookEditionLanded(withEbook, "Some Book [EPUB]") {
		t.Error("a landed ebook should mark the ebook grab imported")
	}
	if !bookEditionLanded(withAudio, "Some Book [M4B]") {
		t.Error("a landed audiobook should mark the audiobook grab imported")
	}
	if bookEditionLanded(withAudio, "Some Book [EPUB]") {
		t.Error("a landed audiobook must NOT mark an ebook grab as imported")
	}
	// Unknown format falls back to "either edition present" — a grab we can't classify is
	// safer treated as done than blocklisted as stalled.
	if !bookEditionLanded(withEbook, "Some Book with no format tag") {
		t.Error("an unclassifiable grab should fall back to any-edition-present")
	}
	if bookEditionLanded(books.Book{}, "Some Book [EPUB]") {
		t.Error("a book with nothing on disk has landed nothing")
	}
}

func titles(rs []indexer.Release) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Title)
	}
	return out
}
