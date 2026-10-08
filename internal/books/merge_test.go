package books

import (
	"io"
	"log/slog"
	"testing"
)

// Adding a book that is already there under another catalogue's key is refused with
// the existing row, not added twice.
func TestFindDuplicateAcrossCatalogues(t *testing.T) {
	repo, ctx := historyRepo(t)
	s := &Service{repo: repo, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	existing, _ := repo.Create(ctx, Book{OLKey: "OL1W", Title: "The Hobbit", Author: "J.R.R. Tolkien"})
	if b, ok := s.findDuplicate(ctx, "Hobbit: 75th Anniversary Edition", "Tolkien, J. R. R."); !ok || b.ID != existing.ID {
		t.Errorf("duplicate not found: ok=%v id=%d", ok, b.ID)
	}
	if _, ok := s.findDuplicate(ctx, "The Hobbit", ""); ok {
		t.Error("a title with no author matched a book that has one")
	}
	if _, ok := s.findDuplicate(ctx, "The Silmarillion", "J.R.R. Tolkien"); ok {
		t.Error("a different title matched")
	}
}
